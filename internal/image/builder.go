package image

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/db"
	"github.com/Rashid2G/on-demand-vms/internal/storage"
	"github.com/google/uuid"
)

// BuildRequest defines parameters for building a new custom image with pre-installed packages
type BuildRequest struct {
	Name        string   `json:"name"`
	OwnerID     string   `json:"owner_id,omitempty"`
	Description string   `json:"description,omitempty"`
	BaseImage   string   `json:"base_image,omitempty"` // defaults to "ubuntu-24.04"
	Packages    []string `json:"packages,omitempty"`
	Dockerfile  string   `json:"dockerfile,omitempty"`
	SizeMB      int      `json:"size_mb,omitempty"` // defaults to 2048 MB
}

// BuildFunc defines the build runner function signature
type BuildFunc func(ctx context.Context, req BuildRequest, outputPath string, sshPubKey string) error

// Builder handles building and registering custom VM images
type Builder struct {
	store     *db.Store
	storMgr   *storage.Manager
	buildFunc BuildFunc
}

// NewBuilder creates a new image builder
func NewBuilder(store *db.Store, storMgr *storage.Manager) *Builder {
	b := &Builder{
		store:   store,
		storMgr: storMgr,
	}
	b.buildFunc = b.defaultDockerBuild
	return b
}

// SetBuildFunc overrides the build runner (useful for tests or custom builders)
func (b *Builder) SetBuildFunc(fn BuildFunc) {
	b.buildFunc = fn
}

// BuildImage creates a database record, runs the build process, and stores the image
func (b *Builder) BuildImage(ctx context.Context, req BuildRequest) (*db.CustomImage, error) {
	cleanName := strings.TrimSpace(req.Name)
	if cleanName == "" {
		return nil, fmt.Errorf("image name cannot be empty")
	}

	// Validate name: only alphanumeric, dashes, dots, underscores
	for _, r := range cleanName {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.') {
			return nil, fmt.Errorf("invalid image name '%s': must contain only letters, numbers, dashes, underscores, and dots", cleanName)
		}
	}

	// Check if image or template with same name already exists for this owner.
	// Names are tenant-scoped, so the same name may be reused by other owners.
	existing, _ := b.store.GetImageByOwner(ctx, req.OwnerID, cleanName)
	if existing != nil {
		return nil, fmt.Errorf("image with name '%s' already exists", cleanName)
	}
	existingTpl, _ := b.store.GetTemplateByOwner(ctx, req.OwnerID, cleanName)
	if existingTpl != nil {
		return nil, fmt.Errorf("template with name '%s' already exists", cleanName)
	}

	baseImage := strings.TrimSpace(req.BaseImage)
	if baseImage == "" {
		baseImage = "ubuntu-24.04"
	}

	sizeMB := req.SizeMB
	if sizeMB <= 0 {
		sizeMB = 2048
	}

	// Normalize packages list
	var packages []string
	for _, p := range req.Packages {
		p = strings.TrimSpace(p)
		if p != "" {
			// In case user supplied comma-separated string inside element
			subPkgs := strings.Split(p, ",")
			for _, sp := range subPkgs {
				sp = strings.TrimSpace(sp)
				if sp != "" {
					packages = append(packages, sp)
				}
			}
		}
	}

	uid := uuid.New().String()
	shortID := strings.ReplaceAll(uid, "-", "")[:8]
	imgID := fmt.Sprintf("img-%s", shortID)

	rootfsFilename := fmt.Sprintf("%s-%s.ext4", cleanName, shortID)
	rootfsPath := filepath.Join(b.storMgr.GetConfig().BaseImagesDir, rootfsFilename)
	kernelPath := b.storMgr.GetBaseKernelPath()

	img := &db.CustomImage{
		ID:          imgID,
		OwnerID:     req.OwnerID,
		Name:        cleanName,
		Description: req.Description,
		BaseImage:   baseImage,
		Packages:    packages,
		RootfsPath:  rootfsPath,
		KernelPath:  kernelPath,
		SizeMB:      sizeMB,
		Status:      db.ImageStatusBuilding,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}

	if err := b.store.SaveImage(ctx, img); err != nil {
		return nil, fmt.Errorf("failed to save image record: %w", err)
	}

	// Load SSH public key for injection
	var sshPubKey string
	sshPrivKey := b.storMgr.GetSSHKeyPath()
	pubKeyPath := sshPrivKey + ".pub"
	if pubBytes, err := os.ReadFile(pubKeyPath); err == nil {
		sshPubKey = strings.TrimSpace(string(pubBytes))
	}

	// Run build function
	buildErr := b.buildFunc(ctx, req, rootfsPath, sshPubKey)
	if buildErr != nil {
		img.Status = db.ImageStatusFailed
		img.ErrorMessage = buildErr.Error()
		img.UpdatedAt = time.Now().UTC()
		_ = b.store.SaveImage(ctx, img)
		_ = os.Remove(rootfsPath)
		return img, fmt.Errorf("image build failed: %w", buildErr)
	}

	img.Status = db.ImageStatusReady
	img.ErrorMessage = ""
	img.UpdatedAt = time.Now().UTC()
	if err := b.store.SaveImage(ctx, img); err != nil {
		return nil, fmt.Errorf("failed to update image status: %w", err)
	}

	return img, nil
}

