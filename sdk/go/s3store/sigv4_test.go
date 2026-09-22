package s3store

import (
	"net/url"
	"strings"
	"testing"
	"time"

	raildrop "github.com/akpor-kofi/raildrop/sdk/go"
)

func TestPresignMatchesAWSReferenceVector(t *testing.T) {
	storage, err := NewRailwayBucketStorage(s3Config(t), nil)
	if err != nil {
		t.Fatalf("storage could not be created: %v", err)
	}
	storage.now = func() time.Time { return time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC) }
	signedURL, err := storage.SignedURL(nil, "test.txt", 86400, "")
	if err != nil {
		t.Fatalf("presign failed: %v", err)
	}
	expected := "https://examplebucket.s3.amazonaws.com/test.txt" +
		"?X-Amz-Algorithm=AWS4-HMAC-SHA256" +
		"&X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20130524%2Fus-east-1%2Fs3%2Faws4_request" +
		"&X-Amz-Date=20130524T000000Z" +
		"&X-Amz-Expires=86400" +
		"&X-Amz-SignedHeaders=host" +
		"&X-Amz-Signature=aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404"
	if signedURL != expected {
		t.Fatalf("presigned URL diverged from the AWS reference vector:\n got %s\nwant %s", signedURL, expected)
	}
}

func s3Config(t *testing.T) raildrop.BucketConfig {
	t.Helper()
	return raildrop.BucketConfig{
		Bucket:          "examplebucket",
		Endpoint:        "https://s3.amazonaws.com",
		Region:          "us-east-1",
		AccessKeyID:     "AKIAIOSFODNN7EXAMPLE",
		SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
	}
}

func TestPresignPutSignsContentTypeAndMetadata(t *testing.T) {
	storage, err := NewRailwayBucketStorage(s3Config(t), nil)
	if err != nil {
		t.Fatalf("storage could not be created: %v", err)
	}
	storage.now = func() time.Time { return time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC) }
	signedURL, err := storage.PresignPut(nil, raildrop.PresignPutArgs{
		Key:         "uploads/file name.png",
		ContentType: "image/png",
		Metadata:    map[string]string{"raildrop-id": "abc"},
		ExpiresIn:   3600,
	})
	if err != nil {
		t.Fatalf("presign failed: %v", err)
	}
	parsed, err := url.Parse(signedURL)
	if err != nil {
		t.Fatalf("presigned URL could not be parsed: %v", err)
	}
	if parsed.EscapedPath() != "/uploads/file%20name.png" {
		t.Fatalf("unexpected path: %s", parsed.EscapedPath())
	}
	query := parsed.Query()
	if query.Get("X-Amz-SignedHeaders") != "content-type;host;x-amz-meta-raildrop-id" {
		t.Fatalf("unexpected signed headers: %s", query.Get("X-Amz-SignedHeaders"))
	}
	if query.Get("X-Amz-Signature") == "" {
		t.Fatal("signature is missing")
	}
	if !strings.HasPrefix(query.Get("X-Amz-Credential"), "AKIAIOSFODNN7EXAMPLE/") {
		t.Fatalf("unexpected credential: %s", query.Get("X-Amz-Credential"))
	}
}

func TestUriEncode(t *testing.T) {
	if encoded := uriEncode("a b/c+d", true); encoded != "a%20b%2Fc%2Bd" {
		t.Fatalf("strict encode diverged: %s", encoded)
	}
	if encoded := uriEncode("a b/c+d", false); encoded != "a%20b/c%2Bd" {
		t.Fatalf("path encode diverged: %s", encoded)
	}
}
