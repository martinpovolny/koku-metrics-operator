# koku-metrics-operator - Agent Guide

## Architecture Overview

**Purpose**: OpenShift Operator that collects cluster usage metrics via Prometheus and uploads to Red Hat Koku.

**Key Components**:
- `cmd/main.go` - Entry point, leader election config, manager setup
- `internal/controller/costmanagementmetricsconfig_controller.go` - Main reconciliation logic
- `internal/controller/prometheus.go` - Time range calculation, retry tracking
- `internal/collector/prometheus.go` - Prometheus queries, exponential backoff retries
- `internal/crhchttp/*.go` - Koku API HTTP client (upload, auth)
- `api/v1beta1/types.go` - MetricsConfig CRD definition

---

## Essential Commands

### Local Development Setup
```bash
# Prevent config changes from being committed
git update-index --assume-unchanged config/manager/kustomization.yaml

# Install pre-commit hooks
pre-commit install
```

### Build & Run
```bash
# Build manager binary
make build

# Run locally (outside cluster)
make run ENABLE_WEBHOOKS=false SECRET_ABSPATH=/path/to/secrets

# Full local dev flow
make build && make install && oc apply -f testing/sa.yaml && \
  make get-token-and-cert && \
  make run ENABLE_WEBHOOKS=false SECRET_ABSPATH=/path/to/secrets
```

### Deploy to Cluster
```bash
# Register CRD
make install

# Build and push image
make docker-build IMG=quay.io/username/koku-metrics-operator:v0.0.1
make docker-push IMG=quay.io/username/koku-metrics-operator:v0.0.1

# Deploy
make deploy IMG=quay.io/username/koku-metrics-operator:v0.0.1

# Create CR with token auth (default)
make deploy-cr

# Create CR with service-account auth
make deploy-cr CLIENT_ID=$ID CLIENT_SECRET=$SECRET AUTH=service-account
```

### Testing
```bash
# Run all tests
make test

# Run envtest setup (required for unit tests)
# Install Kubernetes server binaries per envtest docs
```

---

## Critical Implementation Details

### Leader Election
- **Disabled by default** (`leader-elect: false`) - single instance typical
- Enable via `--leader-elect=true` flag if deploying multiple replicas
- Environment variables override defaults:
  - `LEADER_ELECTION_LEASE_DURATION` (default: 60s)
  - `LEADER_ELECTION_RENEW_DEADLINE` (default: 30s)
  - `LEADER_ELECTION_RETRY_PERIOD` (default: 5s)

### Rate Limiting
- Uses `workqueue.NewTypedItemExponentialFailureRateLimiter`
- **Initial delay**: 5 seconds after first failure
- **Max delay**: 5 minutes
- Only triggers on consecutive reconcile errors, not every run

### Prometheus Query Retry Logic (TWO LAYERS)

**Layer 1 - Collector Level** (`internal/collector/prometheus.go:212-291`):
- Retries **individual failed queries** only
- Exponential backoff: `sleep = max(2^(5-retries), 1)` seconds
- Starts at 1s, then 2s, 4s, 8s, 16s (max 5 retries per query)
- Skips successful queries on retry

**Layer 2 - Controller Level** (`internal/controller/prometheus.go:196-200`):
- Tracks failures **per hour** in `retryTracker map[time.Time]int`
- Retries entire hour if GenerateReports() returns error (except ErrNoData/ErrROS)
- Max 5 retries per hour before giving up
- On success: clears entire tracker (`retryTracker = make(map[...])`)

**Non-Retryable Errors**:
- `collector.ErrNoData` - No metrics for that hour (expected)
- `collector.ErrROSNoEnabledNamespaces` - Resource optimization not enabled

### Authentication Modes
1. **Token** (default): Bearer token from `openshift-config/pull-secret`
2. **Basic**: Username/password from Kubernetes Secret (deprecated after 2024-12-31)
3. **ServiceAccount**: Client ID/secret from ServiceAccount secret

### Storage Modes
- **In-cluster** (`r.InCluster=true`): Uses PVC for persistent storage
- **Outside cluster**: No PVC needed, runs as standalone Go program

---

## Testing Gotchas

### Envtest Prerequisites
Unit tests use `envtest` which requires:
- Kubernetes server binaries installed locally
- Follow envtest installation docs before running `make test`

### Local CR Creation
`make deploy-local-cr` creates CR with:
- External Prometheus route enabled by default
- TLS verification disabled for Prometheus
- Token authentication for Koku (unless overridden)

---

## File Ownership

| Directory | Purpose |
|-----------|---------|
| `cmd/` | Entry points, flags, manager setup |
| `internal/controller/` | Reconciliation logic, controllers |
| `internal/collector/` | Prometheus query execution |
| `internal/crhchttp/` | Koku API HTTP client |
| `internal/clusterversion/` | ClusterVersion CR handling |
| `api/v1beta1/` | CRD types and validation |
| `config/` | Kubernetes manifests (manager, rbac, crd) |
| `bundle/` | OLM bundle for Operator Lifecycle Manager |

---

## Common Pitfalls

### Don't Commit kustomization Changes
```bash
git update-index --assume-unchanged config/manager/kustomization.yaml
```

### Webhooks Require cert-manager
If enabling webhooks, ensure cert-manager is installed or `make deploy` will fail.

### Upload Cycle Minimum
Upload cycle cannot be less than 60 minutes (enforced in controller).

### Initial Data Collection
First reconcile after CR creation collects previous data based on Prometheus retention period. Subsequent reconciles only collect current hour.
