export type VMStatus =
  | 'created'
  | 'queued'
  | 'provisioning'
  | 'booting'
  | 'running_startup_script'
  | 'running_build_script'
  | 'running_build_command'
  | 'running'
  | 'paused'
  | 'snapshotting'
  | 'restoring'
  | 'migrating'
  | 'stopping'
  | 'stopped'
  | 'failed'
  | string;

export type NetworkIsolationMode = 'account' | 'isolated';

export interface HealthResult {
  status: string;
  version?: string;
  name?: string;
  latencyMs: number;
}

export interface VM {
  id: string;
  internal_id?: string;
  owner_id?: string;
  name: string;
  description?: string;
  status: VMStatus;
  vcpus: number;
  memory_mb: number;
  disk_mb?: number;
  ip_address: string;
  public_ip?: string;
  public_ip_enabled?: boolean;
  network_isolation?: NetworkIsolationMode;
  mac_address: string;
  tap_device: string;
  rootfs_path: string;
  kernel_path: string;
  disk_path: string;
  snapshot_path?: string;
  mem_file_path?: string;
  ssh_key_path?: string;
  ssh_key_name?: string;
  ssh_public_key?: string;
  ssh_password_enabled?: boolean;
  hostname?: string;
  pid?: number;
  socket_path?: string;
  protected?: boolean;
  startup_script?: string;
  pool_id?: string;
  worker_id?: string;
  last_activity_at?: string | null;
  auto_hibernated?: boolean;
  hibernate_reason?: string;
  created_at: string;
  updated_at: string;
}

export interface ResourceLimits {
  max_running_vms: number;
  max_total_vcpus: number;
  max_total_memory_mb: number;
  max_total_disk_mb: number;
  min_free_disk_space_mb: number;
}

export interface StatsResult {
  total_vms: number;
  running_vms: number;
  queued_vms: number;
  snapshotted_vms: number;
  stopped_vms: number;
  created_vms: number;
  total_vcpus: number;
  total_memory_mb: number;
  running_vcpus: number;
  running_memory_mb: number;
  allocated_ips: number;
  host_cpus?: number;
  host_memory_available_mb?: number;
  resource_limits?: ResourceLimits;
}

export interface VMTier {
  id: string;
  name: string;
  vcpus: number;
  default_ram_mb: number;
  disks_mb: number[];
  description: string;
}

export interface CreateVMRequest {
  name?: string;
  description?: string;
  tier?: string;
  vcpus: number;
  memory_mb?: number;
  disk_mb?: number;
  disk_gb?: number;
  clone_from?: string;
  image?: string;
  template?: string;
  pool_id?: string;
  worker_id?: string;
  ip_address?: string;
  mac_address?: string;
  public_ip?: boolean;
  public_ip_enabled?: boolean;
  dedicated_ip?: boolean;
  network_isolation?: NetworkIsolationMode;
  isolated?: boolean;
  network_isolated?: boolean;
  auto_start?: boolean;
  protected?: boolean;
  ssh_key_id?: string;
  ssh_public_key?: string;
  ssh_password?: string;
  /** Opts out of embedding the account's default key when no ssh_key_id is set. */
  ssh_no_default_key?: boolean;
  command?: string;
  build_command?: string;
  startup_script?: string;
  exec_user?: string;
}

export interface CloneVMOptions {
  name?: string;
  description?: string;
  vcpus?: number;
  memory_mb?: number;
  disk_mb?: number;
  disk_gb?: number;
  network_isolation?: NetworkIsolationMode;
  network_isolated?: boolean;
  auto_start?: boolean;
  protected?: boolean;
}

export interface FileEntry {
  name: string;
  path: string;
  is_dir: boolean;
  size: number;
}

export interface FileReadResponse {
  path: string;
  content: string;
  format: string;
  size: number;
}

export interface ProcessInfo {
  pid: number;
  ppid: number;
  user: string;
  cpu: number;
  memory: number;
  status: string;
  command: string;
  args: string;
}

