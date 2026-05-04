# Reports

For each hour in the collection time range, the operator queries thanos-querier and writes CSV files to the `reports/` directory.

## CSV Files

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

## Resource Optimization (ROS)

ROS data collection is **opt-in**: namespaces must carry one of these labels:
- `insights_cost_management_optimizations=true`
- `cost_management_optimizations=true`

## Query Sources

| Category | Metrics Queried |
|----------|-----------------|
| **Cost** | `kube_*`, `container_*`, `kubevirt_*`, `DCGM_FI_*` (NVIDIA DCGM) |
| **ROS** | Container CPU/memory request, limit, usage, CPU throttle, GPU utilisation |
