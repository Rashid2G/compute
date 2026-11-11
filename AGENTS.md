# Compute repository guide for coding agents

## Mission and product shape

Compute (`on-demand-vms`) is a Go-based platform for managing Firecracker microVM sandboxes and distributed jobs. The repository contains the `computed` control-plane daemon, `compute` CLI (including the embedded GUI command), `compute-worker`, a React/Vite web UI, SDKs, host networking/runtime code, and deployment tooling. Treat existing APIs and persisted state as compatibility surfaces.

## Read before changing code

1. Read this file and the relevant package documentation/tests.
2. Check `git status --short` and preserve unrelated user changes.
3. Trace a feature across its layers: CLI/client, API types and handlers, persistence/migrations, runtime/worker, and UI/SDK as applicable.
4. For deployment or host changes, inspect the relevant scripts and config before acting. Never print secrets, private keys, enrollment tokens, or environment values into logs.

## Architecture map

- `cmd/computed`: daemon startup and command configuration.
- `cmd/compute`: user CLI and GUI entry point.
- `cmd/compute-worker`, `cmd/compute-agent`: worker and agent processes.
- `internal/master`: control-plane API, auth, audit, PKI, WebSocket relay and orchestration.
- `internal/store`, `internal/db`: persistence and migrations; preserve migration ordering and make schema changes forward-compatible.
- `internal/vm`, `internal/firecracker`, `internal/node`: VM lifecycle, Firecracker integration, worker runtime and guest operations.
- `internal/network`, `internal/storage`, `internal/image`, `internal/scheduler`, `internal/jobs`: host networking, volumes, images, placement and jobs.
- `pkg/api`, `pkg/client`: shared API contracts and Go client.
- `internal/gui`: embedded GUI assets/server; `web`: React/Vite source.
- `sdk/`: language SDKs; `config/`, `scripts/`, Dockerfiles and `Makefile`: operations and packaging.

## Engineering rules

- Follow the established Go formatting, error handling, context cancellation, and package boundaries. Run `gofmt` on changed Go files.
- Keep API request/response changes consistent across server, `pkg/api`, CLI/client, web UI, and SDKs. Handle malformed input and authorization at the boundary.
- For database changes, add a new migration; do not rewrite migrations that may have shipped.
- VM, filesystem, network, and process operations need explicit cleanup on errors and safe retry/reconciliation behavior. Avoid shell interpolation of user-controlled values.
- UI changes should preserve the existing design system, keyboard operation, accessible labels/focus behavior, and loading/empty/error states.
- Add or update focused tests for behavior changes. Do not claim a feature works from compilation alone.
- Do not add dependencies or change generated/build artifacts unless the task requires it. Do not commit secrets, local state, VM disks, screenshots, or binaries unintentionally.

## Build and verification

- Go build: `make build`
- Go tests: `make test` (or focused `go test ./path/...` while iterating)
- Web typecheck/build: `cd web && npm run typecheck && npm run build` (Bun is also supported by the Makefile)
- Full integration feature suite: `make test-features` (requires a running daemon and suitable host); lab suite: `ssh compute-lab "cd ~/on-demand-vms && sudo ./scripts/test-all-features.sh ./bin/compute 127.0.0.1:18080"`.
- Distinguish checks actually run from checks not run; integration tests may require Linux, KVM, root privileges, Docker, or a reachable lab host.

## Deployment targets

The primary deployment host is `compute-lab` (`192.168.1.70`), serving UI on port 8080 and daemon API on port 18080:

- Go/config/scripts changes: `make deploy` (or `make deploy-lab`)
- Any `web/` source change: `make deploy-ui` (or `make deploy-lab-ui`)
- Status: `make remote-status` (or `make remote-status-lab`); SSH: `ssh compute-lab`


### SSH access to `compute-lab`

The deployment VM is reachable through the SSH host alias `compute-lab` (currently `192.168.1.70`). Agents and developers may connect directly with:

```bash
ssh compute-lab
```

The alias must be configured in `~/.ssh/config` and a non-interactive SSH key must be authorized on the VM; deployment scripts use `BatchMode=yes` and do not support password prompts. The deployed checkout is normally `/home/rashid/on-demand-vms`. From the VM, inspect services with `systemctl status`, query the daemon locally at `http://127.0.0.1:18080`, and query the GUI at `http://127.0.0.1:8080`; use `sudo` only where required for service or host-network administration. Do not expose private keys, SSH configuration contents, or credentials in logs or reports.

Deployment restarts live services and affects shared infrastructure. Deploy only when the user explicitly requests deployment or the active task/operating instructions require it. Confirm the correct target from current repository guidance; after deployment verify service health and report any failure. Never infer that a local test VM is safe to modify if it contains user workloads.

## Branch, worktree, and release workflow

- Agents must interact with `dev` only: base all worktrees and pull requests on `dev`, and never checkout, push, merge into, or open a pull request targeting `main`.
- `main` is production and is maintained separately from agent work. Changes may reach it only through a maintainer-approved promotion from `dev`.
- `dev` is the sole integration branch. All agent work targets `dev`.
- Work happens in git worktrees checked out from `dev`:
  - `git fetch origin && git worktree add ../on-demand-vms-<topic> -b <type>/<topic> origin/dev`
  - prefixes: `feat/`, `fix/`, `chore/`, `docs/`, `refactor/`
  - clean up with `git worktree remove` after merge.
- Push branch and open PR against `dev`: `git push -u origin <branch>` then `gh pr create --base dev`.
- Promoting `dev` -> `main` is maintainer-only and requires explicit user approval.
- Never force-push or rewrite published history on `main`/`dev`.
- See skill `dev-workflow` for full details and `deployment` for lab deploy steps.

## Issue-driven tracking

- Persist any out-of-scope problem, deferred work, or follow-up as a GitHub issue so it can be resumed later; see skill `github-issues`.
- Reference issues from branches (`fix/<n>-<slug>`) and PR bodies (`Closes #n` when fully resolved, else `Refs #n`).
- If a PR only partially satisfies an issue, leave a progress comment on the issue and keep it open; see skill `using-skills`.
- Record reproduction steps, environment, affected paths, and testable acceptance criteria; redact secrets.
- Do not close an issue without a linked commit/PR or a written reason.

## Change completion and Git

Before finishing, inspect `git diff --check`, review the final diff, and report files changed, checks run, deployment status, and known gaps. Do not commit or push unless requested. Use descriptive, scoped commits when authorized; do not amend or force-push without explicit direction.

## Agent delegation and issues

Work independently unless the user or repository task explicitly requests delegation. When delegation is requested, provide the child agent this file plus a bounded task, relevant paths, expected output, and shared-worktree coordination instructions. Do not claim a separate issue/thread has been created unless the issue/thread tool confirms it. Record concrete failures with reproduction steps and affected behavior; fix them in the current task when authorized, otherwise return a concise issue-ready report.

## Skills

Use installed skills when their stated scope applies. Load them with the `skill` tool at task start; see the `using-skills` skill for discovery, precedence, and how to apply them. Repository skills live in `.agents/skills/<name>/SKILL.md`. Do not modify unrelated global skills. When operational or environmental facts change (e.g. build locations, host IPs, ports, tooling flags), update the relevant skill in-place per `system-context-learning` so subsequent agents inherit ground truth. Strictly adhere to boundaries: factual/operational changes may be updated autonomously as context-only; any change that alters development workflows, safety guardrails, branching policies, or test requirements must be approved by the user first. If a reusable project-specific workflow is needed, add it under `.agents/skills/<name>/SKILL.md` and document its trigger and limits; keep `AGENTS.md` as the authoritative entry point.
