package vm

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// CgroupLimits defines the Cgroups v2 resource constraints for a microVM.
type CgroupLimits struct {
	VCPUs                int
	CPUWeight            int // Relative CPU share weight (1..10000, default 100)
	MemoryMB             int
	MemorySafetyBufferMB int    // Overhead buffer in MB (default 64MB)
	MemoryHighBufferMB   int    // Throttle watermark buffer in MB (e.g. 32MB)
	PidsMax              int    // Process ceiling (default 512)
	IOMax                string // e.g. "8:0 rbps=20971520 wbps=20971520"
}

// CgroupStats contains resource accounting stats from cgroups v2.
type CgroupStats struct {
	CPUTimeUsec   int64
	MemoryCurrent int64
	PidsCurrent   int
}

// CgroupManager manages per-VM Cgroups v2 lifecycle.
type CgroupManager struct {
	basePath string
	baseDir  string
}

// NewCgroupManager initializes a CgroupManager rooted at basePath.
// Defaults to "/sys/fs/cgroup/compute" if empty.
func NewCgroupManager(basePath string) *CgroupManager {
	if basePath == "" {
		basePath = "/sys/fs/cgroup/compute"
	}
	basePath = filepath.Clean(basePath)
	return &CgroupManager{
		basePath: basePath,
		baseDir:  basePath,
	}
}

// BasePath returns the root directory for compute cgroups.
func (cm *CgroupManager) BasePath() string {
	return cm.basePath
}

// VMPath returns the cgroup directory path for a given VM ID, rejecting path traversal.
func (cm *CgroupManager) VMPath(vmID string) (string, error) {
	if vmID == "" {
		return "", errors.New("vmID cannot be empty")
	}

	// Validate RAW vmID before any path manipulation or cleaning
	if strings.ContainsAny(vmID, "/\\") || strings.Contains(vmID, "..") {
		return "", fmt.Errorf("invalid vmID with path traversal: %s", vmID)
	}

	cleanID := filepath.Clean(vmID)
	if cleanID == "." || cleanID == ".." {
		return "", fmt.Errorf("invalid vmID with path traversal: %s", vmID)
	}

	baseDir := cm.baseDir
	if baseDir == "" {
		baseDir = cm.basePath
	}
	targetDir := filepath.Clean(filepath.Join(baseDir, cleanID))

	rel, err := filepath.Rel(baseDir, targetDir)
	if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
		return "", fmt.Errorf("invalid vmID escaping baseDir: %s", vmID)
	}

	// Ensure target path is not a symlink
	if fi, err := os.Lstat(targetDir); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("cgroup path %s is a symlink: potential path traversal", targetDir)
		}
	}

	return targetDir, nil
}

// IsCgroupV2Supported returns true if the host is running unified Cgroups v2.
func IsCgroupV2Supported() bool {
	_, err := os.Stat("/sys/fs/cgroup/cgroup.controllers")
	return err == nil
}

// FormatCPUMax computes the cpu.max value: "<quota> <period>"
// Period defaults to 100000 us (100ms). Quota is vCPUs * 100000.
func FormatCPUMax(vcpus int) string {
	if vcpus <= 0 {
		return "max 100000"
	}
	quota := vcpus * 100000
	return fmt.Sprintf("%d 100000", quota)
}

// FormatCPUWeight normalizes a cgroups v2 cpu.weight value (1..10000, default 100).
func FormatCPUWeight(weight int) string {
	if weight <= 0 {
		weight = 100
	}
	if weight < 1 {
		weight = 1
	}
	if weight > 10000 {
		weight = 10000
	}
	return fmt.Sprintf("%d", weight)
}

// FormatMemoryMax computes memory.max in bytes: (memoryMB + safetyBufferMB) * 1024 * 1024.
func FormatMemoryMax(memoryMB, safetyBufferMB int) string {
	if memoryMB <= 0 {
		return "max"
	}
	if safetyBufferMB < 0 {
		safetyBufferMB = 64
	}
	totalMB := memoryMB + safetyBufferMB
	bytes := int64(totalMB) * 1024 * 1024
	return fmt.Sprintf("%d", bytes)
}

// FormatMemoryHigh computes memory.high in bytes.
func FormatMemoryHigh(memoryMB, highBufferMB int) string {
	if memoryMB <= 0 {
		return "max"
	}
	if highBufferMB < 0 {
		highBufferMB = 32
	}
	totalMB := memoryMB + highBufferMB
	bytes := int64(totalMB) * 1024 * 1024
	return fmt.Sprintf("%d", bytes)
}

