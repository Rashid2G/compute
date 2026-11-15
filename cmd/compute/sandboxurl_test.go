package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Rashid2G/compute/internal/db"
)

const (
	testDelegatedHostname = "spark.alice.sandbox.example.com"
)

func TestSandboxAccessSSHPrecedence(t *testing.T) {
	cases := []struct {
		name        string
		vm          *db.VM
		wantHost    string
		wantKind    string
		resolvable  bool
		noteContain string
	}{
		{
			name: "hostname with delegated zone",
			vm: &db.VM{
				ID: "vm-1", Name: "spark", IPAddress: "172.16.0.2",
				Hostname: testDelegatedHostname,
			},
			wantHost:    testDelegatedHostname,
			wantKind:    SSHHostHostname,
			resolvable:  true,
			noteContain: "delegated",
		},
		{
			name: "public IP before guest IP",
			vm: &db.VM{
				ID: "vm-1", Name: "spark", IPAddress: "172.16.0.2", PublicIP: "198.51.100.1",
			},
			wantHost:   "198.51.100.1",
			wantKind:   SSHHostPublicIP,
			resolvable: true,
		},
		{
			name:       "guest IP last resort",
			vm:         &db.VM{ID: "vm-1", Name: "spark", IPAddress: "172.16.0.2"},
			wantHost:   "172.16.0.2",
			wantKind:   SSHHostIP,
			resolvable: true,
		},
		{
			name:       "no address",
			vm:         &db.VM{ID: "vm-1", Name: "spark"},
			wantKind:   SSHHostNone,
			resolvable: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			acc := sandboxAccessForVM(tc.vm, "compute", 0)
			if acc.SSHHost != tc.wantHost {
				t.Fatalf("SSHHost = %q, want %q", acc.SSHHost, tc.wantHost)
			}
			if acc.SSHHostKind != tc.wantKind {
				t.Fatalf("SSHHostKind = %q, want %q", acc.SSHHostKind, tc.wantKind)
			}
			if acc.SSHResolvable != tc.resolvable {
				t.Fatalf("SSHResolvable = %v, want %v (note: %s)", acc.SSHResolvable, tc.resolvable, acc.SSHNote)
			}
			if tc.wantHost != "" && acc.SSH != "ssh compute@"+tc.wantHost {
				t.Fatalf("SSH = %q", acc.SSH)
			}
			if tc.noteContain != "" && !strings.Contains(acc.SSHNote, tc.noteContain) {
				t.Fatalf("SSHNote = %q, want containing %q", acc.SSHNote, tc.noteContain)
			}
		})
	}
}

func TestSandboxAccessCustomSSHUser(t *testing.T) {
	acc := sandboxAccessForVM(&db.VM{Name: "spark", Hostname: testDelegatedHostname}, "root", 0)
	if acc.SSH != "ssh root@"+testDelegatedHostname {
		t.Fatalf("SSH = %q", acc.SSH)
	}
}

func TestSandboxAccessURL(t *testing.T) {
	t.Run("delegated zone publishes the hostname URL", func(t *testing.T) {
		acc := sandboxAccessForVM(&db.VM{Name: "spark", Hostname: testDelegatedHostname}, "compute", 0)
		if !acc.URLResolvable {
			t.Fatalf("URLResolvable = false, note: %s", acc.URLNote)
		}
		if acc.URL != "http://"+testDelegatedHostname+"/" {
			t.Fatalf("URL = %q", acc.URL)
		}
		if acc.URLKind != URLKindHostname {
			t.Fatalf("URLKind = %q", acc.URLKind)
		}
		if !strings.Contains(acc.URLNote, "delegated") {
			t.Fatalf("URLNote = %q", acc.URLNote)
		}
	})

	t.Run("no hostname explains how to enable ingress", func(t *testing.T) {
		acc := sandboxAccessForVM(&db.VM{Name: "spark", IPAddress: "172.16.0.2"}, "compute", 0)
		if acc.URL != "" || acc.URLResolvable {
			t.Fatalf("unexpected URL %q (resolvable=%v)", acc.URL, acc.URLResolvable)
		}
		if !strings.Contains(acc.URLNote, "No sandbox DNS name") {
			t.Fatalf("URLNote = %q", acc.URLNote)
		}
	})

	t.Run("hostname is normalized", func(t *testing.T) {
		acc := sandboxAccessForVM(&db.VM{Name: "spark", Hostname: " Spark.Alice.Sandbox.Example.COM. "}, "compute", 0)
		if acc.URL != "http://spark.alice.sandbox.example.com/" {
			t.Fatalf("URL = %q", acc.URL)
		}
		if acc.Hostname != "Spark.Alice.Sandbox.Example.COM" {
			t.Fatalf("Hostname = %q", acc.Hostname)
		}
	})
}

