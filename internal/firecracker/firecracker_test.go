package firecracker

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestFirecrackerClientMock(t *testing.T) {
	// Unix socket path max length is 108 bytes on Linux, so use /tmp
	tmpDir, err := os.MkdirTemp("/tmp", "fc-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	sockPath := filepath.Join(tmpDir, "fc.sock")
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("failed to listen on unix sock: %v", err)
	}
	defer l.Close()

	calledEndpoints := make(map[string]bool)

	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calledEndpoints[r.Method+" "+r.URL.Path] = true

			switch r.URL.Path {
			case "/machine-config":
				var cfg MachineConfig
				json.NewDecoder(r.Body).Decode(&cfg)
				if cfg.VCPUCount != 2 || cfg.MemSizeMiB != 1024 {
					t.Errorf("unexpected machine config: %+v", cfg)
				}
				w.WriteHeader(http.StatusNoContent)
			case "/boot-source":
				var bs BootSource
				json.NewDecoder(r.Body).Decode(&bs)
				if bs.KernelImagePath != "/test/vmlinux" {
					t.Errorf("unexpected boot source: %+v", bs)
				}
				w.WriteHeader(http.StatusNoContent)
			case "/drives/rootfs":
				w.WriteHeader(http.StatusNoContent)
			case "/network-interfaces/eth0":
				w.WriteHeader(http.StatusNoContent)
			case "/actions":
				w.WriteHeader(http.StatusNoContent)
			case "/vm":
				w.WriteHeader(http.StatusNoContent)
			case "/snapshot/create":
				w.WriteHeader(http.StatusNoContent)
			case "/snapshot/load":
				w.WriteHeader(http.StatusNoContent)
			case "/vsock":
				var vsock VsockConfig
				json.NewDecoder(r.Body).Decode(&vsock)
				if vsock.GuestCID != 3 || vsock.UdsPath != "/test/vsock.sock" {
					t.Errorf("unexpected vsock config: %+v", vsock)
				}
				w.WriteHeader(http.StatusNoContent)
			default:
				http.NotFound(w, r)
			}
		}),
	}
	go server.Serve(l)
	defer server.Close()

	client := NewClient(sockPath)
	ctx := context.Background()

	if err := client.SetMachineConfig(ctx, MachineConfig{VCPUCount: 2, MemSizeMiB: 1024}); err != nil {
		t.Fatalf("SetMachineConfig failed: %v", err)
	}

	if err := client.SetBootSource(ctx, BootSource{KernelImagePath: "/test/vmlinux", BootArgs: "console=ttyS0"}); err != nil {
		t.Fatalf("SetBootSource failed: %v", err)
	}

	if err := client.AttachDrive(ctx, Drive{DriveID: "rootfs", PathOnHost: "/test/rootfs.ext4", IsRootDevice: true}); err != nil {
		t.Fatalf("AttachDrive failed: %v", err)
	}

	if err := client.AttachNetworkInterface(ctx, NetworkInterface{IfaceID: "eth0", GuestMAC: "06:00:ac:10:00:02", HostDevName: "tap0"}); err != nil {
		t.Fatalf("AttachNetworkInterface failed: %v", err)
	}

	if err := client.SetVsock(ctx, VsockConfig{VsockID: "vsock", GuestCID: 3, UdsPath: "/test/vsock.sock"}); err != nil {
		t.Fatalf("SetVsock failed: %v", err)
	}

	if err := client.StartInstance(ctx); err != nil {
		t.Fatalf("StartInstance failed: %v", err)
	}

	if err := client.Pause(ctx); err != nil {
		t.Fatalf("Pause failed: %v", err)
	}

	if err := client.CreateSnapshot(ctx, "/test/snap", "/test/mem"); err != nil {
		t.Fatalf("CreateSnapshot failed: %v", err)
	}

	if err := client.LoadSnapshot(ctx, "/test/snap", "/test/mem", true); err != nil {
		t.Fatalf("LoadSnapshot failed: %v", err)
	}

	if err := client.Resume(ctx); err != nil {
		t.Fatalf("Resume failed: %v", err)
	}

	if err := client.SendCtrlAltDel(ctx); err != nil {
		t.Fatalf("SendCtrlAltDel failed: %v", err)
	}

	expected := []string{
		"PUT /machine-config",
		"PUT /boot-source",
		"PUT /drives/rootfs",
		"PUT /network-interfaces/eth0",
		"PUT /vsock",
		"PUT /actions",
		"PATCH /vm",
		"PUT /snapshot/create",
		"PUT /snapshot/load",
	}

	for _, ep := range expected {
		if !calledEndpoints[ep] {
			t.Errorf("expected endpoint %s was not called", ep)
		}
	}
}
