package scheduler

import (
	"context"
	"fmt"
	"testing"

	"github.com/Rashid2G/compute/internal/db"
)

func BenchmarkSelectNodeForSandbox50Nodes(b *testing.B) {
	ctx := context.Background()
	policy := db.DefaultSchedulingPolicy()
	policy.Strategy = "least_loaded"
	policy.CPUHardOvercommitRatio = 2.0

	nodes := make([]*db.WorkerNode, 0, 50)
	for i := 0; i < 50; i++ {
		nodes = append(nodes, &db.WorkerNode{
			ID:              fmt.Sprintf("wrk-%02d", i),
			Name:            fmt.Sprintf("worker-%02d", i),
			PoolID:          "default",
			Status:          "ready",
			AllocatedVCPUs:  16,
			AllocatedMemMB:  32768,
			AllocatedDiskMB: 256000,
			UsedVCPUs:       i % 8,
			UsedMemMB:       (i % 8) * 2048,
			UsedDiskMB:      (i % 8) * 10240,
			RunningVMs:      i % 8,
			LastHeartbeat:   nil,
		})
	}
	req := SandboxPlacementRequest{VCPUs: 2, MemoryMB: 2048, DiskMB: 10240}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := SelectNodeForSandbox(ctx, nodes, nil, req, policy); err != nil {
			b.Fatal(err)
		}
	}
}
