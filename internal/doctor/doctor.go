// Package doctor performs local installation and runtime health checks and
// returns actionable remediation for every failure. The same checks back the
// daemon readiness endpoint, `computed check`, and `compute doctor`.
package doctor

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/Rashid2G/on-demand-vms/internal/config"
	"golang.org/x/crypto/ssh"
)

// Severity controls whether a failed check blocks readiness.
type Severity string

const (
	// Critical failures make the daemon unready.
	Critical Severity = "critical"
	// Warning failures are reported but do not block readiness.
	Warning Severity = "warning"
	// Info checks are informational only.
	Info Severity = "info"
)

// Check is one diagnostic result.
type Check struct {
	Name        string   `json:"name"`
	OK          bool     `json:"ok"`
	Severity    Severity `json:"severity"`
	Detail      string   `json:"detail,omitempty"`
	Remediation string   `json:"remediation,omitempty"`
}

// Report is the complete diagnostic set.
type Report struct {
	OK     bool    `json:"ok"`
	Checks []Check `json:"checks"`
}

// Run executes the readiness checks for cfg.
func Run(cfg config.GlobalConfig) Report {
	return run(cfg, true)
}

// Preflight runs the checks that must pass before the daemon creates host
// networking. It omits the bridge check because startup creates it.
func Preflight(cfg config.GlobalConfig) Report {
	return run(cfg, false)
}

func run(cfg config.GlobalConfig, includeBridge bool) Report {
	checks := []Check{
		checkOS(),
		checkArch(),
		checkHostNetwork(),
		checkKVM(),
		checkFirecracker(cfg.Server.FirecrackerBin),
		checkKernel(cfg.Images.ImagesDir, cfg.Images.DefaultKernel),
		checkRootfs(cfg.Images.ImagesDir, cfg.Images.DefaultRootfs),
		checkSSHKey(cfg.Images.SSHKeyPath),
		checkImagesDir(cfg.Images.ImagesDir),
		checkDBWritable(cfg.Server.DBPath),
		checkCgroups(),
		checkDiskSpace(cfg.Images.ImagesDir, cfg.Resources.MinFreeDiskSpaceMB),
		checkJailer(cfg.Server.JailerEnabled, cfg.Server.JailerBinary),
	}
	if includeBridge {
		checks = append(checks, checkBridge(cfg.Network.BridgeName))
	}
	report := Report{OK: true, Checks: checks}
	for _, c := range checks {
		if !c.OK && c.Severity == Critical {
			report.OK = false
		}
	}
	return report
}

// Failures returns failed critical and warning checks.
func (r Report) Failures() []Check {
	var out []Check
	for _, c := range r.Checks {
		if !c.OK {
			out = append(out, c)
		}
	}
	return out
}

func ok(name string, severity Severity, detail string) Check {
	return Check{Name: name, OK: true, Severity: severity, Detail: detail}
}

func fail(name string, severity Severity, detail, remediation string) Check {
	return Check{Name: name, OK: false, Severity: severity, Detail: detail, Remediation: remediation}
}

func checkOS() Check {
	if runtime.GOOS != "linux" {
		return fail("os", Critical, fmt.Sprintf("running on %s", runtime.GOOS), "Compute requires Linux with KVM.")
	}
	return ok("os", Critical, "linux")
}

func checkArch() Check {
	switch runtime.GOARCH {
	case "amd64", "arm64":
		return ok("arch", Critical, runtime.GOARCH)
	default:
		return fail("arch", Critical, runtime.GOARCH+" is not supported", "Use an x86_64 (amd64) or arm64 host.")
	}
}

func checkHostNetwork() Check {
	// The daemon must own the host network namespace so the sandbox bridge,
	// TAPs, NAT rules and DNS answers are visible to host clients.
	if _, err := os.Stat("/.dockerenv"); err == nil && os.Getenv("COMPUTE_HOST_NETWORK") != "1" {
		return fail("host_network", Critical, "container is not marked as host-networked",
			"Run the daemon with network_mode: host and set COMPUTE_HOST_NETWORK=1; bridge-mode containers keep the sandbox bridge, TAPs, NAT rules and DNS answers inside the container.")
	}
	return ok("host_network", Critical, "host network namespace")
}

func checkKVM() Check {
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		return fail("kvm", Critical, fmt.Sprintf("/dev/kvm is not readable/writable: %v", err),
			"Enable nested virtualization on the host, load the kvm module, and pass /dev/kvm into the container (chmod 666 /dev/kvm or add the user to the kvm group).")
	}
	_ = f.Close()
	return ok("kvm", Critical, "/dev/kvm read/write")
}

func checkFirecracker(path string) Check {
	if path == "" {
		path = "firecracker"
	}
	if info, err := os.Stat(path); err == nil {
		if info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			return ok("firecracker", Critical, path)
		}
		return fail("firecracker", Critical, path+" is not executable", "Rebuild the daemon image or install a Firecracker binary and set COMPUTE_FIRECRACKER_BIN.")
	}
	if found, err := exec.LookPath(filepath.Base(path)); err == nil {
		return ok("firecracker", Critical, found)
	}
	return fail("firecracker", Critical, path+" not found", "Install Firecracker (the daemon image bundles it) or set COMPUTE_FIRECRACKER_BIN to its absolute path.")
}

func checkKernel(imagesDir, name string) Check {
	path := filepath.Join(imagesDir, firstNonEmpty(name, "vmlinux"))
	if err := validateELF(path); err != nil {
		return fail("kernel", Critical, err.Error(),
			"Run `computed init --images` (or docker compose up to run compute-init) so the kernel is downloaded/verified.")
	}
	return ok("kernel", Critical, path)
}

