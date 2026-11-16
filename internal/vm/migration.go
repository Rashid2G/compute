package vm

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/network"
	"github.com/Rashid2G/compute/pkg/api"
	"github.com/google/uuid"
)

const defaultMigrationChunkSize = 4 * 1024 * 1024 // 4 MiB

func validateSafeVMKey(key string) error {
	if key == "" {
		return fmt.Errorf("vm_id is required")
	}
	if key == "." || key == ".." {
		return fmt.Errorf("invalid vm_id %q", key)
	}
	if strings.ContainsAny(key, "/\\\x00") || strings.Contains(key, "..") {
		return fmt.Errorf("invalid vm_id %q: path traversal not allowed", key)
	}
	for _, r := range key {
		safe := (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.'
		if !safe {
			return fmt.Errorf("invalid vm_id %q: unsupported character %q", key, r)
		}
	}
	if filepath.Base(key) != key {
		return fmt.Errorf("invalid vm_id %q", key)
	}
	return nil
}

func verifyPathInsideVMsDir(vmsDir, targetPath string) error {
	if strings.TrimSpace(vmsDir) == "" {
		return fmt.Errorf("vms directory not configured")
	}
	absVMsDir, err := filepath.Abs(vmsDir)
	if err != nil {
		return fmt.Errorf("resolve vms dir: %w", err)
	}
	absTarget, err := filepath.Abs(targetPath)
	if err != nil {
		return fmt.Errorf("resolve target path: %w", err)
	}
	absTargetDir := filepath.Dir(absTarget)
	relDir, err := filepath.Rel(absVMsDir, absTargetDir)
	if err != nil {
		return fmt.Errorf("validate target dir: %w", err)
	}
	if relDir == "." || relDir == ".." || strings.HasPrefix(relDir, "..") {
		return fmt.Errorf("path traversal rejected: target dir %q escapes vms dir", absTargetDir)
	}
	relFile, err := filepath.Rel(absVMsDir, absTarget)
	if err != nil {
		return fmt.Errorf("validate target file: %w", err)
	}
	if relFile == "." || relFile == ".." || strings.HasPrefix(relFile, "..") {
		return fmt.Errorf("path traversal rejected: target file %q escapes vms dir", absTarget)
	}
	return nil
}

// ArtifactPath returns the local path for kind ("disk", "snapshot", "mem") on vm.
func (m *Manager) ArtifactPath(vm *db.VM, kind string) (string, error) {
	if vm == nil {
		return "", fmt.Errorf("vm is nil")
	}
	vmKey := vm.InternalID
	if vmKey == "" {
		vmKey = vm.ID
	}
	if vmKey != "" {
		if err := validateSafeVMKey(vmKey); err != nil {
			return "", err
		}
	}
	var resolved string
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", "disk":
		if vm.DiskPath != "" {
			resolved = vm.DiskPath
		} else if m.storMgr != nil && vmKey != "" {
			resolved = filepath.Join(m.storMgr.GetVMDir(vmKey), "disk.ext4")
		} else {
			return "", fmt.Errorf("vm %s has no disk path", vm.ID)
		}
	case "snapshot":
		if vm.SnapshotPath != "" {
			resolved = vm.SnapshotPath
		} else if m.storMgr != nil && vmKey != "" {
			snapPath, _ := m.storMgr.GetSnapshotPaths(vmKey)
			resolved = snapPath
		} else {
			return "", fmt.Errorf("vm %s has no snapshot path", vm.ID)
		}
	case "mem":
		if vm.MemFilePath != "" {
			resolved = vm.MemFilePath
		} else if m.storMgr != nil && vmKey != "" {
			_, memPath := m.storMgr.GetSnapshotPaths(vmKey)
			resolved = memPath
		} else {
			return "", fmt.Errorf("vm %s has no memory file path", vm.ID)
		}
	default:
		return "", fmt.Errorf("unsupported artifact kind %q", kind)
	}
	if m.storMgr != nil {
		if err := verifyPathInsideVMsDir(m.storMgr.GetConfig().VMsDir, resolved); err != nil {
			return "", err
		}
	}
	return resolved, nil
}

func (m *Manager) targetArtifactPath(vmKey, kind string) (string, error) {
	if m.storMgr == nil {
		return "", fmt.Errorf("storage manager not configured")
	}
	if err := validateSafeVMKey(vmKey); err != nil {
		return "", err
	}
	vmDir := m.storMgr.GetVMDir(vmKey)
	snapPath, memPath := m.storMgr.GetSnapshotPaths(vmKey)
	var targetPath string
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", "disk":
		targetPath = filepath.Join(vmDir, "disk.ext4")
	case "snapshot":
		targetPath = snapPath
	case "mem":
		targetPath = memPath
	default:
		return "", fmt.Errorf("unsupported artifact kind %q", kind)
	}
	if err := verifyPathInsideVMsDir(m.storMgr.GetConfig().VMsDir, targetPath); err != nil {
		return "", err
	}
	return targetPath, nil
}

