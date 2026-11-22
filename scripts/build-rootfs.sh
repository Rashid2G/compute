#!/usr/bin/env bash
set -euo pipefail

export PATH="${PATH}:/sbin:/usr/sbin:/usr/local/sbin:/usr/local/bin"

DEST_DIR="${1:-./data/images}"
IMAGE_SIZE_MB="${2:-${COMPUTE_ROOTFS_SIZE_MB:-2048}}"
EXT4_PATH="${DEST_DIR}/ubuntu-24.04.ext4"
KEY_DIR="${DEST_DIR}/ssh"
FORCE_REBUILD="${FORCE_REBUILD:-0}"

SUDO=""
if [ "$(id -u)" -ne 0 ]; then
    if command -v sudo >/dev/null 2>&1; then
        SUDO="sudo"
    fi
fi

mkdir -p "${DEST_DIR}" "${KEY_DIR}" 2>/dev/null || $SUDO mkdir -p "${DEST_DIR}" "${KEY_DIR}"
if [ "$(id -u)" -ne 0 ] && { [ ! -w "${DEST_DIR}" ] || [ ! -w "${KEY_DIR}" ]; }; then
    $SUDO chown -R "$(id -u):$(id -g)" "${DEST_DIR}" 2>/dev/null || true
fi
chmod 700 "${KEY_DIR}" 2>/dev/null || true

is_valid_ext4() {
    local file="$1"
    [ -f "${file}" ] || return 1
    local magic
    magic="$(od -An -tx1 -j 1080 -N 2 "${file}" 2>/dev/null | tr -d ' \n' || true)"
    [ "${magic}" = "53ef" ]
}

echo "==> [Step 1/5] Generating internal SSH keypair if not exists..."
KEY_GENERATED=0
if [ ! -f "${KEY_DIR}/id_ed25519" ]; then
    KEY_GENERATED=1
    if command -v ssh-keygen >/dev/null 2>&1; then
        ssh-keygen -t ed25519 -N "" -f "${KEY_DIR}/id_ed25519" -C "compute@internal"
    else
        KEY_DIR_ABS="$(cd "${KEY_DIR}" && pwd)"
        docker run --rm -v "${KEY_DIR_ABS}:/keys" ubuntu:24.04 bash -ec \
            "apt-get update && apt-get install -y --no-install-recommends openssh-client >/dev/null && ssh-keygen -t ed25519 -N '' -f /keys/id_ed25519 -C 'compute@internal' && chown $(id -u):$(id -g) /keys/id_ed25519 /keys/id_ed25519.pub"
    fi
fi
chmod 600 "${KEY_DIR}/id_ed25519" 2>/dev/null || $SUDO chmod 600 "${KEY_DIR}/id_ed25519"
chmod 644 "${KEY_DIR}/id_ed25519.pub" 2>/dev/null || $SUDO chmod 644 "${KEY_DIR}/id_ed25519.pub"
PUB_KEY="$(cat "${KEY_DIR}/id_ed25519.pub")"

if [ "${KEY_GENERATED}" = "0" ] && [ "${FORCE_REBUILD}" != "1" ] && is_valid_ext4 "${EXT4_PATH}"; then
    ln -sf ubuntu-24.04.ext4 "${DEST_DIR}/ubuntu-24.ext4"
    echo "==> Rootfs already present and verified: ${EXT4_PATH} (set FORCE_REBUILD=1 to rebuild)"
    exit 0
fi

echo "==> [Step 2/5] Preparing Ubuntu 24.04 rootfs configuration..."
BUILD_DIR="$(mktemp -d /tmp/compute-u24-build-XXXXXX)"
WORKDIR=""
CONTAINER_ID=""
TMP_EXT4="${DEST_DIR}/.$(basename "${EXT4_PATH}").part.$$"

cleanup() {
    rm -f "${TMP_EXT4}" 2>/dev/null || $SUDO rm -f "${TMP_EXT4}" 2>/dev/null || true
    rm -rf "${BUILD_DIR}" 2>/dev/null || $SUDO rm -rf "${BUILD_DIR}" 2>/dev/null || true
    if [ -n "${CONTAINER_ID}" ]; then
        docker rm -f "${CONTAINER_ID}" >/dev/null 2>&1 || true
    fi
    if [ -n "${WORKDIR}" ] && [ -d "${WORKDIR}" ]; then
        rm -rf "${WORKDIR}" 2>/dev/null || $SUDO rm -rf "${WORKDIR}" 2>/dev/null || true
    fi
}
trap cleanup EXIT

printf '%s\n' "${PUB_KEY}" > "${BUILD_DIR}/authorized_keys"

