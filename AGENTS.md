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

---

## Reconciliation Flow

Each reconcile cycle (requeues every 5 min on success, exponential back-off on error):

1. **Fetch CR** → apply defaults → reflect spec into status
2. **Configure PVC storage** if running in-cluster
3. **Resolve cluster ID** from `ClusterVersion` resource
4. **Detect operator version changes** (git commit hash comparison)
5. **Calculate Prometheus query time range** (hourly chunks, up to 96 hours back)
6. **Collect loop**: For each hour, run PromQL queries → write CSV files to `reports/` directory
7. **Package**: After 96 hours of collection, compress CSVs → TAR.GZ with manifest
8. **Auth**: Extract credentials from pull-secret (token) or user-provided secret (basic/SA)
9. **Source check**: Verify/create integration on cloud.redhat.com (every 1440 min by default)
10. **Upload**: POST packaged files to console.redhat.com Ingress API (`/api/ingress/v1/upload`) (every 360 min by default)
11. **Trim old packages** and write results to CR status → requeue

### Time Range Calculation (`getTimeRange()`)

Determines the `[start, end)` window of hours to collect:

- **Initial collection** (`spec.prometheusConfig.collectPreviousData: true`): On first reconcile (no `LastQuerySuccessTime`), start is pushed back by Prometheus retention period (read from `openshift-monitoring/cluster-monitoring-config` ConfigMap, defaults to 14 days, capped at 90 days)
- **Gap recovery**: If `LastQuerySuccessTime` is more than one hour behind current hour, resumes from where it left off (up to retention limit)
- **Normal operation**: Start = previous full hour, end = start + 59m59s

### CSV Reports Generated

For each hour, the operator queries thanos-querier and writes CSV files:

| Report File | Metrics |
|-------------|---------|
| `cm-openshift-node-usage-*` | Node CPU/memory capacity and usage |
| `cm-openshift-pod-usage-*` | Pod requests, limits, and usage |
| `cm-openshift-namespace-usage-*` | Namespace-level aggregation |
| `cm-openshift-storage-usage-*` | PVC storage capacity and usage |
| `cm-openshift-vm-usage-*` | Virtual machine metrics (kubevirt) |
| `cm-openshift-nvidia-gpu-usage-*` | NVIDIA GPU metrics (DCGM) |
| `ros-openshift-container-*` | Resource optimization (container-level) |
| `ros-openshift-namespace-*` | Resource optimization (namespace-level) |

ROS data collection is opt-in: namespaces must carry the label `insights_cost_management_optimizations=true` or `cost_management_optimizations=true`.

### Packaging & Upload

- **Packaging**: Compresses CSVs from `reports/` into `upload/*.tar.gz` with `manifest.json` (UUID, cluster ID, timestamp, file list)
- **End-of-day handling**: At hour 23, files are **moved** out of `reports/` (no more appending); otherwise they are **copied** to allow continued appending
- **Upload cycle**: Runs every `UploadCycle` minutes (default 360 min, minimum 60 min) or at end-of-day
- **Upload gating**: Files accumulate in `upload/` until a valid source/integration exists on console.redhat.com
- **HTTP 202**: Local tar.gz is deleted and `LastSuccessfulUploadTime` is recorded
- **HTTP 401**: Credentials are re-validated immediately

---

## Architecture Diagrams

See [docs/architecture-diagram.md](docs/architecture-diagram.md) for detailed Mermaid diagrams showing:
- External components architecture
- Prometheus/Thanos connection flow
- Authentication flow
- Reporting cycle
- Data flow with external APIs
