# CLAUDE.md

## Agent Workflow (MANDATORY)

All agent tasks modifying code or configuration must follow this 5-step flow:
1. **Plan the changes**: Review architecture, git status, and target packages.
2. **Implement**: Carry out work in a git worktree based on `dev`.
3. **Write tests and run them**: Verify changes with `make test` (or web typecheck/build).
4. **Provide evidence of the end change**: Include visual proof (screenshots) or command output.
5. **Create the PR**: Open a pull request targeting `dev` (`gh pr create --base dev`).

## Deployment Commands
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
