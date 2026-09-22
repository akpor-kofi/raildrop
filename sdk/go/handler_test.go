package raildrop_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	raildrop "github.com/akpor-kofi/raildrop/sdk/go"
	raildroptest "github.com/akpor-kofi/raildrop/sdk/go/testing"
)

const testSecret = "test-secret-that-is-at-least-thirty-two-characters"

func buildTestRouter() raildrop.Router {
	return raildrop.Router{
		"image": raildrop.DefineRoute(raildrop.Rules{"image": {MaxFileSize: "4MB", MaxFileCount: 1}}, raildrop.RouteDefinition[any, map[string]string, map[string]any]{
			Middleware: func(_ context.Context, _ *http.Request, _ any, _ []raildrop.RequestedFile) (map[string]string, error) {
				return map[string]string{"organizationId": "org-1"}, nil
			},
			Namespace: func(_ any, metadata map[string]string, _ raildrop.RequestedFile) ([]string, error) {
				return []string{"organizations", metadata["organizationId"], "images"}, nil
			},
			OnUploadComplete: func(_ context.Context, file raildrop.UploadedFileInfo, _ any, _ map[string]string) (map[string]any, error) {
				return map[string]any{"ok": true}, nil
			},
		}),
		"document": raildrop.DefineRoute(raildrop.Rules{"pdf": {MaxFileSize: "32MB"}}, raildrop.RouteDefinition[any, map[string]string, map[string]any]{
			Namespace: func(_ any, _ map[string]string, _ raildrop.RequestedFile) ([]string, error) {
				return []string{"documents"}, nil
			},
		}, raildrop.WithAccess(raildrop.AccessPrivate), raildrop.WithRetention(raildrop.RetentionTemporary, 7*24*60*60)),
		"limited": raildrop.DefineRoute(raildrop.Rules{"image": {MaxFileSize: "1KB", MaxFileCount: 1}}, raildrop.RouteDefinition[any, map[string]string, map[string]any]{}),
		"file": raildrop.DefineRoute(raildrop.Rules{"text": {MaxFileSize: "1MB"}}, raildrop.RouteDefinition[any, map[string]string, map[string]any]{
			OnUploadComplete: func(_ context.Context, file raildrop.UploadedFileInfo, _ any, _ map[string]string) (map[string]any, error) {
				return map[string]any{"uploadId": file.ID}, nil
			},
		}),
	}
}

