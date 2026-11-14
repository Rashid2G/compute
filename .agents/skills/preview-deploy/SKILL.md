name: preview-deploy
description: >
  MANDATORY for every pull request in this repository: deploy a live preview
  sandbox of the PR's changes to compute-lab using path-based previews and the
  deployments homepage, and verify the preview works before finishing the task.
  Use when opening, updating, rebasing, or reviewing a PR, when the user asks
  for a preview deployment, or when running `make deploy-current` /
  `preview-deploy` / `preview-stop`.
---

# PR Preview Deployments

Every agent opening or updating a pull request in this repository **must deploy
the PR's changes as a live preview sandbox on `compute-lab`** using this
workflow, and include the preview URL in the final report. A PR is not
"done" until the preview is deployed and verified. Do not skip this step.

## How previews work

- Each preview runs inside an isolated Firecracker microVM sandbox on
  `compute-lab` (the `dev` deployment host), named `pr-<number>` (or the
  sanitized branch name when no PR exists yet).
- `scripts/temp-deploy-sandbox.sh` diffs the current branch against
  `origin/dev` and deploys **only the services the PR changes**:
  - web/UI or GUI changes → new `compute gui` in the guest (always the front
    door), talking to the shared lab daemon;
  - daemon/Go changes → new `computed` daemon in the guest too;
  - worker changes → `compute-worker` staged (needs `COMPUTE_WORKER_TOKEN`).
  - docs/scripts/SDK-only changes → GUI front door only.
- Reviewers access previews at the **path-based preview URL**:
  `http://compute-lab:18080/preview/<sandbox-name>/8080/`
  The daemon's ingress proxy rewrites the preview's HTML (base href +
  fetch/XHR/EventSource/WebSocket patching), so the Web UI works unchanged
  under the path prefix. No frontend changes are required for previews.
- A **deployments homepage** at `http://compute-lab:18080/` lists the main dev
  GUI plus every running sandbox with its preview link. Give reviewers this
  URL instead of raw sandbox names.

## Required workflow (agents)

1. Finish the implementation in your worktree branched from `dev` (see
   `dev-workflow`). Commit your changes.
2. Deploy the preview from the current worktree — no PR number needed:

   ```bash
   make deploy-current
   ```

   The sandbox name is derived automatically: `pr-<n>` when the branch has an
   open PR (via `gh pr view`), otherwise the sanitized branch name
   (`feat/add-thing` → `feat-add-thing`).
3. Wait for the script to finish (1–5 minutes: it builds linux binaries,
   provisions the microVM, installs services, and health-checks them). It
   prints the preview URL and the deployments homepage URL.
4. **Verify the preview**: `curl` the printed preview URL and the homepage,
   e.g.

   ```bash
   curl -sf -o /dev/null -w '%{http_code}\n' http://compute-lab:18080/preview/<name>/8080/api/v1/health
   curl -sf http://compute-lab:18080/ | head -n 5
   ```

   For UI changes, confirm the HTML is served (and ideally open the URL in a
   browser/screenshot as evidence).
5. Post the preview URL in the PR description or a comment, e.g.
   `Live preview: http://compute-lab:18080/preview/pr-<n>/8080/` and mention
   the homepage `http://compute-lab:18080/`.
6. Report the preview URL (and homepage) in your final answer to the user,
   together with the PR link.

### Updating a preview

Re-run `make deploy-current` from the same branch/worktree after new commits.
The script recreates the sandbox from scratch (idempotent), so stale state
never accumulates.

### Deploy only what changed

The diff detection is automatic. Do not force full deployments; if the
diff looks wrong (e.g. daemon rebuilt for a docs-only PR), check `git status`
for stray files before assuming the script is broken.

## Teardown

- When a PR is closed/merged, or when the user asks to clean up:

  ```bash
  make preview-stop PR=<n>            # by PR number
  make deploy-current ARGS="--stop"   # for the current branch
  ```

- If a preview is unhealthy, tearing it down and re-deploying is the first
  remedy. Logs are inside the guest:
  `ssh compute-lab "~/on-demand-vms/bin/compute ssh <sandbox>"` then
  inspect `/home/compute/preview/logs/`.

## Reference

- Script: `scripts/temp-deploy-sandbox.sh` (engine), `scripts/deploy-current.sh`
  (name derivation + wrapper), `Makefile` targets `deploy-current`,
  `preview-deploy`, `preview-stop`.
- Preview auth: previews are private by default; reviewers authenticate like
  the normal Web UI. The homepage itself is unauthenticated and exposes only
  sandbox names/owners/status.
- Environment overrides: `COMPUTE_HOST` (SSH host, default `compute-lab`),
  `PREVIEW_BASE` (diff base, default `origin/dev`), `COMPUTE_VCPUS`,
  `COMPUTE_MEMORY_MB`.
