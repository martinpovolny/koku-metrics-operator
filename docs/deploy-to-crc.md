# Deploying the Operator to CRC

This guide covers deploying the operator as a container inside the CRC cluster, as opposed to running it locally with `make run`.

## Prerequisites

- CRC running with monitoring enabled
- `oc` CLI configured and logged in as `kubeadmin`
- Docker installed (`docker --version`)
- `eval $(crc oc-env)` run in your shell

## Option 1: Deploy via CRC Internal Registry

### 1. Expose the Internal Registry

```bash
oc patch configs.imageregistry.operator.openshift.io/cluster \
  --type merge -p '{"spec":{"defaultRoute":true}}'
```

### 2. Get Registry Host

```bash
REGISTRY_HOST=$(oc get route default-route -n openshift-image-registry \
  -o jsonpath='{.spec.host}')
echo $REGISTRY_HOST
# default-route-openshift-image-registry.apps-crc.testing
```

### 3. Login to Registry

```bash
docker login -u kubeadmin -p $(oc whoami -t) $REGISTRY_HOST --tls-verify=false
```

### 4. Build the Image

```bash
IMG=$REGISTRY_HOST/koku-metrics-operator/koku-metrics-operator:latest \
  make docker-build
```

### 5. Push the Image

```bash
IMG=$REGISTRY_HOST/koku-metrics-operator/koku-metrics-operator:latest \
  make docker-push
```

### 6. Deploy the Operator

```bash
IMG=$REGISTRY_HOST/koku-metrics-operator/koku-metrics-operator:latest \
  make deploy
```

### 7. Deploy the CR

```bash
make deploy-local-cr
```

Then patch the Prometheus route:

```bash
THANOS_ROUTE=$(oc get route thanos-querier -n openshift-monitoring \
  -o jsonpath='https://{.spec.host}')
oc patch costmanagementmetricsconfig costmanagementmetricscfg-sample \
  -n koku-metrics-operator --type merge \
  -p "{\"spec\":{\"prometheus_config\":{\"service_address\":\"$THANOS_ROUTE\"}}}"
```

## Option 2: Deploy via Quay.io

If you have a quay.io account:

```bash
# Build and push
make docker-build IMG=quay.io/<your-username>/koku-metrics-operator:latest
make docker-push IMG=quay.io/<your-username>/koku-metrics-operator:latest

# Deploy
make deploy IMG=quay.io/<your-username>/koku-metrics-operator:latest
```

## Verify Deployment

```bash
# Check the operator pod
oc get pods -n koku-metrics-operator

# Check CR status
oc get costmanagementmetricsconfig -n koku-metrics-operator -o yaml
```

## Inspect PVC Contents

The operator stores collected metrics on a PersistentVolumeClaim. Since the operator uses a distroless image without shell utilities, use a temporary busybox pod to inspect the data:

```bash
# List files on the PVC
oc run -n koku-metrics-operator pv-inspector --image=busybox --restart=Never \
  --overrides='{"spec":{"containers":[{"name":"debug","image":"busybox","command":["sh","-c","ls -laR /data/"],"volumeMounts":[{"name":"reports","mountPath":"/data"}]}],"volumes":[{"name":"reports","persistentVolumeClaim":{"claimName":"koku-metrics-operator-data"}}]}}'

sleep 5 && oc logs pv-inspector -n koku-metrics-operator
oc delete pod pv-inspector -n koku-metrics-operator
```

Expected directory structure:
```
/data/
├── data/       # Raw per-hour CSV files
├── staging/    # Files staged for packaging
└── upload/     # Packaged tar.gz archives ready for upload
```

## Undeploy

```bash
make undeploy
oc delete costmanagementmetricsconfig costmanagementmetricscfg-sample -n koku-metrics-operator
```

## Troubleshooting

### ImagePullBackOff

If the pod can't pull the image, check the registry authentication:

```bash
oc describe pod -n koku-metrics-operator -l control-plane=controller-manager
```

For the internal registry, ensure you created an `ImagePullSecret`:

```bash
oc create secret docker-registry crc-registry-secret \
  --docker-server=$REGISTRY_HOST \
  --docker-username=kubeadmin \
  --docker-password=$(oc whoami -t) \
  -n koku-metrics-operator

oc secrets link default crc-registry-secret -n koku-metrics-operator
```

### Permission Denied on Registry

Ensure you're logged in as `kubeadmin` (not `developer`):

```bash
oc whoami
# Should output: kubeadmin
```