COMPUTE_AGENT_BIN="${COMPUTE_AGENT_BIN:-./bin/compute-agent}"
if [ ! -f "${COMPUTE_AGENT_BIN}" ] && command -v go >/dev/null 2>&1; then
    echo "==> Building compute-agent binary (${COMPUTE_AGENT_BIN})..."
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o "${COMPUTE_AGENT_BIN}" ./cmd/compute-agent 2>/dev/null || true
fi

AGENT_DOCKER_SNIPPET=""
if [ -f "${COMPUTE_AGENT_BIN}" ]; then
    cp "${COMPUTE_AGENT_BIN}" "${BUILD_DIR}/compute-agent"
    cat << 'SVCEOF' > "${BUILD_DIR}/compute-agent.service"
[Unit]
Description=Compute In-Guest MicroVM Agent
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/compute-agent
Restart=always
RestartSec=1

[Install]
WantedBy=multi-user.target
SVCEOF
    AGENT_DOCKER_SNIPPET=$(cat << 'SNIP'
COPY compute-agent /usr/local/bin/compute-agent
COPY compute-agent.service /etc/systemd/system/compute-agent.service
RUN chmod 0755 /usr/local/bin/compute-agent && \
    mkdir -p /etc/systemd/system/multi-user.target.wants && \
    ln -sf /etc/systemd/system/compute-agent.service /etc/systemd/system/multi-user.target.wants/compute-agent.service
SNIP
)
fi

cat << 'PROMPT' > "${BUILD_DIR}/00-prompt.sh"
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

cat << 'MOTD' > "${BUILD_DIR}/motd"
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

cat << 'RESOLV' > "${BUILD_DIR}/resolv.conf"
nameserver 1.1.1.1
nameserver 8.8.8.8
RESOLV

cat << 'FSTAB' > "${BUILD_DIR}/fstab"
/dev/vda / ext4 defaults,noatime 0 1
devpts /dev/pts devpts gid=5,mode=620,ptmxmode=666 0 0
proc /proc proc defaults 0 0
sysfs /sys sysfs defaults 0 0
tmpfs /dev/shm tmpfs defaults 0 0
tmpfs /run tmpfs defaults 0 0
FSTAB

cat << 'INITSCRIPT' > "${BUILD_DIR}/init"
#!/bin/bash
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
mount -t proc proc /proc 2>/dev/null || true
mount -t sysfs sysfs /sys 2>/dev/null || true
mount -t devtmpfs devtmpfs /dev 2>/dev/null || true
mkdir -p /dev/pts /dev/shm /run/sshd /run/lock
mount -t tmpfs -o mode=1777 tmpfs /dev/shm 2>/dev/null || true
mount -t tmpfs tmpfs /run 2>/dev/null || true
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

# Start compute-agent if installed
if [ -x /usr/local/bin/compute-agent ]; then
    /usr/local/bin/compute-agent &
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

cat << 'DOCKERFILE' > "${BUILD_DIR}/Dockerfile"
FROM ubuntu:24.04
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
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
    e2fsprogs \
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
    build-essential \
    && rm -rf /var/lib/apt/lists/*

# Ensure ping works for non-root users inside Firecracker microVMs
RUN chmod u+s /bin/ping /usr/bin/ping 2>/dev/null || true && \
    (setcap cap_net_raw+ep /bin/ping 2>/dev/null || setcap cap_net_raw+ep /usr/bin/ping 2>/dev/null || true) && \
    mkdir -p /etc/sysctl.d && \
    echo "net.ipv4.ping_group_range = 0 2147483647" > /etc/sysctl.d/99-compute.conf && \
    if [ -f /etc/sysctl.conf ]; then grep -q "^net.ipv4.ping_group_range" /etc/sysctl.conf || echo "net.ipv4.ping_group_range = 0 2147483647" >> /etc/sysctl.conf; fi

# Compile instant poweroff / reboot binaries
RUN printf '#include <unistd.h>\n#include <sys/reboot.h>\nint main(){ sync(); reboot(RB_POWER_OFF); return 0; }\n' > /tmp/poweroff.c && \
    gcc -O2 /tmp/poweroff.c -o /sbin/poweroff && \
    printf '#include <unistd.h>\n#include <sys/reboot.h>\nint main(){ sync(); reboot(RB_AUTOBOOT); return 0; }\n' > /tmp/reboot.c && \
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

# Configure SSH daemon and host keys
RUN mkdir -p /var/run/sshd /run/sshd /root/.ssh /home/compute/.ssh && \
    sed -i 's/#PermitRootLogin.*/PermitRootLogin yes/' /etc/ssh/sshd_config 2>/dev/null || true && \
    sed -i 's/UsePAM yes/UsePAM no/' /etc/ssh/sshd_config 2>/dev/null || true && \
    echo "PermitRootLogin yes" >> /etc/ssh/sshd_config && \
    echo "PasswordAuthentication no" >> /etc/ssh/sshd_config && \
    echo "KbdInteractiveAuthentication no" >> /etc/ssh/sshd_config && \
    echo "PubkeyAuthentication yes" >> /etc/ssh/sshd_config && \
    echo "UsePAM no" >> /etc/ssh/sshd_config && \
    echo "StrictModes no" >> /etc/ssh/sshd_config && \
    ssh-keygen -A

