package tusdfiber

import (
	"fmt"
	"slices"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/tus/tusd/v2/pkg/handler"
	tusdhooks "github.com/tus/tusd/v2/pkg/hooks"
	"golang.org/x/exp/slog"
)

// Re-export tusd hook types for convenience.
type (
	HookType      = tusdhooks.HookType
	HookRequest   = tusdhooks.HookRequest
	HookResponse  = tusdhooks.HookResponse
	HookHandler   = tusdhooks.HookHandler
)

// Standard hook types.
const (
	HookPostFinish    HookType = "post-finish"
	HookPostTerminate HookType = "post-terminate"
	HookPostReceive   HookType = "post-receive"
	HookPostCreate    HookType = "post-create"
	HookPreCreate     HookType = "pre-create"
	HookPreFinish     HookType = "pre-finish"
	HookPreTerminate  HookType = "pre-terminate"
)

// AvailableHooks lists all supported hook types.
var AvailableHooks = []HookType{
	HookPreCreate, HookPostCreate, HookPostReceive,
	HookPreTerminate, HookPostTerminate, HookPostFinish, HookPreFinish,
}

// NewHandlerWithHooks creates a TUS Fiber handler whose callbacks and
// notification channels are wired to invoke the given HookHandler.
// It accepts the same Config as NewHandler.
//
// This lets you use tusd's file hooks, HTTP hooks, gRPC hooks, or plugin hooks.
//
// Example (file hooks):
//
//	hookHandler := &filehook.FileHook{Directory: "./hooks"}
//	handler, err := tusdfiber.NewHandlerWithHooks(config, hookHandler, tusdfiber.AvailableHooks)
func NewHandlerWithHooks(cfg Config, hookHandler HookHandler, enabledHooks []HookType) (*Handler, error) {
	if err := hookHandler.Setup(); err != nil {
		return nil, fmt.Errorf("unable to setup hooks: %s", err)
	}

	// Enable notifications for post-* hooks
	cfg.NotifyCompleteUploads = cfg.NotifyCompleteUploads || slices.Contains(enabledHooks, HookPostFinish)
	cfg.NotifyTerminatedUploads = cfg.NotifyTerminatedUploads || slices.Contains(enabledHooks, HookPostTerminate)
	cfg.NotifyUploadProgress = cfg.NotifyUploadProgress || slices.Contains(enabledHooks, HookPostReceive)
	cfg.NotifyCreatedUploads = cfg.NotifyCreatedUploads || slices.Contains(enabledHooks, HookPostCreate)

	// Install callbacks for pre-* hooks
	if slices.Contains(enabledHooks, HookPreCreate) && cfg.PreUploadCreateCallback == nil {
		cfg.PreUploadCreateCallback = func(hook HookEvent) (HTTPResponse, FileInfoChanges, error) {
			return preCreateHook(hook, hookHandler)
		}
	}
	if slices.Contains(enabledHooks, HookPreFinish) && cfg.PreFinishResponseCallback == nil {
		cfg.PreFinishResponseCallback = func(hook HookEvent) (HTTPResponse, error) {
			return preFinishHook(hook, hookHandler)
		}
	}
	if slices.Contains(enabledHooks, HookPreTerminate) && cfg.PreUploadTerminateCallback == nil {
		cfg.PreUploadTerminateCallback = func(hook HookEvent) (HTTPResponse, error) {
			return preTerminateHook(hook, hookHandler)
		}
	}

	// Create handler
	h, err := NewHandler(cfg)
	if err != nil {
		return nil, err
	}

	// Listen for notification-based hooks (post-*)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case event, ok := <-h.CompleteUploads:
				if !ok {
					return
				}
				invokeHookAsync(HookPostFinish, event, hookHandler)
			case event, ok := <-h.TerminatedUploads:
				if !ok {
					return
				}
				invokeHookAsync(HookPostTerminate, event, hookHandler)
			case event, ok := <-h.CreatedUploads:
				if !ok {
					return
				}
				invokeHookAsync(HookPostCreate, event, hookHandler)
			case event, ok := <-h.UploadProgress:
				if !ok {
					return
				}
				postReceiveHook(event, hookHandler)
			}
		}
	}()

	return h, nil
}

// ---------------------------------------------------------------------------
// Internal hook callbacks
// ---------------------------------------------------------------------------

var (
	metricsHookErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tusdfiber_hook_errors_total",
			Help: "Total number of execution errors per hook type.",
		},
		[]string{"hooktype"},
	)
	metricsHookInvocationsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tusdfiber_hook_invocations_total",
			Help: "Total number of invocations per hook type.",
		},
		[]string{"hooktype"},
	)

	hookMetricsOnce sync.Once
)

func ensureHookMetrics() {
	hookMetricsOnce.Do(func() {
		for _, t := range AvailableHooks {
			metricsHookErrorsTotal.WithLabelValues(string(t)).Add(0)
			metricsHookInvocationsTotal.WithLabelValues(string(t)).Add(0)
		}
	})
}

