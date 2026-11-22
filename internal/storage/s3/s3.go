// Package s3 implements the Amazon S3 driver.
//
// The driver also serves S3-compatible services such as MinIO, Cloudflare R2
// and Backblaze B2: point tong at them with --s3-endpoint.
package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	awss3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/lucidfrontier45/tong/internal/storage"
)

// mtimeMetadata is the user metadata key that carries the modification time
// of a file. Object storage has no notion of a modification time, so tong
// stores it next to the object.
const mtimeMetadata = "mtime"

// Options configure the S3 driver.
type Options struct {
	// Region overrides the region of the AWS configuration.
	Region string
	// Endpoint is a custom S3-compatible endpoint. When it is set, tong
	// uses path-style addressing, which is what most compatible services
	// expect.
	Endpoint string
}

// Driver implements [storage.Driver] for s3:// locations.
type Driver struct {
	opts Options

	mu     sync.Mutex
	client *awss3.Client
}

// New returns a driver for Amazon S3.
func New(opts Options) *Driver {
	return &Driver{opts: opts}
}

// api returns the S3 client, creating it on first use so that tong does not
// look for credentials when no S3 location is involved.
func (d *Driver) api(ctx context.Context) (*awss3.Client, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.client != nil {
		return d.client, nil
	}

	loadOpts := []func(*config.LoadOptions) error{}
	if d.opts.Region != "" {
		loadOpts = append(loadOpts, config.WithRegion(d.opts.Region))
	}

	cfg, err := config.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("cannot load the AWS configuration: %w", err)
	}

	client := awss3.NewFromConfig(cfg, func(o *awss3.Options) {
		if d.opts.Endpoint != "" {
			o.BaseEndpoint = aws.String(d.opts.Endpoint)
			o.UsePathStyle = true
		}
	})

	d.client = client
	return client, nil
}

// ResolveSource resolves an s3:// source location.
func (d *Driver) ResolveSource(ctx context.Context, loc storage.Location) (storage.Resolved, error) {
	client, err := d.api(ctx)
	if err != nil {
		return storage.Resolved{}, err
	}

	switch {
	case loc.Key == "":
		// The whole bucket.
		return storage.Resolved{
			Endpoint: &endpoint{client: client, bucket: loc.Bucket, fetchModTime: true},
			IsDir:    true,
			Display:  display(loc),
		}, nil
	case loc.Trailing:
		// A key prefix. The prefix has to exist: a prefix that holds
		// nothing would make a sync with --delete empty the destination.
		exists, err := prefixExists(ctx, client, loc)
		if err != nil {
			return storage.Resolved{}, err
		}
		if !exists {
			return storage.Resolved{}, fmt.Errorf("%w: s3://%s/%s", storage.ErrNotFound, loc.Bucket, loc.Key)
		}
		return storage.Resolved{
			Endpoint: &endpoint{client: client, bucket: loc.Bucket, prefix: loc.Key, fetchModTime: true},
			IsDir:    true,
			Display:  display(loc),
		}, nil
	}

	// The location is either an object or a key prefix. Listing with a
	// delimiter tells the two apart.
	out, err := client.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{
		Bucket:    aws.String(loc.Bucket),
		Prefix:    aws.String(loc.Key),
		Delimiter: aws.String("/"),
	})
	if err != nil {
		return storage.Resolved{}, fmt.Errorf("cannot list s3://%s/%s: %w", loc.Bucket, loc.Key, err)
	}

	prefix := loc.Key + "/"
	for _, common := range out.CommonPrefixes {
		if aws.ToString(common.Prefix) != prefix {
			continue
		}
		return storage.Resolved{
			Endpoint: &endpoint{client: client, bucket: loc.Bucket, prefix: prefix, fetchModTime: true},
			Name:     path.Base(loc.Key),
			IsDir:    true,
			Display:  display(loc),
		}, nil
	}

	for _, object := range out.Contents {
		if aws.ToString(object.Key) != loc.Key {
			continue
		}
		parent := ""
		if i := strings.LastIndex(loc.Key, "/"); i >= 0 {
			parent = loc.Key[:i+1]
		}
		return storage.Resolved{
			Endpoint: &endpoint{client: client, bucket: loc.Bucket, prefix: parent, fetchModTime: true},
			FileKey:  path.Base(loc.Key),
			Display:  display(loc),
		}, nil
	}

	return storage.Resolved{}, fmt.Errorf("%w: s3://%s/%s", storage.ErrNotFound, loc.Bucket, loc.Key)
}

