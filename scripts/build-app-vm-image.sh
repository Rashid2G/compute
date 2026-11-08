#!/usr/bin/env bash
# build-app-vm-image.sh — Build a Firecracker-compatible Debian rootfs that
# runs the full Compute application (daemon + Web GUI + CLI) on boot.
#
# Usage: sudo ./scripts/build-app-vm-image.sh [output.ext4] [size_mb]
#
# The resulting image is a bootable ext4 rootfs for the Firecracker kernel in
# data/images/vmlinux. It starts `computed` on :18080 and the embedded Web GUI
# on :8080 inside the microVM.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_IMG="${1:-${REPO_ROOT}/data/images/app-server.ext4}"
SIZE_MB="${2:-3072}"
SUITE="${DEBOOTSTRAP_SUITE:-trixie}"
MIRROR="${DEBOOTSTRAP_MIRROR:-http://deb.debian.org/debian}"
MNT="$(mktemp -d /tmp/compute-appvm-mnt-XXXXXX)"
BIN_SRC="${REPO_ROOT}/bin"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }

cleanup() {
    for d in dev/pts dev proc sys; do
        mountpoint -q "${MNT}/${d}" && umount -l "${MNT}/${d}" || true
    done
    mountpoint -q "${MNT}" && umount -l "${MNT}" || true
    rmdir "${MNT}" 2>/dev/null || true
}
trap cleanup EXIT

if [[ $EUID -ne 0 ]]; then
    echo "This script must run as root (it mounts a loopback image)." >&2
    exit 1
fi

for b in "${BIN_SRC}/compute" "${BIN_SRC}/computed" "${BIN_SRC}/compute-worker"; do
    if [[ ! -x "$b" ]]; then
        echo "Missing binary: $b. Run 'make build' first." >&2
        exit 1
    fi
done

log "Creating ${SIZE_MB}MB ext4 image at ${OUT_IMG}"
mkdir -p "$(dirname "${OUT_IMG}")"
rm -f "${OUT_IMG}"
truncate -s "${SIZE_MB}M" "${OUT_IMG}"
mkfs.ext4 -F -q -L compute-app "${OUT_IMG}"

log "Mounting image"
mount -o loop "${OUT_IMG}" "${MNT}"

log "Bootstrapping Debian ${SUITE} (minbase)"
debootstrap --variant=minbase --arch=amd64 "${SUITE}" "${MNT}" "${MIRROR}" >/tmp/debootstrap-appvm.log 2>&1
mount -t proc proc "${MNT}/proc"
mount -t sysfs sysfs "${MNT}/sys"
mount --bind /dev "${MNT}/dev"
mount --bind /dev/pts "${MNT}/dev/pts"

log "Configuring apt and installing runtime packages"
cat >"${MNT}/etc/apt/sources.list" <<EOF
deb ${MIRROR} ${SUITE} main
deb ${MIRROR} ${SUITE}-updates main
deb ${MIRROR}-security ${SUITE}-security main
EOF

chroot "${MNT}" /bin/bash -c 'export DEBIAN_FRONTEND=noninteractive; apt-get update -qq && apt-get install -y -qq --no-install-recommends openssh-server iproute2 procps curl ca-certificates jq less nano util-linux iputils-ping tzdata logrotate netbase && rm -rf /var/lib/apt/lists/*' >>/tmp/debootstrap-appvm.log 2>&1

log "Creating users and SSH configuration"
chroot "${MNT}" /bin/bash -c '
    (userdel -r debian 2>/dev/null || true)
    if ! id compute >/dev/null 2>&1; then
        useradd -m -s /bin/bash -u 1000 compute
    fi
    echo "root:root" | chpasswd
    echo "compute:compute" | chpasswd
    mkdir -p /root/.ssh /home/compute/.ssh /run/sshd /var/log/compute
    chmod 700 /root/.ssh /home/compute/.ssh
    chown -R compute:compute /home/compute
    sed -i "s/^#\?PermitRootLogin.*/PermitRootLogin yes/" /etc/ssh/sshd_config
    sed -i "s/^#\?PasswordAuthentication.*/PasswordAuthentication yes/" /etc/ssh/sshd_config
    sed -i "s/^#\?UsePAM.*/UsePAM no/" /etc/ssh/sshd_config
    sed -i "s/^#\?PubkeyAuthentication.*/PubkeyAuthentication yes/" /etc/ssh/sshd_config
    grep -q "^UsePAM no" /etc/ssh/sshd_config || echo "UsePAM no" >> /etc/ssh/sshd_config
    ssh-keygen -A
