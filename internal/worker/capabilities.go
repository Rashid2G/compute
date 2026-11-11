package worker

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// tailscaleCache memoizes the local tailnet detection so heartbeats (every
// few seconds) do not shell out to `tailscale status` each time.
var tailscaleCache struct {
	mu      sync.Mutex
	value   map[string]interface{}
	checked time.Time
	ttl     time.Duration
}

func init() {
	tailscaleCache.ttl = 5 * time.Minute
}

func detectTailscaleCaps() map[string]interface{} {
	tailscaleCache.mu.Lock()
	defer tailscaleCache.mu.Unlock()

	if tailscaleCache.value != nil && time.Since(tailscaleCache.checked) < tailscaleCache.ttl {
		return tailscaleCache.value
	}

	caps := map[string]interface{}{"tailscale": false, "tailscale_ip": "", "tailscale_dns": ""}
	if out, err := exec.Command("tailscale", "status", "--json").Output(); err == nil {
		var parsed struct {
			BackendState string `json:"BackendState"`
			Self         struct {
				DNSName      string   `json:"DNSName"`
				TailscaleIPs []string `json:"TailscaleIPs"`
			} `json:"Self"`
		}
		if jsonErr := json.Unmarshal(out, &parsed); jsonErr == nil {
			for _, ip := range parsed.Self.TailscaleIPs {
				if strings.HasPrefix(ip, "100.") {
					caps["tailscale"] = true
					caps["tailscale_ip"] = ip
					caps["tailscale_dns"] = strings.TrimSuffix(parsed.Self.DNSName, ".")
					break
				}
			}
		}
	}
	tailscaleCache.value = caps
	tailscaleCache.checked = time.Now()
	return caps
}

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
	caps := map[string]interface{}{"architecture": runtime.GOARCH, "vcpus": runtime.NumCPU(), "cpus": runtime.NumCPU(), "memory_mb": mem, "disk_mb": disk, "virtualization": kvmErr == nil}
	for k, v := range detectTailscaleCaps() {
		caps[k] = v
	}
	return caps
}
