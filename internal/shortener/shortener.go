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
//   1. Slack truncates long URLs, breaking the links entirely
//   2. Some notification systems (SMS, certain chat systems) have URL length limits
//   3. Long URLs are impossible to share verbally or copy manually
//   4. They make alert notifications harder to read
//
// THE SOLUTION:
// Instead of embedding the full URL in the alert annotation, we:
//   1. Store the full URL in memory with a short ID
//   2. Return a short redirect URL like: https://specter.company.io/s/abc123
//   3. When someone clicks it, Specter looks up the original URL and redirects
//
// ARCHITECTURE:
//   - Store: In-memory storage mapping short IDs to full URLs (with expiration)
//   - Handler: HTTP handler that performs the redirect
//   - generateID: Creates deterministic, URL-safe IDs from URL content
//
// TRADEOFFS:
//   - In-memory storage is lost on restart (but URLs are recreated on next reconcile)
//   - Multiple replicas don't share state (could add Redis if needed)
//   - Simple and fast for most use cases
package shortener

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// =============================================================================
// URL STORE
// =============================================================================
//
// Store maps short IDs to full URLs. It's thread-safe and supports automatic
// expiration of old URLs to bound memory usage.
//
// DESIGN DECISION: We use an in-memory store for simplicity.
//
// For production environments with multiple Specter replicas, consider:
//   - Redis for shared state across replicas
//   - A database for persistence across restarts
//
// For most use cases, in-memory is fine because:
//   - URLs are recreated on each PrometheusRule reconciliation
//   - Missing URLs just mean the user clicks a "not found" page
//   - Short TTL (default 7 days) keeps memory usage bounded
//   - Hash-based IDs mean the same URL always gets the same short ID
//
// =============================================================================

// Entry stores a URL with its metadata.
type Entry struct {
	// OriginalURL is the full, unshortened URL.
	OriginalURL string

	// CreatedAt is when this entry was first created.
	CreatedAt time.Time

	// ExpiresAt is when this entry should be deleted.
	ExpiresAt time.Time

	// AccessCount tracks how many times this URL was accessed via redirect.
	// Useful for metrics and understanding usage patterns.
	AccessCount int64

	// Labels stores the alert labels that generated this URL.
	// Useful for debugging which alert created which short URL.
	Labels map[string]string
}

// Store is a thread-safe in-memory store for shortened URLs.
type Store struct {
	// mu protects concurrent access to the entries map.
	//
	// LEARNING NOTE: Go maps are NOT thread-safe. When multiple goroutines
	// read and write a map simultaneously, you get race conditions.
	// sync.RWMutex provides:
	//   - RLock/RUnlock: Multiple readers can hold the lock simultaneously
	//   - Lock/Unlock: Only one writer can hold the lock, blocks all readers
	//
	// We use RLock for lookups (frequent) and Lock for writes (less frequent).
	mu sync.RWMutex

	// entries maps short IDs to URL entries.
	entries map[string]*Entry

	// defaultTTL is how long URLs live before expiring.
	defaultTTL time.Duration

	// baseURL is the external URL prefix for short links.
	// Example: "https://specter.mycompany.io"
	baseURL string

	// cleanupInterval controls how often we scan for expired entries.
	cleanupInterval time.Duration

	// cleanupTicker triggers periodic cleanup of expired URLs.
	cleanupTicker *time.Ticker

	// stopCleanup signals the cleanup goroutine to exit.
	stopCleanup chan struct{}

	// logger for operational messages.
	logger *slog.Logger
}

// Config holds configuration for creating a new Store.
type Config struct {
	// BaseURL is the external URL where Specter's shortener is accessible.
	// Required. Example: "https://specter.mycompany.io"
	BaseURL string

	// DefaultTTL is how long URLs live before expiring.
	// Default: 7 days (168h)
	DefaultTTL time.Duration

	// CleanupInterval is how often to scan for and remove expired entries.
	// Default: 1 hour
	CleanupInterval time.Duration

	// Logger for operational messages. If nil, logging is disabled.
	Logger *slog.Logger
}

// NewStore creates a new URL store with the given configuration.
//
// Example:
//
//	store := shortener.NewStore(shortener.Config{
//	    BaseURL:    "https://specter.mycompany.io",
//	    DefaultTTL: 7 * 24 * time.Hour,
//	    Logger:     slog.Default(),
//	})
//	defer store.Stop() // Always stop to clean up the background goroutine
func NewStore(cfg Config) *Store {
	if cfg.DefaultTTL == 0 {
		cfg.DefaultTTL = 7 * 24 * time.Hour // 7 days default
	}
	if cfg.CleanupInterval == 0 {
		cfg.CleanupInterval = 1 * time.Hour
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}

	store := &Store{
		entries:         make(map[string]*Entry),
		defaultTTL:      cfg.DefaultTTL,
		baseURL:         cfg.BaseURL,
		cleanupInterval: cfg.CleanupInterval,
		stopCleanup:     make(chan struct{}),
		logger:          cfg.Logger,
	}

	// Start background cleanup goroutine.
	//
	// LEARNING NOTE: This is a common Go pattern - a background goroutine
	// that periodically performs maintenance. We use:
	//   - time.Ticker for regular intervals
	//   - A stop channel for graceful shutdown
	//   - select to handle both tick events and stop signals
	store.cleanupTicker = time.NewTicker(cfg.CleanupInterval)
	go store.cleanupLoop()

	store.logger.Info("URL shortener store initialized",
		slog.String("base_url", cfg.BaseURL),
		slog.Duration("ttl", cfg.DefaultTTL),
	)

	return store
}

