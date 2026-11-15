#!/usr/bin/env bash
# build-ubuntu-ci-image.sh — Build a bootable Ubuntu 24.04 rootfs for Firecracker
# from the Firecracker CI squashfs image, with SSH, sudo, and developer tooling.
#
# Usage: sudo ./scripts/build-ubuntu-ci-image.sh [output.ext4] [size_mb]
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_IMG="${1:-${REPO_ROOT}/data/images/ubuntu-24.04.ext4}"
SIZE_MB="${2:-2560}"
SQUASHFS="${UBUNTU_SQUASHFS:-/tmp/ubuntu-24.04.squashfs}"
MNT="$(mktemp -d /tmp/compute-ubuntu-mnt-XXXXXX)"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
cleanup() {
    for d in dev/pts dev proc sys; do mountpoint -q "${MNT}/${d}" && umount -l "${MNT}/${d}" || true; done
    mountpoint -q "${MNT}" && umount -l "${MNT}" || true
    rmdir "${MNT}" 2>/dev/null || true
}
trap cleanup EXIT
[[ $EUID -eq 0 ]] || { echo "run as root" >&2; exit 1; }
[[ -f "${SQUASHFS}" ]] || { echo "missing ${SQUASHFS}" >&2; exit 1; }

log "Creating ${SIZE_MB}MB ext4 image ${OUT_IMG}"
rm -f "${OUT_IMG}"
truncate -s "${SIZE_MB}M" "${OUT_IMG}"
mkfs.ext4 -F -q -L compute-rootfs "${OUT_IMG}"
mount -o loop "${OUT_IMG}" "${MNT}"

log "Extracting Ubuntu 24.04 CI rootfs"
unsquashfs -f -d "${MNT}" "${SQUASHFS}" >/tmp/unsquashfs.log 2>&1

mount -t proc proc "${MNT}/proc"
mount -t sysfs sysfs "${MNT}/sys"
mount --bind /dev "${MNT}/dev"
mount --bind /dev/pts "${MNT}/dev/pts"

log "Creating compute user (uid 1000) and group"
chroot "${MNT}" /bin/bash -c '
    set -e
    if id ubuntu >/dev/null 2>&1; then
        usermod -l compute ubuntu
        groupmod -n compute ubuntu
        usermod -d /home/compute -m compute
        usermod -s /bin/bash compute
    fi
    getent group sudo >/dev/null || groupadd -g 27 sudo
    usermod -aG sudo compute
    usermod -p '!' compute
    usermod -p '!' root
    mkdir -p /etc/sudoers.d
    echo "compute ALL=(ALL) NOPASSWD: ALL" > /etc/sudoers.d/compute
    chmod 0440 /etc/sudoers.d/compute
'

log "Installing sudo package"
# The deb is fetched by the caller (or cached); try apt as a fallback.
if [[ -f /tmp/sudo.deb ]]; then
    cp /tmp/sudo.deb "${MNT}/tmp/sudo.deb"
    chroot "${MNT}" /usr/bin/dpkg -i /tmp/sudo.deb || true
else
    chroot "${MNT}" /bin/bash -c 'export DEBIAN_FRONTEND=noninteractive; apt-get update -qq && apt-get install -y -qq sudo' || true
fi

log "Configuring SSH and init"
chroot "${MNT}" /bin/bash -c '
    mkdir -p /root/.ssh /home/compute/.ssh /run/sshd
    chmod 700 /root/.ssh /home/compute/.ssh
    chown -R compute:compute /home/compute
    sed -i "s/^#\?PermitRootLogin.*/PermitRootLogin yes/" /etc/ssh/sshd_config
    sed -i "s/^#\?PasswordAuthentication.*/PasswordAuthentication no/" /etc/ssh/sshd_config
    sed -i "s/^#\?PubkeyAuthentication.*/PubkeyAuthentication yes/" /etc/ssh/sshd_config
    sed -i "s/^#\?UsePAM.*/UsePAM no/" /etc/ssh/sshd_config
    grep -q "^UsePAM no" /etc/ssh/sshd_config || echo "UsePAM no" >> /etc/ssh/sshd_config
    grep -q "^KbdInteractiveAuthentication no" /etc/ssh/sshd_config || echo "KbdInteractiveAuthentication no" >> /etc/ssh/sshd_config
    ssh-keygen -A
    rm -f /sbin/init
'

# Custom init: fast boot without systemd, parses the kernel ip= parameter.
cat >"${MNT}/sbin/init" <<'INIT'
#!/bin/bash
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
mount -t proc proc /proc 2>/dev/null || true
mount -t sysfs sysfs /sys 2>/dev/null || true
mount -t devtmpfs devtmpfs /dev 2>/dev/null || true
mkdir -p /dev/pts /dev/shm /run /run/lock
mount -t devpts devpts /dev/pts -o mode=620,ptmxmode=666,gid=5 2>/dev/null || true
mount -t tmpfs tmpfs /dev/shm 2>/dev/null || true
mount -t tmpfs tmpfs /run 2>/dev/null || true
mkdir -p /run/sshd /run/lock
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
    [ -n "$KHST" ] && printf '127.0.0.1 localhost %s\n' "$KHST" > /etc/hosts || true
fi
printf 'nameserver 1.1.1.1\nnameserver 8.8.8.8\n' > /etc/resolv.conf
/usr/sbin/sshd 2>/dev/null || true
while true; do /sbin/getty -L 115200 ttyS0 vt100 || sleep 1; done
INIT
chmod +x "${MNT}/sbin/init"

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
log "Ubuntu 24.04 image ready: ${OUT_IMG}"
