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
      disk_mb: 51200,
      ip_address: '172.16.0.10',
      public_ip: '172.16.0.10',
      public_ip_enabled: true,
      network_isolation: 'account',
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
  ];
  let templates = [];
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
  const launchCalls = [];
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
          defaults: { vcpus: 2, memory_mb: 1024, disk_size_mb: 51200, image: 'ubuntu' },
          images: { available: [{ name: 'ubuntu-24' }], vms_dir: '/var/lib/compute/vms', images_dir: '/var/lib/compute/images' },
          resources: { max_running_vms: 20, max_total_disk_mb: 512000, max_total_vcpus: 16, max_total_memory_mb: 32768 },
        });
      }
      if (p === '/api/v1/network/config') {
        return json(200, { bridge_name: 'fcbr0', mode: 'nat', subnet: '172.16.0.0/24', bridge_ip: '172.16.0.1' });
      }
      if (p === '/api/v1/network/allocations') return json(200, []);
      if (p === '/api/v1/workers') return json(200, []);
      if (p === '/api/v1/worker-tokens') return json(200, []);
      if (p === '/api/v1/events') return json(200, []);
      if (p === '/api/v1/events/stream') {
        res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache' });
        return;
      }
      if (p === '/api/v1/queue') return json(200, { count: 0, queued_vms: [] });
      if (p === '/api/v1/tiers') return json(200, []);
      if (p === '/api/v1/environments') return json(200, []);
      if (p === '/api/v1/images') return json(200, []);
      if (p === '/api/v1/api-keys') return json(200, []);
      if (p === '/api/v1/keys') return json(200, []);
      if (p === '/api/v1/agent-profiles') return json(200, []);

      if (p === '/api/v1/vms' && req.method === 'GET') return json(200, vms);

      const vmMatch = p.match(/^\/api\/v1\/vms\/([^/]+)(?:\/(.*))?$/);
      if (vmMatch) {
        const [, id, sub] = vmMatch;
        const src = vms.find((v) => v.id === id);
        if (sub === 'clone' && req.method === 'POST') {
          cloneCalls.push({ id, body });
          const created = {
            id: `vm-clone-${cloneCalls.length}`,
            name: body.name,
            description: body.description || '',
            status: body.auto_start ? 'running' : 'stopped',
            vcpus: body.vcpus || src?.vcpus || 2,
            memory_mb: body.memory_mb || src?.memory_mb || 1024,
            disk_mb: body.disk_gb ? body.disk_gb * 1024 : (src?.disk_mb || 51200),
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
          disk_mb: 51200,
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

test('Compute branding, self-hosted lifecycle UX (replicate, pause, snapshot, launch), and admin user management', async (t) => {
  const chromeBin = findChromium();
  if (!chromeBin) {
    t.skip('No Chromium/Chrome binary found for browser E2E verification');
    return;
  }

  const { server, cloneCalls, launchCalls, setUserRole } = createMockServer();
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
      window.__setReactInput = (el, value) => {
        const proto = el.tagName === 'TEXTAREA' ? window.HTMLTextAreaElement.prototype : el.tagName === 'SELECT' ? window.HTMLSelectElement.prototype : window.HTMLInputElement.prototype;
        const setter = Object.getOwnPropertyDescriptor(proto, 'value').set;
        setter.call(el, value);
        el.dispatchEvent(new Event(el.tagName === 'SELECT' ? 'change' : 'input', { bubbles: true }));
      };
    `);

    await t.test('Branding is Compute and fake billing metrics are removed', async () => {
      const pageText = await cdp.evalJS(`document.body.innerText`);
      assert(!pageText.toLowerCase().includes('boat.dev'), 'Must not mention boat.dev');
      assert(!pageText.includes('$0.018'), 'Must not display fake $0.018 cost');
      assert(pageText.includes('Compute'), 'Sidebar must display Compute branding');
      assert(pageText.includes('Resources'), 'Instances table must have Resources column');
      assert(pageText.includes('2 vCPU · 1 GB · 50 GB'), 'Resources cell should show vCPU, RAM, Disk');

      await cdp.evalJS(`window.location.hash = '#/settings'`);
      await waitFor(cdp, `window.location.hash === '#/settings' && document.body.innerText.includes('Organization & Quotas')`, 4000, 'settings quotas');
      const settingsText = await cdp.evalJS(`document.body.innerText`);
      assert(!settingsText.includes('Hourly Burn'), 'Settings must not mention Hourly Burn');
      assert(!settingsText.includes('Organization & Billing'), 'Settings must not mention Billing');
      assert(settingsText.includes('Allocated Compute & Memory'), 'Settings must show self-hosted compute allocation');

      await cdp.evalJS(`window.location.hash = '#/instances'`);
      await waitFor(cdp, `window.location.hash === '#/instances' && document.body.innerText.includes('sbx-alpha')`, 4000, 'back to instances');
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
      assert.equal(cloneCalls.length, 2);
      assert.equal(cloneCalls[0].body.name, 'worker-pool-1');
      assert.equal(cloneCalls[1].body.name, 'worker-pool-2');
      assert.equal(cloneCalls[0].body.vcpus, 4);
      assert.equal(cloneCalls[0].body.auto_start, true);
    });

    await t.test('Pause shows PAUSED (STATE SAVED) badge and Resume restores running', async () => {
      await cdp.evalJS(`document.querySelector('button[aria-label="Pause sbx-alpha"]').click()`);
      await waitFor(cdp, `document.body.innerText.includes('PAUSED (STATE SAVED)')`, 4000, 'paused state saved badge');
      await cdp.evalJS(`document.querySelector('button[aria-label="Resume sbx-alpha"]').click()`);
      await waitFor(cdp, `!document.body.innerText.includes('PAUSED (STATE SAVED)')`, 4000, 'resumed to running');
    });

    await t.test('Create Snapshot from row, inspect in Inspector Snapshots tab, and Launch from Snapshots page', async () => {
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

      await cdp.evalJS(`Array.from(document.querySelectorAll('tr')).find(r => r.innerText.includes('sbx-alpha')).click()`);
      await waitFor(cdp, `document.body.innerText.includes('SANDBOX INSPECTOR')`, 3000, 'inspector open');
      await cdp.evalJS(`
        (() => {
          const tabs = Array.from(document.querySelectorAll('button'));
          const snapTab = tabs.find(b => b.innerText.trim() === 'Snapshots');
          snapTab.click();
        })()
      `);
      await waitFor(cdp, `document.body.innerText.includes('alpha-golden')`, 4000, 'snapshot listed in inspector');

      await cdp.evalJS(`window.location.hash = '#/snapshots'`);
      await waitFor(
        cdp,
        `window.location.hash === '#/snapshots' && !document.body.innerText.includes('SANDBOX INSPECTOR') && document.body.innerText.includes('alpha-golden') && Array.from(document.querySelectorAll('button')).some(b => b.innerText.trim().includes('Launch'))`,
        4000,
        'snapshots page loaded',
      );
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
