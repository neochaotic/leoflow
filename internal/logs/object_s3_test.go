package logs

import (
	"context"
	"testing"
)

func TestNewS3StoreRequiresBucket(t *testing.T) {
	if _, err := NewS3Store(context.Background(), S3Config{}); err == nil {
		t.Fatal("NewS3Store with empty bucket = nil error, want error")
	}
}

// TestNewS3StoreBuildsClient constructs a store against an S3-compatible endpoint
// (MinIO/GCS-interop shape) with static credentials, asserting the constructor
// wires the bucket and satisfies ObjectStore without any network call.
func TestNewS3StoreBuildsClient(t *testing.T) {
	store, err := NewS3Store(context.Background(), S3Config{
		Bucket:          "task-logs",
		Region:          "us-east-1",
		Endpoint:        "http://localhost:9000",
		ForcePathStyle:  true,
		AccessKeyID:     "minio",
		SecretAccessKey: "minio-secret",
	})
	if err != nil {
		t.Fatalf("NewS3Store() error = %v", err)
	}
	var _ ObjectStore = store
	if store.bucket != "task-logs" {
		t.Errorf("store.bucket = %q, want \"task-logs\"", store.bucket)
	}
	if store.client == nil {
		t.Error("store.client = nil, want a configured S3 client")
	}
}
