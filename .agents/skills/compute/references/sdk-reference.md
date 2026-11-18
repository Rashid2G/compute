# Compute SDK Reference

Two official clients cover the core sandbox workflow: TypeScript/JavaScript (`@compute/sdk`) and
Python (`compute-sdk`). Both create and boot a sandbox, run commands, move files, manage
background processes, expose ports, and kill the sandbox. Everything else (templates, images,
volumes, jobs, workers, admin) uses the REST API documented in `api-reference.md`.

Both SDKs resolve connection settings from options first, then:
`COMPUTE_HOST` → `COMPUTE_DAEMON` → `http://127.0.0.1:18080`, and `COMPUTE_API_KEY` for auth.

---

## TypeScript / JavaScript

### Install

```bash
npm install @compute/sdk
```

### Quick start

```ts
import { Sandbox } from '@compute/sdk';

const sandbox = await Sandbox.create({
  template: 'ubuntu-24',
  tier: '2vcpu',
  name: 'agent-box',
});

const res = await sandbox.commands.run('node -e "console.log(1 + 1)"');
console.log(res.stdout); // 2

await sandbox.files.write('/workspace/app.js', 'console.log("running")');
console.log(await sandbox.files.read('/workspace/app.js'));

const { url } = await sandbox.exposePort(3000, { public: true });
console.log('Preview:', url);

await sandbox.kill();
```

### `Sandbox`

| Member | Signature | Behavior |
| :--- | :--- | :--- |
| `Sandbox.create` | `(options?: SandboxCreateOptions) => Promise<Sandbox>` | POSTs a sandbox then calls `/run`; returns a connected handle. |
| `Sandbox.connect` | `(id, options?: {host?, apiKey?}) => Sandbox` | Attaches to an existing sandbox without booting it. |
| `id` | `string` | Sandbox ID. |
| `commands` | `Commands` | Command execution. |
| `files` | `Filesystem` | File operations. |
| `process` | `ProcessManager` | Background processes. |
| `exposePort` | `(port, options?: {public?, subdomain?}) => Promise<{url, public_url, public, expires_at?}>` | Creates a dedicated-origin exposure; private exposures return a serve token URL. |
| `setNetworkIsolation` | `(mode: 'account'|'isolated') => Promise<any>` | Switch isolation mode. |
| `kill` | `() => Promise<void>` | DELETE the sandbox and its disk. |
| `request` | `(method, path, body?) => Promise<Response>` | Low-level authenticated request for unsupported endpoints. |

### `SandboxCreateOptions`

```ts
interface SandboxCreateOptions {
  template?: string;
  tier?: '1vcpu' | '2vcpu' | '4vcpu' | string;
  vcpus?: number;
  memoryMb?: number;
  diskMb?: number;
  name?: string;
  apiKey?: string;
  host?: string;
  timeout?: number;
  sshPassword?: string;
  networkIsolation?: 'account' | 'isolated';
  isolated?: boolean;
}
```

`timeout` is accepted by the type but the current implementation does not send it; pass a command
timeout to `commands.run` instead.

### `Commands`

```ts
const result = await sandbox.commands.run('npm test', { timeout: 120, user: 'compute' });
// result: { stdout, stderr, exitCode, durationMs }
```

The SDK does not throw on a non-zero exit code; inspect `exitCode`. REST errors throw
`Error("Compute API Error (<status>): <body>")`.

### `Filesystem`

```ts
await sandbox.files.write('/workspace/data.json', JSON.stringify({ok: true}));
await sandbox.files.write('/workspace/logo.png', base64, 'base64');
const text = await sandbox.files.read('/workspace/data.json');
const entries = await sandbox.files.list('/workspace'); // {name, path, isDir, size}[]
await sandbox.files.mkdir('/workspace/out');
await sandbox.files.remove('/workspace/out');
```

### `ProcessManager`

```ts
const pid = await sandbox.process.start('node /workspace/app.js', {
  cwd: '/workspace',
  env: {NODE_ENV: 'production', PORT: '3000'},
});
const processes = await sandbox.process.list();
await sandbox.process.kill(pid, 'SIGTERM');
```

