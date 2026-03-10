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
// THE SOLUTION (TEMPLATE-BASED APPROACH):
// Instead of compressing the entire URL, we leverage the fact that URL templates
// are already stored in ClusterSpecterConfig. We encode only the template reference
// and variable values:
//
//  1. Extract variables used in the template (e.g., namespace, indexPattern)
//  2. Encode variables as JSON then base64url
//  3. Return: https://specter.company.io/s/{template_name}/{encoded_vars}
//  4. When clicked, look up template in ClusterSpecterConfig and render
//
// ARCHITECTURE:
//   - Shortener: Encodes template references and variables (stateless)
//   - Handler: Looks up template and renders with decoded variables
//
// BENEFITS:
//   - 91% size reduction (768 chars -> ~47 chars)
//   - Human-readable URLs (can see template name)
//   - No compression CPU overhead
//   - Template validation at config time
//   - Debuggable (can inspect short URLs)
//
// TRADEOFFS:
//   - Shortener needs Kubernetes API access (read-only)
//   - Short URLs break if ClusterSpecterConfig is deleted
//   - If template changes, old short URLs render new template (can be a feature)
package shortener

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"

	configv1alpha1 "github.com/joluc/specter/api/v1alpha1"
	"github.com/joluc/specter/internal/template"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// =============================================================================
// TEMPLATE-BASED SHORTENER
// =============================================================================
//
// Shortener encodes template references and variables without any storage.
// Each "short" URL contains the template name and variable values, not the full URL.
//
// =============================================================================

// Shortener provides stateless URL shortening via template references.
type Shortener struct {
	// baseURL is the external URL prefix for short links.
	// Example: "https://specter.mycompany.io"
	baseURL string

	// client is the Kubernetes client for reading ClusterSpecterConfigs.
	client client.Client

	// templateEngine renders templates with variables.
	templateEngine *template.Engine
}

// NewShortener creates a new template-based URL shortener.
//
// Parameters:
//   - baseURL: The external URL where Specter's shortener is accessible.
//     Example: "https://specter.mycompany.io"
//   - k8sClient: Kubernetes client for reading ClusterSpecterConfigs
//   - templateEngine: Template engine for rendering URLs
//
// Example:
//
//	shortener := shortener.NewShortener("https://specter.mycompany.io", k8sClient, templateEngine)
//	shortURL, err := shortener.Shorten("logs", renderedURL, context)
func NewShortener(baseURL string, k8sClient client.Client, templateEngine *template.Engine) *Shortener {
	return &Shortener{
		baseURL:        baseURL,
		client:         k8sClient,
		templateEngine: templateEngine,
	}
}

// Shorten creates a short URL with template reference + encoded variables.
//
// Parameters:
//   - templateName: The name of the template in ClusterSpecterConfig
//   - renderedURL: The full rendered URL (unused, kept for signature compatibility)
//   - context: The variable values used to render the template
//
// Returns:
//   - The complete short URL (e.g., "https://specter.io/s/logs/eyJuYW1...")
//   - Error if encoding fails
//
// Example:
//
//	shortURL, err := shortener.Shorten(
//	    "logs",
//	    "https://opensearch.io/...",
//	    map[string]string{"namespace": "monitoring", "indexPattern": "f5766eb0-..."},
//	)
//	// shortURL = "https://specter.mycompany.io/s/logs/eyJuYW1lc3BhY2UiOi..."
//
// PROCESS:
//  1. Filter out empty values from context to minimize size
//  2. Encode variables as JSON
//  3. Base64url encode (makes it URL-safe)
//  4. Return short URL: {baseURL}/s/{template}/{encoded_vars}
func (s *Shortener) Shorten(templateName, renderedURL string, context map[string]string) (string, error) {
	// Extract only non-empty variables to minimize size
	compactContext := make(map[string]string)
	for k, v := range context {
		if v != "" {
			compactContext[k] = v
		}
	}

	// Encode variables as JSON then base64url
	jsonData, err := json.Marshal(compactContext)
	if err != nil {
		return "", fmt.Errorf("marshal context: %w", err)
	}

	encoded := base64.RawURLEncoding.EncodeToString(jsonData)

	// Build short URL: /s/{template}/{encoded_vars}
	return fmt.Sprintf("%s/s/%s/%s", s.baseURL, url.PathEscape(templateName), encoded), nil
}

