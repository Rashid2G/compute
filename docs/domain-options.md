# Sandbox domain options for `<sandbox>.<username>.<zone>`

Status: proposal (open for user decision) — research thread for issue #16.

This document compares the ways to give every sandbox a nested name of the form

```
<sandbox>.<username>.<zone>
```

reachable from Tailscale clients (and optionally LAN/public clients), given the
daemon's built-in authoritative DNS responder. It records what was tested on
`compute-lab` on 2026-11-11 (base commit `ac6075c`, daemon with DNS + edge
enabled) and recommends an option.

**No base domain has been changed.** All lab config and tailnet test changes
described here were reverted; see [Test hygiene](#test-hygiene).

## Summary

| | Option | Works? | Best for |
| --- | --- | --- | --- |
| A | Private zone (`*.internal`) via Tailscale split DNS | **Yes — tested** | Zero-cost tailnet-only nested names |
| B | A domain you own, via split DNS (tailnet) and optionally NS delegation (public/LAN) | Yes — same DNS path as A; public side needs extra ports and DNS code work | Nested names plus public TLS and a future public/LAN path |
| C | Per-sandbox MagicDNS device names (`<sandbox>-<username>.<tailnet>.ts.net`) | Yes — single label only | Fallback when Tailscale provisioning is wanted and nesting is not |
| D | Public DNS provider automation (Cloudflare/Route53 A records per sandbox) | Technically yes, but does not create reachability | Not recommended for v1 |
| E | Tailscale `extraDNSRecords` policy records under `ts.net` | **No** — `ts.net` suffix rejected; other names accepted but not resolved | Not viable |

**Recommendation:** A as the immediate zero-cost option (`sbx.internal` or
`sandbox.internal`), or B if you own/want a real domain — B is the same split
DNS mechanism today and unlocks a publicly trusted wildcard certificate and a
later public/LAN path. Use C only if nested names are not required.

## Verified constraints

1. **`*.ts.net` cannot be delegated with split DNS.** MagicDNS owns the
   tailnet's `ts.net` zone and answers authoritatively. A split-DNS route for
   `compute-lab.tailb1be99.ts.net -> 100.78.129.96` is accepted by the API and
   shown by `tailscale dns status`, but queries for
   `spark.default.compute-lab.tailb1be99.ts.net` through `100.100.100.100`
   return authoritative `NXDOMAIN` on both `runner` and the lab. This was
   re-confirmed during this research (see [Appendix](#appendix-evidence)).
2. **The daemon's responder is zone-agnostic.** `dns.base_domain` and
   `dns.extra_base_domains` are served identically; nothing in
   `internal/dns` or `cmd/computed/dns.go` depends on Tailscale. Adding a zone
   is a config-only change.
3. **DNS names alone are not reachability.** The responder answers
   `<sandbox>.<username>.<zone>` with the sandbox's tailnet IP first, then its
   guest IP. Tailnet clients can reach the tailnet IP; the guest subnet
   (`172.16.0.0/16`) is only routable inside the `computed` container network
   namespace (see [Reachability](#reachability-is-a-separate-problem)).
4. **The daemon has no TLS listener.** It serves plain HTTP on the edge (`:80`);
   SSH connections are protected by SSH itself, not by the edge.

## Options tested

### A. Private zone via Tailscale split DNS — recommended zero-cost option

Use a private zone such as `sbx.internal` (or `sandbox.internal`) and delegate
it to the daemon with a Tailscale split-DNS route. `.internal` is reserved for
private use and is never delegated on the public internet.

**Test procedure (reversible, run on 2026-11-11):**

1. Lab config (backup first): set
   `dns.extra_base_domains = ["sbx.internal", "sandboxes.example.com"]` while
   keeping `base_domain` unchanged, then restart `computed`.
2. `PATCH /api/v2/tailnet/-/dns/split-dns` with
   `{"sbx.internal":["100.78.129.96"],"sandboxes.example.com":["100.78.129.96"]}`.
3. Query the lab's own MagicDNS resolver (`dig @100.100.100.100`), which is the
   same code path every tailnet client uses.
4. Restore the config and split-DNS routes.

**Results — success:**

```text
GET /api/v1/dns/status
{"zones":["compute-lab.tailb1be99.ts.net","sbx.internal","sandboxes.example.com"], ...}

tailscale dns status
Split DNS Routes:
  - sbx.internal                   -> 100.78.129.96
  - sandboxes.example.com          -> 100.78.129.96
  - ts.net.                        -> 199.247.155.53   (Tailscale's own)

# from the lab, through MagicDNS:
dig @100.100.100.100 spark.default.sbx.internal A
  status: NOERROR, flags: qr aa rd, ANSWER: 1
  spark.default.sbx.internal. 15 IN A 172.16.0.2      # sandbox guest IP

dig @100.100.100.100 spark.default.sandboxes.example.com A
  status: NOERROR, ANSWER: 1
  ... 15 IN A 172.16.0.2

dig @100.100.100.100 gui.sbx.internal A
  status: NOERROR, ANSWER: 2
  gui.sbx.internal. 15 IN A 100.78.129.96
  gui.sbx.internal. 15 IN A 192.168.1.70

dig @100.100.100.100 nope.default.sbx.internal A
  status: NXDOMAIN (AA clear, no SOA in authority)

# control, same resolver, ts.net zone:
dig @100.100.100.100 spark.default.compute-lab.tailb1be99.ts.net A
  status: NXDOMAIN (authoritative, qr aa rd ra ad)
```

**Conclusions:**

- Tailscale split DNS delegates arbitrary non-`ts.net` zones to the daemon and
  the daemon's authoritative answers pass through MagicDNS (`aa` flag set on
  the A answer).
- Any non-`ts.net` suffix works, including a real domain you own
  (`sandboxes.example.com` was used only as a stand-in and is not delegated
  publicly).
- The client sees a normal NXDOMAIN for unknown sandboxes.
- `tailscale dns status` reflects the route immediately; no client restart was
  needed.

**Caveats:**

- Tailnet-only. There are no public DNS records; non-tailnet clients cannot
  resolve the zone unless you give them the daemon as a resolver (LAN
  conditional forwarder) or delegate publicly (option B).
- No publicly trusted TLS is possible for `.internal` (see
  [TLS](#tls-implications)).
- Tested from a Linux client. Split DNS is supported on all Tailscale
  platforms, but iOS/Android per-app resolver behavior was not exercised here.
- If the daemon is down, queries for the zone fail (timeout); other DNS is
  unaffected.
- `POST /dns/split-dns` **merges**; it does not delete omitted routes. Remove a
  route with `PATCH` and a `null` value (this surprised the test cleanup and is
  worth knowing for rollbacks).

### B. A domain you own

Two independent layers; either can be used alone:

**B1 — tailnet-only (same as A):** add `sandbox.<your-domain>` to
`dns.extra_base_domains` and a split-DNS route to `100.78.129.96`. No public
records are needed; resolution stays inside the tailnet. This works today with
the same evidence as A, because Tailscale does not require you to own the zone
for split DNS.

**B2 — public/LAN delegation:** delegate the zone at your DNS provider and
expose the daemon's port 53.

Exact records at the provider (example `sandboxes.example.com`):

```dns
; delegate the child zone to the daemon host
sandboxes.example.com.      IN NS  ns1.sandboxes.example.com.
ns1.sandboxes.example.com.  IN A   <public-ip-of-daemon-host>   ; glue

; optional second nameserver (needs a second address/listener)
sandboxes.example.com.      IN NS  ns2.sandboxes.example.com.
ns2.sandboxes.example.com.  IN A   <second-public-ip>
```

Alternatively, HTTP-only via the edge with a wildcard record:

```dns
*.sandboxes.example.com.    IN A   <public-ip>   ; edge routes by Host header
```

but that does not help SSH (SSH has no hostname to route on; it needs the
per-sandbox A answer from the authoritative responder).

**Requirements for B2:**

- A public static IP for the daemon host and inbound **53/UDP + 53/TCP**
  (DNS) and **80/TCP** (HTTP edge); 443/TCP if you terminate TLS.
- The daemon currently publishes 53/80 only on `192.168.1.70` and
  `100.78.129.96` in `docker-compose.yml`; a public path needs an additional
  bind/forward on the public interface.
- **The responder does not serve NS or SOA records** (verified: in-zone
  `NS`/`SOA`/`MX` queries return non-authoritative `NOERROR` with zero
  answers), and unknown names return `NXDOMAIN` with the AA bit clear and no
  SOA. Positive A answers still work through a delegation, but negative
  answers, zone walking, and some validating resolvers expect AA + SOA/NS. Add
  NS/SOA support in `internal/dns` before relying on public NS delegation.
- For LAN clients, either point their resolver at `192.168.1.70` for the zone
  (conditional forwarder) or add the zone to the local DNS server. A bare
  `NS` record cannot point at an IP address.
- The responder answers A only; AAAA queries get NODATA, so IPv6-preferring
  clients fall back to A.

**Cost:** domain registration (typically ~10–15 USD/year) plus an optional
public IP/port forwarding.

### C. Per-sandbox MagicDNS names (fallback)

Enable Tailscale provisioning for sandboxes (`tailscale.enabled`,
`COMPUTE_TAILSCALE_API_KEY`, `tag:compute:sandbox` ownership and a grant), and
each sandbox joins the tailnet as an ephemeral device. MagicDNS then resolves
its single-label device name:

```text
compute-<vmid>.<tailnet>.ts.net      # current SandboxHostname() form
spark-alice.<tailnet>.ts.net         # if SandboxHostname is changed to name-owner
```

**Evidence:** MagicDNS device names resolve normally today:

```text
dig @100.100.100.100 compute-lab.tailb1be99.ts.net A
  compute-lab.tailb1be99.ts.net. 5 IN A 100.78.129.96
dig @100.100.100.100 debian.tailb1be99.ts.net A
  debian.tailb1be99.ts.net. 5 IN A 100.117.99.112
```

**Tradeoffs versus nested subdomains:**

- Only one label before the tailnet suffix. You can encode
  `<sandbox>-<username>` in that label, but you cannot produce
  `<sandbox>.<username>.<zone>`.
- Depends on Tailscale control-plane provisioning and ACL/tag setup; the name
  is a device property, not a daemon-managed DNS record.
- Names are ephemeral: with ephemeral auth keys the device (and its MagicDNS
  name) disappears after the sandbox stops; a stopped sandbox's name stops
  resolving, unlike the daemon's DNS answers.
- Renaming a sandbox requires re-provisioning/renaming the device; two users
  with the same sandbox name need the `-<username>` suffix to avoid collisions.
- Upside: no custom zone, no split DNS, and `https://<name>.<tailnet>.ts.net`
  can use Tailscale-issued certificates (`tailscale cert` / `tailscale serve`).
- `compute-<vmid>` names are opaque; a name-based hostname change in
  `internal/tailscale/admin.go` (`SandboxHostname`) would make them readable.

### D. Public DNS provider automation (Cloudflare/Route53)

Create an A record per sandbox at the provider, pointing at the sandbox's
reachable address, with create/delete hooks in the VM lifecycle.

**Requirements:** a zone at the provider, an API token with DNS-edit scope
(stored outside the repo and rotated), per-record lifecycle (create on start,
delete on stop/destroy, reconcile drift), rate-limit handling, and a public
address that clients can actually reach. The daemon has no DNS-provider
integration today, so this is new code plus a secret-management surface.

**Why it is not recommended for v1:**

- A record pointing at a sandbox's `100.x` tailnet IP is only useful to tailnet
  clients; public resolvers would hand a private address to everyone.
- A record pointing at a public IP still needs per-sandbox port forwarding or a
  reverse proxy to make the sandbox reachable — DNS is not the hard part.
- Per-sandbox records leak sandbox names and counts to public DNS (enumeration)
  and add provider coupling, token handling, and drift.
- A provider wildcard record (`*.<user>.<zone> -> host`) plus the HTTP edge is
  the simpler variant if only HTTP matters; it still cannot serve SSH by name.

### E. Tailscale `extraDNSRecords` — rejected

Tailscale supports `extraDNSRecords` in the tailnet policy file (alpha; the
`extraDNSRecords` tailnet setting must be enabled by Tailscale support, and the
DNS docs also require "Override DNS servers"). Tested against the live tailnet:

```text
POST /api/v2/tailnet/-/acl with
"extraDNSRecords": [{"Name":"spark.default.compute-lab.tailb1be99.ts.net","Value":"100.78.129.96"}]
→ HTTP 500
  {"message":"extra DNS record name \"spark.default.compute-lab.tailb1be99.ts.net\"
             has unsupported suffix: \"ts.net\""}

POST with {"Name":"probe.sbx.internal","Value":"100.78.129.96"}
→ HTTP 200 (policy accepted)
dig @100.100.100.100 probe.sbx.internal A
→ NXDOMAIN
```

**Conclusion:** names under `ts.net` are explicitly rejected ("unsupported
suffix"), so this cannot provide nested names under the tailnet zone. Other
suffixes are accepted but are not served while global DNS override is off;
enabling override would force every tailnet device onto Tailscale DNS only
(a disruptive, tailnet-wide behavior change) and the feature is still alpha and
support-gated. The ACL was restored byte-for-byte (same SHA-256) after the
test.

## Comparison

| | A. Private zone (split DNS) | B. Owned domain (split DNS) | B2. Owned domain (public NS) | C. MagicDNS device names | D. Provider automation |
| --- | --- | --- | --- | --- | --- | --- |
| Nested `<sandbox>.<user>.<zone>` | **Yes** | **Yes** | **Yes** | No (one label) | Yes |
| Tailnet clients | Yes, after split-DNS route | Yes, after split-DNS route | Yes (public or split DNS) | Yes, once the sandbox joins | Only if the record points at a reachable IP |
| LAN clients | Resolution with a conditional forwarder; reachability needs guest routing | Resolution with a conditional forwarder; reachability needs guest routing | Yes, if 53/80 and guest routing are reachable | Yes, if they use MagicDNS | Yes, if records point at a reachable IP |
| Public clients | No | No (unless B2) | Yes (needs 53/80 exposure) | No | Yes (needs public reachability) |
| Setup effort | Low: one config field + one API PATCH | Low for B1; medium/high for B2 (registrar, NS, ports, code for SOA/NS) | High | Low–medium: Tailscale API key, tags, ACL; optionally rename devices | High: new provider integration + secret lifecycle |
| Cost | 0 | Domain fee | Domain fee + public IP/ports | 0 (Tailscale plan limits apply) | Domain fee (+ hosted zone/API) |
| TLS | None for `.internal`; private CA if needed | Public wildcard via DNS-01 (`*.sbx.example.com`), but needs a TLS terminator | Same as B1 | Per-device `tailscale cert` for `<device>.<tailnet>.ts.net` | Public wildcard via DNS-01 |
| Wildcard vs per-record | Wildcard zone: the daemon answers each name | Wildcard zone | NS delegation: per-name answers from the daemon; or provider wildcard for HTTP only | Per-device record (automatic) | Per-sandbox records |
| Failure modes | Daemon down → zone timeouts; lab tailnet IP changes; missing sandbox reachability | Same, plus DNS-provider/registrar issues; public delegation needs SOA/NS and open 53 | Same, plus public exposure and abuse surface | Tailscale control plane/ACL; names vanish on stop; label collisions | Provider outage/rate limits, token loss, drift, name leakage |
| Daemon/sandbox interaction | Daemon answers `<sandbox>.<user>.<zone>` → tailnet IP then guest IP; apex/`api`/`gui`/`www` → host | Same | Same | Daemon DNS unused for these names; each sandbox is its own DNS record | Records duplicate the daemon's data and must be reconciled |

## Recommendation

1. **Now, zero cost, tailnet-only:** use **A** with a private zone. `sbx.internal`
   is the suggested candidate (`sandbox.internal` works the same). It delivers
   the exact nested scheme, requires one config field and one Tailscale API
   call, and was verified end-to-end on the lab.
2. **If you own or will buy a domain:** use **B1** (`sandbox.<your-domain>` with
   split DNS). It is the same work as A today, and later lets you add public
   wildcard TLS via DNS-01 and B2 public/LAN delegation without changing the
   sandbox names.
3. **Fallback only:** **C** if nested names are not required or you are enabling
   Tailscale sandbox provisioning anyway.
4. **Do not use D or E** for this scheme.

Two prerequisites apply to A and B regardless of the zone:

- The lab's tailnet data plane must allow client → `100.78.129.96` TCP/53,80
  (issue #16, owned by the ACL thread; from `runner` the TCP path is currently
  blocked while disco ping works).
- Sandboxes must be reachable: either join them to the tailnet (then DNS
  answers their `100.x` address) or route `172.16.0.0/16` to the sandboxes.
  Today the lab advertises only an exit node (`0.0.0.0/0`, `::/0`), not the
  guest subnet, and the guest bridge exists in both the host and container
  network namespaces (the host's copy cannot reach guests; the container's
  can). Enabling Tailscale provisioning is the smaller change.

## Implementation steps for the recommended option (A)

Assumes the user approves the zone name; do not apply until then.

1. **Add the zone (non-disruptive).** In `config/daemon.json`:

   ```json
   "dns": {
     "enabled": true,
     "listen": ":53",
     "base_domain": "compute-lab.tailb1be99.ts.net",
     "extra_base_domains": ["sbx.internal"],
     "host_addresses": ["100.78.129.96", "192.168.1.70"]
   }
   ```

   Deploy with the shared lock:
   `flock -w 1800 /tmp/opencode/lab-deploy.lock make deploy-lab-ui`.

2. **Delegate the zone in Tailscale** (API key from the environment; never echo
   it):

   ```bash
   KEY=$(cat /home/rashid/on-demand-vms/.env)
   curl -s -u "$KEY:" -X PATCH -H 'Content-Type: application/json' \
     -d '{"sbx.internal":["100.78.129.96"]}' \
     https://api.tailscale.com/api/v2/tailnet/-/dns/split-dns
   ```

   Equivalent UI: admin console → DNS → Nameservers → Add nameserver →
   Custom → `100.78.129.96` → Restrict to search domain → `sbx.internal`.
   Optionally add `sbx.internal` as a search path so short names work.

3. **Verify from a tailnet client:**

   ```bash
   dig @100.100.100.100 spark.default.sbx.internal A
   dig @100.100.100.100 gui.sbx.internal A
   ssh compute@spark.default.sbx.internal      # after reachability is enabled
   ```

4. **Promote it to the displayed base domain (later, after approval).** Set
   `base_domain` to `sbx.internal` and keep the `ts.net` zone in
   `extra_base_domains` for backward compatibility. This changes the API
   `hostname` field and the GUI's displayed name (the first entry of
   `DNSBaseDomains()` is the primary).

5. **Make sandboxes reachable.** Either enable tailnet provisioning
   (`tailscale.enabled`, `COMPUTE_TAILSCALE_API_KEY`, `tag:compute:sandbox`
   owner + grant) so DNS answers `100.x`, or advertise `172.16.0.0/16` from the
   container's `tailscaled` and approve the route.

6. **Roll back** by reversing step 2 (`PATCH` with `{"sbx.internal": null}`),
   removing the zone from the config, and redeploying. Note that
   `POST /dns/split-dns` merges; only `PATCH` with `null` deletes a route.

## Reachability is a separate problem

The DNS responder makes names resolve; it does not make packets route. This is
worth stating in the user-facing docs because a resolving name that times out
looks like a DNS bug.

- `sandboxAddresses()` (`cmd/computed/dns.go`) returns the sandbox tailnet IP
  first, then the guest IP.
- Guest IPs (`172.16.0.0/16`) live in the `computed` container's network
  namespace. The lab host has a duplicate `mft3br0` with the same
  `172.16.0.1/16` address, but it cannot reach guests (`ping 172.16.0.2` fails
  from the host, succeeds from the container).
- The lab's Tailscale node advertises `0.0.0.0/0` and `::/0` (exit node), not
  `172.16.0.0/16`. Tailnet clients reaching `172.16.0.2` therefore fail unless
  sandboxes join the tailnet or the guest subnet is routed.
- From `runner`, `dig @100.78.129.96` and `curl http://100.78.129.96/` time out
  even though `tailscale ping compute-lab` succeeds — the ACL data-plane bug in
  issue #16, not a DNS issue.

## TLS implications

The daemon has no TLS listener (plain HTTP on `:80`). Options by zone type:

- **`.internal`:** no public CA can issue certificates. Use plain HTTP inside
  the tailnet, or a private CA and install its root on clients. Tailscale HTTPS
  certificates are issued by Let's Encrypt for the tailnet's
  `<device>.<tailnet>.ts.net` names (DNS-01 on a `*.ts.net` TXT record), not for
  custom zones.
- **Owned domain:** issue a wildcard certificate (`*.sbx.example.com`) with a
  DNS-01 challenge at the provider and terminate TLS in front of the edge
  (Caddy/nginx/Traefik), forwarding to `127.0.0.1:80`. This works even if the
  name only resolves over split DNS, because certificate issuance does not
  need public A records — only the DNS-01 TXT record.

## Known gaps / follow-ups

- The responder answers only A, returns non-authoritative NOERROR for in-zone
  `NS`/`SOA` queries, and returns `NXDOMAIN` without AA or SOA for unknown
  in-zone names. Add NS/SOA and authoritative negative answers before public NS
  delegation (option B2).
- Port-prefixed ingress names (`8080-myvm.<zone>`, `myvm-8080.<zone>`) are not
  answered by the authoritative responder (`NXDOMAIN`), even though the edge
  can route them by `Host` header when DNS points at the host. They need a
  provider wildcard record or a responder change if they are meant to be
  first-class URLs.
- The pre-existing split-DNS route
  `compute-lab.tailb1be99.ts.net -> 100.78.129.96` on the lab is inert
  (MagicDNS intercepts it). It was left untouched by this research and can be
  removed.
- ACL data-plane work (issue #16) is owned by another thread; this proposal
  does not depend on it for the DNS evidence, but tailnet clients will not
  reach the lab until it lands.

## Test hygiene

- Lab `config/daemon.json` was backed up, edited only for the test, and
  restored; `/api/v1/dns/status` now reports only
  `["compute-lab.tailb1be99.ts.net"]` and the container is healthy.
- Tailscale split-DNS routes: the two test zones were added and then removed
  with a `PATCH` of `null` values; the original map (including the inert
  `ts.net` route) is back.
- Tailnet policy file: both `extraDNSRecords` attempts were followed by a
  restore; the ACL SHA-256 before and after matches
  (`afdf5e80…b120a5`).
- No sandbox, base domain, or deployed branch was changed.

## Appendix: evidence

Environment: `compute-lab` (`192.168.1.70`, tailnet `100.78.129.96`), tailnet
`tailb1be99.ts.net`, daemon with `dns.enabled=true`, edge on `:80`, DNS on
`:53` UDP+TCP, compose deployment. Date: 2026-11-11. Base commit: `ac6075c`.

Key commands (all read-only unless noted under Test hygiene):

```bash
# ts.net split-DNS control (fails by design)
ssh compute-lab 'dig @100.100.100.100 spark.default.compute-lab.tailb1be99.ts.net A'
# -> NXDOMAIN, flags: qr aa rd ra ad

# private zone delegation (test config)
ssh compute-lab 'dig @100.100.100.100 spark.default.sbx.internal A'
# -> NOERROR, flags: qr aa rd, spark.default.sbx.internal. 15 IN A 172.16.0.2

# MagicDNS device name
ssh compute-lab 'dig @100.100.100.100 compute-lab.tailb1be99.ts.net A'
# -> NOERROR, 100.78.129.96

# policy-field test
curl -u "$KEY:" -X POST -H 'If-Match: <etag>' --data-binary @acl-mod.hujson \
  https://api.tailscale.com/api/v2/tailnet/-/acl
# -> HTTP 500: extra DNS record name "...ts.net" has unsupported suffix: "ts.net"
```

Raw logs from the test run are in `/tmp/opencode/domain-test-a.log` and
`/tmp/opencode/domain-test-e*.log` on `runner` (not committed).
