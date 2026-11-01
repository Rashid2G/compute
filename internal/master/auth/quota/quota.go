package quota

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Rashid2G/on-demand-vms/internal/store/db"
	"github.com/Rashid2G/on-demand-vms/pkg/api"
)

// Quota defines platform resource limits.
type Quota struct {
	Instances        int `json:"instances"`
	RunningInstances int `json:"running_instances"`
	VCPUs            int `json:"vcpus"`
	MemoryMB         int `json:"memory_mb"`
	DiskGB           int `json:"disk_gb"`
	VPCs             int `json:"vpcs"`
	APIKeys          int `json:"api_keys"`
}

// DefaultQuota returns standard platform limits.
func DefaultQuota() Quota {
	return Quota{
		Instances:        10,
		RunningInstances: 5,
		VCPUs:            16,
		MemoryMB:         32768,
		DiskGB:           500,
		VPCs:             5,
		APIKeys:          20,
	}
}

// CheckQuota enforces quota ceilings inside writer transaction.
// Admins bypass all quota checks.
func CheckQuota(ctx context.Context, queries *db.Queries, user *db.User, requestedInstances, requestedVCPUs, requestedMemoryMB, requestedDiskMB, requestedVPCs, requestedAPIKeys int, traceID string) error {
	if user.Role == "admin" {
		return nil
	}

	limits := DefaultQuota()
	if user.QuotaJSON.Valid && user.QuotaJSON.String != "" {
		_ = json.Unmarshal([]byte(user.QuotaJSON.String), &limits)
	}

	usage, err := queries.GetUserQuotaUsage(ctx, user.ID)
	if err != nil {
		return fmt.Errorf("failed to fetch user quota usage: %w", err)
	}

	// 1. Instances
	if requestedInstances > 0 && usage.TotalInstances+requestedInstances > limits.Instances {
		return api.NewQuotaError("instances", limits.Instances, usage.TotalInstances, requestedInstances, traceID)
	}

	// 2. VCPUs
	if requestedVCPUs > 0 && usage.AllocatedVCPUs+requestedVCPUs > limits.VCPUs {
		return api.NewQuotaError("vcpus", limits.VCPUs, usage.AllocatedVCPUs, requestedVCPUs, traceID)
	}

	// 3. Memory
	if requestedMemoryMB > 0 && usage.AllocatedMemory+requestedMemoryMB > limits.MemoryMB {
		return api.NewQuotaError("memory_mb", limits.MemoryMB, usage.AllocatedMemory, requestedMemoryMB, traceID)
	}

	// 4. Disk
	maxDiskMB := limits.DiskGB * 1024
	if requestedDiskMB > 0 && usage.AllocatedDiskMB+requestedDiskMB > maxDiskMB {
		return api.NewQuotaError("disk_mb", maxDiskMB, usage.AllocatedDiskMB, requestedDiskMB, traceID)
	}

	// 5. VPCs
	if requestedVPCs > 0 && usage.TotalVPCs+requestedVPCs > limits.VPCs {
		return api.NewQuotaError("vpcs", limits.VPCs, usage.TotalVPCs, requestedVPCs, traceID)
	}

	// 6. API Keys
	if requestedAPIKeys > 0 && usage.TotalAPIKeys+requestedAPIKeys > limits.APIKeys {
		return api.NewQuotaError("api_keys", limits.APIKeys, usage.TotalAPIKeys, requestedAPIKeys, traceID)
	}

	return nil
}
