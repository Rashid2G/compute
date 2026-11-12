import json
import os
import requests
from typing import Optional, Dict, List, Any
from dataclasses import dataclass
from .exceptions import ComputeError, SandboxNotFoundError, AuthenticationError, CommandExecutionError


@dataclass
class ExecutionResult:
    stdout: str
    stderr: str
    exit_code: int
    duration_ms: int = 0


class Commands:
    def __init__(self, sandbox: "Sandbox"):
        self._sandbox = sandbox

    def run(self, cmd: str, timeout: int = 60, user: Optional[str] = None) -> ExecutionResult:
        """Execute a shell command inside the sandbox."""
        payload = {"command": cmd, "timeout": timeout}
        if user:
            payload["user"] = user

        resp = self._sandbox._request("POST", f"/api/v1/sandboxes/{self._sandbox.id}/exec", json=payload)
        data = resp.json()
        exit_code = data.get("exit_code", 0)
        stdout = data.get("stdout", "")
        stderr = data.get("stderr", "")
        duration_ms = data.get("duration_ms", 0)

        if exit_code != 0:
            raise CommandExecutionError(
                f"Command '{cmd}' exited with code {exit_code}: {stderr}",
                exit_code=exit_code,
                stdout=stdout,
                stderr=stderr
            )

        return ExecutionResult(stdout=stdout, stderr=stderr, exit_code=exit_code, duration_ms=duration_ms)


class Filesystem:
    def __init__(self, sandbox: "Sandbox"):
        self._sandbox = sandbox

    def write(self, path: str, content: str, format: str = "text") -> None:
        """Write content to a file inside the sandbox (creates parent directories automatically)."""
        payload = {"path": path, "content": content, "format": format}
        self._sandbox._request("POST", f"/api/v1/sandboxes/{self._sandbox.id}/files/write", json=payload)

    def read(self, path: str, format: str = "text") -> str:
        """Read content from a file inside the sandbox."""
        resp = self._sandbox._request(
            "GET",
            f"/api/v1/sandboxes/{self._sandbox.id}/files/read",
            params={"path": path, "format": format}
        )
        return resp.json().get("content", "")

    def list(self, path: str = "/workspace") -> List[Dict[str, Any]]:
        """List files and directories inside the sandbox."""
        resp = self._sandbox._request(
            "GET",
            f"/api/v1/sandboxes/{self._sandbox.id}/files/list",
            params={"path": path}
        )
        return resp.json().get("entries", [])

    def remove(self, path: str) -> None:
        """Delete a file or directory inside the sandbox."""
        self._sandbox._request(
            "DELETE",
            f"/api/v1/sandboxes/{self._sandbox.id}/files",
            params={"path": path}
        )

    def mkdir(self, path: str) -> None:
        """Create a directory inside the sandbox."""
        self._sandbox._request(
            "POST",
            f"/api/v1/sandboxes/{self._sandbox.id}/files/mkdir",
            json={"path": path}
        )


class ProcessManager:
    def __init__(self, sandbox: "Sandbox"):
        self._sandbox = sandbox

    def list(self) -> List[Dict[str, Any]]:
        """List all processes currently running inside the sandbox."""
        resp = self._sandbox._request("GET", f"/api/v1/sandboxes/{self._sandbox.id}/processes")
        return resp.json().get("processes", [])

    def start(self, command: str, cwd: Optional[str] = None, env: Optional[Dict[str, str]] = None) -> int:
        """Start a background process inside the sandbox and return its PID."""
        payload = {"command": command}
        if cwd:
            payload["cwd"] = cwd
        if env:
            payload["env"] = env

        resp = self._sandbox._request("POST", f"/api/v1/sandboxes/{self._sandbox.id}/processes", json=payload)
        return resp.json().get("pid", 0)

    def kill(self, pid: int, signal: str = "SIGTERM") -> None:
        """Send a POSIX signal to a process inside the sandbox."""
        payload = {"pid": pid, "signal": signal}
        self._sandbox._request("POST", f"/api/v1/sandboxes/{self._sandbox.id}/processes/kill", json=payload)


