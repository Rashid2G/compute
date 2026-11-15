package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/Rashid2G/on-demand-vms/internal/config"
	"github.com/Rashid2G/on-demand-vms/internal/db"
	"github.com/Rashid2G/on-demand-vms/internal/doctor"
	"github.com/Rashid2G/on-demand-vms/internal/network"
	"github.com/Rashid2G/on-demand-vms/internal/vm"
	"github.com/Rashid2G/on-demand-vms/pkg/api"
	"github.com/Rashid2G/on-demand-vms/pkg/version"
)

type DaemonClient struct {
	baseURL      string
	apiKey       string
	httpClient   *http.Client
	streamClient *http.Client
}

type QueueResponse struct {
	QueuedVMs      []*vm.QueueItem       `json:"queued_vms"`
	Count          int                   `json:"count"`
	RunningVMs     int                   `json:"running_vms"`
	RunningVCPUs   int                   `json:"running_vcpus"`
	RunningMemMB   int                   `json:"running_mem_mb"`
	ResourceLimits config.ResourceLimits `json:"resource_limits"`
}

func NewDaemonClient(endpoint string) *DaemonClient {
	var transport *http.Transport
	if strings.HasPrefix(endpoint, "unix://") {
		sockPath := strings.TrimPrefix(endpoint, "unix://")
		transport = &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
			},
		}
		endpoint = "http://unix"
	} else {
		if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
			endpoint = "http://" + endpoint
		}
		transport = http.DefaultTransport.(*http.Transport).Clone()
	}

	key := os.Getenv("COMPUTE_API_KEY")
	if key == "" {
		if home, err := os.UserHomeDir(); err == nil {
			cfgFile := filepath.Join(home, ".compute", "config.json")
			if data, err := os.ReadFile(cfgFile); err == nil {
				var conf struct {
					APIKey string `json:"api_key"`
				}
				if json.Unmarshal(data, &conf) == nil && conf.APIKey != "" {
					key = conf.APIKey
				}
			}
		}
	}

	return &DaemonClient{
		baseURL:      strings.TrimSuffix(endpoint, "/"),
		apiKey:       key,
		httpClient:   &http.Client{Transport: transport, Timeout: 120 * time.Second},
		streamClient: &http.Client{Transport: transport},
	}
}

func (c *DaemonClient) SetAPIKey(key string) {
	c.apiKey = key
}

func (c *DaemonClient) GetAPIKey() string {
	return c.apiKey
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
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("X-API-Key", c.apiKey)
	}

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

func (c *DaemonClient) CloneVM(ctx context.Context, id string, req vm.CreateRequest) (*db.VM, error) {
	var vm db.VM
	err := c.doRequest(ctx, http.MethodPost, "/api/v1/vms/"+id+"/clone", req, &vm)
	return &vm, err
}

