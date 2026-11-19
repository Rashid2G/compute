#!/usr/bin/env bash
# Build an Ubuntu 24.04 ext4 root filesystem for Firecracker microVMs without
# requiring Docker-in-Docker or host loop-mount tools. Designed to run via
# `computed init --images` or directly as root on Linux.
#
# Usage:
#   build-rootfs-chroot.sh <output.ext4|images_dir> [size_mb] [ssh_pubkey_path]
set -euo pipefail

TARGET="${1:-/var/lib/compute/images/ubuntu-24.04.ext4}"
IMAGE_SIZE_MB="${2:-${COMPUTE_ROOTFS_SIZE_MB:-2048}}"
SSH_PUBKEY_PATH="${3:-}"

if [[ "${TARGET}" == *.ext4 ]] || [[ "${TARGET}" == *.img ]]; then
    EXT4_PATH="${TARGET}"
    DEST_DIR="$(dirname "${EXT4_PATH}")"
else
    DEST_DIR="${TARGET}"
    EXT4_PATH="${DEST_DIR}/ubuntu-24.04.ext4"
fi

if ! [[ "${IMAGE_SIZE_MB}" =~ ^[0-9]+$ ]] || [ "${IMAGE_SIZE_MB}" -lt 1536 ]; then
    echo "COMPUTE_ROOTFS_SIZE_MB must be an integer >= 1536 (got '${IMAGE_SIZE_MB}')" >&2
    exit 2
fi

case "$(uname -m)" in
    x86_64 | amd64) UBUNTU_ARCH="amd64" ;;
    aarch64 | arm64) UBUNTU_ARCH="arm64" ;;
    *)
        echo "unsupported architecture: $(uname -m) (expected x86_64 or aarch64)" >&2
        exit 2
        ;;
esac

for cmd in curl sha256sum tar mkfs.ext4 truncate chroot mount umount; do
    if ! command -v "${cmd}" >/dev/null 2>&1; then
        echo "missing required command '${cmd}' for rootfs build" >&2
        exit 1
    fi
done

mkdir -p "${DEST_DIR}"

WORKDIR=""
TMP_TARBALL=""
TMP_EXT4="${DEST_DIR}/.$(basename "${EXT4_PATH}").part.$$"

unmount_chroot() {
    if [ -n "${WORKDIR}" ] && [ -d "${WORKDIR}" ]; then
        umount -lf "${WORKDIR}/dev/pts" 2>/dev/null || umount "${WORKDIR}/dev/pts" 2>/dev/null || true
        umount -lf "${WORKDIR}/dev" 2>/dev/null || umount "${WORKDIR}/dev" 2>/dev/null || true
        umount -lf "${WORKDIR}/sys" 2>/dev/null || umount "${WORKDIR}/sys" 2>/dev/null || true
        umount -lf "${WORKDIR}/proc" 2>/dev/null || umount "${WORKDIR}/proc" 2>/dev/null || true
    fi
}

cleanup() {
    unmount_chroot
    rm -f "${TMP_EXT4}" 2>/dev/null || true
    rm -f "${TMP_TARBALL}" 2>/dev/null || true
    if [ -n "${WORKDIR}" ] && [ -d "${WORKDIR}" ]; then
        rm -rf "${WORKDIR}" 2>/dev/null || true
    fi
}
trap cleanup EXIT

BASE_URL="${COMPUTE_UBUNTU_BASE_URL:-https://cdimage.ubuntu.com/ubuntu-base/releases/24.04/release}"
echo "==> Resolving Ubuntu 24.04 (${UBUNTU_ARCH}) base tarball..."
SUMS="$(curl --fail --location --retry 3 --retry-delay 2 --connect-timeout 20 -fsSL "${BASE_URL}/SHA256SUMS")"
LINE="$(printf '%s\n' "${SUMS}" | grep -E "ubuntu-base-24\.04(\.[0-9]+)?-base-${UBUNTU_ARCH}\.tar\.gz$" | sort -V | tail -n1 || true)"
if [ -z "${LINE}" ]; then
    echo "could not locate ubuntu-base 24.04 (${UBUNTU_ARCH}) entry in ${BASE_URL}/SHA256SUMS" >&2
    exit 1
fi
EXPECTED_SHA="$(printf '%s\n' "${LINE}" | awk '{print $1}')"
TARBALL_NAME="$(printf '%s\n' "${LINE}" | awk '{print $2}' | sed 's/^\*//')"

