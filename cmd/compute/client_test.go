package main

import (
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/db"
	"github.com/Rashid2G/on-demand-vms/internal/network"
	"github.com/Rashid2G/on-demand-vms/internal/vm"
)

func TestDaemonClient(t *testing.T) {
	mux := http.NewServeMux()

	vmMock := &db.VM{
		ID:        "vm-test1234",
		Name:      "test-vm",
		Status:    db.StatusRunning,
		IPAddress: "172.16.0.2",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	mux.HandleFunc("/api/v1/vms", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode([]*db.VM{vmMock})
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(vmMock)
		}
	})

	mux.HandleFunc("/api/v1/vms/vm-test1234", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode(vmMock)
		case http.MethodDelete:
			json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
		}
	})

	mux.HandleFunc("/api/v1/vms/vm-test1234/run", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(vmMock)
	})

	mux.HandleFunc("/api/v1/vms/vm-test1234/save-state", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		pausedVM := *vmMock
		pausedVM.Status = db.StatusPaused
		json.NewEncoder(w).Encode(&pausedVM)
	})

	mux.HandleFunc("/api/v1/vms/vm-test1234/resume", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(vmMock)
	})

	mux.HandleFunc("/api/v1/vms/vm-test1234/stop", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		stoppedVM := *vmMock
		stoppedVM.Status = db.StatusStopped
		json.NewEncoder(w).Encode(&stoppedVM)
	})

	mux.HandleFunc("/api/v1/network", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(&network.NetworkInfo{
			Mode:         network.ModeNAT,
			BridgeName:   "mft3br0",
			BridgeIP:     "172.16.0.1/16",
			SubnetCIDR:   "172.16.0.0/16",
			GatewayIP:    "172.16.0.1",
			Netmask:      "255.255.0.0",
			BridgeActive: true,
			NATEnabled:   true,
			ActiveTAPs:   []string{"mftap-test"},
			AllocatedIPs: 1,
		})
	})

	mux.HandleFunc("/api/v1/network/allocations", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]*db.NetworkAllocation{
			{
				VMID:       "vm-test1234",
				VMName:     "test-vm",
				Status:     db.StatusRunning,
				IPAddress:  "172.16.0.2",
				MACAddress: "06:00:ac:10:00:02",
				TAPDevice:  "mftap-test",
			},
		})
	})

	mux.HandleFunc("/api/v1/network/prune", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":      "ok",
			"pruned_taps": []string{"mftap-orphan1"},
		})
	})

	mux.HandleFunc("/api/v1/vms/vm-test1234/exec", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(&vm.ExecResponse{Stdout: "output", ExitCode: 0, DurationMs: 10})
	})

	mux.HandleFunc("/api/v1/network/inspect/vm-test1234", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(&vm.NetworkInspectResult{VMID: "vm-test1234", Reachable: true, Connectivity: "Reachable"})
	})

	mux.HandleFunc("/api/v1/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(&vm.StatsResult{TotalVMs: 1, RunningVMs: 1})
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewDaemonClient(server.URL)
	ctx := context.Background()

	// 1. Create
	created, err := client.CreateVM(ctx, vm.CreateRequest{Name: "test-vm"})
	if err != nil || created.ID != "vm-test1234" {
		t.Fatalf("CreateVM failed: %v, got %+v", err, created)
	}

	// 2. List
	vms, err := client.ListVMs(ctx)
	if err != nil || len(vms) != 1 {
		t.Fatalf("ListVMs failed: %v, got %d", err, len(vms))
	}

	// 3. Get
	got, err := client.GetVM(ctx, "vm-test1234")
	if err != nil || got.ID != "vm-test1234" {
		t.Fatalf("GetVM failed: %v", err)
	}

	// 4. Run
	ran, err := client.RunVM(ctx, "vm-test1234")
	if err != nil || ran.Status != db.StatusRunning {
		t.Fatalf("RunVM failed: %v", err)
	}

	// 5. SaveState
	saved, err := client.SaveState(ctx, "vm-test1234")
	if err != nil || saved.Status != db.StatusPaused {
		t.Fatalf("SaveState failed: %v", err)
	}

	// 6. Resume
	resumed, err := client.ResumeVM(ctx, "vm-test1234")
	if err != nil || resumed.Status != db.StatusRunning {
		t.Fatalf("ResumeVM failed: %v", err)
	}

	// 7. Stop
	stopped, err := client.StopVM(ctx, "vm-test1234")
	if err != nil || stopped.Status != db.StatusStopped {
		t.Fatalf("StopVM failed: %v", err)
	}

	// 8. Delete
	if err := client.DeleteVM(ctx, "vm-test1234"); err != nil {
		t.Fatalf("DeleteVM failed: %v", err)
	}

	// 9. Network Info
	netInfo, err := client.GetNetworkInfo(ctx)
	if err != nil || netInfo.Mode != network.ModeNAT {
		t.Fatalf("GetNetworkInfo failed: %v, got %+v", err, netInfo)
	}

	// 10. Network Allocations
	allocs, err := client.GetNetworkAllocations(ctx)
	if err != nil || len(allocs) != 1 || allocs[0].IPAddress != "172.16.0.2" {
		t.Fatalf("GetNetworkAllocations failed: %v, got %+v", err, allocs)
	}

	// 11. Network Prune
	pruned, err := client.PruneNetwork(ctx)
	if err != nil || len(pruned) != 1 || pruned[0] != "mftap-orphan1" {
		t.Fatalf("PruneNetwork failed: %v, got %+v", err, pruned)
	}

	// 12. ExecVM
	execRes, err := client.ExecVM(ctx, "vm-test1234", vm.ExecRequest{Command: "echo test"})
	if err != nil || execRes.Stdout != "output" {
		t.Fatalf("ExecVM failed: %v, got %+v", err, execRes)
	}

	// 13. InspectNetwork
	ins, err := client.InspectNetwork(ctx, "vm-test1234")
	if err != nil || !ins.Reachable {
		t.Fatalf("InspectNetwork failed: %v, got %+v", err, ins)
	}

	// 14. GetStats
	stats, err := client.GetStats(ctx)
	if err != nil || stats.TotalVMs != 1 {
		t.Fatalf("GetStats failed: %v, got %+v", err, stats)
	}
}

