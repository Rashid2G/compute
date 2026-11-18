import { useEffect, useMemo, useState } from 'react';
import {
  AlertTriangle,
  ArrowRightLeft,
  Camera,
  Check,
  Copy,
  Cpu,
  ExternalLink,
  Globe,
  HardDrive,
  Info,
  Key,
  Layers,
  Lock,
  Network,
  Pause,
  Play,
  Plus,
  RefreshCw,
  RotateCcw,
  Save,
  Scaling,
  Server,
  Settings,
  Shield,
  ShieldAlert,
  SlidersHorizontal,
  Square,
  Terminal,
  Trash2,
  Unlock,
  Unplug,
  X,
} from 'lucide-react';
import { toast } from 'sonner';

import { Button } from '@/components/ui/button';
import { CopyButton } from '@/components/copy-button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { StatusBadge } from '@/components/status-badge';
import { api, errorMessage } from '@/lib/api';
import { formatDateTime, formatMemory, formatRelativeTime } from '@/lib/format';
import { openSSHTerminalWindow } from '@/lib/terminal';
import { primarySandboxAddress, sandboxAccess } from '@/lib/sandbox-access';
import type {
  NetworkInspectResult,
  NetworkIsolationMode,
  PortForwardRecord,
  VM,
  VolumeRecord,
  WorkerNodeRecord,
  WorkerPoolRecord,
} from '@/lib/types';
import { cn } from '@/lib/utils';

const DEFAULT_DISK_MB = 20480;

export interface SandboxOptionsDialogProps {
  vm: VM | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  workers?: WorkerNodeRecord[];
  pools?: WorkerPoolRecord[];
  volumes?: VolumeRecord[];
  portForwards?: PortForwardRecord[];
  isAdmin?: boolean;
  onRun?: (vm: VM) => void;
  onStop?: (vm: VM) => void;
  onResume?: (vm: VM) => void;
  onRestart?: (vm: VM) => Promise<void> | void;
  onDelete?: (vm: VM) => void;
  onSnapshot?: (vm: VM) => void;
  onClone?: (vm: VM) => void;
  onResize?: (vm: VM) => void;
  onMigrate?: (vm: VM) => void;
  onToggleProtection?: (vm: VM) => Promise<void> | void;
  onInspectJson?: (vm: VM) => void;
  onRefresh?: () => void | Promise<void>;
}

type SettingsSection =
  | 'general'
  | 'specs'
  | 'networking'
  | 'storage'
  | 'placement'
  | 'snapshots'
  | 'danger';

function deriveTierType(vcpus: number): 'small' | 'default' | 'large' | 'xlarge' {
  if (vcpus <= 2) return 'small';
  if (vcpus <= 4) return 'default';
  if (vcpus <= 8) return 'large';
  return 'xlarge';
}

function deriveTierLabel(vcpus: number, memoryMb: number): string {
  const tier = deriveTierType(vcpus);
  const ram =
    memoryMb >= 1024
      ? `${Number.isInteger(memoryMb / 1024) ? memoryMb / 1024 : (memoryMb / 1024).toFixed(1)}G`
      : `${memoryMb}M`;
  return `${tier} (${vcpus}c/${ram})`;
}

