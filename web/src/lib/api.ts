import type {
  AccessAPIKeyRecord,
  AdminResourcesResponse,
  AuditEventRecord,
  BulkCloneVMsRequest,
  BulkCreateVMsRequest,
  BulkTaskRecord,
  BulkVMsRequest,
  BulkVMsResponse,
  CloneVMOptions,
  ClusterSchedulingPolicy,
  CreateCIRepoRequest,
  CreateTemplateRequest,
  CreateVMRequest,
  DaemonConfig,
  EnvironmentProfileRecord,
  FileEntry,
  FileReadResponse,
  GitHubConnectionRecord,
  HealthResult,
  ImageListItem,
  JobLogRecord,
  JobRecord,
  LaunchTemplateRequest,
  MigratePoolRequest,
  MigrateVMRequest,
  NetworkAllocation,
  NetworkConfig,
  NetworkInspectResult,
  NetworkIsolationMode,
  OTelConfigRecord,
  PoolMigrationResponse,
  PortForwardRecord,
  ProcessInfo,
  QueueResult,
  ResourceLimitErrorDetails,
  SSHKey,
  StatsResult,
  Template,
  TriggerCIRepoRequest,
  UpdateCIRepoRequest,
  UpdateWorkerRequest,
  VM,
  VMTier,
  VolumeRecord,
  WebhookEndpointRecord,
  WorkerNodeRecord,
  WorkerPoolRecord,
  WorkerTokenRecord,
  AuthMeResponse,
  AuthStatusResponse,
  AuthUser,
  CreateUserRequest,
  LoginResponse,
  SignupRequest,
} from './types';

const BASE_PATH = '/api/v1';
const AUTH_TOKEN_KEY = 'compute_auth_token';
const AUTH_USER_KEY = 'compute_auth_user';

export class ApiError extends Error {
  status: number;
  code?: string;
  details?: ResourceLimitErrorDetails;

  constructor(
    message: string,
    status: number,
    code?: string,
    details?: ResourceLimitErrorDetails,
  ) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.details = details;
  }
}

type UnauthorizedListener = () => void;
const unauthorizedListeners: Set<UnauthorizedListener> = new Set();

export function onUnauthorized(fn: UnauthorizedListener): () => void {
  unauthorizedListeners.add(fn);
  return () => {
    unauthorizedListeners.delete(fn);
  };
}

function notifyUnauthorized() {
  for (const listener of unauthorizedListeners) {
    try {
      listener();
    } catch {
      // Ignore listener errors
    }
  }
}

export function getStoredToken(): string | null {
  try {
    return window.localStorage.getItem(AUTH_TOKEN_KEY);
  } catch {
    return null;
  }
}

export function setStoredToken(token: string | null) {
  try {
    if (token) {
      window.localStorage.setItem(AUTH_TOKEN_KEY, token);
    } else {
      window.localStorage.removeItem(AUTH_TOKEN_KEY);
    }
  } catch {
    // Ignore storage errors
  }
}

export function getStoredUser(): AuthUser | null {
  try {
    const raw = window.localStorage.getItem(AUTH_USER_KEY);
    return raw ? (JSON.parse(raw) as AuthUser) : null;
  } catch {
    return null;
  }
}

export function setStoredUser(user: AuthUser | null) {
  try {
    if (user) {
      window.localStorage.setItem(AUTH_USER_KEY, JSON.stringify(user));
    } else {
      window.localStorage.removeItem(AUTH_USER_KEY);
    }
  } catch {
    // Ignore storage errors
  }
}

export function clearAuth() {
  setStoredToken(null);
  setStoredUser(null);
}

