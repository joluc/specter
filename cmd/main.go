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

// Package main is the entry point for the Specter operator.
//
// LEARNING NOTE: A Kubernetes operator is a custom controller that extends
// Kubernetes to manage application-specific resources. Specter is an operator
// that watches PrometheusRules and automatically injects diagnostic URLs.
//
// THE OPERATOR LIFECYCLE:
//  1. Parse command-line flags
//  2. Set up logging (we use Go 1.21+ slog for structured logging)
//  3. Create a Manager (controller-runtime's main orchestrator)
//  4. Register our CRDs with the scheme
//  5. Create and register controllers
//  6. Start the manager (blocks until shutdown signal)
//
// CONTROLLER-RUNTIME COMPONENTS:
//   - Manager: Coordinates all controllers, caches, webhooks, and metrics
//   - Controller: Watches resources and reconciles them toward desired state
//   - Reconciler: The business logic that makes changes
//   - Scheme: Registry mapping Go types to Kubernetes API types
package main

import (
	"crypto/tls"
	"flag"
	"log/slog"
	"os"
	goruntime "runtime"
	"time"

	// Import all Kubernetes client auth plugins (Azure, GCP, OIDC, etc.)
	// This ensures the operator can authenticate with any Kubernetes cluster.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"github.com/go-logr/logr"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	configv1alpha1 "github.com/joluc/specter/api/v1alpha1"
	"github.com/joluc/specter/internal/controller"
	"github.com/joluc/specter/internal/metrics"
	"github.com/joluc/specter/internal/shortener"
	"github.com/joluc/specter/internal/template"
	// +kubebuilder:scaffold:imports
)

// =============================================================================
// BUILD INFORMATION
// =============================================================================
// These variables are set at build time using ldflags.
// Example: go build -ldflags "-X main.version=v1.0.0 -X main.commit=abc123"
//
// LEARNING NOTE: This is a common Go pattern for embedding build metadata.
// The linker (-ldflags) can set variables at compile time, which is useful
// for version strings, commit hashes, and build dates.
// =============================================================================

var (
	// version is the semantic version of Specter.
	version = "dev"

	// commit is the git commit hash.
	commit = "unknown"

	// buildDate is when the binary was built.
	buildDate = "unknown"
)

// =============================================================================
// SCHEME REGISTRATION
// =============================================================================
// The scheme tells controller-runtime how to convert between Go types and
// Kubernetes API types (YAML/JSON). Every CRD we use must be registered.
// =============================================================================

var (
	// scheme is the runtime scheme containing all registered types.
	scheme = runtime.NewScheme()
)

func init() {
	// Register standard Kubernetes types (Pod, Service, ConfigMap, etc.)
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	// Register our custom types (SpecterConfig, ClusterSpecterConfig)
	utilruntime.Must(configv1alpha1.AddToScheme(scheme))

	// Register prometheus-operator types (PrometheusRule)
	// This is required because we watch and modify PrometheusRules.
	utilruntime.Must(monitoringv1.AddToScheme(scheme))

	// +kubebuilder:scaffold:scheme
}

// =============================================================================
// MAIN FUNCTION
// =============================================================================

