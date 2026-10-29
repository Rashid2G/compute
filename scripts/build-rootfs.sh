#!/usr/bin/env bash
set -euo pipefail

DEST_DIR="${1:-/var/lib/microforge/images}"
IMAGE_SIZE_MB="${2:-1024}"
EXT4_PATH="${DEST_DIR}/alpine-3.20.ext4"
KEY_DIR="${DEST_DIR}/ssh"

mkdir -p "${DEST_DIR}" "${KEY_DIR}"

echo "==> [Step 1/5] Generating internal SSH keypair if not exists..."
if [ ! -f "${KEY_DIR}/id_ed25519" ]; then
    ssh-keygen -t ed25519 -N "" -f "${KEY_DIR}/id_ed25519" -C "mforge@internal"
fi
chmod 600 "${KEY_DIR}/id_ed25519"
chmod 644 "${KEY_DIR}/id_ed25519.pub"

echo "==> [Step 2/5] Exporting base Alpine 3.20 container filesystem..."
CONTAINER_ID=$(docker create alpine:3.20 /bin/sh)
WORKDIR=$(mktemp -d /tmp/mforge-rootfs-XXXXXX)

cleanup() {
    docker rm -f "${CONTAINER_ID}" >/dev/null 2>&1 || true
    if [ -d "${WORKDIR}" ]; then
        sudo rm -rf "${WORKDIR}" 2>/dev/null || rm -rf "${WORKDIR}" 2>/dev/null || true
    fi
}
trap cleanup EXIT

docker export "${CONTAINER_ID}" | tar -x -C "${WORKDIR}"

echo "==> [Step 3/5] Installing serverless runtimes and configuring guest environment..."
docker run --rm \
    -v "${WORKDIR}:/mnt/rootfs" \
    -v "${KEY_DIR}:/ssh:ro" \
    alpine:3.20 sh -c '
    set -euo pipefail

    echo "    --> Installing Alpine serverless packages..."
    apk add --root /mnt/rootfs --no-cache \
        openrc \
        openssh-server \
        bash \
        bash-completion \
        curl \
        wget \
        ca-certificates \
        iproute2 \
        bind-tools \
        jq \
        git \
        tar \
        gzip \
        unzip \
        zip \
        procps \
        socat \
        netcat-openbsd \
        sudo \
        shadow \
        nodejs \
        npm \
        python3 \
        py3-pip \
        util-linux

    echo "    --> Configuring users and passwordless sudo..."
    # Non-root user "mforge" (UID 1000)
    chroot /mnt/rootfs addgroup -g 1000 mforge || true
    chroot /mnt/rootfs adduser -u 1000 -G mforge -s /bin/bash -D -h /home/mforge mforge || true
    chroot /mnt/rootfs adduser mforge wheel || true

    # Default login shell for root is bash
    sed -i "s|^root:.*|root:x:0:0:root:/root:/bin/bash|" /mnt/rootfs/etc/passwd

    # Passwordless sudo for mforge & wheel
    mkdir -p /mnt/rootfs/etc/sudoers.d
    cat << "SUDO_CONF" > /mnt/rootfs/etc/sudoers.d/mforge
