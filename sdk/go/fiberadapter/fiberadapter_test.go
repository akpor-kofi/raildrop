package fiberadapter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	raildrop "github.com/akpor-kofi/raildrop/sdk/go"
	fiberadapter "github.com/akpor-kofi/raildrop/sdk/go/fiberadapter"
	raildroptest "github.com/akpor-kofi/raildrop/sdk/go/testing"
)

func TestFiberAdapterForwardsRequestsAndResponses(t *testing.T) {
	router := raildrop.Router{
		"file": raildrop.DefineRoute(raildrop.Rules{"text": {MaxFileSize: "1MB"}}, raildrop.RouteDefinition[any, map[string]string, map[string]any]{
			Middleware: func(_ context.Context, request *http.Request, _ any, _ []raildrop.RequestedFile) (map[string]string, error) {
				return map[string]string{"marker": request.Header.Get("X-Test-Marker")}, nil
			},
			Namespace: func(_ any, metadata map[string]string, _ raildrop.RequestedFile) ([]string, error) {
				return []string{"fiber", metadata["marker"]}, nil
			},
			OnUploadComplete: func(_ context.Context, _ raildrop.UploadedFileInfo, _ any, _ map[string]string) (map[string]any, error) {
				return nil, nil
			},
		}),
	}
	handler := raildrop.NewHandler(raildrop.HandlerConfig{
		Router:        router,
		Storage:       raildroptest.NewMemoryStorage(),
		Secret:        "test-secret-that-is-at-least-thirty-two-characters",
		PublicBaseURL: "https://assets.raildrop.test",
	})
	app := fiber.New()
	fiberadapter.Register(app, "/api/upload/*", handler)

	body := []byte(`{"action":"prepare","endpoint":"file","input":null,"files":[{"name":"note.txt","type":"text/plain","size":4}]}`)
	request := httptest.NewRequest(http.MethodPost, "/api/upload/create", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Test-Marker", "m-1")
	response, err := app.Test(request, 5000)
	if err != nil {
		t.Fatalf("fiber request failed: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("prepare through fiber failed with %d", response.StatusCode)
	}
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("handler headers were not forwarded: %s", response.Header.Get("Cache-Control"))
	}
	var prepared []struct {
		Key          string `json:"key"`
		SessionToken string `json:"sessionToken"`
	}
	if err := json.NewDecoder(response.Body).Decode(&prepared); err != nil {
		t.Fatalf("response could not be parsed: %v", err)
	}
	if !strings.HasPrefix(prepared[0].Key, "public/fiber/m-1/") {
		t.Fatalf("unexpected key from fiber-routed request: %s", prepared[0].Key)
	}

	methodProbe := httptest.NewRequest(http.MethodGet, "/api/upload/create", nil)
	response, err = app.Test(methodProbe, 5000)
	if err != nil {
		t.Fatalf("fiber request failed: %v", err)
	}
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET through fiber expected 405, got %d", response.StatusCode)
	}
}