func main() {
	// =========================================================================
	// COMMAND-LINE FLAGS
	// =========================================================================
	// These flags configure the operator's behavior. They can be set via
	// command line or environment variables in the Deployment manifest.

	var (
		metricsAddr          string
		metricsCertPath      string
		metricsCertName      string
		metricsCertKey       string
		webhookCertPath      string
		webhookCertName      string
		webhookCertKey       string
		enableLeaderElection bool
		probeAddr            string
		secureMetrics        bool
		enableHTTP2          bool
		logLevel             string
		logFormat            string
		shortenerBaseURL     string
		shortenerTTL         time.Duration
	)

	flag.StringVar(&metricsAddr, "metrics-bind-address", "0",
		"The address the metrics endpoint binds to. Use :8443 for HTTPS or :8080 for HTTP, or 0 to disable.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081",
		"The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this ensures only one active controller manager in HA setups.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served via HTTPS. Use --metrics-secure=false for HTTP.")
	flag.StringVar(&webhookCertPath, "webhook-cert-path", "",
		"The directory containing the webhook certificate.")
	flag.StringVar(&webhookCertName, "webhook-cert-name", "tls.crt",
		"The name of the webhook certificate file.")
	flag.StringVar(&webhookCertKey, "webhook-cert-key", "tls.key",
		"The name of the webhook key file.")
	flag.StringVar(&metricsCertPath, "metrics-cert-path", "",
		"The directory containing the metrics server certificate.")
	flag.StringVar(&metricsCertName, "metrics-cert-name", "tls.crt",
		"The name of the metrics server certificate file.")
	flag.StringVar(&metricsCertKey, "metrics-cert-key", "tls.key",
		"The name of the metrics server key file.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers.")
	flag.StringVar(&logLevel, "log-level", "info",
		"Log level: debug, info, warn, error.")
	flag.StringVar(&logFormat, "log-format", "json",
		"Log format: json or text.")
	flag.StringVar(&shortenerBaseURL, "shortener-base-url", "",
		"Base URL for the URL shortener service. If empty, shortening is disabled globally.")
	flag.DurationVar(&shortenerTTL, "shortener-ttl", 7*24*time.Hour,
		"How long shortened URLs remain valid.")

	flag.Parse()

	// =========================================================================
	// LOGGING SETUP
	// =========================================================================
	// We use Go 1.21+ slog for structured logging.
	//
	// LEARNING NOTE: Structured logging (JSON format) is essential in production
	// because it allows log aggregation tools (Elasticsearch, Loki, CloudWatch)
	// to parse and query logs efficiently.

	logger := setupLogger(logLevel, logFormat)
	slog.SetDefault(logger)

	// Set controller-runtime logger to use our slog logger.
	ctrl.SetLogger(logr.FromSlogHandler(logger.Handler()))
	log.SetLogger(logr.FromSlogHandler(logger.Handler()))

	// Log startup information.
	logger.Info("Starting Specter operator",
		slog.String("version", version),
		slog.String("commit", commit),
		slog.String("build_date", buildDate),
	)

	// Set build info metric.
	metrics.SetBuildInfo(version, commit, buildDate, goruntime.Version())

	// =========================================================================
	// TLS CONFIGURATION
	// =========================================================================
	// HTTP/2 is disabled by default due to vulnerabilities (CVE-2023-44487).

	var tlsOpts []func(*tls.Config)
	if !enableHTTP2 {
		logger.Info("HTTP/2 disabled for security")
		tlsOpts = append(tlsOpts, func(c *tls.Config) {
			c.NextProtos = []string{"http/1.1"}
		})
	}

	// =========================================================================
	// WEBHOOK SERVER
	// =========================================================================
	webhookTLSOpts := tlsOpts
	webhookServerOptions := webhook.Options{
		TLSOpts: webhookTLSOpts,
	}
	if len(webhookCertPath) > 0 {
		logger.Info("Using custom webhook certificates",
			slog.String("cert_path", webhookCertPath),
		)
		webhookServerOptions.CertDir = webhookCertPath
		webhookServerOptions.CertName = webhookCertName
		webhookServerOptions.KeyName = webhookCertKey
	}
	webhookServer := webhook.NewServer(webhookServerOptions)

	// =========================================================================
	// METRICS SERVER
	// =========================================================================
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}
	if secureMetrics {
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}
	if len(metricsCertPath) > 0 {
		logger.Info("Using custom metrics certificates",
			slog.String("cert_path", metricsCertPath),
		)
		metricsServerOptions.CertDir = metricsCertPath
		metricsServerOptions.CertName = metricsCertName
		metricsServerOptions.KeyName = metricsCertKey
	}

	// =========================================================================
	// MANAGER CREATION
	// =========================================================================
	// The Manager is the main entry point for controller-runtime.
	// It coordinates controllers, caches, leader election, and metrics.
	//
	// LEARNING NOTE: The Manager handles many complex tasks:
	//   - Shared cache: All controllers share one informer cache (efficient)
	//   - Leader election: Only one replica processes events (HA)
	//   - Graceful shutdown: Clean termination on SIGTERM
	//   - Webhook serving: If you define admission webhooks
	//   - Metrics serving: Prometheus metrics endpoint

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "specter.joluc.de",
		// LeaderElectionReleaseOnCancel speeds up leader transitions when
		// the current leader is terminated gracefully.
	})
	if err != nil {
		logger.Error("Failed to create manager", slog.String("error", err.Error()))
		os.Exit(1)
	}

	// =========================================================================
	// URL SHORTENER (OPTIONAL)
	// =========================================================================
	// The shortener is only created if a base URL is configured.

	var urlShortener *shortener.Store
	if shortenerBaseURL != "" {
		logger.Info("URL shortener enabled",
			slog.String("base_url", shortenerBaseURL),
			slog.Duration("ttl", shortenerTTL),
		)
		urlShortener = shortener.NewStore(shortener.Config{
			BaseURL:    shortenerBaseURL,
			DefaultTTL: shortenerTTL,
			Logger:     logger.With(slog.String("component", "shortener")),
		})
		defer urlShortener.Stop()

		// Add the shortener HTTP handler to serve redirects.
		// This adds a /s/ endpoint to the metrics server.
		shortenerHandler := shortener.NewHandler(urlShortener, logger.With(slog.String("component", "shortener-handler")))
		if err := mgr.AddMetricsServerExtraHandler("/s/", shortenerHandler); err != nil {
			logger.Error("Failed to add shortener handler", slog.String("error", err.Error()))
			os.Exit(1)
		}
	}

	// =========================================================================
	// TEMPLATE ENGINE
	// =========================================================================
	templateEngine := template.NewEngine(logger.With(slog.String("component", "template")))

	// =========================================================================
	// CONTROLLER REGISTRATION
	// =========================================================================
	// Create and register our controllers with the manager.

	// SpecterConfig controller - validates configs and updates status.
	if err := (&controller.SpecterConfigReconciler{
		Client:         mgr.GetClient(),
		Scheme:         mgr.GetScheme(),
		Logger:         logger.With(slog.String("controller", "SpecterConfig")),
		TemplateEngine: templateEngine,
	}).SetupWithManager(mgr); err != nil {
		logger.Error("Failed to create SpecterConfig controller", slog.String("error", err.Error()))
		os.Exit(1)
	}

	// PrometheusRule controller - the main controller that injects annotations.
	if err := (&controller.PrometheusRuleReconciler{
		Client:         mgr.GetClient(),
		Scheme:         mgr.GetScheme(),
		Logger:         logger.With(slog.String("controller", "PrometheusRule")),
		TemplateEngine: templateEngine,
		URLShortener:   urlShortener,
	}).SetupWithManager(mgr); err != nil {
		logger.Error("Failed to create PrometheusRule controller", slog.String("error", err.Error()))
		os.Exit(1)
	}

	// +kubebuilder:scaffold:builder

	// =========================================================================
	// HEALTH CHECKS
	// =========================================================================
	// Kubernetes uses these endpoints to determine if the pod is healthy.
	//
	// LEARNING NOTE:
	//   - /healthz (liveness): Is the process alive? If not, restart it.
	//   - /readyz (readiness): Can it serve traffic? If not, don't send requests.

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		logger.Error("Failed to set up health check", slog.String("error", err.Error()))
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		logger.Error("Failed to set up ready check", slog.String("error", err.Error()))
		os.Exit(1)
	}

	// =========================================================================
	// START MANAGER
	// =========================================================================
	// This blocks until the manager receives a shutdown signal (SIGTERM).
	// The manager handles graceful shutdown of all controllers and servers.

	logger.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Error("Manager exited with error", slog.String("error", err.Error()))
		os.Exit(1)
	}

	logger.Info("Manager stopped gracefully")
}

// setupLogger creates an slog.Logger based on the configuration.
//
// LEARNING NOTE: slog (structured logging) was added in Go 1.21. It provides:
//   - Structured key-value logging (not just strings)
//   - Multiple output formats (JSON for machines, text for humans)
//   - Efficient (minimal allocations)
//   - Integrated with the standard library
func setupLogger(level, format string) *slog.Logger {
	// Parse log level.
	var logLevel slog.Level
	switch level {
	case "debug":
		logLevel = slog.LevelDebug
	case "info":
		logLevel = slog.LevelInfo
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}

	// Create handler based on format.
	opts := &slog.HandlerOptions{
		Level: logLevel,
		// AddSource adds file:line to log entries (useful for debugging).
		AddSource: logLevel == slog.LevelDebug,
	}

	var handler slog.Handler
	switch format {
	case "text":
		handler = slog.NewTextHandler(os.Stdout, opts)
	default:
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}

	return slog.New(handler)
}
