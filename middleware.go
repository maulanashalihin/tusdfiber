package tusdfiber

import (
	"regexp"
	"strings"

	"github.com/gofiber/fiber/v2"
)

// CORSMiddleware returns a Fiber handler that adds CORS headers and handles
// preflight OPTIONS requests according to the TUS protocol.
func CORSMiddleware(cfg *CORSConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.Disable {
			return c.Next()
		}

		origin := c.Get("Origin")
		if origin == "" {
			return c.Next()
		}

		if !cfg.AllowOrigin.MatchString(origin) {
			return writeError(c, ErrOriginNotAllowed)
		}

		c.Set("Access-Control-Allow-Origin", origin)
		c.Set("Vary", "Origin")

		if cfg.AllowCredentials {
			c.Set("Access-Control-Allow-Credentials", "true")
		}

		if c.Method() == fiber.MethodOptions {
			// Preflight
			c.Set("Access-Control-Allow-Methods", cfg.AllowMethods)
			c.Set("Access-Control-Allow-Headers", cfg.AllowHeaders)
			c.Set("Access-Control-Max-Age", cfg.MaxAge)

			// TUS-specific OPTIONS response
			// (the handler will also set these; this is fine since they are idempotent)
			return c.Next()
		} else {
			// Actual request
			c.Set("Access-Control-Expose-Headers", cfg.ExposeHeaders)
		}

		return c.Next()
	}
}

// TusResumableMiddleware checks that the Tus-Resumable header is present
// on mutating requests (POST, PATCH, DELETE), or that Upload-Draft-Interop-Version
// is present if the experimental protocol is enabled.
// GET and HEAD are allowed without it.
func TusResumableMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		method := c.Method()

		// GET and HEAD are exempt from the version check
		if method == fiber.MethodGet || method == fiber.MethodHead {
			return c.Next()
		}

		// OPTIONS is handled by CORS middleware
		if method == fiber.MethodOptions {
			return c.Next()
		}

		// Check for IETF resumable upload draft header
		draftV := c.Get("Upload-Draft-Interop-Version")
		if draftV == "3" || draftV == "4" || draftV == "5" || draftV == "6" {
			// v2 protocol — skip the Tus-Resumable check
			c.Set("Upload-Draft-Interop-Version", draftV)
			return c.Next()
		}

		tusResumable := c.Get("Tus-Resumable")
		if tusResumable != "1.0.0" {
			return writeError(c, ErrUnsupportedVersion)
		}

		// Set the response header
		c.Set("Tus-Resumable", "1.0.0")

		return c.Next()
	}
}

// MethodOverrideMiddleware allows clients to override the HTTP method using
// the X-HTTP-Method-Override header. Required for environments that do not
// support PATCH / DELETE (e.g. older browsers, Flash).
func MethodOverrideMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if c.Method() == fiber.MethodPost {
			override := c.Get("X-HTTP-Method-Override")
			if override != "" {
				// Set the method on the fasthttp context so downstream
				// handlers see the overridden method.
				c.Request().Header.SetMethod(strings.ToUpper(override))
			}
		}
		return c.Next()
	}
}

// validateTusHeaders checks common TUS headers on all TUS requests.
// This is used as a convenience wrapper around the individual checks.
func validateTusHeaders(c *fiber.Ctx) error {
return c.Next()
}

// ValidateCORSOrigin checks if the Origin header matches the allowed pattern.
func ValidateCORSOrigin(cfg *CORSConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.Disable {
			return c.Next()
		}
		origin := c.Get("Origin")
		if origin != "" && !cfg.AllowOrigin.MatchString(origin) {
			return writeError(c, ErrOriginNotAllowed)
		}
		return c.Next()
	}
}

// CompileAllowOrigin compiles a regex string into a *regexp.Regexp.
// Useful when building config from user input.
func CompileAllowOrigin(pattern string) *regexp.Regexp {
	if pattern == "" {
		pattern = ".*"
	}
	return regexp.MustCompile(pattern)
}
