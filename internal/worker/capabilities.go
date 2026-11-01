package worker

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

func DetectCapabilities() map[string]interface{} {
	var st syscall.Statfs_t
	disk := uint64(0)
	if syscall.Statfs("/", &st) == nil {
		disk = st.Bavail * uint64(st.Bsize) / (1024 * 1024)
	}
	mem := uint64(0)
	if f, e := os.Open("/proc/meminfo"); e == nil {
		s := bufio.NewScanner(f)
		for s.Scan() {
			p := strings.Fields(s.Text())
			if len(p) > 1 && p[0] == "MemAvailable:" {
				n, _ := strconv.ParseUint(p[1], 10, 64)
				mem = n / 1024
				break
			}
		}
		f.Close()
	}
	_, kvmErr := os.Stat("/dev/kvm")
	return map[string]interface{}{"architecture": runtime.GOARCH, "vcpus": runtime.NumCPU(), "cpus": runtime.NumCPU(), "memory_mb": mem, "disk_mb": disk, "virtualization": kvmErr == nil}
}
