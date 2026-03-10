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
	"strings"
	"testing"
)

func TestShorten(t *testing.T) {
	shortener := NewShortener("https://specter.example.com")

	testCases := []struct {
		name        string
		url         string
		expectError bool
	}{
		{
			name:        "short URL",
			url:         "https://example.com/test",
			expectError: false,
		},
		{
			name:        "long URL",
			url:         "https://logs.example.com/app/data-explorer/discover#?_a=%28discover%3A%28columns%3A%21%28_source%29%2CisDirty%3A%21f%29%29&_g=%28filters%3A%21%28%29%2Ctime%3A%28from%3Anow-1h%2Cto%3Anow%29%29",
			expectError: false,
		},
		{
			name:        "very long URL",
			url:         strings.Repeat("https://example.com/very/long/path/segment/", 50),
			expectError: false,
		},
		{
			name:        "empty URL",
			url:         "",
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			shortened, err := shortener.Shorten(tc.url)
			if tc.expectError && err == nil {
				t.Errorf("expected error but got none")
			}
			if !tc.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if err == nil {
				if !strings.HasPrefix(shortened, "https://specter.example.com/s/") {
					t.Errorf("shortened URL has wrong prefix: %s", shortened)
				}
				// Verify it's actually shorter for long URLs
				if len(tc.url) > 200 && len(shortened) >= len(tc.url) {
					t.Errorf("shortened URL is not shorter: original=%d, shortened=%d", len(tc.url), len(shortened))
				}
			}
		})
	}
}

func TestExpand(t *testing.T) {
	shortener := NewShortener("https://specter.example.com")

	testCases := []struct {
		name        string
		url         string
		expectError bool
	}{
		{
			name:        "short URL",
			url:         "https://example.com/test",
			expectError: false,
		},
		{
			name:        "long URL with special characters",
			url:         "https://logs.example.com/app?param=value&other=123#fragment",
			expectError: false,
		},
		{
			name:        "URL with unicode",
			url:         "https://example.com/path/文字/test",
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// First shorten
			shortened, err := shortener.Shorten(tc.url)
			if err != nil {
				t.Fatalf("failed to shorten: %v", err)
			}

			// Extract short ID from URL
			shortID := strings.TrimPrefix(shortened, "https://specter.example.com/s/")

			// Then expand
			expanded, err := shortener.Expand(shortID)
			if tc.expectError && err == nil {
				t.Errorf("expected error but got none")
			}
			if !tc.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if err == nil && expanded != tc.url {
				t.Errorf("expanded URL doesn't match original:\noriginal: %s\nexpanded: %s", tc.url, expanded)
			}
		})
	}
}

func TestExpandInvalid(t *testing.T) {
	shortener := NewShortener("https://specter.example.com")

	testCases := []struct {
		name    string
		shortID string
	}{
		{
			name:    "invalid base64",
			shortID: "not-valid-base64!!!",
		},
		{
			name:    "valid base64 but not gzip",
			shortID: "SGVsbG8gV29ybGQ",
		},
		{
			name:    "empty string",
			shortID: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := shortener.Expand(tc.shortID)
			if err == nil {
				t.Errorf("expected error for invalid input but got none")
			}
		})
	}
}

func TestShortenIfNeeded(t *testing.T) {
	shortener := NewShortener("https://specter.example.com")

	testCases := []struct {
		name          string
		url           string
		maxLength     int
		expectShorten bool
	}{
		{
			name:          "short URL below threshold",
			url:           "https://example.com/test",
			maxLength:     200,
			expectShorten: false,
		},
		{
			name:          "long URL above threshold",
			url:           strings.Repeat("https://example.com/very/long/path/", 10),
			maxLength:     200,
			expectShorten: true,
		},
		{
			name:          "URL exactly at threshold",
			url:           strings.Repeat("a", 200),
			maxLength:     200,
			expectShorten: false,
		},
		{
			name:          "URL one char over threshold",
			url:           strings.Repeat("a", 201),
			maxLength:     200,
			expectShorten: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := shortener.ShortenIfNeeded(tc.url, tc.maxLength)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			if tc.expectShorten {
				if result == tc.url {
					t.Errorf("expected URL to be shortened but it wasn't")
				}
				if !strings.HasPrefix(result, "https://specter.example.com/s/") {
					t.Errorf("shortened URL has wrong prefix: %s", result)
				}
			} else {
				if result != tc.url {
					t.Errorf("expected URL to remain unchanged but it was modified")
				}
			}
		})
	}
}

