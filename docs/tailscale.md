# Tailscale Integration

This document describes the Tailscale ("tailnet") integration for Compute. It explains
the architecture, the guarantee we make about not disturbing an operator's existing
Tailscale environment, and how to operate every moving part.

## Why

Compute normally assumes the control-plane host is reachable from anywhere (public IP,
LAN, VPN) and that sandbox IPs are routable on the host network. Neither holds when the
daemon runs on a cloud VM **without a public IP**, or when worker nodes are spread across
networks the operator does not control (home lab + cloud + office).

> **Default model (Service gateway).** The current default is a single-host
> model: only the daemon host joins the tailnet, sandboxes stay on the local
> bridge, and the daemon creates one Tailscale Service per sandbox. See
> [`docs/tailscale-service-gateway.md`](tailscale-service-gateway.md). The
> per-sandbox ephemeral device model described below is the legacy/optional
> path (`legacy_per_sandbox_devices: true`).

A Tailscale tailnet solves both problems:

1. **Control plane reachability** — every machine the operator owns joins the tailnet and
   reaches the daemon via its tailnet IP / MagicDNS name. No port forwarding, no public IP.
2. **Sandbox addresses** — every sandbox can be assigned a real tailnet IP (`100.x.y.z`)
   and a stable MagicDNS name, acting as its "public IP" reachable from every machine the
   operator owns.
3. **Custom domains** — Tailscale *Services* (`svc:<name>`) give each exposed sandbox port
   a stable MagicDNS name (`<service>.<tailnet>.ts.net`) that survives restarts and can be
   re-pointed without touching clients.

## Core design rules

- **Tag everything, touch nothing else.** Every device Compute creates or manages is
  tagged `tag:compute-*` and named with the `compute-` prefix. The integration never
  modifies the operator's policy file, subnet routes, Funnel configuration, existing
  devices, or DNS settings automatically.
- **Ephemeral sandboxes.** Sandbox devices join with *ephemeral* auth keys: when a sandbox
  shuts down, its tailnet node disappears automatically. There is no device litter to clean up.
- **Scoped keys.** Auth keys minted for sandboxes are ephemeral, preauthorized, tagged
  `tag:compute-sandbox`, and short-lived. They are delivered only over the SSH control
  channel that already exists between the daemon host and the guest.
- **Explicit policy snippets, never silent edits.** Where the operator's policy file must
  allow something (e.g. auto-approving sandbox service advertisements), Compute prints the
  exact snippet to add. Applying it is always a manual, reviewed step (`compute tailscale policy`).
- **Fail open.** If Tailscale is unavailable (no binary, no API key, guest without
  connectivity), sandbox creation still succeeds; only tailnet addressing is skipped with
  a clear log/status message.

## Device taxonomy

| Role            | Tag                  | Hostname pattern          | Lifetime            |
|-----------------|----------------------|---------------------------|---------------------|
| Master daemon   | `tag:compute-master` | `compute-master[-<id>]`   | persistent (host)   |
| Worker node     | `tag:compute-node`   | `compute-node-<name>`     | persistent (host)   |
| Sandbox (legacy) | `tag:compute-sandbox`| `compute-<vmid8>`         | ephemeral (per VM)  |

## Component overview

```
tailnet (user-owned)
 ├── compute-master  (tag:compute-master)   ← daemon + GUI + ingress proxy
 │     └── svc:compute-<sandbox>-<id>      ← one Tailscale Service per sandbox (default)
 └── compute-node-X  (tag:compute-node)    ← workers; report tailnet IP as underlay
```

In the legacy model each sandbox also appears as its own ephemeral
`compute-<vmid8>` (`tag:compute-sandbox`) device with a `100.x` address.

### 1. Control plane (`computed`)

Configuration (daemon JSON → `tailscale` section, or env):

