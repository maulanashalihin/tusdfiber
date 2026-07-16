package tusdfiber

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gofiber/fiber/v2"
	tusd "github.com/tus/tusd/v2/pkg/handler"
)

// ---------------------------------------------------------------------------
// Integration tests — real HTTP request/response via Fiber's test API
// ---------------------------------------------------------------------------

// setupTestApp creates a Fiber app with TUS handler for testing.
func setupTestApp(t *testing.T) (*fiber.App, *Handler, *memStore) {
	t.Helper()

	store := &memStore{files: make(map[string]*memFile)}
	locker := &memLocker{locks: make(map[string]*memLock)}

	composer := NewStoreComposer()
	composer.Core = store
	composer.UseLocker(locker)
	composer.UseTerminater(store)

	handler, err := NewHandler(Config{
		StoreComposer: composer,
		BasePath:      "/files/",
		MaxSize:       1 << 20, // 1MB
		NotifyCreatedUploads:   true,
		NotifyCompleteUploads:  true,
		NotifyTerminatedUploads: true,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	app := fiber.New(fiber.Config{
		StreamRequestBody: true,
	})
	for _, mw := range DefaultMiddlewareStack(nil) {
		app.Use(mw)
	}
	handler.Register(app)

	// Drain notification channels
	go func() {
		for range handler.CompleteUploads {
		}
	}()
	go func() {
		for range handler.CreatedUploads {
		}
	}()
	go func() {
		for range handler.TerminatedUploads {
		}
	}()

	return app, handler, store
}

func TestHandler_PostAndHead(t *testing.T) {
	app, _, store := setupTestApp(t)

	// ── POST: create upload ────────────────────────────────
	req := httptest.NewRequest(http.MethodPost, "/files", nil)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", "100")
	req.Header.Set("Upload-Metadata", "filename dGVzdC50eHQ=,filetype dGV4dC9wbGFpbg==")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("POST request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST: expected 201 Created, got %d", resp.StatusCode)
	}

	location := resp.Header.Get("Location")
	if location == "" {
		t.Fatal("POST: expected Location header")
	}
	if !strings.Contains(location, "/files/") {
		t.Fatalf("POST: Location should contain /files/, got %q", location)
	}

	// Extract upload ID from Location
	id := extractIDFromLocation(t, location)

	// Verify store has the upload
	if _, err := store.GetUpload(context.Background(), id); err != nil {
		t.Fatalf("upload %s not found in store after POST", id)
	}

	// ── HEAD: get offset ───────────────────────────────────
	headReq := httptest.NewRequest(http.MethodHead, "/files/"+id, nil)
	headReq.Header.Set("Tus-Resumable", "1.0.0")

	headResp, err := app.Test(headReq)
	if err != nil {
		t.Fatalf("HEAD request failed: %v", err)
	}
	if headResp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD: expected 200 OK, got %d", headResp.StatusCode)
	}

	offset := headResp.Header.Get("Upload-Offset")
	if offset != "0" {
		t.Fatalf("HEAD: expected Upload-Offset=0, got %q", offset)
	}
	if headResp.Header.Get("Upload-Length") != "100" {
		t.Fatalf("HEAD: expected Upload-Length=100, got %q", headResp.Header.Get("Upload-Length"))
	}
}

