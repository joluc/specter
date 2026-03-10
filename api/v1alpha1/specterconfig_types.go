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

// Package v1alpha1 contains the API types for the Specter configuration CRDs.
//
// LEARNING NOTE: Kubernetes Custom Resource Definitions (CRDs) are the standard way
// to extend Kubernetes with your own resource types. When you define a CRD, you need
// corresponding Go types that represent the structure of your custom resource.
//
// The "v1alpha1" version indicates this is an early, unstable API version.
// Kubernetes versioning convention:
//   - v1alpha1: Early development, API may change without notice
//   - v1beta1: Feature complete, minor changes possible
//   - v1: Stable, only backward-compatible changes allowed
//
// This file defines SpecterConfig, which is namespace-scoped. Teams can create
// their own configs to override cluster-wide defaults for their namespace.
package v1alpha1

import (
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// =============================================================================
// SPECTER CONFIG CRD
// =============================================================================
//
// SpecterConfig is the namespace-scoped configuration that defines URL templates
// for diagnostic links. Think of it as the "recipe book" that tells Specter how
// to generate links for different observability tools.
//
// DESIGN DECISION: We support both cluster-scoped (ClusterSpecterConfig) and
// namespace-scoped (SpecterConfig) configs. This enables multi-tenancy where:
//   - Platform team sets global defaults (ClusterSpecterConfig)
//   - Individual teams can override for their namespaces (SpecterConfig)
//
// The precedence order (highest to lowest):
//   1. Annotations directly on the PrometheusRule alert
//   2. SpecterConfig in the same namespace as the PrometheusRule
//   3. ClusterSpecterConfig (cluster-wide defaults)
//
// =============================================================================

// SpecterConfigSpec defines the desired state of SpecterConfig.
// This is where you configure your URL templates and related settings.
//
// LEARNING NOTE: In Kubernetes, the "Spec" is where you declare what you want.
// The controller reads this spec and tries to make reality match your declaration.
// This is called the "declarative" model - you describe the end state, not the steps.
type SpecterConfigSpec struct {
	// Templates is a map of template names to their configurations.
	// Common template names include: "logs", "traces", "metrics", "runbook", "oncall"
	//
	// Each template defines a URL pattern that will be rendered with alert labels
	// and injected into PrometheusRule annotations.
	//
	// Example:
	//   templates:
	//     logs:
	//       url: "https://opensearch.mycompany.io/app/discover#/?query={{.service}}"
	//       description: "View application logs in OpenSearch"
	//     traces:
	//       url: "https://jaeger.mycompany.io/trace?service={{.service}}"
	//       description: "View distributed traces in Jaeger"
	//     metrics:
	//       url: "https://grafana.mycompany.io/d/{{.dashboard}}?var-service={{.service}}"
	//       description: "View Grafana dashboard"
	//
	// +optional
	Templates map[string]TemplateConfig `json:"templates,omitempty"`

	// DefaultLabels are fallback values used when a label is not present on the alert.
	// This is useful for setting organization-wide defaults that apply when
	// individual alerts don't specify certain labels.
	//
	// Example:
	//   defaultLabels:
	//     environment: "production"
	//     region: "eu-west-1"
	//     team: "platform"
	//
	// If an alert has labels {service: "billing-api"} and defaultLabels has
	// {environment: "production"}, the template context will include both.
	//
	// +optional
	DefaultLabels map[string]string `json:"defaultLabels,omitempty"`

	// Shortener configures the optional URL shortening service.
	//
	// WHY THIS EXISTS: Observability tools like OpenSearch generate extremely
	// long URLs because they encode complex queries directly in the URL (using
	// RISON format). These URLs can be 1000+ characters, which causes problems:
	//   - Slack truncates long URLs, breaking the links
	//   - Some notification systems have URL length limits
	//   - Long URLs are hard to share verbally or copy manually
	//
	// The shortener creates compact redirect links like:
	//   https://specter.mycompany.io/s/abc123 -> (redirects to full URL)
	//
	// +optional
	Shortener *ShortenerConfig `json:"shortener,omitempty"`

	// Selector allows you to target specific PrometheusRules for this config.
	// If empty/nil, this config applies to all rules in the namespace that have
	// the label specter.joluc.de/enabled: "true".
	//
	// LEARNING NOTE: Label selectors are fundamental in Kubernetes. They're how
	// components find related resources. Examples:
	//   - Services find Pods via selectors
	//   - Deployments manage ReplicaSets via selectors
	//   - Here, SpecterConfig finds PrometheusRules via selectors
	//
	// Example - only apply to rules with specific team label:
	//   selector:
	//     matchLabels:
	//       team: "payments"
	//
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`
}

// TemplateConfig defines a single URL template and its configuration.
// Each template generates one diagnostic link that will be added to alerts.
type TemplateConfig struct {
	// URL is a Go text/template string that renders to a diagnostic URL.
	//
	// TEMPLATE VARIABLES:
	// All labels from the PrometheusRule alert are available as variables:
	//   - {{.service}}     - The service label
	//   - {{.namespace}}   - The namespace label
	//   - {{.alertname}}   - The name of the alert
	//   - {{.severity}}    - The severity label (if present)
	//   - Any other label defined on the alert
	//
	// TEMPLATE FUNCTIONS (built-in helpers):
	//   String manipulation:
	//   - {{.service | urlEncode}}         - URL-encode special characters
	//   - {{.service | lower}}             - Convert to lowercase
	//   - {{.service | upper}}             - Convert to uppercase
	//   - {{.service | replace "-" "_"}}   - Replace characters
	//   - {{.team | default "platform"}}   - Provide fallback for missing labels
	//
	//   Time functions:
	//   - {{now}}                          - Current Unix timestamp
	//   - {{nowRFC3339}}                   - Current time in RFC3339 format
	//
	//   Query escaping:
	//   - {{.query | kueryEscape}}         - Escape for Kibana Query Language
	//
	// EXAMPLE TEMPLATES:
	//
	// Simple Grafana dashboard:
	//   https://grafana.io/d/abc123?var-service={{.service | urlEncode}}
	//
	// OpenSearch with KQL query:
	//   https://opensearch.io/_dashboards/app/discover#/?_a=(query:(language:kuery,query:'service:"{{.service | kueryEscape}}"'))
	//
	// Jaeger trace search:
	//   https://jaeger.io/search?service={{.service}}&lookback=1h
	//
	// Conditional based on environment:
	//   {{if eq .env "production"}}https://prod.grafana.io{{else}}https://staging.grafana.io{{end}}/d/{{.dashboard}}
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`

	// Description is a human-readable explanation of what this link shows.
	// This appears in alert notifications to help on-call engineers understand
	// what they'll see when clicking the link.
	//
	// Good descriptions are action-oriented:
	//   - "View application logs filtered by service"
	//   - "Open Grafana dashboard showing error rates"
	//   - "Search distributed traces for this service"
	//
	// +optional
	Description string `json:"description,omitempty"`

	// Severity filters which alert severities receive this template.
	// If empty, the template applies to all severities.
	// If specified, only alerts with a matching severity label get this link.
	//
	// USE CASE: You might want expensive APM trace links only for critical alerts,
	// while cheaper log links go to all alerts:
	//
	//   templates:
	//     logs:
	//       url: "https://logs.io/..."
	//       # No severity filter - applies to all alerts
	//     traces:
	//       url: "https://expensive-apm.io/..."
	//       severity: ["critical", "page"]  # Only for high-severity alerts
	//
	// +optional
	Severity []string `json:"severity,omitempty"`

	// RequiredLabels specifies labels that must exist for this template to apply.
	// If any required label is missing from an alert, the template is skipped.
	//
	// USE CASE: Some templates only make sense when certain data is available.
	// For example, a trace link requires a traceId label:
	//
	//   templates:
	//     trace-by-id:
	//       url: "https://jaeger.io/trace/{{.traceId}}"
	//       requiredLabels: ["traceId"]
	//       description: "View the specific trace that triggered this alert"
	//
	// If an alert doesn't have traceId, this template is silently skipped.
	//
	// +optional
	RequiredLabels []string `json:"requiredLabels,omitempty"`

	// Enabled allows temporarily disabling a template without removing it.
	// Useful for maintenance or testing. Defaults to true if not specified.
	//
	// +optional
	// +kubebuilder:default=true
	Enabled *bool `json:"enabled,omitempty"`
}

// ShortenerConfig configures the URL shortening service.
type ShortenerConfig struct {
	// Enabled toggles URL shortening. When enabled, URLs exceeding MaxURLLength
	// are replaced with short redirect links.
	//
	// The shortener uses stateless compression (gzip + base64 encoding).
	// No storage, persistence, or expiration - URLs are encoded directly.
	// Short links look like: https://specter.mycompany.io/s/H4sIAAAA...
	//
	// +kubebuilder:default=false
	Enabled bool `json:"enabled,omitempty"`

	// MaxURLLength is the threshold above which URLs are shortened.
	// URLs shorter than this value are left unchanged to avoid unnecessary redirects.
	//
	// Recommended values:
	//   - 200: Conservative, shortens most observability URLs
	//   - 500: Only shortens very long URLs (OpenSearch with complex queries)
	//   - 2000: Only shortens extremely long URLs
	//
	// +kubebuilder:default=200
	// +kubebuilder:validation:Minimum=50
	MaxURLLength int `json:"maxURLLength,omitempty"`
}

// SpecterConfigStatus defines the observed state of SpecterConfig.
//
// LEARNING NOTE: In Kubernetes, Status is where the controller reports what it
// has observed and done. Users declare what they want in Spec, and the controller
// reports reality in Status. This separation is fundamental to the declarative model.
//
// Status should only be updated by the controller, never by users directly.
type SpecterConfigStatus struct {
	// Conditions represent the latest observations of the config's state.
	// Standard condition types include:
	//   - "Ready": The config is valid and actively being applied
	//   - "TemplatesValid": All URL templates parse without errors
	//   - "Degraded": Some templates have errors (see ValidationErrors)
	//
	// LEARNING NOTE: Conditions are the Kubernetes-standard way to report
	// resource state. They have:
	//   - Type: What aspect of the resource this describes
	//   - Status: True, False, or Unknown
	//   - Reason: Machine-readable reason code
	//   - Message: Human-readable explanation
	//   - LastTransitionTime: When status last changed
	//
	// Tools like kubectl and dashboards understand this pattern.
	//
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// TemplateCount is the number of enabled templates in this config.
	// This is a convenience field for quick status display with kubectl.
	//
	// +optional
	TemplateCount int `json:"templateCount,omitempty"`

	// ProcessedRules lists PrometheusRules that have been annotated by this config.
	// Useful for debugging and understanding which rules this config affects.
	//
	// +optional
	ProcessedRules []ProcessedRuleRef `json:"processedRules,omitempty"`

	// ValidationErrors contains any template parsing or validation errors.
	// If non-empty, the templates with errors won't be applied to alerts.
	// The controller will set the "Degraded" condition when errors exist.
	//
	// +optional
	ValidationErrors []string `json:"validationErrors,omitempty"`
}

// ProcessedRuleRef tracks a PrometheusRule that Specter has processed.
type ProcessedRuleRef struct {
	// Name is the name of the PrometheusRule.
	Name string `json:"name"`

	// Namespace is the namespace of the PrometheusRule.
	Namespace string `json:"namespace"`

	// LastReconciled is when this rule was last processed by Specter.
	LastReconciled metav1.Time `json:"lastReconciled"`

	// AlertCount is the number of alerts in this rule.
	AlertCount int `json:"alertCount"`

	// AnnotatedCount is the number of alerts that received Specter annotations.
	AnnotatedCount int `json:"annotatedCount"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=sc
// +kubebuilder:printcolumn:name="Templates",type="integer",JSONPath=".status.templateCount",description="Number of enabled templates"
// +kubebuilder:printcolumn:name="Rules",type="integer",JSONPath=".status.processedRules",description="Number of processed PrometheusRules"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// SpecterConfig is the Schema for the specterconfigs API.
// It defines URL templates for diagnostic links that are injected into
// PrometheusRule annotations.
//
// USAGE:
//
//  1. Create a SpecterConfig in your namespace with URL templates
//  2. Add the label specter.joluc.de/enabled: "true" to your PrometheusRules
//  3. Specter will automatically inject diagnostic URLs into your alerts
//  4. When alerts fire, the URLs appear in your notification channels
//
// EXAMPLE:
//
//	apiVersion: config.joluc.de/v1alpha1
//	kind: SpecterConfig
//	metadata:
//	  name: team-config
//	  namespace: my-team
//	spec:
//	  templates:
//	    logs:
//	      url: "https://opensearch.io/logs?service={{.service}}"
//	      description: "View application logs"
//	    traces:
//	      url: "https://jaeger.io/search?service={{.service}}"
//	      description: "Search distributed traces"
type SpecterConfig struct {
	metav1.TypeMeta `json:",inline"`

	// Standard object metadata.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// Spec defines the desired state - which templates to use and how.
	// +required
	Spec SpecterConfigSpec `json:"spec"`

	// Status defines the observed state - what Specter has actually done.
	// +optional
	Status SpecterConfigStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// SpecterConfigList contains a list of SpecterConfig resources.
//
// LEARNING NOTE: Every Kubernetes resource type needs a corresponding List type.
// This is used when listing resources (e.g., kubectl get specterconfigs).
// The List type wraps multiple instances of the resource.
type SpecterConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []SpecterConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SpecterConfig{}, &SpecterConfigList{})
}

// =============================================================================
// HELPER METHODS
// =============================================================================
// These methods provide convenient operations on our types.
// Adding methods to types is idiomatic Go - it encapsulates logic close to data.
// =============================================================================

// IsEnabled returns true if the template is enabled.
// Defaults to true if the Enabled field is not set.
func (t *TemplateConfig) IsEnabled() bool {
	if t.Enabled == nil {
		return true
	}
	return *t.Enabled
}

// MatchesSeverity checks if this template should apply to an alert with
// the given severity. Returns true if:
//   - The template has no severity filter (applies to all), OR
//   - The given severity is in the template's severity list
func (t *TemplateConfig) MatchesSeverity(severity string) bool {
	if len(t.Severity) == 0 {
		return true
	}
	return slices.Contains(t.Severity, severity)
}

// HasRequiredLabels checks if all required labels are present in the given map.
// Returns true if all required labels exist (even if empty string).
func (t *TemplateConfig) HasRequiredLabels(labels map[string]string) bool {
	for _, required := range t.RequiredLabels {
		if _, exists := labels[required]; !exists {
			return false
		}
	}
	return true
}

// GetEnabledTemplates returns only the templates that are enabled.
// This filters out any templates where enabled is explicitly set to false.
func (s *SpecterConfigSpec) GetEnabledTemplates() map[string]TemplateConfig {
	result := make(map[string]TemplateConfig)
	for name, tmpl := range s.Templates {
		if tmpl.IsEnabled() {
			result[name] = tmpl
		}
	}
	return result
}

// ApplicableTemplates returns templates that apply to an alert with the given
// severity and labels. This is the main method used during reconciliation.
func (s *SpecterConfigSpec) ApplicableTemplates(severity string, labels map[string]string) map[string]TemplateConfig {
	result := make(map[string]TemplateConfig)
	for name, tmpl := range s.Templates {
		// Skip disabled templates
		if !tmpl.IsEnabled() {
			continue
		}
		// Skip if severity doesn't match
		if !tmpl.MatchesSeverity(severity) {
			continue
		}
		// Skip if required labels are missing
		if !tmpl.HasRequiredLabels(labels) {
			continue
		}
		result[name] = tmpl
	}
	return result
}
