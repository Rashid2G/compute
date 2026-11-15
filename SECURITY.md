# Security Policy

## Security Architecture

Compute is designed for running untrusted or multi-tenant workloads inside isolated Firecracker microVMs on self-hosted Linux hardware:

- **Hardware Virtualization**: Every sandbox runs in its own Firecracker microVM backed by Linux KVM with a dedicated kernel, root filesystem, and virtualized devices — never a shared host kernel.
- **Jailer Isolation**: Production deployments can enable the Firecracker Jailer (`COMPUTE_JAILER_ENABLED=true`) to confine each VMM inside a minimal `chroot`, dedicated PID/mount/network namespaces, and unprivileged UID/GID.
- **Resource Limits (cgroups v2)**: Per-VM CPU, memory, and PID limits are enforced via host cgroups v2 (`COMPUTE_CGROUP_ENFORCE=true`).
- **Network Isolation**: Sandboxes support `account` (shared account subnet communication) and `isolated` (strictly isolated sandbox-only traffic via per-sandbox `iptables` rules) modes.
- **Authentication & Bootstrap Safety**:
  - Control-plane authentication is enabled by default (`COMPUTE_AUTH_ENABLED=true`).
  - Public self-registration is disabled by default (`COMPUTE_SIGNUP_ENABLED=false`); administrators provision users explicitly via the Web Dashboard or `POST /api/v1/users`.
  - The daemon refuses to start with authentication enabled unless `COMPUTE_ADMIN_PASSWORD` or `COMPUTE_ADMIN_SECRET` is configured (or an existing installation credential exists).
  - API keys use a `cmp_...` token format, are hashed before storage, and are displayed only once at creation time.
- **Opt-in Guest SSH Password Auth**: Guest `sshd` defaults to `PasswordAuthentication no` and locked passwords; only embedded account/sandbox SSH public keys and the daemon's internal automation key are authorized unless an explicit `--ssh-password` is supplied.

## Production Hardening Checklist

Before exposing Compute on a shared network or public interface:

1. **Replace default credentials**: set a strong random `COMPUTE_ADMIN_PASSWORD` (e.g., `openssl rand -base64 32`) in `.env` with `chmod 600 .env`, and remove or rotate `COMPUTE_ADMIN_SECRET`.
2. **Keep public signup disabled**: verify `COMPUTE_SIGNUP_ENABLED=false` unless you intentionally want open self-registration.
3. **Terminate TLS**: place `computed` behind a TLS-terminating reverse proxy (Caddy, nginx, Traefik) or configure `COMPUTE_TLS_CERT_FILE` and `COMPUTE_TLS_KEY_FILE`.
4. **Enable Jailer & cgroups**: set `COMPUTE_JAILER_ENABLED=true` and `COMPUTE_CGROUP_ENFORCE=true` on bare-metal or dedicated hosts.
5. **Restrict bind address**: set `COMPUTE_BIND_ADDRESS=127.0.0.1` when fronting the daemon with a reverse proxy or VPN (such as Tailscale).
6. **Protect the internal SSH key**: `<data>/images/ssh/id_ed25519` grants administrative automation access to guests; encrypt backups containing `<data>`.
7. **Disable dev-only worker local exec**: keep `COMPUTE_WORKER_ALLOW_LOCAL_EXEC=false` in all non-development environments.

## Reporting a Vulnerability

If you discover a security vulnerability in Compute, please **do not** open a public GitHub issue with exploit details.

Instead, report it privately via GitHub Security Advisories (**Security → Advisories → Report a vulnerability** on the repository) or contact the maintainers directly with:

- A clear description of the vulnerability and affected component (`computed`, `compute-worker`, `compute` CLI, Web UI, or SDK)
- Steps to reproduce or a minimal proof-of-concept
- Potential impact and any suggested remediation

We will acknowledge reports within 48 hours and work with you on a coordinated fix and release advisory.
