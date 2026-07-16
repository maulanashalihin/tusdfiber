package tusdfiber

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
)

// UnroutedHandler provides TUS protocol methods (PostFile, HeadFile, PatchFile, …)
// that you can wire into any router. Use NewUnroutedHandler to create one.
type UnroutedHandler struct {
	config   Config
	composer *StoreComposer

	// Notification channels (mirror tusd's interface)
	CompleteUploads   chan HookEvent
	TerminatedUploads chan HookEvent
	UploadProgress    chan HookEvent
	CreatedUploads    chan HookEvent

	extensions string
}

// NewUnroutedHandler creates an UnroutedHandler with the given config.
func NewUnroutedHandler(config Config) (*UnroutedHandler, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}

	ext := "creation,creation-with-upload"
	if config.StoreComposer.UsesTerminater && !config.DisableTermination {
		ext += ",termination"
	}
	if config.StoreComposer.UsesConcater && !config.DisableConcatenation {
		ext += ",concatenation"
	}
	if config.StoreComposer.UsesLengthDeferrer {
		ext += ",creation-defer-length"
	}

	return &UnroutedHandler{
		config:            config,
		composer:          config.StoreComposer,
		CompleteUploads:   make(chan HookEvent),
		TerminatedUploads: make(chan HookEvent),
		UploadProgress:    make(chan HookEvent),
		CreatedUploads:    make(chan HookEvent),
		extensions:        ext,
	}, nil
}

// ---------------------------------------------------------------------------
// POST — create upload
// ---------------------------------------------------------------------------

// PostFile creates a new upload resource.
func (h *UnroutedHandler) PostFile(c *fiber.Ctx) error {
	// Route to v2 if the request uses the IETF resumable upload draft
	if h.usesDraft(c) {
		return h.PostFileV2(c)
	}

	ctx := newHookContext(c, h.config.GracefulRequestCompletionTimeout)
	defer ctx.cancel(nil)

	containsChunk := string(c.Request().Header.ContentType()) == "application/offset+octet-stream"

	var concatHeader string
	if h.composer.UsesConcater {
		concatHeader = c.Get("Upload-Concat")
	}
	if concatHeader != "" && h.config.DisableConcatenation {
		return h.writeError(c, ctx, ErrConcatenationUnsupported)
	}

	isPartial, isFinal, partialUploadIDs, err := parseConcat(concatHeader, h.config.BasePath)
	if err != nil {
		return h.writeError(c, ctx, err)
	}

	var size int64
	var sizeIsDeferred bool
	var partialUploads []Upload

	if isFinal {
		if containsChunk {
			return h.writeError(c, ctx, ErrModifyFinal)
		}
		var sizeErr error
		partialUploads, size, sizeErr = h.sizeOfUploads(ctx, partialUploadIDs)
		if sizeErr != nil {
			return h.writeError(c, ctx, sizeErr)
		}
	} else {
		uploadLenHeader := c.Get("Upload-Length")
		deferLenHeader := c.Get("Upload-Defer-Length")
		size, sizeIsDeferred, err = validateNewUploadLength(uploadLenHeader, deferLenHeader, h.composer.UsesLengthDeferrer)
		if err != nil {
			return h.writeError(c, ctx, err)
		}
	}

	if h.config.MaxSize > 0 && size > h.config.MaxSize {
		return h.writeError(c, ctx, ErrMaxSizeExceeded)
	}

	meta := parseMetadataHeader(c.Get("Upload-Metadata"))

	info := FileInfo{
		Size:           size,
		SizeIsDeferred: sizeIsDeferred,
		MetaData:       meta,
		IsPartial:      isPartial,
		IsFinal:        isFinal,
		PartialUploads: partialUploadIDs,
	}

	resp := HTTPResponse{
		StatusCode: http.StatusCreated,
		Header:     HTTPHeader{},
	}

	if h.config.PreUploadCreateCallback != nil {
		resp2, changes, cbErr := h.config.PreUploadCreateCallback(newHookEvent(ctx, info))
		if cbErr != nil {
			return h.writeError(c, ctx, cbErr)
		}
		resp = resp.MergeWith(resp2)
		if changes.ID != "" {
			if err := validateUploadID(changes.ID); err != nil {
				return h.writeError(c, ctx, err)
			}
			info.ID = changes.ID
		}
		if changes.MetaData != nil {
			info.MetaData = changes.MetaData
		}
		if changes.Storage != nil {
			info.Storage = changes.Storage
		}
	}

	upload, err := h.composer.Core.NewUpload(ctx, info)
	if err != nil {
		return h.writeError(c, ctx, err)
	}

	info, err = upload.GetInfo(ctx)
	if err != nil {
		return h.writeError(c, ctx, err)
	}

	id := info.ID
	url := h.absFileURL(c, id)
	resp.Header["Location"] = url

	ctx.log(c, "UploadCreated", "id", id, "size", size, "url", url)
	_ = ctx // ctx used for logging

	if h.config.NotifyCreatedUploads {
		h.CreatedUploads <- newHookEvent(ctx, info)
	}

	if isFinal {
		concatUpload := h.composer.Concater.AsConcatableUpload(upload)
		if err := concatUpload.ConcatUploads(ctx, partialUploads); err != nil {
			return h.writeError(c, ctx, err)
		}
		info.Offset = size
		resp, err = h.emitFinishEvents(ctx, resp, info)
		if err != nil {
			return h.writeError(c, ctx, err)
		}
	}

	if containsChunk {
		if h.composer.UsesLocker {
			lock, lockErr := h.lockUpload(ctx, id)
			if lockErr != nil {
				return h.writeError(c, ctx, lockErr)
			}
			defer lock.Unlock()
		}
		resp, err = h.writeChunk(ctx, resp, upload, info, c)
		if err != nil {
			return h.writeError(c, ctx, err)
		}
	} else if !sizeIsDeferred && size == 0 {
		resp, err = h.finishUploadIfComplete(ctx, resp, upload, info)
		if err != nil {
			return h.writeError(c, ctx, err)
		}
	}

	return h.sendResp(c, resp)
}