func checkRootfs(imagesDir, name string) Check {
	path := filepath.Join(imagesDir, firstNonEmpty(name, "ubuntu-24.04.ext4"))
	if err := validateExt4(path); err != nil {
		return fail("rootfs", Critical, err.Error(),
			"Run `computed init --images` (or docker compose up to run compute-init) so the root filesystem is downloaded/verified.")
	}
	return ok("rootfs", Critical, path)
}

func checkSSHKey(path string) Check {
	if path == "" {
		path = "/var/lib/compute/images/ssh/id_ed25519"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fail("ssh_key", Critical, fmt.Sprintf("read %s: %v", path, err),
			"Run `computed init --images` to generate the internal SSH key, or set COMPUTE_SSH_KEY_PATH to a valid key.")
	}
	if _, err := ssh.ParsePrivateKey(data); err != nil {
		return fail("ssh_key", Critical, fmt.Sprintf("%s is not a valid private key: %v", path, err),
			"Move the invalid key aside and re-run `computed init --images`; do not reuse a corrupted key.")
	}
	if info, err := os.Stat(path); err == nil && info.Mode().Perm()&0077 != 0 {
		return fail("ssh_key", Warning, fmt.Sprintf("%s permissions are %o", path, info.Mode().Perm()),
			"Run chmod 600 on the internal SSH key (it must not be group/world readable).")
	}
	return ok("ssh_key", Critical, path)
}

func checkImagesDir(path string) Check {
	if path == "" {
		return fail("images_dir", Critical, "images directory is empty", "Set COMPUTE_IMAGES_DIR.")
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return fail("images_dir", Critical, fmt.Sprintf("images directory %s is missing", path),
			"Create it or run `computed init --images`; the data volume must be mounted at /var/lib/compute.")
	}
	return ok("images_dir", Critical, path)
}

func checkDBWritable(dbPath string) Check {
	if dbPath == "" {
		return fail("database", Critical, "database path is empty", "Set COMPUTE_DB_PATH.")
	}
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fail("database", Critical, fmt.Sprintf("create %s: %v", dir, err), "Ensure the data directory is writable by the daemon.")
	}
	f, err := os.CreateTemp(dir, ".compute-write-check-*")
	if err != nil {
		return fail("database", Critical, fmt.Sprintf("write %s: %v", dir, err), "Ensure the data directory is writable by the daemon.")
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return ok("database", Critical, dbPath)
}

func checkCgroups() Check {
	data, err := os.ReadFile("/sys/fs/cgroup/cgroup.controllers")
	if err != nil {
		return fail("cgroups", Critical, fmt.Sprintf("cgroup v2 controllers unavailable: %v", err),
			"Use a cgroup v2 host and run computed with `cgroup: host` plus /sys/fs/cgroup mounted read-write.")
	}
	controllers := strings.Fields(string(data))
	present := map[string]bool{}
	for _, c := range controllers {
		present[c] = true
	}
	var missing []string
	for _, want := range []string{"cpu", "memory", "pids"} {
		if !present[want] {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		return fail("cgroups", Warning, "missing controllers: "+strings.Join(missing, ", "),
			"Enable the missing controllers in the host's cgroup v2 hierarchy (see docs/install.md).")
	}
	return ok("cgroups", Critical, strings.Join(controllers, " "))
}

func checkBridge(name string) Check {
	if name == "" {
		name = "mft3br0"
	}
	if _, err := net.InterfaceByName(name); err != nil {
		return fail("bridge", Critical, fmt.Sprintf("interface %s is not up: %v", name, err),
			"Run scripts/host-prepare.sh (or restart computed with NET_ADMIN and host networking); the sandbox bridge must exist on the host.")
	}
	return ok("bridge", Critical, name)
}

func checkDiskSpace(path string, minMB int64) Check {
	if minMB <= 0 {
		minMB = 512
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return fail("disk_space", Warning, fmt.Sprintf("statfs %s: %v", path, err), "Ensure the data directory is on a mounted filesystem.")
	}
	availMB := int64(stat.Bavail) * int64(stat.Bsize) / (1024 * 1024)
	if availMB < minMB {
		return fail("disk_space", Warning, fmt.Sprintf("only %d MB free (minimum %d MB)", availMB, minMB),
			"Free disk space or lower COMPUTE_MIN_FREE_DISK_MB; base images and VM disks need room.")
	}
	return ok("disk_space", Warning, fmt.Sprintf("%d MB free", availMB))
}

func checkJailer(enabled bool, path string) Check {
	if !enabled {
		return ok("jailer", Info, "disabled")
	}
	if path == "" {
		path = "/usr/local/bin/jailer"
	}
	if info, err := os.Stat(path); err == nil && info.Mode().Perm()&0111 != 0 {
		return ok("jailer", Critical, path)
	}
	return fail("jailer", Critical, path+" is not executable", "Install jailer or set COMPUTE_JAILER_BIN; disable with COMPUTE_JAILER_ENABLED=false.")
}

func validateELF(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	defer f.Close()
	head := make([]byte, 4)
	if _, err := f.Read(head); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if string(head) != "\x7fELF" {
		return fmt.Errorf("%s is not an ELF kernel", path)
	}
	return nil
}

func validateExt4(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Seek(1024+56, 0); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	magic := make([]byte, 2)
	if _, err := f.Read(magic); err != nil {
		return fmt.Errorf("%s is not an ext4 image: %w", path, err)
	}
	if magic[0] != 0x53 || magic[1] != 0xEF {
		return fmt.Errorf("%s is not an ext4 image", path)
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
