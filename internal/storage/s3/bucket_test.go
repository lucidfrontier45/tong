package s3_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	awss3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// createBucket creates the bucket of the integration tests, unless it is
// already there.
func createBucket(t *testing.T, ctx context.Context, region, endpoint, bucket string) {
	t.Helper()

	client := awss3.New(awss3.Options{
		Region:       region,
		BaseEndpoint: aws.String(endpoint),
		UsePathStyle: true,
	})

	_, err := client.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(bucket)})
	if err == nil {
		return
	}

	// The bucket may already exist, which is not an error for the test.
	var owned *awss3types.BucketAlreadyOwnedByYou
	var exists *awss3types.BucketAlreadyExists
	if errors.As(err, &owned) || errors.As(err, &exists) {
		return
	}
	t.Fatalf("cannot create the bucket %s: %v", bucket, err)
}
