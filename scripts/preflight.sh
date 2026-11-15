#!/usr/bin/env bash
# Preflight checks for a Compute host. Read-only: reports problems and
# remediation without changing the host. Run as your normal user (use sudo
# only when a check needs it); scripts/host-prepare.sh applies host settings.
set -uo pipefail

FAILED=0
WARNED=0

say_ok()   { printf '  [OK]   %s\n' "$*"; }
say_warn() { printf '  [WARN] %s\n' "$*"; WARNED=$((WARNED + 1)); }
say_fail() { printf '  [FAIL] %s\n' "$*"; FAILED=$((FAILED + 1)); }
say_info() { printf '  [INFO] %s\n' "$*"; }

section() { printf '\n==> %s\n' "$*"; }

# Load .env when present so the checks use the installer's configuration.
if [ -f .env ]; then
    set -a
    # shellcheck disable=SC1091
    . ./.env
    set +a
fi

DATA_DIR="${COMPUTE_HOST_DATA_DIR:-./data}"
BIND_ADDRESS="${COMPUTE_BIND_ADDRESS:-127.0.0.1}"
API_PORT="${COMPUTE_API_PORT:-18080}"
DNS_ENABLED="${COMPUTE_DNS_ENABLED:-false}"
EDGE_LISTEN="${COMPUTE_EDGE_LISTEN:-}"
GUI_PORT="${COMPUTE_GUI_PORT:-8080}"
IMAGES_MODE="${COMPUTE_IMAGES_MODE:-auto}"

have_cmd() {
    command -v "$1" >/dev/null 2>&1 || [ -x "/sbin/$1" ] || [ -x "/usr/sbin/$1" ]
}

version_ge() {
    # version_ge HAVE REQUIRED
    [ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -n1)" = "$2" ]
}

port_in_use() {
    local port="$1" proto="${2:-tcp}"
    command -v ss >/dev/null 2>&1 || return 1
    if [ "$proto" = "udp" ]; then
        ss -H -lun 2>/dev/null | awk '{print $4}' | grep -Eq "[:.]${port}$"
    else
        ss -H -ltn 2>/dev/null | awk '{print $4}' | grep -Eq "[:.]${port}$"
    fi
}

section "Operating system and architecture"
if [ "$(uname -s)" = "Linux" ]; then
    say_ok "Linux $(uname -r)"
else
    say_fail "Compute requires Linux (found $(uname -s))"
fi

case "$(uname -m)" in
    x86_64 | amd64) say_ok "architecture x86_64" ;;
    aarch64 | arm64) say_ok "architecture arm64" ;;
    *) say_fail "unsupported architecture $(uname -m): use x86_64 or arm64" ;;
esac

section "Docker and Compose"
DOCKER_OK=0
if command -v docker >/dev/null 2>&1; then
    DOCKER_VERSION="$(docker version --format '{{.Server.Version}}' 2>/dev/null || true)"
    if [ -n "${DOCKER_VERSION}" ]; then
        DOCKER_OK=1
        if version_ge "${DOCKER_VERSION}" "24.0.0"; then
            say_ok "Docker Engine ${DOCKER_VERSION}"
        else
            say_fail "Docker Engine ${DOCKER_VERSION} is too old (need 24.0+)"
        fi
        if docker info --format '{{json .SecurityOptions}}' 2>/dev/null | grep -q rootless; then
            say_fail "rootless Docker is not supported; use rootful Docker Engine"
        else
            say_ok "rootful Docker Engine"
        fi
    else
        say_fail "cannot talk to the Docker daemon (is it running? is your user in the docker group?)"
    fi
else
    say_fail "docker not found: install Docker Engine 24+ (https://docs.docker.com/engine/install/)"
fi

if docker compose version >/dev/null 2>&1; then
    COMPOSE_VERSION="$(docker compose version --short 2>/dev/null | sed 's/^v//')"
    if version_ge "${COMPOSE_VERSION}" "2.20.0"; then
        say_ok "Docker Compose ${COMPOSE_VERSION}"
    else
        say_fail "Docker Compose ${COMPOSE_VERSION} is too old (need 2.20+ for cgroup: host)"
    fi
else
    say_fail "docker compose v2 not found: install the Compose plugin"
