import type {
  BuildImageRequest,
  CreateTemplateRequest,
  CreateVMRequest,
  CustomImage,
  DaemonConfig,
  ExecRequest,
  ExecResponse,
  HealthResult,
  LaunchTemplateRequest,
  NetworkAllocation,
  NetworkConfig,
  NetworkInspectResult,
  QueueResult,
  SSHKey,
  StatsResult,
  Template,
  VM,
  VMTier,
} from './types';

const BASE_PATH = '/api/v1';

export class ApiError extends Error {
  status: number;

  constructor(message: string, status: number) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  const response = await fetch(`${BASE_PATH}${path}`, {
    ...options,
    headers: {
      'Content-Type': 'application/json',
      ...options.headers,
    },
  });

  if (!response.ok) {
    let message = `Request failed (${response.status})`;
    try {
      const body = (await response.json()) as { error?: string };
      if (body.error) message = body.error;
    } catch {
      // Keep the status-based message when the body is not JSON.
    }
    throw new ApiError(message, response.status);
  }

  return (await response.json()) as T;
}

export const api = {
  async health(): Promise<HealthResult> {
    const started = performance.now();
    const data = await request<{ status: string }>('/health');
    return {
      status: data.status,
      latencyMs: Math.max(1, Math.round(performance.now() - started)),
    };
  },

  stats(): Promise<StatsResult> {
    return request<StatsResult>('/stats');
  },

  tiers(): Promise<VMTier[]> {
    return request<VMTier[]>('/tiers');
  },

  queue(): Promise<QueueResult> {
    return request<QueueResult>('/queue');
  },

  config(): Promise<DaemonConfig> {
    return request<DaemonConfig>('/config');
  },

  listVMs(): Promise<VM[]> {
    return request<VM[]>('/vms');
  },

  getVM(id: string): Promise<VM> {
    return request<VM>(`/vms/${encodeURIComponent(id)}`);
  },

  createVM(payload: CreateVMRequest): Promise<VM> {
    return request<VM>('/vms', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },

  cloneVM(id: string, payload: CreateVMRequest): Promise<VM> {
    return request<VM>(`/vms/${encodeURIComponent(id)}/clone`, {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },

  runVM(id: string): Promise<VM> {
    return request<VM>(`/vms/${encodeURIComponent(id)}/run`, { method: 'POST' });
  },

  stopVM(id: string): Promise<VM> {
    return request<VM>(`/vms/${encodeURIComponent(id)}/stop`, { method: 'POST' });
  },

  snapshotVM(id: string): Promise<VM> {
    return request<VM>(`/vms/${encodeURIComponent(id)}/save-state`, {
      method: 'POST',
    });
  },

  resumeVM(id: string): Promise<VM> {
    return request<VM>(`/vms/${encodeURIComponent(id)}/resume`, { method: 'POST' });
  },

  deleteVM(id: string): Promise<{ status: string; id: string }> {
    return request<{ status: string; id: string }>(`/vms/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    });
  },

  execVM(id: string, payload: ExecRequest): Promise<ExecResponse> {
    return request<ExecResponse>(`/vms/${encodeURIComponent(id)}/exec`, {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },

  networkAllocations(): Promise<NetworkAllocation[]> {
    return request<NetworkAllocation[]>('/network/allocations');
  },

  inspectNetwork(vmID: string): Promise<NetworkInspectResult> {
    return request<NetworkInspectResult>(`/network/inspect/${encodeURIComponent(vmID)}`);
  },

  pruneNetwork(): Promise<{ status: string; pruned_taps: string[] }> {
    return request<{ status: string; pruned_taps: string[] }>('/network/prune', {
      method: 'POST',
    });
  },

  networkConfig(): Promise<NetworkConfig> {
    return request<NetworkConfig>('/network/config');
  },

  setVMPublicIP(id: string, enabled: boolean): Promise<VM> {
    return request<VM>(`/vms/${encodeURIComponent(id)}/public-ip`, {
      method: 'POST',
      body: JSON.stringify({ public_ip_enabled: enabled }),
    });
  },

  listImages(): Promise<CustomImage[]> {
    return request<CustomImage[]>('/images');
  },

  getImage(id: string): Promise<CustomImage> {
    return request<CustomImage>(`/images/${encodeURIComponent(id)}`);
  },

  buildImage(payload: BuildImageRequest): Promise<CustomImage> {
    return request<CustomImage>('/images', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },

  deleteImage(id: string): Promise<{ status: string; id: string }> {
    return request<{ status: string; id: string }>(`/images/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    });
  },

  listTemplates(): Promise<Template[]> {
    return request<Template[]>('/templates');
  },

  getTemplate(id: string): Promise<Template> {
    return request<Template>(`/templates/${encodeURIComponent(id)}`);
  },

  createTemplate(payload: CreateTemplateRequest): Promise<Template> {
    return request<Template>('/templates', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },

  deleteTemplate(id: string): Promise<{ status: string; id: string }> {
    return request<{ status: string; id: string }>(`/templates/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    });
  },

  launchTemplate(id: string, payload: LaunchTemplateRequest): Promise<VM> {
    return request<VM>(`/templates/${encodeURIComponent(id)}/launch`, {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },
  async listKeys(): Promise<SSHKey[]> {
    const res = await request<{ keys: SSHKey[] } | SSHKey[]>("/keys");
    if (Array.isArray(res)) return res;
    return (res as { keys: SSHKey[] }).keys || [];
  },

  createKey(payload: { name: string; public_key: string }): Promise<SSHKey> {
    return request<SSHKey>("/keys", {
      method: "POST",
      body: JSON.stringify(payload),
    });
  },

  getKey(id: string): Promise<SSHKey> {
    return request<SSHKey>(`/keys/${encodeURIComponent(id)}`);
  },

  deleteKey(id: string): Promise<{ status: string; id: string }> {
    return request<{ status: string; id: string }>(`/keys/${encodeURIComponent(id)}`, {
      method: "DELETE",
    });
  },
};

export function errorMessage(error: unknown): string {
  if (error instanceof Error && error.message) return error.message;
  return String(error);
}
