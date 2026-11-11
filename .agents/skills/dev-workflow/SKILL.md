---
name: dev-workflow
description: >
  Development workflow for on-demand-vms: agents work in git worktrees, push to dev branch,
  main only receives changes via approved pull requests. Use when starting any implementation,
  creating branches, committing, pushing, or opening PRs.
---

# Development Workflow (on-demand-vms)

## Branch model
- Agents interact with `dev` only: base all worktrees and pull requests on `dev`, and never checkout, push, merge into, or open a pull request targeting `main`.
- `main` — production branch maintained separately from agent work. It changes only through a maintainer-approved promotion from `dev`.
- `dev` — the sole integration branch. All agent work merges here first.
- Feature work happens in **git worktrees** checked out from `dev`.

## Worktree workflow
1. `git fetch origin`
2. `git worktree add ../on-demand-vms-<topic> -b <type>/<topic> origin/dev`
   - branch prefixes: `feat/`, `fix/`, `chore/`, `docs/`, `refactor/`
3. Work inside the worktree. Run tests locally.
4. Commit with scoped messages: `feat: ...`, `fix: ...`.
5. Push branch: `git push -u origin <branch>`.
6. Open PR **targeting `dev`**: `gh pr create --base dev`.
7. After review/approval, merge into `dev`.

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
- End each task with evidence supporting the completion claim: list checks run and relevant observed results. Include screenshots when they are useful or needed to demonstrate visual behavior; do not commit screenshots unless requested.
