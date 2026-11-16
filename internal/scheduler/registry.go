// Package scheduler contains the daemon's worker stream registry and scheduling primitives.
package scheduler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Rashid2G/compute/pkg/api"
	"github.com/coder/websocket"
	"github.com/google/uuid"
)

type rpcResult struct {
	resp api.WorkerRPCResponse
	err  error
}

type workerTunnel struct {
	id        string
	workerID  string
	relayConn net.Conn

	readyOnce sync.Once
	readyCh   chan error

	writeMu   sync.Mutex
	writeCh   chan []byte
	writeDone bool

	closeOnce sync.Once
	closedCh  chan struct{}
}

func newWorkerTunnel(id, workerID string, relayConn net.Conn) *workerTunnel {
	wt := &workerTunnel{
		id:        id,
		workerID:  workerID,
		relayConn: relayConn,
		readyCh:   make(chan error, 1),
		writeCh:   make(chan []byte, 128),
		closedCh:  make(chan struct{}),
	}
	go wt.writeLoop()
	return wt
}

func (wt *workerTunnel) signalReady(err error) {
	wt.readyOnce.Do(func() {
		wt.readyCh <- err
	})
}

func (wt *workerTunnel) enqueueWrite(data []byte) {
	if len(data) == 0 {
		return
	}
	wt.writeMu.Lock()
	if wt.writeDone {
		wt.writeMu.Unlock()
		return
	}
	ch := wt.writeCh
	wt.writeMu.Unlock()

	select {
	case ch <- data:
	case <-wt.closedCh:
	}
}

func (wt *workerTunnel) markRemoteEOF() {
	wt.writeMu.Lock()
	if !wt.writeDone {
		wt.writeDone = true
		close(wt.writeCh)
	}
	wt.writeMu.Unlock()
}

func (wt *workerTunnel) writeLoop() {
	for data := range wt.writeCh {
		if len(data) == 0 {
			continue
		}
		if _, err := wt.relayConn.Write(data); err != nil {
			wt.close()
			return
		}
	}
	// Remote sent EOF and all buffered writes have flushed to relayConn.
	// Close relayConn so callerConn.Read receives io.EOF.
	wt.close()
}

func (wt *workerTunnel) close() {
	wt.closeOnce.Do(func() {
		close(wt.closedCh)
		wt.markRemoteEOF()
		_ = wt.relayConn.Close()
		wt.signalReady(errors.New("tunnel closed"))
	})
}

// Worker is an authenticated, live worker stream.
type Worker struct {
	ID             string
	PoolID         string
	OrganizationID string
	Labels         map[string]string
	Capabilities   map[string]interface{}
	Metrics        map[string]interface{}
	Status         string
	ConnectedAt    time.Time
	conn           *websocket.Conn
	writeMu        sync.Mutex
	heartbeatSeen  atomic.Bool

	rpcMu      sync.Mutex
	closed     bool
	pendingRPC map[string]chan rpcResult

	tunMu   sync.Mutex
	tunnels map[string]*workerTunnel
}

func (w *Worker) closeAll(err error) {
	w.rpcMu.Lock()
	w.closed = true
	for reqID, ch := range w.pendingRPC {
		select {
		case ch <- rpcResult{err: err}:
		default:
		}
		delete(w.pendingRPC, reqID)
	}
	w.rpcMu.Unlock()

	w.tunMu.Lock()
	for tid, wt := range w.tunnels {
		wt.close()
		delete(w.tunnels, tid)
	}
	w.tunMu.Unlock()
}

// WorkerRegistry tracks connected workers. Persistence remains the repository's responsibility.
type WorkerRegistry struct {
	mu      sync.RWMutex
	workers map[string]*Worker
}

func NewWorkerRegistry() *WorkerRegistry { return &WorkerRegistry{workers: make(map[string]*Worker)} }

