package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// GenerateHTMLReport produces a standalone, self-contained HTML dashboard with embedded charts
func GenerateHTMLReport(filePath string, report BenchmarkReport) error {
	reportJSON, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("failed to serialize report for HTML: %w", err)
	}

	htmlContent := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Compute Performance Benchmark &amp; Telemetry</title>
  <script src="https://cdn.jsdelivr.net/npm/chart.js@4.4.1/dist/chart.umd.min.js"></script>
  <style>
    :root {
      --bg: #090d16;
      --card-bg: #111827;
      --card-border: #1f2937;
      --text: #f3f4f6;
      --text-muted: #9ca3af;
      --primary: #3b82f6;
      --primary-light: #60a5fa;
      --success: #10b981;
      --warning: #f59e0b;
      --danger: #ef4444;
      --accent: #8b5cf6;
      --cyan: #06b6d4;
    }
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
      background-color: var(--bg);
      color: var(--text);
      line-height: 1.5;
      padding: 24px;
    }
    .container { max-width: 1400px; margin: 0 auto; }
    header {
      display: flex;
      justify-content: space-between;
      align-items: center;
      padding-bottom: 24px;
      border-bottom: 1px solid var(--card-border);
      margin-bottom: 24px;
      flex-wrap: wrap;
      gap: 16px;
    }
    h1 { font-size: 1.85rem; font-weight: 700; color: #fff; display: flex; align-items: center; gap: 10px; }
    .badge {
      display: inline-block;
      padding: 4px 10px;
      border-radius: 9999px;
      font-size: 0.75rem;
      font-weight: 600;
      background: rgba(59, 130, 246, 0.2);
      color: var(--primary-light);
      border: 1px solid rgba(59, 130, 246, 0.4);
    }
    .grid { display: grid; gap: 20px; }
    .grid-cols-4 { grid-template-columns: repeat(auto-fit, minmax(260px, 1fr)); }
    .grid-cols-2 { grid-template-columns: repeat(auto-fit, minmax(500px, 1fr)); }
    .card {
      background: var(--card-bg);
      border: 1px solid var(--card-border);
      border-radius: 12px;
      padding: 20px;
      position: relative;
    }
    .metric-title { font-size: 0.825rem; font-weight: 600; text-transform: uppercase; letter-spacing: 0.05em; color: var(--text-muted); margin-bottom: 8px; }
    .metric-value { font-size: 1.9rem; font-weight: 700; color: #fff; font-feature-settings: "tnum"; }
    .metric-sub { font-size: 0.8rem; color: var(--text-muted); margin-top: 6px; }
    .chart-container { position: relative; height: 320px; width: 100%%; margin-top: 10px; }
    table { width: 100%%; border-collapse: collapse; text-align: left; font-size: 0.875rem; }
    th { padding: 12px 14px; background: rgba(31, 41, 55, 0.6); color: var(--text-muted); font-weight: 600; border-bottom: 1px solid var(--card-border); }
    td { padding: 12px 14px; border-bottom: 1px solid rgba(31, 41, 55, 0.5); font-variant-numeric: tabular-nums; }
    tr:hover { background: rgba(55, 65, 81, 0.25); }
    .text-success { color: var(--success); font-weight: 600; }
    .text-warning { color: var(--warning); font-weight: 600; }
    .text-danger { color: var(--danger); font-weight: 600; }
    .text-cyan { color: var(--cyan); font-weight: 600; }
    .tag { padding: 2px 8px; border-radius: 6px; font-size: 0.75rem; font-weight: 600; }
    .tag-cpu { background: rgba(139, 92, 246, 0.15); color: #c4b5fd; border: 1px solid rgba(139, 92, 246, 0.3); }
    .tag-mem { background: rgba(6, 182, 212, 0.15); color: #67e8f9; border: 1px solid rgba(6, 182, 212, 0.3); }
    .tag-disk { background: rgba(245, 158, 11, 0.15); color: #fde68a; border: 1px solid rgba(245, 158, 11, 0.3); }
    .tag-net { background: rgba(16, 185, 129, 0.15); color: #a7f3d0; border: 1px solid rgba(16, 185, 129, 0.3); }
    .section-header { font-size: 1.25rem; font-weight: 600; margin: 32px 0 16px 0; color: #fff; display: flex; align-items: center; gap: 8px; }
  </style>
</head>
<body>
  <div class="container">
    <header>
      <div>
        <h1>⚡ Compute MicroVM Performance Report</h1>
        <div style="margin-top: 6px; color: var(--text-muted); font-size: 0.875rem;">
          Daemon: <span style="color: #fff; font-family: monospace;">%s</span> &bull; 
          Run Date: <span style="color: #fff;">%s</span> &bull; 
          Kernel: <span style="color: #fff;">%s</span>
        </div>
      </div>
      <div>
        <span class="badge">Firecracker v1.9+</span>
        <span class="badge" style="background: rgba(16, 185, 129, 0.2); color: #6ee7b7; border-color: rgba(16, 185, 129, 0.4);">Verified Live Telemetry</span>
      </div>
    </header>

    <!-- Top Metric Cards -->
    <div class="grid grid-cols-4">
      <div class="card">
        <div class="metric-title">Cold Start to Execution</div>
        <div class="metric-value" id="val-cold-exec">--</div>
        <div class="metric-sub">Start VM &rarr; Command executed in guest</div>
      </div>
      <div class="card">
        <div class="metric-title">Snapshot Save / Restore</div>
        <div class="metric-value" id="val-snapshot">--</div>
        <div class="metric-sub">Memory dump &amp; state resume latency</div>
      </div>
      <div class="card">
        <div class="metric-title">Warm Resume to Execution</div>
        <div class="metric-value" id="val-warm-exec">--</div>
        <div class="metric-sub">Resume VM &rarr; Command executed in guest</div>
      </div>
      <div class="card">
        <div class="metric-title">API Throughput</div>
        <div class="metric-value" id="val-throughput">--</div>
        <div class="metric-sub">Concurrent health &amp; fleet control RPS</div>
      </div>
    </div>

    <!-- Charts Row 1: Lifecycle Breakdown & Host vs VM Compute/Memory -->
    <div class="section-header">⏱️ MicroVM Lifecycle Latency &amp; Execution Breakdown</div>
    <div class="grid grid-cols-2">
      <div class="card">
        <div class="metric-title">MicroVM Lifecycle Latency Breakdown (Average &amp; P95)</div>
        <div class="chart-container">
          <canvas id="lifecycleChart"></canvas>
        </div>
      </div>
      <div class="card">
        <div class="metric-title">Start-to-Execution Timelines: Cold Start vs Warm Resume</div>
        <div class="chart-container">
          <canvas id="timelineChart"></canvas>
        </div>
      </div>
    </div>

    <!-- Charts Row 2: Host vs VM Direct Performance Comparison -->
    <div class="section-header">⚖️ Performance on VM vs Host Directly (Apples-to-Apples)</div>
    <div class="grid grid-cols-2">
      <div class="card">
        <div class="metric-title">Compute &amp; Memory Throughput (Host vs MicroVM)</div>
        <div class="chart-container">
          <canvas id="compMemChart"></canvas>
        </div>
      </div>
      <div class="card">
        <div class="metric-title">Storage I/O &amp; Random 4K IOPS (Host vs MicroVM)</div>
        <div class="chart-container">
          <canvas id="diskChart"></canvas>
        </div>
      </div>
    </div>

    <!-- Host vs VM Comparison Table -->
    <div class="section-header">📊 Detailed Benchmark Comparison: Host vs MicroVM</div>
    <div class="card" style="padding: 0; overflow: hidden; margin-bottom: 24px;">
      <table id="compTable">
        <thead>
          <tr>
            <th>Category</th>
            <th>Workload / Metric</th>
            <th>Host Native</th>
            <th>MicroVM</th>
            <th>Virtualization Overhead</th>
            <th>Ratio (VM/Host)</th>
            <th>Efficiency Status</th>
          </tr>
        </thead>
        <tbody id="compTableBody">
        </tbody>
      </table>
    </div>

    <!-- Lifecycle Metrics Table -->
    <div class="section-header">📋 Complete Latency Percentiles &amp; Telemetry</div>
    <div class="card" style="padding: 0; overflow: hidden;">
      <table>
        <thead>
          <tr>
            <th>Metric Name</th>
            <th>Samples</th>
            <th>Average</th>
            <th>Median (P50)</th>
            <th>P90</th>
            <th>P95</th>
            <th>Min</th>
            <th>Max</th>
            <th>Throughput</th>
          </tr>
        </thead>
        <tbody id="metricsTableBody">
        </tbody>
      </table>
    </div>
  </div>

  <script>
    const reportData = %s;

    function formatDur(ns) {
      if (!ns || ns === 0) return '0 ms';
      const ms = ns / 1e6;
      if (ms < 1) return (ns / 1e3).toFixed(2) + ' µs';
      if (ms < 1000) return ms.toFixed(2) + ' ms';
      return (ms / 1000).toFixed(2) + ' s';
    }

    // Populate Top Cards
    const coldExec = reportData.metrics['cold_start_to_execution'] || reportData.metrics['vm_time_to_ssh'];
    if (coldExec) {
      document.getElementById('val-cold-exec').innerText = formatDur(coldExec.avg_ns);
    }
    const saveState = reportData.metrics['vm_save_state_latency'];
    const resume = reportData.metrics['vm_resume_latency'];
    if (saveState && resume) {
      document.getElementById('val-snapshot').innerText = formatDur(saveState.avg_ns) + ' / ' + formatDur(resume.avg_ns);
    }
    const warmExec = reportData.metrics['warm_resume_to_execution'] || reportData.metrics['vm_post_resume_ssh'];
    if (warmExec) {
      document.getElementById('val-warm-exec').innerText = formatDur(warmExec.avg_ns);
    }
    const apiThroughput = reportData.metrics['api_concurrent_health'];
    if (apiThroughput && apiThroughput.ops_per_sec) {
      document.getElementById('val-throughput').innerText = apiThroughput.ops_per_sec.toFixed(0) + ' req/s';
    }

    // Lifecycle Chart
    const lcKeys = [
      'vm_creation_latency',
      'vm_cold_boot_latency',
      'vm_time_to_ssh',
      'cold_start_to_execution',
      'vm_save_state_latency',
      'vm_resume_latency',
      'warm_resume_to_execution',
      'vm_stop_latency',
      'vm_delete_latency'
    ];
    const lcLabels = [
      'Create (IPAM+Disk)',
      'Cold Boot',
      'Time to SSH',
      'Cold Start to Exec',
      'Snapshot Save',
      'Snapshot Resume',
      'Warm Resume to Exec',
      'Stop',
      'Delete'
    ];
    const lcAvg = [];
    const lcP95 = [];

    lcKeys.forEach(k => {
      const m = reportData.metrics[k];
      if (m) {
        lcAvg.push((m.avg_ns / 1e6).toFixed(1));
        lcP95.push((m.p95_ns / 1e6).toFixed(1));
      } else {
        lcAvg.push(0);
        lcP95.push(0);
      }
    });

    new Chart(document.getElementById('lifecycleChart'), {
      type: 'bar',
      data: {
        labels: lcLabels,
        datasets: [
          {
            label: 'Average (ms)',
            data: lcAvg,
            backgroundColor: 'rgba(59, 130, 246, 0.8)',
            borderRadius: 6
          },
          {
            label: 'P95 (ms)',
            data: lcP95,
            backgroundColor: 'rgba(139, 92, 246, 0.7)',
            borderRadius: 6
          }
        ]
      },
      options: {
        responsive: true,
        maintainAspectRatio: false,
        plugins: { legend: { labels: { color: '#9ca3af' } } },
        scales: {
          x: { ticks: { color: '#9ca3af', font: { size: 10 } }, grid: { color: '#1f2937' } },
          y: { ticks: { color: '#9ca3af' }, grid: { color: '#1f2937' }, title: { display: true, text: 'Latency (milliseconds)', color: '#9ca3af' } }
        }
      }
    });

    // Start-to-Execution Timelines
    const bootAvg = reportData.metrics['vm_cold_boot_latency'] ? (reportData.metrics['vm_cold_boot_latency'].avg_ns / 1e6) : 0;
    const sshReadyAvg = reportData.metrics['vm_time_to_ssh'] ? (reportData.metrics['vm_time_to_ssh'].avg_ns / 1e6) : 0;
    const execRoundtripAvg = reportData.metrics['ssh_command_roundtrip'] ? (reportData.metrics['ssh_command_roundtrip'].avg_ns / 1e6) : 0;

    const resumeAvg = reportData.metrics['vm_resume_latency'] ? (reportData.metrics['vm_resume_latency'].avg_ns / 1e6) : 0;
    const warmSSHAvg = reportData.metrics['vm_post_resume_ssh'] ? (reportData.metrics['vm_post_resume_ssh'].avg_ns / 1e6) : 0;

    new Chart(document.getElementById('timelineChart'), {
      type: 'bar',
      data: {
        labels: ['Cold Start Pipeline', 'Snapshot Warm Resume'],
        datasets: [
          {
            label: 'VMM Process / Boot / Load State',
            data: [bootAvg, resumeAvg],
            backgroundColor: 'rgba(59, 130, 246, 0.8)'
          },
          {
            label: 'Kernel & SSH Daemon Readiness',
            data: [sshReadyAvg, warmSSHAvg],
            backgroundColor: 'rgba(16, 185, 129, 0.8)'
          },
          {
            label: 'In-VM Execution & Roundtrip',
            data: [execRoundtripAvg, execRoundtripAvg],
            backgroundColor: 'rgba(245, 158, 11, 0.8)'
          }
        ]
      },
      options: {
        responsive: true,
        maintainAspectRatio: false,
        indexAxis: 'y',
        plugins: {
          legend: { labels: { color: '#9ca3af' } },
          tooltip: {
            callbacks: {
              footer: (items) => {
                let total = 0;
                items.forEach(i => total += i.parsed.x);
                return 'Total Latency: ' + total.toFixed(1) + ' ms';
              }
            }
          }
        },
        scales: {
          x: { stacked: true, ticks: { color: '#9ca3af' }, grid: { color: '#1f2937' }, title: { display: true, text: 'Milliseconds (Stacked)', color: '#9ca3af' } },
          y: { stacked: true, ticks: { color: '#9ca3af' }, grid: { color: '#1f2937' } }
        }
      }
    });

    // Host vs VM Compute & Memory Chart
    const compMemLabels = ['SHA-256 (MB/s)', 'Prime Sieve (ops/s)', 'Matrix Math (MFLOPS)', 'Mem Seq (MB/s)', 'Memcpy (MB/s)'];
    const hostComp = [];
    const vmComp = [];

    if (reportData.host_vs_vm) {
      const h = reportData.host_vs_vm.host;
      const v = reportData.host_vs_vm.vm;
      if (h && v) {
        hostComp.push(h.cpu_sha256_mbps, h.cpu_prime_ops_per_sec, h.cpu_matrix_mflops, h.mem_seq_bandwidth_mbps, h.mem_copy_bandwidth_mbps);
        vmComp.push(v.cpu_sha256_mbps, v.cpu_prime_ops_per_sec, v.cpu_matrix_mflops, v.mem_seq_bandwidth_mbps, v.mem_copy_bandwidth_mbps);
      }
    }

    new Chart(document.getElementById('compMemChart'), {
      type: 'bar',
      data: {
        labels: compMemLabels,
        datasets: [
          {
            label: 'Host Native',
            data: hostComp,
            backgroundColor: 'rgba(16, 185, 129, 0.85)',
            borderRadius: 6
          },
          {
            label: 'MicroVM (Ubuntu/Firecracker)',
            data: vmComp,
            backgroundColor: 'rgba(59, 130, 246, 0.85)',
            borderRadius: 6
          }
        ]
      },
      options: {
        responsive: true,
        maintainAspectRatio: false,
        plugins: { legend: { labels: { color: '#9ca3af' } } },
        scales: {
          x: { ticks: { color: '#9ca3af' }, grid: { color: '#1f2937' } },
          y: { ticks: { color: '#9ca3af' }, grid: { color: '#1f2937' }, title: { display: true, text: 'Throughput / Rate', color: '#9ca3af' } }
        }
      }
    });

    // Storage I/O Chart
    const diskLabels = ['Seq Write Sync (MB/s)', 'Seq Read (MB/s)', 'Random 4K IOPS'];
    const hostDisk = [];
    const vmDisk = [];

    if (reportData.host_vs_vm) {
      const h = reportData.host_vs_vm.host;
      const v = reportData.host_vs_vm.vm;
      if (h && v) {
        hostDisk.push(h.disk_seq_write_mbps, h.disk_seq_read_mbps, h.disk_random_4k_iops);
        vmDisk.push(v.disk_seq_write_mbps, v.disk_seq_read_mbps, v.disk_random_4k_iops);
      }
    }

    new Chart(document.getElementById('diskChart'), {
      type: 'bar',
      data: {
        labels: diskLabels,
        datasets: [
          {
            label: 'Host Native (SATA/NVMe)',
            data: hostDisk,
            backgroundColor: 'rgba(245, 158, 11, 0.85)',
            borderRadius: 6
          },
          {
            label: 'MicroVM virtio-block',
            data: vmDisk,
            backgroundColor: 'rgba(139, 92, 246, 0.85)',
            borderRadius: 6
          }
        ]
      },
      options: {
        responsive: true,
        maintainAspectRatio: false,
        plugins: { legend: { labels: { color: '#9ca3af' } } },
        scales: {
          x: { ticks: { color: '#9ca3af' }, grid: { color: '#1f2937' } },
          y: { ticks: { color: '#9ca3af' }, grid: { color: '#1f2937' }, title: { display: true, text: 'Throughput / IOPS', color: '#9ca3af' } }
        }
      }
    });

    // Populate Comparison Table
    if (reportData.host_vs_vm && reportData.host_vs_vm.comparisons) {
      const tb = document.getElementById('compTableBody');
      reportData.host_vs_vm.comparisons.forEach(c => {
        const tr = document.createElement('tr');
        let badgeClass = 'tag-cpu';
        if (c.category === 'Memory') badgeClass = 'tag-mem';
        if (c.category === 'Disk') badgeClass = 'tag-disk';
        if (c.category === 'Network') badgeClass = 'tag-net';

        let effColor = 'text-success';
        if (c.overhead_pct > 15) effColor = 'text-warning';
        if (c.overhead_pct > 40) effColor = 'text-danger';

        const sign = c.overhead_pct >= 0 ? '+' : '';
        tr.innerHTML = '<td><span class="tag ' + badgeClass + '">' + c.category + '</span></td>' +
          '<td><strong>' + c.metric_name + '</strong></td>' +
          '<td>' + c.host_value.toFixed(1) + ' ' + c.unit + '</td>' +
          '<td>' + c.vm_value.toFixed(1) + ' ' + c.unit + '</td>' +
          '<td class="' + effColor + '">' + sign + c.overhead_pct.toFixed(1) + '%%</td>' +
          '<td><strong>' + c.ratio.toFixed(2) + 'x</strong></td>' +
          '<td><span class="' + effColor + '">' + c.summary + '</span></td>';
        tb.appendChild(tr);
      });
    }

    // Populate Metrics Table
    const mBody = document.getElementById('metricsTableBody');
    Object.keys(reportData.metrics).forEach(k => {
      const m = reportData.metrics[k];
      const tr = document.createElement('tr');
      const ops = m.ops_per_sec > 0 ? m.ops_per_sec.toFixed(1) + '/s' : '-';
      tr.innerHTML = '<td><strong>' + m.name + '</strong></td>' +
        '<td>' + m.count + '</td>' +
        '<td>' + formatDur(m.avg_ns) + '</td>' +
        '<td>' + formatDur(m.p50_ns) + '</td>' +
        '<td>' + formatDur(m.p90_ns) + '</td>' +
        '<td>' + formatDur(m.p95_ns) + '</td>' +
        '<td>' + formatDur(m.min_ns) + '</td>' +
        '<td>' + formatDur(m.max_ns) + '</td>' +
        '<td style="color: var(--cyan); font-weight: 600;">' + ops + '</td>';
      mBody.appendChild(tr);
    });
  </script>
</body>
</html>`,
		report.DaemonAddr,
		report.Timestamp.Format("2006-01-02 15:04:05 UTC"),
		report.Environment["kernel"],
		string(reportJSON),
	)

	return os.WriteFile(filePath, []byte(htmlContent), 0644)
}
