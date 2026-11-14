---
name: preview-deploy
description: >
  MANDATORY for every pull request in this repository: deploy a live Docker
  Compose preview stack of the PR's changes on compute-lab's Docker daemon
  (never inside the dev deployment), verify auth and UI/API work, and report
  the preview URL. Use when opening, updating, rebasing, or reviewing a PR,
  when the user asks for a preview deployment, or when running
  `make deploy-current` / `preview-deploy` / `preview-stop`.
---

# PR Preview Deployments

Every agent opening or updating a pull request in this repository **must deploy
the PR's changes as a live preview Docker Compose stack on `compute-lab`'s
Docker daemon** using this workflow, and include the preview URL in the final
report. A PR is not "done" until the preview is deployed and verified. Do not
skip this step.

> [!IMPORTANT]
> All preview deployments MUST run directly on `compute-lab`'s Docker daemon
> (`scripts/preview-docker.sh`). Do **not** provision preview deployments inside
> the `dev` branch deployment (`compute sandbox`).

## How previews work

- Each preview runs as its own isolated Docker Compose stack (`preview-<name>`)
  on `compute-lab`'s Docker daemon, named `pr-<number>` (or the sanitized branch
  name when no PR exists yet).
- `scripts/preview-docker.sh` builds the PR's `computed` Linux binary (with the
  PR's embedded Web UI), picks a stable free slot (`19000`–`19099`, bridge
  `mft3brp<slot>`, subnet `172.<100+slot>.0.0/16`), sets `COMPUTE_DEPLOYMENTS_INDEX=false`
  so `/` serves the PR's Web UI directly, disables `:53` DNS and `:80` edge
  (`dev` owns those), and inherits authentication credentials from the lab `.env`.
- Reviewers access the preview directly at `http://192.168.1.70:<port>/`.
- Each deploy writes a manifest to `/home/rashid/on-demand-vms/data/previews/<name>.json`,
  which the main `dev` daemon's **deployments homepage** at
  `http://192.168.1.70:18080/` lists alongside `dev`.

## Required workflow (agents)

1. Finish the implementation in your worktree branched from `dev` (see
   `dev-workflow`). Commit your changes.
2. Deploy the preview from the current worktree — no PR number needed:

   ```bash
   make deploy-current
   ```

   The preview name is derived automatically: `pr-<n>` when the branch has an
   open PR (via `gh pr view`), otherwise the sanitized branch name
   (`feat/add-thing` → `feat-add-thing`).
3. Wait for the script to finish: it rebuilds Web UI assets if changed, compiles
   `computed` (and `compute-worker` if changed), starts the Docker Compose stack
   on `compute-lab`, health-checks it, and prints the preview URL and
   deployments homepage URL.
4. **Verify the preview**: `curl` the printed preview URL, health endpoint, and
   homepage, e.g.:

   ```bash
   curl -sf -o /dev/null -w '%{http_code}\n' http://192.168.1.70:<port>/api/v1/health
   curl -sf http://192.168.1.70:18080/ | head -n 5   # homepage lists it
   ```

   Also confirm Web UI HTML is served at `/` and auth is enabled (unauthenticated
   `/api/v1/auth/me` returns `401`, never `"authentication manager not enabled"`).
5. Post the preview URL in the PR description or a comment, e.g.
   `Live preview: http://192.168.1.70:<port>/` and mention the homepage
   `http://192.168.1.70:18080/`.
6. Report the preview URL (and homepage) in your final answer to the user,
   together with the PR link.

### Updating a preview

Re-run `make deploy-current` from the same branch/worktree after new commits.
The script reuses the preview's existing slot/port and redeploys the stack
idempotently.

## Teardown

- When a PR is closed/merged, or when the user asks to clean up:

  ```bash
  make preview-stop PR=<n>            # by PR number or name
  make deploy-current ARGS="--stop"   # for the current branch
  ```

- If a preview is unhealthy, inspect logs with:
  `make deploy-current ARGS="--logs"` or
  `ssh compute-lab 'sudo docker logs -f preview-pr-<n>-computed-1'`.

## Reference

- Scripts: `scripts/preview-docker.sh` (Docker engine),
  `scripts/deploy-current.sh` (name derivation + CLI wrapper), `Makefile`
  targets `deploy-current`, `preview-deploy`, `preview-stop`.
- Preview auth: preview daemons inherit `COMPUTE_AUTH_ENABLED` and admin
  credentials from `compute-lab`'s `.env`; the deployments homepage itself at
  `:18080/` is unauthenticated and exposes only deployment names/branches/status.
- Environment overrides: `COMPUTE_HOST` (SSH host, default `compute-lab`),
  `PREVIEW_BASE` (diff base, default `origin/dev`), `COMPUTE_WORKER_TOKEN`
  (start the PR's worker in preview).
