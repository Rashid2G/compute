package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/config"
	"github.com/Rashid2G/on-demand-vms/internal/db"
)

var (
	wholeBlockDeviceRe   = regexp.MustCompile(`^(nvme\d+n\d+|sd[a-z]+|vd[a-z]+|xvd[a-z]+|mmcblk\d+)$`)
	logicalBlockDeviceRe = regexp.MustCompile(`^(dm-\d+|md\d+)$`)
)

type resourceSampleState struct {
	cpuCapturedAt time.Time
	cpuUser       uint64
	cpuNice       uint64
	cpuSystem     uint64
	cpuIdle       uint64
	cpuIOWait     uint64
	cpuIRQ        uint64
	cpuSoftIRQ    uint64
	cpuSteal      uint64

	diskCapturedAt time.Time
	diskReadBytes  uint64
	diskWriteBytes uint64
	diskReadOps    uint64
	diskWriteOps   uint64

	netCapturedAt time.Time
	netRxBytes    uint64
	netTxBytes    uint64
}

type adminCPUMetrics struct {
	Cores         int     `json:"cores"`
	Model         string  `json:"model,omitempty"`
	UsagePercent  float64 `json:"usage_percent"`
	UserPercent   float64 `json:"user_percent"`
	SystemPercent float64 `json:"system_percent"`
	IOWaitPercent float64 `json:"iowait_percent"`
	IdlePercent   float64 `json:"idle_percent"`
	Load1m        float64 `json:"load_1m"`
	Load5m        float64 `json:"load_5m"`
	Load15m       float64 `json:"load_15m"`
}

type adminMemoryMetrics struct {
	TotalMB     int64   `json:"total_mb"`
	UsedMB      int64   `json:"used_mb"`
	AvailableMB int64   `json:"available_mb"`
	FreeMB      int64   `json:"free_mb"`
	CachedMB    int64   `json:"cached_mb"`
	BuffersMB   int64   `json:"buffers_mb"`
	UsedPercent float64 `json:"used_percent"`
	SwapTotalMB int64   `json:"swap_total_mb"`
	SwapUsedMB  int64   `json:"swap_used_mb"`
}

type adminDiskMount struct {
	Label       string  `json:"label"`
	Path        string  `json:"path"`
	TotalGB     float64 `json:"total_gb"`
	UsedGB      float64 `json:"used_gb"`
	AvailableGB float64 `json:"available_gb"`
	UsedPercent float64 `json:"used_percent"`
}

type adminDiskSpaceMetrics struct {
	Path        string           `json:"path"`
	TotalGB     float64          `json:"total_gb"`
	UsedGB      float64          `json:"used_gb"`
	FreeGB      float64          `json:"free_gb"`
	AvailableGB float64          `json:"available_gb"`
	UsedPercent float64          `json:"used_percent"`
	Mounts      []adminDiskMount `json:"mounts,omitempty"`
}

type adminDiskDeviceIO struct {
	Device       string  `json:"device"`
	ReadOps      uint64  `json:"read_ops"`
	WriteOps     uint64  `json:"write_ops"`
	ReadBytes    uint64  `json:"read_bytes"`
	WriteBytes   uint64  `json:"write_bytes"`
	ReadMB       float64 `json:"read_mb"`
	WriteMB      float64 `json:"write_mb"`
	IOInProgress uint64  `json:"io_in_progress"`
}

type adminDiskIOMetrics struct {
	ReadOps       uint64              `json:"read_ops"`
	WriteOps      uint64              `json:"write_ops"`
	ReadBytes     uint64              `json:"read_bytes"`
	WriteBytes    uint64              `json:"write_bytes"`
	ReadMB        float64             `json:"read_mb"`
	WriteMB       float64             `json:"write_mb"`
	ReadMBPerSec  float64             `json:"read_mb_per_sec"`
	WriteMBPerSec float64             `json:"write_mb_per_sec"`
	ReadIOPS      float64             `json:"read_iops"`
	WriteIOPS     float64             `json:"write_iops"`
	IOInProgress  uint64              `json:"io_in_progress"`
	Devices       []adminDiskDeviceIO `json:"devices,omitempty"`
}

