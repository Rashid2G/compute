import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import crypto from 'node:crypto';
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

function encodeWSFrame(opcode, payloadBuf) {
  const len = payloadBuf.length;
  let header;
  if (len < 126) {
    header = Buffer.from([0x80 | opcode, len]);
  } else if (len < 65536) {
    header = Buffer.alloc(4);
    header[0] = 0x80 | opcode;
    header[1] = 126;
    header.writeUInt16BE(len, 2);
  } else {
    header = Buffer.alloc(10);
    header[0] = 0x80 | opcode;
    header[1] = 127;
    header.writeBigUInt64BE(BigInt(len), 2);
  }
  return Buffer.concat([header, payloadBuf]);
}

function sendWSText(socket, text) {
  if (socket.destroyed || !socket.writable) return;
  socket.write(encodeWSFrame(0x1, Buffer.from(text, 'utf8')));
}

function attachWSReader(socket, { onText, onClose }) {
  let buf = Buffer.alloc(0);
  let closed = false;
  const finish = () => {
    if (closed) return;
    closed = true;
    onClose();
  };

  socket.on('data', (chunk) => {
    buf = Buffer.concat([buf, chunk]);
    while (buf.length >= 2) {
      const b0 = buf[0];
      const b1 = buf[1];
      const opcode = b0 & 0x0f;
      const masked = (b1 & 0x80) !== 0;
      let payloadLen = b1 & 0x7f;
      let offset = 2;

      if (payloadLen === 126) {
        if (buf.length < 4) return;
        payloadLen = buf.readUInt16BE(2);
        offset = 4;
      } else if (payloadLen === 127) {
        if (buf.length < 10) return;
        payloadLen = Number(buf.readBigUInt64BE(2));
        offset = 10;
      }

      const maskLen = masked ? 4 : 0;
      if (buf.length < offset + maskLen + payloadLen) return;

      const maskKey = masked ? buf.subarray(offset, offset + 4) : null;
      const payload = Buffer.from(buf.subarray(offset + maskLen, offset + maskLen + payloadLen));
      if (masked && maskKey) {
        for (let i = 0; i < payload.length; i++) {
          payload[i] ^= maskKey[i % 4];
        }
      }
      buf = buf.subarray(offset + maskLen + payloadLen);

      if (opcode === 0x8) {
        try {
          socket.write(encodeWSFrame(0x8, Buffer.alloc(0)));
          socket.end();
        } catch {}
        finish();
        return;
      }
      if (opcode === 0x9) {
        try {
          socket.write(encodeWSFrame(0xA, payload));
        } catch {}
        continue;
      }
      if (opcode === 0x1) {
        onText(payload.toString('utf8'));
      }
    }
  });

  socket.on('close', finish);
  socket.on('error', finish);
}

const SNAPSHOT_REST_PATHS = new Set([
  '/api/v1/health',
  '/api/v1/stats',
  '/api/v1/vms',
  '/api/v1/network/allocations',
  '/api/v1/network/config',
  '/api/v1/config',
  '/api/v1/queue',
  '/api/v1/workers',
  '/api/v1/pools',
  '/api/v1/worker-tokens',
  '/api/v1/scheduling/policy',
  '/api/v1/admin/resources',
  '/api/v1/templates',
  '/api/v1/images',
  '/api/v1/port-forwards',
  '/api/v1/jobs',
  '/api/v1/volumes',
  '/api/v1/events',
]);

