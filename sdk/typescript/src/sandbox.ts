import { ExecutionResult, FileEntry, ProcessInfo, SandboxCreateOptions } from './types';

export class Commands {
  constructor(private readonly sandbox: Sandbox) {}

  async run(command: string, options?: { timeout?: number; user?: string }): Promise<ExecutionResult> {
    const payload = {
      command,
      timeout: options?.timeout ?? 60,
      user: options?.user,
    };
    const res = await this.sandbox.request('POST', `/api/v1/sandboxes/${this.sandbox.id}/exec`, payload);
    const data = await res.json();
    return {
      stdout: data.stdout || '',
      stderr: data.stderr || '',
      exitCode: data.exit_code ?? 0,
      durationMs: data.duration_ms ?? 0,
    };
  }
}

export class Filesystem {
  constructor(private readonly sandbox: Sandbox) {}

  async write(path: string, content: string, format: 'text' | 'base64' = 'text'): Promise<void> {
    await this.sandbox.request('POST', `/api/v1/sandboxes/${this.sandbox.id}/files/write`, {
      path,
      content,
      format,
    });
  }

  async read(path: string, format: 'text' | 'base64' = 'text'): Promise<string> {
    const q = new URLSearchParams({ path, format });
    const res = await this.sandbox.request('GET', `/api/v1/sandboxes/${this.sandbox.id}/files/read?${q.toString()}`);
    const data = await res.json();
    return data.content || '';
  }

  async list(path: string = '/workspace'): Promise<FileEntry[]> {
    const q = new URLSearchParams({ path });
    const res = await this.sandbox.request('GET', `/api/v1/sandboxes/${this.sandbox.id}/files/list?${q.toString()}`);
    const data = await res.json();
    return (data.entries || []).map((e: any) => ({
      name: e.name,
      path: e.path,
      isDir: e.is_dir,
      size: e.size,
    }));
  }

  async remove(path: string): Promise<void> {
    const q = new URLSearchParams({ path });
    await this.sandbox.request('DELETE', `/api/v1/sandboxes/${this.sandbox.id}/files?${q.toString()}`);
  }

  async mkdir(path: string): Promise<void> {
    await this.sandbox.request('POST', `/api/v1/sandboxes/${this.sandbox.id}/files/mkdir`, { path });
  }
}

export class ProcessManager {
  constructor(private readonly sandbox: Sandbox) {}

  async list(): Promise<ProcessInfo[]> {
    const res = await this.sandbox.request('GET', `/api/v1/sandboxes/${this.sandbox.id}/processes`);
    const data = await res.json();
    return data.processes || [];
  }

  async start(command: string, options?: { cwd?: string; env?: Record<string, string> }): Promise<number> {
    const res = await this.sandbox.request('POST', `/api/v1/sandboxes/${this.sandbox.id}/processes`, {
      command,
      cwd: options?.cwd,
      env: options?.env,
    });
    const data = await res.json();
    return data.pid || 0;
  }

  async kill(pid: number, signal: string = 'SIGTERM'): Promise<void> {
    await this.sandbox.request('POST', `/api/v1/sandboxes/${this.sandbox.id}/processes/kill`, {
      pid,
      signal,
    });
  }
}

export class Sandbox {
  public readonly id: string;
  public readonly commands: Commands;
  public readonly files: Filesystem;
  public readonly process: ProcessManager;

  private readonly host: string;
  private readonly apiKey?: string;

  constructor(id: string, host: string, apiKey?: string) {
    this.id = id;
    this.host = host.replace(/\/$/, '');
    this.apiKey = apiKey;
    this.commands = new Commands(this);
    this.files = new Filesystem(this);
    this.process = new ProcessManager(this);
  }

  static async create(options: SandboxCreateOptions = {}): Promise<Sandbox> {
    const host = options.host || (typeof process !== 'undefined' ? process.env.COMPUTE_HOST || process.env.COMPUTE_DAEMON : '') || 'http://127.0.0.1:18080';
    const normalizedHost = host.startsWith('http') ? host : `http://${host}`;
    const apiKey = options.apiKey || (typeof process !== 'undefined' ? process.env.COMPUTE_API_KEY : undefined);

    const headers: Record<string, string> = { 'Content-Type': 'application/json' };
    if (apiKey) {
      headers['Authorization'] = `Bearer ${apiKey}`;
      headers['X-API-Key'] = apiKey;
    }

    // 1. Create sandbox
    const createRes = await fetch(`${normalizedHost}/api/v1/sandboxes`, {
      method: 'POST',
      headers,
      body: JSON.stringify({
        template: options.template,
        tier: options.tier,
        vcpus: options.vcpus,
        memory_mb: options.memoryMb,
        disk_mb: options.diskMb,
        name: options.name,
      }),
    });

    if (!createRes.ok) {
      const errText = await createRes.text();
      throw new Error(`Failed to create sandbox (${createRes.status}): ${errText}`);
    }

    const data = await createRes.json();
    const sandboxId = data.id;

    // 2. Run sandbox
    const runRes = await fetch(`${normalizedHost}/api/v1/sandboxes/${sandboxId}/run`, {
      method: 'POST',
      headers,
    });

    if (!runRes.ok) {
      const errText = await runRes.text();
      throw new Error(`Failed to start sandbox (${runRes.status}): ${errText}`);
    }

    return new Sandbox(sandboxId, normalizedHost, apiKey);
  }

  static connect(id: string, options: { host?: string; apiKey?: string } = {}): Sandbox {
    const host = options.host || (typeof process !== 'undefined' ? process.env.COMPUTE_HOST || process.env.COMPUTE_DAEMON : '') || 'http://127.0.0.1:18080';
    const normalizedHost = host.startsWith('http') ? host : `http://${host}`;
    const apiKey = options.apiKey || (typeof process !== 'undefined' ? process.env.COMPUTE_API_KEY : undefined);
    return new Sandbox(id, normalizedHost, apiKey);
  }

  getPreviewURL(port: number): string {
    return `${this.host}/preview/${this.id}/${port}/`;
  }

  async kill(): Promise<void> {
    await this.request('DELETE', `/api/v1/sandboxes/${this.id}`);
  }

  async request(method: string, path: string, body?: any): Promise<Response> {
    const headers: Record<string, string> = {};
    if (this.apiKey) {
      headers['Authorization'] = `Bearer ${this.apiKey}`;
      headers['X-API-Key'] = this.apiKey;
    }
    if (body) {
      headers['Content-Type'] = 'application/json';
    }

    const res = await fetch(`${this.host}${path}`, {
      method,
      headers,
      body: body ? JSON.stringify(body) : undefined,
    });

    if (!res.ok) {
      const err = await res.text();
      throw new Error(`Compute API Error (${res.status}): ${err}`);
    }

    return res;
  }
}