func TestHandler_PostAndPatch(t *testing.T) {
	app, _, store := setupTestApp(t)

	// ── POST ───────────────────────────────────────────────
	body := "Hello, TUS! This is a test upload."
	bodyLen := len(body)

	postReq := httptest.NewRequest(http.MethodPost, "/files", nil)
	postReq.Header.Set("Tus-Resumable", "1.0.0")
	postReq.Header.Set("Upload-Length", fmt.Sprintf("%d", bodyLen))

	postResp, err := app.Test(postReq)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if postResp.StatusCode != http.StatusCreated {
		t.Fatalf("POST: expected 201, got %d", postResp.StatusCode)
	}

	id := extractIDFromLocation(t, postResp.Header.Get("Location"))

	// ── PATCH: upload chunk ────────────────────────────────
	patchReq := httptest.NewRequest(http.MethodPatch, "/files/"+id, bytes.NewReader([]byte(body)))
	patchReq.Header.Set("Tus-Resumable", "1.0.0")
	patchReq.Header.Set("Upload-Offset", "0")
	patchReq.Header.Set("Content-Type", "application/offset+octet-stream")

	patchResp, err := app.Test(patchReq)
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	if patchResp.StatusCode != http.StatusNoContent {
		t.Fatalf("PATCH: expected 204 No Content, got %d", patchResp.StatusCode)
	}

	newOffset := patchResp.Header.Get("Upload-Offset")
	if newOffset != fmt.Sprintf("%d", bodyLen) {
		t.Fatalf("PATCH: expected Upload-Offset=%d, got %q", bodyLen, newOffset)
	}

	// ── HEAD: verify offset updated ────────────────────────
	headReq := httptest.NewRequest(http.MethodHead, "/files/"+id, nil)
	headReq.Header.Set("Tus-Resumable", "1.0.0")

	headResp, err := app.Test(headReq)
	if err != nil {
		t.Fatalf("HEAD after PATCH: %v", err)
	}
	if headResp.Header.Get("Upload-Offset") != fmt.Sprintf("%d", bodyLen) {
		t.Fatalf("HEAD after PATCH: expected offset %d, got %q", bodyLen, headResp.Header.Get("Upload-Offset"))
	}

	// ── Verify store data ──────────────────────────────────
	upload, err := store.GetUpload(context.Background(), id)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	info, err := upload.GetInfo(context.Background())
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}
	if info.Offset != int64(bodyLen) {
		t.Fatalf("store offset: expected %d, got %d", bodyLen, info.Offset)
	}

	// ── GET: download ──────────────────────────────────────
	getReq := httptest.NewRequest(http.MethodGet, "/files/"+id, nil)
	getResp, err := app.Test(getReq)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET: expected 200 OK, got %d", getResp.StatusCode)
	}

	downloaded, err := io.ReadAll(getResp.Body)
	if err != nil {
		t.Fatalf("GET read body: %v", err)
	}
	if string(downloaded) != body {
		t.Fatalf("GET: expected %q, got %q", body, string(downloaded))
	}
}

func TestHandler_PostAndDelete(t *testing.T) {
	app, _, store := setupTestApp(t)

	// ── POST ───────────────────────────────────────────────
	postReq := httptest.NewRequest(http.MethodPost, "/files", nil)
	postReq.Header.Set("Tus-Resumable", "1.0.0")
	postReq.Header.Set("Upload-Length", "50")

	postResp, err := app.Test(postReq)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}

	id := extractIDFromLocation(t, postResp.Header.Get("Location"))

	// ── DELETE ─────────────────────────────────────────────
	delReq := httptest.NewRequest(http.MethodDelete, "/files/"+id, nil)
	delReq.Header.Set("Tus-Resumable", "1.0.0")

	delResp, err := app.Test(delReq)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE: expected 204 No Content, got %d", delResp.StatusCode)
	}

	// ── Verify deleted ─────────────────────────────────────
	_, err = store.GetUpload(context.Background(), id)
	if err == nil {
		t.Fatal("expected error after DELETE, upload should not exist")
	}
}

func TestHandler_Options(t *testing.T) {
	app, _, _ := setupTestApp(t)

	req := httptest.NewRequest(http.MethodOptions, "/files", nil)
	req.Header.Set("Tus-Resumable", "1.0.0")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("OPTIONS: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("OPTIONS: expected 200, got %d", resp.StatusCode)
	}

	if resp.Header.Get("Tus-Version") != "1.0.0" {
		t.Fatalf("OPTIONS: missing Tus-Version header")
	}
	if resp.Header.Get("Tus-Extension") == "" {
		t.Fatalf("OPTIONS: missing Tus-Extension header")
	}
}

func TestHandler_InvalidTusVersion(t *testing.T) {
	app, _, _ := setupTestApp(t)

	req := httptest.NewRequest(http.MethodPost, "/files", nil)
	req.Header.Set("Tus-Resumable", "0.0.1") // wrong version

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("expected 412 for wrong version, got %d", resp.StatusCode)
	}
}

func TestHandler_MissingUploadLength(t *testing.T) {
	app, _, _ := setupTestApp(t)

	req := httptest.NewRequest(http.MethodPost, "/files", nil)
	req.Header.Set("Tus-Resumable", "1.0.0")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing Upload-Length, got %d", resp.StatusCode)
	}
}

func TestHandler_UploadMetadata(t *testing.T) {
	app, _, _ := setupTestApp(t)

	req := httptest.NewRequest(http.MethodPost, "/files", nil)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", "10")
	req.Header.Set("Upload-Metadata", "filename aGVsbG8=,type dGV4dC9wbGFpbg==")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST: expected 201, got %d", resp.StatusCode)
	}
}

