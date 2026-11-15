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
