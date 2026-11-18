# Compute v1.0.0 — Self-Hosted Firecracker MicroVM Platform

Compute is an open-source, self-hosted platform for running hardware-isolated **Firecracker microVM sandboxes** and **distributed compute jobs** on Linux hosts with `/dev/kvm`.

- **Sub-Second MicroVMs**: Boot, clone, pause/resume live memory (`save-state`), snapshot templates, stream terminals/exec, and serve guest HTTP ports at their own origin (subdomain or dedicated port) so redirects, cookies and SPAs just work.
- **Multi-Node Workers & Load Balancing**: Enroll remote `compute-worker` nodes over outbound WebSocket RPC/TCP tunnels (zero inbound worker ports) with configurable placement (`least_loaded`, `round_robin`, `bin_pack`, `spread`).
- **Live Migration & Bulk Operations**: Migrate sandboxes and entire pools across workers (`POST /api/v1/vms/:id/migrate`, `POST /api/v1/pools/:id/migrate`) and run single-request bulk actions/deletes.
- **Resource Management**: Hard disk ceilings, soft + hard CPU overcommit weights, RAM hard guarantees with idle SSD auto-hibernation (`auto_hibernated`), and HTTP 429 `ResourceLimitError` admission responses.
- **Unified Control Plane & Persistent WS UI**: Single `computed` binary embeds the Web Dashboard on `:18080` synced in real time over `/api/v1/ws`, alongside the `compute` CLI, REST API, and TypeScript/Python SDKs.

---

## Quickstart (Docker Compose)

Requires Linux (`x86_64` or `arm64`) with `/dev/kvm`, cgroup v2, and rootful Docker Engine 24+ / Compose v2.20+. Firecracker and Jailer are bundled inside the daemon container.

1. **Prepare the host & build base images** (loads kernel modules, verifies host readiness, downloads `vmlinux`, builds `ubuntu-24.04.ext4`, and generates `./data/images/ssh/id_ed25519` with live terminal progress):

   ```bash
   ./scripts/install.sh
   ```

2. **Start the daemon** (or run `make up`, which automatically runs `./scripts/install.sh` first if `./data/images` is not yet populated):

   ```bash
   docker compose up -d
   ```

3. **Verify readiness & open the embedded Web UI**:

   ```bash
   curl -fsS http://127.0.0.1:18080/api/v1/health
   curl -fsS http://127.0.0.1:18080/api/v1/ready
   ```

   Port `:18080` serves **both** the REST/WebSocket API and the embedded Web Dashboard (`http://127.0.0.1:18080/`, default loopback login `admin` / `admin`). The standalone `compute-gui` service (`:8080`) is strictly optional and disabled by default.

4. **Configure `.env` before exposing beyond loopback**:

   ```bash
   cp .env.example .env && chmod 600 .env
   ```

   > **Important**: Setting `COMPUTE_BIND_ADDRESS=0.0.0.0` (or any non-loopback IP) without changing `COMPUTE_ADMIN_PASSWORD` from `admin` (or clearing `COMPUTE_ADMIN_PASSWORD=` and setting `COMPUTE_ADMIN_SECRET`) causes `computed` to refuse startup. See [docs/install.md](docs/install.md).

---

## CLI Install & Building from Source

**Pre-built `compute` CLI** (Linux `amd64` / `arm64`, see [GitHub Releases](https://github.com/Rashid2G/compute/releases)):

```bash
curl -fsSL https://github.com/Rashid2G/compute/releases/latest/download/compute_1.0.0_linux_amd64.tar.gz | sudo tar -xz -C /usr/local/bin
compute --version
```

**Build from source** (requires Go 1.26+ and Node.js 20+):

```bash
cd web && npm ci && cd ..
make build-ui && make build
```

Run `make vet`, `make test`, and `make test-web` to verify local builds (see `make help` for image and systemd targets).

---

## Multi-Node Workers (Zero Inbound Ports)

```bash
# On control plane: create enrollment token & run worker on remote KVM host
compute worker token create --name worker-1 --pool gpu-pool
compute-worker run --control-plane http://<host>:18080 --token <token> --name worker-1 --pool gpu-pool
```

---

## Documentation

- **Operator Manual & API Site**: [docs/index.html](docs/index.html)
- **Host Installation & Multi-Node Guide**: [docs/install.md](docs/install.md)
- **Configuration Reference**: [docs/configuration.md](docs/configuration.md)
- **DNS & SSH Routing**: [docs/dns-ssh.md](docs/dns-ssh.md) · [docs/domain-options.md](docs/domain-options.md)
- **Security & Contributing**: [SECURITY.md](SECURITY.md) · [CONTRIBUTING.md](CONTRIBUTING.md)

---

## License

Compute is open-source software licensed under the [MIT License](LICENSE).