func TestHandler_ChunkedUploadMultiple(t *testing.T) {
	app, _, store := setupTestApp(t)

	// POST — total length = 4 + 4 + 2 = 10
	postReq := httptest.NewRequest(http.MethodPost, "/files", nil)
	postReq.Header.Set("Tus-Resumable", "1.0.0")
	postReq.Header.Set("Upload-Length", "10")

	postResp, err := app.Test(postReq)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	id := extractIDFromLocation(t, postResp.Header.Get("Location"))

	// PATCH chunk 1 (4 bytes: "part")
	patch1 := httptest.NewRequest(http.MethodPatch, "/files/"+id, bytes.NewReader([]byte("part")))
	patch1.Header.Set("Tus-Resumable", "1.0.0")
	patch1.Header.Set("Upload-Offset", "0")
	patch1.Header.Set("Content-Type", "application/offset+octet-stream")

	resp1, err := app.Test(patch1)
	if err != nil {
		t.Fatalf("PATCH1: %v", err)
	}
	if resp1.StatusCode != http.StatusNoContent {
		t.Fatalf("PATCH1: expected 204, got %d", resp1.StatusCode)
	}
	if resp1.Header.Get("Upload-Offset") != "4" {
		t.Fatalf("PATCH1: expected offset 4, got %q", resp1.Header.Get("Upload-Offset"))
	}

	// PATCH chunk 2 (4 bytes: "two2")
	patch2 := httptest.NewRequest(http.MethodPatch, "/files/"+id, bytes.NewReader([]byte("two2")))
	patch2.Header.Set("Tus-Resumable", "1.0.0")
	patch2.Header.Set("Upload-Offset", "4")
	patch2.Header.Set("Content-Type", "application/offset+octet-stream")

	resp2, err := app.Test(patch2)
	if err != nil {
		t.Fatalf("PATCH2: %v", err)
	}
	if resp2.StatusCode != http.StatusNoContent {
		t.Fatalf("PATCH2: expected 204, got %d", resp2.StatusCode)
	}
	if resp2.Header.Get("Upload-Offset") != "8" {
		t.Fatalf("PATCH2: expected offset 8, got %q", resp2.Header.Get("Upload-Offset"))
	}

	// PATCH chunk 3 (2 bytes: "wo") — should finish upload
	patch3 := httptest.NewRequest(http.MethodPatch, "/files/"+id, bytes.NewReader([]byte("wo")))
	patch3.Header.Set("Tus-Resumable", "1.0.0")
	patch3.Header.Set("Upload-Offset", "8")
	patch3.Header.Set("Content-Type", "application/offset+octet-stream")

	resp3, err := app.Test(patch3)
	if err != nil {
		t.Fatalf("PATCH3: %v", err)
	}
	if resp3.StatusCode != http.StatusNoContent {
		t.Fatalf("PATCH3: expected 204, got %d", resp3.StatusCode)
	}
	if resp3.Header.Get("Upload-Offset") != "10" {
		t.Fatalf("PATCH3: expected offset 10, got %q", resp3.Header.Get("Upload-Offset"))
	}

	// Verify complete upload via GET
	getReq := httptest.NewRequest(http.MethodGet, "/files/"+id, nil)
	getResp, err := app.Test(getReq)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(getResp.Body)
	if string(body) != "parttwo2wo" {
		t.Fatalf("GET: expected 'parttwo2wo', got %q", string(body))
	}

	// Verify store offset
	upload, _ := store.GetUpload(context.Background(), id)
	info, _ := upload.GetInfo(context.Background())
	if info.Offset != 10 {
		t.Fatalf("store offset: expected 10, got %d", info.Offset)
	}
}

func TestHandler_MismatchedOffset(t *testing.T) {
	app, _, _ := setupTestApp(t)

	// POST
	postReq := httptest.NewRequest(http.MethodPost, "/files", nil)
	postReq.Header.Set("Tus-Resumable", "1.0.0")
	postReq.Header.Set("Upload-Length", "10")
	postResp, _ := app.Test(postReq)
	id := extractIDFromLocation(t, postResp.Header.Get("Location"))

	// PATCH with wrong offset
	patchReq := httptest.NewRequest(http.MethodPatch, "/files/"+id, bytes.NewReader([]byte("test")))
	patchReq.Header.Set("Tus-Resumable", "1.0.0")
	patchReq.Header.Set("Upload-Offset", "5") // should be 0
	patchReq.Header.Set("Content-Type", "application/offset+octet-stream")

	resp, err := app.Test(patchReq)
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 Conflict for mismatched offset, got %d", resp.StatusCode)
	}
}