func (r *WorkerRegistry) Register(w *Worker) error {
	if w == nil || w.ID == "" || w.conn == nil {
		return errors.New("worker id and connection are required")
	}
	w.ConnectedAt = time.Now().UTC()
	w.rpcMu.Lock()
	if w.pendingRPC == nil {
		w.pendingRPC = make(map[string]chan rpcResult)
	}
	w.closed = false
	w.rpcMu.Unlock()

	w.tunMu.Lock()
	if w.tunnels == nil {
		w.tunnels = make(map[string]*workerTunnel)
	}
	w.tunMu.Unlock()

	r.mu.Lock()
	prev := r.workers[w.ID]
	r.workers[w.ID] = w
	r.mu.Unlock()

	if prev != nil && prev != w {
		prev.closeAll(errors.New("worker reconnected"))
	}
	return nil
}

func (r *WorkerRegistry) Remove(id string, conn *websocket.Conn) {
	r.mu.Lock()
	var removed *Worker
	if w := r.workers[id]; w != nil && w.conn == conn {
		removed = w
		delete(r.workers, id)
	}
	r.mu.Unlock()
	if removed != nil {
		removed.closeAll(errors.New("worker disconnected"))
	}
}

func (r *WorkerRegistry) Get(id string) (*Worker, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	w, ok := r.workers[id]
	return w, ok
}

func (r *WorkerRegistry) List() []*Worker {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Worker, 0, len(r.workers))
	for _, w := range r.workers {
		out = append(out, w)
	}
	return out
}

// Send marshals a protocol message and writes it on the worker's active stream.
func (r *WorkerRegistry) Send(ctx context.Context, workerID string, message any) error {
	w, ok := r.Get(workerID)
	if !ok {
		return errors.New("worker is not connected")
	}
	b, err := json.Marshal(message)
	if err != nil {
		return err
	}
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	return w.conn.Write(ctx, websocket.MessageText, b)
}

// CallRPC sends an RPC request to a connected worker and waits for its response or context cancellation.
func (r *WorkerRegistry) CallRPC(ctx context.Context, workerID, method string, reqPayload any, respOut any) error {
	w, ok := r.Get(workerID)
	if !ok {
		return fmt.Errorf("worker %s is not connected", workerID)
	}

	var rawPayload json.RawMessage
	if reqPayload != nil {
		switch v := reqPayload.(type) {
		case json.RawMessage:
			rawPayload = v
		case []byte:
			rawPayload = json.RawMessage(v)
		default:
			b, err := json.Marshal(reqPayload)
			if err != nil {
				return fmt.Errorf("marshal rpc payload: %w", err)
			}
			rawPayload = b
		}
	}

	reqID := uuid.NewString()
	ch := make(chan rpcResult, 1)

	w.rpcMu.Lock()
	if w.closed {
		w.rpcMu.Unlock()
		return fmt.Errorf("worker %s disconnected", workerID)
	}
	if w.pendingRPC == nil {
		w.pendingRPC = make(map[string]chan rpcResult)
	}
	w.pendingRPC[reqID] = ch
	w.rpcMu.Unlock()

	defer func() {
		w.rpcMu.Lock()
		delete(w.pendingRPC, reqID)
		w.rpcMu.Unlock()
	}()

	req := api.WorkerRPCRequest{
		WorkerMessage: api.WorkerMessage{
			Version:  1,
			Type:     "worker.rpc.req",
			ID:       reqID,
			WorkerID: workerID,
		},
		RequestID: reqID,
		Method:    method,
		Payload:   rawPayload,
	}

	if err := r.Send(ctx, workerID, req); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case res := <-ch:
		if res.err != nil {
			return res.err
		}
		if res.resp.Error != "" {
			if res.resp.Code == api.ErrCodeResourceLimitExceeded && len(res.resp.Result) > 0 {
				var rle api.ResourceLimitError
				if err := json.Unmarshal(res.resp.Result, &rle); err == nil && rle.Code != "" {
					return &rle
				}
			}
			return errors.New(res.resp.Error)
		}
		if respOut != nil && len(res.resp.Result) > 0 {
			if err := json.Unmarshal(res.resp.Result, respOut); err != nil {
				return fmt.Errorf("decode rpc response for %s: %w", method, err)
			}
		}
		return nil
	}
}