func TestDaemonClient_ImagesAndTemplates(t *testing.T) {
	mux := http.NewServeMux()

	imgMock := &db.CustomImage{
		ID:        "img-test1",
		Name:      "python-test",
		BaseImage: "ubuntu-24.04",
		Packages:  []string{"python3", "git"},
		SizeMB:    2048,
		Status:    db.ImageStatusReady,
	}

	tplMock := &db.Template{
		ID:         "tpl-test1",
		Name:       "web-tpl",
		SourceVMID: "vm-src",
		DiskPath:   "/tmp/web-tpl.ext4",
		VCPUs:      2,
		MemoryMB:   4096,
		DiskMB:     51200,
	}

	mux.HandleFunc("/api/v1/images", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode([]ImageItem{{
				ID:       imgMock.ID,
				Name:     imgMock.Name,
				Packages: imgMock.Packages,
				Status:   string(imgMock.Status),
				Kind:     "custom",
			}})
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(imgMock)
		}
	})

	mux.HandleFunc("/api/v1/images/python-test", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode(&ImageItem{
				ID:       imgMock.ID,
				Name:     imgMock.Name,
				Packages: imgMock.Packages,
				Status:   string(imgMock.Status),
				Kind:     "custom",
			})
		case http.MethodDelete:
			json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
		}
	})

	mux.HandleFunc("/api/v1/templates", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode([]*db.Template{tplMock})
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(tplMock)
		}
	})

	mux.HandleFunc("/api/v1/templates/web-tpl", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode(tplMock)
		case http.MethodDelete:
			json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
		}
	})

	mux.HandleFunc("/api/v1/templates/web-tpl/launch", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(&db.VM{
			ID:     "vm-from-tpl",
			Name:   "launched-vm",
			Status: db.StatusRunning,
			VCPUs:  2,
		})
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewDaemonClient(server.URL)
	ctx := context.Background()

	// Images
	imgs, err := client.ListImages(ctx)
	if err != nil || len(imgs) != 1 || imgs[0].Name != "python-test" {
		t.Fatalf("ListImages failed: %v, %+v", err, imgs)
	}

	bldImg, err := client.BuildImage(ctx, vm.BuildImageRequest{
		Name:     "python-test",
		Packages: []string{"python3", "git"},
	})
	if err != nil || bldImg.Name != "python-test" {
		t.Fatalf("BuildImage failed: %v", err)
	}

	getImg, err := client.GetImage(ctx, "python-test")
	if err != nil || getImg.Name != "python-test" {
		t.Fatalf("GetImage failed: %v", err)
	}

	if err := client.DeleteImage(ctx, "python-test"); err != nil {
		t.Fatalf("DeleteImage failed: %v", err)
	}

	// Templates
	tpls, err := client.ListTemplates(ctx)
	if err != nil || len(tpls) != 1 || tpls[0].Name != "web-tpl" {
		t.Fatalf("ListTemplates failed: %v, %+v", err, tpls)
	}

	createdTpl, err := client.CreateTemplate(ctx, vm.CreateTemplateRequest{
		Name: "web-tpl",
		VMID: "vm-src",
	})
	if err != nil || createdTpl.Name != "web-tpl" {
		t.Fatalf("CreateTemplate failed: %v", err)
	}

	getTpl, err := client.GetTemplate(ctx, "web-tpl")
	if err != nil || getTpl.Name != "web-tpl" {
		t.Fatalf("GetTemplate failed: %v", err)
	}

	launchedVM, err := client.LaunchTemplate(ctx, "web-tpl", vm.CreateRequest{Name: "launched-vm"})
	if err != nil || launchedVM.ID != "vm-from-tpl" {
		t.Fatalf("LaunchTemplate failed: %v", err)
	}

	if err := client.DeleteTemplate(ctx, "web-tpl"); err != nil {
		t.Fatalf("DeleteTemplate failed: %v", err)
	}
}