func (c *DaemonClient) GetTiers(ctx context.Context) ([]config.VMTier, error) {
	var tiers []config.VMTier
	err := c.doRequest(ctx, http.MethodGet, "/api/v1/tiers", nil, &tiers)
	return tiers, err
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

func (c *DaemonClient) UpdateVMConfig(ctx context.Context, id string, req vm.UpdateVMConfigRequest) (*db.VM, error) {
	var vm db.VM
	err := c.doRequest(ctx, http.MethodPatch, "/api/v1/vms/"+id, req, &vm)
	return &vm, err
}

func (c *DaemonClient) ProtectVM(ctx context.Context, id string) (*db.VM, error) {
	var vm db.VM
	err := c.doRequest(ctx, http.MethodPost, "/api/v1/vms/"+id+"/protect", nil, &vm)
	return &vm, err
}

func (c *DaemonClient) UnprotectVM(ctx context.Context, id string) (*db.VM, error) {
	var vm db.VM
	err := c.doRequest(ctx, http.MethodPost, "/api/v1/vms/"+id+"/unprotect", nil, &vm)
	return &vm, err
}

func (c *DaemonClient) SetVMNetworkIsolation(ctx context.Context, id, mode string) (*db.VM, error) {
	var vm db.VM
	err := c.doRequest(ctx, http.MethodPost, "/api/v1/vms/"+id+"/network-isolation", map[string]string{
		"network_isolation": mode,
	}, &vm)
	return &vm, err
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

func (c *DaemonClient) GetConfig(ctx context.Context) (*config.GlobalConfig, error) {
	var cfg config.GlobalConfig
	err := c.doRequest(ctx, http.MethodGet, "/api/v1/config", nil, &cfg)
	return &cfg, err
}

func (c *DaemonClient) GetQueue(ctx context.Context) (*QueueResponse, error) {
	var res QueueResponse
	err := c.doRequest(ctx, http.MethodGet, "/api/v1/queue", nil, &res)
	return &res, err
}

func (c *DaemonClient) ExecVM(ctx context.Context, id string, req vm.ExecRequest) (*vm.ExecResponse, error) {
	var res vm.ExecResponse
	err := c.doRequest(ctx, http.MethodPost, "/api/v1/vms/"+id+"/exec", req, &res)
	return &res, err
}

// ExecVMStream executes a command and calls emit for each output chunk as it
// arrives over server-sent events. The final event carries the exit code.
func (c *DaemonClient) ExecVMStream(ctx context.Context, id string, req vm.ExecRequest, emit func(vm.ExecStreamEvent) error) error {
	payload, err := json.Marshal(req)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/vms/"+id+"/exec-stream", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
		httpReq.Header.Set("X-API-Key", c.apiKey)
	}

	resp, err := c.streamClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("connection to daemon failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		var errObj struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &errObj) == nil && errObj.Error != "" {
			return fmt.Errorf("daemon error: %s", errObj.Error)
		}
		return fmt.Errorf("daemon error (status %d): %s", resp.StatusCode, string(body))
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev vm.ExecStreamEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			continue
		}
		if err := emit(ev); err != nil {
			return err
		}
	}
	return scanner.Err()
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
func (c *DaemonClient) SetVMPublicIP(ctx context.Context, id string, enabled bool) (*db.VM, error) {
	reqBody := vm.UpdateVMConfigRequest{
		PublicIP:        &enabled,
		PublicIPEnabled: &enabled,
	}
	var res db.VM
	err := c.doRequest(ctx, http.MethodPost, "/api/v1/vms/"+id+"/public-ip", reqBody, &res)
	return &res, err
}

type ImageItem struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	BaseImage   string   `json:"base_image,omitempty"`
	Packages    []string `json:"packages,omitempty"`
	SizeMB      int      `json:"size_mb"`
	Status      string   `json:"status"`
	Kind        string   `json:"kind"`
	CreatedAt   string   `json:"created_at,omitempty"`
}

func (c *DaemonClient) ListImages(ctx context.Context) ([]ImageItem, error) {
	var images []ImageItem
	err := c.doRequest(ctx, http.MethodGet, "/api/v1/images", nil, &images)
	return images, err
}

func (c *DaemonClient) BuildImage(ctx context.Context, req vm.BuildImageRequest) (*db.CustomImage, error) {
	var img db.CustomImage
	err := c.doRequest(ctx, http.MethodPost, "/api/v1/images", req, &img)
	return &img, err
}

func (c *DaemonClient) GetImage(ctx context.Context, idOrName string) (*ImageItem, error) {
	var img ImageItem
	err := c.doRequest(ctx, http.MethodGet, "/api/v1/images/"+idOrName, nil, &img)
	return &img, err
}

func (c *DaemonClient) DeleteImage(ctx context.Context, idOrName string) error {
	return c.doRequest(ctx, http.MethodDelete, "/api/v1/images/"+idOrName, nil, nil)
}

func (c *DaemonClient) ListTemplates(ctx context.Context) ([]*db.Template, error) {
	var templates []*db.Template
	err := c.doRequest(ctx, http.MethodGet, "/api/v1/templates", nil, &templates)
	return templates, err
}