// ---------------------------------------------------------------------------
// HEAD — get offset / info
// ---------------------------------------------------------------------------

// HeadFile returns the upload offset and metadata.
func (h *UnroutedHandler) HeadFile(c *fiber.Ctx) error {
	ctx := newHookContext(c, h.config.GracefulRequestCompletionTimeout)
	defer ctx.cancel(nil)

	id := getUploadID(c)
	if id == "" {
		return h.writeError(c, ctx, ErrNotFound)
	}

	if h.composer.UsesLocker {
		lock, err := h.lockUpload(ctx, id)
		if err != nil {
			return h.writeError(c, ctx, err)
		}
		defer lock.Unlock()
	}

	upload, err := h.composer.Core.GetUpload(ctx, id)
	if err != nil {
		return h.writeError(c, ctx, err)
	}

	info, err := upload.GetInfo(ctx)
	if err != nil {
		return h.writeError(c, ctx, err)
	}

	isV2 := h.usesDraft(c)

	resp := HTTPResponse{
		StatusCode: http.StatusOK,
		Header: HTTPHeader{
			"Cache-Control": "no-store",
			"Upload-Offset": strconv.FormatInt(info.Offset, 10),
		},
	}

	if isV2 {
		// IETF draft uses 204 No Content for HEAD
		resp.StatusCode = http.StatusNoContent

		isComplete := !info.SizeIsDeferred && info.Offset == info.Size
		setDraftCompleteHeaders(c, getDraftVersion(c), isComplete)
		resp.Header["Upload-Draft-Interop-Version"] = string(getDraftVersion(c))

		if !info.SizeIsDeferred {
			resp.Header["Upload-Length"] = strconv.FormatInt(info.Size, 10)
		}
		resp.Header["Upload-Limit"] = h.getDraftUploadLimits(info)
	} else {
		// TUS v1 — 200 OK with full metadata
		resp.StatusCode = http.StatusOK

		if info.IsPartial {
			resp.Header["Upload-Concat"] = "partial"
		}
		if info.IsFinal {
			v := "final;"
			for _, uid := range info.PartialUploads {
				v += h.absFileURL(c, uid) + " "
			}
			v = strings.TrimRight(v, " ")
			resp.Header["Upload-Concat"] = v
		}
		if len(info.MetaData) != 0 {
			resp.Header["Upload-Metadata"] = serializeMetadataHeader(info.MetaData)
		}
		if info.SizeIsDeferred {
			resp.Header["Upload-Defer-Length"] = "1"
		} else {
			resp.Header["Upload-Length"] = strconv.FormatInt(info.Size, 10)
			resp.Header["Content-Length"] = strconv.FormatInt(info.Size, 10)
		}
	}

	return h.sendResp(c, resp)
}