'

log "Installing Compute binaries, config, and startup init"
install -d "${MNT}/opt/compute/bin" "${MNT}/etc/compute"
install -m0755 "${BIN_SRC}/compute" "${MNT}/opt/compute/bin/compute"
install -m0755 "${BIN_SRC}/computed" "${MNT}/opt/compute/bin/computed"
install -m0755 "${BIN_SRC}/compute-worker" "${MNT}/opt/compute/bin/compute-worker"
install -m0755 "${BIN_SRC}/compute-bench" "${MNT}/opt/compute/bin/compute-bench" 2>/dev/null || true

cat >"${MNT}/etc/compute/daemon.json" <<'JSON'
{
  "server": {
    "listen": "0.0.0.0:18080",
    "db_path": "/var/lib/compute/data/compute.db",
    "firecracker_bin": "/opt/compute/bin/firecracker"
  },
  "network": {
    "mode": "nat",
    "bridge_name": "compute0",
    "bridge_ip": "10.20.0.1/16",
    "subnet": "10.20.0.0/16",
    "gateway_ip": "10.20.0.1"
  },
  "defaults": {
    "vcpus": 1,
    "memory_mb": 512,
    "disk_size_mb": 4096,
    "image": "debian-13"
  },
  "images": {
    "images_dir": "/var/lib/compute/images",
    "vms_dir": "/var/lib/compute/vms",
    "default_kernel": "vmlinux",
    "default_rootfs": "debian-13.ext4",
    "ssh_key_path": "/var/lib/compute/images/ssh/id_ed25519",
    "available": [
      {
        "name": "debian-13",
        "kernel": "vmlinux",
        "rootfs": "debian-13.ext4",
        "description": "Debian 13 (trixie) with SSH and Compute tooling"
      }
    ]
  },
  "resources": {
    "max_running_vms": 0,
    "max_total_vcpus": 0,
    "max_total_memory_mb": 0,
    "max_total_disk_mb": 0,
    "min_free_disk_space_mb": 256
  },
  "jobs": {
    "local_execution": true,
    "max_concurrent": 2,
    "boot_timeout_seconds": 90
  }
}
JSON

cat >"${MNT}/sbin/init" <<'INIT'
#!/bin/bash
# Compute app VM init: mounts pseudo filesystems, configures networking from
# the kernel ip= argument, then starts sshd, the daemon, and the Web GUI.
export PATH=/opt/compute/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
mount -t proc proc /proc 2>/dev/null || true
mount -t sysfs sysfs /sys 2>/dev/null || true
mount -t devtmpfs devtmpfs /dev 2>/dev/null || true
mkdir -p /dev/pts /dev/shm /run /var/log/compute /var/lib/compute/data /var/lib/compute/images /var/lib/compute/vms
mount -t devpts devpts /dev/pts -o mode=620,ptmxmode=666,gid=5 2>/dev/null || true
mount -t tmpfs tmpfs /dev/shm 2>/dev/null || true
mount -t tmpfs tmpfs /run 2>/dev/null || true
mount -t tmpfs tmpfs /tmp -o mode=1777 2>/dev/null || true
mkdir -p /run/sshd /run/lock

ip link set lo up 2>/dev/null || true
ip link set eth0 up 2>/dev/null || true

