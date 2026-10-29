package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/db"
	"github.com/Rashid2G/on-demand-vms/internal/vm"
)

type MetricResult struct {
	Name        string        `json:"name"`
	Count       int           `json:"count"`
	Min         time.Duration `json:"min_ns"`
	Max         time.Duration `json:"max_ns"`
	Avg         time.Duration `json:"avg_ns"`
	P50         time.Duration `json:"p50_ns"`
	P90         time.Duration `json:"p90_ns"`
	P95         time.Duration `json:"p95_ns"`
	StdDev      time.Duration `json:"stddev_ns"`
	OpsPerSec   float64       `json:"ops_per_sec"`
	Description string        `json:"description,omitempty"`
}

type BenchmarkReport struct {
	Timestamp   time.Time               `json:"timestamp"`
	DaemonAddr  string                  `json:"daemon_addr"`
	GitCommit   string                  `json:"git_commit,omitempty"`
	Environment map[string]string       `json:"environment"`
	Metrics     map[string]MetricResult `json:"metrics"`
}

func main() {
	daemonAddr := flag.String("daemon", "127.0.0.1:18080", "Daemon address (e.g. 127.0.0.1:18080)")
	apiIters := flag.Int("api-iterations", 50, "Number of iterations for API benchmarks")
	lifecycleRuns := flag.Int("lifecycle-runs", 2, "Number of runs for full VM lifecycle benchmarks")
	concurrency := flag.Int("concurrency", 5, "Concurrency level for API stress benchmarks")
	apiOnly := flag.Bool("api-only", false, "Run only API latency benchmarks (skip VM lifecycle)")
	outputFile := flag.String("output", "benchmark-results.json", "JSON file to save benchmark results")
	compareFile := flag.String("compare", "", "Path to previous baseline benchmark JSON file for comparison")
	flag.Parse()

	if env := os.Getenv("MFORGE_DAEMON"); env != "" && *daemonAddr == "127.0.0.1:18080" {
		*daemonAddr = env
	}

	fmt.Println("==========================================================================")
	fmt.Println("             MicroForge MicroVM & Serverless Performance Benchmark        ")
	fmt.Println("==========================================================================")
	fmt.Printf("Daemon Target:    %s\n", *daemonAddr)
	fmt.Printf("API Iterations:   %d (Concurrency: %d)\n", *apiIters, *concurrency)
	if !*apiOnly {
		fmt.Printf("Lifecycle Runs:   %d (Full VM Cold-Start, SSH, Runtimes, Snapshot, Resume)\n", *lifecycleRuns)
	} else {
		fmt.Println("Lifecycle Runs:   SKIPPED (--api-only enabled)")
	}
	fmt.Println("==========================================================================")
	fmt.Println()

	client := NewDaemonClient(*daemonAddr)

	// Quick health check to ensure daemon is online
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+*daemonAddr+"/api/v1/health", nil)
	resp, err := http.DefaultClient.Do(req)
	cancel()
	if err != nil || resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "[-] ERROR: Cannot reach MicroForge daemon at %s: %v\n", *daemonAddr, err)
		os.Exit(1)
	}

	report := BenchmarkReport{
		Timestamp:   time.Now().UTC(),
		DaemonAddr:  *daemonAddr,
		Environment: gatherEnvInfo(),
		Metrics:     make(map[string]MetricResult),
	}

	// 1. API Benchmarks
	fmt.Println("--> Running Phase 1: API Latency & Throughput Benchmarks...")
	runAPIBenchmarks(client, *daemonAddr, *apiIters, *concurrency, &report)

	// 2. VM Lifecycle Benchmarks
	if !*apiOnly {
		fmt.Println()
		fmt.Println("--> Running Phase 2: MicroVM Lifecycle & Serverless Runtime Benchmarks...")
		runLifecycleBenchmarks(client, *lifecycleRuns, &report)
	}

	// 3. Print Results Table
	fmt.Println()
	printReportTable(report)

	// 4. Save JSON Report
	if *outputFile != "" {
		saveReportJSON(*outputFile, report)
	}

	// 5. Compare with Baseline if requested
	if *compareFile != "" {
		compareWithBaseline(*compareFile, report)
	}
}

