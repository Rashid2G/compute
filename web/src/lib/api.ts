import type {
  AccessAPIKeyRecord,
  AgentProfileRecord,
  AuditEventRecord,
  BuildImageRequest,
  CreateTemplateRequest,
  CreateVMRequest,
  CustomImage,
  DaemonConfig,
  EnvironmentProfileRecord,
  ExecRequest,
  ExecResponse,
  FileEntry,
  FileReadResponse,
  HealthResult,
  JobLogRecord,
  JobRecord,
  LaunchTemplateRequest,
  NetworkAllocation,
  NetworkConfig,
  NetworkInspectResult,
  OTelConfigRecord,
  PortForwardRecord,
  ProcessInfo,
  QueueResult,
  SSHKey,
  StatsResult,
  Template,
  VM,
  VMTier,
  VolumeRecord,
  WebhookEndpointRecord,
  WorkerNodeRecord,
  WorkerPoolRecord,
  WorkerTokenRecord,
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

  if (response.status === 204) {
    return {} as T;
  }

  return (await response.json()) as T;
}

export const api = {
  async health(): Promise<HealthResult> {
    const started = performance.now();
    const data = await request<{ status: string; version?: string; name?: string }>('/health');
    return {
      status: data.status,
      version: data.version,
      name: data.name,
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

  updateVMConfig(id: string, payload: { public_ip?: boolean; protected?: boolean }): Promise<VM> {
    return request<VM>(`/vms/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: JSON.stringify(payload),
    });
  },

  protectVM(id: string): Promise<VM> {
    return request<VM>(`/vms/${encodeURIComponent(id)}/protect`, { method: 'POST' });
  },

  unprotectVM(id: string): Promise<VM> {
    return request<VM>(`/vms/${encodeURIComponent(id)}/unprotect`, { method: 'POST' });
  },

  execVM(id: string, payload: ExecRequest): Promise<ExecResponse> {
    return request<ExecResponse>(`/vms/${encodeURIComponent(id)}/exec`, {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },

  async listVMFiles(id: string, path = '/'): Promise<FileEntry[]> {
    const res = await request<{ entries: FileEntry[]; path: string }>(
      `/vms/${encodeURIComponent(id)}/files/list?path=${encodeURIComponent(path)}`,
    );
    return res.entries || [];
  },

  readVMFile(id: string, path: string): Promise<FileReadResponse> {
    return request<FileReadResponse>(
      `/vms/${encodeURIComponent(id)}/files/read?path=${encodeURIComponent(path)}`,
    );
  },

  writeVMFile(
    id: string,
    payload: { path: string; content: string; format?: string },
  ): Promise<{ status: string; path: string; size: number }> {
    return request<{ status: string; path: string; size: number }>(
      `/vms/${encodeURIComponent(id)}/files/write`,
      {
        method: 'POST',
        body: JSON.stringify(payload),
      },
    );
  },

  async listVMProcesses(id: string): Promise<ProcessInfo[]> {
    const res = await request<{ processes: ProcessInfo[] }>(
      `/vms/${encodeURIComponent(id)}/processes`,
    );
    return res.processes || [];
  },

  killVMProcess(id: string, pid: number, signal = 'SIGTERM'): Promise<{ status: string }> {
    return request<{ status: string }>(`/vms/${encodeURIComponent(id)}/processes/kill`, {
      method: 'POST',
      body: JSON.stringify({ pid, signal }),
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

  listPortForwards(): Promise<PortForwardRecord[]> {
    return request<PortForwardRecord[]>('/network/port-forwards');
  },

  createPortForward(payload: {
    host_port: number;
    vm_id: string;
    vm_name?: string;
    guest_port: number;
    protocol?: string;
    subdomain?: string;
  }): Promise<PortForwardRecord> {
    return request<PortForwardRecord>('/network/port-forwards', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },

  deletePortForward(id: string): Promise<{ status: string }> {
    return request<{ status: string }>(`/network/port-forwards/${encodeURIComponent(id)}`, {
      method: 'DELETE',
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
    const res = await request<{ keys: SSHKey[] } | SSHKey[]>('/keys');
    if (Array.isArray(res)) return res;
    return (res as { keys: SSHKey[] }).keys || [];
  },

  createKey(payload: { name: string; public_key: string }): Promise<SSHKey> {
    return request<SSHKey>('/keys', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },

  getKey(id: string): Promise<SSHKey> {
    return request<SSHKey>(`/keys/${encodeURIComponent(id)}`);
  },

  deleteKey(id: string): Promise<{ status: string; id: string }> {
    return request<{ status: string; id: string }>(`/keys/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    });
  },

  // Workers & Pools
  listWorkers(): Promise<WorkerNodeRecord[]> {
    return request<WorkerNodeRecord[]>('/workers');
  },

  cordonWorker(id: string): Promise<{ status: string }> {
    return request<{ status: string }>(`/workers/${encodeURIComponent(id)}/cordon`, {
      method: 'POST',
    });
  },

  uncordonWorker(id: string): Promise<{ status: string }> {
    return request<{ status: string }>(`/workers/${encodeURIComponent(id)}/uncordon`, {
      method: 'POST',
    });
  },

  drainWorker(id: string): Promise<{ status: string }> {
    return request<{ status: string }>(`/workers/${encodeURIComponent(id)}/drain`, {
      method: 'POST',
    });
  },

  reconcileWorker(id: string): Promise<{ status: string }> {
    return request<{ status: string }>(`/workers/${encodeURIComponent(id)}/reconcile`, {
      method: 'POST',
    });
  },

  reconcileAllWorkers(): Promise<{ status: string; count?: number }> {
    return request<{ status: string; count?: number }>('/workers/reconcile', {
      method: 'POST',
    });
  },

  deleteWorker(id: string): Promise<void> {
    return request<void>(`/workers/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    });
  },

  listPools(): Promise<WorkerPoolRecord[]> {
    return request<WorkerPoolRecord[]>('/pools');
  },

  createPool(payload: { name: string }): Promise<WorkerPoolRecord> {
    return request<WorkerPoolRecord>('/pools', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },

  listWorkerTokens(): Promise<WorkerTokenRecord[]> {
    return request<WorkerTokenRecord[]>('/worker-tokens');
  },

  createWorkerToken(payload: { name?: string; pool_id?: string }): Promise<WorkerTokenRecord> {
    return request<WorkerTokenRecord>('/worker-tokens', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },

  deleteWorkerToken(id: string): Promise<{ status: string }> {
    return request<{ status: string }>(`/worker-tokens/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    });
  },

  // Jobs & Ephemeral Runners
  listJobs(): Promise<JobRecord[]> {
    return request<JobRecord[]>('/jobs');
  },

  createJob(payload: {
    type?: string;
    pool_id?: string;
    priority?: number;
    spec: Record<string, unknown>;
  }): Promise<JobRecord> {
    return request<JobRecord>('/jobs', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },

  cancelJob(id: string): Promise<{ status: string }> {
    return request<{ status: string }>(`/jobs/${encodeURIComponent(id)}/cancel`, {
      method: 'POST',
    });
  },

  getJobLogs(id: string): Promise<JobLogRecord[]> {
    return request<JobLogRecord[]>(`/jobs/${encodeURIComponent(id)}/logs`);
  },

  // Persistent Storage Volumes
  listVolumes(): Promise<VolumeRecord[]> {
    return request<VolumeRecord[]>('/volumes');
  },

  createVolume(payload: {
    name: string;
    pool?: string;
    size_gb: number;
    filesystem?: string;
    attached_vm?: string;
  }): Promise<VolumeRecord> {
    return request<VolumeRecord>('/volumes', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },

  deleteVolume(id: string): Promise<{ status: string }> {
    return request<{ status: string }>(`/volumes/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    });
  },

  attachVolume(id: string, vm_id: string): Promise<VolumeRecord> {
    return request<VolumeRecord>(`/volumes/${encodeURIComponent(id)}/attach`, {
      method: 'POST',
      body: JSON.stringify({ vm_id }),
    });
  },

  detachVolume(id: string): Promise<VolumeRecord> {
    return request<VolumeRecord>(`/volumes/${encodeURIComponent(id)}/detach`, {
      method: 'POST',
    });
  },

  expandVolume(id: string, add_gb = 10): Promise<VolumeRecord> {
    return request<VolumeRecord>(`/volumes/${encodeURIComponent(id)}/expand`, {
      method: 'POST',
      body: JSON.stringify({ add_gb }),
    });
  },

  snapshotVolume(id: string): Promise<VolumeRecord> {
    return request<VolumeRecord>(`/volumes/${encodeURIComponent(id)}/snapshot`, {
      method: 'POST',
    });
  },

  // Webhooks, OTel & Audit Events
  listWebhooks(): Promise<WebhookEndpointRecord[]> {
    return request<WebhookEndpointRecord[]>('/webhooks');
  },

  createWebhook(payload: {
    url: string;
    description?: string;
    secret?: string;
    events: string[];
  }): Promise<WebhookEndpointRecord> {
    return request<WebhookEndpointRecord>('/webhooks', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },

  updateWebhook(
    id: string,
    payload: Partial<WebhookEndpointRecord>,
  ): Promise<WebhookEndpointRecord> {
    return request<WebhookEndpointRecord>(`/webhooks/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: JSON.stringify(payload),
    });
  },

  deleteWebhook(id: string): Promise<{ status: string }> {
    return request<{ status: string }>(`/webhooks/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    });
  },

  testWebhook(id: string): Promise<{ status: string; code: number }> {
    return request<{ status: string; code: number }>(`/webhooks/${encodeURIComponent(id)}/test`, {
      method: 'POST',
    });
  },

  getOTelConfig(): Promise<OTelConfigRecord> {
    return request<OTelConfigRecord>('/otel');
  },

  saveOTelConfig(payload: Partial<OTelConfigRecord>): Promise<OTelConfigRecord> {
    return request<OTelConfigRecord>('/otel', {
      method: 'PUT',
      body: JSON.stringify(payload),
    });
  },

  testOTelExport(): Promise<OTelConfigRecord> {
    return request<OTelConfigRecord>('/otel/test', {
      method: 'POST',
    });
  },

  listEvents(): Promise<AuditEventRecord[]> {
    return request<AuditEventRecord[]>('/events');
  },

  // API Keys & Agent Proxying
  listAPIKeys(): Promise<AccessAPIKeyRecord[]> {
    return request<AccessAPIKeyRecord[]>('/api-keys');
  },

  createAPIKey(payload: {
    name: string;
    scope?: string;
    expires_at?: string;
  }): Promise<AccessAPIKeyRecord> {
    return request<AccessAPIKeyRecord>('/api-keys', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },

  deleteAPIKey(id: string): Promise<{ status: string }> {
    return request<{ status: string }>(`/api-keys/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    });
  },

  listAgentProfiles(): Promise<AgentProfileRecord[]> {
    return request<AgentProfileRecord[]>('/agent-profiles');
  },

  createAgentProfile(payload: {
    name: string;
    provider: string;
    upstream_url: string;
    inject_header: string;
    secret_ref: string;
  }): Promise<AgentProfileRecord> {
    return request<AgentProfileRecord>('/agent-profiles', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },

  updateAgentProfile(
    id: string,
    payload: Partial<AgentProfileRecord>,
  ): Promise<AgentProfileRecord> {
    return request<AgentProfileRecord>(`/agent-profiles/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: JSON.stringify(payload),
    });
  },

  deleteAgentProfile(id: string): Promise<{ status: string }> {
    return request<{ status: string }>(`/agent-profiles/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    });
  },

  // Environments
  listEnvironments(): Promise<EnvironmentProfileRecord[]> {
    return request<EnvironmentProfileRecord[]>('/environments');
  },

  saveEnvironment(payload: Partial<EnvironmentProfileRecord> & { name: string }): Promise<EnvironmentProfileRecord> {
    return request<EnvironmentProfileRecord>('/environments', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },

  deleteEnvironment(name: string): Promise<{ status: string }> {
    return request<{ status: string }>(`/environments/${encodeURIComponent(name)}`, {
      method: 'DELETE',
    });
  },
};

export function errorMessage(error: unknown): string {
  if (error instanceof Error && error.message) return error.message;
  return String(error);
}
