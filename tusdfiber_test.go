package tusdfiber

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/prometheus/client_golang/prometheus"
)

// ---------------------------------------------------------------------------
// parseMetadataHeader
// ---------------------------------------------------------------------------

func TestParseMetadataHeader_Empty(t *testing.T) {
	m := parseMetadataHeader("")
	if len(m) != 0 {
		t.Fatalf("expected empty map, got %v", m)
	}
}

func TestParseMetadataHeader_Single(t *testing.T) {
	m := parseMetadataHeader("filename aGVsbG8=") // "hello" in base64
	if m["filename"] != "hello" {
		t.Fatalf("expected 'hello', got %q", m["filename"])
	}
}

func TestParseMetadataHeader_Multiple(t *testing.T) {
	m := parseMetadataHeader("name aGVsbG8=,type dGV4dC9wbGFpbg==")
	if m["name"] != "hello" {
		t.Fatalf("expected 'hello', got %q", m["name"])
	}
	if m["type"] != "text/plain" {
		t.Fatalf("expected 'text/plain', got %q", m["type"])
	}
}

func TestParseMetadataHeader_InvalidBase64(t *testing.T) {
	m := parseMetadataHeader("name !!!invalid@@@")
	if _, ok := m["name"]; ok {
		t.Fatal("expected no entry for invalid base64 value")
	}
}

func TestParseMetadataHeader_NoValue(t *testing.T) {
	m := parseMetadataHeader("name")
	if m["name"] != "" {
		t.Fatalf("expected empty value, got %q", m["name"])
	}
}

// ---------------------------------------------------------------------------
// serializeMetadataHeader
// ---------------------------------------------------------------------------

func TestSerializeMetadataHeader_Empty(t *testing.T) {
	s := serializeMetadataHeader(nil)
	if s != "" {
		t.Fatalf("expected empty string, got %q", s)
	}
}

func TestSerializeMetadataHeader_Roundtrip(t *testing.T) {
	original := map[string]string{"filename": "test.txt", "type": "image/png"}
	s := serializeMetadataHeader(original)
	m := parseMetadataHeader(s)
	if m["filename"] != "test.txt" {
		t.Fatalf("expected 'test.txt', got %q", m["filename"])
	}
	if m["type"] != "image/png" {
		t.Fatalf("expected 'image/png', got %q", m["type"])
	}
}

// ---------------------------------------------------------------------------
// parseConcat
// ---------------------------------------------------------------------------

func TestParseConcat_Empty(t *testing.T) {
	p, f, ids, err := parseConcat("", "/files/")
	if p || f || len(ids) != 0 || err != nil {
		t.Fatalf("expected empty result, got partial=%v final=%v ids=%v err=%v", p, f, ids, err)
	}
}

func TestParseConcat_Partial(t *testing.T) {
	p, f, ids, err := parseConcat("partial", "/files/")
	if !p || f || len(ids) != 0 || err != nil {
		t.Fatalf("expected partial=true, got partial=%v final=%v ids=%v err=%v", p, f, ids, err)
	}
}

func TestParseConcat_Final(t *testing.T) {
	p, f, ids, err := parseConcat("final;http://example.com/files/a /files/b", "/files/")
	if p || !f || err != nil {
		t.Fatalf("expected final=true, got partial=%v final=%v err=%v", p, f, err)
	}
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("expected [a b], got %v", ids)
	}
}

func TestParseConcat_FinalWithBasePath(t *testing.T) {
	p, f, ids, err := parseConcat("final;/base/files/x /base/files/y", "/base/files/")
	if p || !f || err != nil {
		t.Fatalf("expected final=true, got partial=%v final=%v err=%v", p, f, err)
	}
	if len(ids) != 2 || ids[0] != "x" || ids[1] != "y" {
		t.Fatalf("expected [x y], got %v", ids)
	}
}

// ---------------------------------------------------------------------------
// validateNewUploadLength
// ---------------------------------------------------------------------------

func TestValidateNewUploadLength_Normal(t *testing.T) {
	size, deferred, err := validateNewUploadLength("100", "", false)
	if err != nil || size != 100 || deferred {
		t.Fatalf("expected size=100 deferred=false, got size=%d deferred=%v err=%v", size, deferred, err)
	}
}

