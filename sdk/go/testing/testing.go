package testing

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"

	raildrop "github.com/akpor-kofi/raildrop/sdk/go"
)

type memoryEntry struct {
	body         []byte
	contentType  string
	cacheControl string
	metadata     map[string]string
	etag         string
	lastModified time.Time
}

type MemoryStorage struct {
	mutex     sync.Mutex
	Objects   map[string]*memoryEntry
	Multipart map[string]raildrop.MultipartUpload
}

func NewMemoryStorage() *MemoryStorage {
	return &MemoryStorage{
		Objects:   make(map[string]*memoryEntry),
		Multipart: make(map[string]raildrop.MultipartUpload),
	}
}

func (storage *MemoryStorage) presignedURL(key string) string {
	return "https://storage.test/" + strings.ReplaceAll(key, "/", "%2F")
}

func (storage *MemoryStorage) PresignPut(ctx context.Context, args raildrop.PresignPutArgs) (string, error) {
	return storage.presignedURL(args.Key), nil
}

func (storage *MemoryStorage) CreateMultipart(ctx context.Context, args raildrop.MultipartArgs) (string, error) {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	uploadID := fmt.Sprintf("multipart-%d", len(storage.Multipart)+1)
	now := time.Now()
	storage.Multipart[uploadID] = raildrop.MultipartUpload{Key: args.Key, UploadID: uploadID, Initiated: &now}
	return uploadID, nil
}

func (storage *MemoryStorage) PresignPart(ctx context.Context, args raildrop.PresignPartArgs) (string, error) {
	return fmt.Sprintf("%s?uploadId=%s&part=%d", storage.presignedURL(args.Key), args.UploadID, args.PartNumber), nil
}

func (storage *MemoryStorage) CompleteMultipart(ctx context.Context, args raildrop.CompleteMultipartArgs) error {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	delete(storage.Multipart, args.UploadID)
	if _, ok := storage.Objects[args.Key]; !ok {
		storage.Objects[args.Key] = &memoryEntry{
			body:        []byte{},
			contentType: "application/octet-stream",
			etag:        "fake-multipart",
			metadata:    map[string]string{},
		}
	}
	return nil
}

func (storage *MemoryStorage) AbortMultipart(ctx context.Context, key string, uploadID string) error {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	delete(storage.Multipart, uploadID)
	return nil
}

func (storage *MemoryStorage) toStored(key string, entry *memoryEntry) *raildrop.StoredObject {
	lastModified := entry.lastModified
	etag := entry.etag
	contentType := entry.contentType
	cacheControl := entry.cacheControl
	metadata := make(map[string]string, len(entry.metadata))
	for name, value := range entry.metadata {
		metadata[name] = value
	}
	return &raildrop.StoredObject{
		Key:          key,
		Size:         int64(len(entry.body)),
		ContentType:  &contentType,
		ETag:         &etag,
		LastModified: &lastModified,
		Metadata:     metadata,
		CacheControl: &cacheControl,
	}
}

func (storage *MemoryStorage) Head(ctx context.Context, key string) (*raildrop.StoredObject, error) {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	entry, ok := storage.Objects[key]
	if !ok {
		return nil, nil
	}
	return storage.toStored(key, entry), nil
}

func (storage *MemoryStorage) Get(ctx context.Context, key string, rangeHeader string) (*raildrop.ObjectBody, error) {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	entry, ok := storage.Objects[key]
	if !ok {
		return nil, nil
	}
	body := entry.body
	var contentRange *string
	if rangeHeader != "" {
		start, end, parseErr := parseRange(rangeHeader, int64(len(body)))
		if parseErr != nil {
			return nil, raildrop.ErrRangeNotSatisfiable
		}
		body = body[start : end+1]
		formatted := fmt.Sprintf("bytes %d-%d/%d", start, end, len(entry.body))
		contentRange = &formatted
	}
	stored := storage.toStored(key, entry)
	return &raildrop.ObjectBody{
		StoredObject: *stored,
		Body:         io.NopCloser(bytes.NewReader(body)),
		ContentRange: contentRange,
	}, nil
}

