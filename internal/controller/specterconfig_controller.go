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

// Package controller implements the Kubernetes controllers for Specter.
//
// LEARNING NOTE: Controllers are the heart of Kubernetes operators. They implement
// the "reconciliation loop" pattern:
//
//  1. WATCH: Observe changes to resources (PrometheusRules, SpecterConfigs)
//  2. COMPARE: Determine what needs to change (are annotations missing?)
//  3. ACT: Make changes to reach desired state (add annotations)
//  4. REPEAT: Controller-runtime handles requeueing automatically
//
// This is the "Level-Triggered" or "Declarative" model:
//   - You don't react to individual events (create, update, delete)
//   - You always reconcile toward the desired state
//   - The reconciler should be idempotent (same input = same output)
//
// SPECTER ARCHITECTURE:
//
//	┌─────────────────────────────────────────────────────────────────────┐
//	│                         Kubernetes Cluster                          │
//	├─────────────────────────────────────────────────────────────────────┤
//	│                                                                     │
//	│  ┌──────────────────┐     watches      ┌──────────────────────┐    │
//	│  │  PrometheusRule  │ ◄─────────────── │  Specter Controller  │    │
//	│  │  (with label     │                  │                      │    │
//	│  │   specter.io/    │  patches         │  - Template Engine   │    │
//	│  │   enabled:true)  │ ◄─────────────── │  - URL Shortener     │    │
//	│  └──────────────────┘                  │  - Metrics           │    │
//	│                                        │                      │    │
//	│  ┌──────────────────┐     reads        └──────────────────────┘    │
//	│  │  SpecterConfig   │ ◄────────────────          │                 │
//	│  │  (templates)     │                            │                 │
//	│  └──────────────────┘                            │                 │
//	│                                                  │                 │
//	│  ┌──────────────────┐     reads                  │                 │
//	│  │ClusterSpecterConf│ ◄──────────────────────────┘                 │
//	│  │  (defaults)      │                                              │
//	│  └──────────────────┘                                              │
//	│                                                                    │
//	└────────────────────────────────────────────────────────────────────┘
//
// RECONCILIATION FLOW:
//  1. Watch PrometheusRule with specter.joluc.de/enabled: "true" label
//  2. For each rule, find applicable SpecterConfig (namespace > cluster)
//  3. For each alert in the rule, render URL templates with alert labels
//  4. Patch the rule's alerts with generated annotation URLs
//  5. Update status and metrics
package controller

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"time"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configv1alpha1 "github.com/joluc/specter/api/v1alpha1"
	"github.com/joluc/specter/internal/metrics"
	"github.com/joluc/specter/internal/shortener"
	"github.com/joluc/specter/internal/template"
)

// =============================================================================
// CONSTANTS
// =============================================================================

const (
	// LabelEnabled is the label that must be present on PrometheusRules
	// for Specter to process them. This is an opt-in mechanism.
	LabelEnabled = "specter.joluc.de/enabled"

	// LabelSkip is a label that can be added to individual alerts within
	// a PrometheusRule to skip Specter processing for that alert.
	LabelSkip = "specter.joluc.de/skip"

	// AnnotationPrefix is the prefix for all Specter-generated annotations.
	AnnotationPrefix = "specter.joluc.de/"

	// AnnotationLastReconciled records when Specter last processed this rule.
	AnnotationLastReconciled = "specter.joluc.de/last-reconciled"

	// AnnotationConfigUsed records which SpecterConfig was used.
	AnnotationConfigUsed = "specter.joluc.de/config-used"

	// DefaultClusterConfigName is the expected name for the cluster-wide config.
	DefaultClusterConfigName = "global"

	// LabelValueTrue is the string "true" used for label comparisons.
	LabelValueTrue = "true"
)

// =============================================================================
// SPECTER CONFIG RECONCILER
// =============================================================================
//
// SpecterConfigReconciler watches SpecterConfig resources and validates them.
// When a SpecterConfig changes, it also triggers reconciliation of all
// PrometheusRules that might be affected.
//
// =============================================================================

// SpecterConfigReconciler reconciles a SpecterConfig object.
type SpecterConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Logger *slog.Logger

	// TemplateEngine validates templates in the config.
	TemplateEngine *template.Engine
}