func (c *DaemonClient) CreateTemplate(ctx context.Context, req vm.CreateTemplateRequest) (*db.Template, error) {
	var tpl db.Template
	err := c.doRequest(ctx, http.MethodPost, "/api/v1/templates", req, &tpl)
	return &tpl, err
}

func (c *DaemonClient) GetTemplate(ctx context.Context, idOrName string) (*db.Template, error) {
	var tpl db.Template
	err := c.doRequest(ctx, http.MethodGet, "/api/v1/templates/"+idOrName, nil, &tpl)
	return &tpl, err
}

func (c *DaemonClient) DeleteTemplate(ctx context.Context, idOrName string) error {
	return c.doRequest(ctx, http.MethodDelete, "/api/v1/templates/"+idOrName, nil, nil)
}

func (c *DaemonClient) LaunchTemplate(ctx context.Context, idOrName string, req vm.CreateRequest) (*db.VM, error) {
	var vm db.VM
	err := c.doRequest(ctx, http.MethodPost, "/api/v1/templates/"+idOrName+"/launch", req, &vm)
	return &vm, err
}

type CreateKeyRequest struct {
	Name         string `json:"name"`
	PublicKey    string `json:"public_key"`
	AllSandboxes bool   `json:"all_sandboxes,omitempty"`
	IsDefault    bool   `json:"is_default,omitempty"`
}

type UpdateKeyRequest struct {
	Name         *string `json:"name,omitempty"`
	PublicKey    *string `json:"public_key,omitempty"`
	AllSandboxes *bool   `json:"all_sandboxes,omitempty"`
	IsDefault    *bool   `json:"is_default,omitempty"`
}

// KeyApplyResult reports an account-wide key reconciliation pass.
type KeyApplyResult struct {
	Key  *db.SSHKey           `json:"key"`
	Sync *vm.SSHKeySyncResult `json:"sync,omitempty"`
}

type KeysResponse struct {
	Keys []*db.SSHKey `json:"keys"`
}

