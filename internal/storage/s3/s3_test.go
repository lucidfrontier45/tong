package s3_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lucidfrontier45/tong/internal/storage"
	"github.com/lucidfrontier45/tong/internal/storage/s3"
)

// TestIntegration runs against a real S3 service. It is skipped unless
// TONG_TEST_S3_ENDPOINT points at one:
//
//	export TONG_TEST_S3_ENDPOINT=http://127.0.0.1:9000
//	export TONG_TEST_S3_BUCKET=tong-test
//	export AWS_ACCESS_KEY_ID=minio
//	export AWS_SECRET_ACCESS_KEY=minio123
func TestIntegration(t *testing.T) {
	endpoint := os.Getenv("TONG_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("set TONG_TEST_S3_ENDPOINT to run the S3 integration tests")
	}

	region := os.Getenv("TONG_TEST_S3_REGION")
	if region == "" {
		region = "us-east-1"
	}
	bucket := os.Getenv("TONG_TEST_S3_BUCKET")
	if bucket == "" {
		bucket = "tong-test"
	}

	ctx := context.Background()
	driver := s3.New(s3.Options{Region: region, Endpoint: endpoint})

	createBucket(t, ctx, region, endpoint, bucket)

	prefix := "tong/" + strings.ReplaceAll(t.Name(), "/", "-") + "/"
	dest, err := driver.ResolveDest(ctx, storage.Location{
		Scheme: storage.SchemeS3,
		Bucket: bucket,
		Key:    prefix,
	}, false)
	if err != nil {
		t.Fatalf("ResolveDest() = %v", err)
	}

	modTime := time.Now().Add(-time.Hour).Truncate(time.Second)
	content := []byte("hello tong")
	if err := dest.Endpoint.Create(ctx, "a.txt", modTime, bytes.NewReader(content)); err != nil {
		t.Fatalf("Create() = %v", err)
	}
	t.Cleanup(func() {
		if err := dest.Endpoint.Delete(ctx, "a.txt"); err != nil {
			t.Logf("cannot clean up: %v", err)
		}
	})

	// The prefix holds an object now, so it is a source.
	source, err := driver.ResolveSource(ctx, storage.Location{
		Scheme:   storage.SchemeS3,
		Bucket:   bucket,
		Key:      prefix,
		Trailing: true,
	})
	if err != nil {
		t.Fatalf("ResolveSource() = %v", err)
	}

	// A prefix that holds nothing is not a source, so that a typo cannot
	// empty a destination with --delete.
	_, err = driver.ResolveSource(ctx, storage.Location{
		Scheme:   storage.SchemeS3,
		Bucket:   bucket,
		Key:      "tong/missing-prefix/",
		Trailing: true,
	})
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("ResolveSource() = %v, want %v", err, storage.ErrNotFound)
	}

	var keys []string
	if err := dest.Endpoint.List(ctx, "", func(info storage.FileInfo) error {
		keys = append(keys, info.Key)
		if info.Key == "a.txt" {
			if info.Size != int64(len(content)) {
				t.Errorf("size = %d, want %d", info.Size, len(content))
			}
			if !info.ModTime.Truncate(time.Second).Equal(modTime) {
				t.Errorf("modification time = %v, want %v", info.ModTime, modTime)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("List() = %v", err)
	}
	if len(keys) != 1 || keys[0] != "a.txt" {
		t.Errorf("the listing is %v, want [a.txt]", keys)
	}

	// Without the modification times the listing falls back to the time
	// the object was written, which saves a request per object.
	if aware, ok := dest.Endpoint.(storage.ModTimeAware); ok {
		aware.UseModTime(false)
		if err := dest.Endpoint.List(ctx, "", func(info storage.FileInfo) error {
			if info.Key == "a.txt" && info.ModTime.Truncate(time.Second).Equal(modTime) {
				t.Errorf("the listing still reports the stored modification time %v", info.ModTime)
			}
			return nil
		}); err != nil {
			t.Fatalf("List() = %v", err)
		}
		aware.UseModTime(true)
	}

	reader, err := source.Endpoint.Open(ctx, "a.txt")
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("cannot read the object: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("cannot close the object: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("the object holds %q, want %q", got, content)
	}

	// An object that is gone is reported as missing, so that the engine
	// can tell it from a failed transfer.
	if err := dest.Endpoint.Delete(ctx, "a.txt"); err != nil {
		t.Fatalf("Delete() = %v", err)
	}
	if _, err := source.Endpoint.Open(ctx, "a.txt"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Open() = %v, want %v", err, storage.ErrNotFound)
	}
}

// TestIntegrationMultipart uploads an object that is larger than a part, so
// that the multipart path of the driver runs. It is skipped unless
// TONG_TEST_S3_ENDPOINT points at a service.
func TestIntegrationMultipart(t *testing.T) {
	endpoint := os.Getenv("TONG_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("set TONG_TEST_S3_ENDPOINT to run the S3 integration tests")
	}

	region := os.Getenv("TONG_TEST_S3_REGION")
	if region == "" {
		region = "us-east-1"
	}
	bucket := os.Getenv("TONG_TEST_S3_BUCKET")
	if bucket == "" {
		bucket = "tong-test"
	}

	ctx := context.Background()
	driver := s3.New(s3.Options{
		Region:   region,
		Endpoint: endpoint,
		// 5 MiB is the smallest part S3 allows, so that a 6 MiB object is
		// uploaded in two parts with the smallest possible payload.
		PartSize:          5 << 20,
		UploadConcurrency: 2,
	})

	createBucket(t, ctx, region, endpoint, bucket)

	prefix := "tong/" + strings.ReplaceAll(t.Name(), "/", "-") + "/"
	dest, err := driver.ResolveDest(ctx, storage.Location{
		Scheme: storage.SchemeS3,
		Bucket: bucket,
		Key:    prefix,
	}, false)
	if err != nil {
		t.Fatalf("ResolveDest() = %v", err)
	}

	modTime := time.Now().Add(-time.Hour).Truncate(time.Second)
	content := bytes.Repeat([]byte("0123456789abcdef"), 6<<20/16)

	// The engine tells the driver how large the file is, which is what lets
	// the transfer manager plan the parts of the upload.
	sized, ok := dest.Endpoint.(storage.SizedCreator)
	if !ok {
		t.Fatal("the S3 endpoint does not accept the size of a file")
	}
	if err := sized.CreateSized(ctx, "big.bin", modTime, int64(len(content)), bytes.NewReader(content)); err != nil {
		t.Fatalf("CreateSized() = %v", err)
	}
	t.Cleanup(func() {
		if err := dest.Endpoint.Delete(ctx, "big.bin"); err != nil {
			t.Logf("cannot clean up: %v", err)
		}
	})

	if err := dest.Endpoint.List(ctx, "", func(info storage.FileInfo) error {
		if info.Key != "big.bin" {
			return nil
		}
		if info.Size != int64(len(content)) {
			t.Errorf("size = %d, want %d", info.Size, len(content))
		}
		if !info.ModTime.Truncate(time.Second).Equal(modTime) {
			t.Errorf("modification time = %v, want %v", info.ModTime, modTime)
		}
		return nil
	}); err != nil {
		t.Fatalf("List() = %v", err)
	}

	reader, err := dest.Endpoint.Open(ctx, "big.bin")
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("cannot read the object: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("cannot close the object: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("the object holds %d bytes, want the %d bytes that were written", len(got), len(content))
	}
}
