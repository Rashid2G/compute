# CLAUDE.md

## Post-Change Deployment Workflow (MANDATORY)

Whenever you finish modifying code, tests, or configurations in this repository, **you must deploy to the `ssh compute-lab` VM so changes take effect**:

```bash
make deploy
```

If you modified files in `web/` (Web UI frontend):
```bash
make deploy-ui
```

To check service health and logs on the remote VM:
```bash
make remote-status
```

## Build & Test Commands
- `make build`: Compile binaries (`bin/compute`, `bin/computed`, `bin/compute-bench`)
- `make test`: Run all unit tests
- `make test-coverage`: Run tests with coverage profile
- `make build-ui`: Build React/Vite Web UI frontend to embedded assets
- `make setup-host`: Configure host Linux network bridge and iptables (requires sudo)
- `make bench`: Run benchmark suite against running daemon