```json
{
  "tailscale": {
    "enabled": true,
    "api_key": "tskey-api-...",          // or env COMPUTE_TAILSCALE_API_KEY
    "tailnet": "-",                      // "-" = the API key's default tailnet
    "node_tag": "tag:compute-master",
    "sandbox_tag": "tag:compute-sandbox",
    "hostname_prefix": "compute",
    "key_expiry_seconds": 7776000,       // 90 days for the reusable sandbox key
    "auto_install_guest": true,          // install the tailscale binary in guests on first use
    "default_sandboxes": false,          // Service gateway is the default; no per-VM device
    "service_gateway": {
      "enabled": true,                   // this host is the only tailnet Service host
      "host_tag": "tag:compute-master",
      "service_tag": "tag:compute-sandbox",
      "auto_approve": true,
      "scan_interval_seconds": 30
    }
  }
}
```

See [`docs/tailscale-service-gateway.md`](tailscale-service-gateway.md) for the
gateway fields, enrollment step, and the policy snippet.

Environment overrides: `COMPUTE_TAILSCALE_ENABLED`, `COMPUTE_TAILSCALE_API_KEY`,
`COMPUTE_TAILSCALE_TAILNET`.

Startup behavior:

- The daemon detects the local Tailscale client (`tailscale status --json`). The host
  itself is joined by the operator (see *Setup*); the daemon never runs `tailscale up`
  on the host.
- The daemon records its own tailnet IP and MagicDNS name and surfaces them via
  `GET /api/v1/tailscale/status` and the CLI.
- When a sandbox requests tailnet addressing, the daemon mints an auth key through the
  Tailscale admin API and provisions the guest (below).

### 2. Worker nodes (`compute-worker`)

- The operator joins the worker host to the tailnet with the node tag:
  `sudo tailscale up --advertise-tags=tag:compute-node --hostname=compute-node-<name>`
- The worker detects its local tailnet IP at startup and reports it in
  `WorkerHello`/heartbeats as capabilities `tailscale=true`, `tailscale_ip=100.x.y.z`.
- The daemon stores that IP as the node's `underlay_ip` (previously hardcoded to
  `127.0.0.1`), so scheduling, SSH relay and audit records carry the real address.