type adminNetworkMetrics struct {
	RxBytes    uint64   `json:"rx_bytes"`
	TxBytes    uint64   `json:"tx_bytes"`
	RxPackets  uint64   `json:"rx_packets"`
	TxPackets  uint64   `json:"tx_packets"`
	RxMB       float64  `json:"rx_mb"`
	TxMB       float64  `json:"tx_mb"`
	RxMBps     float64  `json:"rx_mb_per_sec"`
	TxMBps     float64  `json:"tx_mb_per_sec"`
	Interfaces []string `json:"interfaces,omitempty"`
}

type adminSystemMetrics struct {
	Hostname      string  `json:"hostname"`
	OS            string  `json:"os"`
	Arch          string  `json:"arch"`
	KernelVersion string  `json:"kernel_version,omitempty"`
	UptimeSeconds float64 `json:"uptime_seconds"`
	KVMAvailable  bool    `json:"kvm_available"`
}

type adminSandboxAllocations struct {
	Total             int     `json:"total"`
	Running           int     `json:"running"`
	Stopped           int     `json:"stopped"`
	VCPUsAllocated    int     `json:"vcpus_allocated"`
	RunningVCPUs      int     `json:"running_vcpus"`
	MemoryMBAllocated int     `json:"memory_mb_allocated"`
	RunningMemoryMB   int     `json:"running_memory_mb"`
	DiskMBAllocated   int     `json:"disk_mb_allocated"`
	DiskGBAllocated   float64 `json:"disk_gb_allocated"`
}

type adminJobAllocations struct {
	Total             int     `json:"total"`
	Queued            int     `json:"queued"`
	Running           int     `json:"running"`
	Succeeded         int     `json:"succeeded"`
	Failed            int     `json:"failed"`
	VCPUsAllocated    int     `json:"vcpus_allocated"`
	MemoryMBAllocated int     `json:"memory_mb_allocated"`
	DiskMBAllocated   int     `json:"disk_mb_allocated"`
	DiskGBAllocated   float64 `json:"disk_gb_allocated"`
}

type adminVolumeAllocations struct {
	Total           int     `json:"total"`
	Attached        int     `json:"attached"`
	Available       int     `json:"available"`
	DiskMBAllocated int     `json:"disk_mb_allocated"`
	DiskGBAllocated float64 `json:"disk_gb_allocated"`
}

type adminWorkerAllocations struct {
	Total   int `json:"total"`
	Ready   int `json:"ready"`
	Offline int `json:"offline"`
}

type adminPlatformAllocations struct {
	Sandboxes             adminSandboxAllocations `json:"sandboxes"`
	CIJobs                adminJobAllocations     `json:"ci_jobs"`
	Volumes               adminVolumeAllocations  `json:"volumes"`
	Workers               adminWorkerAllocations  `json:"workers"`
	Limits                config.ResourceLimits   `json:"limits"`
	AllocatedVCPUs        int                     `json:"allocated_vcpus"`
	RunningVCPUs          int                     `json:"running_vcpus"`
	JobVCPUs              int                     `json:"job_vcpus"`
	MaxTotalVCPUs         int                     `json:"max_total_vcpus"`
	AllocatedMemoryMB     int                     `json:"allocated_memory_mb"`
	RunningMemoryMB       int                     `json:"running_memory_mb"`
	JobMemoryMB           int                     `json:"job_memory_mb"`
	MaxTotalMemoryMB      int                     `json:"max_total_memory_mb"`
	AllocatedVMDiskGB     float64                 `json:"allocated_vm_disk_gb"`
	AllocatedVolumeDiskGB float64                 `json:"allocated_volume_disk_gb"`
	MaxTotalDiskGB        float64                 `json:"max_total_disk_gb"`
}

type adminResourcesResponse struct {
	Timestamp        time.Time                `json:"timestamp"`
	CPU              adminCPUMetrics          `json:"cpu"`
	Memory           adminMemoryMetrics       `json:"memory"`
	Disk             adminDiskSpaceMetrics    `json:"disk"`
	DiskIO           adminDiskIOMetrics       `json:"disk_io"`
	Network          adminNetworkMetrics      `json:"network"`
	System           adminSystemMetrics       `json:"system"`
	Allocations      adminPlatformAllocations `json:"allocations"`
	CapacityWarnings []string                 `json:"capacity_warnings"`
}

func round2(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*100) / 100
}

type rawCPUStat struct {
	user, nice, system, idle, iowait, irq, softirq, steal uint64
}

func (c rawCPUStat) total() uint64 {
	return c.user + c.nice + c.system + c.idle + c.iowait + c.irq + c.softirq + c.steal
}

