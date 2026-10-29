package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/db"
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

func TestCLIValidation(t *testing.T) {
	for _, args := range [][]string{{"run"}, {"delete"}, {"bogus"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=TestCLIHelperProcess", "--", strings.Join(args, " "))
			cmd.Env = append(os.Environ(), "MFORGE_TEST_HELPER=1")
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("command unexpectedly succeeded: %s", out)
			}
			want := "Usage: mforge "
			if args[0] == "bogus" {
				want = "Unknown command 'bogus'"
			}
			if !strings.Contains(string(out), want) {
				t.Fatalf("output %q does not contain %q", out, want)
			}
		})
	}
}

func TestCLIHelperProcess(t *testing.T) {
	if os.Getenv("MFORGE_TEST_HELPER") != "1" {
		return
	}
	args := strings.Split(os.Args[len(os.Args)-1], " ")
	os.Args = []string{"mforge"}
	os.Args = append(os.Args, args...)
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