mforge ALL=(ALL) NOPASSWD: ALL
%wheel ALL=(ALL) NOPASSWD: ALL
Defaults !requiretty
SUDO_CONF
    chmod 0440 /mnt/rootfs/etc/sudoers.d/mforge

    echo "root:root" | chroot /mnt/rootfs chpasswd
    echo "mforge:mforge" | chroot /mnt/rootfs chpasswd

    # OpenSSH Server configuration
    mkdir -p /mnt/rootfs/etc/ssh /mnt/rootfs/var/empty
    chown root:root /mnt/rootfs/var/empty /mnt/rootfs/home
    chmod 755 /mnt/rootfs/var/empty /mnt/rootfs/home
    chmod 755 /mnt/rootfs/var/empty
    sed -i "s/#PermitRootLogin.*/PermitRootLogin yes/" /mnt/rootfs/etc/ssh/sshd_config 2>/dev/null || true
    echo "PermitRootLogin yes" >> /mnt/rootfs/etc/ssh/sshd_config
    echo "PasswordAuthentication yes" >> /mnt/rootfs/etc/ssh/sshd_config
    echo "PubkeyAuthentication yes" >> /mnt/rootfs/etc/ssh/sshd_config
    echo "StrictModes no" >> /mnt/rootfs/etc/ssh/sshd_config
    echo "AuthorizedKeysFile %h/.ssh/authorized_keys .ssh/authorized_keys" >> /mnt/rootfs/etc/ssh/sshd_config

    echo "    --> Injecting authorized SSH keys for mforge and root..."
    PUB_KEY=$(cat /ssh/id_ed25519.pub)
    mkdir -p /mnt/rootfs/root/.ssh /mnt/rootfs/home/mforge/.ssh
    echo "${PUB_KEY}" > /mnt/rootfs/root/.ssh/authorized_keys
    echo "${PUB_KEY}" > /mnt/rootfs/home/mforge/.ssh/authorized_keys
    chmod 700 /mnt/rootfs/root/.ssh /mnt/rootfs/home/mforge/.ssh
    chmod 600 /mnt/rootfs/root/.ssh/authorized_keys /mnt/rootfs/home/mforge/.ssh/authorized_keys

    echo "    --> Configuring friendly shell prompt, colors, and aliases..."
    # System-wide bashrc with colored prompt
    mkdir -p /mnt/rootfs/etc/bash
    cat << "BASHRC" > /mnt/rootfs/etc/bash/bashrc
if [ -z "$PS1" ]; then
    return
fi

if [ "$(id -u)" -eq 0 ]; then
    PS1="\[\033[01;31m\]\u@\h\[\033[00m\]:\[\033[01;34m\]\w\[\033[00m\]# "
else
    PS1="\[\033[01;32m\]\u@\h\[\033[00m\]:\[\033[01;34m\]\w\[\033[00m\]\$ "
fi

alias ls="ls --color=auto"
alias ll="ls -la --color=auto"
alias grep="grep --color=auto"
BASHRC

    mkdir -p /mnt/rootfs/etc/profile.d
    cat << "PROMPT" > /mnt/rootfs/etc/profile.d/00-prompt.sh
if [ -n "$BASH_VERSION" ]; then
    if [ "$(id -u)" -eq 0 ]; then
        PS1="\[\033[01;31m\]\u@\h\[\033[00m\]:\[\033[01;34m\]\w\[\033[00m\]# "
    else
        PS1="\[\033[01;32m\]\u@\h\[\033[00m\]:\[\033[01;34m\]\w\[\033[00m\]\$ "
    fi
else
    MY_HOST="$(hostname 2>/dev/null || cat /etc/hostname 2>/dev/null || echo vm)"
    if [ "$(id -u)" -eq 0 ]; then
        PS1="$(whoami)@${MY_HOST}:\$PWD# "
    else
        PS1="$(whoami)@${MY_HOST}:\$PWD\$ "
    fi
fi
export PS1
PROMPT

    cat << "DOTRC" > /mnt/rootfs/home/mforge/.bashrc
[ -f /etc/bash/bashrc ] && . /etc/bash/bashrc
DOTRC
    cat << "DOTPROF" > /mnt/rootfs/home/mforge/.profile
[ -f ~/.bashrc ] && . ~/.bashrc
DOTPROF

    cat << "ROOTRC" > /mnt/rootfs/root/.bashrc
[ -f /etc/bash/bashrc ] && . /etc/bash/bashrc
ROOTRC
    cat << "ROOTPROF" > /mnt/rootfs/root/.profile
[ -f ~/.bashrc ] && . ~/.bashrc
ROOTPROF

    # Set ownership of mforge home
    chown -R 1000:1000 /mnt/rootfs/home/mforge

    echo "    --> Configuring MOTD banner and networking..."
    cat << "MOTD" > /mnt/rootfs/etc/motd
   __  ___ _               ____                       
  /  |/  /(_)cro____   / __/___   ____ ___ _ ___ 
 / /|_/ // // ___// _ \ / /_ / _ \ / __// _ `// -_)
