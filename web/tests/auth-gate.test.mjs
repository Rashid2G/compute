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

function createAuthMockServer({ signupEnabled = false } = {}) {
  let authedUser = null;
  let vmRequestsUnauthed = 0;

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

    if (p === '/api/v1/auth/status') {
      return json(200, { auth_enabled: true, signup_enabled: signupEnabled });
    }
    if (p === '/api/v1/auth/me') {
      if (!authedUser) {
        return json(401, { error: 'unauthorized', message: 'not signed in' });
      }
      return json(200, { user: authedUser });
    }
    if (p === '/api/v1/auth/login' && req.method === 'POST') {
      if (body.username === 'opsadmin' && body.password === 'StrongPass123!') {
        authedUser = {
          id: 'usr_ops',
          email: 'ops@compute.local',
          username: 'opsadmin',
          display_name: 'Ops Admin',
          role: 'admin',
          status: 'active',
        };
        return json(200, { status: 'ok', token: 'tok_ops', csrf: 'csrf_ops', user: authedUser });
      }
      return json(401, { error: 'invalid credentials' });
    }
    if (p === '/api/v1/auth/signup' && req.method === 'POST') {
      if (!signupEnabled) {
        return json(403, {
          error: 'signup_disabled',
          message: 'Public registration is disabled on this Compute instance.',
        });
      }
      authedUser = {
        id: 'usr_new',
        email: body.email,
        username: body.username,
        display_name: body.display_name || body.username,
        role: 'user',
        status: 'active',
      };
      return json(201, { status: 'ok', token: 'tok_new', csrf: 'csrf_new', user: authedUser });
    }
    if (p === '/api/v1/auth/logout' && req.method === 'POST') {
      authedUser = null;
      return json(200, { status: 'ok' });
    }

    if (p.startsWith('/api/v1/')) {
      if (!authedUser && p !== '/api/v1/health') {
        vmRequestsUnauthed += 1;
        return json(401, { error: 'unauthorized' });
      }
      if (p === '/api/v1/health') return json(200, { status: 'ok' });
      if (p === '/api/v1/stats') {
        return json(200, {
          total_vms: 1,
          running_vms: 1,
          snapshotted_vms: 0,
          queued_vms: 0,
          running_vcpus: 2,
          running_memory_mb: 1024,
          allocated_ips: 1,
          host_memory_available_mb: 8192,
          host_cpus: 8,
        });
      }
      if (p === '/api/v1/config') {
        return json(200, {
          server: { listen: '0.0.0.0:18080' },
          defaults: { vcpus: 2, memory_mb: 1024 },
          images: { available: [{ name: 'ubuntu-24' }] },
          resources: { max_total_disk_mb: 512000, max_total_vcpus: 16, max_total_memory_mb: 32768 },
        });
      }
      if (p === '/api/v1/network/config') {
        return json(200, { bridge_name: 'fcbr0', mode: 'nat', subnet: '172.16.0.0/24', bridge_ip: '172.16.0.1' });
      }
      if (p === '/api/v1/vms') {
        return json(200, [
          {
            id: 'vm-alpha',
            name: 'sbx-alpha',
            status: 'running',
            vcpus: 2,
            memory_mb: 1024,
            ip_address: '172.16.0.10',
            public_ip_enabled: true,
            mac_address: '02:fc:00:00:00:10',
            tap_device: 'tap0',
            disk_path: '/var/lib/compute/vms/vm-alpha/disk.ext4',
            rootfs_path: '/var/lib/compute/images/ubuntu.ext4',
            kernel_path: '/var/lib/compute/vmlinux',
            created_at: new Date().toISOString(),
            updated_at: new Date().toISOString(),
            protected: false,
          },
        ]);
      }
      return json(200, []);
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
    getUnauthedDataRequests: () => vmRequestsUnauthed,
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

async function withBrowser(t, fn) {
  const chromeBin = findChromium();
  if (!chromeBin) {
    t.skip('No Chromium/Chrome binary found');
    return;
  }
  const tmpDir = await fs.mkdtemp(path.join('/tmp', 'cdp-auth-'));
  const chrome = spawn(
    chromeBin,
    [
      '--headless=new',
      '--no-sandbox',
      '--disable-gpu',
      '--remote-debugging-port=0',
      `--user-data-dir=${tmpDir}`,
      '--window-size=1280,900',
      'about:blank',
    ],
    { stdio: ['ignore', 'pipe', 'pipe'], env: { ...process.env, TMPDIR: '/tmp', XDG_RUNTIME_DIR: '/tmp' } },
  );

  let wsEndpoint;
  let logs = '';
  await new Promise((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error(`Chromium timeout:\n${logs}`)), 10000);
    const onData = (buf) => {
      logs += buf.toString();
      const m = logs.match(/DevTools listening on (ws:\/\/[^\s]+)/);
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
  const cdp = new CDPClient(pageTarget.webSocketDebuggerUrl);
  await cdp.connect();
  await cdp.send('Page.enable');
  await cdp.send('Runtime.enable');

  try {
    await fn(cdp);
  } finally {
    try { cdp.close(); } catch {}
    chrome.kill('SIGKILL');
    if (chrome.exitCode === null && chrome.signalCode === null) {
      await Promise.race([
        new Promise((resolve) => chrome.once('exit', resolve)),
        sleep(1500),
      ]).catch(() => {});
    }
    await fs.rm(tmpDir, { recursive: true, force: true }).catch(() => {});
  }
}

test('auth gate blocks unauthenticated dashboard access, respects signup_enabled=false, and signs in cleanly', async (t) => {
  await withBrowser(t, async (cdp) => {
    const mock = createAuthMockServer({ signupEnabled: false });
    await new Promise((resolve) => mock.server.listen(0, '127.0.0.1', resolve));
    const { port } = mock.server.address();
    const baseUrl = `http://127.0.0.1:${port}/`;

    try {
      await cdp.send('Page.navigate', { url: baseUrl });
      await waitFor(cdp, `document.body && document.body.innerText.includes('Sign in to Compute')`, 8000, 'login page rendered');

      assert.equal(await cdp.evalJS(`window.location.hash`), '#/login');
      assert.equal(mock.getUnauthedDataRequests(), 0, 'cluster APIs should not be called before sign-in');
      assert.equal(await cdp.evalJS(`document.body.innerText.includes('Create Account')`), false);

      // Navigate to #/signup when disabled
      await cdp.evalJS(`window.location.hash = '#/signup'`);
      await waitFor(cdp, `document.body.innerText.includes('Public self-registration is disabled')`, 3000, 'signup disabled message');
      await cdp.evalJS(`Array.from(document.querySelectorAll('button')).find(b => b.innerText.includes('Back to Sign In')).click()`);
      await waitFor(cdp, `document.body.innerText.includes('Sign in to Compute')`, 3000, 'returned to login');

      // Submit invalid password -> inline error
      await cdp.evalJS(`
        (() => {
          const set = (el, val) => {
            Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(el, val);
            el.dispatchEvent(new Event('input', { bubbles: true }));
          };
          set(document.getElementById('auth-login-identifier'), 'opsadmin');
          set(document.getElementById('auth-login-password'), 'wrong');
          document.querySelector('form').requestSubmit();
        })()
      `);
      await waitFor(cdp, `document.querySelector('[role="alert"]') !== null`, 3000, 'inline login error');

      // Submit valid credentials -> loads dashboard
      await cdp.evalJS(`
        (() => {
          const set = (el, val) => {
            Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(el, val);
            el.dispatchEvent(new Event('input', { bubbles: true }));
          };
          set(document.getElementById('auth-login-password'), 'StrongPass123!');
          document.querySelector('form').requestSubmit();
        })()
      `);
      await waitFor(cdp, `document.body.innerText.includes('sbx-alpha')`, 5000, 'dashboard loaded after sign-in');

      // Sign out returns to #/login
      await cdp.evalJS(`document.querySelector('button[title="Sign out"]').click()`);
      await waitFor(cdp, `document.body.innerText.includes('Sign in to Compute') && window.location.hash === '#/login'`, 5000, 'returned to login after logout');
    } finally {
      mock.server.closeAllConnections?.();
      mock.server.close();
    }
  });
});

test('auth gate enables Create Account flow when signup_enabled=true', async (t) => {
  await withBrowser(t, async (cdp) => {
    const mock = createAuthMockServer({ signupEnabled: true });
    await new Promise((resolve) => mock.server.listen(0, '127.0.0.1', resolve));
    const { port } = mock.server.address();
    const baseUrl = `http://127.0.0.1:${port}/`;

    try {
      await cdp.send('Page.navigate', { url: baseUrl });
      await waitFor(cdp, `document.body && document.body.innerText.includes('Create Account')`, 8000, 'signup tab visible');

      await cdp.evalJS(`Array.from(document.querySelectorAll('button')).find(b => b.innerText.trim() === 'Create Account').click()`);
      await waitFor(cdp, `document.getElementById('auth-signup-email') !== null`, 3000, 'signup form visible');

      await cdp.evalJS(`
        (() => {
          const set = (id, val) => {
            const el = document.getElementById(id);
            Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(el, val);
            el.dispatchEvent(new Event('input', { bubbles: true }));
          };
          set('auth-signup-email', 'alice@example.com');
          set('auth-signup-username', 'alice');
          set('auth-signup-display-name', 'Alice Test');
          set('auth-signup-password', 'StrongPass123!');
          set('auth-signup-confirm', 'StrongPass123!');
          document.querySelector('form').requestSubmit();
        })()
      `);
      await waitFor(cdp, `document.body.innerText.includes('sbx-alpha')`, 5000, 'dashboard loaded after signup');
    } finally {
      mock.server.closeAllConnections?.();
      mock.server.close();
    }
  });
});