TMP_TARBALL="$(mktemp /tmp/ubuntu-base-XXXXXX)"
echo "==> Downloading ${TARBALL_NAME}..."
curl --fail --location --retry 3 --retry-delay 2 --connect-timeout 20 -fsSL "${BASE_URL}/${TARBALL_NAME}" -o "${TMP_TARBALL}"
ACTUAL_SHA="$(sha256sum "${TMP_TARBALL}" | awk '{print $1}')"
if [ "${ACTUAL_SHA}" != "${EXPECTED_SHA}" ]; then
    echo "checksum mismatch for ${TARBALL_NAME}: expected ${EXPECTED_SHA}, got ${ACTUAL_SHA}" >&2
    exit 1
fi

WORKDIR="$(mktemp -d /tmp/compute-rootfs-XXXXXX)"
echo "==> Extracting ${TARBALL_NAME} into chroot..."
tar -xzf "${TMP_TARBALL}" -C "${WORKDIR}"
rm -f "${TMP_TARBALL}"
TMP_TARBALL=""

# Ensure DNS works inside the chroot while installing Ubuntu packages.
rm -f "${WORKDIR}/etc/resolv.conf"
if [ -s /etc/resolv.conf ]; then
    cp /etc/resolv.conf "${WORKDIR}/etc/resolv.conf"
else
    printf 'nameserver 1.1.1.1\nnameserver 8.8.8.8\n' > "${WORKDIR}/etc/resolv.conf"
fi

# Prevent package post-install scripts from starting services inside the build chroot.
mkdir -p "${WORKDIR}/usr/sbin"
cat << 'EOF' > "${WORKDIR}/usr/sbin/policy-rc.d"
#!/bin/sh
exit 101
EOF
chmod +x "${WORKDIR}/usr/sbin/policy-rc.d"

mount -t proc proc "${WORKDIR}/proc"
mount -t sysfs sysfs "${WORKDIR}/sys"
mount --bind /dev "${WORKDIR}/dev"
mkdir -p "${WORKDIR}/dev/pts"
mount -t devpts devpts "${WORKDIR}/dev/pts"

echo "==> Installing Ubuntu 24.04 guest packages (this runs once on first setup)..."
chroot "${WORKDIR}" /usr/bin/env -i \
    HOME=/root \
    TERM=xterm-256color \
    PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    DEBIAN_FRONTEND=noninteractive \
    /bin/bash -ec '
        apt-get update
        apt-get install -y --no-install-recommends \
            iputils-ping \
            bind9-dnsutils \
            netcat-openbsd \
            traceroute \
            iproute2 \
            net-tools \
            curl \
            wget \
            less \
            nano \
            vim-tiny \
            htop \
            lsof \
            psmisc \
            file \
            unzip \
            zip \
            tar \
            gzip \
            xz-utils \
            rsync \
            openssh-server \
            openssh-client \
            jq \
            git \
            ca-certificates \
            sudo \
            procps \
            util-linux \
            bash \
            coreutils \
            findutils \
            grep \
            sed \
            gawk \
            hostname \
            locales \
            libcap2-bin \
            python3 \
            nodejs \
            npm \
            build-essential
        apt-get clean
        rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/*
        rm -f /usr/sbin/policy-rc.d

        # Ensure ping works for non-root users inside Firecracker microVMs
        chmod u+s /bin/ping /usr/bin/ping 2>/dev/null || true
        setcap cap_net_raw+ep /bin/ping 2>/dev/null || setcap cap_net_raw+ep /usr/bin/ping 2>/dev/null || true
        mkdir -p /etc/sysctl.d
        echo "net.ipv4.ping_group_range = 0 2147483647" > /etc/sysctl.d/99-compute.conf
        if [ -f /etc/sysctl.conf ]; then
            grep -q "^net.ipv4.ping_group_range" /etc/sysctl.conf || echo "net.ipv4.ping_group_range = 0 2147483647" >> /etc/sysctl.conf
        fi

        # Compile instant poweroff / reboot helpers
        rm -f /sbin/poweroff /sbin/reboot
        printf "#include <unistd.h>\n#include <sys/reboot.h>\nint main(){ sync(); reboot(RB_POWER_OFF); return 0; }\n" > /tmp/poweroff.c
        gcc -O2 /tmp/poweroff.c -o /sbin/poweroff
        printf "#include <unistd.h>\n#include <sys/reboot.h>\nint main(){ sync(); reboot(RB_AUTOBOOT); return 0; }\n" > /tmp/reboot.c
        gcc -O2 /tmp/reboot.c -o /sbin/reboot
        rm -f /tmp/poweroff.c /tmp/reboot.c

        # Configure user compute (UID 1000) with passwordless sudo
        (userdel -r ubuntu 2>/dev/null || true)
        if ! id -u compute >/dev/null 2>&1; then
            useradd -m -s /bin/bash -u 1000 compute
        fi
        usermod -aG sudo compute
        mkdir -p /etc/sudoers.d
        echo "compute ALL=(ALL) NOPASSWD: ALL" > /etc/sudoers.d/compute
        chmod 0440 /etc/sudoers.d/compute
        usermod -p "!" root
        usermod -p "!" compute

        # Configure SSH daemon and keys
        mkdir -p /var/run/sshd /run/sshd /root/.ssh /home/compute/.ssh
        sed -i "s/#PermitRootLogin.*/PermitRootLogin yes/" /etc/ssh/sshd_config 2>/dev/null || true
        sed -i "s/UsePAM yes/UsePAM no/" /etc/ssh/sshd_config 2>/dev/null || true
        grep -q "^PermitRootLogin " /etc/ssh/sshd_config || echo "PermitRootLogin yes" >> /etc/ssh/sshd_config
        grep -q "^PasswordAuthentication " /etc/ssh/sshd_config || echo "PasswordAuthentication no" >> /etc/ssh/sshd_config
        grep -q "^KbdInteractiveAuthentication " /etc/ssh/sshd_config || echo "KbdInteractiveAuthentication no" >> /etc/ssh/sshd_config
        grep -q "^PubkeyAuthentication " /etc/ssh/sshd_config || echo "PubkeyAuthentication yes" >> /etc/ssh/sshd_config
        grep -q "^UsePAM " /etc/ssh/sshd_config || echo "UsePAM no" >> /etc/ssh/sshd_config
        grep -q "^StrictModes " /etc/ssh/sshd_config || echo "StrictModes no" >> /etc/ssh/sshd_config
        ssh-keygen -A
    '

