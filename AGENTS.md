# AGENTS.md — Instructions for AI Coding Agents

This repository contains **Compute** (`on-demand-vms`), a lightweight Firecracker microVM orchestration platform featuring a REST/WebSocket daemon (`computed`), a command-line client (`compute`), and an embedded Web GUI (`compute gui`).

---

## 🚨 MANDATORY: Deploying Changes to `t3-runner`

Whenever you finish making code modifications, bug fixes, configuration adjustments, or feature additions in this repository, **you MUST deploy your changes to the `ssh t3-runner` VM** so that the running services take effect immediately.

### One-Command Deployment

Run the standard deployment command from the repository root:

```bash
make deploy
```

*(Alternatively: `./scripts/deploy-t3-runner.sh`)*

### If Web GUI Frontend Files Were Changed

If you modified any files under `web/` (React/Vite components, styles, or pages):

```bash
make deploy-ui
```

*(Alternatively: `./scripts/deploy-t3-runner.sh --ui`)*

---

## What the Deployment Process Does Automatically

1. **Builds Static Linux Binaries**: Compiles `bin/compute`, `bin/computed`, and `bin/compute-bench` for `linux/amd64` (`CGO_ENABLED=0`).
2. **Synchronizes Files**: Uses `rsync` to sync updated binaries, configs (`config/`), and helper scripts (`scripts/`) to `t3-runner:/home/rashid/on-demand-vms/`.
3. **Restarts Remote Services**: Executes `sudo systemctl restart computed.service compute-gui.service` on the `t3-runner` host.
4. **Verifies Health**:
   - Ensures `computed.service` and `compute-gui.service` are active.
   - Polls `http://127.0.0.1:18080/api/v1/health` for `{"status":"ok"}`.
   - Validates that the Web GUI on port `8080` responds with `HTTP 200 OK`.
   - Confirms CLI responsiveness via `compute list`.

---

## Helpful Remote Commands for Agents

| Task | Command |
|---|---|
| Deploy changes & restart services | `make deploy` |
| Deploy changes including Web UI | `make deploy-ui` |
| Quick restart without rebuilding | `make deploy-restart` |
| Check remote service status & health | `make remote-status` |
| Inspect remote service logs | `ssh t3-runner "sudo journalctl -u computed -u compute-gui -n 50 --no-pager"` |
| Test remote CLI directly | `ssh t3-runner "COMPUTE_DAEMON=127.0.0.1:18080 /home/rashid/on-demand-vms/bin/compute list"` |

---

## Remote Architecture Reference (`t3-runner`)

- **Host Alias**: `t3-runner` (defined in `~/.ssh/config`, user `rashid`, passwordless sudo enabled).
- **Remote Repo Path**: `/home/rashid/on-demand-vms`
- **Systemd Services**:
  - `computed.service`: Root service running `/home/rashid/on-demand-vms/bin/computed -listen 0.0.0.0:18080`
  - `compute-gui.service`: User service running `/home/rashid/on-demand-vms/bin/compute gui --addr 0.0.0.0 --port 8080 --daemon http://127.0.0.1:18080`
- **State Directories on `t3-runner`**:
  - `/var/lib/compute/data/compute.db`: SQLite database
  - `/var/lib/compute/images`: Rootfs images (e.g. `ubuntu-24.04.ext4`)
  - `/var/lib/compute/vms`: VM workspaces and disks
