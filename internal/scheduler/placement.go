package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/pkg/api"
)

// SandboxPlacementRequest describes the placement constraints and resource needs of a sandbox or job.
type SandboxPlacementRequest struct {
	WorkerID string            `json:"worker_id,omitempty"`
	PoolID   string            `json:"pool_id,omitempty"`
	VCPUs    int               `json:"vcpus"`
	MemoryMB int               `json:"memory_mb"`
	DiskMB   int               `json:"disk_mb"`
	Labels   map[string]string `json:"labels,omitempty"`
}

var roundRobinCounter atomic.Uint64

// SelectNodeForSandbox chooses the best eligible ready worker node (or localNode)
// according to ClusterSchedulingPolicy and explicit WorkerID/PoolID constraints.
func SelectNodeForSandbox(
	ctx context.Context,
	workers []*db.WorkerNode,
	localNode *db.WorkerNode,
	req SandboxPlacementRequest,
	policy db.ClusterSchedulingPolicy,
) (*db.WorkerNode, error) {
	_ = ctx
	candidates := mergeCandidateNodes(workers, localNode)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no worker nodes available")
	}

	reqWorker := strings.TrimSpace(req.WorkerID)
	reqPool := strings.TrimSpace(req.PoolID)

	if reqWorker != "" {
		var chosen *db.WorkerNode
		for _, n := range candidates {
			if n.ID == reqWorker || n.Name == reqWorker {
				chosen = n
				break
			}
		}
		if chosen == nil {
			return nil, fmt.Errorf("worker %q not found", reqWorker)
		}
		if reqPool != "" && chosen.PoolID != reqPool {
			return nil, fmt.Errorf("worker %q is in pool %q, not requested pool %q", chosen.ID, chosen.PoolID, reqPool)
		}
		if chosen.Status != "ready" {
			return nil, fmt.Errorf("worker %q is not ready (status: %s)", chosen.ID, chosen.Status)
		}
		if !nodeMatchesLabels(chosen, req.Labels) {
			return nil, fmt.Errorf("worker %q does not match required labels", chosen.ID)
		}
		if err := checkNodeFits(chosen, req, policy); err != nil {
			return nil, err
		}
		return chosen, nil
	}

	if reqPool != "" {
		filtered := make([]*db.WorkerNode, 0, len(candidates))
		for _, n := range candidates {
			if n.PoolID == reqPool {
				filtered = append(filtered, n)
			}
		}
		if len(filtered) == 0 {
			return nil, fmt.Errorf("no workers available in pool %q", reqPool)
		}
		candidates = filtered
	}

	readyNodes := make([]*db.WorkerNode, 0, len(candidates))
	for _, n := range candidates {
		if n.Status == "ready" && nodeMatchesLabels(n, req.Labels) {
			readyNodes = append(readyNodes, n)
		}
	}
	if len(readyNodes) == 0 {
		if reqPool != "" {
			return nil, fmt.Errorf("no ready workers available in pool %q", reqPool)
		}
		return nil, fmt.Errorf("no ready worker nodes available")
	}

	eligible := make([]*db.WorkerNode, 0, len(readyNodes))
	var lastErr error
	for _, n := range readyNodes {
		if err := checkNodeFits(n, req, policy); err != nil {
			lastErr = err
			continue
		}
		eligible = append(eligible, n)
	}
	if len(eligible) == 0 {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("no worker node has sufficient capacity")
	}

	return pickNodeByStrategy(eligible, policy), nil
}

func mergeCandidateNodes(workers []*db.WorkerNode, localNode *db.WorkerNode) []*db.WorkerNode {
	seen := make(map[string]bool, len(workers)+1)
	out := make([]*db.WorkerNode, 0, len(workers)+1)
	for _, n := range workers {
		if n == nil || n.ID == "" || seen[n.ID] {
			continue
		}
		seen[n.ID] = true
		out = append(out, n)
	}
	if localNode != nil && localNode.ID != "" && !seen[localNode.ID] {
		out = append(out, localNode)
	}
	return out
}

func nodeMatchesLabels(n *db.WorkerNode, required map[string]string) bool {
	if len(required) == 0 {
		return true
	}
	var labels map[string]string
	if n.LabelsJSON != "" {
		_ = json.Unmarshal([]byte(n.LabelsJSON), &labels)
	}
	for k, v := range required {
		if labels[k] != v {
			return false
		}
	}
	return true
}

func nodeEffectiveVCPUs(n *db.WorkerNode, policy db.ClusterSchedulingPolicy) int {
	if n.AllocatedVCPUs > 0 {
		return n.AllocatedVCPUs
	}
	if n.TotalVCPUs > 0 {
		return n.TotalVCPUs
	}
	if n.PhysicalVCPUs > 0 {
		avail := n.PhysicalVCPUs - policy.HostReservedVCPUs
		if avail < 1 {
			avail = 1
		}
		return avail
	}
	return 0
}

