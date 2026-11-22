package version

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestVersionGet(t *testing.T) {
	info := Get("compute", "Compute CLI")
	if info.Name != "Compute CLI" {
		t.Errorf("expected Name 'Compute CLI', got %q", info.Name)
	}
	if info.Service != "compute" {
		t.Errorf("expected Service 'compute', got %q", info.Service)
	}
	if info.Project != "Compute" {
		t.Errorf("expected Project 'Compute', got %q", info.Project)
	}
	if info.Version != "0.1.0" {
		t.Errorf("expected Version '0.1.0', got %q", info.Version)
	}
	if info.ReleaseName != "Compute" {
		t.Errorf("expected ReleaseName 'Compute', got %q", info.ReleaseName)
	}
	if info.GoVersion == "" || info.OS == "" || info.Arch == "" {
		t.Errorf("expected runtime info populated, got %+v", info)
	}

	str := info.String()
	if !strings.Contains(str, "Compute CLI (compute) v0.1.0") {
		t.Errorf("unexpected String output: %s", str)
	}

	detailed := info.Detailed("  ")
	if !strings.Contains(detailed, "Name:        Compute CLI (compute)") || !strings.Contains(detailed, "Version:     0.1.0") {
		t.Errorf("unexpected Detailed output: %s", detailed)
	}

	jsonStr, err := info.JSON()
	if err != nil {
		t.Fatalf("JSON() error: %v", err)
	}
	var unmarshaled Info
	if err := json.Unmarshal([]byte(jsonStr), &unmarshaled); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if unmarshaled.Version != info.Version || unmarshaled.Name != info.Name {
		t.Errorf("unmarshaled info mismatch: %+v", unmarshaled)
	}
}
