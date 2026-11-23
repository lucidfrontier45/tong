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
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	awss3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/lucidfrontier45/tong/internal/storage"
)

// mtimeMetadata is the user metadata key that carries the modification time
// of a file. Object storage has no notion of a modification time, so tong
// stores it next to the object.
const mtimeMetadata = "mtime"

// defaultPartSize is the size of a multipart part. Sixteen megabytes keeps
// the memory of an upload bounded, because the parts of an object whose size
// is not known up front are buffered, and it lifts the 5 GB limit that a
// single PutObject request has.
//
// An object whose size is known is planned with it: the manager grows the
// part size when the object would not fit into the 10,000 parts that S3
// allows, so the whole 5 TB that a single object may hold works. Without the
// size the object tops out at 160 GiB, which is 10,000 parts of this size.
const defaultPartSize = 16 << 20

// defaultUploadConcurrency is the number of parts that are uploaded at the
// same time.
//
// It is deliberately independent of --parallel. The SDK allocates
// Concurrency+1 part buffers per multipart object, so a part concurrency that
// followed the number of files in flight would multiply the memory twice:
// at 16 MiB buffered parts and 16 files in flight, four parts cost 1.3 GiB
// while a part concurrency of eight would cost 2.3 GiB.
const defaultUploadConcurrency = 4

// Options configure the S3 driver.
type Options struct {
	// Region overrides the region of the AWS configuration.
	Region string
	// Endpoint is a custom S3-compatible endpoint. When it is set, tong
	// uses path-style addressing, which is what most compatible services
	// expect.
	Endpoint string
	// PartSize overrides the size of a multipart part. Zero means
	// [defaultPartSize]. It is not exposed on the command line; the tests
	// use it to exercise multipart uploads with small objects.
	PartSize int64
	// UploadConcurrency is the number of parts that are uploaded at the
	// same time. Zero means [defaultUploadConcurrency]. It is not exposed
	// on the command line, so that the memory of an upload does not grow
	// with the number of files in flight.
	UploadConcurrency int
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
			Endpoint: d.newEndpoint(client, loc.Bucket, ""),
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
			Endpoint: d.newEndpoint(client, loc.Bucket, loc.Key),
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
			Endpoint: d.newEndpoint(client, loc.Bucket, prefix),
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
			Endpoint: d.newEndpoint(client, loc.Bucket, parent),
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
				Endpoint: d.newEndpoint(client, loc.Bucket, parent),
				Name:     path.Base(prefix),
				Display:  display(loc),
			}, nil
		}
		prefix += "/"
	}

	return storage.Resolved{
		Endpoint: d.newEndpoint(client, loc.Bucket, prefix),
		IsDir:    true,
		Display:  display(loc),
	}, nil
}

// newEndpoint returns a rooted view of a bucket.
func (d *Driver) newEndpoint(client *awss3.Client, bucket, prefix string) *endpoint {
	partSize := d.opts.PartSize
	if partSize == 0 {
		partSize = defaultPartSize
	}
	concurrency := d.opts.UploadConcurrency
	if concurrency == 0 {
		concurrency = defaultUploadConcurrency
	}
	return &endpoint{
		client:       client,
		bucket:       bucket,
		prefix:       prefix,
		partSize:     partSize,
		concurrency:  concurrency,
		fetchModTime: true,
	}
}

// endpoint is a rooted view of a bucket.
type endpoint struct {
	client *awss3.Client
	bucket string
	// prefix is the key prefix the endpoint is rooted at.
	prefix string
	// partSize is the size of a multipart part.
	partSize int64
	// concurrency is the number of parts that are uploaded at the same
	// time. Zero means the default of the SDK.
	concurrency int
	// fetchModTime tells whether the listing looks up the modification
	// time of every object it finds.
	fetchModTime bool

	mu sync.Mutex
	// mp is the transfer manager that uploads objects in parts, created on
	// first use.
	mp *transfermanager.Client
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
		Version:  aws.ToString(object.ETag),
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

// Create writes an object of unknown size.
//
// The upload goes through the transfer manager of the SDK: a small object is
// written with a single request, a large one is split into parts that are
// uploaded as many at a time as the run allows. The metadata, which carries
// the preserved modification time, is passed to both.
func (e *endpoint) Create(ctx context.Context, key string, modTime time.Time, r io.Reader) error {
	return e.create(ctx, key, modTime, 0, r)
}

// CreateSized writes an object whose size the caller knows, which lets the
// transfer manager plan the upload: it decides whether the object fits into a
// single request, and it grows the part size when the object would otherwise
// need more parts than S3 accepts.
func (e *endpoint) CreateSized(ctx context.Context, key string, modTime time.Time, size int64, r io.Reader) error {
	return e.create(ctx, key, modTime, size, r)
}

// create writes an object. A size that is not positive is not declared.
func (e *endpoint) create(ctx context.Context, key string, modTime time.Time, size int64, r io.Reader) error {
	metadata := map[string]string{}
	if !modTime.IsZero() {
		metadata[mtimeMetadata] = strconv.FormatInt(modTime.Unix(), 10)
	}

	input := &transfermanager.UploadObjectInput{
		Bucket:   aws.String(e.bucket),
		Key:      aws.String(e.key(key)),
		Body:     r,
		Metadata: metadata,
	}
	if size > 0 {
		input.ContentLength = aws.Int64(size)
	}

	if _, err := e.uploader().UploadObject(ctx, input); err != nil {
		return fmt.Errorf("cannot write s3://%s/%s: %w", e.bucket, e.key(key), err)
	}
	return nil
}

// uploader returns the transfer manager of the endpoint, creating it on
// first use. The manager keeps no state between uploads, so every transfer of
// a run shares it.
func (e *endpoint) uploader() *transfermanager.Client {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.mp == nil {
		e.mp = transfermanager.New(e.client, func(o *transfermanager.Options) {
			o.PartSizeBytes = e.partSize
			// An object that is smaller than a part is written with a
			// single request, which is what a small file should cost.
			o.MultipartUploadThreshold = e.partSize
			o.Concurrency = e.concurrency
			// A failed upload aborts its parts: resumable multipart
			// uploads are not supported yet, and leaving them behind
			// would cost storage for every failed transfer.
		})
	}
	return e.mp
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

// Identity returns the bucket and the key of the object.
func (e *endpoint) Identity(_ context.Context, key string) (string, error) {
	return "s3://" + e.bucket + "/" + e.key(key), nil
}

// DeleteIfUnchanged removes an object when its ETag still matches the one
// that was listed.
//
// The delete is conditional on the ETag observed during listing. S3-compatible
// endpoints must enforce If-Match for this to protect a replacement object.
func (e *endpoint) DeleteIfUnchanged(ctx context.Context, key string, info storage.FileInfo) error {
	if info.Version == "" {
		return fmt.Errorf("%w: s3://%s/%s has no ETag", storage.ErrChanged, e.bucket, e.key(key))
	}
	_, err := e.client.DeleteObject(ctx, &awss3.DeleteObjectInput{
		Bucket:  aws.String(e.bucket),
		Key:     aws.String(e.key(key)),
		IfMatch: aws.String(info.Version),
	})
	if err != nil {
		if isNotFound(err) {
			return fmt.Errorf("%w: s3://%s/%s", storage.ErrChanged, e.bucket, e.key(key))
		}
		return fmt.Errorf("cannot conditionally delete s3://%s/%s: %w", e.bucket, e.key(key), err)
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

// display renders a location the way the user wrote it.
func display(loc storage.Location) string {
	return "s3://" + loc.Bucket + "/" + loc.Key
}
