package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/tus/tusdfiber"
	"github.com/tus/tusd/v2/pkg/filelocker"
	"github.com/tus/tusd/v2/pkg/filestore"
)

// TestExampleEndToEnd runs a real TUS protocol flow against the example setup.
// It uses Fiber's in-process Test() API — no real TCP port needed.
func TestExampleEndToEnd(t *testing.T) {
	dir := t.TempDir()

	// ── Same setup as main.go ──────────────────────────────
	app := fiber.New(fiber.Config{
		StreamRequestBody: true,
		ReadBufferSize:    64 * 1024,
	})

	store := filestore.New(dir)
	locker := filelocker.New(dir)

	composer := tusdfiber.NewStoreComposer()
	store.UseIn(composer.StoreComposer)
	locker.UseIn(composer.StoreComposer)

	handler, err := tusdfiber.NewHandler(tusdfiber.Config{
		StoreComposer: composer,
		BasePath:      "/files/",
		MaxSize:       100 * 1024 * 1024,
		NotifyCompleteUploads:  true,
		NotifyTerminatedUploads: true,
		NotifyCreatedUploads:    true,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	for _, mw := range tusdfiber.DefaultMiddlewareStack(nil) {
		app.Use(mw)
	}
	handler.Register(app)

	// Drain notification channels in background
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

	// ── Test data ──────────────────────────────────────────
	uploadContent := "Hello from tusdfiber E2E test! This is resumable upload content."
	contentLen := len(uploadContent)

	// ── Step 1: POST — create upload ───────────────────────
	t.Log("=== Step 1: POST /files ===")
	postReq := newTUSRequest("POST", "/files", nil)
	postReq.Header.Set("Upload-Length", fmt.Sprintf("%d", contentLen))
	postReq.Header.Set("Upload-Metadata",
		"filename aGVsbG8udHh0,filetype dGV4dC9wbGFpbg==",
	)

	postResp, err := app.Test(postReq)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if postResp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(postResp.Body)
		t.Fatalf("POST: expected 201 Created, got %d\nbody: %s", postResp.StatusCode, body)
	}

	location := postResp.Header.Get("Location")
	if location == "" {
		t.Fatal("POST: missing Location header")
	}
	uploadID := extractUploadID(location)
	t.Logf("Upload created: id=%s location=%s", uploadID, location)

	// ── Step 2: HEAD — verify offset = 0 ──────────────────
	t.Log("=== Step 2: HEAD /files/<id> ===")
	headReq := newTUSRequest("HEAD", "/files/"+uploadID, nil)
	headResp, err := app.Test(headReq)
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	if headResp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD: expected 200 OK, got %d", headResp.StatusCode)
	}
	if headResp.Header.Get("Upload-Offset") != "0" {
		t.Fatalf("HEAD: expected Upload-Offset=0, got %q", headResp.Header.Get("Upload-Offset"))
	}
	if headResp.Header.Get("Upload-Length") != fmt.Sprintf("%d", contentLen) {
		t.Fatalf("HEAD: expected Upload-Length=%d, got %q",
			contentLen, headResp.Header.Get("Upload-Length"))
	}
	t.Logf("Offset: 0/%d", contentLen)

	// ── Step 3: PATCH — upload full content ────────────────
	t.Log("=== Step 3: PATCH /files/<id> (full upload) ===")
	patchReq := newTUSRequest("PATCH", "/files/"+uploadID,
		bytes.NewReader([]byte(uploadContent)))
	patchReq.Header.Set("Upload-Offset", "0")
	patchReq.Header.Set("Content-Type", "application/offset+octet-stream")

	patchResp, err := app.Test(patchReq)
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	if patchResp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(patchResp.Body)
		t.Fatalf("PATCH: expected 204 No Content, got %d\nbody: %s",
			patchResp.StatusCode, body)
	}

	finalOffset := patchResp.Header.Get("Upload-Offset")
	if finalOffset != fmt.Sprintf("%d", contentLen) {
		t.Fatalf("PATCH: expected Upload-Offset=%d, got %q", contentLen, finalOffset)
	}
	t.Logf("Upload complete: offset=%s/%d", finalOffset, contentLen)

	// ── Step 4: HEAD — verify upload finished ──────────────
	t.Log("=== Step 4: HEAD /files/<id> (verify completion) ===")
	headReq2 := newTUSRequest("HEAD", "/files/"+uploadID, nil)
	headResp2, err := app.Test(headReq2)
	if err != nil {
		t.Fatalf("HEAD #2: %v", err)
	}
	if headResp2.Header.Get("Upload-Offset") != fmt.Sprintf("%d", contentLen) {
		t.Fatalf("HEAD #2: expected offset %d, got %q",
			contentLen, headResp2.Header.Get("Upload-Offset"))
	}
	t.Log("Upload confirmed finished")

	// ── Step 5: GET — download the uploaded content ────────
	t.Log("=== Step 5: GET /files/<id> (download) ===")
	getReq := newTUSRequest("GET", "/files/"+uploadID, nil)
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
	if string(downloaded) != uploadContent {
		t.Fatalf("GET: content mismatch\n  expected: %q\n  got:      %q",
			uploadContent, string(downloaded))
	}
	t.Logf("Downloaded %d bytes: content verified", len(downloaded))

	// ── Step 6: DELETE — terminate the upload ──────────────
	t.Log("=== Step 6: DELETE /files/<id> (terminate) ===")
	delReq := newTUSRequest("DELETE", "/files/"+uploadID, nil)
	delResp, err := app.Test(delReq)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE: expected 204 No Content, got %d", delResp.StatusCode)
	}
	t.Log("Upload terminated")

	// ── Step 7: GET — verify 404 after deletion ────────────
	t.Log("=== Step 7: GET /files/<id> (expect 404) ===")
	getReq2 := newTUSRequest("GET", "/files/"+uploadID, nil)
	getResp2, err := app.Test(getReq2)
	if err != nil {
		t.Fatalf("GET after DELETE: %v", err)
	}
	if getResp2.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after DELETE: expected 404, got %d", getResp2.StatusCode)
	}
	t.Log("Confirmed: file no longer exists")

	t.Log("=== E2E TEST PASSED ===")
}

