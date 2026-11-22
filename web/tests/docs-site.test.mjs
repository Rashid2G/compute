import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import fs from 'node:fs/promises';
import { existsSync } from 'node:fs';
import http from 'node:http';
import path from 'node:path';
import test from 'node:test';

const WEB_DIR = path.resolve(import.meta.dirname, '..');
const REPO_ROOT = path.resolve(WEB_DIR, '..');
const DOCS_DIR = path.join(REPO_ROOT, 'docs');

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
      throw new Error(res.exceptionDetails.text || 'Runtime.evaluate error');
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

test('GitHub Pages documentation site (docs/index.html) renders, filters sections, and toggles theme', async (t) => {
  assert.ok(existsSync(path.join(DOCS_DIR, '.nojekyll')), 'docs/.nojekyll must exist');
  assert.ok(existsSync(path.join(DOCS_DIR, 'index.html')), 'docs/index.html must exist');
  assert.ok(existsSync(path.join(DOCS_DIR, 'styles.css')), 'docs/styles.css must exist');
  assert.ok(existsSync(path.join(DOCS_DIR, 'app.js')), 'docs/app.js must exist');
  assert.ok(
    existsSync(path.join(REPO_ROOT, '.github', 'workflows', 'docs.yml')),
    '.github/workflows/docs.yml must exist',
  );

  const chromeBin = findChromium();
  if (!chromeBin) {
    t.skip('No Chromium binary found');
    return;
  }

  const mime = {
    '.html': 'text/html; charset=utf-8',
    '.js': 'application/javascript; charset=utf-8',
    '.css': 'text/css; charset=utf-8',
    '.md': 'text/markdown; charset=utf-8',
  };

  const server = http.createServer(async (req, res) => {
    const u = new URL(req.url, 'http://127.0.0.1');
    const rel = u.pathname === '/' ? 'index.html' : u.pathname.replace(/^\/+/, '');
    const filePath = path.join(DOCS_DIR, rel);
    try {
      const data = await fs.readFile(filePath);
      const ext = path.extname(filePath);
      res.writeHead(200, { 'Content-Type': mime[ext] || 'text/plain; charset=utf-8' });
      res.end(data);
    } catch {
      res.writeHead(404);
      res.end('not found');
    }
  });

  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  const { port } = server.address();
  const tmpDir = await fs.mkdtemp(path.join('/tmp', 'cdp-docs-'));

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
    {
      stdio: ['ignore', 'pipe', 'pipe'],
      env: { ...process.env, TMPDIR: '/tmp', XDG_RUNTIME_DIR: '/tmp' },
    },
  );

  let wsEndpoint;
  let logs = '';
  await new Promise((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error(`Chrome timeout:\n${logs}`)), 10000);
    const onData = (b) => {
      logs += b.toString();
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

  const targetRes = await fetch(
    `http://${new URL(wsEndpoint).host}/json/new?${encodeURIComponent(`http://127.0.0.1:${port}/`)}`,
    { method: 'PUT' },
  );
  const target = await targetRes.json();
  const cdp = new CDPClient(target.webSocketDebuggerUrl);
  await cdp.connect();
  await cdp.send('Runtime.enable');

  const pageErrors = [];
  cdp.ws.addEventListener('message', (ev) => {
    const msg = JSON.parse(ev.data);
    if (msg.method === 'Runtime.exceptionThrown') {
      pageErrors.push(msg.params?.exceptionDetails?.text || 'Uncaught exception');
    }
  });

  try {
    await waitFor(
      cdp,
      `document.body && document.body.innerText.includes('Self-Hosted Firecracker MicroVM Sandboxes')`,
      5000,
      'docs hero rendered',
    );

    const sectionCount = await cdp.evalJS(`document.querySelectorAll('.doc-section').length`);
    assert.ok(sectionCount >= 13, `Expected >= 13 documentation sections, got ${sectionCount}`);

    // Verify all sidebar nav-links resolve to matching section IDs
    const missingAnchors = await cdp.evalJS(`
      (() => {
        const missing = [];
        document.querySelectorAll('.nav-link').forEach((a) => {
          const href = a.getAttribute('href') || '';
          if (!href.startsWith('#')) return;
          const id = href.slice(1);
          if (!document.getElementById(id)) missing.push(id);
        });
        return missing;
      })()
    `);
    assert.deepEqual(missingAnchors, [], 'Every nav-link href must match a section ID');

    // Verify generous computed vertical spacing on .doc-section (marginBottom + paddingBottom >= 96px)
    const spacingCheck = await cdp.evalJS(`
      (() => {
        const sections = Array.from(document.querySelectorAll('.doc-section'));
        return sections.map((sec) => {
          const cs = getComputedStyle(sec);
          const mb = parseFloat(cs.marginBottom) || 0;
          const pb = parseFloat(cs.paddingBottom) || 0;
          return { id: sec.id, mb, pb, total: mb + pb };
        });
      })()
    `);
    for (const s of spacingCheck) {
      assert.ok(
        s.total >= 96,
        `Expected section #${s.id} marginBottom + paddingBottom >= 96px, got ${s.total}px (mb=${s.mb}, pb=${s.pb})`,
      );
    }

    // Verify new installation & multi-node sections render expected content cleanly
    const contentCheck = await cdp.evalJS(`
      (() => {
        const quickstart = document.getElementById('quickstart')?.innerText || '';
        const authEnv = document.getElementById('auth-env')?.innerText || '';
        const scheduler = document.getElementById('multi-node-scheduler')?.innerText || '';
        const workers = document.getElementById('distributed-jobs')?.innerText || '';
        return {
          hasInstallScript:
            quickstart.includes('./scripts/install.sh') &&
            quickstart.includes('vmlinux') &&
            !quickstart.includes('compute-init'),
          hasEmbeddedUiNote: quickstart.includes(':18080') && quickstart.includes('compute-gui'),
          hasNonLoopbackNote: authEnv.includes('COMPUTE_BIND_ADDRESS=0.0.0.0') && authEnv.includes('COMPUTE_ADMIN_SECRET'),
          hasSchedulerStrategies: scheduler.includes('least_loaded') && scheduler.includes('bin_pack') && scheduler.includes('ResourceLimitError'),
          hasMigrationAndWS: scheduler.includes('/api/v1/vms/{id}/migrate') && scheduler.includes('/api/v1/ws'),
          hasWorkerTunnel: workers.includes('token.pool_id') && workers.includes('zero inbound ports'),
        };
      })()
    `);
    assert.ok(contentCheck.hasInstallScript, 'Quickstart must explain ./scripts/install.sh and vmlinux without compute-init');
    assert.ok(contentCheck.hasEmbeddedUiNote, 'Quickstart must clarify embedded UI on :18080 vs optional compute-gui');
    assert.ok(contentCheck.hasNonLoopbackNote, 'Auth section must document COMPUTE_BIND_ADDRESS=0.0.0.0 credential requirements');
    assert.ok(contentCheck.hasSchedulerStrategies, 'Scheduler section must document load balancer strategies & ResourceLimitError');
    assert.ok(contentCheck.hasMigrationAndWS, 'Scheduler section must document migration & /api/v1/ws');
    assert.ok(contentCheck.hasWorkerTunnel, 'Workers section must document pool precedence & zero-inbound-port tunnel');

    // Verify search filter narrows sections (both existing and new topics)
    await cdp.evalJS(`
      (() => {
        const input = document.getElementById('doc-search');
        input.value = 'save-state';
        input.dispatchEvent(new Event('input', { bubbles: true }));
      })()
    `);
    await waitFor(
      cdp,
      `!document.getElementById('pausing').classList.contains('search-dimmed') && document.getElementById('sdks').classList.contains('search-dimmed')`,
      3000,
      'search filter highlights pausing section and dims unrelated sections',
    );

    await cdp.evalJS(`
      (() => {
        const input = document.getElementById('doc-search');
        input.value = 'least_loaded';
        input.dispatchEvent(new Event('input', { bubbles: true }));
      })()
    `);
    await waitFor(
      cdp,
      `!document.getElementById('multi-node-scheduler').classList.contains('search-dimmed') && document.getElementById('pausing').classList.contains('search-dimmed')`,
      3000,
      'search filter highlights multi-node-scheduler section for least_loaded',
    );

    // Clear search
    await cdp.evalJS(`
      (() => {
        const input = document.getElementById('doc-search');
        input.value = '';
        input.dispatchEvent(new Event('input', { bubbles: true }));
      })()
    `);

    // Verify theme toggle switches dark class
    const initialDark = await cdp.evalJS(`document.documentElement.classList.contains('dark')`);
    await cdp.evalJS(`document.getElementById('theme-toggle').click()`);
    const afterToggleDark = await cdp.evalJS(`document.documentElement.classList.contains('dark')`);
    assert.notEqual(initialDark, afterToggleDark, 'Theme toggle must flip dark class');

    assert.deepEqual(pageErrors, [], `Expected zero runtime exceptions in docs page, got: ${pageErrors.join('; ')}`);
  } finally {
    try { cdp.close(); } catch {}
    chrome.kill('SIGKILL');
    if (chrome.exitCode === null && chrome.signalCode === null) {
      await Promise.race([
        new Promise((resolve) => chrome.once('exit', resolve)),
        sleep(1500),
      ]).catch(() => {});
    }
    server.closeAllConnections?.();
    server.close();
    await fs.rm(tmpDir, { recursive: true, force: true }).catch(() => {});
  }
});
