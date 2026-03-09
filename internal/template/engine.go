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

// Package template provides the URL template engine for Specter.
//
// LEARNING NOTE: This package is the "brain" of Specter. It takes URL templates
// like "https://grafana.io/d/xyz?service={{.service}}" and renders them with
// actual values from PrometheusRule labels.
//
// We use Go's built-in text/template package, which is powerful and flexible.
// This file adds custom functions to make templates more useful for our
// observability use case (URL encoding, time manipulation, query escaping, etc.).
//
// KEY CONCEPTS:
//   - Template: A string with placeholders like {{.variable}}
//   - Context: The data used to fill in placeholders (alert labels)
//   - FuncMap: Custom functions available in templates (urlEncode, default, etc.)
//
// SECURITY NOTE: We use text/template, not html/template, because we're
// generating URLs, not HTML. However, we provide urlEncode for safety.
package template

import (
	"bytes"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"strings"
	"text/template"
	"time"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// =============================================================================
// TEMPLATE ENGINE
// =============================================================================
//
// Engine is the main type for rendering URL templates. It pre-registers custom
// functions and handles template parsing/execution.
//
// DESIGN DECISION: We create a reusable Engine rather than parsing templates
// on each render. This is more efficient when processing many alerts, as
// template parsing has a non-trivial cost.
//
// The Engine is safe for concurrent use because:
//   - funcMap is read-only after initialization
//   - Each Render call creates its own template instance
//
// =============================================================================

// Engine renders URL templates with alert labels.
type Engine struct {
	// funcMap contains custom template functions available in all templates.
	//
	// LEARNING NOTE: FuncMap is how you extend Go templates with custom functions.
	// Keys are the function names used in templates, values are the Go functions.
	// Example: funcMap["urlEncode"] = url.QueryEscape
	//          Then in template: {{.service | urlEncode}}
	funcMap template.FuncMap

	// logger is used for debug logging during template operations.
	logger *slog.Logger
}

// NewEngine creates a new template engine with all custom functions registered.
//
// Parameters:
//   - logger: Optional slog.Logger for debug output. Pass nil to disable logging.
//
// Example:
//
//	engine := template.NewEngine(slog.Default())
//	url, err := engine.Render("https://example.com?s={{.service}}", labels)
func NewEngine(logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	return &Engine{
		funcMap: createFuncMap(),
		logger:  logger,
	}
}

// createFuncMap builds the map of custom template functions.
// These functions become available in templates as {{.value | functionName}}.
//
// LEARNING NOTE: Template functions use a "pipeline" syntax similar to Unix pipes.
// {{.service | urlEncode | upper}} means:
//   1. Get the value of .service
//   2. Pass it to urlEncode
//   3. Pass the result to upper
//
// Function arguments come BEFORE the piped value when using pipes:
//   {{.service | replace "-" "_"}}  is equivalent to  replace("-", "_", .service)
func createFuncMap() template.FuncMap {
	return template.FuncMap{
		// =====================================================================
		// STRING FUNCTIONS
		// =====================================================================
		// These transform strings in various ways. Essential for building URLs.

		// urlEncode encodes a string for safe use in URL query parameters.
		// Converts special characters to percent-encoded form.
		//
		// Example: "hello world" -> "hello+world"
		//          "foo&bar"     -> "foo%26bar"
		//
		// Usage: {{.service | urlEncode}}
		//
		// WHY THIS MATTERS: URLs cannot contain spaces, &, =, and other special
		// characters in query values. Without encoding, your links will break
		// or behave unexpectedly.
		"urlEncode": url.QueryEscape,

		// urlPathEncode encodes a string for URL path segments.
		// Similar to urlEncode but handles path-specific encoding rules.
		// Notably, spaces become %20 instead of +.
		//
		// Usage: {{.dashboardId | urlPathEncode}}
		"urlPathEncode": url.PathEscape,

		// lower converts a string to lowercase.
		//
		// Usage: {{.severity | lower}}
		// Example: "WARNING" -> "warning"
		"lower": strings.ToLower,

		// upper converts a string to uppercase.
		//
		// Usage: {{.env | upper}}
		// Example: "production" -> "PRODUCTION"
		"upper": strings.ToUpper,

		// title converts a string to title case.
		//
		// Usage: {{.service | title}}
		// Example: "my-service" -> "My-Service"
		"title": cases.Title(language.English).String,

		// replace performs string replacement.
		// First arg is old string, second is new string, value comes from pipe.
		//
		// Usage: {{.service | replace "-" "_"}}
		// Example: "billing-api" -> "billing_api"
		"replace": func(old, new, s string) string {
			return strings.ReplaceAll(s, old, new)
		},

		// trimPrefix removes a prefix from a string if present.
		//
		// Usage: {{.namespace | trimPrefix "team-"}}
		// Example: "team-payments" -> "payments"
		"trimPrefix": func(prefix, s string) string {
			return strings.TrimPrefix(s, prefix)
		},

		// trimSuffix removes a suffix from a string if present.
		//
		// Usage: {{.service | trimSuffix "-svc"}}
		// Example: "billing-svc" -> "billing"
		"trimSuffix": func(suffix, s string) string {
			return strings.TrimSuffix(s, suffix)
		},

		// default provides a fallback value if the input is empty.
		// This is crucial for handling missing labels gracefully.
		//
		// Usage: {{.team | default "platform"}}
		//
		// WHY THIS MATTERS: Not all alerts have all labels. Without default,
		// missing labels would create broken URLs or empty parameters.
		"default": func(defaultVal string, val any) string {
			// Handle various types that might come from the template
			switch v := val.(type) {
			case string:
				if v == "" {
					return defaultVal
				}
				return v
			case nil:
				return defaultVal
			default:
				// For any other type, convert to string
				s := fmt.Sprintf("%v", v)
				if s == "" || s == "<no value>" {
					return defaultVal
				}
				return s
			}
		},

		// quote wraps a string in double quotes and escapes internal quotes.
		// Useful for building JSON-like query strings.
		//
		// Usage: {{.service | quote}}
		// Example: my-service -> "my-service"
		"quote": func(s string) string {
			return fmt.Sprintf("%q", s)
		},

		// join concatenates slice elements with a separator.
		// Useful for labels that contain multiple values.
		//
		// Usage: {{.tags | join ","}}
		"join": func(sep string, elems []string) string {
			return strings.Join(elems, sep)
		},

		// split divides a string by separator into a slice.
		//
		// Usage: {{index (.service | split "-") 0}}
		// Example: "billing-api-v2" split by "-" -> ["billing", "api", "v2"]
		"split": func(sep, s string) []string {
			return strings.Split(s, sep)
		},

		// =====================================================================
		// TIME FUNCTIONS
		// =====================================================================
		// These are essential for building time-bounded queries in observability
		// tools. For example, showing logs from 15 minutes before an alert fired.

		// now returns the current Unix timestamp (seconds since epoch).
		//
		// Usage: {{now}}
		// Example: 1705312200
		"now": func() int64 {
			return time.Now().Unix()
		},

		// nowMillis returns current time as Unix milliseconds.
		// Many observability tools (Grafana, Kibana) use millisecond timestamps.
		//
		// Usage: {{nowMillis}}
		// Example: 1705312200000
		"nowMillis": func() int64 {
			return time.Now().UnixMilli()
		},

		// nowRFC3339 returns the current time in RFC3339 format.
		//
		// Usage: {{nowRFC3339}}
		// Example: "2024-01-15T10:30:00Z"
		"nowRFC3339": func() string {
			return time.Now().UTC().Format(time.RFC3339)
		},

		// formatTime formats a Unix timestamp using Go time format.
		//
		// Usage: {{.timestamp | formatTime "2006-01-02"}}
		//
		// LEARNING NOTE: Go uses a reference time (Mon Jan 2 15:04:05 MST 2006)
		// instead of strftime patterns. The reference time is 01/02 03:04:05 PM '06 -0700.
		"formatTime": func(format string, unixTime int64) string {
			return time.Unix(unixTime, 0).UTC().Format(format)
		},

		// addDuration adds a duration to the current time and returns RFC3339.
		// Duration uses Go's duration format: "1h", "30m", "-15m", etc.
		//
		// Usage: {{addDuration "-15m"}}
		// Result: Current time minus 15 minutes in RFC3339 format
		//
		// USE CASE: "Show me logs starting 15 minutes before now"
		//
		// LEARNING NOTE: Go duration format:
		//   - "1h" = 1 hour
		//   - "30m" = 30 minutes
		//   - "15s" = 15 seconds
		//   - "-1h" = 1 hour ago (negative)
		//   - "1h30m" = 1 hour 30 minutes
		"addDuration": func(durationStr string) (string, error) {
			duration, err := time.ParseDuration(durationStr)
			if err != nil {
				return "", fmt.Errorf("invalid duration %q: %w", durationStr, err)
			}
			return time.Now().Add(duration).UTC().Format(time.RFC3339), nil
		},

		// addDurationMillis adds duration to now and returns Unix milliseconds.
		//
		// Usage: {{addDurationMillis "-1h"}}
		// Result: Unix milliseconds for 1 hour ago
		"addDurationMillis": func(durationStr string) (int64, error) {
			duration, err := time.ParseDuration(durationStr)
			if err != nil {
				return 0, fmt.Errorf("invalid duration %q: %w", durationStr, err)
			}
			return time.Now().Add(duration).UnixMilli(), nil
		},

		// =====================================================================
		// OPENSEARCH / ELASTICSEARCH FUNCTIONS
		// =====================================================================
		// OpenSearch/Kibana uses specific query languages and URL encoding.
		// These functions help build valid OpenSearch Discover URLs.

		// kueryEscape escapes a string for use in Kibana Query Language (KQL).
		// KQL is the query language used in OpenSearch/Kibana Discover.
		//
		// Usage: {{.service | kueryEscape}}
		//
		// Characters that need escaping in KQL:
		//   \ " : ( ) [ ] { } * ? ~ < >
		"kueryEscape": func(s string) string {
			replacer := strings.NewReplacer(
				`\`, `\\`,
				`"`, `\"`,
				`:`, `\:`,
				`(`, `\(`,
				`)`, `\)`,
				`[`, `\[`,
				`]`, `\]`,
				`{`, `\{`,
				`}`, `\}`,
				`*`, `\*`,
				`?`, `\?`,
				`~`, `\~`,
				`<`, `\<`,
				`>`, `\>`,
			)
			return replacer.Replace(s)
		},

		// luceneEscape escapes a string for Lucene query syntax.
		// Similar to KQL but used in some older Elasticsearch setups.
		//
		// Usage: {{.query | luceneEscape}}
		"luceneEscape": func(s string) string {
			replacer := strings.NewReplacer(
				`\`, `\\`,
				`+`, `\+`,
				`-`, `\-`,
				`!`, `\!`,
				`(`, `\(`,
				`)`, `\)`,
				`{`, `\{`,
				`}`, `\}`,
				`[`, `\[`,
				`]`, `\]`,
				`^`, `\^`,
				`"`, `\"`,
				`~`, `\~`,
				`*`, `\*`,
				`?`, `\?`,
				`:`, `\:`,
				`/`, `\/`,
			)
			return replacer.Replace(s)
		},

		// =====================================================================
		// CONDITIONAL FUNCTIONS
		// =====================================================================
		// These enable conditional logic in templates.

		// coalesce returns the first non-empty value from arguments.
		// Useful for fallback chains.
		//
		// Usage: {{coalesce .service .app .deployment "unknown"}}
		// Returns the first non-empty value, or "unknown" if all are empty.
		"coalesce": func(values ...any) string {
			for _, v := range values {
				switch val := v.(type) {
				case string:
					if val != "" {
					return val
				}
			case nil:
				continue
			default:
				s := fmt.Sprintf("%v", val)
				if s != "" && s != "<no value>" {
					return s
				}
			}
		}
		return ""
	},

		// ternary returns trueVal if condition is true, otherwise falseVal.
		//
		// Usage: {{ternary (eq .env "prod") "production" "staging"}}
		"ternary": func(condition bool, trueVal, falseVal string) string {
			if condition {
				return trueVal
			}
			return falseVal
		},

		// eq checks if two strings are equal.
		//
		// Usage: {{if eq .env "production"}}prod-url{{else}}dev-url{{end}}
		"eq": func(a, b string) bool {
			return a == b
		},

		// ne checks if two strings are not equal.
		//
		// Usage: {{if ne .severity "info"}}show-alert{{end}}
		"ne": func(a, b string) bool {
			return a != b
		},

		// contains checks if string s contains substring substr.
		//
		// Usage: {{if contains "api" .service}}...{{end}}
		"contains": func(substr, s string) bool {
			return strings.Contains(s, substr)
		},

		// hasPrefix checks if string s starts with prefix.
		//
		// Usage: {{if hasPrefix "team-" .namespace}}...{{end}}
		"hasPrefix": func(prefix, s string) bool {
			return strings.HasPrefix(s, prefix)
		},

		// hasSuffix checks if string s ends with suffix.
		//
		// Usage: {{if hasSuffix "-api" .service}}...{{end}}
		"hasSuffix": func(suffix, s string) bool {
			return strings.HasSuffix(s, suffix)
		},

		// empty checks if a string is empty.
		//
		// Usage: {{if not (empty .team)}}team={{.team}}{{end}}
		"empty": func(s string) bool {
			return s == ""
		},

		// notEmpty checks if a string is not empty.
		//
		// Usage: {{if notEmpty .team}}team={{.team}}{{end}}
		"notEmpty": func(s string) bool {
			return s != ""
		},
	}
}

// =============================================================================
// RENDER METHODS
// =============================================================================
// These methods do the actual work of combining templates with data.
// =============================================================================

// Render executes a URL template with the provided context (labels).
//
// Parameters:
//   - templateStr: The URL template string with {{.variable}} placeholders
//   - context: A map of label names to values from the alert
//
// Returns:
//   - The rendered URL string
//   - An error if template parsing or execution fails
//
// Example:
//
//	engine := NewEngine(nil)
//	url, err := engine.Render(
//	    "https://grafana.io/d/abc?var-service={{.service | urlEncode}}",
//	    map[string]string{"service": "billing-api", "namespace": "production"},
//	)
//	// url = "https://grafana.io/d/abc?var-service=billing-api"
//
// ERROR HANDLING:
// - Template syntax errors are returned immediately
// - Missing variables in context result in empty strings (not errors)
// - Function errors (e.g., invalid duration) cause template execution to fail
func (e *Engine) Render(templateStr string, context map[string]string) (string, error) {
	e.logger.Debug("rendering template",
		slog.String("template", templateStr),
		slog.Any("context", context),
	)

	// Step 1: Parse the template string.
	//
	// LEARNING NOTE: Template parsing validates syntax and compiles it into an
	// internal representation. This catches errors like:
	//   - {{.foo}  (missing closing brace)
	//   - {{.foo | unknownFunc}}  (undefined function)
	//   - {{if .foo}}no end  (missing {{end}})
	//
	// We use Option("missingkey=zero") so that missing keys in the context
	// produce empty strings instead of "<no value>".
	tmpl, err := template.New("url").
		Option("missingkey=zero").
		Funcs(e.funcMap).
		Parse(templateStr)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	// Step 2: Execute the template with the context.
	//
	// LEARNING NOTE: We use bytes.Buffer because template.Execute writes to
	// an io.Writer interface. Buffer is an efficient way to collect output.
	// The context map is passed as the "dot" value accessible via {{.key}}.
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, context); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	result := buf.String()
	e.logger.Debug("template rendered successfully",
		slog.String("result", result),
	)

	return result, nil
}

