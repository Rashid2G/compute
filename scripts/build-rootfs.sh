#!/usr/bin/env bash
set -euo pipefail

DEST_DIR="${1:-/var/lib/compute/images}"
IMAGE_SIZE_MB="${2:-4096}"
EXT4_PATH="${DEST_DIR}/ubuntu-24.04.ext4"
KEY_DIR="${DEST_DIR}/ssh"

mkdir -p "${DEST_DIR}" "${KEY_DIR}"

echo "==> [Step 1/5] Generating internal SSH keypair if not exists..."
if [ ! -f "${KEY_DIR}/id_ed25519" ]; then
    ssh-keygen -t ed25519 -N "" -f "${KEY_DIR}/id_ed25519" -C "compute@internal"
fi
chmod 600 "${KEY_DIR}/id_ed25519"
chmod 644 "${KEY_DIR}/id_ed25519.pub"
PUB_KEY=$(cat "${KEY_DIR}/id_ed25519.pub")

echo "==> [Step 2/5] Building Ubuntu 24 base image..."
BUILD_DIR=$(mktemp -d /tmp/compute-u24-build-XXXXXX)
WORKDIR=""
CONTAINER_ID=""

cleanup() {
    rm -rf "${BUILD_DIR}" 2>/dev/null || sudo rm -rf "${BUILD_DIR}" 2>/dev/null || true
    if [ -n "${CONTAINER_ID}" ]; then
        docker rm -f "${CONTAINER_ID}" >/dev/null 2>&1 || true
    fi
    if [ -n "${WORKDIR}" ] && [ -d "${WORKDIR}" ]; then
        rm -rf "${WORKDIR}" 2>/dev/null || sudo rm -rf "${WORKDIR}" 2>/dev/null || true
    fi
}
trap cleanup EXIT

cat << 'DOCKERFILE' > "${BUILD_DIR}/Dockerfile"
FROM ubuntu:24.04
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
    openssh-server \
    sudo \
    curl \
    wget \
    git \
    jq \
    iproute2 \
    procps \
    ca-certificates \
    python3 \
    nodejs \
    npm \
    util-linux \
    net-tools \
    build-essential \
    && rm -rf /var/lib/apt/lists/*

# Compile instant poweroff / reboot binaries
RUN echo '#include <unistd.h>\n#include <sys/reboot.h>\nint main(){ sync(); reboot(RB_POWER_OFF); return 0; }' > /tmp/poweroff.c && \
    gcc -O2 /tmp/poweroff.c -o /sbin/poweroff && \
    echo '#include <unistd.h>\n#include <sys/reboot.h>\nint main(){ sync(); reboot(RB_AUTOBOOT); return 0; }' > /tmp/reboot.c && \
    gcc -O2 /tmp/reboot.c -o /sbin/reboot && \
    rm -f /tmp/poweroff.c /tmp/reboot.c

# Configure user compute (UID 1000) with passwordless sudo
RUN (userdel -r ubuntu 2>/dev/null || true) && \
    useradd -m -s /bin/bash -u 1000 compute && \
    usermod -aG sudo compute && \
    mkdir -p /etc/sudoers.d && \
    echo "compute ALL=(ALL) NOPASSWD: ALL" > /etc/sudoers.d/compute && \
    chmod 0440 /etc/sudoers.d/compute && \
    usermod -p '!' root && \
    usermod -p '!' compute

# Configure SSH daemon
RUN mkdir -p /var/run/sshd /root/.ssh /home/compute/.ssh && \
    sed -i 's/#PermitRootLogin.*/PermitRootLogin yes/' /etc/ssh/sshd_config 2>/dev/null || true && \
    sed -i 's/UsePAM yes/UsePAM no/' /etc/ssh/sshd_config 2>/dev/null || true && \
    echo "PermitRootLogin yes" >> /etc/ssh/sshd_config && \
    echo "PasswordAuthentication no" >> /etc/ssh/sshd_config && \
    echo "KbdInteractiveAuthentication no" >> /etc/ssh/sshd_config && \
    echo "PubkeyAuthentication yes" >> /etc/ssh/sshd_config && \
    echo "UsePAM no" >> /etc/ssh/sshd_config && \
    echo "StrictModes no" >> /etc/ssh/sshd_config