// ---------------------------------------------------------------------------
// PATCH — upload chunk
// ---------------------------------------------------------------------------

// PatchFile appends a chunk to an upload.
func (h *UnroutedHandler) PatchFile(c *fiber.Ctx) error {
	ctx := newHookContext(c, h.config.GracefulRequestCompletionTimeout)
	defer ctx.cancel(nil)

	// Validate Content-Type
	ct := string(c.Request().Header.ContentType())
	isV2 := h.usesDraft(c)
	if isV2 {
		// Draft v4+ requires application/partial-upload; v3 accepts anything
		dv := getDraftVersion(c)
		if dv != interopVersion3 && ct != "" && ct != "application/partial-upload" && ct != "application/offset+octet-stream" {
			return h.writeError(c, ctx, ErrInvalidContentType)
		}
	} else if ct != "application/offset+octet-stream" {
		return h.writeError(c, ctx, ErrInvalidContentType)
	}

	// Validate Upload-Offset
	offsetStr := c.Get("Upload-Offset")
	offset, err := strconv.ParseInt(offsetStr, 10, 64)
	if err != nil || offset < 0 {
		return h.writeError(c, ctx, ErrInvalidOffset)
	}

	id := getUploadID(c)
	if id == "" {
		return h.writeError(c, ctx, ErrNotFound)
	}

	if h.composer.UsesLocker {
		lock, lockErr := h.lockUpload(ctx, id)
		if lockErr != nil {
			return h.writeError(c, ctx, lockErr)
		}
		defer lock.Unlock()
	}

	upload, err := h.composer.Core.GetUpload(ctx, id)
	if err != nil {
		return h.writeError(c, ctx, err)
	}

	info, err := upload.GetInfo(ctx)
	if err != nil {
		return h.writeError(c, ctx, err)
	}

	if info.IsFinal {
		return h.writeError(c, ctx, ErrModifyFinal)
	}
	if offset != info.Offset {
		return h.writeError(c, ctx, ErrMismatchOffset)
	}

	resp := HTTPResponse{
		StatusCode: http.StatusNoContent,
		Header:     make(HTTPHeader),
	}

	// If upload is already complete, just return current offset
	if !info.SizeIsDeferred && info.Offset == info.Size {
		resp.Header["Upload-Offset"] = strconv.FormatInt(offset, 10)
		return h.sendResp(c, resp)
	}

	// Handle deferred length declaration
	if c.Get("Upload-Length") != "" {
		if !h.composer.UsesLengthDeferrer {
			return h.writeError(c, ctx, ErrNotImplemented)
		}
		if !info.SizeIsDeferred {
			return h.writeError(c, ctx, ErrInvalidUploadLength)
		}
		uploadLen, parseErr := strconv.ParseInt(c.Get("Upload-Length"), 10, 64)
		if parseErr != nil || uploadLen < 0 || uploadLen < info.Offset ||
			(h.config.MaxSize > 0 && uploadLen > h.config.MaxSize) {
			return h.writeError(c, ctx, ErrInvalidUploadLength)
		}
		decl := h.composer.LengthDeferrer.AsLengthDeclarableUpload(upload)
		if declErr := decl.DeclareLength(ctx, uploadLen); declErr != nil {
			return h.writeError(c, ctx, declErr)
		}
		info.Size = uploadLen
		info.SizeIsDeferred = false
	}

	resp, err = h.writeChunk(ctx, resp, upload, info, c)
	if err != nil {
		return h.writeError(c, ctx, err)
	}

	return h.sendResp(c, resp)
}

// ---------------------------------------------------------------------------
// GET — download
// ---------------------------------------------------------------------------

