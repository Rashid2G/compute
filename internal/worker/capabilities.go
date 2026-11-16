package worker

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

var (
	cpuSampleMu    sync.Mutex
	prevCPUIdle    uint64
	prevCPUTotal   uint64
	hasPrevCPUSamp bool
)

func DetectCapabilities() map[string]interface{} {
	var st syscall.Statfs_t
	diskTotal := uint64(0)
	diskAvail := uint64(0)
	if syscall.Statfs("/", &st) == nil {
		bsize := uint64(st.Bsize)
		diskTotal = st.Blocks * bsize / (1024 * 1024)
		diskAvail = st.Bavail * bsize / (1024 * 1024)
	}
	memTotal := uint64(0)
	memAvail := uint64(0)
	var memFree uint64
	if f, e := os.Open("/proc/meminfo"); e == nil {
		s := bufio.NewScanner(f)
		for s.Scan() {
			p := strings.Fields(s.Text())
			if len(p) < 2 {
				continue
			}
			switch p[0] {
			case "MemTotal:":
				n, _ := strconv.ParseUint(p[1], 10, 64)
				memTotal = n / 1024
			case "MemAvailable:":
				n, _ := strconv.ParseUint(p[1], 10, 64)
				memAvail = n / 1024
			case "MemFree:":
				n, _ := strconv.ParseUint(p[1], 10, 64)
				memFree = n / 1024
			}
		}
		f.Close()
	}
	if memAvail == 0 && memFree > 0 {
		memAvail = memFree
	}
	_, kvmErr := os.Stat("/dev/kvm")
	return map[string]interface{}{
		"architecture":        runtime.GOARCH,
		"vcpus":               runtime.NumCPU(),
		"cpus":                runtime.NumCPU(),
		"memory_mb":           memTotal,
		"memory_available_mb": memAvail,
		"disk_mb":             diskTotal,
		"disk_available_mb":   diskAvail,
		"virtualization":      kvmErr == nil,
		"cpu_usage_percent":   sampleCPUUsagePercent(),
	}
}

func sampleCPUUsagePercent() float64 {
	if b, err := os.ReadFile("/proc/stat"); err == nil {
		lines := strings.Split(string(b), "\n")
		if len(lines) > 0 {
			fields := strings.Fields(lines[0])
			if len(fields) >= 5 && fields[0] == "cpu" {
				var total, idle uint64
				for i, f := range fields[1:] {
					v, _ := strconv.ParseUint(f, 10, 64)
					total += v
					if i == 3 || i == 4 { // idle + iowait
						idle += v
					}
				}
				cpuSampleMu.Lock()
				hadPrev := hasPrevCPUSamp
				pIdle, pTotal := prevCPUIdle, prevCPUTotal
				prevCPUIdle, prevCPUTotal = idle, total
				hasPrevCPUSamp = true
				cpuSampleMu.Unlock()
				if hadPrev && total > pTotal {
					dTotal := float64(total - pTotal)
					dIdle := float64(idle - pIdle)
					usage := ((dTotal - dIdle) / dTotal) * 100.0
					if usage < 0 {
						usage = 0
					}
					if usage > 100 {
						usage = 100
					}
					return usage
				}
			}
		}
	}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(b))
		if len(fields) >= 1 {
			if load1, err := strconv.ParseFloat(fields[0], 64); err == nil {
				cpus := float64(runtime.NumCPU())
				if cpus < 1 {
					cpus = 1
				}
				usage := (load1 / cpus) * 100.0
				if usage < 0 {
					usage = 0
				}
				if usage > 100 {
					usage = 100
				}
				return usage
			}
		}
	}
	return 0
}
