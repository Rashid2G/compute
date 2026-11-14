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
- `internal/gui`: embedded GUI assets/server (`internal/gui/assets` is built from `web/`); `web`: React/Vite source.
- `sdk/`: language SDKs; `config/`, `scripts/`, Dockerfiles and `Makefile`: operations and packaging.

## Engineering rules

- Follow established Go formatting, error handling, context cancellation, and package boundaries. Run `gofmt` on changed Go files.
- Keep API request/response changes consistent across server, `pkg/api`, CLI/client, web UI, and SDKs. Handle malformed input and authorization at the boundary.
- For database changes, add a new migration; do not rewrite migrations that may have shipped.
- VM, filesystem, network, and process operations need explicit cleanup on errors and safe retry/reconciliation behavior. Avoid shell interpolation of user-controlled values.
- UI changes should preserve the existing design system, keyboard operation, accessible labels/focus behavior, and loading/empty/error states. When editing `web/`, regenerate and commit `internal/gui/assets/` (`cd web && npm run build`).
- Add or update focused tests for behavior changes. Do not claim a feature works from compilation alone.
- Do not add unrelated dependencies or change generated/build artifacts unless the task requires it. Do not commit secrets, local state, VM disks, screenshots, or binaries unintentionally.

## Build and verification

- Go build: `make build`
- Go tests: `make test` (or focused `go test ./path/...` while iterating)
- Web typecheck/build: `cd web && ( [ -d node_modules ] || npm ci ) && npm run typecheck && npm run build` (Bun is also supported by the Makefile)
- Full integration suite: `make test-features` (requires running daemon + Linux/KVM host); lab suite: `ssh compute-lab "cd ~/on-demand-vms && sudo ./scripts/test-all-features.sh ./bin/compute 127.0.0.1:18080"` (runs against the shared lab daemon — run only when testing lab integration explicitly).
- Distinguish checks actually run from checks not run (including if `compute-lab` / SSH is unreachable).

## Standard development workflow

**Delivery modes**:
- **PR delivery flow (default)** — Use for all standard implementation, bugfix, and config tasks. Follow all 6 steps below.
- **Local-only edits** — Only when the user explicitly asks for local-only changes (or explicitly says not to commit/open a PR): still inspect `git status --short`, verify tests locally, and stop without committing, pushing, preview-deploying, or opening a PR.

**6-step PR delivery flow**:

1. **Plan**: Inspect relevant packages, tests, config, and `git status --short`; formulate a minimal plan before editing.
2. **Implement in a `dev` worktree**:
   - `dev` is the sole integration branch. Never checkout, push, merge into, or open a PR targeting `main` (promoting `dev` -> `main` is maintainer-only with explicit user approval). Never force-push or rewrite history on `dev` or `main`.
   - Create one worktree per task branched from `origin/dev`:
     ```bash
     git fetch origin && git worktree add ../on-demand-vms-<topic> -b <type>/<topic> origin/dev
     ```
     Use prefixes `feat/`, `fix/`, `chore/`, `docs/`, `refactor/` (include the issue number when applicable, e.g. `fix/42-queue-starvation`).
   - Use scoped commits (`feat: ...`, `fix: ...`, `docs: ...`); do not amend or force-push without explicit direction. Clean up the worktree (`git worktree remove`) after merge.
3. **Test & verify**: Add/update focused tests and run `make test` (for Go/GUI changes) and/or `cd web && npm run typecheck && npm run build` (installing `node_modules` via `npm ci` in a fresh worktree if absent, and staging rebuilt `internal/gui/assets/`). Run `git diff --check`.
4. **Capture evidence**: Record verified command output or UI screenshots (keep screenshots out of commits unless requested).
5. **Open PR targeting `dev`**: Commit your verified changes, push (`git push -u origin <branch>`), and open the PR (`gh pr create --base dev`).
   - **Issue linking**: Use `Closes #<n>` when the PR fully satisfies the issue's acceptance criteria; otherwise use `Refs #<n>` and post a progress comment on the issue (`gh issue comment <n> ...`) keeping it open. Never close an issue without a linked commit/PR or written reason.
   - Persist any out-of-scope bug or follow-up as a GitHub issue (see the `github-issues` skill).
6. **Deploy & verify PR preview (mandatory)**: Run `make deploy-current` from the PR's worktree after opening the PR (so the sandbox is named `pr-<n>`; see the `deploy` skill). Verify the deployments homepage (`http://192.168.1.70:18080/`) and preview URL (`http://192.168.1.70:18080/preview/pr-<n>/8080/`), and add both to the PR description/comment and your final report. If `compute-lab` is unreachable from your environment, report that explicitly instead of skipping silently.

## Deployment targets

The primary deployment host is `compute-lab` (`192.168.1.70`, reachable via `ssh compute-lab` with non-interactive SSH key / `BatchMode=yes`), serving GUI on `:8080` and daemon API on `:18080` (deployed checkout `/home/rashid/on-demand-vms`).

- **PR previews**: `make deploy-current` / `make deploy-current ARGS="--stop --name pr-<n>"`
- **Main lab deploys** (restarts live shared services — run only when explicitly requested or required by the task, and verify with `make remote-status`): `make deploy` (Go/config/scripts) or `make deploy-ui` (`web/` changes)
- **DNS/edge (docker-compose)**: authoritative DNS responder for `compute-lab.tailb1be99.ts.net` on port 53 (UDP+TCP) and HTTP edge listener on port 80, published on `192.168.1.70` and `100.78.129.96`. Sandbox hostnames are `<sandbox>.<username>.compute-lab.tailb1be99.ts.net`; `api`/`gui`/`www` and the zone apex route to the daemon (see `docs/dns-ssh.md`).

See the `deploy` skill for full preview, auth/verification, troubleshooting, and main/local deployment details.

## Agent delegation

Work independently unless the user or task explicitly requests delegation, or invokes the `leader-evaluator-refinement` workflow. When delegating, pass this file plus a bounded task, relevant paths, expected output, and shared-worktree coordination instructions. Do not claim a separate issue/thread was created unless the tool confirms it.

## Skills and operational context

- **Precedence**: Explicit user instructions > `AGENTS.md` > `.agents/skills/<name>/SKILL.md`. If a skill conflicts with `AGENTS.md`, follow `AGENTS.md`.
- **Available skills** (load only when the task needs their deeper procedures, CLI references, or templates):
  - `deploy` — PR preview sandboxes (`make deploy-current`, teardown), `compute-lab` deployments, auth/health checks, guest logs, and local/lab test suites.
  - `github-issues` — Issue templates, triage labels, resuming work, and PR/issue linking details.
  - `compute` — Operating the `compute` CLI & REST API for Firecracker sandboxes, distributed jobs, ingress routing, and dogfooding (do not load for routine codebase edits just because files mention `compute` or `sandbox`).
  - `leader-evaluator-refinement` — Multi-agent Leader + dual zero-context adversarial Evaluator orchestration (when requested or invoked for high-stakes multi-agent review).
- **Keeping context current**: When factual operational details change (e.g., host IPs, ports, paths, build flags), update both the relevant skill(s) and any matching facts in `AGENTS.md` in-place (facts only; no session logs or narrative). Never alter workflows, branching rules, verification gates, or security policies without explicit user approval.
