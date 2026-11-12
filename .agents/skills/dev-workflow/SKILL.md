---
name: dev-workflow
description: >
  Development workflow for on-demand-vms: agents work in git worktrees if changes include code or non-simple things, push to dev branch,
  main only receives changes via approved pull requests. Use when starting any implementation,
  creating branches, committing, pushing, or opening PRs.
---

# Development Workflow (on-demand-vms)

## Branch model
- Agents interact with `dev` only: base all worktrees and pull requests on `dev`, and never checkout, push, merge into, or open a pull request targeting `main`.
- `main` — production branch maintained separately from agent work. It changes only through a maintainer-approved promotion from `dev`.
- `dev` — the sole integration branch. All agent work merges here first.

## Mandatory worktree requirement
- **Agents must work in git worktrees** whenever changes include code or any non-simple task (features, bug fixes, refactoring, multi-file edits, schema/dependency changes).
- Never edit code or carry out non-simple changes directly in the primary workspace checkout.
- Only simple read-only queries or trivial single-line documentation tweaks are permitted without a worktree.

## The 6-step agent workflow

All implementation tasks follow this structured flow:

1. **Plan the changes**: Understand requirements, inspect relevant code/configs/tests, check `git status --short`, and outline changes before editing.
2. **Implement**: Work inside a dedicated git worktree from `dev`. Commit with scoped messages (`feat: ...`, `fix: ...`, `docs: ...`).
3. **Write tests and run them**: Write focused tests and verify locally (`make test`, `npm run typecheck && npm run build` for web).
4. **Provide evidence of the end change**: If the change is visible (UI/CLI/visual), provide concrete evidence (e.g. screenshots of UI changes, verified command outputs).
5. **Run on a separate temp deployment (background sandbox)**: Each preview deployment **must contain the PR number** in its sandbox name (e.g. `pr-<PR_NUMBER>`) and **must run on the main deployment that is deployed from the `dev` branch at the VM `compute-lab`** (self-served by the project daemon). Run `./scripts/temp-deploy-sandbox.sh <PR_NUMBER> <port>` or `make preview-deploy PR=<PR_NUMBER>`. The script deploys **only the services changed by the PR** into the guest (diff vs `origin/dev`) and reuses the compute-lab services for everything else (e.g. a UI-only PR runs the new UI in the sandbox against the shared compute-lab daemon; a daemon change runs the new daemon plus its matching GUI in the guest). Each sandbox enrolls in the tailnet individually (ephemeral, tagged, per-sandbox auth key) and therefore gets its **own unique Tailscale URL** (`http://compute-pr-<PR_NUMBER>.<tailnet>.ts.net:<port>` or its own `http://100.x.y.z:<port>`) — always obtain and link the URL of the sandbox serving this PR, never another sandbox's. Launch in the background without blocking PR creation.
6. **Create the PR**: Push branch (`git push -u origin <branch>`) and open PR targeting `dev` (`gh pr create --base dev`), linking issues and including the Tailscale sandbox URL.

## Worktree workflow
1. `git fetch origin`
2. `git worktree add ../on-demand-vms-<topic> -b <type>/<topic> origin/dev`
   - branch prefixes: `feat/`, `fix/`, `chore/`, `docs/`, `refactor/`
3. Plan and implement inside the worktree (`cd ../on-demand-vms-<topic>`).
4. Write tests and run them locally: `make test` (or web build/check).
5. Provide evidence of the change (screenshots for UI, command output for CLI).
6. Commit with scoped messages (`feat: ...`, `fix: ...`, `docs: ...`) and push branch: `git push -u origin <branch>`.
7. Open PR **targeting `dev`**: `gh pr create --base dev` with `Closes #n` / `Refs #n`.
8. Spin up temp preview sandbox in background with PR number on `compute-lab`: `make preview-deploy PR=<number>` or `./scripts/temp-deploy-sandbox.sh <number> &`.
9. Link the Tailscale URL (`http://compute-pr-<number>.<tailnet>.ts.net:<port>`) in the PR and comments.
10. After review/approval and merge into `dev`, clean up with `git worktree remove ../on-demand-vms-<topic>`.

## Issues
- Track bugs, deferred work, and follow-ups as GitHub issues (see skill `github-issues`).
- Name branches after the issue (`fix/42-queue-starvation`) and reference it in the PR body.

## Promoting dev -> main
- Only the maintainer opens PRs from `dev` to `main`.
- Promotion PRs to `main` require explicit user approval; agents must not create, modify, or merge them.
- Never force-push or rewrite published history on `main`/`dev`.

## Rules
- One worktree per task; clean up with `git worktree remove` after merge.
- Never commit secrets, local state, VM disks, screenshots, binaries.
- Run `gofmt` on changed Go files; `npm run typecheck` for web changes.
- Preserve unrelated user changes; check `git status` first.
- Do not amend or force-push without explicit direction.

## Verification before PR
- `make build` (Go)
- `make test` or focused `go test ./path/...`
- For web: `cd web && npm run typecheck && npm run build`
- `git diff --check`
- Evidence of end change provided (screenshots for UI, verified command outputs for CLI/backend).
- Temporary deployment sandbox named with the PR number (`pr-<PR_NUMBER>`) launched in the background on the main `dev` deployment on `ssh compute-lab`, with only the PR's changed services deployed into the sandbox (unchanged services reused from compute-lab), and its **own per-sandbox Tailscale URL** provided (each sandbox enrolls in the tailnet individually — ephemeral, tagged, per-sandbox auth key — so its URL, `compute-pr-<PR_NUMBER>.<tailnet>.ts.net` / its own `100.x.y.z`, is unique to it and must never be another sandbox's).
