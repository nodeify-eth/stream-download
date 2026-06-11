package source

import "testing"

func TestNewS3StoresBucketAndKey(t *testing.T) {
	src := NewS3(nil, "snapshots", "mainnet/archive.tar.zst")
	if src.bucket != "snapshots" || src.key != "mainnet/archive.tar.zst" {
		t.Fatalf("NewS3 stored bucket/key = %q/%q", src.bucket, src.key)
	}
}
