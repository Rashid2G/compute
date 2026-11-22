package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Server coordinates dispatching requests and handling client vsock connections.
type Server struct {
	// Optional hook for graceful poweroff
	OnShutdown func()
}

// NewServer creates a new agent Server.
func NewServer() *Server {
	return &Server{}
}

// HandleConn manages the lifecycle of a single incoming connection.
func (s *Server) HandleConn(conn net.Conn) {
	defer conn.Close()

	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return
	}

	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		s.writeResponse(conn, Response{Status: StatusError, Error: "malformed JSON: " + err.Error()})
		return
	}

	if req.Action == ActionExecStream {
		s.handleExecStream(conn, reader, req)
		return
	}

	res := s.Dispatch(req)
	s.writeResponse(conn, res)

	if req.Action == ActionShutdown {
		if s.OnShutdown != nil {
			s.OnShutdown()
		} else {
			go func() {
				time.Sleep(100 * time.Millisecond)
				_ = syncAndReboot()
			}()
		}
	}
}

func (s *Server) writeResponse(w io.Writer, res Response) {
	data, _ := json.Marshal(res)
	data = append(data, '\n')
	_, _ = w.Write(data)
}

// Dispatch executes a non-streaming action and returns the Response.
func (s *Server) Dispatch(req Request) Response {
	switch req.Action {
	case ActionPing:
		return Response{Status: StatusOK, Stdout: "pong", Version: "1.0"}
	case ActionReady:
		return Response{Status: StatusOK, Version: "1.0"}
	case ActionExec:
		return s.handleExec(req)
	case ActionFileRead:
		return s.handleFileRead(req)
	case ActionFileWrite:
		return s.handleFileWrite(req)
	case ActionFileList:
		return s.handleFileList(req)
	case ActionFileMkdir:
		return s.handleFileMkdir(req)
	case ActionFileRemove:
		return s.handleFileRemove(req)
	case ActionFileStat:
		return s.handleFileStat(req)
	case ActionProcList:
		return s.handleProcList(req)
	case ActionProcStart:
		return s.handleProcStart(req)
	case ActionProcKill:
		return s.handleProcKill(req)
	case ActionVolumeMount:
		return s.handleVolumeMount(req)
	case ActionVolumeUnmount:
		return s.handleVolumeUnmount(req)
	case ActionStats:
		return s.handleStats(req)
	case ActionClockSync:
		return s.handleClockSync(req)
	case ActionShutdown:
		return Response{Status: StatusOK, Stdout: "sync complete, shutting down"}
	default:
		return Response{Status: StatusError, Error: fmt.Sprintf("unknown action: %s", req.Action)}
	}
}

func (s *Server) handleExec(req Request) Response {
	cmdArgs := req.Cmd
	if len(cmdArgs) == 0 && req.Command != "" {
		cmdArgs = []string{"sh", "-c", req.Command}
	}
	if len(cmdArgs) == 0 {
		return Response{Status: StatusError, Error: "empty command"}
	}

	ctx := context.Background()
	var cancel context.CancelFunc
	if req.TimeoutSec > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(req.TimeoutSec)*time.Second)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...)
	if req.WorkDir != "" {
		cmd.Dir = req.WorkDir
	}
	if len(req.Env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range req.Env {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
		}
	}

	if req.Stdin != "" {
		cmd.Stdin = strings.NewReader(req.Stdin)
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	start := time.Now()
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
		Status:     StatusOK,
		Stdout:     stdoutBuf.String(),
		Stderr:     stderrBuf.String(),
		ExitCode:   exitCode,
		DurationMS: duration,
	}
}

