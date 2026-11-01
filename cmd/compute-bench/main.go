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

type HostVsVMComparison struct {
	MetricName     string  `json:"metric_name"`
	Category       string  `json:"category"` // "CPU", "Memory", "Disk", "Network"
	Unit           string  `json:"unit"`
	HostValue      float64 `json:"host_value"`
	VMValue        float64 `json:"vm_value"`
	HigherIsBetter bool    `json:"higher_is_better"`
	OverheadPct    float64 `json:"overhead_pct"`
	Ratio          float64 `json:"ratio"`
	Summary        string  `json:"summary"`
}

type HostVsVMSummary struct {
	Host        WorkerResult         `json:"host"`
	VM          WorkerResult         `json:"vm"`
	Comparisons []HostVsVMComparison `json:"comparisons"`
}

type BenchmarkReport struct {
	Timestamp   time.Time               `json:"timestamp"`
	DaemonAddr  string                  `json:"daemon_addr"`
	GitCommit   string                  `json:"git_commit,omitempty"`
	Environment map[string]string       `json:"environment"`
	HostVsVM    *HostVsVMSummary        `json:"host_vs_vm,omitempty"`
	Metrics     map[string]MetricResult `json:"metrics"`
}

func main() {
	daemonAddr := flag.String("daemon", "127.0.0.1:18080", "Daemon address (e.g. 127.0.0.1:18080)")
	apiIters := flag.Int("api-iterations", 50, "Number of iterations for API benchmarks")
	lifecycleRuns := flag.Int("lifecycle-runs", 2, "Number of runs for full VM lifecycle benchmarks")
	concurrency := flag.Int("concurrency", 10, "Concurrency level for API stress benchmarks")
	apiOnly := flag.Bool("api-only", false, "Run only API latency benchmarks (skip VM lifecycle)")
	skipComparison := flag.Bool("skip-comparison", false, "Skip Host vs VM performance comparison")
	outputFile := flag.String("output", "benchmark-results.json", "JSON file to save benchmark results")
	htmlFile := flag.String("html", "benchmark-report.html", "HTML report file to save visual dashboard")
	compareFile := flag.String("compare", "", "Path to previous baseline benchmark JSON file for comparison")
	sshKeyPath := flag.String("ssh-key", "/var/lib/compute/images/ssh/id_ed25519", "Path to SSH private key")

	// Worker mode flags
	isWorker := flag.Bool("worker", false, "Execute in worker mode (runs micro-benchmarks on current host/VM)")
	workerTarget := flag.String("target", "host", "Target identifier in worker mode ('host' or 'vm')")
	scratchDir := flag.String("scratch-dir", "", "Scratch directory for worker disk benchmarks")
	netServerPort := flag.Int("net-server", 0, "Run TCP network server on specified port")
	netClientTarget := flag.String("net-client", "", "Run TCP network client against specified target host:port")
	netDataMB := flag.Int("net-mb", 32, "Megabytes to transmit in network test")

	flag.Parse()

	// Handle Network Worker Server
	if *netServerPort > 0 {
		if err := RunNetworkServer(*netServerPort); err != nil {
			fmt.Fprintf(os.Stderr, "Error running net server: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Handle Network Worker Client
	if *netClientTarget != "" {
		mbps, err := RunNetworkClient(*netClientTarget, *netDataMB)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error running net client: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("{\"network_throughput_mbps\": %.2f}\n", mbps)
		return
	}

	// Handle Worker Mode execution
	if *isWorker {
		res, err := RunWorkerBenchmarks(*workerTarget, *scratchDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Worker error: %v\n", err)
			os.Exit(1)
		}
		data, _ := json.Marshal(res)
		fmt.Println(string(data))
		return
	}

	// Master Orchestrator Mode
	if env := os.Getenv("COMPUTE_DAEMON"); env != "" && *daemonAddr == "127.0.0.1:18080" {
		*daemonAddr = env
	}

	fmt.Println("==========================================================================")
	fmt.Println("             Compute MicroVM & Serverless Performance Benchmark        ")
	fmt.Println("==========================================================================")
	fmt.Printf("Daemon Target:    %s\n", *daemonAddr)
	fmt.Printf("API Iterations:   %d (Concurrency: %d)\n", *apiIters, *concurrency)
	if !*apiOnly {
		fmt.Printf("Lifecycle Runs:   %d (Cold-Start, Start-to-Exec, Snapshot, Resume, Stop)\n", *lifecycleRuns)
	} else {
		fmt.Println("Lifecycle Runs:   SKIPPED (--api-only enabled)")
	}
	fmt.Printf("Host vs VM Bench: %t\n", !*skipComparison && !*apiOnly)
	fmt.Println("==========================================================================")
	fmt.Println()

	client := NewDaemonClient(*daemonAddr)

	// Quick health check to ensure daemon is online
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+*daemonAddr+"/api/v1/health", nil)
	resp, err := http.DefaultClient.Do(req)
	cancel()
	if err != nil || resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "[-] ERROR: Cannot reach Compute daemon at %s: %v\n", *daemonAddr, err)
		os.Exit(1)
	}

	report := BenchmarkReport{
		Timestamp:   time.Now().UTC(),
		DaemonAddr:  *daemonAddr,
		Environment: gatherEnvInfo(),
		Metrics:     make(map[string]MetricResult),
	}

	// 1. API Latency & Request Throughput Benchmarks
	fmt.Println("--> Running Phase 1: API Latency & Request Throughput Benchmarks...")
	runAPIBenchmarks(client, *daemonAddr, *apiIters, *concurrency, &report)

	// 2. MicroVM Lifecycle & Start-to-Execution Benchmarks
	if !*apiOnly {
		fmt.Println()
		fmt.Println("--> Running Phase 2: MicroVM Lifecycle & Start-to-Execution Benchmarks...")
		runLifecycleBenchmarks(client, *lifecycleRuns, *sshKeyPath, &report)

		// 3. Host vs MicroVM Direct Performance Comparison
		if !*skipComparison {
			fmt.Println()
			fmt.Println("--> Running Phase 3: Host vs MicroVM Direct Performance Comparison...")
			runHostVsVMBenchmarks(client, *sshKeyPath, &report)
		}

		// 4. Throughput Benchmarks (Command execution rate & Virtual Network MB/s)
		fmt.Println()
		fmt.Println("--> Running Phase 4: In-VM Execution & Network TAP Throughput Benchmarks...")
		runThroughputBenchmarks(client, *sshKeyPath, &report)
	}

	// 5. Print Results Tables
	fmt.Println()
	printReportTable(report)

	if report.HostVsVM != nil {
		fmt.Println()
		printHostVsVMTable(report.HostVsVM)
	}

	// 6. Save JSON Report
	if *outputFile != "" {
		saveReportJSON(*outputFile, report)
	}

	// 7. Generate Standalone HTML Dashboard Report
	if *htmlFile != "" {
		if err := GenerateHTMLReport(*htmlFile, report); err == nil {
			fmt.Printf("[+] Interactive HTML report saved to %s\n", *htmlFile)
		} else {
			fmt.Fprintf(os.Stderr, "[-] Error generating HTML report: %v\n", err)
		}
	}

	// 8. Compare with Baseline if requested
	if *compareFile != "" {
		compareWithBaseline(*compareFile, report)
	}
}