func nodeEffectiveMemMB(n *db.WorkerNode, policy db.ClusterSchedulingPolicy) int {
	if n.AllocatedMemMB > 0 {
		return n.AllocatedMemMB
	}
	if n.TotalMemMB > 0 {
		return n.TotalMemMB
	}
	if n.PhysicalMemMB > 0 {
		avail := n.PhysicalMemMB - policy.HostReservedMemoryMB
		if avail < 0 {
			avail = 0
		}
		return avail
	}
	return 0
}

func nodeEffectiveDiskMB(n *db.WorkerNode, policy db.ClusterSchedulingPolicy) int {
	if n.AllocatedDiskMB > 0 {
		return n.AllocatedDiskMB
	}
	if n.TotalDiskMB > 0 {
		return n.TotalDiskMB
	}
	if n.PhysicalDiskMB > 0 {
		avail := n.PhysicalDiskMB - policy.HostReservedDiskMB
		if avail < 0 {
			avail = 0
		}
		return avail
	}
	return 0
}

func checkNodeFits(n *db.WorkerNode, req SandboxPlacementRequest, policy db.ClusterSchedulingPolicy) error {
	// 1. Hard disk check: effective disk ceiling applies whenever effDisk > 0;
	// physical disk safety is enforced via HostReservedDiskMB and AvailableDiskMB.
	effDisk := nodeEffectiveDiskMB(n, policy)
	if effDisk > 0 && req.DiskMB > 0 {
		freeLogical := effDisk - n.UsedDiskMB
		if freeLogical < req.DiskMB {
			if freeLogical < 0 {
				freeLogical = 0
			}
			return &api.ResourceLimitError{
				Code:       api.ErrCodeResourceLimitExceeded,
				Resource:   "disk",
				LimitType:  "hard",
				NodeID:     n.ID,
				Requested:  int64(req.DiskMB),
				Available:  int64(freeLogical),
				Limit:      int64(effDisk),
				Message:    fmt.Sprintf("worker %s disk capacity exceeded: %d MB used + %d MB requested > %d MB allocated", n.ID, n.UsedDiskMB, req.DiskMB, effDisk),
				Suggestion: "Choose another worker/pool or delete unused sandboxes",
			}
		}
	}
	minFree := policy.HostReservedDiskMB
	if n.AvailableDiskMB > 0 && minFree > 0 && n.AvailableDiskMB < minFree {
		return &api.ResourceLimitError{
			Code:       api.ErrCodeResourceLimitExceeded,
			Resource:   "disk",
			LimitType:  "hard",
			NodeID:     n.ID,
			Requested:  int64(req.DiskMB),
			Available:  int64(n.AvailableDiskMB),
			Limit:      int64(minFree),
			Message:    fmt.Sprintf("worker %s below minimum free disk reserve: %d MB available < %d MB reserve", n.ID, n.AvailableDiskMB, minFree),
			Suggestion: "Free host disk space or select another worker",
		}
	}
	if n.AvailableDiskMB > 0 && req.DiskMB > 0 && n.AvailableDiskMB < req.DiskMB {
		return &api.ResourceLimitError{
			Code:       api.ErrCodeResourceLimitExceeded,
			Resource:   "disk",
			LimitType:  "hard",
			NodeID:     n.ID,
			Requested:  int64(req.DiskMB),
			Available:  int64(n.AvailableDiskMB),
			Limit:      int64(n.AvailableDiskMB),
			Message:    fmt.Sprintf("worker %s insufficient available disk: %d MB requested > %d MB available", n.ID, req.DiskMB, n.AvailableDiskMB),
			Suggestion: "Select another worker or free host disk space",
		}
	}

	// 2. RAM hard guarantee
	allocMem := nodeEffectiveMemMB(n, policy)
	if allocMem > 0 && req.MemoryMB > 0 {
		freeMem := allocMem - n.UsedMemMB
		if freeMem < req.MemoryMB {
			if freeMem < 0 {
				freeMem = 0
			}
			return &api.ResourceLimitError{
				Code:       api.ErrCodeResourceLimitExceeded,
				Resource:   "memory",
				LimitType:  "hard",
				NodeID:     n.ID,
				Requested:  int64(req.MemoryMB),
				Available:  int64(freeMem),
				Limit:      int64(allocMem),
				Message:    fmt.Sprintf("worker %s memory capacity exceeded: %d MB used + %d MB requested > %d MB allocated", n.ID, n.UsedMemMB, req.MemoryMB, allocMem),
				Suggestion: "Stop or snapshot idle sandboxes or pick another worker",
			}
		}
	}
	if n.AvailableMemMB > 0 && req.MemoryMB > 0 && n.AvailableMemMB < req.MemoryMB {
		return &api.ResourceLimitError{
			Code:       api.ErrCodeResourceLimitExceeded,
			Resource:   "memory",
			LimitType:  "hard",
			NodeID:     n.ID,
			Requested:  int64(req.MemoryMB),
			Available:  int64(n.AvailableMemMB),
			Limit:      int64(n.AvailableMemMB),
			Message:    fmt.Sprintf("worker %s insufficient available RAM: %d MB requested > %d MB available", n.ID, req.MemoryMB, n.AvailableMemMB),
			Suggestion: "Select another worker or free RAM",
		}
	}

	// 3. CPU hard overcommit limit
	allocVCPUs := nodeEffectiveVCPUs(n, policy)
	if allocVCPUs > 0 && req.VCPUs > 0 {
		hardRatio := policy.CPUHardOvercommitRatio
		if hardRatio < 1.0 {
			hardRatio = 1.0
		}
		hardLimit := int(math.Floor(float64(allocVCPUs) * hardRatio))
		if hardLimit < allocVCPUs {
			hardLimit = allocVCPUs
		}
		if n.UsedVCPUs+req.VCPUs > hardLimit {
			avail := hardLimit - n.UsedVCPUs
			if avail < 0 {
				avail = 0
			}
			return &api.ResourceLimitError{
				Code:       api.ErrCodeResourceLimitExceeded,
				Resource:   "cpu",
				LimitType:  "hard",
				NodeID:     n.ID,
				Requested:  int64(req.VCPUs),
				Available:  int64(avail),
				Limit:      int64(hardLimit),
				Message:    fmt.Sprintf("worker %s hard vCPU limit exceeded: %d used + %d requested > %d hard limit", n.ID, n.UsedVCPUs, req.VCPUs, hardLimit),
				Suggestion: "Select another worker or stop idle sandboxes",
			}
		}
	}

	return nil
}

