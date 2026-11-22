package image

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/storage"
	"github.com/google/uuid"
)

var validPackageNameRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.+_-]*$`)

// ValidatePackageName ensures package name matches ^[a-zA-Z0-9][a-zA-Z0-9.+_-]*$
// and does not contain shell metacharacters or flags starting with '-'.
func ValidatePackageName(pkg string) error {
	p := strings.TrimSpace(pkg)
	if p == "" {
		return fmt.Errorf("package name cannot be empty")
	}
	if strings.HasPrefix(p, "-") {
		return fmt.Errorf("invalid package name %q: flags not allowed", pkg)
	}
	if !validPackageNameRegex.MatchString(p) {
		return fmt.Errorf("invalid package name %q: must contain only letters, numbers, dashes, underscores, dots, or plus signs, and start with an alphanumeric character", pkg)
	}
	return nil
}

// ValidateBuildRequest performs security and sanity validation on image build parameters.
func ValidateBuildRequest(req BuildRequest) error {
	if strings.TrimSpace(req.Dockerfile) != "" {
		return fmt.Errorf("raw Dockerfile builds are disabled for security isolation: custom images must use packages and commands")
	}
	cleanName := strings.TrimSpace(req.Name)
	if cleanName == "" {
		return fmt.Errorf("image name cannot be empty")
	}
	for _, r := range cleanName {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.') {
			return fmt.Errorf("invalid image name '%s': must contain only letters, numbers, dashes, underscores, and dots", cleanName)
		}
	}
	if cleanName == "." || cleanName == ".." || strings.Contains(cleanName, "..") || strings.HasPrefix(cleanName, ".") || strings.HasPrefix(cleanName, "-") {
		return fmt.Errorf("invalid image name '%s': path traversal not allowed", cleanName)
	}
	for _, p := range req.Packages {
		p = strings.TrimSpace(p)
		if p == "" {
			return fmt.Errorf("package entry cannot be empty")
		}
		for _, sp := range strings.Split(p, ",") {
			sp = strings.TrimSpace(sp)
			if sp == "" {
				return fmt.Errorf("package entry cannot be empty")
			}
			if err := ValidatePackageName(sp); err != nil {
				return err
			}
		}
	}
	return nil
}

// BuildRequest defines parameters for building a new custom image with pre-installed packages
type BuildRequest struct {
	Name        string   `json:"name"`
	OwnerID     string   `json:"owner_id,omitempty"`
	Description string   `json:"description,omitempty"`
	BaseImage   string   `json:"base_image,omitempty"` // defaults to "ubuntu-24.04"
	Packages    []string `json:"packages,omitempty"`
	Command     string   `json:"command,omitempty"`
	Commands    []string `json:"commands,omitempty"`
	Dockerfile  string   `json:"dockerfile,omitempty"`
	Script      string   `json:"script,omitempty"`
	SizeMB      int      `json:"size_mb,omitempty"` // defaults to 2048 MB
}

// VMCreateRequest defines parameters for creating an ephemeral builder microVM.
type VMCreateRequest struct {
	Name     string `json:"name"`
	OwnerID  string `json:"owner_id,omitempty"`
	Image    string `json:"image,omitempty"`
	VCPUs    int    `json:"vcpus"`
	MemoryMB int    `json:"memory_mb,omitempty"`
	DiskMB   int    `json:"disk_mb,omitempty"`
}

// VMExecRequest defines parameters for executing a command inside an ephemeral microVM.
type VMExecRequest struct {
	Command string `json:"command"`
	User    string `json:"user,omitempty"`
	Timeout int    `json:"timeout_seconds,omitempty"`
}

// VMExecResponse represents execution output from a command inside a microVM.
type VMExecResponse struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	DurationMs int64  `json:"duration_ms"`
}

// VMRunner provides VM lifecycle operations for building custom images inside isolated microVMs.
type VMRunner interface {
	CreateVM(ctx context.Context, req VMCreateRequest) (*db.VM, error)
	RunVM(ctx context.Context, vmID string) error
	StopVM(ctx context.Context, vmID string) error
	DeleteVM(ctx context.Context, vmID string) error
	ExecVM(ctx context.Context, vmID string, req VMExecRequest) (*VMExecResponse, error)
	GetVM(ctx context.Context, vmID string) (*db.VM, error)
}

// BuildFunc defines the build runner function signature
type BuildFunc func(ctx context.Context, req BuildRequest, outputPath string, sshPubKey string) error

// Builder handles building and registering custom VM images
type Builder struct {
	store        *db.Store
	storMgr      *storage.Manager
	buildFunc    BuildFunc
	vmRunner     VMRunner
	bootTimeout  time.Duration
	buildTimeout time.Duration
}

// NewBuilder creates a new image builder
func NewBuilder(store *db.Store, storMgr *storage.Manager) *Builder {
	b := &Builder{
		store:        store,
		storMgr:      storMgr,
		bootTimeout:  90 * time.Second,
		buildTimeout: 30 * time.Minute,
	}
	b.buildFunc = b.defaultMicroVMBuild
	return b
}

// SetBuildFunc overrides the build runner (useful for tests or custom builders)
func (b *Builder) SetBuildFunc(fn BuildFunc) {
	b.buildFunc = fn
}

// SetVMRunner configures the VM runner used for isolated microVM builds
func (b *Builder) SetVMRunner(runner VMRunner) {
	b.vmRunner = runner
}

// GetVMRunner returns the configured VM runner
func (b *Builder) GetVMRunner() VMRunner {
	return b.vmRunner
}

// SetBootTimeout sets the timeout for waiting for the ephemeral builder microVM to boot
func (b *Builder) SetBootTimeout(d time.Duration) {
	if d > 0 {
		b.bootTimeout = d
	}
}

// SetBuildTimeout sets the timeout for guest build script execution
func (b *Builder) SetBuildTimeout(d time.Duration) {
	if d > 0 {
		b.buildTimeout = d
	}
}

// BuildImage creates a database record, runs the build process, and stores the image
func (b *Builder) BuildImage(ctx context.Context, req BuildRequest) (*db.CustomImage, error) {
	if err := ValidateBuildRequest(req); err != nil {
		return nil, err
	}

	cleanName := strings.TrimSpace(req.Name)

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
			for _, sp := range strings.Split(p, ",") {
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
	if filepath.Base(rootfsFilename) != rootfsFilename || strings.Contains(rootfsFilename, "..") || strings.Contains(rootfsFilename, "/") || strings.Contains(rootfsFilename, "\\") {
		return nil, fmt.Errorf("invalid rootfs filename '%s': path traversal detected", rootfsFilename)
	}
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

	// Load SSH public key for injection if needed
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

// defaultMicroVMBuild builds a custom image inside an ephemeral Firecracker microVM.
func (b *Builder) defaultMicroVMBuild(ctx context.Context, req BuildRequest, outputPath string, sshPubKey string) error {
	if b.vmRunner == nil {
		return fmt.Errorf("no microVM runner configured: host chroot and host docker builds are disabled for security isolation")
	}
	if strings.TrimSpace(req.Dockerfile) != "" {
		return fmt.Errorf("Dockerfile builds are disabled for security isolation: custom images must use packages and script commands")
	}

	sizeMB := req.SizeMB
	if sizeMB <= 0 {
		sizeMB = 2048
	}

	uid := uuid.New().String()
	shortID := strings.ReplaceAll(uid, "-", "")[:8]
	ephemeralName := fmt.Sprintf("builder-%s-%s", req.Name, shortID)
	ownerID := req.OwnerID
	if ownerID == "" {
		ownerID = "system"
	}

	// 1. Prepare/create ephemeral builder microVM
	createdVM, err := b.vmRunner.CreateVM(ctx, VMCreateRequest{
		Name:     ephemeralName,
		OwnerID:  ownerID,
		Image:    req.BaseImage,
		VCPUs:    2,
		MemoryMB: 2048,
		DiskMB:   sizeMB,
	})
	if err != nil {
		return fmt.Errorf("failed to create ephemeral builder microVM: %w", err)
	}
	vmID := createdVM.ID

	// Guaranteed defer cleanup of the ephemeral VM on error or completion
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if delErr := b.vmRunner.DeleteVM(cleanupCtx, vmID); delErr != nil {
			log.Printf("[image-builder] warning: failed to delete ephemeral builder VM %s: %v", vmID, delErr)
		}
	}()

	// 2. Boot the ephemeral microVM
	if err := b.vmRunner.RunVM(ctx, vmID); err != nil {
		return fmt.Errorf("failed to start ephemeral builder microVM: %w", err)
	}

	// Wait for builder microVM to become reachable
	if err := b.waitVMReady(ctx, vmID); err != nil {
		return fmt.Errorf("ephemeral builder microVM not ready: %w", err)
	}

	// Build and run the in-guest package install and custom scripts
	buildScript := buildGuestBuildScript(req)
	execResp, err := b.vmRunner.ExecVM(ctx, vmID, VMExecRequest{
		Command: buildScript,
		User:    "root",
		Timeout: int(b.buildTimeout.Seconds()),
	})
	if err != nil {
		return fmt.Errorf("build execution failed inside guest microVM: %w", err)
	}
	if execResp == nil || execResp.ExitCode != 0 {
		exitCode := -1
		errMsg := ""
		if execResp != nil {
			exitCode = execResp.ExitCode
			errMsg = strings.TrimSpace(execResp.Stderr)
			if errMsg == "" {
				errMsg = strings.TrimSpace(execResp.Stdout)
			}
		}
		if errMsg == "" {
			errMsg = fmt.Sprintf("exit code %d", exitCode)
		}
		return fmt.Errorf("build commands inside microVM failed: %s", errMsg)
	}

	// 3. Cleanly stop the builder microVM
	if err := b.vmRunner.StopVM(ctx, vmID); err != nil {
		return fmt.Errorf("failed to cleanly stop builder microVM: %w", err)
	}

	// Retrieve stopped VM to get final disk path
	stoppedVM, err := b.vmRunner.GetVM(ctx, vmID)
	if err != nil || stoppedVM == nil {
		stoppedVM = createdVM
	}
	srcDiskPath := stoppedVM.DiskPath
	if srcDiskPath == "" {
		return fmt.Errorf("builder microVM has no recorded disk path")
	}
	if fi, err := os.Stat(srcDiskPath); err != nil || fi.Size() == 0 {
		return fmt.Errorf("builder microVM disk missing or empty at %s: %v", srcDiskPath, err)
	}

	// Move/capture its clean stopped ext4 rootfs disk into BaseImagesDir/<rootfsFilename>
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}
	_ = os.Remove(outputPath)

	if err := os.Rename(srcDiskPath, outputPath); err != nil {
		// If cross-device, copy with sparse CloneFileCOW
		if cloneErr := storage.CloneFileCOW(srcDiskPath, outputPath); cloneErr != nil {
			return fmt.Errorf("failed to capture builder disk to %s: %w", outputPath, cloneErr)
		}
		_ = os.Remove(srcDiskPath)
	}

	// Verify the output file exists and is not empty
	outFi, err := os.Stat(outputPath)
	if err != nil {
		return fmt.Errorf("verified output rootfs does not exist at %s: %w", outputPath, err)
	}
	if outFi.Size() == 0 {
		_ = os.Remove(outputPath)
		return fmt.Errorf("verified output rootfs at %s is empty", outputPath)
	}

	return nil
}

func (b *Builder) waitVMReady(ctx context.Context, vmID string) error {
	deadline := time.Now().Add(b.bootTimeout)
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("build canceled while waiting for sandbox: %w", err)
		}
		resp, err := b.vmRunner.ExecVM(ctx, vmID, VMExecRequest{
			Command: "true",
			User:    "root",
			Timeout: 5,
		})
		if err == nil && resp != nil && resp.ExitCode == 0 {
			return nil
		}
		// Also try user compute if root SSH isn't ready yet
		if resp == nil || resp.ExitCode != 0 {
			resp2, err2 := b.vmRunner.ExecVM(ctx, vmID, VMExecRequest{
				Command: "true",
				User:    "compute",
				Timeout: 5,
			})
			if err2 == nil && resp2 != nil && resp2.ExitCode == 0 {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("builder microVM did not become reachable within %s", b.bootTimeout)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("build canceled while waiting for sandbox: %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

func buildGuestBuildScript(req BuildRequest) string {
	var sb strings.Builder
	sb.WriteString("set -euo pipefail\n")
	sb.WriteString("export DEBIAN_FRONTEND=noninteractive\n")
	sb.WriteString("export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin\n")

	if len(req.Packages) > 0 {
		sb.WriteString("apt-get update -y\n")
		sb.WriteString("apt-get install -y --no-install-recommends")
		for _, pkg := range req.Packages {
			sb.WriteString(" " + shellQuote(pkg))
		}
		sb.WriteString("\n")
	}

	for _, cmd := range req.Commands {
		cmd = strings.TrimSpace(cmd)
		if cmd != "" {
			sb.WriteString(fmt.Sprintf("/bin/bash -e -c %s\n", shellQuote(cmd)))
		}
	}

	if cmd := strings.TrimSpace(req.Command); cmd != "" {
		sb.WriteString(fmt.Sprintf("/bin/bash -e -c %s\n", shellQuote(cmd)))
	}

	if script := strings.TrimSpace(req.Script); script != "" {
		sb.WriteString(fmt.Sprintf("/bin/bash -e -c %s\n", shellQuote(script)))
	}

	// Clean up builder-specific transient state inside the guest
	sb.WriteString("apt-get clean\n")
	sb.WriteString("rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/*\n")
	sb.WriteString("sync\n")

	return sb.String()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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
%s`, baseDockerImage, pkgsArg, formatCustomCommands(req))
}

func formatCustomCommands(req BuildRequest) string {
	allCommands := append([]string{}, req.Commands...)
	if strings.TrimSpace(req.Command) != "" {
		allCommands = append(allCommands, strings.TrimSpace(req.Command))
	}
	if len(allCommands) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n")
	for _, c := range allCommands {
		c = strings.TrimSpace(c)
		if c != "" {
			b.WriteString(fmt.Sprintf("RUN /bin/bash -c %q\n", c))
		}
	}
	return b.String()
}

func defaultInitScript() string {
	return `#!/bin/bash
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
