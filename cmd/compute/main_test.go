package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Rashid2G/compute/internal/db"
)

func TestDaemonClientHTTPErrorResponses(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"bad request", http.StatusBadRequest, `{"error":"invalid request"}`, "daemon error: invalid request"},
		{"not found", http.StatusNotFound, `not found`, "daemon error (status 404): not found"},
		{"internal error", http.StatusInternalServerError, `{"error":"storage unavailable"}`, "daemon error: storage unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, tc.body, tc.status)
			}))
			defer srv.Close()
			_, err := NewDaemonClient(srv.URL).ListVMs(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ListVMs error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestDaemonClientConnectionFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	_, err = NewDaemonClient(addr).ListVMs(context.Background())
	if err == nil || !strings.Contains(err.Error(), "connection to daemon failed") {
		t.Fatalf("ListVMs error = %v, want connection failure", err)
	}
}

func TestDaemonClientGetConfigAndQueue(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/config", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"config_path":"/etc/compute/config.json","server":{"listen":"0.0.0.0:18080"},"defaults":{"vcpus":2,"memory_mb":1024}}`)
	})
	mux.HandleFunc("/api/v1/queue", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"count":1,"queued_vms":[{"position":1,"vm_id":"vm-123","vm_name":"queued-vm","reason":"host full"}]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := NewDaemonClient(srv.URL)
	cfg, err := client.GetConfig(context.Background())
	if err != nil {
		t.Fatalf("GetConfig error: %v", err)
	}
	if cfg.ConfigPath != "/etc/compute/config.json" {
		t.Fatalf("unexpected config path: %s", cfg.ConfigPath)
	}
	if cfg.Server.ListenAddr != "0.0.0.0:18080" || cfg.Defaults.VCPUs != 2 {
		t.Fatalf("unexpected config returned: %+v", cfg)
	}

	queue, err := client.GetQueue(context.Background())
	if err != nil {
		t.Fatalf("GetQueue error: %v", err)
	}
	if queue.Count != 1 || len(queue.QueuedVMs) != 1 || queue.QueuedVMs[0].VMID != "vm-123" {
		t.Fatalf("unexpected queue returned: %+v", queue)
	}
}

func TestQueuedVMReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"count":1,"queued_vms":[{"position":1,"vm_id":"vm-123","vm_name":"queued-vm","reason":"insufficient host memory"}]}`)
	}))
	defer srv.Close()

	client := NewDaemonClient(srv.URL)
	if got := queuedVMReason(context.Background(), client, "vm-123"); got != "insufficient host memory" {
		t.Fatalf("queuedVMReason = %q, want %q", got, "insufficient host memory")
	}
	if got := queuedVMReason(context.Background(), client, "vm-missing"); got != "" {
		t.Fatalf("queuedVMReason for unknown VM = %q, want empty", got)
	}

	srv.Close()
	if got := queuedVMReason(context.Background(), client, "vm-123"); got != "" {
		t.Fatalf("queuedVMReason after queue failure = %q, want empty", got)
	}
}

func TestCLIValidation(t *testing.T) {
	for _, args := range [][]string{{"sandbox", "start"}, {"sandbox", "delete"}, {"bogus"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", strings.Join(args, " "))
			cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1")
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("command unexpectedly succeeded: %s", out)
			}
			wants := "Usage: compute sandbox "
			if args[0] == "bogus" {
				wants = "Unknown command 'bogus'"
			}
			if !strings.Contains(string(out), wants) {
				t.Fatalf("output %q does not contain %q", out, wants)
			}
		})
	}
}

func TestNestedHelpDoesNotContactDaemon(t *testing.T) {
	for _, args := range []string{"jobs help", "jobs list --help", "sandbox create --help", "image delete -h", "vm"} {
		cmd := exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", args)
		cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON=127.0.0.1:1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s failed: %v, out: %s", args, err, out)
		}
		if !strings.Contains(string(out), "Usage:") {
			t.Fatalf("%s did not print command help: %s", args, out)
		}
		if args == "vm" && !strings.Contains(string(out), "Commands:") {
			t.Fatalf("%s did not print available subcommands: %s", args, out)
		}
	}
}

func TestTopLevelResourceCommandsShowHelpWithoutListing(t *testing.T) {
	commands := []string{
		"sandbox", "vm",
		"job", "jobs",
		"worker", "workers",
		"pool", "pools",
		"image", "images",
		"template", "templates",
	}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", command)
			cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON=127.0.0.1:1")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%s failed: %v, out: %s", command, err, out)
			}
			output := string(out)
			if !strings.Contains(output, "Usage:") || !strings.Contains(output, "Commands:") {
				t.Fatalf("%s did not print available subcommands: %s", command, output)
			}
			if strings.Contains(output, "connection to daemon failed") {
				t.Fatalf("%s contacted the daemon instead of showing help: %s", command, output)
			}
		})
	}
}

func TestCLIHelperProcess(t *testing.T) {
	if os.Getenv("COMPUTE_TEST_HELPER") != "1" {
		return
	}
	dashIdx := -1
	for i, arg := range os.Args {
		if arg == "--" {
			dashIdx = i
			break
		}
	}
	var targetArgs []string
	if dashIdx != -1 && dashIdx+1 < len(os.Args) {
		trailing := os.Args[dashIdx+1:]
		if len(trailing) == 1 {
			if trailing[0] != "" {
				targetArgs = strings.Split(trailing[0], " ")
			}
		} else {
			targetArgs = trailing
		}
	} else if len(os.Args) > 1 {
		raw := os.Args[len(os.Args)-1]
		if raw != "" {
			targetArgs = strings.Split(raw, " ")
		}
	}
	os.Args = append([]string{"compute"}, targetArgs...)
	main()
	os.Exit(0)
}

func TestListVMsTableFormatting(t *testing.T) {
	created := time.Date(2024, 3, 4, 5, 6, 7, 0, time.UTC)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vms", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `[{"id":"vm-1","name":"sample","status":"running","ip_address":"192.0.2.1","vcpus":2,"memory_mb":1024,"tap_device":"tap0","pid":42,"created_at":%q}]`, created.Format(time.RFC3339))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	// Exercise the same tabwriter formatting used by the list command using the server response.
	vms, err := NewDaemonClient(srv.URL).ListVMs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(vms) != 1 || vms[0].ID != "vm-1" {
		t.Fatalf("unexpected VMs: %+v", vms)
	}
	if vms[0].CreatedAt.UTC().Format("2006-01-02 15:04:05") != "2024-03-04 05:06:07" {
		t.Fatalf("unexpected timestamp: %v", vms[0].CreatedAt)
	}
	_ = db.StatusRunning
}

func TestMassRunAndLifecycleOperations(t *testing.T) {
	mux := http.NewServeMux()
	var runCalls []string
	var stopCalls []string
	var deleteCalls []string

	mux.HandleFunc("/api/v1/vms", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `[
			{"id":"web","name":"web","status":"stopped","ip_address":"172.16.0.2","pid":0},
			{"id":"db","name":"db","status":"created","ip_address":"172.16.0.3","pid":0},
			{"id":"cache","name":"cache","status":"running","ip_address":"172.16.0.4","pid":100}
		]`)
	})

	mux.HandleFunc("/api/v1/vms/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/vms/"), "/")
		vmTarget := parts[0]
		if len(parts) == 1 && r.Method == http.MethodDelete {
			deleteCalls = append(deleteCalls, vmTarget)
			fmt.Fprintf(w, `{"status":"deleted","id":%q}`, vmTarget)
			return
		}
		if len(parts) > 1 {
			action := parts[1]
			switch action {
			case "run", "start":
				runCalls = append(runCalls, vmTarget)
				fmt.Fprintf(w, `{"id":%q,"name":%q,"status":"running","ip_address":"172.16.0.10","pid":200}`, vmTarget, vmTarget)
				return
			case "stop":
				stopCalls = append(stopCalls, vmTarget)
				fmt.Fprintf(w, `{"id":%q,"name":%q,"status":"stopped"}`, vmTarget, vmTarget)
				return
			}
		}
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	// 1. Mass run explicit names
	cmd := exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "sandbox start web db")
	cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON="+srv.URL)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mass run failed: %v, out: %s", err, string(out))
	}
	if !strings.Contains(string(out), "Sandbox Started: ID=web") || !strings.Contains(string(out), "Sandbox Started: ID=db") {
		t.Fatalf("expected both VMs to start, got: %s", string(out))
	}

	// 2. Mass run --all
	cmd = exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "sandbox start web db")
	cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON="+srv.URL)
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mass run --all failed: %v, out: %s", err, string(out))
	}
	if !strings.Contains(string(out), "Sandbox Started: ID=web") || !strings.Contains(string(out), "Sandbox Started: ID=db") {
		t.Fatalf("expected stopped and created VMs to start with --all, got: %s", string(out))
	}

	// 3. Mass stop --all
	cmd = exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "sandbox stop web db")
	cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON="+srv.URL)
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mass stop --all failed: %v, out: %s", err, string(out))
	}
	if !strings.Contains(string(out), "Sandbox Stopped: ID=web") {
		t.Fatalf("expected running VM cache to stop with --all, got: %s", string(out))
	}

	// 4. Mass delete explicit names
	cmd = exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "sandbox delete web db")
	cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON="+srv.URL)
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mass delete failed: %v, out: %s", err, string(out))
	}
	if !strings.Contains(string(out), "Sandbox Deleted: ID=web") || !strings.Contains(string(out), "Sandbox Deleted: ID=db") {
		t.Fatalf("expected both VMs to be deleted, got: %s", string(out))
	}

	// 5. Shell-style wildcard matching (quote '*' in a shell to prevent local expansion).
	cmd = exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "sandbox delete *")
	cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON="+srv.URL)
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("wildcard delete failed: %v, out: %s", err, string(out))
	}
	if !strings.Contains(string(out), "Sandbox Deleted: ID=web") ||
		!strings.Contains(string(out), "Sandbox Deleted: ID=db") ||
		!strings.Contains(string(out), "Sandbox Deleted: ID=cache") {
		t.Fatalf("expected wildcard to delete all VMs, got: %s", out)
	}
}

func TestCompletionScripts(t *testing.T) {
	for _, sub := range []string{"bash", "zsh", "fish"} {
		cmd := exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "completion "+sub)
		cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("completion %s failed: %v, out: %s", sub, err, string(out))
		}
		str := string(out)
		switch sub {
		case "bash":
			if !strings.Contains(str, "_compute_completion") || !strings.Contains(str, "complete -F _compute_completion compute") {
				t.Fatalf("unexpected bash script: %s", str)
			}
		case "zsh":
			if !strings.Contains(str, "#compdef compute") {
				t.Fatalf("unexpected zsh script: %s", str)
			}
		case "fish":
			if !strings.Contains(str, "complete -c compute") {
				t.Fatalf("unexpected fish script: %s", str)
			}
		}
	}

	// Test autocomplete alias
	cmd := exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "autocomplete bash")
	cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "_compute_completion") {
		t.Fatalf("autocomplete alias failed: %v, out: %s", err, string(out))
	}

	// Test hidden __complete vms
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vms", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `[{"id":"my-web-app","name":"my-web-app","internal_id":"ins_12345"}]`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cmd = exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "__complete vms")
	cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON="+srv.URL)
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("__complete vms failed: %v, out: %s", err, string(out))
	}
	if !strings.Contains(string(out), "my-web-app") || !strings.Contains(string(out), "ins_12345") {
		t.Fatalf("expected VM name and internal ID in completions, got: %s", string(out))
	}

	mux.HandleFunc("/api/v1/images", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `[{"id":"img-1","name":"ubuntu-custom"}]`)
	})
	cmd = exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "__complete images")
	cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON="+srv.URL)
	out, err = cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "ubuntu-custom") {
		t.Fatalf("__complete images failed: %v, out: %s", err, string(out))
	}

	mux.HandleFunc("/api/v1/templates", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `[{"id":"tmpl-1","name":"node-starter"}]`)
	})
	cmd = exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "__complete templates")
	cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON="+srv.URL)
	out, err = cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "node-starter") {
		t.Fatalf("__complete templates failed: %v, out: %s", err, string(out))
	}

	mux.HandleFunc("/api/v1/pools", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `[{"id":"pool-1","name":"gpu-pool"}]`)
	})
	cmd = exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "__complete pools")
	cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON="+srv.URL)
	out, err = cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "gpu-pool") {
		t.Fatalf("__complete pools failed: %v, out: %s", err, string(out))
	}

	mux.HandleFunc("/api/v1/workers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `[{"id":"worker-1","name":"worker-alpha"}]`)
	})
	cmd = exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "__complete workers")
	cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON="+srv.URL)
	out, err = cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "worker-alpha") {
		t.Fatalf("__complete workers failed: %v, out: %s", err, string(out))
	}

	mux.HandleFunc("/api/v1/jobs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `[{"id":"job-xyz"}]`)
	})
	cmd = exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "__complete jobs")
	cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON="+srv.URL)
	out, err = cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "job-xyz") {
		t.Fatalf("__complete jobs failed: %v, out: %s", err, string(out))
	}

	mux.HandleFunc("/api/v1/keys", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `[{"id":"key-1","name":"my-laptop-key"}]`)
	})
	cmd = exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "__complete keys")
	cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON="+srv.URL)
	out, err = cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "my-laptop-key") {
		t.Fatalf("__complete keys failed: %v, out: %s", err, string(out))
	}
}

func TestImageAndTemplateCLI(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/images", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			fmt.Fprintln(w, `[
				{"id":"img-base","name":"ubuntu-24","kind":"base","status":"ready","size_mb":4096,"packages":["openssh-server","sudo"]},
				{"id":"img-custom","name":"python-stack","kind":"custom","status":"ready","size_mb":2048,"packages":["python3","pip"]}
			]`)
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintln(w, `{"id":"img-new","name":"node-stack","status":"ready","size_mb":2048,"packages":["nodejs","npm"]}`)
		}
	})

	mux.HandleFunc("/api/v1/images/python-stack", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			fmt.Fprintln(w, `{"id":"img-custom","name":"python-stack","kind":"custom","status":"ready","size_mb":2048,"packages":["python3","pip"]}`)
		case http.MethodDelete:
			fmt.Fprintln(w, `{"status":"deleted","id":"python-stack"}`)
		}
	})

	mux.HandleFunc("/api/v1/templates", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			fmt.Fprintln(w, `[
				{"id":"tpl-1","name":"web-template","source_vm_id":"vm-123","vcpus":2,"memory_mb":4096,"disk_mb":51200,"created_at":"2026-10-31T12:00:00Z"}
			]`)
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintln(w, `{"id":"tpl-2","name":"my-tpl","source_vm_id":"vm-456","vcpus":2,"memory_mb":4096,"disk_mb":51200,"created_at":"2026-10-31T12:00:00Z"}`)
		}
	})

	mux.HandleFunc("/api/v1/templates/web-template", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			fmt.Fprintln(w, `{"id":"tpl-1","name":"web-template","source_vm_id":"vm-123","disk_path":"/var/lib/compute/templates/web-template.ext4","vcpus":2,"memory_mb":4096,"disk_mb":51200,"created_at":"2026-10-31T12:00:00Z"}`)
		case http.MethodDelete:
			fmt.Fprintln(w, `{"status":"deleted","id":"web-template"}`)
		}
	})

	mux.HandleFunc("/api/v1/templates/web-template/launch", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintln(w, `{"id":"vm-launched","name":"launched-vm","status":"running","ip_address":"172.16.0.22","vcpus":2,"memory_mb":4096}`)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	// 1. Image list
	cmd := exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "image list")
	cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON="+srv.URL)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "python-stack") {
		t.Fatalf("image list failed: %v, out: %s", err, string(out))
	}

	// 2. Image inspect
	cmd = exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "image inspect python-stack")
	cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON="+srv.URL)
	out, err = cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "python3, pip") {
		t.Fatalf("image inspect failed: %v, out: %s", err, string(out))
	}

	// 3. Image build
	cmd = exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "image build --name node-stack --packages nodejs,npm")
	cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON="+srv.URL)
	out, err = cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Image Built Successfully") {
		t.Fatalf("image build failed: %v, out: %s", err, string(out))
	}

	// 4. Template list
	cmd = exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "template list")
	cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON="+srv.URL)
	out, err = cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "web-template") {
		t.Fatalf("template list failed: %v, out: %s", err, string(out))
	}

	// 5. Template create from VM
	cmd = exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "template create my-tpl --vm vm-456")
	cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON="+srv.URL)
	out, err = cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Template Created Successfully") {
		t.Fatalf("template create failed: %v, out: %s", err, string(out))
	}

	// 6. Template launch
	cmd = exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "template launch web-template --name launched-vm")
	cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON="+srv.URL)
	out, err = cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Sandbox Launched from template") {
		t.Fatalf("template launch failed: %v, out: %s", err, string(out))
	}

	// 7. Template delete
	cmd = exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "template delete web-template")
	cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON="+srv.URL)
	out, err = cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Template Deleted") {
		t.Fatalf("template delete failed: %v, out: %s", err, string(out))
	}
}

func TestBoatStyleCLICommands(t *testing.T) {
	mux := http.NewServeMux()

	// Sandboxes (vms)
	mux.HandleFunc("/api/v1/vms", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `[{"id":"sbx-1","name":"web-sandbox","status":"running","ip_address":"172.16.0.10","vcpus":2,"memory_mb":2048}]`)
	})

	// Workers
	mux.HandleFunc("/api/v1/workers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `[{"id":"nod-1","name":"worker-alpha","pool_id":"default","role":"worker","status":"ready","arch":"amd64","total_vcpus":8,"total_mem_mb":16384}]`)
	})

	// Worker tokens
	mux.HandleFunc("/api/v1/worker-tokens", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintln(w, `{"token":"mft_test_token_12345","pool_id":"default","expires_at":1893456000}`)
	})

	// Pools
	mux.HandleFunc("/api/v1/pools", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `[{"id":"pool-1","name":"default","created_at":"2026-10-31T12:00:00Z"}]`)
	})

	// Jobs
	mux.HandleFunc("/api/v1/jobs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			fmt.Fprintln(w, `[{"id":"job-101","type":"command","pool_id":"default","state":"succeeded","priority":10,"queued_at":"2026-10-31T12:00:00Z"}]`)
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintln(w, `{"id":"job-102","type":"command","pool_id":"default","state":"queued","priority":10,"queued_at":"2026-10-31T12:00:00Z"}`)
		}
	})

	// Exec
	mux.HandleFunc("/api/v1/vms/sbx-1/exec", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"stdout":"Linux build machine feedback\n","stderr":"","exit_code":0,"duration_ms":42}`)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	// 1. compute sandbox list
	cmd := exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "sandbox list")
	cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON="+srv.URL)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "web-sandbox") {
		t.Fatalf("sandbox list failed: %v, out: %s", err, string(out))
	}

	// 2. compute vm alias
	cmd = exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "vm list")
	cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON="+srv.URL)
	out, err = cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "web-sandbox") {
		t.Fatalf("vm alias list failed: %v, out: %s", err, string(out))
	}

	// 3. compute worker list
	cmd = exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "worker list")
	cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON="+srv.URL)
	out, err = cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "worker-alpha") {
		t.Fatalf("worker list failed: %v, out: %s", err, string(out))
	}

	// 4. compute worker token create
	cmd = exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "worker token create")
	cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON="+srv.URL)
	out, err = cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "mft_test_token_12345") {
		t.Fatalf("worker token create failed: %v, out: %s", err, string(out))
	}

	// 5. compute pool list
	cmd = exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "pool list")
	cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON="+srv.URL)
	out, err = cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "default") {
		t.Fatalf("pool list failed: %v, out: %s", err, string(out))
	}

	// 6. compute job list
	cmd = exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "job list")
	cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON="+srv.URL)
	out, err = cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "job-101") {
		t.Fatalf("job list failed: %v, out: %s", err, string(out))
	}

	// 7. compute exec top-level
	cmd = exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "exec --no-stream sbx-1 -- echo hello")
	cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON="+srv.URL)
	out, err = cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Linux build machine feedback") {
		t.Fatalf("compute exec failed: %v, out: %s", err, string(out))
	}
}