// TestExampleChunkedUpload tests resumable upload with multiple PATCH requests.
func TestExampleChunkedUpload(t *testing.T) {
	dir := t.TempDir()

	app := fiber.New(fiber.Config{
		StreamRequestBody: true,
	})
	store := filestore.New(dir)
	locker := filelocker.New(dir)

	composer := tusdfiber.NewStoreComposer()
	store.UseIn(composer.StoreComposer)
	locker.UseIn(composer.StoreComposer)

	handler, err := tusdfiber.NewHandler(tusdfiber.Config{
		StoreComposer: composer,
		BasePath:      "/files/",
		MaxSize:       1 << 20,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	for _, mw := range tusdfiber.DefaultMiddlewareStack(nil) {
		app.Use(mw)
	}
	handler.Register(app)

	// ── Upload in 3 chunks ────────────────────────────────
	part1 := "Hello "
	part2 := "Resumable "
	part3 := "Upload!"
	totalLen := len(part1) + len(part2) + len(part3)

	// POST
	postReq := newTUSRequest("POST", "/files", nil)
	postReq.Header.Set("Upload-Length", fmt.Sprintf("%d", totalLen))
	postResp, _ := app.Test(postReq)
	id := extractUploadID(postResp.Header.Get("Location"))
	t.Logf("Created: id=%s total=%d", id, totalLen)

	// PATCH chunk 1
	p1 := newTUSRequest("PATCH", "/files/"+id, bytes.NewReader([]byte(part1)))
	p1.Header.Set("Upload-Offset", "0")
	p1.Header.Set("Content-Type", "application/offset+octet-stream")
	r1, _ := app.Test(p1)
	off1 := r1.Header.Get("Upload-Offset")
	t.Logf("Chunk1: sent %d, offset=%s", len(part1), off1)

	// PATCH chunk 2
	p2 := newTUSRequest("PATCH", "/files/"+id, bytes.NewReader([]byte(part2)))
	p2.Header.Set("Upload-Offset", off1)
	p2.Header.Set("Content-Type", "application/offset+octet-stream")
	r2, _ := app.Test(p2)
	off2 := r2.Header.Get("Upload-Offset")
	t.Logf("Chunk2: sent %d, offset=%s", len(part2), off2)

	// PATCH chunk 3 — last
	p3 := newTUSRequest("PATCH", "/files/"+id, bytes.NewReader([]byte(part3)))
	p3.Header.Set("Upload-Offset", off2)
	p3.Header.Set("Content-Type", "application/offset+octet-stream")
	r3, _ := app.Test(p3)
	off3 := r3.Header.Get("Upload-Offset")
	t.Logf("Chunk3: sent %d, offset=%s", len(part3), off3)

	// Verify via GET
	getReq := newTUSRequest("GET", "/files/"+id, nil)
	getResp, _ := app.Test(getReq)
	body, _ := io.ReadAll(getResp.Body)
	if string(body) != "Hello Resumable Upload!" {
		t.Fatalf("chunked upload: expected 'Hello Resumable Upload!', got %q", string(body))
	}
	t.Logf("Chunked upload verified: %q", string(body))
}

// TestExampleOptions tests protocol discovery.
func TestExampleOptions(t *testing.T) {
	dir := t.TempDir()

	app := fiber.New()
	store := filestore.New(dir)
	locker := filelocker.New(dir)

	composer := tusdfiber.NewStoreComposer()
	store.UseIn(composer.StoreComposer)
	locker.UseIn(composer.StoreComposer)

	handler, err := tusdfiber.NewHandler(tusdfiber.Config{
		StoreComposer: composer,
		BasePath:      "/files/",
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	for _, mw := range tusdfiber.DefaultMiddlewareStack(nil) {
		app.Use(mw)
	}
	handler.Register(app)

	req := newTUSRequest("OPTIONS", "/files", nil)
	resp, _ := app.Test(req)

	if resp.Header.Get("Tus-Version") != "1.0.0" {
		t.Fatalf("OPTIONS: missing Tus-Version header")
	}
	if resp.Header.Get("Tus-Extension") == "" {
		t.Fatalf("OPTIONS: missing Tus-Extension header")
	}
	t.Logf("OPTIONS: version=%s extensions=%s",
		resp.Header.Get("Tus-Version"),
		resp.Header.Get("Tus-Extension"))
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newTUSRequest creates an HTTP request with standard TUS headers set.
func newTUSRequest(method, path string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Tus-Resumable", "1.0.0")
	return req
}

// extractUploadID extracts the upload ID from a Location header.
// Location format: http://hostname/files/<id>
func extractUploadID(location string) string {
	parts := strings.Split(location, "/files/")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-1]
}
