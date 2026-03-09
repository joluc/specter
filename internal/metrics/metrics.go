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

// Package metrics exposes Prometheus metrics for monitoring Specter itself.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	resultSuccess = "success"
	resultError   = "error"
)

// =============================================================================
// METRIC DEFINITIONS
// =============================================================================
//
// All metrics are defined here as package-level variables.
// We use promauto which automatically registers metrics with the default
// Prometheus registry.
//
// LEARNING NOTE: promauto is convenient for most cases. For more control
// (e.g., custom registries for testing), use prometheus.NewCounter() etc.
// and register manually with prometheus.MustRegister().
//
// =============================================================================

var (
	// =========================================================================
	// RECONCILIATION METRICS
	// =========================================================================
	// These track the core reconciliation loop that processes PrometheusRules.

	// ReconcileTotal counts reconciliation attempts for PrometheusRules.
	//
	// Labels:
	//   - result: "success" or "error"
	//   - namespace: The namespace of the PrometheusRule
	//
	// Example PromQL queries:
	//   - rate(specter_reconcile_total{result="error"}[5m])
	//     Shows error rate over last 5 minutes
	//   - sum(increase(specter_reconcile_total[1h])) by (namespace)
	//     Shows reconciles per namespace in last hour
	ReconcileTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "specter_reconcile_total",
			Help: "Total number of PrometheusRule reconciliations",
		},
		[]string{"result", "namespace"},
	)

	// ReconcileDuration tracks how long reconciliations take.
	//
	// LEARNING NOTE: Histograms bucket observations into ranges, allowing
	// server-side percentile calculation. Default buckets are:
	// .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10 seconds
	//
	// Example PromQL queries:
	//   - histogram_quantile(0.99, rate(specter_reconcile_duration_seconds_bucket[5m]))
	//     Shows 99th percentile latency
	//   - specter_reconcile_duration_seconds_sum / specter_reconcile_duration_seconds_count
	//     Shows average latency (less useful than percentiles)
	ReconcileDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "specter_reconcile_duration_seconds",
			Help:    "Duration of PrometheusRule reconciliations in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"namespace"},
	)

	// RulesWatched is a gauge of currently watched PrometheusRules.
	// This tracks rules that have specter.joluc.de/enabled: "true".
	//
	// Labels:
	//   - namespace: The namespace of the rules
	//
	// LEARNING NOTE: This is a Gauge (not Counter) because the number can
	// go down when rules are deleted or have the label removed.
	RulesWatched = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "specter_rules_watched",
			Help: "Number of PrometheusRules currently being watched by Specter",
		},
		[]string{"namespace"},
	)

	// =========================================================================
	// TEMPLATE METRICS
	// =========================================================================
	// These track URL template rendering operations.

	// TemplateRenderTotal counts template render attempts.
	//
	// Labels:
	//   - template: The template name (logs, traces, metrics, etc.)
	//   - result: "success" or "error"
	TemplateRenderTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "specter_template_render_total",
			Help: "Total number of URL template render attempts",
		},
		[]string{"template", "result"},
	)

	// TemplateErrorsTotal counts template errors by type.
	//
	// Labels:
	//   - template: The template name
	//   - error_type: "parse_error", "render_error", "missing_variable"
	TemplateErrorsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "specter_template_errors_total",
			Help: "Total number of template errors by type",
		},
		[]string{"template", "error_type"},
	)

	// =========================================================================
	// ALERT ANNOTATION METRICS
	// =========================================================================

	// AlertsAnnotated counts alerts that have received Specter annotations.
	//
	// Labels:
	//   - namespace: The namespace of the PrometheusRule
	AlertsAnnotated = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "specter_alerts_annotated",
			Help: "Number of alerts with Specter annotations",
		},
		[]string{"namespace"},
	)

	// LinksGenerated counts diagnostic links generated.
	//
	// Labels:
	//   - template: The type of link (logs, traces, metrics, etc.)
	//   - namespace: The namespace of the PrometheusRule
	LinksGenerated = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "specter_links_generated_total",
			Help: "Total number of diagnostic links generated",
		},
		[]string{"template", "namespace"},
	)

	// =========================================================================
	// URL SHORTENER METRICS
	// =========================================================================

	// ShortenerOperationsTotal counts URL shortener operations.
	//
	// Labels:
	//   - operation: "shortened", "lookup_hit", "lookup_miss", "expired"
	ShortenerOperationsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "specter_shortener_operations_total",
			Help: "Total URL shortener operations",
		},
		[]string{"operation"},
	)

	// ShortenerStoredURLs is a gauge of URLs currently in the shortener store.
	ShortenerStoredURLs = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "specter_shortener_stored_urls",
			Help: "Current number of URLs stored in the shortener",
		},
	)

	// ShortenerRedirectLatency tracks redirect lookup latency.
	ShortenerRedirectLatency = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "specter_shortener_redirect_latency_seconds",
			Help:    "Latency of URL shortener redirects",
			Buckets: []float64{0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05},
		},
	)

	// =========================================================================
	// CONFIGURATION METRICS
	// =========================================================================

	// ConfigReloadsTotal counts SpecterConfig reloads.
	//
	// Labels:
	//   - result: "success" or "error"
	//   - scope: "cluster" or "namespace"
	ConfigReloadsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "specter_config_reloads_total",
			Help: "Total number of SpecterConfig reloads",
		},
		[]string{"result", "scope"},
	)

	// ConfigTemplates shows templates per config.
	//
	// Labels:
	//   - config_name: Name of the SpecterConfig
	//   - config_namespace: Namespace (empty for cluster-scoped)
	ConfigTemplates = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "specter_config_templates",
			Help: "Number of templates defined in each SpecterConfig",
		},
		[]string{"config_name", "config_namespace"},
	)

	// =========================================================================
	// WEBHOOK METRICS
	// =========================================================================
	// These track the validating webhook (if enabled).

	// WebhookRequestsTotal counts validation webhook requests.
	//
	// Labels:
	//   - operation: "CREATE", "UPDATE", "DELETE"
	//   - result: "allowed" or "denied"
	WebhookRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "specter_webhook_requests_total",
			Help: "Total validation webhook requests",
		},
		[]string{"operation", "result"},
	)

	// WebhookLatency tracks webhook response time.
	WebhookLatency = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "specter_webhook_latency_seconds",
			Help:    "Webhook request latency in seconds",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25},
		},
	)

	// =========================================================================
	// BUILD INFO METRIC
	// =========================================================================
	// Info metrics expose static information as labels.
	// They always have a value of 1.

	// BuildInfo exposes build information about Specter.
	//
	// Labels include version, commit hash, build date, Go version.
	//
	// Example: specter_build_info{version="v0.1.0", commit="abc123"} 1
	//
	// LEARNING NOTE: Info metrics are a common pattern. They let you:
	//   - Track which version is running
	//   - Join with other metrics for analysis
	//   - Alert on version mismatches in a cluster
	BuildInfo = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "specter_build_info",
			Help: "Build information about Specter",
		},
		[]string{"version", "commit", "build_date", "go_version"},
	)
)

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================
// These make it easier to record metrics with the correct labels.
// Using helper functions ensures consistency and reduces boilerplate.
// =============================================================================