// +kubebuilder:rbac:groups=config.joluc.de,resources=specterconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=config.joluc.de,resources=specterconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=config.joluc.de,resources=specterconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups=config.joluc.de,resources=clusterspecterconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=config.joluc.de,resources=clusterspecterconfigs/status,verbs=get;update;patch

// Reconcile handles SpecterConfig changes.
//
// LEARNING NOTE: The Reconcile function is called whenever:
//   - A SpecterConfig is created, updated, or deleted
//   - The reconcile is requeued (e.g., after an error or explicit request)
//   - A watched resource triggers re-reconciliation
//
// The function should be idempotent - calling it multiple times with the
// same input should produce the same result.
func (r *SpecterConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Logger.With(
		slog.String("controller", "SpecterConfig"),
		slog.String("namespace", req.Namespace),
		slog.String("name", req.Name),
	)
	logger.Debug("reconciling SpecterConfig")

	// Fetch the SpecterConfig instance.
	var config configv1alpha1.SpecterConfig
	if err := r.Get(ctx, req.NamespacedName, &config); err != nil {
		if errors.IsNotFound(err) {
			// Config was deleted. Nothing to do - the PrometheusRule reconciler
			// will handle updating rules that used this config.
			logger.Debug("SpecterConfig not found, likely deleted")
			return ctrl.Result{}, nil
		}
		logger.Error("failed to get SpecterConfig", slog.String("error", err.Error()))
		return ctrl.Result{}, err
	}

	// Validate all templates in the config.
	validationErrors := r.validateTemplates(&config)

	// Update status with validation results.
	config.Status.TemplateCount = len(config.Spec.GetEnabledTemplates())
	config.Status.ValidationErrors = validationErrors

	// Set conditions based on validation.
	if len(validationErrors) > 0 {
		setCondition(&config.Status.Conditions, metav1.Condition{
			Type:    "TemplatesValid",
			Status:  metav1.ConditionFalse,
			Reason:  "ValidationFailed",
			Message: fmt.Sprintf("%d template(s) have errors", len(validationErrors)),
		})
		setCondition(&config.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionFalse,
			Reason:  "ValidationFailed",
			Message: "Some templates have validation errors",
		})
	} else {
		setCondition(&config.Status.Conditions, metav1.Condition{
			Type:    "TemplatesValid",
			Status:  metav1.ConditionTrue,
			Reason:  "AllTemplatesValid",
			Message: "All templates parsed successfully",
		})
		setCondition(&config.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionTrue,
			Reason:  "ConfigurationValid",
			Message: "Configuration is valid and active",
		})
	}

	// Update the status subresource.
	if err := r.Status().Update(ctx, &config); err != nil {
		logger.Error("failed to update SpecterConfig status", slog.String("error", err.Error()))
		return ctrl.Result{}, err
	}

	// Update metrics.
	metrics.UpdateConfigTemplates(config.Name, config.Namespace, float64(config.Status.TemplateCount))
	metrics.RecordConfigReload("namespace", nil)

	logger.Info("SpecterConfig reconciled",
		slog.Int("templates", config.Status.TemplateCount),
		slog.Int("validation_errors", len(validationErrors)),
	)

	return ctrl.Result{}, nil
}

// validateTemplates checks all templates in a SpecterConfig for syntax errors.
func (r *SpecterConfigReconciler) validateTemplates(config *configv1alpha1.SpecterConfig) []string {
	var validationErrs []string

	for name, tmpl := range config.Spec.Templates {
		if err := r.TemplateEngine.Validate(tmpl.URL); err != nil {
			validationErrs = append(validationErrs, fmt.Sprintf("template %q: %v", name, err))
		}
	}

	return validationErrs
}

// SetupWithManager sets up the controller with the Manager.
func (r *SpecterConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&configv1alpha1.SpecterConfig{}).
		Named("specterconfig").
		Complete(r)
}

// =============================================================================
// PROMETHEUS RULE RECONCILER
// =============================================================================
//
// PrometheusRuleReconciler is the main controller. It watches PrometheusRules
// and injects diagnostic URL annotations based on SpecterConfig templates.
//
// =============================================================================