func TestValidateNewUploadLength_Deferred(t *testing.T) {
	// supportsDefer=true is required for deferred length to work
	size, deferred, err := validateNewUploadLength("", "1", true)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !deferred {
		t.Fatal("expected deferred=true")
	}
	if size != 0 {
		t.Fatalf("expected size=0, got %d", size)
	}
}

func TestValidateNewUploadLength_DeferredNotSupported(t *testing.T) {
	_, _, err := validateNewUploadLength("", "1", false)
	// With supportsDefer=false, deferred without implementation returns ErrNotImplemented
	if err == nil || !strings.Contains(err.Error(), "implemented") {
		t.Fatalf("expected NotImplemented error, got %v", err)
	}
}

func TestValidateNewUploadLength_BothSet(t *testing.T) {
	_, _, err := validateNewUploadLength("100", "1", true)
	if err == nil || !strings.Contains(err.Error(), "both") {
		t.Fatalf("expected error about both headers, got %v", err)
	}
}

func TestValidateNewUploadLength_InvalidDefer(t *testing.T) {
	_, _, err := validateNewUploadLength("", "2", true)
	if err == nil || !strings.Contains(err.Error(), "Upload-Defer-Length") {
		t.Fatalf("expected error about invalid defer, got %v", err)
	}
}

func TestValidateNewUploadLength_InvalidSize(t *testing.T) {
	_, _, err := validateNewUploadLength("-1", "", false)
	if err == nil || !strings.Contains(err.Error(), "Upload-Length") {
		t.Fatalf("expected error about invalid length, got %v", err)
	}
}

func TestValidateNewUploadLength_Empty(t *testing.T) {
	_, _, err := validateNewUploadLength("", "", false)
	if err == nil {
		t.Fatal("expected error for empty upload length")
	}
}

// ---------------------------------------------------------------------------
// validateUploadID
// ---------------------------------------------------------------------------

func TestValidateUploadID_Empty(t *testing.T) {
	if err := validateUploadID(""); err != nil {
		t.Fatalf("expected no error for empty id, got %v", err)
	}
}

