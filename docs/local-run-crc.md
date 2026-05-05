# Running the Operator Locally with CRC

## Prerequisites

- [CRC (CodeReady Containers)](https://crc.dev/docs/installing/) installed
- `oc` CLI available (bundled with CRC at `~/.crc/bin/oc/oc`)
- Go toolchain installed
- This repository cloned

## Setup

### 1. Configure CRC with Monitoring

The operator requires the OpenShift monitoring stack (Prometheus/Thanos). CRC needs sufficient memory for the monitoring stack (18 GB recommended):

```bash
crc config set enable-cluster-monitoring true
crc config set memory 18000
crc stop
crc start
```

Verify the monitoring stack is running:

```bash
oc get pods -n openshift-monitoring | grep thanos
```

Expected output:
```
thanos-querier-xxxxxxxxx-xxxxx   6/6   Running   0   Xm
```

### 2. Login to CRC

```bash
crc console --credentials
# Use the kubeadmin credentials:
oc login -u kubeadmin -p <password> https://api.crc.testing:6443
```

### 3. Add `oc` to PATH

```bash
eval $(crc oc-env)
```

Or add `~/.crc/bin/oc` to your shell profile permanently.

### 4. Create Namespace

```bash
oc new-project koku-metrics-operator
```

### 5. Build the Operator

```bash
go build -o bin/manager cmd/main.go
```

> **Note**: Avoid `make build` as it runs `go get -u ./...` which upgrades dependencies and corrupts the vendor directory.

### 6. Install CRDs

```bash
make install
```

### 7. Deploy ServiceAccount

```bash
oc apply -f testing/sa.yaml
```

### 8. Get Token and CA Certificate

```bash
make get-token-and-cert
```

This generates `testing/token` and `testing/service-ca.crt`.

### 9. Run the Operator

```bash
WATCH_NAMESPACE=koku-metrics-operator SECRET_ABSPATH=./testing go run cmd/main.go
```

Two environment variables are required:

| Variable | Purpose |
|----------|---------|
| `WATCH_NAMESPACE` | Namespace the operator watches (required by `main.go`) |
| `SECRET_ABSPATH` | Path to `token` and `service-ca.crt` files; triggers local dev mode |

### 10. Deploy a CR

> **Important**: The monitoring stack must be running before this step. The `make deploy-local-cr` target auto-detects the Thanos route. If the route doesn't exist, the CR will be created with an empty `service_address` and you'll need to re-create it (see step 11).

In a separate terminal:

```bash
make deploy-local-cr
```

This creates a `CostManagementMetricsConfig` CR with:
- Token authentication (default)
- TLS verification disabled for Prometheus
- External Prometheus route auto-detected via `oc get routes thanos-querier`

### 11. Configure Prometheus Route

The CR is created with an empty `service_address`. Update it with the Thanos route:

```bash
THANOS_ROUTE=$(oc get route thanos-querier -n openshift-monitoring -o jsonpath='https://{.spec.host}')
oc patch costmanagementmetricsconfig costmanagementmetricscfg-sample -n koku-metrics-operator \
  --type merge -p "{\"spec\":{\"prometheus_config\":{\"service_address\":\"$THANOS_ROUTE\"}}}"
```

> **Note**: The Thanos route requires OAuth authentication. The operator uses the service account token from `testing/token` to authenticate. If you see `403 Forbidden`, the token may not have sufficient permissions. Grant the `cluster-monitoring-view` role:
>
> ```bash
> oc adm policy add-cluster-role-to-user cluster-monitoring-view -z koku-metrics-controller-manager -n koku-metrics-operator
> ```

### 12. Verify Collected Metrics

After reconciliation, metrics are stored locally in the `tmp/` directory:

```
tmp/koku-metrics-operator-reports/
├── data/          # Raw per-hour CSV files (appended to)
│   ├── cm-openshift-node-usage-202605.csv
│   ├── cm-openshift-pod-usage-202605.csv
│   ├── cm-openshift-storage-usage-202605.csv
│   ├── cm-openshift-vm-usage-202605.csv
│   ├── cm-openshift-namespace-usage-202605.csv
│   └── cm-openshift-nvidia-gpu-usage-202605.csv
├── staging/       # Files staged for packaging (UUID-prefixed)
│   ├── manifest.json
│   └── *.csv
└── upload/        # Packaged tar.gz archives ready for upload
    └── 20260505T130831_657827-cost-mgmt.tar.gz
```

Inspect the data:
```bash
# View collected CSV files
head tmp/koku-metrics-operator-reports/data/cm-openshift-node-usage-*.csv

# List packaged archives
ls -la tmp/koku-metrics-operator-reports/upload/

# Inspect package contents
tar tzf tmp/koku-metrics-operator-reports/upload/*.tar.gz
```

## Troubleshooting

### Port Already in Use

```bash
lsof -ti:8081 | xargs kill -9
lsof -ti:8080 | xargs kill -9
```

### Prometheus Connection Failed — Empty Host

If you see `http: no Host in request URL`, the Prometheus service address is empty. This happens when:
- The monitoring stack is not enabled in CRC
- No thanos-querier route exists

Fix: `crc config set enable-cluster-monitoring true && crc config set memory 18000 && crc stop && crc start`

### Prometheus Connection Failed — 403 Forbidden

If you see `client_error: client error: 403`, the service account token lacks permissions to query Thanos.

Fix:
```bash
oc adm policy add-cluster-role-to-user cluster-monitoring-view -z koku-metrics-controller-manager -n koku-metrics-operator
```

If the role doesn't exist, use `view` instead:
```bash
oc adm policy add-role-to-user view -z koku-metrics-controller-manager -n koku-metrics-operator
```

### Token Not Found

If you see `open /var/run/secrets/kubernetes.io/serviceaccount/token: no such file or directory`, the `SECRET_ABSPATH` env var is not set or the `testing/token` file doesn't exist. Re-run `make get-token-and-cert`.

### Vendor Directory Corruption

If `make build` was run and dependencies were upgraded:

```bash
git checkout -- go.mod go.sum vendor/
git clean -fd vendor/
```

## Key Resources

| Resource | Command |
|----------|---------|
| CRD | `oc get crd \| grep costmanagement` |
| CR instances | `oc get costmanagementmetricsconfigs -n koku-metrics-operator` |
| CR details | `oc describe costmanagementmetricsconfig <name> -n koku-metrics-operator` |
| CR status | `oc get costmanagementmetricsconfig <name> -n koku-metrics-operator -o jsonpath='{.status}'` |
| Prometheus status | `oc get costmanagementmetricsconfig <name> -n koku-metrics-operator -o jsonpath='{.status.prometheus}'` |
| ServiceAccount | `oc get sa koku-metrics-controller-manager -n koku-metrics-operator` |
| Monitoring pods | `oc get pods -n openshift-monitoring` |
| Thanos route | `oc get routes -n openshift-monitoring \| grep thanos` |