- Connectivity is pure configuration: run the worker with
  `--control-plane http://<master-magicdns>:18080` (or the master's `100.x` IP).

### 3. Sandboxes (address assignment)

**Default (Service gateway):** a sandbox created with default settings stays on
the local bridge and does **not** join the tailnet. The daemon creates one
Tailscale Service for it on the API host and proxies its listening TCP ports;
the Service VIP plus MagicDNS name is the sandbox's stable address. Nothing runs
inside the guest. Details: [`docs/tailscale-service-gateway.md`](tailscale-service-gateway.md).

**Legacy (per-sandbox device):** when the daemon sets
`tailscale.legacy_per_sandbox_devices: true` (or a sandbox is created with
`tailscale: true`), after boot the daemon provisions the guest over its existing
SSH control channel:

1. Mint an ephemeral, preauth, tagged auth key (`tag:compute-sandbox`) via the admin API.
2. Ensure the `tailscale` binary exists in the guest; if `auto_install_guest` is enabled
   and the binary is missing, install the static binaries over SSH.
3. Run `tailscale up --authkey=<key> --hostname=compute-<vmid8>
   --advertise-tags=tag:compute-sandbox --accept-dns=false --ssh=false` in the guest.
4. Read back `tailscale ip -4` and the MagicDNS name; store them on the VM record
   (`tailnet_ip`, `tailnet_dns`, `tailscale_device_id`).

Result: the sandbox is reachable **directly** at `100.x.y.z` (and
`compute-<vmid8>.<tailnet>.ts.net`) from any machine in the tailnet, subject to the
operator's ACLs. When the sandbox stops, the ephemeral node evaporates from the tailnet.

Storage/DB: `vms` gains `tailscale_enabled`, `tailnet_ip`, `tailnet_dns`,
`tailscale_device_id` (forward-compatible column migration). The API/CLI expose these
fields on sandbox objects.

### 4. Tailscale Services (custom domains over MagicDNS)

Each exposed sandbox port becomes a *Tailscale Service*, giving it a stable custom domain
on the tailnet:

- Service naming convention: `svc:compute-<vmid8>-<port>` (e.g. `svc:compute-a1b2c3d4-8080`)
- The guest advertises the endpoint itself, so the service survives VM restarts and is
  routed by Tailscale:
  `tailscale serve --service=svc:compute-a1b2c3d4-8080 --bg tcp:8080 tcp://127.0.0.1:8080`
- The resolved MagicDNS name (`compute-a1b2c3d4-8080.<tailnet>.ts.net`) is recorded as the
  port forward's `public_url`, so CLI/GUI links work like the normal public URLs do.

Required policy (operator applies manually via `compute tailscale policy`):

```jsonc
// autoApprovers: sandboxes may publish and approve their own tagged services
"autoApprovers": {
  "services": {
    "tag:compute-sandbox": ["tag:compute-sandbox"]
  }
},
// grants: who in the tailnet may reach compute services
{
  "src": ["autogroup:member"],
  "dst": ["tag:compute-sandbox"],
  "ip": ["*"]
}
```

The daemon API/CLI can list existing Services (`compute tailscale services`) for
visibility, but service *definition* happens through the guest's `tailscale serve`, which
keeps the admin-API surface minimal.

### 5. Never disturbs the operator's tailnet

Guarantees, by construction:

- Compute only creates **tagged, ephemeral** nodes with `compute-` hostnames; it never
  touches user devices, never deletes anything without an explicit `--confirm`, and
  cleanup commands filter strictly on `tag:compute-sandbox`.
- Compute never writes to the policy file. `compute tailscale policy` only *prints* the
  recommended snippet for review.
- No subnet routes are advertised; no Funnel configuration is changed; no DERP or DNS
  settings are modified.
- If the operator later removes Compute, every trace is one command:
  `compute tailscale cleanup --confirm` (deletes only `tag:compute-sandbox` devices),
  plus removing the two host devices (`compute-master`, `compute-node-*`).

## Setup guide

### Master host (cloud VM without public IP)

```bash
# 1. Join the tailnet (operator step; once)
curl -fsSL https://tailscale.com/install.sh | sh
sudo tailscale up --advertise-tags=tag:compute-master --hostname=compute-master

# 2. Create an API access token in the Tailscale admin console, then:
sudo COMPUTE_TAILSCALE_ENABLED=true COMPUTE_TAILSCALE_API_KEY=tskey-api-... computed --listen 0.0.0.0:18080

# 3. Verify + print the URL workers should use
compute tailscale status
```

### Worker / local machine

```bash
sudo tailscale up --advertise-tags=tag:compute-node --hostname=compute-node-lab1
compute-worker --control-plane http://compute-master.your-tailnet.ts.net:18080 \
               --token <worker-token> --name lab1
```

### Sandboxes

```bash
compute sandbox create dev-box --tailscale
compute sandbox info dev-box        # shows tailnet_ip + tailnet_dns
# From any owned machine:
ssh compute@100.101.102.103
```

Authentication over the tailnet still goes to the guest's sshd, so the same
keys and optional password apply. To authorize your key on every tailnet
sandbox without selecting it each time:

```bash
compute keys add --name laptop --key-file ~/.ssh/id_ed25519.pub --all
```

See `docs/dns-ssh.md` for per-sandbox keys, password authentication, and how
account-wide keys are propagated to running and stopped sandboxes.

### ACL recommendations

Run `compute tailscale policy` to print the recommended, fully-scoped snippet
(`autoApprovers` for tagged sandboxes, grants limited to `tag:compute-*`). Review and
paste it into the tailnet policy file yourself.

## API surface

| Endpoint | Description |
|---|---|
| `GET /api/v1/tailscale/status` | Local tailnet state, master MagicDNS/IP, configured tags, recommended worker URL |
| `POST /api/v1/sandboxes` body `{"tailscale": true}` | Sandbox requests tailnet addressing |
| Sandbox object fields | `tailscale_enabled`, `tailnet_ip`, `tailnet_dns`, `tailscale_device_id` |

## CLI

| Command | Description |
|---|---|
| `compute tailscale status` | Local + master tailnet status, sandbox summary, recommended URLs |
| `compute tailscale policy` | Print the recommended policy-file snippet |
| `compute tailscale cleanup [--confirm]` | Remove stale `tag:compute-sandbox` devices (dry-run default) |
| `compute sandbox create --tailscale` | Create a sandbox with tailnet addressing |

## Testing & verification

- `internal/tailscale` admin client is unit-tested against a mock HTTP server.
- Policy generation is unit-tested.
- Provisioning is exercised through a pluggable runner in tests (no real tailscaled needed).
- End-to-end verification requires a real tailnet: see `scripts/test-all-features.sh` on
  the lab host; Tailscale-specific scenarios are manual (auth keys cannot be minted in CI).

## Lab deployment (compute-lab)

The API key never lives in tracked `config/daemon.json`. Supply it through the
environment:

```bash
# On the lab, /home/rashid/on-demand-vms/.env (mode 600, untracked)
COMPUTE_TAILSCALE_API_KEY=tskey-api-...
COMPUTE_TAILSCALE_ENABLED=true
COMPUTE_TAILSCALE_TAILNET=-
```

- `docker-compose.yml` reads these values for the `computed` container.
- `config/systemd/computed.service` uses `EnvironmentFile=-.../.env` for the
  legacy systemd deployment.
- `scripts/deploy.sh` excludes `.env`/`.env.*` from its `--delete-after` rsync,
  so the lab secret survives deploys.
- `config/daemon.json` enables the integration and
  `default_sandboxes: true`; the key is intentionally empty there.

Minimum policy for a tagged client (for example the `tag:t3-vm` runner) to
reach compute devices, in addition to `tagOwners` for `tag:compute-master` and
`tag:compute-sandbox`:

```jsonc
{
  "src": ["tag:t3-vm"],
  "dst": ["autogroup:member", "tag:compute-master", "tag:compute-sandbox"],
  "ip": ["*"],
}
```

## Tailscale Services (implementation)

On port-forward creation for a TCP port on a tailnet-enabled sandbox, the
daemon:

1. Defines `svc:compute-<vmid8>-<port>` through the admin API (ports
   `tcp:443`, tagged `tag:compute-sandbox`).
2. Runs `tailscale serve --service=svc:<name> --https=443 --yes
   http://127.0.0.1:<guestPort>` in the guest over SSH.
3. Records `https://<name>.<tailnet>.ts.net` as `port_forwards.public_url`.

Deleting the port forward clears the guest serve configuration and deletes the
Service. Service hosts are auto-approved when the policy contains the
`autoApprovers.services` snippet printed by `compute tailscale policy`.
Services are TCP-only and assume an HTTP(S) backend; UDP forwards keep the
host-local URL.

## Known limitations / future work

- **UDP exposure** through Services (Tailscale currently TCP-only; L3 `--tun`
  endpoints on Linux are possible later).
- **tsnet (embedded userspace Tailscale) mode** for hosts where the operator
  cannot install tailscaled. Concrete plan:
  1. Add `tailscale.tsnet` config (`enabled`, `state_dir`, `hostname`, `tags`)
     and an `internal/tailscale/tsnet` package wrapping `tsnet.Server`.
  2. Run the daemon control-plane listener through `tsnet.Listen("tcp", ":18080")`
     alongside the normal listener, so no host `tailscaled` is required.
  3. For sandboxes, publish ports with `tsnet.Server.ListenService` from a small
     helper binary injected into the guest image, replacing the `tailscale`
     CLI/serve path.
  4. Keep host-mode (`tailscaled` + CLI) as the default; selection is per-host
     config and must not change device naming/tagging.
  This is not implemented yet; the issue item stays open.
- Automatic per-sandbox service provisioning is orchestrated via guest
  `tailscale serve`; a master-side orchestrator for services on worker-hosted
  sandboxes lands with remote sandbox scheduling.

## Sandbox DNS names

Sandboxes can additionally be reached by stable hostnames
(`<sandbox>.<username>.<base-domain>`) served by the daemon's built-in DNS
responder **when the base domain is a zone the operator controls**. See
`docs/dns-ssh.md` for the full setup.

Tailscale's own `*.ts.net` zone is owned by MagicDNS: split DNS for a
`<something>.<tailnet>.ts.net` subdomain is accepted by the admin API but
MagicDNS answers authoritatively (NXDOMAIN for names it does not know), so
per-user subdomains under `ts.net` are not resolvable from tailnet clients. Use
the per-sandbox MagicDNS device name (`tailnet_dns`) or choose a custom domain
for the daemon's DNS responder.
