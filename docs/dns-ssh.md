# Sandbox DNS names and SSH by hostname

Every sandbox can be given a stable, routable hostname of the form

```
<sandbox>.<username>.<base-domain>
```

For example, with the base domain `sandboxes.example.com`, a sandbox
named `myvm` owned by user `alice` is reachable at:

```
ssh compute@myvm.alice.sandboxes.example.com
```

> **Note:** `*.ts.net` domains are **not resolvable as delegated zones from tailnet clients**:
> MagicDNS owns `*.ts.net` and answers it authoritatively. See the delegation section below
> and `docs/domain-options.md`. Use a custom/private zone (e.g. `sbx.internal` or `sandboxes.example.com`)
> for the per-user subdomain scheme.

When the Tailscale Service gateway is enabled, each sandbox name is answered with the
sandbox Service's VIP first and the guest IP second. Tailnet clients reach the
VIP through the Service host without any subnet route; LAN clients fall back
to the guest IP.

The base domain is **generic** — it works for any zone you can delegate (a
custom/private zone via Tailscale split DNS, a domain at any DNS provider, or
a LAN zone). It is not tied to Tailscale.

## Why routing happens in DNS

SSH connections carry no hostname — unlike HTTP there is no `Host:` header or
TLS SNI — so a TCP-level listener cannot tell which subdomain an SSH client
connected to. The wildcard dispatch therefore happens one layer lower: the
daemon runs an **authoritative DNS server** that answers sandbox names with
the sandbox's *own address* (tailnet IP first, guest IP second), and the
network routes the connection directly to the sandbox. Plain `ssh` and every
other existing SSH method (CLI `compute ssh`, the GUI terminal, direct guest
IP) keep working unchanged.

## Configuration

```jsonc
// config/daemon.json
{
  "dns": {
    "enabled": true,
    "listen": ":53",                              // UDP + TCP
    "base_domain": "sandboxes.example.com",       // falls back to network.base_domain
    "extra_base_domains": []                      // optional additional zones
  },
  "server": {
    "edge_listen": ":80"   // default ":80" while DNS is enabled; "-" disables
  }
}
```

### What the DNS server answers

Inside the zone:

| Name                        | Answer                                                        |
| --------------------------- | ------------------------------------------------------------- |
| `<zone>` (apex)             | the host (tailnet IP when joined, else primary outbound IP)   |
| `api.<zone>`                | the host (reserved for the API)                               |
| `gui.<zone>`, `www.<zone>`  | the host (reserved for the GUI)                               |
| `<sandbox>.<username>.<zone>` | the sandbox: tailnet IP first, guest IP second              |

Anything else inside the zone is `NXDOMAIN`; queries outside the zone are
forwarded to the system resolver, so the daemon can also be used as a
catch-all nameserver.

Notes:

- Names resolve regardless of sandbox state (a stopped sandbox answers with
  its address and the connection simply fails until it is started).
- Sandboxes in `dhcp`/bridged mode without a known IP answer `NXDOMAIN`.
- Legacy sandbox names that predate DNS-safe validation are sanitized
  (lowercase, invalid characters → `-`) at lookup time, so old sandboxes stay
  reachable.
- Stopped/unknown first addresses fail fast ("network unreachable"), letting
  OpenSSH fall through to the next address.

## Making the zone reachable

The zone needs to be *delegated* to the daemon so clients resolve the names.
Pick whichever matches how the daemon is served (see `docs/domain-options.md`
for a tested comparison of zone choices):