// GetFile serves the uploaded content for download.
func (h *UnroutedHandler) GetFile(c *fiber.Ctx) error {
	ctx := newHookContext(c, h.config.GracefulRequestCompletionTimeout)
	defer ctx.cancel(nil)

	id := getUploadID(c)
	if id == "" {
		return h.writeError(c, ctx, ErrNotFound)
	}

	if h.composer.UsesLocker {
		lock, err := h.lockUpload(ctx, id)
		if err != nil {
			return h.writeError(c, ctx, err)
		}
		defer lock.Unlock()
	}

	upload, err := h.composer.Core.GetUpload(ctx, id)
	if err != nil {
		return h.writeError(c, ctx, err)
	}

	info, err := upload.GetInfo(ctx)
	if err != nil {
		return h.writeError(c, ctx, err)
	}

	contentType, contentDisposition := filterContentType(info)

	// If the store has a ContentServer delegate
	if h.composer.UsesContentServer {
		servable := h.composer.ContentServer.AsServableUpload(upload)
		c.Set("Content-Type", contentType)
		c.Set("Content-Disposition", contentDisposition)
		// Delegate to the store's ServeContent — it writes directly.
		return servable.ServeContent(ctx, newFiberResponseWriter(c), &http.Request{})
	}

	if info.Offset == 0 {
		return h.sendResp(c, HTTPResponse{StatusCode: http.StatusNoContent})
	}

	c.Set("Content-Type", contentType)
	c.Set("Content-Disposition", contentDisposition)
	c.Set("Content-Length", strconv.FormatInt(info.Offset, 10))
	c.Status(http.StatusOK)

	reader, err := upload.GetReader(ctx)
	if err != nil {
		return h.writeError(c, ctx, err)
	}
	defer reader.Close()

	_, err = io.Copy(c.Context().Response.BodyWriter(), reader)
	if err != nil {
		return h.writeError(c, ctx, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// DELETE — terminate
// ---------------------------------------------------------------------------

// DelFile terminates an upload.
func (h *UnroutedHandler) DelFile(c *fiber.Ctx) error {
	ctx := newHookContext(c, h.config.GracefulRequestCompletionTimeout)
	defer ctx.cancel(nil)

	if !h.composer.UsesTerminater {
		return h.writeError(c, ctx, ErrNotImplemented)
	}

	id := getUploadID(c)
	if id == "" {
		return h.writeError(c, ctx, ErrNotFound)
	}

	if h.composer.UsesLocker {
		lock, err := h.lockUpload(ctx, id)
		if err != nil {
			return h.writeError(c, ctx, err)
		}
		defer lock.Unlock()
	}

	upload, err := h.composer.Core.GetUpload(ctx, id)
	if err != nil {
		return h.writeError(c, ctx, err)
	}

	var info FileInfo
	if h.config.NotifyTerminatedUploads || h.config.PreUploadTerminateCallback != nil {
		info, err = upload.GetInfo(ctx)
		if err != nil {
			return h.writeError(c, ctx, err)
		}
	}

	resp := HTTPResponse{StatusCode: http.StatusNoContent}

	if h.config.PreUploadTerminateCallback != nil {
		resp2, cbErr := h.config.PreUploadTerminateCallback(newHookEvent(ctx, info))
		if cbErr != nil {
			return h.writeError(c, ctx, cbErr)
		}
		resp = resp.MergeWith(resp2)
	}

	term := h.composer.Terminater.AsTerminatableUpload(upload)
	if err := term.Terminate(ctx); err != nil {
		return h.writeError(c, ctx, err)
	}

	if h.config.NotifyTerminatedUploads {
		h.TerminatedUploads <- newHookEvent(ctx, info)
	}

	return h.sendResp(c, resp)
}

// ---------------------------------------------------------------------------
// OPTIONS — protocol discovery
// ---------------------------------------------------------------------------

// Options handles OPTIONS requests for protocol discovery.
func (h *UnroutedHandler) Options(c *fiber.Ctx) error {
	c.Set("Tus-Version", "1.0.0")
	c.Set("Tus-Extension", h.extensions)
	if h.config.MaxSize > 0 {
		c.Set("Tus-Max-Size", strconv.FormatInt(h.config.MaxSize, 10))
	}

	// If the client requested the IETF draft, include draft-specific headers
	dv := c.Get("Upload-Draft-Interop-Version")
	if dv != "" && h.config.EnableExperimentalProtocol {
		limits := "min-size=0"
		if h.config.MaxSize > 0 {
			limits += ",max-size=" + strconv.FormatInt(h.config.MaxSize, 10)
		}
		c.Set("Upload-Limit", limits)
	}

	return c.SendStatus(http.StatusOK)
}

// Extensions returns a comma-separated list of supported tus extensions.
func (h *UnroutedHandler) Extensions() string {
	return h.extensions
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// writeChunk reads the body and appends it to the upload.
func (h *UnroutedHandler) writeChunk(
	ctx *hookContext,
	resp HTTPResponse,
	upload Upload,
	info FileInfo,
	c *fiber.Ctx,
) (HTTPResponse, error) {
	length := int64(c.Request().Header.ContentLength())
	offset := info.Offset

	// Validate size
	if !info.SizeIsDeferred && offset+length > info.Size {
		return resp, ErrSizeExceeded
	}

	maxSize := info.Size - offset
	if info.SizeIsDeferred {
		if h.config.MaxSize > 0 {
			maxSize = h.config.MaxSize - offset
		} else {
			maxSize = math.MaxInt64
		}
	}
	if length > 0 {
		maxSize = length
	}

	stream := c.Context().RequestBodyStream()
	if stream == nil {
		return resp, ErrNotFound
	}

	bodyR := newBodyReader(io.NopCloser(stream), maxSize)

	// Support stopping an upload via callback
	stopFn := func(res HTTPResponse) {
		cause := ErrUploadStoppedByServer
		cause.HTTPResponse = cause.HTTPResponse.MergeWith(res)
		ctx.cancel(cause)
		bodyR.closeWithError(cause)
	}
	ctx.stopUpload = stopFn
	RegisterStopCallback(info.ID, stopFn)
	defer UnregisterStopCallback(info.ID)

	if h.config.NotifyUploadProgress {
		go h.sendProgressMessages(ctx, info, bodyR, offset)
	}

	bytesWritten, err := upload.WriteChunk(ctx, offset, bodyR)

	bodyErr := bodyR.hasError()
	if bodyErr != nil {
		// ctx.log("BodyReadError", "error", bodyErr.Error())
		if err == nil {
			err = bodyErr
		}
	}

	// Terminate if stopped by server
	if errors.Is(bodyErr, ErrUploadStoppedByServer) && h.composer.UsesTerminater {
		if termErr := h.terminateUpload(ctx, upload, info); termErr != nil {
			_ = termErr // log only
		}
	}

	newOffset := offset + bytesWritten
	resp.Header["Upload-Offset"] = strconv.FormatInt(newOffset, 10)
	info.Offset = newOffset

	finishResp, finishErr := h.finishUploadIfComplete(ctx, resp, upload, info)
	if err != nil {
		return resp, err
	}
	if finishErr != nil {
		return finishResp, finishErr
	}
	return finishResp, nil
}

// finishUploadIfComplete checks if the upload is done and calls FinishUpload.
func (h *UnroutedHandler) finishUploadIfComplete(
	ctx *hookContext,
	resp HTTPResponse,
	upload Upload,
	info FileInfo,
) (HTTPResponse, error) {
	if !info.SizeIsDeferred && info.Offset == info.Size {
		if err := upload.FinishUpload(ctx); err != nil {
			return resp, err
		}
		return h.emitFinishEvents(ctx, resp, info)
	}
	return resp, nil
}

// emitFinishEvents calls PreFinishResponseCallback and sends to CompleteUploads.
func (h *UnroutedHandler) emitFinishEvents(ctx *hookContext, resp HTTPResponse, info FileInfo) (HTTPResponse, error) {
	if h.config.PreFinishResponseCallback != nil {
		resp2, err := h.config.PreFinishResponseCallback(newHookEvent(ctx, info))
		if err != nil {
			return resp, err
		}
		resp = resp.MergeWith(resp2)
	}
	if h.config.NotifyCompleteUploads {
		h.CompleteUploads <- newHookEvent(ctx, info)
	}
	return resp, nil
}

// terminateUpload terminates the upload via the store's Terminater.
func (h *UnroutedHandler) terminateUpload(ctx *hookContext, upload Upload, info FileInfo) error {
	term := h.composer.Terminater.AsTerminatableUpload(upload)
	return term.Terminate(ctx)
}

// sendProgressMessages emits progress notifications periodically.
func (h *UnroutedHandler) sendProgressMessages(ctx *hookContext, info FileInfo, br *bodyReader, originalOffset int64) {
	hook := newHookEvent(ctx, info)
	prev := int64(-1)
	tick := time.NewTicker(h.config.UploadProgressInterval)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			current := originalOffset + br.bytesRead()
			if current != prev {
				hook.Upload.Offset = current
				h.UploadProgress <- hook
			}
			return
		case <-tick.C:
			current := originalOffset + br.bytesRead()
			if current != prev {
				hook.Upload.Offset = current
				h.UploadProgress <- hook
				prev = current
			}
		}
	}
}

// absFileURL builds an absolute URL for the Location header.
func (h *UnroutedHandler) absFileURL(c *fiber.Ctx, id string) string {
	if h.config.isAbs {
		return h.config.BasePath + id
	}
	proto := "http"
	if strings.HasPrefix(string(c.Context().URI().Scheme()), "https") {
		proto = "https"
	}
	host := string(c.Context().Host())
	return fmt.Sprintf("%s://%s%s%s", proto, host, h.config.BasePath, id)
}

// lockUpload creates and acquires a lock for the given upload ID.
func (h *UnroutedHandler) lockUpload(ctx *hookContext, id string) (Lock, error) {
	lock, err := h.composer.Locker.NewLock(id)
	if err != nil {
		return nil, err
	}
	lockCtx, cancel := context.WithTimeout(ctx, h.config.AcquireLockTimeout)
	defer cancel()

	releaseLock := func() {
		ctx.cancel(ErrUploadInterrupted)
	}

	if err := lock.Lock(lockCtx, releaseLock); err != nil {
		return nil, err
	}
	return lock, nil
}

// sizeOfUploads returns the total size of a list of partial uploads.
func (h *UnroutedHandler) sizeOfUploads(ctx context.Context, ids []string) ([]Upload, int64, error) {
	uploads := make([]Upload, len(ids))
	var totalSize int64

	for i, id := range ids {
		upload, err := h.composer.Core.GetUpload(ctx, id)
		if err != nil {
			return nil, 0, err
		}
		info, err := upload.GetInfo(ctx)
		if err != nil {
			return nil, 0, err
		}
		if info.SizeIsDeferred || info.Offset != info.Size {
			return nil, 0, ErrUploadNotFinished
		}
		totalSize += info.Size
		uploads[i] = upload
	}
	return uploads, totalSize, nil
}

// ---------------------------------------------------------------------------
// Response helpers
// ---------------------------------------------------------------------------

func (h *UnroutedHandler) sendResp(c *fiber.Ctx, resp HTTPResponse) error {
	for k, v := range resp.Header {
		c.Set(k, v)
	}
	if resp.Body != "" {
		c.Set("Content-Length", strconv.Itoa(len(resp.Body)))
	}
	c.Status(resp.StatusCode)
	if resp.Body != "" {
		return c.SendString(resp.Body)
	}
	return nil
}

func (h *UnroutedHandler) writeError(c *fiber.Ctx, ctx *hookContext, err error) error {
	var tErr *TUSError
	if e, ok := err.(*TUSError); ok {
		tErr = e
	} else {
		_ = ctx // for logging, would use ctx.log
		tErr = NewError("ERR_INTERNAL_SERVER_ERROR", err.Error(), http.StatusInternalServerError)
	}
	for k, v := range tErr.HTTPResponse.Header {
		c.Set(k, v)
	}
	c.Status(tErr.HTTPResponse.StatusCode)
	return c.SendString(tErr.HTTPResponse.Body)
}

// ---------------------------------------------------------------------------
// hookContext logging helper
// ---------------------------------------------------------------------------

func (ctx *hookContext) log(c *fiber.Ctx, event string, keysAndValues ...interface{}) {
	// Simple structured log via slog — can be extended.
	_ = c
	_ = event
	_ = keysAndValues
}

// ---------------------------------------------------------------------------
// Utility — for use by non-hook-context code
// ---------------------------------------------------------------------------

func writeError(c *fiber.Ctx, err error) error {
	var tErr *TUSError
	if e, ok := err.(*TUSError); ok {
		tErr = e
	} else {
		tErr = NewError("ERR_INTERNAL_SERVER_ERROR", err.Error(), http.StatusInternalServerError)
	}
	for k, v := range tErr.HTTPResponse.Header {
		c.Set(k, v)
	}
	c.Status(tErr.HTTPResponse.StatusCode)
	return c.SendString(tErr.HTTPResponse.Body)
}

func sendResp(c *fiber.Ctx, resp HTTPResponse) error {
	for k, v := range resp.Header {
		c.Set(k, v)
	}
	if resp.Body != "" {
		c.Set("Content-Length", strconv.Itoa(len(resp.Body)))
	}
	c.Status(resp.StatusCode)
	if resp.Body != "" {
		return c.SendString(resp.Body)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Header parsing helpers
// ---------------------------------------------------------------------------

func parseMetadataHeader(header string) map[string]string {
	meta := make(map[string]string)
	for element := range strings.SplitSeq(header, ",") {
		element = strings.TrimSpace(element)
		parts := strings.Split(element, " ")
		if len(parts) > 2 {
			continue
		}
		key := parts[0]
		if key == "" {
			continue
		}
		value := ""
		if len(parts) == 2 {
			dec, err := base64.StdEncoding.DecodeString(parts[1])
			if err != nil {
				continue
			}
			value = string(dec)
		}
		meta[key] = value
	}
	return meta
}

func serializeMetadataHeader(meta map[string]string) string {
	var b strings.Builder
	for key, value := range meta {
		enc := base64.StdEncoding.EncodeToString([]byte(value))
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(key)
		b.WriteByte(' ')
		b.WriteString(enc)
	}
	return b.String()
}

func parseConcat(header string, basePath string) (isPartial bool, isFinal bool, ids []string, err error) {
	if header == "" {
		return
	}
	if header == "partial" {
		return true, false, nil, nil
	}
	if strings.HasPrefix(header, "final;") && len(header) > len("final;") {
		isFinal = true
		list := strings.Fields(header[len("final;"):])
		for _, val := range list {
			id, extractErr := extractIDFromURL(val, basePath)
			if extractErr != nil {
				err = extractErr
				return
			}
			ids = append(ids, id)
		}
		if len(ids) == 0 {
			isFinal = false
			err = ErrInvalidConcat
		}
		return
	}
	return
}

func extractIDFromURL(url, basePath string) (string, error) {
	_, id, ok := strings.Cut(url, basePath)
	if !ok {
		return "", ErrNotFound
	}
	return strings.Trim(id, "/"), nil
}

func validateNewUploadLength(uploadLength, deferLength string, supportsDefer bool) (int64, bool, error) {
	haveBoth := uploadLength != "" && deferLength != ""
	invalidDefer := deferLength != "" && deferLength != "1"
	deferred := deferLength == "1"

	if deferred && !supportsDefer {
		return 0, false, ErrNotImplemented
	}
	if haveBoth {
		return 0, false, ErrUploadLengthAndUploadDeferLength
	}
	if invalidDefer {
		return 0, false, ErrInvalidUploadDeferLength
	}
	if deferred {
		return 0, true, nil
	}
	if uploadLength == "" {
		return 0, false, ErrInvalidUploadLength
	}
	size, err := strconv.ParseInt(uploadLength, 10, 64)
	if err != nil || size < 0 {
		return 0, false, ErrInvalidUploadLength
	}
	return size, false, nil
}

func validateUploadID(id string) error {
	if id == "" {
		return nil
	}
	if strings.HasPrefix(id, "/") || strings.HasSuffix(id, "/") {
		return fmt.Errorf("upload ID must not begin or end with slash: %s", id)
	}
	return nil
}

// getUploadID extracts the upload ID from the request, checking both
// Fiber route params (c.Params) and Locals (for manual middleware routing).
func getUploadID(c *fiber.Ctx) string {
	if id := c.Params("id"); id != "" {
		return id
	}
	if id, ok := c.Locals("id").(string); ok {
		return id
	}
	return ""
}

func filterContentType(info FileInfo) (contentType, contentDisposition string) {
	filetype := info.MetaData["filetype"]
	if ft, _, err := mime.ParseMediaType(filetype); err == nil {
		contentType = filetype
		if isInlineSafe(ft) {
			contentDisposition = "inline"
		} else {
			contentDisposition = "attachment"
		}
	} else {
		contentType = "application/octet-stream"
		contentDisposition = "attachment"
	}
	if filename, ok := info.MetaData["filename"]; ok {
		contentDisposition += ";filename=" + strconv.Quote(filename)
	}
	return
}

var inlineSafeTypes = map[string]struct{}{
	"text/plain": {},
	"image/png": {}, "image/jpeg": {}, "image/gif": {},
	"image/bmp": {}, "image/webp": {},
	"audio/wave": {}, "audio/wav": {}, "audio/x-wav": {}, "audio/x-pn-wav": {},
	"audio/webm": {}, "audio/ogg": {},
	"video/mp4": {}, "video/webm": {}, "video/ogg": {},
	"application/ogg": {},
}

func isInlineSafe(mimeType string) bool {
	_, ok := inlineSafeTypes[mimeType]
	return ok
}
