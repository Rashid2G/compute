package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/worker"
)

func TestEndToEndDistributedWorkerAndJob(t *testing.T) {
	server := newTestServer(t)

	// 1. Generate an enrollment token
	tokenResp, err := http.Post(server.URL+"/api/v1/worker-tokens", "application/json", strings.NewReader(`{"name":"e2e-worker","pool_id":"default"}`))
	if err != nil {
		t.Fatalf("failed to create worker token: %v", err)
	}
	defer tokenResp.Body.Close()
	if tokenResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created for token, got %d", tokenResp.StatusCode)
	}

	// 2. Start worker client in background
	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()

	workerClient := &worker.Client{
		Config: worker.Config{
			ControlPlaneURL: server.URL,
			Name:            "e2e-node-1",
			Token:           "dummy-token",
		},
	}

	go func() {
		_ = workerClient.Run(workerCtx)
	}()

	// Wait for worker to connect and register via GET /api/v1/workers
	var workerFound bool
	for i := 0; i < 20; i++ {
		time.Sleep(100 * time.Millisecond)
		resp, err := http.Get(server.URL + "/api/v1/workers")
		if err == nil && resp.StatusCode == http.StatusOK {
			var workers []*db.WorkerNode
			_ = json.NewDecoder(resp.Body).Decode(&workers)
			resp.Body.Close()
			if len(workers) > 0 {
				workerFound = true
				break
			}
		}
	}
	if !workerFound {
		t.Fatalf("worker failed to register within timeout")
	}

	// 3. Submit Job to daemon
	jobBody := `{"type":"command","pool_id":"default","priority":10,"spec":{"image_id":"ubuntu-24","architecture":"amd64","vcpus":1,"memory_mb":512,"command":["echo","hello from distributed worker"]}}`
	respJob, err := http.Post(server.URL+"/api/v1/jobs", "application/json", strings.NewReader(jobBody))
	if err != nil {
		t.Fatalf("failed to post job: %v", err)
	}
	defer respJob.Body.Close()
	if respJob.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created for job, got %d", respJob.StatusCode)
	}
	var createdJob db.Job
	_ = json.NewDecoder(respJob.Body).Decode(&createdJob)

	// 4. Wait for job to reach succeeded state via GET /api/v1/jobs/{id}
	var succeeded bool
	for i := 0; i < 30; i++ {
		time.Sleep(100 * time.Millisecond)
		resp, err := http.Get(server.URL + "/api/v1/jobs/" + createdJob.ID)
		if err == nil && resp.StatusCode == http.StatusOK {
			var j db.Job
			_ = json.NewDecoder(resp.Body).Decode(&j)
			resp.Body.Close()
			if j.State == "succeeded" {
				succeeded = true
				break
			}
		}
	}

	if !succeeded {
		t.Fatalf("job did not reach succeeded state within timeout")
	}

	// 5. Verify logs were streamed and stored via GET /api/v1/jobs/{id}/logs
	respLogs, err := http.Get(server.URL + "/api/v1/jobs/" + createdJob.ID + "/logs")
	if err != nil {
		t.Fatalf("failed to get job logs: %v", err)
	}
	defer respLogs.Body.Close()
	if respLogs.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for logs, got %d", respLogs.StatusCode)
	}

	var logs []*db.JobLog
	_ = json.NewDecoder(respLogs.Body).Decode(&logs)
	if len(logs) == 0 {
		raw, _ := io.ReadAll(respLogs.Body)
		t.Fatalf("expected at least one log entry streamed from worker, got 0. raw: %s", string(raw))
	}
	if !strings.Contains(logs[0].Data, "hello from distributed worker") {
		t.Fatalf("unexpected log output: %s", logs[0].Data)
	}
}
