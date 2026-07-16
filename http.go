package tusdfiber

import (
	"maps"
	"net/http"
	"strconv"

	tusd "github.com/tus/tusd/v2/pkg/handler"
)

// Type aliases for tusd types — users can use either package.
type (
	FileInfo       = tusd.FileInfo
	FileInfoChanges = tusd.FileInfoChanges
	MetaData       = tusd.MetaData
	Upload         = tusd.Upload
	DataStore      = tusd.DataStore
	Locker         = tusd.Locker
	Lock           = tusd.Lock
	TerminaterDataStore    = tusd.TerminaterDataStore
	TerminatableUpload     = tusd.TerminatableUpload
	ConcaterDataStore      = tusd.ConcaterDataStore
	ConcatableUpload       = tusd.ConcatableUpload
	LengthDeferrerDataStore = tusd.LengthDeferrerDataStore
	LengthDeclarableUpload = tusd.LengthDeclarableUpload
	ServableUpload         = tusd.ServableUpload
	ContentServerDataStore = tusd.ContentServerDataStore
)

// HTTPRequest contains basic details of an incoming HTTP request.
type HTTPRequest struct {
	Method     string
	URI        string
	RemoteAddr string
	Header     http.Header
}

// HTTPHeader is a map of HTTP header names to values.
type HTTPHeader map[string]string

// HTTPResponse contains basic details of an outgoing HTTP response.
type HTTPResponse struct {
	StatusCode int
	Body       string
	Header     HTTPHeader
}

// writeTo writes the HTTP response headers and body.
func (resp HTTPResponse) writeTo(w http.ResponseWriter) {
	headers := w.Header()
	for key, value := range resp.Header {
		headers.Set(key, value)
	}
	if len(resp.Body) > 0 {
		headers.Set("Content-Length", strconv.Itoa(len(resp.Body)))
	}
	w.WriteHeader(resp.StatusCode)
	if len(resp.Body) > 0 {
		w.Write([]byte(resp.Body))
	}
}

// MergeWith merges resp2 into a copy of resp1 (resp2 wins on conflicts).
func (resp1 HTTPResponse) MergeWith(resp2 HTTPResponse) HTTPResponse {
	newResp := resp1
	if resp2.StatusCode != 0 {
		newResp.StatusCode = resp2.StatusCode
	}
	if len(resp2.Body) > 0 {
		newResp.Body = resp2.Body
	}
	newResp.Header = make(HTTPHeader, len(resp1.Header)+len(resp2.Header))
	maps.Copy(newResp.Header, resp1.Header)
	maps.Copy(newResp.Header, resp2.Header)
	return newResp
}

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