if [ -n "${SSH_PUBKEY_PATH}" ] && [ -f "${SSH_PUBKEY_PATH}" ]; then
    PUB_KEY="$(cat "${SSH_PUBKEY_PATH}")"
    printf '%s\n' "${PUB_KEY}" > "${WORKDIR}/root/.ssh/authorized_keys"
    printf '%s\n' "${PUB_KEY}" > "${WORKDIR}/home/compute/.ssh/authorized_keys"
fi
chmod 700 "${WORKDIR}/root/.ssh" "${WORKDIR}/home/compute/.ssh"
[ -f "${WORKDIR}/root/.ssh/authorized_keys" ] && chmod 600 "${WORKDIR}/root/.ssh/authorized_keys"
[ -f "${WORKDIR}/home/compute/.ssh/authorized_keys" ] && chmod 600 "${WORKDIR}/home/compute/.ssh/authorized_keys"
chown -R 1000:1000 "${WORKDIR}/home/compute"

mkdir -p "${WORKDIR}/etc/profile.d"
cat << 'PROMPT' > "${WORKDIR}/etc/profile.d/00-prompt.sh"
if [ -n "$BASH_VERSION" ]; then
    if [ "$(id -u)" -eq 0 ]; then
        PS1="\[\033[01;31m\]\u@\h\[\033[00m\]:\[\033[01;34m\]\w\[\033[00m\]# "
    else
        PS1="\[\033[01;32m\]\u@\h\[\033[00m\]:\[\033[01;34m\]\w\[\033[00m\]\$ "
    fi
fi
export PS1
alias ls="ls --color=auto"
alias ll="ls -la --color=auto"
PROMPT

cat << 'MOTD' > "${WORKDIR}/etc/motd"
   __  ___ _               ____                       
  /  |/  /(_)cro____   / __/___   ____ ___ _ ___ 
 / /|_/ // // ___// _ \ / /_ / _ \ / __// _ `// -_)
/_/  /_//_//_/   \___//_/   \___//_/   \_, / \__/ 
                                       /___/      
Welcome to Compute Ubuntu 24.04 LTS (Noble Numbat) MicroVM!
• User:       compute (passwordless sudo enabled)
• Runtimes:   Node.js, npm, Python 3, bash, jq, curl, git
• Networking: Virtual NAT & DNS enabled
MOTD

cat << 'RESOLV' > "${WORKDIR}/etc/resolv.conf"
nameserver 1.1.1.1
nameserver 8.8.8.8
RESOLV

cat << 'FSTAB' > "${WORKDIR}/etc/fstab"
/dev/vda / ext4 defaults,noatime 0 1
devpts /dev/pts devpts gid=5,mode=620,ptmxmode=666 0 0
proc /proc proc defaults 0 0
sysfs /sys sysfs defaults 0 0
tmpfs /dev/shm tmpfs defaults 0 0
tmpfs /run tmpfs defaults 0 0
FSTAB

