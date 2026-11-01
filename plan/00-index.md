# Compute Remote Worker Platform Plan

This directory is the implementation specification for turning Compute into a
platform where users install a worker on their own Linux machine and run
Firecracker VMs, CI jobs, agent tasks, and arbitrary compute through the
Compute control plane.

## Reading order

1. [01-product-scope-and-principles.md](01-product-scope-and-principles.md)
2. [02-current-state-and-gaps.md](02-current-state-and-gaps.md)
3. [03-target-architecture.md](03-target-architecture.md)
4. [04-worker-agent.md](04-worker-agent.md)
5. [05-control-plane-and-scheduler.md](05-control-plane-and-scheduler.md)
6. [06-protocol-and-api.md](06-protocol-and-api.md)
7. [07-data-model-and-migrations.md](07-data-model-and-migrations.md)
8. [08-security-and-trust.md](08-security-and-trust.md)
9. [09-workloads-and-ci.md](09-workloads-and-ci.md)
10. [10-cli-gui-and-user-experience.md](10-cli-gui-and-user-experience.md)
11. [11-operations-observability-and-upgrades.md](11-operations-observability-and-upgrades.md)
12. [12-testing-and-acceptance.md](12-testing-and-acceptance.md)
13. [13-implementation-sequence.md](13-implementation-sequence.md)

The existing [15-cli-implementation-plan.md](15-cli-implementation-plan.md)
remains the detailed CLI and WebSocket follow-on plan. This set adds the
worker, scheduler, workload, security, and operations details needed to make
that CLI useful across remote customer-owned machines.

## Status vocabulary

- **Existing**: present in the repository and reusable, but may need hardening.
- **Extend**: present but missing the remote-worker behavior.
- **Build**: new implementation required.
- **Defer**: intentionally excluded from the first production release.

## First release definition

A release is complete only when a user can install `compute-worker` on a clean
supported Linux host, enroll it without opening an inbound port, submit an
ephemeral VM job, observe logs, retrieve artifacts, cancel it, and verify that
the VM and temporary credentials are removed.