func readProcCPUStat() (rawCPUStat, bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return rawCPUStat{}, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			return rawCPUStat{}, false
		}
		parse := func(idx int) uint64 {
			if idx >= len(fields) {
				return 0
			}
			v, _ := strconv.ParseUint(fields[idx], 10, 64)
			return v
		}
		return rawCPUStat{
			user:    parse(1),
			nice:    parse(2),
			system:  parse(3),
			idle:    parse(4),
			iowait:  parse(5),
			irq:     parse(6),
			softirq: parse(7),
			steal:   parse(8),
		}, true
	}
	return rawCPUStat{}, false
}

func readCPUModelAndLoad(cpu *adminCPUMetrics) {
	if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(strings.ToLower(line), "model name") {
				if idx := strings.Index(line, ":"); idx >= 0 {
					cpu.Model = strings.TrimSpace(line[idx+1:])
					break
				}
			}
		}
	}
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 3 {
			cpu.Load1m, _ = strconv.ParseFloat(fields[0], 64)
			cpu.Load5m, _ = strconv.ParseFloat(fields[1], 64)
			cpu.Load15m, _ = strconv.ParseFloat(fields[2], 64)
		}
	}
}

func readMemInfo() adminMemoryMetrics {
	var mem adminMemoryMetrics
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return mem
	}
	valsKB := make(map[string]int64)
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) == 0 {
			continue
		}
		if v, err := strconv.ParseInt(fields[0], 10, 64); err == nil {
			valsKB[strings.TrimSpace(parts[0])] = v
		}
	}
	totalKB := valsKB["MemTotal"]
	freeKB := valsKB["MemFree"]
	buffersKB := valsKB["Buffers"]
	cachedKB := valsKB["Cached"] + valsKB["SReclaimable"]
	availKB := valsKB["MemAvailable"]
	if availKB <= 0 {
		availKB = freeKB + buffersKB + cachedKB
	}
	usedKB := totalKB - availKB
	if usedKB < 0 {
		usedKB = 0
	}
	swapTotalKB := valsKB["SwapTotal"]
	swapFreeKB := valsKB["SwapFree"]
	swapUsedKB := swapTotalKB - swapFreeKB
	if swapUsedKB < 0 {
		swapUsedKB = 0
	}

	mem.TotalMB = totalKB / 1024
	mem.UsedMB = usedKB / 1024
	mem.AvailableMB = availKB / 1024
	mem.FreeMB = freeKB / 1024
	mem.CachedMB = cachedKB / 1024
	mem.BuffersMB = buffersKB / 1024
	mem.SwapTotalMB = swapTotalKB / 1024
	mem.SwapUsedMB = swapUsedKB / 1024
	if totalKB > 0 {
		mem.UsedPercent = round2(float64(usedKB) * 100.0 / float64(totalKB))
	}
	return mem
}