func TestVersionCLI(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"name":"Compute Daemon","service":"computed","project":"Compute","version":"1.1.0","release_name":"Compute","go_version":"go1.23","os":"linux","arch":"amd64"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// 1. compute version --client
	t.Run("client_only", func(t *testing.T) {
		cmd := exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "version --client")
		cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("version --client failed: %v, out: %s", err, string(out))
		}
		outStr := string(out)
		if !strings.Contains(outStr, "Compute CLI") || !strings.Contains(outStr, "1.1.0") || !strings.Contains(outStr, "Compute") {
			t.Fatalf("unexpected version output: %s", outStr)
		}
	})

	// 2. compute version --short
	t.Run("short", func(t *testing.T) {
		cmd := exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "version --short")
		cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("version --short failed: %v, out: %s", err, string(out))
		}
		if strings.TrimSpace(string(out)) != "1.1.0" {
			t.Fatalf("expected 1.1.0, got %q", string(out))
		}
	})

	// 3. compute version --json
	t.Run("json", func(t *testing.T) {
		cmd := exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "version --json")
		cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON="+srv.URL)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("version --json failed: %v, out: %s", err, string(out))
		}
		var res map[string]interface{}
		if err := json.Unmarshal(out, &res); err != nil {
			t.Fatalf("failed to parse JSON: %v, out: %s", err, string(out))
		}
		if res["client"] == nil || res["daemon"] == nil {
			t.Fatalf("expected both client and daemon in json output: %s", string(out))
		}
	})

	// 4. compute version (with running daemon)
	t.Run("full_connected", func(t *testing.T) {
		cmd := exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "version")
		cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON="+srv.URL)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("version failed: %v, out: %s", err, string(out))
		}
		outStr := string(out)
		if !strings.Contains(outStr, "Compute CLI") || !strings.Contains(outStr, "Compute Daemon") {
			t.Fatalf("expected both client and daemon info: %s", outStr)
		}
	})

	// 5. compute -v and --version aliases
	t.Run("aliases", func(t *testing.T) {
		for _, alias := range []string{"-v", "--version"} {
			cmd := exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", alias)
			cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON="+srv.URL)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%s failed: %v, out: %s", alias, err, string(out))
			}
			if !strings.Contains(string(out), "Compute CLI") {
				t.Fatalf("%s output missing Compute CLI: %s", alias, string(out))
			}
		}
	})

	// 6. compute version when daemon is unreachable
	t.Run("daemon_unreachable", func(t *testing.T) {
		cmd := exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "version")
		cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON=127.0.0.1:59999")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("version with unreachable daemon should not return exit error: %v, out: %s", err, string(out))
		}
		outStr := string(out)
		if !strings.Contains(outStr, "Compute CLI") || !strings.Contains(outStr, "unreachable") {
			t.Fatalf("unexpected unreachable output: %s", outStr)
		}
	})
}