func preCreateHook(event HookEvent, hh HookHandler) (HTTPResponse, FileInfoChanges, error) {
	ensureHookMetrics()
	metricsHookInvocationsTotal.WithLabelValues(string(HookPreCreate)).Add(1)

	res, err := hh.InvokeHook(HookRequest{
		Type:  HookPreCreate,
		Event: convertEvent(event),
	})
	if err != nil {
		slog.Error("HookInvocationError", "type", HookPreCreate, "id", event.Upload.ID, "error", err.Error())
		metricsHookErrorsTotal.WithLabelValues(string(HookPreCreate)).Add(1)
		return HTTPResponse{}, FileInfoChanges{}, err
	}

	if res.RejectUpload {
		e := handler.ErrUploadRejectedByServer
		e.HTTPResponse = e.HTTPResponse.MergeWith(res.HTTPResponse)
		return HTTPResponse{}, FileInfoChanges{}, e
	}

	return res.HTTPResponse, res.ChangeFileInfo, nil
}

func preFinishHook(event HookEvent, hh HookHandler) (HTTPResponse, error) {
	ensureHookMetrics()
	metricsHookInvocationsTotal.WithLabelValues(string(HookPreFinish)).Add(1)

	res, err := hh.InvokeHook(HookRequest{
		Type:  HookPreFinish,
		Event: convertEvent(event),
	})
	if err != nil {
		slog.Error("HookInvocationError", "type", HookPreFinish, "id", event.Upload.ID, "error", err.Error())
		metricsHookErrorsTotal.WithLabelValues(string(HookPreFinish)).Add(1)
		return HTTPResponse{}, err
	}

	return res.HTTPResponse, nil
}

func preTerminateHook(event HookEvent, hh HookHandler) (HTTPResponse, error) {
	ensureHookMetrics()
	metricsHookInvocationsTotal.WithLabelValues(string(HookPreTerminate)).Add(1)

	res, err := hh.InvokeHook(HookRequest{
		Type:  HookPreTerminate,
		Event: convertEvent(event),
	})
	if err != nil {
		slog.Error("HookInvocationError", "type", HookPreTerminate, "id", event.Upload.ID, "error", err.Error())
		metricsHookErrorsTotal.WithLabelValues(string(HookPreTerminate)).Add(1)
		return HTTPResponse{}, err
	}

	if res.RejectTermination {
		e := handler.ErrUploadTerminationRejected
		e.HTTPResponse = e.HTTPResponse.MergeWith(res.HTTPResponse)
		return HTTPResponse{}, e
	}

	return res.HTTPResponse, nil
}

func postReceiveHook(event HookEvent, hh HookHandler) {
	ensureHookMetrics()
	metricsHookInvocationsTotal.WithLabelValues(string(HookPostReceive)).Add(1)

	res, err := hh.InvokeHook(HookRequest{
		Type:  HookPostReceive,
		Event: convertEvent(event),
	})
	if err != nil {
		slog.Error("HookInvocationError", "type", HookPostReceive, "id", event.Upload.ID, "error", err.Error())
		metricsHookErrorsTotal.WithLabelValues(string(HookPostReceive)).Add(1)
		return
	}

	if res.StopUpload {
		slog.Info("HookStopUpload", "id", event.Upload.ID)
		triggerStopUpload(event.Upload.ID, res.HTTPResponse)
	}
}

func invokeHookAsync(typ HookType, event HookEvent, hh HookHandler) {
	go func() {
		ensureHookMetrics()
		metricsHookInvocationsTotal.WithLabelValues(string(typ)).Add(1)

		_, err := hh.InvokeHook(HookRequest{
			Type:  typ,
			Event: convertEvent(event),
		})
		if err != nil {
			slog.Error("HookInvocationError", "type", typ, "id", event.Upload.ID, "error", err.Error())
			metricsHookErrorsTotal.WithLabelValues(string(typ)).Add(1)
		}
	}()
}

// convertEvent converts our HookEvent to tusd's handler.HookEvent for hook compatibility.
func convertEvent(ev HookEvent) handler.HookEvent {
	return handler.HookEvent{
		Upload:      ev.Upload,
		HTTPRequest: ev.HTTPRequest,
	}
}

// ── Upload stop callback registry ────────────────────────────────────
// Allows postReceive hooks to stop an active upload.

var stopCallbacks sync.Map

// RegisterStopCallback stores a callback that can stop an upload mid-stream.
// The callback is invoked when a hook returns StopUpload: true.
func RegisterStopCallback(id string, fn func(HTTPResponse)) {
	stopCallbacks.Store(id, fn)
}

// UnregisterStopCallback removes a previously registered stop callback.
func UnregisterStopCallback(id string) {
	stopCallbacks.Delete(id)
}

// triggerStopUpload invokes the registered stop callback for the given upload ID.
func triggerStopUpload(id string, resp handler.HTTPResponse) {
	if val, ok := stopCallbacks.Load(id); ok {
		if fn, ok := val.(func(HTTPResponse)); ok {
			fn(resp)
		}
	}
}

// Ensure tusd types are imported (for the convertEvent above).
var _ = handler.HookEvent{}