// cleanupLoop runs in a goroutine and periodically removes expired URLs.
func (s *Store) cleanupLoop() {
	for {
		select {
		case <-s.cleanupTicker.C:
			removed := s.removeExpired()
			if removed > 0 {
				s.logger.Info("cleaned up expired URLs",
					slog.Int("removed", removed),
					slog.Int("remaining", s.Len()),
				)
			}
		case <-s.stopCleanup:
			s.logger.Debug("cleanup goroutine stopped")
			return
		}
	}
}

// removeExpired deletes all URLs that have passed their expiration time.
// Returns the number of entries removed.
func (s *Store) removeExpired() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	removed := 0

	for id, entry := range s.entries {
		if entry.ExpiresAt.Before(now) {
			delete(s.entries, id)
			removed++
		}
	}

	return removed
}

// Stop shuts down the background cleanup goroutine.
// Always call this when done with the store to prevent goroutine leaks.
//
// LEARNING NOTE: Goroutine leaks are a common bug in Go. If you start a
// goroutine that runs forever (like our cleanup loop), you must provide
// a way to stop it. The stop channel pattern is idiomatic Go.
func (s *Store) Stop() {
	s.cleanupTicker.Stop()
	close(s.stopCleanup)
	s.logger.Info("URL shortener store stopped")
}

// =============================================================================
// STORE OPERATIONS
// =============================================================================

// Shorten creates a short URL for the given original URL.
//
// Parameters:
//   - originalURL: The full URL to shorten
//   - labels: Optional labels from the alert (stored for debugging)
//
// Returns:
//   - The complete short URL (e.g., "https://specter.io/s/abc123")
//
// Example:
//
//	shortURL := store.Shorten(
//	    "https://opensearch.io/very/long/url/with/rison/encoding...",
//	    map[string]string{"service": "billing-api", "alertname": "HighLatency"},
//	)
//	// shortURL = "https://specter.mycompany.io/s/7f83b16"
//
// IDEMPOTENCY: The same original URL always produces the same short ID
// (because we hash the URL). This prevents duplicate entries and makes
// the system predictable.
func (s *Store) Shorten(originalURL string, labels map[string]string) string {
	// Generate a deterministic short ID from the URL.
	//
	// DESIGN DECISION: Using a hash means:
	//   - Same URL always gets the same short ID (idempotent)
	//   - No need to check for duplicates
	//   - Collisions are extremely unlikely with SHA-256
	id := generateID(originalURL)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if this URL is already stored.
	// If so, just refresh the expiration time.
	if entry, exists := s.entries[id]; exists {
		entry.ExpiresAt = time.Now().Add(s.defaultTTL)
		s.logger.Debug("refreshed existing short URL",
			slog.String("id", id),
			slog.Time("new_expiry", entry.ExpiresAt),
		)
		return s.formatShortURL(id)
	}

	// Store the new URL.
	s.entries[id] = &Entry{
		OriginalURL: originalURL,
		CreatedAt:   time.Now(),
		ExpiresAt:   time.Now().Add(s.defaultTTL),
		AccessCount: 0,
		Labels:      labels,
	}

	s.logger.Debug("created new short URL",
		slog.String("id", id),
		slog.Int("url_length", len(originalURL)),
		slog.Any("labels", labels),
	)

	return s.formatShortURL(id)
}

// Lookup retrieves the original URL for a short ID.
//
// Parameters:
//   - id: The short ID (the part after /s/ in the URL)
//
// Returns:
//   - The original URL and true if found and not expired
//   - Empty string and false if not found or expired
func (s *Store) Lookup(id string) (string, bool) {
	s.mu.RLock()
	entry, exists := s.entries[id]
	s.mu.RUnlock()

	if !exists {
		return "", false
	}

	// Check if expired (lazy expiration).
	if entry.ExpiresAt.Before(time.Now()) {
		// Entry is expired, remove it.
		s.mu.Lock()
		delete(s.entries, id)
		s.mu.Unlock()
		return "", false
	}

	// Increment access count (best-effort, not critical for correctness).
	s.mu.Lock()
	entry.AccessCount++
	s.mu.Unlock()

	return entry.OriginalURL, true
}

// formatShortURL creates the full short URL from an ID.
func (s *Store) formatShortURL(id string) string {
	return fmt.Sprintf("%s/s/%s", s.baseURL, id)
}

// Len returns the current number of stored URLs.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

// Stats returns statistics about the URL store.
type Stats struct {
	TotalURLs     int
	TotalAccesses int64
	OldestEntry   time.Time
	NewestEntry   time.Time
}