// ExportVMArtifactChunk reads a chunk from a VM's local disk or snapshot file, gzips it, and returns base64 data.
func (m *Manager) ExportVMArtifactChunk(ctx context.Context, vmID, kind string, offset int64, length int) (*api.WorkerVMExportDiskResponse, error) {
	if err := validateSafeVMKey(vmID); err != nil {
		return nil, err
	}
	vm, err := m.GetVM(ctx, vmID)
	if err != nil {
		return nil, err
	}
	if vm == nil {
		return nil, fmt.Errorf("vm %s not found", vmID)
	}
	path, err := m.ArtifactPath(vm, kind)
	if err != nil {
		return nil, err
	}
	return ExportFileChunk(path, offset, length)
}

// ExportFileChunk reads up to length bytes at offset from path, compresses with gzip, and base64 encodes them.
func ExportFileChunk(path string, offset int64, length int) (*api.WorkerVMExportDiskResponse, error) {
	if offset < 0 {
		offset = 0
	}
	if length <= 0 {
		length = defaultMigrationChunkSize
	}
	if length > 16*1024*1024 {
		length = 16 * 1024 * 1024
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	totalSize := fi.Size()
	if offset >= totalSize {
		return &api.WorkerVMExportDiskResponse{
			Offset:    offset,
			RawLength: 0,
			TotalSize: totalSize,
			Data:      "",
			EOF:       true,
			Gzipped:   true,
		}, nil
	}

	toRead := int64(length)
	if offset+toRead > totalSize {
		toRead = totalSize - offset
	}
	buf := make([]byte, toRead)
	n, err := f.ReadAt(buf, offset)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("read %s at %d: %w", path, offset, err)
	}
	buf = buf[:n]

	var gzBuf bytes.Buffer
	gw := gzip.NewWriter(&gzBuf)
	if _, err := gw.Write(buf); err != nil {
		_ = gw.Close()
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}

	return &api.WorkerVMExportDiskResponse{
		Offset:    offset,
		RawLength: n,
		TotalSize: totalSize,
		Data:      base64.StdEncoding.EncodeToString(gzBuf.Bytes()),
		EOF:       offset+int64(n) >= totalSize,
		Gzipped:   true,
	}, nil
}

// ImportFileChunk decodes (and optionally gunzips) a chunk and writes non-zero bytes at offset, preserving sparseness.
func ImportFileChunk(path string, offset, totalSize int64, dataB64 string, gzipped bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	if offset == 0 && totalSize >= 0 {
		if err := f.Truncate(totalSize); err != nil {
			return fmt.Errorf("truncate %s to %d: %w", path, totalSize, err)
		}
	}

	if dataB64 == "" {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(dataB64)
	if err != nil {
		return fmt.Errorf("decode chunk base64: %w", err)
	}
	if gzipped {
		gr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return fmt.Errorf("gzip open: %w", err)
		}
		decompressed, err := io.ReadAll(gr)
		_ = gr.Close()
		if err != nil {
			return fmt.Errorf("gzip read: %w", err)
		}
		raw = decompressed
	}
	if len(raw) == 0 || isAllZero(raw) {
		return nil
	}
	if _, err := f.WriteAt(raw, offset); err != nil {
		return fmt.Errorf("write %s at %d: %w", path, offset, err)
	}
	return nil
}

func isAllZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

