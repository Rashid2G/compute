# CLAUDE.md

## Agent Workflow (MANDATORY)

All agent tasks modifying code or configuration must follow this 6-step flow:
1. **Plan the changes**: Review architecture, git status, and target packages.
2. **Implement**: Carry out work in a git worktree based on `dev`.
3. **Write tests and run them**: Verify changes with `make test` (or web typecheck/build).
4. **Provide evidence of the end change**: Include visual proof (screenshots) or command output.
5. **Run on a separate temp deployment (in the background)**: Launch an isolated microVM sandbox named with the PR number (`pr-<PR_NUMBER>`) running on the main deployment deployed from `dev` on `compute-lab` via `make preview-deploy PR=<PR_NUMBER>` (or `./scripts/temp-deploy-sandbox.sh <PR_NUMBER>`) and link its Tailscale URL (`http://compute-pr-<PR_NUMBER>.<tailnet>.ts.net:<port>`). Run in the background without blocking PR creation. The preview deploys only the services changed by the PR into the sandbox and reuses the compute-lab services for the rest (UI-only PR: new UI in sandbox + shared lab API; daemon change: new daemon + matching GUI in sandbox).
6. **Create the PR**: Open a pull request targeting `dev` (`gh pr create --base dev`) with the Tailscale URL.

## Deployment Commands
- `make preview-deploy PR=<n>`: Spin up an isolated temp preview sandbox named `pr-<n>` running on the main `dev` deployment on `compute-lab` in the background and output its Tailscale URL (non-blocking for PRs); deploys only the PR's changed services into the sandbox and reuses compute-lab services for the rest
- `make deploy`: Deploy code/config/scripts to the main shared deployment on `ssh compute-lab` (restarts services)
- `make deploy-ui`: Build web UI and deploy to main shared deployment
- `make remote-status`: Check health of main deployment services
- `make remote-logs`: Tail logs on remote VM

## Build & Test Commands
- `make build`: Compile binaries (`bin/compute`, `bin/computed`, `bin/compute-bench`)
- `make test`: Run all unit tests
- `make test-coverage`: Run tests with coverage profile
- `make build-ui`: Build React/Vite Web UI frontend to embedded assets
- `make setup-host`: Configure host Linux network bridge and iptables (requires sudo)
- `make bench`: Run benchmark suite against running daemon
