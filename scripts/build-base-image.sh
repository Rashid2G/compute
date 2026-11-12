#!/usr/bin/env bash
# build-base-image.sh — Build a bootable Firecracker base rootfs (Ubuntu or Debian)
# with SSH enabled and the Compute guest init.
#
# Usage: sudo ./scripts/build-base-image.sh <ubuntu|debian> <output.ext4> [size_mb]
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DISTRO="${1:-ubuntu}"
OUT_IMG="${2:-${REPO_ROOT}/data/images/ubuntu-24.04.ext4}"
SIZE_MB="${3:-2048}"
MNT="$(mktemp -d /tmp/compute-base-mnt-XXXXXX)"

case "${DISTRO}" in
  ubuntu)
    SUITE="${UBUNTU_SUITE:-noble}"
    MIRROR="${UBUNTU_MIRROR:-http://archive.ubuntu.com/ubuntu}"
    KEYRING="/usr/share/keyrings/ubuntu-archive-keyring.gpg"
    ;;
  debian)
    SUITE="${DEBIAN_SUITE:-trixie}"
    MIRROR="${DEBIAN_MIRROR:-http://deb.debian.org/debian}"
    KEYRING="/usr/share/keyrings/debian-archive-keyring.gpg"
    ;;
  *) echo "unknown distro: ${DISTRO}" >&2; exit 2 ;;
esac

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
cleanup() {
    for d in dev/pts dev proc sys; do mountpoint -q "${MNT}/${d}" && umount -l "${MNT}/${d}" || true; done
    mountpoint -q "${MNT}" && umount -l "${MNT}" || true
    rmdir "${MNT}" 2>/dev/null || true
}
trap cleanup EXIT

[[ $EUID -eq 0 ]] || { echo "run as root" >&2; exit 1; }
command -v debootstrap >/dev/null || { echo "debootstrap not installed" >&2; exit 1; }
[[ -f "${KEYRING}" ]] || { echo "missing keyring ${KEYRING}" >&2; exit 1; }

log "Creating ${SIZE_MB}MB image ${OUT_IMG}"
mkdir -p "$(dirname "${OUT_IMG}")"
rm -f "${OUT_IMG}"
truncate -s "${SIZE_MB}M" "${OUT_IMG}"
mkfs.ext4 -F -q -L compute-rootfs "${OUT_IMG}"
mount -o loop "${OUT_IMG}" "${MNT}"

log "Bootstrapping ${DISTRO} ${SUITE}"
debootstrap --variant=minbase --arch=amd64 --keyring="${KEYRING}" "${SUITE}" "${MNT}" "${MIRROR}" >/tmp/debootstrap-base.log 2>&1
mount -t proc proc "${MNT}/proc"
mount -t sysfs sysfs "${MNT}/sys"
mount --bind /dev "${MNT}/dev"
mount --bind /dev/pts "${MNT}/dev/pts"

log "Installing guest packages"
if [[ "${DISTRO}" == "ubuntu" ]]; then
  cat >"${MNT}/etc/apt/sources.list" <<EOF
deb ${MIRROR} ${SUITE} main universe
deb ${MIRROR} ${SUITE}-updates main universe
deb ${MIRROR} ${SUITE}-security main universe
EOF
else
  cat >"${MNT}/etc/apt/sources.list" <<EOF
deb ${MIRROR} ${SUITE} main
deb ${MIRROR} ${SUITE}-updates main
deb ${MIRROR}-security ${SUITE}-security main
EOF
fi
chroot "${MNT}" /bin/bash -c 'export DEBIAN_FRONTEND=noninteractive; apt-get update -qq && apt-get install -y -qq --no-install-recommends openssh-server sudo iproute2 procps curl ca-certificates jq less nano util-linux iputils-ping tzdata netbase git python3 nodejs npm && rm -rf /var/lib/apt/lists/*' >>/tmp/debootstrap-base.log 2>&1

