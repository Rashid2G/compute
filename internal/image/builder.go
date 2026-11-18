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

	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/storage"
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
	Script      string   `json:"script,omitempty"`
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

	baseImage := strings.ToLower(strings.TrimSpace(req.BaseImage))
	switch baseImage {
	case "", "ubuntu", "ubuntu-24", "ubuntu-24.04", "ubuntu-2404":
		baseImage = "ubuntu-24.04"
	default:
		return nil, fmt.Errorf("unsupported base image '%s': only Ubuntu 24.04 is supported", req.BaseImage)
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
	req.Name = cleanName
	req.BaseImage = baseImage
	req.Packages = packages
	req.SizeMB = sizeMB

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

func lookSystemBin(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, candidate := range []string{"/usr/sbin/" + name, "/sbin/" + name, "/usr/local/sbin/" + name} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return name
}

func (b *Builder) findBaseRootfsPath() string {
	var candidates []string
	if b != nil && b.storMgr != nil {
		if p := b.storMgr.GetBaseRootfsPath(); p != "" {
			candidates = append(candidates, p)
		}
		if dir := b.storMgr.GetConfig().BaseImagesDir; dir != "" {
			candidates = append(candidates,
				filepath.Join(dir, "ubuntu-24.04.ext4"),
				filepath.Join(dir, "ubuntu-24.ext4"),
			)
		}
	}
	candidates = append(candidates, "/var/lib/compute/images/ubuntu-24.04.ext4")
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c
		}
	}
	return ""
}

