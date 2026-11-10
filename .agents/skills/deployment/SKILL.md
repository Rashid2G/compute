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
- Go/config/scripts changes: `make deploy` (or `make deploy-lab`)
- Web UI changes: `make deploy-ui` (or `make deploy-lab-ui`)
- Restart only: `make deploy-restart`
- Status: `make remote-status` (or `make remote-status-lab`)
- Logs: `make remote-logs`

## SSH
`ssh compute-lab` (BatchMode, non-interactive key). Never print secrets/keys/tokens.

## Rules
- Deploy only when explicitly requested or required by task.
- Verify service health after deploy (`make remote-status`).
- `make deploy` restarts live services — affects shared infra; confirm before running.
- For web changes always use `make deploy-ui` (builds embedded assets).

## Local dev alternative
- `make build && make dev` runs daemon on 127.0.0.1:18080 locally (requires setup-host).
- `make setup-host` configures host bridge/iptables (sudo).

## Full integration tests
- `make test-features` (running daemon + host).
- Lab suite: `ssh compute-lab "cd ~/on-demand-vms && sudo ./scripts/test-all-features.sh ./bin/compute 127.0.0.1:18080"`.
