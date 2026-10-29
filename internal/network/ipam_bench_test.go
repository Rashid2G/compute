package network

import (
	"fmt"
	"testing"
)

func BenchmarkAllocateIP(b *testing.B) {
	ipam, err := NewIPAM("172.16.0.0/16")
	if err != nil {
		b.Fatalf("failed to create IPAM: %v", err)
	}

	used := make(map[string]bool)
	for i := 2; i < 50; i++ {
		used[fmt.Sprintf("172.16.0.%d", i)] = true
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, err := ipam.AllocateNextIP(used)
		if err != nil {
			b.Fatalf("AllocateNextIP failed: %v", err)
		}
	}
}
