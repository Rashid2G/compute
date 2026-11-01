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

func TestWorkerMicroBenchmarks(t *testing.T) {
	tmpDir := t.TempDir()
	res, err := RunWorkerBenchmarks("test-host", tmpDir)
	if err != nil {
		t.Fatalf("RunWorkerBenchmarks failed: %v", err)
	}
	if res.CPUSHA256MBps <= 0 {
		t.Errorf("expected positive SHA256 throughput, got %f", res.CPUSHA256MBps)
	}
	if res.CPUPrimeDurationMs <= 0 {
		t.Errorf("expected positive prime duration, got %f", res.CPUPrimeDurationMs)
	}
	if res.MemSeqBandwidthMBps <= 0 {
		t.Errorf("expected positive mem bandwidth, got %f", res.MemSeqBandwidthMBps)
	}
	if res.DiskSeqWriteMBps <= 0 {
		t.Errorf("expected positive disk write throughput, got %f", res.DiskSeqWriteMBps)
	}
}

func TestBuildComparisons(t *testing.T) {
	h := WorkerResult{
		Target:               "host",
		CPUSHA256MBps:        1000.0,
		CPUPrimeDurationMs:   10.0,
		CPUPrimeOpsPerSec:    100.0,
		CPUMatrixMFLOPS:      500.0,
		MemSeqBandwidthMBps:  8000.0,
		MemCopyBandwidthMBps: 6000.0,
		DiskSeqWriteMBps:     200.0,
		DiskSeqReadMBps:      400.0,
		DiskRandom4KIOPS:     2500.0,
		DiskRandom4KAvgMs:    0.4,
	}

	v := WorkerResult{
		Target:               "vm",
		CPUSHA256MBps:        950.0, // 5% slower
		CPUPrimeDurationMs:   10.5,
		CPUPrimeOpsPerSec:    95.0,
		CPUMatrixMFLOPS:      480.0,
		MemSeqBandwidthMBps:  7600.0,
		MemCopyBandwidthMBps: 5700.0,
		DiskSeqWriteMBps:     180.0,
		DiskSeqReadMBps:      360.0,
		DiskRandom4KIOPS:     2200.0,
		DiskRandom4KAvgMs:    0.45,
	}

	comps := buildComparisons(h, v)
	if len(comps) == 0 {
		t.Fatalf("expected comparisons, got 0")
	}

	// First comparison is SHA-256
	shaComp := comps[0]
	if shaComp.MetricName != "SHA-256 Hashing Throughput" {
		t.Errorf("unexpected metric name: %s", shaComp.MetricName)
	}
	if shaComp.Ratio <= 0 || shaComp.Ratio > 1.0 {
		t.Errorf("expected ratio between 0 and 1, got %f", shaComp.Ratio)
	}
}

func TestGenerateHTMLReport(t *testing.T) {
	tmpDir := t.TempDir()
	htmlPath := filepath.Join(tmpDir, "report.html")

	report := BenchmarkReport{
		Timestamp:   time.Now().UTC(),
		DaemonAddr:  "127.0.0.1:18080",
		Environment: map[string]string{"kernel": "Linux 6.8.0"},
		Metrics: map[string]MetricResult{
			"api_health_latency": {
				Name:  "API: GET /health Latency",
				Count: 10,
				Avg:   time.Millisecond,
			},
		},
		HostVsVM: &HostVsVMSummary{
			Host: WorkerResult{CPUSHA256MBps: 1000},
			VM:   WorkerResult{CPUSHA256MBps: 900},
			Comparisons: []HostVsVMComparison{
				{
					MetricName:  "SHA-256",
					Category:    "CPU",
					Unit:        "MB/s",
					HostValue:   1000,
					VMValue:     900,
					OverheadPct: 10,
					Ratio:       0.9,
					Summary:     "Low Overhead (< 15%)",
				},
			},
		},
	}

	if err := GenerateHTMLReport(htmlPath, report); err != nil {
		t.Fatalf("GenerateHTMLReport failed: %v", err)
	}

	content, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) == 0 {
		t.Errorf("expected non-empty HTML report")
	}
}
