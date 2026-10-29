package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCalculateMetrics(t *testing.T) {
	durations := []time.Duration{
		10 * time.Millisecond,
		20 * time.Millisecond,
		30 * time.Millisecond,
		40 * time.Millisecond,
		50 * time.Millisecond,
	}

	m := calculateMetrics("test_metric", durations)
	if m.Count != 5 {
		t.Fatalf("expected count 5, got %d", m.Count)
	}
	if m.Min != 10*time.Millisecond {
		t.Errorf("expected min 10ms, got %v", m.Min)
	}
	if m.Max != 50*time.Millisecond {
		t.Errorf("expected max 50ms, got %v", m.Max)
	}
	if m.Avg != 30*time.Millisecond {
		t.Errorf("expected avg 30ms, got %v", m.Avg)
	}
	if m.P50 != 30*time.Millisecond {
		t.Errorf("expected P50 30ms, got %v", m.P50)
	}
	if m.OpsPerSec <= 0 {
		t.Errorf("expected positive ops per sec, got %f", m.OpsPerSec)
	}
}

func TestCalculateMetricsEmpty(t *testing.T) {
	m := calculateMetrics("empty", nil)
	if m.Count != 0 {
		t.Errorf("expected count 0 for empty slice, got %d", m.Count)
	}
}

func TestSaveAndCompareBaseline(t *testing.T) {
	tmpDir := t.TempDir()
	baseFile := filepath.Join(tmpDir, "baseline.json")

	baseReport := BenchmarkReport{
		Timestamp:  time.Now().UTC(),
		DaemonAddr: "127.0.0.1:18080",
		Metrics: map[string]MetricResult{
			"api_health_latency": {
				Name:  "API: GET /health Latency",
				Count: 10,
				Avg:   2 * time.Millisecond,
			},
		},
	}

	data, err := json.Marshal(baseReport)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(baseFile, data, 0644); err != nil {
		t.Fatal(err)
	}

	currReport := BenchmarkReport{
		Timestamp:  time.Now().UTC(),
		DaemonAddr: "127.0.0.1:18080",
		Metrics: map[string]MetricResult{
			"api_health_latency": {
				Name:  "API: GET /health Latency",
				Count: 10,
				Avg:   1 * time.Millisecond, // 50% faster
			},
		},
	}

	// Exercise comparison function
	compareWithBaseline(baseFile, currReport)
}
