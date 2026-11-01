export type VMStatus =
  | 'created'
  | 'queued'
  | 'running'
  | 'paused'
  | 'stopped'
  | 'failed';

export interface HealthResult {
  status: string;
  latencyMs: number;
}

export interface VM {
  id: string;
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
  name: string;
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
  ssh_key_id?: string;
  ssh_public_key?: string;
}

export interface ExecRequest {
  command: string;
  user?: string;
  timeout_seconds?: number;
}

export interface ExecResponse {
  stdout: string;
  stderr: string;
  exit_code: number;
  duration_ms: number;
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
  base_image: string;
  packages: string[];
  disk_size_mb: number;
  image_path: string;
  status: ImageStatus;
  error?: string;
  created_at: string;
  updated_at: string;
}

export interface BuildImageRequest {
  name: string;
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
  disk_size_mb: number;
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