// RenderWithDefaults renders a template, using default values for missing labels.
//
// Parameters:
//   - templateStr: The URL template string
//   - context: Alert labels
//   - defaults: Fallback values for missing labels
//
// The context takes precedence over defaults for any key present in both.
func (e *Engine) RenderWithDefaults(templateStr string, context, defaults map[string]string) (string, error) {
	merged := make(map[string]string)
	maps.Copy(merged, defaults)
	maps.Copy(merged, context)
	return e.Render(templateStr, merged)
}

// RenderMultiple renders multiple templates with the same context.
// This is the main method used by the controller to generate all
// diagnostic links for an alert at once.
//
// Parameters:
//   - templates: Map of template names to template strings
//   - context: The labels to use for rendering
//
// Returns:
//   - Map of template names to rendered URLs (only successful renders)
//   - Map of template names to errors (for templates that failed)
//
// Example:
//
//	templates := map[string]string{
//	    "logs":   "https://opensearch.io/logs?service={{.service}}",
//	    "traces": "https://jaeger.io/search?service={{.service}}",
//	}
//	urls, errors := engine.RenderMultiple(templates, labels)
//	// urls = {"logs": "https://...", "traces": "https://..."}
//	// errors = {} (empty if all succeeded)
func (e *Engine) RenderMultiple(templates map[string]string, context map[string]string) (map[string]string, map[string]error) {
	results := make(map[string]string)
	errors := make(map[string]error)

	for name, templateStr := range templates {
		rendered, err := e.Render(templateStr, context)
		if err != nil {
			errors[name] = err
			e.logger.Warn("template render failed",
				slog.String("template_name", name),
				slog.String("error", err.Error()),
			)
		} else {
			results[name] = rendered
		}
	}

	return results, errors
}

