import { useEffect, useMemo, useState } from 'react';
import {
  ArrowRightLeft,
  Camera,
  Check,
  Cpu,
  ExternalLink,
  Globe,
  HardDrive,
  Layers,
  Loader2,
  Network,
  Pause,
  Play,
  Plus,
  RotateCcw,
  Scaling,
  Server,
  Shield,
  ShieldAlert,
  SlidersHorizontal,
  Square,
  Terminal,
  Trash2,
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
import {
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from '@/components/ui/tabs';
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

interface SandboxOptionsDialogProps {
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
  const [activeTab, setActiveTab] = useState('overview');
  const [networkInspect, setNetworkInspect] = useState<NetworkInspectResult | null>(null);
  const [restarting, setRestarting] = useState(false);

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

  // Load sandbox connectivity and network inspection on open
  useEffect(() => {
    if (!open || !vmId) return;

    let cancelled = false;

    // Initialize resize state
    if (vm) {
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

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[92vh] max-w-3xl overflow-hidden p-0 border border-border bg-background shadow-xl">
        {/* Header */}
        <DialogHeader className="border-b border-border bg-muted/15 p-5 pb-4">
          <div className="flex flex-wrap items-start justify-between gap-4 pr-6">
            <div className="flex items-center gap-3">
              <span className="flex h-10 w-10 shrink-0 items-center justify-center border border-border bg-background text-foreground shadow-xs">
                <SlidersHorizontal className="h-5 w-5 text-primary" />
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
                <DialogDescription className="mt-1 flex items-center gap-2 font-mono text-[11px] text-muted-foreground">
                  <span>ID: {vm.id}</span>
                  <CopyButton value={vm.id} label="Copy ID" className="h-4 w-4" />
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
                    <span>Open Terminal</span>
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

        {/* Tab Navigation & Content */}
        <div className="max-h-[calc(92vh-9.5rem)] overflow-y-auto p-5">
          <Tabs value={activeTab} onValueChange={setActiveTab}>
            <TabsList className="mb-4 w-full justify-start border-b border-border bg-transparent p-0">
              <TabsTrigger
                value="overview"
                className="gap-1.5 border-b-2 border-transparent px-3 py-2 text-xs data-[state=active]:border-primary data-[state=active]:text-foreground"
              >
                <Layers className="h-3.5 w-3.5" />
                <span>Overview</span>
              </TabsTrigger>
              <TabsTrigger
                value="specs"
                className="gap-1.5 border-b-2 border-transparent px-3 py-2 text-xs data-[state=active]:border-primary data-[state=active]:text-foreground"
              >
                <Cpu className="h-3.5 w-3.5" />
                <span>Compute Specs</span>
              </TabsTrigger>
              <TabsTrigger
                value="networking"
                className="gap-1.5 border-b-2 border-transparent px-3 py-2 text-xs data-[state=active]:border-primary data-[state=active]:text-foreground"
              >
                <Globe className="h-3.5 w-3.5" />
                <span>Port Forwards &amp; Ingress</span>
              </TabsTrigger>
              <TabsTrigger
                value="storage"
                className="gap-1.5 border-b-2 border-transparent px-3 py-2 text-xs data-[state=active]:border-primary data-[state=active]:text-foreground"
              >
                <HardDrive className="h-3.5 w-3.5" />
                <span>Attached Volumes</span>
              </TabsTrigger>
              <TabsTrigger
                value="actions"
                className="gap-1.5 border-b-2 border-transparent px-3 py-2 text-xs data-[state=active]:border-primary data-[state=active]:text-foreground"
              >
                <ShieldAlert className="h-3.5 w-3.5" />
                <span>Actions &amp; Danger</span>
              </TabsTrigger>
            </TabsList>

            {/* Tab 1: Overview */}
            <TabsContent value="overview" className="space-y-4 pt-1">
              {/* Connectivity Access Bar */}
              <div className="border border-border bg-secondary/15 p-4 space-y-3">
                <div className="text-xs font-semibold text-foreground uppercase tracking-wider font-mono">
                  Sandbox Connectivity
                </div>
                <div className="grid grid-cols-1 md:grid-cols-2 gap-3 text-xs">
                  {/* IP Address */}
                  <div className="border border-border bg-background p-2.5 space-y-1">
                    <span className="text-[10.5px] font-mono uppercase text-muted-foreground">
                      IP Address
                    </span>
                    <div className="flex items-center justify-between font-mono font-medium text-foreground">
                      <span>{vm.ip_address || 'Unassigned'}</span>
                      {vm.ip_address && (
                        <CopyButton value={vm.ip_address} label="Copy IP address" />
                      )}
                    </div>
                  </div>

                  {/* SSH Connection */}
                  <div className="border border-border bg-background p-2.5 space-y-1">
                    <span className="text-[10.5px] font-mono uppercase text-muted-foreground">
                      SSH Access
                    </span>
                    <div className="flex items-center justify-between font-mono text-[11px] text-foreground">
                      <span className="truncate">
                        {access.ssh || (vm.ip_address ? `ssh compute@${vm.ip_address}` : '—')}
                      </span>
                      {(access.ssh || vm.ip_address) && (
                        <CopyButton
                          value={access.ssh || `ssh compute@${vm.ip_address}`}
                          label="Copy SSH command"
                        />
                      )}
                    </div>
                  </div>
                </div>

                {/* Ingress URL */}
                {access.url && (
                  <div className="border border-border bg-background p-2.5 flex items-center justify-between gap-2">
                    <div className="min-w-0">
                      <span className="block text-[10.5px] font-mono uppercase text-muted-foreground">
                        Web Ingress URL
                      </span>
                      <a
                        href={access.url}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="font-mono text-xs text-[color:var(--link)] hover:underline truncate block"
                      >
                        {access.url}
                      </a>
                    </div>
                    <div className="flex items-center gap-1.5 shrink-0">
                      <CopyButton value={access.url} label="Copy Ingress URL" />
                      <a
                        href={access.url}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="inline-flex h-6 items-center gap-1 border border-border px-2 text-[10.5px] text-foreground hover:border-primary"
                      >
                        <ExternalLink className="h-3 w-3" />
                        <span>Open</span>
                      </a>
                    </div>
                  </div>
                )}
              </div>

              {/* Placement & Meta Details */}
              <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
                <div className="border border-border bg-background p-3">
                  <div className="text-[10px] uppercase font-mono text-muted-foreground">
                    Worker Node
                  </div>
                  <div className="mt-1 font-mono text-xs font-medium text-foreground truncate" title={workerLabel}>
                    {workerNode?.name || vm.worker_id || 'nod_host_local'}
                  </div>
                </div>
                <div className="border border-border bg-background p-3">
                  <div className="text-[10px] uppercase font-mono text-muted-foreground">
                    Worker Pool
                  </div>
                  <div className="mt-1 font-mono text-xs font-medium text-foreground">
                    {vm.pool_id || 'default'}
                  </div>
                </div>
                <div className="border border-border bg-background p-3">
                  <div className="text-[10px] uppercase font-mono text-muted-foreground">
                    Base Image
                  </div>
                  <div className="mt-1 font-mono text-xs font-medium text-foreground truncate">
                    {vm.snapshot_path ? vm.snapshot_path.split('/').pop() : 'Ubuntu 24.04'}
                  </div>
                </div>
                <div className="border border-border bg-background p-3">
                  <div className="text-[10px] uppercase font-mono text-muted-foreground">
                    Created
                  </div>
                  <div className="mt-1 font-mono text-xs font-medium text-muted-foreground">
                    {formatRelativeTime(vm.created_at)}
                  </div>
                </div>
              </div>

              {/* Deletion Protection Setting */}
              <div className="flex items-center justify-between border border-border bg-secondary/10 p-3.5">
                <div className="space-y-0.5">
                  <div className="flex items-center gap-1.5 text-xs font-medium text-foreground">
                    <Shield className="h-3.5 w-3.5 text-primary" />
                    <span>Deletion Protection</span>
                  </div>
                  <p className="text-[11px] text-muted-foreground">
                    {vm.protected
                      ? 'Enabled: Accidental deletion via UI or API is strictly blocked.'
                      : 'Disabled: Sandbox can be deleted anytime.'}
                  </p>
                </div>
                <Button
                  type="button"
                  size="sm"
                  variant={vm.protected ? 'outline' : 'default'}
                  onClick={() => onToggleProtection?.(vm)}
                  className="h-7 text-xs"
                >
                  {vm.protected ? 'Remove Protection' : 'Enable Protection'}
                </Button>
              </div>
            </TabsContent>

            {/* Tab 2: Compute Specs & Sizing */}
            <TabsContent value="specs" className="space-y-4 pt-1">
              {/* Current Compute Specs */}
              <div className="border border-border bg-secondary/15 p-4 space-y-3">
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-1.5 text-xs font-semibold text-foreground uppercase tracking-wider font-mono">
                    <Cpu className="h-3.5 w-3.5 text-primary" />
                    <span>Compute Specs</span>
                  </div>
                  <span className="font-mono text-xs font-medium text-primary">
                    {tierLabel}
                  </span>
                </div>

                <div className="grid grid-cols-2 sm:grid-cols-4 gap-3 font-mono text-xs">
                  <div className="border border-border bg-background p-3">
                    <span className="text-[10px] uppercase text-muted-foreground">vCPUs</span>
                    <div className="mt-1 text-sm font-semibold text-foreground">{vm.vcpus} vCPU</div>
                  </div>
                  <div className="border border-border bg-background p-3">
                    <span className="text-[10px] uppercase text-muted-foreground">Memory</span>
                    <div className="mt-1 text-sm font-semibold text-foreground">{formatMemory(vm.memory_mb)}</div>
                  </div>
                  <div className="border border-border bg-background p-3">
                    <span className="text-[10px] uppercase text-muted-foreground">Disk Storage</span>
                    <div className="mt-1 text-sm font-semibold text-foreground">{diskGb} GB</div>
                  </div>
                  <div className="border border-border bg-background p-3">
                    <span className="text-[10px] uppercase text-muted-foreground">Architecture</span>
                    <div className="mt-1 text-sm font-semibold text-foreground">x86_64</div>
                  </div>
                </div>
              </div>

              {/* In-place Scaling / Resize */}
              <div className="border border-border bg-background p-4 space-y-3.5">
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-1.5 text-xs font-medium text-foreground">
                    <Scaling className="h-3.5 w-3.5 text-primary" />
                    <span>Scale / Resize Sandbox</span>
                  </div>
                  <span className="text-[10.5px] text-muted-foreground">
                    Provisions updated allocation
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
                          'border p-2 text-left font-mono text-[11px] transition-colors',
                          isSelected
                            ? 'border-primary bg-primary/10 text-foreground font-medium'
                            : 'border-border bg-secondary/20 hover:border-border/80 text-muted-foreground hover:text-foreground',
                        )}
                      >
                        <div className="font-semibold text-foreground">{preset.label}</div>
                        <div>{preset.vcpu} vCPU · {preset.ram / 1024} GB</div>
                      </button>
                    );
                  })}
                </div>

                <form onSubmit={(e) => void handleResizeSubmit(e)} className="space-y-3 pt-1">
                  <div className="grid grid-cols-3 gap-3">
                    <div>
                      <label className="block text-[10px] font-mono uppercase text-muted-foreground mb-1">
                        vCPUs
                      </label>
                      <input
                        type="number"
                        min={1}
                        max={16}
                        value={resizeVcpus}
                        onChange={(e) => setResizeVcpus(Number(e.target.value))}
                        className="h-8 w-full border border-border bg-background px-2.5 font-mono text-xs"
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
                        className="h-8 w-full border border-border bg-background px-2.5 font-mono text-xs"
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
                        className="h-8 w-full border border-border bg-background px-2.5 font-mono text-xs"
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
            </TabsContent>

            {/* Tab 3: Networking & Port Forwards */}
            <TabsContent value="networking" className="space-y-4 pt-1">
              {/* Port Forwards List & Add Form */}
              <div className="border border-border bg-background p-4 space-y-3">
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-1.5">
                    <Globe className="h-3.5 w-3.5 text-primary" />
                    <span className="text-xs font-semibold uppercase tracking-wider font-mono text-foreground">
                      Port Forwards &amp; Ingress
                    </span>
                  </div>
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    onClick={() => setAddForwardOpen((v) => !v)}
                    className="h-6.5 gap-1 text-[11px]"
                  >
                    <Plus className="h-3 w-3" />
                    <span>Add Port Forward</span>
                  </Button>
                </div>

                {/* Ingress URL banner */}
                {access.url && (
                  <div className="flex items-center justify-between gap-2 border border-border bg-secondary/20 px-3 py-2 font-mono text-xs">
                    <div className="min-w-0">
                      <span className="text-[10px] uppercase text-muted-foreground block">
                        Live Web Ingress
                      </span>
                      <a
                        href={access.url}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="truncate text-[color:var(--link)] hover:underline block"
                      >
                        {access.url}
                      </a>
                    </div>
                    <CopyButton value={access.url} label="Copy Ingress URL" />
                  </div>
                )}

                {/* Port forward items */}
                {forwardsLoading && vmPortForwards.length === 0 ? (
                  <div className="font-mono text-xs text-muted-foreground py-2">
                    Loading port exposures…
                  </div>
                ) : vmPortForwards.length === 0 ? (
                  <div className="text-xs text-muted-foreground py-2">
                    No ports exposed yet. Forward guest ports to access web services or APIs.
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
                              ? `port :${pf.host_port} → :${pf.guest_port}`
                              : `subdomain → :${pf.guest_port}`}
                            <span className="ml-2 text-[10px] text-muted-foreground">
                              ({pf.protocol}, {pf.public ? 'public' : 'private'})
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

                {/* Add Forward Drawer Form */}
                {addForwardOpen && (
                  <form
                    onSubmit={(e) => void handleAddPortForward(e)}
                    className="space-y-3 border border-border bg-secondary/15 p-3.5 mt-2"
                  >
                    <div className="grid grid-cols-2 gap-3">
                      <div>
                        <label className="block text-[10.5px] font-mono text-muted-foreground mb-1">
                          Guest Port
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
                          <option value="HTTP/HTTPS">HTTP</option>
                          <option value="TCP">TCP</option>
                        </select>
                      </div>
                    </div>

                    {fwProtocol === 'HTTP/HTTPS' && (
                      <div>
                        <label className="block text-[10.5px] font-mono text-muted-foreground mb-1">
                          Optional Subdomain
                        </label>
                        <input
                          type="text"
                          value={fwSubdomain}
                          onChange={(e) => setFwSubdomain(e.target.value)}
                          placeholder="e.g. app, api, dashboard"
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
                      <span>Public Ingress URL (accessible outside cluster network)</span>
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

              {/* Public IP & Isolation Settings */}
              <div className="space-y-3 pt-1">
                <div className="flex items-center justify-between border border-border bg-secondary/15 p-3.5">
                  <div className="space-y-0.5">
                    <div className="text-xs font-medium text-foreground flex items-center gap-1.5">
                      <Globe className="h-3.5 w-3.5 text-primary" />
                      Dedicated Public IP
                    </div>
                    <p className="text-[11px] text-muted-foreground">
                      {vm.public_ip_enabled
                        ? 'Sandbox takes a dedicated IP on the main network.'
                        : 'Public IP is off. Sandbox is isolated from direct public routing.'}
                    </p>
                  </div>
                  <Button
                    type="button"
                    size="sm"
                    variant={vm.public_ip_enabled ? 'outline' : 'default'}
                    onClick={() => void handleTogglePublicIP()}
                    className="h-7 text-xs"
                  >
                    {vm.public_ip_enabled ? 'Turn Off Public IP' : 'Turn On Public IP'}
                  </Button>
                </div>

                <div className="flex items-center justify-between border border-border bg-secondary/15 p-3.5">
                  <div className="space-y-0.5">
                    <div className="text-xs font-medium text-foreground flex items-center gap-1.5">
                      <Network className="h-3.5 w-3.5 text-primary" />
                      Sandbox-to-Sandbox Network
                    </div>
                    <p className="text-[11px] text-muted-foreground">
                      {(vm.network_isolation || 'account') === 'isolated'
                        ? 'Isolated: Blocked from communicating with all other sandboxes.'
                        : 'Account: Can communicate with other non-isolated sandboxes in your account.'}
                    </p>
                  </div>
                  <Button
                    type="button"
                    size="sm"
                    variant={(vm.network_isolation || 'account') === 'isolated' ? 'default' : 'outline'}
                    onClick={() => void handleToggleNetworkIsolation()}
                    className="h-7 text-xs"
                  >
                    {(vm.network_isolation || 'account') === 'isolated'
                      ? 'Allow Account Sandboxes'
                      : 'Isolate Sandbox'}
                  </Button>
                </div>
              </div>
            </TabsContent>

            {/* Tab 4: Attached Volumes */}
            <TabsContent value="storage" className="space-y-4 pt-1">
              <div className="border border-border bg-background p-4 space-y-3">
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wider font-mono text-foreground">
                    <HardDrive className="h-3.5 w-3.5 text-primary" />
                    <span>Attached Volumes ({attachedVolumes.length})</span>
                  </div>
                </div>

                {volumesLoading && attachedVolumes.length === 0 ? (
                  <div className="font-mono text-xs text-muted-foreground py-2">
                    Loading volumes…
                  </div>
                ) : attachedVolumes.length === 0 ? (
                  <div className="text-xs text-muted-foreground py-2">
                    No block volumes attached to this sandbox. Attach an existing volume below.
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
                            {vol.size_gb} GB · {vol.filesystem} · {vol.mount_point || '/mnt'}
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

                {/* Attach Volume Form */}
                <div className="pt-2 border-t border-border">
                  <label className="block text-[11px] font-mono text-muted-foreground mb-1.5">
                    Attach Available Volume
                  </label>
                  {availableVolumes.length === 0 ? (
                    <p className="text-xs text-muted-foreground italic">
                      No unattached volumes available. Create volumes in the Storage section.
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
                        className="h-8 text-xs"
                      >
                        Attach Volume
                      </Button>
                    </form>
                  )}
                </div>
              </div>
            </TabsContent>

            {/* Tab 5: Actions & Danger Zone */}
            <TabsContent value="actions" className="space-y-4 pt-1">
              {/* Snapshot & Replicate */}
              <div className="border border-border bg-background p-4 space-y-3">
                <div className="text-xs font-semibold uppercase tracking-wider font-mono text-foreground">
                  Snapshots &amp; Replication
                </div>
                <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                  <div className="border border-border p-3 space-y-2">
                    <div className="text-xs font-medium text-foreground flex items-center gap-1.5">
                      <Camera className="h-3.5 w-3.5 text-primary" />
                      Take Snapshot
                    </div>
                    <p className="text-[11px] text-muted-foreground">
                      Capture an immutable image of this sandbox state.
                    </p>
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

                  <div className="border border-border p-3 space-y-2">
                    <div className="text-xs font-medium text-foreground flex items-center gap-1.5">
                      <Layers className="h-3.5 w-3.5 text-primary" />
                      Clone / Replicate
                    </div>
                    <p className="text-[11px] text-muted-foreground">
                      Provision exact duplicate sandboxes with custom replica count.
                    </p>
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
              </div>

              {/* Admin Migration */}
              {isAdmin && (
                <div className="border border-border bg-background p-4 space-y-3">
                  <div className="text-xs font-semibold uppercase tracking-wider font-mono text-foreground flex items-center gap-1.5">
                    <ArrowRightLeft className="h-3.5 w-3.5 text-primary" />
                    <span>Migrate to Another Worker</span>
                  </div>
                  <form onSubmit={(e) => void handleMigrateSubmit(e)} className="space-y-3">
                    <div className="grid grid-cols-2 gap-3">
                      <div>
                        <label className="block text-[10.5px] font-mono text-muted-foreground mb-1">
                          Target Worker Node
                        </label>
                        <select
                          value={migrateWorkerId}
                          onChange={(e) => setMigrateWorkerId(e.target.value)}
                          className="h-8 w-full border border-border bg-background px-2 font-mono text-xs"
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
                          className="h-8 w-full border border-border bg-background px-2 font-mono text-xs"
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

                    <div className="flex items-center justify-between">
                      <label className="flex items-center gap-2 text-xs text-muted-foreground cursor-pointer">
                        <input
                          type="checkbox"
                          checked={migrateForce}
                          onChange={(e) => setMigrateForce(e.target.checked)}
                          className="h-3.5 w-3.5 accent-primary"
                        />
                        <span>Force migration even if target is overloaded</span>
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
              )}

              {/* JSON Spec */}
              <div className="border border-border bg-background p-4 space-y-2">
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wider font-mono text-foreground">
                    <Server className="h-3.5 w-3.5 text-muted-foreground" />
                    <span>Raw JSON Specification</span>
                  </div>
                  <CopyButton value={jsonSpec} label="Copy JSON" />
                </div>
                <pre className="max-h-40 overflow-auto border border-border bg-secondary/30 p-2.5 font-mono text-[10.5px] leading-relaxed">
                  {jsonSpec}
                </pre>
              </div>

              {/* Danger Zone */}
              <div className="border border-destructive/40 bg-destructive/5 p-4 space-y-3">
                <div className="text-xs font-semibold uppercase tracking-wider font-mono text-destructive">
                  Danger Zone
                </div>
                <div className="flex items-center justify-between gap-3">
                  <div className="space-y-0.5">
                    <div className="text-xs font-medium text-foreground">
                      Delete Sandbox
                    </div>
                    <p className="text-[11px] text-muted-foreground">
                      {vm.protected
                        ? 'This sandbox is deletion protected. Remove protection first.'
                        : 'Permanently deletes this microVM, overlay disk, and network tap.'}
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
                    className="h-7 text-xs gap-1"
                  >
                    <Trash2 className="h-3 w-3" />
                    <span>{vm.protected ? 'Delete (Protected)' : 'Delete Sandbox'}</span>
                  </Button>
                </div>
              </div>
            </TabsContent>
          </Tabs>

          <div className="mt-4 pt-3 border-t border-border flex items-center justify-between font-mono text-[10.5px] text-muted-foreground">
            <span>Created: {formatDateTime(vm.created_at)}</span>
            <span>Last Updated: {formatDateTime(vm.updated_at)}</span>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