func (s *Server) handleExecStream(conn net.Conn, reader *bufio.Reader, req Request) {
	cmdArgs := req.Cmd
	if len(cmdArgs) == 0 && req.Command != "" {
		cmdArgs = []string{"sh", "-c", req.Command}
	}
	if len(cmdArgs) == 0 {
		frame, _ := json.Marshal(StreamFrame{Type: FrameError, Error: "empty command"})
		_, _ = conn.Write(append(frame, '\n'))
		return
	}

	ctx := context.Background()
	var cancel context.CancelFunc
	if req.TimeoutSec > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(req.TimeoutSec)*time.Second)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...)
	if req.WorkDir != "" {
		cmd.Dir = req.WorkDir
	}
	if len(req.Env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range req.Env {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
		}
	}

	var writeMu sync.Mutex
	sendFrame := func(f StreamFrame) {
		writeMu.Lock()
		defer writeMu.Unlock()
		data, err := json.Marshal(f)
		if err == nil {
			_, _ = conn.Write(append(data, '\n'))
		}
	}

	if req.PTY {
		master, slave, err := openPTY(req.Rows, req.Cols)
		if err == nil {
			cmd.Stdin = slave
			cmd.Stdout = slave
			cmd.Stderr = slave
			cmd.SysProcAttr = &syscall.SysProcAttr{
				Setsid:  true,
				Setctty: true,
			}

			if startErr := cmd.Start(); startErr != nil {
				master.Close()
				slave.Close()
				sendFrame(StreamFrame{Type: FrameError, Error: startErr.Error()})
				sendFrame(StreamFrame{Type: FrameExit, ExitCode: 1})
				return
			}
			_ = slave.Close() // slave closed in parent

			// Read PTY output -> stream frames
			go func() {
				buf := make([]byte, 8192)
				for {
					n, rErr := master.Read(buf)
					if n > 0 {
						sendFrame(StreamFrame{Type: FrameStdout, Data: string(buf[:n])})
					}
					if rErr != nil {
						break
					}
				}
			}()

			// Read client frames -> PTY control
			go func() {
				for {
					line, rErr := reader.ReadBytes('\n')
					if len(line) > 0 {
						var inFrame StreamFrame
						if jErr := json.Unmarshal(line, &inFrame); jErr == nil {
							switch inFrame.Type {
							case FrameStdin:
								_, _ = master.Write([]byte(inFrame.Data))
							case FrameResize:
								_ = setPTYSize(int(master.Fd()), inFrame.Rows, inFrame.Cols)
							case FrameSignal:
								if cmd.Process != nil {
									sig := parseSignal(inFrame.Signal)
									_ = cmd.Process.Signal(sig)
								}
							}
						}
					}
					if rErr != nil {
						break
					}
				}
			}()

			waitErr := cmd.Wait()
			master.Close()

			exitCode := 0
			if waitErr != nil {
				if exitErr, ok := waitErr.(*exec.ExitError); ok {
					exitCode = exitErr.ExitCode()
				} else {
					exitCode = 1
				}
			}
			sendFrame(StreamFrame{Type: FrameExit, ExitCode: exitCode})
			return
		}
		// Fallback to pipes if PTY creation fails
	}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		sendFrame(StreamFrame{Type: FrameError, Error: err.Error()})
		sendFrame(StreamFrame{Type: FrameExit, ExitCode: 1})
		return
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		sendFrame(StreamFrame{Type: FrameError, Error: err.Error()})
		sendFrame(StreamFrame{Type: FrameExit, ExitCode: 1})
		return
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		sendFrame(StreamFrame{Type: FrameError, Error: err.Error()})
		sendFrame(StreamFrame{Type: FrameExit, ExitCode: 1})
		return
	}

	if err := cmd.Start(); err != nil {
		sendFrame(StreamFrame{Type: FrameError, Error: err.Error()})
		sendFrame(StreamFrame{Type: FrameExit, ExitCode: 1})
		return
	}

	var streamWG sync.WaitGroup
	streamWG.Add(2)

	go func() {
		defer streamWG.Done()
		buf := make([]byte, 8192)
		for {
			n, rErr := stdoutPipe.Read(buf)
			if n > 0 {
				sendFrame(StreamFrame{Type: FrameStdout, Data: string(buf[:n])})
			}
			if rErr != nil {
				break
			}
		}
	}()

	go func() {
		defer streamWG.Done()
		buf := make([]byte, 8192)
		for {
			n, rErr := stderrPipe.Read(buf)
			if n > 0 {
				sendFrame(StreamFrame{Type: FrameStderr, Data: string(buf[:n])})
			}
			if rErr != nil {
				break
			}
		}
	}()

	// Incoming frames from client
	go func() {
		for {
			line, rErr := reader.ReadBytes('\n')
			if len(line) > 0 {
				var inFrame StreamFrame
				if jErr := json.Unmarshal(line, &inFrame); jErr == nil {
					switch inFrame.Type {
					case FrameStdin:
						_, _ = stdinPipe.Write([]byte(inFrame.Data))
					case FrameSignal:
						if cmd.Process != nil {
							sig := parseSignal(inFrame.Signal)
							_ = cmd.Process.Signal(sig)
						}
					}
				}
			}
			if rErr != nil {
				_ = stdinPipe.Close()
				break
			}
		}
	}()

	streamWG.Wait()
	waitErr := cmd.Wait()

	exitCode := 0
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}
	sendFrame(StreamFrame{Type: FrameExit, ExitCode: exitCode})
}

