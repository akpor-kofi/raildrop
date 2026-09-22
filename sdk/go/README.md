# Raildrop for Go

Type-safe uploads and public delivery for S3-compatible buckets — the Go counterpart of
[`raildrop/server`](https://www.npmjs.com/package/raildrop). Raildrop for Go implements the
same wire protocol, session-token format, and storage semantics as the TypeScript SDK, so
Go backends and TypeScript frontends can be mixed freely.

```go
import "github.com/akpor-kofi/raildrop/sdk/go"
```

- **One dependency** (`golang.org/x/text`); everything else — including the S3 SigV4
  implementation — is Go standard library.
- **One handler type.** `raildrop.Handler` is a plain `http.Handler`, so it mounts directly
  into `net/http`, `chi`, `gorilla/mux`, `echo`, and anything else that speaks `net/http`.
  Fiber is covered by a dedicated adapter (see below).
- **Interoperable by construction.** Cross-language session-token conformance is locked by
  golden fixtures executed in both CI suites.

## Install

```bash
go get github.com/akpor-kofi/raildrop/sdk/go@latest
```

Requirements: Go 1.22+. On macOS 26 with Go 1.22, run tests with `CGO_ENABLED=0` (dyld
restriction), or use Go 1.24+.

## Quickstart

### 1. Define your upload routes

```go
package main

import (
	"context"
	"net/http"

	"github.com/akpor-kofi/raildrop/sdk/go"
)

type sessionMeta struct {
	UserID string `json:"userId"`
}

var router = raildrop.Router{
	"avatar": raildrop.DefineRoute(
		raildrop.Rules{"image": {MaxFileSize: "4MB", MaxFileCount: 1}},
		raildrop.RouteDefinition[any, sessionMeta, map[string]any]{
			Middleware: func(ctx context.Context, request *http.Request, _ any, _ []raildrop.RequestedFile) (sessionMeta, error) {
				user, err := getSessionUser(request) // your auth
				if err != nil {
					return sessionMeta{}, raildrop.NewError(raildrop.CodeForbidden, "Unauthorized")
				}
				return sessionMeta{UserID: user.ID}, nil
			},
			Namespace: func(_ any, metadata sessionMeta, _ raildrop.RequestedFile) ([]string, error) {
				return []string{"users", metadata.UserID, "avatars"}, nil
			},
			OnUploadComplete: func(ctx context.Context, file raildrop.UploadedFileInfo, _ any, metadata sessionMeta) (map[string]any, error) {
				if err := db.UpdateAvatar(ctx, metadata.UserID, file.Key); err != nil {
					return nil, err
				}
				return map[string]any{"ok": true}, nil
			},
		},
	),
}
```

Generics replace TypeScript inference: `RouteDefinition[Input, Metadata, Output]` types the
middleware input, the metadata returned from auth, and the `serverData` payload returned to
the client on finalize.

Route knobs map 1:1 to the TypeScript builder methods:

| TypeScript                        | Go                                                |
| --------------------------------- | ------------------------------------------------- |
| `.input<T>()`                     | `RouteDefinition` `Input` type parameter          |
| `.access('private')`              | `raildrop.WithAccess(raildrop.AccessPrivate)`     |
| `.retention('temporary', {...})`  | `raildrop.WithRetention(raildrop.RetentionTemporary, ttl)` |
| `.middleware(...)`                | `Middleware` field                                |
| `.namespace(...)`                 | `Namespace` field                                 |
| `.validate(...)`                  | `Validate` field                                  |
| `.onUploadComplete(...)`          | `OnUploadComplete` field                          |
| `.onUploadError(...)`             | `OnUploadError` field                             |
| `withRaildropPolicy(metadata, p)` | `raildrop.WithPolicy(metadata, policy)`           |

### 2. Mount the handler

```go
handler := raildrop.NewHandler(raildrop.HandlerConfig{
	Router:        router,
	Storage:       s3storage,                 // see below
	Secret:        os.Getenv("RAILDROP_SECRET"), // 32+ random chars
	PublicBaseURL: os.Getenv("RAILDROP_PUBLIC_BASE_URL"),
})

mux := http.NewServeMux()
mux.Handle("POST /api/upload", handler)
http.ListenAndServe(":3000", mux)
```

**Framework mounting.** Because `NewHandler` returns `http.Handler`, every mainstream
framework mounts it without an adapter:

```go
// net/http (Go 1.22+ patterns)
mux.Handle("POST /api/upload", handler)

// chi
r.Post("/api/upload", handler.ServeHTTP)

// gorilla/mux
r.Handle("/api/upload", handler).Methods(http.MethodPost)

// echo
e.POST("/api/upload", echo.WrapHandler(handler))

// gin
r.POST("/api/upload", gin.WrapH(handler))
```

**Fiber.** Fiber is fasthttp-based, so it gets a dedicated adapter in a separate module
(keeping fasthttp out of your dependency graph unless you use it):

```bash
go get github.com/akpor-kofi/raildrop/sdk/go/fiberadapter@latest
```

```go
import "github.com/akpor-kofi/raildrop/sdk/go/fiberadapter"

app := fiber.New()
fiberadapter.Register(app, "/api/upload/*", handler)
```

### 3. Wire up storage

```go
import "github.com/akpor-kofi/raildrop/sdk/go/s3store"

storage, err := s3store.NewRailwayBucketStorage(raildrop.BucketConfig{
	Bucket:         os.Getenv("RAILDROP_BUCKET"),
	Endpoint:       os.Getenv("RAILDROP_ENDPOINT"),
	Region:         "auto",
	AccessKeyID:    os.Getenv("RAILDROP_ACCESS_KEY_ID"),
	SecretAccessKey: os.Getenv("RAILDROP_SECRET_ACCESS_KEY"),
	ForcePathStyle: false,
}, nil)
```

Or read the environment directly (same variables as the TypeScript CLI, with Railway-style
fallbacks):

```go
storage, err := s3store.NewRailwayBucketStorage(must(s3store.BucketConfigFromEnv()), nil)
```

Anything implementing `raildrop.Storage` can back Raildrop — the interface mirrors the
TypeScript `RaildropStorage` (presign, multipart, head, get, put, delete, copy, list,
signed URLs). `s3store.RailwayBucketStorage` is the bundled S3-compatible implementation
(SigV4 presigned PUTs, presigned multipart parts, batch deletes, CORS management).

### 4. Upload from a browser (TypeScript client)

Go servers serve the same API the TypeScript browser/React/Expo clients already speak —
keep using `genUploader<UploadRouter>({ url: '/api/upload' })` from `raildrop/client` in
your frontend and point it at the Go handler.

### 5. Upload from Go (server-to-server)

```go
client := raildrop.NewClient(raildrop.ClientConfig{
	URL:            "https://api.example.com/api/upload",
	PartConcurrency: 3,
	RetryAttempts:  2,
})

uploaded, err := client.Upload(ctx, "avatar", raildrop.UploadOptions{
	Input: nil,
	Files: []raildrop.UploadFile{
		{Name: "report.pdf", Type: "application/pdf", Body: pdfBytes},
	},
	OnUploadProgress: func(name string, progress int) {
		log.Printf("%s: %d%%", name, progress)
	},
})
```

`Upload` performs prepare → presigned PUT (or concurrent multipart with retries) →
finalize, and returns the finalized file records including `serverData`.

## Public delivery and CDN

```go
gateway := raildrop.NewPublicGateway(raildrop.PublicGatewayConfig{
	Storage: storage,
	// Prefix: "/assets",
})
mux.Handle("/assets/", gateway)
```

Serves normalized `public/` objects with immutable caching, resolves checksum-addressed
`global/<alias>` requests through the manifest, answers conditional requests (`ETag`,
`If-Modified-Since`), supports `Range`, refuses private or expired objects, and forces
`attachment` for inline-unsafe types — matching the TypeScript gateway.

Private files stay behind your own authorization; hand out short-lived URLs with
`raildrop.API.GetSignedURL`.

## Server-side file management

```go
api := &raildrop.API{Storage: storage, PublicBaseURL: publicBaseURL}

result, _ := api.Upload(ctx, raildrop.ServerUpload{
	Body:      data,
	Name:      "invoice.pdf",
	Type:      "application/pdf",
	Namespace: []string{"orgs", "acme", "invoices"},
})
signed, _ := api.GetSignedURL(ctx, "private/orgs/acme/invoices/x/file.pdf", raildrop.SignedURLOptions{
	ExpiresIn:    300,
	DownloadName: "invoice.pdf",
})
api.HeadFile(ctx, key)
api.CopyFiles(ctx, sourceKey, destinationKey)
api.ListFiles(ctx, "public/orgs/acme/", "")
api.DeleteFiles(ctx, keys...)
```

## Maintenance

```go
result, _ := raildrop.CleanupExpiredObjects(ctx, storage, time.Now(), 0)
// {objectsDeleted, multipartUploadsAborted}

result, _ := raildrop.ReconcileUntrackedObjects(ctx, storage, isTracked, raildrop.ReconcileOptions{})
// deletes objects your database no longer references

synced, _ := raildrop.SyncGlobalAssets(ctx, storage, sources)   // replace manifest
upserted, _ := raildrop.UpsertGlobalAssets(ctx, storage, sources) // merge manifest
mirrored, _ := raildrop.MirrorGlobalAssets(ctx, targets)        // multi-bucket mirror
```

## CLI

```bash
go run github.com/akpor-kofi/raildrop/sdk/go/cmd/raildrop@latest <command>
```

Commands mirror the npm CLI: `health`, `cleanup`, `cors`, `cors:apply`,
`sync <manifest.json>`, `upsert <manifest.json>`, `mirror`. Manifest files are the same
JSON documents the TypeScript CLI consumes. Multi-environment credentials use the same
`RAILDROP_DEVELOPMENT_*` / `RAILDROP_STAGING_*` / `RAILDROP_PRODUCTION_*` groups.

## Environment

```
RAILDROP_BUCKET / BUCKET
RAILDROP_ENDPOINT / ENDPOINT
RAILDROP_REGION / REGION
RAILDROP_ACCESS_KEY_ID / ACCESS_KEY_ID
RAILDROP_SECRET_ACCESS_KEY / SECRET_ACCESS_KEY
RAILDROP_SECRET
RAILDROP_PUBLIC_BASE_URL
RAILDROP_ALLOWED_ORIGINS
RAILDROP_FORCE_PATH_STYLE
```

`RAILDROP_SECRET` must be an independent random secret with at least 32 characters and
must never reuse a bucket credential.

## Testing your integration

```go
import raildroptest "github.com/akpor-kofi/raildrop/sdk/go/testing"

harness := raildroptest.NewHarness(router, raildroptest.HarnessOptions{})
response := harness.Do(httptest.NewRequest(http.MethodPost, "/api/upload", body))
// harness.Storage.Objects holds every written object
```

`testing.MemoryStorage` is an in-memory `raildrop.Storage` (with range support) so routes
can be exercised without a bucket.

## TypeScript parity

| Surface                    | TypeScript entry     | Go                                     |
| -------------------------- | -------------------- | -------------------------------------- |
| Route builder              | `raildrop/server`    | `raildrop.DefineRoute`                 |
| HTTP handler               | `createRouteHandler` | `raildrop.NewHandler` (`http.Handler`) |
| Framework adapter          | `raildrop/hono`      | `fiberadapter` (net/http needs none)   |
| S3 storage                 | `RailwayBucketStorage` | `s3store.RailwayBucketStorage`       |
| Browser/React/Expo clients | `raildrop/client` etc. | TypeScript only (by design)          |
| Server-to-server uploads   | `raildrop.API.Upload`  | `raildrop.Client.Upload` / `raildrop.API` |
| Public gateway             | `createPublicGatewayHandler` | `raildrop.NewPublicGateway`     |
| Cleanup                    | `cleanupExpiredObjects` | `raildrop.CleanupExpiredObjects`     |
| Global assets              | `syncGlobalAssets` etc. | `raildrop.SyncGlobalAssets` etc.     |
| CLI                        | `raildrop <cmd>`     | `cmd/raildrop <cmd>`                   |
| Test utilities             | `raildrop/testing`   | `raildrop/testing`                     |

Deliberate differences:

- Resume state persistence (`localStorage`) is a browser concern and is not ported; the
  Go client retries parts in-process.
- NFKC name sanitization uses `golang.org/x/text` to match JavaScript exactly.
- The Go gateway runs behind `net/http`, which does not perform WHATWG URL normalization;
  traversal-encoded paths (`%2e%2e`) are rejected as invalid asset paths (400) instead of
  being resolved first.

## Wire-protocol conformance

Session tokens (`v1.iv.ciphertext.tag.signature` — AES-256-GCM + HMAC-SHA256, base64url)
are byte-compatible across SDKs. Golden fixtures in
[`test/fixtures/go-session-conformance.json`](../../test/fixtures/go-session-conformance.json)
are minted by each implementation and verified by the other's test suite
(`sdk/go/session_test.go` and `test/go-session-conformance.test.ts`). Regenerate after
touching the session format:

```bash
pnpm exec tsx scripts/generate-go-session-conformance.ts
```

## Versioning

This is a nested Go module inside the raildrop repository. Releases are git tags:

```bash
git tag sdk/go/v0.1.0 && git push origin sdk/go/v0.1.0
```

The module path (`github.com/akpor-kofi/raildrop/sdk/go`) matches the directory, so
`pkg.go.dev` indexes it automatically from the tag. Until v1, breaking changes bump the
minor version. The Fiber adapter is tagged separately as `sdk/go/fiberadapter/vX.Y.Z`.

## License

[MIT](../../LICENSE)