func statfsForPath(path string) (totalBytes, freeBytes, availBytes uint64, fsidKey string, resolved string, ok bool) {
	cur := strings.TrimSpace(path)
	if cur == "" {
		cur = "/"
	}
	for {
		if _, err := os.Stat(cur); err == nil {
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			cur = "/"
			break
		}
		cur = parent
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(cur, &st); err != nil {
		return 0, 0, 0, "", cur, false
	}
	bsize := uint64(st.Bsize)
	return st.Blocks * bsize, st.Bfree * bsize, st.Bavail * bsize, fmt.Sprintf("%+v", st.Fsid), cur, true
}

func (s *Server) readDiskSpace() adminDiskSpaceMetrics {
	type candidate struct {
		label string
		path  string
	}
	candidates := []candidate{
		{label: "root", path: "/"},
	}
	vmsDir := s.cfg.Images.VMsDir
	imagesDir := s.cfg.Images.ImagesDir
	if s.vmMgr != nil && s.vmMgr.GetStorageManager() != nil {
		scfg := s.vmMgr.GetStorageManager().GetConfig()
		if vmsDir == "" {
			vmsDir = scfg.VMsDir
		}
		if imagesDir == "" {
			imagesDir = scfg.BaseImagesDir
		}
	}
	if vmsDir != "" {
		candidates = append(candidates, candidate{label: "vms", path: vmsDir})
	}
	if imagesDir != "" {
		candidates = append(candidates, candidate{label: "images", path: imagesDir})
	}
	if s.cfg.Server.DBPath != "" {
		candidates = append(candidates, candidate{label: "database", path: filepath.Dir(s.cfg.Server.DBPath)})
	}

	seenFS := make(map[string]bool)
	var mounts []adminDiskMount
	var primary *adminDiskMount
	var primaryFree uint64

	for _, c := range candidates {
		total, free, avail, fsidKey, resolved, ok := statfsForPath(c.path)
		if !ok || total == 0 {
			continue
		}
		key := fmt.Sprintf("%s:%d", fsidKey, total)
		used := uint64(0)
		if total > free {
			used = total - free
		}
		const gb = 1024.0 * 1024.0 * 1024.0
		m := adminDiskMount{
			Label:       c.label,
			Path:        resolved,
			TotalGB:     round2(float64(total) / gb),
			UsedGB:      round2(float64(used) / gb),
			AvailableGB: round2(float64(avail) / gb),
			UsedPercent: round2(float64(used) * 100.0 / float64(total)),
		}
		if c.label == "vms" || primary == nil {
			copyM := m
			primary = &copyM
			primaryFree = free
		}
		if !seenFS[key] {
			seenFS[key] = true
			mounts = append(mounts, m)
		}
	}

	var out adminDiskSpaceMetrics
	if primary != nil {
		out = adminDiskSpaceMetrics{
			Path:        primary.Path,
			TotalGB:     primary.TotalGB,
			UsedGB:      primary.UsedGB,
			FreeGB:      round2(float64(primaryFree) / (1024.0 * 1024.0 * 1024.0)),
			AvailableGB: primary.AvailableGB,
			UsedPercent: primary.UsedPercent,
			Mounts:      mounts,
		}
	}
	return out
}

func parseDiskStatsByRegex(lines []string, re *regexp.Regexp) adminDiskIOMetrics {
	var out adminDiskIOMetrics
	const sectorSize = 512
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 12 {
			continue
		}
		dev := fields[2]
		if !re.MatchString(dev) {
			continue
		}
		readOps, _ := strconv.ParseUint(fields[3], 10, 64)
		sectorsRead, _ := strconv.ParseUint(fields[5], 10, 64)
		writeOps, _ := strconv.ParseUint(fields[7], 10, 64)
		sectorsWritten, _ := strconv.ParseUint(fields[9], 10, 64)
		inProg, _ := strconv.ParseUint(fields[11], 10, 64)

		readBytes := sectorsRead * sectorSize
		writeBytes := sectorsWritten * sectorSize

		out.ReadOps += readOps
		out.WriteOps += writeOps
		out.ReadBytes += readBytes
		out.WriteBytes += writeBytes
		out.IOInProgress += inProg

		out.Devices = append(out.Devices, adminDiskDeviceIO{
			Device:       dev,
			ReadOps:      readOps,
			WriteOps:     writeOps,
			ReadBytes:    readBytes,
			WriteBytes:   writeBytes,
			ReadMB:       round2(float64(readBytes) / (1024.0 * 1024.0)),
			WriteMB:      round2(float64(writeBytes) / (1024.0 * 1024.0)),
			IOInProgress: inProg,
		})
	}
	out.ReadMB = round2(float64(out.ReadBytes) / (1024.0 * 1024.0))
	out.WriteMB = round2(float64(out.WriteBytes) / (1024.0 * 1024.0))
	return out
}

func readProcDiskStats() (adminDiskIOMetrics, bool) {
	data, err := os.ReadFile("/proc/diskstats")
	if err != nil {
		return adminDiskIOMetrics{}, false
	}
	lines := strings.Split(string(data), "\n")
	out := parseDiskStatsByRegex(lines, wholeBlockDeviceRe)
	if len(out.Devices) == 0 {
		out = parseDiskStatsByRegex(lines, logicalBlockDeviceRe)
	}
	return out, true
}