func TestHandler_OptionsWithDraft(t *testing.T) {
	store := &memStore{files: make(map[string]*memFile)}
	locker := &memLocker{locks: make(map[string]*memLock)}
	composer := NewStoreComposer()
	composer.Core = store
	composer.UseLocker(locker)

	h, _ := NewHandler(Config{
		StoreComposer:              composer,
		BasePath:                   "/v2/",
		EnableExperimentalProtocol: true,
	})
	draftApp := fiber.New()
	for _, mw := range DefaultMiddlewareStack(nil) {
		draftApp.Use(mw)
	}
	h.Register(draftApp)

	req := httptest.NewRequest(http.MethodOptions, "/v2", nil)
	req.Header.Set("Upload-Draft-Interop-Version", "4")

	resp, err := draftApp.Test(req)
	if err != nil {
		t.Fatalf("OPTIONS: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("OPTIONS: expected 200, got %d", resp.StatusCode)
	}
	if resp.Header.Get("Upload-Limit") == "" {
		t.Fatalf("OPTIONS: expected Upload-Limit header for draft request")
	}
}

func TestHandler_GetNonExistent(t *testing.T) {
	app, _, _ := setupTestApp(t)

	req := httptest.NewRequest(http.MethodGet, "/files/nonexistent", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	t.Logf("GET non-existent: status=%d body=%q", resp.StatusCode, string(body))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET non-existent: expected 404, got %d (body: %s)", resp.StatusCode, string(body))
	}
}

// ---------------------------------------------------------------------------
// extract helpers
// ---------------------------------------------------------------------------

func extractIDFromLocation(t *testing.T, location string) string {
	t.Helper()
	parts := strings.Split(location, "/files/")
	if len(parts) < 2 || parts[len(parts)-1] == "" {
		t.Fatalf("cannot extract ID from Location: %q", location)
		return ""
	}
	return parts[len(parts)-1]
}

// ---------------------------------------------------------------------------
// In-memory store for testing
// ---------------------------------------------------------------------------

type memFile struct {
	mu   sync.Mutex
	data []byte
	info FileInfo
}

type memStore struct {
	mu    sync.Mutex
	files map[string]*memFile
}

func (s *memStore) NewUpload(ctx context.Context, info FileInfo) (Upload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if info.ID == "" {
		info.ID = fmt.Sprintf("test-%d", len(s.files)+1)
	}
	s.files[info.ID] = &memFile{
		data: make([]byte, 0, info.Size),
		info: info,
	}
	return &memUpload{store: s, id: info.ID}, nil
}

func (s *memStore) GetUpload(ctx context.Context, id string) (Upload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, ok := s.files[id]
	if !ok {
		return nil, tusd.ErrNotFound
	}
	return &memUpload{store: s, id: id, file: f}, nil
}

type memUpload struct {
	store *memStore
	id    string
	file  *memFile
}

func (u *memUpload) WriteChunk(ctx context.Context, offset int64, src io.Reader) (int64, error) {
	u.store.mu.Lock()
	f := u.store.files[u.id]
	u.store.mu.Unlock()

	f.mu.Lock()
	defer f.mu.Unlock()

	// Ensure data slice is large enough
	needed := offset - int64(len(f.data))
	if needed > 0 {
		f.data = append(f.data, make([]byte, needed)...)
	}

	// Read from src and append
	buf, readErr := io.ReadAll(src)
	if readErr != nil && readErr.Error() != "EOF" {
		// Check if it's one of our TUS errors
		_ = readErr
	}

	// Pad if writing beyond current end
	if offset > int64(len(f.data)) {
		f.data = append(f.data, make([]byte, offset-int64(len(f.data)))...)
	}

	f.data = append(f.data[:offset], buf...)
	f.info.Offset = offset + int64(len(buf))

	return int64(len(buf)), nil
}

func (u *memUpload) GetInfo(ctx context.Context) (FileInfo, error) {
	u.store.mu.Lock()
	f := u.store.files[u.id]
	u.store.mu.Unlock()
	return f.info, nil
}

func (u *memUpload) GetReader(ctx context.Context) (io.ReadCloser, error) {
	u.store.mu.Lock()
	f := u.store.files[u.id]
	u.store.mu.Unlock()
	f.mu.Lock()
	defer f.mu.Unlock()
	return io.NopCloser(bytes.NewReader(append([]byte{}, f.data...))), nil
}

func (u *memUpload) FinishUpload(ctx context.Context) error {
	return nil
}

func (s *memStore) AsTerminatableUpload(upload Upload) TerminatableUpload {
	return upload.(*memUpload)
}

func (u *memUpload) Terminate(ctx context.Context) error {
	u.store.mu.Lock()
	defer u.store.mu.Unlock()
	delete(u.store.files, u.id)
	return nil
}

// ── Locker ──────────────────────────────────────────────

type memLock struct {
	mu      sync.Mutex
	held    bool
	id      string
	release func()
}

type memLocker struct {
	mu    sync.Mutex
	locks map[string]*memLock
}

func (l *memLocker) NewLock(id string) (Lock, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	lock := &memLock{id: id}
	l.locks[id] = lock
	return lock, nil
}

func (l *memLock) Lock(ctx context.Context, requestUnlock func()) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held {
		return fmt.Errorf("already locked: %s", l.id)
	}
	l.held = true
	l.release = requestUnlock
	return nil
}

func (l *memLock) Unlock() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.held = false
	return nil
}
