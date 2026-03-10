/*
Copyright 2026 Specter.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package shortener

import (
	"log/slog"
	"net/http"
	"strings"
)

// =============================================================================
// HTTP HANDLER FOR REDIRECTS
// =============================================================================
//
// Handler serves the redirect endpoint. When someone visits
// https://specter.io/s/{encoded}, this handler decodes and decompresses
// the URL, then sends an HTTP redirect.
//
// =============================================================================

// Handler is an HTTP handler that serves URL redirects for the stateless shortener.
type Handler struct {
	shortener *Shortener
	logger    *slog.Logger
}

// NewHandler creates a new HTTP handler for the URL shortener.
//
// Parameters:
//   - shortener: The Shortener instance to use for expanding URLs
//   - logger: Logger for operational messages (nil disables logging)
//
// Example (with standard net/http):
//
//	shortener := shortener.NewShortener("https://specter.mycompany.io")
//	handler := shortener.NewHandler(shortener, logger)
//	http.Handle("/s/", handler)
//
// Example (mounting on a specific path):
//
//	mux := http.NewServeMux()
//	mux.Handle("/s/", handler)
//	http.ListenAndServe(":8080", mux)
func NewHandler(shortener *Shortener, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(nil, nil))
	}
	return &Handler{
		shortener: shortener,
		logger:    logger,
	}
}

// ServeHTTP handles requests to /s/{encoded}.
// It decodes the URL and redirects to the original destination.
//
// LEARNING NOTE: HTTP redirects use different status codes:
//   - 301 Moved Permanently: Browsers cache this forever
//   - 302 Found: Temporary redirect, no caching
//   - 307 Temporary Redirect: Like 302, preserves HTTP method
//
// We use 302 because:
//   - We don't want browsers to cache aggressively
//   - It's the most widely supported redirect type
//   - Our URLs are deterministic but not truly "permanent"
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Extract short ID from path: /s/{encoded}
	path := strings.TrimPrefix(r.URL.Path, "/s/")
	if path == "" || path == r.URL.Path {
		h.logger.Warn("short URL request with invalid path",
			"path", r.URL.Path,
		)
		http.Error(w, "Invalid short URL", http.StatusBadRequest)
		return
	}

	// Decode and decompress
	originalURL, err := h.shortener.Expand(path)
	if err != nil {
		h.logger.Error("Failed to expand short URL",
			"path", path,
			"error", err,
		)
		http.Error(w, "Invalid or corrupted short URL", http.StatusBadRequest)
		return
	}

	// Validate URL (basic security check)
	// Ensure the URL starts with http:// or https://
	if !strings.HasPrefix(originalURL, "http://") && !strings.HasPrefix(originalURL, "https://") {
		h.logger.Warn("expanded URL has invalid scheme",
			"url", originalURL,
		)
		http.Error(w, "Invalid URL scheme", http.StatusBadRequest)
		return
	}

	h.logger.Debug("Redirected short URL",
		"short_id_length", len(path),
		"original_url_length", len(originalURL),
	)

	// Redirect (HTTP 302 - temporary)
	http.Redirect(w, r, originalURL, http.StatusFound)
}