export interface NetworkAllocation {
  vm_id: string;
  vm_name: string;
  status: VMStatus;
  ip_address: string;
  public_ip?: string;
  public_ip_enabled?: boolean;
  network_isolation?: NetworkIsolationMode;
  mac_address: string;
  tap_device: string;
  updated_at: string;
}

export interface NetworkInspectResult {
  vm_id: string;
  vm_name: string;
  status: string;
  ip_address: string;
  public_ip?: string;
  public_ip_enabled?: boolean;
  network_isolation?: NetworkIsolationMode;
  mac_address: string;
  tap_device: string;
  bridge_name: string;
  gateway: string;
  reachable: boolean;
  connectivity: string;
}

export interface NetworkConfig {
  mode: string;
  bridge_name: string;
  bridge_ip: string;
  subnet: string;
  subnet_cidr?: string;
  physical_nic?: string;
  gateway_ip?: string;
}

export interface AvailableImage {
  name: string;
  kernel: string;
  rootfs: string;
  description?: string;
}

export type ImageStatus = 'building' | 'ready' | 'failed';

export interface CustomImage {
  id: string;
  name: string;
  description?: string;
  base_image: string;
  packages: string[];
  disk_size_mb?: number;
  size_mb?: number;
  image_path?: string;
  rootfs_path?: string;
  kernel_path?: string;
  status: ImageStatus;
  error?: string;
  error_message?: string;
  created_at: string;
  updated_at: string;
}

export interface BuildImageRequest {
  name: string;
  description?: string;
  base_image?: string;
  packages: string[];
  disk_size_mb?: number;
}

export interface Template {
  id: string;
  name: string;
  description: string;
  source_vm_id: string;
  disk_path: string;
  disk_size_mb?: number;
  disk_mb?: number;
  vcpus: number;
  memory_mb: number;
  created_at: string;
  updated_at: string;
}

export interface CreateTemplateRequest {
  vm_id: string;
  name: string;
  description?: string;
}

export interface LaunchTemplateRequest {
  name: string;
  vcpus?: number;
  memory_mb?: number;
  network_isolation?: NetworkIsolationMode;
  isolated?: boolean;
  auto_start?: boolean;
}

export interface DaemonConfig {
  server: {
    listen: string;
    db_path: string;
    firecracker_bin: string;
  };
  network: NetworkConfig;
  defaults: {
    vcpus: number;
    memory_mb: number;
    disk_size_mb: number;
    image: string;
  };
  images: {
    images_dir: string;
    vms_dir: string;
    default_kernel: string;
    default_rootfs: string;
    ssh_key_path?: string;
    available?: AvailableImage[];
  };
  resources: ResourceLimits;
}

export interface QueueItem {
  position: number;
  vm_id: string;
  vm_name: string;
  vcpus: number;
  memory_mb: number;
  queued_at: string;
  reason: string;
}

export interface QueueResult {
  queued_vms: QueueItem[];
  count: number;
  running_vms: number;
  running_vcpus: number;
  running_mem_mb: number;
}

export interface SSHKey {
  id: string;
  owner_id: string;
  name: string;
  public_key: string;
  fingerprint: string;
  all_sandboxes?: boolean;
  is_default?: boolean;
  created_at: string;
}

export interface WorkerNodeRecord {
  id: string;
  name: string;
  pool_id: string;
  role: string;
  status: string;
  underlay_ip: string;
  arch: string;
  total_vcpus: number;
  total_mem_mb: number;
  total_disk_mb: number;
  allocated_vcpus?: number;
  allocated_mem_mb?: number;
  allocated_disk_mb?: number;
  physical_vcpus?: number;
  physical_mem_mb?: number;
  physical_disk_mb?: number;
  used_vcpus?: number;
  used_mem_mb?: number;
  used_disk_mb?: number;
  running_vms?: number;
  available_mem_mb?: number;
  available_disk_mb?: number;
  labels_json: string;
  capabilities_json: string;
  agent_version: string;
  fc_version: string;
  last_heartbeat?: string | null;
  created_at: string;
  updated_at: string;
}