func (c *DaemonClient) Host() string {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func (c *DaemonClient) ListKeys(ctx context.Context) ([]*db.SSHKey, error) {
	var resp KeysResponse
	if err := c.doRequest(ctx, http.MethodGet, "/api/v1/keys", nil, &resp); err != nil {
		var list []*db.SSHKey
		if err2 := c.doRequest(ctx, http.MethodGet, "/api/v1/keys", nil, &list); err2 == nil {
			return list, nil
		}
		return nil, err
	}
	return resp.Keys, nil
}

func (c *DaemonClient) CreateKey(ctx context.Context, name, pubKey string, allSandboxes, isDefault bool) (*db.SSHKey, error) {
	req := CreateKeyRequest{Name: name, PublicKey: pubKey, AllSandboxes: allSandboxes, IsDefault: isDefault}
	var key db.SSHKey
	if err := c.doRequest(ctx, http.MethodPost, "/api/v1/keys", req, &key); err != nil {
		return nil, err
	}
	return &key, nil
}

func (c *DaemonClient) UpdateKey(ctx context.Context, idOrName string, req UpdateKeyRequest) (*db.SSHKey, error) {
	var key db.SSHKey
	if err := c.doRequest(ctx, http.MethodPatch, "/api/v1/keys/"+url.PathEscape(idOrName), req, &key); err != nil {
		return nil, err
	}
	return &key, nil
}

func (c *DaemonClient) ApplyKey(ctx context.Context, idOrName string) (*KeyApplyResult, error) {
	var res KeyApplyResult
	if err := c.doRequest(ctx, http.MethodPost, "/api/v1/keys/"+url.PathEscape(idOrName)+"/apply", nil, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *DaemonClient) ReconcileKeys(ctx context.Context) (*KeyApplyResult, error) {
	var res struct {
		Status string               `json:"status"`
		Sync   *vm.SSHKeySyncResult `json:"sync"`
	}
	if err := c.doRequest(ctx, http.MethodPost, "/api/v1/keys/reconcile", nil, &res); err != nil {
		return nil, err
	}
	return &KeyApplyResult{Sync: res.Sync}, nil
}

func (c *DaemonClient) ReconcileSandboxSSH(ctx context.Context, idOrName string) error {
	return c.doRequest(ctx, http.MethodPost, "/api/v1/sandboxes/"+url.PathEscape(idOrName)+"/reconcile-ssh", nil, nil)
}

func (c *DaemonClient) GetKey(ctx context.Context, idOrName string) (*db.SSHKey, error) {
	var key db.SSHKey
	if err := c.doRequest(ctx, http.MethodGet, "/api/v1/keys/"+idOrName, nil, &key); err != nil {
		return nil, err
	}
	return &key, nil
}

func (c *DaemonClient) DeleteKey(ctx context.Context, idOrName string) error {
	return c.doRequest(ctx, http.MethodDelete, "/api/v1/keys/"+idOrName, nil, nil)
}

// ResourceRequest exposes the job, worker, and pool control-plane API.
func (c *DaemonClient) ResourceRequest(ctx context.Context, method, path string, body, result interface{}) error {
	return c.doRequest(ctx, method, path, body, result)
}

// Worker, Pool, and Job client methods

func (c *DaemonClient) CreateWorkerToken(ctx context.Context, req api.WorkerTokenCreateRequest) (*api.WorkerTokenResponse, error) {
	var resp api.WorkerTokenResponse
	err := c.doRequest(ctx, http.MethodPost, "/api/v1/worker-tokens", req, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *DaemonClient) ListWorkers(ctx context.Context) ([]*db.WorkerNode, error) {
	var workers []*db.WorkerNode
	err := c.doRequest(ctx, http.MethodGet, "/api/v1/workers", nil, &workers)
	if err != nil {
		return nil, err
	}
	return workers, nil
}

func (c *DaemonClient) GetWorker(ctx context.Context, id string) (*db.WorkerNode, error) {
	var worker db.WorkerNode
	err := c.doRequest(ctx, http.MethodGet, "/api/v1/workers/"+id, nil, &worker)
	if err != nil {
		return nil, err
	}
	return &worker, nil
}

func (c *DaemonClient) CordonWorker(ctx context.Context, id string) error {
	return c.doRequest(ctx, http.MethodPost, "/api/v1/workers/"+id+"/cordon", nil, nil)
}

func (c *DaemonClient) DrainWorker(ctx context.Context, id string) error {
	return c.doRequest(ctx, http.MethodPost, "/api/v1/workers/"+id+"/drain", nil, nil)
}

func (c *DaemonClient) DeleteWorker(ctx context.Context, id string) error {
	return c.doRequest(ctx, http.MethodDelete, "/api/v1/workers/"+id, nil, nil)
}

func (c *DaemonClient) ListPools(ctx context.Context) ([]*db.WorkerPool, error) {
	var pools []*db.WorkerPool
	err := c.doRequest(ctx, http.MethodGet, "/api/v1/pools", nil, &pools)
	if err != nil {
		return nil, err
	}
	return pools, nil
}

func (c *DaemonClient) CreatePool(ctx context.Context, name string) (*db.WorkerPool, error) {
	var pool db.WorkerPool
	err := c.doRequest(ctx, http.MethodPost, "/api/v1/pools", api.PoolCreateRequest{Name: name}, &pool)
	if err != nil {
		return nil, err
	}
	return &pool, nil
}

func (c *DaemonClient) DeletePool(ctx context.Context, id string) error {
	return c.doRequest(ctx, http.MethodDelete, "/api/v1/pools/"+id, nil, nil)
}

func (c *DaemonClient) CreateJob(ctx context.Context, req api.JobCreateRequest) (*db.Job, error) {
	var job db.Job
	err := c.doRequest(ctx, http.MethodPost, "/api/v1/jobs", req, &job)
	if err != nil {
		return nil, err
	}
	return &job, nil
}

func (c *DaemonClient) GetJob(ctx context.Context, id string) (*db.Job, error) {
	var job db.Job
	err := c.doRequest(ctx, http.MethodGet, "/api/v1/jobs/"+id, nil, &job)
	if err != nil {
		return nil, err
	}
	return &job, nil
}

func (c *DaemonClient) ListJobs(ctx context.Context) ([]*db.Job, error) {
	var jobs []*db.Job
	err := c.doRequest(ctx, http.MethodGet, "/api/v1/jobs", nil, &jobs)
	if err != nil {
		return nil, err
	}
	return jobs, nil
}

func (c *DaemonClient) CancelJob(ctx context.Context, id string) error {
	return c.doRequest(ctx, http.MethodPost, "/api/v1/jobs/"+id+"/cancel", nil, nil)
}

func (c *DaemonClient) GetJobLogs(ctx context.Context, id string) ([]*db.JobLog, error) {
	var logs []*db.JobLog
	err := c.doRequest(ctx, http.MethodGet, "/api/v1/jobs/"+id+"/logs", nil, &logs)
	if err != nil {
		return nil, err
	}
	return logs, nil
}

// GetReadiness fetches the daemon readiness report. A 503 response is a valid
// report, not a transport error, so `compute doctor` can print remediation.
func (c *DaemonClient) GetReadiness(ctx context.Context) (*doctor.Report, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/ready", nil)
	if err != nil {
		return nil, err
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("X-API-Key", c.apiKey)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connection to daemon failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusServiceUnavailable {
		return nil, fmt.Errorf("daemon error (status %d): %s", resp.StatusCode, string(body))
	}
	var report doctor.Report
	if err := json.Unmarshal(body, &report); err != nil {
		return nil, fmt.Errorf("invalid readiness response: %w", err)
	}
	return &report, nil
}

func (c *DaemonClient) GetVersion(ctx context.Context) (*version.Info, error) {
	var info version.Info
	err := c.doRequest(ctx, http.MethodGet, "/api/v1/version", nil, &info)
	if err != nil {
		return nil, err
	}
	return &info, nil
}

func (c *DaemonClient) SSHWebSocketURL(vmID string, query url.Values) string {
	base := c.baseURL
	var wsScheme string
	var hostAndPath string
	if strings.HasPrefix(base, "https://") {
		wsScheme = "wss://"
		hostAndPath = strings.TrimPrefix(base, "https://")
	} else {
		wsScheme = "ws://"
		hostAndPath = strings.TrimPrefix(base, "http://")
	}
	u := fmt.Sprintf("%s%s/api/v1/vms/%s/ssh", wsScheme, hostAndPath, url.PathEscape(vmID))
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	return u
}

func (c *DaemonClient) DialSSHWebSocket(ctx context.Context, vmID string, query url.Values) (*websocket.Conn, error) {
	wsURL := c.SSHWebSocketURL(vmID, query)
	header := make(http.Header)
	if c.apiKey != "" {
		header.Set("Authorization", "Bearer "+c.apiKey)
		header.Set("X-API-Key", c.apiKey)
	}
	opts := &websocket.DialOptions{
		HTTPClient: c.httpClient,
		HTTPHeader: header,
	}
	conn, _, err := websocket.Dial(ctx, wsURL, opts)
	return conn, err
}

// Auth API methods

func (c *DaemonClient) Signup(ctx context.Context, email, username, displayName, password string) error {
	req := map[string]string{
		"email":        email,
		"username":     username,
		"display_name": displayName,
		"password":     password,
	}
	return c.doRequest(ctx, http.MethodPost, "/api/v1/auth/signup", req, nil)
}

func (c *DaemonClient) Login(ctx context.Context, username, password string) (string, map[string]interface{}, error) {
	req := map[string]string{
		"username": username,
		"password": password,
	}
	var res struct {
		Token string                 `json:"token"`
		User  map[string]interface{} `json:"user"`
	}
	err := c.doRequest(ctx, http.MethodPost, "/api/v1/auth/login", req, &res)
	if err != nil {
		return "", nil, err
	}
	return res.Token, res.User, nil
}

func (c *DaemonClient) GetMe(ctx context.Context) (map[string]interface{}, error) {
	var res map[string]interface{}
	err := c.doRequest(ctx, http.MethodGet, "/api/v1/auth/me", nil, &res)
	return res, err
}

func (c *DaemonClient) CreateAPIKey(ctx context.Context, name string, expiresInSec int64) (string, error) {
	req := map[string]interface{}{
		"name": name,
	}
	if expiresInSec > 0 {
		req["expires_in_seconds"] = expiresInSec
	}
	var res struct {
		Token string          `json:"token"`
		Key   json.RawMessage `json:"key"`
	}
	err := c.doRequest(ctx, http.MethodPost, "/api/v1/auth/keys", req, &res)
	if err != nil {
		return "", err
	}
	if res.Token != "" {
		return res.Token, nil
	}
	// Backwards compatibility: older daemons returned the secret in "key".
	if len(res.Key) > 0 {
		var keyStr string
		if json.Unmarshal(res.Key, &keyStr) == nil && keyStr != "" {
			return keyStr, nil
		}
		var keyObj struct {
			Token  string `json:"token"`
			Secret string `json:"secret"`
			Value  string `json:"value"`
		}
		if json.Unmarshal(res.Key, &keyObj) == nil {
			if keyObj.Token != "" {
				return keyObj.Token, nil
			}
			if keyObj.Secret != "" {
				return keyObj.Secret, nil
			}
			if keyObj.Value != "" {
				return keyObj.Value, nil
			}
		}
	}
	return "", fmt.Errorf("daemon returned no API key token")
}

func (c *DaemonClient) ListAPIKeys(ctx context.Context) ([]map[string]interface{}, error) {
	var res []map[string]interface{}
	err := c.doRequest(ctx, http.MethodGet, "/api/v1/auth/keys", nil, &res)
	return res, err
}

func (c *DaemonClient) RevokeAPIKey(ctx context.Context, id string) error {
	return c.doRequest(ctx, http.MethodDelete, "/api/v1/auth/keys/"+id, nil, nil)
}

func (c *DaemonClient) ListOrgs(ctx context.Context) ([]map[string]interface{}, error) {
	var res []map[string]interface{}
	err := c.doRequest(ctx, http.MethodGet, "/api/v1/orgs", nil, &res)
	return res, err
}

func (c *DaemonClient) CreateOrg(ctx context.Context, name, slug string) (map[string]interface{}, error) {
	req := map[string]string{
		"name": name,
		"slug": slug,
	}
	var res map[string]interface{}
	err := c.doRequest(ctx, http.MethodPost, "/api/v1/orgs", req, &res)
	return res, err
}

// Filesystem API types & methods

type SandboxFileEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

type SandboxFileReadResult struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Format  string `json:"format"`
	Size    int    `json:"size"`
}

type SandboxProcessInfo struct {
	PID     int     `json:"pid"`
	PPID    int     `json:"ppid"`
	User    string  `json:"user"`
	CPU     float64 `json:"cpu"`
	Memory  float64 `json:"memory"`
	Status  string  `json:"status"`
	Command string  `json:"command"`
	Args    string  `json:"args"`
}

func (c *DaemonClient) WriteFile(ctx context.Context, sandboxID, path, content, format string) error {
	req := map[string]string{
		"path":    path,
		"content": content,
		"format":  format,
	}
	return c.doRequest(ctx, http.MethodPost, "/api/v1/sandboxes/"+sandboxID+"/files/write", req, nil)
}

func (c *DaemonClient) MkdirFile(ctx context.Context, sandboxID, path string) error {
	req := map[string]string{
		"path": path,
	}
	return c.doRequest(ctx, http.MethodPost, "/api/v1/sandboxes/"+sandboxID+"/files/mkdir", req, nil)
}

func (c *DaemonClient) ReadFile(ctx context.Context, sandboxID, path, format string) (string, error) {
	res, err := c.ReadFileDetails(ctx, sandboxID, path, format)
	if err != nil {
		return "", err
	}
	return res.Content, nil
}

func (c *DaemonClient) ReadFileDetails(ctx context.Context, sandboxID, path, format string) (*SandboxFileReadResult, error) {
	q := url.Values{}
	q.Set("path", path)
	if format != "" {
		q.Set("format", format)
	}
	var res SandboxFileReadResult
	err := c.doRequest(ctx, http.MethodGet, "/api/v1/sandboxes/"+sandboxID+"/files/read?"+q.Encode(), nil, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *DaemonClient) ListFiles(ctx context.Context, sandboxID, path string) ([]map[string]interface{}, error) {
	q := url.Values{}
	if path != "" {
		q.Set("path", path)
	}
	var res struct {
		Entries []map[string]interface{} `json:"entries"`
	}
	err := c.doRequest(ctx, http.MethodGet, "/api/v1/sandboxes/"+sandboxID+"/files/list?"+q.Encode(), nil, &res)
	if err != nil {
		return nil, err
	}
	return res.Entries, nil
}

func (c *DaemonClient) ListFilesDetails(ctx context.Context, sandboxID, path string) ([]SandboxFileEntry, error) {
	q := url.Values{}
	if path != "" {
		q.Set("path", path)
	}
	var res struct {
		Entries []SandboxFileEntry `json:"entries"`
	}
	err := c.doRequest(ctx, http.MethodGet, "/api/v1/sandboxes/"+sandboxID+"/files/list?"+q.Encode(), nil, &res)
	if err != nil {
		return nil, err
	}
	return res.Entries, nil
}

func (c *DaemonClient) RemoveFile(ctx context.Context, sandboxID, path string) error {
	q := url.Values{}
	q.Set("path", path)
	return c.doRequest(ctx, http.MethodDelete, "/api/v1/sandboxes/"+sandboxID+"/files?"+q.Encode(), nil, nil)
}

// Process API methods

func (c *DaemonClient) ListProcesses(ctx context.Context, sandboxID string) ([]map[string]interface{}, error) {
	var res struct {
		Processes []map[string]interface{} `json:"processes"`
	}
	err := c.doRequest(ctx, http.MethodGet, "/api/v1/sandboxes/"+sandboxID+"/processes", nil, &res)
	if err != nil {
		return nil, err
	}
	return res.Processes, nil
}

func (c *DaemonClient) ListProcessesDetails(ctx context.Context, sandboxID string) ([]SandboxProcessInfo, error) {
	var res struct {
		Processes []SandboxProcessInfo `json:"processes"`
	}
	err := c.doRequest(ctx, http.MethodGet, "/api/v1/sandboxes/"+sandboxID+"/processes", nil, &res)
	if err != nil {
		return nil, err
	}
	return res.Processes, nil
}

func (c *DaemonClient) StartProcess(ctx context.Context, sandboxID, command, cwd string, env map[string]string) (int, error) {
	req := map[string]interface{}{
		"command": command,
		"cwd":     cwd,
		"env":     env,
	}
	var res struct {
		PID int `json:"pid"`
	}
	err := c.doRequest(ctx, http.MethodPost, "/api/v1/sandboxes/"+sandboxID+"/processes", req, &res)
	if err != nil {
		return 0, err
	}
	return res.PID, nil
}

func (c *DaemonClient) KillProcess(ctx context.Context, sandboxID string, pid int, signal string) error {
	req := map[string]interface{}{
		"pid":    pid,
		"signal": signal,
	}
	return c.doRequest(ctx, http.MethodPost, "/api/v1/sandboxes/"+sandboxID+"/processes/kill", req, nil)
}
