import { Braces, Copy, Database, Terminal } from 'lucide-react';

import { CopyButton } from '@/components/copy-button';
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from '@/components/ui/card';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';
import type { DaemonConfig } from '@/lib/types';
import { cn } from '@/lib/utils';

interface Endpoint {
  method: 'GET' | 'POST' | 'DELETE';
  path: string;
  description: string;
  example: string;
}

const ENDPOINTS: Endpoint[] = [
  {
    method: 'GET',
    path: '/api/v1/health',
    description: 'Daemon liveness check.',
    example: 'curl -s http://127.0.0.1:18080/api/v1/health',
  },
  {
    method: 'GET',
    path: '/api/v1/stats',
    description: 'Fleet and capacity summary.',
    example: 'curl -s http://127.0.0.1:18080/api/v1/stats',
  },
  {
    method: 'GET',
    path: '/api/v1/vms',
    description: 'List every registered instance.',
    example: 'curl -s http://127.0.0.1:18080/api/v1/vms',
  },
  {
    method: 'POST',
    path: '/api/v1/vms',
    description: 'Provision a new microVM.',
    example:
      'curl -s -X POST http://127.0.0.1:18080/api/v1/vms \\\n  -H "Content-Type: application/json" \\\n  -d \'{"name":"worker-1","vcpus":2,"memory_mb":1024}\'',
  },
  {
    method: 'POST',
    path: '/api/v1/vms/:id/run',
    description: 'Boot a stopped or created instance.',
    example: 'curl -s -X POST http://127.0.0.1:18080/api/v1/vms/vm-1234/run',
  },
  {
    method: 'POST',
    path: '/api/v1/vms/:id/save-state',
    description: 'Snapshot guest memory to disk.',
    example:
      'curl -s -X POST http://127.0.0.1:18080/api/v1/vms/vm-1234/save-state',
  },
  {
    method: 'POST',
    path: '/api/v1/vms/:id/exec',
    description: 'Run a command inside the guest.',
    example:
      'curl -s -X POST http://127.0.0.1:18080/api/v1/vms/vm-1234/exec \\\n  -H "Content-Type: application/json" \\\n  -d \'{"command":"uname -a"}\'',
  },
  {
    method: 'GET',
    path: '/api/v1/images',
    description: 'List custom pre-baked rootfs images.',
    example: 'curl -s http://127.0.0.1:18080/api/v1/images',
  },
  {
    method: 'POST',
    path: '/api/v1/images',
    description: 'Build a custom image with pre-installed packages.',
    example:
      'curl -s -X POST http://127.0.0.1:18080/api/v1/images \\\n  -H "Content-Type: application/json" \\\n  -d \'{"name":"python-tools","packages":["python3","git","curl"]}\'',
  },
  {
    method: 'GET',
    path: '/api/v1/templates',
    description: 'List reusable VM disk templates.',
    example: 'curl -s http://127.0.0.1:18080/api/v1/templates',
  },
  {
    method: 'POST',
    path: '/api/v1/templates',
    description: 'Create a reusable disk template from an existing VM.',
    example:
      'curl -s -X POST http://127.0.0.1:18080/api/v1/templates \\\n  -H "Content-Type: application/json" \\\n  -d \'{"vm_id":"vm-1234","name":"node20-env"}\'',
  },
  {
    method: 'POST',
    path: '/api/v1/templates/:id/launch',
    description: 'Launch a new VM instance from a disk template.',
    example:
      'curl -s -X POST http://127.0.0.1:18080/api/v1/templates/node20-env/launch \\\n  -H "Content-Type: application/json" \\\n  -d \'{"name":"worker-node"}\'',
  },
  {
    method: 'GET',
    path: '/api/v1/network/allocations',
    description: 'Inspect the IPAM lease table.',
    example:
      'curl -s http://127.0.0.1:18080/api/v1/network/allocations',
  },
  {
    method: 'POST',
    path: '/api/v1/network/prune',
    description: 'Remove orphaned TAP interfaces.',
    example:
      'curl -s -X POST http://127.0.0.1:18080/api/v1/network/prune',
  },
];