COPY authorized_keys /root/.ssh/authorized_keys
COPY authorized_keys /home/compute/.ssh/authorized_keys
COPY 00-prompt.sh /etc/profile.d/00-prompt.sh
COPY motd /etc/motd
COPY resolv.conf /etc/resolv.conf
COPY fstab /etc/fstab
COPY init /sbin/init

RUN chmod 700 /root/.ssh /home/compute/.ssh && \
    chmod 600 /root/.ssh/authorized_keys /home/compute/.ssh/authorized_keys && \
    chown -R 1000:1000 /home/compute && \
    chmod +x /sbin/init
DOCKERFILE

if [ -n "${AGENT_DOCKER_SNIPPET}" ]; then
    printf '\n%s\n' "${AGENT_DOCKER_SNIPPET}" >> "${BUILD_DIR}/Dockerfile"
fi

echo "==> [Step 3/5] Building Ubuntu 24.04 Docker image..."
docker build -t compute-ubuntu:24.04 "${BUILD_DIR}"

echo "==> [Step 4/5] Exporting rootfs and formatting ext4 image (${IMAGE_SIZE_MB}MB sparse)..."
CONTAINER_ID="$(docker create compute-ubuntu:24.04 /bin/bash)"
WORKDIR="$(mktemp -d /tmp/compute-u24-rootfs-XXXXXX)"
docker export "${CONTAINER_ID}" | $SUDO tar --numeric-owner -xpf - -C "${WORKDIR}"
$SUDO chmod 755 "${WORKDIR}"
# Docker binds /etc/resolv.conf during container creation; re-apply guest DNS resolvers.
$SUDO cp "${BUILD_DIR}/resolv.conf" "${WORKDIR}/etc/resolv.conf"
$SUDO chmod 644 "${WORKDIR}/etc/resolv.conf"

rm -f "${TMP_EXT4}" 2>/dev/null || $SUDO rm -f "${TMP_EXT4}"
truncate -s "${IMAGE_SIZE_MB}M" "${TMP_EXT4}"

if command -v mkfs.ext4 >/dev/null 2>&1 && $SUDO mkfs.ext4 -F -q -d "${WORKDIR}" -L "rootfs" "${TMP_EXT4}"; then
    :
elif command -v mkfs.ext4 >/dev/null 2>&1; then
    $SUDO mkfs.ext4 -F -q -L "rootfs" "${TMP_EXT4}"
    MOUNTDIR="$(mktemp -d /tmp/compute-u24-mnt-XXXXXX)"
    $SUDO mount -o loop "${TMP_EXT4}" "${MOUNTDIR}"
    $SUDO cp -a "${WORKDIR}/." "${MOUNTDIR}/"
    $SUDO chmod 755 "${MOUNTDIR}"
    $SUDO umount "${MOUNTDIR}"
    rm -rf "${MOUNTDIR}"
else
    DEST_DIR_ABS="$(cd "${DEST_DIR}" && pwd)"
    docker run --rm \
        -v "${WORKDIR}:/rootfs:ro" \
        -v "${DEST_DIR_ABS}:/out" \
        compute-ubuntu:24.04 \
        mkfs.ext4 -F -q -d /rootfs -L "rootfs" "/out/$(basename "${TMP_EXT4}")"
fi

$SUDO chmod 644 "${TMP_EXT4}"
$SUDO chown "$(id -u):$(id -g)" "${TMP_EXT4}" 2>/dev/null || true
mv -f "${TMP_EXT4}" "${EXT4_PATH}"

ln -sf ubuntu-24.04.ext4 "${DEST_DIR}/ubuntu-24.ext4"

echo "==> [Step 5/5] Rootfs image created successfully at ${EXT4_PATH}!"
echo "==> Ubuntu 24.04 LTS (Noble Numbat) image ready with Node.js, Python 3, SSH, sudo, git, curl, jq!"