func isVirtualOrGuestNetIface(iface string) bool {
	lower := strings.ToLower(strings.TrimSpace(iface))
	if lower == "" || lower == "lo" {
		return true
	}
	for _, prefix := range []string{"tap", "veth", "fcbr", "docker", "br-", "virbr"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func readProcNetDev() adminNetworkMetrics {
	var out adminNetworkMetrics
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return out
	}
	lines := strings.Split(string(data), "\n")
	accumulate := func(skipVirtual bool) adminNetworkMetrics {
		var m adminNetworkMetrics
		for _, line := range lines {
			colon := strings.Index(line, ":")
			if colon < 0 {
				continue
			}
			iface := strings.TrimSpace(line[:colon])
			if iface == "" || iface == "lo" {
				continue
			}
			if skipVirtual && isVirtualOrGuestNetIface(iface) {
				continue
			}
			fields := strings.Fields(line[colon+1:])
			if len(fields) < 10 {
				continue
			}
			rxBytes, _ := strconv.ParseUint(fields[0], 10, 64)
			rxPkts, _ := strconv.ParseUint(fields[1], 10, 64)
			txBytes, _ := strconv.ParseUint(fields[8], 10, 64)
			txPkts, _ := strconv.ParseUint(fields[9], 10, 64)
			m.RxBytes += rxBytes
			m.RxPackets += rxPkts
			m.TxBytes += txBytes
			m.TxPackets += txPkts
			m.Interfaces = append(m.Interfaces, iface)
		}
		m.RxMB = round2(float64(m.RxBytes) / (1024.0 * 1024.0))
		m.TxMB = round2(float64(m.TxBytes) / (1024.0 * 1024.0))
		return m
	}

	out = accumulate(true)
	if len(out.Interfaces) == 0 {
		out = accumulate(false)
	}
	return out
}

func readSystemMetrics() adminSystemMetrics {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "localhost"
	}
	sys := adminSystemMetrics{
		Hostname: hostname,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
	}
	if data, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		sys.KernelVersion = strings.TrimSpace(string(data))
	}
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) > 0 {
			sys.UptimeSeconds, _ = strconv.ParseFloat(fields[0], 64)
			sys.UptimeSeconds = round2(sys.UptimeSeconds)
		}
	}
	if _, err := os.Stat("/dev/kvm"); err == nil {
		sys.KVMAvailable = true
	}
	return sys
}

