import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  Activity,
  Camera,
  Check,
  ChevronDown,
  Copy,
  Cpu,
  FolderOpen,
  HardDrive,
  HelpCircle,
  MoreHorizontal,
  Play,
  RefreshCw,
  RotateCcw,
  Search,
  Server,
  Shield,
  Square,
  Terminal,
  Trash2,
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
import { CopyButton } from '@/components/copy-button';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { UnprotectVMDialog } from '@/components/unprotect-vm-dialog';
import { api, errorMessage } from '@/lib/api';
import { primarySandboxAddress, sandboxAccess } from '@/lib/sandbox-access';
import {
  formatMemory,
  formatRelativeTime,
  formatTimestamp,
} from '@/lib/format';
import type {
  AuditEventRecord,
  FileEntry,
  NetworkInspectResult,
  ProcessInfo,
  VM,
} from '@/lib/types';
import { cn } from '@/lib/utils';

interface InstancesPageProps {
  vms: VM[];
  events?: AuditEventRecord[];
  isLoading: boolean;
  onNew: () => void;
  onQuickCreate?: () => void;
  onRun: (vm: VM) => void;
  onStop: (vm: VM) => void;
  onSnapshot: (vm: VM) => void;
  onResume: (vm: VM) => void;
  onDelete: (vm: VM) => void;
  onBulkDelete?: (ids: string[]) => Promise<void>;
  onInspect: (vm: VM) => void;
  onConsole: (vm: VM) => void;
  onRefresh?: () => void;
}

type DeepDiveTab =
  | 'overview'
  | 'exec'
  | 'files'
  | 'processes'
  | 'network'
  | 'snapshots'
  | 'events';

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

function deriveHourlyRate(vcpus: number): number {
  if (vcpus <= 2) return 0.018;
  if (vcpus <= 4) return 0.036;
  if (vcpus <= 8) return 0.072;
  return 0.144;
}

function computeAccruedCost(vm: VM): string {
  const rate = deriveHourlyRate(vm.vcpus);
  const createdMs = new Date(vm.created_at).getTime();
  if (Number.isNaN(createdMs)) return `$${rate.toFixed(3)}/h`;
  const elapsedHours = Math.max(0.05, (Date.now() - createdMs) / 3_600_000);
  const accrued = vm.status === 'running' ? elapsedHours * rate : rate * 0.1;
  return `$${accrued.toFixed(3)}`;
}