func runAPIBenchmarks(client *DaemonClient, addr string, iterations int, concurrency int, report *BenchmarkReport) {
	// Benchmark: Health check endpoint (single thread)
	healthURL := fmt.Sprintf("http://%s/api/v1/health", addr)
	healthDurations := make([]time.Duration, 0, iterations)
	for i := 0; i < iterations; i++ {
		start := time.Now()
		req, _ := http.NewRequest(http.MethodGet, healthURL, nil)
		r, err := http.DefaultClient.Do(req)
		if err == nil {
			r.Body.Close()
			healthDurations = append(healthDurations, time.Since(start))
		}
	}
	report.Metrics["api_health_latency"] = calculateMetrics("API: GET /health Latency", healthDurations)

	// Benchmark: List VMs endpoint
	listDurations := make([]time.Duration, 0, iterations)
	for i := 0; i < iterations; i++ {
		start := time.Now()
		_, err := client.ListVMs(context.Background())
		if err == nil {
			listDurations = append(listDurations, time.Since(start))
		}
	}
	report.Metrics["api_list_vms_latency"] = calculateMetrics("API: GET /api/v1/vms Latency", listDurations)

	// Benchmark: Concurrent health throughput
	var wg sync.WaitGroup
	concurrentDurations := make([]time.Duration, iterations*concurrency)
	durChan := make(chan time.Duration, iterations*concurrency)

	concurrentStart := time.Now()
	for c := 0; c < concurrency; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				t0 := time.Now()
				req, _ := http.NewRequest(http.MethodGet, healthURL, nil)
				r, err := http.DefaultClient.Do(req)
				if err == nil {
					r.Body.Close()
					durChan <- time.Since(t0)
				}
			}
		}()
	}
	wg.Wait()
	close(durChan)
	totalConcurTime := time.Since(concurrentStart)

	idx := 0
	for d := range durChan {
		concurrentDurations[idx] = d
		idx++
	}
	concurrentDurations = concurrentDurations[:idx]

	m := calculateMetrics("API: Concurrent Health Latency", concurrentDurations)
	if totalConcurTime > 0 {
		m.OpsPerSec = float64(len(concurrentDurations)) / totalConcurTime.Seconds()
	}
	report.Metrics["api_concurrent_health"] = m
}