// FormatPidsMax computes pids.max limit string.
func FormatPidsMax(pids int) string {
	if pids <= 0 {
		pids = 512
	}
	return fmt.Sprintf("%d", pids)
}

// requiredControllers returns the cgroup v2 controllers needed to expose the
// limit files written by Setup.
func requiredControllers(limits CgroupLimits) []string {
	controllers := []string{"cpu", "memory", "pids"}
	if strings.TrimSpace(limits.IOMax) != "" {
		controllers = append(controllers, "io")
	}
	return controllers
}

// ensureControllers enables the controllers required by limits in the base
// cgroup so its VM child cgroups expose cpu.max, memory.max and friends.
//
// Cgroup v2 does not delegate controllers to children automatically: a parent
// must list them in cgroup.subtree_control first. Without this, writing to
// <vm>/cpu.max fails with EACCES even though the directory was created.
//
// Base paths that are not cgroup v2 hierarchies (for example test fixtures or
// cgroup v1 hosts) are left untouched.
func (cm *CgroupManager) ensureControllers(limits CgroupLimits) error {
	baseDir := cm.baseDir
	if baseDir == "" {
		baseDir = cm.basePath
	}

	data, err := os.ReadFile(filepath.Join(baseDir, "cgroup.controllers"))
	if err != nil {
		return nil
	}

	available := make(map[string]bool)
	for _, controller := range strings.Fields(string(data)) {
		available[controller] = true
	}

	var enable []string
	for _, controller := range requiredControllers(limits) {
		if available[controller] {
			enable = append(enable, "+"+controller)
		}
	}
	if len(enable) == 0 {
		return nil
	}

	subtreeControl := filepath.Join(baseDir, "cgroup.subtree_control")
	if err := os.WriteFile(subtreeControl, []byte(strings.Join(enable, " ")+"\n"), 0644); err != nil {
		return fmt.Errorf("failed to enable cgroup controllers (%s) in %s: %w", strings.Join(enable, " "), baseDir, err)
	}
	return nil
}

// Setup creates the VM's cgroup directory and applies CPU, Memory, PIDs, and IO limits.
// Returns the cgroup path.
func (cm *CgroupManager) Setup(vmID string, limits CgroupLimits) (string, error) {
	cgPath, err := cm.VMPath(vmID)
	if err != nil {
		return "", fmt.Errorf("invalid cgroup path: %w", err)
	}

	// Check if target is a symlink before creation
	if fi, err := os.Lstat(cgPath); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("cgroup path %s is a symlink", cgPath)
		}
	}

	// Ensure the base cgroup exists and delegates the controllers required by
	// this VM, otherwise its limit files do not exist.
	baseDir := cm.baseDir
	if baseDir == "" {
		baseDir = cm.basePath
	}
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create cgroup base directory %s: %w", baseDir, err)
	}
	if err := cm.ensureControllers(limits); err != nil {
		return "", err
	}

	if err := os.MkdirAll(cgPath, 0755); err != nil {
		return "", fmt.Errorf("failed to create cgroup directory %s: %w", cgPath, err)
	}

	// Verify newly created directory is not a symlink
	if fi, err := os.Lstat(cgPath); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("cgroup path %s is a symlink", cgPath)
		}
	}

	if limits.MemorySafetyBufferMB <= 0 {
		limits.MemorySafetyBufferMB = 64
	}
	if limits.PidsMax <= 0 {
		limits.PidsMax = 512
	}

	writeLimit := func(filename, val string) error {
		p := filepath.Join(cgPath, filename)
		return os.WriteFile(p, []byte(val+"\n"), 0644)
	}

	isV2 := IsCgroupV2Supported()

	// 1. cpu.max
	if err := writeLimit("cpu.max", FormatCPUMax(limits.VCPUs)); err != nil {
		if isV2 || !os.IsNotExist(err) {
			return "", fmt.Errorf("failed to write cpu.max: %w", err)
		}
	}

	// 1b. cpu.weight (soft overcommit CPU share weight)
	if limits.CPUWeight > 0 {
		if err := writeLimit("cpu.weight", FormatCPUWeight(limits.CPUWeight)); err != nil {
			if isV2 || !os.IsNotExist(err) {
				return "", fmt.Errorf("failed to write cpu.weight: %w", err)
			}
		}
	}

	// 2. memory.max
	if err := writeLimit("memory.max", FormatMemoryMax(limits.MemoryMB, limits.MemorySafetyBufferMB)); err != nil {
		if isV2 || !os.IsNotExist(err) {
			return "", fmt.Errorf("failed to write memory.max: %w", err)
		}
	}

	// 3. memory.high (optional throttle threshold)
	if limits.MemoryHighBufferMB > 0 {
		if err := writeLimit("memory.high", FormatMemoryHigh(limits.MemoryMB, limits.MemoryHighBufferMB)); err != nil {
			if isV2 || !os.IsNotExist(err) {
				return "", fmt.Errorf("failed to write memory.high: %w", err)
			}
		}
	}

	// 4. pids.max
	if err := writeLimit("pids.max", FormatPidsMax(limits.PidsMax)); err != nil {
		if isV2 || !os.IsNotExist(err) {
			return "", fmt.Errorf("failed to write pids.max: %w", err)
		}
	}

	// 5. io.max (if configured)
	if limits.IOMax != "" {
		if err := writeLimit("io.max", limits.IOMax); err != nil {
			if isV2 || !os.IsNotExist(err) {
				return "", fmt.Errorf("failed to write io.max: %w", err)
			}
		}
	}

	return cgPath, nil
}