// HandleRPCResponse delivers a worker.rpc.resp frame to its awaiting CallRPC caller.
func (r *WorkerRegistry) HandleRPCResponse(resp api.WorkerRPCResponse) {
	if resp.RequestID == "" {
		return
	}
	var target *Worker
	if resp.WorkerID != "" {
		target, _ = r.Get(resp.WorkerID)
	}
	if target == nil {
		r.mu.RLock()
		for _, w := range r.workers {
			w.rpcMu.Lock()
			_, exists := w.pendingRPC[resp.RequestID]
			w.rpcMu.Unlock()
			if exists {
				target = w
				break
			}
		}
		r.mu.RUnlock()
	}
	if target == nil {
		return
	}

	target.rpcMu.Lock()
	ch, ok := target.pendingRPC[resp.RequestID]
	if ok {
		delete(target.pendingRPC, resp.RequestID)
	}
	target.rpcMu.Unlock()

	if ok && ch != nil {
		select {
		case ch <- rpcResult{resp: resp}:
		default:
		}
	}
}

// OpenTunnel opens a full-duplex TCP tunnel to targetAddr through a connected worker's WebSocket stream.
func (r *WorkerRegistry) OpenTunnel(ctx context.Context, workerID, targetAddr string) (net.Conn, error) {
	w, ok := r.Get(workerID)
	if !ok {
		return nil, fmt.Errorf("worker %s is not connected", workerID)
	}

	callerConn, relayConn := net.Pipe()
	tunnelID := "tun_" + uuid.NewString()
	wt := newWorkerTunnel(tunnelID, workerID, relayConn)

	w.tunMu.Lock()
	w.rpcMu.Lock()
	closed := w.closed
	w.rpcMu.Unlock()
	if closed {
		w.tunMu.Unlock()
		wt.close()
		_ = callerConn.Close()
		return nil, fmt.Errorf("worker %s disconnected", workerID)
	}
	if w.tunnels == nil {
		w.tunnels = make(map[string]*workerTunnel)
	}
	w.tunnels[tunnelID] = wt
	w.tunMu.Unlock()

	removeTunnel := func() {
		w.tunMu.Lock()
		delete(w.tunnels, tunnelID)
		w.tunMu.Unlock()
	}

	openMsg := api.WorkerTunnelOpen{
		WorkerMessage: api.WorkerMessage{
			Version:  1,
			Type:     "worker.tunnel.open",
			ID:       tunnelID,
			WorkerID: workerID,
		},
		TunnelID:   tunnelID,
		TargetAddr: targetAddr,
	}
	if err := r.Send(ctx, workerID, openMsg); err != nil {
		removeTunnel()
		wt.close()
		_ = callerConn.Close()
		return nil, err
	}

	select {
	case err := <-wt.readyCh:
		if err != nil {
			removeTunnel()
			wt.close()
			_ = callerConn.Close()
			return nil, err
		}
	case <-ctx.Done():
		removeTunnel()
		wt.close()
		_ = callerConn.Close()
		_ = r.Send(context.Background(), workerID, api.WorkerTunnelClose{
			WorkerMessage: api.WorkerMessage{
				Version:  1,
				Type:     "worker.tunnel.close",
				ID:       uuid.NewString(),
				WorkerID: workerID,
			},
			TunnelID: tunnelID,
			Error:    ctx.Err().Error(),
		})
		return nil, ctx.Err()
	}

	// Pump callerConn -> relayConn -> worker WebSocket
	go func() {
		defer func() {
			removeTunnel()
			wt.close()
		}()
		buf := make([]byte, 32*1024)
		for {
			n, err := relayConn.Read(buf)
			if n > 0 {
				frame := api.WorkerTunnelData{
					WorkerMessage: api.WorkerMessage{
						Version:  1,
						Type:     "worker.tunnel.data",
						ID:       uuid.NewString(),
						WorkerID: workerID,
					},
					TunnelID: tunnelID,
					Data:     base64.StdEncoding.EncodeToString(buf[:n]),
				}
				if sendErr := r.Send(context.Background(), workerID, frame); sendErr != nil {
					return
				}
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					_ = r.Send(context.Background(), workerID, api.WorkerTunnelData{
						WorkerMessage: api.WorkerMessage{
							Version:  1,
							Type:     "worker.tunnel.data",
							ID:       uuid.NewString(),
							WorkerID: workerID,
						},
						TunnelID: tunnelID,
						EOF:      true,
					})
				} else {
					_ = r.Send(context.Background(), workerID, api.WorkerTunnelClose{
						WorkerMessage: api.WorkerMessage{
							Version:  1,
							Type:     "worker.tunnel.close",
							ID:       uuid.NewString(),
							WorkerID: workerID,
						},
						TunnelID: tunnelID,
						Error:    err.Error(),
					})
				}
				return
			}
		}
	}()

	return callerConn, nil
}

