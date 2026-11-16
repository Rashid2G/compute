import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  ArrowRightLeft,
  Camera,
  Check,
  ChevronDown,
  Copy,
  Cpu,
  ExternalLink,
  Globe,
  HardDrive,
  HelpCircle,
  MoreHorizontal,
  Pause,
  Play,
  Plus,
  RotateCcw,
  Scaling,
  Server,
  Shield,
  Square,
  Terminal,
  Trash2,
  Unplug,
  X,
} from 'lucide-react';
import { toast } from 'sonner';

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog';
import { BulkDeleteVMDialog } from '@/components/bulk-delete-vm-dialog';
import { CloneVMDialog } from '@/components/clone-vm-dialog';
import { CopyButton } from '@/components/copy-button';
import { CreateSnapshotDialog } from '@/components/create-snapshot-dialog';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { SSHTerminal, type SSHTerminalHandle } from '@/components/ssh-terminal';
import { UnprotectVMDialog } from '@/components/unprotect-vm-dialog';
import { api, errorMessage } from '@/lib/api';
import { formatMemory, formatRelativeTime } from '@/lib/format';
import { primarySandboxAddress, sandboxAccess } from '@/lib/sandbox-access';
import { openSSHTerminalWindow } from '@/lib/terminal';
import type {
  AuditEventRecord,
  PortForwardRecord,
  VM,
  VolumeRecord,
  WorkerNodeRecord,
  WorkerPoolRecord,
} from '@/lib/types';
import { cn } from '@/lib/utils';

interface InstancesPageProps {
  vms: VM[];
  workers?: WorkerNodeRecord[];
  pools?: WorkerPoolRecord[];
  volumes?: VolumeRecord[];
  portForwards?: PortForwardRecord[];
  events?: AuditEventRecord[];
  isLoading: boolean;
  isAdmin?: boolean;
  onNew: () => void;
  onRun: (vm: VM) => void;
  onStop: (vm: VM) => void;
  onSnapshot: (vm: VM) => void;
  onResume: (vm: VM) => void;
  onDelete: (vm: VM) => void;
  onBulkDelete?: (ids: string[]) => Promise<void>;
  onInspect: (vm: VM) => void;
  onConsole?: (vm: VM) => void;
  onRefresh?: () => void | Promise<void>;
}

const DEFAULT_DISK_MB = 20480; // 20 GB

