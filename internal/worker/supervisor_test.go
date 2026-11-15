package worker_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/worker"
	"github.com/Rashid2G/on-demand-vms/pkg/api"
)

func TestLocalSandboxSupervisor_Execution(t *testing.T) {
	tmpDir := t.TempDir()
	supervisor := worker.NewLocalSandboxSupervisor(tmpDir)

	ctx := context.Background()
	var mu sync.Mutex
	var logs []string
	streamLog := func(stream, data string) {
		mu.Lock()
		logs = append(logs, stream+":"+data)
		mu.Unlock()
	}

	assignment := api.WorkerAssignment{
		WorkerMessage: api.WorkerMessage{
			JobID: "job-test-1",
		},
		AttemptID: "att-1",
		Spec: api.JobSpec{
			Command: []string{"sh", "-c", "echo 'hello from sandbox' && echo 'error line' >&2"},
			Env:     map[string]string{"TEST_ENV": "active"},
		},
	}

	code, err := supervisor.Execute(ctx, assignment, streamLog)
	if err != nil {
		t.Fatalf("supervisor.Execute failed: %v", err)
	}
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}

	var hasStdout, hasStderr bool
	for _, l := range logs {
		if strings.Contains(l, "stdout:hello from sandbox") {
			hasStdout = true
		}
		if strings.Contains(l, "stderr:error line") {
			hasStderr = true
		}
	}
	if !hasStdout || !hasStderr {
		t.Fatalf("missing expected log stream output, got: %v", logs)
	}
}

func TestLocalSandboxSupervisor_FileExtraction(t *testing.T) {
	tmpDir := t.TempDir()
	supervisor := worker.NewLocalSandboxSupervisor(tmpDir)

	// Create test tar.gz
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	fileContent := []byte("print('isolated code execution')\n")
	hdr := &tar.Header{
		Name: "script.py",
		Mode: 0644,
		Size: int64(len(fileContent)),
	}
	_ = tw.WriteHeader(hdr)
	_, _ = tw.Write(fileContent)
	_ = tw.Close()
	_ = gw.Close()

	encoded := base64.StdEncoding.EncodeToString(buf.Bytes())

	ctx := context.Background()
	var logs []string
	streamLog := func(stream, data string) {
		logs = append(logs, stream+":"+data)
	}

	assignment := api.WorkerAssignment{
		WorkerMessage: api.WorkerMessage{
			JobID: "job-test-2",
		},
		AttemptID: "att-2",
		Spec: api.JobSpec{
			Files:   encoded,
			Command: []string{"sh", "-c", "cat script.py"},
		},
	}

	code, err := supervisor.Execute(ctx, assignment, streamLog)
	if err != nil {
		t.Fatalf("supervisor.Execute with files failed: %v", err)
	}
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}

	foundFileOutput := false
	for _, l := range logs {
		if strings.Contains(l, "isolated code execution") {
			foundFileOutput = true
		}
	}
	if !foundFileOutput {
		t.Fatalf("expected file content in logs, got: %v", logs)
	}
}

func TestLocalSandboxSupervisor_Cancellation(t *testing.T) {
	tmpDir := t.TempDir()
	supervisor := worker.NewLocalSandboxSupervisor(tmpDir)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	assignment := api.WorkerAssignment{
		WorkerMessage: api.WorkerMessage{
			JobID: "job-test-timeout",
		},
		AttemptID: "att-3",
		Spec: api.JobSpec{
			Command: []string{"sleep", "5"},
		},
	}

	code, err := supervisor.Execute(ctx, assignment, func(s, d string) {})
	if err == nil && code == 0 {
		t.Fatal("expected non-zero or error on timeout cancellation, got success")
	}
}
