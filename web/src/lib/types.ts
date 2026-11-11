export type VMStatus =
  | 'created'
  | 'queued'
  | 'running'
  | 'paused'
  | 'stopped'
  | 'failed';

export interface HealthResult {
  status: string;
  version?: string;
  name?: string;
  latencyMs: number;
}

export interface VM {
  id: string;
  internal_id?: string;
  name: string;
  status: VMStatus;
  vcpus: number;
  memory_mb: number;
  ip_address: string;
  public_ip?: string;
  public_ip_enabled?: boolean;
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
  pid?: number;
  socket_path?: string;
  protected?: boolean;
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
  tier?: string;
  vcpus: number;
  memory_mb?: number;
  disk_mb?: number;
  clone_from?: string;
  image?: string;
  template?: string;
  ip_address?: string;
  mac_address?: string;
  public_ip?: boolean;
  public_ip_enabled?: boolean;
  dedicated_ip?: boolean;
  protected?: boolean;
  ssh_key_id?: string;
  ssh_public_key?: string;
}

export interface ExecRequest {
  command: string;
  user?: string;
  timeout?: number;
  timeout_seconds?: number;
}

export interface ExecResponse {
  stdout: string;
  stderr: string;
  exit_code: number;
  duration_ms: number;
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
  state: 'queued' | 'running' | 'completed' | 'failed' | 'canceled' | string;
  priority: number;
  spec_json: string;
  result_json: string;
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

export interface AgentProfileRecord {
  id: string;
  name: string;
  provider: string;
  upstream_url: string;
  inject_header: string;
  secret_ref: string;
  sandboxes_linked: number;
  status: 'active' | 'disabled' | string;
  created_at: string;
  updated_at: string;
}

export interface EnvironmentProfileRecord {
  name: string;
  is_default: boolean;
  safe_for_third_parties: boolean;
  github_repos_enabled: boolean;
  github_repos: string[];
  secrets_enabled: boolean;
  secrets: string;
  boat_credentials_enabled: boolean;
  agent_credentials_enabled: boolean;
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
  status: 'active' | 'inactive' | string;
  created_at: string;
}