func TestCLIErrorExplanationAndAvailableCommands(t *testing.T) {
	tests := []struct {
		name      string
		args      string
		wantError string
		wantHelp  string
	}{
		{
			name:      "unknown_top_level_command",
			args:      "boguscmd",
			wantError: "Error: Unknown command 'boguscmd'.",
			wantHelp:  "Usage: compute <command> [subcommand] [options]",
		},
		{
			name:      "no_args_provided",
			args:      "",
			wantError: "Error: no command specified.",
			wantHelp:  "Core Commands:",
		},
		{
			name:      "unknown_sandbox_subcommand",
			args:      "sandbox invalidsub",
			wantError: "Error: unknown sandbox command 'invalidsub'.",
			wantHelp:  "Manage isolated Firecracker sandboxes.",
		},
		{
			name:      "unknown_job_subcommand",
			args:      "job invalidsub",
			wantError: "Error: unknown job command 'invalidsub'.",
			wantHelp:  "Run and monitor distributed jobs.",
		},
		{
			name:      "unknown_worker_subcommand",
			args:      "worker invalidsub",
			wantError: "Error: unknown worker command 'invalidsub'.",
			wantHelp:  "Manage worker nodes and enrollment.",
		},
		{
			name:      "unknown_pool_subcommand",
			args:      "pool invalidsub",
			wantError: "Error: unknown pool command 'invalidsub'.",
			wantHelp:  "Manage worker pools.",
		},
		{
			name:      "unknown_image_subcommand",
			args:      "image invalidsub",
			wantError: "Error: unknown image command 'invalidsub'.",
			wantHelp:  "Manage base and custom rootfs images.",
		},
		{
			name:      "unknown_template_subcommand",
			args:      "template invalidsub",
			wantError: "Error: unknown template command 'invalidsub'.",
			wantHelp:  "Manage reusable VM disk templates.",
		},
		{
			name:      "unsupported_shell_completion",
			args:      "completion powershell",
			wantError: "Error: unsupported shell 'powershell'.",
			wantHelp:  "Generate shell autocompletion script.",
		},
		{
			name:      "missing_sandbox_start_arg",
			args:      "sandbox start",
			wantError: "Error: missing required argument for 'sandbox start'.",
			wantHelp:  "Manage isolated Firecracker sandboxes.",
		},
		{
			name:      "missing_job_run_cmd",
			args:      "job run",
			wantError: "Error: no command specified for job.",
			wantHelp:  "Run and monitor distributed jobs.",
		},
		{
			name:      "missing_pool_create_arg",
			args:      "pool create",
			wantError: "Error: missing required pool name.",
			wantHelp:  "Manage worker pools.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", tc.args)
			cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1")
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("expected error exit, got success: %s", out)
			}
			outStr := string(out)
			if !strings.Contains(outStr, tc.wantError) {
				t.Errorf("output does not contain error line %q: %s", tc.wantError, outStr)
			}
			if !strings.Contains(outStr, tc.wantHelp) {
				t.Errorf("output does not contain command help %q: %s", tc.wantHelp, outStr)
			}
		})
	}
}

