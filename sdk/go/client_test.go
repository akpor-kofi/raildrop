package raildrop_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	raildrop "github.com/akpor-kofi/raildrop/sdk/go"
	raildroptest "github.com/akpor-kofi/raildrop/sdk/go/testing"
)

type e2eStorage struct {
	*raildroptest.MemoryStorage
	serverURL string
	mutex     sync.Mutex
	parts     map[string][]byte
	metadata  map[string]map[string]string
	types     map[string]string
}

func newE2EStorage(serverURL string) *e2eStorage {
	return &e2eStorage{
		MemoryStorage: raildroptest.NewMemoryStorage(),
		serverURL:     serverURL,
		parts:         make(map[string][]byte),
		metadata:      make(map[string]map[string]string),
		types:         make(map[string]string),
	}
}

func (storage *e2eStorage) PresignPut(_ context.Context, args raildrop.PresignPutArgs) (string, error) {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	storage.metadata[args.Key] = args.Metadata
	storage.types[args.Key] = args.ContentType
	return storage.serverURL + "/storage/" + args.Key, nil
}

func (storage *e2eStorage) CreateMultipart(_ context.Context, args raildrop.MultipartArgs) (string, error) {
	uploadID, err := storage.MemoryStorage.CreateMultipart(context.Background(), raildrop.MultipartArgs{
		Key:         args.Key,
		ContentType: args.ContentType,
		Metadata:    args.Metadata,
	})
	if err != nil {
		return "", err
	}
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	storage.metadata[uploadID] = args.Metadata
	storage.types[uploadID] = args.ContentType
	return uploadID, nil
}

func (storage *e2eStorage) PresignPart(_ context.Context, args raildrop.PresignPartArgs) (string, error) {
	return storage.serverURL + "/storage/" + args.Key + "?uploadId=" + args.UploadID + "&partNumber=" + strconv.Itoa(args.PartNumber), nil
}

func (storage *e2eStorage) CompleteMultipart(_ context.Context, args raildrop.CompleteMultipartArgs) error {
	storage.mutex.Lock()
	defer storage.mutex.Unlock()
	var body []byte
	for _, part := range args.Parts {
		body = append(body, storage.parts[args.UploadID+":"+strconv.Itoa(part.PartNumber)]...)
	}
	if _, err := storage.MemoryStorage.Put(context.Background(), raildrop.PutArgs{
		Key:         args.Key,
		Body:        body,
		ContentType: storage.types[args.UploadID],
		Metadata:    storage.metadata[args.UploadID],
	}); err != nil {
		return err
	}
	return storage.MemoryStorage.CompleteMultipart(context.Background(), raildrop.CompleteMultipartArgs{
		Key:      args.Key,
		UploadID: args.UploadID,
		Parts:    args.Parts,
	})
}