// defaultDockerBuild builds an Ubuntu rootfs with specified packages using Docker and formats it as ext4
func (b *Builder) defaultDockerBuild(ctx context.Context, req BuildRequest, outputPath string, sshPubKey string) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker is required for default image building but was not found in PATH")
	}

	buildDir, err := os.MkdirTemp("/tmp", "compute-build-*-pkg")
	if err != nil {
		return fmt.Errorf("failed to create temp build dir: %w", err)
	}
	defer os.RemoveAll(buildDir)

	workDir, err := os.MkdirTemp("/tmp", "compute-rootfs-*-pkg")
	if err != nil {
		return fmt.Errorf("failed to create temp rootfs dir: %w", err)
	}
	defer os.RemoveAll(workDir)

	baseDockerImage := "ubuntu:24.04"
	if strings.Contains(req.BaseImage, "alpine") {
		return fmt.Errorf("alpine base images are not supported by the custom image builder; use ubuntu or debian")
	} else if strings.Contains(req.BaseImage, "debian") {
		baseDockerImage = "debian:12"
	}

	// Build package list for apt-get
	pkgsArg := strings.Join(req.Packages, " ")

	dockerfileContent := fmt.Sprintf(`FROM %s
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
    util-linux \
    net-tools \
    build-essential \
    %s \
    && rm -rf /var/lib/apt/lists/*

RUN echo '#include <unistd.h>\n#include <sys/reboot.h>\nint main(){ sync(); reboot(RB_POWER_OFF); return 0; }' > /tmp/poweroff.c && \
    gcc -O2 /tmp/poweroff.c -o /sbin/poweroff && \
    echo '#include <unistd.h>\n#include <sys/reboot.h>\nint main(){ sync(); reboot(RB_AUTOBOOT); return 0; }' > /tmp/reboot.c && \
    gcc -O2 /tmp/reboot.c -o /sbin/reboot && \
    rm -f /tmp/poweroff.c /tmp/reboot.c

RUN (userdel -r ubuntu 2>/dev/null || true) && \
    useradd -m -s /bin/bash -u 1000 compute && \
    usermod -aG sudo compute && \
    mkdir -p /etc/sudoers.d && \
    echo "compute ALL=(ALL) NOPASSWD: ALL" > /etc/sudoers.d/compute && \
    chmod 0440 /etc/sudoers.d/compute && \
    echo "root:root" | chpasswd && \
    echo "compute:compute" | chpasswd

RUN mkdir -p /var/run/sshd /root/.ssh /home/compute/.ssh && \
    sed -i 's/#PermitRootLogin.*/PermitRootLogin yes/' /etc/ssh/sshd_config 2>/dev/null || true && \
    sed -i 's/UsePAM yes/UsePAM no/' /etc/ssh/sshd_config 2>/dev/null || true && \
    echo "PermitRootLogin yes" >> /etc/ssh/sshd_config && \
    echo "PasswordAuthentication yes" >> /etc/ssh/sshd_config && \
    echo "PubkeyAuthentication yes" >> /etc/ssh/sshd_config && \
    echo "UsePAM no" >> /etc/ssh/sshd_config && \
    echo "StrictModes no" >> /etc/ssh/sshd_config
`, baseDockerImage, pkgsArg)

	if strings.TrimSpace(req.Dockerfile) != "" {
		dockerfileContent = req.Dockerfile
	}

	dockerfilePath := filepath.Join(buildDir, "Dockerfile")
	if err := os.WriteFile(dockerfilePath, []byte(dockerfileContent), 0644); err != nil {
		return fmt.Errorf("failed to write Dockerfile: %w", err)
	}

	tag := fmt.Sprintf("compute-custom:%s", req.Name)

	buildCmd := exec.CommandContext(ctx, "docker", "build", "-t", tag, buildDir)
	var buildErrBuf bytes.Buffer
	buildCmd.Stderr = &buildErrBuf
	if err := buildCmd.Run(); err != nil {
		return fmt.Errorf("docker build failed: %w (%s)", err, buildErrBuf.String())
	}
	defer func() {
		_ = exec.Command("docker", "rmi", "-f", tag).Run()
	}()

	// Export rootfs
	createCmd := exec.CommandContext(ctx, "docker", "create", tag, "/bin/bash")
	cidOut, err := createCmd.Output()
	if err != nil {
		return fmt.Errorf("docker create failed: %w", err)
	}
	cid := strings.TrimSpace(string(cidOut))
	defer func() {
		_ = exec.Command("docker", "rm", "-f", cid).Run()
	}()

	exportCmd := exec.CommandContext(ctx, "docker", "export", cid)
	tarCmd := exec.CommandContext(ctx, "tar", "-x", "-C", workDir)

	pipeR, pipeW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("pipe failed: %w", err)
	}
	exportCmd.Stdout = pipeW
	tarCmd.Stdin = pipeR

	if err := exportCmd.Start(); err != nil {
		return fmt.Errorf("docker export start failed: %w", err)
	}
	if err := tarCmd.Start(); err != nil {
		return fmt.Errorf("tar extract start failed: %w", err)
	}

	_ = exportCmd.Wait()
	_ = pipeW.Close()
	if err := tarCmd.Wait(); err != nil {
		return fmt.Errorf("tar extract failed: %w", err)
	}

	// Inject SSH authorized keys
	if sshPubKey != "" {
		_ = os.MkdirAll(filepath.Join(workDir, "root", ".ssh"), 0700)
		_ = os.MkdirAll(filepath.Join(workDir, "home", "compute", ".ssh"), 0700)
		_ = os.WriteFile(filepath.Join(workDir, "root", ".ssh", "authorized_keys"), []byte(sshPubKey+"\n"), 0600)
		computeKey := filepath.Join(workDir, "home", "compute", ".ssh", "authorized_keys")
		_ = os.WriteFile(computeKey, []byte(sshPubKey+"\n"), 0600)
		// The guest runs sshd as root but the target user must own its key file;
		// otherwise OpenSSH rejects the key ("bad ownership or modes").
		_ = os.Chown(filepath.Join(workDir, "home", "compute"), 1000, 1000)
		_ = os.Chown(filepath.Join(workDir, "home", "compute", ".ssh"), 1000, 1000)
		_ = os.Chown(computeKey, 1000, 1000)
		_ = os.Chmod(filepath.Join(workDir, "home", "compute", ".ssh"), 0700)
		_ = os.Chmod(computeKey, 0600)
	}

	// Write Firecracker /sbin/init
	initScript := `#!/bin/bash
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

if grep -q "ip=" /proc/cmdline; then
    IP_SPEC=$(grep -o "ip=[^ ]*" /proc/cmdline | head -n1 | cut -d= -f2)
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

    if [ -n "$CIP" ]; then
        PREFIX=16
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
    fi
else
    ip link set eth0 up 2>/dev/null || true
fi

if [ ! -f /etc/ssh/ssh_host_ed25519_key ]; then
    ssh-keygen -A 2>/dev/null || true
fi

/usr/sbin/sshd

while true; do
    /sbin/getty -L 115200 ttyS0 vt100 || sleep 1
done
`
	initPath := filepath.Join(workDir, "sbin", "init")
	_ = os.WriteFile(initPath, []byte(initScript), 0755)

	// Format ext4 filesystem directly from directory using mkfs.ext4 -d
	sizeMB := req.SizeMB
	if sizeMB <= 0 {
		sizeMB = 2048
	}
	sizeArg := fmt.Sprintf("%dM", sizeMB)

	_ = os.Remove(outputPath)
	mkfsCmd := exec.CommandContext(ctx, "mkfs.ext4", "-F", "-q", "-d", workDir, "-L", "rootfs", outputPath, sizeArg)
	var mkfsErrBuf bytes.Buffer
	mkfsCmd.Stderr = &mkfsErrBuf
	if err := mkfsCmd.Run(); err != nil {
		return fmt.Errorf("mkfs.ext4 failed: %w (%s)", err, mkfsErrBuf.String())
	}

	return nil
}
