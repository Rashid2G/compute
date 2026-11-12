# Compute HTTP & WebSocket API Reference

The `computed` daemon exposes a comprehensive REST and WebSocket API. By default, the control plane listens on port `18080` (e.g. `http://127.0.0.1:18080`).

## Headers & Authentication

All API requests (except `/api/v1/health` and public preview routes) require authentication:
```http
Authorization: Bearer <API_KEY_OR_TOKEN>
X-API-Key: <API_KEY_OR_TOKEN>
Content-Type: application/json
```

---

## 1. System & Health

### `GET /api/v1/health`
Checks control plane daemon liveness.
- **Response:** `200 OK`
```json
{
  "status": "ok",
  "time": "2026-11-11T10:00:00Z"
}
```

### `GET /api/v1/version`
Returns daemon build and release metadata.

---

## 2. Sandboxes (`/api/v1/sandboxes` or `/api/v1/vms`)

### `GET /api/v1/sandboxes`
List all accessible sandboxes.

### `POST /api/v1/sandboxes`
Create a new microVM sandbox.
- **Request Body:**
```json
{
  "name": "web-service-1",
  "tier": "2vcpu",
  "vcpus": 2,
  "memory_mb": 2048,
  "disk_mb": 10240,
  "image": "ubuntu-base",
  "protected": false
}
```

### `GET /api/v1/sandboxes/:id`
Inspect detailed metadata for a specific sandbox.

### `POST /api/v1/sandboxes/:id/start`
Start the microVM instance.

### `POST /api/v1/sandboxes/:id/stop`
Stop the microVM instance gracefully.

### `POST /api/v1/sandboxes/:id/save-state`
Take a live memory snapshot of the microVM. Transitions state to `paused`.

### `POST /api/v1/sandboxes/:id/resume`
Resume execution from snapshot. Transitions state back to `running`.

### `DELETE /api/v1/sandboxes/:id`
Delete sandbox and associated storage. Fails with `400 Bad Request` if protected.

### `POST /api/v1/sandboxes/:id/protect`
Enable deletion protection.

### `POST /api/v1/sandboxes/:id/unprotect`
Disable deletion protection.

---

## 3. Command Execution & SSH

### `POST /api/v1/sandboxes/:id/exec`
Synchronously execute a command inside the sandbox.
- **Request Body:**
```json
{
  "command": "npm test",
  "timeout": 60,
  "user": "compute"
}
```
- **Response Body:**
```json
{
  "exit_code": 0,
  "stdout": "All tests passed\n",
  "stderr": "",
  "duration_ms": 1420
}
```

### `GET /api/v1/sandboxes/:id/exec/stream`
Server-Sent Events (SSE) or WebSocket streaming endpoint for live execution output.

### `GET /api/v1/sandboxes/:id/ssh`
WebSocket tunnel for interactive SSH terminal sessions.

---

## 4. Guest Filesystem Operations

Operate directly on the guest filesystem over REST without spawning subshells:

### `POST /api/v1/sandboxes/:id/files` (or `/files/write`)
Write a file into the guest filesystem.
- **Request Body:**
```json
{
  "path": "/home/compute/app/config.json",
  "content": "eyJwb3J0IjogMzAwMH0=",
  "format": "base64"
}
```
*(Format can be `"text"` or `"base64"`).*

### `GET /api/v1/sandboxes/:id/files?path=/path/to/file&format=text`
Read file content from the guest. Returns `{ "path": "...", "content": "...", "format": "text", "size": 128 }`.

### `GET /api/v1/sandboxes/:id/files?path=/path/to/dir&list=true`
List directory entries inside the guest. Returns `[{"name": "index.js", "path": "...", "is_dir": false, "size": 2048}]`.

### `DELETE /api/v1/sandboxes/:id/files?path=/path/to/file`
Remove a file or directory inside the guest.

---

## 5. Guest Process Management

Launch and monitor background processes inside the guest:

### `POST /api/v1/sandboxes/:id/processes`
Launch a background process.
- **Request Body:**
```json
{
  "command": "npm run start",
  "cwd": "/home/compute/app",
  "env": {
    "NODE_ENV": "production",
    "PORT": "3000"
  }
}
```
- **Response:** `200 OK`
```json
{
  "status": "started",
  "pid": 2841,
  "command": "npm run start"
}
```

### `GET /api/v1/sandboxes/:id/processes`
List all active processes inside the guest with PID, PPID, User, CPU%, Memory%, Status, Command, and Args.

### `POST /api/v1/sandboxes/:id/processes/kill`
Send a signal to a process inside the guest.
- **Request Body:**
```json
{
  "pid": 2841,
  "signal": "SIGTERM"
}
```

---

## 6. Ingress Reverse Proxy & Previews

The unified Ingress Reverse Proxy exposes running sandbox guest ports to host or public networks.

### Route Resolution

1. **Path-Based Route:**
   - Pattern: `/preview/:sandbox_id_or_name/:port/*`
   - Rewrite: Strips prefix and passes subpath to guest `http://<guest_ip>:<port>/*`.
2. **Subdomain-Based Route:**
   - Pattern: `<port>-<sandbox_id_or_name>.<base_domain>` (e.g. `3000-my-web-app.preview.localhost:18080`)
   - Pattern: `<sandbox_id_or_name>-<port>.<base_domain>` (e.g. `my-web-app-3000.preview.localhost:18080`)

### Public Port Exposure API: `POST /api/v1/network/port-forwards`
Registers a port forwarding rule and configures authorization bypass for public ingress:
- **Request Body:**
```json
{
  "vm_id": "my-web-app",
  "guest_port": 3000,
  "host_port": 34000,
  "protocol": "tcp",
  "public": true
}
```
- **Response:** `201 Created`
```json
{
  "id": "pf_a1b2c3d4",
  "vm_id": "my-web-app",
  "guest_port": 3000,
  "host_port": 34000,
  "public": true,
  "status": "active"
}
```

### Delete Port Forward: `DELETE /api/v1/network/port-forwards/:id`
Tears down the host forwarding rule and unregisters public exposure.

---

## 7. Distributed Jobs API (`/api/v1/jobs`)

### `POST /api/v1/jobs`
Submit an ephemeral job.
- **Request Body:**
```json
{
  "type": "command",
  "pool_id": "default",
  "priority": 10,
  "spec": {
    "image_id": "default",
    "vcpus": 2,
    "memory_mb": 2048,
    "command": ["npm", "test"],
    "cwd": "/workspace",
    "files": "<base64-encoded-tar-gz>",
    "timeout_seconds": 300
  }
}
```

### `GET /api/v1/jobs/:id`
Inspect job status (`pending`, `running`, `succeeded`, `failed`, `canceled`).

### `GET /api/v1/jobs/:id/logs`
Retrieve job log events (`stdout` and `stderr` lines with sequence numbers).

### `POST /api/v1/jobs/:id/cancel`
Cancel an active or queued job.
