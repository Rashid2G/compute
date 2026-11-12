package storage

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// SSH user accounts configured inside a sandbox image. Password authentication
// is applied to these accounts when a password is supplied, and they are locked
// when it is not.
var defaultSSHUsers = []string{"compute", "root"}

// sshd keeps the first value it obtains for most keywords, and Include
// processes sshd_config.d files in lexical order. Naming the drop-in 00- makes
// the managed policy win over any vendor or image-provided drop-in.
const sshAccessDropInPath = "/etc/ssh/sshd_config.d/00-compute-access.conf"

// SSHAccess describes how SSH authentication should be configured in a guest
// disk image. PublicKey is always allowed. Password is optional: when empty,
// password authentication is disabled and the managed accounts are locked.
type SSHAccess struct {
	PublicKey string
	// PublicKeys may carry additional keys (for example account-wide keys) that
	// are authorized alongside PublicKey. The daemon's internal automation key
	// is always added.
	PublicKeys []string
	Password   string
	Users      []string
}

// imageFS abstracts the small set of file operations needed to configure SSH
// access inside an ext4 image. Implementations use a loop mount when available
// and debugfs otherwise.
type imageFS interface {
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte, mode os.FileMode, uid, gid int) error
	MkdirAll(path string, mode os.FileMode, uid, gid int) error
}

// ConfigureSSHAccess applies authorized keys, account password state, and the
// sshd password policy to a guest disk image.
//
// If SSHAccess.Password is empty, password authentication is turned off and the
// managed accounts are locked. Otherwise the password is hashed and installed
// for every managed account, and password authentication is enabled.
func (m *Manager) ConfigureSSHAccess(diskPath string, access SSHAccess) error {
	if strings.TrimSpace(diskPath) == "" {
		return errors.New("disk path is empty")
	}
	if _, err := os.Stat(diskPath); err != nil {
		return fmt.Errorf("stat disk image: %w", err)
	}

	users := access.Users
	if len(users) == 0 {
		users = defaultSSHUsers
	}
	keysContent := m.sshAuthorizedKeys(append([]string{access.PublicKey}, access.PublicKeys...)...)

	var attempts []func() error
	if os.Geteuid() == 0 {
		attempts = append(attempts, func() error {
			fs, err := newMountImageFS(diskPath)
			if err != nil {
				return err
			}
			defer fs.Close()
			return applySSHAccess(fs, access, users, keysContent)
		})
	}
	attempts = append(attempts, func() error {
		return applySSHAccess(newDebugfsImageFS(diskPath), access, users, keysContent)
	})

	var failures []string
	for _, attempt := range attempts {
		if err := attempt(); err == nil {
			return nil
		} else {
			failures = append(failures, err.Error())
		}
	}
	return fmt.Errorf("configure SSH access on %s: %s", diskPath, strings.Join(failures, "; "))
}

func applySSHAccess(fs imageFS, access SSHAccess, users []string, keysContent string) error {
	if err := applyAuthorizedKeys(fs, users, keysContent); err != nil {
		return err
	}

	shadow, err := fs.ReadFile("/etc/shadow")
	if err != nil {
		return fmt.Errorf("read /etc/shadow: %w", err)
	}

	var passwordHash string
	if access.Password != "" {
		hashed, err := bcrypt.GenerateFromPassword([]byte(access.Password), bcrypt.DefaultCost)
		if err != nil {
			return fmt.Errorf("hash SSH password: %w", err)
		}
		passwordHash = string(hashed)
	}

	updatedShadow, matched := updateShadowUsers(shadow, users, passwordHash)
	if access.Password != "" && matched == 0 {
		return fmt.Errorf("none of the SSH users (%s) exist in /etc/shadow", strings.Join(users, ", "))
	}
	if err := fs.WriteFile("/etc/shadow", updatedShadow, 0600, 0, 0); err != nil {
		return fmt.Errorf("write /etc/shadow: %w", err)
	}

	sshdConfig, err := fs.ReadFile("/etc/ssh/sshd_config")
	if err != nil {
		return fmt.Errorf("read /etc/ssh/sshd_config: %w", err)
	}
	passwordEnabled := access.Password != ""
	updatedConfig := updateSSHDConfig(sshdConfig, passwordEnabled)
	if err := fs.WriteFile("/etc/ssh/sshd_config", updatedConfig, 0644, 0, 0); err != nil {
		return fmt.Errorf("write /etc/ssh/sshd_config: %w", err)
	}
	if err := fs.MkdirAll(filepath.Dir(sshAccessDropInPath), 0755, 0, 0); err != nil {
		return fmt.Errorf("create sshd_config.d: %w", err)
	}
	if err := fs.WriteFile(sshAccessDropInPath, []byte(dropInSSHDConfig(passwordEnabled)), 0644, 0, 0); err != nil {
		return fmt.Errorf("write sshd drop-in: %w", err)
	}
	return nil
}