func TestSandboxAccessPortURLs(t *testing.T) {
	t.Run("tenant label is kept", func(t *testing.T) {
		acc := sandboxAccessForVM(&db.VM{Name: "spark", Hostname: testDelegatedHostname}, "compute", 8080)
		if acc.PortURL != "http://8080-spark.alice.sandbox.example.com/" {
			t.Fatalf("PortURL = %q", acc.PortURL)
		}
		if acc.LegacyPortURL != "" {
			t.Fatalf("LegacyPortURL = %q, want empty for a named tenant", acc.LegacyPortURL)
		}
		if acc.URLKind != URLKindPort {
			t.Fatalf("URLKind = %q", acc.URLKind)
		}
	})

	t.Run("legacy default tenant keeps the single-label form", func(t *testing.T) {
		acc := sandboxAccessForVM(&db.VM{Name: "spark", Hostname: "spark.default.sandbox.example.com"}, "compute", 3000)
		if acc.PortURL != "http://3000-spark.default.sandbox.example.com/" {
			t.Fatalf("PortURL = %q", acc.PortURL)
		}
		if acc.LegacyPortURL != "http://3000-spark.sandbox.example.com/" {
			t.Fatalf("LegacyPortURL = %q", acc.LegacyPortURL)
		}
	})

	t.Run("legacy two-label hostname", func(t *testing.T) {
		acc := sandboxAccessForVM(&db.VM{Name: "spark", Hostname: "spark.sandbox.example.com"}, "compute", 8080)
		if acc.PortURL != "http://8080-spark.sandbox.example.com/" {
			t.Fatalf("PortURL = %q", acc.PortURL)
		}
		if acc.LegacyPortURL != "" {
			t.Fatalf("LegacyPortURL = %q", acc.LegacyPortURL)
		}
	})

	t.Run("no port requested", func(t *testing.T) {
		acc := sandboxAccessForVM(&db.VM{Name: "spark", Hostname: testDelegatedHostname}, "compute", 0)
		if acc.PortURL != "" {
			t.Fatalf("PortURL = %q, want empty", acc.PortURL)
		}
	})
}

func TestSandboxAccessLines(t *testing.T) {
	acc := sandboxAccessForVM(&db.VM{
		ID: "vm-1", Name: "spark", IPAddress: "172.16.0.2",
		Hostname: testDelegatedHostname,
	}, "compute", 0)
	lines := strings.Join(sandboxAccessLines(acc), "\n")
	for _, want := range []string{
		"Hostname:",
		testDelegatedHostname,
		"SSH:",
		"ssh compute@" + testDelegatedHostname,
		"URL:",
		"CLI:",
		"compute ssh spark",
	} {
		if !strings.Contains(lines, want) {
			t.Fatalf("access lines missing %q:\n%s", want, lines)
		}
	}
}

func TestWriteSandboxListAddsURLOrSSHColumn(t *testing.T) {
	vms := []*db.VM{
		{ID: "vm-1", Name: "spark", Status: db.StatusRunning, VCPUs: 2, MemoryMB: 1024, IPAddress: "172.16.0.2", Hostname: testDelegatedHostname},
		{ID: "vm-2", Name: "plain", Status: db.StatusStopped, VCPUs: 1, MemoryMB: 512, IPAddress: "172.16.0.3"},
	}
	var buf bytes.Buffer
	writeSandboxList(&buf, vms)
	out := buf.String()

	if !strings.Contains(out, "URL / SSH NAME") {
		t.Fatalf("header missing URL column:\n%s", out)
	}
	if !strings.Contains(out, "http://"+testDelegatedHostname+"/") {
		t.Fatalf("row missing URL:\n%s", out)
	}
	if !strings.Contains(out, "172.16.0.3") {
		t.Fatalf("row missing SSH/IP fallback:\n%s", out)
	}
	// Existing columns keep their order.
	if !strings.HasPrefix(strings.TrimSpace(strings.Split(out, "\n")[0]), "ID") {
		t.Fatalf("unexpected header:\n%s", out)
	}
}

