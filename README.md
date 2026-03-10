# Specter

A Kubernetes operator that automatically injects diagnostic URLs into PrometheusRule alerts. When alerts fire, on-call engineers get direct links to logs, traces, metrics, and runbooks.

## The Problem

When a Prometheus alert fires, engineers often need to:
1. Open OpenSearch and manually construct a query
2. Navigate to Grafana and find the right dashboard
3. Search for traces in Jaeger
4. Look up the runbook in your wiki

This context-switching wastes precious minutes during incidents.

## The Solution

Specter automatically generates and injects these URLs into your alerts. When `BillingServiceHighErrorRate` fires, your notification includes direct links to logs, traces, metrics, and runbooks.

## Quick Start

### 1. Install with Helm

```bash
helm install specter oci://registry-1.docker.io/joluc/specter --version 0.1.0
```

### 2. Create a ClusterSpecterConfig

```yaml
apiVersion: config.joluc.de/v1alpha1
kind: ClusterSpecterConfig
metadata:
  name: global
spec:
  templates:
    logs:
      url: "https://opensearch.mycompany.io/logs?service={{.service}}"
    traces:
      url: "https://jaeger.mycompany.io/search?service={{.service}}"
    metrics:
      url: "https://grafana.mycompany.io/d/overview?var-service={{.service}}"
```

### 3. Enable Specter on Your PrometheusRules

```yaml
apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: my-alerts
  labels:
    specter.joluc.de/enabled: "true"
spec:
  groups:
    - name: example
      rules:
        - alert: HighErrorRate
          expr: error_rate > 0.05
          labels:
            service: my-service
            severity: critical
```

### 4. Update Alertmanager Templates

```yaml
text: |
  Alert: {{ .Annotations.summary }}
  Logs: {{ .Annotations.specter_joluc_de_logs }}
  Traces: {{ .Annotations.specter_joluc_de_traces }}
  Metrics: {{ .Annotations.specter_joluc_de_metrics }}
```

## Features

### Multi-tenancy

- **ClusterSpecterConfig**: Organization-wide defaults
- **SpecterConfig**: Namespace-specific overrides

### Template Functions

Templates use Go text/template syntax with custom functions:

| Function | Description |
|----------|-------------|
| `urlEncode` | URL-encode query parameters |
| `urlPathEncode` | URL-encode path segments |
| `default "fallback"` | Provide fallback for missing labels |
| `lower`, `upper`, `title` | Case transformation |
| `replace "old" "new"` | String replacement |
| `trimPrefix`, `trimSuffix` | Trim strings |
| `kueryEscape` | Escape for Kibana Query Language |
| `coalesce .a .b "default"` | First non-empty value |
| `now`, `nowMillis`, `nowRFC3339` | Time functions |
| `addDuration "-15m"` | Time arithmetic |

### Severity-based Templates

```yaml
templates:
  apm:
    url: "https://apm.io/services/{{.service}}"
    severity:
      - critical
```

### Required Labels

```yaml
templates:
  trace-by-id:
    url: "https://jaeger.io/trace/{{.traceId}}"
    requiredLabels:
      - traceId
```

### URL Shortening

```yaml
spec:
  shortener:
    enabled: true
    maxURLLength: 200  # Only shorten URLs longer than this
```

Configure the shortener base URL via Helm values:

```yaml
shortener:
  enabled: true
  baseURL: "https://specter.mycompany.io"

ingress:
  enabled: true
  hosts:
    - host: specter.mycompany.io
      paths:
        - path: /s
          pathType: Prefix
```

### Skip Individual Alerts

```yaml
- alert: DebugAlert
  labels:
    specter.joluc.de/skip: "true"
```

## Configuration

See [config/samples/](config/samples/) for examples.

## Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `specter_reconcile_total` | Counter | Total reconciliations |
| `specter_reconcile_duration_seconds` | Histogram | Reconciliation latency |
| `specter_template_render_total` | Counter | Template render attempts |
| `specter_links_generated_total` | Counter | Diagnostic links generated |
| `specter_rules_watched` | Gauge | PrometheusRules being watched |

## Development

### Prerequisites

- Go 1.26+
- Docker
- kubectl
- Access to a Kubernetes cluster

### Commands

```bash
make install    # Install CRDs
make run        # Run controller locally
make test       # Run tests
make build      # Build binary
make docker-build IMG=specter:dev
```

## License

Apache License 2.0