// HandleTunnelFrame processes worker.tunnel.data and worker.tunnel.close frames from a worker.
func (r *WorkerRegistry) HandleTunnelFrame(workerID string, msgType string, raw json.RawMessage) {
	var findTunnel = func(tunnelID string) (*Worker, *workerTunnel) {
		if workerID != "" {
			if w, ok := r.Get(workerID); ok {
				w.tunMu.Lock()
				wt := w.tunnels[tunnelID]
				w.tunMu.Unlock()
				if wt != nil {
					return w, wt
				}
			}
		}
		r.mu.RLock()
		defer r.mu.RUnlock()
		for _, w := range r.workers {
			w.tunMu.Lock()
			wt := w.tunnels[tunnelID]
			w.tunMu.Unlock()
			if wt != nil {
				return w, wt
			}
		}
		return nil, nil
	}

	switch msgType {
	case "worker.tunnel.data":
		var frame api.WorkerTunnelData
		if err := json.Unmarshal(raw, &frame); err != nil || frame.TunnelID == "" {
			return
		}
		_, wt := findTunnel(frame.TunnelID)
		if wt == nil {
			return
		}
		wt.signalReady(nil)
		if frame.Data != "" {
			data, err := base64.StdEncoding.DecodeString(frame.Data)
			if err == nil && len(data) > 0 {
				wt.enqueueWrite(data)
			}
		}
		if frame.EOF {
			wt.markRemoteEOF()
		}

	case "worker.tunnel.close":
		var frame api.WorkerTunnelClose
		if err := json.Unmarshal(raw, &frame); err != nil || frame.TunnelID == "" {
			return
		}
		w, wt := findTunnel(frame.TunnelID)
		if wt == nil {
			return
		}
		if w != nil {
			w.tunMu.Lock()
			delete(w.tunnels, frame.TunnelID)
			w.tunMu.Unlock()
		}
		errMsg := frame.Error
		if errMsg == "" {
			errMsg = "tunnel closed"
		}
		wt.signalReady(errors.New(errMsg))
		wt.close()
	}
}

// ServeStream processes worker messages after hello has authenticated and registered the connection.
func (r *WorkerRegistry) ServeStream(ctx context.Context, w *Worker, handle func(context.Context, *Worker, api.WorkerMessage, json.RawMessage) error) error {
	if err := r.Register(w); err != nil {
		return err
	}
	defer r.Remove(w.ID, w.conn)
	for {
		_, b, err := w.conn.Read(ctx)
		if err != nil {
			return err
		}
		var envelope api.WorkerMessage
		if err = json.Unmarshal(b, &envelope); err != nil {
			continue
		}
		if envelope.WorkerID == "" {
			envelope.WorkerID = w.ID
		}
		switch envelope.Type {
		case "worker.rpc.resp":
			var resp api.WorkerRPCResponse
			if err := json.Unmarshal(b, &resp); err == nil {
				if resp.WorkerID == "" {
					resp.WorkerID = w.ID
				}
				r.HandleRPCResponse(resp)
			}
			continue
		case "worker.tunnel.data", "worker.tunnel.close":
			r.HandleTunnelFrame(w.ID, envelope.Type, b)
			continue
		}
		if handle != nil {
			if err = handle(ctx, w, envelope, b); err != nil {
				return err
			}
		}
	}
}

// NewWorker constructs an active Worker stream object.
func NewWorker(id, poolID string, capabilities map[string]interface{}, conn *websocket.Conn) *Worker {
	return &Worker{
		ID:           id,
		PoolID:       poolID,
		Status:       "ready",
		Capabilities: capabilities,
		ConnectedAt:  time.Now().UTC(),
		conn:         conn,
		pendingRPC:   make(map[string]chan rpcResult),
		tunnels:      make(map[string]*workerTunnel),
	}
}