// =============================================================================
// VALIDATION METHODS
// =============================================================================
// These methods check templates without rendering, useful for CRD validation.
// =============================================================================

// Validate checks if a template is syntactically valid.
// This is called during CRD validation (via webhook) to catch errors early.
//
// Parameters:
//   - templateStr: The URL template to validate
//
// Returns:
//   - nil if valid
//   - Error describing the syntax problem if invalid
//
// Example:
//
//	err := engine.Validate("https://example.com?q={{.service | urlEncode}}")
//	// err = nil (valid)
//
//	err := engine.Validate("https://example.com?q={{.service | unknownFunc}}")
//	// err = "invalid template: ... function \"unknownFunc\" not defined"
func (e *Engine) Validate(templateStr string) error {
	_, err := template.New("url").Funcs(e.funcMap).Parse(templateStr)
	if err != nil {
		return fmt.Errorf("invalid template: %w", err)
	}
	return nil
}

// ValidateWithSample validates a template and tries to execute it with sample data.
// This catches errors that only appear at execution time, like type mismatches.
//
// Parameters:
//   - templateStr: The template to validate
//   - sampleData: Sample labels to use for test execution
//
// Returns:
//   - nil if validation and test execution succeed
//   - Error describing the problem if validation fails
func (e *Engine) ValidateWithSample(templateStr string, sampleData map[string]string) error {
	// First, validate syntax
	if err := e.Validate(templateStr); err != nil {
		return err
	}

	// Then, try to execute with sample data
	_, err := e.Render(templateStr, sampleData)
	if err != nil {
		return fmt.Errorf("template execution failed with sample data: %w", err)
	}

	return nil
}