// ConfigureAuthorizedKeys replaces authorized_keys for the managed SSH users
// without touching password state or sshd policy. It is used to propagate
// account-wide keys to existing sandboxes, including stopped ones whose disks
// can be edited offline.
func (m *Manager) ConfigureAuthorizedKeys(diskPath string, pubKeys ...string) error {
	if strings.TrimSpace(diskPath) == "" {
		return errors.New("disk path is empty")
	}
	if _, err := os.Stat(diskPath); err != nil {
		return fmt.Errorf("stat disk image: %w", err)
	}

	keysContent := m.sshAuthorizedKeys(pubKeys...)

	var attempts []func() error
	if os.Geteuid() == 0 {
		attempts = append(attempts, func() error {
			fs, err := newMountImageFS(diskPath)
			if err != nil {
				return err
			}
			defer fs.Close()
			return applyAuthorizedKeys(fs, defaultSSHUsers, keysContent)
		})
	}
	attempts = append(attempts, func() error {
		return applyAuthorizedKeys(newDebugfsImageFS(diskPath), defaultSSHUsers, keysContent)
	})

	var failures []string
	for _, attempt := range attempts {
		if err := attempt(); err == nil {
			return nil
		} else {
			failures = append(failures, err.Error())
		}
	}
	return fmt.Errorf("configure authorized_keys on %s: %s", diskPath, strings.Join(failures, "; "))
}

// AuthorizedKeys renders the managed authorized_keys content for the given
// public keys. The daemon's internal automation key is always included so the
// control plane can keep reaching the sandbox.
func (m *Manager) AuthorizedKeys(pubKeys ...string) string {
	return m.sshAuthorizedKeys(pubKeys...)
}

func applyAuthorizedKeys(fs imageFS, users []string, keysContent string) error {
	// Replace authorized_keys for the managed accounts so a sandbox created from
	// a template or clone never inherits keys that no longer belong to it.
	for _, user := range users {
		dir, uid, gid, ok := sshHomeDir(user)
		if !ok {
			continue
		}
		if err := fs.MkdirAll(dir, 0700, uid, gid); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
		if err := fs.WriteFile(filepath.Join(dir, "authorized_keys"), []byte(keysContent), 0600, uid, gid); err != nil {
			return fmt.Errorf("write authorized_keys for %s: %w", user, err)
		}
	}
	return nil
}

func sshHomeDir(user string) (dir string, uid int, gid int, ok bool) {
	switch user {
	case "root":
		return "/root/.ssh", 0, 0, true
	case "compute":
		return "/home/compute/.ssh", 1000, 1000, true
	default:
		return "", 0, 0, false
	}
}

