# Sandbox domain options for `<sandbox>.<username>.<zone>`

This document compares the ways to give every sandbox a nested name of the form:

```
<sandbox>.<username>.<zone>
```

reachable from Tailscale clients (and optionally LAN/public clients), using the daemon's built-in authoritative DNS responder.

## Summary

| | Option | Works? | Best for |
| --- | --- | --- | --- |
| A | Private zone (`*.internal`) via Tailscale split DNS | **Yes — recommended** | Zero-cost tailnet-only nested names |
| B | A domain you own, via split DNS (tailnet) and optionally NS delegation (public/LAN) | Yes — same DNS path as A; public side needs extra ports and DNS records | Nested names plus public TLS and a future public/LAN path |
| C | Per-sandbox MagicDNS device names (`<sandbox>-<username>.<tailnet>.ts.net`) | Yes — single label only | Fallback when Tailscale provisioning is wanted and nesting is not |
| D | Public DNS provider automation (Cloudflare/Route53 A records per sandbox) | Technically yes, but does not create reachability | Not recommended for v1 |
| E | Tailscale `extraDNSRecords` policy records under `ts.net` | **No** — `ts.net` suffix rejected; other names require global DNS override | Not viable |

**Recommendation:** Use **A** as the zero-cost option (`sbx.internal` or `sandbox.internal`), or **B** if you own/want a real domain — B uses the same split-DNS mechanism today and unlocks a publicly trusted wildcard certificate and a public/LAN path. Use **C** only if nested names are not required.

## Constraints

1. **`*.ts.net` cannot be delegated with split DNS.** MagicDNS owns the tailnet's `ts.net` zone and answers authoritatively. A split-DNS route for `<host>.<tailnet>.ts.net` returns authoritative `NXDOMAIN` through MagicDNS (`100.100.100.100`).
2. **The daemon's responder is zone-agnostic.** `COMPUTE_DNS_BASE_DOMAIN` (`dns.base_domain`) and `COMPUTE_DNS_EXTRA_BASE_DOMAINS` (`dns.extra_base_domains`) are served identically.
3. **DNS names alone are not reachability.** The responder answers `<sandbox>.<username>.<zone>` with the sandbox's tailnet IP first, then its guest IP. Tailnet clients can reach the tailnet IP; the guest subnet (`172.16.0.0/16`) requires host/subnet routing.
4. **TLS termination.** The HTTP edge listens on `:80`; terminate TLS with a reverse proxy (Caddy, nginx, Traefik) or set `COMPUTE_TLS_CERT_FILE` / `COMPUTE_TLS_KEY_FILE` on the API listener.

## Option A: Private zone via Tailscale split DNS (Recommended)

Use a private zone such as `sbx.internal` (or `sandbox.internal`) and delegate it to the daemon with a Tailscale split-DNS route. `.internal` is reserved for private use and is never delegated on the public internet.

1. Set environment variables in `.env`:

   ```dotenv
   COMPUTE_DNS_ENABLED=true
   COMPUTE_DOMAIN=sbx.internal
   ```

   Or in `config/daemon.json`:

   ```json
   "dns": {
     "enabled": true,
     "listen": ":53",
     "base_domain": "sbx.internal",
     "host_addresses": ["100.64.0.10", "192.0.2.10"]
   }
   ```

2. Delegate the zone in Tailscale (admin console → DNS → Nameservers → Add nameserver → Custom → `<daemon-tailnet-ip>` → Restrict to search domain → `sbx.internal`), or via the Tailscale API:

   ```bash
   curl -s -u "${TAILSCALE_API_KEY}:" -X PATCH -H 'Content-Type: application/json' \
     -d '{"sbx.internal":["100.64.0.10"]}' \
     https://api.tailscale.com/api/v2/tailnet/-/dns/split-dns
   ```

3. Verify from a tailnet client:

   ```bash
   dig @100.100.100.100 myvm.alice.sbx.internal A
   dig @100.100.100.100 gui.sbx.internal A
   ssh compute@myvm.alice.sbx.internal
   ```

## Option B: A domain you own

Two independent layers; either can be used alone:

- **B1 — tailnet-only:** add `sandbox.<your-domain>` to `COMPUTE_DOMAIN` / `dns.extra_base_domains` and a Tailscale split-DNS route pointing at the daemon host's tailnet IP. No public DNS records are required, and you can still issue a wildcard certificate (`*.sandbox.<your-domain>`) via ACME DNS-01 and terminate TLS in Caddy/nginx/Traefik.
- **B2 — public/LAN delegation:** delegate the child zone at your DNS provider (`NS` + glue `A` record pointing to the daemon host) and expose **53/UDP + 53/TCP** and **80/TCP** (or **443/TCP** behind a TLS proxy). For LAN clients, configure a conditional forwarder in your local DNS server pointing to the daemon host's LAN IP.

```dns
sandboxes.example.com.      IN NS  ns1.sandboxes.example.com.
ns1.sandboxes.example.com.  IN A   <public-ip-of-daemon-host>
```

## Option C: Per-sandbox MagicDNS names

If guests join your tailnet directly via Tailscale inside the sandbox, they receive single-label MagicDNS names (`<device>.<tailnet>.ts.net`) without split DNS.

## Serve domains (one origin per published port)

Published sandbox ports use a **separate** wildcard domain from the
`<sandbox>.<username>.<zone>` naming above. Each exposure is served at
`https://<slug>.<serve-domain>/`, which gives the app its own origin so
redirects, cookies, service workers and absolute asset URLs work unchanged.

```dns
serves.example.com.        IN A   <public-ip-of-daemon-host>
*.serves.example.com.      IN A   <public-ip-of-daemon-host>
```

- Wildcard certificates (`*.serves.example.com`) cover exactly one label before
  the domain, which matches the `<slug>` form. Obtain one with ACME DNS-01 (or
  from your provider) and point `COMPUTE_SERVE_TLS_CERT_FILE` /
  `COMPUTE_SERVE_TLS_KEY_FILE` at it, or terminate TLS in Caddy/nginx/Traefik.
- Set `COMPUTE_SERVE_DOMAIN=serves.example.com` and
  `COMPUTE_PUBLIC_URL=https://serves.example.com`.
- On loopback binds the serve domain defaults to `localhost`, so no DNS or
  certificate work is needed (`http://web-8080.localhost:18080/`).
- On a tailnet or a bare public IP without a wildcard domain, use port mode:
  one listener per exposure (`COMPUTE_SERVE_PORT_START.._END`). Map a port with
  `tailscale serve --bg --https=<port> http://127.0.0.1:<port>` to get
  `https://<node>.<tailnet>.ts.net:<port>/` with a valid certificate.

Do **not** reuse the DNS zone used for `<sandbox>.<username>.<zone>` unless you
accept that serve slugs and sandbox hostnames share a namespace; the reserved
`api`/`gui`/`www` labels are never treated as serve slugs.