fi

section "Virtualization (KVM)"
if [ -c /dev/kvm ]; then
    if [ -r /dev/kvm ] && [ -w /dev/kvm ]; then
        say_ok "/dev/kvm is a character device and is read/write"
    elif [ "${DOCKER_OK}" = "1" ]; then
        # The supported install runs computed in a privileged container that
        # receives /dev/kvm directly, so host-user group membership is not
        # needed. Keep the hint for people who also run computed natively.
        say_warn "/dev/kvm is not read/write for this user; the privileged computed container receives /dev/kvm itself (add your user to the kvm group for a native/systemd install)"
    else
        say_fail "/dev/kvm is not read/write for this user: chmod 666 /dev/kvm or add your user to the kvm group"
    fi
else
    say_fail "/dev/kvm missing: enable VT-x/AMD-V (and nested virtualization on a VM host)"
fi
if grep -Eq 'vmx|svm' /proc/cpuinfo 2>/dev/null; then
    say_ok "CPU virtualization extensions reported by /proc/cpuinfo"
else
    say_warn "no vmx/svm flags in /proc/cpuinfo (nested virtualization may be disabled)"
fi

section "cgroups v2"
if [ -f /sys/fs/cgroup/cgroup.controllers ]; then
    CONTROLLERS="$(cat /sys/fs/cgroup/cgroup.controllers)"
    say_ok "cgroup v2 unified hierarchy (${CONTROLLERS:-no controllers listed})"
    for want in cpu memory pids; do
        case " ${CONTROLLERS} " in
            *" ${want} "*) ;;
            *) say_warn "cgroup controller '${want}' is not available at the root; VM limits may be best-effort" ;;
        esac
    done
else
    say_fail "cgroup v2 not mounted at /sys/fs/cgroup (cgroup v1 hosts are unsupported)"
fi

section "Kernel modules and sysctls"
resolve_cmd() {
    PATH="${PATH}:/sbin:/usr/sbin" command -v "$1" 2>/dev/null || true
}
module_available() {
    local modprobe_bin modinfo_bin
    modprobe_bin="$(resolve_cmd modprobe)"
    modinfo_bin="$(resolve_cmd modinfo)"
    # modprobe -n needs module dependency lookups; modinfo works as a
    # non-root fallback when the module exists but is not loaded yet.
    if [ -n "${modprobe_bin}" ] && "${modprobe_bin}" -n "$1" >/dev/null 2>&1; then
        return 0
    fi
    [ -n "${modinfo_bin}" ] && "${modinfo_bin}" "$1" >/dev/null 2>&1
}
for module in tun bridge br_netfilter vhost_net; do
    if [ -d "/sys/module/${module}" ]; then
        say_ok "module ${module} loaded"
    elif module_available "${module}"; then
        say_warn "module ${module} available but not loaded: sudo modprobe ${module} (or run scripts/host-prepare.sh)"
    elif [ "${module}" = "vhost_net" ]; then
        # Firecracker uses userspace virtio-net when vhost_net is absent;
        # vhost_net only improves throughput on some hosts.
        say_warn "module ${module} is unavailable; Firecracker will use userspace virtio-net (optional performance hint)"
    else
        say_fail "module ${module} is unavailable: install linux-modules-extra for your kernel"
    fi
done

SYSCTL_BIN="$(resolve_cmd sysctl)"
if [ -n "${SYSCTL_BIN}" ]; then
    if [ "$("${SYSCTL_BIN}" -n net.ipv4.ip_forward 2>/dev/null || echo 0)" = "1" ]; then
        say_ok "net.ipv4.ip_forward=1"
    else
        say_warn "net.ipv4.ip_forward is not enabled (computed enables it automatically on startup, or run scripts/host-prepare.sh)"
    fi
fi

section "Image build tools"
if [ "${IMAGES_MODE}" = "build" ]; then
    # Host-side tools are only needed when running scripts/build-rootfs.sh directly.
    missing_build_tools=""
    for cmd in curl sha256sum ssh-keygen mkfs.ext4 mount truncate tar; do
        if have_cmd "${cmd}"; then
            say_ok "${cmd} available"
        else
            missing_build_tools="${missing_build_tools} ${cmd}"
        fi
    done
    if [ -n "${missing_build_tools}" ]; then
        say_fail "missing image build tools:${missing_build_tools} (install curl, coreutils, openssh-client, e2fsprogs, util-linux, tar; or use COMPUTE_IMAGES_MODE=auto)"
    fi
