package scheduler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rashid2G/compute/pkg/api"
	"github.com/coder/websocket"
)

func startRegistryTestServer(t *testing.T, reg *WorkerRegistry) (*httptest.Server, <-chan struct{}) {
	t.Helper()
	disconnected := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() {
			_ = conn.Close(websocket.StatusNormalClosure, "")
			once.Do(func() { close(disconnected) })
		}()
		worker := NewWorker("wrk-rpc-1", "default", nil, conn)
		_ = reg.ServeStream(r.Context(), worker, nil)
	}))
	t.Cleanup(srv.Close)
	return srv, disconnected
}

func TestWorkerRegistry_CallRPC_SuccessAndErrors(t *testing.T) {
	reg := NewWorkerRegistry()
	srv, _ := startRegistryTestServer(t, reg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Wait for worker registration
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := reg.Get("wrk-rpc-1"); ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := reg.Get("wrk-rpc-1"); !ok {
		t.Fatalf("worker did not register")
	}

	// Mock worker RPC responder
	go func() {
		for {
			_, raw, rErr := conn.Read(ctx)
			if rErr != nil {
				return
			}
			var req api.WorkerRPCRequest
			if json.Unmarshal(raw, &req) != nil || req.Type != "worker.rpc.req" {
				continue
			}
			resp := api.WorkerRPCResponse{
				WorkerMessage: api.WorkerMessage{
					Version:  1,
					Type:     "worker.rpc.resp",
					ID:       req.ID,
					WorkerID: "wrk-rpc-1",
				},
				RequestID: req.RequestID,
			}
			switch req.Method {
			case "echo.ok":
				resp.Result = req.Payload
			case "echo.limit":
				rle := api.ResourceLimitError{
					Code:      api.ErrCodeResourceLimitExceeded,
					Resource:  "memory",
					LimitType: "hard",
					NodeID:    "wrk-rpc-1",
					Requested: 2048,
					Available: 512,
					Limit:     1024,
					Message:   "not enough memory",
				}
				resp.Code = api.ErrCodeResourceLimitExceeded
				resp.Error = rle.Message
				resp.Result, _ = json.Marshal(rle)
			default:
				resp.Error = "unknown method"
			}
			b, _ := json.Marshal(resp)
			_ = conn.Write(ctx, websocket.MessageText, b)
		}
	}()

	var out map[string]string
	if err := reg.CallRPC(ctx, "wrk-rpc-1", "echo.ok", map[string]string{"hello": "world"}, &out); err != nil {
		t.Fatalf("CallRPC echo.ok failed: %v", err)
	}
	if out["hello"] != "world" {
		t.Fatalf("unexpected RPC response: %+v", out)
	}

	err = reg.CallRPC(ctx, "wrk-rpc-1", "echo.limit", nil, nil)
	var rle *api.ResourceLimitError
	if !errors.As(err, &rle) {
		t.Fatalf("expected *api.ResourceLimitError, got %T: %v", err, err)
	}
	if rle.Resource != "memory" || rle.NodeID != "wrk-rpc-1" {
		t.Fatalf("unexpected ResourceLimitError: %+v", rle)
	}

	if err := reg.CallRPC(ctx, "wrk-rpc-1", "other", nil, nil); err == nil || !strings.Contains(err.Error(), "unknown method") {
		t.Fatalf("expected unknown method error, got %v", err)
	}

	if err := reg.CallRPC(ctx, "wrk-missing", "echo.ok", nil, nil); err == nil {
		t.Fatalf("expected error for disconnected worker")
	}
}

func TestWorkerRegistry_OpenTunnel_EchoAndEOF(t *testing.T) {
	reg := NewWorkerRegistry()
	srv, _ := startRegistryTestServer(t, reg)

	// Start TCP echo server that closes cleanly on client EOF.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	wsConn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer wsConn.Close(websocket.StatusNormalClosure, "")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := reg.Get("wrk-rpc-1"); ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Minimal worker-side tunnel relay loop
	var writeMu sync.Mutex
	sendWS := func(v interface{}) {
		b, _ := json.Marshal(v)
		writeMu.Lock()
		_ = wsConn.Write(ctx, websocket.MessageText, b)
		writeMu.Unlock()
	}

	var tunMu sync.Mutex
	tunnels := make(map[string]net.Conn)
	go func() {
		for {
			_, raw, err := wsConn.Read(ctx)
			if err != nil {
				return
			}
			var head api.WorkerMessage
			if json.Unmarshal(raw, &head) != nil {
				continue
			}
			switch head.Type {
			case "worker.tunnel.open":
				var open api.WorkerTunnelOpen
				_ = json.Unmarshal(raw, &open)
				tc, err := net.Dial("tcp", open.TargetAddr)
				if err != nil {
					sendWS(api.WorkerTunnelClose{
						WorkerMessage: api.WorkerMessage{Version: 1, Type: "worker.tunnel.close", WorkerID: "wrk-rpc-1"},
						TunnelID:      open.TunnelID,
						Error:         err.Error(),
					})
					continue
				}
				tunMu.Lock()
				tunnels[open.TunnelID] = tc
				tunMu.Unlock()
				// Ack tunnel ready
				sendWS(api.WorkerTunnelData{
					WorkerMessage: api.WorkerMessage{Version: 1, Type: "worker.tunnel.data", WorkerID: "wrk-rpc-1"},
					TunnelID:      open.TunnelID,
				})
				go func(tid string, tc net.Conn) {
					buf := make([]byte, 8192)
					for {
						n, rErr := tc.Read(buf)
						if n > 0 {
							sendWS(api.WorkerTunnelData{
								WorkerMessage: api.WorkerMessage{Version: 1, Type: "worker.tunnel.data", WorkerID: "wrk-rpc-1"},
								TunnelID:      tid,
								Data:          base64.StdEncoding.EncodeToString(buf[:n]),
							})
						}
						if rErr != nil {
							sendWS(api.WorkerTunnelData{
								WorkerMessage: api.WorkerMessage{Version: 1, Type: "worker.tunnel.data", WorkerID: "wrk-rpc-1"},
								TunnelID:      tid,
								EOF:           true,
							})
							return
						}
					}
				}(open.TunnelID, tc)

			case "worker.tunnel.data":
				var frame api.WorkerTunnelData
				_ = json.Unmarshal(raw, &frame)
				tunMu.Lock()
				tc := tunnels[frame.TunnelID]
				tunMu.Unlock()
				if tc == nil {
					continue
				}
				if frame.Data != "" {
					data, _ := base64.StdEncoding.DecodeString(frame.Data)
					if len(data) > 0 {
						_, _ = tc.Write(data)
					}
				}
				if frame.EOF {
					if cw, ok := tc.(interface{ CloseWrite() error }); ok {
						_ = cw.CloseWrite()
					} else {
						_ = tc.Close()
					}
				}

			case "worker.tunnel.close":
				var cl api.WorkerTunnelClose
				_ = json.Unmarshal(raw, &cl)
				tunMu.Lock()
				tc := tunnels[cl.TunnelID]
				delete(tunnels, cl.TunnelID)
				tunMu.Unlock()
				if tc != nil {
					_ = tc.Close()
				}
			}
		}
	}()

	tunConn, err := reg.OpenTunnel(ctx, "wrk-rpc-1", ln.Addr().String())
	if err != nil {
		t.Fatalf("OpenTunnel failed: %v", err)
	}
	defer tunConn.Close()

	payload := "ping-over-worker-tunnel-0123456789\nsecond-line"
	go func() {
		_, _ = io.WriteString(tunConn, payload)
		if cw, ok := tunConn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			// net.Pipe half-close via Close would close reads too; wait after reading if CloseWrite absent
		}
	}()

	// net.Pipe does not implement CloseWrite, so read exact length first, then close.
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(tunConn, got); err != nil {
		t.Fatalf("ReadFull over tunnel failed: %v", err)
	}
	if string(got) != payload {
		t.Fatalf("echo mismatch: got %q want %q", string(got), payload)
	}
}

func TestWorkerRegistry_DisconnectCleansUpRPCAndTunnels(t *testing.T) {
	reg := NewWorkerRegistry()
	srv, disconnected := startRegistryTestServer(t, reg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	wsConn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := reg.Get("wrk-rpc-1"); ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	rpcErrCh := make(chan error, 1)
	go func() {
		rpcErrCh <- reg.CallRPC(ctx, "wrk-rpc-1", "vm.status", map[string]string{"vm_id": "x"}, nil)
	}()

	// Wait briefly so CallRPC registers pending channel, then drop WebSocket
	time.Sleep(50 * time.Millisecond)
	_ = wsConn.Close(websocket.StatusGoingAway, "bye")

	select {
	case <-disconnected:
	case <-time.After(2 * time.Second):
		t.Fatalf("server handler did not observe disconnect")
	}

	select {
	case err := <-rpcErrCh:
		if err == nil {
			t.Fatalf("expected pending CallRPC to fail on disconnect")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("pending CallRPC hung after disconnect")
	}
}
