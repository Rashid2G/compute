# 4. Worker Agent

## Binary and package layout

Add:

```text
cmd/compute-worker/main.go
internal/worker/config.go
internal/worker/enrollment.go
internal/worker/connection.go
internal/worker/protocol.go
internal/worker/capabilities.go
internal/worker/admission.go
internal/worker/supervisor.go
internal/worker/imagecache.go
internal/worker/artifacts.go
internal/worker/metrics.go
internal/worker/reconcile.go
scripts/install-worker.sh
scripts/compute-worker.service
```

Reuse `internal/vm`, `internal/node/runtime/firecracker`, networking, storage,
guest-agent protocol, PKI, and configuration packages. Do not copy Firecracker
launch logic into the worker package.

## Installation

The installer must:

1. Detect Linux and architecture.
2. Verify the downloaded binary checksum and signature.
3. Create a dedicated `compute` system user/group.
4. Create `/etc/compute-worker/`.
5. Create `/var/lib/compute-worker/`.
6. Create `/var/cache/compute-worker/images/`.
7. Install a systemd unit with hardening options.
8. Prompt for or accept an enrollment command.
9. Refuse to print secrets to shell history.
10. Provide `uninstall`, `status`, `logs`, and `diagnose` commands.

Support a tarball and package-manager path later. The installer must never
execute a remote shell script with unverified content in production without a
documented checksum/signature fallback.

## Configuration

Example:

```toml
control_plane = "https://control.example.com"
worker_id = "nod_01..."
certificate_file = "/etc/compute-worker/worker.crt"
private_key_file = "/etc/compute-worker/worker.key"
ca_file = "/etc/compute-worker/ca.crt"
state_dir = "/var/lib/compute-worker"
cache_dir = "/var/cache/compute-worker/images"

[resources]
max_vcpus = 8
max_memory_mb = 16384
max_disk_mb = 102400
reserved_vcpus = 1
reserved_memory_mb = 1024

[policy]
allow_untrusted_jobs = false
allow_nested_virtualization = false
allow_public_egress = true
max_job_runtime_seconds = 21600
```

Configuration rules:

- Private key files are mode `0600`.
- Configuration cannot increase kernel or host limits.
- Local limits are authoritative over control-plane requests.
- A missing or invalid policy causes safe startup failure, not permissive mode.

## Lifecycle

Worker states:

```text
enrolling -> pending -> ready -> cordoned -> draining -> offline -> revoked
```

Rules:

- `pending`: certificate accepted but capabilities not confirmed.
- `ready`: can receive work.
- `cordoned`: no new work; existing workloads continue.
- `draining`: no new work; worker waits for active work or receives cancellation.
- `offline`: heartbeat/stream unavailable.
- `revoked`: credentials rejected; worker must stop accepting assignments.

The worker must persist enough local state to reconcile running VMs after a
process restart. It must never claim a workload is complete solely because its
own process restarted.

## Capability report

Report:

- Worker ID and agent version.
- OS and kernel version.
- Architecture.
- Firecracker version.
- Guest-agent version.
- CPU count and usable CPU capacity.
- Total and usable memory.
- Disk capacity and free space.
- Network mode and egress policy.
- Supported image architectures.
- Nested virtualization availability.
- Labels configured by the operator.
- Image/template cache contents.
- Active workload IDs.

Do not report host environment variables, user home contents, process command
lines, or arbitrary sensitive metadata.

## Connection behavior

- Use mTLS with a pinned control-plane CA.
- Open one control stream plus a bounded number of upload streams.
- Send a heartbeat at the negotiated interval.
- Apply server backpressure to logs and artifacts.
- Reconnect with exponential backoff and jitter.
- Resume from the last acknowledged event sequence.
- Reject stale or duplicate assignments.
- Stop accepting new work when certificate validation fails.

## Local admission control

Before accepting work, atomically verify:

- vCPU reservation.
- Memory reservation.
- Disk reservation.
- Maximum number of VMs.
- Image availability or download capacity.
- Required labels/capabilities.
- Local policy permits the workload.

Release reservations on every terminal path, including process crashes,
cancellation, timeout, VM launch failure, and artifact upload failure.

## Supervisor

The supervisor owns one record per workload:

```text
assigned -> preparing -> booting -> ready -> running
                                  |          |
                                  v          v
                              failed     stopping
                                               |
                                               v
                                            cleaned
```

It must:

- Enforce deadlines locally.
- Kill guest processes before destroying a VM.
- Capture exit code and signal.
- Bound stdout/stderr buffers.
- Upload logs incrementally.
- Preserve a final cleanup record.
- Refuse a second supervisor for the same workload.

## Worker shutdown

On SIGTERM:

1. Mark the worker draining locally.
2. Notify the control plane.
3. Stop accepting assignments.
4. Finish or cancel workloads according to policy.
5. Send final workload states.
6. Close the stream.

On forced shutdown, the control plane must discover the interruption through
heartbeat and lease expiry.