async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  const token = getStoredToken();
  const authHeaders: Record<string, string> = {};
  if (token) {
    authHeaders['Authorization'] = `Bearer ${token}`;
  }

  const method = (options.method || 'GET').toUpperCase();
  const canKeepalive =
    method !== 'GET' &&
    method !== 'HEAD' &&
    typeof options.body === 'string' &&
    options.body.length < 60000;

  const response = await fetch(`${BASE_PATH}${path}`, {
    credentials: 'include',
    keepalive: options.keepalive ?? canKeepalive,
    ...options,
    headers: {
      'Content-Type': 'application/json',
      ...authHeaders,
      ...options.headers,
    },
  });

  if (!response.ok) {
    let message = `Request failed (${response.status})`;
    let errorCode: string | undefined;
    let details: ResourceLimitErrorDetails | undefined;
    try {
      const body = (await response.json()) as {
        error?: string;
        message?: string;
        code?: string;
        resource?: string;
        limit_type?: string;
        node_id?: string;
        requested?: number;
        available?: number;
        limit?: number;
        suggestion?: string;
      };
      if (body.code) {
        errorCode = body.code;
      }
      if (body.message) {
        message = body.message;
      } else if (body.error) {
        message = body.error;
      }
      if (
        body.code === 'RESOURCE_LIMIT_EXCEEDED' ||
        body.resource ||
        body.suggestion ||
        body.limit_type
      ) {
        details = {
          code: body.code,
          resource: body.resource,
          limit_type: body.limit_type,
          node_id: body.node_id,
          requested: body.requested,
          available: body.available,
          limit: body.limit,
          error: body.error || body.message,
          suggestion: body.suggestion,
        };
      }
    } catch {
      // Keep the status-based message when the body is not JSON.
    }

    if (
      response.status === 401 &&
      !path.startsWith('/auth/login') &&
      !path.startsWith('/auth/signup') &&
      !path.startsWith('/auth/me') &&
      !path.startsWith('/auth/status')
    ) {
      notifyUnauthorized();
    }

    throw new ApiError(message, response.status, errorCode, details);
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

  bulkCreateVMs(payload: BulkCreateVMsRequest): Promise<BulkVMsResponse> {
    return request<BulkVMsResponse>('/vms/bulk-create', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },

  cloneVM(id: string, nameOrOptions: string | CloneVMOptions | CreateVMRequest): Promise<VM> {
    const payload =
      typeof nameOrOptions === 'string' ? { name: nameOrOptions } : nameOrOptions;
    return request<VM>(`/vms/${encodeURIComponent(id)}/clone`, {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },

  bulkCloneVMs(payload: BulkCloneVMsRequest): Promise<BulkVMsResponse> {
    return request<BulkVMsResponse>('/vms/bulk-clone', {
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

  restartVM(id: string): Promise<VM> {
    return request<VM>(`/vms/${encodeURIComponent(id)}/restart`, { method: 'POST' });
  },

  snapshotVM(id: string): Promise<VM> {
    return request<VM>(`/vms/${encodeURIComponent(id)}/save-state`, {
      method: 'POST',
    });
  },

  pauseVM(id: string): Promise<VM> {
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

  bulkDeleteVMs(
    ids: string[],
    opts?: { async?: boolean },
  ): Promise<BulkVMsResponse> {
    return request<BulkVMsResponse>('/vms/bulk-delete', {
      method: 'POST',
      body: JSON.stringify({ ids, async: opts?.async ?? false }),
    });
  },

  bulkActionVMs(payload: BulkVMsRequest): Promise<BulkVMsResponse> {
    return request<BulkVMsResponse>('/vms/bulk-action', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },

  /** Resolve a queued bulk task's final outcome. */
  getBulkTask(taskID: string): Promise<BulkTaskRecord> {
    return request<BulkTaskRecord>(`/tasks/${encodeURIComponent(taskID)}`);
  },

  migrateVM(id: string, payload: MigrateVMRequest): Promise<VM> {
    return request<VM>(`/vms/${encodeURIComponent(id)}/migrate`, {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },

  updateVMConfig(
    id: string,
    payload: {
      public_ip?: boolean;
      protected?: boolean;
      network_isolation?: NetworkIsolationMode;
      isolated?: boolean;
    },
  ): Promise<VM> {
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

  setVMNetworkIsolation(id: string, mode: NetworkIsolationMode): Promise<VM> {
    return request<VM>(`/vms/${encodeURIComponent(id)}/network-isolation`, {
      method: 'POST',
      body: JSON.stringify({ network_isolation: mode }),
    });
  },

  listPortForwards(): Promise<PortForwardRecord[]> {
    return request<PortForwardRecord[]>('/network/port-forwards');
  },

  createPortForward(payload: {
    host_port?: number;
    vm_id: string;
    vm_name?: string;
    guest_port: number;
    protocol?: string;
    subdomain?: string;
    public?: boolean;
  }): Promise<PortForwardRecord> {
    return request<PortForwardRecord>('/network/port-forwards', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },

  setPortForwardPublic(
    id: string,
    isPublic: boolean,
  ): Promise<PortForwardRecord> {
    return request<PortForwardRecord>(
      `/network/port-forwards/${encodeURIComponent(id)}`,
      {
        method: 'PATCH',
        body: JSON.stringify({ public: isPublic }),
      },
    );
  },

  deletePortForward(id: string): Promise<{ status: string }> {
    return request<{ status: string }>(`/network/port-forwards/${encodeURIComponent(id)}`, {
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

  createSnapshot(vmId: string, name: string, description = ''): Promise<Template> {
    return request<Template>('/templates', {
      method: 'POST',
      body: JSON.stringify({
        vm_id: vmId,
        name,
        description,
      }),
    });
  },

  restoreSnapshot(
    snapshotId: string,
    name: string,
    options: Omit<LaunchTemplateRequest, 'name'> = {},
  ): Promise<VM> {
    return request<VM>(`/templates/${encodeURIComponent(snapshotId)}/launch`, {
      method: 'POST',
      body: JSON.stringify({
        name,
        ...options,
      }),
    });
  },

  async listKeys(): Promise<SSHKey[]> {
    const res = await request<{ keys: SSHKey[] } | SSHKey[]>('/keys');
    if (Array.isArray(res)) return res;
    return (res as { keys: SSHKey[] }).keys || [];
  },

  createKey(payload: { name: string; public_key: string; all_sandboxes?: boolean }): Promise<SSHKey> {
    return request<SSHKey>('/keys', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },

  updateKey(
    id: string,
    payload: { name?: string; public_key?: string; all_sandboxes?: boolean; is_default?: boolean },
  ): Promise<SSHKey> {
    return request<SSHKey>(`/keys/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: JSON.stringify(payload),
    });
  },

  applyKey(
    id: string,
  ): Promise<{ key: SSHKey; sync?: { applied: string[]; failed: { vm_id: string; name?: string; error: string }[] } }> {
    return request(`/keys/${encodeURIComponent(id)}/apply`, {
      method: 'POST',
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

  updateWorker(id: string, payload: UpdateWorkerRequest): Promise<WorkerNodeRecord> {
    return request<WorkerNodeRecord>(`/workers/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: JSON.stringify(payload),
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

  migratePool(id: string, payload: MigratePoolRequest): Promise<PoolMigrationResponse> {
    return request<PoolMigrationResponse>(`/pools/${encodeURIComponent(id)}/migrate`, {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },

  getSchedulingPolicy(): Promise<ClusterSchedulingPolicy> {
    return request<ClusterSchedulingPolicy>('/scheduler/policy');
  },

  updateSchedulingPolicy(
    payload: Partial<ClusterSchedulingPolicy>,
  ): Promise<ClusterSchedulingPolicy> {
    return request<ClusterSchedulingPolicy>('/scheduler/policy', {
      method: 'PUT',
      body: JSON.stringify(payload),
    });
  },

  listImages(): Promise<ImageListItem[]> {
    return request<ImageListItem[]>('/images');
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
    repo_id?: string;
    source?: string;
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

  // CI Connected GitHub Repositories
  listCIRepos(): Promise<GitHubConnectionRecord[]> {
    return request<GitHubConnectionRecord[]>('/ci/repos');
  },

  createCIRepo(payload: CreateCIRepoRequest): Promise<GitHubConnectionRecord> {
    return request<GitHubConnectionRecord>('/ci/repos', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },

  updateCIRepo(id: string, payload: UpdateCIRepoRequest): Promise<GitHubConnectionRecord> {
    return request<GitHubConnectionRecord>(`/ci/repos/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: JSON.stringify(payload),
    });
  },

  deleteCIRepo(id: string): Promise<{ status: string; id: string }> {
    return request<{ status: string; id: string }>(`/ci/repos/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    });
  },

  triggerCIRepo(id: string, payload: TriggerCIRepoRequest = {}): Promise<JobRecord> {
    return request<JobRecord>(`/ci/repos/${encodeURIComponent(id)}/trigger`, {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },

  // Admin Host & Platform Resource Telemetry
  adminResources(): Promise<AdminResourcesResponse> {
    return request<AdminResourcesResponse>('/admin/resources');
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

  // API Keys
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

  // Authentication & Identity
  authStatus(): Promise<AuthStatusResponse> {
    return request<AuthStatusResponse>('/auth/status');
  },

  async login(username: string, password: string): Promise<LoginResponse> {
    const res = await request<LoginResponse>('/auth/login', {
      method: 'POST',
      body: JSON.stringify({ username, password }),
    });
    if (res.token) {
      setStoredToken(res.token);
    }
    if (res.user) {
      setStoredUser(res.user);
    }
    return res;
  },

  async signup(payload: SignupRequest): Promise<LoginResponse> {
    const res = await request<LoginResponse>('/auth/signup', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
    if (res.token) {
      setStoredToken(res.token);
    }
    if (res.user) {
      setStoredUser(res.user);
    }
    return res;
  },

  async logout(): Promise<void> {
    try {
      await request<{ status: string }>('/auth/logout', { method: 'POST' });
    } catch {
      // Ignore network errors on logout
    } finally {
      clearAuth();
    }
  },

  async me(): Promise<AuthMeResponse> {
    const res = await request<AuthMeResponse>('/auth/me');
    if (res.user) {
      setStoredUser(res.user);
    }
    return res;
  },

  listUsers(): Promise<AuthUser[]> {
    return request<AuthUser[]>('/users');
  },

  createUser(payload: CreateUserRequest): Promise<{ status: string; user: AuthUser }> {
    return request<{ status: string; user: AuthUser }>('/users', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },

  deleteUser(id: string): Promise<{ status: string; id: string }> {
    return request<{ status: string; id: string }>(`/users/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    });
  },

  getAuthToken(): string | null {
    return getStoredToken();
  },

  getAuthUser(): AuthUser | null {
    return getStoredUser();
  },

  setAuthToken(token: string | null): void {
    setStoredToken(token);
  },

  clearAuth(): void {
    clearAuth();
  },
};


export function errorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    const base = error.message || `Request failed (${error.status})`;
    const d = error.details;
    if (d) {
      const parts: string[] = [base];
      if (d.node_id && !base.includes(d.node_id)) {
        parts.push(`[node: ${d.node_id}]`);
      }
      if (
        d.resource &&
        d.requested !== undefined &&
        d.available !== undefined &&
        !base.includes(String(d.requested))
      ) {
        parts.push(
          `(${d.resource}: requested ${d.requested}, available ${d.available}${
            d.limit !== undefined ? `, limit ${d.limit}` : ''
          })`,
        );
      }
      if (d.suggestion && !base.includes(d.suggestion)) {
        parts.push(`— ${d.suggestion}`);
      }
      return parts.join(' ');
    }
    return base;
  }
  if (error instanceof Error && error.message) return error.message;
  return String(error);
}