// PrometheusRuleReconciler reconciles PrometheusRule objects.
type PrometheusRuleReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Logger *slog.Logger

	// TemplateEngine renders URL templates.
	TemplateEngine *template.Engine

	// URLShortener optionally shortens long URLs via stateless compression.
	URLShortener *shortener.Shortener
}

// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=prometheusrules,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=prometheusrules/status,verbs=get

// Reconcile handles PrometheusRule changes.
//
// This is where the main Specter logic lives:
//  1. Check if the rule has the enabled label
//  2. Find the applicable SpecterConfig
//  3. For each alert, render templates and inject annotations
//  4. Patch the PrometheusRule with new annotations
func (r *PrometheusRuleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	start := time.Now()
	logger := r.Logger.With(
		slog.String("controller", "PrometheusRule"),
		slog.String("namespace", req.Namespace),
		slog.String("name", req.Name),
	)
	logger.Debug("reconciling PrometheusRule")

	// Fetch the PrometheusRule.
	var rule monitoringv1.PrometheusRule
	if err := r.Get(ctx, req.NamespacedName, &rule); err != nil {
		if errors.IsNotFound(err) {
			logger.Debug("PrometheusRule not found, likely deleted")
			return ctrl.Result{}, nil
		}
		logger.Error("failed to get PrometheusRule", slog.String("error", err.Error()))
		metrics.RecordReconcile(req.Namespace, err, time.Since(start).Seconds())
		return ctrl.Result{}, err
	}

	// Check if Specter is enabled for this rule.
	if !isSpecterEnabled(&rule) {
		logger.Debug("Specter not enabled for this rule, skipping")
		return ctrl.Result{}, nil
	}

	// Find the applicable SpecterConfig.
	config, err := r.findConfig(ctx, req.Namespace)
	if err != nil {
		logger.Error("failed to find SpecterConfig", slog.String("error", err.Error()))
		metrics.RecordReconcile(req.Namespace, err, time.Since(start).Seconds())
		return ctrl.Result{}, err
	}
	if config == nil {
		logger.Warn("no SpecterConfig found for namespace, skipping")
		return ctrl.Result{}, nil
	}

	// Process the rule and inject annotations.
	modified, alertCount := r.processRule(&rule, config)

	// If we made changes, update the rule.
	if modified {
		// Add metadata annotations.
		if rule.Annotations == nil {
			rule.Annotations = make(map[string]string)
		}
		rule.Annotations[AnnotationLastReconciled] = time.Now().UTC().Format(time.RFC3339)
		rule.Annotations[AnnotationConfigUsed] = fmt.Sprintf("%s/%s", config.Namespace, config.Name)

		if err := r.Update(ctx, &rule); err != nil {
			logger.Error("failed to update PrometheusRule", slog.String("error", err.Error()))
			metrics.RecordReconcile(req.Namespace, err, time.Since(start).Seconds())
			return ctrl.Result{}, err
		}

		logger.Info("PrometheusRule updated with diagnostic links",
			slog.Int("alerts_processed", alertCount),
			slog.String("config_used", config.Name),
		)
	} else {
		logger.Debug("no changes needed for PrometheusRule")
	}

	// Record metrics.
	metrics.RecordReconcile(req.Namespace, nil, time.Since(start).Seconds())
	metrics.UpdateAlertsAnnotated(req.Namespace, float64(alertCount))

	return ctrl.Result{}, nil
}

// isSpecterEnabled checks if a PrometheusRule has the Specter enabled label.
func isSpecterEnabled(rule *monitoringv1.PrometheusRule) bool {
	if rule.Labels == nil {
		return false
	}
	return rule.Labels[LabelEnabled] == LabelValueTrue
}

