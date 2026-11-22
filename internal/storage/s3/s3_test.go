package s3_test

import (
	"bytes"
	"context"
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

	prefix := "tong-test/" + strings.ReplaceAll(t.Name(), "/", "-") + "/"
	source, err := driver.ResolveSource(ctx, storage.Location{
		Scheme:   storage.SchemeS3,
		Bucket:   bucket,
		Key:      prefix,
		Trailing: true,
	})
	if err != nil {
		t.Fatalf("ResolveSource() = %v", err)
	}
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
}
