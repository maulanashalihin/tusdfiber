package tusdfiber

import (
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Config configures the TUS handler for Fiber.
type Config struct {
	// StoreComposer composes the core data store and optional extensions.
	StoreComposer *StoreComposer

	// MaxSize is the maximum upload size in bytes (0 = no limit).
	MaxSize int64

	// BasePath is the URL prefix for uploads, e.g. "/files/".
	// If no trailing slash is present, one will be added.
	BasePath string
	isAbs    bool

	// DisableDownload removes the GET / download endpoint.
	DisableDownload bool

	// DisableTermination removes the DELETE endpoint.
	DisableTermination bool

	// DisableConcatenation rejects Upload-Concat headers.
	DisableConcatenation bool

	// NotifyCompleteUploads enables the CompleteUploads notification channel.
	NotifyCompleteUploads bool

	// NotifyTerminatedUploads enables the TerminatedUploads notification channel.
	NotifyTerminatedUploads bool

	// NotifyUploadProgress enables the UploadProgress notification channel.
	NotifyUploadProgress bool

	// NotifyCreatedUploads enables the CreatedUploads notification channel.
	NotifyCreatedUploads bool

	// UploadProgressInterval controls how often progress notifications are emitted.
	UploadProgressInterval time.Duration

	// RespectForwardedHeaders makes the handler read X-Forwarded-* / Forwarded headers.
	RespectForwardedHeaders bool

	// PreUploadCreateCallback is called before a new upload is created.
	// Return an error to reject the upload.
	PreUploadCreateCallback func(hook HookEvent) (HTTPResponse, FileInfoChanges, error)

	// PreFinishResponseCallback is called after an upload is completed.
	PreFinishResponseCallback func(hook HookEvent) (HTTPResponse, error)

	// PreUploadTerminateCallback is called before an upload is terminated.
	PreUploadTerminateCallback func(hook HookEvent) (HTTPResponse, error)

	// GracefulRequestCompletionTimeout is the time given to stores after a request ends.
	GracefulRequestCompletionTimeout time.Duration

	// AcquireLockTimeout is the maximum time to wait for an upload lock.
	AcquireLockTimeout time.Duration

	// NetworkTimeout is the read deadline for individual body reads.
	NetworkTimeout time.Duration

	// EnableExperimentalProtocol enables the IETF resumable upload draft
	// (Upload-Draft-Interop-Version) next to the TUS v1 protocol.
	EnableExperimentalProtocol bool

	// CORS configuration. If nil, DefaultCORSConfig is used.
	CORS *CORSConfig
}

// CORSConfig controls Cross-Origin Resource Sharing behaviour.
type CORSConfig struct {
	// Disable skips all CORS handling.
	Disable bool

	// AllowOrigin is a regex that the Origin header must match.
	AllowOrigin *regexp.Regexp

	// AllowCredentials includes Access-Control-Allow-Credentials: true.
	AllowCredentials bool

	// AllowMethods is the value of Access-Control-Allow-Methods.
	AllowMethods string

	// AllowHeaders is the value of Access-Control-Allow-Headers.
	AllowHeaders string

	// MaxAge is the value of Access-Control-Max-Age.
	MaxAge string

	// ExposeHeaders is the value of Access-Control-Expose-Headers.
	ExposeHeaders string
}

// DefaultCORSConfig is used when Config.CORS is nil.
var DefaultCORSConfig = CORSConfig{
	Disable:          false,
	AllowOrigin:      regexp.MustCompile(".*"),
	AllowCredentials: false,
	AllowMethods:     "POST, HEAD, PATCH, OPTIONS, GET, DELETE",
	AllowHeaders: "Authorization, Origin, X-Requested-With, X-Request-ID, " +
		"X-HTTP-Method-Override, Content-Type, Upload-Length, Upload-Offset, " +
		"Tus-Resumable, Upload-Metadata, Upload-Defer-Length, Upload-Concat, " +
		"Upload-Incomplete, Upload-Complete, Upload-Draft-Interop-Version",
	MaxAge: "86400",
	ExposeHeaders: "Upload-Offset, Location, Upload-Length, Tus-Version, " +
		"Tus-Resumable, Tus-Max-Size, Tus-Extension, Upload-Metadata, " +
		"Upload-Defer-Length, Upload-Concat, Upload-Incomplete, " +
		"Upload-Complete, Upload-Draft-Interop-Version",
}

// validate normalises Config and checks required fields.
func (cfg *Config) validate() error {
	base := cfg.BasePath
	uri, err := url.Parse(base)
	if err != nil {
		return err
	}
	if base != "" && !strings.HasSuffix(base, "/") {
		base += "/"
	}
	if !uri.IsAbs() && len(base) > 0 && !strings.HasPrefix(base, "/") {
		base = "/" + base
	}
	cfg.BasePath = base
	cfg.isAbs = uri.IsAbs()

	if cfg.StoreComposer == nil || cfg.StoreComposer.Core == nil {
		return NewError("ERR_CONFIG", "StoreComposer with a non-nil Core data store is required", 0)
	}

	if cfg.UploadProgressInterval <= 0 {
		cfg.UploadProgressInterval = 1 * time.Second
	}
	if cfg.GracefulRequestCompletionTimeout <= 0 {
		cfg.GracefulRequestCompletionTimeout = 10 * time.Second
	}
	if cfg.AcquireLockTimeout <= 0 {
		cfg.AcquireLockTimeout = 20 * time.Second
	}
	if cfg.NetworkTimeout <= 0 {
		cfg.NetworkTimeout = 60 * time.Second
	}
	if cfg.CORS == nil {
		cc := DefaultCORSConfig // copy
		cfg.CORS = &cc
	}

	return nil
}