// ImportVMArtifactChunk writes an incoming VM disk/snapshot chunk to local storage and, when Finalize is set,
// provisions local network metadata and saves the VM record in this Manager's store.
func (m *Manager) ImportVMArtifactChunk(ctx context.Context, req api.WorkerVMImportDiskRequest) (*db.VM, error) {
	if err := validateSafeVMKey(req.VMID); err != nil {
		return nil, err
	}
	var incoming *db.VM
	if len(req.VM) > 0 {
		var parsed db.VM
		if err := json.Unmarshal(req.VM, &parsed); err != nil {
			return nil, fmt.Errorf("invalid vm metadata: %w", err)
		}
		if parsed.InternalID != "" {
			if err := validateSafeVMKey(parsed.InternalID); err != nil {
				return nil, err
			}
		}
		if parsed.ID != "" {
			if err := validateSafeVMKey(parsed.ID); err != nil {
				return nil, err
			}
		}
		incoming = &parsed
	}

	existing, _ := m.GetVM(ctx, req.VMID)
	vmKey := req.VMID
	if existing != nil && existing.InternalID != "" {
		vmKey = existing.InternalID
	} else if incoming != nil && incoming.InternalID != "" {
		vmKey = incoming.InternalID
	}
	if err := validateSafeVMKey(vmKey); err != nil {
		return nil, err
	}

	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	if kind == "" {
		kind = "disk"
	}
	targetPath, err := m.targetArtifactPath(vmKey, kind)
	if err != nil {
		return nil, err
	}

	if req.Offset == 0 && kind == "disk" && req.TotalSize > 0 {
		// Check local disk capacity before writing a new migrated disk.
		reqMB := req.TotalSize / (1024 * 1024)
		if reqMB <= 0 && incoming != nil && incoming.DiskMB > 0 {
			reqMB = int64(incoming.DiskMB)
		}
		if err := m.WithDiskLock(func() error {
			return m.CheckDiskCapacity(ctx, reqMB, 0)
		}); err != nil {
			return nil, err
		}
	}

	if req.Data != "" || (req.Offset == 0 && req.TotalSize >= 0) {
		if err := ImportFileChunk(targetPath, req.Offset, req.TotalSize, req.Data, req.Gzipped); err != nil {
			return nil, err
		}
	}

	if !req.Finalize {
		return existing, nil
	}

	vm := existing
	if vm == nil {
		if incoming == nil {
			return nil, fmt.Errorf("vm metadata is required when finalizing import")
		}
		cp := *incoming
		vm = &cp
	} else if incoming != nil {
		// Refresh fields from incoming snapshot while keeping local paths/network if desired.
		if incoming.Name != "" {
			vm.Name = incoming.Name
		}
		if incoming.OwnerID != "" {
			vm.OwnerID = incoming.OwnerID
		}
		if incoming.PoolID != "" {
			vm.PoolID = incoming.PoolID
		}
		if incoming.VCPUs > 0 {
			vm.VCPUs = incoming.VCPUs
		}
		if incoming.MemoryMB > 0 {
			vm.MemoryMB = incoming.MemoryMB
		}
		if incoming.DiskMB > 0 {
			vm.DiskMB = incoming.DiskMB
		}
		vm.Description = incoming.Description
		vm.Protected = incoming.Protected
		vm.SSHPublicKey = incoming.SSHPublicKey
		vm.SSHKeyName = incoming.SSHKeyName
		vm.SSHPasswordEnabled = incoming.SSHPasswordEnabled
		vm.PublicIPEnabled = incoming.PublicIPEnabled
		vm.NetworkIsolation = incoming.NetworkIsolation
		vm.Status = incoming.Status
	}

	if vm.InternalID == "" {
		vm.InternalID = vmKey
	}
	if vm.ID == "" {
		vm.ID = req.VMID
	}
	if vm.Status == "" || vm.Status == db.StatusRunning {
		vm.Status = db.StatusStopped
	}
	vm.DesiredRunning = false
	vm.PID = 0
	vm.SocketPath = ""
	if m.storMgr != nil {
		vm.DiskPath, _ = m.targetArtifactPath(vmKey, "disk")
		vm.KernelPath = m.storMgr.GetBaseKernelPath()
		vm.RootfsPath = m.storMgr.GetBaseRootfsPath()
		vm.SSHKeyPath = m.storMgr.GetSSHKeyPath()
		if vm.Status == db.StatusPaused {
			snapPath, memPath := m.storMgr.GetSnapshotPaths(vmKey)
			if _, err := os.Stat(snapPath); err == nil {
				vm.SnapshotPath = snapPath
			}
			if _, err := os.Stat(memPath); err == nil {
				vm.MemFilePath = memPath
			}
		} else {
			vm.SnapshotPath = ""
			vm.MemFilePath = ""
		}
	}

	// Allocate local network IP/MAC/TAP on this node's subnet.
	if err := m.assignLocalNetworkForImportedVM(ctx, vm); err != nil {
		return nil, err
	}

	vm.UpdatedAt = time.Now().UTC()
	if vm.CreatedAt.IsZero() {
		vm.CreatedAt = vm.UpdatedAt
	}
	if err := m.saveVMRecord(ctx, vm); err != nil {
		return nil, err
	}
	return vm, nil
}

func (m *Manager) assignLocalNetworkForImportedVM(ctx context.Context, vm *db.VM) error {
	if m.netMgr == nil || m.store == nil {
		return nil
	}
	usedIPs, err := m.store.GetAllocatedIPs(ctx)
	if err != nil {
		return err
	}
	if gw := m.netMgr.GetGatewayIP(); gw != "" {
		usedIPs[gw] = true
	}
	cfg := m.netMgr.GetConfig()
	if cfg.BridgeIP != "" {
		if bIP, _, err := net.ParseCIDR(cfg.BridgeIP); err == nil {
			usedIPs[bIP.String()] = true
		}
	}

	// Check if vm already has an IP valid for this node's subnet and unused by another VM.
	needNewIP := vm.IPAddress == "" || vm.IPAddress == "dhcp" || usedIPs[vm.IPAddress]
	if !needNewIP {
		if err := m.netMgr.ValidateIP(vm.IPAddress, usedIPs); err != nil {
			needNewIP = true
		}
	}
	if needNewIP {
		ip, mac, err := m.netMgr.AllocateIP(usedIPs)
		if err != nil {
			return fmt.Errorf("allocate local ip for imported vm: %w", err)
		}
		vm.IPAddress = ip
		vm.MACAddress = mac
	} else if vm.MACAddress == "" {
		vm.MACAddress = network.GenerateMAC(net.ParseIP(vm.IPAddress))
	}

	if vm.PublicIPEnabled {
		vm.PublicIP = vm.IPAddress
	} else {
		vm.PublicIP = ""
	}
	if vm.TAPDevice == "" {
		short := strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
		vm.TAPDevice = fmt.Sprintf("mftap-%s", short)
	}
	return nil
}