// Assign places a process (by PID) into the VM's cgroup.
func (cm *CgroupManager) Assign(vmID string, pid int) error {
	if pid <= 0 {
		return fmt.Errorf("invalid pid: %d", pid)
	}
	cgPath, err := cm.VMPath(vmID)
	if err != nil {
		return fmt.Errorf("invalid cgroup path: %w", err)
	}
	procsFile := filepath.Join(cgPath, "cgroup.procs")
	if err := os.WriteFile(procsFile, []byte(fmt.Sprintf("%d\n", pid)), 0644); err != nil {
		return fmt.Errorf("failed to write cgroup.procs: %w", err)
	}
	return nil
}

// Teardown kills any remaining processes in the cgroup and removes the cgroup directory.
func (cm *CgroupManager) Teardown(vmID string) error {
	cgPath, err := cm.VMPath(vmID)
	if err != nil {
		return fmt.Errorf("invalid cgroup path: %w", err)
	}
	if _, err := os.Stat(cgPath); os.IsNotExist(err) {
		return nil
	}

	// Kill all processes in this cgroup if cgroup.kill is available
	killFile := filepath.Join(cgPath, "cgroup.kill")
	if _, err := os.Stat(killFile); err == nil {
		if kErr := os.WriteFile(killFile, []byte("1\n"), 0644); kErr != nil && !os.IsNotExist(kErr) {
			log.Printf("[cgroup] Warning: failed to write cgroup.kill for %s: %v", vmID, kErr)
		}
		time.Sleep(20 * time.Millisecond)
	}

	var removeErr error
	for i := 0; i < 5; i++ {
		removeErr = os.RemoveAll(cgPath)
		if removeErr == nil || os.IsNotExist(removeErr) {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	log.Printf("[cgroup] Error: failed to remove cgroup directory %s for vm %s: %v", cgPath, vmID, removeErr)
	return fmt.Errorf("failed to remove cgroup directory %s: %w", cgPath, removeErr)
}

// GetStats reads accounting metrics from the cgroup.
func (cm *CgroupManager) GetStats(vmID string) (*CgroupStats, error) {
	cgPath, err := cm.VMPath(vmID)
	if err != nil {
		return nil, fmt.Errorf("invalid cgroup path: %w", err)
	}
	stats := &CgroupStats{}

	// Memory current
	if data, err := os.ReadFile(filepath.Join(cgPath, "memory.current")); err == nil {
		if val, parseErr := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64); parseErr == nil {
			stats.MemoryCurrent = val
		}
	}

	// Pids current
	if data, err := os.ReadFile(filepath.Join(cgPath, "pids.current")); err == nil {
		if val, parseErr := strconv.Atoi(strings.TrimSpace(string(data))); parseErr == nil {
			stats.PidsCurrent = val
		}
	}

	// CPU usage from cpu.stat
	if data, err := os.ReadFile(filepath.Join(cgPath, "cpu.stat")); err == nil {
		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "usage_usec ") {
				parts := strings.Fields(line)
				if len(parts) == 2 {
					if val, parseErr := strconv.ParseInt(parts[1], 10, 64); parseErr == nil {
						stats.CPUTimeUsec = val
					}
				}
			}
		}
	}

	return stats, nil
}
