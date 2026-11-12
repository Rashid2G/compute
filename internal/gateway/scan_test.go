package gateway

import "testing"

func TestParseListeningPortsSS(t *testing.T) {
	raw := `LISTEN 0      4096         0.0.0.0:22           0.0.0.0:*
LISTEN 0      4096            [::]:22              [::]:*
LISTEN 0      511          127.0.0.1:631          0.0.0.0:*
LISTEN 0      511            0.0.0.0:3000         0.0.0.0:*
LISTEN 0      511        172.16.0.42:8080        0.0.0.0:*
`
	got := ParseListeningPorts(raw, "172.16.0.42")
	want := []int{22, 3000, 8080}
	if len(got) != len(want) {
		t.Fatalf("ports = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ports = %v, want %v", got, want)
		}
	}
}

func TestParseListeningPortsNetstat(t *testing.T) {
	raw := `Active Internet connections (only servers)
Proto Recv-Q Send-Q Local Address           Foreign Address         State
tcp        0      0 0.0.0.0:22              0.0.0.0:*               LISTEN
tcp        0      0 127.0.0.1:25            0.0.0.0:*               LISTEN
tcp6       0      0 :::443                  :::*                    LISTEN
`
	got := ParseListeningPorts(raw, "")
	if len(got) != 2 || got[0] != 22 || got[1] != 443 {
		t.Fatalf("ports = %v, want [22 443]", got)
	}
}

func TestParseListeningPortsIgnoresLoopbackOnly(t *testing.T) {
	got := ParseListeningPorts("LISTEN 0 4096 127.0.0.1:8080 0.0.0.0:*", "")
	if len(got) != 0 {
		t.Fatalf("loopback-only listener was exposed: %v", got)
	}
}

func TestParseListeningPortsExposeLoopback(t *testing.T) {
	got := ParseListeningPortsWith("LISTEN 0 4096 127.0.0.1:8080 0.0.0.0:*", "", true)
	if len(got) != 1 || got[0] != 8080 {
		t.Fatalf("loopback listener not exposed when configured: %v", got)
	}
}