func TestBuildExecCommandQuoting(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected string
	}{
		{
			name:     "empty",
			input:    []string{},
			expected: "",
		},
		{
			name:     "single string preserved as raw command",
			input:    []string{"mkdir -p /foo/bar && echo done"},
			expected: "mkdir -p /foo/bar && echo done",
		},
		{
			name:     "compound bash -c preserves payload intact",
			input:    []string{"bash", "-c", "mkdir -p /path && cmd"},
			expected: "bash -c 'mkdir -p /path && cmd'",
		},
		{
			name:     "python -c with single quotes",
			input:    []string{"python3", "-c", "print('hello world')"},
			expected: "python3 -c 'print('\\''hello world'\\'')'",
		},
		{
			name:     "simple command and safe flags",
			input:    []string{"ls", "-la", "/var/log"},
			expected: "ls -la /var/log",
		},
		{
			name:     "argument with spaces",
			input:    []string{"touch", "file with spaces.txt"},
			expected: "touch 'file with spaces.txt'",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildExecCommand(tc.input)
			if got != tc.expected {
				t.Fatalf("expected %q, got %q", tc.expected, got)
			}
		})
	}
}

func TestSandboxHelpListsDiscoveryAndStartFlags(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "sandbox")
	cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON=127.0.0.1:1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox help failed: %v: %s", err, out)
	}
	outStr := string(out)
	for _, expected := range []string{
		"images",
		"templates",
		"--start/--boot",
		"--startup-script",
	} {
		if !strings.Contains(outStr, expected) {
			t.Errorf("sandbox help missing expected term %q:\n%s", expected, outStr)
		}
	}
}