const CLI_COMMANDS = [
  { command: 'compute sandbox create --name worker-1 --vcpus 2 --memory 1024', label: 'Provision' },
  { command: 'compute image build --name dev-tools --packages "git,curl,python3"', label: 'Build Image' },
  { command: 'compute image list', label: 'List Images' },
  { command: 'compute template create --vm <vm-id> --name dev-tmpl', label: 'Create Template' },
  { command: 'compute template launch <tmpl-id> --name worker-dev', label: 'Launch Template' },
  { command: 'compute sandbox create --name worker-2 --template dev-tmpl', label: 'Create from Template' },
  { command: 'compute sandbox start <vm-id>', label: 'Start' },
  { command: 'compute sandbox stop <vm-id>', label: 'Stop' },
  { command: 'compute sandbox snapshot <vm-id>', label: 'Snapshot' },
  { command: 'compute sandbox resume <vm-id>', label: 'Resume' },
  { command: 'compute sandbox exec <vm-id> -- uname -a', label: 'Exec' },
  { command: 'compute gui --addr 0.0.0.0 --port 8080', label: 'Launch UI' },
];

export function ApiPage({ config }: { config: DaemonConfig | null }) {
  return (
    <div className="grid items-start gap-4 animate-fade-in lg:grid-cols-2">
      <Card>
        <CardHeader className="flex-row items-center justify-between space-y-0 border-b pb-4">
          <div className="flex items-center gap-2">
            <Braces className="h-4 w-4 text-muted-foreground" />
            <CardTitle>REST API</CardTitle>
          </div>
          <span className="font-mono text-xs text-muted-foreground">
            {ENDPOINTS.length} endpoints
          </span>
        </CardHeader>
        <CardContent className="space-y-4 pt-4">
          <p className="text-xs text-muted-foreground">
            The daemon exposes an HTTP/JSON API on{' '}
            <code className="font-mono text-foreground">
              {config?.server.listen || '127.0.0.1:18080'}
            </code>
            . All mutating requests accept and return JSON.
          </p>
          <div className="space-y-2">
            {ENDPOINTS.map((endpoint) => (
              <div
                key={`${endpoint.method}-${endpoint.path}`}
                className="rounded-md border p-3 text-xs"
              >
                <div className="flex items-center justify-between gap-2">
                  <div className="flex items-center gap-2 font-mono">
                    <span
                      className={cn(
                        'rounded px-1.5 py-0.5 text-[10px] font-semibold',
                        endpoint.method === 'GET' &&
                          'bg-info/10 text-info',
                        endpoint.method === 'POST' &&
                          'bg-success/10 text-success',
                        endpoint.method === 'DELETE' &&
                          'bg-destructive/10 text-destructive',
                      )}
                    >
                      {endpoint.method}
                    </span>
                    <span className="font-medium text-foreground">
                      {endpoint.path}
                    </span>
                  </div>
                  <CopyButton value={endpoint.example} label="Copy example curl" />
                </div>
                <p className="mt-1 text-muted-foreground">
                  {endpoint.description}
                </p>
                <pre className="mt-2 overflow-x-auto rounded bg-secondary/50 p-2 font-mono text-[11px] text-foreground">
                  {endpoint.example}
                </pre>
              </div>
            ))}
          </div>
        </CardContent>
      </Card>

      <div className="space-y-4">
        <Card>
          <CardHeader className="flex-row items-center justify-between space-y-0 border-b pb-4">
            <div className="flex items-center gap-2">
              <Terminal className="h-4 w-4 text-muted-foreground" />
              <CardTitle>CLI reference</CardTitle>
            </div>
            <span className="font-mono text-xs text-muted-foreground">
              compute
            </span>
          </CardHeader>
          <CardContent className="space-y-2 pt-4">
            {CLI_COMMANDS.map((item) => (
              <div
                key={item.command}
                className="flex items-center justify-between rounded-md border p-2.5 text-xs"
              >
                <div className="min-w-0 pr-2">
                  <div className="text-[10px] uppercase tracking-wider text-muted-foreground">
                    {item.label}
                  </div>
                  <code className="block truncate font-mono text-foreground">
                    {item.command}
                  </code>
                </div>
                <CopyButton value={item.command} label="Copy command" />
              </div>
            ))}
          </CardContent>
        </Card>

        <Card>
          <CardHeader className="flex-row items-center justify-between space-y-0 border-b pb-4">
            <div className="flex items-center gap-2">
              <Database className="h-4 w-4 text-muted-foreground" />
              <CardTitle>Runtime configuration</CardTitle>
            </div>
            <span className="font-mono text-xs text-muted-foreground">
              active
            </span>
          </CardHeader>
          <CardContent className="pt-4">
            <pre className="max-h-96 overflow-auto rounded bg-secondary/50 p-3 font-mono text-xs">
              {JSON.stringify(config, null, 2)}
            </pre>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
