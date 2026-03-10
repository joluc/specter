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

	configv1alpha1 "github.com/joluc/specter/api/v1alpha1"
	"github.com/joluc/specter/internal/template"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestHandlerRedirect(t *testing.T) {
	config := &configv1alpha1.ClusterSpecterConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "global"},
		Spec: configv1alpha1.ClusterSpecterConfigSpec{
			Templates: map[string]configv1alpha1.TemplateConfig{
				"logs": {
					URL: "https://logs.example.com/test/path?param={{.param}}",
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(config).Build()
	engine := template.NewEngine(nil)
	shortener := NewShortener("https://specter.example.com", fakeClient, engine)
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	handler := NewHandler(shortener, logger)

	vars := map[string]string{
		"param": "value",
	}

	// First shorten the URL to get a valid short URL
	shortened, err := shortener.Shorten("logs", "", vars)
	if err != nil {
		t.Fatalf("failed to shorten URL: %v", err)
	}

	// Extract path after /s/
	shortPath := strings.TrimPrefix(shortened, "https://specter.example.com/s/")

	// Test the redirect
	req := httptest.NewRequest("GET", "/s/"+shortPath, nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	// Check status code
	if w.Code != http.StatusFound {
		t.Errorf("expected status %d, got %d", http.StatusFound, w.Code)
	}

	// Check redirect location
	location := w.Header().Get("Location")
	expectedURL := "https://logs.example.com/test/path?param=value"
	if location != expectedURL {
		t.Errorf("expected redirect to %s, got %s", expectedURL, location)
	}
}

func TestHandlerInvalidPath(t *testing.T) {
	config := &configv1alpha1.ClusterSpecterConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "global"},
		Spec: configv1alpha1.ClusterSpecterConfigSpec{
			Templates: map[string]configv1alpha1.TemplateConfig{
				"logs": {URL: "https://logs.example.com"},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(config).Build()
	engine := template.NewEngine(nil)
	shortener := NewShortener("https://specter.example.com", fakeClient, engine)
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
			name:           "missing template part",
			path:           "/s/onlyonepart",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "invalid base64 in vars",
			path:           "/s/logs/not-valid-base64!!!",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "nonexistent template",
			path:           "/s/nonexistent/e30",
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
	// Template with invalid scheme should be rejected
	config := &configv1alpha1.ClusterSpecterConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "global"},
		Spec: configv1alpha1.ClusterSpecterConfigSpec{
			Templates: map[string]configv1alpha1.TemplateConfig{
				"badscheme": {
					URL: "javascript:alert('xss')",
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(config).Build()
	engine := template.NewEngine(nil)
	shortener := NewShortener("https://specter.example.com", fakeClient, engine)
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	handler := NewHandler(shortener, logger)

	// Create a short URL for the bad template
	shortened, err := shortener.Shorten("badscheme", "", map[string]string{})
	if err != nil {
		t.Fatalf("failed to shorten URL: %v", err)
	}

	shortPath := strings.TrimPrefix(shortened, "https://specter.example.com/s/")

	req := httptest.NewRequest("GET", "/s/"+shortPath, nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	// Should reject URLs without http/https scheme
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status %d for invalid scheme, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestHandlerMultipleTemplates(t *testing.T) {
	config := &configv1alpha1.ClusterSpecterConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "global"},
		Spec: configv1alpha1.ClusterSpecterConfigSpec{
			Templates: map[string]configv1alpha1.TemplateConfig{
				"logs": {
					URL: "https://logs.example.com?ns={{.namespace}}",
				},
				"dashboard": {
					URL: "https://grafana.example.com/d/{{.dashboardId}}",
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(config).Build()
	engine := template.NewEngine(nil)
	shortener := NewShortener("https://specter.example.com", fakeClient, engine)
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	handler := NewHandler(shortener, logger)

	testCases := []struct {
		name         string
		templateName string
		vars         map[string]string
		expectedURL  string
	}{
		{
			name:         "logs template",
			templateName: "logs",
			vars:         map[string]string{"namespace": "monitoring"},
			expectedURL:  "https://logs.example.com?ns=monitoring",
		},
		{
			name:         "dashboard template",
			templateName: "dashboard",
			vars:         map[string]string{"dashboardId": "abc123"},
			expectedURL:  "https://grafana.example.com/d/abc123",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Shorten
			shortened, err := shortener.Shorten(tc.templateName, "", tc.vars)
			if err != nil {
				t.Fatalf("failed to shorten URL: %v", err)
			}

			shortPath := strings.TrimPrefix(shortened, "https://specter.example.com/s/")

			// Redirect
			req := httptest.NewRequest("GET", "/s/"+shortPath, nil)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			if w.Code != http.StatusFound {
				t.Errorf("expected status %d, got %d", http.StatusFound, w.Code)
			}

			location := w.Header().Get("Location")
			if location != tc.expectedURL {
				t.Errorf("expected redirect to %s, got %s", tc.expectedURL, location)
			}
		})
	}
}
