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
		// Enable request body streaming — required for TUS PATCH requests
		StreamRequestBody: true,
		// Read buffer size for streaming
		ReadBufferSize: 64 * 1024,
	})

	// ─── Storage Setup ───────────────────────────────────────────────

	store := filestore.New("./uploads")
	locker := filelocker.New("./uploads")

	composer := tusdfiber.NewStoreComposer()
	// Use the underlying tusd StoreComposer for compatibility with existing stores
	store.UseIn(composer.StoreComposer)
	locker.UseIn(composer.StoreComposer)

	// ─── TUS Handler ─────────────────────────────────────────────────

	handler, err := tusdfiber.NewHandler(tusdfiber.Config{
		StoreComposer: composer,
		BasePath:      "/files/",
		MaxSize:       100 * 1024 * 1024, // 100MB

		// Enable notification channels
		NotifyCompleteUploads:  true,
		NotifyTerminatedUploads: true,
		NotifyCreatedUploads:    true,

		// Pre-create hook example: validate metadata
		PreUploadCreateCallback: func(hook tusdfiber.HookEvent) (tusdfiber.HTTPResponse, tusdfiber.FileInfoChanges, error) {
			log.Printf("Pre-create: upload=%s size=%d metadata=%v\n",
				hook.Upload.ID, hook.Upload.Size, hook.Upload.MetaData)
			return tusdfiber.HTTPResponse{}, tusdfiber.FileInfoChanges{}, nil
		},
	})
	if err != nil {
		log.Fatalf("Failed to create TUS handler: %s", err)
	}

	// ─── Middleware ───────────────────────────────────────────────────

	// Use default TUS middleware stack: MethodOverride → CORS → Tus-Resumable
	for _, mw := range tusdfiber.DefaultMiddlewareStack(nil) {
		app.Use(mw)
	}

	// ─── Register Routes ──────────────────────────────────────────────

	// Method 1: Auto-register all TUS routes
	handler.Register(app)

	// ─── Event Listeners (optional) ───────────────────────────────────

	go func() {
		for event := range handler.CompleteUploads {
			log.Printf("Upload completed: id=%s size=%d\n",
				event.Upload.ID, event.Upload.Size)
		}
	}()

	go func() {
		for event := range handler.CreatedUploads {
			log.Printf("Upload created: id=%s\n", event.Upload.ID)
		}
	}()

	// ─── Health Check ────────────────────────────────────────────────

	app.Get("/health", func(c *fiber.Ctx) error {
		return c.SendString("OK")
	})

	// ─── Start Server ────────────────────────────────────────────────

	log.Println("TUS server starting on :8080")
	log.Println("Upload endpoint: http://localhost:8080/files")
	log.Println("Supported extensions:", handler.UnroutedHandler.Extensions())

	log.Fatal(app.Listen(":8080"))
}
