# 8. Security and Trust

## Threat model

Assume:

- A guest workload may be malicious.
- A job may attempt to escape its VM.
- A worker may be compromised.
- A user may submit malformed or abusive jobs.
- A control connection may disconnect or replay messages.
- A public CI pull request may be attacker-controlled.
- Logs and artifacts may contain secrets.
- A certificate or API key may be leaked.

Trust boundaries:

```text
control plane <-> worker host <-> Firecracker VM <-> workload process
```

## Worker authentication

- One-use enrollment token, hashed at rest.
- Worker-generated private key.
- Control-plane-signed certificate.
- mTLS on every worker connection.
- Certificate expiry, rotation, and revocation.
- Organization and pool binding.
- No fallback to bearer tokens after enrollment.

## Authorization

Every request must establish:

- Actor identity.
- Organization.
- Resource owner.
- Required scope.
- Allowed state transition.

Suggested scopes:

```text
worker:read
worker:admin
job:submit
job:read
job:cancel
artifact:read
ci:admin
image:read
image:write
secret:use
```

Default API keys should be read-only or job-scoped, not full administrative
keys.

## Host hardening

The systemd worker unit should use, where compatible with Firecracker:

- Dedicated user and group.
- `NoNewPrivileges=true`.
- Private temporary directory.
- Protected system and home paths.
- Restricted device access.
- Restricted address families as appropriate.
- Capability allowlist.
- Resource limits.
- Read-only configuration.
- Separate writable state/cache paths.

Document which hardening options are incompatible with `/dev/kvm`, tap devices,
Firecracker jailer, or rootless operation. Fail installation clearly when the
host cannot meet the selected security profile.

## VM isolation

Enforce:

- Firecracker jailer.
- Unique VM directories.
- Unique tap devices.
- Read-only base image.
- Copy-on-write root disk.
- No host mounts.
- No host Docker socket.
- Per-VM process and memory limits.
- Network egress policy.
- DNS policy.
- Guest agent authentication and request limits.

## Secrets

Secret lifecycle:

1. API stores encrypted secret material.
2. Job references allowed secret IDs, not values.
3. Scheduler validates access.
4. Worker receives an encrypted, short-lived delivery.
5. Guest agent injects it into memory or a temporary file.
6. Secret is excluded from logs and snapshots.
7. Cleanup removes temporary files and invalidates the delivery.

Never put secrets in command-line arguments, image layers, environment dumps,
debug artifacts, or worker logs.

## CI safety

- Disable public repository jobs by default.
- Require explicit repository and organization allowlists.
- Use one-time runner registration tokens.
- Run each job in a clean VM.
- Do not pass worker environment or control-plane credentials to jobs.
- Delete runner registration and VM even on cancellation.
- Apply maximum runtime and disk/output limits.
- Make debug retention an explicit, time-limited choice.

## Audit events

Audit:

- Enrollment token created/consumed/revoked.
- Worker connected/disconnected/cordoned/drained/revoked.
- Job submitted/assigned/canceled/retried.
- Secret granted.
- Image published/deprecated.
- CI integration changed.
- Certificate rotated.
- Authorization denied.

Audit records need actor, organization, resource, action, result, trace ID,
source IP where meaningful, and timestamp.
