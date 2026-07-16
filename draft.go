package tusdfiber

import (
	"mime"
	"net/http"
	"strconv"

	"github.com/gofiber/fiber/v2"
)

// draftVersion represents the IETF resumable upload draft interop version.
type draftVersion string

const (
	interopVersion3 draftVersion = "3" // draft -01
	interopVersion4 draftVersion = "4" // draft -02
	interopVersion5 draftVersion = "5" // draft -03
	interopVersion6 draftVersion = "6" // draft -04 and -05

	uploadLengthDeferred = "1"
)

// getDraftVersion returns the Upload-Draft-Interop-Version from the request, if valid.
func getDraftVersion(c *fiber.Ctx) draftVersion {
	v := draftVersion(c.Get("Upload-Draft-Interop-Version"))
	switch v {
	case interopVersion3, interopVersion4, interopVersion5, interopVersion6:
		return v
	default:
		return ""
	}
}

// usesDraft returns true if the request uses the IETF resumable upload draft.
func (h *UnroutedHandler) usesDraft(c *fiber.Ctx) bool {
	return h.config.EnableExperimentalProtocol && getDraftVersion(c) != ""
}

// isUploadCompleteDraft returns true if the request signals upload completion.
func isUploadCompleteDraft(c *fiber.Ctx) bool {
	v := getDraftVersion(c)
	switch v {
	case interopVersion4, interopVersion5, interopVersion6:
		return c.Get("Upload-Complete") == "?1"
	case interopVersion3:
		return c.Get("Upload-Incomplete") == "?0"
	default:
		return false
	}
}

// getDraftUploadLength returns the upload length from the v2 request.
// It checks Upload-Length and Content-Length headers.
func getDraftUploadLength(c *fiber.Ctx) (length int64, isDeferred bool, err error) {
	var uploadLen int64
	hasUploadLen := false
	var contentLen int64
	hasContentLen := false

	willComplete := isUploadCompleteDraft(c)
	if willComplete && c.Request().Header.ContentLength() >= 0 {
		contentLen = int64(c.Request().Header.ContentLength())
		hasContentLen = true
	}

	uploadLenStr := c.Get("Upload-Length")
	if uploadLenStr != "" {
		var e error
		uploadLen, e = strconv.ParseInt(uploadLenStr, 10, 64)
		if e != nil {
			return 0, false, ErrInvalidUploadLength
		}
		hasUploadLen = true
	}

	// If both are set, they must match
	if hasContentLen && hasUploadLen && uploadLen != contentLen {
		return 0, false, ErrInvalidUploadLength
	}

	if hasUploadLen {
		return uploadLen, false, nil
	}
	if hasContentLen {
		return contentLen, false, nil
	}

	// No length set — deferred
	return 0, true, nil
}

// setDraftCompleteHeaders sets Upload-Complete or Upload-Incomplete on the response.
func setDraftCompleteHeaders(c *fiber.Ctx, v draftVersion, isComplete bool) {
	switch v {
	case interopVersion3:
		if isComplete {
			c.Set("Upload-Incomplete", "?0")
		} else {
			c.Set("Upload-Incomplete", "?1")
		}
	case interopVersion4, interopVersion5, interopVersion6:
		if isComplete {
			c.Set("Upload-Complete", "?1")
		} else {
			c.Set("Upload-Complete", "?0")
		}
	}
}

// ---------------------------------------------------------------------------
// PostFileV2 — IETF resumable upload draft (experimental)
// ---------------------------------------------------------------------------