export interface WorkerPoolRecord {
  id: string;
  name: string;
  labels_json: string;
  policy_json: string;
  created_at: string;
  updated_at: string;
}

export interface WorkerTokenRecord {
  id?: string;
  token: string;
  name?: string;
  prefix?: string;
  pool_id: string;
  labels_json?: string;
  expires_at: string | number;
  used_at?: string | null;
  created_at?: string;
}

export interface JobRecord {
  id: string;
  owner_id: string;
  pool_id: string;
  type: string;
  state: 'queued' | 'assigned' | 'running' | 'succeeded' | 'completed' | 'failed' | 'canceled' | 'lost' | string;
  priority: number;
  spec_json: string;
  result_json: string;
  repo_id?: string;
  repo_url?: string;
  repo_name?: string;
  branch?: string;
  commit_sha?: string;
  source?: 'github' | 'manual' | 'cli' | string;
  vm_id?: string;
  worker_id?: string;
  queued_at: string;
  started_at?: string | null;
  finished_at?: string | null;
  deadline_at?: string | null;
  created_at: string;
  updated_at: string;
}

export interface JobLogRecord {
  id: string;
  job_id: string;
  attempt_id: string;
  stream: string;
  seq: number;
  data: string;
  created_at: string;
}

export interface GitHubConnectionRecord {
  id: string;
  owner_id: string;
  name: string;
  repo_url: string;
  default_branch: string;
  token_masked?: string;
  has_token: boolean;
  webhook_secret?: string;
  webhook_secret_masked?: string;
  has_webhook_secret: boolean;
  default_command: string;
  image_id?: string;
  vcpus: number;
  memory_mb: number;
  timeout_sec?: number;
  timeout_seconds?: number;
  status?: string;
  auto_trigger?: boolean;
  last_triggered_at?: string | null;
  last_delivery_at?: string | null;
  created_at: string;
  updated_at?: string;
}

export interface CreateCIRepoRequest {
  name?: string;
  repo_url: string;
  default_branch?: string;
  github_token?: string;
  webhook_secret?: string;
  default_command?: string;
  vcpus?: number;
  memory_mb?: number;
  timeout_sec?: number;
}

export interface UpdateCIRepoRequest {
  name?: string;
  repo_url?: string;
  default_branch?: string;
  github_token?: string;
  webhook_secret?: string;
  default_command?: string;
  vcpus?: number;
  memory_mb?: number;
  timeout_sec?: number;
  status?: string;
}

export interface TriggerCIRepoRequest {
  branch?: string;
  commit_sha?: string;
  command?: string;
  source?: string;
  image_id?: string;
  pool_id?: string;
  vcpus?: number;
  memory_mb?: number;
  timeout_sec?: number;
  env?: Record<string, string>;
}

export interface AdminCPUMetrics {
  cores: number;
  model?: string;
  usage_percent: number;
  user_percent: number;
  system_percent: number;
  iowait_percent: number;
  idle_percent: number;
  load_1m: number;
  load_5m: number;
  load_15m: number;
}

export interface AdminMemoryMetrics {
  total_mb: number;
  used_mb: number;
  available_mb: number;
  free_mb: number;
  cached_mb: number;
  buffers_mb: number;
  used_percent: number;
  swap_total_mb: number;
  swap_used_mb: number;
}

export interface AdminDiskMount {
  label: string;
  path: string;
  total_gb: number;
  used_gb: number;
  available_gb: number;
  used_percent: number;
}

export interface AdminDiskSpaceMetrics {
  path: string;
  total_gb: number;
  used_gb: number;
  free_gb: number;
  available_gb: number;
  used_percent: number;
  mounts?: AdminDiskMount[];
}

