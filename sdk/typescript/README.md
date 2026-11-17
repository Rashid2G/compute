# @compute/sdk

Official TypeScript & JavaScript client for launching and interacting with Compute Firecracker microVM sandboxes.

## Installation

```bash
npm install @compute/sdk
```

## Quickstart

```typescript
import { Sandbox } from '@compute/sdk';

async function main() {
  // 1. Launch microVM sandbox
  const sandbox = await Sandbox.create({
    template: 'ubuntu-24',
    tier: '1vcpu',
  });

  // 2. Run shell command
  const res = await sandbox.commands.run('node -e "console.log(1 + 1)"');
  console.log(res.stdout); // 2

  // 3. Write & read files
  await sandbox.files.write('/workspace/server.js', 'console.log("running")');
  const code = await sandbox.files.read('/workspace/server.js');
  console.log(code);

  // 4. Ingress preview URL
  const { url: serveUrl } = await sandbox.exposePort(3000);
  console.log('Preview:', previewUrl);

  // 5. Clean up
  await sandbox.kill();
}

main().catch(console.error);
```
