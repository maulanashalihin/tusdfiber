package tusdfiber

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v2"
)

// hookContext carries request-scoped state for the TUS handler.
// It wraps a Fiber context and provides cancellation with a grace delay.
type hookContext struct {
	context.Context
	fiberCtx   *fiber.Ctx
	cancel     context.CancelCauseFunc
	stopUpload func(HTTPResponse) // called to stop an upload mid-stream
}

// newHookContext builds a hookContext from a Fiber request.
func newHookContext(c *fiber.Ctx, gracefulTimeout time.Duration) *hookContext {
	reqCtx := c.Context()
	// The fasthttp request context is cancelled when the connection closes
	// or ServeHTTP returns.
	cancellable, cancel := context.WithCancelCause(reqCtx)

	// Delayed context: after the parent is cancelled, wait before really
	// cancelling so data stores can finish their work.
	delayed := newDelayedContext(cancellable, gracefulTimeout)

	return &hookContext{
		Context:  delayed,
		fiberCtx: c,
		cancel:   cancel,
	}
}

// newDelayedContext returns a context that cancels `delay` after the parent is done.
func newDelayedContext(parent context.Context, delay time.Duration) context.Context {
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	go func() {
		<-parent.Done()
		<-time.After(delay)
		cancel()
	}()
	return ctx
}
