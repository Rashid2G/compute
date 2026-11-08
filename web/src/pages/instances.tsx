import { useEffect, useMemo, useState } from 'react';
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
  Play,
  RefreshCw,
  RotateCcw,
  Search,
  Server,
  Square,
  Terminal,
  Trash2,
  X,
} from 'lucide-react';
import { toast } from 'sonner';

import { api, errorMessage } from '@/lib/api';
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
  onInspect: (vm: VM) => void;
  onConsole: (vm: VM) => void;
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
  onInspect,
  onConsole,
}: InstancesPageProps) {
  const [search, setSearch] = useState('');
  const [stateFilter, setStateFilter] = useState<string>('all');
  const [snapshotFilter, setSnapshotFilter] = useState<string>('all');
  const [typeFilter, setTypeFilter] = useState<string>('all');
  const [selectedIds, setSelectedIds] = useState<string[]>([]);
  const [copiedId, setCopiedId] = useState<string | null>(null);
  const [splitMenuOpen, setSplitMenuOpen] = useState(false);

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

  const filteredVMs = useMemo(() => {
    return vms.filter((vm) => {
      const q = search.trim().toLowerCase();
      const subdomain = `${vm.name}.sbx.compute.dev`.toLowerCase();
      const matchesSearch =
        !q ||
        vm.name.toLowerCase().includes(q) ||
        subdomain.includes(q) ||
        vm.id.toLowerCase().includes(q) ||
        (vm.ip_address && vm.ip_address.toLowerCase().includes(q));

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

  // Load tab-specific real telemetry when selectedVM or deepDiveTab changes
  useEffect(() => {
    if (!selectedVM) return;
    if (deepDiveTab === 'files' && selectedVM.status === 'running') {
      setFilesLoading(true);
      api
        .listVMFiles(selectedVM.id, currentDir)
        .then((res) => setFiles(res || []))
        .catch(() => setFiles([]))
        .finally(() => setFilesLoading(false));
    } else if (deepDiveTab === 'processes' && selectedVM.status === 'running') {
      setProcessesLoading(true);
      api
        .listVMProcesses(selectedVM.id)
        .then((res) => setProcesses(res || []))
        .catch(() => setProcesses([]))
        .finally(() => setProcessesLoading(false));
    } else if (deepDiveTab === 'network') {
      setNetInspectLoading(true);
      api
        .inspectNetwork(selectedVM.id)
        .then((res) => setNetInspect(res))
        .catch(() => setNetInspect(null))
        .finally(() => setNetInspectLoading(false));
    }
  }, [selectedVM, deepDiveTab, currentDir]);

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

  async function handleBatchStop() {
    for (const id of selectedIds) {
      const vm = vms.find((v) => v.id === id);
      if (vm && vm.status === 'running') {
        onStop(vm);
      }
    }
    setSelectedIds([]);
  }

  async function handleBatchSnapshot() {
    for (const id of selectedIds) {
      const vm = vms.find((v) => v.id === id);
      if (vm && vm.status === 'running') {
        onSnapshot(vm);
      }
    }
    setSelectedIds([]);
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
            placeholder="Search name, subdomain, id"
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
          <div className="ml-auto flex items-center gap-2 border border-[#263fa9] bg-[#0f1634]/40 px-2.5 py-1 text-[11px]">
            <span className="font-mono text-foreground">
              {selectedIds.length} selected
            </span>
            <button
              type="button"
              onClick={() => void handleBatchSnapshot()}
              className="text-muted-foreground hover:text-foreground underline"
            >
              Snapshot
            </button>
            <button
              type="button"
              onClick={() => void handleBatchStop()}
              className="text-destructive hover:underline"
            >
              Stop
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
                <th className="py-2 px-1 font-normal">Name / Subdomain</th>
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
                <th className="py-2 pl-1.5 pr-2.5 text-right font-normal">
                  Actions
                </th>
              </tr>
            </thead>

            {filteredVMs.length === 0 ? (
              <tbody>
                <tr>
                  <td colSpan={11} className="py-20 text-center">
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
                  const subdomain = `${vm.name}.sbx.compute.dev`;
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

                      {/* Name / Subdomain */}
                      <td className="py-2 px-1.5">
                        <div className="font-medium text-foreground">
                          {vm.name}
                        </div>
                        <div className="font-mono text-[10px] text-muted-foreground">
                          {subdomain}
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

                      {/* Actions */}
                      <td
                        className="py-2 pl-1.5 pr-2.5 text-right"
                        onClick={(e) => e.stopPropagation()}
                      >
                        <div className="inline-flex items-center gap-1">
                          <button
                            type="button"
                            onClick={() => onConsole(vm)}
                            title="Open SSH Web Terminal"
                            className="flex h-6 items-center gap-1 border border-border bg-background px-1.5 text-[10px] text-foreground hover:border-[#263fa9]"
                          >
                            <Terminal className="h-2.5 w-2.5" />
                            <span>SSH</span>
                          </button>

                          {vm.status === 'running' ? (
                            <>
                              <button
                                type="button"
                                onClick={() => onSnapshot(vm)}
                                title="Pause & Snapshot Memory"
                                className="flex h-6 w-6 items-center justify-center border border-border bg-background text-muted-foreground hover:text-foreground"
                              >
                                <Camera className="h-2.5 w-2.5" />
                              </button>
                              <button
                                type="button"
                                onClick={() => onStop(vm)}
                                title="Stop Sandbox"
                                className="flex h-6 w-6 items-center justify-center border border-border bg-background text-muted-foreground hover:text-destructive"
                              >
                                <Square className="h-2.5 w-2.5" />
                              </button>
                            </>
                          ) : vm.status === 'paused' ? (
                            <button
                              type="button"
                              onClick={() => onResume(vm)}
                              title="Resume from Snapshot"
                              className="flex h-6 w-6 items-center justify-center border border-border bg-background text-success hover:border-success"
                            >
                              <RotateCcw className="h-2.5 w-2.5" />
                            </button>
                          ) : (
                            <button
                              type="button"
                              onClick={() => onRun(vm)}
                              title="Start Sandbox"
                              className="flex h-6 w-6 items-center justify-center border border-border bg-background text-success hover:border-success"
                            >
                              <Play className="h-2.5 w-2.5" />
                            </button>
                          )}

                          <button
                            type="button"
                            onClick={() => onDelete(vm)}
                            title="Delete Sandbox"
                            className="flex h-6 w-6 items-center justify-center border border-border bg-background text-muted-foreground hover:border-destructive hover:text-destructive"
                          >
                            <Trash2 className="h-2.5 w-2.5" />
                          </button>
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
                    SSH Command: ssh compute@{selectedVM.ip_address}
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
                        setFilesLoading(true);
                        api
                          .listVMFiles(selectedVM.id, currentDir)
                          .then((r) => setFiles(r || []))
                          .catch(() => setFiles([]))
                          .finally(() => setFilesLoading(false));
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
                ) : filesLoading ? (
                  <p className="font-mono text-[12px] text-muted-foreground">
                    Reading guest directory {currentDir}…
                  </p>
                ) : files.length === 0 ? (
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
                ) : processesLoading ? (
                  <p className="font-mono text-[12px] text-muted-foreground">
                    Querying guest process table…
                  </p>
                ) : processes.length === 0 ? (
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
                    {netInspectLoading
                      ? 'Probing…'
                      : netInspect
                        ? `${netInspect.connectivity} (GW ${netInspect.gateway})`
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
    </div>
  );
}