func postJSON(t *testing.T, handler http.Handler, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("request could not be encoded: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "https://api.test/raildrop", bytes.NewReader(raw))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

type preparedWire struct {
	ID           string          `json:"id"`
	Key          string          `json:"key"`
	SessionToken string          `json:"sessionToken"`
	Upload       preparedUploads `json:"upload"`
}

type preparedUploads struct {
	Kind     string            `json:"kind"`
	URL      string            `json:"url"`
	Headers  map[string]string `json:"headers"`
	UploadID string            `json:"uploadId"`
	Parts    []struct {
		PartNumber int   `json:"partNumber"`
		Start      int64 `json:"start"`
		End        int64 `json:"end"`
	} `json:"parts"`
}

func completeStoredObject(t *testing.T, storage *raildroptest.MemoryStorage, key string, id string, body string, contentType string) {
	t.Helper()
	if _, err := storage.Put(nil, raildrop.PutArgs{
		Key:         key,
		Body:        []byte(body),
		ContentType: contentType,
		Metadata: map[string]string{
			"raildrop-id":        id,
			"raildrop-access":    "public",
			"raildrop-retention": "permanent",
		},
	}); err != nil {
		t.Fatalf("storage put failed: %v", err)
	}
}

func TestPrepareDefaultsPublicAndFinalizeVerifiesStoredObject(t *testing.T) {
	harness := raildroptest.NewHarness(buildTestRouter(), raildroptest.HarnessOptions{Secret: testSecret})
	prepare := postJSON(t, harness.Handler, map[string]any{
		"action":   "prepare",
		"endpoint": "image",
		"files":    []map[string]any{{"name": "photo.png", "type": "image/png", "size": 3}},
	})
	if prepare.Code != http.StatusOK {
		t.Fatalf("prepare failed with %d: %s", prepare.Code, prepare.Body.String())
	}
	var prepared []preparedWire
	if err := json.Unmarshal(prepare.Body.Bytes(), &prepared); err != nil {
		t.Fatalf("prepare response could not be parsed: %v", err)
	}
	entry := prepared[0]
	if !strings.HasPrefix(entry.Key, "public/organizations/org-1/images/") {
		t.Fatalf("unexpected key: %s", entry.Key)
	}
	expectedHeaders := map[string]string{
		"Content-Type":                  "image/png",
		"x-amz-meta-raildrop-access":    "public",
		"x-amz-meta-raildrop-id":        entry.ID,
		"x-amz-meta-raildrop-name":      "cGhvdG8ucG5n",
		"x-amz-meta-raildrop-retention": "permanent",
	}
	if len(entry.Upload.Headers) != len(expectedHeaders) {
		t.Fatalf("unexpected headers: %v", entry.Upload.Headers)
	}
	for name, value := range expectedHeaders {
		if entry.Upload.Headers[name] != value {
			t.Fatalf("header %s = %q; want %q", name, entry.Upload.Headers[name], value)
		}
	}
	if entry.Upload.Kind != "put" {
		t.Fatalf("unexpected upload kind: %s", entry.Upload.Kind)
	}
	for _, segment := range strings.Split(entry.SessionToken, ".")[1:4] {
		if strings.Contains(segment, "org-1") {
			t.Fatal("session token leaked unencrypted metadata")
		}
	}
	wrongEndpoint := postJSON(t, harness.Handler, map[string]any{
		"action":       "finalize",
		"endpoint":     "another-route",
		"sessionToken": entry.SessionToken,
	})
	if wrongEndpoint.Code != http.StatusForbidden {
		t.Fatalf("wrong endpoint expected 403, got %d", wrongEndpoint.Code)
	}
	completeStoredObject(t, harness.Storage, entry.Key, entry.ID, "ab", "image/png")
	mismatched := postJSON(t, harness.Handler, map[string]any{
		"action":       "finalize",
		"endpoint":     "image",
		"sessionToken": entry.SessionToken,
	})
	if mismatched.Code != http.StatusBadRequest {
		t.Fatalf("size mismatch expected 400, got %d", mismatched.Code)
	}
	completeStoredObject(t, harness.Storage, entry.Key, entry.ID, "abc", "image/png")
	finalize := postJSON(t, harness.Handler, map[string]any{
		"action":       "finalize",
		"endpoint":     "image",
		"sessionToken": entry.SessionToken,
	})
	if finalize.Code != http.StatusOK {
		t.Fatalf("finalize failed with %d: %s", finalize.Code, finalize.Body.String())
	}
	var result struct {
		ID         string         `json:"id"`
		PublicURL  string         `json:"publicUrl"`
		ServerData map[string]any `json:"serverData"`
	}
	if err := json.Unmarshal(finalize.Body.Bytes(), &result); err != nil {
		t.Fatalf("finalize response could not be parsed: %v", err)
	}
	if result.PublicURL != "https://assets.raildrop.test/"+entry.Key {
		t.Fatalf("unexpected public URL: %s", result.PublicURL)
	}
	if result.ServerData == nil || result.ServerData["ok"] != true {
		t.Fatalf("unexpected serverData: %v", result.ServerData)
	}
	tampered := postJSON(t, harness.Handler, map[string]any{
		"action":       "finalize",
		"endpoint":     "image",
		"sessionToken": entry.SessionToken + "x",
	})
	if tampered.Code != http.StatusForbidden {
		t.Fatalf("tampered token expected 403, got %d", tampered.Code)
	}
}

func TestPrepareEnforcesTypeSizeAndCount(t *testing.T) {
	harness := raildroptest.NewHarness(buildTestRouter(), raildroptest.HarnessOptions{Secret: testSecret})
	invalidType := postJSON(t, harness.Handler, map[string]any{
		"action":   "prepare",
		"endpoint": "limited",
		"files":    []map[string]any{{"name": "payload.pdf", "type": "application/pdf", "size": 10}},
	})
	if invalidType.Code != http.StatusBadRequest {
		t.Fatalf("invalid type expected 400, got %d", invalidType.Code)
	}
	if !strings.Contains(invalidType.Body.String(), "INVALID_TYPE") {
		t.Fatalf("unexpected error body: %s", invalidType.Body.String())
	}
	tooLarge := postJSON(t, harness.Handler, map[string]any{
		"action":   "prepare",
		"endpoint": "limited",
		"files":    []map[string]any{{"name": "photo.png", "type": "image/png", "size": 1001}},
	})
	if tooLarge.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("too large expected 413, got %d", tooLarge.Code)
	}
	tooMany := postJSON(t, harness.Handler, map[string]any{
		"action":   "prepare",
		"endpoint": "limited",
		"files": []map[string]any{
			{"name": "first.png", "type": "image/png", "size": 10},
			{"name": "second.png", "type": "image/png", "size": 10},
		},
	})
	if tooMany.Code != http.StatusBadRequest {
		t.Fatalf("too many expected 400, got %d", tooMany.Code)
	}
	if len(harness.Storage.Objects) != 0 {
		t.Fatal("validation must not write to storage")
	}
}

func TestPrivateTemporaryMultipartPreparation(t *testing.T) {
	harness := raildroptest.NewHarness(buildTestRouter(), raildroptest.HarnessOptions{
		Secret:                  testSecret,
		MultipartThresholdBytes: 5,
		MultipartPartSizeBytes:  5_000_000,
	})
	size := 11_000_001
	response := postJSON(t, harness.Handler, map[string]any{
		"action":   "prepare",
		"endpoint": "document",
		"files":    []map[string]any{{"name": "id.pdf", "type": "application/pdf", "size": size}},
	})
	if response.Code != http.StatusOK {
		t.Fatalf("prepare failed with %d: %s", response.Code, response.Body.String())
	}
	var prepared []preparedWire
	if err := json.Unmarshal(response.Body.Bytes(), &prepared); err != nil {
		t.Fatalf("prepare response could not be parsed: %v", err)
	}
	entry := prepared[0]
	if !strings.HasPrefix(entry.Key, "private/tmp/") || !strings.Contains(entry.Key, "/documents/") {
		t.Fatalf("unexpected key: %s", entry.Key)
	}
	if entry.Upload.Kind != "multipart" {
		t.Fatalf("unexpected upload kind: %s", entry.Upload.Kind)
	}
	if len(entry.Upload.Parts) != 3 {
		t.Fatalf("unexpected part count: %d", len(entry.Upload.Parts))
	}
	expectedRanges := [][2]int64{{0, 5_000_000}, {5_000_000, 10_000_000}, {10_000_000, int64(size)}}
	for index, expected := range expectedRanges {
		part := entry.Upload.Parts[index]
		if part.Start != expected[0] || part.End != expected[1] || part.PartNumber != index+1 {
			t.Fatalf("part %d diverged: %+v", index, part)
		}
	}
	if len(harness.Storage.Multipart) != 1 {
		t.Fatalf("expected one multipart upload, got %d", len(harness.Storage.Multipart))
	}
	aborted := postJSON(t, harness.Handler, map[string]any{
		"action":       "abort",
		"endpoint":     "document",
		"sessionToken": entry.SessionToken,
	})
	if aborted.Code != http.StatusOK {
		t.Fatalf("abort failed with %d", aborted.Code)
	}
	if len(harness.Storage.Multipart) != 0 {
		t.Fatal("abort did not remove the multipart upload")
	}
}

func TestMalformedRequestsFailAsBadRequest(t *testing.T) {
	harness := raildroptest.NewHarness(buildTestRouter(), raildroptest.HarnessOptions{Secret: testSecret})
	response := postJSON(t, harness.Handler, map[string]any{
		"action":   "prepare",
		"endpoint": "file",
		"files":    []any{nil},
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("malformed files expected 400, got %d", response.Code)
	}
	invalidJSON := httptest.NewRequest(http.MethodPost, "https://api.test/raildrop", strings.NewReader("{"))
	recorder := httptest.NewRecorder()
	harness.Handler.ServeHTTP(recorder, invalidJSON)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid JSON expected 400, got %d", recorder.Code)
	}
	methodProbe := httptest.NewRequest(http.MethodGet, "https://api.test/raildrop", nil)
	recorder = httptest.NewRecorder()
	harness.Handler.ServeHTTP(recorder, methodProbe)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET expected 405, got %d", recorder.Code)
	}
	unknownAction := postJSON(t, harness.Handler, map[string]any{"action": "sync", "endpoint": "file"})
	if unknownAction.Code != http.StatusBadRequest {
		t.Fatalf("unknown action expected 400, got %d", unknownAction.Code)
	}
	if !strings.Contains(unknownAction.Body.String(), "Unknown Raildrop action.") {
		t.Fatalf("unexpected body: %s", unknownAction.Body.String())
	}
}