- **Tailscale (custom zone)**: in the
  [admin console](https://login.tailscale.com/admin/dns) add a *custom
  nameserver* with the daemon host's tailnet IP and tick **Restrict to search
  domain** for a zone you control (for example `sandboxes.example.com`).
  Split DNS works for arbitrary zones, but **not for subdomains of the
  tailnet's own `ts.net` zone**: MagicDNS answers those authoritatively
  (`NXDOMAIN` for names it does not know), so a
  `<host>.<tailnet>.ts.net` base domain cannot be delegated this way.
  Use a custom/private zone for per-user subdomains, or per-sandbox MagicDNS
  device names (`<sandbox>-<username>.<tailnet>.ts.net`).
- **Any DNS provider**: create an `NS` record for the base domain pointing at
  the daemon host (plus the needed `A`/glue records).
- **Same LAN**: point clients' resolver at the daemon host, or add the daemon
  host as a nameserver in your local DNS server for the zone.

For non-tailnet clients to reach sandbox *guest IPs* directly, the guest
subnet (default `172.16.0.0/16`) must be routed to the daemon host (a static
route on the client's router, or a Tailscale subnet route advertised by the
host). Sandboxes that joined the tailnet are reachable by their tailnet IP
without any route.

## HTTP: reserved labels, wildcard ingress, port 80

GUI and API are HTTP, so for those the wildcard *is* handled by a router that
reads the subdomain — the built-in edge listener (port 80 by default) plus the
sandbox ingress proxy:

| Host                                  | Served by                          |
| ------------------------------------- | ---------------------------------- |
| `<zone>` apex, `gui.<zone>`, `www.<zone>` | the daemon (Web GUI)           |
| `api.<zone>`                          | the daemon (API)                   |
| `<vm>[-<port>][.<username>].<zone>`   | sandbox ingress (guest HTTP/WS)    |

Ingress forms (all of these still work):

```
8080-myvm.<zone>            # legacy single-label form
myvm-3000.<zone>
myvm.alice.<zone>           # tenant wildcard form, guest port 80
8080-myvm.alice.<zone>
myvm-8080.alice.<zone>
```

The apex and reserved labels are intercepted before the ingress so they can
never collide with a sandbox, and `api.<zone>` is reserved for the API even
when the edge is disabled (on the daemon's own port the mux serves both).

## Serving apps from sandboxes (one origin per exposure)

Path-prefix previews (`/preview/<sandbox>/<port>/`) cannot be made transparent
for arbitrary apps: redirects, `history.pushState`, absolute asset imports,
cookies and service workers all assume the app owns its origin. Compute instead
gives **every exposure its own origin**, so the app sees `/` as its root and no
response rewriting is involved.

```bash
# Public: anyone with the URL can reach the app
compute expose web 8080

# Private: only the owning tenant (browser session or short-lived token)
compute expose web 8080 --private

# Preferred, DNS-safe label (made unique): <name>.<serve-domain>
compute expose web 8080 --subdomain my-app

compute ports web        # list exposures and their URLs
compute unexpose web 8080
```

### Two transports

| Mode | URL | When it is selected |
| --- | --- | --- |
| **Subdomain** | `https://<slug>.<serve-domain>/` | `COMPUTE_SERVE_DOMAIN` is set, or the API binds loopback (then the domain defaults to `localhost`) |
| **Port** | `https://<daemon-host>:<serve-port>/` | `COMPUTE_PUBLIC_URL` points at a non-loopback host, or the API binds a non-loopback address without a serve domain |

Subdomain mode is the fully isolated option (per-origin cookies and storage).
Port mode is the no-DNS fallback for bare public IPs and tailnets; browsers
treat a distinct port as a distinct origin for `localStorage`, `sessionStorage`,
service workers and CORS, though **cookies are still host-scoped** (a cookie
set by one port is sent to other ports on the same host).

**Localhost needs no setup.** Browsers resolve `*.localhost` to loopback and
treat it as a secure context, so a loopback daemon on `:18080` publishes
`http://web-8080.localhost:18080/` with no DNS or certificate work.

**Public domain.** Point a wildcard record at the daemon and configure the
domain and a wildcard certificate:

```bash
# DNS: *.serves.example.com  A/AAAA  <daemon public IP>
COMPUTE_SERVE_DOMAIN=serves.example.com
COMPUTE_PUBLIC_URL=https://serves.example.com
# Terminate TLS at the daemon (wildcard cert) or in front of it (Caddy/nginx).
COMPUTE_SERVE_TLS_CERT_FILE=/etc/compute/wildcard.pem
COMPUTE_SERVE_TLS_KEY_FILE=/etc/compute/wildcard.key
```

**Tailnet / bare IP (port mode).** The daemon listens on one port per exposure
from `COMPUTE_SERVE_PORT_START..COMPUTE_SERVE_PORT_END` (default `18400-18500`)
on `COMPUTE_SERVE_BIND` (default `127.0.0.1`). Port-mode URLs use `http://`
unless `COMPUTE_SERVE_TLS_CERT_FILE` / `_KEY_FILE` are configured.

```bash
# Tailnet-only raw TCP mapping (any port; traffic stays inside WireGuard).
# -> http://compute-lab.tailb1be99.ts.net:18400/
tailscale serve --bg --tcp=18400 tcp://127.0.0.1:18400

# HTTPS on the three ports Tailscale can terminate (443/8443/10000):
# -> https://compute-lab.tailb1be99.ts.net:8443/
tailscale serve --bg --https=8443 http://127.0.0.1:18400
```

For HTTPS on arbitrary ports, terminate TLS in the daemon with a certificate
for the node name (`tailscale cert <node>.<tailnet>.ts.net`, then point
`COMPUTE_SERVE_TLS_CERT_FILE` / `_KEY_FILE` at it) and map the port with
`tailscale serve --tcp`. Or bind `COMPUTE_SERVE_BIND=0.0.0.0` and expose the
range directly on a public IP. Port-mode listeners also work for sandboxes on
remote workers: the daemon proxies through the existing outbound worker tunnel.

### Access control

- `public` exposures are reachable by anyone with the URL.
- `private` exposures accept:
  - the daemon session cookie or `Authorization: Bearer cmp_...` when the
    client can send it (port mode shares the host, so the session cookie is
    sent automatically);
  - a short-lived signed serve token. Browsers are bounced through
    `GET /api/v1/serve/authorize` (which holds the daemon session) and return
    with `?__serve_token=...`, exchanged for an HttpOnly `compute_serve` cookie
    scoped to the serve origin. CLI/SDK clients use
    `POST /api/v1/sandboxes/<id>/serve-token` to mint the same URL.

Tokens are HMAC-signed with `COMPUTE_SERVE_TOKEN_KEY` (or a key derived from
`COMPUTE_ADMIN_SECRET`; otherwise a random per-process key, which resets
private-serve sessions on restart).

### API

| Method / path | Purpose |
| --- | --- |
| `GET /api/v1/sandboxes/<id>/ports` | list exposures for a sandbox |
| `POST /api/v1/sandboxes/<id>/expose` | expose `{"guest_port":8080,"public":true,"subdomain":"my-app"}` |
| `POST /api/v1/sandboxes/<id>/unexpose` | remove `{"guest_port":8080}` |
| `POST /api/v1/sandboxes/<id>/serve-token` | mint `{"url":...}` for a private exposure |
| `PATCH /api/v1/sandboxes/<id>/ports/<port>` | toggle `{"public":false}` |
| `DELETE /api/v1/sandboxes/<id>/ports/<port>` | remove the exposure |
| `GET /api/v1/serve/authorize?next=<serve-url>` | browser auth hop for private serves |
| `POST /api/v1/network/port-forwards` | low-level create (`vm_id`, `guest_port`, `public`) |
| `GET /api/v1/network/port-forwards?vm_id=<id>` | list/filter all exposure rules |
| `PATCH /api/v1/network/port-forwards/<id>` | toggle visibility / serve label |

`public_url` in responses is **absolute** and is the canonical URL for the
exposure. Set `COMPUTE_PUBLIC_URL` to the externally reachable daemon base URL
so port-mode URLs use the right host; it is advertised as `public_url` on
`GET /api/v1/version`.

### Notes and limits

- Re-exposing the same guest port keeps the existing origin (slug/port).
- A serve label is generated from `<sandbox>-<port>` and made globally unique;
  `--subdomain` requests a preferred label.
- Apps that hard-code an absolute URL at build time (`PUBLIC_URL=...`) still
  need their own base-URL configuration; no proxy can fix that.
- Legacy `/preview/...` path routes were removed in this design. Use the
  `public_url` returned by the API.

## API surface

- `GET /api/v1/dns/status` — responder state (enabled, listen address, zones,
  host IP, last error).
- VM responses now include a transient `hostname` field
  (`<sandbox>.<username>.<zone>`) whenever DNS naming is configured — the GUI
  shows it in the inspect dialog and SSH terminal window.

## Naming rules

New sandboxes and new usernames must be valid DNS labels (lowercase letters,
digits, dashes, ≤ 63 chars, no leading/trailing dash) because they become part
of the hostname. The GUI slugifies sandbox names automatically. Existing
names/usernames keep working through sanitization.

## SSH authentication

SSH auth is handled by the sandbox's own sshd exactly as before: the user's
SSH keys are embedded into the guest at creation (`/api/v1/keys`), the default
SSH user is `compute`, and root login is available in the base image.

Password authentication is opt-in per sandbox. Pass `ssh_password` in the sandbox
create request (or `--ssh-password` on `compute sandbox create`) to enable it:

- The password is installed for the `compute` and `root` accounts, and sshd is
  configured with `PasswordAuthentication yes`. The embedded public key remains
  valid, so either method works.
- If no password is supplied, sshd is configured with
  `PasswordAuthentication no`, and both accounts are locked. Only the embedded
  public key (including the daemon's internal automation key) can log in.
- The password is hashed into the guest's `/etc/shadow`; the daemon never stores
  the plaintext or the hash. Sandbox responses only expose the boolean
  `ssh_password_enabled`.
- `compute sandbox ssh --password <password> user@sandbox` uses the same password
  when connecting through the daemon's SSH proxy.

The password is applied when the disk is created, including when cloning a
sandbox or launching a snapshot template. A sandbox created without a password
from a source that had one will have its managed accounts locked again.

### Managing public keys

Saved public keys live under `/api/v1/keys` and can be managed from the CLI:

```bash
# Save a key for the account
compute keys add --name laptop --key-file ~/.ssh/id_ed25519.pub

# Mark it account-wide: embedded into every current and future sandbox
compute keys add --name laptop --key-file ~/.ssh/id_ed25519.pub --all

# Promote or demote an existing key
compute keys apply laptop
compute keys update laptop --no-all

# Inspect and remove
compute keys list
compute keys show laptop
compute keys delete laptop
```

The GUI exposes the same controls in the **Account SSH Keys** dialog: each key
has an *all sandboxes* toggle, and the sandbox create dialog lets you pick a
specific saved key in addition to the account-wide set.

Account-wide keys are resolved at creation time and reconciled afterwards:

- New sandboxes embed the owner's account-wide keys automatically.
- Running sandboxes are updated over SSH, and stopped/paused sandboxes have
  their `authorized_keys` edited offline on the disk.
- Removing the account-wide flag or deleting the key reconciles the remaining
  sandboxes so the key is no longer authorized.
- A per-sandbox key selected with `--ssh-key <name|path>` stays scoped to that
  sandbox. `--ssh-key` also accepts a literal key or a path to a `.pub` file.

The daemon's internal automation key is always authorized so the control plane
can continue to reach the sandbox. Managed accounts are `compute` and `root`.