IP_SPEC=$(tr ' ' '\n' < /proc/cmdline | sed -n 's/^ip=//p' | head -n1)
if [ -n "$IP_SPEC" ] && [ "$IP_SPEC" != "dhcp" ]; then
    CIP=$(echo "$IP_SPEC" | cut -d: -f1)
    GW=$(echo "$IP_SPEC" | cut -d: -f3)
    MASK=$(echo "$IP_SPEC" | cut -d: -f4)
    KHST=$(echo "$IP_SPEC" | cut -d: -f5)
    DEV=$(echo "$IP_SPEC" | cut -d: -f6)
    [ -z "$DEV" ] && DEV="eth0"
    PREFIX=24
    case "$MASK" in
        255.255.255.255) PREFIX=32 ;;
        255.255.255.0)   PREFIX=24 ;;
        255.255.0.0)     PREFIX=16 ;;
        255.0.0.0)       PREFIX=8 ;;
        [0-9]*)          PREFIX="$MASK" ;;
    esac
    ip addr flush dev "$DEV" 2>/dev/null || true
    ip addr add "${CIP}/${PREFIX}" dev "$DEV" 2>/dev/null || true
    [ -n "$GW" ] && ip route add default via "$GW" dev "$DEV" 2>/dev/null || true
    if [ -n "$KHST" ]; then
        hostname "$KHST" 2>/dev/null || true
        echo "127.0.0.1 localhost $KHST" > /etc/hosts
    fi
fi
printf 'nameserver 1.1.1.1\nnameserver 8.8.8.8\n' > /etc/resolv.conf

/usr/sbin/sshd 2>/dev/null || true

cd /opt/compute
./bin/computed -config /etc/compute/daemon.json >>/var/log/compute/computed.log 2>&1 &
sleep 2
./bin/compute gui --addr 0.0.0.0 --port 8080 --daemon http://127.0.0.1:18080 >>/var/log/compute/gui.log 2>&1 &

# Keep PID 1 alive and reap children.
while true; do sleep 60; done
INIT
chmod +x "${MNT}/sbin/init"

cat >"${MNT}/etc/fstab" <<'FSTAB'
/dev/vda / ext4 defaults,noatime 0 1
proc /proc proc defaults 0 0
sysfs /sys sysfs defaults 0 0
devpts /dev/pts devpts gid=5,mode=620,ptmxmode=666 0 0
tmpfs /dev/shm tmpfs defaults 0 0
tmpfs /run tmpfs defaults 0 0
FSTAB

cat >"${MNT}/etc/motd" <<'MOTD'

  ____                            _        _    __     __  __  __
 / ___|___  _ __ ___  _ __  _   _| |_ ___ | |   \ \   / / |  \/  |
| |   / _ \| '_ ` _ \| '_ \| | | | __/ _ \| |    \ \ / /  | |\/| |
| |__| (_) | | | | | | |_) | |_| | ||  __/| |     \ V /   | |  | |
 \____\___/|_| |_| |_| .__/ \__,_|\__\___|_|      \_/    |_|  |_|
                     |_|
Compute application microVM — daemon :18080 · Web GUI :8080
MOTD

install -d "${MNT}/etc/profile.d"
cat >"${MNT}/etc/profile.d/compute.sh" <<'PROFILE'
export COMPUTE_DAEMON="${COMPUTE_DAEMON:-127.0.0.1:18080}"
export PATH="/opt/compute/bin:$PATH"
alias compute=/opt/compute/bin/compute
PROFILE

# Bake the host automation SSH public key when available.
PUBKEY=""
for candidate in "${REPO_ROOT}/data/images/ssh/id_ed25519.pub" /root/.ssh/id_ed25519.pub /home/rashid/.ssh/id_ed25519.pub; do
    if [[ -f "$candidate" ]]; then PUBKEY="$(cat "$candidate")"; break; fi
done
if [[ -n "$PUBKEY" ]]; then
    echo "$PUBKEY" >"${MNT}/root/.ssh/authorized_keys"
    echo "$PUBKEY" >"${MNT}/home/compute/.ssh/authorized_keys"
    chmod 600 "${MNT}/root/.ssh/authorized_keys" "${MNT}/home/compute/.ssh/authorized_keys"
    chown -R 1000:1000 "${MNT}/home/compute"
fi

log "Cleaning up and unmounting"
rm -rf "${MNT}/var/cache/apt/archives/"*.deb 2>/dev/null || true
sync
cleanup
trap - EXIT

log "App VM image ready: ${OUT_IMG}"
