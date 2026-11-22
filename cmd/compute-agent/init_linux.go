//go:build linux

package main

import (
	"os"
	"os/exec"
	"time"

	"golang.org/x/sys/unix"
)

func setupGuestEnvironment() {
	// Mount virtual filesystems
	mounts := []struct {
		target string
		fstype string
		flags  uintptr
		data   string
	}{
		{"/proc", "proc", 0, ""},
		{"/sys", "sysfs", 0, ""},
		{"/dev", "devtmpfs", 0, ""},
		{"/dev/pts", "devpts", 0, "gid=5,mode=620"},
		{"/run", "tmpfs", 0, "mode=755"},
		{"/tmp", "tmpfs", 0, "mode=1777"},
	}

	for _, m := range mounts {
		_ = os.MkdirAll(m.target, 0755)
		_ = unix.Mount(m.fstype, m.target, m.fstype, m.flags, m.data)
	}

	// Set hostname
	_ = unix.Sethostname([]byte("compute-guest"))

	// Loopback up
	_ = exec.Command("ip", "link", "set", "lo", "up").Run()

	// Default resolv.conf if missing
	if _, err := os.Stat("/etc/resolv.conf"); os.IsNotExist(err) {
		_ = os.WriteFile("/etc/resolv.conf", []byte("nameserver 1.1.1.1\nnameserver 8.8.8.8\n"), 0644)
	}
}

func reapZombies() {
	for {
		var status unix.WaitStatus
		pid, err := unix.Wait4(-1, &status, unix.WNOHANG, nil)
		if err != nil || pid <= 0 {
			time.Sleep(200 * time.Millisecond)
			continue
		}
	}
}