func parseSignal(sig string) syscall.Signal {
	switch strings.ToUpper(strings.TrimPrefix(sig, "SIG")) {
	case "INT":
		return syscall.SIGINT
	case "KILL":
		return syscall.SIGKILL
	case "HUP":
		return syscall.SIGHUP
	case "QUIT":
		return syscall.SIGQUIT
	case "USR1":
		return syscall.SIGUSR1
	case "USR2":
		return syscall.SIGUSR2
	default:
		return syscall.SIGTERM
	}
}

func (s *Server) handleFileRead(req Request) Response {
	if req.Path == "" {
		return Response{Status: StatusError, Error: "missing file path"}
	}
	clean := filepath.Clean(req.Path)
	info, err := os.Stat(clean)
	if err != nil {
		return Response{Status: StatusError, Error: err.Error()}
	}
	if info.IsDir() {
		return Response{Status: StatusError, Error: "path is a directory"}
	}
	// Limit read size to 50 MiB to prevent memory exhaustion
	if info.Size() > 50*1024*1024 {
		return Response{Status: StatusError, Error: "file size exceeds 50MB limit"}
	}

	data, err := os.ReadFile(clean)
	if err != nil {
		return Response{Status: StatusError, Error: err.Error()}
	}

	content := string(data)
	format := "text"
	if strings.ToLower(req.Format) == "base64" {
		content = base64.StdEncoding.EncodeToString(data)
		format = "base64"
	}

	return Response{
		Status:  StatusOK,
		Content: content,
		Format:  format,
		Size:    info.Size(),
	}
}

func (s *Server) handleFileWrite(req Request) Response {
	if req.Path == "" {
		return Response{Status: StatusError, Error: "missing file path"}
	}
	clean := filepath.Clean(req.Path)
	var data []byte
	var err error

	if strings.ToLower(req.Format) == "base64" {
		data, err = base64.StdEncoding.DecodeString(req.Content)
		if err != nil {
			return Response{Status: StatusError, Error: "invalid base64 content: " + err.Error()}
		}
	} else {
		data = []byte(req.Content)
	}

	if err := os.MkdirAll(filepath.Dir(clean), 0755); err != nil {
		return Response{Status: StatusError, Error: err.Error()}
	}

	mode := os.FileMode(0644)
	if req.Mode > 0 {
		mode = os.FileMode(req.Mode)
	}

	if err := os.WriteFile(clean, data, mode); err != nil {
		return Response{Status: StatusError, Error: err.Error()}
	}

	return Response{
		Status: StatusOK,
		Size:   int64(len(data)),
	}
}

func (s *Server) handleFileList(req Request) Response {
	dir := req.Path
	if dir == "" {
		dir = "."
	}
	clean := filepath.Clean(dir)

	entries, err := os.ReadDir(clean)
	if err != nil {
		return Response{Status: StatusError, Error: err.Error()}
	}

	var results []FileInfo
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		results = append(results, FileInfo{
			Name:    e.Name(),
			Path:    filepath.Join(clean, e.Name()),
			Size:    info.Size(),
			Mode:    uint32(info.Mode()),
			IsDir:   e.IsDir(),
			ModTime: info.ModTime(),
		})
	}

	return Response{
		Status:  StatusOK,
		Entries: results,
	}
}

func (s *Server) handleFileMkdir(req Request) Response {
	if req.Path == "" {
		return Response{Status: StatusError, Error: "missing directory path"}
	}
	mode := os.FileMode(0755)
	if req.Mode > 0 {
		mode = os.FileMode(req.Mode)
	}
	if err := os.MkdirAll(filepath.Clean(req.Path), mode); err != nil {
		return Response{Status: StatusError, Error: err.Error()}
	}
	return Response{Status: StatusOK}
}

func (s *Server) handleFileRemove(req Request) Response {
	if req.Path == "" {
		return Response{Status: StatusError, Error: "missing path to remove"}
	}
	clean := filepath.Clean(req.Path)
	var err error
	if req.Recursive {
		err = os.RemoveAll(clean)
	} else {
		err = os.Remove(clean)
	}
	if err != nil {
		return Response{Status: StatusError, Error: err.Error()}
	}
	return Response{Status: StatusOK}
}

func (s *Server) handleFileStat(req Request) Response {
	if req.Path == "" {
		return Response{Status: StatusError, Error: "missing file path"}
	}
	clean := filepath.Clean(req.Path)
	info, err := os.Stat(clean)
	if err != nil {
		return Response{Status: StatusError, Error: err.Error()}
	}
	return Response{
		Status: StatusOK,
		FileInfo: &FileInfo{
			Name:    info.Name(),
			Path:    clean,
			Size:    info.Size(),
			Mode:    uint32(info.Mode()),
			IsDir:   info.IsDir(),
			ModTime: info.ModTime(),
		},
	}
}

