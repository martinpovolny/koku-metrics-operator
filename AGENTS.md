# koku-metrics-operator - Agent Guide

## Overview

OpenShift Operator that collects cluster usage metrics via Prometheus and uploads to Red Hat Koku.

## Documentation Index

| Document | Description |
|----------|-------------|
| [Commands](docs/commands.md) | Build, test, deploy, and local development commands |
| [Reconciliation](docs/reconciliation.md) | Detailed 11-step reconcile loop, time range calculation |
| [Implementation Details](docs/implementation.md) | Leader election, rate limiting, retry logic, auth modes, storage |
| [Reports](docs/reports.md) | CSV reports generated and their metrics |
| [Packaging & Upload](docs/packaging.md) | TAR.GZ packaging, upload cycle, HTTP response handling |
| [Testing](docs/testing.md) | Test commands, envtest prerequisites, local CR creation |
| [Pitfalls](docs/pitfalls.md) | Common gotchas and how to avoid them |
| [Architecture Diagrams](docs/architecture-diagram.md) | Mermaid diagrams of system components and flows |

## Key Components

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

## Reconciliation Summary

1. Fetch CR → apply defaults → reflect spec into status
2. Configure PVC storage (if in-cluster)
3. Resolve cluster ID from `ClusterVersion`
4. Detect operator version changes
5. Calculate Prometheus query time range
6. Collect loop: PromQL queries → CSV files
7. Package: CSVs → TAR.GZ with manifest
8. Auth: Extract credentials
9. Source check: Verify/create integration
10. Upload: POST to Ingress API
11. Trim old packages → update status → requeue

See [docs/reconciliation.md](docs/reconciliation.md) for details.