class Sandbox:
    """
    Compute Sandbox representing an isolated microVM.
    Provides complete parity with E2B and Boat.dev workflows.
    """

    def __init__(self, sandbox_id: str, host: str, api_key: Optional[str] = None):
        self.id = sandbox_id
        self._host = host.rstrip("/")
        self._api_key = api_key
        self.commands = Commands(self)
        self.files = Filesystem(self)
        self.process = ProcessManager(self)

    @classmethod
    def create(
        cls,
        template: Optional[str] = None,
        tier: Optional[str] = None,
        vcpus: Optional[int] = None,
        memory_mb: Optional[int] = None,
        disk_mb: Optional[int] = None,
        name: Optional[str] = None,
        api_key: Optional[str] = None,
        host: Optional[str] = None,
        timeout: Optional[int] = None,
        ssh_password: Optional[str] = None,
    ) -> "Sandbox":
        """Provision and launch a new isolated compute sandbox in milliseconds."""
        resolved_host = host or os.getenv("COMPUTE_HOST") or os.getenv("COMPUTE_DAEMON") or "http://127.0.0.1:18080"
        if not resolved_host.startswith("http://") and not resolved_host.startswith("https://"):
            resolved_host = "http://" + resolved_host

        resolved_key = api_key or os.getenv("COMPUTE_API_KEY")

        payload = {}
        if template:
            payload["template"] = template
        if tier:
            payload["tier"] = tier
        if vcpus:
            payload["vcpus"] = vcpus
        if memory_mb:
            payload["memory_mb"] = memory_mb
        if disk_mb:
            payload["disk_mb"] = disk_mb
        if name:
            payload["name"] = name
        if ssh_password:
            payload["ssh_password"] = ssh_password

        headers = {"Content-Type": "application/json"}
        if resolved_key:
            headers["Authorization"] = f"Bearer {resolved_key}"

        # 1. Create sandbox
        resp = requests.post(f"{resolved_host}/api/v1/sandboxes", json=payload, headers=headers)
        if resp.status_code == 401:
            raise AuthenticationError("Authentication failed: invalid or missing API key.")
        if resp.status_code >= 400:
            raise ComputeError(f"Failed to create sandbox: {resp.text}")

        data = resp.json()
        sb_id = data.get("id")

        # 2. Boot sandbox
        run_resp = requests.post(f"{resolved_host}/api/v1/sandboxes/{sb_id}/run", headers=headers)
        if run_resp.status_code >= 400:
            raise ComputeError(f"Failed to boot sandbox {sb_id}: {run_resp.text}")

        return cls(sandbox_id=sb_id, host=resolved_host, api_key=resolved_key)

    @classmethod
    def connect(cls, sandbox_id: str, host: Optional[str] = None, api_key: Optional[str] = None) -> "Sandbox":
        """Connect to an existing running sandbox by ID."""
        resolved_host = host or os.getenv("COMPUTE_HOST") or os.getenv("COMPUTE_DAEMON") or "http://127.0.0.1:18080"
        if not resolved_host.startswith("http://") and not resolved_host.startswith("https://"):
            resolved_host = "http://" + resolved_host
        resolved_key = api_key or os.getenv("COMPUTE_API_KEY")
        return cls(sandbox_id=sandbox_id, host=resolved_host, api_key=resolved_key)

    def get_preview_url(self, port: int) -> str:
        """Return the public preview URL to access an HTTP/WebSocket server running on a port inside the sandbox."""
        return f"{self._host}/preview/{self.id}/{port}/"

    def kill(self) -> None:
        """Stop and terminate the microVM sandbox and free all associated resources."""
        self._request("DELETE", f"/api/v1/sandboxes/{self.id}")

    def close(self) -> None:
        """Alias for kill()."""
        self.kill()

    def _request(self, method: str, path: str, **kwargs) -> requests.Response:
        url = f"{self._host}{path}"
        headers = kwargs.pop("headers", {})
        if self._api_key:
            headers["Authorization"] = f"Bearer {self._api_key}"
            headers["X-API-Key"] = self._api_key

        resp = requests.request(method, url, headers=headers, **kwargs)
        if resp.status_code == 401:
            raise AuthenticationError("Unauthorized: please provide a valid API key.")
        if resp.status_code == 404:
            raise SandboxNotFoundError(f"Sandbox {self.id} not found.")
        if resp.status_code >= 400:
            raise ComputeError(f"Compute API error ({resp.status_code}): {resp.text}")
        return resp

    def __enter__(self) -> "Sandbox":
        return self

    def __exit__(self, exc_type, exc_val, exc_tb):
        self.kill()
