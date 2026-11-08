package network

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type recordingRunner struct {
	commands [][]string
	missing  bool // when true, iptables -C reports "rule does not exist"
}

func (r *recordingRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := append([]string{name}, args...)
	r.commands = append(r.commands, cmd)
	if r.missing && contains(args, "-C") {
		return []byte("rule not found"), errors.New("exit status 1")
	}
	return nil, nil
}

func contains(items []string, needle string) bool {
	for _, item := range items {
		if item == needle {
			return true
		}
	}
	return false
}

func TestAddPortForwardInstallsDNATAndForwardRules(t *testing.T) {
	runner := &recordingRunner{missing: true}
	mgr, err := NewManager(Config{}, runner)
	if err != nil {
		t.Fatal(err)
	}

	if err := mgr.AddPortForward(context.Background(), "tcp", 19022, "172.16.0.2", 22); err != nil {
		t.Fatalf("AddPortForward: %v", err)
	}

	want := []string{
		"-t nat -A PREROUTING -p tcp --dport 19022 -j DNAT --to-destination 172.16.0.2:22",
		"-t nat -A OUTPUT -p tcp --dport 19022 -j DNAT --to-destination 172.16.0.2:22",
		"-A FORWARD -p tcp -d 172.16.0.2 --dport 22 -j ACCEPT",
	}
	for _, w := range want {
		found := false
		for _, cmd := range runner.commands {
			if strings.Join(cmd, " ") == "iptables "+w {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected iptables command %q; got %v", w, runner.commands)
		}
	}

	runner.commands = nil
	if err := mgr.RemovePortForward(context.Background(), "tcp", 19022, "172.16.0.2", 22); err != nil {
		t.Fatalf("RemovePortForward: %v", err)
	}
	joined := ""
	for _, cmd := range runner.commands {
		joined += strings.Join(cmd, " ") + "\n"
	}
	for _, wantDelete := range []string{
		"-t nat -D PREROUTING -p tcp --dport 19022",
		"-t nat -D OUTPUT -p tcp --dport 19022",
		"-D FORWARD -p tcp -d 172.16.0.2 --dport 22",
	} {
		if !strings.Contains(joined, wantDelete) {
			t.Fatalf("expected delete command %q in %q", wantDelete, joined)
		}
	}
}

func TestAddPortForwardRejectsInvalidInput(t *testing.T) {
	runner := &recordingRunner{}
	mgr, err := NewManager(Config{}, runner)
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.AddPortForward(context.Background(), "sctp", 1, "172.16.0.2", 22); err == nil {
		t.Fatal("expected unsupported protocol error")
	}
	if err := mgr.AddPortForward(context.Background(), "tcp", 0, "172.16.0.2", 22); err == nil {
		t.Fatal("expected invalid port error")
	}
	if err := mgr.AddPortForward(context.Background(), "tcp", 80, "not-an-ip", 22); err == nil {
		t.Fatal("expected invalid IP error")
	}
}
