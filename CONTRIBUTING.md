# Contributing to Compute

Thank you for your interest in contributing to Compute! This guide covers repository layout, development prerequisites, local build and test workflows, and pull request standards.

## Prerequisites

- **Go**: 1.24 or newer
- **Node.js**: 20 or newer (with `npm`, or `bun`) for the Web Dashboard
- **Linux with `/dev/kvm`** (optional for unit tests, required for booting live Firecracker microVMs and running `make test-features`)
- **Docker Engine 24+ & Compose v2.20+** (optional, for validating and running the containerized stack)

## Repository Structure

| Path | Purpose |
| --- | --- |
| `cmd/computed` | Control-plane daemon, REST/WebSocket APIs, DNS responder, HTTP edge/preview proxy |
| `cmd/compute` | `compute` CLI and standalone GUI server command |
| `cmd/compute-worker` | Outbound-connecting worker node agent for distributed jobs |
| `cmd/compute-bench` | Automated performance and microVM lifecycle benchmark tool |
| `internal/vm`, `internal/firecracker` | MicroVM lifecycle, Firecracker API client, pause/resume state snapshots, templates |
| `internal/master` | Auth, sessions, API keys, quotas, audit events, WebSocket relay |
| `internal/store`, `internal/db` | SQLite persistence and forward-compatible schema migrations |
| `internal/network`, `internal/dns` | Bridge/TAP management, per-sandbox/account iptables rules, DNS responder, ingress proxy |
| `internal/gui` | Embedded static Web UI server and compiled assets (`internal/gui/assets/`) |
| `web/` | React + TypeScript + Vite Web Dashboard source and browser tests |
| `pkg/api`, `pkg/client` | Public API contract types and Go client SDK |
| `sdk/python`, `sdk/typescript` | Python (`compute-sdk`) and TypeScript (`@compute/sdk`) client libraries |
| `scripts/`, `config/` | Host preflight/setup, image builders, systemd templates, Compose validation |
| `docs/` | Static GitHub Pages documentation site and operational guides |

## Building from Source

1. Build the Web Dashboard assets (embeds into `internal/gui/assets/`):

   ```bash
   cd web && npm ci && cd ..
   make build-ui
   ```

2. Build all Go binaries into `bin/`:

   ```bash
   make build
   ```

## Running Tests & Checks

Before opening a pull request, run the full verification suite:

```bash
# Static analysis
make vet

# Go unit & API integration tests
make test

# Web Dashboard TypeScript checks & tests
make test-web

# Docker Compose & script syntax checks
make validate-compose

# Trailing whitespace / patch hygiene
git diff --check
```

If you are on a Linux host with `/dev/kvm` and a running `computed` daemon, you can also run the live microVM integration suite:

```bash
make test-features
```

## Pull Request Standards

- **Keep changes focused**: scope each PR to a single feature, bugfix, or documentation improvement.
- **Rebuild embedded UI assets**: if you modify files under `web/`, run `make build-ui` and include the updated `internal/gui/assets/` output.
- **Database migrations**: never modify previously shipped migration files under `internal/store/migrations/`; add a new numbered migration instead.
- **Security & hygiene**: never commit `.env` files, private keys, tokens, screenshots, or host-specific IP addresses.
- **Tests**: include focused unit or UI tests covering new behavior or bugfixes.
