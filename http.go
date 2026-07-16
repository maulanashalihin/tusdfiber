package tusdfiber

import (
	"net/http"

	tusd "github.com/tus/tusd/v2/pkg/handler"
)

// All HTTP types are aliased from tusd for full compatibility.
type (
	HTTPRequest  = tusd.HTTPRequest
	HTTPHeader   = tusd.HTTPHeader
	HTTPResponse = tusd.HTTPResponse
)

// Type aliases for core tusd types.
type (
	FileInfo               = tusd.FileInfo
	FileInfoChanges        = tusd.FileInfoChanges
	MetaData               = tusd.MetaData
	Upload                 = tusd.Upload
	DataStore              = tusd.DataStore
	Locker                 = tusd.Locker
	Lock                   = tusd.Lock
	TerminaterDataStore    = tusd.TerminaterDataStore
	TerminatableUpload     = tusd.TerminatableUpload
	ConcaterDataStore      = tusd.ConcaterDataStore
	ConcatableUpload       = tusd.ConcatableUpload
	LengthDeferrerDataStore = tusd.LengthDeferrerDataStore
	LengthDeclarableUpload = tusd.LengthDeclarableUpload
	ServableUpload         = tusd.ServableUpload
	ContentServerDataStore = tusd.ContentServerDataStore
)

// HookEvent is an event from tusd that can be handled by the application.
type HookEvent struct {
	Upload      FileInfo
	HTTPRequest HTTPRequest
}

// newHookEvent builds a HookEvent from the current request context.
func newHookEvent(c *hookContext, info FileInfo) HookEvent {
	req := c.fiberCtx.Request()
	h := make(http.Header)
	req.Header.VisitAll(func(key, value []byte) {
		h.Set(string(key), string(value))
	})
	h.Set("Host", string(req.Header.Host()))

	return HookEvent{
		Upload: info,
		HTTPRequest: HTTPRequest{
			Method:     c.fiberCtx.Method(),
			URI:       string(req.RequestURI()),
			RemoteAddr: c.fiberCtx.IP(),
			Header:    h,
		},
	}
}
