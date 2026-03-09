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

package template

import (
	"testing"
)

// =============================================================================
// BASIC RENDERING TESTS
// =============================================================================
// These tests demonstrate basic template functionality and serve as documentation.

// TestEngine_Render_BasicVariable demonstrates the simplest use case:
// replacing a variable with its value from the context.
func TestEngine_Render_BasicVariable(t *testing.T) {
	engine := NewEngine(nil)

	result, err := engine.Render(
		"https://grafana.io/d/abc?var-service={{.service}}",
		map[string]string{
			"service": "billing-api",
		},
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "https://grafana.io/d/abc?var-service=billing-api"
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
}

// TestEngine_Render_MultipleVariables shows using multiple labels in one template.
func TestEngine_Render_MultipleVariables(t *testing.T) {
	engine := NewEngine(nil)

	result, err := engine.Render(
		"https://opensearch.io/app/discover?service={{.service}}&namespace={{.namespace}}&env={{.environment}}",
		map[string]string{
			"service":     "payment-gateway",
			"namespace":   "production",
			"environment": "prod",
		},
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "https://opensearch.io/app/discover?service=payment-gateway&namespace=production&env=prod"
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
}

// TestEngine_Render_MissingVariable shows behavior when a variable is missing.
// Missing variables result in empty strings, not errors.
func TestEngine_Render_MissingVariable(t *testing.T) {
	engine := NewEngine(nil)

	result, err := engine.Render(
		"https://example.com?service={{.service}}&team={{.team}}",
		map[string]string{
			"service": "billing",
			// Note: "team" is NOT provided
		},
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Missing variable becomes empty string (due to missingkey=zero)
	expected := "https://example.com?service=billing&team="
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
}

// =============================================================================
// URL ENCODING TESTS
// =============================================================================

// TestEngine_Render_URLEncode demonstrates the importance of URL encoding.
func TestEngine_Render_URLEncode(t *testing.T) {
	engine := NewEngine(nil)

	result, err := engine.Render(
		"https://logs.io/search?q={{.query | urlEncode}}",
		map[string]string{
			"query": "error & warning",
		},
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Spaces become +, & becomes %26
	expected := "https://logs.io/search?q=error+%26+warning"
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
}

// =============================================================================
// DEFAULT VALUE TESTS
// =============================================================================

// TestEngine_Render_DefaultValue shows handling missing labels with defaults.
func TestEngine_Render_DefaultValue(t *testing.T) {
	engine := NewEngine(nil)

	result, err := engine.Render(
		`https://runbook.io/teams/{{.team | default "platform"}}/alerts`,
		map[string]string{
			"service": "api-gateway",
			// "team" is NOT provided
		},
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "https://runbook.io/teams/platform/alerts"
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
}

// TestEngine_Render_DefaultNotUsed verifies default is only used when empty.
func TestEngine_Render_DefaultNotUsed(t *testing.T) {
	engine := NewEngine(nil)

	result, err := engine.Render(
		`https://runbook.io/teams/{{.team | default "platform"}}/alerts`,
		map[string]string{
			"team": "payments", // Team IS provided
		},
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "https://runbook.io/teams/payments/alerts"
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
}

// =============================================================================
// STRING MANIPULATION TESTS
// =============================================================================

func TestEngine_Render_StringFunctions(t *testing.T) {
	engine := NewEngine(nil)

	tests := []struct {
		name     string
		template string
		context  map[string]string
		expected string
	}{
		{
			name:     "lowercase",
			template: "{{.env | lower}}",
			context:  map[string]string{"env": "PRODUCTION"},
			expected: "production",
		},
		{
			name:     "uppercase",
			template: "{{.env | upper}}",
			context:  map[string]string{"env": "staging"},
			expected: "STAGING",
		},
		{
			name:     "replace",
			template: `{{.service | replace "-" "_"}}`,
			context:  map[string]string{"service": "billing-api-v2"},
			expected: "billing_api_v2",
		},
		{
			name:     "trimPrefix",
			template: `{{.namespace | trimPrefix "team-"}}`,
			context:  map[string]string{"namespace": "team-payments"},
			expected: "payments",
		},
		{
			name:     "trimSuffix",
			template: `{{.service | trimSuffix "-svc"}}`,
			context:  map[string]string{"service": "billing-svc"},
			expected: "billing",
		},
		{
			name:     "coalesce",
			template: `{{coalesce .primary .secondary "fallback"}}`,
			context:  map[string]string{"secondary": "value2"},
			expected: "value2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := engine.Render(tt.template, tt.context)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result != tt.expected {
				t.Errorf("got %q, want %q", result, tt.expected)
			}
		})
	}
}

// =============================================================================
// QUERY ESCAPING TESTS
// =============================================================================

func TestEngine_Render_KueryEscape(t *testing.T) {
	engine := NewEngine(nil)

	result, err := engine.Render(
		`query:'service:"{{.service | kueryEscape}}"'`,
		map[string]string{
			"service": `test:service`,
		},
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Colon should be escaped
	expected := `query:'service:"test\:service"'`
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
}

// =============================================================================
// CONDITIONAL TESTS
// =============================================================================

func TestEngine_Render_Conditional(t *testing.T) {
	engine := NewEngine(nil)

	template := `{{if eq .env "production"}}https://prod.grafana.io{{else}}https://staging.grafana.io{{end}}/d/{{.dashboard}}`

	tests := []struct {
		env      string
		expected string
	}{
		{"production", "https://prod.grafana.io/d/abc123"},
		{"staging", "https://staging.grafana.io/d/abc123"},
		{"development", "https://staging.grafana.io/d/abc123"},
	}

	for _, tt := range tests {
		t.Run(tt.env, func(t *testing.T) {
			result, err := engine.Render(template, map[string]string{
				"env":       tt.env,
				"dashboard": "abc123",
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result != tt.expected {
				t.Errorf("env=%s: got %q, want %q", tt.env, result, tt.expected)
			}
		})
	}
}

// =============================================================================
// VALIDATION TESTS
// =============================================================================

func TestEngine_Validate_ValidTemplates(t *testing.T) {
	engine := NewEngine(nil)

	validTemplates := []string{
		"https://example.com",
		"https://example.com?service={{.service}}",
		"https://example.com?q={{.service | urlEncode}}",
		`{{.service | default "unknown" | urlEncode}}`,
		`{{if eq .env "prod"}}https://prod.example.com{{else}}https://dev.example.com{{end}}`,
	}

	for _, tmpl := range validTemplates {
		if err := engine.Validate(tmpl); err != nil {
			t.Errorf("template %q should be valid but got error: %v", tmpl, err)
		}
	}
}

func TestEngine_Validate_InvalidTemplates(t *testing.T) {
	engine := NewEngine(nil)

	invalidTemplates := []string{
		"{{.service | unknownFunction}}", // Unknown function
		"{{.service",                      // Unclosed brace
		"{{if .service}}no end",           // Missing {{end}}
	}

	for _, tmpl := range invalidTemplates {
		if err := engine.Validate(tmpl); err == nil {
			t.Errorf("template %q should be invalid but passed validation", tmpl)
		}
	}
}

// =============================================================================
// VARIABLE EXTRACTION TESTS
// =============================================================================

func TestEngine_ExtractVariables(t *testing.T) {
	engine := NewEngine(nil)

	// Use a template without function calls for cleaner extraction
	template := "https://example.com?service={{.service}}&ns={{.namespace}}&env={{.environment}}"

	vars := engine.ExtractVariables(template)

	expectedVars := map[string]bool{
		"service":     true,
		"namespace":   true,
		"environment": true,
	}

	if len(vars) != len(expectedVars) {
		t.Errorf("got %d variables, want %d: %v", len(vars), len(expectedVars), vars)
	}

	for _, v := range vars {
		if !expectedVars[v] {
			t.Errorf("unexpected variable: %s", v)
		}
	}
}

// =============================================================================
// RENDER MULTIPLE TESTS
// =============================================================================

func TestEngine_RenderMultiple(t *testing.T) {
	engine := NewEngine(nil)

	templates := map[string]string{
		"logs":    "https://opensearch.io/logs?service={{.service}}",
		"traces":  "https://jaeger.io/search?service={{.service}}",
		"metrics": "https://grafana.io/d/abc?var-service={{.service}}",
		"runbook": "https://wiki.io/runbooks/{{.alertname}}",
	}

	context := map[string]string{
		"service":   "payment-api",
		"alertname": "HighErrorRate",
	}

	results, errors := engine.RenderMultiple(templates, context)

	if len(errors) > 0 {
		t.Errorf("unexpected errors: %v", errors)
	}

	expectedResults := map[string]string{
		"logs":    "https://opensearch.io/logs?service=payment-api",
		"traces":  "https://jaeger.io/search?service=payment-api",
		"metrics": "https://grafana.io/d/abc?var-service=payment-api",
		"runbook": "https://wiki.io/runbooks/HighErrorRate",
	}

	for name, expected := range expectedResults {
		if results[name] != expected {
			t.Errorf("%s: got %q, want %q", name, results[name], expected)
		}
	}
}

// =============================================================================
// RENDER WITH DEFAULTS TESTS
// =============================================================================

func TestEngine_RenderWithDefaults(t *testing.T) {
	engine := NewEngine(nil)

	defaults := map[string]string{
		"environment": "production",
		"region":      "eu-west-1",
	}

	context := map[string]string{
		"service":     "billing-api",
		"environment": "staging", // This should override the default
	}

	result, err := engine.RenderWithDefaults(
		"{{.service}}-{{.environment}}-{{.region}}",
		context,
		defaults,
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// environment from context (staging) overrides default (production)
	// region comes from defaults
	expected := "billing-api-staging-eu-west-1"
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
}

// =============================================================================
// REAL-WORLD EXAMPLE TESTS
// =============================================================================

func TestEngine_Render_OpenSearchURL(t *testing.T) {
	engine := NewEngine(nil)

	// Realistic OpenSearch Discover URL
	template := `https://opensearch.mycompany.io/_dashboards/app/discover#/?_a=(query:(language:kuery,query:'service:"{{.service | kueryEscape}}" AND namespace:"{{.namespace | kueryEscape}}"'))`

	result, err := engine.Render(template, map[string]string{
		"service":   "billing-api",
		"namespace": "production",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := `https://opensearch.mycompany.io/_dashboards/app/discover#/?_a=(query:(language:kuery,query:'service:"billing-api" AND namespace:"production"'))`
	if result != expected {
		t.Errorf("got:\n%s\nwant:\n%s", result, expected)
	}
}

func TestEngine_Render_GrafanaExploreURL(t *testing.T) {
	engine := NewEngine(nil)

	// Grafana Explore URL with Loki query
	template := `https://grafana.mycompany.io/explore?left={"queries":[{"expr":"{service=\"{{.service}}\",namespace=\"{{.namespace}}\"}"}]}`

	result, err := engine.Render(template, map[string]string{
		"service":   "checkout-api",
		"namespace": "ecommerce",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := `https://grafana.mycompany.io/explore?left={"queries":[{"expr":"{service=\"checkout-api\",namespace=\"ecommerce\"}"}]}`
	if result != expected {
		t.Errorf("got:\n%s\nwant:\n%s", result, expected)
	}
}