func runLifecycleBenchmarks(client *DaemonClient, runs int, report *BenchmarkReport) {
	createDurations := make([]time.Duration, 0, runs)
	bootDurations := make([]time.Duration, 0, runs)
	sshReadyDurations := make([]time.Duration, 0, runs)
	sshCommandDurations := make([]time.Duration, 0, runs)
	nodeDurations := make([]time.Duration, 0, runs)
	pythonDurations := make([]time.Duration, 0, runs)
	jqDurations := make([]time.Duration, 0, runs)
	sudoDurations := make([]time.Duration, 0, runs)
	saveStateDurations := make([]time.Duration, 0, runs)
	resumeDurations := make([]time.Duration, 0, runs)
	resumeSSHDurations := make([]time.Duration, 0, runs)
	stopDurations := make([]time.Duration, 0, runs)
	deleteDurations := make([]time.Duration, 0, runs)

	for run := 1; run <= runs; run++ {
		fmt.Printf("   [Lifecycle Run %d/%d] Provisioning, booting, probing, snapshotting, and cleaning...\n", run, runs)

		// 1. Provision VM
		ctx := context.Background()
		t0 := time.Now()
		newVM, err := client.CreateVM(ctx, vm.CreateRequest{
			Name:     fmt.Sprintf("bench-vm-%d", run),
			VCPUs:    1,
			MemoryMB: 512,
		})
		if err != nil {
			fmt.Printf("     [-] Error creating VM: %v\n", err)
			continue
		}
		createDurations = append(createDurations, time.Since(t0))
		vmID := newVM.ID

		// Ensure cleanup at end of iteration if anything fails
		defer func(id string) {
			_ = client.DeleteVM(context.Background(), id)
		}(vmID)

		// 2. Cold Boot VM
		t1 := time.Now()
		_, err = client.RunVM(ctx, vmID)
		if err != nil {
			fmt.Printf("     [-] Error running VM: %v\n", err)
			continue
		}
		bootDurations = append(bootDurations, time.Since(t1))

		// 3. Measure time until SSH is ready
		sshKeyPath := newVM.SSHKeyPath
		if sshKeyPath == "" {
			sshKeyPath = "/var/lib/microforge/images/ssh/id_ed25519"
		}

		tSSHStart := time.Now()
		sshReady := false
		sshDeadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(sshDeadline) {
			cmd := exec.Command("ssh",
				"-i", sshKeyPath,
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "ConnectTimeout=1",
				"-o", "LogLevel=ERROR",
				fmt.Sprintf("mforge@%s", newVM.IPAddress),
				"true",
			)
			if cmd.Run() == nil {
				sshReady = true
				break
			}
			time.Sleep(100 * time.Millisecond)
		}

		if sshReady {
			sshReadyDurations = append(sshReadyDurations, time.Since(tSSHStart))

			// 4. SSH Roundtrip probe
			tProbe := time.Now()
			probeCmd := exec.Command("ssh",
				"-i", sshKeyPath,
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "LogLevel=ERROR",
				fmt.Sprintf("mforge@%s", newVM.IPAddress),
				"uname -a",
			)
			if probeCmd.Run() == nil {
				sshCommandDurations = append(sshCommandDurations, time.Since(tProbe))
			}

			// 5. Serverless Runtimes: Node.js execution
			tNode := time.Now()
			nodeCmd := exec.Command("ssh",
				"-i", sshKeyPath,
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "LogLevel=ERROR",
				fmt.Sprintf("mforge@%s", newVM.IPAddress),
				`node -e 'const res = {status:"ok", val: 40+2}; console.log(JSON.stringify(res))'`,
			)
			if nodeCmd.Run() == nil {
				nodeDurations = append(nodeDurations, time.Since(tNode))
			}

			// 6. Serverless Runtimes: Python3 execution
			tPy := time.Now()
			pyCmd := exec.Command("ssh",
				"-i", sshKeyPath,
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "LogLevel=ERROR",
				fmt.Sprintf("mforge@%s", newVM.IPAddress),
				`python3 -c 'import json; print(json.dumps({"status":"ok", "answer": 42}))'`,
			)
			if pyCmd.Run() == nil {
				pythonDurations = append(pythonDurations, time.Since(tPy))
			}

			// 7. Serverless Tools: jq JSON processing
			tJQ := time.Now()
			jqCmd := exec.Command("ssh",
				"-i", sshKeyPath,
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "LogLevel=ERROR",
				fmt.Sprintf("mforge@%s", newVM.IPAddress),
				`echo '{"msg":"hello","code":200}' | jq .code`,
			)
			if jqCmd.Run() == nil {
				jqDurations = append(jqDurations, time.Since(tJQ))
			}

			// 8. Passwordless sudo execution
			tSudo := time.Now()
			sudoCmd := exec.Command("ssh",
				"-i", sshKeyPath,
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "LogLevel=ERROR",
				fmt.Sprintf("mforge@%s", newVM.IPAddress),
				`sudo whoami`,
			)
			if sudoCmd.Run() == nil {
				sudoDurations = append(sudoDurations, time.Since(tSudo))
			}
		}

		// 9. Save State (Memory snapshot)
		tSave := time.Now()
		_, err = client.SaveState(ctx, vmID)
		if err == nil {
			saveStateDurations = append(saveStateDurations, time.Since(tSave))
		}

		// 10. Instant Resume from Snapshot
		tResume := time.Now()
		_, err = client.ResumeVM(ctx, vmID)
		if err == nil {
			resumeDurations = append(resumeDurations, time.Since(tResume))

			// 11. Time to SSH after resume
			tResumeSSH := time.Now()
			rCmd := exec.Command("ssh",
				"-i", sshKeyPath,
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "LogLevel=ERROR",
				"-o", "ConnectTimeout=2",
				fmt.Sprintf("mforge@%s", newVM.IPAddress),
				"true",
			)
			if rCmd.Run() == nil {
				resumeSSHDurations = append(resumeSSHDurations, time.Since(tResumeSSH))
			}
		}

		// 12. Stop VM
		tStop := time.Now()
		_, _ = client.StopVM(ctx, vmID)
		stopDurations = append(stopDurations, time.Since(tStop))

		// 13. Delete VM
		tDel := time.Now()
		_ = client.DeleteVM(ctx, vmID)
		deleteDurations = append(deleteDurations, time.Since(tDel))
	}

	report.Metrics["vm_creation_latency"] = calculateMetrics("Lifecycle: VM Creation (IPAM+Disk)", createDurations)
	report.Metrics["vm_cold_boot_latency"] = calculateMetrics("Lifecycle: Firecracker Boot", bootDurations)
	report.Metrics["vm_time_to_ssh"] = calculateMetrics("Lifecycle: Cold-Start to SSH Ready", sshReadyDurations)
	report.Metrics["ssh_command_roundtrip"] = calculateMetrics("SSH: Remote Command Roundtrip", sshCommandDurations)
	report.Metrics["serverless_nodejs_exec"] = calculateMetrics("Serverless: Node.js Execution", nodeDurations)
	report.Metrics["serverless_python_exec"] = calculateMetrics("Serverless: Python3 Execution", pythonDurations)
	report.Metrics["serverless_jq_exec"] = calculateMetrics("Serverless: jq JSON Parse", jqDurations)
	report.Metrics["sudo_execution_latency"] = calculateMetrics("Security: Passwordless Sudo", sudoDurations)
	report.Metrics["vm_save_state_latency"] = calculateMetrics("Lifecycle: Snapshot Memory Dump", saveStateDurations)
	report.Metrics["vm_resume_latency"] = calculateMetrics("Lifecycle: Instant Resume Load", resumeDurations)
	report.Metrics["vm_post_resume_ssh"] = calculateMetrics("Lifecycle: Warm Resume SSH Ping", resumeSSHDurations)
	report.Metrics["vm_stop_latency"] = calculateMetrics("Lifecycle: Graceful VM Stop", stopDurations)
	report.Metrics["vm_delete_latency"] = calculateMetrics("Lifecycle: VM Resource Cleanup", deleteDurations)
}