func runAPIBenchmarks(client *DaemonClient, addr string, iterations int, concurrency int, report *BenchmarkReport) {
	healthURL := fmt.Sprintf("http://%s/api/v1/health", addr)
	listURL := fmt.Sprintf("http://%s/api/v1/vms", addr)

	// Single thread health latency
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

	// Single thread list VMs latency
	listDurations := make([]time.Duration, 0, iterations)
	for i := 0; i < iterations; i++ {
		start := time.Now()
		_, err := client.ListVMs(context.Background())
		if err == nil {
			listDurations = append(listDurations, time.Since(start))
		}
	}
	report.Metrics["api_list_vms_latency"] = calculateMetrics("API: GET /api/v1/vms Latency", listDurations)

	// Concurrent Health Throughput
	var wg sync.WaitGroup
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

	concurrentDurations := make([]time.Duration, 0, iterations*concurrency)
	for d := range durChan {
		concurrentDurations = append(concurrentDurations, d)
	}

	mHealth := calculateMetrics("API: Concurrent Health Latency", concurrentDurations)
	if totalConcurTime > 0 {
		mHealth.OpsPerSec = float64(len(concurrentDurations)) / totalConcurTime.Seconds()
	}
	report.Metrics["api_concurrent_health"] = mHealth

	// Concurrent List VMs Throughput
	var wgList sync.WaitGroup
	listChan := make(chan time.Duration, iterations*concurrency)
	concurListStart := time.Now()
	for c := 0; c < concurrency; c++ {
		wgList.Add(1)
		go func() {
			defer wgList.Done()
			for i := 0; i < iterations; i++ {
				t0 := time.Now()
				req, _ := http.NewRequest(http.MethodGet, listURL, nil)
				r, err := http.DefaultClient.Do(req)
				if err == nil {
					r.Body.Close()
					listChan <- time.Since(t0)
				}
			}
		}()
	}
	wgList.Wait()
	close(listChan)
	totalListTime := time.Since(concurListStart)

	concurrentListDurations := make([]time.Duration, 0, iterations*concurrency)
	for d := range listChan {
		concurrentListDurations = append(concurrentListDurations, d)
	}

	mList := calculateMetrics("API: Concurrent List VMs Latency", concurrentListDurations)
	if totalListTime > 0 {
		mList.OpsPerSec = float64(len(concurrentListDurations)) / totalListTime.Seconds()
	}
	report.Metrics["api_concurrent_list_vms"] = mList
}