// =============================================================================
// VARIABLE EXTRACTION
// =============================================================================
// Helpers to understand what a template needs.
// =============================================================================

// ExtractVariables parses a template and returns variable names used.
// Useful for:
//   - Validating that alerts have required labels
//   - Generating documentation
//   - Dry-run previews
//
// Example:
//
//	vars := engine.ExtractVariables("https://example.com?service={{.service}}&ns={{.namespace}}")
//	// vars = ["service", "namespace"]
//
// LIMITATION: This is a best-effort extraction using simple pattern matching.
// It won't catch variables in complex expressions like {{index .labels "foo"}}.
func (e *Engine) ExtractVariables(templateStr string) []string {
	var variables []string
	seen := make(map[string]bool)

	// Simple state machine to find {{.varname}} patterns
	inTemplate := false
	var currentVar strings.Builder

	for i, c := range templateStr {
		// Check for template start: {{
		if c == '{' && i+1 < len(templateStr) && templateStr[i+1] == '{' {
			inTemplate = true
			continue
		}

		// Check for template end: }}
		if c == '}' && i+1 < len(templateStr) && templateStr[i+1] == '}' {
			inTemplate = false
			if currentVar.Len() > 0 {
				varName := currentVar.String()
				if !seen[varName] {
					variables = append(variables, varName)
					seen[varName] = true
				}
				currentVar.Reset()
			}
			continue
		}

		if inTemplate {
			// Look for .varname pattern
			if c == '.' && currentVar.Len() == 0 {
				// Start of variable reference, skip the dot
				continue
			}

			// Accumulate alphanumeric and underscore characters
			if isVarChar(c) && (currentVar.Len() > 0 || isVarStartChar(c)) {
				currentVar.WriteRune(c)
			} else if currentVar.Len() > 0 {
				// End of variable name (hit a pipe, space, etc.)
				varName := currentVar.String()
				if !seen[varName] {
					variables = append(variables, varName)
					seen[varName] = true
				}
				currentVar.Reset()
			}
		}
	}

	return variables
}

// isVarStartChar returns true if c can start a variable name.
func isVarStartChar(c rune) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

// isVarChar returns true if c can be part of a variable name.
func isVarChar(c rune) bool {
	return isVarStartChar(c) || (c >= '0' && c <= '9')
}
