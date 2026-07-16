package tusdfiber

import (
	"net/http"

	"github.com/gofiber/fiber/v2"
)

// fiberResponseWriter adapts fiber.Ctx to implement http.ResponseWriter.
// Used when a data store's ServeContent method needs a standard http.ResponseWriter.
type fiberResponseWriter struct {
	ctx     *fiber.Ctx
	header  http.Header
	status  int
	written bool
}

func newFiberResponseWriter(c *fiber.Ctx) *fiberResponseWriter {
	return &fiberResponseWriter{
		ctx:    c,
		header: make(http.Header),
	}
}

func (w *fiberResponseWriter) Header() http.Header {
	return w.header
}

func (w *fiberResponseWriter) Write(b []byte) (int, error) {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}
	return w.ctx.Context().Response.BodyWriter().Write(b)
}

func (w *fiberResponseWriter) WriteHeader(statusCode int) {
	if w.written {
		return
	}
	w.written = true
	w.status = statusCode

	// Copy accumulated headers to fasthttp response
	for key, values := range w.header {
		for _, v := range values {
			w.ctx.Context().Response.Header.Set(key, v)
		}
	}
	w.ctx.Context().Response.SetStatusCode(statusCode)
}

// Ensure interface compliance.
var _ http.ResponseWriter = (*fiberResponseWriter)(nil)