function createWSMockServer() {
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
  ];
  const workers = [
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
  ];
  const pools = [
    {
      id: 'pool-default',
      name: 'default',
      labels_json: '{}',
      policy_json: '{}',
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    },
  ];
  const adminUser = {
    id: 'usr_admin',
    username: 'admin',
    email: 'admin@compute.local',
    display_name: 'Administrator',
    role: 'admin',
    status: 'active',
    created_at: Math.floor(Date.now() / 1000) - 3600,
  };

  let wsConnectionsOpened = 0;
  let wsConnectionsClosed = 0;
  let wsRefreshMessages = 0;
  const activeSockets = new Set();
  const restSnapshotGets = [];

  function buildSnapshot() {
    const runningVMs = vms.filter((v) => v.status === 'running').length;
    return {
      type: 'snapshot',
      stats: {
        total_vms: vms.length,
        running_vms: runningVMs,
        stopped_vms: vms.length - runningVMs,
        paused_vms: 0,
        total_vcpus: 16,
        used_vcpus: vms.reduce((acc, v) => acc + (v.status === 'running' ? v.vcpus : 0), 0),
        total_memory_mb: 32768,
        used_memory_mb: vms.reduce((acc, v) => acc + (v.status === 'running' ? v.memory_mb : 0), 0),
        active_ips: vms.length,
        max_vms: 100,
        max_vcpus: 64,
        max_memory_mb: 65536,
        total_workers: workers.length,
        ready_workers: workers.length,
      },
      vms,
      allocations: [],
      network_config: {
        bridge_name: 'fcbr0',
        mode: 'nat',
        subnet: '172.16.0.0/24',
        bridge_ip: '172.16.0.1',
      },
      config: {
        server: { listen_addr: '127.0.0.1:18080', data_dir: '/var/lib/compute' },
        defaults: { vcpus: 1, memory_mb: 512, disk_gb: 10 },
      },
      queue: { queued_vms: [], count: 0, running_vms: runningVMs, running_vcpus: 2, running_mem_mb: 1024 },
      workers,
      pools,
      worker_tokens: [],
      scheduling_policy: {
        strategy: 'least_loaded',
        cpu_weight: 1.0,
        memory_weight: 1.0,
        disk_weight: 0.5,
        cpu_soft_overcommit_ratio: 4.0,
        cpu_hard_overcommit_ratio: 6.0,
        cpu_share_weight_base: 1024,
        memory_hard_guarantee: true,
        idle_ram_snapshot_enabled: false,
        idle_threshold_seconds: 900,
        auto_reclaim_on_pressure: true,
        host_reserved_vcpus: 1,
        host_reserved_memory_mb: 512,
        host_reserved_disk_mb: 4096,
        disk_hard_enforcement: true,
      },
      resources: {
        timestamp: new Date().toISOString(),
        cpu: {
          cores: 16,
          model: 'Mock EPYC',
          usage_percent: 12.5,
          user_percent: 9.0,
          system_percent: 3.0,
          iowait_percent: 0.5,
          idle_percent: 87.5,
          load_1m: 0.4,
          load_5m: 0.3,
          load_15m: 0.2,
        },
        memory: {
          total_mb: 32768,
          used_mb: 4768,
          available_mb: 28000,
          free_mb: 24000,
          cached_mb: 3000,
          buffers_mb: 1000,
          used_percent: 14.5,
          swap_total_mb: 0,
          swap_used_mb: 0,
        },
        disk: {
          path: '/var/lib/compute',
          total_gb: 500,
          used_gb: 50,
          free_gb: 450,
          available_gb: 440,
          used_percent: 10.0,
          mounts: [
            { label: 'compute-data', path: '/var/lib/compute', total_gb: 500, used_gb: 50, available_gb: 440, used_percent: 10.0 },
          ],
        },
        disk_io: {
          read_ops: 1000,
          write_ops: 500,
          read_bytes: 10240000,
          write_bytes: 5120000,
          read_mb: 10,
          write_mb: 5,
          read_mb_per_sec: 1.2,
          write_mb_per_sec: 0.6,
          read_iops: 50,
          write_iops: 25,
          io_in_progress: 0,
          devices: [],
        },
        network: {
          rx_bytes: 2048000,
          tx_bytes: 1024000,
          rx_packets: 1500,
          tx_packets: 1200,
          rx_mb: 2.0,
          tx_mb: 1.0,
          rx_mb_per_sec: 0.5,
          tx_mb_per_sec: 0.2,
          interfaces: ['eth0', 'fcbr0'],
        },
        system: {
          hostname: 'compute-host-01',
          os: 'linux',
          arch: 'amd64',
          kernel_version: '6.8.0',
          uptime_seconds: 3600,
          kvm_available: true,
        },
        allocations: {
          sandboxes: { total: vms.length, running: runningVMs, stopped: vms.length - runningVMs, vcpus_allocated: 2, running_vcpus: 2, memory_mb_allocated: 1024, running_memory_mb: 1024, disk_mb_allocated: 20480, disk_gb_allocated: 20 },
          ci_jobs: { total: 0, queued: 0, running: 0, succeeded: 0, failed: 0, vcpus_allocated: 0, memory_mb_allocated: 0, disk_mb_allocated: 0, disk_gb_allocated: 0 },
          volumes: { total: 0, attached: 0, available: 0, disk_mb_allocated: 0, disk_gb_allocated: 0 },
          workers: { total: workers.length, ready: workers.length, offline: 0 },
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
          allocated_volume_disk_gb: 0,
          max_total_disk_gb: 500,
        },
        capacity_warnings: [],
      },
      templates: [],
      images: [
        { id: 'ubuntu-24.04', name: 'ubuntu-24.04', base_image: 'ubuntu-24.04', status: 'ready', size_bytes: 536870912 },
      ],
      port_forwards: [],
      jobs: [],
      volumes: [],
      events: [],
      timestamp: new Date().toISOString(),
    };
  }

  function pushSnapshot() {
    const payload = JSON.stringify(buildSnapshot());
    for (const sock of activeSockets) {
      sendWSText(sock, payload);
    }
  }

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
      if (req.method === 'GET' && SNAPSHOT_REST_PATHS.has(p)) {
        restSnapshotGets.push(p);
      }

      if (p === '/api/v1/auth/status') {
        return json(200, { auth_enabled: true, signup_enabled: false, admin_username: 'admin' });
      }
      if (p === '/api/v1/auth/me') {
        return json(200, { user: adminUser });
      }
      if (p === '/api/v1/vms/vm-alpha/stop' && req.method === 'POST') {
        vms = vms.map((v) => (v.id === 'vm-alpha' ? { ...v, status: 'stopped', pid: 0 } : v));
        return json(200, vms.find((v) => v.id === 'vm-alpha'));
      }
      if (p === '/api/v1/vms/vm-alpha/run' && req.method === 'POST') {
        vms = vms.map((v) => (v.id === 'vm-alpha' ? { ...v, status: 'running', pid: 12346 } : v));
        return json(200, vms.find((v) => v.id === 'vm-alpha'));
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

  server.on('upgrade', (req, socket) => {
    const u = new URL(req.url, 'http://127.0.0.1');
    if (u.pathname !== '/api/v1/ws') {
      socket.destroy();
      return;
    }
    const key = req.headers['sec-websocket-key'];
    if (!key) {
      socket.destroy();
      return;
    }
    const accept = crypto
      .createHash('sha1')
      .update(key + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11')
      .digest('base64');

    socket.write(
      [
        'HTTP/1.1 101 Switching Protocols',
        'Upgrade: websocket',
        'Connection: Upgrade',
        `Sec-WebSocket-Accept: ${accept}`,
        '',
        '',
      ].join('\r\n'),
    );

    wsConnectionsOpened += 1;
    activeSockets.add(socket);

    attachWSReader(socket, {
      onText: (text) => {
        try {
          const msg = JSON.parse(text);
          if (msg.type === 'refresh') {
            wsRefreshMessages += 1;
            sendWSText(socket, JSON.stringify(buildSnapshot()));
          }
        } catch {}
      },
      onClose: () => {
        if (activeSockets.delete(socket)) {
          wsConnectionsClosed += 1;
        }
      },
    });

    // Send initial snapshot immediately on WS open
    sendWSText(socket, JSON.stringify(buildSnapshot()));
  });

  return {
    server,
    getStats: () => ({
      wsConnectionsOpened,
      wsConnectionsClosed,
      wsActiveCount: activeSockets.size,
      wsRefreshMessages,
      restSnapshotGets: [...restSnapshotGets],
    }),
    addLiveVMAndBroadcast: (vm) => {
      vms = [...vms, vm];
      pushSnapshot();
    },
    closeAllSockets: () => {
      for (const sock of activeSockets) {
        try {
          sock.destroy();
        } catch {}
      }
      activeSockets.clear();
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
  close() {
    this.ws.close();
  }
}

async function waitFor(cdp, expr, timeoutMs = 4000, label = expr) {
  const start = Date.now();
  while (Date.now() - start < timeoutMs) {
    const ok = await cdp.evalJS(`Boolean(${expr})`);
    if (ok) return;
    await sleep(40);
  }
  throw new Error(`Timed out waiting for: ${label}`);
}

test('Persistent /api/v1/ws sync: single connection across routes & mutations, zero REST bursts, live frame updates', async (t) => {
  const chromeBin = findChromium();
  if (!chromeBin) {
    t.skip('No Chromium binary found');
    return;
  }

  const mock = createWSMockServer();
  await new Promise((resolve) => mock.server.listen(0, '127.0.0.1', resolve));
  const port = mock.server.address().port;
  const baseUrl = `http://127.0.0.1:${port}`;

  const tmpDir = await fs.mkdtemp(path.join('/tmp', 'compute-ws-e2e-'));
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

  let cdp = null;
  try {
    let wsEndpoint = '';
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
    const pageTarget = targets.find((x) => x.type === 'page') || targets[0];
    assert(pageTarget, 'Expected a page target');
    cdp = new CDPClient(pageTarget.webSocketDebuggerUrl);
    await cdp.connect();

    await cdp.send('Page.enable');
    await cdp.send('Runtime.enable');

    await cdp.send('Page.navigate', { url: `${baseUrl}/#/instances` });
    await waitFor(cdp, `document.body && document.body.innerText.includes('sbx-alpha')`, 5000, 'initial WS snapshot rendered');
    await sleep(600); // Ensure fallbackTimer (400ms) never fires a REST burst

    let stats = mock.getStats();
    assert.equal(stats.wsConnectionsOpened, 1, 'Expected exactly 1 WS connection opened on mount');
    assert.equal(stats.wsConnectionsClosed, 0, 'Expected 0 WS connections closed on mount');
    assert.equal(stats.wsActiveCount, 1, 'Expected 1 active WS connection');
    assert.deepEqual(stats.restSnapshotGets, [], 'Expected zero REST snapshot GETs while WS is connected');

    // Navigate across multiple views and confirm WS stays open with zero REST bursts
    const routes = [
      { hash: '#/workers', waitExpr: `document.body.innerText.includes('worker-01')` },
      { hash: '#/resources', waitExpr: `document.body.innerText.includes('Platform Allocations')` },
      { hash: '#/settings', waitExpr: `document.body.innerText.includes('Default VM Resources')` },
      { hash: '#/instances', waitExpr: `document.body.innerText.includes('sbx-alpha')` },
    ];
    for (const r of routes) {
      await cdp.evalJS(`window.location.hash = '${r.hash}'`);
      await waitFor(cdp, `window.location.hash === '${r.hash}' && ${r.waitExpr}`, 4000, `navigate ${r.hash}`);
    }
    await sleep(300);

    stats = mock.getStats();
    assert.equal(stats.wsConnectionsOpened, 1, 'WS must stay open across navigation without reopening');
    assert.equal(stats.wsConnectionsClosed, 0, 'WS must not close across navigation');
    assert.equal(stats.wsActiveCount, 1, 'WS active socket count must remain 1');
    assert.deepEqual(stats.restSnapshotGets, [], 'No redundant REST bursts should fire on navigation');

    // Trigger a mutation (Stop sbx-alpha) -> refresh() must use WS {type:'refresh'}, zero REST bursts
    await cdp.evalJS(`document.querySelector('button[title="Stop Sandbox"]')?.click()`);
    await waitFor(
      cdp,
      `Array.from(document.querySelectorAll('button')).some(b => (b.innerText || '').trim() === 'Stop sandbox')`,
      3000,
      'stop confirmation dialog open',
    );
    await cdp.evalJS(
      `Array.from(document.querySelectorAll('button')).find(b => (b.innerText || '').trim() === 'Stop sandbox')?.click()`,
    );
    await waitFor(cdp, `document.body.innerText.includes('STOPPED')`, 4000, 'sbx-alpha transitioned to STOPPED via WS');

    stats = mock.getStats();
    assert.equal(stats.wsConnectionsOpened, 1, 'WS must remain single connection after mutation');
    assert.equal(stats.wsConnectionsClosed, 0, 'WS must not close after mutation');
    assert(stats.wsRefreshMessages >= 1, `Expected at least 1 WS {type:'refresh'} message, got ${stats.wsRefreshMessages}`);
    assert.deepEqual(stats.restSnapshotGets, [], 'No REST burst should fire on post-mutation refresh');

    // Push an unprompted server WS frame without any button click or Refresh action
    mock.addLiveVMAndBroadcast({
      id: 'vm-live-push',
      name: 'sbx-live-pushed',
      status: 'running',
      vcpus: 4,
      memory_mb: 2048,
      disk_mb: 20480,
      ip_address: '172.16.0.77',
      public_ip_enabled: false,
      network_isolation: 'account',
      mac_address: '02:fc:00:00:00:77',
      tap_device: 'tap77',
      worker_id: 'nod-1',
      pool_id: 'default',
      disk_path: '/var/lib/compute/vms/vm-live-push/disk.ext4',
      rootfs_path: '/var/lib/compute/images/ubuntu.ext4',
      kernel_path: '/var/lib/compute/vmlinux',
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
      protected: false,
    });
    await waitFor(cdp, `document.body.innerText.includes('sbx-live-pushed')`, 4000, 'unprompted WS frame updated UI');

    stats = mock.getStats();
    assert.equal(stats.wsConnectionsOpened, 1, 'WS must remain single connection after live push');
    assert.deepEqual(stats.restSnapshotGets, [], 'No REST burst should fire after live push');

    // Verify Command Palette has no manual Sync/Refresh Cluster State action
    await cdp.evalJS(`window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', ctrlKey: true, bubbles: true }))`);
    await waitFor(cdp, `document.body.innerText.includes('.compute control-plane')`, 3000, 'command palette open');
    const paletteText = await cdp.evalJS(`document.body.innerText`);
    assert(!paletteText.includes('Sync Cluster State'), 'Command palette must not contain Sync Cluster State');
    assert(!paletteText.includes('Refresh Cluster State'), 'Command palette must not contain Refresh Cluster State');
  } finally {
    mock.closeAllSockets();
    try { cdp?.close(); } catch {}
    chrome.kill('SIGKILL');
    if (chrome.exitCode === null && chrome.signalCode === null) {
      await Promise.race([
        new Promise((resolve) => chrome.once('exit', resolve)),
        sleep(1500),
      ]).catch(() => {});
    }
    mock.server.closeAllConnections?.();
    mock.server.close();
    await fs.rm(tmpDir, { recursive: true, force: true }).catch(() => {});
  }
});