export function SandboxOptionsDialog({
  vm,
  open,
  onOpenChange,
  workers = [],
  pools = [],
  volumes: initialVolumes = [],
  portForwards: initialPortForwards = [],
  isAdmin = false,
  onRun,
  onStop,
  onResume,
  onRestart,
  onDelete,
  onSnapshot,
  onClone,
  onResize,
  onMigrate,
  onToggleProtection,
  onInspectJson,
  onRefresh,
}: SandboxOptionsDialogProps) {
  const [activeSection, setActiveSection] = useState<SettingsSection>('general');
  const [networkInspect, setNetworkInspect] = useState<NetworkInspectResult | null>(null);
  const [restarting, setRestarting] = useState(false);

  // Description state (editable on the go)
  const [description, setDescription] = useState(vm?.description || '');
  const [isSavingDesc, setIsSavingDesc] = useState(false);
  const [descDirty, setDescDirty] = useState(false);

  // SSH Key reconciliation & SSH password auth (editable on the go)
  const [isReconcilingSSH, setIsReconcilingSSH] = useState(false);
  const [isTogglingSSHPassword, setIsTogglingSSHPassword] = useState(false);

  // Pool assignment (editable on the go)
  const [assignedPoolId, setAssignedPoolId] = useState(vm?.pool_id || 'default');
  const [isSavingPool, setIsSavingPool] = useState(false);

  // Volumes state
  const [localVolumes, setLocalVolumes] = useState<VolumeRecord[]>(initialVolumes);
  const [attachVolumeId, setAttachVolumeId] = useState('');
  const [volumesLoading, setVolumesLoading] = useState(false);

  // Port forwards state
  const [localPortForwards, setLocalPortForwards] = useState<PortForwardRecord[]>(initialPortForwards);
  const [forwardsLoading, setForwardsLoading] = useState(false);
  const [addForwardOpen, setAddForwardOpen] = useState(false);
  const [fwGuestPort, setFwGuestPort] = useState('8080');
  const [fwProtocol, setFwProtocol] = useState<'TCP' | 'HTTP/HTTPS'>('HTTP/HTTPS');
  const [fwSubdomain, setFwSubdomain] = useState('');
  const [fwPublic, setFwPublic] = useState(true);

  // In-dialog Resize state
  const [resizeVcpus, setResizeVcpus] = useState(2);
  const [resizeMemoryMb, setResizeMemoryMb] = useState(2048);
  const [resizeDiskGb, setResizeDiskGb] = useState(20);
  const [resizeAutoStart, setResizeAutoStart] = useState(false);
  const [isResizing, setIsResizing] = useState(false);

  // In-dialog Migrate state
  const [migrateWorkerId, setMigrateWorkerId] = useState('');
  const [migratePoolId, setMigratePoolId] = useState('');
  const [migrateForce, setMigrateForce] = useState(false);
  const [isMigrating, setIsMigrating] = useState(false);

  const vmId = vm?.id ?? null;

  // Sync passed volumes/port forwards
  useEffect(() => {
    setLocalVolumes(initialVolumes);
  }, [initialVolumes]);

  useEffect(() => {
    setLocalPortForwards(initialPortForwards);
  }, [initialPortForwards]);

  // Load sandbox connectivity, network inspection, and initial editable fields
  useEffect(() => {
    if (!open || !vmId) return;

    let cancelled = false;

    if (vm) {
      setDescription(vm.description || '');
      setDescDirty(false);
      setAssignedPoolId(vm.pool_id || 'default');
      setResizeVcpus(vm.vcpus || 2);
      setResizeMemoryMb(vm.memory_mb || 2048);
      setResizeDiskGb(Math.max(1, Math.round((vm.disk_mb || DEFAULT_DISK_MB) / 1024)));
      setResizeAutoStart(vm.status === 'running');
      setMigrateWorkerId(vm.worker_id || '');
      setMigratePoolId(vm.pool_id || 'default');
    }

    // Inspect network
    api
      .inspectNetwork(vmId)
      .then((res) => {
        if (!cancelled) setNetworkInspect(res);
      })
      .catch(() => {
        if (!cancelled) setNetworkInspect(null);
      });

    // Refresh volumes & port forwards
    setVolumesLoading(true);
    setForwardsLoading(true);

    Promise.allSettled([api.listVolumes(), api.listPortForwards()])
      .then(([volRes, pfRes]) => {
        if (cancelled) return;
        if (volRes.status === 'fulfilled') {
          setLocalVolumes(volRes.value);
        }
        if (pfRes.status === 'fulfilled') {
          setLocalPortForwards(pfRes.value);
        }
      })
      .finally(() => {
        if (!cancelled) {
          setVolumesLoading(false);
          setForwardsLoading(false);
        }
      });

    return () => {
      cancelled = true;
    };
  }, [open, vmId, vm]);

  const access = useMemo(() => sandboxAccess(vm), [vm]);
  const primaryAddress = useMemo(() => primarySandboxAddress(access), [access]);

  const attachedVolumes = useMemo(() => {
    if (!vm) return [];
    return localVolumes.filter(
      (vol) => vol.attached_vm === vm.name || vol.attached_vm === vm.id,
    );
  }, [localVolumes, vm]);

  const availableVolumes = useMemo(() => {
    return localVolumes.filter((vol) => !vol.attached_vm || vol.attached_vm === '');
  }, [localVolumes]);

  const vmPortForwards = useMemo(() => {
    if (!vm) return [];
    return localPortForwards.filter(
      (pf) => pf.vm_id === vm.id || pf.vm_name === vm.name,
    );
  }, [localPortForwards, vm]);

  const workerNode = useMemo(() => {
    if (!vm) return null;
    return workers.find((w) => w.id === vm.worker_id) || null;
  }, [workers, vm]);

  const workerLabel = workerNode
    ? `${workerNode.name} (${workerNode.id})`
    : vm?.worker_id || 'nod_host_local';

  if (!vm) return null;

  const diskGb = Math.max(1, Math.round((vm.disk_mb || DEFAULT_DISK_MB) / 1024));
  const tierLabel = deriveTierLabel(vm.vcpus, vm.memory_mb);
  const jsonSpec = JSON.stringify(vm, null, 2);

  // Description Handler
  async function handleSaveDescription(e: React.FormEvent) {
    e.preventDefault();
    if (!vm) return;
    setIsSavingDesc(true);
    const toastID = toast.loading('Saving description…');
    try {
      await api.updateVMConfig(vm.id, { description });
      toast.success('Description updated', { id: toastID });
      setDescDirty(false);
      await onRefresh?.();
    } catch (err) {
      toast.error(`Could not save description: ${errorMessage(err)}`, { id: toastID });
    } finally {
      setIsSavingDesc(false);
    }
  }

  // SSH Key Reconciliation Handler
  async function handleReconcileSSH() {
    if (!vm) return;
    setIsReconcilingSSH(true);
    const toastID = toast.loading('Reconciling authorized SSH keys…');
    try {
      await api.reconcileVMSSH(vm.id);
      toast.success('SSH keys reconciled successfully', { id: toastID });
      await onRefresh?.();
    } catch (err) {
      toast.error(`Failed to reconcile SSH keys: ${errorMessage(err)}`, { id: toastID });
    } finally {
      setIsReconcilingSSH(false);
    }
  }

  // SSH Password Auth Toggle Handler
  async function handleToggleSSHPassword() {
    if (!vm) return;
    const next = !vm.ssh_password_enabled;
    setIsTogglingSSHPassword(true);
    const toastID = toast.loading(`${next ? 'Enabling' : 'Disabling'} SSH password login…`);
    try {
      await api.updateVMConfig(vm.id, { ssh_password_enabled: next });
      toast.success(`SSH password login ${next ? 'enabled' : 'disabled'}`, { id: toastID });
      await onRefresh?.();
    } catch (err) {
      toast.error(`Could not update SSH password setting: ${errorMessage(err)}`, { id: toastID });
    } finally {
      setIsTogglingSSHPassword(false);
    }
  }

  // Worker Pool Assignment Handler
  async function handleSavePool(newPool: string) {
    if (!vm || newPool === vm.pool_id) return;
    setAssignedPoolId(newPool);
    setIsSavingPool(true);
    const toastID = toast.loading(`Updating worker pool to ${newPool}…`);
    try {
      await api.updateVMConfig(vm.id, { pool_id: newPool });
      toast.success(`Worker pool updated to ${newPool}`, { id: toastID });
      await onRefresh?.();
    } catch (err) {
      toast.error(`Could not update pool: ${errorMessage(err)}`, { id: toastID });
    } finally {
      setIsSavingPool(false);
    }
  }

  // Volume Handlers
  async function handleAttachVolume(e: React.FormEvent) {
    e.preventDefault();
    if (!vm || !attachVolumeId) return;
    const vol = availableVolumes.find((v) => v.id === attachVolumeId);
    const toastID = toast.loading(`Attaching volume ${vol?.name || attachVolumeId}…`);
    try {
      const updated = await api.attachVolume(attachVolumeId, vm.id);
      setLocalVolumes((prev) =>
        prev.map((v) => (v.id === updated.id ? updated : v)),
      );
      setAttachVolumeId('');
      toast.success(`Attached ${vol?.name || attachVolumeId} to ${vm.name}`, { id: toastID });
      await onRefresh?.();
    } catch (err) {
      toast.error(`Failed to attach volume: ${errorMessage(err)}`, { id: toastID });
    }
  }

  async function handleDetachVolume(vol: VolumeRecord) {
    if (!vm) return;
    const toastID = toast.loading(`Detaching volume ${vol.name}…`);
    try {
      const updated = await api.detachVolume(vol.id);
      setLocalVolumes((prev) =>
        prev.map((v) => (v.id === updated.id ? updated : v)),
      );
      toast.success(`Detached ${vol.name} from ${vm.name}`, { id: toastID });
      await onRefresh?.();
    } catch (err) {
      toast.error(`Failed to detach volume: ${errorMessage(err)}`, { id: toastID });
    }
  }

  // Port Forward Handlers
  async function handleAddPortForward(e: React.FormEvent) {
    e.preventDefault();
    if (!vm) return;
    const gPort = parseInt(fwGuestPort, 10);
    if (!gPort || gPort < 1 || gPort > 65535) {
      toast.error('Guest port must be between 1 and 65535.');
      return;
    }
    const toastID = toast.loading(`Exposing guest port ${gPort}…`);
    try {
      const created = await api.createPortForward({
        vm_id: vm.id,
        vm_name: vm.name,
        guest_port: gPort,
        protocol: fwProtocol === 'HTTP/HTTPS' ? 'HTTP' : 'TCP',
        subdomain: fwProtocol === 'HTTP/HTTPS' && fwSubdomain.trim() ? fwSubdomain.trim() : undefined,
        public: fwPublic,
      });
      setLocalPortForwards((prev) => [...prev, created]);
      setAddForwardOpen(false);
      setFwSubdomain('');
      toast.success(`Exposed port :${gPort} for ${vm.name}`, { id: toastID });
      await onRefresh?.();
    } catch (err) {
      toast.error(`Failed to expose port: ${errorMessage(err)}`, { id: toastID });
    }
  }

  async function handleDeletePortForward(pf: PortForwardRecord) {
    if (!vm) return;
    const toastID = toast.loading(`Removing port forward :${pf.guest_port}…`);
    try {
      await api.deletePortForward(pf.id);
      setLocalPortForwards((prev) => prev.filter((p) => p.id !== pf.id));
      toast.success(`Removed port exposure :${pf.guest_port}`, { id: toastID });
      await onRefresh?.();
    } catch (err) {
      toast.error(`Failed to remove port forward: ${errorMessage(err)}`, { id: toastID });
    }
  }

  // Restart Handler
  async function handleRestart() {
    if (!vm) return;
    setRestarting(true);
    try {
      if (onRestart) {
        await onRestart(vm);
      } else {
        const toastID = toast.loading(`Restarting sandbox ${vm.name}…`);
        await api.restartVM(vm.id);
        toast.success(`Sandbox ${vm.name} restarted`, { id: toastID });
        await onRefresh?.();
      }
    } catch (err) {
      toast.error(`Failed to restart ${vm.name}: ${errorMessage(err)}`);
    } finally {
      setRestarting(false);
    }
  }

  // Resize Handler
  async function handleResizeSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!vm) return;
    if (onResize) {
      onOpenChange(false);
      onResize(vm);
      return;
    }
    setIsResizing(true);
    const toastID = toast.loading(`Provisioning resized sandbox for ${vm.name}…`);
    try {
      const created = await api.cloneVM(vm.id, {
        name: `${vm.name}-resized`,
        vcpus: resizeVcpus,
        memory_mb: resizeMemoryMb,
        disk_gb: resizeDiskGb,
        auto_start: resizeAutoStart,
      });
      toast.success(`Created resized sandbox ${created.name}`, { id: toastID });
      onOpenChange(false);
      await onRefresh?.();
    } catch (err) {
      toast.error(`Resize failed: ${errorMessage(err)}`, { id: toastID });
    } finally {
      setIsResizing(false);
    }
  }

  // Migrate Handler
  async function handleMigrateSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!vm) return;
    if (onMigrate) {
      onOpenChange(false);
      onMigrate(vm);
      return;
    }
    setIsMigrating(true);
    const toastID = toast.loading(`Migrating sandbox ${vm.name}…`);
    try {
      await api.migrateVM(vm.id, {
        target_worker_id: migrateWorkerId || undefined,
        target_pool_id: migratePoolId || undefined,
        force: migrateForce,
      });
      toast.success(`Migrated sandbox ${vm.name}`, { id: toastID });
      onOpenChange(false);
      await onRefresh?.();
    } catch (err) {
      toast.error(`Migration failed: ${errorMessage(err)}`, { id: toastID });
    } finally {
      setIsMigrating(false);
    }
  }

  // Public IP & Isolation toggles
  async function handleTogglePublicIP() {
    if (!vm) return;
    const next = !vm.public_ip_enabled;
    const toastID = toast.loading(`${next ? 'Enabling' : 'Disabling'} public IP for ${vm.name}…`);
    try {
      await api.setVMPublicIP(vm.id, next);
      toast.success(`Public IP ${next ? 'enabled' : 'disabled'} for ${vm.name}`, { id: toastID });
      await onRefresh?.();
    } catch (err) {
      toast.error(`Could not update public IP for ${vm.name}: ${errorMessage(err)}`, { id: toastID });
    }
  }

  async function handleToggleNetworkIsolation() {
    if (!vm) return;
    const currentMode = vm.network_isolation || 'account';
    const nextMode: NetworkIsolationMode = currentMode === 'isolated' ? 'account' : 'isolated';
    const toastID = toast.loading(`Switching ${vm.name} to ${nextMode} network…`);
    try {
      await api.setVMNetworkIsolation(vm.id, nextMode);
      toast.success(
        nextMode === 'isolated'
          ? `${vm.name} is now isolated on its own`
          : `${vm.name} can now communicate with account sandboxes`,
        { id: toastID },
      );
      await onRefresh?.();
    } catch (err) {
      toast.error(`Could not update network isolation: ${errorMessage(err)}`, { id: toastID });
    }
  }

  // Sidebar navigation sections
  const SECTIONS = [
    {
      id: 'general' as const,
      label: 'General & Security',
      icon: Settings,
      hint: vm.protected ? 'Protected' : 'Notes & SSH',
    },
    {
      id: 'specs' as const,
      label: 'Compute Specs',
      icon: Cpu,
      hint: `${vm.vcpus} vCPU · ${formatMemory(vm.memory_mb)}`,
    },
    {
      id: 'networking' as const,
      label: 'Port Forwards & Ingress',
      icon: Globe,
      hint: `${vmPortForwards.length} forwarded`,
    },
    {
      id: 'storage' as const,
      label: 'Attached Volumes',
      icon: HardDrive,
      hint: `${attachedVolumes.length} attached`,
    },
    {
      id: 'placement' as const,
      label: 'Worker & Placement',
      icon: Server,
      hint: workerNode?.name || vm.worker_id || 'Default',
    },
    {
      id: 'snapshots' as const,
      label: 'Snapshots & Clones',
      icon: Camera,
      hint: 'Replication',
    },
    {
      id: 'danger' as const,
      label: 'Danger & Advanced',
      icon: ShieldAlert,
      hint: 'Spec & Delete',
    },
  ];

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[92vh] h-[840px] max-w-5xl overflow-hidden p-0 border border-border bg-background shadow-2xl flex flex-col">
        {/* Top Header Bar */}
        <DialogHeader className="border-b border-border bg-muted/20 px-5 py-3.5 shrink-0">
          <div className="flex flex-wrap items-center justify-between gap-4 pr-6">
            <div className="flex items-center gap-3 min-w-0">
              <span className="flex h-9 w-9 shrink-0 items-center justify-center border border-border bg-background text-foreground shadow-xs">
                <SlidersHorizontal className="h-4 w-4 text-primary" />
              </span>
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <DialogTitle className="font-display text-base font-semibold text-foreground truncate">
                    {vm.name}
                  </DialogTitle>
                  <StatusBadge status={vm.status} />
                  {vm.protected && (
                    <span className="inline-flex items-center gap-1 border border-primary/40 bg-primary/10 px-1.5 py-0.5 font-mono text-[9.5px] uppercase text-primary">
                      <Shield className="h-2.5 w-2.5" />
                      Protected
                    </span>
                  )}
                  {vm.auto_hibernated && (
                    <span className="inline-flex items-center border border-warning/40 bg-warning/10 px-1.5 py-0.5 font-mono text-[9.5px] uppercase text-warning">
                      SSD Hibernated
                    </span>
                  )}
                </div>
                <DialogDescription className="mt-0.5 flex items-center gap-2 font-mono text-[11px] text-muted-foreground">
                  <span>ID: {vm.id}</span>
                  <CopyButton value={vm.id} label="Copy ID" className="h-3.5 w-3.5" />
                  <span>·</span>
                  <span>Created {formatRelativeTime(vm.created_at)}</span>
                </DialogDescription>
              </div>
            </div>

            {/* Quick Lifecycle Action Bar */}
            <div className="flex flex-wrap items-center gap-1.5">
              {vm.status === 'running' ? (
                <>
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    onClick={() => {
                      onOpenChange(false);
                      onStop?.(vm);
                    }}
                    className="h-7 gap-1 border-border text-[11px] hover:border-warning hover:text-warning"
                  >
                    <Square className="h-3 w-3" />
                    <span>Stop</span>
                  </Button>
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    disabled={restarting}
                    onClick={() => void handleRestart()}
                    className="h-7 gap-1 border-border text-[11px] hover:border-primary"
                  >
                    <RotateCcw className={cn('h-3 w-3', restarting && 'animate-spin')} />
                    <span>{restarting ? 'Restarting…' : 'Restart'}</span>
                  </Button>
                  <Button
                    type="button"
                    size="sm"
                    onClick={() => openSSHTerminalWindow(vm.id, vm.name, 'compute')}
                    className="h-7 gap-1 bg-primary text-[11px] font-medium text-primary-foreground hover:bg-primary-hover"
                  >
                    <Terminal className="h-3 w-3" />
                    <span>Terminal</span>
                  </Button>
                </>
              ) : vm.status === 'paused' ? (
                <>
                  <Button
                    type="button"
                    size="sm"
                    onClick={() => {
                      onOpenChange(false);
                      onResume?.(vm);
                    }}
                    className="h-7 gap-1 bg-success text-[11px] font-medium text-success-foreground hover:bg-success/90"
                  >
                    <RotateCcw className="h-3 w-3" />
                    <span>Resume</span>
                  </Button>
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    onClick={() => {
                      onOpenChange(false);
                      onStop?.(vm);
                    }}
                    className="h-7 gap-1 border-border text-[11px] hover:border-warning hover:text-warning"
                  >
                    <Square className="h-3 w-3" />
                    <span>Stop</span>
                  </Button>
                </>
              ) : (
                <Button
                  type="button"
                  size="sm"
                  onClick={() => {
                    onOpenChange(false);
                    onRun?.(vm);
                  }}
                  className="h-7 gap-1 bg-primary text-[11px] font-medium text-primary-foreground hover:bg-primary-hover"
                >
                  <Play className="h-3 w-3" />
                  <span>Start Sandbox</span>
                </Button>
              )}
            </div>
          </div>
        </DialogHeader>

        {/* Master-Detail Layout: Sidebar Options + Main Content */}
        <div className="flex flex-1 min-h-0 overflow-hidden divide-x divide-border">
          {/* Left Options Navigation Sidebar */}
          <aside className="w-64 sm:w-72 bg-secondary/15 flex flex-col justify-between shrink-0 overflow-y-auto p-2.5">
            <div className="space-y-1">
              <div className="px-2.5 py-1 text-[10px] font-mono font-semibold uppercase tracking-wider text-muted-foreground">
                Settings Sections
              </div>
              <nav className="space-y-0.5">
                {SECTIONS.map((sec) => {
                  const Icon = sec.icon;
                  const isSelected = activeSection === sec.id;
                  return (
                    <button
                      key={sec.id}
                      type="button"
                      onClick={() => setActiveSection(sec.id)}
                      className={cn(
                        'w-full flex items-center justify-between gap-2.5 px-3 py-2 text-left text-xs transition-colors rounded-none border-l-2',
                        isSelected
                          ? 'border-primary bg-primary/10 text-primary font-medium shadow-2xs'
                          : 'border-transparent text-muted-foreground hover:text-foreground hover:bg-muted/40',
                      )}
                    >
                      <div className="flex items-center gap-2.5 min-w-0">
                        <Icon
                          className={cn(
                            'h-4 w-4 shrink-0',
                            isSelected ? 'text-primary' : 'text-muted-foreground',
                          )}
                        />
                        <span className="truncate">{sec.label}</span>
                      </div>
                      {sec.hint && (
                        <span
                          className={cn(
                            'font-mono text-[10px] truncate max-w-[85px]',
                            isSelected
                              ? 'text-primary/90 font-medium'
                              : 'text-muted-foreground/75',
                          )}
                        >
                          {sec.hint}
                        </span>
                      )}
                    </button>
                  );
                })}
              </nav>
            </div>

            {/* Sidebar Footer: Quick Host & IP summary */}
            <div className="mt-4 pt-3 border-t border-border/80 px-2.5 space-y-2 font-mono text-[10.5px]">
              <div className="flex items-center justify-between text-muted-foreground">
                <span>Address</span>
                <span className="text-foreground font-medium truncate max-w-[130px]">
                  {vm.ip_address || 'Unassigned'}
                </span>
              </div>
              <div className="flex items-center justify-between text-muted-foreground">
                <span>Tap Device</span>
                <span className="text-foreground font-medium truncate max-w-[130px]">
                  {vm.tap_device || 'none'}
                </span>
              </div>
              <div className="flex items-center justify-between text-muted-foreground">
                <span>Updated</span>
                <span>{formatRelativeTime(vm.updated_at)}</span>
              </div>
            </div>
          </aside>

          {/* Right Main Content Pane */}
          <main className="flex-1 min-w-0 overflow-y-auto p-6 space-y-6 bg-background">
            {/* Section 1: General & Security */}
            {activeSection === 'general' && (
              <div className="space-y-6">
                <div>
                  <h3 className="text-sm font-semibold text-foreground flex items-center gap-2">
                    <Settings className="h-4 w-4 text-primary" />
                    General &amp; Security Settings
                  </h3>
                  <p className="text-xs text-muted-foreground mt-0.5">
                    Manage sandbox identification, custom description notes, deletion protection, and SSH access controls.
                  </p>
                </div>

                {/* Identity & Metadata Card */}
                <div className="border border-border bg-secondary/10 p-4 space-y-3">
                  <div className="text-[11px] font-mono font-semibold uppercase tracking-wider text-muted-foreground">
                    Sandbox Metadata &amp; Hostname
                  </div>
                  <div className="grid grid-cols-1 sm:grid-cols-2 gap-3 text-xs">
                    <div className="border border-border bg-background p-3 space-y-1">
                      <span className="text-[10px] uppercase font-mono text-muted-foreground">
                        Sandbox Name
                      </span>
                      <div className="font-mono font-medium text-foreground truncate">
                        {vm.name}
                      </div>
                    </div>
                    <div className="border border-border bg-background p-3 space-y-1">
                      <span className="text-[10px] uppercase font-mono text-muted-foreground">
                        External Identifier (UUID)
                      </span>
                      <div className="flex items-center justify-between font-mono font-medium text-foreground">
                        <span className="truncate">{vm.id}</span>
                        <CopyButton value={vm.id} label="Copy ID" />
                      </div>
                    </div>
                    <div className="border border-border bg-background p-3 space-y-1">
                      <span className="text-[10px] uppercase font-mono text-muted-foreground">
                        Internal ID / Slot
                      </span>
                      <div className="font-mono text-muted-foreground truncate">
                        {vm.internal_id || vm.id}
                      </div>
                    </div>
                    <div className="border border-border bg-background p-3 space-y-1">
                      <span className="text-[10px] uppercase font-mono text-muted-foreground">
                        DNS Fully Qualified Name
                      </span>
                      <div className="flex items-center justify-between font-mono text-foreground">
                        <span className="truncate">
                          {vm.hostname || `${vm.name}.compute.internal`}
                        </span>
                        <CopyButton
                          value={vm.hostname || `${vm.name}.compute.internal`}
                          label="Copy hostname"
                        />
                      </div>
                    </div>
                  </div>
                </div>

                {/* Editable Description & Notes (Modifiable on the go) */}
                <form
                  onSubmit={(e) => void handleSaveDescription(e)}
                  className="border border-border bg-background p-4 space-y-3"
                >
                  <div className="flex items-center justify-between">
                    <div>
                      <div className="text-xs font-semibold uppercase tracking-wider font-mono text-foreground">
                        Description &amp; Operational Notes
                      </div>
                      <p className="text-[11px] text-muted-foreground mt-0.5">
                        Add tags, workload purpose, or developer instructions for this sandbox.
                      </p>
                    </div>
                    {descDirty && (
                      <span className="text-[10.5px] font-mono text-warning font-medium">
                        Unsaved changes
                      </span>
                    )}
                  </div>
                  <textarea
                    rows={3}
                    value={description}
                    onChange={(e) => {
                      setDescription(e.target.value);
                      setDescDirty(true);
                    }}
                    placeholder="e.g. Staging backend for payments service with PostgreSQL cache..."
                    className="w-full border border-border bg-background p-2.5 font-mono text-xs text-foreground placeholder:text-muted-foreground focus:border-primary focus:outline-none"
                  />
                  <div className="flex items-center justify-end gap-2">
                    {descDirty && (
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        onClick={() => {
                          setDescription(vm.description || '');
                          setDescDirty(false);
                        }}
                        className="h-7 text-xs"
                      >
                        Reset
                      </Button>
                    )}
                    <Button
                      type="submit"
                      size="sm"
                      disabled={!descDirty || isSavingDesc}
                      className="h-7 text-xs gap-1.5"
                    >
                      <Save className="h-3 w-3" />
                      <span>{isSavingDesc ? 'Saving…' : 'Save Description'}</span>
                    </Button>
                  </div>
                </form>

                {/* Deletion Protection Setting (Modifiable on the go) */}
                <div className="flex items-center justify-between border border-border bg-secondary/15 p-4">
                  <div className="space-y-0.5 max-w-lg">
                    <div className="flex items-center gap-1.5 text-xs font-medium text-foreground">
                      <Shield className="h-4 w-4 text-primary" />
                      <span>Deletion Protection</span>
                    </div>
                    <p className="text-[11px] text-muted-foreground">
                      {vm.protected
                        ? 'Protected is ACTIVE. Accidental destruction via UI, CLI, or API calls is strictly blocked.'
                        : 'Protection is OFF. This sandbox can be terminated or deleted at any time.'}
                    </p>
                  </div>
                  <Button
                    type="button"
                    size="sm"
                    variant={vm.protected ? 'outline' : 'default'}
                    onClick={() => onToggleProtection?.(vm)}
                    className="h-7 text-xs shrink-0"
                  >
                    {vm.protected ? (
                      <>
                        <Unlock className="h-3 w-3 mr-1" />
                        Remove Protection
                      </>
                    ) : (
                      <>
                        <Lock className="h-3 w-3 mr-1" />
                        Enable Protection
                      </>
                    )}
                  </Button>
                </div>

                {/* SSH & Key Management Card (Modifiable on the go) */}
                <div className="border border-border bg-background p-4 space-y-4">
                  <div className="flex items-center justify-between">
                    <div>
                      <div className="text-xs font-semibold uppercase tracking-wider font-mono text-foreground flex items-center gap-1.5">
                        <Key className="h-3.5 w-3.5 text-primary" />
                        <span>SSH Authentication &amp; Authorized Keys</span>
                      </div>
                      <p className="text-[11px] text-muted-foreground mt-0.5">
                        Configure guest authentication and synchronize cluster authorized keys into this microVM.
                      </p>
                    </div>
                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      disabled={isReconcilingSSH}
                      onClick={() => void handleReconcileSSH()}
                      className="h-7 text-xs gap-1"
                    >
                      <RefreshCw className={cn('h-3 w-3', isReconcilingSSH && 'animate-spin')} />
                      <span>{isReconcilingSSH ? 'Reconciling…' : 'Reconcile Keys'}</span>
                    </Button>
                  </div>

                  <div className="grid grid-cols-1 md:grid-cols-2 gap-3 text-xs">
                    {/* SSH Password Auth Setting */}
                    <div className="border border-border bg-secondary/10 p-3 space-y-2 flex flex-col justify-between">
                      <div>
                        <div className="text-[10px] uppercase font-mono text-muted-foreground">
                          Password Authentication
                        </div>
                        <p className="text-[11px] text-muted-foreground mt-1">
                          {vm.ssh_password_enabled
                            ? 'Password login via SSH is enabled for guest accounts.'
                            : 'Password login is disabled. Only cryptographic SSH keys are accepted.'}
                        </p>
                      </div>
                      <div className="pt-2">
                        <Button
                          type="button"
                          size="sm"
                          variant="outline"
                          disabled={isTogglingSSHPassword}
                          onClick={() => void handleToggleSSHPassword()}
                          className="h-6.5 text-[11px]"
                        >
                          {vm.ssh_password_enabled
                            ? 'Disable Password Login'
                            : 'Enable Password Login'}
                        </Button>
                      </div>
                    </div>

                    {/* Active Key Info */}
                    <div className="border border-border bg-secondary/10 p-3 space-y-1">
                      <div className="text-[10px] uppercase font-mono text-muted-foreground">
                        Embedded Public Key
                      </div>
                      <div className="text-[11px] font-mono text-foreground truncate mt-1">
                        {vm.ssh_key_name || (vm.ssh_public_key ? 'Embedded Key' : 'Default Cluster Key')}
                      </div>
                      {vm.ssh_public_key ? (
                        <div className="flex items-center justify-between font-mono text-[10px] text-muted-foreground pt-1">
                          <span className="truncate max-w-[200px]">{vm.ssh_public_key}</span>
                          <CopyButton value={vm.ssh_public_key} label="Copy Public Key" />
                        </div>
                      ) : (
                        <p className="text-[10.5px] text-muted-foreground italic pt-1">
                          Reconciles cluster user keys automatically.
                        </p>
                      )}
                    </div>
                  </div>
                </div>
              </div>
            )}

            {/* Section 2: Compute Specs & Sizing */}
            {activeSection === 'specs' && (
              <div className="space-y-6">
                <div>
                  <h3 className="text-sm font-semibold text-foreground flex items-center gap-2">
                    <Cpu className="h-4 w-4 text-primary" />
                    Compute Specs &amp; Hardware Sizing
                  </h3>
                  <p className="text-xs text-muted-foreground mt-0.5">
                    Inspect current allocation and resize or scale the microVM vCPUs, memory, and persistent storage.
                  </p>
                </div>

                {/* Current Hardware Specs */}
                <div className="border border-border bg-secondary/15 p-4 space-y-3">
                  <div className="flex items-center justify-between">
                    <div className="text-xs font-semibold text-foreground uppercase tracking-wider font-mono">
                      Current Allocation
                    </div>
                    <span className="font-mono text-xs font-semibold text-primary">
                      {tierLabel}
                    </span>
                  </div>

                  <div className="grid grid-cols-2 sm:grid-cols-4 gap-3 font-mono text-xs">
                    <div className="border border-border bg-background p-3">
                      <span className="text-[10px] uppercase text-muted-foreground">vCPUs</span>
                      <div className="mt-1 text-sm font-semibold text-foreground">
                        {vm.vcpus} vCPU
                      </div>
                    </div>
                    <div className="border border-border bg-background p-3">
                      <span className="text-[10px] uppercase text-muted-foreground">Memory</span>
                      <div className="mt-1 text-sm font-semibold text-foreground">
                        {formatMemory(vm.memory_mb)}
                      </div>
                    </div>
                    <div className="border border-border bg-background p-3">
                      <span className="text-[10px] uppercase text-muted-foreground">Disk Storage</span>
                      <div className="mt-1 text-sm font-semibold text-foreground">
                        {diskGb} GB
                      </div>
                    </div>
                    <div className="border border-border bg-background p-3">
                      <span className="text-[10px] uppercase text-muted-foreground">Architecture</span>
                      <div className="mt-1 text-sm font-semibold text-foreground">
                        x86_64
                      </div>
                    </div>
                  </div>
                </div>

                {/* Scale / Resize Sandbox Form */}
                <div className="border border-border bg-background p-4 space-y-4">
                  <div className="flex items-center justify-between">
                    <div>
                      <div className="text-xs font-medium text-foreground flex items-center gap-1.5">
                        <Scaling className="h-3.5 w-3.5 text-primary" />
                        <span>Scale / Resize Sandbox</span>
                      </div>
                      <p className="text-[11px] text-muted-foreground mt-0.5">
                        Select a standard compute preset or enter custom allocation parameters.
                      </p>
                    </div>
                    <span className="text-[10.5px] font-mono text-muted-foreground">
                      Target: {resizeVcpus}c / {(resizeMemoryMb / 1024).toFixed(1)}GB
                    </span>
                  </div>

                  {/* Preset Tier Buttons */}
                  <div className="grid grid-cols-2 sm:grid-cols-4 gap-2">
                    {[
                      { label: 'Small', vcpu: 1, ram: 1024, disk: 10 },
                      { label: 'Default', vcpu: 2, ram: 2048, disk: 20 },
                      { label: 'Large', vcpu: 4, ram: 4096, disk: 40 },
                      { label: 'XLarge', vcpu: 8, ram: 8192, disk: 80 },
                    ].map((preset) => {
                      const isSelected =
                        resizeVcpus === preset.vcpu &&
                        resizeMemoryMb === preset.ram &&
                        resizeDiskGb === preset.disk;
                      return (
                        <button
                          key={preset.label}
                          type="button"
                          onClick={() => {
                            setResizeVcpus(preset.vcpu);
                            setResizeMemoryMb(preset.ram);
                            setResizeDiskGb(preset.disk);
                          }}
                          className={cn(
                            'border p-2.5 text-left font-mono text-[11px] transition-colors',
                            isSelected
                              ? 'border-primary bg-primary/10 text-foreground font-medium'
                              : 'border-border bg-secondary/20 hover:border-border/80 text-muted-foreground hover:text-foreground',
                          )}
                        >
                          <div className="font-semibold text-foreground">{preset.label}</div>
                          <div className="text-[10.5px] text-muted-foreground mt-0.5">
                            {preset.vcpu} vCPU · {preset.ram / 1024} GB · {preset.disk} GB
                          </div>
                        </button>
                      );
                    })}
                  </div>

                  <form onSubmit={(e) => void handleResizeSubmit(e)} className="space-y-4 pt-1">
                    <div className="grid grid-cols-3 gap-3">
                      <div>
                        <label className="block text-[10px] font-mono uppercase text-muted-foreground mb-1">
                          vCPUs (1 - 16)
                        </label>
                        <input
                          type="number"
                          min={1}
                          max={16}
                          value={resizeVcpus}
                          onChange={(e) => setResizeVcpus(Number(e.target.value))}
                          className="h-8 w-full border border-border bg-background px-2.5 font-mono text-xs text-foreground focus:border-primary focus:outline-none"
                        />
                      </div>
                      <div>
                        <label className="block text-[10px] font-mono uppercase text-muted-foreground mb-1">
                          Memory (MB)
                        </label>
                        <input
                          type="number"
                          min={512}
                          step={512}
                          value={resizeMemoryMb}
                          onChange={(e) => setResizeMemoryMb(Number(e.target.value))}
                          className="h-8 w-full border border-border bg-background px-2.5 font-mono text-xs text-foreground focus:border-primary focus:outline-none"
                        />
                      </div>
                      <div>
                        <label className="block text-[10px] font-mono uppercase text-muted-foreground mb-1">
                          Disk (GB)
                        </label>
                        <input
                          type="number"
                          min={5}
                          max={500}
                          value={resizeDiskGb}
                          onChange={(e) => setResizeDiskGb(Number(e.target.value))}
                          className="h-8 w-full border border-border bg-background px-2.5 font-mono text-xs text-foreground focus:border-primary focus:outline-none"
                        />
                      </div>
                    </div>

                    <div className="flex items-center justify-between pt-1">
                      <label className="flex items-center gap-2 text-xs text-muted-foreground cursor-pointer">
                        <input
                          type="checkbox"
                          checked={resizeAutoStart}
                          onChange={(e) => setResizeAutoStart(e.target.checked)}
                          className="h-3.5 w-3.5 accent-primary"
                        />
                        <span>Auto-start resized sandbox immediately</span>
                      </label>

                      <Button
                        type="submit"
                        disabled={isResizing}
                        size="sm"
                        className="gap-1.5"
                      >
                        <Scaling className="h-3.5 w-3.5" />
                        <span>{isResizing ? 'Resizing…' : 'Apply Resize'}</span>
                      </Button>
                    </div>
                  </form>
                </div>
              </div>
            )}

            {/* Section 3: Port Forwards & Ingress (Networking) */}
            {activeSection === 'networking' && (
              <div className="space-y-6">
                <div>
                  <h3 className="text-sm font-semibold text-foreground flex items-center gap-2">
                    <Globe className="h-4 w-4 text-primary" />
                    Port Forwards &amp; Ingress
                  </h3>
                  <p className="text-xs text-muted-foreground mt-0.5">
                    Expose internal microVM TCP or HTTP service ports to the cluster or public internet.
                  </p>
                </div>

                {/* Live Connectivity Banner */}
                <div className="border border-border bg-secondary/15 p-4 space-y-3">
                  <div className="text-xs font-semibold text-foreground uppercase tracking-wider font-mono">
                    Live Web Connectivity
                  </div>
                  {access.url && (
                    <div className="flex items-center justify-between gap-3 border border-border bg-background p-3">
                      <div className="min-w-0">
                        <span className="text-[10px] uppercase font-mono text-muted-foreground block">
                          Ingress Public URL
                        </span>
                        <a
                          href={access.url}
                          target="_blank"
                          rel="noopener noreferrer"
                          className="font-mono text-xs text-[color:var(--link)] hover:underline truncate block font-medium"
                        >
                          {access.url}
                        </a>
                      </div>
                      <div className="flex items-center gap-2 shrink-0">
                        <CopyButton value={access.url} label="Copy Ingress URL" />
                        <a
                          href={access.url}
                          target="_blank"
                          rel="noopener noreferrer"
                          className="inline-flex h-7 items-center gap-1 border border-border px-2 text-xs text-foreground hover:border-primary"
                        >
                          <ExternalLink className="h-3 w-3" />
                          <span>Open</span>
                        </a>
                      </div>
                    </div>
                  )}

                  <div className="grid grid-cols-1 sm:grid-cols-2 gap-3 text-xs">
                    <div className="border border-border bg-background p-2.5 flex items-center justify-between">
                      <div>
                        <div className="text-[10px] uppercase font-mono text-muted-foreground">
                          Internal IP
                        </div>
                        <div className="font-mono font-medium text-foreground">
                          {vm.ip_address || 'Unassigned'}
                        </div>
                      </div>
                      {vm.ip_address && <CopyButton value={vm.ip_address} label="Copy IP" />}
                    </div>

                    <div className="border border-border bg-background p-2.5 flex items-center justify-between">
                      <div className="min-w-0">
                        <div className="text-[10px] uppercase font-mono text-muted-foreground">
                          SSH Command
                        </div>
                        <div className="font-mono text-[11px] text-foreground truncate">
                          {access.ssh || (vm.ip_address ? `ssh compute@${vm.ip_address}` : '—')}
                        </div>
                      </div>
                      {(access.ssh || vm.ip_address) && (
                        <CopyButton
                          value={access.ssh || `ssh compute@${vm.ip_address}`}
                          label="Copy SSH"
                        />
                      )}
                    </div>
                  </div>
                </div>

                {/* Dedicated Public IP & Network Isolation (Modifiable on the go) */}
                <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                  <div className="border border-border bg-background p-4 space-y-3 flex flex-col justify-between">
                    <div>
                      <div className="text-xs font-semibold text-foreground flex items-center gap-1.5">
                        <Globe className="h-3.5 w-3.5 text-primary" />
                        <span>Dedicated Public IP</span>
                      </div>
                      <p className="text-[11px] text-muted-foreground mt-1 leading-relaxed">
                        {vm.public_ip_enabled
                          ? 'Public IP enabled. The sandbox is routed directly on the external network.'
                          : 'Public IP is off. The sandbox is kept strictly internal.'}
                      </p>
                    </div>
                    <Button
                      type="button"
                      size="sm"
                      variant={vm.public_ip_enabled ? 'outline' : 'default'}
                      onClick={() => void handleTogglePublicIP()}
                      className="h-7 text-xs w-full"
                    >
                      {vm.public_ip_enabled ? 'Turn Off Public IP' : 'Turn On Public IP'}
                    </Button>
                  </div>

                  <div className="border border-border bg-background p-4 space-y-3 flex flex-col justify-between">
                    <div>
                      <div className="text-xs font-semibold text-foreground flex items-center gap-1.5">
                        <Network className="h-3.5 w-3.5 text-primary" />
                        <span>Network Isolation</span>
                      </div>
                      <p className="text-[11px] text-muted-foreground mt-1 leading-relaxed">
                        {(vm.network_isolation || 'account') === 'isolated'
                          ? 'Isolated mode. Blocked from communicating with all other sandboxes.'
                          : 'Account mode. Can communicate with sibling sandboxes in this account.'}
                      </p>
                    </div>
                    <Button
                      type="button"
                      size="sm"
                      variant={(vm.network_isolation || 'account') === 'isolated' ? 'default' : 'outline'}
                      onClick={() => void handleToggleNetworkIsolation()}
                      className="h-7 text-xs w-full"
                    >
                      {(vm.network_isolation || 'account') === 'isolated'
                        ? 'Allow Account Sandboxes'
                        : 'Isolate Sandbox'}
                    </Button>
                  </div>
                </div>

                {/* Port Forwards List & Add Form */}
                <div className="border border-border bg-background p-4 space-y-3">
                  <div className="flex items-center justify-between">
                    <div>
                      <div className="text-xs font-semibold uppercase tracking-wider font-mono text-foreground">
                        Forwarded Guest Ports ({vmPortForwards.length})
                      </div>
                      <p className="text-[11px] text-muted-foreground mt-0.5">
                        Active port forward mapping rules for web services, databases, or API listeners.
                      </p>
                    </div>
                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      onClick={() => setAddForwardOpen((v) => !v)}
                      className="h-7 gap-1 text-[11px]"
                    >
                      <Plus className="h-3 w-3" />
                      <span>{addForwardOpen ? 'Close Form' : 'Add Port Forward'}</span>
                    </Button>
                  </div>

                  {forwardsLoading && vmPortForwards.length === 0 ? (
                    <div className="font-mono text-xs text-muted-foreground py-2">
                      Loading port exposures…
                    </div>
                  ) : vmPortForwards.length === 0 ? (
                    <div className="text-xs text-muted-foreground py-2 border border-dashed border-border p-4 text-center">
                      No port forwards mapped yet. Expose guest ports to route traffic into this microVM.
                    </div>
                  ) : (
                    <div className="divide-y divide-border border border-border">
                      {vmPortForwards.map((pf) => (
                        <div
                          key={pf.id}
                          className="flex items-center justify-between gap-3 px-3 py-2 font-mono text-xs"
                        >
                          <div className="min-w-0">
                            <div className="text-foreground font-medium">
                              {pf.host_port > 0
                                ? `:${pf.host_port} → guest :${pf.guest_port}`
                                : `subdomain → guest :${pf.guest_port}`}
                              <span className="ml-2 text-[10px] text-muted-foreground">
                                ({pf.protocol}, {pf.public ? 'public ingress' : 'private cluster'})
                              </span>
                            </div>
                            {pf.public_url && (
                              <a
                                href={pf.public_url}
                                target="_blank"
                                rel="noopener noreferrer"
                                className="block truncate text-[11px] text-[color:var(--link)] hover:underline mt-0.5"
                              >
                                {pf.public_url}
                              </a>
                            )}
                          </div>
                          <Button
                            type="button"
                            variant="ghost"
                            size="sm"
                            onClick={() => void handleDeletePortForward(pf)}
                            aria-label={`Remove exposure for port ${pf.guest_port}`}
                            className="h-7 w-7 p-0 text-muted-foreground hover:text-destructive hover:bg-destructive/10"
                          >
                            <Trash2 className="h-3.5 w-3.5" />
                          </Button>
                        </div>
                      ))}
                    </div>
                  )}

                  {/* Add Forward Form */}
                  {addForwardOpen && (
                    <form
                      onSubmit={(e) => void handleAddPortForward(e)}
                      className="space-y-3 border border-border bg-secondary/15 p-4 mt-2"
                    >
                      <div className="grid grid-cols-2 gap-3">
                        <div>
                          <label className="block text-[10.5px] font-mono text-muted-foreground mb-1">
                            Guest Port (inside VM)
                          </label>
                          <input
                            type="number"
                            min={1}
                            max={65535}
                            value={fwGuestPort}
                            onChange={(e) => setFwGuestPort(e.target.value)}
                            className="h-8 w-full border border-border bg-background px-2.5 font-mono text-xs"
                            placeholder="8080"
                            required
                          />
                        </div>
                        <div>
                          <label className="block text-[10.5px] font-mono text-muted-foreground mb-1">
                            Protocol
                          </label>
                          <select
                            value={fwProtocol}
                            onChange={(e) =>
                              setFwProtocol(e.target.value as 'TCP' | 'HTTP/HTTPS')
                            }
                            className="h-8 w-full border border-border bg-background px-2 font-mono text-xs"
                          >
                            <option value="HTTP/HTTPS">HTTP / Web</option>
                            <option value="TCP">TCP Raw</option>
                          </select>
                        </div>
                      </div>

                      {fwProtocol === 'HTTP/HTTPS' && (
                        <div>
                          <label className="block text-[10.5px] font-mono text-muted-foreground mb-1">
                            Custom Subdomain (Optional)
                          </label>
                          <input
                            type="text"
                            value={fwSubdomain}
                            onChange={(e) => setFwSubdomain(e.target.value)}
                            placeholder="e.g. api, app, preview"
                            className="h-8 w-full border border-border bg-background px-2.5 font-mono text-xs"
                          />
                        </div>
                      )}

                      <label className="flex items-center gap-2 text-xs text-muted-foreground cursor-pointer">
                        <input
                          type="checkbox"
                          checked={fwPublic}
                          onChange={(e) => setFwPublic(e.target.checked)}
                          className="h-3.5 w-3.5 accent-primary"
                        />
                        <span>Public ingress URL (accessible from outside cluster network)</span>
                      </label>

                      <div className="flex justify-end gap-2 pt-1">
                        <Button
                          type="button"
                          variant="ghost"
                          size="sm"
                          onClick={() => setAddForwardOpen(false)}
                          className="h-7 text-xs"
                        >
                          Cancel
                        </Button>
                        <Button type="submit" size="sm" className="h-7 text-xs">
                          Expose Port
                        </Button>
                      </div>
                    </form>
                  )}
                </div>

                {/* Low-Level Network Diagnostics */}
                {networkInspect && (
                  <div className="border border-border bg-background p-4 space-y-2">
                    <div className="text-[11px] font-mono font-semibold uppercase tracking-wider text-muted-foreground">
                      Network Diagnostics
                    </div>
                    <div className="grid grid-cols-2 sm:grid-cols-4 gap-2 font-mono text-[11px]">
                      <div className="border border-border p-2 bg-secondary/10">
                        <span className="text-[10px] text-muted-foreground block">TAP Device</span>
                        <span className="font-medium text-foreground">{networkInspect.tap_device}</span>
                      </div>
                      <div className="border border-border p-2 bg-secondary/10">
                        <span className="text-[10px] text-muted-foreground block">MAC Address</span>
                        <span className="font-medium text-foreground">{networkInspect.mac_address}</span>
                      </div>
                      <div className="border border-border p-2 bg-secondary/10">
                        <span className="text-[10px] text-muted-foreground block">Gateway</span>
                        <span className="font-medium text-foreground">{networkInspect.gateway || '172.16.0.1'}</span>
                      </div>
                      <div className="border border-border p-2 bg-secondary/10">
                        <span className="text-[10px] text-muted-foreground block">Bridge</span>
                        <span className="font-medium text-foreground">{networkInspect.bridge_name || 'fc-br0'}</span>
                      </div>
                    </div>
                  </div>
                )}
              </div>
            )}

            {/* Section 4: Attached Volumes (Storage) */}
            {activeSection === 'storage' && (
              <div className="space-y-6">
                <div>
                  <h3 className="text-sm font-semibold text-foreground flex items-center gap-2">
                    <HardDrive className="h-4 w-4 text-primary" />
                    Attached Volumes &amp; Storage Layout
                  </h3>
                  <p className="text-xs text-muted-foreground mt-0.5">
                    Attach, inspect, and detach persistent block storage volumes.
                  </p>
                </div>

                {/* Volumes List */}
                <div className="border border-border bg-background p-4 space-y-3">
                  <div className="flex items-center justify-between">
                    <div className="text-xs font-semibold uppercase tracking-wider font-mono text-foreground">
                      Attached Persistent Volumes ({attachedVolumes.length})
                    </div>
                  </div>

                  {volumesLoading && attachedVolumes.length === 0 ? (
                    <div className="font-mono text-xs text-muted-foreground py-2">
                      Loading attached volumes…
                    </div>
                  ) : attachedVolumes.length === 0 ? (
                    <div className="text-xs text-muted-foreground py-2 border border-dashed border-border p-4 text-center">
                      No block volumes currently attached. Select an available unattached volume below to mount.
                    </div>
                  ) : (
                    <div className="divide-y divide-border border border-border">
                      {attachedVolumes.map((vol) => (
                        <div
                          key={vol.id}
                          className="flex items-center justify-between gap-3 px-3 py-2.5 font-mono text-xs"
                        >
                          <div className="min-w-0">
                            <div className="font-medium text-foreground truncate">
                              {vol.name}
                            </div>
                            <div className="text-[10.5px] text-muted-foreground">
                              {vol.size_gb} GB · {vol.filesystem} · mount: {vol.mount_point || '/mnt'}
                            </div>
                          </div>
                          <Button
                            type="button"
                            variant="outline"
                            size="sm"
                            onClick={() => void handleDetachVolume(vol)}
                            className="h-6.5 gap-1 text-[11px] hover:border-warning hover:text-warning"
                          >
                            <Unplug className="h-3 w-3" />
                            <span>Detach</span>
                          </Button>
                        </div>
                      ))}
                    </div>
                  )}

                  {/* Attach Volume Form (Modifiable on the go) */}
                  <div className="pt-3 border-t border-border">
                    <label className="block text-[11px] font-mono text-muted-foreground mb-1.5">
                      Attach Available Volume
                    </label>
                    {availableVolumes.length === 0 ? (
                      <p className="text-xs text-muted-foreground italic">
                        No unattached volumes found in this pool. Create new volumes from the Storage page.
                      </p>
                    ) : (
                      <form
                        onSubmit={(e) => void handleAttachVolume(e)}
                        className="flex items-center gap-2"
                      >
                        <select
                          aria-label="Attach volume"
                          value={attachVolumeId}
                          onChange={(e) => setAttachVolumeId(e.target.value)}
                          className="h-8 flex-1 border border-border bg-background px-2.5 font-mono text-xs text-foreground focus:border-primary focus:outline-none"
                        >
                          <option value="">Select available volume…</option>
                          {availableVolumes.map((vol) => (
                            <option key={vol.id} value={vol.id}>
                              {vol.name} ({vol.size_gb} GB · {vol.filesystem})
                            </option>
                          ))}
                        </select>
                        <Button
                          type="submit"
                          disabled={!attachVolumeId}
                          size="sm"
                          className="h-8 text-xs shrink-0"
                        >
                          Attach Volume
                        </Button>
                      </form>
                    )}
                  </div>
                </div>

                {/* Storage Layout & Paths */}
                <div className="border border-border bg-secondary/10 p-4 space-y-3 font-mono text-xs">
                  <div className="text-[10px] uppercase font-semibold text-muted-foreground">
                    Disk Storage Paths
                  </div>
                  <div className="space-y-2">
                    <div className="border border-border bg-background p-2.5">
                      <span className="text-[10px] uppercase text-muted-foreground block">
                        Rootfs Image
                      </span>
                      <span className="truncate block text-foreground mt-0.5">
                        {vm.rootfs_path || '/var/lib/compute/images/ubuntu.ext4'}
                      </span>
                    </div>
                    <div className="border border-border bg-background p-2.5">
                      <span className="text-[10px] uppercase text-muted-foreground block">
                        Overlay / Copy-on-Write Disk
                      </span>
                      <span className="truncate block text-foreground mt-0.5">
                        {vm.disk_path || 'Overlay ext4 allocated dynamically'}
                      </span>
                    </div>
                  </div>
                </div>
              </div>
            )}

            {/* Section 5: Worker & Placement */}
            {activeSection === 'placement' && (
              <div className="space-y-6">
                <div>
                  <h3 className="text-sm font-semibold text-foreground flex items-center gap-2">
                    <Server className="h-4 w-4 text-primary" />
                    Worker Node &amp; Pool Placement
                  </h3>
                  <p className="text-xs text-muted-foreground mt-0.5">
                    View active host node assignment, reassign worker pools, or execute microVM migration.
                  </p>
                </div>

                {/* Placement Information Card */}
                <div className="grid grid-cols-1 sm:grid-cols-2 gap-3 text-xs">
                  <div className="border border-border bg-background p-3.5 space-y-1">
                    <span className="text-[10px] uppercase font-mono text-muted-foreground">
                      Current Worker Host
                    </span>
                    <div className="font-mono font-medium text-foreground truncate">
                      {workerLabel}
                    </div>
                    <p className="text-[10.5px] text-muted-foreground mt-1">
                      Physical host executing this microVM Firecracker process.
                    </p>
                  </div>

                  <div className="border border-border bg-background p-3.5 space-y-1">
                    <span className="text-[10px] uppercase font-mono text-muted-foreground">
                      Assigned Worker Pool
                    </span>
                    <div className="font-mono font-medium text-foreground">
                      {vm.pool_id || 'default'}
                    </div>
                    <p className="text-[10.5px] text-muted-foreground mt-1">
                      Resource pool defining limits and scheduler priority.
                    </p>
                  </div>
                </div>

                {/* Reassign Worker Pool (Modifiable on the go) */}
                <div className="border border-border bg-background p-4 space-y-3">
                  <div className="text-xs font-semibold uppercase tracking-wider font-mono text-foreground">
                    Reassign Worker Pool
                  </div>
                  <p className="text-[11px] text-muted-foreground">
                    Change which worker pool this sandbox belongs to for scheduling and isolation policies.
                  </p>
                  <div className="flex items-center gap-2 pt-1">
                    <select
                      value={assignedPoolId}
                      onChange={(e) => setAssignedPoolId(e.target.value)}
                      className="h-8 flex-1 border border-border bg-background px-2.5 font-mono text-xs text-foreground focus:border-primary focus:outline-none"
                    >
                      <option value="default">default (Default Pool)</option>
                      {pools.map((p) => (
                        <option key={p.id} value={p.id}>
                          {p.name} ({p.id})
                        </option>
                      ))}
                    </select>
                    <Button
                      type="button"
                      size="sm"
                      disabled={isSavingPool || assignedPoolId === vm.pool_id}
                      onClick={() => void handleSavePool(assignedPoolId)}
                      className="h-8 text-xs shrink-0"
                    >
                      {isSavingPool ? 'Updating…' : 'Save Pool'}
                    </Button>
                  </div>
                </div>

                {/* Migration Form */}
                <div className="border border-border bg-background p-4 space-y-3">
                  <div className="text-xs font-semibold uppercase tracking-wider font-mono text-foreground flex items-center gap-1.5">
                    <ArrowRightLeft className="h-3.5 w-3.5 text-primary" />
                    <span>Migrate to Another Worker Host</span>
                  </div>
                  <p className="text-[11px] text-muted-foreground">
                    Move execution to a different cluster worker node.
                  </p>
                  <form onSubmit={(e) => void handleMigrateSubmit(e)} className="space-y-3 pt-1">
                    <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                      <div>
                        <label className="block text-[10.5px] font-mono text-muted-foreground mb-1">
                          Target Worker Node
                        </label>
                        <select
                          value={migrateWorkerId}
                          onChange={(e) => setMigrateWorkerId(e.target.value)}
                          className="h-8 w-full border border-border bg-background px-2 font-mono text-xs text-foreground"
                        >
                          <option value="">Automatic (Scheduler)</option>
                          {workers.map((w) => (
                            <option key={w.id} value={w.id}>
                              {w.name} ({w.id})
                            </option>
                          ))}
                        </select>
                      </div>
                      <div>
                        <label className="block text-[10.5px] font-mono text-muted-foreground mb-1">
                          Target Worker Pool
                        </label>
                        <select
                          value={migratePoolId}
                          onChange={(e) => setMigratePoolId(e.target.value)}
                          className="h-8 w-full border border-border bg-background px-2 font-mono text-xs text-foreground"
                        >
                          <option value="">Keep current pool</option>
                          {pools.map((p) => (
                            <option key={p.id} value={p.id}>
                              {p.name} ({p.id})
                            </option>
                          ))}
                        </select>
                      </div>
                    </div>

                    <div className="flex items-center justify-between pt-1">
                      <label className="flex items-center gap-2 text-xs text-muted-foreground cursor-pointer">
                        <input
                          type="checkbox"
                          checked={migrateForce}
                          onChange={(e) => setMigrateForce(e.target.checked)}
                          className="h-3.5 w-3.5 accent-primary"
                        />
                        <span>Force migration even if target node is under load</span>
                      </label>
                      <Button
                        type="submit"
                        disabled={isMigrating}
                        size="sm"
                        className="h-7 text-xs"
                      >
                        {isMigrating ? 'Migrating…' : 'Migrate Sandbox'}
                      </Button>
                    </div>
                  </form>
                </div>
              </div>
            )}

            {/* Section 6: Snapshots & Clones */}
            {activeSection === 'snapshots' && (
              <div className="space-y-6">
                <div>
                  <h3 className="text-sm font-semibold text-foreground flex items-center gap-2">
                    <Camera className="h-4 w-4 text-primary" />
                    Snapshots, Cloning &amp; Replication
                  </h3>
                  <p className="text-xs text-muted-foreground mt-0.5">
                    Capture point-in-time state or provision duplicate sandboxes.
                  </p>
                </div>

                <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
                  {/* Snapshot Card */}
                  <div className="border border-border bg-background p-4 space-y-3 flex flex-col justify-between">
                    <div>
                      <div className="text-xs font-semibold text-foreground flex items-center gap-1.5">
                        <Camera className="h-3.5 w-3.5 text-primary" />
                        <span>Take Snapshot</span>
                      </div>
                      <p className="text-[11px] text-muted-foreground mt-1 leading-relaxed">
                        Create an immutable snapshot of memory and rootfs state for fast restoration or template creation.
                      </p>
                    </div>
                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      onClick={() => {
                        onOpenChange(false);
                        onSnapshot?.(vm);
                      }}
                      className="h-7 text-xs w-full"
                    >
                      Create Snapshot
                    </Button>
                  </div>

                  {/* Clone Card */}
                  <div className="border border-border bg-background p-4 space-y-3 flex flex-col justify-between">
                    <div>
                      <div className="text-xs font-semibold text-foreground flex items-center gap-1.5">
                        <Layers className="h-3.5 w-3.5 text-primary" />
                        <span>Clone Sandbox</span>
                      </div>
                      <p className="text-[11px] text-muted-foreground mt-1 leading-relaxed">
                        Duplicate this sandbox with identical disk contents, custom replicas, or override resource specs.
                      </p>
                    </div>
                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      onClick={() => {
                        onOpenChange(false);
                        onClone?.(vm);
                      }}
                      className="h-7 text-xs w-full"
                    >
                      Clone Sandbox
                    </Button>
                  </div>
                </div>

                {/* Auto-hibernation info */}
                <div className="border border-border bg-secondary/10 p-4 space-y-2">
                  <div className="text-xs font-semibold uppercase tracking-wider font-mono text-foreground flex items-center gap-1.5">
                    <Info className="h-3.5 w-3.5 text-primary" />
                    <span>Memory Hibernation Policy</span>
                  </div>
                  <p className="text-xs text-muted-foreground leading-relaxed">
                    When inactive, microVMs can be auto-hibernated to disk to release physical host RAM.
                    {vm.auto_hibernated && (
                      <span className="block mt-1 font-mono text-warning">
                        Currently Hibernated: {vm.hibernate_reason || 'Memory pressure threshold reached'}.
                      </span>
                    )}
                  </p>
                </div>
              </div>
            )}

            {/* Section 7: Danger & Advanced */}
            {activeSection === 'danger' && (
              <div className="space-y-6">
                <div>
                  <h3 className="text-sm font-semibold text-foreground flex items-center gap-2">
                    <ShieldAlert className="h-4 w-4 text-destructive" />
                    Danger Zone &amp; Raw Specification
                  </h3>
                  <p className="text-xs text-muted-foreground mt-0.5">
                    View raw microVM machine definitions or permanently terminate this instance.
                  </p>
                </div>

                {/* Raw JSON Spec */}
                <div className="border border-border bg-background p-4 space-y-2">
                  <div className="flex items-center justify-between">
                    <div className="text-xs font-semibold uppercase tracking-wider font-mono text-foreground flex items-center gap-1.5">
                      <Server className="h-3.5 w-3.5 text-muted-foreground" />
                      <span>MicroVM Machine Specification (JSON)</span>
                    </div>
                    <CopyButton value={jsonSpec} label="Copy JSON Spec" />
                  </div>
                  <pre className="max-h-52 overflow-auto border border-border bg-secondary/30 p-3 font-mono text-[10.5px] leading-relaxed text-foreground">
                    {jsonSpec}
                  </pre>
                </div>

                {/* Danger Zone */}
                <div className="border border-destructive/40 bg-destructive/5 p-4 space-y-3">
                  <div className="text-xs font-semibold uppercase tracking-wider font-mono text-destructive flex items-center gap-1.5">
                    <AlertTriangle className="h-4 w-4" />
                    <span>Danger Zone</span>
                  </div>
                  <div className="flex items-center justify-between gap-4">
                    <div className="space-y-0.5">
                      <div className="text-xs font-medium text-foreground">
                        Delete Sandbox Permanently
                      </div>
                      <p className="text-[11px] text-muted-foreground">
                        {vm.protected
                          ? 'This sandbox is protected against deletion. To delete it, disable deletion protection first.'
                          : 'Deletes the microVM instance, overlay storage volume, and network configuration.'}
                      </p>
                    </div>
                    <Button
                      type="button"
                      variant="destructive"
                      size="sm"
                      disabled={vm.protected}
                      onClick={() => {
                        onOpenChange(false);
                        onDelete?.(vm);
                      }}
                      className="h-7 text-xs gap-1 shrink-0"
                    >
                      <Trash2 className="h-3 w-3" />
                      <span>{vm.protected ? 'Protected' : 'Delete Sandbox'}</span>
                    </Button>
                  </div>
                </div>
              </div>
            )}
          </main>
        </div>
      </DialogContent>
    </Dialog>
  );
}
