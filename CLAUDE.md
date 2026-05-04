# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with this repository.

**All comprehensive documentation has been consolidated into [AGENTS.md](./AGENTS.md).**

Please refer to `AGENTS.md` for:
- Architecture overview and key components
- Essential commands (build, test, deploy)
- Critical implementation details (leader election, rate limiting, retry logic, auth modes)
- Reconciliation flow (12-step detailed breakdown)
- CSV reports generated
- Packaging & upload pipeline
- Testing gotchas and common pitfalls
- Architecture diagrams (see `docs/architecture-diagram.md`)

## Quick Reference

```bash
# Build
make build

# Test
make test

# Lint
make lint

# Run locally
make run ENABLE_WEBHOOKS=false

# Deploy to cluster
make deploy IMG=quay.io/username/koku-metrics-operator:v0.0.1
```

For detailed instructions, see [AGENTS.md](./AGENTS.md).
