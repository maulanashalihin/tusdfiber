package tusdfiber

import (
	"github.com/gofiber/fiber/v2"
)

// Handler wraps an UnroutedHandler with Fiber-native route registration.
// Create one via NewHandler, then call Register() or use Middleware().
type Handler struct {
	*UnroutedHandler
	config Config
}

// NewHandler creates a TUS Handler that auto-registers all TUS protocol routes
// on a Fiber router when you call Register().
func NewHandler(config Config) (*Handler, error) {
	uh, err := NewUnroutedHandler(config)
	if err != nil {
		return nil, err
	}
	return &Handler{
		UnroutedHandler: uh,
		config:          config,
	}, nil
}

// Register mounts all TUS protocol endpoints on the given Fiber router
// (app, group, or custom router) at the configured BasePath.
//
// Example:
//
//	app := fiber.New()
//	handler, _ := tusdfiber.NewHandler(config)
//	handler.Register(app)   // routes under /files/
func (h *Handler) Register(base fiber.Router) {
	bp := h.config.BasePath

	// Strip trailing slash for route registration consistency
	bpTrimmed := bp
	if len(bpTrimmed) > 1 && bpTrimmed[len(bpTrimmed)-1] == '/' {
		bpTrimmed = bpTrimmed[:len(bpTrimmed)-1]
	}

	// Base path without trailing slash: for POST (creation) and OPTIONS
	base.Post(bpTrimmed, h.PostFile)
	base.Head(bpTrimmed+"/:id", h.HeadFile)
	base.Patch(bpTrimmed+"/:id", h.PatchFile)
	base.Options(bpTrimmed, h.Options)

	// GET (download) — optional
	if !h.config.DisableDownload {
		base.Get(bpTrimmed+"/:id", h.GetFile)
	}

	// DELETE (termination) — optional
	if !h.config.DisableTermination && h.composer.UsesTerminater {
		base.Delete(bpTrimmed+"/:id", h.DelFile)
	}
}

// Middleware returns a Fiber handler that routes TUS requests internally.
// It is a convenience for cases where you want to mount it on a sub-router:
//
//	sub := app.Group("/files")
//	sub.Use("/", handler.Middleware())
func (h *Handler) Middleware() fiber.Handler {
	bp := h.config.BasePath
	bpTrimmed := bp
	if len(bpTrimmed) > 1 && bpTrimmed[len(bpTrimmed)-1] == '/' {
		bpTrimmed = bpTrimmed[:len(bpTrimmed)-1]
	}

	// Strip trailing slash for route matching
	prefix := bpTrimmed

	return func(c *fiber.Ctx) error {
		path := c.Path()
		method := c.Method()

		// Strip prefix
		if len(path) < len(prefix) || path[:len(prefix)] != prefix {
			return c.Next()
		}
		remainder := path[len(prefix):]

		// Root endpoint: POST or OPTIONS
		if remainder == "" || remainder == "/" {
			switch method {
			case fiber.MethodPost:
				return h.PostFile(c)
			case fiber.MethodOptions:
				return h.Options(c)
			default:
				return c.SendStatus(fiber.StatusMethodNotAllowed)
			}
		}

		// Resource endpoint: /:id or /:id/
		id := remainder
		if len(id) > 0 && id[0] == '/' {
			id = id[1:]
		}
		if len(id) > 0 && id[len(id)-1] == '/' {
			id = id[:len(id)-1]
		}
		if id == "" {
			return c.SendStatus(fiber.StatusNotFound)
		}

		// Store ID in locals for downstream handler methods
		c.Locals("id", id)

		switch method {
		case fiber.MethodHead:
			return h.HeadFile(c)
		case fiber.MethodPatch:
			return h.PatchFile(c)
		case fiber.MethodGet:
			if h.config.DisableDownload {
				return c.SendStatus(fiber.StatusMethodNotAllowed)
			}
			return h.GetFile(c)
		case fiber.MethodDelete:
			if h.config.DisableTermination || !h.composer.UsesTerminater {
				return c.SendStatus(fiber.StatusMethodNotAllowed)
			}
			return h.DelFile(c)
		default:
			return c.SendStatus(fiber.StatusMethodNotAllowed)
		}
	}
}

// DefaultMiddlewareStack returns the standard TUS middleware chain:
// MethodOverride → CORS → TusResumable.
// You can use this if you need to compose the middleware manually.
func DefaultMiddlewareStack(cfg *CORSConfig) []fiber.Handler {
	return []fiber.Handler{
		MethodOverrideMiddleware(),
		CORSMiddleware(cfg),
		TusResumableMiddleware(),
	}
}