func runLifecycleBenchmarks(client *DaemonClient, runs int, sshKeyPath string, report *BenchmarkReport) {
	createDurations := make([]time.Duration, 0, runs)
	bootDurations := make([]time.Duration, 0, runs)
	sshReadyDurations := make([]time.Duration, 0, runs)
	coldStartToExecDurations := make([]time.Duration, 0, runs)
	sshCommandDurations := make([]time.Duration, 0, runs)
	nodeDurations := make([]time.Duration, 0, runs)
	pythonDurations := make([]time.Duration, 0, runs)
	jqDurations := make([]time.Duration, 0, runs)
	sudoDurations := make([]time.Duration, 0, runs)
	saveStateDurations := make([]time.Duration, 0, runs)
	resumeDurations := make([]time.Duration, 0, runs)
	warmResumeToExecDurations := make([]time.Duration, 0, runs)
	resumeSSHDurations := make([]time.Duration, 0, runs)
	stopDurations := make([]time.Duration, 0, runs)
	deleteDurations := make([]time.Duration, 0, runs)

	for run := 1; run <= runs; run++ {
		fmt.Printf("   [Lifecycle Run %d/%d] Measuring cold boot, start-to-exec, snapshot save/restore, and cleanup...\n", run, runs)

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

		keyPath := newVM.SSHKeyPath
		if keyPath == "" {
			keyPath = sshKeyPath
		}

		// Ensure cleanup at end of iteration if anything fails
		defer func(id string) {
			_ = client.DeleteVM(context.Background(), id)
		}(vmID)

		// 2. Cold Boot VM & Measure Start-to-Execution
		tColdStart := time.Now()
		_, err = client.RunVM(ctx, vmID)
		if err != nil {
			fmt.Printf("     [-] Error running VM: %v\n", err)
			continue
		}
		bootDurations = append(bootDurations, time.Since(tColdStart))

		// 3. Poll SSH until port is ready & execute first guest command
		tSSHStart := time.Now()
		sshReady := false
		sshDeadline := time.Now().Add(25 * time.Second)
		for time.Now().Before(sshDeadline) {
			cmd := exec.Command("ssh",
				"-i", keyPath,
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "ConnectTimeout=1",
				"-o", "LogLevel=ERROR",
				fmt.Sprintf("compute@%s", newVM.IPAddress),
				"true",
			)
			if cmd.Run() == nil {
				sshReady = true
				break
			}
			time.Sleep(50 * time.Millisecond)
		}

		if sshReady {
			sshReadyDurations = append(sshReadyDurations, time.Since(tSSHStart))
			coldStartToExec := time.Since(tColdStart)
			coldStartToExecDurations = append(coldStartToExecDurations, coldStartToExec)

			// 4. SSH Roundtrip probe
			tProbe := time.Now()
			probeCmd := exec.Command("ssh",
				"-i", keyPath,
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "LogLevel=ERROR",
				fmt.Sprintf("compute@%s", newVM.IPAddress),
				"uname -a",
			)
			if probeCmd.Run() == nil {
				sshCommandDurations = append(sshCommandDurations, time.Since(tProbe))
			}

			// 5. Node.js execution inside VM
			tNode := time.Now()
			nodeCmd := exec.Command("ssh",
				"-i", keyPath,
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "LogLevel=ERROR",
				fmt.Sprintf("compute@%s", newVM.IPAddress),
				`node -e 'const res = {status:"ok", val: 40+2}; console.log(JSON.stringify(res))'`,
			)
			if nodeCmd.Run() == nil {
				nodeDurations = append(nodeDurations, time.Since(tNode))
			}

			// 6. Python3 execution inside VM
			tPy := time.Now()
			pyCmd := exec.Command("ssh",
				"-i", keyPath,
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "LogLevel=ERROR",
				fmt.Sprintf("compute@%s", newVM.IPAddress),
				`python3 -c 'import json; print(json.dumps({"status":"ok", "answer": 42}))'`,
			)
			if pyCmd.Run() == nil {
				pythonDurations = append(pythonDurations, time.Since(tPy))
			}

			// 7. JQ tool execution inside VM
			tJQ := time.Now()
			jqCmd := exec.Command("ssh",
				"-i", keyPath,
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "LogLevel=ERROR",
				fmt.Sprintf("compute@%s", newVM.IPAddress),
				`echo '{"msg":"hello","code":200}' | jq .code`,
			)
			if jqCmd.Run() == nil {
				jqDurations = append(jqDurations, time.Since(tJQ))
			}

			// 8. Passwordless sudo execution
			tSudo := time.Now()
			sudoCmd := exec.Command("ssh",
				"-i", keyPath,
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "LogLevel=ERROR",
				fmt.Sprintf("compute@%s", newVM.IPAddress),
				`sudo whoami`,
			)
			if sudoCmd.Run() == nil {
				sudoDurations = append(sudoDurations, time.Since(tSudo))
			}
		}

		// 9. Snapshot: Save State (Memory dump + snapshot serialization)
		tSave := time.Now()
		_, err = client.SaveState(ctx, vmID)
		if err == nil {
			saveStateDurations = append(saveStateDurations, time.Since(tSave))
		}

		// 10. Snapshot: Warm Resume from Snapshot & Measure Warm Start-to-Execution
		tWarmStart := time.Now()
		_, err = client.ResumeVM(ctx, vmID)
		if err == nil {
			resumeDurations = append(resumeDurations, time.Since(tWarmStart))

			// 11. Time to execute after resume
			tResumeSSH := time.Now()
			warmSSHReady := false
			warmDeadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(warmDeadline) {
				rCmd := exec.Command("ssh",
					"-i", keyPath,
					"-o", "StrictHostKeyChecking=no",
					"-o", "UserKnownHostsFile=/dev/null",
					"-o", "LogLevel=ERROR",
					"-o", "ConnectTimeout=1",
					fmt.Sprintf("compute@%s", newVM.IPAddress),
					"uptime",
				)
				if rCmd.Run() == nil {
					warmSSHReady = true
					break
				}
				time.Sleep(30 * time.Millisecond)
			}
			if warmSSHReady {
				resumeSSHDurations = append(resumeSSHDurations, time.Since(tResumeSSH))
				warmResumeToExecDurations = append(warmResumeToExecDurations, time.Since(tWarmStart))
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
	report.Metrics["vm_time_to_ssh"] = calculateMetrics("Lifecycle: Boot to SSH Ready", sshReadyDurations)
	report.Metrics["cold_start_to_execution"] = calculateMetrics("Start-to-Exec: Cold Start to Execution", coldStartToExecDurations)
	report.Metrics["ssh_command_roundtrip"] = calculateMetrics("SSH: Remote Command Roundtrip", sshCommandDurations)
	report.Metrics["serverless_nodejs_exec"] = calculateMetrics("Serverless: Node.js Execution", nodeDurations)
	report.Metrics["serverless_python_exec"] = calculateMetrics("Serverless: Python3 Execution", pythonDurations)
	report.Metrics["serverless_jq_exec"] = calculateMetrics("Serverless: jq JSON Parse", jqDurations)
	report.Metrics["sudo_execution_latency"] = calculateMetrics("Security: Passwordless Sudo", sudoDurations)
	report.Metrics["vm_save_state_latency"] = calculateMetrics("Snapshot: Memory Dump + State Save", saveStateDurations)
	report.Metrics["vm_resume_latency"] = calculateMetrics("Snapshot: Warm Resume from State", resumeDurations)
	report.Metrics["vm_post_resume_ssh"] = calculateMetrics("Snapshot: Post-Resume SSH Ping", resumeSSHDurations)
	report.Metrics["warm_resume_to_execution"] = calculateMetrics("Start-to-Exec: Warm Resume to Execution", warmResumeToExecDurations)
	report.Metrics["vm_stop_latency"] = calculateMetrics("Lifecycle: Graceful VM Stop", stopDurations)
	report.Metrics["vm_delete_latency"] = calculateMetrics("Lifecycle: VM Resource Cleanup", deleteDurations)
}

func runHostVsVMBenchmarks(client *DaemonClient, sshKeyPath string, report *BenchmarkReport) {
	fmt.Println("   --> Executing native worker micro-benchmarks on Host...")
	hostWorkerRes, err := RunWorkerBenchmarks("host", "")
	if err != nil {
		fmt.Printf("   [-] Host worker error: %v\n", err)
		return
	}

	fmt.Println("   --> Launching dedicated MicroVM to run guest micro-benchmarks...")
	ctx := context.Background()
	testVM, err := client.CreateVM(ctx, vm.CreateRequest{
		Name:     "bench-perf-compare",
		VCPUs:    1,
		MemoryMB: 512,
	})
	if err != nil {
		fmt.Printf("   [-] Error creating comparison VM: %v\n", err)
		return
	}
	defer func() {
		_, _ = client.StopVM(context.Background(), testVM.ID)
		_ = client.DeleteVM(context.Background(), testVM.ID)
	}()

	if _, err := client.RunVM(ctx, testVM.ID); err != nil {
		fmt.Printf("   [-] Error starting comparison VM: %v\n", err)
		return
	}

	keyPath := testVM.SSHKeyPath
	if keyPath == "" {
		keyPath = sshKeyPath
	}

	// Wait for SSH
	sshDeadline := time.Now().Add(25 * time.Second)
	sshOnline := false
	for time.Now().Before(sshDeadline) {
		cmd := exec.Command("ssh",
			"-i", keyPath,
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "ConnectTimeout=1",
			fmt.Sprintf("compute@%s", testVM.IPAddress),
			"true",
		)
		if cmd.Run() == nil {
			sshOnline = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !sshOnline {
		fmt.Printf("   [-] SSH timed out on comparison VM %s\n", testVM.IPAddress)
		return
	}

	// Deploy worker binary to VM via SCP
	selfBin, err := os.Executable()
	if err != nil {
		selfBin = os.Args[0]
	}
	fmt.Printf("   --> Copying worker executable (%s) to MicroVM...\n", filepath.Base(selfBin))
	scpCmd := exec.Command("scp",
		"-i", keyPath,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		selfBin,
		fmt.Sprintf("compute@%s:/tmp/compute-bench-worker", testVM.IPAddress),
	)
	if out, err := scpCmd.CombinedOutput(); err != nil {
		fmt.Printf("   [-] SCP error: %v, output: %s\n", err, string(out))
		return
	}

	// Make executable & run worker inside VM
	fmt.Println("   --> Executing guest micro-benchmarks inside MicroVM...")
	workerExecCmd := exec.Command("ssh",
		"-i", keyPath,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		fmt.Sprintf("compute@%s", testVM.IPAddress),
		"chmod +x /tmp/compute-bench-worker && /tmp/compute-bench-worker --worker --target=vm",
	)
	out, err := workerExecCmd.Output()
	if err != nil {
		fmt.Printf("   [-] Worker execution failed inside VM: %v\n", err)
		return
	}

	var vmWorkerRes WorkerResult
	// Find JSON line
	lines := strings.Split(string(out), "\n")
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "{") && strings.HasSuffix(l, "}") {
			if err := json.Unmarshal([]byte(l), &vmWorkerRes); err == nil {
				break
			}
		}
	}

	if vmWorkerRes.Target == "" {
		fmt.Printf("   [-] Could not parse VM worker JSON output: %s\n", string(out))
		return
	}

	// Build Comparisons
	summary := &HostVsVMSummary{
		Host:        hostWorkerRes,
		VM:          vmWorkerRes,
		Comparisons: buildComparisons(hostWorkerRes, vmWorkerRes),
	}
	report.HostVsVM = summary
}

func buildComparisons(h, v WorkerResult) []HostVsVMComparison {
	calc := func(name, cat, unit string, hVal, vVal float64, higherBetter bool) HostVsVMComparison {
		var overhead float64
		var ratio float64
		if higherBetter {
			if hVal > 0 {
				overhead = ((hVal - vVal) / hVal) * 100.0
				ratio = vVal / hVal
			}
		} else {
			if hVal > 0 {
				overhead = ((vVal - hVal) / hVal) * 100.0
				ratio = vVal / hVal
			}
		}

		var summary string
		if math.Abs(overhead) <= 5.0 {
			summary = "Near-Native (< 5% delta)"
		} else if overhead < 0 {
			summary = "VM Faster"
		} else if overhead <= 15.0 {
			summary = "Low Overhead (< 15%)"
		} else if overhead <= 30.0 {
			summary = "Moderate Virtualization Tax"
		} else {
			summary = "High Virtualization Overhead"
		}

		return HostVsVMComparison{
			MetricName:     name,
			Category:       cat,
			Unit:           unit,
			HostValue:      hVal,
			VMValue:        vVal,
			HigherIsBetter: higherBetter,
			OverheadPct:    overhead,
			Ratio:          ratio,
			Summary:        summary,
		}
	}

	return []HostVsVMComparison{
		calc("SHA-256 Hashing Throughput", "CPU", "MB/s", h.CPUSHA256MBps, v.CPUSHA256MBps, true),
		calc("Prime Sieve Calculation", "CPU", "ms", h.CPUPrimeDurationMs, v.CPUPrimeDurationMs, false),
		calc("Prime Sieve Rate", "CPU", "ops/s", h.CPUPrimeOpsPerSec, v.CPUPrimeOpsPerSec, true),
		calc("Matrix Math Operations", "CPU", "MFLOPS", h.CPUMatrixMFLOPS, v.CPUMatrixMFLOPS, true),
		calc("Memory Sequential Access", "Memory", "MB/s", h.MemSeqBandwidthMBps, v.MemSeqBandwidthMBps, true),
		calc("Memory Memcpy Bandwidth", "Memory", "MB/s", h.MemCopyBandwidthMBps, v.MemCopyBandwidthMBps, true),
		calc("Disk Sequential Write (Sync)", "Disk", "MB/s", h.DiskSeqWriteMBps, v.DiskSeqWriteMBps, true),
		calc("Disk Sequential Read", "Disk", "MB/s", h.DiskSeqReadMBps, v.DiskSeqReadMBps, true),
		calc("Disk Random 4K IOPS", "Disk", "IOPS", h.DiskRandom4KIOPS, v.DiskRandom4KIOPS, true),
		calc("Disk Random 4K Avg Latency", "Disk", "ms", h.DiskRandom4KAvgMs, v.DiskRandom4KAvgMs, false),
	}
}

func runThroughputBenchmarks(client *DaemonClient, sshKeyPath string, report *BenchmarkReport) {
	// Find or provision an active VM for throughput tests
	vms, err := client.ListVMs(context.Background())
	if err != nil || len(vms) == 0 {
		return
	}

	var targetVM *db.VM
	for _, v := range vms {
		if v.Status == db.StatusRunning {
			targetVM = v
			break
		}
	}

	var createdTemp bool
	if targetVM == nil {
		testVM, err := client.CreateVM(context.Background(), vm.CreateRequest{
			Name:     "bench-throughput-vm",
			VCPUs:    1,
			MemoryMB: 512,
		})
		if err == nil {
			if _, err := client.RunVM(context.Background(), testVM.ID); err == nil {
				targetVM = testVM
				createdTemp = true
				time.Sleep(1 * time.Second)
			}
		}
	}

	if targetVM == nil {
		return
	}
	defer func() {
		if createdTemp {
			_, _ = client.StopVM(context.Background(), targetVM.ID)
			_ = client.DeleteVM(context.Background(), targetVM.ID)
		}
	}()

	keyPath := targetVM.SSHKeyPath
	if keyPath == "" {
		keyPath = sshKeyPath
	}

	// 1. In-VM Command Execution Throughput (30 consecutive commands)
	cmdCount := 30
	cmdDurations := make([]time.Duration, 0, cmdCount)
	tCmdStart := time.Now()
	for i := 0; i < cmdCount; i++ {
		t0 := time.Now()
		c := exec.Command("ssh",
			"-i", keyPath,
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "LogLevel=ERROR",
			fmt.Sprintf("compute@%s", targetVM.IPAddress),
			"true",
		)
		if c.Run() == nil {
			cmdDurations = append(cmdDurations, time.Since(t0))
		}
	}
	tTotalCmd := time.Since(tCmdStart)
	mExec := calculateMetrics("Throughput: In-VM Remote Command Exec", cmdDurations)
	if tTotalCmd.Seconds() > 0 {
		mExec.OpsPerSec = float64(len(cmdDurations)) / tTotalCmd.Seconds()
	}
	report.Metrics["vm_command_execution_throughput"] = mExec

	// 2. Virtual TAP Network Throughput
	// Launch net server inside VM on port 19999
	_ = exec.Command("ssh",
		"-i", keyPath,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		fmt.Sprintf("compute@%s", targetVM.IPAddress),
		"pkill -f compute-bench-worker || true",
	).Run()

	netSrvCmd := exec.Command("ssh",
		"-i", keyPath,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		fmt.Sprintf("compute@%s", targetVM.IPAddress),
		"/tmp/compute-bench-worker --net-server=19999",
	)
	_ = netSrvCmd.Start()
	time.Sleep(200 * time.Millisecond)

	// Send 32MB from Host to VM
	netMBps, netErr := RunNetworkClient(fmt.Sprintf("%s:19999", targetVM.IPAddress), 32)
	_ = netSrvCmd.Wait()

	if netErr == nil && netMBps > 0 {
		report.Metrics["virtual_tap_network_throughput"] = MetricResult{
			Name:        "Throughput: Host <-> VM Virtual Network (TAP)",
			Count:       1,
			Avg:         time.Duration(1e9 / netMBps),
			OpsPerSec:   netMBps, // MB/s stored here for unified table
			Description: fmt.Sprintf("%.2f MB/s streaming data throughput", netMBps),
		}
	}
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

	keys := []string{
		"api_health_latency",
		"api_list_vms_latency",
		"api_concurrent_health",
		"api_concurrent_list_vms",
		"vm_creation_latency",
		"vm_cold_boot_latency",
		"vm_time_to_ssh",
		"cold_start_to_execution",
		"ssh_command_roundtrip",
		"serverless_nodejs_exec",
		"serverless_python_exec",
		"serverless_jq_exec",
		"sudo_execution_latency",
		"vm_save_state_latency",
		"vm_resume_latency",
		"vm_post_resume_ssh",
		"warm_resume_to_execution",
		"vm_stop_latency",
		"vm_delete_latency",
		"vm_command_execution_throughput",
		"virtual_tap_network_throughput",
	}

	for _, k := range keys {
		m, ok := report.Metrics[k]
		if !ok || m.Count == 0 {
			continue
		}
		opsStr := "-"
		if m.OpsPerSec > 0 {
			if k == "virtual_tap_network_throughput" {
				opsStr = fmt.Sprintf("%.1f MB/s", m.OpsPerSec)
			} else {
				opsStr = fmt.Sprintf("%.1f/s", m.OpsPerSec)
			}
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

func printHostVsVMTable(summary *HostVsVMSummary) {
	fmt.Println("==========================================================================")
	fmt.Println("       DIRECT PERFORMANCE COMPARISON: HOST NATIVE vs MICROVM GUEST        ")
	fmt.Println("==========================================================================")

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "CATEGORY\tWORKLOAD / METRIC\tHOST NATIVE\tMICROVM\tOVERHEAD (%)\tRATIO\tSTATUS")
	fmt.Fprintln(w, "--------\t-----------------\t-----------\t-------\t------------\t-----\t------")

	for _, c := range summary.Comparisons {
		overheadStr := fmt.Sprintf("%+.1f%%", c.OverheadPct)
		ratioStr := fmt.Sprintf("%.2fx", c.Ratio)
		hostValStr := fmt.Sprintf("%.1f %s", c.HostValue, c.Unit)
		vmValStr := fmt.Sprintf("%.1f %s", c.VMValue, c.Unit)

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			c.Category,
			c.MetricName,
			hostValStr,
			vmValStr,
			overheadStr,
			ratioStr,
			c.Summary,
		)
	}
	w.Flush()
	fmt.Println("--------------------------------------------------------------------------")
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