func (s *Server) collectHostMetrics() (adminCPUMetrics, adminMemoryMetrics, adminDiskSpaceMetrics, adminDiskIOMetrics, adminNetworkMetrics, adminSystemMetrics) {
	cpu := adminCPUMetrics{Cores: runtime.NumCPU()}
	readCPUModelAndLoad(&cpu)
	mem := readMemInfo()
	diskSpace := s.readDiskSpace()
	diskIO, _ := readProcDiskStats()
	netMetrics := readProcNetDev()
	sys := readSystemMetrics()

	now := time.Now()
	curCPU, cpuOK := readProcCPUStat()

	s.resourceSampleMu.Lock()
	needsBaseline := s.resourceSamples.cpuCapturedAt.IsZero()
	if needsBaseline {
		if cpuOK {
			s.resourceSamples.cpuCapturedAt = now
			s.resourceSamples.cpuUser = curCPU.user
			s.resourceSamples.cpuNice = curCPU.nice
			s.resourceSamples.cpuSystem = curCPU.system
			s.resourceSamples.cpuIdle = curCPU.idle
			s.resourceSamples.cpuIOWait = curCPU.iowait
			s.resourceSamples.cpuIRQ = curCPU.irq
			s.resourceSamples.cpuSoftIRQ = curCPU.softirq
			s.resourceSamples.cpuSteal = curCPU.steal
		}
		s.resourceSamples.diskCapturedAt = now
		s.resourceSamples.diskReadBytes = diskIO.ReadBytes
		s.resourceSamples.diskWriteBytes = diskIO.WriteBytes
		s.resourceSamples.diskReadOps = diskIO.ReadOps
		s.resourceSamples.diskWriteOps = diskIO.WriteOps
		s.resourceSamples.netCapturedAt = now
		s.resourceSamples.netRxBytes = netMetrics.RxBytes
		s.resourceSamples.netTxBytes = netMetrics.TxBytes
	}
	s.resourceSampleMu.Unlock()

	if needsBaseline {
		time.Sleep(20 * time.Millisecond)
		now = time.Now()
		curCPU, cpuOK = readProcCPUStat()
		diskIO, _ = readProcDiskStats()
		netMetrics = readProcNetDev()
	}

	s.resourceSampleMu.Lock()
	defer s.resourceSampleMu.Unlock()

	if cpuOK {
		if !s.resourceSamples.cpuCapturedAt.IsZero() {
			prev := rawCPUStat{
				user:    s.resourceSamples.cpuUser,
				nice:    s.resourceSamples.cpuNice,
				system:  s.resourceSamples.cpuSystem,
				idle:    s.resourceSamples.cpuIdle,
				iowait:  s.resourceSamples.cpuIOWait,
				irq:     s.resourceSamples.cpuIRQ,
				softirq: s.resourceSamples.cpuSoftIRQ,
				steal:   s.resourceSamples.cpuSteal,
			}
			totalDelta := curCPU.total() - prev.total()
			if totalDelta > 0 && curCPU.total() >= prev.total() {
				userDelta := (curCPU.user - prev.user) + (curCPU.nice - prev.nice)
				sysDelta := (curCPU.system - prev.system) + (curCPU.irq - prev.irq) + (curCPU.softirq - prev.softirq)
				iowaitDelta := curCPU.iowait - prev.iowait
				idleDelta := (curCPU.idle - prev.idle) + iowaitDelta
				cpu.UserPercent = round2(float64(userDelta) * 100.0 / float64(totalDelta))
				cpu.SystemPercent = round2(float64(sysDelta) * 100.0 / float64(totalDelta))
				cpu.IOWaitPercent = round2(float64(iowaitDelta) * 100.0 / float64(totalDelta))
				cpu.IdlePercent = round2(float64(curCPU.idle-prev.idle) * 100.0 / float64(totalDelta))
				activeDelta := totalDelta - idleDelta
				cpu.UsagePercent = round2(float64(activeDelta) * 100.0 / float64(totalDelta))
			}
		}
		s.resourceSamples.cpuCapturedAt = now
		s.resourceSamples.cpuUser = curCPU.user
		s.resourceSamples.cpuNice = curCPU.nice
		s.resourceSamples.cpuSystem = curCPU.system
		s.resourceSamples.cpuIdle = curCPU.idle
		s.resourceSamples.cpuIOWait = curCPU.iowait
		s.resourceSamples.cpuIRQ = curCPU.irq
		s.resourceSamples.cpuSoftIRQ = curCPU.softirq
		s.resourceSamples.cpuSteal = curCPU.steal
	}

	if !s.resourceSamples.diskCapturedAt.IsZero() {
		elapsed := now.Sub(s.resourceSamples.diskCapturedAt).Seconds()
		if elapsed > 0.01 {
			if diskIO.ReadBytes >= s.resourceSamples.diskReadBytes {
				diskIO.ReadMBPerSec = round2(float64(diskIO.ReadBytes-s.resourceSamples.diskReadBytes) / (1024.0 * 1024.0) / elapsed)
			}
			if diskIO.WriteBytes >= s.resourceSamples.diskWriteBytes {
				diskIO.WriteMBPerSec = round2(float64(diskIO.WriteBytes-s.resourceSamples.diskWriteBytes) / (1024.0 * 1024.0) / elapsed)
			}
			if diskIO.ReadOps >= s.resourceSamples.diskReadOps {
				diskIO.ReadIOPS = round2(float64(diskIO.ReadOps-s.resourceSamples.diskReadOps) / elapsed)
			}
			if diskIO.WriteOps >= s.resourceSamples.diskWriteOps {
				diskIO.WriteIOPS = round2(float64(diskIO.WriteOps-s.resourceSamples.diskWriteOps) / elapsed)
			}
		}
	}
	s.resourceSamples.diskCapturedAt = now
	s.resourceSamples.diskReadBytes = diskIO.ReadBytes
	s.resourceSamples.diskWriteBytes = diskIO.WriteBytes
	s.resourceSamples.diskReadOps = diskIO.ReadOps
	s.resourceSamples.diskWriteOps = diskIO.WriteOps

	if !s.resourceSamples.netCapturedAt.IsZero() {
		elapsed := now.Sub(s.resourceSamples.netCapturedAt).Seconds()
		if elapsed > 0.01 {
			if netMetrics.RxBytes >= s.resourceSamples.netRxBytes {
				netMetrics.RxMBps = round2(float64(netMetrics.RxBytes-s.resourceSamples.netRxBytes) / (1024.0 * 1024.0) / elapsed)
			}
			if netMetrics.TxBytes >= s.resourceSamples.netTxBytes {
				netMetrics.TxMBps = round2(float64(netMetrics.TxBytes-s.resourceSamples.netTxBytes) / (1024.0 * 1024.0) / elapsed)
			}
		}
	}
	s.resourceSamples.netCapturedAt = now
	s.resourceSamples.netRxBytes = netMetrics.RxBytes
	s.resourceSamples.netTxBytes = netMetrics.TxBytes

	return cpu, mem, diskSpace, diskIO, netMetrics, sys
}

