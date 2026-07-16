# tusdfiber

**Native TUS resumable upload protocol for Go Fiber / fasthttp.**

[![Go Reference](https://pkg.go.dev/badge/github.com/maulanashalihin/tusdfiber.svg)](https://pkg.go.dev/github.com/maulanashalihin/tusdfiber)
[![Go Report Card](https://goreportcard.com/badge/github.com/maulanashalihin/tusdfiber)](https://goreportcard.com/report/github.com/maulanashalihin/tusdfiber)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

`tusdfiber` implements the [TUS resumable upload protocol v1](https://tus.io/protocols/resumable-upload) natively on Go Fiber — no `adaptor.HTTPHandler()` wrapper needed. All handlers accept `func(c *fiber.Ctx) error` directly.

Storage backends from [tusd](https://github.com/tus/tusd) (FileStore, S3, GCS, Azure) are fully compatible.

---

## Features

- ✅ **TUS v1** — POST (create), HEAD (offset), PATCH (upload chunk), GET (download), DELETE (terminate), OPTIONS (discovery)
- ✅ **Streaming body** — reads PATCH request body via fasthttp `RequestBodyStream()`, no full buffering
- ✅ **CORS** — configurable origin allowlist, credentials, headers
- ✅ **Concatenation** — partial & final uploads (`Upload-Concat`)
- ✅ **Deferred length** — uploads with unknown size (`Upload-Defer-Length`)
- ✅ **Notifications** — channels for complete, created, terminated, progress events
- ✅ **Callbacks** — pre-create, pre-finish, pre-terminate hooks
- ✅ **Locking** — file-based or memory-based lock coordination
- ✅ **Method override** — `X-HTTP-Method-Override` for PATCH/DELETE in restricted environments
- ✅ **No adaptor** — pure `func(c *fiber.Ctx) error`, no `http.Handler` bridge

---

## Installation

```bash
go get github.com/maulanashalihin/tusdfiber
```

You'll also need a storage backend. Install one from tusd:

```bash
go get github.com/tus/tusd/v2/pkg/filestore
go get github.com/tus/tusd/v2/pkg/filelocker
```

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

const file = document.querySelector('input[type=file]').files[0]

const upload = new tus.Upload(file, {
  endpoint: 'http://localhost:8080/files',
  metadata: {
    filename: file.name,
    filetype: file.type,
  },
  onError: (err) => console.error(err),
  onProgress: (bytesSent, bytesTotal) =>
    console.log(`${bytesSent}/${bytesTotal}`),
  onSuccess: () => console.log('Done!'),
})

upload.start()
```

---

## API

### `tusdfiber.NewHandler(config)`

Creates a routed TUS handler.

### `tusdfiber.NewUnroutedHandler(config)`

Creates an unrouted handler — you wire the methods yourself:

| Method | Fiber Handler | Description |
|--------|---------------|-------------|
| `PostFile` | `func(c *fiber.Ctx) error` | Create upload |
| `HeadFile` | `func(c *fiber.Ctx) error` | Get offset |
| `PatchFile` | `func(c *fiber.Ctx) error` | Upload chunk |
| `GetFile` | `func(c *fiber.Ctx) error` | Download |
| `DelFile` | `func(c *fiber.Ctx) error` | Terminate |
| `Options` | `func(c *fiber.Ctx) error` | Protocol discovery |

### `handler.Register(router)`

Registers all TUS routes on a Fiber router:

| Route | Method | Purpose |
|-------|--------|---------|
| `{BasePath}` | POST | Create upload |
| `{BasePath}` | OPTIONS | Protocol discovery |
| `{BasePath}:id` | HEAD | Get offset/info |
| `{BasePath}:id` | PATCH | Upload chunk |
| `{BasePath}:id` | GET | Download (if enabled) |
| `{BasePath}:id` | DELETE | Terminate (if enabled) |

### `tusdfiber.DefaultMiddlewareStack(corsConfig)`

Returns the standard middleware chain:

1. `MethodOverrideMiddleware()` — `X-HTTP-Method-Override` support
2. `CORSMiddleware(cfg)` — CORS headers & preflight
3. `TusResumableMiddleware()` — `Tus-Resumable: 1.0.0` validation

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

### CORS

```go
config := tusdfiber.Config{
    CORS: &tusdfiber.CORSConfig{
        AllowOrigin:      regexp.MustCompile(`^https://myapp\.com$`),
        AllowCredentials: true,
        MaxAge:           "3600",
    },
}
```

Pass `nil` to use the default (allow all origins).

---

## Storage Backends

All tusd data stores work out of the box:

| Store | Import | Description |
|-------|--------|-------------|
| **FileStore** | `github.com/tus/tusd/v2/pkg/filestore` | Local disk |
| **S3Store** | `github.com/tus/tusd/v2/pkg/s3store` | AWS S3 / MinIO |
| **GCSStore** | `github.com/tus/tusd/v2/pkg/gcsstore` | Google Cloud Storage |
| **AzureStore** | `github.com/tus/tusd/v2/pkg/azurestore` | Azure Blob Storage |

### S3 Example

```go
import (
    "github.com/aws/aws-sdk-go-v2/config"
    "github.com/aws/aws-sdk-go-v2/service/s3"
    "github.com/tus/tusd/v2/pkg/s3store"
)

cfg, _ := config.LoadDefaultConfig(ctx)
client := s3.NewFromConfig(cfg)

composer := tusdfiber.NewStoreComposer()
s3store.New("my-bucket", client).UseIn(composer.StoreComposer)
memorylocker.New().UseIn(composer.StoreComposer)
```

---

## Notifications

```go
handler, _ := tusdfiber.NewHandler(config)

go func() {
    for ev := range handler.CompleteUploads {
        // Upload finished
        log.Printf("Done: %s (%d bytes)", ev.Upload.ID, ev.Upload.Size)
    }
}()

go func() {
    for ev := range handler.CreatedUploads {
        // Upload created
        log.Printf("New: %s", ev.Upload.ID)
    }
}()

go func() {
    for ev := range handler.UploadProgress {
        // Progress update (requires NotifyUploadProgress: true)
        log.Printf("Progress %s: %d/%d", ev.Upload.ID, ev.Upload.Offset, ev.Upload.Size)
    }
}()
```

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
│  └────────────────────────────────────────────┘   │
│  ┌────────────────────────────────────────────┐   │
│  │  Handler.Register()                        │   │
│  │  POST   /files     → PostFile              │   │
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
│  │  └─ StoreComposer → tusd DataStore         │   │
│  └────────────────────────────────────────────┘   │
└──────────────────────────────────────────────────┘
```

---

## Comparison

| Feature | tusd (`net/http`) | tusdfiber (Fiber) |
|---------|:---:|:---:|
| Native Fiber handler | ❌ | ✅ |
| Streaming body | ✅ | ✅ |
| TUS v1 protocol | ✅ | ✅ |
| IETF draft protocol | ✅ | ❌ (planned) |
| Storage backends | ✅ FileStore, S3, GCS, Azure | ✅ (same, via import) |
| Hooks | ✅ File, HTTP, gRPC, Plugin | ✅ Callback-based |
| Metrics | ✅ Prometheus | ❌ (planned) |
| Dependencies | `net/http` only | Fiber + fasthttp |

---

## Roadmap

- [x] TUS v1 core (POST, HEAD, PATCH)
- [x] Download (GET)
- [x] Termination (DELETE)
- [x] CORS
- [x] Concatenation
- [x] Deferred length
- [ ] IETF Resumable Upload Draft (v2 protocol)
- [ ] Prometheus metrics
- [ ] Unit test coverage
- [ ] File / HTTP hook integrations

---

## License

MIT — see [LICENSE](LICENSE).

Built on top of [tusd](https://github.com/tus/tusd) by Transloadit and contributors.
