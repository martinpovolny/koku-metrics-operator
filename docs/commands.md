# Commands

## Local Development Setup

```bash
# Prevent config changes from being committed
git update-index --assume-unchanged config/manager/kustomization.yaml

# Install pre-commit hooks
pre-commit install
```

## Build & Run

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

## Deploy to Cluster

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

## Testing

```bash
# Run all tests
make test

# Run envtest setup (required for unit tests)
# Install Kubernetes server binaries per envtest docs
```

## Code Quality

```bash
make lint    # Run pre-commit hooks (golangci-lint)
make fmt     # go fmt
make vet     # go vet
```

## Code Generation

```bash
make generate    # Regenerate DeepCopy methods
make manifests   # Regenerate CRD manifests
```

## Container & OLM

```bash
make docker-build IMG=<image>     # Build container image
make docker-buildx IMG=<image>    # Multi-platform build (amd64/arm64/s390x/ppc64le)
make bundle                       # Generate OLM bundle manifests
make downstream                   # Generate downstream release variant
```
