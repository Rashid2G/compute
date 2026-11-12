# Tailscale Service gateway (single-host routing)

Status: implemented (opt-in via `tailscale.service_gateway.enabled`).

This document describes the routing model where **only the machine running the
daemon joins the tailnet**. Sandboxes stay on the host-local bridge
(`172.16.0.0/16`); the daemon creates one [Tailscale Service](https://tailscale.com/kb/1552/tailscale-services)
per sandbox, giving each sandbox a stable VIP and MagicDNS name without
enrolling the sandbox itself.

## Why

The previous model joined every sandbox to the tailnet as an ephemeral device.
That works, but it means:

- one tailnet device per sandbox (device limits, churn, cleanup),
- a tailscale daemon and auth key in every guest,
- an address that disappears when the sandbox stops.

The gateway model keeps the user-facing promise — every sandbox has a stable,
routable address — while moving all tailnet state to the one enrolled host.

## Topology

```
tailnet
  └── compute-lab (tag:compute-master)          ← the only Compute tailnet device
        ├── Service svc:compute-dev-box-<id>     VIP 100.x.y.z, tag:compute-sandbox
        │     ├── tcp:22  → gateway listener → 172.16.0.2:22   (SSH)
        │     └── tcp:80  → gateway listener → 172.16.0.2:80   (HTTP)
        └── Service svc:compute-api-<id>         VIP 100.x.y.z
              └── tcp:22  → gateway listener → 172.16.0.3:22

local bridge 172.16.0.0/16
  ├── 172.16.0.2  sandbox dev-box
  └── 172.16.0.3  sandbox api
```

For each running sandbox the daemon:

1. scans the guest for TCP listeners over its existing SSH control channel
   (`ss -ltn`, falling back to `netstat -ltn`),
2. defines one Service `svc:compute-<sandbox>-<short-id>` with
   `tcp:<port>` for every exposed port (SSH is always included),
3. allocates a loopback listener in the daemon's network namespace for each
   port; the listener proxies to `<guest-ip>:<port>`,
4. runs `tailscale serve --service=svc:... --tcp=<port> tcp://<daemon-ip>:<local-port>`
   against the host's tailscaled, so Service traffic is forwarded to the
   gateway,
5. reads the Service's VIP back from the admin API and answers the daemon's DNS
   zone with it, so `<sandbox>.<username>.<zone>` resolves to the VIP,
6. deletes the Service and closes the listeners when the sandbox stops or is
   deleted.

Because the Service keeps a stable VIP, a sandbox address is elastic: creating
a sandbox creates an address, deleting it deletes the address, and the
operator's tailnet device list never grows.

## Configuration

```jsonc
// config/daemon.json
{
  "tailscale": {
    "enabled": true,
    "api_key": "",                       // prefer COMPUTE_TAILSCALE_API_KEY
    "tailnet": "-",
    "default_sandboxes": false,          // legacy per-sandbox devices off
    "service_gateway": {
      "enabled": true,
      "host_tag": "tag:compute-master",  // tag required of the API host
      "service_tag": "tag:compute-sandbox", // tag applied to created Services
      "auto_approve": true,              // approve this host over the admin API
      "scan_interval_seconds": 30,       // listening-port reconcile interval
      "expose_loopback": false,          // do not publish 127.0.0.1-only listeners
      "exclude_ports": [],               // ports that are never exposed
      "target_address": "",              // auto: this process's non-loopback IP
      "tailscale_binary": "tailscale",
      "tailscale_socket": "/var/run/tailscale/tailscaled.sock",
      "ssh_user": "compute",
      "service_prefix": "compute"
    }
  }
}
```

Notes:

- `target_address` is the address tailscaled dials. In the docker-compose
  deployment tailscaled runs on the host and the daemon is a container, so the
  auto-detected value is the container's bridge IP (for example `172.18.0.2`);
  the host has a connected route to it. On a bare-host deployment the LAN
  address is used.
- `expose_loopback` is off by default. A process bound to `127.0.0.1` inside the
  sandbox is intentionally local and is not published.
- `exclude_ports` is the place to block ports such as `25`.

## One-time host enrollment

Tailscale only allows **tagged** devices to act as Service hosts. The daemon
does not tag its host automatically; run:

```bash
compute tailscale enroll            # preview current/desired tags
compute tailscale enroll --confirm  # apply tag:compute-master
```

The command uses `tailscale status --json` to find the local device and
`POST /api/v2/device/{id}/tags` through the configured API key. It preserves
existing tags. This changes the host's tailnet identity from user-owned to
tagged; the grants in `docs/tailscale.md` cover member access, but review your
policy first.

After enrollment, Service host advertisements are auto-approved through
`POST /api/v2/tailnet/{tailnet}/services/{name}/device/{id}/approved` when
`auto_approve` is true. If approval fails, the log explains what to approve in
the admin console, or add the policy snippet below.

## Policy

The snippet printed by `compute tailscale policy` now includes the gateway
case. Minimum additions:

```jsonc
"tagOwners": {
  "tag:compute-master":  ["autogroup:admin"],
  "tag:compute-sandbox": ["tag:compute-master", "autogroup:admin"]
},
"autoApprovers": {
  "services": {
    // services tagged compute-sandbox may be hosted by the API host
    "tag:compute-sandbox": ["tag:compute-master", "tag:compute-sandbox"]
  }
},
{
  // who may reach the Services
  "src": ["autogroup:member"],
  "dst": ["tag:compute-sandbox"],
  "ip": ["*"]
}
```

Services are granted by tag, so new sandboxes need no policy changes.
Tagged clients (for example `tag:t3-vm`) need their own grant to
`tag:compute-sandbox`, exactly like the existing device-mode grant.

## Addresses and names

| Address | Form | Notes |
| --- | --- | --- |
| Service MagicDNS | `<service>.<tailnet>.ts.net` | stable per sandbox; `<service>` = `compute-<sandbox>-<short-id>` |
| Service VIP | `100.x.y.z` | assigned by Tailscale; returned by the admin API |
| Nested DNS name | `<sandbox>.<username>.<zone>` | answered by the daemon's DNS responder with the VIP first, guest IP second |

All endpoints are layer-4 TCP forwarders, so SSH, HTTP, WebSockets, databases,
and arbitrary protocols pass through unchanged. TLS is terminated by the
sandbox's own server; Tailscale does not issue a certificate for these ports.

## API and CLI

| Surface | Description |
| --- | --- |
| `GET /api/v1/tailscale/status` | now includes `host_tag`, `service_gateway`, and `gateway_services` |
| `POST /api/v1/tailscale/enroll?confirm=true` | tag the daemon host as the Service host (admin only) |
| `compute tailscale status` | shows the gateway target, services, VIPs, ports, last error |
| `compute tailscale enroll [--confirm]` | preview/apply the host tag |

Every port forward creation/deletion kicks an immediate reconcile; the periodic
scan (default 30s) catches servers started or stopped inside the guest.

## Deployment notes

- `Dockerfile.daemon` installs the pinned Tailscale CLI (`TAILSCALE_VERSION`);
  the daemon only needs the client, not `tailscaled`.
- `docker-compose.yml` mounts `/var/run/tailscale` from the host and supplies
  `COMPUTE_TAILSCALE_API_KEY` through the untracked `.env` file.
- The host must run `tailscaled` and be connected to the tailnet.
- The daemon needs no new secrets beyond the existing API key.

## Failure behavior

- Admin API or `tailscale serve` failures are logged per sandbox and never
  affect VM lifecycle; the next reconcile retries.
- A stopped or deleted sandbox removes its Service and endpoints. Stale
  Services from a crashed daemon are reset on the first reconcile after
  restart (the daemon clears the service's serve config before re-advertising).
- If the host is not enrolled/tagged, Services are created but stay in
  `Pending approval`; `compute tailscale status` shows the gateway error.
- If a guest port scan fails, the last successful port set is kept (or SSH
  only) so a transient SSH error does not tear down the Service.

## Migration from per-sandbox devices

1. Deploy the gateway build and set `service_gateway.enabled: true` and
   `default_sandboxes: false`.
2. Run `compute tailscale enroll --confirm` once.
3. Existing ephemeral sandbox devices disappear when those sandboxes stop, or
   remove them with `compute tailscale cleanup --confirm`.
4. The legacy path remains available with
   `legacy_per_sandbox_devices: true` and `default_sandboxes: true`, but is not
   used by default.