func TestServerToServerClientUploadsSingleAndMultipart(t *testing.T) {
	mem := raildroptest.NewMemoryStorage()
	e2e := newE2EStorage("")
	e2e.MemoryStorage = mem
	handler := raildrop.NewHandler(raildrop.HandlerConfig{
		Router:                  buildTestRouter(),
		Storage:                 e2e,
		Secret:                  testSecret,
		PublicBaseURL:           "https://assets.raildrop.test",
		MultipartThresholdBytes: 64,
		MultipartPartSizeBytes:  1024,
	})
	mux := http.NewServeMux()
	mux.Handle("/api/upload", handler)
	mux.HandleFunc("/storage/", func(writer http.ResponseWriter, request *http.Request) {
		key := strings.TrimPrefix(request.URL.Path, "/storage/")
		if request.URL.Query().Has("uploadId") {
			body, _ := io.ReadAll(request.Body)
			e2e.mutex.Lock()
			e2e.parts[request.URL.Query().Get("uploadId")+":"+request.URL.Query().Get("partNumber")] = body
			e2e.mutex.Unlock()
			writer.Header().Set("ETag", "\"etag-part-"+request.URL.Query().Get("partNumber")+"\"")
			writer.WriteHeader(http.StatusOK)
			return
		}
		if _, err := mem.Put(request.Context(), raildrop.PutArgs{
			Key:         key,
			Body:        readAll(t, request.Body),
			ContentType: request.Header.Get("Content-Type"),
			Metadata: map[string]string{
				"raildrop-id":        request.Header.Get("x-amz-meta-raildrop-id"),
				"raildrop-access":    request.Header.Get("x-amz-meta-raildrop-access"),
				"raildrop-retention": request.Header.Get("x-amz-meta-raildrop-retention"),
				"raildrop-name":      request.Header.Get("x-amz-meta-raildrop-name"),
			},
		}); err != nil {
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		writer.Header().Set("ETag", "\"etag-object\"")
		writer.WriteHeader(http.StatusOK)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	e2e.serverURL = server.URL

	client := raildrop.NewClient(raildrop.ClientConfig{URL: server.URL + "/api/upload"})
	uploaded, err := client.Upload(context.Background(), "image", raildrop.UploadOptions{
		Files: []raildrop.UploadFile{{Name: "photo.png", Type: "image/png", Body: []byte("abc")}},
	})
	if err != nil {
		t.Fatalf("single-part upload failed: %v", err)
	}
	if len(uploaded) != 1 || uploaded[0].Size != 3 || uploaded[0].ServerData == nil {
		t.Fatalf("unexpected upload result: %+v", uploaded)
	}
	if uploaded[0].PublicURL == nil || !strings.HasPrefix(*uploaded[0].PublicURL, "https://assets.raildrop.test/public/organizations/org-1/images/") {
		t.Fatalf("unexpected public URL: %+v", uploaded[0].PublicURL)
	}

	multipartBody := make([]byte, 5000)
	for index := range multipartBody {
		multipartBody[index] = byte(index % 251)
	}
	uploaded, err = client.Upload(context.Background(), "image", raildrop.UploadOptions{
		Files: []raildrop.UploadFile{{Name: "big.png", Type: "image/png", Body: multipartBody}},
	})
	if err != nil {
		t.Fatalf("multipart upload failed: %v", err)
	}
	stored, err := mem.Head(context.Background(), uploaded[0].Key)
	if err != nil || stored == nil {
		t.Fatalf("multipart object missing: %v", err)
	}
	if stored.Size != int64(len(multipartBody)) {
		t.Fatalf("multipart object size mismatch: %d", stored.Size)
	}
	body, err := mem.Get(context.Background(), uploaded[0].Key, "")
	if err != nil || body == nil {
		t.Fatalf("multipart object could not be read: %v", err)
	}
	defer body.Close()
	content, err := io.ReadAll(body.Body)
	if err != nil {
		t.Fatalf("multipart content could not be read: %v", err)
	}
	if string(content) != string(multipartBody) {
		t.Fatal("multipart content diverged from the source bytes")
	}
}

func readAll(t *testing.T, body io.Reader) []byte {
	t.Helper()
	content, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("body could not be read: %v", err)
	}
	return content
}

func TestClientRejectsEmptyFileList(t *testing.T) {
	client := raildrop.NewClient(raildrop.ClientConfig{URL: "https://api.test/upload"})
	if _, err := client.Upload(context.Background(), "image", raildrop.UploadOptions{}); err == nil {
		t.Fatal("empty file list must be rejected")
	}
}

func TestClientSurfacesProtocolErrors(t *testing.T) {
	handler := raildrop.NewHandler(raildrop.HandlerConfig{
		Router:        buildTestRouter(),
		Storage:       raildroptest.NewMemoryStorage(),
		Secret:        testSecret,
		PublicBaseURL: "https://assets.raildrop.test",
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	client := raildrop.NewClient(raildrop.ClientConfig{URL: server.URL + "/api/upload"})
	_, err := client.Upload(context.Background(), "missing-endpoint", raildrop.UploadOptions{
		Files: []raildrop.UploadFile{{Name: "photo.png", Type: "image/png", Body: []byte("abc")}},
	})
	if err == nil {
		t.Fatal("unknown endpoint must fail")
	}
	raildropErr, ok := raildrop.AsError(err)
	if !ok || raildropErr.Code != raildrop.CodeNotFound {
		t.Fatalf("expected NOT_FOUND, got %v", err)
	}
	if !strings.Contains(raildropErr.Message, "Unknown upload endpoint") {
		t.Fatalf("unexpected error message: %s", raildropErr.Message)
	}
}

func TestClientForwardsPerUploadHeadersToProtocolCalls(t *testing.T) {
	mem := raildroptest.NewMemoryStorage()
	e2e := newE2EStorage("")
	e2e.MemoryStorage = mem
	uploadHandler := raildrop.NewHandler(raildrop.HandlerConfig{
		Router:        buildTestRouter(),
		Storage:       e2e,
		Secret:        testSecret,
		PublicBaseURL: "https://assets.raildrop.test",
	})
	var seen []string
	var seenMutex sync.Mutex
	tracked := http.NewServeMux()
	tracked.HandleFunc("/storage/", func(writer http.ResponseWriter, request *http.Request) {
		key := strings.TrimPrefix(request.URL.Path, "/storage/")
		if _, err := mem.Put(request.Context(), raildrop.PutArgs{
			Key:         key,
			Body:        readAll(t, request.Body),
			ContentType: request.Header.Get("Content-Type"),
			Metadata: map[string]string{
				"raildrop-id":        request.Header.Get("x-amz-meta-raildrop-id"),
				"raildrop-access":    request.Header.Get("x-amz-meta-raildrop-access"),
				"raildrop-retention": request.Header.Get("x-amz-meta-raildrop-retention"),
				"raildrop-name":      request.Header.Get("x-amz-meta-raildrop-name"),
			},
		}); err != nil {
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		writer.Header().Set("ETag", "\"etag-object\"")
		writer.WriteHeader(http.StatusOK)
	})
	tracked.Handle("/api/upload", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seenMutex.Lock()
		seen = append(seen, request.Method+" "+request.Header.Get("Authorization"))
		seenMutex.Unlock()
		uploadHandler.ServeHTTP(writer, request)
	}))
	server := httptest.NewServer(tracked)
	defer server.Close()
	e2e.serverURL = server.URL
	client := raildrop.NewClient(raildrop.ClientConfig{
		URL: server.URL + "/api/upload",
	})
	_, err := client.Upload(context.Background(), "image", raildrop.UploadOptions{
		Headers: http.Header{"Authorization": []string{"Bearer token-1"}},
		Files:   []raildrop.UploadFile{{Name: "photo.png", Type: "image/png", Body: []byte("abc")}},
	})
	if err != nil {
		t.Fatalf("authenticated upload failed: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("expected prepare and finalize through the wrapper, saw %d calls", len(seen))
	}
	for index, authorization := range seen {
		if !strings.HasSuffix(authorization, "Bearer token-1") {
			t.Fatalf("protocol call %d did not forward the Authorization header: %q", index, authorization)
		}
	}
}