else
    say_ok "host image build tools not required for COMPUTE_IMAGES_MODE=${IMAGES_MODE} (compute-init handles bootstrap inside the container)"
fi

section "Network ports"
say_info "API would bind ${BIND_ADDRESS}:${API_PORT}"
case "${BIND_ADDRESS}" in
    127.0.0.1 | localhost | ::1 | "[::1]") ;;
    *)
        say_warn "API binds a non-loopback address (${BIND_ADDRESS}): keep COMPUTE_AUTH_ENABLED=true and restrict access with a firewall"
        ;;
esac
if port_in_use "${API_PORT}" tcp; then
    say_warn "TCP ${API_PORT} is already in use (an existing daemon or another service)"
else
    say_ok "TCP ${API_PORT} is free"
fi
if [ "${EDGE_LISTEN}" = "-" ]; then
    say_info "HTTP edge explicitly disabled"
elif [ -z "${EDGE_LISTEN}" ] && [ "${DNS_ENABLED}" != "true" ]; then
    say_info "HTTP edge disabled (set COMPUTE_EDGE_LISTEN or enable DNS)"
else
    EDGE_PORT="${EDGE_LISTEN##*:}"
    [ -n "${EDGE_PORT}" ] || EDGE_PORT=80
    if port_in_use "${EDGE_PORT}" tcp; then
        say_warn "TCP ${EDGE_PORT} is already in use (systemd-resolved or another web server?)"
    else
        say_ok "TCP ${EDGE_PORT} is free"
    fi
fi
if [ "${DNS_ENABLED}" = "true" ]; then
    if port_in_use 53 udp || port_in_use 53 tcp; then
        say_warn "DNS port 53 is already in use (systemd-resolved may own it; configure delegation or disable DNS)"
    else
        say_ok "DNS port 53 is free (UDP and TCP)"
    fi
fi
if port_in_use "${GUI_PORT}" tcp; then
    say_info "TCP ${GUI_PORT} is already in use (only needed by the optional gui profile; the daemon serves its own UI on ${API_PORT})"
else
    say_info "TCP ${GUI_PORT} is free for the optional gui profile"
fi

section "Data directory and disk space"
if [ -d "${DATA_DIR}" ]; then
    say_ok "data directory exists: ${DATA_DIR}"
else
    say_info "data directory ${DATA_DIR} does not exist yet; docker compose creates it"
fi
DISK_PARENT="${DATA_DIR}"
while [ ! -d "${DISK_PARENT}" ] && [ "${DISK_PARENT}" != "/" ]; do
    DISK_PARENT="$(dirname "${DISK_PARENT}")"
done
AVAIL_MB="$(df -Pm "${DISK_PARENT}" 2>/dev/null | awk 'NR==2 {print $4}')"
if [ -n "${AVAIL_MB}" ]; then
    if [ "${AVAIL_MB}" -ge 10240 ]; then
        say_ok "${AVAIL_MB} MB free under ${DISK_PARENT}"
    else
        say_warn "only ${AVAIL_MB} MB free under ${DISK_PARENT}; base images plus VM disks need at least 10 GB"
    fi
else
    say_warn "could not determine free disk space under ${DISK_PARENT}"
fi

section "Mandatory access control"
if command -v getenforce >/dev/null 2>&1; then
    say_info "SELinux: $(getenforce). The container mounts the data volume without :z; add :z if denials appear."
fi
if [ -d /sys/kernel/security/apparmor ]; then
    say_info "AppArmor is enabled; Firecracker and TAP operations run privileged, no profile is applied by default."
fi

printf '\n'
if [ "${FAILED}" -gt 0 ]; then
    printf 'Preflight failed: %d error(s), %d warning(s). Fix the errors above, then re-run.\n' "${FAILED}" "${WARNED}"
    exit 1
fi
printf 'Preflight passed with %d warning(s).\n' "${WARNED}"