func TestCallbackFailureIsRetryableWithStableUploadID(t *testing.T) {
	attempts := 0
	callbackFailed := errors.New("retry me")
	router := raildrop.Router{
		"file": raildrop.DefineRoute(raildrop.Rules{"text": {MaxFileSize: "1MB"}}, raildrop.RouteDefinition[any, map[string]string, map[string]any]{
			OnUploadComplete: func(_ context.Context, file raildrop.UploadedFileInfo, _ any, _ map[string]string) (map[string]any, error) {
				attempts++
				if attempts == 1 {
					return nil, callbackFailed
				}
				return map[string]any{"uploadId": file.ID}, nil
			},
		}),
	}
	harness := raildroptest.NewHarness(router, raildroptest.HarnessOptions{Secret: testSecret})
	prepare := postJSON(t, harness.Handler, map[string]any{
		"action":   "prepare",
		"endpoint": "file",
		"files":    []map[string]any{{"name": "note.txt", "type": "text/plain", "size": 4}},
	})
	if prepare.Code != http.StatusOK {
		t.Fatalf("prepare failed with %d", prepare.Code)
	}
	var prepared []preparedWire
	if err := json.Unmarshal(prepare.Body.Bytes(), &prepared); err != nil {
		t.Fatalf("prepare response could not be parsed: %v", err)
	}
	entry := prepared[0]
	completeStoredObject(t, harness.Storage, entry.Key, entry.ID, "note", "text/plain")
	finalizeBody := map[string]any{
		"action":       "finalize",
		"endpoint":     "file",
		"sessionToken": entry.SessionToken,
	}
	first := postJSON(t, harness.Handler, finalizeBody)
	if first.Code != http.StatusBadGateway {
		t.Fatalf("callback failure expected 502, got %d: %s", first.Code, first.Body.String())
	}
	retry := postJSON(t, harness.Handler, finalizeBody)
	if retry.Code != http.StatusOK {
		t.Fatalf("callback retry expected 200, got %d: %s", retry.Code, retry.Body.String())
	}
	var result struct {
		ID         string `json:"id"`
		ServerData struct {
			UploadID string `json:"uploadId"`
		} `json:"serverData"`
	}
	if err := json.Unmarshal(retry.Body.Bytes(), &result); err != nil {
		t.Fatalf("response could not be parsed: %v", err)
	}
	if result.ID != entry.ID || result.ServerData.UploadID != entry.ID {
		t.Fatalf("upload id was not stable: %+v", result)
	}
}
