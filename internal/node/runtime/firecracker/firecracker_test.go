package firecracker

import (
	"encoding/json"
	"testing"
)

func TestBuildVMConfig(t *testing.T) {
	cfg, err := BuildVMConfig(
		"/vmlinux",
		"/rootfs.ext4",
		"console=ttyS0 quiet root=/dev/vda rw",
		2,
		1024,
		"tap0",
		"06:00:ac:10:00:02",
		"/v.sock",
	)
	if err != nil {
		t.Fatalf("BuildVMConfig failed: %v", err)
	}

	data, err := cfg.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON failed: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("invalid json generated: %v", err)
	}

	// Verify boot-source
	bootSource, ok := parsed["boot-source"].(map[string]interface{})
	if !ok || bootSource["kernel_image_path"] != "/vmlinux" {
		t.Errorf("expected kernel path /vmlinux, got %v", bootSource)
	}

	// Verify machine-config
	machineCfg, ok := parsed["machine-config"].(map[string]interface{})
	if !ok || machineCfg["vcpu_count"].(float64) != 2 || machineCfg["mem_size_mib"].(float64) != 1024 {
		t.Errorf("unexpected machine-config: %v", machineCfg)
	}

	// Verify vsock
	vsockCfg, ok := parsed["vsock"].(map[string]interface{})
	if !ok || vsockCfg["guest_cid"].(float64) != 3 || vsockCfg["uds_path"] != "/v.sock" {
		t.Errorf("unexpected vsock config: %v", vsockCfg)
	}

	// Verify MMDS is NOT present (SR-ISO-05)
	if _, present := parsed["mmds-config"]; present {
		t.Error("MMDS must remain disabled, but mmds-config was found in config JSON")
	}
}
