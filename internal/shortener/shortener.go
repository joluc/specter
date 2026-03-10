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

// Package shortener provides URL shortening functionality for Specter.
//
// LEARNING NOTE: Why do we need URL shortening?
//
// Observability tools like OpenSearch generate extremely long URLs because they
// encode complex queries directly in the URL (using RISON or base64 encoding).
// These URLs can be 1000+ characters, which causes problems:
//
//  1. Slack truncates long URLs, breaking the links entirely
//  2. Some notification systems (SMS, certain chat systems) have URL length limits
//  3. Long URLs are impossible to share verbally or copy manually
//  4. They make alert notifications harder to read
//
// THE SOLUTION (STATELESS APPROACH):
// Instead of storing URLs in memory/disk/database, we encode the URL directly
// into the short link using compression + base64 encoding:
//
//  1. Compress the URL with gzip (1200 chars -> ~300 bytes)
//  2. Encode with base64url (URL-safe) -> ~400 chars
//  3. Return a short URL like: https://specter.company.io/s/{encoded}
//  4. When clicked, decode and decompress to get the original URL
//
// ARCHITECTURE:
//   - Shortener: Compresses and encodes URLs (stateless, no storage)
//   - Handler: HTTP handler that decompresses and redirects
//
// BENEFITS:
//   - No storage needed (no memory, no disk, no database)
//   - No cleanup/expiration logic
//   - Works across multiple replicas (no shared state needed)
//   - Survives restarts (nothing to lose)
//   - Simple and reliable
//
// TRADEOFFS:
//   - "Short" URLs are ~400 chars (not as short as 8-char hash IDs)
//   - Still much shorter than original (67% reduction: 1200 -> 400)
//   - No access analytics (can't track clicks)
//   - Perfect for PagerDuty and most notification systems
package shortener

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"io"
)

// =============================================================================
// STATELESS SHORTENER
// =============================================================================
//
// Shortener compresses and encodes URLs without any storage.
// Each "short" URL contains the original URL itself, just compressed.
//
// =============================================================================

// Shortener provides stateless URL shortening via compression and encoding.
type Shortener struct {
	// baseURL is the external URL prefix for short links.
	// Example: "https://specter.mycompany.io"
	baseURL string
}

// NewShortener creates a new stateless URL shortener.
//
// Parameters:
//   - baseURL: The external URL where Specter's shortener is accessible.
//     Example: "https://specter.mycompany.io"
//
// Example:
//
//	shortener := shortener.NewShortener("https://specter.mycompany.io")
//	shortURL, err := shortener.Shorten("https://opensearch.io/very/long/url...")
func NewShortener(baseURL string) *Shortener {
	return &Shortener{
		baseURL: baseURL,
	}
}

// Shorten compresses and encodes a URL into a shorter form.
//
// Parameters:
//   - originalURL: The full URL to shorten
//
// Returns:
//   - The complete short URL (e.g., "https://specter.io/s/{encoded}")
//   - Error if compression or encoding fails
//
// Example:
//
//	shortURL, err := shortener.Shorten(
//	    "https://opensearch.io/very/long/url/with/rison/encoding...",
//	)
//	// shortURL = "https://specter.mycompany.io/s/H4sIAAAA..."
//
// PROCESS:
//  1. Compress URL with gzip (reduces size ~75%)
//  2. Base64url encode (makes it URL-safe)
//  3. Return full short URL with encoded payload
func (s *Shortener) Shorten(originalURL string) (string, error) {
	// 1. Compress with gzip
	var buf bytes.Buffer
	gzWriter := gzip.NewWriter(&buf)
	if _, err := gzWriter.Write([]byte(originalURL)); err != nil {
		return "", fmt.Errorf("gzip write: %w", err)
	}
	if err := gzWriter.Close(); err != nil {
		return "", fmt.Errorf("gzip close: %w", err)
	}

	// 2. Base64url encode (URL-safe, no padding)
	//
	// LEARNING NOTE: Standard base64 uses + and / which are NOT URL-safe.
	// RawURLEncoding (RFC 4648) uses - and _ instead, and omits = padding.
	encoded := base64.RawURLEncoding.EncodeToString(buf.Bytes())

	// 3. Return short URL
	return fmt.Sprintf("%s/s/%s", s.baseURL, encoded), nil
}

// Expand decompresses and decodes a short ID back to the original URL.
//
// Parameters:
//   - shortID: The encoded portion of the short URL (after /s/)
//
// Returns:
//   - The original URL
//   - Error if decoding or decompression fails
//
// Example:
//
//	originalURL, err := shortener.Expand("H4sIAAAA...")
//	// originalURL = "https://opensearch.io/very/long/url..."
//
// PROCESS:
//  1. Base64url decode
//  2. Decompress with gzip
//  3. Return original URL string
func (s *Shortener) Expand(shortID string) (string, error) {
	// 1. Base64url decode
	compressed, err := base64.RawURLEncoding.DecodeString(shortID)
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}

	// 2. Decompress with gzip
	gzReader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return "", fmt.Errorf("gzip reader: %w", err)
	}
	defer func() {
		if closeErr := gzReader.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("gzip close: %w", closeErr)
		}
	}()

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, gzReader); err != nil {
		return "", fmt.Errorf("gzip decompress: %w", err)
	}

	return buf.String(), nil
}

// ShortenIfNeeded only shortens the URL if it exceeds the maximum length.
// This avoids unnecessary encoding for already-short URLs.
//
// Parameters:
//   - url: The URL to potentially shorten
//   - maxLength: Maximum URL length before shortening kicks in
//
// Returns:
//   - The original URL if under maxLength
//   - A short URL if over maxLength
//   - Error is returned but original URL is still valid (fail-safe)
//
// Example:
//
//	finalURL, err := shortener.ShortenIfNeeded(longURL, 200)
//	if err != nil {
//	    // Still use finalURL (will be original URL)
//	    log.Warn("failed to shorten URL", "error", err)
//	}
//
// DESIGN: If shortening fails, we return the original URL rather than
// erroring out completely. This ensures alerts still have a (long) working
// link rather than no link at all.
func (s *Shortener) ShortenIfNeeded(url string, maxLength int) (string, error) {
	if len(url) <= maxLength {
		return url, nil // Already short enough
	}

	shortened, err := s.Shorten(url)
	if err != nil {
		return url, err // Fall back to original on error
	}

	return shortened, nil
}