log "Configuring users and SSH"
chroot "${MNT}" /bin/bash -c '
    if ! id compute >/dev/null 2>&1; then useradd -m -s /bin/bash -u 1000 compute; fi
    usermod -aG sudo compute
    mkdir -p /etc/sudoers.d
    echo "compute ALL=(ALL) NOPASSWD: ALL" > /etc/sudoers.d/compute
    chmod 0440 /etc/sudoers.d/compute
    usermod -p '!' root
    usermod -p '!' compute
    mkdir -p /root/.ssh /home/compute/.ssh /run/sshd
    chmod 700 /root/.ssh /home/compute/.ssh
    chmod 1777 /tmp /var/tmp
    chmod 1777 /run/lock /dev/shm 2>/dev/null || true
    chown -R compute:compute /home/compute
    sed -i "s/^#\?PermitRootLogin.*/PermitRootLogin yes/" /etc/ssh/sshd_config
    sed -i "s/^#\?PasswordAuthentication.*/PasswordAuthentication no/" /etc/ssh/sshd_config
    sed -i "s/^#\?PubkeyAuthentication.*/PubkeyAuthentication yes/" /etc/ssh/sshd_config
    sed -i "s/^#\?UsePAM.*/UsePAM no/" /etc/ssh/sshd_config
    grep -q "^UsePAM no" /etc/ssh/sshd_config || echo "UsePAM no" >> /etc/ssh/sshd_config
    grep -q "^KbdInteractiveAuthentication no" /etc/ssh/sshd_config || echo "KbdInteractiveAuthentication no" >> /etc/ssh/sshd_config
    ssh-keygen -A
'

cat >"${MNT}/sbin/init" <<'INIT'
#!/bin/bash
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
mount -t proc proc /proc 2>/dev/null || true
mount -t sysfs sysfs /sys 2>/dev/null || true
mount -t devtmpfs devtmpfs /dev 2>/dev/null || true
mkdir -p /dev/pts /dev/shm /run /run/sshd /run/lock
mount -t devpts devpts /dev/pts -o mode=620,ptmxmode=666,gid=5 2>/dev/null || true
mount -t tmpfs tmpfs /dev/shm 2>/dev/null || true
mount -t tmpfs tmpfs /run 2>/dev/null || true
mkdir -p /run/sshd /run/lock
chmod 1777 /dev/shm /run/lock 2>/dev/null || true
ip link set lo up 2>/dev/null || true
ip link set eth0 up 2>/dev/null || true
IP_SPEC=$(tr ' ' '\n' < /proc/cmdline | sed -n 's/^ip=//p' | head -n1)
if [ -n "$IP_SPEC" ] && [ "$IP_SPEC" != "dhcp" ]; then
    CIP=$(echo "$IP_SPEC" | cut -d: -f1); GW=$(echo "$IP_SPEC" | cut -d: -f3)
    MASK=$(echo "$IP_SPEC" | cut -d: -f4); KHST=$(echo "$IP_SPEC" | cut -d: -f5)
    DEV=$(echo "$IP_SPEC" | cut -d: -f6); [ -z "$DEV" ] && DEV="eth0"
    PREFIX=24
    case "$MASK" in 255.255.255.0) PREFIX=24;; 255.255.0.0) PREFIX=16;; 255.0.0.0) PREFIX=8;; [0-9]*) PREFIX="$MASK";; esac
    ip addr flush dev "$DEV" 2>/dev/null || true
    ip addr add "${CIP}/${PREFIX}" dev "$DEV" 2>/dev/null || true
    [ -n "$GW" ] && ip route add default via "$GW" dev "$DEV" 2>/dev/null || true
    [ -n "$KHST" ] && hostname "$KHST" 2>/dev/null || true
fi
printf 'nameserver 1.1.1.1\nnameserver 8.8.8.8\n' > /etc/resolv.conf
/usr/sbin/sshd 2>/dev/null || true
while true; do /sbin/getty -L 115200 ttyS0 vt100 || sleep 1; done
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

PUBKEY=""
for candidate in "${REPO_ROOT}/data/images/ssh/id_ed25519.pub" /root/.ssh/id_ed25519.pub; do
    [[ -f "$candidate" ]] && { PUBKEY="$(cat "$candidate")"; break; }
done
if [[ -n "$PUBKEY" ]]; then
    echo "$PUBKEY" >"${MNT}/root/.ssh/authorized_keys"
    echo "$PUBKEY" >"${MNT}/home/compute/.ssh/authorized_keys"
    chmod 600 "${MNT}/root/.ssh/authorized_keys" "${MNT}/home/compute/.ssh/authorized_keys"
    chown -R 1000:1000 "${MNT}/home/compute"
fi

sync
cleanup
trap - EXIT
log "Base image ready: ${OUT_IMG}"