func TestShortenDeterministic(t *testing.T) {
	shortener := NewShortener("https://specter.example.com")

	url := "https://example.com/test/path?param=value"

	// Shorten the same URL multiple times
	shortened1, err1 := shortener.Shorten(url)
	shortened2, err2 := shortener.Shorten(url)
	shortened3, err3 := shortener.Shorten(url)

	if err1 != nil || err2 != nil || err3 != nil {
		t.Fatalf("errors during shortening: %v, %v, %v", err1, err2, err3)
	}

	// All results should be identical (deterministic)
	if shortened1 != shortened2 || shortened2 != shortened3 {
		t.Errorf("shortening is not deterministic:\n1: %s\n2: %s\n3: %s", shortened1, shortened2, shortened3)
	}
}

func TestCompressionRatio(t *testing.T) {
	shortener := NewShortener("https://specter.example.com")

	// Create a very long URL (typical OpenSearch URL)
	longURL := "https://logs.example.com/app/data-explorer/discover#?_a=%28discover%3A%28columns%3A%21%28_source%29%2CisDirty%3A%21f%2Csort%3A%21%28%29%29%2Cmetadata%3A%28indexPattern%3Af5766eb0-172e-11f1-aad7-8d5ba2b4be4e%2Cview%3Adiscover%29%29&_g=%28filters%3A%21%28%29%2CrefreshInterval%3A%28pause%3A%21t%2Cvalue%3A0%29%2Ctime%3A%28from%3Anow-1h%2Cto%3Anow%29%29&_q=%28filters%3A%21%28%28%27%24state%27%3A%28store%3AappState%29%2Cmeta%3A%28alias%3A%21n%2Cdisabled%3A%21f%2Cindex%3Af5766eb0-172e-11f1-aad7-8d5ba2b4be4e%2Ckey%3Aresource.k8s.namespace.name%2Cnegate%3A%21f%2Cparams%3A%28query%3Amynamespace%29%2Ctype%3Aphrase%29%2Cquery%3A%28match_phrase%3A%28resource.k8s.namespace.name%3Amynamespace%29%29%29%29%2Cquery%3A%28language%3Akuery%2Cquery%3A%27%27%29%29"

	shortened, err := shortener.Shorten(longURL)
	if err != nil {
		t.Fatalf("failed to shorten: %v", err)
	}

	originalLen := len(longURL)
	shortenedLen := len(shortened)
	ratio := float64(shortenedLen) / float64(originalLen)

	t.Logf("Original length: %d", originalLen)
	t.Logf("Shortened length: %d", shortenedLen)
	t.Logf("Compression ratio: %.2f%%", ratio*100)

	// Verify it's actually shorter
	if shortenedLen >= originalLen {
		t.Errorf("shortened URL is not shorter than original: shortened=%d, original=%d", shortenedLen, originalLen)
	}

	// For very long URLs, expect the shortened version to be significantly smaller
	if originalLen > 700 && ratio > 0.80 {
		t.Errorf("compression ratio too high (%.2f%%) for a long URL, expected < 80%%", ratio*100)
	}
}

func BenchmarkShorten(b *testing.B) {
	shortener := NewShortener("https://specter.example.com")
	url := "https://logs.example.com/app/data-explorer/discover#?_a=%28discover%3A%28columns%3A%21%28_source%29%29%29"

	b.ResetTimer()
	for b.Loop() {
		_, _ = shortener.Shorten(url)
	}
}

func BenchmarkExpand(b *testing.B) {
	shortener := NewShortener("https://specter.example.com")
	url := "https://logs.example.com/app/data-explorer/discover#?_a=%28discover%3A%28columns%3A%21%28_source%29%29%29"

	shortened, err := shortener.Shorten(url)
	if err != nil {
		b.Fatalf("setup failed: %v", err)
	}
	shortID := strings.TrimPrefix(shortened, "https://specter.example.com/s/")

	b.ResetTimer()
	for b.Loop() {
		_, _ = shortener.Expand(shortID)
	}
}
