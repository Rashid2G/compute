# Future features: MVP evaluation

This document evaluates features for the first working version of this project, with two use cases in mind:

1. Users launch and manage VMs through the CLI or API.
2. AI agents run commands and workloads inside those VMs.

“Required” means necessary for a useful and safe first release, not feature parity with Boat.dev. Difficulty estimates are relative to this repository, which already has a Firecracker VM manager, daemon, CLI, SQLite persistence, networking, persistent disks, SSH, and RAM snapshot support.

## Feature evaluation

| Feature | Description | MVP importance | Required for MVP? | Implementation difficulty |
|---|---|---:|:---:|---:|
| VM lifecycle: create, start, stop, delete, list, inspect | The basic controls to provision VMs, check their state, and clean them up. | Critical | Yes | Medium — the project already implements most of this lifecycle. |
| CLI | Lets users run common VM operations from a terminal. | Critical | Yes | Low–Medium — an `compute` CLI already exists. |
| HTTP API | Lets applications and agents control VMs without invoking a shell command. | Critical | Yes | Medium — the daemon API exists; it needs a stable, complete contract. |
| VM readiness and structured errors | Reports when a VM is usable and explains failures in a way clients can handle. | High | Yes | Medium — needs consistent states and error responses across daemon and CLI. |
| Resource sizing and host capacity limits | Lets users choose CPU and memory while preventing oversubscription of the host. | High | Yes | Medium — sizing exists; host-wide capacity checks need definition and enforcement. |
| Remote command execution | Runs a command inside a VM and returns output and exit status. | Critical | Yes, especially for agents | Medium — SSH execution exists; a structured API job interface would be more reliable. |
| Command timeouts and cancellation | Stops commands that run too long or are no longer needed. | High | Yes for safe automation | Medium — needs process tracking and clear behavior when a VM or daemon stops. |
| Authentication and API keys | Controls who can create, inspect, execute in, or delete VMs. | Critical | Yes before multi-user or network exposure | Medium–High — API permissions must align with VM ownership and host access. |
| VM isolation and network controls | Prevents one VM or user from reaching another VM or the host improperly. | Critical | Yes | High — requires careful review of Firecracker, TAP, firewall, and daemon boundaries. |
| Persistent VM disks | Keeps files and installed dependencies across stop and restart. | High | Yes for persistent workspaces | Low–Medium — persistent disks already exist. |
| Clean base VM image | Gives users and agents a predictable operating system and toolchain. | High | Yes | Low–Medium — an Alpine image exists; confirm it fits target agent workloads. |
| Environment variables and secret files | Supplies project configuration and credentials to a VM. | High | Recommended; required for many real workloads | Medium — needs secure injection and protection from logs and metadata leaks. |
| Setup scripts and repository checkout | Prepares a VM with a project and its dependencies. | High | Strongly recommended, but can follow basic VM launch | Medium — needs setup status, failure handling, and per-project configuration. |
| Agent-friendly execution interface | Gives agents a stable way to create VMs, run tasks, stream output, inspect results, and clean up. | Critical | Yes for the agent use case | Medium–High — builds on API auth, lifecycle state, and command jobs. |
| Agent harness integration | Installs or invokes specific tools such as Claude Code, Codex, or other harnesses inside a VM. | Medium | No | Medium–High — useful convenience; initially agents can install and run their own tools. |
| Detached/background command jobs | Lets long tasks such as tests and development servers outlive the initial API request. | High | Strongly recommended | Medium — needs job IDs, output retrieval, exit status, and cancellation. |
| Output streaming and command history | Shows live command output and allows users to inspect completed work later. | High | Streaming recommended; history can follow | Medium — needs streaming, storage, and retention limits. |
| Automatic VM expiry | Stops VMs after a configured duration so forgotten VMs do not consume host capacity. | High | Recommended for shared or exposed service | Medium — needs persisted deadlines and a reliable cleanup worker. |
| Disk snapshots and VM forks | Saves a prepared disk state and creates copies for separate tasks or agents. | Medium–High | No for first release | High — disk snapshots differ from RAM/device snapshots; copy-on-write adds storage complexity. |
| File upload and download | Moves source files, artifacts, and results between client and VM. | High | Recommended | Medium — SCP exists; a structured API or CLI workflow would improve automation. |
| Public HTTPS port forwarding | Gives users a URL to reach a web app running inside a VM. | Medium | No, unless preview URLs are a launch requirement | High — requires routing, TLS, access tokens, and strong VM isolation. |
| Webhooks | Notifies external systems when a VM is ready, fails, stops, or changes state. | Medium | No | Medium — requires signed events, retries, and delivery tracking. |
| SDKs | Provides language-specific clients, such as Python and TypeScript, for API operations. | Medium | No | Medium — best added after the API contract stabilizes. |
| Desktop/browser streaming | Shows and controls a graphical desktop inside a VM. | Low | No | High — requires a guest desktop, streaming, authentication, and input handling. |
| Multi-user teams and shared VMs | Lets multiple users share VM access and account resources. | Low | No for a single-user or internal MVP | High — requires tenancy, permissions, quotas, and auditability. |
| Billing and usage metering | Tracks consumption and charges or limits users based on usage. | Low | No for self-hosted/internal MVP | High — substantial product and operational scope. |
| Monitoring and audit logs | Records VM health and user actions to aid debugging and accountability. | Medium | Basic structured logs: yes; full monitoring: no | Medium — basic logs are straightforward; metrics and audit retention add scope. |

## Recommended first release

1. CLI and HTTP API for VM create, list, inspect, start, stop, and delete.
2. Clear VM readiness states and structured errors.
3. Host capacity checks and safe VM isolation.
4. Authenticated API access before exposing the daemon to multiple users or an untrusted network.
5. Persistent disks and a documented base image.
6. Command execution with captured output, exit status, timeout, and cancellation.
7. File transfer and environment-variable injection.
8. Basic VM expiry or an explicit operator cleanup mechanism.
9. An agent workflow example that provisions a VM, runs a task, collects results, and cleans up.

Defer agent-specific harness management, disk forks, public preview URLs, desktop streaming, teams, billing, and SDKs until the basic CLI/API and agent execution workflow is reliable.

## Basis for the assessment

The project baseline is described in [README.md](README.md). Boat.dev feature references include its [documentation index](https://docs.boat.dev/llms.txt), [snapshots](https://docs.boat.dev/snapshots.md), [environments](https://docs.boat.dev/environments.md), [long-running tasks](https://docs.boat.dev/long-running-tasks.md), [public URLs](https://docs.boat.dev/hosting.md), [integrated agents](https://docs.boat.dev/integrated-agents.md), [organizations](https://docs.boat.dev/organizations.md), [API keys](https://docs.boat.dev/api-keys.md), [limits](https://docs.boat.dev/limits.md), and [CI](https://docs.boat.dev/ci.md). Priorities and difficulty estimates are project-specific judgments, not Boat.dev classifications.