// updateShadowUsers replaces the password field for the requested users. An
// empty hash locks the account; a non-empty hash sets it. It returns the number
// of matching accounts found.
func updateShadowUsers(shadow []byte, users []string, passwordHash string) ([]byte, int) {
	selected := make(map[string]struct{}, len(users))
	for _, user := range users {
		selected[user] = struct{}{}
	}
	value := passwordHash
	if value == "" {
		value = "!"
	}

	lines := strings.Split(string(shadow), "\n")
	matched := 0
	for i, line := range lines {
		if line == "" {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 2 {
			continue
		}
		if _, ok := selected[fields[0]]; !ok {
			continue
		}
		fields[1] = value
		lines[i] = strings.Join(fields, ":")
		matched++
	}
	return []byte(strings.Join(lines, "\n")), matched
}

type sshdDirective struct {
	key   string
	label string
	value string
}

func sshdDirectives(passwordEnabled bool) []sshdDirective {
	password := "no"
	if passwordEnabled {
		password = "yes"
	}
	return []sshdDirective{
		{key: "passwordauthentication", label: "PasswordAuthentication", value: password},
		{key: "kbdinteractiveauthentication", label: "KbdInteractiveAuthentication", value: "no"},
		{key: "challengeresponseauthentication", label: "ChallengeResponseAuthentication", value: "no"},
		{key: "pubkeyauthentication", label: "PubkeyAuthentication", value: "yes"},
	}
}

// updateSSHDConfig rewrites the global authentication directives that control
// key/password access. Directives inside Match blocks are left untouched.
func updateSSHDConfig(config []byte, passwordEnabled bool) []byte {
	directives := sshdDirectives(passwordEnabled)
	byKey := make(map[string]sshdDirective, len(directives))
	for _, d := range directives {
		byKey[d.key] = d
	}

	lines := strings.Split(string(config), "\n")
	firstMatch := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if lower == "match" || strings.HasPrefix(lower, "match ") {
			firstMatch = i
			break
		}
	}
	globalEnd := len(lines)
	if firstMatch >= 0 {
		globalEnd = firstMatch
	}

	seen := make(map[string]bool, len(directives))
	for i := 0; i < globalEnd; i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) == 0 {
			continue
		}
		key := strings.ToLower(fields[0])
		directive, ok := byKey[key]
		if !ok {
			continue
		}
		indent := lines[i][:len(lines[i])-len(strings.TrimLeft(lines[i], " \t"))]
		lines[i] = indent + directive.label + " " + directive.value
		seen[key] = true
	}

	var missing []string
	for _, directive := range directives {
		if !seen[directive.key] {
			missing = append(missing, directive.label+" "+directive.value)
		}
	}
	if len(missing) == 0 {
		return []byte(strings.Join(lines, "\n"))
	}

	insertAt := len(lines)
	if firstMatch >= 0 {
		insertAt = firstMatch
	} else if len(lines) > 0 && lines[len(lines)-1] == "" {
		insertAt = len(lines) - 1
	}
	result := make([]string, 0, len(lines)+len(missing))
	result = append(result, lines[:insertAt]...)
	result = append(result, missing...)
	result = append(result, lines[insertAt:]...)
	return []byte(strings.Join(result, "\n"))
}

func dropInSSHDConfig(passwordEnabled bool) string {
	password := "no"
	if passwordEnabled {
		password = "yes"
	}
	return strings.Join([]string{
		"# Managed by Compute. Reapplied whenever a sandbox is created.",
		"# Password access is enabled only when the sandbox was created with a password.",
		"PasswordAuthentication " + password,
		"KbdInteractiveAuthentication no",
		"PubkeyAuthentication yes",
		"",
	}, "\n")
}

func (m *Manager) sshAuthorizedKeys(pubKeys ...string) string {
	var internalPubKey string
	candidates := []string{}
	if privKey := m.GetSSHKeyPath(); privKey != "" {
		candidates = append(candidates, privKey+".pub")
	}
	candidates = append(candidates,
		filepath.Join(m.cfg.BaseImagesDir, "ssh", "id_ed25519.pub"),
		"/var/lib/compute/images/ssh/id_ed25519.pub",
		filepath.Join(os.TempDir(), "compute-ssh", "id_ed25519.pub"),
	)
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".ssh", "id_ed25519.pub"))
	}
	for _, candidate := range candidates {
		if data, err := os.ReadFile(candidate); err == nil && len(strings.TrimSpace(string(data))) > 0 {
			internalPubKey = strings.TrimSpace(string(data))
			break
		}
	}

	var keys []string
	seen := make(map[string]bool)
	add := func(raw string) {
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || seen[line] {
				continue
			}
			seen[line] = true
			keys = append(keys, line)
		}
	}
	if internalPubKey != "" {
		add(internalPubKey)
	}
	for _, key := range pubKeys {
		add(key)
	}
	if len(keys) == 0 {
		return ""
	}
	return strings.Join(keys, "\n") + "\n"
}

type mountImageFS struct {
	root       string
	mountPoint string
}