func (s *Server) handleProcList(req Request) Response {
	// Query processes via ps
	out, err := exec.Command("ps", "-eo", "pid,ppid,user,%cpu,%mem,stat,comm,args", "--no-headers").Output()
	if err != nil {
		return Response{Status: StatusError, Error: "failed to list processes: " + err.Error()}
	}

	var procs []Process
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		fields := strings.Fields(l)
		if len(fields) < 7 {
			continue
		}
		pid, _ := strconv.Atoi(fields[0])
		ppid, _ := strconv.Atoi(fields[1])
		cpu, _ := strconv.ParseFloat(fields[3], 64)
		mem, _ := strconv.ParseFloat(fields[4], 64)
		args := strings.Join(fields[7:], " ")
		if args == "" {
			args = fields[6]
		}
		procs = append(procs, Process{
			PID:     pid,
			PPID:    ppid,
			User:    fields[2],
			CPU:     cpu,
			Memory:  mem,
			Status:  fields[5],
			Command: fields[6],
			Args:    args,
		})
	}

	return Response{
		Status:    StatusOK,
		Processes: procs,
	}
}

func (s *Server) handleProcStart(req Request) Response {
	cmdArgs := req.Cmd
	if len(cmdArgs) == 0 && req.Command != "" {
		cmdArgs = []string{"sh", "-c", req.Command}
	}
	if len(cmdArgs) == 0 {
		return Response{Status: StatusError, Error: "empty command"}
	}

	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	if req.WorkDir != "" {
		cmd.Dir = req.WorkDir
	}
	if len(req.Env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range req.Env {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
		}
	}

	// Detach process into its own session
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	logFile, err := os.OpenFile("/tmp/bg-proc.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err == nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}

	if err := cmd.Start(); err != nil {
		if logFile != nil {
			_ = logFile.Close()
		}
		return Response{Status: StatusError, Error: "failed to start process: " + err.Error()}
	}

	pid := cmd.Process.Pid
	go func() {
		_ = cmd.Wait()
		if logFile != nil {
			_ = logFile.Close()
		}
	}()

	return Response{
		Status: StatusOK,
		PID:    pid,
	}
}

func (s *Server) handleProcKill(req Request) Response {
	if req.PID <= 0 {
		return Response{Status: StatusError, Error: "invalid pid"}
	}
	sig := parseSignal(req.Signal)
	proc, err := os.FindProcess(req.PID)
	if err != nil {
		return Response{Status: StatusError, Error: err.Error()}
	}
	if err := proc.Signal(sig); err != nil {
		return Response{Status: StatusError, Error: err.Error()}
	}
	return Response{Status: StatusOK, PID: req.PID}
}

func (s *Server) handleVolumeMount(req Request) Response {
	if req.Device == "" || req.MountPoint == "" {
		return Response{Status: StatusError, Error: "missing device or mount_point"}
	}
	if err := os.MkdirAll(req.MountPoint, 0755); err != nil {
		return Response{Status: StatusError, Error: "failed to create mountpoint: " + err.Error()}
	}
	if err := mountDev(req.Device, req.MountPoint, req.FSType, req.ReadOnly); err != nil {
		return Response{Status: StatusError, Error: "mount failed: " + err.Error()}
	}
	return Response{Status: StatusOK}
}

func (s *Server) handleVolumeUnmount(req Request) Response {
	if req.MountPoint == "" {
		return Response{Status: StatusError, Error: "missing mount_point"}
	}
	if err := unmountDev(req.MountPoint); err != nil {
		return Response{Status: StatusError, Error: "unmount failed: " + err.Error()}
	}
	return Response{Status: StatusOK}
}

func (s *Server) handleStats(req Request) Response {
	stats, err := readPlatformStats()
	if err != nil {
		return Response{Status: StatusError, Error: err.Error()}
	}
	return Response{
		Status: StatusOK,
		Stats:  stats,
	}
}

func (s *Server) handleClockSync(req Request) Response {
	nano := req.UnixNano
	if nano <= 0 {
		nano = time.Now().UnixNano()
	}
	if err := setClock(nano); err != nil {
		return Response{Status: StatusError, Error: "clock sync failed: " + err.Error()}
	}
	return Response{
		Status:          StatusOK,
		CurrentUnixNano: time.Now().UnixNano(),
	}
}
