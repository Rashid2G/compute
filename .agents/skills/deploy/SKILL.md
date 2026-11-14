---
name: deploy
description: >
  Deploy on-demand-vms changes: mandatory per-PR live preview sandboxes on compute-lab
  (`make deploy-current`, `make preview-stop`, path-based URLs and deployments homepage),
  main lab deployments (`make deploy`, `make deploy-ui` on 192.168.1.70:18080/8080),
  local dev startup, and lab integration tests. Use when opening/updating a PR, deploying
  to compute-lab, checking remote status/logs, or troubleshooting preview sandboxes.
---

# Deployments (`compute-lab` & PR Previews)

## Target Environment

- **Host alias**: `compute-lab` (`192.168.1.70`, SSH with non-interactive key / `BatchMode=yes`)
- **Main Daemon API**: `http://127.0.0.1:18080` (on VM) / `http://compute-lab:18080`
- **Main GUI**: `http://127.0.0.1:8080` (on VM)
- **Deployments Homepage**: `http://compute-lab:18080/` (lists main dev GUI + all active preview sandboxes)
- **Deployed checkout**: `/home/rashid/on-demand-vms`

Never print secrets, private keys, SSH config contents, or tokens in logs or reports.

---

## 1. PR Preview Sandboxes (Mandatory for every PR)

Every PR must have a live preview sandbox deployed on `compute-lab` and verified before the PR is considered complete.

### How previews work

- Each preview runs inside an isolated Firecracker microVM sandbox on `compute-lab`, named `pr-<number>` (or the sanitized branch name when no PR exists yet, e.g. `feat/add-thing` -> `feat-add-thing`).
- `scripts/temp-deploy-sandbox.sh` diffs the current branch against `origin/dev` and deploys **only the services the PR changes**:
  - `web/` or GUI changes -> new `compute gui` in the guest (always the front door), talking to the shared lab daemon.
  - Daemon / Go changes -> new `computed` daemon in the guest too.
  - Worker changes -> `compute-worker` staged (requires `COMPUTE_WORKER_TOKEN`).
  - Docs / scripts / SDK-only changes -> GUI front door only.
- Reviewers access previews via the **path-based preview URL**:
  `http://compute-lab:18080/preview/<sandbox-name>/8080/`
  The daemon ingress proxy rewrites HTML (`<base href>`, `fetch`/XHR/`EventSource`/WebSocket patching) so the Web UI works unchanged under the prefix.
- Previews are private by default (reviewers authenticate like the normal Web UI). The deployments homepage (`http://compute-lab:18080/`) is unauthenticated and lists sandbox names, owners, and status.

### Preview workflow

1. Commit your changes in your `dev`-based worktree.
2. Deploy from the current worktree (no PR number required):
   ```bash
   make deploy-current
   ```
   Wait 1–5 minutes for Linux cross-builds, sandbox provisioning, and health checks. Re-running `make deploy-current` after new commits recreates the sandbox cleanly from scratch.
   - If the diff detection rebuilds unexpected services, check `git status` for stray files before assuming the script is broken.
3. Verify the printed preview URL and deployments homepage:
   ```bash
   curl -sf -o /dev/null -w '%{http_code}\n' http://compute-lab:18080/preview/<name>/8080/api/v1/health
   curl -sf http://compute-lab:18080/ | head -n 5
   ```
   For UI changes, verify the HTML loads (and capture a browser screenshot as evidence if applicable).
4. Include both `http://compute-lab:18080/preview/<name>/8080/` and `http://compute-lab:18080/` in the PR description/comment and final report.

### Preview troubleshooting & teardown

- **Logs inside guest**:
  ```bash
  ssh compute-lab "~/on-demand-vms/bin/compute ssh <sandbox>"
  # inspect /home/compute/preview/logs/
  ```
  If a preview is unhealthy, tearing it down and re-deploying is the first remedy.
- **Teardown** (when a PR is closed/merged or cleanup is requested):
  ```bash
  make preview-stop PR=<n>            # by PR number
  make deploy-current ARGS="--stop"   # for the current branch
  ```
- **Scripts & env overrides**:
  - `scripts/temp-deploy-sandbox.sh` (engine), `scripts/deploy-current.sh` (name derivation + wrapper), `Makefile` targets `deploy-current`, `preview-deploy`, `preview-stop`.
  - Overrides: `COMPUTE_HOST` (default `compute-lab`), `PREVIEW_BASE` (default `origin/dev`), `COMPUTE_VCPUS`, `COMPUTE_MEMORY_MB`.

---

## 2. Main `compute-lab` Deployment

Deploying to the main lab service restarts live shared infrastructure—run only when explicitly requested or required by the task.

- **Go / config / scripts**: `make deploy` (or `make deploy-lab`)
- **Web UI changes**: `make deploy-ui` (or `make deploy-lab-ui`) — builds embedded assets
- **Restart only**: `make deploy-restart`
- **Health & logs**: `make remote-status` (or `make remote-status-lab`), `make remote-logs`

Always verify service health (`make remote-status`) after deploying to main.

---

## 3. Local Dev & Full Integration Suite

- **Local daemon**: `make setup-host` (sudo, configures host bridge/iptables) then `make build && make dev` (serves `127.0.0.1:18080`).
- **Local feature suite**: `make test-features` (requires running daemon + suitable Linux/KVM host).
- **Lab feature suite**:
  ```bash
  ssh compute-lab "cd ~/on-demand-vms && sudo ./scripts/test-all-features.sh ./bin/compute 127.0.0.1:18080"
  ```
