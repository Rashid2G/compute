package guest

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func startMockVsockUDS(t *testing.T, udsPath string) func() {
	ln, err := net.Listen("unix", udsPath)
	if err != nil {
		t.Fatalf("failed to listen on mock vsock unix socket: %v", err)
	}

	stopCh := make(chan struct{})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-stopCh:
					return
				default:
					continue
				}
			}

			go func(c net.Conn) {
				defer c.Close()
				reader := bufio.NewReader(c)

				// 1. Read CONNECT 1024\n
				line, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if !strings.HasPrefix(line, "CONNECT") {
					return
				}

				// 2. Respond OK 1024\n
				_, _ = c.Write([]byte("OK 1024\n"))

				// 3. Process requests
				for {
					reqLine, err := reader.ReadBytes('\n')
					if err != nil {
						return
					}

					var req map[string]interface{}
					if err := json.Unmarshal(reqLine, &req); err != nil {
						return
					}

					action, _ := req["action"].(string)
					var res map[string]interface{}
					switch action {
					case "ping":
						res = map[string]interface{}{"status": "ok", "stdout": "pong"}
					case "exec":
						res = map[string]interface{}{
							"status":      "ok",
							"stdout":      "executed mock output",
							"exit_code":   0,
							"duration_ms": 12,
						}
					case "shutdown":
						res = map[string]interface{}{"status": "ok"}
					default:
						res = map[string]interface{}{"status": "error", "error": "unknown action"}
					}

					data, _ := json.Marshal(res)
					data = append(data, '\n')
					_, _ = c.Write(data)
				}
			}(conn)
		}
	}()

	return func() {
		close(stopCh)
		_ = ln.Close()
	}
}

func TestGuestClient_Lifecycle(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/tmp", "vsock-*")
	if err != nil { t.Fatalf("temp dir: %v", err) }
	defer os.RemoveAll(tmpDir)
	udsPath := filepath.Join(tmpDir, "v.sock")

	cleanup := startMockVsockUDS(t, udsPath)
	defer cleanup()

	client := NewClient(udsPath, 1024)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// 1. Ping
	if err := client.Ping(ctx); err != nil {
		t.Fatalf("Ping failed: %v", err)
	}

	// 2. WaitReady
	if err := client.WaitReady(ctx, 10*time.Millisecond); err != nil {
		t.Fatalf("WaitReady failed: %v", err)
	}

	// 3. Exec
	res, err := client.Exec(ctx, ExecRequest{
		Cmd: []string{"echo", "hello"},
	})
	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}
	if res.ExitCode != 0 || res.Stdout != "executed mock output" {
		t.Errorf("unexpected exec result: %+v", res)
	}

	// 4. Shutdown
	if err := client.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}
}