// prefixExists reports whether a key prefix holds at least one object.
func prefixExists(ctx context.Context, client *awss3.Client, loc storage.Location) (bool, error) {
	out, err := client.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{
		Bucket:  aws.String(loc.Bucket),
		Prefix:  aws.String(loc.Key),
		MaxKeys: aws.Int32(1),
	})
	if err != nil {
		return false, fmt.Errorf("cannot list s3://%s/%s: %w", loc.Bucket, loc.Key, err)
	}
	return len(out.Contents) > 0 || len(out.CommonPrefixes) > 0, nil
}

// ResolveDest resolves an s3:// destination location.
func (d *Driver) ResolveDest(ctx context.Context, loc storage.Location, singleFile bool) (storage.Resolved, error) {
	client, err := d.api(ctx)
	if err != nil {
		return storage.Resolved{}, err
	}

	prefix := loc.Key
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		if singleFile {
			// The destination holds the key of the file that is copied.
			parent := ""
			if i := strings.LastIndex(prefix, "/"); i >= 0 {
				parent = prefix[:i+1]
			}
			return storage.Resolved{
				Endpoint: &endpoint{client: client, bucket: loc.Bucket, prefix: parent, fetchModTime: true},
				Name:     path.Base(prefix),
				Display:  display(loc),
			}, nil
		}
		prefix += "/"
	}

	return storage.Resolved{
		Endpoint: &endpoint{client: client, bucket: loc.Bucket, prefix: prefix, fetchModTime: true},
		IsDir:    true,
		Display:  display(loc),
	}, nil
}

// endpoint is a rooted view of a bucket.
type endpoint struct {
	client *awss3.Client
	bucket string
	// prefix is the key prefix the endpoint is rooted at.
	prefix string
	// fetchModTime tells whether the listing looks up the modification
	// time of every object it finds.
	fetchModTime bool
}

// UseModTime tells the endpoint whether the modification times of the listed
// objects matter.
func (e *endpoint) UseModTime(enabled bool) {
	e.fetchModTime = enabled
}

// key maps a key of the endpoint to the key of the bucket.
func (e *endpoint) key(key string) string {
	return e.prefix + key
}

// List calls fn for every object below prefix.
func (e *endpoint) List(ctx context.Context, prefix string, fn func(storage.FileInfo) error) error {
	paginator := awss3.NewListObjectsV2Paginator(e.client, &awss3.ListObjectsV2Input{
		Bucket: aws.String(e.bucket),
		Prefix: aws.String(e.key(prefix)),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("cannot list s3://%s/%s: %w", e.bucket, e.prefix, err)
		}
		for _, object := range page.Contents {
			key := strings.TrimPrefix(aws.ToString(object.Key), e.prefix)
			if key == "" {
				continue
			}
			info, err := e.info(ctx, aws.ToString(object.Key), key, object)
			if err != nil {
				return err
			}
			if err := fn(info); err != nil {
				return err
			}
		}
	}
	return nil
}

// ListShallow calls fn for every object directly below prefix.
func (e *endpoint) ListShallow(ctx context.Context, prefix string, fn func(storage.FileInfo) error) error {
	paginator := awss3.NewListObjectsV2Paginator(e.client, &awss3.ListObjectsV2Input{
		Bucket:    aws.String(e.bucket),
		Prefix:    aws.String(e.key(prefix)),
		Delimiter: aws.String("/"),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("cannot list s3://%s/%s: %w", e.bucket, e.prefix, err)
		}
		for _, object := range page.Contents {
			key := strings.TrimPrefix(aws.ToString(object.Key), e.prefix)
			if key == "" {
				continue
			}
			info, err := e.info(ctx, aws.ToString(object.Key), key, object)
			if err != nil {
				return err
			}
			if err := fn(info); err != nil {
				return err
			}
		}
	}
	return nil
}