// defaultDockerBuild builds an Ubuntu rootfs with specified packages using a
// native base-rootfs clone + chroot fallback when running as root without Docker
// (or when no custom Dockerfile is provided and a base rootfs exists), or via
// Docker build/export + mkfs.ext4.
func (b *Builder) defaultDockerBuild(ctx context.Context, req BuildRequest, outputPath string, sshPubKey string) error {
	_, dockerLookErr := exec.LookPath("docker")
	baseRootfs := b.findBaseRootfsPath()

	if strings.TrimSpace(req.Dockerfile) == "" && baseRootfs != "" && os.Geteuid() == 0 {
		chrootErr := b.chrootRootfsBuild(ctx, req, baseRootfs, outputPath, sshPubKey)
		if chrootErr == nil {
			return nil
		}
		if dockerLookErr != nil {
			return chrootErr
		}
	}

	if dockerLookErr != nil {
		if strings.TrimSpace(req.Dockerfile) != "" {
			return fmt.Errorf("docker is required for Dockerfile image building but was not found in PATH")
		}
		if os.Geteuid() != 0 {
			return fmt.Errorf("docker is required for default image building but was not found in PATH (native rootfs clone/chroot fallback requires root privileges)")
		}
		return fmt.Errorf("docker is required for default image building but was not found in PATH (and no base rootfs image was found to clone)")
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

	// Build package list for apt-get
	dockerfileContent := buildDockerfile(req)

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

	injectSSHAndInit(workDir, sshPubKey)

	// Format ext4 filesystem directly from directory using mkfs.ext4 -d
	sizeMB := req.SizeMB
	if sizeMB <= 0 {
		sizeMB = 2048
	}
	sizeArg := fmt.Sprintf("%dM", sizeMB)

	_ = os.Remove(outputPath)
	mkfsCmd := exec.CommandContext(ctx, lookSystemBin("mkfs.ext4"), "-F", "-q", "-d", workDir, "-L", "rootfs", outputPath, sizeArg)
	var mkfsErrBuf bytes.Buffer
	mkfsCmd.Stderr = &mkfsErrBuf
	if err := mkfsCmd.Run(); err != nil {
		return fmt.Errorf("mkfs.ext4 failed: %w (%s)", err, mkfsErrBuf.String())
	}

	return nil
}

func injectSSHAndInit(rootDir, sshPubKey string) {
	if sshPubKey != "" {
		_ = os.MkdirAll(filepath.Join(rootDir, "root", ".ssh"), 0700)
		_ = os.MkdirAll(filepath.Join(rootDir, "home", "compute", ".ssh"), 0700)
		_ = os.WriteFile(filepath.Join(rootDir, "root", ".ssh", "authorized_keys"), []byte(sshPubKey+"\n"), 0600)
		computeKey := filepath.Join(rootDir, "home", "compute", ".ssh", "authorized_keys")
		_ = os.WriteFile(computeKey, []byte(sshPubKey+"\n"), 0600)
		// The guest runs sshd as root but the target user must own its key file;
		// otherwise OpenSSH rejects the key ("bad ownership or modes").
		_ = os.Chown(filepath.Join(rootDir, "home", "compute"), 1000, 1000)
		_ = os.Chown(filepath.Join(rootDir, "home", "compute", ".ssh"), 1000, 1000)
		_ = os.Chown(computeKey, 1000, 1000)
		_ = os.Chmod(filepath.Join(rootDir, "home", "compute", ".ssh"), 0700)
		_ = os.Chmod(computeKey, 0600)
	}

	// Write Firecracker /sbin/init
	initScript := defaultInitScript()
	initPath := filepath.Join(rootDir, "sbin", "init")
	_ = os.MkdirAll(filepath.Dir(initPath), 0755)
	_ = os.WriteFile(initPath, []byte(initScript), 0755)
}

func (b *Builder) chrootRootfsBuild(ctx context.Context, req BuildRequest, baseRootfs, outputPath, sshPubKey string) error {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	stagingPath := outputPath + ".staging"
	_ = os.Remove(stagingPath)
	defer func() {
		_ = os.Remove(stagingPath)
	}()

	if err := storage.CloneFileCOW(baseRootfs, stagingPath); err != nil {
		return fmt.Errorf("clone base rootfs %s: %w", baseRootfs, err)
	}

	sizeMB := req.SizeMB
	if sizeMB <= 0 {
		sizeMB = 2048
	}
	if fi, err := os.Stat(stagingPath); err == nil && int64(sizeMB)*1024*1024 > fi.Size() {
		if err := os.Truncate(stagingPath, int64(sizeMB)*1024*1024); err == nil {
			_ = exec.CommandContext(ctx, lookSystemBin("e2fsck"), "-pf", stagingPath).Run()
			_ = exec.CommandContext(ctx, lookSystemBin("resize2fs"), "-f", stagingPath).Run()
		}
	}

	mntDir, err := os.MkdirTemp("", "compute-build-mnt-*")
	if err != nil {
		return fmt.Errorf("create mount dir: %w", err)
	}

	mountBin := lookSystemBin("mount")
	umountBin := lookSystemBin("umount")
	if out, err := exec.CommandContext(ctx, mountBin, "-o", "loop", stagingPath, mntDir).CombinedOutput(); err != nil {
		_ = exec.Command(umountBin, mntDir).Run()
		_ = os.RemoveAll(mntDir)
		return fmt.Errorf("loop mount %s: %w (%s)", stagingPath, err, strings.TrimSpace(string(out)))
	}

	var submounts []string
	unmountAll := func() error {
		for i := len(submounts) - 1; i >= 0; i-- {
			if err := exec.Command(umountBin, submounts[i]).Run(); err != nil {
				_ = exec.Command(umountBin, "-l", submounts[i]).Run()
			}
		}
		submounts = nil
		if err := exec.Command(umountBin, mntDir).Run(); err != nil {
			if err2 := exec.Command(umountBin, "-l", mntDir).Run(); err2 != nil {
				return fmt.Errorf("unmount %s: %w", mntDir, err)
			}
		}
		_ = os.RemoveAll(mntDir)
		return nil
	}
	defer func() {
		if mntDir != "" {
			_ = unmountAll()
		}
	}()

	var packages []string
	for _, p := range req.Packages {
		p = strings.TrimSpace(p)
		if p != "" {
			for _, sp := range strings.Split(p, ",") {
				if sp = strings.TrimSpace(sp); sp != "" {
					packages = append(packages, sp)
				}
			}
		}
	}

	if len(packages) > 0 || strings.TrimSpace(req.Script) != "" {
		procDir := filepath.Join(mntDir, "proc")
		sysDir := filepath.Join(mntDir, "sys")
		devDir := filepath.Join(mntDir, "dev")
		_ = os.MkdirAll(procDir, 0755)
		_ = os.MkdirAll(sysDir, 0755)
		_ = os.MkdirAll(devDir, 0755)

		if err := exec.CommandContext(ctx, mountBin, "-t", "proc", "proc", procDir).Run(); err == nil {
			submounts = append(submounts, procDir)
		}
		if err := exec.CommandContext(ctx, mountBin, "-t", "sysfs", "sysfs", sysDir).Run(); err == nil {
			submounts = append(submounts, sysDir)
		}
		if err := exec.CommandContext(ctx, mountBin, "--bind", "/dev", devDir).Run(); err == nil {
			submounts = append(submounts, devDir)
		}

		// Ensure DNS works inside the chroot
		resolvDst := filepath.Join(mntDir, "etc", "resolv.conf")
		_ = os.MkdirAll(filepath.Dir(resolvDst), 0755)
		_ = os.Remove(resolvDst)
		resolvBytes, err := os.ReadFile("/etc/resolv.conf")
		if err != nil || len(bytes.TrimSpace(resolvBytes)) == 0 {
			resolvBytes = []byte("nameserver 8.8.8.8\nnameserver 1.1.1.1\n")
		}
		_ = os.WriteFile(resolvDst, resolvBytes, 0644)

		// Prevent apt postinst scripts from starting background daemons inside the chroot
		policyRC := filepath.Join(mntDir, "usr", "sbin", "policy-rc.d")
		_ = os.MkdirAll(filepath.Dir(policyRC), 0755)
		_ = os.WriteFile(policyRC, []byte("#!/bin/sh\nexit 101\n"), 0755)
		defer func() {
			_ = os.Remove(policyRC)
		}()

		chrootBin := lookSystemBin("chroot")
		chrootEnv := []string{
			"DEBIAN_FRONTEND=noninteractive",
			"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
			"HOME=/root",
			"LC_ALL=C.UTF-8",
		}

		if len(packages) > 0 {
			args := append([]string{
				mntDir,
				"/bin/sh", "-e", "-c",
				`apt-get update && apt-get install -y --no-install-recommends "$@" && rm -rf /var/lib/apt/lists/*`,
				"--",
			}, packages...)
			cmd := exec.CommandContext(ctx, chrootBin, args...)
			cmd.Env = chrootEnv
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("chroot apt-get install failed: %w (%s)", err, strings.TrimSpace(string(out)))
			}
		}

		if script := strings.TrimSpace(req.Script); script != "" {
			cmd := exec.CommandContext(ctx, chrootBin, mntDir, "/bin/bash", "-e", "-c", script)
			cmd.Env = chrootEnv
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("chroot script failed: %w (%s)", err, strings.TrimSpace(string(out)))
			}
		}
		_ = os.Remove(policyRC)
	}

	injectSSHAndInit(mntDir, sshPubKey)
	_ = exec.Command("sync").Run()

	if err := unmountAll(); err != nil {
		return err
	}
	mntDir = ""

	_ = os.Remove(outputPath)
	if err := os.Rename(stagingPath, outputPath); err != nil {
		return fmt.Errorf("move built rootfs into place: %w", err)
	}
	return nil
}

