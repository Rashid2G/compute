package vm

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Rashid2G/compute/internal/db"
	"golang.org/x/crypto/bcrypt"
)

// SSHKeySyncFailure describes one sandbox that could not be updated.
type SSHKeySyncFailure struct {
	VMID  string `json:"vm_id"`
	Name  string `json:"name,omitempty"`
	Error string `json:"error"`
}

// SSHKeySyncResult reports an account-wide key reconciliation pass.
type SSHKeySyncResult struct {
	Applied []string            `json:"applied"`
	Failed  []SSHKeySyncFailure `json:"failed"`
	Skipped []string            `json:"skipped"`
}

// GlobalSSHKeys returns the account-wide public keys (all_sandboxes) for one
// owner. These keys are embedded into every sandbox the owner creates and are
// kept in sync on existing sandboxes.
func (m *Manager) GlobalSSHKeys(ctx context.Context, ownerID string) ([]string, error) {
	if m.store == nil {
		return nil, nil
	}
	keys, err := m.store.ListSSHKeys(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	var result []string
	for _, key := range keys {
		if key != nil && key.AllSandboxes && strings.TrimSpace(key.PublicKey) != "" {
			result = append(result, strings.TrimSpace(key.PublicKey))
		}
	}
	return result, nil
}

// SyncSSHKeysForVM reconciles the managed authorized_keys for one sandbox. A
// running sandbox is updated over its existing SSH control channel; a stopped,
// paused, or created sandbox has its disk edited offline.
func (m *Manager) SyncSSHKeysForVM(ctx context.Context, vmID string) error {
	vm, err := m.GetVM(ctx, vmID)
	if err != nil {
		return err
	}
	if vm == nil {
		return fmt.Errorf("vm %s not found", vmID)
	}
	keys, err := m.GlobalSSHKeys(ctx, vm.OwnerID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(vm.SSHPublicKey) != "" {
		keys = append(keys, vm.SSHPublicKey)
	}
	if m.storMgr == nil {
		return fmt.Errorf("storage manager is not configured")
	}
	content := m.storMgr.AuthorizedKeys(keys...)
	if content == "" {
		return fmt.Errorf("no managed authorized keys available")
	}

	if vm.Status == db.StatusRunning {
		if vm.IPAddress == "" || vm.IPAddress == "dhcp" {
			return fmt.Errorf("vm %s is running without a reachable IP", vmID)
		}
		return m.writeAuthorizedKeysRunning(ctx, vm, content)
	}
	if strings.TrimSpace(vm.DiskPath) == "" {
		return fmt.Errorf("vm %s has no disk path", vmID)
	}
	return m.storMgr.ConfigureAuthorizedKeys(vm.DiskPath, keys...)
}

// SyncExplicitSSHKeysForVM writes the given public keys (plus vm.SSHPublicKey) to a local sandbox.
// Used by remote workers receiving resolved public keys from the control plane.
func (m *Manager) SyncExplicitSSHKeysForVM(ctx context.Context, vmID string, explicitKeys []string) error {
	vm, err := m.GetVM(ctx, vmID)
	if err != nil {
		return err
	}
	if vm == nil {
		return fmt.Errorf("vm %s not found", vmID)
	}
	if m.storMgr == nil {
		return fmt.Errorf("storage manager is not configured")
	}
	keys := make([]string, 0, len(explicitKeys)+1)
	for _, k := range explicitKeys {
		k = strings.TrimSpace(k)
		if k != "" {
			keys = append(keys, k)
		}
	}
	if strings.TrimSpace(vm.SSHPublicKey) != "" {
		keys = append(keys, vm.SSHPublicKey)
	}
	content := m.storMgr.AuthorizedKeys(keys...)
	if content == "" {
		return fmt.Errorf("no managed authorized keys available")
	}
	if vm.Status == db.StatusRunning {
		if vm.IPAddress == "" || vm.IPAddress == "dhcp" {
			return fmt.Errorf("vm %s is running without a reachable IP", vmID)
		}
		return m.writeAuthorizedKeysRunning(ctx, vm, content)
	}
	if strings.TrimSpace(vm.DiskPath) == "" {
		return fmt.Errorf("vm %s has no disk path", vmID)
	}
	return m.storMgr.ConfigureAuthorizedKeys(vm.DiskPath, keys...)
}

// ReplaceSandboxSSHKey updates the explicit per-sandbox key on every sandbox
// owned by ownerID that still references oldKey. It returns the affected VM IDs
// so callers can reconcile their authorized_keys immediately.
func (m *Manager) ReplaceSandboxSSHKey(ctx context.Context, ownerID, oldKey, newKey string) ([]string, error) {
	if m.store == nil {
		return nil, fmt.Errorf("store is not configured")
	}
	oldKey = strings.TrimSpace(oldKey)
	newKey = strings.TrimSpace(newKey)
	if oldKey == "" || oldKey == newKey {
		return nil, nil
	}
	vms, err := m.store.ListVMsByOwner(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	var affected []string
	for _, vm := range vms {
		if vm == nil || strings.TrimSpace(vm.SSHPublicKey) != oldKey {
			continue
		}
		vm.SSHPublicKey = newKey
		if err := m.saveVMRecord(ctx, vm); err != nil {
			return affected, err
		}
		affected = append(affected, vm.ID)
	}
	return affected, nil
}

// SyncSSHKeysForOwner applies account-wide keys to every sandbox owned by
// ownerID. It is best-effort per sandbox: one unreachable guest does not stop
// the rest from being updated.
func (m *Manager) SyncSSHKeysForOwner(ctx context.Context, ownerID string) (*SSHKeySyncResult, error) {
	if m.store == nil {
		return nil, fmt.Errorf("store is not configured")
	}
	vms, err := m.store.ListVMsByOwner(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	result := &SSHKeySyncResult{}
	for _, vm := range vms {
		if vm == nil || IsRemoteVM(vm) {
			continue
		}
		if err := m.SyncSSHKeysForVM(ctx, vm.ID); err != nil {
			result.Failed = append(result.Failed, SSHKeySyncFailure{VMID: vm.ID, Name: vm.Name, Error: err.Error()})
			continue
		}
		result.Applied = append(result.Applied, vm.ID)
	}
	return result, nil
}

// maybeSyncSSHKeys reconciles managed keys in the background after a sandbox
// starts or resumes. It retries briefly because guest sshd may still be
// booting; failures are logged, never fatal to the sandbox lifecycle.
func (m *Manager) maybeSyncSSHKeys(vm *db.VM) {
	if vm == nil || m.store == nil || m.storMgr == nil {
		return
	}
	m.mu.RLock()
	hasCustomLauncher := m.launcher != nil && m.guestSyncer == nil
	m.mu.RUnlock()
	if hasCustomLauncher {
		return
	}
	go func(vmID string) {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		var lastErr error
		for {
			if err := m.SyncSSHKeysForVM(ctx, vmID); err == nil {
				return
			} else {
				lastErr = err
			}
			select {
			case <-ctx.Done():
				log.Printf("[vm-manager] %s: account-wide SSH key sync skipped: %v", vmID, lastErr)
				return
			case <-time.After(2 * time.Second):
			}
		}
	}(vm.ID)
}

// writeAuthorizedKeysRunning replaces authorized_keys on both managed accounts
// through the guest's SSH daemon. The key material is base64-encoded so key
// comments and options cannot break the shell command.
func (m *Manager) writeAuthorizedKeysRunning(ctx context.Context, vm *db.VM, content string) error {
	encoded := base64.StdEncoding.EncodeToString([]byte(content))

	// Generate an unguessable bcrypt placeholder hash in Go to unlock legacy
	// accounts ('!' or '*') so OpenSSH 9.6 with UsePAM=no allows public-key login.
	placeholderBuf := make([]byte, 32)
	_, _ = rand.Read(placeholderBuf)
	bcryptHashBytes, _ := bcrypt.GenerateFromPassword([]byte(hex.EncodeToString(placeholderBuf)), bcrypt.DefaultCost)
	bcryptHash := string(bcryptHashBytes)

	script := fmt.Sprintf(`set -e
for u in root compute; do
  home="$(getent passwd "$u" | cut -d: -f6)"
  [ -n "$home" ] || continue
  d="$home/.ssh"
  mkdir -p "$d"
  printf '%%s' '%s' | base64 -d > "$d/authorized_keys"
  chmod 700 "$d"
  chmod 600 "$d/authorized_keys"
  chown "$u:$u" "$d" "$d/authorized_keys" 2>/dev/null || chown "$u" "$d" "$d/authorized_keys" || true
  shadow_entry="$(getent shadow "$u" 2>/dev/null || true)"
  if [ -n "$shadow_entry" ]; then
    pw_field="$(printf '%%s' "$shadow_entry" | cut -d: -f2)"
    case "$pw_field" in
      '!'*|'*'*|'')
        sed -i "s|^${u}:[^:]*:|${u}:%s:|" /etc/shadow 2>/dev/null || true
        ;;
    esac
  fi
done
`, encoded, bcryptHash)

	target := vm.InternalID
	if target == "" {
		target = vm.ID
	}
	res, err := m.ExecVM(ctx, target, ExecRequest{Command: script, User: "root", Timeout: 30})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("write authorized_keys: %s", strings.TrimSpace(res.Stderr))
	}
	return nil
}