export interface AdminDiskDeviceIO {
  device: string;
  read_ops: number;
  write_ops: number;
  read_bytes: number;
  write_bytes: number;
  read_mb: number;
  write_mb: number;
  io_in_progress: number;
}

export interface AdminDiskIOMetrics {
  read_ops: number;
  write_ops: number;
  read_bytes: number;
  write_bytes: number;
  read_mb: number;
  write_mb: number;
  read_mb_per_sec: number;
  write_mb_per_sec: number;
  read_iops: number;
  write_iops: number;
  io_in_progress: number;
  devices?: AdminDiskDeviceIO[];
}

export interface AdminNetworkMetrics {
  rx_bytes: number;
  tx_bytes: number;
  rx_packets: number;
  tx_packets: number;
  rx_mb: number;
  tx_mb: number;
  rx_mb_per_sec: number;
  tx_mb_per_sec: number;
  interfaces?: string[];
}

export interface AdminSystemMetrics {
  hostname: string;
  os: string;
  arch: string;
  kernel_version?: string;
  uptime_seconds: number;
  kvm_available: boolean;
}

export interface AdminSandboxAllocations {
  total: number;
  running: number;
  stopped: number;
  vcpus_allocated: number;
  running_vcpus: number;
  memory_mb_allocated: number;
  running_memory_mb: number;
  disk_mb_allocated: number;
  disk_gb_allocated: number;
}

export interface AdminJobAllocations {
  total: number;
  queued: number;
  running: number;
  succeeded: number;
  failed: number;
  vcpus_allocated: number;
  memory_mb_allocated: number;
  disk_mb_allocated: number;
  disk_gb_allocated: number;
}

export interface AdminVolumeAllocations {
  total: number;
  attached: number;
  available: number;
  disk_mb_allocated: number;
  disk_gb_allocated: number;
}

export interface AdminWorkerAllocations {
  total: number;
  ready: number;
  offline: number;
}

export interface AdminPlatformAllocations {
  sandboxes: AdminSandboxAllocations;
  ci_jobs: AdminJobAllocations;
  volumes: AdminVolumeAllocations;
  workers: AdminWorkerAllocations;
  limits: ResourceLimits;
  allocated_vcpus: number;
  running_vcpus: number;
  job_vcpus: number;
  max_total_vcpus: number;
  allocated_memory_mb: number;
  running_memory_mb: number;
  job_memory_mb: number;
  max_total_memory_mb: number;
  allocated_vm_disk_gb: number;
  allocated_volume_disk_gb: number;
  max_total_disk_gb: number;
}

export interface AdminResourcesResponse {
  timestamp: string;
  cpu: AdminCPUMetrics;
  memory: AdminMemoryMetrics;
  disk: AdminDiskSpaceMetrics;
  disk_io: AdminDiskIOMetrics;
  network: AdminNetworkMetrics;
  system: AdminSystemMetrics;
  allocations: AdminPlatformAllocations;
  capacity_warnings: string[];
}

export interface VolumeRecord {
  id: string;
  name: string;
  pool: string;
  size_gb: number;
  used_gb: number;
  status: 'attached' | 'available' | 'snapshotting' | string;
  attached_vm: string;
  device_path: string;
  mount_point: string;
  filesystem: 'ext4' | 'xfs' | 'btrfs' | string;
  iops: number;
  throughput: string;
  snapshots: number;
  created_at: string;
  updated_at: string;
}

export interface WebhookEndpointRecord {
  id: string;
  url: string;
  description: string;
  secret: string;
  events: string[];
  active: boolean;
  last_delivery: string;
  success_rate: string;
  deliveries_count: number;
  created_at: string;
  updated_at: string;
}

export interface OTelConfigRecord {
  enabled: boolean;
  endpoint: string;
  protocol: string;
  auth_header: string;
  service_name: string;
  sample_rate: string;
  export_interval: string;
  metrics_enabled: boolean;
  traces_enabled: boolean;
  logs_enabled: boolean;
  spans_exported: number;
  last_export_at: string;
}

