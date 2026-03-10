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
	"context"
	"strings"
	"testing"

	configv1alpha1 "github.com/joluc/specter/api/v1alpha1"
	"github.com/joluc/specter/internal/template"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = configv1alpha1.AddToScheme(scheme)
	return scheme
}

func TestShorten(t *testing.T) {
	// Setup fake Kubernetes client with ClusterSpecterConfig
	config := &configv1alpha1.ClusterSpecterConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "global"},
		Spec: configv1alpha1.ClusterSpecterConfigSpec{
			Templates: map[string]configv1alpha1.TemplateConfig{
				"logs": {
					URL: "https://logs.example.com?ns={{.namespace}}&idx={{.indexPattern}}",
				},
			},
			DefaultLabels: map[string]string{
				"indexPattern": "default-*",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(config).Build()
	engine := template.NewEngine(nil)
	s := NewShortener("https://specter.io", fakeClient, engine)

	vars := map[string]string{
		"namespace":    "monitoring",
		"indexPattern": "logs-2024-*",
	}

	// Test shortening
	shortURL, err := s.Shorten("logs", "https://logs.example.com?ns=monitoring&idx=logs-2024-*", vars)
	require.NoError(t, err)
	assert.Contains(t, shortURL, "https://specter.io/s/logs/")
	assert.Less(t, len(shortURL), 150) // Should be very short
}

func TestExpand(t *testing.T) {
	config := &configv1alpha1.ClusterSpecterConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "global"},
		Spec: configv1alpha1.ClusterSpecterConfigSpec{
			Templates: map[string]configv1alpha1.TemplateConfig{
				"logs": {
					URL: "https://logs.example.com?ns={{.namespace}}&idx={{.indexPattern}}",
				},
			},
			DefaultLabels: map[string]string{
				"indexPattern": "default-*",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(config).Build()
	engine := template.NewEngine(nil)
	s := NewShortener("https://specter.io", fakeClient, engine)

	// Create a short URL
	vars := map[string]string{
		"namespace":    "monitoring",
		"indexPattern": "logs-2024-*",
	}
	shortURL, _ := s.Shorten("logs", "", vars)

	// Extract encoded part
	parts := strings.Split(strings.TrimPrefix(shortURL, "https://specter.io/s/"), "/")
	templateName := parts[0]
	encodedVars := parts[1]

	// Test expansion
	ctx := context.Background()
	expandedURL, err := s.Expand(ctx, templateName, encodedVars)
	require.NoError(t, err)
	assert.Equal(t, "https://logs.example.com?ns=monitoring&idx=logs-2024-*", expandedURL)
}

func TestExpandWithDefaultLabels(t *testing.T) {
	config := &configv1alpha1.ClusterSpecterConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "global"},
		Spec: configv1alpha1.ClusterSpecterConfigSpec{
			Templates: map[string]configv1alpha1.TemplateConfig{
				"dashboard": {
					URL: "https://dash.example.com?ns={{.namespace}}&env={{.environment}}",
				},
			},
			DefaultLabels: map[string]string{
				"environment": "production",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(config).Build()
	engine := template.NewEngine(nil)
	s := NewShortener("https://specter.io", fakeClient, engine)

	// Only provide namespace, environment should come from defaults
	vars := map[string]string{
		"namespace": "monitoring",
	}
	shortURL, _ := s.Shorten("dashboard", "", vars)

	// Extract encoded part
	parts := strings.Split(strings.TrimPrefix(shortURL, "https://specter.io/s/"), "/")
	templateName := parts[0]
	encodedVars := parts[1]

	// Test expansion
	ctx := context.Background()
	expandedURL, err := s.Expand(ctx, templateName, encodedVars)
	require.NoError(t, err)
	assert.Equal(t, "https://dash.example.com?ns=monitoring&env=production", expandedURL)
}

func TestShortenIfNeeded(t *testing.T) {
	config := &configv1alpha1.ClusterSpecterConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "global"},
		Spec: configv1alpha1.ClusterSpecterConfigSpec{
			Templates: map[string]configv1alpha1.TemplateConfig{
				"logs": {URL: "https://logs.example.com?ns={{.namespace}}"},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(config).Build()
	engine := template.NewEngine(nil)
	s := NewShortener("https://specter.io", fakeClient, engine)

	tests := []struct {
		name      string
		url       string
		maxLength int
		wantShort bool
	}{
		{
			name:      "short URL stays unchanged",
			url:       "https://example.com/short",
			maxLength: 100,
			wantShort: false,
		},
		{
			name:      "long URL gets shortened",
			url:       strings.Repeat("x", 300),
			maxLength: 200,
			wantShort: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := s.ShortenIfNeeded("logs", tt.url, map[string]string{"namespace": "test"}, tt.maxLength)
			require.NoError(t, err)

			if tt.wantShort {
				assert.Contains(t, result, "/s/")
			} else {
				assert.Equal(t, tt.url, result)
			}
		})
	}
}

func TestTemplateNotFound(t *testing.T) {
	config := &configv1alpha1.ClusterSpecterConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "global"},
		Spec: configv1alpha1.ClusterSpecterConfigSpec{
			Templates: map[string]configv1alpha1.TemplateConfig{
				"logs": {
					URL: "https://logs.example.com",
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(config).Build()
	engine := template.NewEngine(nil)
	s := NewShortener("https://specter.io", fakeClient, engine)

	ctx := context.Background()
	_, err := s.Expand(ctx, "nonexistent", "e30")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "template \"nonexistent\" not found")
}

func TestShortenDeterministic(t *testing.T) {
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
	s := NewShortener("https://specter.io", fakeClient, engine)

	vars := map[string]string{
		"namespace": "test",
		"alert":     "HighCPU",
	}

	// Shorten the same context multiple times
	shortened1, err1 := s.Shorten("logs", "", vars)
	shortened2, err2 := s.Shorten("logs", "", vars)
	shortened3, err3 := s.Shorten("logs", "", vars)

	require.NoError(t, err1)
	require.NoError(t, err2)
	require.NoError(t, err3)

	// All results should be identical (deterministic)
	assert.Equal(t, shortened1, shortened2)
	assert.Equal(t, shortened2, shortened3)
}

func TestShortenEmptyValues(t *testing.T) {
	config := &configv1alpha1.ClusterSpecterConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "global"},
		Spec: configv1alpha1.ClusterSpecterConfigSpec{
			Templates: map[string]configv1alpha1.TemplateConfig{
				"logs": {URL: "https://logs.example.com?ns={{.namespace}}"},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(config).Build()
	engine := template.NewEngine(nil)
	s := NewShortener("https://specter.io", fakeClient, engine)

	// Context with empty values
	vars := map[string]string{
		"namespace": "monitoring",
		"empty1":    "",
		"empty2":    "",
	}

	shortURL, err := s.Shorten("logs", "", vars)
	require.NoError(t, err)

	// Empty values should be filtered out, making URL shorter
	// Decode and check that only non-empty values are present
	parts := strings.Split(strings.TrimPrefix(shortURL, "https://specter.io/s/"), "/")
	encodedVars := parts[1]

	ctx := context.Background()
	expandedURL, err := s.Expand(ctx, "logs", encodedVars)
	require.NoError(t, err)
	assert.Equal(t, "https://logs.example.com?ns=monitoring", expandedURL)
}

func BenchmarkShorten(b *testing.B) {
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
	s := NewShortener("https://specter.io", fakeClient, engine)

	vars := map[string]string{
		"namespace":    "monitoring",
		"indexPattern": "logs-2024-*",
	}

	b.ResetTimer()
	for b.Loop() {
		_, _ = s.Shorten("logs", "", vars)
	}
}

func BenchmarkExpand(b *testing.B) {
	config := &configv1alpha1.ClusterSpecterConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "global"},
		Spec: configv1alpha1.ClusterSpecterConfigSpec{
			Templates: map[string]configv1alpha1.TemplateConfig{
				"logs": {URL: "https://logs.example.com?ns={{.namespace}}"},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(config).Build()
	engine := template.NewEngine(nil)
	s := NewShortener("https://specter.io", fakeClient, engine)

	vars := map[string]string{
		"namespace": "monitoring",
	}

	shortURL, err := s.Shorten("logs", "", vars)
	if err != nil {
		b.Fatalf("setup failed: %v", err)
	}

	parts := strings.Split(strings.TrimPrefix(shortURL, "https://specter.io/s/"), "/")
	templateName := parts[0]
	encodedVars := parts[1]

	ctx := context.Background()

	b.ResetTimer()
	for b.Loop() {
		_, _ = s.Expand(ctx, templateName, encodedVars)
	}
}
