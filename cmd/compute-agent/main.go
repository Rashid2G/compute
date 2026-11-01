package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const VsockPort = 1024

type Request struct {
	Action string            `json:"action"` // ping, ready, exec, stats, shutdown
	Cmd    []string          `json:"cmd,omitempty"`
	Env    map[string]string `json:"env,omitempty"`
	Stdin  string            `json:"stdin,omitempty"`
}

type Response struct {
	Status     string  `json:"status"` // "ok", "error"
	Error      string  `json:"error,omitempty"`
	Stdout     string  `json:"stdout,omitempty"`
	Stderr     string  `json:"stderr,omitempty"`
	ExitCode   int     `json:"exit_code"`
	DurationMS int64   `json:"duration_ms,omitempty"`
	CPUPercent float64 `json:"cpu_percent,omitempty"`
	MemUsedMB  int64   `json:"mem_used_mb,omitempty"`
	MemTotalMB int64   `json:"mem_total_mb,omitempty"`
	UptimeSec  int64   `json:"uptime_sec,omitempty"`
}

func main() {
	isPID1 := os.Getpid() == 1 || filepath.Base(os.Args[0]) == "compute-init"

	if isPID1 {
		setupGuestEnvironment()
		go reapZombies()
	}

	// Start vsock listener on port 1024
	ln, err := listenVsock(VsockPort)
	if err != nil {
		fmt.Fprintf(os.Stderr, "compute-agent: failed to listen on vsock port %d: %v\n", VsockPort, err)
		// If vsock fails (e.g. running outside microVM), fallback to TCP 1024 for testing
		ln, err = net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", VsockPort))
		if err != nil {
			fmt.Fprintf(os.Stderr, "compute-agent: failed to listen on fallback tcp %d: %v\n", VsockPort, err)
			if isPID1 {
				select {} // Keep PID 1 alive
			}
			os.Exit(1)
		}
	}
	defer ln.Close()

	fmt.Printf("compute-agent: listening on port %d (PID 1: %v)\n", VsockPort, isPID1)

	for {
		conn, err := ln.Accept()
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go handleConnection(conn)
	}
}

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

func listenVsock(port uint32) (net.Listener, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, fmt.Errorf("socket(AF_VSOCK) failed: %w", err)
	}

	sa := &unix.SockaddrVM{
		CID:  unix.VMADDR_CID_ANY,
		Port: port,
	}

	if err := unix.Bind(fd, sa); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("bind vsock failed: %w", err)
	}

	if err := unix.Listen(fd, 32); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("listen vsock failed: %w", err)
	}

	file := os.NewFile(uintptr(fd), "vsock-listener")
	return net.FileListener(file)
}

func handleConnection(conn net.Conn) {
	defer conn.Close()

	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}

		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			sendResponse(conn, Response{Status: "error", Error: "malformed JSON"})
			continue
		}

		res := dispatch(req)
		sendResponse(conn, res)

		if req.Action == "shutdown" {
			time.Sleep(200 * time.Millisecond)
			unix.Sync()
			_ = unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
			return
		}
	}
}

func dispatch(req Request) Response {
	switch req.Action {
	case "ping":
		return Response{Status: "ok", Stdout: "pong"}
	case "ready":
		return Response{Status: "ok"}
	case "stats":
		return getStats()
	case "exec":
		return executeCommand(req)
	case "shutdown":
		return Response{Status: "ok", Stdout: "shutting down"}
	default:
		return Response{Status: "error", Error: fmt.Sprintf("unknown action: %s", req.Action)}
	}
}

func executeCommand(req Request) Response {
	if len(req.Cmd) == 0 {
		return Response{Status: "error", Error: "empty command"}
	}

	start := time.Now()
	cmd := exec.Command(req.Cmd[0], req.Cmd[1:]...)

	if len(req.Env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range req.Env {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
		}
	}

	if req.Stdin != "" {
		cmd.Stdin = strings.NewReader(req.Stdin)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	duration := time.Since(start).Milliseconds()

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}

	return Response{
		Status:     "ok",
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		ExitCode:   exitCode,
		DurationMS: duration,
	}
}

func getStats() Response {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	var sysinfo unix.Sysinfo_t
	_ = unix.Sysinfo(&sysinfo)

	memTotal := int64(sysinfo.Totalram * uint64(sysinfo.Unit) / (1024 * 1024))
	memFree := int64(sysinfo.Freeram * uint64(sysinfo.Unit) / (1024 * 1024))
	memUsed := memTotal - memFree

	return Response{
		Status:     "ok",
		MemTotalMB: memTotal,
		MemUsedMB:  memUsed,
		UptimeSec:  int64(sysinfo.Uptime),
	}
}

func sendResponse(w io.Writer, res Response) {
	data, _ := json.Marshal(res)
	data = append(data, '\n')
	_, _ = w.Write(data)
}
