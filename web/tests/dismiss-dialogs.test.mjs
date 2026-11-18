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
const DELAY_MS = 550;

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
      ip_address: '172.16.0.10',
      public_ip: '172.16.0.10',
      public_ip_enabled: true,
      mac_address: '02:fc:00:00:00:10',
      tap_device: 'tap0',
      pid: 12345,
      disk_path: '/var/lib/compute/vms/vm-alpha/disk.ext4',
      rootfs_path: '/var/lib/compute/images/ubuntu.ext4',
      kernel_path: '/var/lib/compute/vmlinux',
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
      protected: false,
    },
    {
      id: 'vm-beta',
      name: 'sbx-beta',
      status: 'stopped',
      vcpus: 1,
      memory_mb: 512,
      ip_address: '172.16.0.11',
      public_ip_enabled: false,
      mac_address: '02:fc:00:00:00:11',
      tap_device: 'tap1',
      disk_path: '/var/lib/compute/vms/vm-beta/disk.ext4',
      rootfs_path: '/var/lib/compute/images/ubuntu.ext4',
      kernel_path: '/var/lib/compute/vmlinux',
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
      protected: false,
    },
    {
      id: 'vm-gamma',
      name: 'sbx-gamma',
      status: 'stopped',
      vcpus: 1,
      memory_mb: 512,
      ip_address: '172.16.0.13',
      public_ip_enabled: false,
      mac_address: '02:fc:00:00:00:13',
      tap_device: 'tap3',
      disk_path: '/var/lib/compute/vms/vm-gamma/disk.ext4',
      rootfs_path: '/var/lib/compute/images/ubuntu.ext4',
      kernel_path: '/var/lib/compute/vmlinux',
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
      protected: false,
    },
    {
      id: 'vm-delta',
      name: 'sbx-delta',
      status: 'stopped',
      vcpus: 1,
      memory_mb: 512,
      ip_address: '172.16.0.14',
      public_ip_enabled: false,
      mac_address: '02:fc:00:00:00:14',
      tap_device: 'tap4',
      disk_path: '/var/lib/compute/vms/vm-delta/disk.ext4',
      rootfs_path: '/var/lib/compute/images/ubuntu.ext4',
      kernel_path: '/var/lib/compute/vmlinux',
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
      protected: false,
    },
    {
      id: 'vm-fail',
      name: 'sbx-fail',
      status: 'stopped',
      vcpus: 1,
      memory_mb: 512,
      ip_address: '172.16.0.15',
      public_ip_enabled: false,
      mac_address: '02:fc:00:00:00:15',
      tap_device: 'tap5',
      disk_path: '/var/lib/compute/vms/vm-fail/disk.ext4',
      rootfs_path: '/var/lib/compute/images/ubuntu.ext4',
      kernel_path: '/var/lib/compute/vmlinux',
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
      protected: false,
    },
    {
      id: 'vm-prot',
      name: 'sbx-protected',
      status: 'stopped',
      vcpus: 1,
      memory_mb: 512,
      ip_address: '172.16.0.12',
      public_ip_enabled: false,
      mac_address: '02:fc:00:00:00:12',
      tap_device: 'tap2',
      disk_path: '/var/lib/compute/vms/vm-prot/disk.ext4',
      rootfs_path: '/var/lib/compute/images/ubuntu.ext4',
      kernel_path: '/var/lib/compute/vmlinux',
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
      protected: true,
    },
  ];
  let volumes = [
    {
      id: 'vol-1',
      name: 'pgdata',
      pool: 'default',
      filesystem: 'ext4',
      mount_point: '/mnt/pgdata',
      device_path: '/dev/vdb',
      status: 'available',
      size_gb: 10,
      used_gb: 1.2,
      iops: 3000,
      throughput: '125 MB/s',
      snapshots: 0,
      created_at: new Date().toISOString(),
    },
  ];
  let templates = [];
  let images = [];
  let jobs = [];
  let portForwards = [];
  let pools = [{ id: 'pool-default', name: 'default' }];
  let workers = [
    {
      id: 'nod_1',
      name: 'worker-01',
      pool_id: 'default',
      role: 'worker',
      status: 'ready',
      underlay_ip: '10.0.0.11',
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
  let bulkDeleteCalls = [];
  let webhooks = [];
  let ciRepos = [];
  let apiKeys = [];
  let sshKeys = [];

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
      if (p === '/api/v1/auth/me') return json(200, { user: { id: 'u1', username: 'admin', role: 'admin' } });
      if (p === '/api/v1/auth/status') return json(200, { auth_enabled: true, signup_enabled: false });
      if (p === '/api/v1/users' && req.method === 'GET') return json(200, [{ id: 'u1', username: 'admin', email: 'admin@local', role: 'admin', status: 'active' }]);
      if (p === '/api/v1/health') return json(200, { status: 'ok' });
      if (p === '/api/v1/stats') {
        return json(200, {
          total_vms: vms.length,
          running_vms: vms.filter((v) => v.status === 'running').length,
          snapshotted_vms: 0,
          queued_vms: 0,
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
          defaults: { vcpus: 2, memory_mb: 1024 },
          images: { available: [{ name: 'ubuntu-24' }], vms_dir: '/var/lib/compute/vms', images_dir: '/var/lib/compute/images' },
          resources: { max_total_disk_mb: 512000, max_total_vcpus: 16, max_total_memory_mb: 32768 },
        });
      }
      if (p === '/api/v1/network/config') {
        return json(200, { bridge_name: 'fcbr0', mode: 'nat', subnet: '172.16.0.0/24', bridge_ip: '172.16.0.1' });
      }
      if (p === '/api/v1/network/allocations') {
        return json(200, vms.map((v) => ({
          vm_id: v.id,
          vm_name: v.name,
          ip_address: v.ip_address,
          public_ip: v.public_ip,
          public_ip_enabled: v.public_ip_enabled,
          mac_address: v.mac_address,
          tap_device: v.tap_device,
          status: v.status,
        })));
      }
      if (p === '/api/v1/workers' && req.method === 'GET') return json(200, workers);
      const workerUpdateMatch = p.match(/^\/api\/v1\/workers\/([^/]+)$/);
      if (workerUpdateMatch && (req.method === 'PATCH' || req.method === 'PUT')) {
        workers = workers.map((w) => (w.id === workerUpdateMatch[1] ? { ...w, ...body } : w));
        return json(200, workers.find((w) => w.id === workerUpdateMatch[1]) || {});
      }
      if (p === '/api/v1/workers/tokens' || (p === '/api/v1/worker-tokens' && req.method === 'GET')) {
        return json(200, workerTokens);
      }
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
      if (p === '/api/v1/scheduler/policy' && req.method === 'GET') return json(200, schedulingPolicy);
      if (p === '/api/v1/scheduler/policy' && req.method === 'PUT') {
        schedulingPolicy = { ...schedulingPolicy, ...body, updated_at: new Date().toISOString() };
        return json(200, schedulingPolicy);
      }
      if (p === '/api/v1/events') return json(200, []);
      if (p === '/api/v1/events/stream') {
        res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache' });
        return;
      }
      if (p === '/api/v1/queue') return json(200, { count: 0, items: [] });
      if (p === '/api/v1/tiers') return json(200, []);
      if (p === '/api/v1/environments') return json(200, []);
      if (p === '/api/v1/otel') return json(200, { enabled: false, endpoint: '', protocol: 'HTTP/JSON (4318)', service_name: 'compute' });

      if (p === '/api/v1/vms' && req.method === 'GET') return json(200, vms);
      if (p === '/api/v1/vms' && req.method === 'POST') {
        await sleep(DELAY_MS);
        const created = {
          id: `vm-${Date.now()}`,
          name: body.name || 'sbx-new',
          status: 'stopped',
          vcpus: body.vcpus || 1,
          memory_mb: body.memory_mb || 512,
          ip_address: '172.16.0.50',
          public_ip_enabled: true,
          mac_address: '02:fc:00:00:00:50',
          tap_device: 'tap50',
          disk_path: '/var/lib/compute/vms/new/disk.ext4',
          rootfs_path: '/var/lib/compute/images/ubuntu.ext4',
          kernel_path: '/var/lib/compute/vmlinux',
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
          protected: Boolean(body.protected),
        };
        vms = [...vms, created];
        return json(201, created);
      }
      if (p === '/api/v1/vms/bulk-delete' && req.method === 'POST') {
        await sleep(DELAY_MS);
        bulkDeleteCalls.push(body);
        const ids = Array.isArray(body.ids) ? body.ids : (Array.isArray(body.vm_ids) ? body.vm_ids : []);
        const succeeded = [];
        const failed = [];
        for (const id of ids) {
          if (id === 'vm-fail') {
            failed.push({ id, error: 'simulated delete failure' });
          } else {
            vms = vms.filter((v) => v.id !== id);
            succeeded.push(id);
          }
        }
        return json(202, { action: 'delete', status: 'queued', task_id: 'task_test_bulk_delete', requested: ids.length, queued: succeeded.length, succeeded: [], failed: [] });
      }

      const vmMatch = p.match(/^\/api\/v1\/vms\/([^/]+)(?:\/(.*))?$/);
      if (vmMatch) {
        const [, id, sub] = vmMatch;
        if (!sub && req.method === 'DELETE') {
          await sleep(DELAY_MS);
          if (id === 'vm-fail') {
            return json(500, { error: 'simulated delete failure' });
          }
          vms = vms.filter((v) => v.id !== id);
          return json(200, { deleted: true });
        }
        if (sub === 'run' && req.method === 'POST') {
          await sleep(200);
          vms = vms.map((v) => (v.id === id ? { ...v, status: 'running' } : v));
          return json(200, { status: 'running' });
        }
        if (sub === 'stop' && req.method === 'POST') {
          await sleep(DELAY_MS);
          vms = vms.map((v) => (v.id === id ? { ...v, status: 'stopped', pid: 0 } : v));
          return json(200, { status: 'stopped' });
        }
        if (sub === 'unprotect' && req.method === 'POST') {
          await sleep(DELAY_MS);
          vms = vms.map((v) => (v.id === id ? { ...v, protected: false } : v));
          return json(200, { protected: false });
        }
        if (sub === 'migrate' && req.method === 'POST') {
          await sleep(DELAY_MS);
          vms = vms.map((v) =>
            v.id === id
              ? {
                  ...v,
                  worker_id: body.target_worker_id || v.worker_id,
                  pool_id: body.target_pool_id || v.pool_id,
                }
              : v,
          );
          return json(200, vms.find((v) => v.id === id) || {});
        }
        if (sub === 'network' && req.method === 'GET') {
          return json(200, { vm_id: id, reachable: true, bridge_name: 'fcbr0', gateway: '172.16.0.1', connectivity: 'OK' });
        }
        if (sub === 'public-ip' && (req.method === 'PATCH' || req.method === 'POST' || req.method === 'PUT')) {
          await sleep(DELAY_MS);
          const next = Boolean(body.public_ip_enabled ?? body.enabled);
          vms = vms.map((v) => (v.id === id ? { ...v, public_ip_enabled: next } : v));
          return json(200, { public_ip_enabled: next });
        }
      }

      if (p === '/api/v1/volumes' && req.method === 'GET') return json(200, volumes);
      if (p === '/api/v1/volumes' && req.method === 'POST') {
        await sleep(DELAY_MS);
        const created = {
          id: `vol-${Date.now()}`,
          name: body.name,
          pool: body.pool || 'default',
          filesystem: body.filesystem || 'ext4',
          mount_point: '/mnt/data',
          device_path: '/dev/vdc',
          status: 'available',
          size_gb: body.size_gb || 10,
          used_gb: 0,
          iops: 3000,
          throughput: '125 MB/s',
          snapshots: 0,
          created_at: new Date().toISOString(),
        };
        volumes = [...volumes, created];
        return json(201, created);
      }
      if (p.match(/^\/api\/v1\/volumes\/([^/]+)\/attach$/) && req.method === 'POST') {
        const volId = p.match(/^\/api\/v1\/volumes\/([^/]+)\/attach$/)[1];
        await sleep(DELAY_MS);
        volumes = volumes.map((v) => (v.id === volId ? { ...v, status: 'attached', attached_vm_id: body.vm_id } : v));
        return json(200, { status: 'attached' });
      }

      if (p === '/api/v1/templates' && req.method === 'GET') return json(200, templates);
      if (p === '/api/v1/templates' && req.method === 'POST') {
        await sleep(DELAY_MS);
        const tpl = { id: `tpl-${Date.now()}`, name: body.name, description: body.description, source_vm_id: body.vm_id, vcpus: 2, memory_mb: 1024, disk_path: '/tmp/tpl', created_at: new Date().toISOString() };
        templates = [...templates, tpl];
        return json(201, tpl);
      }

      if (p === '/api/v1/ci/repos' && req.method === 'GET') return json(200, ciRepos);
      if (p === '/api/v1/ci/repos' && req.method === 'POST') {
        await sleep(DELAY_MS);
        const repo = {
          id: `gh-${Date.now()}`,
          owner_id: 'u1',
          name: body.name || 'acme/repo',
          repo_url: body.repo_url,
          default_branch: body.default_branch || 'main',
          has_token: Boolean(body.github_token),
          has_webhook_secret: true,
          webhook_secret: 'whsec_test_123',
          default_command: body.default_command || 'npm test',
          vcpus: body.vcpus || 2,
          memory_mb: body.memory_mb || 2048,
          timeout_sec: body.timeout_sec || 600,
          created_at: new Date().toISOString(),
        };
        ciRepos = [...ciRepos, repo];
        return json(201, repo);
      }

      if (p === '/api/v1/jobs' && req.method === 'GET') return json(200, jobs);
      if (p === '/api/v1/jobs' && req.method === 'POST') {
        await sleep(DELAY_MS);
        const job = { id: `job-${Date.now()}`, type: 'command', pool_id: 'default', state: 'queued', spec_json: JSON.stringify(body.spec), queued_at: new Date().toISOString() };
        jobs = [...jobs, job];
        return json(201, job);
      }
      if (p.match(/^\/api\/v1\/jobs\/[^/]+\/logs$/) && req.method === 'GET') {
        return json(200, []);
      }

      if (p === '/api/v1/network/port-forwards' && req.method === 'GET') return json(200, portForwards);
      if (p === '/api/v1/network/port-forwards' && req.method === 'POST') {
        await sleep(DELAY_MS);
        const pf = { id: `pf-${Date.now()}`, ...body, created_at: new Date().toISOString() };
        portForwards = [...portForwards, pf];
        return json(201, pf);
      }

      if (p === '/api/v1/pools' && req.method === 'GET') return json(200, pools);
      if (p === '/api/v1/pools' && req.method === 'POST') {
        await sleep(DELAY_MS);
        const pool = { id: `pool-${Date.now()}`, name: body.name };
        pools = [...pools, pool];
        return json(201, pool);
      }

      if (p === '/api/v1/webhooks' && req.method === 'GET') return json(200, webhooks);
      if (p === '/api/v1/webhooks' && req.method === 'POST') {
        await sleep(DELAY_MS);
        const wh = { id: `wh-${Date.now()}`, ...body, secret: 'whsec_test123', active: true };
        webhooks = [...webhooks, wh];
        return json(201, wh);
      }

      if (p === '/api/v1/api-keys' && req.method === 'GET') return json(200, apiKeys);
      if (p === '/api/v1/api-keys' && req.method === 'POST') {
        await sleep(DELAY_MS);
        const ak = { id: `ak-${Date.now()}`, name: body.name, prefix: 'cmp_live_', scope: body.scope, expires_at: body.expires_at, token: 'cmp_live_secret_token_123', created_at: new Date().toISOString() };
        apiKeys = [...apiKeys, ak];
        return json(201, ak);
      }

      if (p === '/api/v1/keys' && req.method === 'GET') return json(200, sshKeys);
      if (p === '/api/v1/keys' && req.method === 'POST') {
        await sleep(DELAY_MS);
        const sk = { id: `sk-${Date.now()}`, name: body.name, public_key: body.public_key, fingerprint: 'SHA256:test', created_at: new Date().toISOString() };
        sshKeys = [...sshKeys, sk];
        return json(201, sk);
      }
      if (p.match(/^\/api\/v1\/keys\/[^/]+$/) && req.method === 'DELETE') {
        const keyId = p.split('/').pop();
        await sleep(DELAY_MS);
        sshKeys = sshKeys.filter((k) => k.id !== keyId);
        return json(200, { deleted: true });
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

  server.bulkDeleteCalls = bulkDeleteCalls;
  return server;
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
      const el = (${pointExpr});
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

test('operation dialogs and drawers close immediately while loading toasts persist', async (t) => {
  const chromeBin = findChromium();
  if (!chromeBin) {
    t.skip('No Chromium/Chrome binary found for browser E2E verification');
    return;
  }

  const server = createMockServer();
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  const { port } = server.address();
  const baseUrl = `http://127.0.0.1:${port}/`;

  const tmpDir = await fs.mkdtemp(path.join('/tmp', 'cdp-v-'));
  const chrome = spawn(chromeBin, [
    '--headless=new',
    '--no-sandbox',
    '--disable-gpu',
    '--remote-debugging-port=0',
    `--user-data-dir=${tmpDir}`,
    '--window-size=1440,960',
    'about:blank',
  ], {
    stdio: ['ignore', 'pipe', 'pipe'],
    env: { ...process.env, TMPDIR: '/tmp', XDG_RUNTIME_DIR: '/tmp' },
  });

  let wsEndpoint;
  let chromeLogs = '';
  await new Promise((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error(`Chromium startup timeout:\n${chromeLogs}`)), 10000);
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

  const targetsRes = await fetch(wsEndpoint.replace(/^ws:/, 'http:').replace(/\/devtools\/browser\/.*/, '/json/list'));
  const targets = await targetsRes.json();
  const pageTarget = targets.find((tg) => tg.type === 'page');
  assert(pageTarget, 'Expected a page target');

  const cdp = new CDPClient(pageTarget.webSocketDebuggerUrl);
  await cdp.connect();
  await cdp.send('Page.enable');
  await cdp.send('Runtime.enable');
  await cdp.send('Page.addScriptToEvaluateOnNewDocument', {
    source: `
      window.__unhandledErrors = [];
      window.addEventListener('unhandledrejection', e => window.__unhandledErrors.push(String(e.reason)));
      window.addEventListener('error', e => window.__unhandledErrors.push(String(e.message)));
    `,
  });

  try {
    await cdp.send('Page.navigate', { url: baseUrl });
    await waitFor(cdp, `document.body && document.body.innerText.includes('sbx-alpha')`, 15000, 'instances loaded');

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

    await t.test('Delete VM closes modal immediately while loading toast shows', async () => {
      await cdp.realClick(`document.querySelector('button[aria-label="More actions for sbx-beta"]')`);
      await waitFor(cdp, `Array.from(document.querySelectorAll('[role="menuitem"]')).some(el => el.innerText.trim() === 'Delete')`, 3000, 'dropdown menu open');
      await sleep(120);
      await cdp.realClick(`Array.from(document.querySelectorAll('[role="menuitem"]')).find(el => el.innerText.trim() === 'Delete')`);
      await waitFor(cdp, `document.body.innerText.includes('Delete this instance?')`, 3000, 'delete modal open');
      await cdp.evalJS(`window.__clickButtonByText('Delete instance')`);
      await sleep(70);
      assert.equal(await cdp.evalJS(`!document.body.innerText.includes('Delete this instance?')`), true);
      const toastText = await cdp.evalJS(`window.__getToastsText()`);
      assert(toastText.includes('Deleting sbx-beta'), `Expected loading toast, got: ${toastText}`);
      await waitFor(cdp, `window.__getToastsText().includes('sbx-beta deleted')`, 3000, 'delete toast success');
      await waitFor(cdp, `!(document.querySelector('tbody')?.innerText || '').includes('sbx-beta') && !document.querySelector('[role="alertdialog"]')`, 3000, 'delete refresh settled');
    });

    await t.test('Delete VM failure still keeps modal closed and shows error toast', async () => {
      await cdp.realClick(`document.querySelector('button[aria-label="More actions for sbx-fail"]')`);
      await waitFor(cdp, `Array.from(document.querySelectorAll('[role="menuitem"]')).some(el => el.innerText.trim() === 'Delete')`, 3000, 'dropdown menu open fail');
      await sleep(120);
      await cdp.realClick(`Array.from(document.querySelectorAll('[role="menuitem"]')).find(el => el.innerText.trim() === 'Delete')`);
      await waitFor(cdp, `document.body.innerText.includes('Delete this instance?')`, 3000, 'delete fail modal open');
      await cdp.evalJS(`window.__clickButtonByText('Delete instance')`);
      await sleep(70);
      assert.equal(await cdp.evalJS(`!document.body.innerText.includes('Delete this instance?')`), true);
      const toastText = await cdp.evalJS(`window.__getToastsText()`);
      assert(toastText.includes('Deleting sbx-fail'), `Expected loading toast, got: ${toastText}`);
      await waitFor(cdp, `window.__getToastsText().includes('Could not delete sbx-fail')`, 3000, 'delete toast error');
      await waitFor(cdp, `!document.querySelector('[role="alertdialog"]')`, 3000, 'alertdialog unmounted');
    });

    await t.test('Bulk Delete VMs closes modal immediately while loading toast shows', async () => {
      await cdp.evalJS(`document.querySelector('input[aria-label="Select sbx-gamma"]').click()`);
      await cdp.evalJS(`document.querySelector('input[aria-label="Select sbx-delta"]').click()`);
      await waitFor(cdp, `document.body.innerText.includes('2 selected')`, 3000, 'bulk action bar');
      await cdp.evalJS(`
        (() => {
          const bar = Array.from(document.querySelectorAll('div')).find(d => d.innerText.includes('2 selected') && d.querySelector('button'));
          const delBtn = Array.from(bar.querySelectorAll('button')).find(b => b.innerText.trim() === 'Delete');
          delBtn.click();
        })()
      `);
      await waitFor(cdp, `document.body.innerText.includes('Delete 2 instances?')`, 3000, 'bulk delete dialog open');
      await cdp.evalJS(`window.__clickButtonByText('Delete 2 instances')`);
      await sleep(70);
      assert.equal(await cdp.evalJS(`!document.body.innerText.includes('Delete 2 instances?')`), true);
      const toastText = await cdp.evalJS(`window.__getToastsText()`);
      assert(toastText.includes('Queueing deletion of 2 sandbox(es)'), `Expected bulk loading toast, got: ${toastText}`);
      await waitFor(cdp, `window.__getToastsText().includes('Queued deletion of 2 sandbox(es)')`, 3000, 'bulk delete toast success');
      await waitFor(cdp, `!(document.querySelector('tbody')?.innerText || '').includes('sbx-gamma') && !document.querySelector('[role="alertdialog"]')`, 3000, 'bulk delete refresh settled');
      assert.equal(server.bulkDeleteCalls.length, 1, 'Expected single POST /api/v1/vms/bulk-delete request');
      assert.deepEqual(server.bulkDeleteCalls[0].ids, ['vm-gamma', 'vm-delta']);
    });

    await t.test('Unprotect VM closes modal immediately while loading toast shows', async () => {
      await cdp.realClick(`document.querySelector('button[aria-label="More actions for sbx-protected"]')`);
      await waitFor(cdp, `Array.from(document.querySelectorAll('[role="menuitem"]')).some(el => el.innerText.trim() === 'Unprotect')`, 3000, 'dropdown menu unprotect');
      await sleep(120);
      await cdp.realClick(`Array.from(document.querySelectorAll('[role="menuitem"]')).find(el => el.innerText.trim() === 'Unprotect')`);
      await waitFor(cdp, `document.body.innerText.includes('Remove deletion protection?')`, 3000, 'unprotect dialog open');
      await cdp.evalJS(`
        (() => {
          const input = document.getElementById('unprotect-confirmation');
          window.__setReactInput(input, 'sbx-protected');
        })()
      `);
      await sleep(50);
      await cdp.evalJS(`window.__clickButtonByText('Remove protection')`);
      await sleep(70);
      assert.equal(await cdp.evalJS(`!document.body.innerText.includes('Remove deletion protection?')`), true);
      const toastText = await cdp.evalJS(`window.__getToastsText()`);
      assert(toastText.includes('Disabling protection'), `Expected unprotect loading toast, got: ${toastText}`);
      await waitFor(cdp, `window.__getToastsText().includes('protection disabled')`, 3000, 'unprotect toast success');
    });

    await t.test('Stop VM closes confirmation dialog immediately while loading toast shows', async () => {
      await cdp.evalJS(`document.querySelector('button[title="Stop Sandbox"]').click()`);
      await waitFor(cdp, `document.body.innerText.includes('Stop sbx-alpha?')`, 3000, 'stop dialog open');
      await cdp.evalJS(`window.__clickButtonByText('Stop sandbox')`);
      await sleep(70);
      assert.equal(await cdp.evalJS(`!document.body.innerText.includes('Stop sbx-alpha?')`), true);
      const toastText = await cdp.evalJS(`window.__getToastsText()`);
      assert(toastText.includes('Stopping sbx-alpha'), `Expected Stopping toast, got: ${toastText}`);
      await waitFor(cdp, `window.__getToastsText().includes('sbx-alpha stopped')`, 3000, 'stop toast success');
    });

    await t.test('Inspect VM Public IP toggle closes modal immediately with loading toast', async () => {
      await cdp.evalJS(`Array.from(document.querySelectorAll('tr')).find(r => r.innerText.includes('sbx-alpha')).click()`);
      await waitFor(cdp, `document.querySelector('[data-testid="sandbox-detail-panel"]') !== null`, 3000, 'detail panel open');
      await cdp.realClick(`Array.from(document.querySelectorAll('[data-testid="sandbox-detail-panel"] button')).find(b => b.innerText.trim() === 'More')`);
      await waitFor(cdp, `Array.from(document.querySelectorAll('[role="menuitem"]')).some(el => el.innerText.trim() === 'JSON Spec')`, 3000, 'detail More menu open');
      await sleep(120);
      await cdp.realClick(`Array.from(document.querySelectorAll('[role="menuitem"]')).find(el => el.innerText.trim() === 'JSON Spec')`);
      await waitFor(cdp, `document.body.innerText.includes('vm-alpha') && Array.from(document.querySelectorAll('[role="tab"]')).some(t => t.innerText.includes('Network'))`, 3000, 'inspect dialog open');
      await cdp.realClick(`Array.from(document.querySelectorAll('[role="tab"]')).find(t => t.innerText.includes('Network'))`);
      await waitFor(cdp, `document.body.innerText.includes('Turn Off Public IP')`, 3000, 'public ip button visible');
      await cdp.evalJS(`window.__clickButtonByText('Turn Off Public IP')`);
      await sleep(70);
      assert.equal(await cdp.evalJS(`!document.body.innerText.includes('Turn Off Public IP')`), true);
      const toastText = await cdp.evalJS(`window.__getToastsText()`);
      assert(toastText.includes('Disabling public IP for sbx-alpha'), `Expected public IP loading toast, got: ${toastText}`);
      await waitFor(cdp, `window.__getToastsText().includes('Public IP disabled for sbx-alpha')`, 3000, 'public ip toast success');
    });

    await t.test('Create VM modal closes immediately while provisioning toast shows', async () => {
      await cdp.evalJS(`window.__clickButtonByText('New')`);
      await waitFor(cdp, `document.getElementById('advanced-create-title') !== null`, 3000, 'create modal open');
      await cdp.evalJS(`
        (() => {
          const input = document.querySelector('input[aria-label="Sandbox name"]');
          window.__setReactInput(input, 'sbx-fast-close');
        })()
      `);
      await sleep(50);
      await cdp.evalJS(`document.querySelector('input[aria-label="Sandbox name"]').closest('form').requestSubmit()`);
      await sleep(70);
      assert.equal(await cdp.evalJS(`document.getElementById('advanced-create-title') === null`), true);
      const toastText = await cdp.evalJS(`window.__getToastsText()`);
      assert(toastText.includes('Provisioning & booting sbx-fast-close'), `Expected Provisioning toast, got: ${toastText}`);
      await waitFor(cdp, `window.__getToastsText().includes('sbx-fast-close provisioned')`, 4000, 'create+boot toast success');
    });

    await t.test('SSHKeysDialog closes immediately on add and delete with loading toast', async () => {
      await cdp.evalJS(`window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', ctrlKey: true, bubbles: true }))`);
      await waitFor(cdp, `document.body.innerText.includes('Manage SSH Public Keys')`, 3000, 'command palette open');
      await cdp.evalJS(`
        (() => {
          const items = Array.from(document.querySelectorAll('button'));
          const btn = items.find(b => b.innerText.includes('Manage SSH Public Keys'));
          btn.click();
        })()
      `);
      await waitFor(cdp, `document.body.innerText.includes('Account SSH Keys')`, 3000, 'ssh keys dialog open');
      await cdp.evalJS(`
        (() => {
          const nameInput = document.getElementById('key-name');
          const pubInput = document.getElementById('key-content');
          window.__setReactInput(nameInput, 'modal-key');
          window.__setReactInput(pubInput, 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest test@example');
        })()
      `);
      await sleep(50);
      await cdp.evalJS(`document.getElementById('key-name').closest('form').requestSubmit()`);
      await sleep(70);
      assert.equal(await cdp.evalJS(`!document.body.innerText.includes('Account SSH Keys')`), true, 'SSHKeysDialog should close immediately on add');
      assert((await cdp.evalJS(`window.__getToastsText()`)).includes('Adding SSH key "modal-key"'), 'Expected SSH key modal loading toast');
      await waitFor(cdp, `window.__getToastsText().includes('Added SSH key "modal-key"')`, 3000, 'ssh key modal toast success');
    });

    await t.test('Storage Create Volume & Attach Volume close immediately while loading toast shows', async () => {
      await cdp.send('Page.navigate', { url: `${baseUrl}#/storage` });
      await waitFor(cdp, `document.body.innerText.includes('Persistent Volumes')`, 3000, 'storage page');
      await cdp.evalJS(`window.__clickButtonByText('New Volume')`);
      await waitFor(cdp, `document.querySelector('input[aria-label="Volume name"]') !== null`, 3000, 'volume drawer open');
      await cdp.evalJS(`
        (() => {
          const input = document.querySelector('input[aria-label="Volume name"]');
          window.__setReactInput(input, 'fast-vol-1');
        })()
      `);
      await sleep(50);
      await cdp.evalJS(`document.querySelector('input[aria-label="Volume name"]').closest('form').requestSubmit()`);
      await sleep(70);
      assert.equal(await cdp.evalJS(`document.querySelector('input[aria-label="Volume name"]') === null`), true);
      const toastText = await cdp.evalJS(`window.__getToastsText()`);
      assert(toastText.includes('Provisioning volume fast-vol-1'), `Expected volume loading toast, got: ${toastText}`);
      await waitFor(cdp, `window.__getToastsText().includes('Provisioned persistent volume fast-vol-1')`, 3000, 'volume toast success');

      await waitFor(cdp, `document.querySelector('button[title="Attach to Sandbox"]') !== null`, 3000, 'attach button present');
      await cdp.evalJS(`document.querySelector('button[title="Attach to Sandbox"]').click()`);
      await waitFor(cdp, `document.body.innerText.includes('Confirm Attach')`, 3000, 'attach modal open');
      await cdp.evalJS(`window.__clickButtonByText('Confirm Attach')`);
      await sleep(70);
      assert.equal(await cdp.evalJS(`!document.body.innerText.includes('Confirm Attach')`), true);
      const attachToast = await cdp.evalJS(`window.__getToastsText()`);
      assert(attachToast.includes('Attaching'), `Expected attach loading toast, got: ${attachToast}`);
      await waitFor(cdp, `window.__getToastsText().includes('Attached')`, 3000, 'attach toast success');
    });

    await t.test('Snapshots & Templates modal closes immediately while loading toast shows', async () => {
      await cdp.send('Page.navigate', { url: `${baseUrl}#/snapshots` });
      await waitFor(cdp, `document.body.innerText.includes('Create Snapshot')`, 3000, 'snapshots page');
      await cdp.evalJS(`window.__clickButtonByText('Create Snapshot')`);
      await waitFor(cdp, `document.body.innerText.includes('Create Sandbox Snapshot')`, 3000, 'snapshot modal open');
      await cdp.evalJS(`
        (() => {
          const sel = document.querySelector('select[aria-label="Snapshot source sandbox"]');
          if (!sel.value && sel.options.length > 1) {
            window.__setReactInput(sel, sel.options[1].value);
          }
        })()
      `);
      await sleep(50);
      await cdp.evalJS(`
        (() => {
          const input = document.querySelector('input[aria-label="Snapshot name"]');
          window.__setReactInput(input, 'tpl-fast');
        })()
      `);
      await sleep(50);
      await cdp.evalJS(`document.querySelector('input[aria-label="Snapshot name"]').closest('form').requestSubmit()`);
      await sleep(70);
      assert.equal(await cdp.evalJS(`!document.body.innerText.includes('Create Sandbox Snapshot')`), true);
      assert((await cdp.evalJS(`window.__getToastsText()`)).includes('Creating snapshot "tpl-fast"'));
      await waitFor(cdp, `window.__getToastsText().includes('Snapshot "tpl-fast" created')`, 3000, 'snapshot toast success');
    });

    await t.test('CI Jobs Ad-hoc Run and Connect GitHub Repository modals close immediately while loading toasts show', async () => {
      await cdp.send('Page.navigate', { url: `${baseUrl}#/jobs` });
      await waitFor(cdp, `document.body.innerText.includes('CI Jobs')`, 3000, 'jobs page');
      await cdp.evalJS(`window.__clickButtonByText('Run Ad-hoc Job')`);
      await waitFor(cdp, `document.body.innerText.includes('Run Ad-hoc Ephemeral Job')`, 3000, 'job modal open');
      await cdp.evalJS(`
        (() => {
          const btn = Array.from(document.querySelectorAll('button')).find(b => b.innerText.trim() === 'Run Job');
          btn.closest('form').requestSubmit();
        })()
      `);
      await sleep(70);
      assert.equal(await cdp.evalJS(`!document.body.innerText.includes('Run Ad-hoc Ephemeral Job')`), true);
      const toastText = await cdp.evalJS(`window.__getToastsText()`);
      assert(toastText.includes('Dispatching CI job'), `Expected job loading toast, got: ${toastText}`);
      await waitFor(cdp, `window.__getToastsText().includes('Dispatched CI job')`, 3000, 'job toast success');

      await cdp.evalJS(`window.__clickButtonByText('Connect GitHub Repository')`);
      await waitFor(cdp, `document.getElementById('ci-repo-modal-title') !== null`, 3000, 'connect repo modal open');
      await cdp.evalJS(`
        (() => {
          const urlInput = document.querySelector('input[aria-label="Repository URL"]');
          window.__setReactInput(urlInput, 'https://github.com/acme/web-app');
        })()
      `);
      await sleep(50);
      await cdp.evalJS(`document.querySelector('input[aria-label="Repository URL"]').closest('form').requestSubmit()`);
      await sleep(70);
      assert.equal(await cdp.evalJS(`document.getElementById('ci-repo-modal-title') === null`), true);
      assert((await cdp.evalJS(`window.__getToastsText()`)).includes('Connecting GitHub repository'), 'Expected connecting repo loading toast');
      await waitFor(cdp, `window.__getToastsText().includes('Connected acme/repo')`, 3000, 'connect repo toast success');
    });

    await t.test('Network Add Port Forward drawer closes immediately while loading toast shows', async () => {
      await cdp.send('Page.navigate', { url: `${baseUrl}#/network` });
      await waitFor(cdp, `document.body.innerText.includes('DNAT Port Forwarding')`, 3000, 'network page');
      await cdp.evalJS(`window.__clickButtonByText('DNAT Port Forwarding')`);
      await waitFor(cdp, `window.__clickButtonByText('Add Port Rule')`, 3000, 'add port rule button');
      await waitFor(cdp, `document.body.innerText.includes('Add Port Exposure')`, 3000, 'pf drawer open');
      await cdp.evalJS(`
        (() => {
          const sel = document.querySelector('select[aria-label="Sandbox to forward"]');
          if (!sel.value && sel.options.length > 1) {
            window.__setReactInput(sel, sel.options[1].value);
          }
        })()
      `);
      await sleep(50);
      await cdp.evalJS(`document.querySelector('select[aria-label="Sandbox to forward"]').closest('form').requestSubmit()`);
      await sleep(70);
      assert.equal(await cdp.evalJS(`!document.body.innerText.includes('Add Port Exposure')`), true);
      assert((await cdp.evalJS(`window.__getToastsText()`)).includes('Adding exposure'));
      await waitFor(cdp, `window.__getToastsText().includes('Added exposure')`, 3000, 'pf toast success');
    });

    await t.test('Workers Create Pool form closes immediately while loading toast shows', async () => {
      await cdp.send('Page.navigate', { url: `${baseUrl}#/workers` });
      await waitFor(cdp, `document.body.innerText.includes('New Pool')`, 3000, 'workers page');
      await cdp.evalJS(`window.__clickButtonByText('New Pool')`);
      await waitFor(cdp, `document.querySelector('input[aria-label="New pool name"]') !== null`, 3000, 'pool modal open');
      await cdp.evalJS(`
        (() => {
          const input = document.querySelector('input[aria-label="New pool name"]');
          window.__setReactInput(input, 'pool-fast');
        })()
      `);
      await sleep(50);
      await cdp.evalJS(`document.querySelector('input[aria-label="New pool name"]').closest('form').requestSubmit()`);
      await sleep(70);
      assert.equal(await cdp.evalJS(`document.querySelector('input[aria-label="New pool name"]') === null`), true);
      assert((await cdp.evalJS(`window.__getToastsText()`)).includes('Creating worker pool "pool-fast"'));
      await waitFor(cdp, `window.__getToastsText().includes('Created worker pool "pool-fast"')`, 3000, 'pool toast success');
    });

    await t.test('Events Add Webhook drawer closes immediately while loading toast shows', async () => {
      await cdp.send('Page.navigate', { url: `${baseUrl}#/events` });
      await waitFor(cdp, `document.body.innerText.includes('Add Webhook')`, 3000, 'events page');
      await cdp.evalJS(`window.__clickButtonByText('Add Webhook')`);
      await waitFor(cdp, `document.body.innerText.includes('Register Outbound Webhook Subscription')`, 3000, 'webhook drawer open');
      await cdp.evalJS(`
        (() => {
          const input = document.querySelector('input[type="url"]');
          window.__setReactInput(input, 'https://example.com/hook');
        })()
      `);
      await sleep(50);
      await cdp.evalJS(`document.querySelector('input[type="url"]').closest('form').requestSubmit()`);
      await sleep(70);
      assert.equal(await cdp.evalJS(`!document.body.innerText.includes('Register Outbound Webhook Subscription')`), true);
      assert((await cdp.evalJS(`window.__getToastsText()`)).includes('Registering webhook endpoint'));
      await waitFor(cdp, `window.__getToastsText().includes('Registered webhook endpoint')`, 3000, 'webhook toast success');
    });

    await t.test('Legacy API route redirects cleanly to Settings Access & Keys', async () => {
      await cdp.send('Page.navigate', { url: `${baseUrl}#/api` });
      await waitFor(cdp, `document.body.innerText.includes('Settings') && document.body.innerText.includes('Create API Key')`, 3000, 'api redirect to settings');
    });

    await t.test('Access Create API Key & Add SSH Key drawers close immediately while loading toasts show', async () => {
      await cdp.send('Page.navigate', { url: `${baseUrl}#/access` });
      await waitFor(cdp, `document.body.innerText.includes('Create API Key')`, 3000, 'access page');
      await cdp.evalJS(`window.__clickButtonByText('Create API Key')`);
      await waitFor(cdp, `document.body.innerText.includes('Create Scoped API Key')`, 3000, 'api key drawer open');
      await cdp.evalJS(`
        (() => {
          const input = document.querySelector('input[aria-label="API key name"]');
          window.__setReactInput(input, 'key-fast');
        })()
      `);
      await sleep(50);
      await cdp.evalJS(`document.querySelector('input[aria-label="API key name"]').closest('form').requestSubmit()`);
      await sleep(70);
      assert.equal(await cdp.evalJS(`!document.body.innerText.includes('Create Scoped API Key')`), true);
      assert((await cdp.evalJS(`window.__getToastsText()`)).includes('Creating API key "key-fast"'));
      await waitFor(cdp, `window.__getToastsText().includes('Created API key "key-fast"')`, 3000, 'api key toast success');

      await cdp.evalJS(`window.__clickButtonByText('SSH Public Keys')`);
      await waitFor(cdp, `window.__clickButtonByText('Add SSH Key')`, 3000, 'add ssh key button');
      await waitFor(cdp, `document.body.innerText.includes('Register SSH Public Key')`, 3000, 'ssh key drawer open');
      await cdp.evalJS(`
        (() => {
          const nameInput = document.querySelector('input[placeholder*="work-laptop-ed25519"]');
          const pubInput = document.querySelector('textarea');
          window.__setReactInput(nameInput, 'ssh-fast');
          window.__setReactInput(pubInput, 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest test@example');
        })()
      `);
      await sleep(50);
      await cdp.evalJS(`document.querySelector('input[placeholder*="work-laptop-ed25519"]').closest('form').requestSubmit()`);
      await sleep(70);
      assert.equal(await cdp.evalJS(`!document.body.innerText.includes('Register SSH Public Key')`), true);
      assert((await cdp.evalJS(`window.__getToastsText()`)).includes('Adding SSH key "ssh-fast"'));
      await waitFor(cdp, `window.__getToastsText().includes('Added SSH key "ssh-fast"')`, 3000, 'ssh key toast success');
    });

    const pageErrs = await cdp.evalJS(`window.__unhandledErrors || []`);
    assert.deepEqual(pageErrs, [], `Unexpected page unhandled errors: ${JSON.stringify(pageErrs)}`);
  } finally {
    try { cdp.close(); } catch {}
    chrome.kill('SIGKILL');
    await new Promise((resolve) => chrome.once('exit', resolve)).catch(() => {});
    server.close();
    await fs.rm(tmpDir, { recursive: true, force: true }).catch(() => {});
  }
});
