# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
# Build
make build                        # Build manager binary to bin/manager
make manager                      # Build just the binary

# Test
make test                         # Run unit tests with coverage
make test-qemu                    # Multi-arch tests (arm64, ppc64le) via QEMU

# Code quality
make lint                         # Run pre-commit hooks (golangci-lint)
make fmt                          # go fmt
make vet                          # go vet

# Code generation (run after modifying API types)
make generate                     # Regenerate DeepCopy methods
make manifests                    # Regenerate CRD manifests

# Local development
make install                      # Install CRDs to cluster
make run ENABLE_WEBHOOKS=false    # Run operator against current kube context
make deploy-local-cr AUTH=service-account  # Deploy a test CR (also: basic, token)

# Container
make docker-build IMG=<image>     # Build container image
make docker-buildx IMG=<image>    # Multi-platform build (amd64/arm64/s390x/ppc64le)

# OLM bundling
make bundle                       # Generate OLM bundle manifests
make downstream                   # Generate downstream release variant
```

## Architecture

The operator manages a single CRD: **CostManagementMetricsConfig** (group: `costmanagement-metrics-cfg`, version: `v1beta1`). Its purpose is to collect OpenShift cluster usage metrics from Prometheus and upload them to Red Hat's cloud.redhat.com (cost management console).

### Key Packages

- **`api/v1beta1/`** — CRD types (`CostManagementMetricsConfig` spec/status) and default values
- **`internal/controller/`** — Main reconciler; orchestrates the entire collection/upload pipeline
- **`internal/collector/`** — Queries Prometheus (via thanos-querier), generates CSV reports for pod usage, storage, nodes, namespaces, NVIDIA GPU, and ROS
- **`internal/packaging/`** — Compresses reports into TAR.GZ archives with a manifest (UUID-based filenames)
- **`internal/crhchttp/`** — HTTP client for cloud.redhat.com; handles upload and API calls with token/basic/service-account auth
- **`internal/sources/`** — Creates/verifies integrations (Sources API) on cloud.redhat.com
- **`internal/dirconfig/`** — Manages filesystem layout for PVC-backed report storage
- **`internal/clusterversion/`** — Reads `ClusterVersion` resource for cluster ID and OCP version

### Reconciliation Flow

Each reconcile cycle (requeues every 5 min, or on error):

1. Fetch CR → apply defaults → reflect spec into status
2. Configure PVC storage if running in-cluster
3. Resolve cluster ID from `ClusterVersion` resource
4. Detect operator version changes (git commit hash comparison)
5. Calculate Prometheus query time range (hourly chunks, up to 96 hours back)
6. **Collect loop**: For each hour, run PromQL queries → write CSV files to `reports/` directory
7. **Package**: After 96 hours of collection, compress CSVs → TAR.GZ with manifest
8. **Auth**: Extract credentials from pull-secret (token) or user-provided secret (basic/SA)
9. **Source check**: Verify/create integration on cloud.redhat.com (every 1440 min by default)
10. **Upload**: POST packaged files to console.redhat.com Ingress API (`/api/ingress/v1/upload`) (every 360 min by default)
11. Write results to CR status → requeue

### Testing

Tests use **Ginkgo/Gomega** (BDD-style) with **envtest** (embedded Kubernetes control plane). The `internal/testutils/` package has shared helpers. Run a single test package:

```bash
go test ./internal/collector/... -v
go test ./internal/controller/... -v -run "TestSomething"
```

### Tooling Notes

- Code generation tools (controller-gen, kustomize, setup-envtest, yq, operator-sdk) are auto-downloaded into `bin/` by Makefile targets — don't commit them.
- Linting uses golangci-lint v2.8.0 via pre-commit. Install hooks: `pre-commit install`.
- The `.golangci.yaml` enables: errcheck, govet, ineffassign, staticcheck, unused.
- Local dev secrets/tokens go under `./testing/` (gitignored); see `docs/local-development.md` for full setup steps.
- Multi-arch support: amd64, arm64, s390x, ppc64le — CI tests arm64/ppc64le via QEMU.