// RecordReconcile records a reconciliation attempt.
//
// Parameters:
//   - namespace: The namespace of the reconciled PrometheusRule
//   - err: The error (nil for success)
//   - durationSeconds: How long the reconciliation took
//
// Example:
//
//	start := time.Now()
//	err := reconcileRule(rule)
//	metrics.RecordReconcile(rule.Namespace, err, time.Since(start).Seconds())
func RecordReconcile(namespace string, err error, durationSeconds float64) {
	result := resultSuccess
	if err != nil {
		result = resultError
	}

	ReconcileTotal.WithLabelValues(result, namespace).Inc()
	ReconcileDuration.WithLabelValues(namespace).Observe(durationSeconds)
}

// RecordTemplateRender records a template render attempt.
func RecordTemplateRender(templateName string, err error) {
	result := resultSuccess
	if err != nil {
		result = resultError
	}
	TemplateRenderTotal.WithLabelValues(templateName, result).Inc()
}

// RecordTemplateError records a specific template error.
//
// Parameters:
//   - templateName: Name of the template
//   - errorType: Type of error ("parse_error", "render_error", "missing_variable")
func RecordTemplateError(templateName, errorType string) {
	TemplateErrorsTotal.WithLabelValues(templateName, errorType).Inc()
}

// RecordLinkGenerated records that a diagnostic link was generated.
//
// Parameters:
//   - templateName: Type of link (e.g., "logs", "traces")
//   - namespace: Namespace of the PrometheusRule
func RecordLinkGenerated(templateName, namespace string) {
	LinksGenerated.WithLabelValues(templateName, namespace).Inc()
}

// RecordShortenerOperation records a URL shortener operation.
//
// Parameters:
//   - operation: One of "shortened", "lookup_hit", "lookup_miss", "expired"
func RecordShortenerOperation(operation string) {
	ShortenerOperationsTotal.WithLabelValues(operation).Inc()
}

// RecordConfigReload records a config reload attempt.
//
// Parameters:
//   - scope: "cluster" or "namespace"
//   - err: The error (nil for success)
func RecordConfigReload(scope string, err error) {
	result := resultSuccess
	if err != nil {
		result = resultError
	}
	ConfigReloadsTotal.WithLabelValues(result, scope).Inc()
}

// RecordWebhookRequest records a webhook validation request.
//
// Parameters:
//   - operation: The Kubernetes operation ("CREATE", "UPDATE", "DELETE")
//   - allowed: Whether the request was allowed
//   - latencySeconds: How long the webhook took
func RecordWebhookRequest(operation string, allowed bool, latencySeconds float64) {
	result := "allowed"
	if !allowed {
		result = "denied"
	}
	WebhookRequestsTotal.WithLabelValues(operation, result).Inc()
	WebhookLatency.Observe(latencySeconds)
}

// SetBuildInfo sets the build info metric.
// Call this once at startup.
//
// Example:
//
//	metrics.SetBuildInfo("v0.1.0", "abc123", "2024-01-15", "go1.22")
func SetBuildInfo(version, commit, buildDate, goVersion string) {
	BuildInfo.WithLabelValues(version, commit, buildDate, goVersion).Set(1)
}

// UpdateRulesWatched updates the count of watched rules for a namespace.
func UpdateRulesWatched(namespace string, count float64) {
	RulesWatched.WithLabelValues(namespace).Set(count)
}

// UpdateAlertsAnnotated updates the count of annotated alerts for a namespace.
func UpdateAlertsAnnotated(namespace string, count float64) {
	AlertsAnnotated.WithLabelValues(namespace).Set(count)
}

// UpdateShortenerStoredURLs updates the count of stored URLs.
func UpdateShortenerStoredURLs(count float64) {
	ShortenerStoredURLs.Set(count)
}

// UpdateConfigTemplates updates the template count for a config.
func UpdateConfigTemplates(configName, configNamespace string, count float64) {
	ConfigTemplates.WithLabelValues(configName, configNamespace).Set(count)
}
