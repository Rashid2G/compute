package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Rashid2G/compute/pkg/api"
	"github.com/coder/websocket"
)

func TestDetectCapabilities(t *testing.T) {
	caps := DetectCapabilities()
	if caps == nil {
		t.Fatalf("expected non-nil capabilities")
	}
	arch, ok := caps["architecture"].(string)
	if !ok || arch == "" {
		t.Fatalf("expected valid architecture, got %v", caps["architecture"])
	}
	cpus, ok := caps["cpus"].(int)
	if !ok || cpus <= 0 {
		t.Fatalf("expected cpus > 0, got %v", caps["cpus"])
	}
}

func TestWorkerClientHandshake(t *testing.T) {
	// Setup mock server
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/api/v1/worker/connect") {
			http.NotFound(w, r)
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")

		// Read hello
		_, b, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		var hello api.WorkerHello
		if err := json.Unmarshal(b, &hello); err != nil {
			return
		}

		// Send hello_ack
		ack := api.WorkerHelloAck{
			WorkerMessage: api.WorkerMessage{
				Version:  1,
				Type:     "worker.hello_ack",
				WorkerID: "worker-mock-1",
			},
			Accepted: true,
			WorkerID: "worker-mock-1",
		}
		ackBytes, _ := json.Marshal(ack)
		_ = conn.Write(r.Context(), websocket.MessageText, ackBytes)

		// Send an assignment
		assign := api.WorkerAssignment{
			WorkerMessage: api.WorkerMessage{
				Version:  1,
				Type:     "worker.assignment",
				JobID:    "job-mock-1",
				WorkerID: "worker-mock-1",
			},
			LeaseID:   "lease-1",
			AttemptID: "attempt-1",
			Spec: api.JobSpec{
				Command: []string{"echo", "worker test passed"},
			},
		}
		assignBytes, _ := json.Marshal(assign)
		_ = conn.Write(r.Context(), websocket.MessageText, assignBytes)

		// Read assignment_ack
		_, ackRaw, _ := conn.Read(r.Context())
		var assignAck api.WorkerAssignmentAck
		_ = json.Unmarshal(ackRaw, &assignAck)

		// Read log chunks and workload event
		for {
			_, msgRaw, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var head api.WorkerMessage
			if err := json.Unmarshal(msgRaw, &head); err != nil {
				continue
			}
			if head.Type == "worker.workload_event" {
				return
			}
		}
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := &Client{
		Config: Config{
			ControlPlaneURL: srv.URL,
			Name:            "test-worker",
			Token:           "test-token",
		},
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- client.Run(ctx)
	}()

	select {
	case <-time.After(1 * time.Second):
		cancel()
	case err := <-errCh:
		if err != nil && !strings.Contains(err.Error(), "context canceled") && websocket.CloseStatus(err) != websocket.StatusNormalClosure {
			t.Fatalf("worker run failed: %v", err)
		}
	}
}