func TestValidateUploadID_Valid(t *testing.T) {
	if err := validateUploadID("abc123"); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestValidateUploadID_LeadingSlash(t *testing.T) {
	if err := validateUploadID("/abc"); err == nil {
		t.Fatal("expected error for leading slash")
	}
}

func TestValidateUploadID_TrailingSlash(t *testing.T) {
	if err := validateUploadID("abc/"); err == nil {
		t.Fatal("expected error for trailing slash")
	}
}

// ---------------------------------------------------------------------------
// filterContentType
// ---------------------------------------------------------------------------

func TestFilterContentType_PlainText(t *testing.T) {
	info := FileInfo{MetaData: MetaData{"filetype": "text/plain", "filename": "readme.txt"}}
	ct, cd := filterContentType(info)
	if ct != "text/plain" {
		t.Fatalf("expected 'text/plain', got %q", ct)
	}
	if !strings.Contains(cd, "inline") {
		t.Fatalf("expected inline disposition, got %q", cd)
	}
	if !strings.Contains(cd, "readme.txt") {
		t.Fatalf("expected filename in disposition, got %q", cd)
	}
}

func TestFilterContentType_HTML(t *testing.T) {
	info := FileInfo{MetaData: MetaData{"filetype": "text/html"}}
	_, cd := filterContentType(info)
	if !strings.Contains(cd, "attachment") {
		t.Fatalf("expected attachment for html, got %q", cd)
	}
}

func TestFilterContentType_NoMeta(t *testing.T) {
	info := FileInfo{}
	ct, cd := filterContentType(info)
	if ct != "application/octet-stream" {
		t.Fatalf("expected application/octet-stream, got %q", ct)
	}
	if !strings.Contains(cd, "attachment") {
		t.Fatalf("expected attachment, got %q", cd)
	}
}

// ---------------------------------------------------------------------------
// bodyReader
// ---------------------------------------------------------------------------

func TestBodyReader_ReadSmall(t *testing.T) {
	r := newBodyReader(io.NopCloser(strings.NewReader("hello")), 100)
	buf := make([]byte, 5)

	// First read: data available, no error
	n, err := r.Read(buf)
	if err != nil {
		t.Fatalf("expected no error on first read, got %v", err)
	}
	if n != 5 || string(buf[:n]) != "hello" {
		t.Fatalf("expected 'hello', got %q", string(buf[:n]))
	}

	// Second read: EOF
	n, err = r.Read(buf)
	if err != io.EOF {
		t.Fatalf("expected EOF on second read, got %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 bytes on second read, got %d", n)
	}
}

func TestBodyReader_HasErrorNone(t *testing.T) {
	r := newBodyReader(io.NopCloser(strings.NewReader("data")), 100)
	buf := make([]byte, 10)
	r.Read(buf)
	if err := r.hasError(); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestBodyReader_BytesRead(t *testing.T) {
	r := newBodyReader(io.NopCloser(strings.NewReader("1234567890")), 100)
	buf := make([]byte, 5)
	r.Read(buf)
	if n := r.bytesRead(); n != 5 {
		t.Fatalf("expected 5 bytes read, got %d", n)
	}
	r.Read(buf)
	if n := r.bytesRead(); n != 10 {
		t.Fatalf("expected 10 bytes read, got %d", n)
	}
}

func TestBodyReader_Limit(t *testing.T) {
	r := newBodyReader(io.NopCloser(strings.NewReader("1234567890")), 3)
	buf := make([]byte, 10)

	// First read: limited to 3 bytes
	n, err := r.Read(buf)
	if n != 3 {
		t.Fatalf("expected 3 bytes, got %d", n)
	}
	if err != nil {
		t.Fatalf("expected no error on first read, got %v", err)
	}
	if string(buf[:n]) != "123" {
		t.Fatalf("expected '123', got %q", string(buf[:n]))
	}

	// Second read: EOF from limit reader
	n, err = r.Read(buf)
	if err != io.EOF {
		t.Fatalf("expected EOF after limit, got %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 bytes, got %d", n)
	}
}

// ---------------------------------------------------------------------------
// Config validation
// ---------------------------------------------------------------------------

func TestConfigValidate_Defaults(t *testing.T) {
	composer := NewStoreComposer()
	composer.Core = &mockDataStore{}

	cfg := Config{
		StoreComposer: composer,
		BasePath:      "/files/",
	}

	if err := cfg.validate(); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if cfg.BasePath != "/files/" {
		t.Fatalf("expected /files/, got %q", cfg.BasePath)
	}
}

func TestConfigValidate_AddsSlash(t *testing.T) {
	composer := NewStoreComposer()
	composer.Core = &mockDataStore{}

	cfg := Config{
		StoreComposer: composer,
		BasePath:      "/files",
	}

	if err := cfg.validate(); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if cfg.BasePath != "/files/" {
		t.Fatalf("expected /files/, got %q", cfg.BasePath)
	}
}

func TestConfigValidate_MissingStore(t *testing.T) {
	cfg := Config{BasePath: "/files/"}
	if err := cfg.validate(); err == nil {
		t.Fatal("expected error for nil StoreComposer")
	}
}

func TestConfigValidate_MissingCore(t *testing.T) {
	cfg := Config{
		StoreComposer: NewStoreComposer(),
		BasePath:      "/files/",
	}
	if err := cfg.validate(); err == nil {
		t.Fatal("expected error for nil core data store")
	}
}

// ---------------------------------------------------------------------------
// NewUnroutedHandler
// ---------------------------------------------------------------------------

func TestNewUnroutedHandler_Extensions(t *testing.T) {
	h := newTestHandler(t)
	if h.extensions == "" {
		t.Fatal("expected non-empty extensions string")
	}
	if !strings.Contains(h.extensions, "creation") {
		t.Fatalf("expected 'creation' in extensions, got %q", h.extensions)
	}
}

func TestNewUnroutedHandler_ExtensionTermination(t *testing.T) {
	composer := NewStoreComposer()
	composer.Core = &mockDataStore{}
	composer.Terminater = &mockTerminater{}
	composer.UsesTerminater = true

	h, err := NewUnroutedHandler(Config{
		StoreComposer: composer,
		BasePath:      "/files/",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(h.extensions, "termination") {
		t.Fatalf("expected 'termination' in extensions, got %q", h.extensions)
	}
}

// ---------------------------------------------------------------------------
// TUSError
// ---------------------------------------------------------------------------

func TestTUSError_StatusCode(t *testing.T) {
	err := ErrNotFound
	if err.HTTPResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", err.HTTPResponse.StatusCode)
	}
}

func TestTUSError_Message(t *testing.T) {
	err := ErrInvalidOffset
	if !strings.Contains(err.Error(), "Upload-Offset") {
		t.Fatalf("expected Upload-Offset in error, got %q", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newTestHandler(t *testing.T) *UnroutedHandler {
	t.Helper()
	composer := NewStoreComposer()
	composer.Core = &mockDataStore{}

	h, err := NewUnroutedHandler(Config{
		StoreComposer: composer,
		BasePath:      "/files/",
	})
	if err != nil {
		t.Fatalf("failed to create handler: %v", err)
	}
	return h
}

// mockDataStore implements DataStore minimally for testing.
type mockDataStore struct{}

func (m *mockDataStore) NewUpload(ctx context.Context, info FileInfo) (Upload, error) {
	_ = ctx
	_ = info
	return &mockUpload{}, nil
}

func (m *mockDataStore) GetUpload(ctx context.Context, id string) (Upload, error) {
	_ = ctx
	return &mockUpload{}, nil
}

type mockUpload struct{}

func (m *mockUpload) WriteChunk(ctx context.Context, offset int64, src io.Reader) (int64, error) {
	_ = ctx
	_ = offset
	return io.Copy(io.Discard, src)
}

func (m *mockUpload) GetInfo(ctx context.Context) (FileInfo, error) {
	_ = ctx
	return FileInfo{ID: "test-id", Size: 100, Offset: 0}, nil
}

func (m *mockUpload) GetReader(ctx context.Context) (io.ReadCloser, error) {
	_ = ctx
	return io.NopCloser(strings.NewReader("test-data")), nil
}

func (m *mockUpload) FinishUpload(ctx context.Context) error {
	_ = ctx
	return nil
}

type mockTerminater struct{}

func (m *mockTerminater) AsTerminatableUpload(upload Upload) TerminatableUpload {
	return &mockTerminatableUpload{}
}

type mockTerminatableUpload struct{}

func (m *mockTerminatableUpload) Terminate(ctx context.Context) error {
	_ = ctx
	return nil
}

// ---------------------------------------------------------------------------
// Prometheus Metrics
// ---------------------------------------------------------------------------

func TestPrometheusHandler_ReturnsMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	m.UploadsCreated.Inc()
	m.UploadsCreated.Inc()
	m.UploadsFinished.Inc()
	m.UploadsTerminated.Inc()
	m.BytesReceived.Add(1024 * 1024)
	m.ErrorsTotal.WithLabelValues("ERR_TEST").Inc()
	m.RequestsTotal.WithLabelValues("POST").Inc()

	app := fiber.New()
	app.Get("/metrics", PrometheusHandler(reg))

	req := httptest.NewRequest("GET", "/metrics", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal("GET /metrics:", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	s := string(body)
	t.Logf("Metrics output:\n%s", s)

	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		t.Error("missing Content-Type header")
	}

	// Prometheus formats bytes as float, so check with decimal
	checks := []string{
		`tusdfiber_uploads_created_total 2`,
		`tusdfiber_uploads_finished_total 1`,
		`tusdfiber_uploads_terminated_total 1`,
		`tusdfiber_bytes_received_total 1.048576e+06`,
		`tusdfiber_errors_total{code="ERR_TEST"} 1`,
		`tusdfiber_requests_total{method="POST"} 1`,
	}

	for _, check := range checks {
		if !strings.Contains(s, check) {
			t.Errorf("missing metric in output: %s", check)
		}
	}
}

func TestPrometheusMiddleware_CountsRequests(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	app := fiber.New()
	app.Use(m.Middleware())
	app.Get("/test", func(c *fiber.Ctx) error {
		return c.SendString("ok")
	})

	req := httptest.NewRequest("GET", "/test", nil)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	_ = body

	// Verify request was counted via separate Fiber app using same registry
	app2 := fiber.New()
	app2.Get("/metrics", PrometheusHandler(reg))
	checkReq := httptest.NewRequest("GET", "/metrics", nil)
	checkResp, _ := app2.Test(checkReq)
	checkBody, _ := io.ReadAll(checkResp.Body)
	if !strings.Contains(string(checkBody), `tusdfiber_requests_total{method="GET"} 1`) {
		t.Error("GET request not counted in metrics")
	}
}