function isVisibleSandbox(vm: VM): boolean {
  if (vm.owner_id === 'jobs') return false;
  if (vm.name?.startsWith('job-')) return false;
  return true;
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

export function InstancesPage({
  vms: rawVMs,
  workers = [],
  pools = [],
  volumes: propVolumes,
  portForwards: propPortForwards,
  isLoading,
  isAdmin = false,
  onNew,
  onRun,
  onStop,
  onSnapshot,
  onResume,
  onDelete,
  onBulkDelete,
  onInspect,
  onRefresh,
}: InstancesPageProps) {
  const vms = useMemo(() => rawVMs.filter(isVisibleSandbox), [rawVMs]);

  const workerNameById = useMemo(() => {
    const map: Record<string, string> = {};
    for (const w of workers) {
      map[w.id] = w.name;
    }
    return map;
  }, [workers]);

  const [search, setSearch] = useState('');
  const [stateFilter, setStateFilter] = useState<string>('all');
  const [snapshotFilter, setSnapshotFilter] = useState<string>('all');
  const [typeFilter, setTypeFilter] = useState<string>('all');
  const [selectedIds, setSelectedIds] = useState<string[]>([]);
  const [bulkDeleteOpen, setBulkDeleteOpen] = useState(false);
  const [unprotectOpen, setUnprotectOpen] = useState(false);
  const [unprotectTargets, setUnprotectTargets] = useState<VM[]>([]);
  const [copiedId, setCopiedId] = useState<string | null>(null);
  const [cloneTarget, setCloneTarget] = useState<VM | null>(null);
  const [snapshotTarget, setSnapshotTarget] = useState<VM | null>(null);
  const [stopConfirmTargets, setStopConfirmTargets] = useState<VM[] | null>(null);

  // Selected sandbox detail view state
  const [selectedVMId, setSelectedVMId] = useState<string | null>(null);
  const [terminalUser, setTerminalUser] = useState<'compute' | 'root'>('compute');
  const terminalRef = useRef<SSHTerminalHandle | null>(null);
  const [restartingId, setRestartingId] = useState<string | null>(null);

  // Resize modal state (provisions resized clone via POST /api/v1/vms/{id}/clone)
  const [resizeTarget, setResizeTarget] = useState<VM | null>(null);
  const [resizeName, setResizeName] = useState('');
  const [resizeVcpus, setResizeVcpus] = useState(2);
  const [resizeMemoryMb, setResizeMemoryMb] = useState(2048);
  const [resizeDiskGb, setResizeDiskGb] = useState(20);
  const [resizeAutoStart, setResizeAutoStart] = useState(true);

  // Migrate modal state
  const [migrateTarget, setMigrateTarget] = useState<VM | null>(null);
  const [migrateWorkerId, setMigrateWorkerId] = useState('');
  const [migratePoolId, setMigratePoolId] = useState('');
  const [migrateForce, setMigrateForce] = useState(false);

  // Detail cards state: Volumes & Port Forwards
  const [volumes, setVolumes] = useState<VolumeRecord[]>(propVolumes || []);
  const [volumesLoading, setVolumesLoading] = useState(false);
  const [attachVolumeId, setAttachVolumeId] = useState('');

  const [portForwards, setPortForwards] = useState<PortForwardRecord[]>(
    propPortForwards || [],
  );
  const [forwardsLoading, setForwardsLoading] = useState(false);
  const [addForwardOpen, setAddForwardOpen] = useState(false);
  const [fwHostPort, setFwHostPort] = useState('8080');
  const [fwGuestPort, setFwGuestPort] = useState('8080');
  const [fwProtocol, setFwProtocol] = useState<'TCP' | 'HTTP/HTTPS'>('TCP');
  const [fwSubdomain, setFwSubdomain] = useState('');

  useEffect(() => {
    if (propVolumes) setVolumes(propVolumes);
  }, [propVolumes]);

  useEffect(() => {
    if (propPortForwards) setPortForwards(propPortForwards);
  }, [propPortForwards]);

  const selectedVMs = useMemo(
    () => vms.filter((v) => selectedIds.includes(v.id)),
    [vms, selectedIds],
  );

  const selectedVM = useMemo(
    () => vms.find((v) => v.id === selectedVMId) ?? null,
    [vms, selectedVMId],
  );

  const selectedAccess = useMemo(() => sandboxAccess(selectedVM), [selectedVM]);

  const filteredVMs = useMemo(() => {
    return vms.filter((vm) => {
      const q = search.trim().toLowerCase();
      const access = sandboxAccess(vm);
      const workerName = vm.worker_id ? workerNameById[vm.worker_id] : '';
      const matchable = [
        vm.name,
        vm.id,
        vm.internal_id,
        vm.ip_address,
        vm.hostname,
        vm.worker_id,
        workerName,
        vm.pool_id,
        access.url,
        access.sshHost,
      ]
        .filter(Boolean)
        .join(' ')
        .toLowerCase();
      const matchesSearch = !q || matchable.includes(q);

      const matchesState =
        stateFilter === 'all' || vm.status.toLowerCase() === stateFilter;

      const hasSnap = Boolean(vm.snapshot_path);
      const matchesSnapshot =
        snapshotFilter === 'all' ||
        (snapshotFilter === 'has-snapshot' && hasSnap) ||
        (snapshotFilter === 'no-snapshot' && !hasSnap);

      const tierType = deriveTierType(vm.vcpus);
      const matchesType = typeFilter === 'all' || tierType === typeFilter;

      return matchesSearch && matchesState && matchesSnapshot && matchesType;
    });
  }, [vms, search, stateFilter, snapshotFilter, typeFilter, workerNameById]);

  const allSelected =
    filteredVMs.length > 0 && selectedIds.length === filteredVMs.length;

  const toggleAll = () => {
    if (allSelected) {
      setSelectedIds([]);
    } else {
      setSelectedIds(filteredVMs.map((r) => r.id));
    }
  };

  const toggleRow = (id: string) => {
    setSelectedIds((prev) =>
      prev.includes(id) ? prev.filter((item) => item !== id) : [...prev, id],
    );
  };

  const copyText = (text: string) => {
    void navigator.clipboard.writeText(text);
    setCopiedId(text);
    toast.success(`Copied ${text}`);
    setTimeout(() => setCopiedId(null), 1500);
  };

  const hasPropVolumesRef = useRef(propVolumes !== undefined);
  const hasPropPortForwardsRef = useRef(propPortForwards !== undefined);
  hasPropVolumesRef.current = propVolumes !== undefined;
  hasPropPortForwardsRef.current = propPortForwards !== undefined;

  const loadSandboxConnectivity = useCallback(async (force = false) => {
    const needVolumes = force || !hasPropVolumesRef.current;
    const needForwards = force || !hasPropPortForwardsRef.current;
    if (!needVolumes && !needForwards) return;

    if (needVolumes) setVolumesLoading(true);
    if (needForwards) setForwardsLoading(true);
    const [volsRes, pfsRes] = await Promise.allSettled([
      needVolumes ? api.listVolumes() : Promise.resolve(null),
      needForwards ? api.listPortForwards() : Promise.resolve(null),
    ]);
    if (needVolumes && volsRes.status === 'fulfilled' && volsRes.value) {
      setVolumes(volsRes.value || []);
    }
    if (needForwards && pfsRes.status === 'fulfilled' && pfsRes.value) {
      setPortForwards(pfsRes.value || []);
    }
    setVolumesLoading(false);
    setForwardsLoading(false);
  }, []);

  const selectedVMKey = selectedVM?.id ?? null;

  useEffect(() => {
    if (!selectedVMKey) return;
    setAddForwardOpen(false);
    setAttachVolumeId('');
    void loadSandboxConnectivity(false);
  }, [selectedVMKey, loadSandboxConnectivity]);

  function openMigrateModal(vm: VM) {
    setMigrateTarget(vm);
    setMigrateWorkerId('');
    setMigratePoolId(vm.pool_id || '');
    setMigrateForce(false);
  }

  function handleMigrateSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!migrateTarget) return;
    const src = migrateTarget;
    const vmName = src.name || src.id;
    const payload = {
      target_worker_id: migrateWorkerId || undefined,
      target_pool_id: migratePoolId || undefined,
      force: migrateForce,
    };
    setMigrateTarget(null);

    const toastID = toast.loading(`Migrating sandbox ${vmName}…`);
    void api
      .migrateVM(src.id, payload)
      .then(async () => {
        toast.success(`Migrated sandbox ${vmName}`, { id: toastID });
        await onRefresh?.();
      })
      .catch((err) => {
        toast.error(`Migration failed for ${vmName}`, {
          id: toastID,
          description: errorMessage(err),
        });
      });
  }

  function openResizeModal(vm: VM) {
    setResizeTarget(vm);
    setResizeName(`${vm.name}-resized`);
    setResizeVcpus(vm.vcpus || 2);
    setResizeMemoryMb(vm.memory_mb || 2048);
    setResizeDiskGb(Math.max(1, Math.round((vm.disk_mb || DEFAULT_DISK_MB) / 1024)));
    setResizeAutoStart(vm.status === 'running');
  }

  async function handleResizeSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!resizeTarget) return;
    const src = resizeTarget;
    const nextName =
      resizeName
        .trim()
        .toLowerCase()
        .replace(/[^a-z0-9-]+/g, '-')
        .replace(/^-+|-+$/g, '') || `${src.name}-resized`;
    setResizeTarget(null);

    const toastID = toast.loading(`Provisioning resized sandbox ${nextName}…`);
    try {
      const created = await api.cloneVM(src.id, {
        name: nextName,
        vcpus: resizeVcpus,
        memory_mb: resizeMemoryMb,
        disk_gb: resizeDiskGb,
        auto_start: resizeAutoStart,
      });
      toast.success(`Created resized sandbox ${created.name}`, { id: toastID });
      setSelectedVMId(created.id);
      await onRefresh?.();
    } catch (err) {
      toast.error(`Resize failed: ${errorMessage(err)}`, { id: toastID });
    }
  }

  async function handleRestartVM(vm: VM) {
    const vmName = vm.name || vm.id;
    setRestartingId(vm.id);
    const toastID = toast.loading(`Restarting ${vmName}…`);
    try {
      await api.restartVM(vm.id);
      toast.success(`${vmName} restarted`, { id: toastID });
      await onRefresh?.();
    } catch (err) {
      toast.error(`Could not restart ${vmName}: ${errorMessage(err)}`, {
        id: toastID,
      });
      await onRefresh?.();
    } finally {
      setRestartingId(null);
    }
  }

  async function handleAttachVolume(e: React.FormEvent) {
    e.preventDefault();
    if (!selectedVM || !attachVolumeId) return;
    const vol = volumes.find((v) => v.id === attachVolumeId);
    const toastID = toast.loading(`Attaching ${vol?.name || attachVolumeId}…`);
    try {
      await api.attachVolume(attachVolumeId, selectedVM.id);
      toast.success(`Attached ${vol?.name || attachVolumeId} to ${selectedVM.name}`, {
        id: toastID,
      });
      setAttachVolumeId('');
      await loadSandboxConnectivity(true);
    } catch (err) {
      toast.error(`Attach failed: ${errorMessage(err)}`, { id: toastID });
    }
  }

  async function handleDetachVolume(vol: VolumeRecord) {
    const toastID = toast.loading(`Detaching ${vol.name}…`);
    try {
      await api.detachVolume(vol.id);
      toast.success(`Detached ${vol.name}`, { id: toastID });
      await loadSandboxConnectivity(true);
    } catch (err) {
      toast.error(`Detach failed: ${errorMessage(err)}`, { id: toastID });
    }
  }

  async function handleAddPortForward(e: React.FormEvent) {
    e.preventDefault();
    if (!selectedVM) return;
    const hostPort = Number(fwHostPort);
    const guestPort = Number(fwGuestPort);
    if (!hostPort || !guestPort) {
      toast.error('Host and guest ports are required');
      return;
    }
    const toastID = toast.loading(`Adding port forward :${hostPort} → :${guestPort}…`);
    try {
      await api.createPortForward({
        host_port: hostPort,
        guest_port: guestPort,
        vm_id: selectedVM.id,
        vm_name: selectedVM.name,
        protocol: fwProtocol,
        subdomain: fwSubdomain.trim() || undefined,
      });
      toast.success(`Forwarding :${hostPort} → ${selectedVM.name}:${guestPort}`, {
        id: toastID,
      });
      setAddForwardOpen(false);
      setFwSubdomain('');
      await loadSandboxConnectivity(true);
    } catch (err) {
      toast.error(`Failed to add forward: ${errorMessage(err)}`, { id: toastID });
    }
  }

  async function handleDeletePortForward(pf: PortForwardRecord) {
    const toastID = toast.loading(`Removing port forward :${pf.host_port}…`);
    try {
      await api.deletePortForward(pf.id);
      toast.success(`Removed port forward :${pf.host_port}`, { id: toastID });
      await loadSandboxConnectivity(true);
    } catch (err) {
      toast.error(`Failed to remove forward: ${errorMessage(err)}`, { id: toastID });
    }
  }

  async function confirmStop() {
    const targets = stopConfirmTargets ?? [];
    if (targets.length === 0) return;
    setStopConfirmTargets(null);
    setSelectedIds((prev) =>
      prev.filter((id) => !targets.some((vm) => vm.id === id)),
    );
    if (targets.length === 1) {
      await Promise.resolve(onStop(targets[0])).catch(() => {});
      return;
    }
    const toastID = toast.loading(`Queueing stop for ${targets.length} sandboxes…`);
    try {
      const res = await api.bulkActionVMs({
        ids: targets.map((vm) => vm.id),
        action: 'stop',
        async: true,
      });
      if (res.status === 'queued') {
        toast.success(`Queued stop for ${res.queued ?? targets.length} sandboxes`, {
          id: toastID,
        });
      } else if (res.failed.length > 0) {
        toast.warning(
          `Stopped ${res.succeeded.length} of ${res.requested} sandboxes`,
          { id: toastID, description: res.failed.map((f) => `${f.id}: ${f.error}`).join('; ') },
        );
      } else {
        toast.success(`Stopped ${res.succeeded.length} sandboxes`, { id: toastID });
      }
      await onRefresh?.();
    } catch (err) {
      toast.error(`Stop failed: ${errorMessage(err)}`, { id: toastID });
    }
  }

  async function handleBatchStart() {
    const toStart = selectedIds
      .map((id) => vms.find((v) => v.id === id))
      .filter((vm): vm is VM => !!vm && (vm.status === 'stopped' || vm.status === 'failed'));
    if (toStart.length === 0) {
      toast.info('None of the selected sandboxes can be started.');
      return;
    }
    setSelectedIds([]);
    if (toStart.length === 1) {
      await onRun(toStart[0]);
      return;
    }
    const toastID = toast.loading(`Queueing start for ${toStart.length} sandboxes…`);
    try {
      const res = await api.bulkActionVMs({
        ids: toStart.map((vm) => vm.id),
        action: 'start',
        async: true,
      });
      if (res.status === 'queued') {
        toast.success(`Queued start for ${res.queued ?? toStart.length} sandboxes`, {
          id: toastID,
        });
      } else if (res.failed.length > 0) {
        toast.warning(
          `Started ${res.succeeded.length} of ${res.requested} sandboxes`,
          { id: toastID, description: res.failed.map((f) => `${f.id}: ${f.error}`).join('; ') },
        );
      } else {
        toast.success(`Started ${res.succeeded.length} sandboxes`, { id: toastID });
      }
      await onRefresh?.();
    } catch (err) {
      toast.error(`Start failed: ${errorMessage(err)}`, { id: toastID });
    }
  }

  function requestBatchStop() {
    const toStop = selectedIds
      .map((id) => vms.find((v) => v.id === id))
      .filter((vm): vm is VM => !!vm && vm.status === 'running');
    if (toStop.length === 0) {
      toast.info('None of the selected sandboxes are running.');
      return;
    }
    setStopConfirmTargets(toStop);
  }

  async function handleBatchSnapshot() {
    const toSnap = selectedIds
      .map((id) => vms.find((v) => v.id === id))
      .filter((vm): vm is VM => !!vm && vm.status === 'running');
    if (toSnap.length === 0) {
      toast.info('Only running sandboxes can be snapshotted.');
      return;
    }
    setSelectedIds([]);
    if (toSnap.length === 1) {
      await onSnapshot(toSnap[0]);
      return;
    }
    const toastID = toast.loading(`Queueing pause/snapshot for ${toSnap.length} sandboxes…`);
    try {
      const res = await api.bulkActionVMs({
        ids: toSnap.map((vm) => vm.id),
        action: 'snapshot',
        async: true,
      });
      if (res.status === 'queued') {
        toast.success(`Queued pause for ${res.queued ?? toSnap.length} sandboxes`, {
          id: toastID,
        });
      } else if (res.failed.length > 0) {
        toast.warning(
          `Paused ${res.succeeded.length} of ${res.requested} sandboxes`,
          { id: toastID, description: res.failed.map((f) => `${f.id}: ${f.error}`).join('; ') },
        );
      } else {
        toast.success(`Paused ${res.succeeded.length} sandboxes`, { id: toastID });
      }
      await onRefresh?.();
    } catch (err) {
      toast.error(`Snapshot failed: ${errorMessage(err)}`, { id: toastID });
    }
  }

  async function handleBatchClone() {
    if (selectedIds.length === 0) return;
    const idsToClone = [...selectedIds];
    setSelectedIds([]);
    if (idsToClone.length === 1) {
      const id = idsToClone[0];
      const source = vms.find((v) => v.id === id);
      const cloneName = source
        ? `${source.name}-clone`
        : `sbx-clone-${id.slice(0, 6)}`;
      const toastID = toast.loading(`Cloning ${source?.name || id}…`);
      try {
        await api.cloneVM(id, {
          name: cloneName,
          vcpus: source?.vcpus || 2,
          memory_mb: source?.memory_mb,
        });
        toast.success(`Cloned ${cloneName}`, { id: toastID });
        await onRefresh?.();
      } catch (err) {
        toast.error(`Clone failed: ${errorMessage(err)}`, { id: toastID });
        await onRefresh?.();
      }
      return;
    }

    const toastID = toast.loading(`Queueing clone of ${idsToClone.length} sandboxes…`);
    try {
      const items = idsToClone.map((id) => {
        const source = vms.find((v) => v.id === id);
        const cloneName = source
          ? `${source.name}-clone`
          : `sbx-clone-${id.slice(0, 6)}`;
        return {
          source_id: id,
          name: cloneName,
          vcpus: source?.vcpus || 2,
          memory_mb: source?.memory_mb,
        };
      });
      const res = await api.bulkCloneVMs({ items, async: true });
      if (res.status === 'queued') {
        toast.success(`Queued clone of ${res.queued ?? idsToClone.length} sandboxes`, {
          id: toastID,
        });
      } else if (res.failed.length > 0) {
        toast.warning(
          `Cloned ${res.succeeded.length} of ${res.requested} sandboxes`,
          { id: toastID, description: res.failed.map((f) => `${f.id}: ${f.error}`).join('; ') },
        );
      } else {
        toast.success(`Successfully cloned ${res.succeeded.length} sandboxes`, {
          id: toastID,
        });
      }
      await onRefresh?.();
    } catch (err) {
      toast.error(`Clone failed: ${errorMessage(err)}`, { id: toastID });
      await onRefresh?.();
    }
  }

  function requestUnprotect(targets: VM[]) {
    const protectedTargets = targets.filter((vm) => vm.protected);
    if (protectedTargets.length === 0) return;
    setUnprotectTargets(protectedTargets);
    setUnprotectOpen(true);
  }

  function handleBatchUnprotect() {
    const protectedVMs = selectedVMs.filter((vm) => vm.protected);
    if (protectedVMs.length === 0) {
      toast.error('No selected sandboxes are deletion protected.');
      return;
    }
    requestUnprotect(protectedVMs);
  }

  async function confirmUnprotect(ids: string[]) {
    const targets = ids
      .map((id) => vms.find((vm) => vm.id === id))
      .filter((vm): vm is VM => !!vm);
    const toastID = toast.loading(
      targets.length === 1
        ? 'Disabling protection…'
        : `Queueing protection removal for ${targets.length} sandboxes…`,
    );
    try {
      if (ids.length === 1) {
        await api.unprotectVM(ids[0]);
        toast.success(`Sandbox ${targets[0]?.name || ids[0]} protection disabled`, {
          id: toastID,
        });
      } else {
        const res = await api.bulkActionVMs({
          ids,
          action: 'unprotect',
          async: true,
        });
        if (res.status === 'queued') {
          toast.success(
            `Queued protection removal for ${res.queued ?? ids.length} sandboxes`,
            { id: toastID },
          );
        } else {
          toast.success(`Protection disabled for ${res.succeeded.length} sandboxes`, {
            id: toastID,
          });
        }
      }
      onRefresh?.();
    } catch (err) {
      toast.error(`Failed to update protection: ${errorMessage(err)}`, {
        id: toastID,
      });
      throw err;
    }
  }

  async function toggleProtection(targetVM: VM) {
    if (targetVM.protected) {
      requestUnprotect([targetVM]);
      return;
    }

    const toastID = toast.loading('Enabling protection…');
    try {
      await api.protectVM(targetVM.id);
      toast.success(`Sandbox ${targetVM.name} is now deletion protected`, {
        id: toastID,
      });
      onRefresh?.();
    } catch (err) {
      toast.error(`Failed to update protection: ${errorMessage(err)}`, {
        id: toastID,
      });
    }
  }

  async function executeBulkDelete(ids: string[]) {
    const protectedIds = ids.filter((id) => vms.find((v) => v.id === id)?.protected);
    if (protectedIds.length > 0) {
      toast.error(
        `Cannot delete ${protectedIds.length} protected sandbox(es). Unprotect them first.`,
      );
      ids = ids.filter((id) => !vms.find((v) => v.id === id)?.protected);
      if (ids.length === 0) {
        setSelectedIds([]);
        return;
      }
    }
    setSelectedIds([]);
    if (onBulkDelete) {
      await onBulkDelete(ids);
    } else {
      const toastID = toast.loading(`Queueing deletion of ${ids.length} sandbox(es)…`);
      try {
        const res = await api.bulkDeleteVMs(ids, { async: true });
        if (res.status === 'queued') {
          toast.success(`Queued deletion of ${res.queued ?? ids.length} sandbox(es)`, {
            id: toastID,
          });
        } else {
          toast.success(`Deleted ${res.succeeded.length} sandbox(es)`, { id: toastID });
        }
        await onRefresh?.();
      } catch (err) {
        toast.error(`Delete failed: ${errorMessage(err)}`, { id: toastID });
        throw err;
      }
    }
  }

  // Volumes & Port Forwards filtered for selected sandbox
  const attachedVolumes = useMemo(() => {
    if (!selectedVM) return [];
    return volumes.filter(
      (vol) =>
        !vol.id.startsWith('vol_root_') &&
        (vol.attached_vm === selectedVM.name || vol.attached_vm === selectedVM.id),
    );
  }, [volumes, selectedVM]);

  const availableVolumes = useMemo(
    () =>
      volumes.filter(
        (vol) => !vol.id.startsWith('vol_root_') && vol.status === 'available',
      ),
    [volumes],
  );

  const vmPortForwards = useMemo(() => {
    if (!selectedVM) return [];
    return portForwards.filter(
      (pf) => pf.vm_id === selectedVM.id || pf.vm_name === selectedVM.name,
    );
  }, [portForwards, selectedVM]);

  return (
    <div className="space-y-4">
      {/* Top Page Title & Single [ ▷ New ] Button */}
      <div className="flex items-center justify-between">
        <h1 className="font-display text-[22px] font-medium tracking-tight text-foreground">
          Sandboxes
        </h1>

        <button
          type="button"
          onClick={onNew}
          className="inline-flex h-8 items-center gap-1.5 bg-primary px-3.5 text-[12px] font-medium text-primary-foreground hover:bg-primary-hover transition-colors cursor-pointer"
        >
          <Play className="h-3 w-3" />
          <span>New</span>
        </button>
      </div>

      {/* Filter Bar */}
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative w-full sm:w-64">
          <input
            type="text"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search name, URL, hostname, id"
            className="h-8 w-full border border-border bg-background px-3 text-[12px] text-foreground placeholder:text-muted-foreground focus:border-primary focus:outline-none"
          />
        </div>

        <div className="relative">
          <select
            aria-label="Filter by state"
            value={stateFilter}
            onChange={(e) => setStateFilter(e.target.value)}
            className="h-8 appearance-none border border-border bg-background pl-2.5 pr-7 text-[12px] text-muted-foreground hover:text-foreground focus:border-primary focus:outline-none cursor-pointer"
          >
            <option value="all">State</option>
            <option value="running">Running</option>
            <option value="paused">Paused (State Saved)</option>
            <option value="stopped">Stopped</option>
            <option value="queued">Queued</option>
            <option value="failed">Failed</option>
          </select>
          <ChevronDown className="pointer-events-none absolute right-2 top-1/2 h-3 w-3 -translate-y-1/2 text-muted-foreground" />
        </div>

        <div className="relative">
          <select
            aria-label="Filter by snapshot"
            value={snapshotFilter}
            onChange={(e) => setSnapshotFilter(e.target.value)}
            className="h-8 appearance-none border border-border bg-background pl-2.5 pr-7 text-[12px] text-muted-foreground hover:text-foreground focus:border-primary focus:outline-none cursor-pointer"
          >
            <option value="all">Snapshot</option>
            <option value="has-snapshot">Has snapshot</option>
            <option value="no-snapshot">No snapshot</option>
          </select>
          <ChevronDown className="pointer-events-none absolute right-2 top-1/2 h-3 w-3 -translate-y-1/2 text-muted-foreground" />
        </div>

        <div className="relative">
          <select
            aria-label="Filter by tier type"
            value={typeFilter}
            onChange={(e) => setTypeFilter(e.target.value)}
            className="h-8 appearance-none border border-border bg-background pl-2.5 pr-7 text-[12px] text-muted-foreground hover:text-foreground focus:border-primary focus:outline-none cursor-pointer"
          >
            <option value="all">Type</option>
            <option value="small">small (2 vCPU)</option>
            <option value="default">default (4 vCPU)</option>
            <option value="large">large (8 vCPU)</option>
            <option value="xlarge">xlarge (16 vCPU)</option>
          </select>
          <ChevronDown className="pointer-events-none absolute right-2 top-1/2 h-3 w-3 -translate-y-1/2 text-muted-foreground" />
        </div>

        {selectedIds.length > 0 && (
          <div className="ml-auto flex flex-wrap items-center gap-1.5 border border-primary/60 bg-selected/60 px-2.5 py-1.5 shadow-sm">
            <span className="mr-1 inline-flex items-center gap-1.5 font-mono text-[11px] text-foreground">
              <span className="h-1.5 w-1.5 bg-primary" />
              {selectedIds.length} selected
            </span>
            <span className="h-4 w-px bg-border" />

            <button
              type="button"
              onClick={() => void handleBatchStart()}
              title="Start all selected stopped sandboxes"
              className="inline-flex h-6.5 items-center gap-1.5 border border-border bg-background px-2.5 text-[11px] font-medium text-foreground hover:border-success hover:text-success transition-colors cursor-pointer"
            >
              <Play className="h-3 w-3" />
              <span>Start</span>
            </button>
            <button
              type="button"
              onClick={requestBatchStop}
              title="Stop all selected running sandboxes"
              className="inline-flex h-6.5 items-center gap-1.5 border border-border bg-background px-2.5 text-[11px] font-medium text-foreground hover:border-warning hover:text-warning transition-colors cursor-pointer"
            >
              <Square className="h-3 w-3" />
              <span>Stop</span>
            </button>
            <button
              type="button"
              onClick={() => void handleBatchSnapshot()}
              title="Pause & save state for selected running sandboxes"
              className="inline-flex h-6.5 items-center gap-1.5 border border-border bg-background px-2.5 text-[11px] font-medium text-foreground hover:border-primary hover:text-primary dark:hover:text-[var(--accent-bright)] transition-colors cursor-pointer"
            >
              <Pause className="h-3 w-3" />
              <span>Pause</span>
            </button>
            <button
              type="button"
              onClick={() => void handleBatchClone()}
              title="Replicate all selected sandboxes"
              className="inline-flex h-6.5 items-center gap-1.5 border border-border bg-background px-2.5 text-[11px] font-medium text-foreground hover:border-primary hover:text-primary dark:hover:text-[var(--accent-bright)] transition-colors cursor-pointer"
            >
              <Copy className="h-3 w-3" />
              <span>Replicate</span>
            </button>
            {selectedVMs.some((vm) => vm.protected) && (
              <button
                type="button"
                onClick={handleBatchUnprotect}
                title="Remove deletion protection from selected sandboxes"
                className="inline-flex h-6.5 items-center gap-1.5 border border-border bg-background px-2.5 text-[11px] font-medium text-primary dark:text-[var(--accent-bright)] hover:border-primary transition-colors cursor-pointer"
              >
                <Shield className="h-3 w-3" />
                <span>Unprotect</span>
              </button>
            )}

            <span className="h-4 w-px bg-border" />
            <button
              type="button"
              onClick={() => setBulkDeleteOpen(true)}
              title="Delete all selected sandboxes"
              className="inline-flex h-6.5 items-center gap-1.5 border border-destructive/40 bg-destructive/10 px-2.5 text-[11px] font-medium text-destructive hover:bg-destructive/20 transition-colors cursor-pointer"
            >
              <Trash2 className="h-3 w-3" />
              <span>Delete</span>
            </button>
            <button
              type="button"
              onClick={() => setSelectedIds([])}
              title="Clear selection"
              aria-label="Clear selection"
              className="inline-flex h-6.5 w-6.5 items-center justify-center text-muted-foreground hover:text-foreground transition-colors cursor-pointer"
            >
              <X className="h-3.5 w-3.5" />
            </button>
          </div>
        )}
      </div>

      {/* Sandboxes Table */}
      <div className="border border-border bg-background">
        <div className="overflow-x-auto">
          <table className="w-full border-collapse text-left">
            <thead>
              <tr className="border-b border-border bg-muted/30 text-[11px] font-normal text-muted-foreground whitespace-nowrap">
                <th className="w-7 py-2 pl-2.5 pr-1">
                  <input
                    type="checkbox"
                    aria-label="Select all sandboxes"
                    checked={allSelected}
                    onChange={toggleAll}
                    className="h-3.5 w-3.5 accent-primary cursor-pointer"
                  />
                </th>
                <th className="py-2 px-1 font-normal">Name / URL</th>
                <th className="py-2 px-1 font-normal">Sandbox ID</th>
                <th className="py-2 px-1 font-normal">State</th>
                <th className="py-2 px-1 font-normal">Node / Pool</th>
                <th className="py-2 px-1 font-normal">Created</th>
                <th className="py-2 px-1 font-normal">Type</th>
                <th className="py-2 px-1 font-normal">
                  <span className="inline-flex items-center gap-0.5">
                    Auto-stop
                    <HelpCircle className="h-3 w-3 text-muted-foreground/70" />
                  </span>
                </th>
                <th className="py-2 px-1 font-normal">Snapshot</th>
                <th className="py-2 px-1 font-normal">
                  <span className="inline-flex items-center gap-0.5">
                    Resources
                    <HelpCircle className="h-3 w-3 text-muted-foreground/70" />
                  </span>
                </th>
                <th className="py-2 px-1 font-normal">
                  <span className="inline-flex items-center gap-0.5">
                    IP Address
                    <HelpCircle className="h-3 w-3 text-muted-foreground/70" />
                  </span>
                </th>
                <th className="py-2 px-1 font-normal">
                  <span className="inline-flex items-center gap-0.5">
                    URL / SSH
                    <HelpCircle className="h-3 w-3 text-muted-foreground/70" />
                  </span>
                </th>
                <th className="py-2 pl-1.5 pr-2.5 text-right font-normal">
                  Actions
                </th>
              </tr>
            </thead>

            {filteredVMs.length === 0 ? (
              <tbody>
                <tr>
                  <td colSpan={13} className="py-20 text-center">
                    <div className="mx-auto flex max-w-xs flex-col items-center justify-center space-y-3.5">
                      <svg
                        viewBox="0 0 120 110"
                        className="h-28 w-32 text-primary"
                        fill="currentColor"
                        shapeRendering="crispEdges"
                        aria-hidden="true"
                      >
                        <defs>
                          <pattern
                            id="compute-dots"
                            x="0"
                            y="0"
                            width="4"
                            height="4"
                            patternUnits="userSpaceOnUse"
                          >
                            <rect
                              x="0"
                              y="0"
                              width="2.2"
                              height="2.2"
                              fill="currentColor"
                              opacity="0.7"
                            />
                          </pattern>
                        </defs>
                        <polygon points="60,12 104,34 60,56 16,34" fill="url(#compute-dots)" />
                        <polygon points="16,38 58,59 58,98 16,77" fill="url(#compute-dots)" />
                        <polygon points="104,38 62,59 62,98 104,77" fill="url(#compute-dots)" />
                      </svg>

                      <p className="text-[12.5px] text-foreground/90">
                        {isLoading ? 'Connecting to compute daemon…' : 'No sandboxes yet.'}
                      </p>

                      <button
                        type="button"
                        onClick={onNew}
                        className="inline-flex h-8 items-center gap-1.5 bg-primary px-3.5 text-[12px] font-medium text-primary-foreground hover:bg-primary-hover transition-colors"
                      >
                        <Play className="h-3 w-3" />
                        <span>New</span>
                      </button>
                    </div>
                  </td>
                </tr>
              </tbody>
            ) : (
              <tbody className="divide-y divide-border text-[11px] whitespace-nowrap">
                {filteredVMs.map((vm) => {
                  const isChecked = selectedIds.includes(vm.id);
                  const isSelected = selectedVM?.id === vm.id;
                  const access = sandboxAccess(vm);
                  const addressLabel = access.urlHost || access.sshHost || '—';
                  const addressHint = access.urlResolvable
                    ? access.urlNote
                    : access.sshNote || access.urlNote;
                  const primaryAddress = primarySandboxAddress(access);
                  const snapTag = vm.snapshot_path
                    ? vm.snapshot_path.split('/').pop() || 'snapshot'
                    : 'ubuntu-24';
                  const tierLabel = deriveTierLabel(vm.vcpus, vm.memory_mb);
                  const diskGb = Math.max(
                    1,
                    Math.round((vm.disk_mb || DEFAULT_DISK_MB) / 1024),
                  );
                  const resourcesStr = `${vm.vcpus} vCPU · ${formatMemory(vm.memory_mb)} · ${diskGb} GB`;
                  const shortId = (vm.internal_id || vm.id).slice(0, 12);
                  const nodeName =
                    (vm.worker_id && workerNameById[vm.worker_id]) ||
                    vm.worker_id ||
                    'local';
                  const poolLabel = vm.pool_id || 'default';

                  return (
                    <tr
                      key={vm.id}
                      onClick={() => setSelectedVMId(vm.id)}
                      className={cn(
                        'cursor-pointer transition-colors',
                        isSelected ? 'bg-selected/75' : 'hover:bg-muted/40',
                      )}
                    >
                      <td
                        className="py-2 pl-2.5 pr-1"
                        onClick={(e) => e.stopPropagation()}
                      >
                        <input
                          type="checkbox"
                          aria-label={`Select ${vm.name}`}
                          checked={isChecked}
                          onChange={() => toggleRow(vm.id)}
                          className="h-3.5 w-3.5 accent-primary cursor-pointer"
                        />
                      </td>

                      <td className="py-2 px-1.5">
                        <div className="flex items-center gap-1.5 font-medium text-foreground">
                          <span>{vm.name}</span>
                          {vm.protected && (
                            <span
                              title="Deletion protected"
                              className="inline-flex items-center gap-0.5 border border-primary/40 bg-primary/10 px-1 py-0.5 text-[9px] font-mono text-primary dark:text-[var(--accent-bright)] uppercase"
                            >
                              <Shield className="h-2.5 w-2.5" />
                              prot
                            </span>
                          )}
                        </div>
                        <div
                          className="flex items-center gap-1 font-mono text-[10px] text-muted-foreground"
                          title={addressHint}
                        >
                          <span className="max-w-[220px] truncate">{addressLabel}</span>
                          {primaryAddress ? (
                            <CopyButton
                              value={primaryAddress}
                              label={access.url ? 'Copy URL' : 'Copy SSH name'}
                              className="h-4 w-4"
                            />
                          ) : null}
                          {access.sshHost && !access.sshResolvable ? (
                            <span
                              className="text-warning"
                              aria-label="DNS delegation required"
                            >
                              DNS
                            </span>
                          ) : null}
                        </div>
                      </td>

                      <td
                        className="py-2 px-1.5 font-mono text-[10.5px]"
                        onClick={(e) => e.stopPropagation()}
                      >
                        <button
                          type="button"
                          onClick={() => copyText(vm.internal_id || vm.id)}
                          className="inline-flex items-center gap-1 border border-border bg-muted/40 px-1.5 py-0.5 text-foreground hover:border-primary transition-colors"
                          title={`Copy ${vm.internal_id || vm.id}`}
                        >
                          <span>{shortId}</span>
                          {copiedId === (vm.internal_id || vm.id) ? (
                            <Check className="h-2.5 w-2.5 text-success" />
                          ) : (
                            <Copy className="h-2.5 w-2.5 text-muted-foreground" />
                          )}
                        </button>
                      </td>

                      <td className="py-2 px-1.5">
                        <div className="flex items-center gap-1">
                          <span
                            className={cn(
                              'inline-flex items-center gap-1 border px-1.5 py-0.5 font-mono text-[9.5px] uppercase tracking-wider',
                              vm.status === 'running' &&
                                'border-success/40 bg-success/10 text-success',
                              vm.status === 'paused' &&
                                'border-warning/40 bg-warning/10 text-warning',
                              (vm.status === 'stopped' || vm.status === 'created') &&
                                'border-border bg-muted/40 text-muted-foreground',
                              vm.status === 'failed' &&
                                'border-destructive/40 bg-destructive/10 text-destructive',
                            )}
                          >
                            <span
                              className={cn(
                                'h-1.5 w-1.5',
                                vm.status === 'running' && 'bg-success',
                                vm.status === 'paused' && 'bg-warning',
                                (vm.status === 'stopped' || vm.status === 'created') &&
                                  'bg-muted-foreground',
                                vm.status === 'failed' && 'bg-destructive',
                              )}
                            />
                            {vm.status === 'paused' ? 'PAUSED (STATE SAVED)' : vm.status}
                          </span>
                          {vm.auto_hibernated && (
                            <span
                              title={vm.hibernate_reason || 'Auto-hibernated to SSD'}
                              className="inline-flex items-center border border-warning/40 bg-warning/10 px-1 py-0.5 font-mono text-[9px] uppercase text-warning"
                            >
                              SSD Hibernated
                            </span>
                          )}
                        </div>
                      </td>

                      <td className="py-2 px-1.5 font-mono text-[10.5px]">
                        <div className="text-foreground truncate max-w-[140px]" title={vm.worker_id || ''}>
                          {nodeName}
                        </div>
                        <div className="text-[9.5px] text-muted-foreground">
                          pool:{poolLabel}
                        </div>
                      </td>

                      <td className="py-2 px-1.5 font-mono text-[10.5px] text-muted-foreground tabular">
                        {formatRelativeTime(vm.created_at)}
                      </td>

                      <td className="py-2 px-1.5 font-mono text-[10.5px] text-foreground tabular">
                        {tierLabel}
                      </td>

                      <td className="py-2 px-1.5 font-mono text-[10.5px] text-muted-foreground tabular">
                        {vm.status === 'running'
                          ? 'Manual'
                          : vm.status === 'paused'
                            ? 'Paused'
                            : '—'}
                      </td>

                      <td className="py-2 px-1.5 font-mono text-[10.5px]">
                        <span className="border border-border bg-muted/30 px-1 py-0.5 text-muted-foreground">
                          {snapTag}
                        </span>
                      </td>

                      <td className="py-2 px-1.5 font-mono text-[10.5px] text-foreground tabular">
                        {resourcesStr}
                      </td>

                      <td className="py-2 px-1.5 font-mono text-[10.5px] text-muted-foreground tabular">
                        {vm.ip_address || '—'}
                      </td>

                      <td
                        className="py-2 px-1.5"
                        onClick={(e) => e.stopPropagation()}
                      >
                        <div className="flex items-center gap-1" title={addressHint}>
                          {access.url ? (
                            <a
                              href={access.url}
                              target="_blank"
                              rel="noopener noreferrer"
                              className="max-w-[220px] truncate font-mono text-[10.5px] text-[color:var(--link)] hover:underline"
                            >
                              {access.url}
                            </a>
                          ) : (
                            <span className="max-w-[220px] truncate font-mono text-[10.5px] text-muted-foreground">
                              {access.ssh || '—'}
                            </span>
                          )}
                          {primaryAddress ? (
                            <CopyButton
                              value={primaryAddress}
                              label={access.url ? 'Copy URL' : 'Copy SSH command'}
                              className="h-5 w-5 shrink-0"
                            />
                          ) : null}
                        </div>
                      </td>

                      <td
                        className="py-2 pl-1.5 pr-2.5 text-right"
                        onClick={(e) => e.stopPropagation()}
                      >
                        <div className="inline-flex items-center gap-1">
                          {vm.status === 'running' ? (
                            <>
                              <button
                                type="button"
                                onClick={() => setStopConfirmTargets([vm])}
                                title="Stop Sandbox"
                                className="flex h-6 items-center gap-1 border border-border bg-background px-1.5 text-[10px] text-muted-foreground hover:border-warning hover:text-warning transition-colors"
                              >
                                <Square className="h-2.5 w-2.5" />
                                <span>Stop</span>
                              </button>
                              <button
                                type="button"
                                onClick={() => onSnapshot(vm)}
                                title="Pause Sandbox (Save State)"
                                aria-label={`Pause ${vm.name}`}
                                className="flex h-6 items-center gap-1 border border-border bg-background px-1.5 text-[10px] text-muted-foreground hover:border-warning hover:text-warning transition-colors"
                              >
                                <Pause className="h-2.5 w-2.5" />
                                <span>Pause</span>
                              </button>
                            </>
                          ) : vm.status === 'paused' ? (
                            <button
                              type="button"
                              onClick={() => onResume(vm)}
                              title="Resume from Snapshot"
                              aria-label={`Resume ${vm.name}`}
                              className="flex h-6 items-center gap-1 border border-border bg-background px-1.5 text-[10px] text-success hover:border-success transition-colors"
                            >
                              <RotateCcw className="h-2.5 w-2.5" />
                              <span>Resume</span>
                            </button>
                          ) : (
                            <button
                              type="button"
                              onClick={() => onRun(vm)}
                              title="Start Sandbox"
                              className="flex h-6 items-center gap-1 border border-border bg-background px-1.5 text-[10px] text-success hover:border-success transition-colors"
                            >
                              <Play className="h-2.5 w-2.5" />
                              <span>Start</span>
                            </button>
                          )}

                          <button
                            type="button"
                            onClick={() => setSnapshotTarget(vm)}
                            title="Create Snapshot"
                            aria-label={`Snapshot ${vm.name}`}
                            className="flex h-6 items-center gap-1 border border-border bg-background px-1.5 text-[10px] text-muted-foreground hover:border-primary hover:text-foreground transition-colors"
                          >
                            <Camera className="h-2.5 w-2.5" />
                            <span>Snapshot</span>
                          </button>

                          <button
                            type="button"
                            onClick={() => setCloneTarget(vm)}
                            title="Replicate Sandbox"
                            aria-label={`Replicate ${vm.name}`}
                            className="flex h-6 items-center gap-1 border border-border bg-background px-1.5 text-[10px] text-muted-foreground hover:border-primary hover:text-foreground transition-colors"
                          >
                            <Copy className="h-2.5 w-2.5" />
                            <span>Replicate</span>
                          </button>

                          <DropdownMenu>
                            <DropdownMenuTrigger asChild>
                              <button
                                type="button"
                                title="More actions"
                                aria-label={`More actions for ${vm.name}`}
                                className="flex h-6 w-6 items-center justify-center border border-border bg-background text-muted-foreground hover:border-primary hover:text-foreground transition-colors outline-none data-[state=open]:border-primary data-[state=open]:text-foreground"
                              >
                                <MoreHorizontal className="h-3 w-3" />
                              </button>
                            </DropdownMenuTrigger>
                            <DropdownMenuContent align="end" className="w-48">
                              <DropdownMenuItem
                                onSelect={() =>
                                  openSSHTerminalWindow(vm.id, vm.name, 'compute')
                                }
                              >
                                <Terminal className="h-3.5 w-3.5" />
                                Pop Out Terminal
                              </DropdownMenuItem>
                              <DropdownMenuItem
                                onSelect={() => access.ssh && copyText(access.ssh)}
                                disabled={!access.ssh}
                              >
                                <Copy className="h-3.5 w-3.5" />
                                Copy SSH command
                              </DropdownMenuItem>
                              <DropdownMenuItem
                                onSelect={() => access.url && copyText(access.url)}
                                disabled={!access.url}
                              >
                                <Copy className="h-3.5 w-3.5" />
                                Copy URL
                              </DropdownMenuItem>
                              <DropdownMenuItem onSelect={() => setSnapshotTarget(vm)}>
                                <Camera className="h-3.5 w-3.5" />
                                Snapshot
                              </DropdownMenuItem>
                              <DropdownMenuItem onSelect={() => setCloneTarget(vm)}>
                                <Copy className="h-3.5 w-3.5" />
                                Clone
                              </DropdownMenuItem>
                              <DropdownMenuItem onSelect={() => openResizeModal(vm)}>
                                <Scaling className="h-3.5 w-3.5" />
                                Resize
                              </DropdownMenuItem>
                              {isAdmin && (
                                <DropdownMenuItem onSelect={() => openMigrateModal(vm)}>
                                  <ArrowRightLeft className="h-3.5 w-3.5" />
                                  Migrate Sandbox
                                </DropdownMenuItem>
                              )}
                              <DropdownMenuItem onSelect={() => onInspect(vm)}>
                                <Server className="h-3.5 w-3.5" />
                                JSON Spec
                              </DropdownMenuItem>
                              <DropdownMenuItem
                                onSelect={() => void toggleProtection(vm)}
                              >
                                <Shield className="h-3.5 w-3.5" />
                                {vm.protected ? 'Unprotect' : 'Protect'}
                              </DropdownMenuItem>
                              <DropdownMenuSeparator />
                              <DropdownMenuItem
                                variant="destructive"
                                onSelect={() => onDelete(vm)}
                                disabled={vm.protected}
                              >
                                <Trash2 className="h-3.5 w-3.5" />
                                {vm.protected ? 'Delete (protected)' : 'Delete'}
                              </DropdownMenuItem>
                            </DropdownMenuContent>
                          </DropdownMenu>
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            )}
          </table>
        </div>
      </div>

      {/* Redesigned Calm Sandbox Detail View (Item #6 & #7) */}
      {selectedVM && (
        <div
          data-testid="sandbox-detail-panel"
          className="border border-border bg-background"
        >
          {/* Clean Header */}
          <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border bg-muted/20 px-4 py-3">
            <div className="flex flex-wrap items-center gap-2.5 min-w-0">
              <span className="font-display text-[15px] font-medium text-foreground">
                {selectedVM.name}
              </span>
              <span
                className={cn(
                  'inline-flex items-center gap-1 border px-1.5 py-0.5 font-mono text-[9.5px] uppercase tracking-wider',
                  selectedVM.status === 'running' &&
                    'border-success/40 bg-success/10 text-success',
                  selectedVM.status === 'paused' &&
                    'border-warning/40 bg-warning/10 text-warning',
                  (selectedVM.status === 'stopped' ||
                    selectedVM.status === 'created') &&
                    'border-border bg-muted/40 text-muted-foreground',
                  selectedVM.status === 'failed' &&
                    'border-destructive/40 bg-destructive/10 text-destructive',
                )}
              >
                {selectedVM.status === 'paused'
                  ? 'PAUSED (STATE SAVED)'
                  : selectedVM.status}
              </span>
              {selectedVM.auto_hibernated && (
                <span
                  title={selectedVM.hibernate_reason || 'Auto-hibernated to SSD'}
                  className="inline-flex items-center border border-warning/40 bg-warning/10 px-1.5 py-0.5 font-mono text-[9.5px] uppercase text-warning"
                >
                  SSD Hibernated
                </span>
              )}
              {selectedVM.protected && (
                <span className="inline-flex items-center gap-1 border border-primary/40 bg-primary/10 px-1.5 py-0.5 font-mono text-[9.5px] uppercase text-primary dark:text-[var(--accent-bright)]">
                  <Shield className="h-2.5 w-2.5" />
                  Protected
                </span>
              )}
              <span className="inline-flex items-center gap-1 border border-border bg-background px-2 py-0.5 font-mono text-[10.5px] text-muted-foreground">
                <span>
                  {(selectedVM.worker_id && workerNameById[selectedVM.worker_id]) ||
                    selectedVM.worker_id ||
                    'local'}
                </span>
                <span>·</span>
                <span>pool:{selectedVM.pool_id || 'default'}</span>
              </span>

              {selectedVM.ip_address && (
                <button
                  type="button"
                  onClick={() => copyText(selectedVM.ip_address)}
                  title="Copy IP address"
                  className="inline-flex items-center gap-1 border border-border bg-background px-2 py-0.5 font-mono text-[11px] text-foreground hover:border-primary transition-colors"
                >
                  <span>{selectedVM.ip_address}</span>
                  {copiedId === selectedVM.ip_address ? (
                    <Check className="h-3 w-3 text-success" />
                  ) : (
                    <Copy className="h-3 w-3 text-muted-foreground" />
                  )}
                </button>
              )}

              {selectedVM.ip_address && (
                <button
                  type="button"
                  onClick={() =>
                    copyText(selectedAccess.ssh || `ssh compute@${selectedVM.ip_address}`)
                  }
                  title="Copy SSH command"
                  className="inline-flex items-center gap-1 border border-border bg-background px-2 py-0.5 font-mono text-[11px] text-muted-foreground hover:text-foreground hover:border-primary transition-colors"
                >
                  <span>
                    {selectedAccess.ssh || `ssh compute@${selectedVM.ip_address}`}
                  </span>
                  <Copy className="h-3 w-3" />
                </button>
              )}
            </div>

            <div className="flex flex-wrap items-center gap-1.5">
              {selectedVM.status === 'running' ? (
                <>
                  <button
                    type="button"
                    onClick={() => setStopConfirmTargets([selectedVM])}
                    className="inline-flex h-7 items-center gap-1.5 border border-border bg-background px-2.5 text-[11.5px] text-foreground hover:border-warning hover:text-warning transition-colors"
                  >
                    <Square className="h-3 w-3" />
                    <span>Stop</span>
                  </button>
                  <button
                    type="button"
                    disabled={restartingId === selectedVM.id}
                    onClick={() => void handleRestartVM(selectedVM)}
                    className="inline-flex h-7 items-center gap-1.5 border border-border bg-background px-2.5 text-[11.5px] text-foreground hover:border-primary transition-colors disabled:opacity-50"
                  >
                    <RotateCcw className="h-3 w-3" />
                    <span>
                      {restartingId === selectedVM.id ? 'Restarting…' : 'Restart'}
                    </span>
                  </button>
                  <button
                    type="button"
                    onClick={() =>
                      openSSHTerminalWindow(selectedVM.id, selectedVM.name, terminalUser)
                    }
                    className="inline-flex h-7 items-center gap-1.5 bg-primary px-2.5 text-[11.5px] font-medium text-primary-foreground hover:bg-primary-hover transition-colors"
                  >
                    <ExternalLink className="h-3 w-3" />
                    <span>Pop Out Terminal</span>
                  </button>
                </>
              ) : selectedVM.status === 'paused' ? (
                <>
                  <button
                    type="button"
                    onClick={() => onResume(selectedVM)}
                    className="inline-flex h-7 items-center gap-1.5 border border-success/40 bg-success/10 px-2.5 text-[11.5px] font-medium text-success hover:bg-success/20 transition-colors"
                  >
                    <RotateCcw className="h-3 w-3" />
                    <span>Resume</span>
                  </button>
                  <button
                    type="button"
                    onClick={() => setStopConfirmTargets([selectedVM])}
                    className="inline-flex h-7 items-center gap-1.5 border border-border bg-background px-2.5 text-[11.5px] text-foreground hover:border-warning hover:text-warning transition-colors"
                  >
                    <Square className="h-3 w-3" />
                    <span>Stop</span>
                  </button>
                </>
              ) : (
                <button
                  type="button"
                  onClick={() => onRun(selectedVM)}
                  className="inline-flex h-7 items-center gap-1.5 bg-primary px-3 text-[11.5px] font-medium text-primary-foreground hover:bg-primary-hover transition-colors"
                >
                  <Play className="h-3 w-3" />
                  <span>Start</span>
                </button>
              )}

              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <button
                    type="button"
                    className="inline-flex h-7 items-center gap-1 border border-border bg-background px-2.5 text-[11.5px] text-foreground hover:border-primary transition-colors"
                  >
                    <span>More</span>
                    <ChevronDown className="h-3 w-3 text-muted-foreground" />
                  </button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" className="w-48">
                  <DropdownMenuItem onSelect={() => setSnapshotTarget(selectedVM)}>
                    <Camera className="h-3.5 w-3.5" />
                    Snapshot
                  </DropdownMenuItem>
                  <DropdownMenuItem onSelect={() => setCloneTarget(selectedVM)}>
                    <Copy className="h-3.5 w-3.5" />
                    Clone
                  </DropdownMenuItem>
                  <DropdownMenuItem onSelect={() => openResizeModal(selectedVM)}>
                    <Scaling className="h-3.5 w-3.5" />
                    Resize
                  </DropdownMenuItem>
                  {isAdmin && (
                    <DropdownMenuItem onSelect={() => openMigrateModal(selectedVM)}>
                      <ArrowRightLeft className="h-3.5 w-3.5" />
                      Migrate Sandbox
                    </DropdownMenuItem>
                  )}
                  {selectedVM.status === 'running' && (
                    <DropdownMenuItem onSelect={() => onSnapshot(selectedVM)}>
                      <Pause className="h-3.5 w-3.5" />
                      Pause &amp; Save State
                    </DropdownMenuItem>
                  )}
                  <DropdownMenuItem
                    onSelect={() => void toggleProtection(selectedVM)}
                  >
                    <Shield className="h-3.5 w-3.5" />
                    {selectedVM.protected ? 'Unprotect' : 'Protect'}
                  </DropdownMenuItem>
                  <DropdownMenuItem onSelect={() => onInspect(selectedVM)}>
                    <Server className="h-3.5 w-3.5" />
                    JSON Spec
                  </DropdownMenuItem>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem
                    variant="destructive"
                    disabled={selectedVM.protected}
                    onSelect={() => onDelete(selectedVM)}
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                    {selectedVM.protected ? 'Delete (protected)' : 'Delete'}
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>

              <button
                type="button"
                onClick={() => setSelectedVMId(null)}
                className="flex h-7 w-7 items-center justify-center border border-border text-muted-foreground hover:text-foreground"
                title="Close detail panel"
                aria-label="Close detail panel"
              >
                <X className="h-3.5 w-3.5" />
              </button>
            </div>
          </div>

          {/* Embedded Pure Interactive SSH Terminal */}
          <div className="border-b border-border">
            {selectedVM.status === 'running' ? (
              <div>
                <div className="flex items-center justify-between border-b border-border bg-muted/15 px-4 py-1.5">
                  <div className="flex items-center gap-2">
                    <span className="font-mono text-[11px] text-muted-foreground">
                      Terminal User
                    </span>
                    <div className="inline-flex border border-border bg-background">
                      {(['compute', 'root'] as const).map((u) => (
                        <button
                          key={u}
                          type="button"
                          onClick={() => setTerminalUser(u)}
                          className={cn(
                            'px-2 py-0.5 font-mono text-[11px] transition-colors',
                            terminalUser === u
                              ? 'bg-primary text-primary-foreground font-medium'
                              : 'text-muted-foreground hover:text-foreground',
                          )}
                        >
                          {u}
                        </button>
                      ))}
                    </div>
                  </div>

                  <div className="flex items-center gap-1.5">
                    <button
                      type="button"
                      onClick={() => terminalRef.current?.reconnect()}
                      className="inline-flex h-6 items-center gap-1 border border-border bg-background px-2 font-mono text-[11px] text-foreground hover:border-primary"
                    >
                      <RotateCcw className="h-3 w-3" />
                      <span>Reconnect</span>
                    </button>
                    <button
                      type="button"
                      onClick={() =>
                        openSSHTerminalWindow(
                          selectedVM.id,
                          selectedVM.name,
                          terminalUser,
                        )
                      }
                      className="inline-flex h-6 items-center gap-1 border border-border bg-background px-2 font-mono text-[11px] text-foreground hover:border-primary"
                    >
                      <ExternalLink className="h-3 w-3" />
                      <span>Pop Out</span>
                    </button>
                  </div>
                </div>
                <div className="h-[360px] w-full bg-[#0a0a0a]">
                  <SSHTerminal
                    ref={terminalRef}
                    vmId={selectedVM.id}
                    user={terminalUser}
                    raw
                    autoFocus={false}
                    className="h-full w-full"
                  />
                </div>
              </div>
            ) : (
              <div className="flex flex-col items-center justify-center gap-3 bg-muted/10 px-4 py-12 text-center">
                <Terminal className="h-6 w-6 text-muted-foreground" />
                <div className="space-y-1">
                  <p className="text-[13px] font-medium text-foreground">
                    Sandbox is {selectedVM.status}
                  </p>
                  <p className="text-[11.5px] text-muted-foreground">
                    {selectedVM.status === 'paused'
                      ? 'Resume the saved state to open an interactive shell.'
                      : 'Start the sandbox to open an interactive SSH terminal.'}
                  </p>
                </div>
                {selectedVM.status === 'paused' ? (
                  <button
                    type="button"
                    onClick={() => onResume(selectedVM)}
                    className="inline-flex h-8 items-center gap-1.5 bg-primary px-3.5 text-[12px] font-medium text-primary-foreground hover:bg-primary-hover"
                  >
                    <RotateCcw className="h-3.5 w-3.5" />
                    <span>Resume Sandbox</span>
                  </button>
                ) : (
                  <button
                    type="button"
                    onClick={() => onRun(selectedVM)}
                    className="inline-flex h-8 items-center gap-1.5 bg-primary px-3.5 text-[12px] font-medium text-primary-foreground hover:bg-primary-hover"
                  >
                    <Play className="h-3.5 w-3.5" />
                    <span>Start Sandbox</span>
                  </button>
                )}
              </div>
            )}
          </div>

          {/* Focused Resource & Connectivity Cards */}
          <div className="grid grid-cols-1 gap-4 p-4 lg:grid-cols-3">
            {/* Card 1: Compute Specs */}
            <div className="border border-border bg-background p-3.5 space-y-2.5">
              <div className="flex items-center justify-between">
                <span className="text-[12px] font-medium text-foreground">
                  Compute Specs
                </span>
                <Cpu className="h-3.5 w-3.5 text-muted-foreground" />
              </div>
              <div className="grid grid-cols-2 gap-y-2 font-mono text-[11.5px]">
                <span className="text-muted-foreground">vCPUs</span>
                <span className="text-right text-foreground">
                  {selectedVM.vcpus} vCPU
                </span>
                <span className="text-muted-foreground">Memory</span>
                <span className="text-right text-foreground">
                  {formatMemory(selectedVM.memory_mb)}
                </span>
                <span className="text-muted-foreground">Disk</span>
                <span className="text-right text-foreground">
                  {Math.max(
                    1,
                    Math.round((selectedVM.disk_mb || DEFAULT_DISK_MB) / 1024),
                  )}{' '}
                  GB
                </span>
                <span className="text-muted-foreground">Worker Node</span>
                <span className="text-right text-foreground truncate" title={selectedVM.worker_id || ''}>
                  {(selectedVM.worker_id && workerNameById[selectedVM.worker_id]) ||
                    selectedVM.worker_id ||
                    'nod_host_local'}
                </span>
                <span className="text-muted-foreground">Pool</span>
                <span className="text-right text-foreground">
                  {selectedVM.pool_id || 'default'}
                </span>
                {selectedVM.auto_hibernated && (
                  <>
                    <span className="text-muted-foreground">Hibernation</span>
                    <span
                      className="text-right text-warning truncate"
                      title={selectedVM.hibernate_reason || 'Auto-hibernated to SSD'}
                    >
                      SSD Hibernated
                    </span>
                  </>
                )}
                <span className="text-muted-foreground">OS Image</span>
                <span className="text-right text-foreground">Ubuntu 24.04 LTS</span>
                <span className="text-muted-foreground">Created</span>
                <span className="text-right text-muted-foreground">
                  {formatRelativeTime(selectedVM.created_at)}
                </span>
              </div>
            </div>

            {/* Card 2: Attached Volumes */}
            <div className="border border-border bg-background p-3.5 space-y-2.5">
              <div className="flex items-center justify-between">
                <span className="text-[12px] font-medium text-foreground">
                  Attached Volumes ({attachedVolumes.length})
                </span>
                <HardDrive className="h-3.5 w-3.5 text-muted-foreground" />
              </div>

              {volumesLoading && attachedVolumes.length === 0 ? (
                <div className="font-mono text-[11px] text-muted-foreground">
                  Loading volumes…
                </div>
              ) : attachedVolumes.length === 0 ? (
                <div className="text-[11.5px] text-muted-foreground">
                  No block volumes attached.
                </div>
              ) : (
                <div className="divide-y divide-border border border-border">
                  {attachedVolumes.map((vol) => (
                    <div
                      key={vol.id}
                      className="flex items-center justify-between gap-2 px-2.5 py-1.5 font-mono text-[11px]"
                    >
                      <div className="min-w-0">
                        <div className="truncate font-medium text-foreground">
                          {vol.name}
                        </div>
                        <div className="text-[10px] text-muted-foreground">
                          {vol.size_gb} GB · {vol.filesystem} · {vol.mount_point || '/mnt'}
                        </div>
                      </div>
                      <button
                        type="button"
                        onClick={() => void handleDetachVolume(vol)}
                        className="inline-flex h-6 shrink-0 items-center gap-1 border border-border px-2 text-[10.5px] text-muted-foreground hover:border-warning hover:text-warning"
                      >
                        <Unplug className="h-3 w-3" />
                        <span>Detach</span>
                      </button>
                    </div>
                  ))}
                </div>
              )}

              {availableVolumes.length > 0 && (
                <form
                  onSubmit={(e) => void handleAttachVolume(e)}
                  className="flex items-center gap-1.5 pt-1"
                >
                  <select
                    aria-label="Attach volume"
                    value={attachVolumeId}
                    onChange={(e) => setAttachVolumeId(e.target.value)}
                    className="h-7 flex-1 border border-border bg-background px-2 font-mono text-[11px] text-foreground focus:border-primary focus:outline-none"
                  >
                    <option value="">Select available volume…</option>
                    {availableVolumes.map((vol) => (
                      <option key={vol.id} value={vol.id}>
                        {vol.name} ({vol.size_gb} GB)
                      </option>
                    ))}
                  </select>
                  <button
                    type="submit"
                    disabled={!attachVolumeId}
                    className="h-7 bg-primary px-2.5 text-[11px] font-medium text-primary-foreground hover:bg-primary-hover disabled:opacity-50"
                  >
                    Attach
                  </button>
                </form>
              )}
            </div>

            {/* Card 3: Port Forwards & Public Ingress */}
            <div className="border border-border bg-background p-3.5 space-y-2.5">
              <div className="flex items-center justify-between">
                <div className="flex items-center gap-1.5">
                  <Globe className="h-3.5 w-3.5 text-muted-foreground" />
                  <span className="text-[12px] font-medium text-foreground">
                    Port Forwards &amp; Ingress
                  </span>
                </div>
                <button
                  type="button"
                  onClick={() => setAddForwardOpen((v) => !v)}
                  className="inline-flex h-6 items-center gap-1 border border-border bg-background px-2 text-[10.5px] text-foreground hover:border-primary"
                >
                  <Plus className="h-3 w-3" />
                  <span>Add</span>
                </button>
              </div>

              {selectedAccess.url && (
                <div className="flex items-center justify-between gap-2 border border-border bg-muted/20 px-2.5 py-1.5 font-mono text-[11px]">
                  <a
                    href={selectedAccess.url}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="truncate text-[color:var(--link)] hover:underline"
                  >
                    {selectedAccess.url}
                  </a>
                  <CopyButton
                    value={selectedAccess.url}
                    label="Copy ingress URL"
                    className="h-4 w-4 shrink-0"
                  />
                </div>
              )}

              {forwardsLoading && vmPortForwards.length === 0 ? (
                <div className="font-mono text-[11px] text-muted-foreground">
                  Loading port forwards…
                </div>
              ) : vmPortForwards.length === 0 ? (
                <div className="text-[11.5px] text-muted-foreground">
                  No custom port forwards configured.
                </div>
              ) : (
                <div className="divide-y divide-border border border-border">
                  {vmPortForwards.map((pf) => (
                    <div
                      key={pf.id}
                      className="flex items-center justify-between gap-2 px-2.5 py-1.5 font-mono text-[11px]"
                    >
                      <div className="min-w-0">
                        <div className="text-foreground">
                          :{pf.host_port} → :{pf.guest_port}{' '}
                          <span className="text-[10px] text-muted-foreground">
                            ({pf.protocol})
                          </span>
                        </div>
                        {pf.public_url && (
                          <div className="truncate text-[10px] text-[color:var(--link)]">
                            {pf.public_url}
                          </div>
                        )}
                      </div>
                      <button
                        type="button"
                        onClick={() => void handleDeletePortForward(pf)}
                        aria-label={`Remove forward ${pf.host_port}`}
                        className="flex h-6 w-6 shrink-0 items-center justify-center border border-border text-muted-foreground hover:border-destructive hover:text-destructive"
                      >
                        <Trash2 className="h-3 w-3" />
                      </button>
                    </div>
                  ))}
                </div>
              )}

              {addForwardOpen && (
                <form
                  onSubmit={(e) => void handleAddPortForward(e)}
                  className="space-y-2 border border-border bg-muted/15 p-2.5"
                >
                  <div className="grid grid-cols-3 gap-1.5">
                    <div>
                      <label className="block text-[10px] text-muted-foreground mb-0.5">
                        Host Port
                      </label>
                      <input
                        type="number"
                        min={1}
                        max={65535}
                        value={fwHostPort}
                        onChange={(e) => setFwHostPort(e.target.value)}
                        className="h-7 w-full border border-border bg-background px-2 font-mono text-[11px]"
                      />
                    </div>
                    <div>
                      <label className="block text-[10px] text-muted-foreground mb-0.5">
                        Guest Port
                      </label>
                      <input
                        type="number"
                        min={1}
                        max={65535}
                        value={fwGuestPort}
                        onChange={(e) => setFwGuestPort(e.target.value)}
                        className="h-7 w-full border border-border bg-background px-2 font-mono text-[11px]"
                      />
                    </div>
                    <div>
                      <label className="block text-[10px] text-muted-foreground mb-0.5">
                        Protocol
                      </label>
                      <select
                        value={fwProtocol}
                        onChange={(e) =>
                          setFwProtocol(e.target.value as 'TCP' | 'HTTP/HTTPS')
                        }
                        className="h-7 w-full border border-border bg-background px-1.5 font-mono text-[11px]"
                      >
                        <option value="TCP">TCP</option>
                        <option value="HTTP/HTTPS">HTTP</option>
                      </select>
                    </div>
                  </div>
                  {fwProtocol === 'HTTP/HTTPS' && (
                    <input
                      type="text"
                      value={fwSubdomain}
                      onChange={(e) => setFwSubdomain(e.target.value)}
                      placeholder="Optional subdomain (e.g. app)"
                      className="h-7 w-full border border-border bg-background px-2 font-mono text-[11px]"
                    />
                  )}
                  <div className="flex justify-end gap-1.5">
                    <button
                      type="button"
                      onClick={() => setAddForwardOpen(false)}
                      className="h-6.5 border border-border px-2 text-[11px] text-muted-foreground"
                    >
                      Cancel
                    </button>
                    <button
                      type="submit"
                      className="h-6.5 bg-primary px-2.5 text-[11px] font-medium text-primary-foreground hover:bg-primary-hover"
                    >
                      Add Forward
                    </button>
                  </div>
                </form>
              )}
            </div>
          </div>
        </div>
      )}

      {/* Stop confirmation dialog */}
      <AlertDialog
        open={stopConfirmTargets !== null}
        onOpenChange={(open) => !open && setStopConfirmTargets(null)}
      >
        <AlertDialogContent className="max-w-md w-[calc(100%-2rem)]">
          <AlertDialogHeader>
            <div className="mb-1 flex h-10 w-10 items-center justify-center rounded-lg border border-warning/40 bg-warning/10 text-warning">
              <Square className="h-4 w-4 fill-current" />
            </div>
            <AlertDialogTitle>
              {stopConfirmTargets && stopConfirmTargets.length > 1
                ? `Stop ${stopConfirmTargets.length} running sandboxes?`
                : `Stop ${stopConfirmTargets?.[0]?.name ?? 'sandbox'}?`}
            </AlertDialogTitle>
            <AlertDialogDescription>
              Running microVM processes will be shut down and their resources
              released. Unsaved work inside the guest will be lost.
            </AlertDialogDescription>
          </AlertDialogHeader>

          {stopConfirmTargets && stopConfirmTargets.length > 1 && (
            <div className="my-2 max-h-40 space-y-1 overflow-y-auto border border-border bg-secondary/30 p-2.5 text-xs">
              {stopConfirmTargets.map((vm) => (
                <div
                  key={vm.id}
                  className="flex items-center justify-between gap-2 border-b border-border/40 py-1 last:border-b-0"
                >
                  <span className="truncate font-medium text-foreground">
                    {vm.name}
                  </span>
                  <span className="shrink-0 font-mono text-[10px] text-muted-foreground">
                    {vm.ip_address || vm.id.slice(0, 8)}
                  </span>
                </div>
              ))}
            </div>
          )}

          <AlertDialogFooter className="mt-3 flex flex-row justify-end gap-2">
            <AlertDialogCancel className="border-border bg-background hover:bg-muted text-foreground px-4">
              Cancel
            </AlertDialogCancel>
            <AlertDialogAction
              onClick={(e) => {
                e.preventDefault();
                confirmStop();
              }}
              className="bg-warning text-[#1c1400] hover:bg-warning/90 font-medium px-4 inline-flex items-center"
            >
              <Square className="mr-1.5 h-3.5 w-3.5 fill-current" />
              {stopConfirmTargets && stopConfirmTargets.length > 1
                ? `Stop ${stopConfirmTargets.length} sandboxes`
                : 'Stop sandbox'}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Resize Sandbox Modal (provisions resized clone) */}
      {resizeTarget && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/75 p-4"
          onClick={() => setResizeTarget(null)}
        >
          <form
            role="dialog"
            aria-modal="true"
            onClick={(e) => e.stopPropagation()}
            onSubmit={(e) => void handleResizeSubmit(e)}
            className="w-full max-w-md border border-border bg-background p-5 space-y-4 text-foreground"
          >
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                <Scaling className="h-4 w-4 text-primary" />
                <h2 className="text-[15px] font-medium">
                  Resize {resizeTarget.name}
                </h2>
              </div>
              <button
                type="button"
                onClick={() => setResizeTarget(null)}
                className="text-muted-foreground hover:text-foreground"
              >
                <X className="h-4 w-4" />
              </button>
            </div>
            <p className="text-[11.5px] text-muted-foreground">
              Provisions a resized sandbox from {resizeTarget.name}&apos;s disk state
              with updated vCPU, memory, or disk capacity.
            </p>
            <div className="space-y-3">
              <div>
                <label className="block text-[11px] text-muted-foreground mb-1">
                  New Sandbox Name
                </label>
                <input
                  type="text"
                  value={resizeName}
                  onChange={(e) => setResizeName(e.target.value)}
                  required
                  className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px]"
                />
              </div>
              <div className="grid grid-cols-3 gap-2">
                <div>
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    vCPUs
                  </label>
                  <select
                    value={resizeVcpus}
                    onChange={(e) => setResizeVcpus(Number(e.target.value))}
                    className="h-8 w-full border border-border bg-background px-2 font-mono text-[12px]"
                  >
                    {[1, 2, 4, 8, 16].map((c) => (
                      <option key={c} value={c}>
                        {c} vCPU
                      </option>
                    ))}
                  </select>
                </div>
                <div>
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    Memory
                  </label>
                  <select
                    value={resizeMemoryMb}
                    onChange={(e) => setResizeMemoryMb(Number(e.target.value))}
                    className="h-8 w-full border border-border bg-background px-2 font-mono text-[12px]"
                  >
                    {[512, 1024, 2048, 4096, 8192, 16384, 32768].map((m) => (
                      <option key={m} value={m}>
                        {formatMemory(m)}
                      </option>
                    ))}
                  </select>
                </div>
                <div>
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    Disk (GB)
                  </label>
                  <input
                    type="number"
                    min={10}
                    max={500}
                    value={resizeDiskGb}
                    onChange={(e) => setResizeDiskGb(Number(e.target.value) || 20)}
                    className="h-8 w-full border border-border bg-background px-2 font-mono text-[12px]"
                  />
                </div>
              </div>
              <label className="flex items-center gap-2 text-[12px] text-muted-foreground cursor-pointer">
                <input
                  type="checkbox"
                  checked={resizeAutoStart}
                  onChange={(e) => setResizeAutoStart(e.target.checked)}
                  className="h-3.5 w-3.5 accent-primary"
                />
                <span>Start resized sandbox immediately</span>
              </label>
            </div>
            <div className="flex justify-end gap-2 pt-2 border-t border-border">
              <button
                type="button"
                onClick={() => setResizeTarget(null)}
                className="h-8 border border-border px-3 text-[12px] text-muted-foreground hover:text-foreground"
              >
                Cancel
              </button>
              <button
                type="submit"
                className="h-8 bg-primary px-3.5 text-[12px] font-medium text-primary-foreground hover:bg-primary-hover"
              >
                Provision Resized
              </button>
            </div>
          </form>
        </div>
      )}

      {/* Migrate Sandbox Modal */}
      {migrateTarget && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/75 p-4"
          onClick={() => setMigrateTarget(null)}
        >
          <form
            role="dialog"
            aria-modal="true"
            aria-label="Migrate Sandbox"
            onClick={(e) => e.stopPropagation()}
            onSubmit={handleMigrateSubmit}
            className="w-full max-w-md border border-border bg-background p-5 space-y-4 text-foreground"
          >
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                <ArrowRightLeft className="h-4 w-4 text-primary" />
                <h2 className="text-[15px] font-medium">
                  Migrate {migrateTarget.name}
                </h2>
              </div>
              <button
                type="button"
                aria-label="Close migrate dialog"
                onClick={() => setMigrateTarget(null)}
                className="text-muted-foreground hover:text-foreground"
              >
                <X className="h-4 w-4" />
              </button>
            </div>
            <p className="text-[11.5px] text-muted-foreground">
              Move sandbox <span className="font-mono text-foreground">{migrateTarget.name}</span> to a target worker node or placement pool.
            </p>
            <div className="space-y-3 text-[12px]">
              <div>
                <label className="block text-[11px] text-muted-foreground mb-1">
                  Target Worker Node
                </label>
                <select
                  aria-label="Target Worker Node"
                  value={migrateWorkerId}
                  onChange={(e) => setMigrateWorkerId(e.target.value)}
                  className="h-8 w-full border border-border bg-background px-2 font-mono text-[12px] text-foreground focus:border-primary focus:outline-none"
                >
                  <option value="">Auto-select in target pool</option>
                  {workers
                    .filter((w) => w.id !== migrateTarget.worker_id)
                    .map((w) => (
                      <option key={w.id} value={w.id}>
                        {w.name} ({w.pool_id} · {w.status})
                      </option>
                    ))}
                </select>
              </div>
              <div>
                <label className="block text-[11px] text-muted-foreground mb-1">
                  Target Pool
                </label>
                <select
                  aria-label="Target Pool"
                  value={migratePoolId}
                  onChange={(e) => setMigratePoolId(e.target.value)}
                  className="h-8 w-full border border-border bg-background px-2 font-mono text-[12px] text-foreground focus:border-primary focus:outline-none"
                >
                  <option value="">Keep current / match worker</option>
                  <option value="default">default</option>
                  {pools
                    .filter((p) => p.name !== 'default')
                    .map((p) => (
                      <option key={p.id || p.name} value={p.name}>
                        {p.name}
                      </option>
                    ))}
                </select>
              </div>
              <label className="flex items-center gap-2 text-[12px] text-muted-foreground cursor-pointer">
                <input
                  type="checkbox"
                  checked={migrateForce}
                  onChange={(e) => setMigrateForce(e.target.checked)}
                  className="h-3.5 w-3.5 accent-primary"
                />
                <span>Force stop &amp; migrate if currently running</span>
              </label>
            </div>
            <div className="flex justify-end gap-2 pt-2 border-t border-border">
              <button
                type="button"
                onClick={() => setMigrateTarget(null)}
                className="h-8 border border-border px-3 text-[12px] text-muted-foreground hover:text-foreground"
              >
                Cancel
              </button>
              <button
                type="submit"
                className="h-8 bg-primary px-3.5 text-[12px] font-medium text-primary-foreground hover:bg-primary-hover"
              >
                Migrate Sandbox
              </button>
            </div>
          </form>
        </div>
      )}

      <BulkDeleteVMDialog
        vms={selectedVMs}
        open={bulkDeleteOpen}
        onOpenChange={setBulkDeleteOpen}
        onConfirm={executeBulkDelete}
      />

      <UnprotectVMDialog
        vms={unprotectTargets}
        open={unprotectOpen}
        onOpenChange={setUnprotectOpen}
        onConfirm={confirmUnprotect}
      />

      <CloneVMDialog
        vm={cloneTarget}
        open={cloneTarget !== null}
        onOpenChange={(open) => !open && setCloneTarget(null)}
        onCloned={() => onRefresh?.()}
      />

      <CreateSnapshotDialog
        vm={snapshotTarget}
        open={snapshotTarget !== null}
        onOpenChange={(open) => !open && setSnapshotTarget(null)}
        onCreated={() => onRefresh?.()}
      />
    </div>
  );
}
