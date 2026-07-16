package tusdfiber

import (
	"bufio"
	"errors"
	"net"
	"net/http"

	"github.com/gofiber/fiber/v2"
)

// fiberResponseWriter adapts fiber.Ctx to implement http.ResponseWriter
// plus http.Flusher and http.Hijacker for compatibility with stores that
// use ServeFile or other net/http internals.
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
	for key, values := range w.header {
		for _, v := range values {
			w.ctx.Context().Response.Header.Set(key, v)
		}
	}
	w.ctx.Context().Response.SetStatusCode(statusCode)
}

// Flush implements http.Flusher — needed by http.ServeFile / Range requests.
func (w *fiberResponseWriter) Flush() {
	// fasthttp handles flushing automatically; no-op is safe.
}

// Hijack implements http.Hijacker — needed by some net/http internals.
func (w *fiberResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, errors.New("hijacking not supported")
}

// Interface checks.
var (
	_ http.ResponseWriter = (*fiberResponseWriter)(nil)
	_ http.Flusher        = (*fiberResponseWriter)(nil)
	_ http.Hijacker       = (*fiberResponseWriter)(nil)
)