func TestSandboxCreateStartupScriptCLI(t *testing.T) {
	var receivedScript string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vms", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var req map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			if s, ok := req["startup_script"].(string); ok {
				receivedScript = s
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintln(w, `{"id":"sbx-test","name":"sbx-test","status":"created","vcpus":1,"memory_mb":512,"ip_address":"172.16.0.5","startup_script":"`+strings.ReplaceAll(receivedScript, "\n", "\\n")+`"}`)
			return
		}
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// 1. Inline startup script
	inlineScript := "#!/bin/bash\necho inline-ok\n"
	cmd := exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "sandbox", "create", "--name", "sbx-test", "--startup-script", inlineScript)
	cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON="+srv.URL)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("create failed: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "Startup script:  configured") {
		t.Fatalf("expected startup script configured in output, got: %s", out)
	}
	if receivedScript != inlineScript {
		t.Fatalf("daemon did not receive inline script: got %q, want %q", receivedScript, inlineScript)
	}

	// 2. Startup script from file
	tmpFile, err := os.CreateTemp("", "startup-*.sh")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	fileScript := "#!/bin/bash\napt-get update\n"
	_ = os.WriteFile(tmpFile.Name(), []byte(fileScript), 0644)

	cmd2 := exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", "sandbox", "create", "--name", "sbx-test", "--startup-script-file", tmpFile.Name())
	cmd2.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON="+srv.URL)
	out2, err := cmd2.CombinedOutput()
	if err != nil {
		t.Fatalf("create from file failed: %v: %s", err, out2)
	}
	if receivedScript != fileScript {
		t.Fatalf("daemon did not receive file script: got %q, want %q", receivedScript, fileScript)
	}
}