func (s *Server) handleAdminResources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireAdminAccess(w, r) {
		return
	}
	ctx := r.Context()

	cpu, mem, diskSpace, diskIO, netMetrics, sys := s.collectHostMetrics()

	var sandboxAlloc adminSandboxAllocations
	var jobAlloc adminJobAllocations
	var volAlloc adminVolumeAllocations
	var workerAlloc adminWorkerAllocations
	var limits config.ResourceLimits

	countedJobVMs := make(map[string]bool)
	if s.vmMgr != nil {
		limits = s.vmMgr.GetResourceLimits()
		defaultDiskMB := s.vmMgr.GetDefaults().DiskSizeMB
		if defaultDiskMB <= 0 {
			defaultDiskMB = 20480
		}
		if vms, err := s.vmMgr.ListVMs(ctx); err == nil {
			for _, v := range vms {
				if v == nil {
					continue
				}
				diskMB := v.DiskMB
				if diskMB <= 0 {
					diskMB = defaultDiskMB
				}
				if isJobVM(v) {
					countedJobVMs[v.ID] = true
					jobAlloc.VCPUsAllocated += v.VCPUs
					jobAlloc.MemoryMBAllocated += v.MemoryMB
					jobAlloc.DiskMBAllocated += diskMB
					continue
				}
				sandboxAlloc.Total++
				sandboxAlloc.VCPUsAllocated += v.VCPUs
				sandboxAlloc.MemoryMBAllocated += v.MemoryMB
				sandboxAlloc.DiskMBAllocated += diskMB
				if v.Status == db.StatusRunning {
					sandboxAlloc.Running++
					sandboxAlloc.RunningVCPUs += v.VCPUs
					sandboxAlloc.RunningMemoryMB += v.MemoryMB
				} else {
					sandboxAlloc.Stopped++
				}
			}
		}
		if st := s.vmMgr.GetStore(); st != nil {
			if jobs, err := st.ListJobs(ctx, ""); err == nil {
				for _, j := range jobs {
					if j == nil {
						continue
					}
					jobAlloc.Total++
					stateLower := strings.ToLower(j.State)
					switch stateLower {
					case "queued", "assigned":
						jobAlloc.Queued++
					case "running":
						jobAlloc.Running++
					case "succeeded":
						jobAlloc.Succeeded++
					case "failed", "canceled", "lost":
						jobAlloc.Failed++
					}
					if (stateLower == "running" || stateLower == "assigned") && (j.VMID == "" || !countedJobVMs[j.VMID]) {
						var spec struct {
							VCPUs    int `json:"vcpus"`
							MemoryMB int `json:"memory_mb"`
							DiskMB   int `json:"disk_mb"`
						}
						_ = json.Unmarshal([]byte(j.SpecJSON), &spec)
						vcpus := spec.VCPUs
						if vcpus <= 0 {
							vcpus = 2
						}
						memMB := spec.MemoryMB
						if memMB <= 0 {
							memMB = 2048
						}
						diskMB := spec.DiskMB
						if diskMB <= 0 {
							diskMB = defaultDiskMB
						}
						jobAlloc.VCPUsAllocated += vcpus
						jobAlloc.MemoryMBAllocated += memMB
						jobAlloc.DiskMBAllocated += diskMB
					}
				}
			}
			if vols, err := st.ListVolumes(ctx); err == nil {
				for _, vol := range filterNonJobVolumes(vols) {
					if vol == nil || strings.HasPrefix(vol.ID, "vol_root_") {
						continue
					}
					volAlloc.Total++
					if strings.EqualFold(vol.Status, "attached") || vol.AttachedVM != "" {
						volAlloc.Attached++
					} else {
						volAlloc.Available++
					}
					volAlloc.DiskMBAllocated += vol.SizeGB * 1024
				}
			}
			if nodes, err := st.ListWorkerNodes(ctx); err == nil {
				for _, n := range nodes {
					if n == nil {
						continue
					}
					workerAlloc.Total++
					if strings.EqualFold(n.Status, "ready") {
						workerAlloc.Ready++
					} else {
						workerAlloc.Offline++
					}
				}
			}
		}
	}

	sandboxAlloc.DiskGBAllocated = round2(float64(sandboxAlloc.DiskMBAllocated) / 1024.0)
	jobAlloc.DiskGBAllocated = round2(float64(jobAlloc.DiskMBAllocated) / 1024.0)
	volAlloc.DiskGBAllocated = round2(float64(volAlloc.DiskMBAllocated) / 1024.0)

	maxVCPUs := limits.MaxTotalVCPUs
	if maxVCPUs <= 0 {
		maxVCPUs = cpu.Cores
	}
	maxMemMB := limits.MaxTotalMemoryMB
	if maxMemMB <= 0 {
		maxMemMB = int(mem.TotalMB)
	}
	maxDiskGB := round2(float64(limits.MaxTotalDiskMB) / 1024.0)
	if maxDiskGB <= 0 {
		maxDiskGB = diskSpace.TotalGB
	}

	totalAllocatedVCPUs := sandboxAlloc.VCPUsAllocated + jobAlloc.VCPUsAllocated
	totalRunningVCPUs := sandboxAlloc.RunningVCPUs + jobAlloc.VCPUsAllocated
	totalAllocatedMemMB := sandboxAlloc.MemoryMBAllocated + jobAlloc.MemoryMBAllocated
	totalRunningMemMB := sandboxAlloc.RunningMemoryMB + jobAlloc.MemoryMBAllocated
	totalVMDiskGB := round2(sandboxAlloc.DiskGBAllocated + jobAlloc.DiskGBAllocated)

	warnings := []string{}
	if cpu.UsagePercent >= 80 {
		warnings = append(warnings, fmt.Sprintf("High host CPU utilization: %.1f%%", cpu.UsagePercent))
	}
	if mem.UsedPercent >= 80 {
		warnings = append(warnings, fmt.Sprintf("High host memory utilization: %.1f%% (%d/%d MB)", mem.UsedPercent, mem.UsedMB, mem.TotalMB))
	}
	if diskSpace.UsedPercent >= 80 {
		warnings = append(warnings, fmt.Sprintf("High disk space utilization on %s: %.1f%% (%.1f/%.1f GB)", diskSpace.Path, diskSpace.UsedPercent, diskSpace.UsedGB, diskSpace.TotalGB))
	}
	if limits.MaxTotalVCPUs > 0 && totalRunningVCPUs*100 >= limits.MaxTotalVCPUs*80 {
		warnings = append(warnings, fmt.Sprintf("Allocated vCPUs nearing configured limit: %d/%d", totalRunningVCPUs, limits.MaxTotalVCPUs))
	}
	if limits.MaxTotalMemoryMB > 0 && totalRunningMemMB*100 >= limits.MaxTotalMemoryMB*80 {
		warnings = append(warnings, fmt.Sprintf("Allocated memory nearing configured limit: %d/%d MB", totalRunningMemMB, limits.MaxTotalMemoryMB))
	}
	totalDiskMB := int64(sandboxAlloc.DiskMBAllocated + jobAlloc.DiskMBAllocated + volAlloc.DiskMBAllocated)
	if limits.MaxTotalDiskMB > 0 && totalDiskMB*100 >= limits.MaxTotalDiskMB*80 {
		warnings = append(warnings, fmt.Sprintf("Allocated disk nearing configured limit: %d/%d MB", totalDiskMB, limits.MaxTotalDiskMB))
	}

	resp := adminResourcesResponse{
		Timestamp: time.Now().UTC(),
		CPU:       cpu,
		Memory:    mem,
		Disk:      diskSpace,
		DiskIO:    diskIO,
		Network:   netMetrics,
		System:    sys,
		Allocations: adminPlatformAllocations{
			Sandboxes:             sandboxAlloc,
			CIJobs:                jobAlloc,
			Volumes:               volAlloc,
			Workers:               workerAlloc,
			Limits:                limits,
			AllocatedVCPUs:        totalAllocatedVCPUs,
			RunningVCPUs:          totalRunningVCPUs,
			JobVCPUs:              jobAlloc.VCPUsAllocated,
			MaxTotalVCPUs:         maxVCPUs,
			AllocatedMemoryMB:     totalAllocatedMemMB,
			RunningMemoryMB:       totalRunningMemMB,
			JobMemoryMB:           jobAlloc.MemoryMBAllocated,
			MaxTotalMemoryMB:      maxMemMB,
			AllocatedVMDiskGB:     totalVMDiskGB,
			AllocatedVolumeDiskGB: volAlloc.DiskGBAllocated,
			MaxTotalDiskGB:        maxDiskGB,
		},
		CapacityWarnings: warnings,
	}

	json.NewEncoder(w).Encode(resp)
}