export function InstancesPage({
  vms,
  events = [],
  isLoading,
  onNew,
  onQuickCreate,
  onRun,
  onStop,
  onSnapshot,
  onResume,
  onDelete,
  onBulkDelete,
  onInspect,
  onConsole,
  onRefresh,
}: InstancesPageProps) {
  const [search, setSearch] = useState('');
  const [stateFilter, setStateFilter] = useState<string>('all');
  const [snapshotFilter, setSnapshotFilter] = useState<string>('all');
  const [typeFilter, setTypeFilter] = useState<string>('all');
  const [selectedIds, setSelectedIds] = useState<string[]>([]);
  const [bulkDeleteOpen, setBulkDeleteOpen] = useState(false);
  const [unprotectOpen, setUnprotectOpen] = useState(false);
  const [unprotectTargets, setUnprotectTargets] = useState<VM[]>([]);
  const [copiedId, setCopiedId] = useState<string | null>(null);
  const [splitMenuOpen, setSplitMenuOpen] = useState(false);
  // Critical-action confirmation: list of VMs pending a stop confirmation.
  const [stopConfirmTargets, setStopConfirmTargets] = useState<VM[] | null>(null);

  const selectedVMs = useMemo(
    () => vms.filter((v) => selectedIds.includes(v.id)),
    [vms, selectedIds],
  );

  const [selectedVMId, setSelectedVMId] = useState<string | null>(null);
  const [deepDiveTab, setDeepDiveTab] = useState<DeepDiveTab>('overview');

  // Real Exec state
  const [execCommand, setExecCommand] = useState('uname -a && free -m && df -h /');
  const [execRunning, setExecRunning] = useState(false);
  const [execOutput, setExecOutput] = useState<string>('');
  const [execMeta, setExecMeta] = useState<{
    exitCode: number;
    durationMs: number;
  } | null>(null);

  // Real Filesystem state
  const [currentDir, setCurrentDir] = useState('/home/compute');
  const [files, setFiles] = useState<FileEntry[]>([]);
  const [filesDir, setFilesDir] = useState('/home/compute');
  const [filesLoading, setFilesLoading] = useState(false);

  // Real Processes state
  const [processes, setProcesses] = useState<ProcessInfo[]>([]);
  const [processesLoading, setProcessesLoading] = useState(false);

  // Real Network inspect state
  const [netInspect, setNetInspect] = useState<NetworkInspectResult | null>(null);
  const [netInspectLoading, setNetInspectLoading] = useState(false);

  const selectedVM = useMemo(
    () => vms.find((v) => v.id === selectedVMId) ?? null,
    [vms, selectedVMId],
  );

  const selectedAccess = useMemo(() => sandboxAccess(selectedVM), [selectedVM]);

  const filteredVMs = useMemo(() => {
    return vms.filter((vm) => {
      const q = search.trim().toLowerCase();
      const access = sandboxAccess(vm);
      const matchable = [
        vm.name,
        vm.id,
        vm.internal_id,
        vm.ip_address,
        vm.hostname,
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
  }, [vms, search, stateFilter, snapshotFilter, typeFilter]);

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

  // Open the SSH terminal in a dedicated popup browser window. The window
  // loads the standalone terminal route and connects straight to the daemon
  // over its own WebSocket, independent of the main dashboard tab.
  function openSSHTerminal(vm: VM) {
    const base = `${window.location.origin}${window.location.pathname}`;
    const url = `${base}#/terminal/${encodeURIComponent(vm.id)}`;
    const width = Math.min(1100, Math.max(560, window.screen.width - 120));
    const height = Math.min(760, Math.max(400, window.screen.height - 120));
    const left = Math.max(0, Math.round((window.screen.width - width) / 2));
    const top = Math.max(0, Math.round((window.screen.height - height) / 2));
    window.open(
      url,
      `ssh-terminal-${vm.id}`,
      `popup,width=${width},height=${height},left=${left},top=${top}`,
    );
  }

  async function cloneOne(vm: VM) {
    const toastID = toast.loading(`Cloning ${vm.name}…`);
    try {
      await api.cloneVM(vm.id, {
        name: `${vm.name}-clone`,
        vcpus: vm.vcpus,
        memory_mb: vm.memory_mb,
      });
      toast.success(`Cloned ${vm.name}`, { id: toastID });
    } catch (err) {
      toast.error(`Clone failed: ${errorMessage(err)}`, { id: toastID });
    }
  }

  function confirmStop() {
    const targets = stopConfirmTargets ?? [];
    if (targets.length === 0) return;
    setStopConfirmTargets(null);
    setSelectedIds((prev) =>
      prev.filter((id) => !targets.some((vm) => vm.id === id)),
    );
    void Promise.allSettled(
      targets.map((vm) => Promise.resolve(onStop(vm))),
    );
  }

  // Load tab-specific real telemetry when the selected sandbox or tab changes.
  // Depend on stable primitives (vm id, status) rather than the selectedVM
  // object: the sandbox list is polled every few seconds, which recreates the
  // object identity and would otherwise re-trigger these fetches and flash the
  // loading placeholder over already-rendered content.
  const selectedVMKey = selectedVM?.id ?? null;
  const selectedVMStatus = selectedVM?.status ?? null;

  const fetchFiles = useCallback((vmId: string, dir: string) => {
    setFilesLoading(true);
    return api
      .listVMFiles(vmId, dir)
      .then((res) => {
        setFiles(res || []);
        setFilesDir(dir);
      })
      .catch(() => setFiles([]))
      .finally(() => setFilesLoading(false));
  }, []);

  const fetchProcesses = useCallback((vmId: string) => {
    setProcessesLoading(true);
    return api
      .listVMProcesses(vmId)
      .then((res) => setProcesses(res || []))
      .catch(() => setProcesses([]))
      .finally(() => setProcessesLoading(false));
  }, []);

  const fetchNetInspect = useCallback((vmId: string) => {
    setNetInspectLoading(true);
    return api
      .inspectNetwork(vmId)
      .then((res) => setNetInspect(res))
      .catch(() => setNetInspect(null))
      .finally(() => setNetInspectLoading(false));
  }, []);

  const prevVMIdRef = useRef<string | null>(null);

  useEffect(() => {
    if (!selectedVMKey || selectedVMStatus !== 'running') return;
    // Switching to a different sandbox: drop data fetched for the previous
    // sandbox so nothing from it renders under the new one.
    if (prevVMIdRef.current !== selectedVMKey) {
      prevVMIdRef.current = selectedVMKey;
      setFiles([]);
      setFilesDir(currentDir);
      setProcesses([]);
      setNetInspect(null);
    }
    if (deepDiveTab === 'files') {
      // Navigating to a different directory: drop the stale listing so the
      // old path's entries never render under the new path.
      setFiles((prevFiles) => (filesDir === currentDir ? prevFiles : []));
      void fetchFiles(selectedVMKey, currentDir);
    } else if (deepDiveTab === 'processes') {
      void fetchProcesses(selectedVMKey);
    } else if (deepDiveTab === 'network') {
      void fetchNetInspect(selectedVMKey);
    }
  }, [
    selectedVMKey,
    selectedVMStatus,
    deepDiveTab,
    currentDir,
    fetchFiles,
    fetchProcesses,
    fetchNetInspect,
  ]);

  async function handleRunExec() {
    if (!selectedVM || !execCommand.trim()) return;
    setExecRunning(true);
    try {
      const res = await api.execVM(selectedVM.id, {
        command: execCommand.trim(),
        timeout: 30,
      });
      const combined = [res.stdout, res.stderr].filter(Boolean).join('\n');
      setExecOutput(combined || '(command exited with no output)');
      setExecMeta({ exitCode: res.exit_code, durationMs: res.duration_ms });
    } catch (err) {
      setExecOutput(`Error executing command: ${errorMessage(err)}`);
      setExecMeta({ exitCode: 1, durationMs: 0 });
    } finally {
      setExecRunning(false);
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
    await Promise.all(toStart.map((vm) => onRun(vm)));
    setSelectedIds([]);
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
    await Promise.all(toSnap.map((vm) => onSnapshot(vm)));
    setSelectedIds([]);
  }

  async function handleBatchClone() {
    if (selectedIds.length === 0) return;
    const toastID = toast.loading(`Cloning ${selectedIds.length} sandbox(es)…`);
    try {
      await Promise.all(
        selectedIds.map(async (id) => {
          const source = vms.find((v) => v.id === id);
          const cloneName = source ? `${source.name}-clone` : `sbx-clone-${id.slice(0, 6)}`;
          await api.cloneVM(id, {
            name: cloneName,
            vcpus: source?.vcpus || 1,
            memory_mb: source?.memory_mb,
          });
        })
      );
      toast.success(`Successfully cloned ${selectedIds.length} sandbox(es)`, {
        id: toastID,
      });
      setSelectedIds([]);
    } catch (err) {
      toast.error(`Clone failed: ${errorMessage(err)}`, { id: toastID });
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
        : `Disabling protection for ${targets.length} sandboxes…`,
    );
    try {
      await Promise.all(ids.map((id) => api.unprotectVM(id)));
      if (targets.length === 1) {
        toast.success(`Sandbox ${targets[0].name} protection disabled`, { id: toastID });
      } else {
        toast.success(`Protection disabled for ${targets.length} sandboxes`, { id: toastID });
      }
      onRefresh?.();
    } catch (err) {
      toast.error(`Failed to update protection: ${errorMessage(err)}`, { id: toastID });
      throw err;
    }
  }

  async function toggleProtection(targetVM: VM) {
    if (targetVM.protected) {
      // Removing a safety guard is deliberate: always require typed confirmation.
      requestUnprotect([targetVM]);
      return;
    }

    const toastID = toast.loading('Enabling protection…');
    try {
      await api.protectVM(targetVM.id);
      toast.success(`Sandbox ${targetVM.name} is now deletion protected`, { id: toastID });
      onRefresh?.();
    } catch (err) {
      toast.error(`Failed to update protection: ${errorMessage(err)}`, { id: toastID });
    }
  }

  async function executeBulkDelete(ids: string[]) {
    const protectedIds = ids.filter((id) => vms.find((v) => v.id === id)?.protected);
    if (protectedIds.length > 0) {
      toast.error(`Cannot delete ${protectedIds.length} protected sandbox(es). Unprotect them first.`);
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
      const toastID = toast.loading(`Deleting ${ids.length} sandbox(es)…`);
      try {
        await Promise.all(ids.map((id) => api.deleteVM(id)));
        toast.success(`Deleted ${ids.length} sandbox(es)`, { id: toastID });
      } catch (err) {
        toast.error(`Delete failed: ${errorMessage(err)}`, { id: toastID });
        throw err;
      }
    }
  }

  return (
    <div className="space-y-4">
      {/* Top Page Title & Split Cobalt Action Button matching boat.dev/dashboard 1:1 */}
      <div className="flex items-center justify-between">
        <h1 className="font-display text-[22px] font-medium tracking-tight text-foreground">
          Sandboxes
        </h1>

        <div className="relative flex items-center">
          {/* Split Cobalt [ ▷ New | ⌄ ] Button matching boat.dev/dashboard */}
          <div className="inline-flex items-stretch">
            <button
              type="button"
              onClick={onNew}
              className="flex h-8 items-center gap-1.5 bg-[#263fa9] px-3 text-[12px] font-medium text-[#fefefe] hover:bg-[#1e3180] transition-colors"
            >
              <Play className="h-3 w-3" />
              <span>New</span>
            </button>
            <button
              type="button"
              onClick={() => setSplitMenuOpen((prev) => !prev)}
              aria-label="More creation options"
              className="flex h-8 w-7 items-center justify-center border-l border-[#fefefe]/20 bg-[#263fa9] text-[#fefefe] hover:bg-[#1e3180] transition-colors"
            >
              <ChevronDown className="h-3.5 w-3.5" />
            </button>
          </div>

          {splitMenuOpen && (
            <div
              onMouseLeave={() => setSplitMenuOpen(false)}
              className="absolute right-0 top-9 z-50 w-48 border border-border bg-background py-1 text-[12px]"
            >
              <button
                type="button"
                onClick={() => {
                  setSplitMenuOpen(false);
                  onNew();
                }}
                className="flex w-full items-center justify-between px-3 py-1.5 text-left text-foreground hover:bg-muted/60"
              >
                <span>Advanced create…</span>
                <span className="font-mono text-[10px] text-muted-foreground">
                  Modal
                </span>
              </button>
              {onQuickCreate && (
                <button
                  type="button"
                  onClick={() => {
                    setSplitMenuOpen(false);
                    onQuickCreate();
                  }}
                  className="flex w-full items-center justify-between px-3 py-1.5 text-left text-foreground hover:bg-muted/60"
                >
                  <span>Quick default sandbox</span>
                  <span className="font-mono text-[10px] text-muted-foreground">
                    4 vCPU
                  </span>
                </button>
              )}
            </div>
          )}
        </div>
      </div>

      {/* Filter Bar matching boat.dev/dashboard 1:1 */}
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative w-full sm:w-64">
          <input
            type="text"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search name, URL, hostname, id"
            className="h-8 w-full border border-border bg-background px-3 text-[12px] text-foreground placeholder:text-muted-foreground focus:border-[#263fa9] focus:outline-none"
          />
        </div>

        {/* State dropdown */}
        <div className="relative">
          <select
            aria-label="Filter by state"
            value={stateFilter}
            onChange={(e) => setStateFilter(e.target.value)}
            className="h-8 appearance-none border border-border bg-background pl-2.5 pr-7 text-[12px] text-muted-foreground hover:text-foreground focus:border-[#263fa9] focus:outline-none cursor-pointer"
          >
            <option value="all">State</option>
            <option value="running">Running</option>
            <option value="paused">Paused (Snap)</option>
            <option value="stopped">Stopped</option>
            <option value="queued">Queued</option>
            <option value="failed">Failed</option>
          </select>
          <ChevronDown className="pointer-events-none absolute right-2 top-1/2 h-3 w-3 -translate-y-1/2 text-muted-foreground" />
        </div>

        {/* Snapshot dropdown */}
        <div className="relative">
          <select
            aria-label="Filter by snapshot"
            value={snapshotFilter}
            onChange={(e) => setSnapshotFilter(e.target.value)}
            className="h-8 appearance-none border border-border bg-background pl-2.5 pr-7 text-[12px] text-muted-foreground hover:text-foreground focus:border-[#263fa9] focus:outline-none cursor-pointer"
          >
            <option value="all">Snapshot</option>
            <option value="has-snapshot">Has snapshot</option>
            <option value="no-snapshot">No snapshot</option>
          </select>
          <ChevronDown className="pointer-events-none absolute right-2 top-1/2 h-3 w-3 -translate-y-1/2 text-muted-foreground" />
        </div>

        {/* Type dropdown */}
        <div className="relative">
          <select
            aria-label="Filter by tier type"
            value={typeFilter}
            onChange={(e) => setTypeFilter(e.target.value)}
            className="h-8 appearance-none border border-border bg-background pl-2.5 pr-7 text-[12px] text-muted-foreground hover:text-foreground focus:border-[#263fa9] focus:outline-none cursor-pointer"
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
          <div className="ml-auto flex flex-wrap items-center gap-1.5 border border-[#263fa9]/60 bg-[#0f1634]/50 px-2.5 py-1.5 shadow-sm">
            <span className="mr-1 inline-flex items-center gap-1.5 font-mono text-[11px] text-foreground">
              <span className="h-1.5 w-1.5 bg-[#5e7ff5]" />
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
              title="Snapshot all selected running sandboxes"
              className="inline-flex h-6.5 items-center gap-1.5 border border-border bg-background px-2.5 text-[11px] font-medium text-foreground hover:border-[#263fa9] hover:text-[#5e7ff5] transition-colors cursor-pointer"
            >
              <Camera className="h-3 w-3" />
              <span>Snapshot</span>
            </button>
            <button
              type="button"
              onClick={() => void handleBatchClone()}
              title="Clone all selected sandboxes"
              className="inline-flex h-6.5 items-center gap-1.5 border border-border bg-background px-2.5 text-[11px] font-medium text-foreground hover:border-[#263fa9] hover:text-[#5e7ff5] transition-colors cursor-pointer"
            >
              <Copy className="h-3 w-3" />
              <span>Clone</span>
            </button>
            {selectedVMs.some((vm) => vm.protected) && (
              <button
                type="button"
                onClick={handleBatchUnprotect}
                title="Remove deletion protection from selected sandboxes"
                className="inline-flex h-6.5 items-center gap-1.5 border border-border bg-background px-2.5 text-[11px] font-medium text-[#5e7ff5] hover:border-[#263fa9] hover:text-[#5e7ff5] transition-colors cursor-pointer"
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

      {/* Sandboxes 10-Column Table matching boat.dev/dashboard 1:1 */}
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
                    className="h-3.5 w-3.5 accent-[#263fa9] cursor-pointer"
                  />
                </th>
                <th className="py-2 px-1 font-normal">Name / URL</th>
                <th className="py-2 px-1 font-normal">Sandbox ID</th>
                <th className="py-2 px-1 font-normal">State</th>
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
                    Cost
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
                  <td colSpan={12} className="py-20 text-center">
                    <div className="mx-auto flex max-w-xs flex-col items-center justify-center space-y-3.5">
                      {/* Cobalt Blue Dithered Dot-Matrix Sailboat matching boat.dev/dashboard 1:1 */}
                      <svg
                        viewBox="0 0 120 110"
                        className="h-28 w-32 text-[#263fa9]"
                        fill="currentColor"
                        shapeRendering="crispEdges"
                        aria-hidden="true"
                      >
                        <defs>
                          <pattern
                            id="boat-dots"
                            x="0"
                            y="0"
                            width="4"
                            height="4"
                            patternUnits="userSpaceOnUse"
                          >
                            <rect x="0" y="0" width="2.2" height="2.2" fill="currentColor" opacity="0.65" />
                          </pattern>
                        </defs>
                        {/* Left Main Sail */}
                        <polygon points="56,6 14,76 56,76" fill="url(#boat-dots)" />
                        {/* Right Jib Sail */}
                        <polygon points="62,24 62,76 104,76" fill="url(#boat-dots)" />
                        {/* Hull */}
                        <polygon points="6,82 114,82 102,98 18,98" fill="url(#boat-dots)" />
                      </svg>

                      <p className="text-[12.5px] text-foreground/90">
                        {isLoading
                          ? 'Connecting to compute daemon…'
                          : 'No sandboxes yet.'}
                      </p>

                      <button
                        type="button"
                        onClick={onNew}
                        className="inline-flex h-8 items-center gap-1.5 bg-[#263fa9] px-3.5 text-[12px] font-medium text-[#fefefe] hover:bg-[#1e3180] transition-colors"
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
                    : 'base:v1';
                  const tierLabel = deriveTierLabel(vm.vcpus, vm.memory_mb);
                  const costStr = computeAccruedCost(vm);
                  const shortId = (vm.internal_id || vm.id).slice(0, 12);

                  return (
                    <tr
                      key={vm.id}
                      onClick={() => setSelectedVMId(vm.id)}
                      className={cn(
                        'cursor-pointer transition-colors',
                        isSelected
                          ? 'bg-[#0f1634]/75'
                          : 'hover:bg-muted/40',
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
                          className="h-3.5 w-3.5 accent-[#263fa9] cursor-pointer"
                        />
                      </td>

                      {/* Name / URL */}
                      <td className="py-2 px-1.5">
                        <div className="flex items-center gap-1.5 font-medium text-foreground">
                          <span>{vm.name}</span>
                          {vm.protected && (
                            <span
                              title="Deletion protected"
                              className="inline-flex items-center gap-0.5 border border-[#5e7ff5]/40 bg-[#5e7ff5]/10 px-1 py-0.2 text-[9px] font-mono text-[#5e7ff5] uppercase"
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
                            <span className="text-warning" aria-label="DNS delegation required">
                              DNS
                            </span>
                          ) : null}
                        </div>
                      </td>

                      {/* Sandbox ID */}
                      <td
                        className="py-2 px-1.5 font-mono text-[10.5px]"
                        onClick={(e) => e.stopPropagation()}
                      >
                        <button
                          type="button"
                          onClick={() => copyText(vm.internal_id || vm.id)}
                          className="inline-flex items-center gap-1 border border-border bg-muted/40 px-1.5 py-0.5 text-foreground hover:border-[#263fa9] transition-colors"
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

                      {/* State */}
                      <td className="py-2 px-1.5">
                        <span
                          className={cn(
                            'inline-flex items-center gap-1 border px-1.5 py-0.5 font-mono text-[9.5px] uppercase tracking-wider',
                            vm.status === 'running' &&
                              'border-success/40 bg-success/10 text-success',
                            vm.status === 'paused' &&
                              'border-warning/40 bg-warning/10 text-warning',
                            (vm.status === 'stopped' ||
                              vm.status === 'created') &&
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
                              (vm.status === 'stopped' ||
                                vm.status === 'created') &&
                                'bg-muted-foreground',
                              vm.status === 'failed' && 'bg-destructive',
                            )}
                          />
                          {vm.status}
                        </span>
                      </td>

                      {/* Created */}
                      <td className="py-2 px-1.5 font-mono text-[10.5px] text-muted-foreground tabular">
                        {formatRelativeTime(vm.created_at)}
                      </td>

                      {/* Type */}
                      <td className="py-2 px-1.5 font-mono text-[10.5px] text-foreground tabular">
                        {tierLabel}
                      </td>

                      {/* Stops in */}
                      <td className="py-2 px-1.5 font-mono text-[10.5px] text-muted-foreground tabular">
                        {vm.status === 'running'
                          ? 'Manual'
                          : vm.status === 'paused'
                            ? 'Paused'
                            : '—'}
                      </td>

                      {/* Snapshot */}
                      <td className="py-2 px-1.5 font-mono text-[10.5px]">
                        <span className="border border-border bg-muted/30 px-1 py-0.5 text-muted-foreground">
                          {snapTag}
                        </span>
                      </td>

                      {/* Cost */}
                      <td className="py-2 px-1.5 font-mono text-[10.5px] text-foreground tabular">
                        {costStr}
                      </td>

                      {/* IP Address */}
                      <td className="py-2 px-1.5 font-mono text-[10.5px] text-muted-foreground tabular">
                        {vm.ip_address || '—'}
                      </td>

                      {/* URL / SSH */}
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

                      {/* Actions: primary lifecycle button + overflow menu */}
                      <td
                        className="py-2 pl-1.5 pr-2.5 text-right"
                        onClick={(e) => e.stopPropagation()}
                      >
                        <div className="inline-flex items-center gap-1">
                          {vm.status === 'running' ? (
                            <button
                              type="button"
                              onClick={() => setStopConfirmTargets([vm])}
                              title="Stop Sandbox"
                              className="flex h-6 items-center gap-1 border border-border bg-background px-1.5 text-[10px] text-muted-foreground hover:border-warning hover:text-warning transition-colors"
                            >
                              <Square className="h-2.5 w-2.5" />
                              <span>Stop</span>
                            </button>
                          ) : vm.status === 'paused' ? (
                            <button
                              type="button"
                              onClick={() => onResume(vm)}
                              title="Resume from Snapshot"
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

                          <DropdownMenu>
                            <DropdownMenuTrigger asChild>
                              <button
                                type="button"
                                title="More actions"
                                aria-label={`More actions for ${vm.name}`}
                                className="flex h-6 w-6 items-center justify-center border border-border bg-background text-muted-foreground hover:border-[#263fa9] hover:text-foreground transition-colors outline-none data-[state=open]:border-[#263fa9] data-[state=open]:text-foreground"
                              >
                                <MoreHorizontal className="h-3 w-3" />
                              </button>
                            </DropdownMenuTrigger>
                            <DropdownMenuContent align="end" className="w-48">
                              <DropdownMenuItem onSelect={() => openSSHTerminal(vm)}>
                                <Terminal className="h-3.5 w-3.5" />
                                SSH Terminal
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
                              <DropdownMenuItem
                                onSelect={() => onSnapshot(vm)}
                                disabled={vm.status !== 'running'}
                              >
                                <Camera className="h-3.5 w-3.5" />
                                Snapshot
                              </DropdownMenuItem>
                              <DropdownMenuItem onSelect={() => void cloneOne(vm)}>
                                <Copy className="h-3.5 w-3.5" />
                                Clone
                              </DropdownMenuItem>
                              <DropdownMenuItem onSelect={() => void toggleProtection(vm)}>
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

      {/* Live Sandbox Deep-Dive Drawer when a row is selected */}
      {selectedVM && (
        <div className="border border-border bg-background">
          <div className="flex flex-wrap items-center justify-between gap-2 border-b border-border bg-muted/20 px-4 py-2.5">
            <div className="flex items-center gap-3">
              <span className="font-mono text-[11px] uppercase tracking-wider text-[color:var(--link)]">
                SANDBOX INSPECTOR
              </span>
              <span className="font-display text-[14px] font-medium text-foreground">
                {selectedVM.name}
              </span>
              <span className="font-mono text-[11px] text-muted-foreground">
                ({selectedVM.id})
              </span>
              <span className="border border-border bg-background px-2 py-0.5 font-mono text-[11px] text-muted-foreground">
                IP: {selectedVM.ip_address || 'unassigned'} · TAP:{' '}
                {selectedVM.tap_device || '—'}
              </span>
            </div>

            <div className="flex items-center gap-1.5">
              <button
                type="button"
                onClick={() => onConsole(selectedVM)}
                className="flex h-7 items-center gap-1.5 bg-[#263fa9] px-2.5 text-[11px] font-medium text-[#fefefe] hover:bg-[#1e3180]"
              >
                <Terminal className="h-3 w-3" />
                <span>Open Full Console</span>
              </button>
              <button
                type="button"
                onClick={() => onInspect(selectedVM)}
                className="flex h-7 items-center gap-1 border border-border bg-background px-2.5 text-[11px] text-foreground hover:bg-muted/50"
              >
                <span>JSON Spec</span>
              </button>
              <button
                type="button"
                onClick={() => void toggleProtection(selectedVM)}
                className={cn(
                  'flex h-7 items-center gap-1 border border-border bg-background px-2.5 text-[11px] transition-colors',
                  selectedVM.protected
                    ? 'border-[#5e7ff5]/50 text-[#5e7ff5] hover:bg-[#5e7ff5]/10'
                    : 'text-foreground hover:bg-muted/50',
                )}
                title={selectedVM.protected ? 'Unprotect sandbox' : 'Protect sandbox against deletion'}
              >
                <Shield className="h-3 w-3" />
                <span>{selectedVM.protected ? 'Protected' : 'Protect'}</span>
              </button>
              <button
                type="button"
                onClick={() => setSelectedVMId(null)}
                className="flex h-7 w-7 items-center justify-center border border-border text-muted-foreground hover:text-foreground"
                title="Close inspector"
              >
                <X className="h-3.5 w-3.5" />
              </button>
            </div>
          </div>

          {/* Inspector Tabs */}
          <div className="flex overflow-x-auto border-b border-border bg-background px-4">
            {(
              [
                { id: 'overview', label: 'Overview & Telemetry' },
                { id: 'exec', label: 'Exec Runner' },
                { id: 'files', label: 'Filesystem' },
                { id: 'processes', label: 'Processes' },
                { id: 'network', label: 'Network & Ports' },
                { id: 'snapshots', label: 'Snapshots' },
                { id: 'events', label: 'Audit Trail' },
              ] as Array<{ id: DeepDiveTab; label: string }>
            ).map((t) => (
              <button
                key={t.id}
                type="button"
                onClick={() => setDeepDiveTab(t.id)}
                className={cn(
                  'border-b-2 px-3 py-2 text-[12px] transition-colors whitespace-nowrap',
                  deepDiveTab === t.id
                    ? 'border-[#263fa9] text-foreground font-medium'
                    : 'border-transparent text-muted-foreground hover:text-foreground',
                )}
              >
                {t.label}
              </button>
            ))}
          </div>

          <div className="p-4">
            {deepDiveTab === 'overview' && (
              <div className="grid grid-cols-1 gap-4 md:grid-cols-4">
                <div className="border border-border p-3 space-y-1">
                  <div className="flex items-center justify-between text-[11px] text-muted-foreground">
                    <span>Compute Spec</span>
                    <Cpu className="h-3.5 w-3.5" />
                  </div>
                  <div className="font-mono text-[15px] font-medium text-foreground">
                    {selectedVM.vcpus} vCPU · {formatMemory(selectedVM.memory_mb)}
                  </div>
                  <div className="font-mono text-[11px] text-muted-foreground">
                    PID: {selectedVM.pid || 'not running'}
                  </div>
                </div>

                <div className="border border-border p-3 space-y-1">
                  <div className="flex items-center justify-between text-[11px] text-muted-foreground">
                    <span>Network Lease</span>
                    <Activity className="h-3.5 w-3.5" />
                  </div>
                  <div className="font-mono text-[15px] font-medium text-foreground">
                    {selectedVM.ip_address || '—'}
                  </div>
                  <div className="font-mono text-[11px] text-muted-foreground">
                    MAC: {selectedVM.mac_address} · {selectedVM.tap_device}
                  </div>
                </div>

                <div className="border border-border p-3 space-y-1">
                  <div className="flex items-center justify-between text-[11px] text-muted-foreground">
                    <span>Rootfs & Overlay</span>
                    <HardDrive className="h-3.5 w-3.5" />
                  </div>
                  <div className="truncate font-mono text-[12px] font-medium text-foreground">
                    {selectedVM.rootfs_path}
                  </div>
                  <div className="truncate font-mono text-[11px] text-muted-foreground">
                    Disk: {selectedVM.disk_path}
                  </div>
                </div>

                <div className="border border-border p-3 space-y-1">
                  <div className="flex items-center justify-between text-[11px] text-muted-foreground">
                    <span>Lifecycle</span>
                    <Server className="h-3.5 w-3.5" />
                  </div>
                  <div className="font-mono text-[12px] font-medium text-foreground">
                    Created {formatTimestamp(selectedVM.created_at)}
                  </div>
                  <div className="font-mono text-[11px] text-muted-foreground">
                    Status: {selectedVM.status}
                  </div>
                </div>

                <div className="border border-border p-3 space-y-1">
                  <div className="flex items-center justify-between text-[11px] text-muted-foreground">
                    <span>Access</span>
                    <Terminal className="h-3.5 w-3.5" />
                  </div>
                  <div className="flex items-center gap-1 font-mono text-[12px] font-medium text-foreground">
                    <span className="min-w-0 flex-1 truncate" title={selectedAccess.ssh}>
                      {selectedAccess.ssh || 'No SSH target yet'}
                    </span>
                    {selectedAccess.ssh ? (
                      <CopyButton
                        value={selectedAccess.ssh}
                        label="Copy SSH command"
                        className="h-5 w-5 shrink-0"
                      />
                    ) : null}
                  </div>
                  <div className="flex items-center gap-1 font-mono text-[11px] text-muted-foreground">
                    {selectedAccess.url ? (
                      <>
                        <a
                          href={selectedAccess.url}
                          target="_blank"
                          rel="noopener noreferrer"
                          className="min-w-0 flex-1 truncate text-[color:var(--link)] hover:underline"
                          title={selectedAccess.url}
                        >
                          {selectedAccess.url}
                        </a>
                        <CopyButton
                          value={selectedAccess.url}
                          label="Copy URL"
                          className="h-5 w-5 shrink-0"
                        />
                      </>
                    ) : (
                      <span className="min-w-0 flex-1 truncate" title={selectedAccess.urlNote}>
                        No HTTP URL — see JSON Spec for the reason
                      </span>
                    )}
                  </div>
                </div>
              </div>
            )}

            {deepDiveTab === 'exec' && (
              <div className="space-y-3">
                <div className="flex items-center gap-2">
                  <span className="font-mono text-[12px] text-muted-foreground">
                    $
                  </span>
                  <input
                    type="text"
                    value={execCommand}
                    onChange={(e) => setExecCommand(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter') void handleRunExec();
                    }}
                    placeholder="Enter shell command to run inside microVM…"
                    className="h-8 flex-1 border border-border bg-background px-3 font-mono text-[12px] text-foreground focus:border-[#263fa9] focus:outline-none"
                  />
                  <button
                    type="button"
                    disabled={execRunning || selectedVM.status !== 'running'}
                    onClick={() => void handleRunExec()}
                    className="h-8 bg-[#263fa9] px-3 text-[12px] font-medium text-[#fefefe] hover:bg-[#1e3180] disabled:opacity-50"
                  >
                    {execRunning ? 'Running…' : 'Execute via SSH'}
                  </button>
                </div>
                {execMeta && (
                  <div className="flex items-center gap-4 font-mono text-[11px] text-muted-foreground">
                    <span>
                      Exit Code:{' '}
                      <strong
                        className={
                          execMeta.exitCode === 0
                            ? 'text-success'
                            : 'text-destructive'
                        }
                      >
                        {execMeta.exitCode}
                      </strong>
                    </span>
                    <span>Duration: {execMeta.durationMs} ms</span>
                  </div>
                )}
                <pre className="max-h-56 overflow-auto border border-border bg-[#060606] p-3 font-mono text-[11.5px] leading-relaxed text-[#fefefe]">
                  {execOutput ||
                    (selectedVM.status === 'running'
                      ? 'Click "Execute via SSH" to run command on live guest.'
                      : 'Start the sandbox to execute commands.')}
                </pre>
              </div>
            )}

            {deepDiveTab === 'files' && (
              <div className="space-y-3">
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-2">
                    <FolderOpen className="h-4 w-4 text-muted-foreground" />
                    <input
                      type="text"
                      value={currentDir}
                      onChange={(e) => setCurrentDir(e.target.value)}
                      className="h-7 w-64 border border-border bg-background px-2 font-mono text-[12px] text-foreground"
                    />
                  </div>
                  <button
                    type="button"
                    onClick={() => {
                      if (selectedVM.status === 'running') {
                        void fetchFiles(selectedVM.id, currentDir);
                      }
                    }}
                    className="flex h-7 items-center gap-1 border border-border px-2.5 text-[11px] text-foreground hover:bg-muted/50"
                  >
                    <RefreshCw className="h-3 w-3" />
                    <span>Refresh</span>
                  </button>
                </div>
                {selectedVM.status !== 'running' ? (
                  <p className="text-[12px] text-muted-foreground">
                    Sandbox must be running to browse guest filesystem.
                  </p>
                ) : filesLoading && files.length === 0 ? (
                  <p className="font-mono text-[12px] text-muted-foreground">
                    Reading guest directory {currentDir}…
                  </p>
                ) : !filesLoading && files.length === 0 ? (
                  <p className="font-mono text-[12px] text-muted-foreground">
                    No files found in {currentDir}.
                  </p>
                ) : (
                  <div className="max-h-48 overflow-y-auto border border-border divide-y divide-border font-mono text-[11.5px]">
                    {files.map((f) => (
                      <div
                        key={f.path}
                        onClick={() => {
                          if (f.is_dir) setCurrentDir(f.path);
                        }}
                        className={cn(
                          'flex items-center justify-between px-3 py-1.5',
                          f.is_dir && 'cursor-pointer hover:bg-muted/40',
                        )}
                      >
                        <span className="text-foreground">
                          {f.is_dir ? `📁 ${f.name}/` : `📄 ${f.name}`}
                        </span>
                        <span className="text-muted-foreground">
                          {f.is_dir ? 'DIR' : `${f.size} B`}
                        </span>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            )}

            {deepDiveTab === 'processes' && (
              <div className="space-y-2">
                {selectedVM.status !== 'running' ? (
                  <p className="text-[12px] text-muted-foreground">
                    Sandbox must be running to inspect live guest processes.
                  </p>
                ) : processesLoading && processes.length === 0 ? (
                  <p className="font-mono text-[12px] text-muted-foreground">
                    Querying guest process table…
                  </p>
                ) : !processesLoading && processes.length === 0 ? (
                  <p className="font-mono text-[12px] text-muted-foreground">
                    No process entries returned.
                  </p>
                ) : (
                  <div className="max-h-48 overflow-y-auto border border-border">
                    <table className="w-full text-left font-mono text-[11px]">
                      <thead>
                        <tr className="border-b border-border bg-muted/30 text-muted-foreground">
                          <th className="py-1.5 px-2.5">PID</th>
                          <th className="py-1.5 px-2.5">USER</th>
                          <th className="py-1.5 px-2.5">CPU%</th>
                          <th className="py-1.5 px-2.5">MEM%</th>
                          <th className="py-1.5 px-2.5">COMMAND</th>
                        </tr>
                      </thead>
                      <tbody className="divide-y divide-border">
                        {processes.map((proc) => (
                          <tr key={proc.pid}>
                            <td className="py-1 px-2.5 text-foreground">
                              {proc.pid}
                            </td>
                            <td className="py-1 px-2.5 text-muted-foreground">
                              {proc.user}
                            </td>
                            <td className="py-1 px-2.5 text-foreground">
                              {proc.cpu.toFixed(1)}%
                            </td>
                            <td className="py-1 px-2.5 text-foreground">
                              {proc.memory.toFixed(1)}%
                            </td>
                            <td className="py-1 px-2.5 text-muted-foreground truncate max-w-md">
                              {proc.command} {proc.args}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                )}
              </div>
            )}

            {deepDiveTab === 'network' && (
              <div className="grid grid-cols-1 gap-4 md:grid-cols-3 font-mono text-[12px]">
                <div className="border border-border p-3 space-y-1">
                  <div className="text-[11px] text-muted-foreground">
                    Internal Bridge IP
                  </div>
                  <div className="text-foreground font-medium">
                    {selectedVM.ip_address || '—'}
                  </div>
                  <div className="text-[11px] text-muted-foreground">
                    TAP Interface: {selectedVM.tap_device}
                  </div>
                </div>
                <div className="border border-border p-3 space-y-1">
                  <div className="text-[11px] text-muted-foreground">
                    Reachability & Gateway
                  </div>
                  <div className="text-foreground font-medium">
                    {netInspect
                      ? `${netInspect.connectivity} (GW ${netInspect.gateway})`
                      : netInspectLoading
                        ? 'Probing…'
                        : 'Bridge NAT Active'}
                  </div>
                  <div className="text-[11px] text-muted-foreground">
                    MAC: {selectedVM.mac_address}
                  </div>
                </div>
                <div className="border border-border p-3 space-y-1">
                  <div className="text-[11px] text-muted-foreground">
                    Public Routable IP
                  </div>
                  <div className="flex items-center justify-between">
                    <span className="text-foreground">
                      {selectedVM.public_ip ||
                        (selectedVM.public_ip_enabled
                          ? 'NAT Egress Enabled'
                          : 'Disabled')}
                    </span>
                    <button
                      type="button"
                      onClick={() => {
                        void api
                          .setVMPublicIP(
                            selectedVM.id,
                            !selectedVM.public_ip_enabled,
                          )
                          .then(() =>
                            toast.success('Updated public IP routing'),
                          )
                          .catch((err) => toast.error(errorMessage(err)));
                      }}
                      className="border border-border px-2 py-0.5 text-[11px] text-foreground hover:border-[#263fa9]"
                    >
                      Toggle
                    </button>
                  </div>
                </div>
              </div>
            )}

            {deepDiveTab === 'snapshots' && (
              <div className="flex flex-wrap items-center justify-between gap-4 border border-border p-3">
                <div className="space-y-1">
                  <div className="text-[12.5px] font-medium text-foreground">
                    Memory & Disk State Snapshot
                  </div>
                  <div className="font-mono text-[11px] text-muted-foreground">
                    {selectedVM.snapshot_path
                      ? `Snapshot file: ${selectedVM.snapshot_path}`
                      : 'No memory state snapshot saved yet.'}
                  </div>
                </div>
                <div className="flex items-center gap-2">
                  {selectedVM.status === 'running' && (
                    <button
                      type="button"
                      onClick={() => onSnapshot(selectedVM)}
                      className="h-8 bg-[#263fa9] px-3 text-[12px] font-medium text-[#fefefe] hover:bg-[#1e3180]"
                    >
                      Save Memory State
                    </button>
                  )}
                  {selectedVM.status === 'paused' && (
                    <button
                      type="button"
                      onClick={() => onResume(selectedVM)}
                      className="h-8 bg-success px-3 text-[12px] font-medium text-[#fefefe]"
                    >
                      Resume MicroVM
                    </button>
                  )}
                </div>
              </div>
            )}

            {deepDiveTab === 'events' && (
              <div className="max-h-48 overflow-y-auto border border-border divide-y divide-border font-mono text-[11px]">
                {events.filter((e) => e.target.includes(selectedVM.name) || e.target.includes(selectedVM.id))
                  .length === 0 ? (
                  <div className="p-3 text-muted-foreground">
                    Sandbox created at {formatTimestamp(selectedVM.created_at)}.
                  </div>
                ) : (
                  events
                    .filter(
                      (e) =>
                        e.target.includes(selectedVM.name) ||
                        e.target.includes(selectedVM.id),
                    )
                    .map((ev) => (
                      <div
                        key={ev.id}
                        className="flex items-center justify-between px-3 py-1.5"
                      >
                        <span className="text-foreground">{ev.event}</span>
                        <span className="text-muted-foreground">
                          {formatTimestamp(ev.timestamp)}
                        </span>
                      </div>
                    ))
                )}
              </div>
            )}
          </div>
        </div>
      )}

      {/* Critical action confirmation: stop one or many running sandboxes */}
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
    </div>
  );
}