/_/  /_//_//_/   \___//_/   \___//_/   \_, / \__/ 
                                       /___/      
Welcome to MicroForge Serverless MicroVM!
• User:       mforge (passwordless sudo enabled)
• Runtimes:   Node.js, Python 3, bash, jq, curl, git
• Networking: Virtual NAT & DNS enabled
MOTD

    cat << "RESOLV" > /mnt/rootfs/etc/resolv.conf
nameserver 1.1.1.1
nameserver 8.8.8.8
RESOLV

    mkdir -p /mnt/rootfs/etc/runlevels/default
    ln -sf /etc/init.d/sshd /mnt/rootfs/etc/runlevels/default/sshd

    cat << "INITHK" > /mnt/rootfs/etc/init.d/mforge-net
#!/sbin/openrc-run
description="Initialize Firecracker VM Network and Hostname"

start() {
    ebegin "Configuring network interfaces and hostname"
    ip link set lo up
    ip link set eth0 up 2>/dev/null || true

    if grep -q "ip=" /proc/cmdline; then
        KHST=$(grep -o "ip=[^ ]*" /proc/cmdline | cut -d: -f5)
        if [ -n "$KHST" ]; then
            hostname "$KHST"
            echo "$KHST" > /etc/hostname
        fi
    fi
    eend 0
}
INITHK
    chmod +x /mnt/rootfs/etc/init.d/mforge-net
    ln -sf /etc/init.d/mforge-net /mnt/rootfs/etc/runlevels/default/mforge-net

    if grep -q "ttyS0" /mnt/rootfs/etc/inittab; then
        sed -i "s/#ttyS0::respawn.*/ttyS0::respawn:\/sbin\/getty -L 115200 ttyS0 vt100/\" /mnt/rootfs/etc/inittab
    else
        echo "ttyS0::respawn:/sbin/getty -L 115200 ttyS0 vt100" >> /mnt/rootfs/etc/inittab
    fi
'

sudo umount "${EXT4_PATH}" 2>/dev/null || true
echo "==> [Step 4/5] Building ext4 filesystem image (${IMAGE_SIZE_MB}MB)..."
sudo dd if=/dev/zero of="${EXT4_PATH}" bs=1M count="${IMAGE_SIZE_MB}" status=none
sudo mkfs.ext4 -F -q -L "rootfs" "${EXT4_PATH}"

echo "==> [Step 5/5] Copying rootfs into ext4 filesystem and generating host SSH keys..."
MOUNTDIR=$(mktemp -d /tmp/mforge-mnt-XXXXXX)
sudo mount -o loop "${EXT4_PATH}" "${MOUNTDIR}"
sudo cp -a "${WORKDIR}/." "${MOUNTDIR}/"
# Ensure the filesystem root, tmp, and user home have correct permissions and ownership
sudo chown root:root "${MOUNTDIR}" "${MOUNTDIR}/home"
sudo chmod 755 "${MOUNTDIR}" "${MOUNTDIR}/home"
sudo chmod 1777 "${MOUNTDIR}/tmp" "${MOUNTDIR}/var/tmp" 2>/dev/null || true
sudo chown -R 1000:1000 "${MOUNTDIR}/home/mforge"
sudo chmod 750 "${MOUNTDIR}/home/mforge"
sudo chmod 700 "${MOUNTDIR}/home/mforge/.ssh"
sudo chmod 600 "${MOUNTDIR}/home/mforge/.ssh/authorized_keys"
sudo chroot "${MOUNTDIR}" /usr/bin/ssh-keygen -A
sudo umount "${MOUNTDIR}"
rm -rf "${MOUNTDIR}"

echo "==> SUCCESS: Rootfs image ready at ${EXT4_PATH}!"
echo "==> SSH private key: ${KEY_DIR}/id_ed25519"
echo "==> Standard image: non-root user 'mforge', passwordless sudo, colored prompt, serverless utils."
