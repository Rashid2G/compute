# Security Policy

## Security Architecture

Compute is designed for running untrusted or multi-tenant workloads inside isolated Firecracker microVMs on self-hosted Linux hardware with defense-in-depth across virtualization, networking, data storage, and the control-plane HTTP API:

### 1. Virtualization & Jailer Isolation
- **Hardware Virtualization**: Every sandbox runs in its own Firecracker microVM backed by Linux KVM with a dedicated kernel, root filesystem, and virtualized devices — never a shared host kernel.
- **Firecracker Jailer**: Production deployments can enable the Firecracker Jailer (`COMPUTE_JAILER_ENABLED=true`) to confine each VMM inside a minimal `chroot`, dedicated PID, mount, and network namespaces, and unprivileged UID/GID. The daemon manages detached process lifecycles, persistent hardlink and bind-mount disks, and deterministic per-VM UID/GID allocation.
- **Resource Limits (cgroups v2)**: Per-VM CPU, memory, and PID limits are enforced via host cgroups v2 (`COMPUTE_CGROUP_ENFORCE=true`).
- **Ephemeral MicroVM Image Builds**: Custom image builds execute exclusively within isolated, ephemeral microVM sandboxes; legacy host chroot and Docker daemon builds have been removed.

### 2. Network Isolation & Egress Firewall
- **Default Egress Firewall**: Outbound guest traffic through the bridge is governed by an egress firewall that blocks access to Cloud IMDS (`169.254.0.0/16`), RFC1918 private subnets (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`), CGNAT (`100.64.0.0/10`), loopback (`127.0.0.0/8`), IPv6 link-local/ULA, and host bridge daemon ports.
- **Network Modes**: Sandboxes support `account` (shared account subnet communication) and `isolated` (strictly isolated sandbox-only traffic via per-sandbox firewall rules) modes, along with per-VM and bridge-wide `no-internet` isolation.

### 3. Data Protection, Encryption & RBAC
- **Envelope Encryption**: Secrets at rest (webhook secrets, environment credentials) are encrypted with AES-256-GCM using a local key (`master.key`).
- **Hashed Tokens & API Keys**: API keys (`cmp_...`) and worker enrollment tokens are hashed with SHA-256 prior to database storage and displayed only once at creation.
- **Filesystem Permissions**: Database files are locked with `0600` permissions and directories with `0700`. Control Unix domain sockets enforce restrictive `0660` permissions (owner and group only, never world-writable) under `0750` parent directories.
- **Tenant Profiles**: Tenant-scoped `EnvironmentProfile` manages credential injection safely with shell-injection-safe cloning.

### 4. HTTP Control-Plane Hardening
- **Server Timeouts**: Control plane and serve listeners configure `ReadHeaderTimeout: 10s`, `IdleTimeout: 120s`, and `MaxHeaderBytes: 1 MiB` to protect against slowloris and slow-client exhaustion attacks without interrupting streaming APIs.
- **Request Body Limits**: API routes enforce an automatic body limit via `http.MaxBytesReader` (default 4 MiB for JSON payloads; 512 MiB cap for file uploads, volumes, snapshots, and image imports), cleanly returning HTTP 413 (`Request Entity Too Large`) when exceeded.
- **Session-Bound CSRF Protection**: Cookie-authenticated mutating endpoints (`POST`, `PUT`, `PATCH`, `DELETE`) require an `X-CSRF-Token` header verified in constant time against the active session's stored CSRF hash. A non-HttpOnly `compute_csrf` cookie is set alongside the session cookie for browser clients.
- **Strict WebSocket Origin Validation**: WebSocket endpoints strictly validate the `Origin` header against `r.Host`, `COMPUTE_PUBLIC_URL`, and configured serve domains, rejecting cross-origin browser upgrade requests with HTTP 403 before upgrade. Non-browser CLI and worker connections without an `Origin` header are permitted.
- **Per-IP Token-Bucket Rate Limiting**: Concurrent-safe token-bucket rate limiting enforces strict thresholds on authentication routes (20 req/min, burst 10) and general API endpoints (600 req/min, burst 120), returning HTTP 429 (`Too Many Requests`) with `Retry-After`. `X-Forwarded-For` and `X-Real-IP` headers are only trusted from configured `COMPUTE_TRUSTED_PROXIES` CIDRs.
- **Dial-Time Webhook SSRF Protection**: Outbound webhook delivery utilizes a custom transport that resolves DNS at dial time and verifies every resolved IP address before connecting, blocking loopback, private, link-local, Cloud IMDS, CGNAT, and multicast destinations. HTTP redirects are validated and delivery statistics are updated atomically.

---

## Production Hardening Checklist

Before exposing Compute on a shared network or public interface:

1. **Replace default credentials**: set a strong random `COMPUTE_ADMIN_PASSWORD` (e.g., `openssl rand -base64 32`) in `.env` with `chmod 600 .env`, and remove or rotate `COMPUTE_ADMIN_SECRET`.
2. **Keep public signup disabled**: verify `COMPUTE_SIGNUP_ENABLED=false` unless you intentionally want open self-registration.
3. **Terminate TLS**: place `computed` behind a TLS-terminating reverse proxy (Caddy, nginx, Traefik) or configure `COMPUTE_TLS_CERT_FILE` and `COMPUTE_TLS_KEY_FILE`.
4. **Configure Trusted Proxies**: when operating behind a reverse proxy or load balancer, set `COMPUTE_TRUSTED_PROXIES` to the proxy's IP/CIDR so rate limiting and audit logs see true client IPs.
5. **Enable Jailer & cgroups**: set `COMPUTE_JAILER_ENABLED=true` and `COMPUTE_CGROUP_ENFORCE=true` on bare-metal or dedicated hosts.
6. **Restrict bind address**: set `COMPUTE_BIND_ADDRESS=127.0.0.1` when fronting the daemon with a reverse proxy or VPN (such as Tailscale).
7. **Protect the internal SSH key**: `<data>/images/ssh/id_ed25519` grants administrative automation access to guests; encrypt backups containing `<data>`.
8. **Disable dev-only worker local exec**: keep `COMPUTE_WORKER_ALLOW_LOCAL_EXEC=false` in all non-development environments.

---

## Reporting a Vulnerability

If you discover a security vulnerability in Compute, please **do not** open a public GitHub issue with exploit details.

Instead, report it privately via GitHub Security Advisories (**Security → Advisories → Report a vulnerability** on the repository) or contact the maintainers directly with:

- A clear description of the vulnerability and affected component (`computed`, `compute-worker`, `compute` CLI, Web UI, or SDK)
- Steps to reproduce or a minimal proof-of-concept
- Potential impact and any suggested remediation

We will acknowledge reports within 48 hours and work with you on a coordinated fix and release advisory.
