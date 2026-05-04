# Pitfalls

## Don't Commit kustomization Changes

```bash
git update-index --assume-unchanged config/manager/kustomization.yaml
```

## Webhooks Require cert-manager

If enabling webhooks, ensure cert-manager is installed or `make deploy` will fail.

## Upload Cycle Minimum

Upload cycle cannot be less than 60 minutes (enforced in controller).

## Initial Data Collection

First reconcile after CR creation collects previous data based on Prometheus retention period. Subsequent reconciles only collect current hour.

## Tooling in `bin/`

Code generation tools (controller-gen, kustomize, setup-envtest, yq, operator-sdk) are auto-downloaded into `bin/` by Makefile targets — **do not commit them**.

## Linting

- Uses golangci-lint v2.8.0 via pre-commit
- Install hooks: `pre-commit install`
- Enabled linters: errcheck, govet, ineffassign, staticcheck, unused