### Raw REST escape hatch

```ts
const res = await sandbox.request('POST', `/api/v1/sandboxes/${sandbox.id}/save-state`);
const vm = await res.json();
```

Use `sandbox.request` for endpoints the SDK does not wrap (snapshots, templates, volumes,
metrics, etc.). It applies the same auth headers.

---

## Python

### Install

```bash
pip install compute-sdk
```

### Quick start

```python
from compute import Sandbox

sandbox = Sandbox.create(template="ubuntu-24", tier="2vcpu", name="agent-box")
result = sandbox.commands.run("python3 --version")
print(result.stdout)

sandbox.files.write("/workspace/hello.py", "print('hello')")
print(sandbox.files.read("/workspace/hello.py"))

pid = sandbox.process.start("python3 -m http.server 8080", cwd="/workspace")
preview = sandbox.expose_port(8080)
print(preview["url"])

sandbox.kill()
```

### Context manager

```python
with Sandbox.create() as sandbox:
    print(sandbox.commands.run("echo ready").stdout)
# __exit__ calls kill() even if the block raised
```

### `Sandbox`

| Member | Signature | Behavior |
| :--- | :--- | :--- |
| `Sandbox.create` | `(template=None, tier=None, vcpus=None, memory_mb=None, disk_mb=None, name=None, api_key=None, host=None, timeout=None, ssh_password=None, network_isolation=None, isolated=None) -> Sandbox` | Creates and boots. |
| `Sandbox.connect` | `(sandbox_id, host=None, api_key=None) -> Sandbox` | Attaches to an existing sandbox. |
| `commands` | `Commands` | Command execution. |
| `files` | `Filesystem` | File operations. |
| `process` | `ProcessManager` | Background processes. |
| `expose_port` | `(port, public=True, subdomain="") -> dict` | Returns `{url, public_url, public}` or a serve-token payload for private exposures. |
| `set_network_isolation` | `(mode: str) -> dict` | `"account"` or `"isolated"`. |
| `kill` / `close` | `() -> None` | Terminate the sandbox. |
| `_request` | `(method, path, **kwargs) -> requests.Response` | Low-level authenticated request. |

### `Commands`

```python
result = sandbox.commands.run("make test", timeout=600, user="compute")
print(result.stdout, result.stderr, result.exit_code, result.duration_ms)
```

Unlike the TypeScript SDK, Python raises `CommandExecutionError` when `exit_code != 0`; the
exception carries `exit_code`, `stdout`, and `stderr`.

### `Filesystem`

```python
sandbox.files.write("/workspace/data.json", '{"ok": true}')
sandbox.files.write("/workspace/logo.png", base64_data, format="base64")
text = sandbox.files.read("/workspace/data.txt")
entries = sandbox.files.list("/workspace")
sandbox.files.mkdir("/workspace/out")
sandbox.files.remove("/workspace/out")
```

### `ProcessManager`

```python
pid = sandbox.process.start("node /workspace/app.js", cwd="/workspace", env={"PORT": "3000"})
processes = sandbox.process.list()
sandbox.process.kill(pid, signal="SIGTERM")
```

### Errors

`ComputeError` is the base class. `AuthenticationError` maps HTTP 401, `SandboxNotFoundError`
maps HTTP 404, and `CommandExecutionError` maps a non-zero guest exit. Other HTTP failures raise
`ComputeError` with the status and response body.

### Raw REST escape hatch

```python
resp = sandbox._request("POST", f"/api/v1/sandboxes/{sandbox.id}/save-state")
vm = resp.json()
```

---

## What the SDKs do not cover

Use REST for:

- templates, images, volumes
- snapshots beyond raw requests (`save-state`, `resume`)
- jobs and GitHub CI
- workers, pools, scheduling policy, migration
- users, orgs, API keys, SSH keys
- events, webhooks, OTel, environments
- operations, tasks, queue, admin resources
- bulk create/clone/action/delete

All of those endpoints accept the same `Authorization`/`X-API-Key` headers the SDKs use, so a
raw request or a thin wrapper is enough.