// findConfig finds the applicable SpecterConfig for a namespace.
// It first looks for a namespace-scoped config, then falls back to cluster-scoped.
func (r *PrometheusRuleReconciler) findConfig(ctx context.Context, namespace string) (*configv1alpha1.SpecterConfig, error) {
	// First, try to find a namespace-scoped SpecterConfig.
	var configList configv1alpha1.SpecterConfigList
	if err := r.List(ctx, &configList, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("failed to list SpecterConfigs: %w", err)
	}

	// If we found configs in the namespace, use the first one.
	// TODO: Support multiple configs with selectors.
	if len(configList.Items) > 0 {
		return &configList.Items[0], nil
	}

	// Fall back to cluster-scoped config.
	var clusterConfig configv1alpha1.ClusterSpecterConfig
	if err := r.Get(ctx, types.NamespacedName{Name: DefaultClusterConfigName}, &clusterConfig); err != nil {
		if errors.IsNotFound(err) {
			// No config found anywhere.
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get ClusterSpecterConfig: %w", err)
	}

	// Convert ClusterSpecterConfig to SpecterConfig for uniform handling.
	// This is a bit wasteful but keeps the processing logic simple.
	return &configv1alpha1.SpecterConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterConfig.Name,
			Namespace: "", // Cluster-scoped
		},
		Spec: configv1alpha1.SpecterConfigSpec{
			Templates:     clusterConfig.Spec.Templates,
			DefaultLabels: clusterConfig.Spec.DefaultLabels,
			Shortener:     clusterConfig.Spec.Shortener,
			Selector:      clusterConfig.Spec.Selector,
		},
	}, nil
}

// processRule processes a PrometheusRule and injects annotations into its alerts.
// Returns whether the rule was modified and the number of alerts processed.
func (r *PrometheusRuleReconciler) processRule(
	rule *monitoringv1.PrometheusRule,
	config *configv1alpha1.SpecterConfig,
) (modified bool, alertCount int) {

	logger := r.Logger.With(
		slog.String("rule", rule.Name),
		slog.String("namespace", rule.Namespace),
	)

	// Iterate through all groups and alerts in the rule.
	for gi, group := range rule.Spec.Groups {
		for ai, alert := range group.Rules {
			// Skip non-alerting rules (recording rules).
			if alert.Alert == "" {
				continue
			}

			alertCount++

			// Check if this alert should be skipped.
			if shouldSkipAlert(alert) {
				logger.Debug("skipping alert due to skip label",
					slog.String("alert", alert.Alert),
				)
				continue
			}

			// Build the context (labels) for template rendering.
			alertCtx := buildAlertContext(alert, rule, config.Spec.DefaultLabels)

			// Get applicable templates for this alert.
			severity := ""
			if alert.Labels != nil {
				severity = alert.Labels["severity"]
			}
			templates := config.Spec.ApplicableTemplates(severity, alertCtx)

			// Render each template and add to annotations.
			for templateName, templateConfig := range templates {
				url, err := r.TemplateEngine.Render(templateConfig.URL, alertCtx)
				if err != nil {
					logger.Warn("failed to render template",
						slog.String("alert", alert.Alert),
						slog.String("template", templateName),
						slog.String("error", err.Error()),
					)
					metrics.RecordTemplateError(templateName, "render_error")
					continue
				}

				// Optionally shorten the URL.
				if r.URLShortener != nil && config.Spec.Shortener != nil && config.Spec.Shortener.Enabled {
					maxLen := config.Spec.Shortener.MaxURLLength
					if maxLen == 0 {
						maxLen = 200
					}

					shortened, err := r.URLShortener.ShortenIfNeeded(url, maxLen)
					if err != nil {
						logger.Warn("failed to shorten URL, using original",
							slog.String("alert", alert.Alert),
							slog.String("template", templateName),
							slog.String("error", err.Error()),
						)
						// Continue with original URL on error
					} else {
						url = shortened
					}
				}

				// Add the annotation.
				annotationKey := AnnotationPrefix + templateName
				if rule.Spec.Groups[gi].Rules[ai].Annotations == nil {
					rule.Spec.Groups[gi].Rules[ai].Annotations = make(map[string]string)
				}

				// Only mark as modified if the value actually changed.
				if rule.Spec.Groups[gi].Rules[ai].Annotations[annotationKey] != url {
					rule.Spec.Groups[gi].Rules[ai].Annotations[annotationKey] = url
					modified = true
				}

				metrics.RecordTemplateRender(templateName, nil)
				metrics.RecordLinkGenerated(templateName, rule.Namespace)
			}
		}
	}

	return modified, alertCount
}

