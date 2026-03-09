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
// This file defines ClusterSpecterConfig, the cluster-scoped version of SpecterConfig.
// It provides organization-wide defaults that apply to all namespaces.
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// =============================================================================
// CLUSTER SPECTER CONFIG CRD
// =============================================================================
//
// ClusterSpecterConfig is the cluster-scoped configuration for Specter.
// It provides organization-wide defaults that apply when no namespace-scoped
// SpecterConfig exists or when namespaced configs don't define certain templates.
//
// DESIGN DECISION: Why have both cluster-scoped and namespace-scoped configs?
//
// Multi-tenancy Pattern:
//   - ClusterSpecterConfig: Set by platform/SRE team, provides sensible defaults
//   - SpecterConfig: Set by application teams, can override for their namespace
//
// This follows the principle of "sensible defaults with team autonomy":
//   - Platform team ensures all alerts have basic diagnostic links
//   - Application teams can customize for their specific tools/dashboards
//
// PRECEDENCE (highest to lowest):
//   1. Annotations directly on the PrometheusRule alert
//   2. SpecterConfig in the same namespace as the rule
//   3. ClusterSpecterConfig (this resource)
//
// EXAMPLE USE CASE:
// Platform team creates a ClusterSpecterConfig with:
//   - logs template pointing to centralized OpenSearch
//   - metrics template pointing to shared Grafana
//
// The payments team creates a namespace SpecterConfig with:
//   - traces template pointing to their dedicated Jaeger instance
//   - logs template pointing to their specific OpenSearch index
//
// Result: Payments alerts get the team-specific logs/traces and the
// cluster-wide metrics template.
//
// =============================================================================

// ClusterSpecterConfigSpec defines the desired state of ClusterSpecterConfig.
// It's identical to SpecterConfigSpec - the only difference is scope.
type ClusterSpecterConfigSpec struct {
	// Templates is a map of template names to their configurations.
	// These templates apply cluster-wide as defaults.
	//
	// +optional
	Templates map[string]TemplateConfig `json:"templates,omitempty"`

	// DefaultLabels are fallback values used when a label is not present on alerts.
	// These apply cluster-wide.
	//
	// +optional
	DefaultLabels map[string]string `json:"defaultLabels,omitempty"`

	// Shortener configures the cluster-wide URL shortening service.
	//
	// +optional
	Shortener *ShortenerConfig `json:"shortener,omitempty"`

	// Selector allows targeting specific PrometheusRules cluster-wide.
	// If empty, applies to all rules with specter.joluc.de/enabled: "true".
	//
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`
}

// ClusterSpecterConfigStatus defines the observed state of ClusterSpecterConfig.
type ClusterSpecterConfigStatus struct {
	// Conditions represent the latest observations of the config's state.
	//
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// TemplateCount is the number of enabled templates in this config.
	//
	// +optional
	TemplateCount int `json:"templateCount,omitempty"`

	// ProcessedRulesCount is the total number of PrometheusRules processed
	// using this cluster config (where no namespace config overrides).
	//
	// +optional
	ProcessedRulesCount int `json:"processedRulesCount,omitempty"`

	// NamespacesUsing lists namespaces that are using this cluster config
	// (i.e., namespaces without their own SpecterConfig).
	//
	// +optional
	NamespacesUsing []string `json:"namespacesUsing,omitempty"`

	// ValidationErrors contains any template parsing errors.
	//
	// +optional
	ValidationErrors []string `json:"validationErrors,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=csc
// +kubebuilder:printcolumn:name="Templates",type="integer",JSONPath=".status.templateCount",description="Number of enabled templates"
// +kubebuilder:printcolumn:name="Namespaces",type="integer",JSONPath=".status.processedRulesCount",description="Rules using this config"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// ClusterSpecterConfig is the Schema for the cluster-scoped Specter configuration.
// It defines organization-wide URL templates that serve as defaults for all namespaces.
//
// USAGE:
//
//  1. Platform/SRE team creates a ClusterSpecterConfig with default templates
//  2. All PrometheusRules with specter.joluc.de/enabled: "true" get these templates
//  3. Teams can override by creating namespace-scoped SpecterConfig resources
//
// EXAMPLE:
//
//	apiVersion: config.joluc.de/v1alpha1
//	kind: ClusterSpecterConfig
//	metadata:
//	  name: global  # Convention: use "global" for the primary cluster config
//	spec:
//	  templates:
//	    logs:
//	      url: "https://opensearch.company.io/logs?service={{.service}}"
//	      description: "View logs in central OpenSearch"
//	    metrics:
//	      url: "https://grafana.company.io/d/overview?var-service={{.service}}"
//	      description: "View service dashboard in Grafana"
//	  defaultLabels:
//	    environment: "production"
//	  shortener:
//	    enabled: true
//	    baseURL: "https://specter.company.io"
type ClusterSpecterConfig struct {
	metav1.TypeMeta `json:",inline"`

	// Standard object metadata.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// Spec defines the desired cluster-wide configuration.
	// +required
	Spec ClusterSpecterConfigSpec `json:"spec"`

	// Status defines the observed state.
	// +optional
	Status ClusterSpecterConfigStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ClusterSpecterConfigList contains a list of ClusterSpecterConfig resources.
type ClusterSpecterConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ClusterSpecterConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterSpecterConfig{}, &ClusterSpecterConfigList{})
}

// =============================================================================
// HELPER METHODS
// =============================================================================

// GetEnabledTemplates returns only the templates that are enabled.
func (s *ClusterSpecterConfigSpec) GetEnabledTemplates() map[string]TemplateConfig {
	result := make(map[string]TemplateConfig)
	for name, tmpl := range s.Templates {
		if tmpl.IsEnabled() {
			result[name] = tmpl
		}
	}
	return result
}

// ApplicableTemplates returns templates that apply to an alert with the given
// severity and labels.
func (s *ClusterSpecterConfigSpec) ApplicableTemplates(severity string, labels map[string]string) map[string]TemplateConfig {
	result := make(map[string]TemplateConfig)
	for name, tmpl := range s.Templates {
		if !tmpl.IsEnabled() {
			continue
		}
		if !tmpl.MatchesSeverity(severity) {
			continue
		}
		if !tmpl.HasRequiredLabels(labels) {
			continue
		}
		result[name] = tmpl
	}
	return result
}