func parseRange(header string, size int64) (int64, int64, error) {
	parts := strings.SplitN(header, "=", 2)
	if len(parts) != 2 || parts[0] != "bytes" {
		return 0, 0, fmt.Errorf("unsupported range")
	}
	span := strings.SplitN(parts[1], "-", 2)
	if len(span) != 2 {
		return 0, 0, fmt.Errorf("unsupported range")
	}
	start, err := strconv.ParseInt(span[0], 10, 64)
	if err != nil || start >= size {
		return 0, 0, fmt.Errorf("range not satisfiable")
	}
	end, err := strconv.ParseInt(span[1], 10, 64)
	if err != nil || end >= size {
		end = size - 1
	}
	if end < start {
		return 0, 0, fmt.Errorf("range not satisfiable")
	}
	return start, end, nil
}

func (storage *MemoryStorage) Put(ctx context.Context, args raildrop.PutArgs) (*raildrop.StoredObject, error) {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	metadata := make(map[string]string, len(args.Metadata))
	for name, value := range args.Metadata {
		metadata[name] = value
	}
	entry := &memoryEntry{
		body:         append([]byte{}, args.Body...),
		contentType:  args.ContentType,
		cacheControl: args.CacheControl,
		metadata:     metadata,
		etag:         fmt.Sprintf("etag-%d", len(args.Body)),
		lastModified: time.Now(),
	}
	storage.Objects[args.Key] = entry
	return storage.toStored(args.Key, entry), nil
}

func (storage *MemoryStorage) Delete(ctx context.Context, keys ...string) error {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	for _, key := range keys {
		delete(storage.Objects, key)
	}
	return nil
}

func (storage *MemoryStorage) Copy(ctx context.Context, sourceKey string, destinationKey string) error {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	source, ok := storage.Objects[sourceKey]
	if !ok {
		return fmt.Errorf("source does not exist")
	}
	cloned := *source
	cloned.body = append([]byte{}, source.body...)
	cloned.metadata = make(map[string]string, len(source.metadata))
	for name, value := range source.metadata {
		cloned.metadata[name] = value
	}
	storage.Objects[destinationKey] = &cloned
	return nil
}

func (storage *MemoryStorage) List(ctx context.Context, prefix string, continuationToken string) (*raildrop.ListResult, error) {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	result := &raildrop.ListResult{Objects: []raildrop.StoredObject{}}
	for key, entry := range storage.Objects {
		if strings.HasPrefix(key, prefix) {
			result.Objects = append(result.Objects, *storage.toStored(key, entry))
		}
	}
	return result, nil
}

func (storage *MemoryStorage) ListMultipart(ctx context.Context, prefix string) ([]raildrop.MultipartUpload, error) {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	uploads := []raildrop.MultipartUpload{}
	for _, upload := range storage.Multipart {
		if strings.HasPrefix(upload.Key, prefix) {
			uploads = append(uploads, upload)
		}
	}
	return uploads, nil
}

func (storage *MemoryStorage) SignedURL(ctx context.Context, key string, expiresIn int, downloadName string) (string, error) {
	return fmt.Sprintf("%s?expiresIn=%d", storage.presignedURL(key), expiresIn), nil
}

type Harness struct {
	Storage *MemoryStorage
	Handler http.Handler
}

type HarnessOptions struct {
	Secret                  string
	PublicBaseURL           string
	MultipartThresholdBytes int64
	MultipartPartSizeBytes  int64
	UploadExpirySeconds     int
}

func NewHarness(router raildrop.Router, options HarnessOptions) *Harness {
	storage := NewMemoryStorage()
	secret := options.Secret
	if secret == "" {
		secret = "raildrop-testing-secret-at-least-thirty-two-characters"
	}
	publicBaseURL := options.PublicBaseURL
	if publicBaseURL == "" {
		publicBaseURL = "https://assets.raildrop.test"
	}
	handler := raildrop.NewHandler(raildrop.HandlerConfig{
		Router:                  router,
		Storage:                 storage,
		Secret:                  secret,
		PublicBaseURL:           publicBaseURL,
		MultipartThresholdBytes: options.MultipartThresholdBytes,
		MultipartPartSizeBytes:  options.MultipartPartSizeBytes,
		UploadExpirySeconds:     options.UploadExpirySeconds,
	})
	return &Harness{Storage: storage, Handler: handler}
}

func (harness *Harness) Do(request *http.Request) *http.Response {
	recorder := httptest.NewRecorder()
	harness.Handler.ServeHTTP(recorder, request)
	return recorder.Result()
}