cat << 'INITSCRIPT' > "${WORKDIR}/sbin/init"
#!/bin/bash
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
mount -t proc proc /proc 2>/dev/null || true
mount -t sysfs sysfs /sys 2>/dev/null || true
mount -t devtmpfs devtmpfs /dev 2>/dev/null || true
mkdir -p /dev/pts /dev/shm
mount -t tmpfs -o mode=1777 tmpfs /dev/shm 2>/dev/null || true
mount -t tmpfs tmpfs /run 2>/dev/null || true
mkdir -p /run/sshd /run/lock
chmod 1777 /dev/shm /tmp /var/tmp /run/lock 2>/dev/null || true
chmod 0755 /run/sshd
mount -t devpts devpts /dev/pts -o mode=620,ptmxmode=666,gid=5 2>/dev/null || true

# Allow non-root users (including compute) to send ICMP echo requests
sysctl -w net.ipv4.ping_group_range="0 2147483647" >/dev/null 2>&1 || echo "0 2147483647" > /proc/sys/net/ipv4/ping_group_range 2>/dev/null || true

ip link set lo up 2>/dev/null || true

# Parse kernel ip= parameters (static or DHCP from main router)
ip link set eth0 up 2>/dev/null || true
if grep -q "ip=" /proc/cmdline; then
    IP_SPEC=$(grep -o "ip=[^ ]*" /proc/cmdline | head -n1 | cut -d= -f2)
    if [ "$IP_SPEC" = "dhcp" ] || [ "$IP_SPEC" = ":::::eth0:dhcp" ] || echo "$IP_SPEC" | grep -q "dhcp"; then
        udhcpc -i eth0 -n -q 2>/dev/null || dhclient -1 eth0 2>/dev/null || true
    else
        CIP=$(echo "$IP_SPEC" | cut -d: -f1)
        GW=$(echo "$IP_SPEC" | cut -d: -f3)
        MASK=$(echo "$IP_SPEC" | cut -d: -f4)
        KHST=$(echo "$IP_SPEC" | cut -d: -f5)
        DEV=$(echo "$IP_SPEC" | cut -d: -f6)
        [ -z "$DEV" ] && DEV="eth0"

        if [ -n "$KHST" ]; then
            hostname "$KHST"
            echo "$KHST" > /etc/hostname
            echo "127.0.0.1 localhost $KHST" > /etc/hosts
        fi

        if [ -n "$CIP" ] && [ "$CIP" != "dhcp" ]; then
            PREFIX=24
            case "$MASK" in
                255.255.255.255) PREFIX=32 ;;
                255.255.255.0)   PREFIX=24 ;;
                255.255.0.0)     PREFIX=16 ;;
                255.0.0.0)       PREFIX=8  ;;
                [0-9]*)          PREFIX="$MASK" ;;
            esac
            ip link set "$DEV" up 2>/dev/null || true
            ip addr flush dev "$DEV" 2>/dev/null || true
            ip addr add "${CIP}/${PREFIX}" dev "$DEV" 2>/dev/null || true
            if [ -n "$GW" ]; then
                ip route add default via "$GW" dev "$DEV" 2>/dev/null || true
            fi
        else
            udhcpc -i eth0 -n -q 2>/dev/null || dhclient -1 eth0 2>/dev/null || true
        fi
    fi
else
    udhcpc -i eth0 -n -q 2>/dev/null || dhclient -1 eth0 2>/dev/null || true
fi

# Ensure host keys exist
if [ ! -f /etc/ssh/ssh_host_ed25519_key ]; then
    ssh-keygen -A 2>/dev/null || true
fi

# Start SSH daemon
/usr/sbin/sshd

# Serial console getty loop
GETTY_BIN="/sbin/getty"
[ -x "${GETTY_BIN}" ] || GETTY_BIN="/sbin/agetty"
while true; do
    "${GETTY_BIN}" -L 115200 ttyS0 vt100 || sleep 1
done
INITSCRIPT
chmod +x "${WORKDIR}/sbin/init"
chmod 755 "${WORKDIR}"

# Unmount virtual filesystems cleanly before packing the rootfs tree.
unmount_chroot

echo "==> Formatting ext4 rootfs image (${IMAGE_SIZE_MB}MB sparse)..."
rm -f "${TMP_EXT4}"
truncate -s "${IMAGE_SIZE_MB}M" "${TMP_EXT4}"
mkfs.ext4 -F -q -d "${WORKDIR}" -L "rootfs" "${TMP_EXT4}"
chmod 644 "${TMP_EXT4}"
mv -f "${TMP_EXT4}" "${EXT4_PATH}"

if [ "$(basename "${EXT4_PATH}")" = "ubuntu-24.04.ext4" ]; then
    ln -sf ubuntu-24.04.ext4 "${DEST_DIR}/ubuntu-24.ext4"
fi

echo "==> Rootfs image ready at ${EXT4_PATH}"
