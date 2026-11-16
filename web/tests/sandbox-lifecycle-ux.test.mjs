import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import fs from 'node:fs/promises';
import { existsSync } from 'node:fs';
import http from 'node:http';
import path from 'node:path';
import test from 'node:test';

const WEB_DIR = path.resolve(import.meta.dirname, '..');
const REPO_ROOT = path.resolve(WEB_DIR, '..');
const ASSETS_DIR = path.join(REPO_ROOT, 'internal', 'gui', 'assets');

function findChromium() {
  const candidates = [
    process.env.CHROME_BIN,
    process.env.CHROMIUM_BIN,
    '/usr/bin/chromium',
    '/usr/bin/chromium-browser',
    '/usr/bin/google-chrome',
  ].filter(Boolean);
  return candidates.find((p) => existsSync(p)) || null;
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function createMockServer() {
  let vms = [
    {
      id: 'vm-alpha',
      name: 'sbx-alpha',
      status: 'running',
      vcpus: 2,
      memory_mb: 1024,
      disk_mb: 20480,
      ip_address: '172.16.0.10',
      public_ip: '172.16.0.10',
      public_ip_enabled: true,
      network_isolation: 'account',
      mac_address: '02:fc:00:00:00:10',
      tap_device: 'tap0',
      pid: 12345,
      worker_id: 'nod-1',
      pool_id: 'default',
      disk_path: '/var/lib/compute/vms/vm-alpha/disk.ext4',
      rootfs_path: '/var/lib/compute/images/ubuntu.ext4',
      kernel_path: '/var/lib/compute/vmlinux',
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
      protected: false,
    },
    {
      id: 'vm-hibernated',
      name: 'sbx-hibernated',
      status: 'stopped',
      auto_hibernated: true,
      hibernate_reason: 'memory_pressure',
      vcpus: 1,
      memory_mb: 512,
      disk_mb: 20480,
      ip_address: '172.16.0.12',
      public_ip_enabled: false,
      network_isolation: 'account',
      mac_address: '02:fc:00:00:00:12',
      tap_device: 'tap12',
      worker_id: 'nod-1',
      pool_id: 'default',
      disk_path: '/var/lib/compute/vms/vm-hibernated/disk.ext4',
      rootfs_path: '/var/lib/compute/images/ubuntu.ext4',
      kernel_path: '/var/lib/compute/vmlinux',
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
      protected: false,
    },
    {
      id: 'vm-job-hidden',
      owner_id: 'jobs',
      name: 'job-ci-9999',
      status: 'running',
      vcpus: 2,
      memory_mb: 2048,
      disk_mb: 20480,
      ip_address: '172.16.0.99',
      mac_address: '02:fc:00:00:00:99',
      tap_device: 'tap99',
      disk_path: '/var/lib/compute/vms/job-ci-9999/disk.ext4',
      rootfs_path: '/var/lib/compute/images/ubuntu.ext4',
      kernel_path: '/var/lib/compute/vmlinux',
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    },
  ];
  let templates = [];
  let volumes = [
    {
      id: 'vol-data-1',
      name: 'ci-cache',
      pool: 'default',
      size_gb: 25,
      used_gb: 4,
      status: 'available',
      attached_vm: '',
      device_path: '/dev/vdb',
      mount_point: '/mnt/ci-cache',
      filesystem: 'ext4',
      iops: 3000,
      throughput: '250 MB/s',
      snapshots: 0,
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    },
  ];
  let portForwards = [];
  let ciRepos = [];
  let jobs = [];
  let workers = [
    {
      id: 'nod-1',
      name: 'worker-01',
      pool_id: 'default',
      role: 'worker',
      status: 'ready',
      underlay_ip: '10.0.1.11',
      arch: 'amd64',
      total_vcpus: 16,
      allocated_vcpus: 16,
      physical_vcpus: 16,
      used_vcpus: 2,
      total_mem_mb: 32768,
      allocated_mem_mb: 32768,
      physical_mem_mb: 32768,
      used_mem_mb: 1024,
      available_mem_mb: 31744,
      total_disk_mb: 512000,
      allocated_disk_mb: 512000,
      physical_disk_mb: 512000,
      used_disk_mb: 20480,
      available_disk_mb: 491520,
      running_vms: 1,
      fc_version: '1.9.1',
      agent_version: '1.0.0',
      last_heartbeat: new Date().toISOString(),
    },
    {
      id: 'nod-2',
      name: 'worker-02',
      pool_id: 'gpu-pool',
      role: 'worker',
      status: 'ready',
      underlay_ip: '10.0.1.12',
      arch: 'amd64',
      total_vcpus: 32,
      allocated_vcpus: 32,
      physical_vcpus: 32,
      used_vcpus: 0,
      total_mem_mb: 65536,
      allocated_mem_mb: 65536,
      physical_mem_mb: 65536,
      used_mem_mb: 0,
      available_mem_mb: 65536,
      total_disk_mb: 1024000,
      allocated_disk_mb: 1024000,
      physical_disk_mb: 1024000,
      used_disk_mb: 0,
      available_disk_mb: 1024000,
      running_vms: 0,
      fc_version: '1.9.1',
      agent_version: '1.0.0',
      last_heartbeat: new Date().toISOString(),
    },
  ];
  let pools = [
    { id: 'pool-default', name: 'default', labels_json: '{}', policy_json: '{}', created_at: new Date().toISOString(), updated_at: new Date().toISOString() },
    { id: 'pool-gpu', name: 'gpu-pool', labels_json: '{}', policy_json: '{}', created_at: new Date().toISOString(), updated_at: new Date().toISOString() },
  ];
  let workerTokens = [];
  let schedulingPolicy = {
    strategy: 'least_loaded',
    cpu_weight: 1.0,
    memory_weight: 1.0,
    disk_weight: 0.5,
    cpu_soft_overcommit_ratio: 4.0,
    cpu_hard_overcommit_ratio: 6.0,
    cpu_share_weight_base: 1024,
    memory_hard_guarantee: true,
    idle_ram_snapshot_enabled: true,
    idle_threshold_seconds: 900,
    auto_reclaim_on_pressure: true,
    host_reserved_vcpus: 1,
    host_reserved_memory_mb: 1024,
    host_reserved_disk_mb: 4096,
    disk_hard_enforcement: true,
  };
  let users = [
    {
      id: 'usr_admin',
      username: 'admin',
      email: 'admin@compute.local',
      display_name: 'Administrator',
      role: 'admin',
      status: 'active',
      created_at: Math.floor(Date.now() / 1000) - 3600,
    },
  ];
  const cloneCalls = [];
  const bulkCloneCalls = [];
  const launchCalls = [];
  const createCalls = [];
  const bulkCreateCalls = [];
  const bulkActionCalls = [];
  const restartCalls = [];
  const runCalls = [];
  const migrateCalls = [];
  const workerUpdateCalls = [];
  const policyUpdateCalls = [];
  const poolMigrateCalls = [];
  const bulkDeleteCalls = [];
  let currentUserRole = 'admin';

  const mime = {
    '.html': 'text/html; charset=utf-8',
    '.js': 'application/javascript; charset=utf-8',
    '.css': 'text/css; charset=utf-8',
    '.woff2': 'font/woff2',
    '.svg': 'image/svg+xml',
    '.json': 'application/json',
  };

  const server = http.createServer(async (req, res) => {
    const u = new URL(req.url, 'http://127.0.0.1');
    const p = u.pathname;
    const chunks = [];
    for await (const c of req) chunks.push(c);
    const bodyText = Buffer.concat(chunks).toString('utf8');
    let body = {};
    try {
      if (bodyText) body = JSON.parse(bodyText);
    } catch {}

    const json = (status, data) => {
      res.writeHead(status, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify(data));
    };

    if (p.startsWith('/api/v1/')) {
      if (p === '/api/v1/auth/status') {
        return json(200, { auth_enabled: true, signup_enabled: false, admin_username: 'admin' });
      }
      if (p === '/api/v1/auth/me') {
        const activeUser = currentUserRole === 'admin' ? users[0] : {
          id: 'usr_viewer',
          username: 'viewer',
          email: 'viewer@compute.local',
          display_name: 'Viewer',
          role: 'user',
          status: 'active',
        };
        return json(200, {
          user: activeUser,
          role: currentUserRole,
          auth_type: 'session',
        });
      }
      if (p === '/api/v1/health') {
        return json(200, { status: 'ok', version: '1.0.0', name: 'compute' });
      }
      if (p === '/api/v1/stats') {
        return json(200, {
          total_vms: vms.length,
          running_vms: vms.filter((v) => v.status === 'running').length,
          snapshotted_vms: vms.filter((v) => v.status === 'paused').length,
          queued_vms: 0,
          stopped_vms: vms.filter((v) => v.status === 'stopped').length,
          created_vms: 0,
          total_vcpus: vms.reduce((acc, v) => acc + v.vcpus, 0),
          total_memory_mb: vms.reduce((acc, v) => acc + v.memory_mb, 0),
          running_vcpus: 2,
          running_memory_mb: 1024,
          allocated_ips: vms.length,
          host_memory_available_mb: 8192,
          host_cpus: 8,
        });
      }
      if (p === '/api/v1/config') {
        return json(200, {
          server: { listen: '0.0.0.0:18080' },
          defaults: { vcpus: 2, memory_mb: 2048, disk_size_mb: 20480, image: 'ubuntu-24' },
          images: { available: [{ name: 'ubuntu-24' }], vms_dir: '/var/lib/compute/vms', images_dir: '/var/lib/compute/images' },
          resources: { max_running_vms: 20, max_total_disk_mb: 512000, max_total_vcpus: 16, max_total_memory_mb: 32768 },
        });
      }
      if (p === '/api/v1/admin/resources') {
        return json(200, {
          timestamp: new Date().toISOString(),
          cpu: {
            cores: 16,
            model: 'AMD EPYC 7313P',
            usage_percent: 34.5,
            user_percent: 25.0,
            system_percent: 7.5,
            iowait_percent: 2.0,
            idle_percent: 65.5,
            load_1m: 1.42,
            load_5m: 1.18,
            load_15m: 0.95,
          },
          memory: {
            total_mb: 65536,
            used_mb: 24576,
            available_mb: 40960,
            free_mb: 32000,
            cached_mb: 7200,
            buffers_mb: 1760,
            used_percent: 37.5,
            swap_total_mb: 0,
            swap_used_mb: 0,
          },
          disk: {
            path: '/var/lib/compute',
            total_gb: 500,
            used_gb: 145,
            free_gb: 355,
            available_gb: 340,
            used_percent: 29.0,
            mounts: [
              { label: 'compute-data', path: '/var/lib/compute', total_gb: 500, used_gb: 145, available_gb: 340, used_percent: 29.0 },
            ],
          },
          disk_io: {
            read_ops: 12400,
            write_ops: 8300,
            read_bytes: 1024000000,
            write_bytes: 512000000,
            read_mb: 976.5,
            write_mb: 488.2,
            read_mb_per_sec: 14.2,
            write_mb_per_sec: 8.6,
            read_iops: 320,
            write_iops: 180,
            io_in_progress: 1,
            devices: [
              { device: 'nvme0n1', read_ops: 12400, write_ops: 8300, read_bytes: 1024000000, write_bytes: 512000000, read_mb: 976.5, write_mb: 488.2, io_in_progress: 1 },
            ],
          },
          network: {
            rx_bytes: 2048000000,
            tx_bytes: 1024000000,
            rx_packets: 150000,
            tx_packets: 120000,
            rx_mb: 1953.1,
            tx_mb: 976.5,
            rx_mb_per_sec: 3.45,
            tx_mb_per_sec: 1.9,
            interfaces: ['eth0', 'fcbr0'],
          },
          system: {
            hostname: 'compute-host-01',
            os: 'linux',
            arch: 'amd64',
            kernel_version: '6.8.0',
            uptime_seconds: 86400,
            kvm_available: true,
          },
          allocations: {
            sandboxes: { total: 1, running: 1, stopped: 0, vcpus_allocated: 2, running_vcpus: 2, memory_mb_allocated: 1024, running_memory_mb: 1024, disk_mb_allocated: 20480, disk_gb_allocated: 20 },
            ci_jobs: { total: jobs.length, queued: 0, running: 0, succeeded: jobs.length, failed: 0, vcpus_allocated: 0, memory_mb_allocated: 0, disk_mb_allocated: 0, disk_gb_allocated: 0 },
            volumes: { total: volumes.length, attached: 0, available: volumes.length, disk_mb_allocated: 25600, disk_gb_allocated: 25 },
            workers: { total: 1, ready: 1, offline: 0 },
            limits: { max_running_vms: 20, max_total_vcpus: 16, max_total_memory_mb: 32768, max_total_disk_mb: 512000, min_free_disk_space_mb: 4096 },
            allocated_vcpus: 2,
            running_vcpus: 2,
            job_vcpus: 0,
            max_total_vcpus: 16,
            allocated_memory_mb: 1024,
            running_memory_mb: 1024,
            job_memory_mb: 0,
            max_total_memory_mb: 32768,
            allocated_vm_disk_gb: 20,
            allocated_volume_disk_gb: 25,
            max_total_disk_gb: 500,
          },
          capacity_warnings: ['Memory headroom approaching threshold in test warning'],
        });
      }
      if (p === '/api/v1/network/config') {
        return json(200, { bridge_name: 'fcbr0', mode: 'nat', subnet: '172.16.0.0/24', bridge_ip: '172.16.0.1' });
      }
      if (p === '/api/v1/network/allocations') return json(200, []);
      if (p === '/api/v1/network/port-forwards' && req.method === 'GET') return json(200, portForwards);
      if (p === '/api/v1/network/port-forwards' && req.method === 'POST') {
        const pf = { id: `pf-${portForwards.length + 1}`, ...body, status: 'active', created_at: new Date().toISOString() };
        portForwards = [...portForwards, pf];
        return json(201, pf);
      }
      if (p === '/api/v1/volumes' && req.method === 'GET') return json(200, volumes);
      if (p === '/api/v1/workers' && req.method === 'GET') return json(200, workers);
      const workerUpdateMatch = p.match(/^\/api\/v1\/workers\/([^/]+)$/);
      if (workerUpdateMatch && (req.method === 'PATCH' || req.method === 'PUT')) {
        workerUpdateCalls.push({ id: workerUpdateMatch[1], body });
        workers = workers.map((w) => (w.id === workerUpdateMatch[1] ? { ...w, ...body } : w));
        return json(200, workers.find((w) => w.id === workerUpdateMatch[1]) || {});
      }
      if (p === '/api/v1/worker-tokens' && req.method === 'GET') return json(200, workerTokens);
      if (p === '/api/v1/worker-tokens' && req.method === 'POST') {
        const wt = { id: `wt-${Date.now()}`, token: `wt_tok_${Date.now()}`, prefix: 'wt_tok_', pool_id: body.pool_id || 'default', created_at: new Date().toISOString() };
        workerTokens = [...workerTokens, wt];
        return json(201, wt);
      }
      if (p.match(/^\/api\/v1\/worker-tokens\/[^/]+$/) && req.method === 'DELETE') {
        const tid = p.split('/').pop();
        workerTokens = workerTokens.filter((wt) => wt.id !== tid && wt.token !== tid);
        return json(200, { status: 'revoked' });
      }
      if (p === '/api/v1/pools' && req.method === 'GET') return json(200, pools);
      const poolMigrateMatch = p.match(/^\/api\/v1\/pools\/([^/]+)\/migrate$/);
      if (poolMigrateMatch && req.method === 'POST') {
        poolMigrateCalls.push({ pool: poolMigrateMatch[1], body });
        return json(200, {
          source_pool: poolMigrateMatch[1],
          target_pool: body.target_pool_id || '',
          workers_moved: 1,
          workers_failed: 0,
          sandboxes_migrated: 1,
          sandboxes_failed: 0,
          errors: [],
        });
      }
      if (p === '/api/v1/scheduler/policy' && req.method === 'GET') return json(200, schedulingPolicy);
      if (p === '/api/v1/scheduler/policy' && req.method === 'PUT') {
        policyUpdateCalls.push(body);
        schedulingPolicy = { ...schedulingPolicy, ...body, updated_at: new Date().toISOString() };
        return json(200, schedulingPolicy);
      }
      if (p === '/api/v1/events') return json(200, []);
      if (p === '/api/v1/events/stream') {
        res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache' });
        return;
      }
      if (p === '/api/v1/queue') return json(200, { count: 0, queued_vms: [] });
      if (p === '/api/v1/tiers') return json(200, []);
      if (p === '/api/v1/environments') return json(200, []);
      if (p === '/api/v1/api-keys') return json(200, []);
      if (p === '/api/v1/keys') return json(200, []);

      if (p === '/api/v1/ci/repos' && req.method === 'GET') return json(200, ciRepos);
      if (p === '/api/v1/ci/repos' && req.method === 'POST') {
        const name = body.name || 'acme/api-service';
        const created = {
          id: `gh_${ciRepos.length + 1}`,
          owner_id: 'usr_admin',
          name,
          repo_url: body.repo_url,
          default_branch: body.default_branch || 'main',
          has_token: Boolean(body.github_token),
          has_webhook_secret: true,
          webhook_secret: body.webhook_secret || 'whsec_test_secret_123',
          default_command: body.default_command || 'npm test',
          vcpus: body.vcpus || 2,
          memory_mb: body.memory_mb || 2048,
          timeout_sec: body.timeout_sec || 600,
          status: 'connected',
          created_at: new Date().toISOString(),
        };
        ciRepos = [...ciRepos, created];
        return json(201, created);
      }
      const triggerMatch = p.match(/^\/api\/v1\/ci\/repos\/([^/]+)\/trigger$/);
      if (triggerMatch && req.method === 'POST') {
        const repo = ciRepos.find((r) => r.id === triggerMatch[1]);
        const job = {
          id: `job-${jobs.length + 1}`,
          owner_id: 'usr_admin',
          pool_id: 'default',
          type: 'command',
          state: 'succeeded',
          priority: 10,
          repo_id: repo?.id,
          repo_name: repo?.name,
          repo_url: repo?.repo_url,
          branch: body.branch || repo?.default_branch || 'main',
          commit_sha: body.commit_sha || 'abc1234def',
          source: 'manual',
          vm_id: 'job-ci-runner-1',
          spec_json: JSON.stringify({ command: body.command || repo?.default_command }),
          result_json: '{"exit_code":0}',
          queued_at: new Date().toISOString(),
          started_at: new Date().toISOString(),
          finished_at: new Date().toISOString(),
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
        };
        jobs = [...jobs, job];
        return json(201, job);
      }
      if (p === '/api/v1/jobs' && req.method === 'GET') return json(200, jobs);
      if (p.match(/^\/api\/v1\/jobs\/[^/]+\/logs$/) && req.method === 'GET') {
        return json(200, [{ id: 'log-1', job_id: 'job-1', attempt_id: 'att-1', stream: 'stdout', seq: 1, data: 'All CI tests passed', created_at: new Date().toISOString() }]);
      }

      if (p === '/api/v1/vms' && req.method === 'GET') return json(200, vms);
      if (p === '/api/v1/vms' && req.method === 'POST') {
        if (body.name === 'sbx-limit-test') {
          return json(429, {
            code: 'RESOURCE_LIMIT_EXCEEDED',
            error: 'Worker node memory limit exceeded',
            resource: 'memory_mb',
            limit_type: 'worker_allocation',
            node_id: 'nod-1',
            requested: 4096,
            available: 512,
            limit: 32768,
            suggestion: 'Select another worker pool or reduce sandbox memory',
          });
        }
        createCalls.push(body);
        const created = {
          id: `vm-created-${createCalls.length}`,
          name: body.name || 'sbx-created',
          status: 'stopped',
          vcpus: body.vcpus || 2,
          memory_mb: body.memory_mb || 2048,
          disk_mb: body.disk_mb || ((body.disk_gb || 20) * 1024),
          worker_id: body.worker_id || 'nod-1',
          pool_id: body.pool_id || 'default',
          ip_address: `172.16.0.${30 + createCalls.length}`,
          mac_address: `02:fc:00:00:00:3${createCalls.length}`,
          tap_device: `tap3${createCalls.length}`,
          disk_path: `/var/lib/compute/vms/${body.name}/disk.ext4`,
          rootfs_path: '/var/lib/compute/images/ubuntu.ext4',
          kernel_path: '/var/lib/compute/vmlinux',
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
        };
        vms = [...vms, created];
        return json(201, created);
      }
      if (p === '/api/v1/vms/bulk-delete' && req.method === 'POST') {
        bulkDeleteCalls.push(body);
        const ids = Array.isArray(body.ids) ? body.ids : (Array.isArray(body.vm_ids) ? body.vm_ids : []);
        vms = vms.filter((v) => !ids.includes(v.id));
        return json(200, { action: 'delete', requested: ids.length, succeeded: ids, failed: [] });
      }
      if (p === '/api/v1/vms/bulk-clone' && req.method === 'POST') {
        const names = Array.isArray(body.names) ? body.names : [];
        bulkCloneCalls.push({
          source_id: body.source_id,
          names,
          vcpus: body.vcpus,
          memory_mb: body.memory_mb,
          disk_gb: body.disk_gb,
          network_isolated: body.network_isolated,
          description: body.description,
          auto_start: body.auto_start,
          async: body.async,
        });
        const createdClones = names.map((name, idx) => ({
          id: `vm-clone-${vms.filter((v) => v.id.startsWith('vm-clone-')).length + idx + 1}`,
          name,
          description: body.description || '',
          status: body.auto_start ? 'running' : 'stopped',
          vcpus: body.vcpus || src?.vcpus || 2,
          memory_mb: body.memory_mb || src?.memory_mb || 1024,
          disk_mb: body.disk_mb || (body.disk_gb ? body.disk_gb * 1024 : (src?.disk_mb || 20480)),
          ip_address: `172.16.0.${20 + vms.filter((v) => v.id.startsWith('vm-clone-')).length + idx + 1}`,
          network_isolation: body.network_isolated ? 'isolated' : 'account',
          mac_address: `02:fc:00:00:00:2${vms.filter((v) => v.id.startsWith('vm-clone-')).length + idx + 1}`,
          tap_device: `tap2${vms.filter((v) => v.id.startsWith('vm-clone-')).length + idx + 1}`,
          disk_path: `/var/lib/compute/vms/${name}/disk.ext4`,
          rootfs_path: '/var/lib/compute/images/ubuntu.ext4',
          kernel_path: '/var/lib/compute/vmlinux',
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
        }));
        vms = [...vms, ...createdClones];
        return json(202, {
          action: 'clone',
          status: 'queued',
          task_id: 'task_test_bulk_clone',
          requested: names.length,
          queued: names.length,
          succeeded: [],
          failed: [],
        });
      }

      const vmMatch = p.match(/^\/api\/v1\/vms\/([^/]+)(?:\/(.*))?$/);
      if (vmMatch) {
        const [, id, sub] = vmMatch;
        const src = vms.find((v) => v.id === id);
        if (sub === 'run' && req.method === 'POST') {
          vms = vms.map((v) => (v.id === id ? { ...v, status: 'running' } : v));
          return json(200, vms.find((v) => v.id === id));
        }
        if (sub === 'migrate' && req.method === 'POST') {
          migrateCalls.push({ id, body });
          vms = vms.map((v) =>
            v.id === id
              ? {
                  ...v,
                  worker_id: body.target_worker_id || v.worker_id,
                  pool_id: body.target_pool_id || v.pool_id,
                }
              : v,
          );
          return json(200, vms.find((v) => v.id === id));
        }
        if (sub === 'clone' && req.method === 'POST') {
          cloneCalls.push({ id, body });
          const created = {
            id: `vm-clone-${cloneCalls.length}`,
            name: body.name,
            description: body.description || '',
            status: body.auto_start ? 'running' : 'stopped',
            vcpus: body.vcpus || src?.vcpus || 2,
            memory_mb: body.memory_mb || src?.memory_mb || 1024,
            disk_mb: body.disk_mb || (body.disk_gb ? body.disk_gb * 1024 : (src?.disk_mb || 20480)),
            ip_address: `172.16.0.${20 + cloneCalls.length}`,
            network_isolation: body.network_isolated ? 'isolated' : 'account',
            mac_address: `02:fc:00:00:00:2${cloneCalls.length}`,
            tap_device: `tap2${cloneCalls.length}`,
            disk_path: `/var/lib/compute/vms/${body.name}/disk.ext4`,
            rootfs_path: '/var/lib/compute/images/ubuntu.ext4',
            kernel_path: '/var/lib/compute/vmlinux',
            created_at: new Date().toISOString(),
            updated_at: new Date().toISOString(),
          };
          vms = [...vms, created];
          return json(201, created);
        }
        if (sub === 'save-state' && req.method === 'POST') {
          vms = vms.map((v) =>
            v.id === id
              ? { ...v, status: 'paused', snapshot_path: `/var/lib/compute/vms/${id}/vm.snap` }
              : v,
          );
          return json(200, vms.find((v) => v.id === id));
        }
        if (sub === 'resume' && req.method === 'POST') {
          vms = vms.map((v) => (v.id === id ? { ...v, status: 'running' } : v));
          return json(200, vms.find((v) => v.id === id));
        }
      }

      if (p === '/api/v1/templates' && req.method === 'GET') return json(200, templates);
      if (p === '/api/v1/templates' && req.method === 'POST') {
        const src = vms.find((v) => v.id === body.vm_id);
        const tpl = {
          id: `tpl-${templates.length + 1}`,
          name: body.name,
          description: body.description || '',
          source_vm_id: body.vm_id,
          disk_path: `/var/lib/compute/templates/${body.name}.ext4`,
          vcpus: src?.vcpus || 2,
          memory_mb: src?.memory_mb || 1024,
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
        };
        templates = [...templates, tpl];
        return json(201, tpl);
      }
      const launchMatch = p.match(/^\/api\/v1\/templates\/([^/]+)\/launch$/);
      if (launchMatch && req.method === 'POST') {
        launchCalls.push({ id: launchMatch[1], body });
        const tpl = templates.find((t) => t.id === launchMatch[1]);
        const created = {
          id: `vm-from-snap-${launchCalls.length}`,
          name: body.name,
          status: body.auto_start === false ? 'stopped' : 'running',
          vcpus: body.vcpus || tpl?.vcpus || 2,
          memory_mb: body.memory_mb || tpl?.memory_mb || 1024,
          disk_mb: 20480,
          ip_address: `172.16.0.${40 + launchCalls.length}`,
          mac_address: `02:fc:00:00:00:4${launchCalls.length}`,
          tap_device: `tap4${launchCalls.length}`,
          disk_path: `/var/lib/compute/vms/${body.name}/disk.ext4`,
          rootfs_path: '/var/lib/compute/images/ubuntu.ext4',
          kernel_path: '/var/lib/compute/vmlinux',
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
        };
        vms = [...vms, created];
        return json(201, created);
      }
      const delTplMatch = p.match(/^\/api\/v1\/templates\/([^/]+)$/);
      if (delTplMatch && req.method === 'DELETE') {
        templates = templates.filter((t) => t.id !== delTplMatch[1]);
        return json(200, { status: 'deleted', id: delTplMatch[1] });
      }

      if (p === '/api/v1/users' && req.method === 'GET') return json(200, users);
      if (p === '/api/v1/users' && req.method === 'POST') {
        const created = {
          id: `usr_${users.length + 1}`,
          username: body.username,
          email: body.email,
          display_name: body.display_name || body.username,
          role: body.role || 'user',
          status: 'active',
          created_at: Math.floor(Date.now() / 1000),
        };
        users = [...users, created];
        return json(201, { status: 'created', user: created });
      }
      const delUserMatch = p.match(/^\/api\/v1\/users\/([^/]+)$/);
      if (delUserMatch && req.method === 'DELETE') {
        users = users.filter((usr) => usr.id !== delUserMatch[1]);
        return json(200, { status: 'deleted', id: delUserMatch[1] });
      }

      return json(404, { error: 'not found: ' + p });
    }

    const rel = p === '/' ? 'index.html' : p.replace(/^\/+/, '');
    const filePath = path.join(ASSETS_DIR, rel);
    try {
      const data = await fs.readFile(filePath);
      const ext = path.extname(filePath);
      res.writeHead(200, { 'Content-Type': mime[ext] || 'application/octet-stream' });
      res.end(data);
    } catch {
      const indexHtml = await fs.readFile(path.join(ASSETS_DIR, 'index.html'));
      res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
      res.end(indexHtml);
    }
  });

  return {
    server,
    cloneCalls,
    launchCalls,
    createCalls,
    migrateCalls,
    workerUpdateCalls,
    policyUpdateCalls,
    poolMigrateCalls,
    bulkDeleteCalls,
    bulkCloneCalls,
    setUserRole: (role) => {
      currentUserRole = role;
    },
  };
}

class CDPClient {
  constructor(wsUrl) {
    this.ws = new WebSocket(wsUrl);
    this.nextId = 1;
    this.pending = new Map();
  }
  async connect() {
    await new Promise((resolve, reject) => {
      this.ws.addEventListener('open', resolve, { once: true });
      this.ws.addEventListener('error', reject, { once: true });
    });
    this.ws.addEventListener('message', (ev) => {
      const msg = JSON.parse(ev.data);
      if (msg.id && this.pending.has(msg.id)) {
        const { resolve, reject } = this.pending.get(msg.id);
        this.pending.delete(msg.id);
        if (msg.error) reject(new Error(msg.error.message));
        else resolve(msg.result);
      }
    });
  }
  send(method, params = {}) {
    const id = this.nextId++;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.ws.send(JSON.stringify({ id, method, params }));
    });
  }
  async evalJS(expression) {
    const res = await this.send('Runtime.evaluate', {
      expression,
      returnByValue: true,
      awaitPromise: true,
    });
    if (res.exceptionDetails) {
      throw new Error(`JS error: ${JSON.stringify(res.exceptionDetails)}`);
    }
    return res.result?.value;
  }
  async realClick(pointExpr) {
    const pt = await this.evalJS(`(() => {
      const el = ${pointExpr};
      if (!el) return null;
      el.scrollIntoView({ block: 'center', inline: 'center' });
      const r = el.getBoundingClientRect();
      return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
    })()`);
    assert(pt, `Element not found for realClick: ${pointExpr}`);
    await this.send('Input.dispatchMouseEvent', { type: 'mouseMoved', x: pt.x, y: pt.y });
    await this.send('Input.dispatchMouseEvent', { type: 'mousePressed', x: pt.x, y: pt.y, button: 'left', clickCount: 1 });
    await this.send('Input.dispatchMouseEvent', { type: 'mouseReleased', x: pt.x, y: pt.y, button: 'left', clickCount: 1 });
  }
  close() {
    this.ws.close();
  }
}