func TestDaemonClient_Keys(t *testing.T) {
	mux := http.NewServeMux()

	mockKey := &db.SSHKey{
		ID:          "ssh-1234",
		Name:        "laptop",
		PublicKey:   "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKVMQHURyGt9mXytbJpOeTM4z4jIIhwMQ+aeXq/vDwAj test@client",
		Fingerprint: "SHA256:testfp",
		CreatedAt:   time.Now().UTC(),
	}

	mux.HandleFunc("/api/v1/keys", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode(map[string]interface{}{"keys": []*db.SSHKey{mockKey}})
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(mockKey)
		}
	})

	mux.HandleFunc("/api/v1/keys/laptop", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode(mockKey)
		case http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		}
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewDaemonClient(server.URL)
	ctx := context.Background()

	// 1. List
	keys, err := client.ListKeys(ctx)
	if err != nil || len(keys) != 1 || keys[0].Name != "laptop" {
		t.Fatalf("ListKeys failed: %v, got %+v", err, keys)
	}

	// 2. Create
	created, err := client.CreateKey(ctx, "laptop", mockKey.PublicKey)
	if err != nil || created.Name != "laptop" {
		t.Fatalf("CreateKey failed: %v", err)
	}

	// 3. Get
	got, err := client.GetKey(ctx, "laptop")
	if err != nil || got.ID != "ssh-1234" {
		t.Fatalf("GetKey failed: %v", err)
	}

	// 4. Delete
	if err := client.DeleteKey(ctx, "laptop"); err != nil {
		t.Fatalf("DeleteKey failed: %v", err)
	}
}

func TestExecVMStream(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vms/vm-stream/exec-stream", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("response writer does not support flushing")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"stream\":\"stdout\",\"data\":\"hello\\n\"}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("data: {\"stream\":\"stderr\",\"data\":\"oops\\n\"}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("data: {\"exit_code\":3,\"duration_ms\":12}\n\n"))
		flusher.Flush()
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewDaemonClient(server.URL)
	var stdout, stderr string
	exitCode := 0
	err := client.ExecVMStream(context.Background(), "vm-stream", vm.ExecRequest{Command: "echo hi"}, func(ev vm.ExecStreamEvent) error {
		switch ev.Stream {
		case "stdout":
			stdout += ev.Data
		case "stderr":
			stderr += ev.Data
		}
		if ev.ExitCode != nil {
			exitCode = *ev.ExitCode
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ExecVMStream failed: %v", err)
	}
	if stdout != "hello\n" || stderr != "oops\n" {
		t.Fatalf("unexpected output: stdout=%q stderr=%q", stdout, stderr)
	}
	if exitCode != 3 {
		t.Fatalf("exit code = %d, want 3", exitCode)
	}
}

func TestExecVMStreamErrorStatus(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vms/missing/exec-stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"vm not found"}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewDaemonClient(server.URL)
	err := client.ExecVMStream(context.Background(), "missing", vm.ExecRequest{Command: "true"}, func(vm.ExecStreamEvent) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "vm not found") {
		t.Fatalf("expected vm not found error, got %v", err)
	}
}

func TestClientSSHWebSocket(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vms/vm-123/ssh", func(w http.ResponseWriter, r *http.Request) {
		opts := &websocket.AcceptOptions{InsecureSkipVerify: true}
		conn, err := websocket.Accept(w, r, opts)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		_ = conn.Write(r.Context(), websocket.MessageText, []byte("hello from ssh ws"))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewDaemonClient(server.URL)
	query := url.Values{"user": []string{"compute"}}
	wsURL := client.SSHWebSocketURL("vm-123", query)
	if !strings.HasPrefix(wsURL, "ws://") || !strings.Contains(wsURL, "/api/v1/vms/vm-123/ssh?user=compute") {
		t.Fatalf("unexpected wsURL: %s", wsURL)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, err := client.DialSSHWebSocket(ctx, "vm-123", query)
	if err != nil {
		t.Fatalf("DialSSHWebSocket failed: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	msgType, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("failed reading from ws: %v", err)
	}
	if msgType != websocket.MessageText || string(data) != "hello from ssh ws" {
		t.Fatalf("unexpected msg: %s", string(data))
	}
}
