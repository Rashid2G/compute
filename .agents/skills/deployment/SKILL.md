---
name: deployment
description: >
  Deploy on-demand-vms changes to the compute-lab VM (192.168.1.70): daemon on 18080,
  GUI on 8080. Use when code/config/scripts or web UI changes need to take effect on the lab.
---

# Deployment to compute-lab

## Target
- Host alias: `compute-lab` (192.168.1.70)
- Daemon API: http://127.0.0.1:18080 (on VM)
- GUI: http://127.0.0.1:8080 (on VM)
- Deployed checkout: /home/rashid/on-demand-vms

## Commands
- **Temporary PR preview sandbox (non-blocking)**: `make preview-deploy PR=<number>` (or `./scripts/temp-deploy-sandbox.sh <pr-number> [port]`)
- Go/config/scripts changes to main deployment: `make deploy` (or `make deploy-lab`)
- Web UI changes to main deployment: `make deploy-ui` (or `make deploy-lab-ui`)
- Restart only: `make deploy-restart`
- Status: `make remote-status` (or `make remote-status-lab`)
- Logs: `make remote-logs`

## Temporary Preview Deployments (Self-Served Sandboxes)
For testing changes and pull request verification, do NOT restart the main host deployment. Instead, deploy to an ephemeral microVM sandbox served directly by the project on `compute-lab`:
```bash
# Provision temp sandbox in background and get its Tailscale URL:
./scripts/temp-deploy-sandbox.sh <pr-number> [port] &
# Or make target:
make preview-deploy PR=<pr-number>
```
- **PR Number Required**: Each preview deployment **must contain the PR number** and is named `pr-<pr-number>`.
- **Runs on Main Dev Deployment**: Each preview deployment **must run on the main deployment deployed from the `dev` branch on the VM `compute-lab`** (`/home/rashid/on-demand-vms`).
- **Selective service deployment**: The script diffs the branch against `origin/dev` and deploys ONLY the changed services into the guest; unchanged services are reused from the compute-lab main deployment to keep the sandbox light:
  - Web UI / GUI server (new `compute` binary, embedded UI) — always runs in the guest as the preview front door (:8080); its `/api/*` proxy targets the compute-lab daemon (`http://172.16.0.1:18080`) unless the daemon itself changed.
  - `computed` daemon — runs in the guest (:18080) only when daemon-side code changed (`cmd/computed`, `internal/**`, `pkg/**`, `go.mod`, `config/**`); otherwise the shared compute-lab daemon is reused.
  - `compute-worker` — runs in the guest only when worker code changed (needs `COMPUTE_WORKER_TOKEN` to register against a control plane).
  - Docs/scripts/SDK-only changes — no service rebuild; the GUI front door is deployed so the preview URL always serves the working app.
- **Per-sandbox Tailscale enrollment (one URL per preview)**: The daemon mints a scoped, ephemeral, preauthorized auth key **per sandbox** and joins each guest to the tailnet as its own ephemeral, tagged device (`tag:compute-sandbox`, userspace networking — see `internal/tailscale/provision.go`). Every preview sandbox therefore has its **own** Tailscale identity and its **own unique URL** (`http://compute-<sandbox-name>.<tailnet>.ts.net:<port>` or its own `http://100.x.y.z:<port>`); URLs are never shared between sandboxes. On teardown the sandbox logs out of the tailnet and the ephemeral node expires automatically.
- **Non-blocking**: Designed to be launched in the background without holding up PR creation.
- **Teardown**: `ssh compute-lab "~/on-demand-vms/bin/compute sandbox stop pr-<pr-number> && ~/on-demand-vms/bin/compute sandbox delete pr-<pr-number>"`

## SSH
`ssh compute-lab` (BatchMode, non-interactive key). Never print secrets/keys/tokens.

## Rules
- For PRs and feature validation, use temporary preview sandboxes (`make preview-deploy`).
- Deploying to main (`make deploy`) restarts live services and affects shared infrastructure; run only when explicitly requested.
- Verify service health after main deploy (`make remote-status`).
- For web changes on main host, always use `make deploy-ui` (builds embedded assets).

## Local dev alternative
- `make build && make dev` runs daemon on 127.0.0.1:18080 locally (requires setup-host).
- `make setup-host` configures host bridge/iptables (sudo).

## Full integration tests
- `make test-features` (running daemon + host).
- Lab suite: `ssh compute-lab "cd ~/on-demand-vms && sudo ./scripts/test-all-features.sh ./bin/compute 127.0.0.1:18080"`.