func nodeHeadroomScore(n *db.WorkerNode, policy db.ClusterSchedulingPolicy) float64 {
	wCPU := policy.CPUWeight
	wMem := policy.MemoryWeight
	wDisk := policy.DiskWeight
	if wCPU <= 0 && wMem <= 0 && wDisk <= 0 {
		wCPU, wMem, wDisk = 0.35, 0.40, 0.25
	}
	weightSum := wCPU + wMem + wDisk
	wCPU /= weightSum
	wMem /= weightSum
	wDisk /= weightSum

	cpuHeadroom := 1.0
	if allocVCPUs := nodeEffectiveVCPUs(n, policy); allocVCPUs > 0 {
		cpuHeadroom = float64(allocVCPUs-n.UsedVCPUs) / float64(allocVCPUs)
	}
	memHeadroom := 1.0
	if allocMem := nodeEffectiveMemMB(n, policy); allocMem > 0 {
		memHeadroom = float64(allocMem-n.UsedMemMB) / float64(allocMem)
		if memHeadroom < 0 {
			memHeadroom = 0
		}
	}
	diskHeadroom := 1.0
	if allocDisk := nodeEffectiveDiskMB(n, policy); allocDisk > 0 {
		diskHeadroom = float64(allocDisk-n.UsedDiskMB) / float64(allocDisk)
		if diskHeadroom < 0 {
			diskHeadroom = 0
		}
	}

	return wCPU*cpuHeadroom + wMem*memHeadroom + wDisk*diskHeadroom
}

func orderNodesByStrategy(eligible []*db.WorkerNode, policy db.ClusterSchedulingPolicy) []*db.WorkerNode {
	ordered := append([]*db.WorkerNode(nil), eligible...)
	strategy := strings.ToLower(strings.TrimSpace(policy.Strategy))
	if strategy == "" {
		strategy = "least_loaded"
	}

	switch strategy {
	case "round_robin":
		sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
		if len(ordered) > 1 {
			start := int((roundRobinCounter.Add(1) - 1) % uint64(len(ordered)))
			rotated := make([]*db.WorkerNode, 0, len(ordered))
			rotated = append(rotated, ordered[start:]...)
			rotated = append(rotated, ordered[:start]...)
			return rotated
		}
		return ordered

	case "bin_pack":
		sort.Slice(ordered, func(i, j int) bool {
			si := nodeHeadroomScore(ordered[i], policy)
			sj := nodeHeadroomScore(ordered[j], policy)
			if math.Abs(si-sj) > 1e-9 {
				return si < sj
			}
			return ordered[i].ID < ordered[j].ID
		})
		return ordered

	case "spread":
		sort.Slice(ordered, func(i, j int) bool {
			if ordered[i].RunningVMs != ordered[j].RunningVMs {
				return ordered[i].RunningVMs < ordered[j].RunningVMs
			}
			if ordered[i].UsedMemMB != ordered[j].UsedMemMB {
				return ordered[i].UsedMemMB < ordered[j].UsedMemMB
			}
			return ordered[i].ID < ordered[j].ID
		})
		return ordered

	default: // "least_loaded"
		sort.Slice(ordered, func(i, j int) bool {
			si := nodeHeadroomScore(ordered[i], policy)
			sj := nodeHeadroomScore(ordered[j], policy)
			if math.Abs(si-sj) > 1e-9 {
				return si > sj
			}
			return ordered[i].ID < ordered[j].ID
		})
		return ordered
	}
}

func pickNodeByStrategy(eligible []*db.WorkerNode, policy db.ClusterSchedulingPolicy) *db.WorkerNode {
	ordered := orderNodesByStrategy(eligible, policy)
	if len(ordered) == 0 {
		return nil
	}
	return ordered[0]
}