// shouldSkipAlert checks if an alert has the skip label.
func shouldSkipAlert(alert monitoringv1.Rule) bool {
	if alert.Labels == nil {
		return false
	}
	return alert.Labels[LabelSkip] == LabelValueTrue
}

// buildAlertContext creates the template context from alert labels.
// It merges default labels with alert labels (alert labels take precedence).
func buildAlertContext(
	alert monitoringv1.Rule,
	rule *monitoringv1.PrometheusRule,
	defaultLabels map[string]string,
) map[string]string {
	alertCtx := make(map[string]string)

	// Start with default labels.
	maps.Copy(alertCtx, defaultLabels)

	// Add rule-level metadata.
	alertCtx["alertname"] = alert.Alert
	alertCtx["namespace"] = rule.Namespace
	alertCtx["rule_name"] = rule.Name

	// Add alert labels (these override defaults).
	if alert.Labels != nil {
		maps.Copy(alertCtx, alert.Labels)
	}

	return alertCtx
}

// SetupWithManager sets up the controller with the Manager.
func (r *PrometheusRuleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Create a predicate that filters for rules with the enabled label.
	//
	// LEARNING NOTE: Predicates filter which events trigger reconciliation.
	// Without this, we'd reconcile EVERY PrometheusRule in the cluster,
	// even those that don't use Specter. This is both inefficient and
	// could cause unexpected behavior.
	enabledPredicate := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		labels := obj.GetLabels()
		return labels != nil && labels[LabelEnabled] == LabelValueTrue
	})

	return ctrl.NewControllerManagedBy(mgr).
		// Watch PrometheusRules with the enabled label.
		For(&monitoringv1.PrometheusRule{}, builder.WithPredicates(enabledPredicate)).
		// Also watch SpecterConfigs - when they change, re-reconcile affected rules.
		//
		// LEARNING NOTE: Watches() lets you trigger reconciliation of one resource
		// type when another changes. Here, when a SpecterConfig changes, we want
		// to re-reconcile all PrometheusRules in that namespace.
		Watches(
			&configv1alpha1.SpecterConfig{},
			handler.EnqueueRequestsFromMapFunc(r.findRulesForConfig),
		).
		Named("prometheusrule").
		Complete(r)
}

// findRulesForConfig returns reconcile requests for all enabled PrometheusRules
// in the same namespace as the changed SpecterConfig.
func (r *PrometheusRuleReconciler) findRulesForConfig(ctx context.Context, obj client.Object) []reconcile.Request {
	config, ok := obj.(*configv1alpha1.SpecterConfig)
	if !ok {
		return nil
	}

	// List all PrometheusRules in the config's namespace with the enabled label.
	var ruleList monitoringv1.PrometheusRuleList
	if err := r.List(ctx, &ruleList,
		client.InNamespace(config.Namespace),
		client.MatchingLabels{LabelEnabled: "true"},
	); err != nil {
		r.Logger.Error("failed to list PrometheusRules for config change",
			slog.String("config", config.Name),
			slog.String("error", err.Error()),
		)
		return nil
	}

	// Create reconcile requests for each rule.
	requests := make([]reconcile.Request, 0, len(ruleList.Items))
	for _, rule := range ruleList.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      rule.Name,
				Namespace: rule.Namespace,
			},
		})
	}

	r.Logger.Debug("config change triggered rule reconciliation",
		slog.String("config", config.Name),
		slog.Int("rules", len(requests)),
	)

	return requests
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

// setCondition updates or adds a condition in the conditions slice.
//
// LEARNING NOTE: Conditions are the standard Kubernetes way to report resource
// state. This helper ensures we update existing conditions rather than
// duplicating them, and sets the LastTransitionTime appropriately.
func setCondition(conditions *[]metav1.Condition, newCondition metav1.Condition) {
	now := metav1.Now()
	newCondition.LastTransitionTime = now

	// Look for existing condition of the same type.
	for i, existing := range *conditions {
		if existing.Type == newCondition.Type {
			// Only update LastTransitionTime if status actually changed.
			if existing.Status == newCondition.Status {
				newCondition.LastTransitionTime = existing.LastTransitionTime
			}
			(*conditions)[i] = newCondition
			return
		}
	}

	// Condition doesn't exist, add it.
	*conditions = append(*conditions, newCondition)
}
