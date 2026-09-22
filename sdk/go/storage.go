package raildrop

import (
	"context"
	"io"
	"time"
)

var ErrRangeNotSatisfiable = NewError(CodeBadRequest, "Range not satisfiable")

type BucketConfig struct {
	Bucket          string
	Endpoint        string
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	ForcePathStyle  bool
}

type StoredObject struct {
	Key                string
	Size               int64
	ContentType        *string
	ETag               *string
	LastModified       *time.Time
	Metadata           map[string]string
	ContentDisposition *string
	CacheControl       *string
}

type ObjectBody struct {
	StoredObject
	Body         io.ReadCloser
	ContentRange *string
}

func (object *ObjectBody) Close() error {
	if object == nil || object.Body == nil {
		return nil
	}
	return object.Body.Close()
}

type ListResult struct {
	Objects   []StoredObject
	NextToken string
}

type MultipartUpload struct {
	Key       string
	UploadID  string
	Initiated *time.Time
}

type PresignPutArgs struct {
	Key         string
	ContentType string
	Metadata    map[string]string
	ExpiresIn   int
}

type MultipartArgs struct {
	Key         string
	ContentType string
	Metadata    map[string]string
}

type PresignPartArgs struct {
	Key        string
	UploadID   string
	PartNumber int
	ExpiresIn  int
}

type CompletedPart struct {
	PartNumber int
	ETag       string
}

type CompleteMultipartArgs struct {
	Key      string
	UploadID string
	Parts    []CompletedPart
}

type PutArgs struct {
	Key          string
	Body         []byte
	ContentType  string
	CacheControl string
	Metadata     map[string]string
}

type CORSRule struct {
	AllowedOrigins []string
	AllowedMethods []string
	AllowedHeaders []string
	ExposeHeaders  []string
	MaxAgeSeconds  int
}

type Storage interface {
	PresignPut(ctx context.Context, args PresignPutArgs) (string, error)
	CreateMultipart(ctx context.Context, args MultipartArgs) (string, error)
	PresignPart(ctx context.Context, args PresignPartArgs) (string, error)
	CompleteMultipart(ctx context.Context, args CompleteMultipartArgs) error
	AbortMultipart(ctx context.Context, key string, uploadID string) error
	Head(ctx context.Context, key string) (*StoredObject, error)
	Get(ctx context.Context, key string, rangeHeader string) (*ObjectBody, error)
	Put(ctx context.Context, args PutArgs) (*StoredObject, error)
	Delete(ctx context.Context, keys ...string) error
	Copy(ctx context.Context, sourceKey string, destinationKey string) error
	List(ctx context.Context, prefix string, continuationToken string) (*ListResult, error)
	ListMultipart(ctx context.Context, prefix string) ([]MultipartUpload, error)
	SignedURL(ctx context.Context, key string, expiresIn int, downloadName string) (string, error)
}