func TestMatchSandboxReference(t *testing.T) {
	vms := []*db.VM{
		{
			ID: "vm-1", Name: "spark", InternalID: "ins_1",
			Hostname: testDelegatedHostname,
			PublicIP: "198.51.100.1", IPAddress: "172.16.0.2",
		},
		{ID: "vm-2", Name: "other"},
	}
	cases := []struct {
		ref  string
		want string
	}{
		{"vm-1", "vm-1"},
		{"spark", "vm-1"},
		{"ins_1", "vm-1"},
		{testDelegatedHostname, "vm-1"},
		{strings.ToUpper(testDelegatedHostname) + ".", "vm-1"},
		{"198.51.100.1", "vm-1"},
		{"172.16.0.2", "vm-1"},
		{"other", "vm-2"},
		{"missing", ""},
	}
	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			got := matchSandboxReference(vms, tc.ref)
			if tc.want == "" {
				if got != nil {
					t.Fatalf("matchSandboxReference(%q) = %+v, want nil", tc.ref, got)
				}
				return
			}
			if got == nil || got.ID != tc.want {
				t.Fatalf("matchSandboxReference(%q) = %+v, want ID %q", tc.ref, got, tc.want)
			}
		})
	}
}

func TestURLCommandPrintsURLAndResolvesHostname(t *testing.T) {
	created := time.Date(2024, 3, 4, 5, 6, 7, 0, time.UTC)
	mux := http.NewServeMux()
	vmJSON := fmt.Sprintf(`{"id":"vm-1","name":"spark","status":"running","ip_address":"172.16.0.2","hostname":%q,"vcpus":2,"memory_mb":1024,"created_at":%q}`,
		testDelegatedHostname, created.Format(time.RFC3339))
	mux.HandleFunc("/api/v1/vms", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, "[%s]", vmJSON)
	})
	mux.HandleFunc("/api/v1/vms/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.TrimPrefix(r.URL.Path, "/api/v1/vms/") == "vm-1" || strings.TrimPrefix(r.URL.Path, "/api/v1/vms/") == "spark" {
			fmt.Fprintln(w, vmJSON)
			return
		}
		http.Error(w, `{"error":"sandbox not found"}`, http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	run := func(args string) (string, string) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", args)
		cmd.Env = append(os.Environ(), "COMPUTE_TEST_HELPER=1", "COMPUTE_DAEMON="+srv.URL)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s failed: %v\nstdout: %s\nstderr: %s", args, err, stdout.String(), stderr.String())
		}
		return stdout.String(), stderr.String()
	}

	stdout, _ := run("url spark")
	if strings.TrimSpace(stdout) != "http://"+testDelegatedHostname+"/" {
		t.Fatalf("url output = %q", stdout)
	}

	// A sandbox hostname resolves through the list fallback even though the
	// daemon's own lookup only understands IDs and names.
	stdout, _ = run("url " + testDelegatedHostname + " --json")
	var acc SandboxAccess
	if err := json.Unmarshal([]byte(stdout), &acc); err != nil {
		t.Fatalf("--json output is not SandboxAccess: %v\n%s", err, stdout)
	}
	if acc.URL != "http://"+testDelegatedHostname+"/" || acc.Sandbox != "spark" {
		t.Fatalf("unexpected JSON access model: %+v", acc)
	}

	stdout, _ = run("url spark --port 8080 --json")
	if err := json.Unmarshal([]byte(stdout), &acc); err != nil {
		t.Fatalf("--json output is not SandboxAccess: %v\n%s", err, stdout)
	}
	if acc.PortURL != "http://8080-spark.alice.sandbox.example.com/" {
		t.Fatalf("PortURL = %q", acc.PortURL)
	}
}

func TestResolveSandboxReferenceUsesDaemonIDLookup(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vms/vm-1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id":"vm-1","name":"spark","status":"running"}`)
	})
	mux.HandleFunc("/api/v1/vms", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `[{"id":"vm-1","name":"spark","status":"running","hostname":"spark.alice.sandbox.example.com"}]`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := NewDaemonClient(srv.URL)
	v, err := resolveSandboxReference(context.Background(), client, "vm-1")
	if err != nil || v == nil || v.ID != "vm-1" {
		t.Fatalf("resolve vm-1 = %+v, %v", v, err)
	}
	v, err = resolveSandboxReference(context.Background(), client, "spark.alice.sandbox.example.com")
	if err != nil || v == nil || v.ID != "vm-1" {
		t.Fatalf("resolve hostname = %+v, %v", v, err)
	}
	if _, err := resolveSandboxReference(context.Background(), client, "missing"); err == nil {
		t.Fatalf("expected an error for an unknown reference")
	}
}
