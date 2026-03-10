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
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerRedirect(t *testing.T) {
	shortener := NewShortener("https://specter.example.com")
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	handler := NewHandler(shortener, logger)

	testURL := "https://logs.example.com/test/path?param=value"

	// First shorten the URL to get a valid short ID
	shortened, err := shortener.Shorten(testURL)
	if err != nil {
		t.Fatalf("failed to shorten URL: %v", err)
	}

	shortID := strings.TrimPrefix(shortened, "https://specter.example.com/s/")

	// Test the redirect
	req := httptest.NewRequest("GET", "/s/"+shortID, nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	// Check status code
	if w.Code != http.StatusFound {
		t.Errorf("expected status %d, got %d", http.StatusFound, w.Code)
	}

	// Check redirect location
	location := w.Header().Get("Location")
	if location != testURL {
		t.Errorf("expected redirect to %s, got %s", testURL, location)
	}
}

func TestHandlerInvalidPath(t *testing.T) {
	shortener := NewShortener("https://specter.example.com")
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	handler := NewHandler(shortener, logger)

	testCases := []struct {
		name           string
		path           string
		expectedStatus int
	}{
		{
			name:           "empty path",
			path:           "/s/",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "missing /s/ prefix",
			path:           "/invalid",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "invalid base64",
			path:           "/s/not-valid-base64!!!",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "valid base64 but not gzip",
			path:           "/s/SGVsbG8gV29ybGQ",
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tc.path, nil)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			if w.Code != tc.expectedStatus {
				t.Errorf("expected status %d, got %d", tc.expectedStatus, w.Code)
			}
		})
	}
}

func TestHandlerInvalidScheme(t *testing.T) {
	// This tests the scheme validation in the handler
	// We need to manually create a corrupted short ID that decodes to an invalid URL

	shortener := NewShortener("https://specter.example.com")
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	handler := NewHandler(shortener, logger)

	// Create a short ID for a URL with invalid scheme
	invalidURL := "javascript:alert('xss')"
	shortened, err := shortener.Shorten(invalidURL)
	if err != nil {
		t.Fatalf("failed to shorten URL: %v", err)
	}

	shortID := strings.TrimPrefix(shortened, "https://specter.example.com/s/")

	req := httptest.NewRequest("GET", "/s/"+shortID, nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	// Should reject URLs without http/https scheme
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status %d for invalid scheme, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestHandlerMultipleRedirects(t *testing.T) {
	shortener := NewShortener("https://specter.example.com")
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	handler := NewHandler(shortener, logger)

	urls := []string{
		"https://logs.example.com/test1",
		"https://logs.example.com/test2",
		"https://grafana.example.com/dashboard",
	}

	for _, testURL := range urls {
		t.Run(testURL, func(t *testing.T) {
			// Shorten
			shortened, err := shortener.Shorten(testURL)
			if err != nil {
				t.Fatalf("failed to shorten URL: %v", err)
			}

			shortID := strings.TrimPrefix(shortened, "https://specter.example.com/s/")

			// Redirect
			req := httptest.NewRequest("GET", "/s/"+shortID, nil)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			if w.Code != http.StatusFound {
				t.Errorf("expected status %d, got %d", http.StatusFound, w.Code)
			}

			location := w.Header().Get("Location")
			if location != testURL {
				t.Errorf("expected redirect to %s, got %s", testURL, location)
			}
		})
	}
}