export interface AuditEventRecord {
  id: string;
  timestamp: string;
  event: string;
  actor: string;
  target: string;
  status: 'delivered' | 'recorded' | 'failed' | 'retrying' | string;
  duration_ms: number;
  payload: string;
}

export interface AccessAPIKeyRecord {
  id: string;
  name: string;
  prefix: string;
  token?: string;
  scope: string;
  last_used: string;
  expires_at: string;
  created_by: string;
  created_at: string;
}

export interface EnvironmentProfileRecord {
  name: string;
  is_default: boolean;
  safe_for_third_parties: boolean;
  github_repos_enabled: boolean;
  github_repos: string[];
  secrets_enabled: boolean;
  secrets: string;
  compute_credentials_enabled?: boolean;
  boat_credentials_enabled?: boolean;
  credential_profile: string;
  version: number;
  updated_at: string;
}

export interface PortForwardRecord {
  id: string;
  host_port: number;
  vm_id: string;
  vm_name: string;
  guest_port: number;
  protocol: 'TCP' | 'HTTP/HTTPS' | string;
  subdomain: string;
  public_url: string;
  public: boolean;
  status: 'active' | 'inactive' | string;
  created_at: string;
}

export interface AuthUser {
  id: string;
  email: string;
  username: string;
  display_name: string;
  role: string;
  status: string;
  created_at?: number;
  updated_at?: number;
}

export interface AuthStatusResponse {
  auth_enabled: boolean;
  signup_enabled: boolean;
  admin_username?: string;
}

export interface SignupRequest {
  email: string;
  username: string;
  display_name?: string;
  password: string;
}

export interface CreateUserRequest {
  email: string;
  username: string;
  display_name?: string;
  password: string;
  role?: 'user' | 'admin';
}

export interface LoginResponse {
  status: string;
  token?: string;
  csrf?: string;
  user: AuthUser;
}

export interface AuthMeResponse {
  user: AuthUser;
  org_id?: string;
  role?: string;
  auth_type?: string;
  scopes?: string[];
}

export interface ImageListItem {
  id: string;
  name: string;
  description?: string;
  base_image?: string;
  packages?: string[];
  size_mb: number;
  status: string;
  kind: 'base' | 'custom' | string;
  created_at?: string;
}

export interface PrepareImageRequest {
  name: string;
  description?: string;
  base_image?: string;
  packages?: string[];
  script?: string;
  vcpus?: number;
  memory_mb?: number;
  disk_mb?: number;
}

export type ImageDraftStatus = 'draft' | 'building' | 'succeeded' | 'failed';

export interface ImageDraft {
  id: string;
  name: string;
  description: string;
  base_image: string;
  packages: string[];
  script: string;
  vcpus: number;
  memory_mb: number;
  disk_mb: number;
  status: ImageDraftStatus;
  last_job_id?: string;
  last_error?: string;
  last_exit_code?: number;
  template_id?: string;
  template_name?: string;
  created_at: string;
  updated_at: string;
}

export type SchedulingStrategy = 'least_loaded' | 'round_robin' | 'bin_pack' | 'spread';

export interface ClusterSchedulingPolicy {
  strategy: SchedulingStrategy | string;
  cpu_weight: number;
  memory_weight: number;
  disk_weight: number;
  cpu_soft_overcommit_ratio: number;
  cpu_hard_overcommit_ratio: number;
  cpu_share_weight_base: number;
  memory_hard_guarantee: boolean;
  idle_ram_snapshot_enabled: boolean;
  idle_threshold_seconds: number;
  auto_reclaim_on_pressure: boolean;
  host_reserved_vcpus: number;
  host_reserved_memory_mb: number;
  host_reserved_disk_mb: number;
  disk_hard_enforcement: boolean;
}

