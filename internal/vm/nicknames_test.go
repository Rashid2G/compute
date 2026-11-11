package vm

import (
	"strings"
	"testing"
)

func TestSandboxNicknamesList(t *testing.T) {
	if len(SandboxNicknames) != 100 {
		t.Fatalf("expected exactly 100 sandbox nicknames, got %d", len(SandboxNicknames))
	}

	seen := make(map[string]bool)
	expectedExamples := []string{"doom", "runner", "pusher", "trove", "neutron", "punisher"}

	for _, name := range SandboxNicknames {
		if strings.TrimSpace(name) == "" {
			t.Fatalf("empty nickname found")
		}
		if seen[name] {
			t.Fatalf("duplicate nickname found: %s", name)
		}
		seen[name] = true
	}

	for _, exp := range expectedExamples {
		if !seen[exp] {
			t.Fatalf("expected required nickname %q not found in SandboxNicknames", exp)
		}
	}

	// Test RandomNickname produces a valid nickname from the list
	for i := 0; i < 50; i++ {
		rnd := RandomNickname()
		if !seen[rnd] {
			t.Fatalf("RandomNickname() returned unexpected name %q", rnd)
		}
	}
}
