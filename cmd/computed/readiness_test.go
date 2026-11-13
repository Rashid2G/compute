package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rashid2G/on-demand-vms/internal/config"
	"github.com/Rashid2G/on-demand-vms/internal/doctor"
)

func TestReadyEndpointReportsMissingComponents(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.DBPath = filepath.Join(t.TempDir(), "data", "compute.db")
	cfg.Images.ImagesDir = t.TempDir()
	s := &Server{cfg: cfg}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ready", nil)
	rec := httptest.NewRecorder()
	s.handleReady(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("content type = %q", ct)
	}
	var report doctor.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if report.OK {
		t.Fatal("report should not be ready")
	}
	var names []string
	for _, c := range report.Checks {
		names = append(names, c.Name)
	}
	if !strings.Contains(strings.Join(names, ","), "rootfs") {
		t.Fatalf("checks missing rootfs: %v", names)
	}
}

func TestReadyEndpointRejectsPost(t *testing.T) {
	s := &Server{cfg: config.DefaultConfig()}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ready", nil)
	rec := httptest.NewRecorder()
	s.handleReady(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestHealthEndpointStaysLivenessOnly(t *testing.T) {
	s := &Server{cfg: config.DefaultConfig()}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rec := httptest.NewRecorder()
	s.handleHealth(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" {
		t.Fatalf("body = %+v", body)
	}
}