export interface UpdateWorkerRequest {
  pool_id?: string;
  allocated_vcpus?: number;
  allocated_mem_mb?: number;
  allocated_disk_mb?: number;
  labels?: Record<string, string>;
  status?: 'ready' | 'cordoned' | 'draining' | 'offline' | string;
}

export interface MigrateVMRequest {
  target_worker_id?: string;
  target_pool_id?: string;
  force?: boolean;
}

export interface MigratePoolRequest {
  target_pool_id?: string;
  target_worker_id?: string;
  migrate_workers?: boolean;
  migrate_sandboxes?: boolean;
  force?: boolean;
}

export interface PoolMigrationResponse {
  source_pool_id: string;
  target_pool_id: string;
  workers_moved: number;
  workers_failed: number;
  sandboxes_migrated: number;
  sandboxes_failed: number;
  moved_worker_ids: string[];
  migrated_vm_ids: string[];
  errors?: string[];
}

export interface BulkVMsRequest {
  ids: string[];
  action?:
    | 'start'
    | 'stop'
    | 'restart'
    | 'save-state'
    | 'snapshot'
    | 'resume'
    | 'protect'
    | 'unprotect'
    | 'delete'
    | string;
  async?: boolean;
}

export interface BulkVMItemError {
  id: string;
  error: string;
}

export interface BulkVMsResponse {
  action?: string;
  status?: 'queued' | 'completed' | string;
  task_id?: string;
  queued?: number;
  requested: number;
  succeeded: string[];
  failed: BulkVMItemError[];
  warnings?: BulkVMItemError[];
  /** Durable operation records created for this request, in item order. */
  operation_ids?: string[];
}

/** Durable record of a long-running unit of work. */
export interface OperationRecord {
  id: string;
  owner_id: string;
  kind: string;
  target_id?: string;
  target_name?: string;
  status: 'queued' | 'running' | 'succeeded' | 'failed' | 'canceled' | string;
  progress: number;
  request_json?: string;
  result_json?: string;
  error_msg?: string;
  task_id?: string;
  actor?: string;
  trace_id?: string;
  created_at: string;
  started_at?: string;
  updated_at: string;
  finished_at?: string;
}

/** Terminal or in-flight state of an asynchronous bulk task. */
export interface BulkTaskRecord {
  id: string;
  action: string;
  status: 'queued' | 'running' | 'completed' | 'partial' | 'failed' | string;
  stage?: string;
  requested: number;
  queued: number;
  completed?: number;
  succeeded: string[];
  failed: BulkVMItemError[];
  warnings?: BulkVMItemError[];
  elapsed_ms?: number;
  created_at: string;
  updated_at: string;
}

export interface BulkCreateVMsRequest {
  items: CreateVMRequest[];
  async?: boolean;
}

export interface BulkCloneItem extends CreateVMRequest {
  source_id: string;
}

export interface BulkCloneVMsRequest {
  source_id?: string;
  names?: string[];
  items?: BulkCloneItem[];
  description?: string;
  vcpus?: number;
  memory_mb?: number;
  disk_mb?: number;
  disk_gb?: number;
  network_isolation?: NetworkIsolationMode;
  network_isolated?: boolean;
  auto_start?: boolean;
  protected?: boolean;
  worker_id?: string;
  pool_id?: string;
  async?: boolean;
}

export interface ResourceLimitErrorDetails {
  code?: string;
  resource?: 'cpu' | 'memory' | 'disk' | string;
  limit_type?: 'hard' | 'soft' | string;
  node_id?: string;
  requested?: number;
  available?: number;
  limit?: number;
  error?: string;
  suggestion?: string;
}

export interface ExecVMRequest {
  command: string;
  user?: string;
  timeout?: number;
}

export interface ExecVMResponse {
  stdout: string;
  stderr: string;
  exit_code: number;
  duration_ms: number;
}

export interface ExecStreamEvent {
  stream?: 'stdout' | 'stderr' | string;
  data?: string;
  error?: string;
  exit_code?: number;
}
