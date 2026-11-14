---
name: deploy
description: >
  Deploy on-demand-vms changes: mandatory per-PR live Docker Compose preview stacks
  on compute-lab (`make deploy-current`, `make preview-stop`, port 19000-19099 URLs
  and deployments homepage), main lab deployments (`make deploy`, `make deploy-ui` on
  192.168.1.70:18080/8080), local dev startup, and lab integration tests. Use when
  opening/updating a PR, deploying to compute-lab, checking remote status/logs, or
  troubleshooting preview stacks.
---

# Deployments (`compute-lab` & PR Previews)

## Target Environment

- **Host alias**: `compute-lab` (`192.168.1.70`, SSH with non-interactive key / `BatchMode=yes`)
- **Main Daemon API**: `http://127.0.0.1:18080` (on VM) / `http://192.168.1.70:18080`
- **Main GUI**: `http://127.0.0.1:8080` (on VM)
- **Deployments Homepage**: `http://192.168.1.70:18080/` (lists main dev GUI + all active preview stacks)
- **Deployed checkout**: `/home/rashid/on-demand-vms`

Never print secrets, private keys, SSH config contents, or tokens in logs or reports.

---

## 1. PR Preview Stacks (Mandatory for every PR)

Every PR must have a live preview Docker Compose stack deployed on `compute-lab`'s Docker daemon and verified before the PR is considered complete.

> [!IMPORTANT]
> All preview deployments MUST run directly on `compute-lab`'s Docker daemon (`scripts/preview-docker.sh`). Do **not** provision preview deployments inside the `dev` branch deployment (`compute sandbox`).

### How previews work

- Each preview runs as its own isolated Docker Compose stack (`preview-<name>`) on `compute-lab`'s Docker daemon, named `pr-<number>` (or the sanitized branch name when no PR exists yet, e.g. `feat/add-thing` -> `feat-add-thing`).
- `scripts/preview-docker.sh` builds the PR's `computed` Linux binary (with the PR's embedded Web UI), picks a stable free slot (`19000`–`19099`, bridge `mft3brp<slot>`, subnet `172.<100+slot>.0.0/16`), sets `COMPUTE_DEPLOYMENTS_INDEX=false` so `/` serves the PR's Web UI directly, disables `:53` DNS and `:80` edge (`dev` owns those), and inherits authentication credentials from the lab `.env`.
- Reviewers access the preview directly at `http://192.168.1.70:<port>/`.
- Each deploy writes a manifest to `/home/rashid/on-demand-vms/data/previews/<name>.json`, which the main `dev` daemon's deployments homepage at `http://192.168.1.70:18080/` lists alongside `dev` (unauthenticated, exposing only deployment names, branches, and status).

### Preview workflow

1. Commit your changes in your `dev`-based worktree.
2. Deploy from the current worktree (no PR number required):
   ```bash
   make deploy-current
   ```
   The script rebuilds Web UI assets if changed, compiles `computed` (and `compute-worker` if changed), starts the Docker Compose stack on `compute-lab`, health-checks it, and prints the preview URL and deployments homepage URL. Re-running `make deploy-current` after new commits reuses the preview's existing slot/port and redeploys idempotently.
3. Verify the printed preview URL, auth, and deployments homepage:
   ```bash
   curl -sf -o /dev/null -w '%{http_code}\n' http://192.168.1.70:<port>/api/v1/health
   curl -sf http://192.168.1.70:18080/ | head -n 5
   ```
   Also confirm Web UI HTML is served at `/` and auth is enabled (unauthenticated `/api/v1/auth/me` returns `401`, never `"authentication manager not enabled"`). For UI changes, capture a browser screenshot as evidence if applicable.
4. Include both `http://192.168.1.70:<port>/` and `http://192.168.1.70:18080/` in the PR description/comment and final report.

### Preview troubleshooting & teardown

- **Logs**:
  ```bash
  make deploy-current ARGS="--logs"
  # or directly on compute-lab:
  ssh compute-lab 'sudo docker logs -f preview-pr-<n>-computed-1'
  ```
- **Status**:
  ```bash
  make deploy-current ARGS="--status"
  ```
- **Teardown** (when a PR is closed/merged or cleanup is requested):
  ```bash
  make preview-stop PR=<n>            # by PR number or name
  make deploy-current ARGS="--stop"   # for the current branch
  ```
- **Scripts & env overrides**:
  - `scripts/preview-docker.sh` (Docker engine), `scripts/deploy-current.sh` (name derivation + CLI wrapper), `Makefile` targets `deploy-current`, `preview-deploy`, `preview-stop`.
  - Overrides: `COMPUTE_HOST` (SSH host, default `compute-lab`), `PREVIEW_BASE` (diff base, default `origin/dev`), `COMPUTE_WORKER_TOKEN` (start the PR's worker in preview when set).

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
