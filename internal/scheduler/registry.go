// Package scheduler contains the daemon's worker stream registry and scheduling primitives.
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/Rashid2G/compute/pkg/api"
	"github.com/coder/websocket"
)

// Worker is an authenticated, live worker stream.
type Worker struct {
	ID             string
	PoolID         string
	OrganizationID string
	Labels         map[string]string
	Capabilities   map[string]interface{}
	Status         string
	ConnectedAt    time.Time
	conn           *websocket.Conn
	writeMu        sync.Mutex
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
	r.mu.Lock()
	defer r.mu.Unlock()
	r.workers[w.ID] = w
	return nil
}

func (r *WorkerRegistry) Remove(id string, conn *websocket.Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if w := r.workers[id]; w != nil && w.conn == conn {
		delete(r.workers, id)
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
	}
}