// GetStats returns current statistics about the store.
// Useful for monitoring and debugging.
func (s *Store) GetStats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := Stats{}

	for _, entry := range s.entries {
		stats.TotalURLs++
		stats.TotalAccesses += entry.AccessCount

		if stats.OldestEntry.IsZero() || entry.CreatedAt.Before(stats.OldestEntry) {
			stats.OldestEntry = entry.CreatedAt
		}
		if entry.CreatedAt.After(stats.NewestEntry) {
			stats.NewestEntry = entry.CreatedAt
		}
	}

	return stats
}

// =============================================================================
// SHORT ID GENERATION
// =============================================================================
//
// We use a hash of the URL to generate deterministic, short IDs.
// Benefits:
//   - Same URL always gets same ID (idempotent)
//   - IDs are URL-safe (base64url encoding)
//   - Collisions extremely unlikely with SHA-256
//
// =============================================================================

// generateID creates a short, URL-safe ID from a URL.
// Uses SHA-256 hash truncated to 8 characters for brevity.
//
// LEARNING NOTE: We use only 8 characters of the base64-encoded hash.
// This gives us 48 bits of entropy, which means:
//   - 2^48 = ~281 trillion possible IDs
//   - Birthday problem: 50% collision chance at ~17 million URLs
//   - For a URL shortener, this is more than sufficient
//
// If you need more uniqueness, increase the substring length.
func generateID(url string) string {
	// Hash the URL using SHA-256.
	hash := sha256.Sum256([]byte(url))

	// Encode as base64url (URL-safe base64 without padding).
	//
	// LEARNING NOTE: Standard base64 uses + and / which are NOT URL-safe.
	// base64url (RFC 4648) replaces them:
	//   + -> -
	//   / -> _
	// RawURLEncoding omits the = padding.
	encoded := base64.RawURLEncoding.EncodeToString(hash[:])

	// Return only the first 8 characters.
	return encoded[:8]
}

// =============================================================================
// HTTP HANDLER
// =============================================================================
//
// Handler serves the redirect endpoint. When someone visits
// https://specter.io/s/abc123, this handler looks up the original URL
// and sends an HTTP redirect.
//
// =============================================================================

// Handler is an HTTP handler that serves URL redirects.
type Handler struct {
	store  *Store
	logger *slog.Logger
}

// NewHandler creates a new HTTP handler for the URL shortener.
//
// Example (with standard net/http):
//
//	store := shortener.NewStore(cfg)
//	handler := shortener.NewHandler(store, logger)
//	http.Handle("/s/", handler)
//
// Example (with controller-runtime):
//
//	mgr.AddMetricsServerExtraHandler("/s/", handler)
func NewHandler(store *Store, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Handler{
		store:  store,
		logger: logger,
	}
}

// ServeHTTP handles requests to /s/{id}.
// It looks up the short ID and redirects to the original URL.
//
// LEARNING NOTE: HTTP redirects use different status codes:
//   - 301 Moved Permanently: Browsers cache this forever
//   - 302 Found: Temporary redirect, no caching
//   - 307 Temporary Redirect: Like 302, preserves HTTP method
//
// We use 302 because:
//   - We don't want browsers to cache (URL might expire)
//   - It's the most widely supported redirect type
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Extract the ID from the URL path.
	// Expected path format: /s/{id}
	path := r.URL.Path
	id := ""

	// Handle both "/s/abc123" and "abc123" (if mounted at /s/)
	if len(path) > 3 && path[:3] == "/s/" {
		id = path[3:]
	} else if len(path) > 0 && path[0] != '/' {
		id = path
	} else if len(path) > 1 {
		id = path[1:]
	}

	if id == "" {
		h.logger.Warn("short URL request with empty ID",
			slog.String("path", path),
		)
		http.Error(w, "Missing URL ID", http.StatusBadRequest)
		return
	}

	// Look up the original URL.
	originalURL, found := h.store.Lookup(id)
	if !found {
		h.logger.Debug("short URL not found or expired",
			slog.String("id", id),
		)
		http.Error(w, "URL not found or expired", http.StatusNotFound)
		return
	}

	h.logger.Debug("redirecting short URL",
		slog.String("id", id),
		slog.String("target", originalURL),
	)

	// Redirect to the original URL.
	http.Redirect(w, r, originalURL, http.StatusFound)
}

// =============================================================================
// CONDITIONAL SHORTENING HELPER
// =============================================================================

// ShortenIfNeeded shortens a URL only if it exceeds the maximum length.
// This avoids unnecessary redirects for already-short URLs.
//
// Parameters:
//   - store: The URL store to use for shortening
//   - url: The URL to potentially shorten
//   - maxLength: Maximum URL length before shortening kicks in
//   - labels: Labels for debugging (passed to Store.Shorten)
//
// Returns:
//   - The original URL if under maxLength
//   - A short URL if over maxLength
//
// Example:
//
//	finalURL := shortener.ShortenIfNeeded(store, longURL, 200, labels)
func ShortenIfNeeded(store *Store, url string, maxLength int, labels map[string]string) string {
	if len(url) <= maxLength {
		return url
	}
	return store.Shorten(url, labels)
}