func calculateMetrics(name string, durations []time.Duration) MetricResult {
	n := len(durations)
	if n == 0 {
		return MetricResult{Name: name, Count: 0}
	}

	sort.Slice(durations, func(i, j int) bool {
		return durations[i] < durations[j]
	})

	var sum time.Duration
	for _, d := range durations {
		sum += d
	}
	avg := sum / time.Duration(n)

	p50 := durations[n*50/100]
	p90 := durations[n*90/100]
	p95 := durations[n*95/100]

	// Standard Deviation
	var varianceSum float64
	for _, d := range durations {
		diff := float64(d - avg)
		varianceSum += diff * diff
	}
	stdDev := time.Duration(math.Sqrt(varianceSum / float64(n)))

	opsPerSec := 0.0
	if avg > 0 {
		opsPerSec = 1.0 / avg.Seconds()
	}

	return MetricResult{
		Name:      name,
		Count:     n,
		Min:       durations[0],
		Max:       durations[n-1],
		Avg:       avg,
		P50:       p50,
		P90:       p90,
		P95:       p95,
		StdDev:    stdDev,
		OpsPerSec: opsPerSec,
	}
}

func formatDuration(d time.Duration) string {
	if d == 0 {
		return "N/A"
	}
	if d < time.Millisecond {
		return fmt.Sprintf("%.2f µs", float64(d.Nanoseconds())/1000.0)
	}
	if d < time.Second {
		return fmt.Sprintf("%.2f ms", float64(d.Nanoseconds())/1000000.0)
	}
	return fmt.Sprintf("%.2f s", d.Seconds())
}

func printReportTable(report BenchmarkReport) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "BENCHMARK METRIC\tSAMPLES\tAVG\tMEDIAN (P50)\tP90\tP95\tMIN\tMAX\tOPS/SEC")
	fmt.Fprintln(w, "----------------\t-------\t---\t------------\t---\t---\t---\t---\t-------")

	// Order metric keys
	keys := []string{
		"api_health_latency",
		"api_list_vms_latency",
		"api_concurrent_health",
		"vm_creation_latency",
		"vm_cold_boot_latency",
		"vm_time_to_ssh",
		"ssh_command_roundtrip",
		"serverless_nodejs_exec",
		"serverless_python_exec",
		"serverless_jq_exec",
		"sudo_execution_latency",
		"vm_save_state_latency",
		"vm_resume_latency",
		"vm_post_resume_ssh",
		"vm_stop_latency",
		"vm_delete_latency",
	}

	for _, k := range keys {
		m, ok := report.Metrics[k]
		if !ok || m.Count == 0 {
			continue
		}
		opsStr := "-"
		if m.OpsPerSec > 0 {
			opsStr = fmt.Sprintf("%.1f/s", m.OpsPerSec)
		}
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			m.Name,
			m.Count,
			formatDuration(m.Avg),
			formatDuration(m.P50),
			formatDuration(m.P90),
			formatDuration(m.P95),
			formatDuration(m.Min),
			formatDuration(m.Max),
			opsStr,
		)
	}
	w.Flush()
}

func saveReportJSON(filePath string, report BenchmarkReport) {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error serializing report JSON: %v\n", err)
		return
	}
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing report to %s: %v\n", filePath, err)
		return
	}
	fmt.Printf("\n[+] Benchmark results saved to %s\n", filePath)
}

