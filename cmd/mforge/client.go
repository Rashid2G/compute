package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/db"
	"github.com/Rashid2G/on-demand-vms/internal/network"
	"github.com/Rashid2G/on-demand-vms/internal/vm"
)

type DaemonClient struct {
	baseURL    string
	httpClient *http.Client
}

func NewDaemonClient(endpoint string) *DaemonClient {
	httpClient := &http.Client{Timeout: 60 * time.Second}

	if strings.HasPrefix(endpoint, "unix://") {
		sockPath := strings.TrimPrefix(endpoint, "unix://")
		httpClient.Transport = &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
			},
		}
		endpoint = "http://localhost"
	} else if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "http://" + endpoint
	}

	return &DaemonClient{
		baseURL:    strings.TrimSuffix(endpoint, "/"),
		httpClient: httpClient,
	}
}

func (c *DaemonClient) doRequest(ctx context.Context, method, path string, body interface{}, result interface{}) error {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("connection to daemon failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errObj struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(respBytes, &errObj) == nil && errObj.Error != "" {
			return fmt.Errorf("daemon error: %s", errObj.Error)
		}
		return fmt.Errorf("daemon error (status %d): %s", resp.StatusCode, string(respBytes))
	}

	if result != nil && len(respBytes) > 0 {
		return json.Unmarshal(respBytes, result)
	}
	return nil
}

func (c *DaemonClient) CreateVM(ctx context.Context, req vm.CreateRequest) (*db.VM, error) {
	var vm db.VM
	err := c.doRequest(ctx, http.MethodPost, "/api/v1/vms", req, &vm)
	return &vm, err
}

func (c *DaemonClient) ListVMs(ctx context.Context) ([]*db.VM, error) {
	var vms []*db.VM
	err := c.doRequest(ctx, http.MethodGet, "/api/v1/vms", nil, &vms)
	return vms, err
}

func (c *DaemonClient) GetVM(ctx context.Context, id string) (*db.VM, error) {
	var vm db.VM
	err := c.doRequest(ctx, http.MethodGet, "/api/v1/vms/"+id, nil, &vm)
	return &vm, err
}

func (c *DaemonClient) RunVM(ctx context.Context, id string) (*db.VM, error) {
	var vm db.VM
	err := c.doRequest(ctx, http.MethodPost, "/api/v1/vms/"+id+"/run", nil, &vm)
	return &vm, err
}

func (c *DaemonClient) SaveState(ctx context.Context, id string) (*db.VM, error) {
	var vm db.VM
	err := c.doRequest(ctx, http.MethodPost, "/api/v1/vms/"+id+"/save-state", nil, &vm)
	return &vm, err
}

func (c *DaemonClient) ResumeVM(ctx context.Context, id string) (*db.VM, error) {
	var vm db.VM
	err := c.doRequest(ctx, http.MethodPost, "/api/v1/vms/"+id+"/resume", nil, &vm)
	return &vm, err
}

func (c *DaemonClient) StopVM(ctx context.Context, id string) (*db.VM, error) {
	var vm db.VM
	err := c.doRequest(ctx, http.MethodPost, "/api/v1/vms/"+id+"/stop", nil, &vm)
	return &vm, err
}

func (c *DaemonClient) DeleteVM(ctx context.Context, id string) error {
	return c.doRequest(ctx, http.MethodDelete, "/api/v1/vms/"+id, nil, nil)
}

func (c *DaemonClient) GetNetworkInfo(ctx context.Context) (*network.NetworkInfo, error) {
	var info network.NetworkInfo
	err := c.doRequest(ctx, http.MethodGet, "/api/v1/network", nil, &info)
	return &info, err
}

func (c *DaemonClient) GetNetworkAllocations(ctx context.Context) ([]*db.NetworkAllocation, error) {
	var allocs []*db.NetworkAllocation
	err := c.doRequest(ctx, http.MethodGet, "/api/v1/network/allocations", nil, &allocs)
	return allocs, err
}

func (c *DaemonClient) PruneNetwork(ctx context.Context) ([]string, error) {
	var res struct {
		Status     string   `json:"status"`
		PrunedTAPs []string `json:"pruned_taps"`
	}
	err := c.doRequest(ctx, http.MethodPost, "/api/v1/network/prune", nil, &res)
	return res.PrunedTAPs, err
}

func (c *DaemonClient) GetNetworkConfig(ctx context.Context) (*network.Config, error) {
	var cfg network.Config
	err := c.doRequest(ctx, http.MethodGet, "/api/v1/network/config", nil, &cfg)
	return &cfg, err
}

func (c *DaemonClient) ExecVM(ctx context.Context, id string, req vm.ExecRequest) (*vm.ExecResponse, error) {
	var res vm.ExecResponse
	err := c.doRequest(ctx, http.MethodPost, "/api/v1/vms/"+id+"/exec", req, &res)
	return &res, err
}

func (c *DaemonClient) InspectNetwork(ctx context.Context, id string) (*vm.NetworkInspectResult, error) {
	var res vm.NetworkInspectResult
	err := c.doRequest(ctx, http.MethodGet, "/api/v1/network/inspect/"+id, nil, &res)
	return &res, err
}

func (c *DaemonClient) GetStats(ctx context.Context) (*vm.StatsResult, error) {
	var stats vm.StatsResult
	err := c.doRequest(ctx, http.MethodGet, "/api/v1/stats", nil, &stats)
	return &stats, err
}
