# Reconciliation

Every reconcile is triggered by a change to `CostManagementMetricsConfig` or by a requeue
(every 5 minutes on success, exponential back-off on error).

## Flow

1. Fetch CR → apply defaults → reflect spec into status
2. Configure PVC storage if running in-cluster
3. Resolve cluster ID from `ClusterVersion` resource
4. Detect operator version changes (git commit hash comparison)
5. Calculate Prometheus query time range (hourly chunks, up to 96 hours back)
6. Collect loop: For each hour, run PromQL queries → write CSV files to `reports/` directory
7. Package: Compress CSVs → TAR.GZ with manifest
8. Auth: Extract credentials from pull-secret (token) or user-provided secret (basic/SA)
9. Source check: Verify/create integration on cloud.redhat.com (every 1440 min by default)
10. Upload: POST packaged files to console.redhat.com Ingress API (every 360 min by default)
11. Trim old packages → update CR status → requeue

## Time Range Calculation

`getTimeRange()` in `internal/controller/prometheus.go`

Determines the `[start, end)` window of hours to collect:

| Scenario | Behavior |
|----------|----------|
| **Initial collection** (`collectPreviousData: true`) | Start pushed back by Prometheus retention period (from ConfigMap, default 14d, capped 90d) |
| **Gap recovery** | If `LastQuerySuccessTime` > 1 hour behind, resumes from where it left off |
| **Normal operation** | Start = previous full hour, end = start + 59m59s |

## Upgrade Detection

`setOperatorCommit()` in `internal/controller/costmanagementmetricsconfig_controller.go`

Compares embedded git commit hash with `cr.Status.OperatorCommit`. If they differ:
- Existing unpackaged report files are packaged immediately
- Collection start time is truncated to beginning of today

## Retry Behavior

On reconcile error, controller-runtime requeues with exponential back-off:
- Initial delay: 5 seconds
- Max delay: 5 minutes

See [implementation.md](implementation.md) for Prometheus query retry details.