async function waitFor(cdp, fnExpr, timeoutMs = 5000, label = fnExpr) {
  const start = Date.now();
  while (Date.now() - start < timeoutMs) {
    const val = await cdp.evalJS(fnExpr);
    if (val) return val;
    await sleep(40);
  }
  throw new Error(`Timeout waiting for: ${label}`);
}

test('Compute branding, self-hosted lifecycle UX, Milestone 3 Sandboxes, Raw Terminal, CI & Admin Resources', async (t) => {
  const chromeBin = findChromium();
  if (!chromeBin) {
    t.skip('No Chromium/Chrome binary found for browser E2E verification');
    return;
  }

  const {
    server,
    cloneCalls,
    launchCalls,
    createCalls,
    migrateCalls,
    workerUpdateCalls,
    policyUpdateCalls,
    poolMigrateCalls,
    bulkDeleteCalls,
    bulkCloneCalls,
    setUserRole,
  } = createMockServer();
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  const { port } = server.address();
  const baseUrl = `http://127.0.0.1:${port}/`;

  const tmpDir = await fs.mkdtemp(path.join('/tmp', 'cdp-ux-'));
  const chrome = spawn(
    chromeBin,
    [
      '--headless=new',
      '--no-sandbox',
      '--disable-gpu',
      '--remote-debugging-port=0',
      `--user-data-dir=${tmpDir}`,
      '--window-size=1440,960',
      'about:blank',
    ],
    {
      stdio: ['ignore', 'pipe', 'pipe'],
      env: { ...process.env, TMPDIR: '/tmp', XDG_RUNTIME_DIR: '/tmp' },
    },
  );

  let wsEndpoint;
  let chromeLogs = '';
  await new Promise((resolve, reject) => {
    const timeout = setTimeout(
      () => reject(new Error(`Chromium startup timeout:\n${chromeLogs}`)),
      10000,
    );
    const onData = (buf) => {
      chromeLogs += buf.toString();
      const m = chromeLogs.match(/DevTools listening on (ws:\/\/[^\s]+)/);
      if (m) {
        wsEndpoint = m[1];
        clearTimeout(timeout);
        resolve();
      }
    };
    chrome.stderr.on('data', onData);
    chrome.stdout.on('data', onData);
  });

  const targetsRes = await fetch(
    wsEndpoint.replace(/^ws:/, 'http:').replace(/\/devtools\/browser\/.*/, '/json/list'),
  );
  const targets = await targetsRes.json();
  const pageTarget = targets.find((tg) => tg.type === 'page');
  assert(pageTarget, 'Expected a page target');

  const cdp = new CDPClient(pageTarget.webSocketDebuggerUrl);
  await cdp.connect();
  await cdp.send('Page.enable');
  await cdp.send('Runtime.enable');

  try {
    await cdp.send('Page.navigate', { url: baseUrl });
    await waitFor(cdp, `document.body && document.body.innerText.includes('sbx-alpha')`, 8000, 'instances loaded');

    await cdp.evalJS(`
      window.__clickButtonByText = (text, root = document) => {
        const btns = Array.from(root.querySelectorAll('button'));
        const btn = btns.find((b) => (b.innerText || b.getAttribute('aria-label') || '').trim().includes(text));
        if (!btn) return false;
        btn.click();
        return true;
      };
      window.__getToastsText = () => Array.from(document.querySelectorAll('[data-sonner-toast]')).map(el => el.innerText.trim()).join(' | ');
      window.__setReactInput = (el, value) => {
        const proto = el.tagName === 'TEXTAREA' ? window.HTMLTextAreaElement.prototype : el.tagName === 'SELECT' ? window.HTMLSelectElement.prototype : window.HTMLInputElement.prototype;
        const setter = Object.getOwnPropertyDescriptor(proto, 'value').set;
        setter.call(el, value);
        el.dispatchEvent(new Event(el.tagName === 'SELECT' ? 'change' : 'input', { bubbles: true }));
      };
    `);

    await t.test('Branding is Compute, job VMs are hidden, quotas & defaults match self-hosted model', async () => {
      const pageText = await cdp.evalJS(`document.body.innerText`);
      assert(!pageText.toLowerCase().includes('boat.dev'), 'Must not mention boat.dev');
      assert(!pageText.includes('$0.018'), 'Must not display fake $0.018 cost');
      assert(!pageText.includes('job-ci-9999'), 'Defensive filter must hide owner_id=jobs VM from Sandboxes list');
      assert(pageText.includes('Compute'), 'Sidebar must display Compute branding');
      assert(pageText.includes('Resources'), 'Instances table must have Resources column');
      assert(pageText.includes('2 vCPU · 1 GB · 20 GB'), 'Resources cell should show vCPU, RAM, 20 GB Disk');

      await cdp.evalJS(`window.location.hash = '#/settings'`);
      await waitFor(cdp, `window.location.hash === '#/settings' && document.body.innerText.includes('Default VM Resources')`, 4000, 'settings defaults');
      const settingsText = await cdp.evalJS(`document.body.innerText`);
      assert(!settingsText.includes('Hourly Burn'), 'Settings must not mention Hourly Burn');
      assert(!settingsText.includes('Organization & Billing'), 'Settings must not mention Billing');
      assert(settingsText.includes('Default Disk Size'), 'Settings must show Default Disk Size');
      assert(settingsText.includes('20 GB'), 'Settings must show 20 GB default disk size');
      assert(settingsText.includes('ubuntu-24'), 'Settings must show ubuntu-24 default image');

      await cdp.evalJS(`window.location.hash = '#/workers'`);
      await waitFor(cdp, `window.location.hash === '#/workers' && document.body.innerText.includes('Quotas & Resource Limits')`, 4000, 'workers quotas');
      const workersText = await cdp.evalJS(`document.body.innerText`);
      assert(workersText.includes('Allocated Compute & Memory'), 'Workers page must show self-hosted compute allocation');

      await cdp.evalJS(`window.location.hash = '#/instances'`);
      await waitFor(cdp, `window.location.hash === '#/instances' && document.body.innerText.includes('sbx-alpha')`, 4000, 'back to instances');
    });

    await t.test('Single New button (no chevron) opens Create Sandbox with 20 GB default and Ubuntu 24.04 LTS', async () => {
      const hasChevron = await cdp.evalJS(`Boolean(document.querySelector('button[aria-label="More creation options"]'))`);
      assert.equal(hasChevron, false, 'Split dropdown chevron button must be removed');

      const clickedNew = await cdp.evalJS(`window.__clickButtonByText('New')`);
      assert.equal(clickedNew, true, 'Single New button must exist');
      await waitFor(cdp, `document.getElementById('advanced-create-title') !== null`, 3000, 'create modal open');

      const modalInfo = await cdp.evalJS(`(() => {
        const dialog = document.querySelector('[role="dialog"]');
        const diskInput = dialog.querySelector('input[aria-label="Disk size in GB"]');
        const imageSelect = dialog.querySelector('select[aria-label="Base OS image"]');
        return {
          text: dialog.innerText,
          diskVal: diskInput ? diskInput.value : null,
          hasOsPicker: Boolean(imageSelect),
        };
      })()`);
      assert.equal(modalInfo.hasOsPicker, false, 'Base OS picker must be removed');
      assert(modalInfo.text.includes('Ubuntu 24.04 LTS'), 'Create modal should state Ubuntu 24.04 LTS');
      assert.equal(modalInfo.diskVal, '20', 'Default disk size must be 20 GB');

      await cdp.evalJS(`(() => {
        const input = document.querySelector('input[aria-label="Sandbox name"]');
        window.__setReactInput(input, 'sbx-default-20g');
        input.closest('form').requestSubmit();
      })()`);
      await waitFor(cdp, `document.getElementById('advanced-create-title') === null`, 3000, 'create modal closed');
      await waitFor(cdp, `document.body.innerText.includes('sbx-default-20g')`, 4000, 'created sandbox rendered');
      assert.equal(createCalls.length, 1);
      assert.equal(createCalls[0].name, 'sbx-default-20g');
      assert.equal(createCalls[0].image, 'ubuntu-24');
      assert.equal(createCalls[0].disk_mb, 20480);
    });

    await t.test('Replicate dialog creates multiple replicas with overrides', async () => {
      await cdp.evalJS(`document.querySelector('button[aria-label="Replicate sbx-alpha"]').click()`);
      await waitFor(cdp, `document.getElementById('clone-vm-title') !== null`, 3000, 'clone modal open');
      await cdp.evalJS(`
        (() => {
          const baseInput = document.querySelector('input[aria-label="Replica base name"]');
          window.__setReactInput(baseInput, 'worker-pool');
          document.querySelector('button[aria-label="Replica count 2"]').click();
          const vcpuSel = document.querySelector('select[aria-label="Replica vCPUs"]');
          window.__setReactInput(vcpuSel, '4');
          const autoStart = document.getElementById('clone-auto-start');
          if (!autoStart.checked) autoStart.click();
          baseInput.closest('form').requestSubmit();
        })()
      `);
      await waitFor(cdp, `document.getElementById('clone-vm-title') === null`, 3000, 'clone modal closed');
      await waitFor(cdp, `document.body.innerText.includes('worker-pool-1') && document.body.innerText.includes('worker-pool-2')`, 4000, 'replicas rendered');
      assert.equal(bulkCloneCalls.length, 1, 'Expected single queued POST /api/v1/vms/bulk-clone request');
      assert.equal(cloneCalls.length, 0, 'Multi-replica replication must use bulk-clone, not per-VM clone');
      assert.deepEqual(bulkCloneCalls[0].names, ['worker-pool-1', 'worker-pool-2']);
      assert.equal(bulkCloneCalls[0].vcpus, 4);
      assert.equal(bulkCloneCalls[0].auto_start, true);
      assert.equal(bulkCloneCalls[0].async, true);
    });

    await t.test('Pause shows PAUSED (STATE SAVED) badge and Resume restores running', async () => {
      await cdp.evalJS(`document.querySelector('button[aria-label="Pause sbx-alpha"]').click()`);
      await waitFor(cdp, `document.body.innerText.includes('PAUSED (STATE SAVED)')`, 4000, 'paused state saved badge');
      await cdp.evalJS(`document.querySelector('button[aria-label="Resume sbx-alpha"]').click()`);
      await waitFor(cdp, `!document.body.innerText.includes('PAUSED (STATE SAVED)')`, 4000, 'resumed to running');
    });

    await t.test('Redesigned calm Sandbox Detail View replaces legacy Inspector and Snapshots page omits Image Builder', async () => {
      await cdp.evalJS(`Array.from(document.querySelectorAll('tr')).find(r => r.innerText.includes('sbx-alpha')).click()`);
      await waitFor(cdp, `document.querySelector('[data-testid="sandbox-detail-panel"]') !== null`, 3000, 'detail panel open');
      const detailText = await cdp.evalJS(`document.querySelector('[data-testid="sandbox-detail-panel"]').innerText`);
      assert(!detailText.includes('SANDBOX INSPECTOR'), 'Legacy SANDBOX INSPECTOR header must be gone');
      assert(!detailText.includes('Execute Command'), 'Non-interactive exec must be removed');
      assert(!detailText.includes('Execute via SSH'), 'Legacy Execute via SSH section must be removed');
      assert(detailText.includes('Reconnect') && detailText.includes('Pop Out'), 'Embedded terminal controls must be present');
      assert(detailText.includes('Compute Specs') && detailText.includes('Attached Volumes') && detailText.includes('Port Forwards & Ingress'), 'Resource and connectivity cards must render');

      await cdp.evalJS(`document.querySelector('button[aria-label="Snapshot sbx-alpha"]').click()`);
      await waitFor(cdp, `document.getElementById('create-snapshot-title') !== null`, 3000, 'snapshot modal open');
      await cdp.evalJS(`
        (() => {
          const input = document.querySelector('input[aria-label="Snapshot name"]');
          window.__setReactInput(input, 'alpha-golden');
          input.closest('form').requestSubmit();
        })()
      `);
      await waitFor(cdp, `document.getElementById('create-snapshot-title') === null`, 3000, 'snapshot modal closed');

      await cdp.evalJS(`window.location.hash = '#/snapshots'`);
      await waitFor(
        cdp,
        `window.location.hash === '#/snapshots' && document.querySelector('[data-testid="sandbox-detail-panel"]') === null && document.body.innerText.includes('alpha-golden') && Array.from(document.querySelectorAll('button')).some(b => b.innerText.trim().includes('Launch'))`,
        4000,
        'snapshots page loaded',
      );
      const snapPageText = await cdp.evalJS(`document.body.innerText`);
      assert(!snapPageText.includes('Custom Rootfs Images') && !snapPageText.includes('Build Image'), 'Custom Rootfs Image builder must be removed from Snapshots page');

      const clickedLaunch = await cdp.evalJS(`window.__clickButtonByText('Launch')`);
      assert.equal(clickedLaunch, true, 'Launch button must be found and clicked');
      await waitFor(cdp, `document.getElementById('launch-snapshot-title') !== null`, 3000, 'launch modal open');
      await cdp.evalJS(`
        (() => {
          const input = document.querySelector('input[aria-label="Launch sandbox name"]');
          window.__setReactInput(input, 'sbx-from-golden');
          input.closest('form').requestSubmit();
        })()
      `);
      await waitFor(
        cdp,
        `window.location.hash === '#/instances' && document.querySelector('table')?.innerText.includes('sbx-from-golden')`,
        4000,
        'navigated to instances with launched sandbox',
      );
      assert.equal(launchCalls.length, 1);
      assert.equal(launchCalls[0].body.name, 'sbx-from-golden');
      assert.equal(launchCalls[0].body.auto_start, true);
    });

    await t.test('Raw popup terminal route renders pure chromeless xterm viewport', async () => {
      await cdp.evalJS(`window.location.hash = '#/terminal/vm-alpha?user=compute'`);
      await waitFor(cdp, `document.querySelector('[data-testid="raw-terminal-window"]') !== null`, 4000, 'raw terminal window');
      const termStats = await cdp.evalJS(`(() => {
        const root = document.querySelector('[data-testid="raw-terminal-window"]');
        return {
          buttonCount: root.querySelectorAll('button').length,
          hasXterm: Boolean(root.querySelector('.xterm')),
          width: root.clientWidth,
          height: root.clientHeight,
        };
      })()`);
      assert.equal(termStats.buttonCount, 0, 'Raw terminal window must have zero buttons/chrome');
      assert.equal(termStats.hasXterm, true, 'Raw terminal window must mount xterm canvas');
      assert(termStats.width >= 1000 && termStats.height >= 700, 'Raw terminal must fill viewport');
    });

    await t.test('CI Jobs page connects GitHub repos, triggers runs, and streams logs', async () => {
      await cdp.evalJS(`window.location.hash = '#/jobs'`);
      await waitFor(cdp, `window.location.hash === '#/jobs' && document.body.innerText.includes('CI Jobs')`, 4000, 'ci jobs page loaded');

      const clickedConnect = await cdp.evalJS(`window.__clickButtonByText('Connect GitHub Repository')`);
      assert.equal(clickedConnect, true, 'Connect GitHub Repository button must be present');
      await waitFor(cdp, `document.getElementById('ci-repo-modal-title') !== null`, 3000, 'connect repo modal open');
      await cdp.evalJS(`(() => {
        window.__setReactInput(document.querySelector('input[aria-label="Repository URL"]'), 'https://github.com/acme/api-service');
        window.__setReactInput(document.querySelector('input[aria-label="Default branch"]'), 'main');
        window.__setReactInput(document.querySelector('input[aria-label="Default CI command"]'), 'npm test');
        document.getElementById('ci-repo-modal-title').closest('form').requestSubmit();
      })()`);
      await waitFor(cdp, `document.getElementById('ci-repo-modal-title') === null`, 3000, 'connect repo modal closed');
      await waitFor(cdp, `document.body.innerText.includes('whsec_test_secret_123') && document.body.innerText.includes('acme/api-service')`, 4000, 'connected repo and secret banner');

      const clickedTrigger = await cdp.evalJS(`window.__clickButtonByText('Trigger')`);
      assert.equal(clickedTrigger, true, 'Trigger button must exist for connected repo');
      await waitFor(cdp, `document.body.innerText.includes('Trigger CI Run')`, 3000, 'trigger modal open');
      await cdp.evalJS(`window.__clickButtonByText('Start CI Run')`);
      await waitFor(cdp, `document.querySelector('[data-testid="job-logs-drawer"]') !== null`, 4000, 'job logs drawer open');
      await waitFor(cdp, `document.querySelector('[data-testid="job-logs-drawer"]').innerText.includes('All CI tests passed')`, 4000, 'job logs rendered');
    });

    await t.test('Admin Resources page shows live host metrics, warnings, and platform allocations', async () => {
      await cdp.evalJS(`window.location.hash = '#/resources'`);
      await waitFor(cdp, `window.location.hash === '#/resources' && document.body.innerText.includes('34.5%')`, 4000, 'resources telemetry loaded');
      const resText = await cdp.evalJS(`document.body.innerText`);
      assert(resText.includes('37.5%'), 'Expected host memory utilization percentage');
      assert(resText.includes('29.0%'), 'Expected host disk utilization percentage');
      assert(resText.includes('compute-host-01'), 'Expected host system hostname');
      assert(resText.includes('Platform Allocations'), 'Expected Platform Allocations section');
      const hasWarning = await cdp.evalJS(`document.querySelector('[data-testid="capacity-warnings-banner"]')?.innerText.includes('Memory headroom approaching threshold')`);
      assert.equal(hasWarning, true, 'Expected capacity warnings banner');
    });

    await t.test('Zero manual Refresh buttons exist across views', async () => {
      const routes = [
        { hash: '#/instances', waitForExpr: `document.body.innerText.includes('sbx-alpha')` },
        { hash: '#/snapshots', waitForExpr: `document.body.innerText.includes('Create Snapshot')` },
        { hash: '#/storage', waitForExpr: `document.body.innerText.includes('Persistent Volumes')` },
        { hash: '#/jobs', waitForExpr: `document.body.innerText.includes('CI Jobs')` },
        { hash: '#/workers', waitForExpr: `document.body.innerText.includes('Worker Nodes')` },
        { hash: '#/resources', waitForExpr: `document.body.innerText.includes('Platform Allocations')` },
        { hash: '#/settings', waitForExpr: `document.body.innerText.includes('Default VM Resources')` },
        { hash: '#/access', waitForExpr: `document.body.innerText.includes('Create API Key')` },
        { hash: '#/events', waitForExpr: `document.body.innerText.includes('Add Webhook')` },
      ];
      for (const r of routes) {
        await cdp.evalJS(`window.location.hash = '${r.hash}'`);
        await waitFor(cdp, `window.location.hash === '${r.hash}' && ${r.waitForExpr}`, 4000, `route ${r.hash}`);
        const badButtons = await cdp.evalJS(`
          Array.from(document.querySelectorAll('button'))
            .map(b => (b.innerText || '').trim())
            .filter(t => t === 'Refresh' || t === 'Refresh Logs' || t === 'Refresh Cluster State')
        `);
        assert.deepEqual(badButtons, [], `Unexpected manual refresh buttons on ${r.hash}: ${JSON.stringify(badButtons)}`);
      }
    });

    await t.test('Create Sandbox Placement selector and 429 ResourceLimitError formatting', async () => {
      await cdp.evalJS(`window.location.hash = '#/instances'`);
      await waitFor(cdp, `window.location.hash === '#/instances' && document.body.innerText.includes('sbx-alpha')`, 4000, 'instances for placement');

      await cdp.evalJS(`window.__clickButtonByText('New')`);
      await waitFor(cdp, `document.getElementById('advanced-create-title') !== null`, 3000, 'create modal open for worker placement');
      await cdp.evalJS(`window.__clickButtonByText('Worker Node')`);
      await waitFor(cdp, `document.querySelector('select[aria-label="Placement worker node"]') !== null`, 3000, 'placement worker select visible');
      await cdp.evalJS(`(() => {
        const sel = document.querySelector('select[aria-label="Placement worker node"]');
        window.__setReactInput(sel, 'nod-2');
        const input = document.querySelector('input[aria-label="Sandbox name"]');
        window.__setReactInput(input, 'sbx-placed-w2');
        input.closest('form').requestSubmit();
      })()`);
      await waitFor(cdp, `document.getElementById('advanced-create-title') === null`, 3000, 'placed create modal closed');
      await waitFor(cdp, `document.body.innerText.includes('sbx-placed-w2')`, 4000, 'placed sandbox rendered');
      const placedCall = createCalls.find((c) => c.name === 'sbx-placed-w2');
      assert(placedCall, 'Expected sbx-placed-w2 create call');
      assert.equal(placedCall.worker_id, 'nod-2');
      assert.equal(placedCall.pool_id, 'gpu-pool');

      // Test 429 ResourceLimitError toast formatting
      await cdp.evalJS(`window.__clickButtonByText('New')`);
      await waitFor(cdp, `document.getElementById('advanced-create-title') !== null`, 3000, 'create modal open for 429');
      await cdp.evalJS(`(() => {
        const input = document.querySelector('input[aria-label="Sandbox name"]');
        window.__setReactInput(input, 'sbx-limit-test');
        input.closest('form').requestSubmit();
      })()`);
      await waitFor(
        cdp,
        `window.__getToastsText().includes('Worker node memory limit exceeded') && window.__getToastsText().includes('nod-1') && window.__getToastsText().includes('Select another worker pool')`,
        4000,
        '429 ResourceLimitError formatted toast',
      );
    });

    await t.test('Workers page supports expandable sandboxes, Edit Allocation/Pool, Load Balancer policy & Pool Migration', async () => {
      await cdp.evalJS(`window.location.hash = '#/workers'`);
      await waitFor(cdp, `window.location.hash === '#/workers' && document.body.innerText.includes('worker-01')`, 4000, 'workers loaded');

      // Expand worker-01 sandboxes
      await cdp.evalJS(`document.querySelector('button[aria-label="Toggle sandboxes for worker-01"]').click()`);
      await waitFor(cdp, `document.body.innerText.includes('Sandboxes on worker-01') && document.body.innerText.includes('sbx-alpha')`, 3000, 'expanded worker sandboxes');

      // Edit node allocation & pool
      await cdp.evalJS(`document.querySelector('button[title="Edit Node Allocation & Pool"]').click()`);
      await waitFor(cdp, `document.body.innerText.includes('Edit Node Allocation & Pool')`, 3000, 'edit node modal open');
      await cdp.evalJS(`(() => {
        const poolSel = document.querySelector('select[aria-label="Node Pool"]');
        window.__setReactInput(poolSel, 'gpu-pool');
        poolSel.closest('form').requestSubmit();
      })()`);
      await waitFor(cdp, `!document.body.innerText.includes('Edit Node Allocation & Pool')`, 3000, 'edit node modal closed');
      await waitFor(cdp, `window.__getToastsText().includes('Updated node worker-01')`, 3000, 'edit node toast success');
      assert.equal(workerUpdateCalls.length, 1);
      assert.equal(workerUpdateCalls[0].body.pool_id, 'gpu-pool');

      // Save Cluster Load Balancer & Resource Policy
      await cdp.evalJS(`(() => {
        const stratSel = document.querySelector('select[aria-label="Placement Strategy"]');
        window.__setReactInput(stratSel, 'bin_pack');
      })()`);
      await cdp.evalJS(`window.__clickButtonByText('Save Policy')`);
      await waitFor(cdp, `window.__getToastsText().includes('Scheduling & load-balancer policy updated')`, 3000, 'policy save toast');
      assert.equal(policyUpdateCalls.length, 1);
      assert.equal(policyUpdateCalls[0].strategy, 'bin_pack');

      // Migrate Pool
      await cdp.evalJS(`window.__clickButtonByText('Migrate Pool')`);
      await waitFor(cdp, `document.querySelector('[role="dialog"][aria-label="Migrate Pool"]') !== null`, 3000, 'migrate pool modal open');
      await cdp.evalJS(`(() => {
        const dlg = document.querySelector('[role="dialog"][aria-label="Migrate Pool"]');
        const destSel = dlg.querySelector('select[aria-label="Destination Pool"]');
        window.__setReactInput(destSel, 'gpu-pool');
        dlg.querySelector('form').requestSubmit();
      })()`);
      await waitFor(cdp, `document.querySelector('[role="dialog"][aria-label="Migrate Pool"]') === null`, 3000, 'migrate pool modal closed');
      await waitFor(cdp, `window.__getToastsText().includes('Migrated pool')`, 3000, 'migrate pool toast');
      assert.equal(poolMigrateCalls.length, 1);
      assert.equal(poolMigrateCalls[0].body.target_pool_id, 'gpu-pool');
    });

    await t.test('Instances shows SSD Hibernated badge and Migrate Sandbox modal', async () => {
      await cdp.evalJS(`window.location.hash = '#/instances'`);
      await waitFor(cdp, `window.location.hash === '#/instances' && document.body.innerText.includes('sbx-hibernated')`, 4000, 'instances with hibernated VM');
      const instText = await cdp.evalJS(`document.body.innerText`);
      assert(instText.toUpperCase().includes('SSD HIBERNATED'), 'Expected SSD Hibernated badge on hibernated sandbox');

      await cdp.realClick(`document.querySelector('button[aria-label="More actions for sbx-alpha"]')`);
      await waitFor(cdp, `Array.from(document.querySelectorAll('[role="menuitem"]')).some(el => el.innerText.trim() === 'Migrate Sandbox')`, 3000, 'migrate menu item open');
      await sleep(120);
      await cdp.realClick(`Array.from(document.querySelectorAll('[role="menuitem"]')).find(el => el.innerText.trim() === 'Migrate Sandbox')`);
      await waitFor(cdp, `document.querySelector('form[aria-label="Migrate Sandbox"]') !== null`, 3000, 'migrate sandbox modal open');
      await cdp.evalJS(`(() => {
        const form = document.querySelector('form[aria-label="Migrate Sandbox"]');
        const workerSel = form.querySelector('select[aria-label="Target Worker Node"]');
        window.__setReactInput(workerSel, 'nod-2');
        form.requestSubmit();
      })()`);
      await waitFor(cdp, `document.querySelector('form[aria-label="Migrate Sandbox"]') === null`, 3000, 'migrate sandbox modal closed');
      await waitFor(cdp, `window.__getToastsText().includes('Migrated sandbox sbx-alpha')`, 3000, 'migrate sandbox toast');
      assert.equal(migrateCalls.length, 1);
      assert.equal(migrateCalls[0].body.target_worker_id, 'nod-2');
    });

    await t.test('Access Users tab displays signup status, provisions users, and deletes non-admin users', async () => {
      await cdp.evalJS(`window.location.hash = '#/access'`);
      await waitFor(
        cdp,
        `window.location.hash === '#/access' && document.querySelector('[data-testid="access-tab-users"]') !== null`,
        4000,
        'access page with Users tab',
      );
      await cdp.evalJS(`document.querySelector('[data-testid="access-tab-users"]').click()`);
      await waitFor(
        cdp,
        `document.querySelector('[data-testid="access-tab-users"]')?.getAttribute('aria-selected') === 'true' && document.body.innerText.toUpperCase().includes('DISABLED (ADMIN-ONLY PROVISIONING)')`,
        4000,
        'signup disabled banner',
      );

      const clickedCreate = await cdp.evalJS(`window.__clickButtonByText('Create User')`);
      assert.equal(clickedCreate, true, 'Create User button must be found and clicked');
      await waitFor(cdp, `document.querySelector('input[aria-label="New username"]') !== null`, 3000, 'create user form open');
      await cdp.evalJS(`
        (() => {
          window.__setReactInput(document.querySelector('input[aria-label="New username"]'), 'alice');
          window.__setReactInput(document.querySelector('input[aria-label="New email"]'), 'alice@compute.local');
          window.__setReactInput(document.querySelector('input[aria-label="New password"]'), 'supersecret123');
          document.querySelector('input[aria-label="New username"]').closest('form').requestSubmit();
        })()
      `);
      await waitFor(cdp, `document.body.innerText.includes('alice@compute.local')`, 4000, 'created user rendered');

      await cdp.evalJS(`document.querySelector('button[aria-label="Delete user alice"]').click()`);
      await waitFor(cdp, `!document.body.innerText.includes('alice@compute.local')`, 4000, 'deleted user removed');

      // Verify non-admin user sees admin-required notice and cannot see Create User button
      setUserRole('user');
      await cdp.evalJS(`
        localStorage.setItem('compute_auth_user', JSON.stringify({
          id: 'usr_viewer',
          username: 'viewer',
          email: 'viewer@compute.local',
          role: 'user',
          status: 'active'
        }));
        window.location.hash = '#/instances';
      `);
      await waitFor(cdp, `window.location.hash === '#/instances'`, 3000, 'switch away from access');
      await cdp.evalJS(`window.location.hash = '#/access'`);
      await waitFor(cdp, `document.querySelector('[data-testid="access-tab-users"]') !== null`, 3000, 'access page reloaded as non-admin');
      await cdp.evalJS(`document.querySelector('[data-testid="access-tab-users"]').click()`);
      await waitFor(
        cdp,
        `document.body.innerText.includes('Admin privileges required') && !Array.from(document.querySelectorAll('button')).some(b => b.innerText.trim() === 'Create User')`,
        4000,
        'non-admin guard hides Create User and shows notice',
      );
      setUserRole('admin');
    });
  } finally {
    try { cdp.close(); } catch {}
    chrome.kill('SIGKILL');
    await new Promise((resolve) => chrome.once('exit', resolve)).catch(() => {});
    server.close();
    await fs.rm(tmpDir, { recursive: true, force: true }).catch(() => {});
  }
});