func newMountImageFS(diskPath string) (*mountImageFS, error) {
	dir, err := os.MkdirTemp("", "compute-ssh-mount-")
	if err != nil {
		return nil, err
	}
	if out, err := exec.Command("mount", "-o", "loop", diskPath, dir).CombinedOutput(); err != nil {
		// Defensive unmount: never remove the directory while it may still be
		// mounted, or RemoveAll would delete the guest filesystem contents.
		_ = exec.Command("umount", dir).Run()
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("mount %s: %w (%s)", diskPath, err, strings.TrimSpace(string(out)))
	}
	return &mountImageFS{root: dir, mountPoint: dir}, nil
}

func (m *mountImageFS) Close() {
	if err := exec.Command("umount", m.mountPoint).Run(); err != nil {
		// Leave the mount point in place rather than risk deleting guest files.
		return
	}
	_ = os.RemoveAll(m.mountPoint)
}

func (m *mountImageFS) hostPath(path string) string {
	return filepath.Join(m.root, strings.TrimPrefix(path, "/"))
}

func (m *mountImageFS) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(m.hostPath(path))
}

func (m *mountImageFS) WriteFile(path string, data []byte, mode os.FileMode, uid, gid int) error {
	hostPath := m.hostPath(path)
	if err := os.MkdirAll(filepath.Dir(hostPath), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(hostPath, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(hostPath, mode); err != nil {
		return err
	}
	return os.Chown(hostPath, uid, gid)
}

func (m *mountImageFS) MkdirAll(path string, mode os.FileMode, uid, gid int) error {
	hostPath := m.hostPath(path)
	if err := os.MkdirAll(hostPath, mode); err != nil {
		return err
	}
	if err := os.Chmod(hostPath, mode); err != nil {
		return err
	}
	return os.Chown(hostPath, uid, gid)
}

type debugfsImageFS struct {
	diskPath string
}

func newDebugfsImageFS(diskPath string) *debugfsImageFS {
	return &debugfsImageFS{diskPath: diskPath}
}

func (d *debugfsImageFS) ReadFile(path string) ([]byte, error) {
	if _, err := exec.LookPath("debugfs"); err != nil {
		return nil, fmt.Errorf("debugfs not available: %w", err)
	}
	tmp, err := os.CreateTemp("", "compute-image-read-")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(tmpPath)
	defer os.Remove(tmpPath)

	out, err := exec.Command("debugfs", "-R", fmt.Sprintf("dump %s %s", path, tmpPath), d.diskPath).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("debugfs dump %s: %w (%s)", path, err, strings.TrimSpace(string(out)))
	}
	data, err := os.ReadFile(tmpPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, os.ErrNotExist
		}
		return nil, err
	}
	return data, nil
}

func (d *debugfsImageFS) WriteFile(path string, data []byte, mode os.FileMode, uid, gid int) error {
	tmp, err := os.CreateTemp("", "compute-image-write-")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	script := fmt.Sprintf("rm %s\nwrite %s %s\nset_inode_field %s mode %#o\nset_inode_field %s uid %d\nset_inode_field %s gid %d\n",
		path, tmpPath, path,
		path, 0o100000|int(mode.Perm()),
		path, uid,
		path, gid,
	)
	return d.runScript(script)
}

func (d *debugfsImageFS) MkdirAll(path string, mode os.FileMode, uid, gid int) error {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	var script strings.Builder
	current := ""
	for _, part := range strings.Split(trimmed, "/") {
		if part == "" {
			continue
		}
		current += "/" + part
		fmt.Fprintf(&script, "mkdir %s\n", current)
	}
	fmt.Fprintf(&script, "set_inode_field %s mode %#o\n", path, 0o040000|int(mode.Perm()))
	fmt.Fprintf(&script, "set_inode_field %s uid %d\n", path, uid)
	fmt.Fprintf(&script, "set_inode_field %s gid %d\n", path, gid)
	return d.runScript(script.String())
}

func (d *debugfsImageFS) runScript(script string) error {
	if _, err := exec.LookPath("debugfs"); err != nil {
		return fmt.Errorf("debugfs not available: %w", err)
	}
	tmp, err := os.CreateTemp("", "compute-image-script-")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(script); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	out, err := exec.Command("debugfs", "-w", "-f", tmpPath, d.diskPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("debugfs: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}