func compareWithBaseline(baselinePath string, current BenchmarkReport) {
	data, err := os.ReadFile(baselinePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] Could not read baseline file %s: %v\n", baselinePath, err)
		return
	}
	var baseline BenchmarkReport
	if err := json.Unmarshal(data, &baseline); err != nil {
		fmt.Fprintf(os.Stderr, "[-] Could not parse baseline JSON: %v\n", err)
		return
	}

	fmt.Println()
	fmt.Println("==========================================================================")
	fmt.Printf("   REGRESSION COMPARISON: Current vs Baseline (%s)\n", filepath.Base(baselinePath))
	fmt.Println("==========================================================================")

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "METRIC\tBASELINE AVG\tCURRENT AVG\tDELTA (%)\tSTATUS")
	fmt.Fprintln(w, "------\t------------\t-----------\t---------\t------")

	var improvements, regressions, neutral int

	for key, currMetric := range current.Metrics {
		baseMetric, exists := baseline.Metrics[key]
		if !exists || baseMetric.Count == 0 || currMetric.Count == 0 {
			continue
		}

		baseAvg := float64(baseMetric.Avg.Nanoseconds())
		currAvg := float64(currMetric.Avg.Nanoseconds())

		if baseAvg <= 0 {
			continue
		}

		diffPercent := ((currAvg - baseAvg) / baseAvg) * 100.0
		var status string
		if diffPercent < -5.0 {
			status = "FASTER (Improved)"
			improvements++
		} else if diffPercent > 5.0 {
			status = "SLOWER (Regressed)"
			regressions++
		} else {
			status = "SIMILAR"
			neutral++
		}

		diffStr := fmt.Sprintf("%+.1f%%", diffPercent)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			currMetric.Name,
			formatDuration(baseMetric.Avg),
			formatDuration(currMetric.Avg),
			diffStr,
			status,
		)
	}
	w.Flush()

	fmt.Println("--------------------------------------------------------------------------")
	fmt.Printf("Summary: %d metrics improved, %d regressed, %d similar\n", improvements, regressions, neutral)
	if regressions > 0 {
		fmt.Printf("⚠️  Performance alert: %d metrics experienced latency regressions (> 5%% slower).\n", regressions)
	} else {
		fmt.Println("✅ No performance regressions detected! All benchmarks within acceptable limits.")
	}
}

func gatherEnvInfo() map[string]string {
	env := make(map[string]string)
	if hostname, err := os.Hostname(); err == nil {
		env["hostname"] = hostname
	}
	if out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output(); err == nil {
		env["git_commit"] = strings.TrimSpace(string(out))
	}
	if out, err := exec.Command("uname", "-sr").Output(); err == nil {
		env["kernel"] = strings.TrimSpace(string(out))
	}
	return env
}

// Client helper
type DaemonClient struct {
	baseURL    string
	httpClient *http.Client
}

func NewDaemonClient(endpoint string) *DaemonClient {
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "http://" + endpoint
	}
	return &DaemonClient{
		baseURL:    strings.TrimSuffix(endpoint, "/"),
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *DaemonClient) doRequest(ctx context.Context, method, path string, body interface{}, result interface{}) error {
	var bodyReader *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(data)
	} else {
		bodyReader = bytes.NewReader(nil)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("daemon returned status %d", resp.StatusCode)
	}

	if result != nil {
		return json.NewDecoder(resp.Body).Decode(result)
	}
	return nil
}

func (c *DaemonClient) CreateVM(ctx context.Context, req vm.CreateRequest) (*db.VM, error) {
	var created db.VM
	err := c.doRequest(ctx, http.MethodPost, "/api/v1/vms", req, &created)
	return &created, err
}

func (c *DaemonClient) ListVMs(ctx context.Context) ([]*db.VM, error) {
	var list []*db.VM
	err := c.doRequest(ctx, http.MethodGet, "/api/v1/vms", nil, &list)
	return list, err
}

func (c *DaemonClient) RunVM(ctx context.Context, id string) (*db.VM, error) {
	var v db.VM
	err := c.doRequest(ctx, http.MethodPost, "/api/v1/vms/"+id+"/run", nil, &v)
	return &v, err
}

func (c *DaemonClient) SaveState(ctx context.Context, id string) (*db.VM, error) {
	var v db.VM
	err := c.doRequest(ctx, http.MethodPost, "/api/v1/vms/"+id+"/save-state", nil, &v)
	return &v, err
}

func (c *DaemonClient) ResumeVM(ctx context.Context, id string) (*db.VM, error) {
	var v db.VM
	err := c.doRequest(ctx, http.MethodPost, "/api/v1/vms/"+id+"/resume", nil, &v)
	return &v, err
}

func (c *DaemonClient) StopVM(ctx context.Context, id string) (*db.VM, error) {
	var v db.VM
	err := c.doRequest(ctx, http.MethodPost, "/api/v1/vms/"+id+"/stop", nil, &v)
	return &v, err
}

func (c *DaemonClient) DeleteVM(ctx context.Context, id string) error {
	return c.doRequest(ctx, http.MethodDelete, "/api/v1/vms/"+id, nil, nil)
}
