# Implementation Details

## Leader Election

- **Disabled by default** (`leader-elect: false`) — single instance typical
- Enable via `--leader-elect=true` flag if deploying multiple replicas
- Environment variables override defaults:

| Variable | Default |
|----------|---------|
| `LEADER_ELECTION_LEASE_DURATION` | 60s |
| `LEADER_ELECTION_RENEW_DEADLINE` | 30s |
| `LEADER_ELECTION_RETRY_PERIOD` | 5s |

## Rate Limiting

- Uses `workqueue.NewTypedItemExponentialFailureRateLimiter`
- Initial delay: 5 seconds after first failure
- Max delay: 5 minutes
- Only triggers on consecutive reconcile errors, not every run

## Prometheus Query Retry Logic (Two Layers)

### Layer 1 — Collector Level

`internal/collector/prometheus.go:212-291`

- Retries **individual failed queries** only
- Exponential backoff: `sleep = max(2^(5-retries), 1)` seconds
- Starts at 1s, then 2s, 4s, 8s, 16s (max 5 retries per query)
- Skips successful queries on retry

### Layer 2 — Controller Level

`internal/controller/prometheus.go:196-200`

- Tracks failures **per hour** in `retryTracker map[time.Time]int`
- Retries entire hour if `GenerateReports()` returns error (except `ErrNoData`/`ErrROS`)
- Max 5 retries per hour before giving up
- On success: clears entire tracker

### Non-Retryable Errors

- `collector.ErrNoData` — No metrics for that hour (expected)
- `collector.ErrROSNoEnabledNamespaces` — Resource optimization not enabled

## Authentication Modes

| Mode | Credentials | Validation |
|------|-------------|------------|
| **Token** (default) | `openshift-config/pull-secret` → `cloud.openshift.com` | None needed |
| **ServiceAccount** | Secret with `client_id` + `client_secret` | Exchange for token via `tokenURL` |
| **Basic** (deprecated) | Secret with `username` + `password` | `GET /api/sources/v1.0/sources` (cached 1440 min) |

## Storage Modes

| Mode | Description |
|------|-------------|
| **In-cluster** (`r.InCluster=true`) | Uses PVC for persistent storage |
| **Outside cluster** | No PVC needed, runs as standalone Go program |
