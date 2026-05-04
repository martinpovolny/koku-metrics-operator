# Testing

## Commands

```bash
# Run all tests
make test

# Run specific package
go test ./internal/collector/... -v
go test ./internal/controller/... -v -run "TestSomething"

# Multi-arch tests (arm64, ppc64le) via QEMU
make test-qemu
```

## Envtest Prerequisites

Unit tests use `envtest` which requires Kubernetes server binaries installed locally.

Follow the [envtest documentation](https://book.kubebuilder.io/reference/envtest) for installation before running `make test`.

## Local CR Creation

`make deploy-local-cr` creates a CR with:
- External Prometheus route enabled by default
- TLS verification disabled for Prometheus
- Token authentication for Koku (unless overridden)

Override auth type:
```bash
make deploy-local-cr AUTH=service-account CLIENT_ID=$ID CLIENT_SECRET=$SECRET
make deploy-local-cr AUTH=basic
```

## Test Framework

- **Ginkgo/Gomega** — BDD-style testing
- **envtest** — Embedded Kubernetes control plane
- **`internal/testutils/`** — Shared test helpers
