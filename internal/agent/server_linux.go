//go:build linux

package agent

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func openPTY(rows, cols uint16) (*os.File, *os.File, error) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}

	// Unlock slave
	var unlock int = 0
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, unlock); err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("ioctl TIOCSPTLCK: %w", err)
	}

	// Get slave pty number
	ptn, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("ioctl TIOCGPTN: %w", err)
	}

	slavePath := fmt.Sprintf("/dev/pts/%d", ptn)
	slave, err := os.OpenFile(slavePath, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("open %s: %w", slavePath, err)
	}

	if rows > 0 && cols > 0 {
		ws := &unix.Winsize{Row: rows, Col: cols}
		_ = unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, ws)
	}

	return master, slave, nil
}

func setPTYSize(fd int, rows, cols uint16) error {
	ws := &unix.Winsize{Row: rows, Col: cols}
	return unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, ws)
}

func setClock(unixNano int64) error {
	sec := unixNano / 1e9
	nsec := unixNano % 1e9
	ts := &unix.Timespec{
		Sec:  sec,
		Nsec: nsec,
	}
	return unix.ClockSettime(unix.CLOCK_REALTIME, ts)
}

func mountDev(dev, target, fstype string, readOnly bool) error {
	if fstype == "" {
		fstype = "ext4"
	}
	// Check if already formatted
	if !isDeviceFormatted(dev) {
		out, err := exec.Command("mkfs.ext4", "-F", dev).CombinedOutput()
		if err != nil {
			return fmt.Errorf("mkfs.ext4 %s failed: %w (out: %s)", dev, err, string(out))
		}
	}
	var flags uintptr = 0
	if readOnly {
		flags |= unix.MS_RDONLY
	}
	return unix.Mount(dev, target, fstype, flags, "")
}

func unmountDev(target string) error {
	return unix.Unmount(target, 0)
}

func isDeviceFormatted(dev string) bool {
	f, err := os.Open(dev)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 2048)
	n, err := f.Read(buf)
	if err != nil || n < 1082 {
		return false
	}
	// ext superblock magic 0xEF53 at offset 1080 (little-endian)
	return buf[1080] == 0x53 && buf[1081] == 0xEF
}

func syncAndReboot() error {
	unix.Sync()
	return unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
}

func readPlatformStats() (*GuestStats, error) {
	stats := &GuestStats{}

	// 1. Meminfo
	if f, err := os.Open("/proc/meminfo"); err == nil {
		scanner := bufio.NewScanner(f)
		var totalKB, availKB, freeKB int64
		for scanner.Scan() {
			line := scanner.Text()
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				key := strings.TrimSuffix(fields[0], ":")
				val, _ := strconv.ParseInt(fields[1], 10, 64)
				switch key {
				case "MemTotal":
					totalKB = val
				case "MemAvailable":
					availKB = val
				case "MemFree":
					freeKB = val
				}
			}
		}
		f.Close()
		stats.MemTotalMB = totalKB / 1024
		if availKB > 0 {
			stats.MemUsedMB = (totalKB - availKB) / 1024
		} else {
			stats.MemUsedMB = (totalKB - freeKB) / 1024
		}
	}

	// 2. Uptime
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) > 0 {
			if upSec, err := strconv.ParseFloat(fields[0], 64); err == nil {
				stats.UptimeSec = int64(upSec)
			}
		}
	}

	// 3. Loadavg
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 3 {
			stats.LoadAvg1, _ = strconv.ParseFloat(fields[0], 64)
			stats.LoadAvg5, _ = strconv.ParseFloat(fields[1], 64)
			stats.LoadAvg15, _ = strconv.ParseFloat(fields[2], 64)
		}
	}

	// 4. Disk stats from /
	var stat unix.Statfs_t
	if err := unix.Statfs("/", &stat); err == nil {
		totalBytes := stat.Blocks * uint64(stat.Bsize)
		freeBytes := stat.Bfree * uint64(stat.Bsize)
		stats.DiskTotalMB = int64(totalBytes / (1024 * 1024))
		stats.DiskUsedMB = int64((totalBytes - freeBytes) / (1024 * 1024))
	}

	// 5. CPU usage estimate from /proc/stat
	if data, err := os.ReadFile("/proc/stat"); err == nil {
		lines := strings.Split(string(data), "\n")
		for _, l := range lines {
			if strings.HasPrefix(l, "cpu ") {
				fields := strings.Fields(l)
				if len(fields) >= 5 {
					user, _ := strconv.ParseFloat(fields[1], 64)
					nice, _ := strconv.ParseFloat(fields[2], 64)
					system, _ := strconv.ParseFloat(fields[3], 64)
					idle, _ := strconv.ParseFloat(fields[4], 64)
					total := user + nice + system + idle
					if total > 0 {
						stats.CPUPercent = ((total - idle) / total) * 100.0
					}
				}
				break
			}
		}
	}

	return stats, nil
}