// DefaultBasePackages returns the standard CLI, networking, and runtime packages
// installed in every Ubuntu 24.04 sandbox rootfs image.
func DefaultBasePackages() []string {
	return []string{
		"iputils-ping",
		"bind9-dnsutils",
		"netcat-openbsd",
		"traceroute",
		"iproute2",
		"net-tools",
		"curl",
		"wget",
		"less",
		"nano",
		"vim-tiny",
		"htop",
		"lsof",
		"psmisc",
		"file",
		"unzip",
		"zip",
		"tar",
		"gzip",
		"xz-utils",
		"rsync",
		"openssh-server",
		"openssh-client",
		"jq",
		"git",
		"ca-certificates",
		"sudo",
		"procps",
		"util-linux",
		"bash",
		"coreutils",
		"findutils",
		"grep",
		"sed",
		"gawk",
		"hostname",
		"locales",
		"libcap2-bin",
		"python3",
		"build-essential",
	}
}

func buildDockerfile(req BuildRequest) string {
	const baseDockerImage = "ubuntu:24.04"
	allPkgs := append([]string{}, DefaultBasePackages()...)
	allPkgs = append(allPkgs, req.Packages...)
	pkgsArg := strings.Join(allPkgs, " \\\n    ")

	return fmt.Sprintf(`FROM %s
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
    %s \
    && rm -rf /var/lib/apt/lists/*

RUN chmod u+s /bin/ping /usr/bin/ping 2>/dev/null || true && \
    (setcap cap_net_raw+ep /bin/ping 2>/dev/null || setcap cap_net_raw+ep /usr/bin/ping 2>/dev/null || true) && \
    mkdir -p /etc/sysctl.d && \
    echo "net.ipv4.ping_group_range = 0 2147483647" > /etc/sysctl.d/99-compute.conf && \
    if [ -f /etc/sysctl.conf ]; then grep -q "^net.ipv4.ping_group_range" /etc/sysctl.conf || echo "net.ipv4.ping_group_range = 0 2147483647" >> /etc/sysctl.conf; fi

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
    usermod -p '!' root && \
    usermod -p '!' compute

RUN mkdir -p /var/run/sshd /root/.ssh /home/compute/.ssh && \
    sed -i 's/#PermitRootLogin.*/PermitRootLogin yes/' /etc/ssh/sshd_config 2>/dev/null || true && \
    sed -i 's/UsePAM yes/UsePAM no/' /etc/ssh/sshd_config 2>/dev/null || true && \
    echo "PermitRootLogin yes" >> /etc/ssh/sshd_config && \
    echo "PasswordAuthentication no" >> /etc/ssh/sshd_config && \
    echo "KbdInteractiveAuthentication no" >> /etc/ssh/sshd_config && \
    echo "PubkeyAuthentication yes" >> /etc/ssh/sshd_config && \
    echo "UsePAM no" >> /etc/ssh/sshd_config && \
    echo "StrictModes no" >> /etc/ssh/sshd_config
`, baseDockerImage, pkgsArg)
}

func defaultInitScript() string {
	return `#!/bin/bash
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
mount -t proc proc /proc 2>/dev/null || true
mount -t sysfs sysfs /sys 2>/dev/null || true
mount -t devtmpfs devtmpfs /dev 2>/dev/null || true
mount -t tmpfs tmpfs /dev/shm 2>/dev/null || true
mount -t tmpfs tmpfs /run 2>/dev/null || true
mkdir -p /dev/pts /dev/shm /run/sshd /run/lock
chmod 0755 /run/sshd
mount -t devpts devpts /dev/pts -o mode=620,ptmxmode=666,gid=5 2>/dev/null || true

# Allow non-root users (including compute) to send ICMP echo requests
sysctl -w net.ipv4.ping_group_range="0 2147483647" >/dev/null 2>&1 || echo "0 2147483647" > /proc/sys/net/ipv4/ping_group_range 2>/dev/null || true

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
    if [ -x /sbin/getty ]; then
        /sbin/getty -L 115200 ttyS0 vt100 || sleep 1
    elif [ -x /sbin/agetty ]; then
        /sbin/agetty -L 115200 ttyS0 vt100 || sleep 1
    else
        sleep 3600
    fi
done
`
}