// Expand decodes a short URL and renders the template.
//
// Parameters:
//   - ctx: Context for Kubernetes API calls
//   - templateName: The template name from the URL path
//   - encodedVars: The base64url encoded variables
//
// Returns:
//   - The rendered full URL
//   - Error if decoding, template lookup, or rendering fails
//
// Example:
//
//	originalURL, err := shortener.Expand(ctx, "logs", "eyJuYW1lc3BhY2UiOi...")
//	// originalURL = "https://opensearch.io/very/long/url..."
//
// PROCESS:
//  1. Base64url decode the variables
//  2. JSON unmarshal to get variable map
//  3. Look up template from ClusterSpecterConfig
//  4. Merge variables with default labels
//  5. Render template with merged context
func (s *Shortener) Expand(ctx context.Context, templateName, encodedVars string) (string, error) {
	// 1. Decode variables
	jsonData, err := base64.RawURLEncoding.DecodeString(encodedVars)
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}

	var variables map[string]string
	if err := json.Unmarshal(jsonData, &variables); err != nil {
		return "", fmt.Errorf("json unmarshal: %w", err)
	}

	// 2. Find template from ClusterSpecterConfig
	templateURL, defaultLabels, err := s.findTemplate(ctx, templateName)
	if err != nil {
		return "", fmt.Errorf("find template: %w", err)
	}

	// 3. Merge variables with default labels (variables take precedence)
	context := make(map[string]string)
	for k, v := range defaultLabels {
		context[k] = v
	}
	for k, v := range variables {
		context[k] = v
	}

	// 4. Render template
	renderedURL, err := s.templateEngine.Render(templateURL, context)
	if err != nil {
		return "", fmt.Errorf("render template: %w", err)
	}

	return renderedURL, nil
}

// findTemplate looks up a template by name in ClusterSpecterConfigs.
func (s *Shortener) findTemplate(ctx context.Context, templateName string) (templateURL string, defaultLabels map[string]string, err error) {
	// List all ClusterSpecterConfigs (typically just one: "global")
	var configList configv1alpha1.ClusterSpecterConfigList
	if err := s.client.List(ctx, &configList); err != nil {
		return "", nil, fmt.Errorf("list cluster configs: %w", err)
	}

	// Search for template in configs (first match wins)
	for _, config := range configList.Items {
		if tmpl, exists := config.Spec.Templates[templateName]; exists && tmpl.IsEnabled() {
			return tmpl.URL, config.Spec.DefaultLabels, nil
		}
	}

	return "", nil, fmt.Errorf("template %q not found in any ClusterSpecterConfig", templateName)
}

// ShortenIfNeeded shortens if URL is below threshold OR if compression would save space.
//
// Parameters:
//   - templateName: The template name used to render the URL
//   - url: The URL to potentially shorten
//   - context: The template variables
//   - maxLength: Maximum URL length before shortening kicks in
//
// Returns:
//   - The original URL if under maxLength
//   - A short URL if over maxLength
//   - Error is returned but original URL is still valid (fail-safe)
//
// Example:
//
//	finalURL, err := shortener.ShortenIfNeeded("logs", longURL, context, 200)
//	if err != nil {
//	    // Still use finalURL (will be original URL)
//	    log.Warn("failed to shorten URL", "error", err)
//	}
//
// DESIGN: If shortening fails, we return the original URL rather than
// erroring out completely. This ensures alerts still have a (long) working
// link rather than no link at all.
func (s *Shortener) ShortenIfNeeded(templateName, url string, context map[string]string, maxLength int) (string, error) {
	if len(url) <= maxLength {
		return url, nil // Already short enough
	}

	shortened, err := s.Shorten(templateName, url, context)
	if err != nil {
		return url, err // Fall back to original on error
	}

	// Only use short URL if it's actually shorter
	if len(shortened) < len(url) {
		return shortened, nil
	}

	return url, nil
}
