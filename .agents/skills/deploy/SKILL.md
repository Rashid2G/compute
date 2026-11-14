---
name: deploy
description: >
  Deploy on-demand-vms changes: mandatory per-PR live preview sandboxes on compute-lab
  (`make deploy-current`, teardown, path-based URLs and deployments homepage),
  main lab deployments (`make deploy`, `make deploy-ui` on 192.168.1.70:18080/8080),
  local dev startup, and lab integration tests. Use when opening/updating a PR, deploying
  to compute-lab, checking remote status/logs, or troubleshooting preview sandboxes.
---

# Deployments (`compute-lab` & PR Previews)

## Target Environment

- **Host alias**: `compute-lab` (`192.168.1.70`, SSH with non-interactive key / `BatchMode=yes`; passwordless `sudo -n` required on remote for preview provisioning)
- **Main Daemon API**: `http://127.0.0.1:18080` (on VM) / `http://192.168.1.70:18080` (LAN)
- **Main GUI**: `http://127.0.0.1:8080` (on VM) / `http://192.168.1.70:8080` (LAN)
- **Deployments Homepage**: `http://192.168.1.70:18080/` (lists main dev GUI + all active preview sandboxes)
- **Deployed checkout**: `/home/rashid/on-demand-vms`

Note: `compute-lab` is an SSH alias in `~/.ssh/config` and may not resolve in local DNS/HTTP; use `192.168.1.70` (or the `HOST_IP` printed by the deploy script) for `curl` and browser URLs. Never print secrets, private keys, SSH config contents, or tokens in logs or reports.

---

## 1. PR Preview Sandboxes (Mandatory for every PR)

Every PR must have a live preview sandbox deployed on `compute-lab` and verified before the PR is considered complete.

### How previews work

- Each preview runs inside an isolated Firecracker microVM sandbox on `compute-lab`, named `pr-<number>` (when an open PR exists for the branch; otherwise `deploy-current.sh` passes the sanitized branch name and `temp-deploy-sandbox.sh` prefixes `pr-`, producing `pr-<branch>`).
- **Recommended timing**: Commit, push, and run `gh pr create --base dev` **before** running `make deploy-current` so `gh pr view` detects the PR number and names the sandbox `pr-<n>` cleanly (avoiding orphaned `pr-<branch>` sandboxes).
- `scripts/temp-deploy-sandbox.sh` diffs `HEAD` plus any uncommitted/untracked working-tree files against `origin/dev` and deploys **only the services the PR changes**:
  - `web/` or `internal/gui/` or `cmd/compute/` changes -> new `compute gui` in the guest (always the front door), talking to the shared lab daemon.
  - Daemon / Go changes -> new `computed` daemon in the guest too.
  - Worker changes -> `compute-worker` staged (requires `COMPUTE_WORKER_TOKEN`).
  - Docs / scripts / SDK-only changes -> GUI front door only.
- Reviewers access previews via the **path-based preview URL**:
  `http://192.168.1.70:18080/preview/pr-<n>/8080/`
  The daemon ingress proxy rewrites HTML (`<base href>`, `fetch`/XHR/`EventSource`/WebSocket patching) so the Web UI works unchanged under the prefix.
- Previews are **private by default** (`401 Unauthorized` without auth; reviewers sign in via Web UI or pass an API key / Bearer token). The deployments homepage (`http://192.168.1.70:18080/`) is unauthenticated and lists sandbox names, owners, and status.

### Preview workflow

1. Commit your verified changes in your `dev`-based worktree (no stray uncommitted/untracked files), push, and open the PR (`gh pr create --base dev`).
2. Deploy from the current worktree:
   ```bash
   make deploy-current
   ```
   Wait 1–5 minutes for Linux cross-builds, sandbox provisioning, and health checks. Re-running `make deploy-current` after new commits recreates the sandbox cleanly from scratch.
   - If the diff detection rebuilds unexpected services, check `git status` for stray files before assuming the script is broken.
3. Verify the deployments homepage and preview route:
   ```bash
   # Homepage (unauthenticated, expects 200 and lists the sandbox):
   curl -sf http://192.168.1.70:18080/ | head -n 20

   # Authenticated preview health check (pass COMPUTE_API_KEY via env, never echo it):
   curl -sf -o /dev/null -w '%{http_code}\n' \
     -H "X-API-Key: ${COMPUTE_API_KEY}" \
     http://192.168.1.70:18080/preview/pr-<n>/8080/api/v1/health
   ```
   Without `COMPUTE_API_KEY` locally, `curl -s -o /dev/null -w '%{http_code}\n' http://192.168.1.70:18080/preview/pr-<n>/8080/` returning `401` (from the ingress auth gate) together with `200` on `http://192.168.1.70:18080/` (plus the script's own internal guest health check `200`) confirms the preview is up. For UI changes, verify in a browser/screenshot if credentials/browser are available, or note the unauthenticated limitation honestly.
4. Add both `http://192.168.1.70:18080/preview/pr-<n>/8080/` and `http://192.168.1.70:18080/` to the PR description/comment and final report.
5. If `compute-lab` / `192.168.1.70` is unreachable from your current execution environment, report `make deploy-current` as **not run (network/SSH unreachable)** rather than skipping silently.

### Preview troubleshooting & teardown

- **Logs inside guest**:
  ```bash
  ssh compute-lab "~/on-demand-vms/bin/compute sandbox ssh <sandbox>"
  # inspect /home/compute/preview/logs/
  ```
  If a preview is unhealthy, tearing it down and re-deploying is the first remedy.
- **Teardown** (when a PR is closed/merged or cleanup is requested):
  ```bash
  make deploy-current ARGS="--stop --name pr-<n>"   # stop any sandbox by exact name
  make deploy-current ARGS="--stop"                # stop current branch/PR sandbox (must run from its worktree)
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
- **Lab feature suite** (runs against the shared live `compute-lab` daemon — run only when testing lab integration explicitly):
  ```bash
  ssh compute-lab "cd ~/on-demand-vms && sudo ./scripts/test-all-features.sh ./bin/compute 127.0.0.1:18080"
  ```
