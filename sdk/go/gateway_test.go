package raildrop_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	raildrop "github.com/akpor-kofi/raildrop/sdk/go"
	raildroptest "github.com/akpor-kofi/raildrop/sdk/go/testing"
)

func seedGatewayStorage(t *testing.T) *raildroptest.MemoryStorage {
	t.Helper()
	storage := raildroptest.NewMemoryStorage()
	if _, err := storage.Put(nil, raildrop.PutArgs{
		Key:         "private/users/u1/file.pdf",
		Body:        []byte("private"),
		ContentType: "application/pdf",
		Metadata:    map[string]string{"raildrop-access": "private"},
	}); err != nil {
		t.Fatalf("seed failed: %v", err)
	}
	return storage
}

func gatewayGet(gateway http.Handler, target string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	gateway.ServeHTTP(recorder, request)
	return recorder
}

func TestPublicGatewayHidesPrivateKeys(t *testing.T) {
	storage := seedGatewayStorage(t)
	gateway := raildrop.NewPublicGateway(raildrop.PublicGatewayConfig{Storage: storage})
	if response := gatewayGet(gateway, "https://assets.test/private/users/u1/file.pdf", nil); response.Code != http.StatusNotFound {
		t.Fatalf("private key expected 404, got %d", response.Code)
	}
}

func TestPublicGatewayResolvesGlobalAliases(t *testing.T) {
	storage := raildroptest.NewMemoryStorage()
	if _, err := raildrop.SyncGlobalAssets(context.Background(), storage, []raildrop.GlobalAssetSource{{
		Alias:       "product-template",
		Body:        []byte("template"),
		Filename:    "template.xlsx",
		ContentType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		Namespace:   []string{"templates"},
	}}); err != nil {
		t.Fatalf("sync failed: %v", err)
	}
	gateway := raildrop.NewPublicGateway(raildrop.PublicGatewayConfig{Storage: storage})
	response := gatewayGet(gateway, "https://assets.test/global/product-template", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("alias expected 200, got %d", response.Code)
	}
	if response.Header().Get("Cache-Control") != "public, max-age=300, stale-while-revalidate=86400" {
		t.Fatalf("unexpected alias cache control: %s", response.Header().Get("Cache-Control"))
	}
	if response.Body.String() != "template" {
		t.Fatalf("unexpected alias body: %q", response.Body.String())
	}
}

func TestPublicGatewayConditionalRequests(t *testing.T) {
	storage := raildroptest.NewMemoryStorage()
	if _, err := storage.Put(nil, raildrop.PutArgs{
		Key:          "public/files/upload/file.txt",
		Body:         []byte("hello"),
		ContentType:  "text/plain",
		CacheControl: "public, max-age=31536000, immutable",
		Metadata:     map[string]string{"raildrop-access": "public"},
	}); err != nil {
		t.Fatalf("seed failed: %v", err)
	}
	gateway := raildrop.NewPublicGateway(raildrop.PublicGatewayConfig{Storage: storage})
	head := httptest.NewRequest(http.MethodHead, "https://assets.test/public/files/upload/file.txt", nil)
	headRecorder := httptest.NewRecorder()
	gateway.ServeHTTP(headRecorder, head)
	if headRecorder.Code != http.StatusOK || headRecorder.Header().Get("Content-Length") != "5" {
		t.Fatalf("head failed: %d %s", headRecorder.Code, headRecorder.Header().Get("Content-Length"))
	}
	conditional := gatewayGet(gateway, "https://assets.test/public/files/upload/file.txt", map[string]string{
		"If-None-Match": headRecorder.Header().Get("ETag"),
	})
	if conditional.Code != http.StatusNotModified {
		t.Fatalf("conditional expected 304, got %d", conditional.Code)
	}
	precedence := gatewayGet(gateway, "https://assets.test/public/files/upload/file.txt", map[string]string{
		"If-None-Match":     "\"different-etag\"",
		"If-Modified-Since": time.Now().Add(time.Minute).UTC().Format(http.TimeFormat),
	})
	if precedence.Code != http.StatusOK {
		t.Fatalf("etag precedence expected 200, got %d", precedence.Code)
	}
}

func TestPublicGatewayRefusesTraversalAndEncodedPaths(t *testing.T) {
	storage := raildroptest.NewMemoryStorage()
	gateway := raildrop.NewPublicGateway(raildrop.PublicGatewayConfig{Storage: storage})
	if response := gatewayGet(gateway, "https://assets.test/public/%2e%2e/private/file", nil); response.Code >= 200 && response.Code < 400 {
		t.Fatalf("encoded traversal must not be served, got %d", response.Code)
	}
	if response := gatewayGet(gateway, "https://assets.test/public/files/%00/file.txt", nil); response.Code != http.StatusBadRequest {
		t.Fatalf("null byte expected 400, got %d", response.Code)
	}
	if response := gatewayGet(gateway, "https://assets.test/public/files/%252e%252e/file.txt", nil); response.Code != http.StatusBadRequest {
		t.Fatalf("double-encoded traversal expected 400, got %d", response.Code)
	}
	if response := gatewayGet(gateway, "https://assets.test/public/hello%20world/upload/file.txt", nil); response.Code != http.StatusNotFound {
		t.Fatalf("encoded namespace should reach storage (404 when missing), got %d", response.Code)
	}
}

func TestPublicGatewayCapsTemporaryCaching(t *testing.T) {
	storage := raildroptest.NewMemoryStorage()
	expiresAt := time.Now().Add(10 * time.Minute).UTC().Format("2006-01-02T15:04:05.000Z07:00")
	if _, err := storage.Put(nil, raildrop.PutArgs{
		Key:          "public/tmp/99999/files/upload/file.txt",
		Body:         []byte("temporary"),
		ContentType:  "text/plain",
		CacheControl: "public, max-age=31536000, immutable",
		Metadata: map[string]string{
			"raildrop-access":     "public",
			"raildrop-retention":  "temporary",
			"raildrop-expires-at": expiresAt,
		},
	}); err != nil {
		t.Fatalf("seed failed: %v", err)
	}
	gateway := raildrop.NewPublicGateway(raildrop.PublicGatewayConfig{Storage: storage})
	response := gatewayGet(gateway, "https://assets.test/public/tmp/99999/files/upload/file.txt", nil)
	cacheControl := response.Header().Get("Cache-Control")
	if response.Code != http.StatusOK {
		t.Fatalf("temporary object expected 200, got %d", response.Code)
	}
	if cacheControl == "" || len(cacheControl) > len("public, max-age=600, must-revalidate") {
		t.Fatalf("unexpected cache control: %q", cacheControl)
	}
}