// info describes an object of a listing.
func (e *endpoint) info(ctx context.Context, objectKey, key string, object awss3types.Object) (storage.FileInfo, error) {
	info := storage.FileInfo{
		Key:      key,
		Size:     aws.ToInt64(object.Size),
		ModTime:  aws.ToTime(object.LastModified),
		Checksum: aws.ToString(object.ETag),
	}

	if !e.fetchModTime {
		return info, nil
	}

	// Object storage has no modification time of its own, so the time tong
	// preserved is read back from the object metadata.
	modTime, err := e.modTime(ctx, objectKey)
	if err != nil {
		if !isNotFound(err) {
			return storage.FileInfo{}, err
		}
		// The object is gone already, so the listing time is good
		// enough. The transfer reports it as a vanished file.
		return info, nil
	}
	if !modTime.IsZero() {
		info.ModTime = modTime
	}
	return info, nil
}

// modTime reads the modification time tong stored with an object.
func (e *endpoint) modTime(ctx context.Context, key string) (time.Time, error) {
	out, err := e.client.HeadObject(ctx, &awss3.HeadObjectInput{
		Bucket: aws.String(e.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return time.Time{}, notFoundError(fmt.Errorf("cannot stat s3://%s/%s", e.bucket, key), err)
		}
		return time.Time{}, fmt.Errorf("cannot stat s3://%s/%s: %w", e.bucket, key, err)
	}

	raw := out.Metadata[mtimeMetadata]
	if raw == "" {
		return time.Time{}, nil
	}
	seconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid %s metadata %q: %w", mtimeMetadata, raw, err)
	}
	return time.Unix(seconds, 0).UTC(), nil
}

// Open returns a reader for an object.
func (e *endpoint) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := e.client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(e.bucket),
		Key:    aws.String(e.key(key)),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, notFoundError(fmt.Errorf("cannot read s3://%s/%s", e.bucket, e.key(key)), err)
		}
		return nil, fmt.Errorf("cannot read s3://%s/%s: %w", e.bucket, e.key(key), err)
	}
	return out.Body, nil
}

// Create writes an object.
func (e *endpoint) Create(ctx context.Context, key string, modTime time.Time, r io.Reader) error {
	body, cleanup, err := seekable(r)
	if err != nil {
		return err
	}
	defer cleanup()

	metadata := map[string]string{}
	if !modTime.IsZero() {
		metadata[mtimeMetadata] = strconv.FormatInt(modTime.Unix(), 10)
	}

	_, err = e.client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket:   aws.String(e.bucket),
		Key:      aws.String(e.key(key)),
		Body:     body,
		Metadata: metadata,
	})
	if err != nil {
		return fmt.Errorf("cannot write s3://%s/%s: %w", e.bucket, e.key(key), err)
	}
	return nil
}

// Delete removes an object.
func (e *endpoint) Delete(ctx context.Context, key string) error {
	_, err := e.client.DeleteObject(ctx, &awss3.DeleteObjectInput{
		Bucket: aws.String(e.bucket),
		Key:    aws.String(e.key(key)),
	})
	if err != nil {
		return fmt.Errorf("cannot delete s3://%s/%s: %w", e.bucket, e.key(key), err)
	}
	return nil
}

// isNotFound reports whether an error means that the object is gone.
func isNotFound(err error) bool {
	if _, ok := errors.AsType[*awss3types.NoSuchKey](err); ok {
		return true
	}
	_, ok := errors.AsType[*awss3types.NotFound](err)
	return ok
}

// notFoundError marks an error as the absence of an object, so that the
// engine can tell a vanished file from a failed transfer.
func notFoundError(message, err error) error {
	return fmt.Errorf("%w: %w", message, errors.Join(storage.ErrNotFound, err))
}

// seekable returns a reader the SDK can compute a checksum of. A body that
// cannot be rewound is spilled to a temporary file, which keeps the memory
// use of a transfer bounded.
func seekable(r io.Reader) (io.Reader, func(), error) {
	if rs, ok := r.(io.ReadSeeker); ok {
		return rs, func() {}, nil
	}

	file, err := os.CreateTemp("", "tong-upload-*")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() {
		name := file.Name()
		_ = file.Close()
		_ = os.Remove(name)
	}

	if _, err := io.Copy(file, r); err != nil {
		cleanup()
		return nil, nil, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, nil, err
	}
	return file, cleanup, nil
}

// display renders a location the way the user wrote it.
func display(loc storage.Location) string {
	return "s3://" + loc.Bucket + "/" + loc.Key
}
