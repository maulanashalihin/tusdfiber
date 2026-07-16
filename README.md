# tusdfiber

**Native TUS resumable upload protocol for Go Fiber / fasthttp.**

[![CI](https://github.com/maulanashalihin/tusdfiber/actions/workflows/ci.yml/badge.svg)](https://github.com/maulanashalihin/tusdfiber/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/maulanashalihin/tusdfiber.svg)](https://pkg.go.dev/github.com/maulanashalihin/tusdfiber)
[![Go Report Card](https://goreportcard.com/badge/github.com/maulanashalihin/tusdfiber)](https://goreportcard.com/report/github.com/maulanashalihin/tusdfiber)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

`tusdfiber` implements the [TUS resumable upload protocol](https://tus.io/protocols/resumable-upload) natively on Go Fiber — no `adaptor.HTTPHandler()` wrapper needed. All handlers accept `func(c *fiber.Ctx) error` directly.

Includes **TUS v1** and **IETF Resumable Upload Draft** (v2 protocol), **Prometheus metrics**, and full compatibility with [tusd](https://github.com/tus/tusd) storage backends (FileStore, S3, GCS, Azure) and hook systems (file, HTTP, gRPC).

---

## Features

- ✅ **TUS v1** — POST (create), HEAD (offset), PATCH (upload chunk), GET (download), DELETE (terminate), OPTIONS (discovery)
- ✅ **IETF Resumable Upload Draft (v2)** — `Upload-Draft-Interop-Version` 3–6, `Upload-Complete/Incomplete`, 104 Early Hints, `Upload-Limit`
- ✅ **Streaming body** — reads PATCH body via fasthttp `RequestBodyStream()`, no full buffering
- ✅ **CORS** — configurable origin allowlist, credentials, headers
- ✅ **Concatenation** — partial & final uploads (`Upload-Concat`)
- ✅ **Deferred length** — uploads with unknown size (`Upload-Defer-Length`)
- ✅ **Notifications** — channels for complete, created, terminated, progress events
- ✅ **Callbacks** — pre-create, pre-finish, pre-terminate hooks
- ✅ **Hooks system** — file, HTTP, gRPC hooks via tusd `HookHandler` interface
- ✅ **Prometheus metrics** — uploads created/finished/terminated, bytes, errors, active uploads
- ✅ **Locking** — file-based or memory-based lock coordination
- ✅ **Method override** — `X-HTTP-Method-Override` for PATCH/DELETE in restricted environments
- ✅ **No adaptor** — pure `func(c *fiber.Ctx) error`, no `http.Handler` bridge
- ✅ **51 unit tests** — CI on every push, including E2E example tests
- ✅ **E2E tested** — full TUS upload cycle tested: POST → HEAD → PATCH → HEAD → GET → DELETE

---

## Installation

```bash
go get github.com/maulanashalihin/tusdfiber
```

Storage backend and locker (both required):

```bash
go get github.com/tus/tusd/v2/pkg/filestore   # or: s3store, gcsstore, azurestore
go get github.com/tus/tusd/v2/pkg/filelocker  # always required
```

> **Note:** `filestore` is one of several storage backends — pick **one** store that fits your infra (`filestore` for local, `s3store` for S3, `gcsstore` for GCS, `azurestore` for Azure). `filelocker` is always needed regardless of which store you choose.

> **⚠️ Important:** Your Fiber app **must** set `StreamRequestBody: true` in `fiber.Config`. Without this, large uploads will hang because fasthttp tries to buffer the entire request body in memory instead of streaming it to the TUS handler. See Quick Start below.

> **⚠️ Notification channels:** If you enable `NotifyCompleteUploads`, `NotifyCreatedUploads`, or `NotifyTerminatedUploads`, you **must** drain the corresponding channel (`handler.CompleteUploads`, etc.) in a goroutine. These channels are unbuffered — sending blocks until someone reads. See the [example](example/main.go) for the drain pattern.

---

## Quick Start

```go
package main

import (
	"log"

	"github.com/gofiber/fiber/v2"
	"github.com/maulanashalihin/tusdfiber"
	"github.com/tus/tusd/v2/pkg/filelocker"
	"github.com/tus/tusd/v2/pkg/filestore"
)

func main() {
	app := fiber.New(fiber.Config{
		StreamRequestBody: true,
	})

	// ── Storage ──────────────────────────────────────────────
	store := filestore.New("./uploads")
	locker := filelocker.New("./uploads")

	composer := tusdfiber.NewStoreComposer()
	store.UseIn(composer.StoreComposer)
	locker.UseIn(composer.StoreComposer)

	// ── TUS Handler ───────────────────────────────────────────
	handler, err := tusdfiber.NewHandler(tusdfiber.Config{
		StoreComposer: composer,
		BasePath:      "/files/",
		MaxSize:       100 * 1024 * 1024, // 100 MB
	})
	if err != nil {
		log.Fatal(err)
	}

	// ── Middleware ────────────────────────────────────────────
	app.Use(tusdfiber.DefaultMiddlewareStack(nil)...)

	// ── Routes ───────────────────────────────────────────────
	handler.Register(app)

	// ── Events (optional) ────────────────────────────────────
	go func() {
		for ev := range handler.CompleteUploads {
			log.Printf("Upload selesai: %s (%d bytes)", ev.Upload.ID, ev.Upload.Size)
		}
	}()

	log.Fatal(app.Listen(":8080"))
}
```

### Client Example (using tus-js-client)

```js
import * as tus from 'tus-js-client'

const upload = new tus.Upload(file, {
  endpoint: 'http://localhost:8080/files',
  metadata: { filename: file.name, filetype: file.type },
  onError: (err) => console.error(err),
  onProgress: (bytesSent, bytesTotal) => console.log(`${bytesSent}/${bytesTotal}`),
  onSuccess: () => console.log('Done!'),
})
upload.start()
```

---

## IETF Resumable Upload Draft (v2)

Enable by setting `EnableExperimentalProtocol: true` in Config. The handler auto-detects
`Upload-Draft-Interop-Version` headers and routes to the v2 protocol implementation.

```go
handler, _ := tusdfiber.NewHandler(tusdfiber.Config{
    StoreComposer:              composer,
    BasePath:                   "/files/",
    EnableExperimentalProtocol: true, // ← enables v2 protocol
})
```

The v2 protocol supports:

| Header | Purpose |
|--------|---------|
| `Upload-Draft-Interop-Version` | Protocol version (`3`, `4`, `5`, `6`) |
| `Upload-Complete: ?1` / `Upload-Incomplete: ?0` | Upload completion signal |
| `Upload-Limit: min-size=0,max-size=N` | Server-enforced limits |
| `Content-Type: application/partial-upload` | Chunk content type (v4+) |

HEAD responses return `204 No Content` instead of `200 OK` for v2 requests.

> **Note:** The draft spec recommends sending `104 Early Hints` before processing
> the request body, but fasthttp does not support sending interim 1xx responses.
> Instead, all response headers (including Location) are sent in the final `201 Created` response.
> This has no impact on client compatibility — tus-js-client and other implementations
> handle both patterns correctly.

---

## Prometheus Metrics

```go
import "github.com/gofiber/fiber/v2/middleware/adaptor"

m := tusdfiber.NewMetrics(nil)

// Count requests & active uploads
app.Use(m.Middleware())

// Expose metrics endpoint
app.Get("/metrics", adaptor.HTTPHandler(tusdfiber.PromHTTPHandler()))
```

### Available Metrics

| Metric | Type | Labels |
|--------|------|--------|
| `tusdfiber_uploads_created_total` | Counter | — |
| `tusdfiber_uploads_finished_total` | Counter | — |
| `tusdfiber_uploads_terminated_total` | Counter | — |
| `tusdfiber_bytes_received_total` | Counter | — |
| `tusdfiber_errors_total` | CounterVec | `code` |
| `tusdfiber_requests_total` | CounterVec | `method` |
| `tusdfiber_active_uploads` | Gauge | — |
| `tusdfiber_hook_invocations_total` | CounterVec | `hooktype` |
| `tusdfiber_hook_errors_total` | CounterVec | `hooktype` |

---

## Hooks System

Use tusd's file, HTTP, or gRPC hooks with `NewHandlerWithHooks`:

### File Hooks

```go
import "github.com/tus/tusd/v2/pkg/hooks/file"

handler, err := tusdfiber.NewHandlerWithHooks(config, &file.FileHook{
    Directory: "./hooks",
}, tusdfiber.AvailableHooks)
```

### HTTP Hooks

```go
import "github.com/tus/tusd/v2/pkg/hooks/http"

handler, err := tusdfiber.NewHandlerWithHooks(config, &http.HttpHook{
    Endpoint: "https://example.com/hooks",
}, tusdfiber.AvailableHooks)
```

### gRPC Hooks

```go
import "github.com/tus/tusd/v2/pkg/hooks/grpc"

handler, err := tusdfiber.NewHandlerWithHooks(config, &grpc.GrpcHook{
    Endpoint: "localhost:50051",
}, tusdfiber.AvailableHooks)
```

### Available Hook Types

| Hook | When | Can Reject |
|------|------|:----------:|
| `HookPreCreate` | Before upload creation | ✅ |
| `HookPostCreate` | After upload creation | ❌ |
| `HookPostReceive` | During chunk upload | ✅ (stop) |
| `HookPreFinish` | Before upload completion | ❌ |
| `HookPostFinish` | After upload completion | ❌ |
| `HookPreTerminate` | Before upload termination | ✅ |
| `HookPostTerminate` | After upload termination | ❌ |

---

## API

### `tusdfiber.NewHandler(config)`

Creates a routed TUS handler (TUS v1 + auto-detection of v2 draft).

### `tusdfiber.NewHandlerWithHooks(config, hookHandler, enabledHooks)`

Creates a routed handler with tusd-compatible hook integration.

### `tusdfiber.NewUnroutedHandler(config)`

Creates an unrouted handler for manual route wiring:

| Method | Description |
|--------|-------------|
| `PostFile` | Create upload (routes to v2 if draft detected) |
| `HeadFile` | Get offset |
| `PatchFile` | Upload chunk |
| `GetFile` | Download |
| `DelFile` | Terminate |
| `Options` | Protocol discovery |

### `handler.Register(router)`

| Route | Method | Purpose |
|-------|--------|---------|
| `{BasePath}` | POST | Create upload |
| `{BasePath}` | OPTIONS | Protocol discovery |
| `{BasePath}:id` | HEAD | Get offset/info |
| `{BasePath}:id` | PATCH | Upload chunk |
| `{BasePath}:id` | GET | Download |
| `{BasePath}:id` | DELETE | Terminate |

### `tusdfiber.DefaultMiddlewareStack(corsConfig)`

```go
app.Use(tusdfiber.DefaultMiddlewareStack(nil)...)
```

1. `MethodOverrideMiddleware()` — `X-HTTP-Method-Override`
2. `CORSMiddleware(cfg)` — CORS headers & preflight
3. `TusResumableMiddleware()` — `Tus-Resumable: 1.0.0` / `Upload-Draft-Interop-Version`

---

## Configuration

```go
type Config struct {
    StoreComposer                 *StoreComposer
    MaxSize                       int64
    BasePath                      string
    DisableDownload               bool
    DisableTermination            bool
    DisableConcatenation          bool
    EnableExperimentalProtocol    bool  // v2 IETF draft
    NotifyCompleteUploads         bool
    NotifyTerminatedUploads       bool
    NotifyUploadProgress          bool
    NotifyCreatedUploads          bool
    UploadProgressInterval        time.Duration
    RespectForwardedHeaders       bool
    PreUploadCreateCallback       func(HookEvent) (HTTPResponse, FileInfoChanges, error)
    PreFinishResponseCallback     func(HookEvent) (HTTPResponse, error)
    PreUploadTerminateCallback    func(HookEvent) (HTTPResponse, error)
    GracefulRequestCompletionTimeout time.Duration
    AcquireLockTimeout            time.Duration
    NetworkTimeout                time.Duration
    CORS                          *CORSConfig
}
```

> **⚠️ Important:** When creating your Fiber app, always enable `StreamRequestBody: true`:
> ```go
> app := fiber.New(fiber.Config{
>     StreamRequestBody: true, // required for TUS
> })
> ```
> Without this, `fasthttp` buffers the entire request body in memory, causing large uploads to hang or exhaust server memory.

---

## Storage Backends

All tusd data stores work out of the box:

| Store | Import |
|-------|--------|
| **FileStore** | `github.com/tus/tusd/v2/pkg/filestore` |
| **S3Store** | `github.com/tus/tusd/v2/pkg/s3store` |
| **GCSStore** | `github.com/tus/tusd/v2/pkg/gcsstore` |
| **AzureStore** | `github.com/tus/tusd/v2/pkg/azurestore` |

---

## Comparison

| Feature | tusd (`net/http`) | tusdfiber (Fiber) |
|---------|:---:|:---:|
| Native Fiber handler | ❌ | ✅ |
| Streaming body | ✅ | ✅ |
| TUS v1 protocol | ✅ | ✅ |
| IETF draft protocol | ✅ | ✅ |
| Storage backends | ✅ | ✅ (same, via import) |
| File / HTTP / gRPC hooks | ✅ | ✅ (same, via import) |
| Prometheus metrics | ✅ | ✅ |
| Callback hooks | ✅ | ✅ |
| Unit tests | ✅ | ✅ (51 tests) |
| Dependencies | `net/http` only | Fiber + fasthttp |

---

## Architecture

```
┌──────────────────────────────────────────────────┐
│                  Fiber App                        │
│  ┌────────────────────────────────────────────┐   │
│  │  DefaultMiddlewareStack                     │   │
│  │  ├─ MethodOverrideMiddleware()              │   │
│  │  ├─ CORSMiddleware()                        │   │
│  │  └─ TusResumableMiddleware()                │   │
│  │  ┌─ MetricsMiddleware()         (optional)  │   │
│  └────────────────────────────────────────────┘   │
│  ┌────────────────────────────────────────────┐   │
│  │  Handler.Register()                        │   │
│  │  POST   /files     → PostFile / PostFileV2 │   │
│  │  HEAD   /files/:id → HeadFile              │   │
│  │  PATCH  /files/:id → PatchFile             │   │
│  │  GET    /files/:id → GetFile               │   │
│  │  DELETE /files/:id → DelFile               │   │
│  │  OPTIONS /files    → Options               │   │
│  └────────────────────────────────────────────┘   │
│  ┌────────────────────────────────────────────┐   │
│  │  UnroutedHandler                           │   │
│  │  ├─ BodyReader (fasthttp streaming)        │   │
│  │  ├─ Context  (delayed cancellation)        │   │
│  │  ├─ Hooks    (file/HTTP/gRPC via tusd)     │   │
│  │  └─ StoreComposer → tusd DataStore         │   │
│  └────────────────────────────────────────────┘   │
│  ┌────────────────────────────────────────────┐   │
│  │  /metrics  →  Prometheus                   │   │
│  └────────────────────────────────────────────┘   │
└──────────────────────────────────────────────────┘
```

---

## Testing

```bash
# All tests (unit + integration)
go test -v -count=1 -race ./...

# Just helper tests
go test -v -count=1 -run 'Test(Parse|Serialize|Validate|Body|Config|New|TUSError)' .

# Just HTTP integration tests
go test -v -count=1 -run 'TestHandler_' .

# Just E2E example tests (uses filestore, real TUS upload cycle)
go test -v -count=1 ./example/

```

Tests cover:

| Layer | File | Tests |
|-------|------|:-----:|
| **Helpers** — metadata, concat, upload length, content-type | `tusdfiber_test.go` | 20 |
| **Body reader** — streaming, limits, error handling | `tusdfiber_test.go` | 4 |
| **Config validation** — defaults, missing fields, path normalization | `tusdfiber_test.go` | 4 |
| **Handler integration** — POST, HEAD, PATCH, GET, DELETE via `fiber.Test()` | `handler_test.go` | 11 |
| **Draft protocol** — v2 OPTIONS with `Upload-Draft-Interop-Version` | `handler_test.go` | 1 |
| **E2E example** — full TUS cycle + chunked upload + OPTIONS | `example/example_test.go` | 3 |

---

## License

MIT — see [LICENSE](LICENSE).

Built on top of [tusd](https://github.com/tus/tusd) by Transloadit and contributors.