// PostFileV2 creates an upload using the IETF resumable upload draft protocol.
func (h *UnroutedHandler) PostFileV2(c *fiber.Ctx) error {
	ctx := newHookContext(c, h.config.GracefulRequestCompletionTimeout)
	defer ctx.cancel(nil)

	draftV := getDraftVersion(c)

	// Parse headers
	contentType := string(c.Request().Header.ContentType())
	contentDisposition := c.Get("Content-Disposition")
	willComplete := isUploadCompleteDraft(c)

	info := FileInfo{
		MetaData: make(MetaData),
	}

	size, sizeIsDeferred, err := getDraftUploadLength(c)
	if err != nil {
		return h.writeError(c, ctx, err)
	}
	if !sizeIsDeferred {
		info.Size = size
	} else {
		if !h.composer.UsesLengthDeferrer {
			return h.writeError(c, ctx, ErrNotImplemented)
		}
		info.SizeIsDeferred = true
	}

	// Extract file type / name from Content-Type / Content-Disposition
	if contentType != "" {
		fileType, _, parseErr := mime.ParseMediaType(contentType)
		if parseErr == nil {
			info.MetaData["filetype"] = fileType
		}
	}
	if contentDisposition != "" {
		_, values, parseErr := mime.ParseMediaType(contentDisposition)
		if parseErr == nil && values["filename"] != "" {
			info.MetaData["filename"] = values["filename"]
		}
	}

	resp := HTTPResponse{
		StatusCode: http.StatusCreated,
		Header:     HTTPHeader{},
	}

	// Pre-create callback
	if h.config.PreUploadCreateCallback != nil {
		resp2, changes, cbErr := h.config.PreUploadCreateCallback(newHookEvent(ctx, info))
		if cbErr != nil {
			return h.writeError(c, ctx, cbErr)
		}
		resp = resp.MergeWith(resp2)
		if changes.ID != "" {
			if validErr := validateUploadID(changes.ID); validErr != nil {
				return h.writeError(c, ctx, validErr)
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

	upload, createErr := h.composer.Core.NewUpload(ctx, info)
	if createErr != nil {
		return h.writeError(c, ctx, createErr)
	}

	info, getInfoErr := upload.GetInfo(ctx)
	if getInfoErr != nil {
		return h.writeError(c, ctx, getInfoErr)
	}

	id := info.ID
	url := h.absFileURL(c, id)
	limits := h.getDraftUploadLimits(info)

	// Step 1: Send 104 Early Hints with Location + headers
	// fasthttp doesn't support explicit flushing of interim responses,
	// so we include the Location, Version, and Limits in the final response
	// while still processing the body as the draft requires.

	if h.config.NotifyCreatedUploads {
		h.CreatedUploads <- newHookEvent(ctx, info)
	}

	// Step 2: Lock
	if h.composer.UsesLocker {
		lock, lockErr := h.lockUpload(ctx, id)
		if lockErr != nil {
			return h.writeError(c, ctx, lockErr)
		}
		defer lock.Unlock()
	}

	// Step 3: Write chunk from request body
	resp, writeErr := h.writeChunk(ctx, resp, upload, info, c)
	if writeErr != nil {
		return h.writeError(c, ctx, writeErr)
	}

	// Step 4: Finish upload if Upload-Complete: ?1 and length is deferred
	if willComplete && info.SizeIsDeferred {
		info, getInfoErr = upload.GetInfo(ctx)
		if getInfoErr != nil {
			return h.writeError(c, ctx, getInfoErr)
		}

		uploadLen := info.Offset
		decl := h.composer.LengthDeferrer.AsLengthDeclarableUpload(upload)
		if declErr := decl.DeclareLength(ctx, uploadLen); declErr != nil {
			return h.writeError(c, ctx, declErr)
		}
		info.Size = uploadLen
		info.SizeIsDeferred = false

		resp, writeErr = h.finishUploadIfComplete(ctx, resp, upload, info)
		if writeErr != nil {
			return h.writeError(c, ctx, writeErr)
		}
	}

	// Set draft-specific response headers
	resp.Header["Location"] = url
	resp.Header["Upload-Draft-Interop-Version"] = string(draftV)
	resp.Header["Upload-Limit"] = limits

	return h.sendResp(c, resp)
}

// getDraftUploadLimits returns the Upload-Limit header value.
func (h *UnroutedHandler) getDraftUploadLimits(info FileInfo) string {
	limits := "min-size=0"
	if h.config.MaxSize > 0 {
		limits += ",max-size=" + strconv.FormatInt(h.config.MaxSize, 10)
	} else if !info.SizeIsDeferred {
		limits += ",max-size=" + strconv.FormatInt(info.Size, 10)
	}
	return limits
}

// ---------------------------------------------------------------------------
// Draft-specific middleware modifications
// ---------------------------------------------------------------------------

// draftVersionCheck returns an error if v2 is not enabled but draft headers are present.
func (h *UnroutedHandler) draftVersionCheck(c *fiber.Ctx) error {
	dv := getDraftVersion(c)
	if dv != "" && !h.config.EnableExperimentalProtocol {
		return writeError(c, ErrUnsupportedVersion)
	}
	return nil
}