DOCKERFILE

docker build -t compute-ubuntu:24.04 "${BUILD_DIR}"

echo "==> [Step 3/5] Exporting rootfs and configuring Firecracker init..."
CONTAINER_ID=$(docker create compute-ubuntu:24.04 /bin/bash)
WORKDIR=$(mktemp -d /tmp/compute-u24-rootfs-XXXXXX)
docker export "${CONTAINER_ID}" | tar -x -C "${WORKDIR}"

# Inject SSH authorized keys
echo "${PUB_KEY}" > "${WORKDIR}/root/.ssh/authorized_keys"
echo "${PUB_KEY}" > "${WORKDIR}/home/compute/.ssh/authorized_keys"
chmod 700 "${WORKDIR}/root/.ssh" "${WORKDIR}/home/compute/.ssh"
chmod 600 "${WORKDIR}/root/.ssh/authorized_keys" "${WORKDIR}/home/compute/.ssh/authorized_keys"
# Set ownership for compute
chown -R 1000:1000 "${WORKDIR}/home/compute"

# Configure friendly shell prompt and profile
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

# Configure MOTD
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

# Configure DNS
cat << 'RESOLV' > "${WORKDIR}/etc/resolv.conf"
nameserver 1.1.1.1
nameserver 8.8.8.8
RESOLV

# Configure fstab
cat << 'FSTAB' > "${WORKDIR}/etc/fstab"
/dev/vda / ext4 defaults,noatime 0 1
devpts /dev/pts devpts gid=5,mode=620,ptmxmode=666 0 0
proc /proc proc defaults 0 0
sysfs /sys sysfs defaults 0 0
tmpfs /dev/shm tmpfs defaults 0 0
tmpfs /run tmpfs defaults 0 0
FSTAB

# Write Firecracker /sbin/init script
cat << 'INITSCRIPT' > "${WORKDIR}/sbin/init"
#!/bin/bash
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
mount -t proc proc /proc 2>/dev/null || true
mount -t sysfs sysfs /sys 2>/dev/null || true
mount -t devtmpfs devtmpfs /dev 2>/dev/null || true
mount -t tmpfs tmpfs /dev/shm 2>/dev/null || true
mount -t tmpfs tmpfs /run 2>/dev/null || true
mkdir -p /dev/pts /dev/shm /run/sshd /run/lock
chmod 0755 /run/sshd
mount -t devpts devpts /dev/pts -o mode=620,ptmxmode=666,gid=5 2>/dev/null || true

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
while true; do
    /sbin/getty -L 115200 ttyS0 vt100 || sleep 1
done
INITSCRIPT
chmod +x "${WORKDIR}/sbin/init"

echo "==> [Step 4/5] Formatting ext4 rootfs image (${IMAGE_SIZE_MB}MB)..."
# Create sparse file
rm -f "${EXT4_PATH}"
truncate -s "${IMAGE_SIZE_MB}M" "${EXT4_PATH}"
mkfs.ext4 -F -q -L "rootfs" "${EXT4_PATH}"

# Mount and copy files
MOUNTDIR=$(mktemp -d /tmp/compute-u24-mnt-XXXXXX)
sudo mount -o loop "${EXT4_PATH}" "${MOUNTDIR}"
sudo cp -a "${WORKDIR}/." "${MOUNTDIR}/"
# Ensure root directory has standard 0755 permissions
sudo chmod 755 "${MOUNTDIR}"
# Ensure host SSH keys are generated inside the image
sudo chroot "${MOUNTDIR}" /usr/bin/ssh-keygen -A 2>/dev/null || true
sudo umount "${MOUNTDIR}"
rm -rf "${MOUNTDIR}"

# Create convenient alias symlink
ln -sf ubuntu-24.04.ext4 "${DEST_DIR}/ubuntu-24.ext4"
chmod 644 "${EXT4_PATH}" "${DEST_DIR}/ubuntu-24.ext4" 2>/dev/null || true

echo "==> [Step 5/5] Rootfs image created successfully at ${EXT4_PATH}!"
echo "==> Ubuntu 24.04 LTS (Noble Numbat) image ready with Node.js, Python 3, SSH, sudo, git, curl, jq!"
