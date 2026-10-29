#!/usr/bin/env bash
set -euo pipefail

DEST_DIR="${1:-/var/lib/microforge/images}"
IMAGE_SIZE_MB="${2:-512}"
EXT4_PATH="${DEST_DIR}/alpine-3.20.ext4"
KEY_DIR="${DEST_DIR}/ssh"

mkdir -p "${DEST_DIR}" "${KEY_DIR}"

echo "==> Generating SSH keypair if not exists..."
if [ ! -f "${KEY_DIR}/id_ed25519" ]; then
    ssh-keygen -t ed25519 -N "" -f "${KEY_DIR}/id_ed25519" -C "mforge@internal"
fi
chmod 600 "${KEY_DIR}/id_ed25519"
chmod 644 "${KEY_DIR}/id_ed25519.pub"

PUB_KEY=$(cat "${KEY_DIR}/id_ed25519.pub")

echo "==> Building base Alpine container with SSH and networking..."
CONTAINER_ID=$(docker create alpine:3.20 /bin/sh)

WORKDIR=$(mktemp -d /tmp/mforge-rootfs-XXXXXX)
trap 'rm -rf "${WORKDIR}"; docker rm -f "${CONTAINER_ID}" >/dev/null 2>&1 || true' EXIT

docker export "${CONTAINER_ID}" | tar -x -C "${WORKDIR}"

echo "==> Installing packages and configuring inside rootfs..."
docker run --rm \
    -v "${WORKDIR}:/mnt/rootfs" \
    alpine:3.20 sh -c '
    apk add --root /mnt/rootfs --no-cache \
        openrc \
        openssh-server \
        bash \
        curl \
        iproute2 \
        ca-certificates

    # Configure root password and SSH
    echo "root:root" | chpasswd --root /mnt/rootfs

    # Enable SSH root login
    mkdir -p /mnt/rootfs/etc/ssh
    sed -i "s/#PermitRootLogin.*/PermitRootLogin yes/" /mnt/rootfs/etc/ssh/sshd_config 2>/dev/null || true
    echo "PermitRootLogin yes" >> /mnt/rootfs/etc/ssh/sshd_config
    echo "PasswordAuthentication yes" >> /mnt/rootfs/etc/ssh/sshd_config

    # Setup authorized_keys
    mkdir -p /mnt/rootfs/root/.ssh
    chmod 700 /mnt/rootfs/root/.ssh
'

# Inject public key
echo "${PUB_KEY}" > "${WORKDIR}/root/.ssh/authorized_keys"
chmod 600 "${WORKDIR}/root/.ssh/authorized_keys"

# Configure DNS
cat << 'RESOLV' > "${WORKDIR}/etc/resolv.conf"
nameserver 1.1.1.1
nameserver 8.8.8.8
RESOLV

# Configure startup script for OpenRC / Firecracker
mkdir -p "${WORKDIR}/etc/runlevels/default"
ln -sf /etc/init.d/sshd "${WORKDIR}/etc/runlevels/default/sshd"

# Add network initialization script
cat << 'INITHK' > "${WORKDIR}/etc/init.d/mforge-net"
#!/sbin/openrc-run

description="Initialize Firecracker VM Network"

start() {
    ebegin "Configuring network interfaces"
    ip link set lo up
    ip link set eth0 up 2>/dev/null || true
    eend 0
}
INITHK
chmod +x "${WORKDIR}/etc/init.d/mforge-net"
ln -sf /etc/init.d/mforge-net "${WORKDIR}/etc/runlevels/default/mforge-net"

# Ensure /etc/inittab spawns getty on ttyS0 (Firecracker serial console)
if grep -q "ttyS0" "${WORKDIR}/etc/inittab"; then
    sed -i "s/#ttyS0::respawn.*/ttyS0::respawn:\/sbin\/getty -L 115200 ttyS0 vt100/" "${WORKDIR}/etc/inittab"
else
    echo "ttyS0::respawn:/sbin/getty -L 115200 ttyS0 vt100" >> "${WORKDIR}/etc/inittab"
fi

echo "==> Creating ext4 filesystem image (${IMAGE_SIZE_MB}MB)..."
dd if=/dev/zero of="${EXT4_PATH}" bs=1M count="${IMAGE_SIZE_MB}" status=none
mkfs.ext4 -F -q -L "rootfs" "${EXT4_PATH}"

MOUNTDIR=$(mktemp -d /tmp/mforge-mnt-XXXXXX)
sudo mount -o loop "${EXT4_PATH}" "${MOUNTDIR}"
cp -a "${WORKDIR}/." "${MOUNTDIR}/"
# Generate host keys using chroot inside mount
sudo chroot "${MOUNTDIR}" /usr/bin/ssh-keygen -A
sudo umount "${MOUNTDIR}"
rm -rf "${MOUNTDIR}"

echo "==> Rootfs created successfully at ${EXT4_PATH}!"
echo "==> SSH private key: ${KEY_DIR}/id_ed25519"
