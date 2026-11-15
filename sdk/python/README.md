# Compute Python SDK

Official Python client for launching and interacting with Compute Firecracker microVM sandboxes.

## Installation

```bash
pip install compute-sdk
```

## Quickstart

```python
from compute import Sandbox

# 1. Launch an isolated microVM sandbox
sandbox = Sandbox.create(template="ubuntu-24", tier="1vcpu")

# 2. Execute shell commands
result = sandbox.commands.run("python3 -c 'import sys; print(sys.version)'")
print(result.stdout)

# 3. Read & Write Files
sandbox.files.write("/workspace/hello.py", "print('Hello from microVM!')")
code = sandbox.files.read("/workspace/hello.py")
print(code)

# 4. Process Management
pid = sandbox.process.start("python3 -m http.server 8080", cwd="/workspace")
print(f"Server running with PID {pid}")

# 5. Access Ingress Preview URL
preview_url = sandbox.get_preview_url(8080)
print(f"Preview URL: {preview_url}")

# 6. Terminate sandbox
sandbox.kill()
```

### Context Manager Support

```python
with Sandbox.create() as sandbox:
    res = sandbox.commands.run("echo 'Instant ephemeral sandbox'")
    print(res.stdout)
# Automatically terminated when exiting context
```
