import { useEffect, useMemo, useRef, useState } from 'react';
import { AlertTriangle, ChevronDown, Play, Shield, X } from 'lucide-react';

import { api } from '@/lib/api';
import type {
  CreateVMRequest,
  DaemonConfig,
  NetworkIsolationMode,
  SSHKey,
  StatsResult,
  Template,
  WorkerNodeRecord,
  WorkerPoolRecord,
} from '@/lib/types';
import { cn } from '@/lib/utils';

interface CreateVMDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  config?: DaemonConfig | null;
  workers?: WorkerNodeRecord[];
  pools?: WorkerPoolRecord[];
  templates?: Template[];
  stats?: StatsResult | null;
  onCreate: (payload: CreateVMRequest, autoStart: boolean) => Promise<void>;
  onBulkCreate?: (payloads: CreateVMRequest[], autoStart: boolean) => Promise<void>;
}

type PlacementMode = 'auto' | 'pool' | 'worker';

interface Preset {
  vcpus: 1 | 2 | 4;
  label: string;
  defaultMemoryMb: number;
}

const PRESETS: Preset[] = [
  { vcpus: 1, label: 'micro', defaultMemoryMb: 512 },
  { vcpus: 2, label: 'small', defaultMemoryMb: 1024 },
  { vcpus: 4, label: 'medium', defaultMemoryMb: 2048 },
];

const DEFAULT_DISK_GB = 20;
const HOST_RESERVE_MB = 256;

function formatMemory(mb: number): string {
  if (mb >= 1024) {
    const gb = mb / 1024;
    return Number.isInteger(gb) ? `${gb} GB` : `${gb.toFixed(1)} GB`;
  }
  return `${mb} MB`;
}

export function CreateVMDialog({
  open,
  onOpenChange,
  workers: propWorkers,
  pools: propPools,
  templates: propTemplates,
  stats: propStats,
  onCreate,
  onBulkCreate,
}: CreateVMDialogProps) {
  const [vcpus, setVcpus] = useState<1 | 2 | 4>(1);
  const [memoryMbInput, setMemoryMbInput] = useState('512');
  const [diskGbInput, setDiskGbInput] = useState(String(DEFAULT_DISK_GB));
  const [countInput, setCountInput] = useState('1');
  const [sandboxName, setSandboxName] = useState('');
  const [fromSource, setFromSource] = useState('fresh');
  const [placementMode, setPlacementMode] = useState<PlacementMode>('auto');
  const [selectedPoolId, setSelectedPoolId] = useState('');
  const [selectedWorkerId, setSelectedWorkerId] = useState('');
  const [sshKeyId, setSshKeyId] = useState('');
  const [sshPassword, setSshPassword] = useState('');
  const [networkIsolation, setNetworkIsolation] =
    useState<NetworkIsolationMode>('account');
  const [protectedVM, setProtectedVM] = useState(false);

  const [fetchedTemplates, setFetchedTemplates] = useState<Template[]>([]);
  const [fetchedWorkers, setFetchedWorkers] = useState<WorkerNodeRecord[]>([]);
  const [fetchedPools, setFetchedPools] = useState<WorkerPoolRecord[]>([]);
  const [sshKeys, setSshKeys] = useState<SSHKey[]>([]);
  const [fetchedStats, setFetchedStats] = useState<StatsResult | null>(null);

  const memoryTouchedRef = useRef(false);
  const hasPropTemplatesRef = useRef(Boolean(propTemplates && propTemplates.length > 0));
  const hasPropWorkersRef = useRef(Boolean(propWorkers && propWorkers.length > 0));
  const hasPropPoolsRef = useRef(Boolean(propPools && propPools.length > 0));
  const hasPropStatsRef = useRef(Boolean(propStats));
  hasPropTemplatesRef.current = Boolean(propTemplates && propTemplates.length > 0);
  hasPropWorkersRef.current = Boolean(propWorkers && propWorkers.length > 0);
  hasPropPoolsRef.current = Boolean(propPools && propPools.length > 0);
  hasPropStatsRef.current = Boolean(propStats);

  const templates = useMemo(
    () => (propTemplates && propTemplates.length > 0 ? propTemplates : fetchedTemplates),
    [propTemplates, fetchedTemplates],
  );
  const workers = useMemo(
    () => (propWorkers && propWorkers.length > 0 ? propWorkers : fetchedWorkers),
    [propWorkers, fetchedWorkers],
  );
  const pools = useMemo(
    () => (propPools && propPools.length > 0 ? propPools : fetchedPools),
    [propPools, fetchedPools],
  );
  const stats = propStats ?? fetchedStats;

  useEffect(() => {
    if (!open) return;
    if (!hasPropTemplatesRef.current) {
      void api
        .listTemplates()
        .then((res) => setFetchedTemplates(res || []))
        .catch(() => setFetchedTemplates([]));
    }
    if (!hasPropWorkersRef.current) {
      void api
        .listWorkers()
        .then((res) => setFetchedWorkers(res || []))
        .catch(() => setFetchedWorkers([]));
    }
    if (!hasPropPoolsRef.current) {
      void api
        .listPools()
        .then((res) => setFetchedPools(res || []))
        .catch(() => setFetchedPools([]));
    }
    void api
      .listKeys()
      .then((res) => setSshKeys(res || []))
      .catch(() => setSshKeys([]));
    if (!hasPropStatsRef.current) {
      void api
        .stats()
        .then((res) => setFetchedStats(res))
        .catch(() => setFetchedStats(null));
    }
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onOpenChange(false);
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [open, onOpenChange]);

  const selectedWorker = useMemo(
    () => workers.find((w) => w.id === selectedWorkerId) ?? null,
    [workers, selectedWorkerId],
  );

  // Keep the default memory inside the host's actual headroom on initial open
  // without overwriting user edits when live stats refresh.
  const maxUsableMb = useMemo(() => {
    const available = stats?.host_memory_available_mb ?? 0;
    if (!available) return null;
    return Math.max(128, available - HOST_RESERVE_MB);
  }, [stats]);

  useEffect(() => {
    if (!open || !maxUsableMb || memoryTouchedRef.current) return;
    setMemoryMbInput((current) => {
      const n = Number(current);
      if (!Number.isFinite(n) || n <= 0) return current;
      return String(Math.min(n, maxUsableMb));
    });
  }, [open, maxUsableMb]);

  // Never keep a typed password or stale inputs in memory after the dialog closes.
  useEffect(() => {
    if (!open) {
      memoryTouchedRef.current = false;
      setSandboxName('');
      setVcpus(1);
      setMemoryMbInput('512');
      setCountInput('1');
      setProtectedVM(false);
      setSshPassword('');
      setSshKeyId('');
      setNetworkIsolation('account');
      setDiskGbInput(String(DEFAULT_DISK_GB));
      setFromSource('fresh');
      setPlacementMode('auto');
      setSelectedPoolId('');
      setSelectedWorkerId('');
    }
  }, [open]);

  if (!open) return null;

  const memoryMb = Math.max(128, Math.round(Number(memoryMbInput) || 512));
  const diskGb = Math.max(1, Math.round(Number(diskGbInput) || DEFAULT_DISK_GB));
  const count = Math.max(1, Math.min(20, Math.round(Number(countInput) || 1)));

  const willQueue = maxUsableMb !== null && memoryMb > maxUsableMb;
  const defaultKey = sshKeys.find((k) => k.is_default) ?? null;

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    const baseName = sandboxName.trim();
    const calculatedDiskMb = Math.max(1, Math.round(diskGb)) * 1024;
    const totalToCreate = Math.max(1, count);

    const payloads: CreateVMRequest[] = Array.from({ length: totalToCreate }, (_, i) => {
      let name: string | undefined;
      if (baseName) {
        name = totalToCreate > 1 ? `${baseName}-${i + 1}` : baseName;
      }

      const payload: CreateVMRequest = {
        name: name || undefined,
        vcpus,
        memory_mb: memoryMb,
        disk_mb: calculatedDiskMb,
        public_ip_enabled: true,
        network_isolation: networkIsolation,
        protected: protectedVM,
        ssh_key_id: sshKeyId && sshKeyId !== 'none' ? sshKeyId : undefined,
        ssh_no_default_key: sshKeyId === 'none' || undefined,
        ssh_password: sshPassword || undefined,
      };

      if (placementMode === 'pool' && selectedPoolId) {
        payload.pool_id = selectedPoolId;
      } else if (placementMode === 'worker' && selectedWorkerId) {
        payload.worker_id = selectedWorkerId;
        if (selectedWorker?.pool_id) {
          payload.pool_id = selectedWorker.pool_id;
        }
      }

      if (fromSource.startsWith('tpl:')) {
        payload.template = fromSource.slice(4);
      } else {
        payload.image = 'ubuntu-24';
      }

      return payload;
    });

    onOpenChange(false);
    memoryTouchedRef.current = false;
    setSandboxName('');
    setSshPassword('');
    setNetworkIsolation('account');
    setProtectedVM(false);
    setDiskGbInput(String(DEFAULT_DISK_GB));
    setMemoryMbInput('512');
    setFromSource('fresh');
    setPlacementMode('auto');
    setSelectedPoolId('');
    setSelectedWorkerId('');
    setCountInput('1');

    if (payloads.length > 1) {
      if (onBulkCreate) {
        void onBulkCreate(payloads, true).catch(() => {});
      } else {
        void api.bulkCreateVMs({
          items: payloads.map((p) => ({ ...p, auto_start: true })),
          async: true,
        }).catch(() => {});
      }
      return;
    }

    void onCreate(payloads[0], true).catch(() => {});
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/75 p-4 overflow-y-auto"
      onClick={() => onOpenChange(false)}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="advanced-create-title"
        className="w-full max-w-[460px] max-h-[92vh] overflow-y-auto border border-border bg-background p-6 text-foreground my-auto"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between mb-5">
          <div>
            <h2
              id="advanced-create-title"
              className="text-[15px] font-medium text-foreground"
            >
              New sandbox
            </h2>
            <p className="mt-0.5 font-mono text-[10.5px] text-muted-foreground">
              Ubuntu 24.04 LTS (ubuntu-24)
            </p>
          </div>
          <button
            type="button"
            onClick={() => onOpenChange(false)}
            className="text-muted-foreground hover:text-foreground transition-colors"
            aria-label="Close modal"
          >
            <X className="h-4 w-4" />
          </button>
        </div>

        <form onSubmit={handleSubmit} className="space-y-4">
          <div className="space-y-1.5">
            <label className="block text-[12px] text-muted-foreground">
              Name (optional)
            </label>
            <input
              type="text"
              aria-label="Sandbox name"
              value={sandboxName}
              onChange={(e) =>
                setSandboxName(
                  e.target.value
                    .toLowerCase()
                    .replace(/[^a-z0-9-]+/g, '-')
                    .replace(/-{2,}/g, '-')
                    .slice(0, 63),
                )
              }
              placeholder="sbx-prod-api-01"
              className="h-9 w-full border border-border bg-background px-3 font-mono text-[12.5px] text-foreground placeholder:text-muted-foreground focus:border-primary focus:outline-none"
            />
          </div>

          <div className="space-y-1.5">
            <div className="flex items-center justify-between">
              <label className="block text-[12px] text-muted-foreground">
                Quantity (bulk create)
              </label>
              <div className="flex items-center gap-1">
                {[1, 2, 3, 5, 10].map((qty) => (
                  <button
                    key={qty}
                    type="button"
                    onClick={() => setCountInput(String(qty))}
                    className={cn(
                      'px-1.5 py-0.5 text-[10px] font-mono border transition-colors',
                      count === qty
                        ? 'border-primary bg-selected text-selected-foreground'
                        : 'border-border text-muted-foreground hover:text-foreground',
                    )}
                  >
                    {qty}x
                  </button>
                ))}
              </div>
            </div>
            <input
              type="number"
              aria-label="Quantity of sandboxes to create"
              min={1}
              max={20}
              value={countInput}
              onChange={(e) => setCountInput(e.target.value)}
              className="h-9 w-full border border-border bg-background px-3 font-mono text-[12.5px] text-foreground focus:border-primary focus:outline-none"
            />
          </div>

          <div className="space-y-2">
            <label className="block text-[12px] text-muted-foreground">
              Type
            </label>
            <div className="grid grid-cols-3 gap-2">
              {PRESETS.map((preset) => {
                const selected = preset.vcpus === vcpus;
                return (
                  <button
                    key={preset.vcpus}
                    type="button"
                    onClick={() => {
                      memoryTouchedRef.current = true;
                      setVcpus(preset.vcpus);
                      setMemoryMbInput(String(preset.defaultMemoryMb));
                    }}
                    className={cn(
                      'flex flex-col items-center justify-center border py-2.5 px-1.5 text-center transition-colors',
                      selected
                        ? 'border-primary bg-selected text-selected-foreground'
                        : 'border-border bg-background text-foreground hover:border-foreground/40',
                    )}
                  >
                    <span className="text-[12px] font-medium leading-tight">
                      {preset.label}
                    </span>
                    <span className="mt-1 text-[10px] text-muted-foreground leading-tight">
                      {preset.vcpus} vCPU · {formatMemory(preset.defaultMemoryMb)}
                    </span>
                  </button>
                );
              })}
            </div>
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-1.5">
              <label className="block text-[12px] text-muted-foreground">
                Memory (MB)
              </label>
              <input
                type="number"
                aria-label="Memory in megabytes"
                min={128}
                step={128}
                value={memoryMbInput}
                onChange={(e) => {
                  memoryTouchedRef.current = true;
                  setMemoryMbInput(e.target.value);
                }}
                className="h-9 w-full border border-border bg-background px-3 font-mono text-[12.5px] text-foreground focus:border-primary focus:outline-none"
              />
            </div>
            <div className="space-y-1.5">
              <div className="flex items-center justify-between">
                <label className="block text-[12px] text-muted-foreground">
                  Disk size
                </label>
                <div className="flex items-center gap-1">
                  {[10, 20, 50, 100].map((preset) => (
                    <button
                      key={preset}
                      type="button"
                      onClick={() => setDiskGbInput(String(preset))}
                      className={cn(
                        'px-1.5 py-0.5 text-[10px] font-mono border transition-colors',
                        diskGb === preset
                          ? 'border-primary bg-selected text-selected-foreground'
                          : 'border-border text-muted-foreground hover:text-foreground',
                      )}
                    >
                      {preset}G
                    </button>
                  ))}
                </div>
              </div>
              <div className="relative">
                <input
                  type="number"
                  aria-label="Disk size in GB"
                  min={1}
                  max={2048}
                  value={diskGbInput}
                  onChange={(e) => setDiskGbInput(e.target.value)}
                  className="h-9 w-full border border-border bg-background px-3 pr-8 font-mono text-[12.5px] text-foreground focus:border-primary focus:outline-none"
                />
                <span className="pointer-events-none absolute right-2.5 top-1/2 -translate-y-1/2 font-mono text-xs text-muted-foreground">
                  GB
                </span>
              </div>
            </div>
          </div>

          {templates.length > 0 && (
            <div className="space-y-1.5">
              <label className="block text-[12px] text-muted-foreground">
                Snapshot (optional)
              </label>
              <div className="relative">
                <select
                  aria-label="Sandbox snapshot"
                  value={fromSource}
                  onChange={(e) => setFromSource(e.target.value)}
                  className="h-9 w-full appearance-none border border-border bg-background px-3 pr-8 text-[12.5px] text-foreground focus:border-primary focus:outline-none"
                >
                  <option value="fresh">Fresh Ubuntu 24.04 LTS</option>
                  {templates.map((tpl) => (
                    <option key={`tpl-${tpl.id}`} value={`tpl:${tpl.name}`}>
                      Snapshot: {tpl.name}
                    </option>
                  ))}
                </select>
                <ChevronDown className="pointer-events-none absolute right-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
              </div>
            </div>
          )}

          <div className="space-y-1.5">
            <label className="block text-[12px] text-muted-foreground">
              Placement
            </label>
            <div className="grid grid-cols-3 gap-1.5">
              {(
                [
                  { id: 'auto', label: 'Auto (Scheduler)' },
                  { id: 'pool', label: 'Pool' },
                  { id: 'worker', label: 'Worker Node' },
                ] as const
              ).map((opt) => (
                <button
                  key={opt.id}
                  type="button"
                  onClick={() => setPlacementMode(opt.id)}
                  className={cn(
                    'h-8 border px-2 text-[11px] font-medium transition-colors',
                    placementMode === opt.id
                      ? 'border-primary bg-selected text-selected-foreground'
                      : 'border-border bg-background text-muted-foreground hover:text-foreground',
                  )}
                >
                  {opt.label}
                </button>
              ))}
            </div>

            {placementMode === 'pool' && (
              <div className="relative pt-1">
                <select
                  aria-label="Placement pool"
                  value={selectedPoolId}
                  onChange={(e) => setSelectedPoolId(e.target.value)}
                  className="h-9 w-full appearance-none border border-border bg-background px-3 pr-8 font-mono text-[12px] text-foreground focus:border-primary focus:outline-none"
                >
                  <option value="">Select pool (default)</option>
                  {pools.map((pool) => (
                    <option key={pool.id} value={pool.name || pool.id}>
                      {pool.name} ({workers.filter((w) => w.pool_id === (pool.name || pool.id)).length} nodes)
                    </option>
                  ))}
                </select>
                <ChevronDown className="pointer-events-none absolute right-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
              </div>
            )}

            {placementMode === 'worker' && (
              <div className="space-y-1.5 pt-1">
                <div className="relative">
                  <select
                    aria-label="Placement worker node"
                    value={selectedWorkerId}
                    onChange={(e) => setSelectedWorkerId(e.target.value)}
                    className="h-9 w-full appearance-none border border-border bg-background px-3 pr-8 font-mono text-[12px] text-foreground focus:border-primary focus:outline-none"
                  >
                    <option value="">Select worker node…</option>
                    {workers.map((node) => {
                      const allocVcpus = node.allocated_vcpus || node.total_vcpus || 0;
                      const allocMem = node.allocated_mem_mb || node.total_mem_mb || 0;
                      const usedVcpus = node.used_vcpus ?? 0;
                      const usedMem = node.used_mem_mb ?? 0;
                      return (
                        <option key={node.id} value={node.id}>
                          {node.name} [{node.pool_id}] · {node.status} · {usedVcpus}/{allocVcpus} vCPU · {formatMemory(Math.max(0, allocMem - usedMem))} free
                        </option>
                      );
                    })}
                  </select>
                  <ChevronDown className="pointer-events-none absolute right-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
                </div>
                {selectedWorker && (
                  <div className="border border-border bg-muted/20 px-2.5 py-1.5 font-mono text-[10.5px] text-muted-foreground">
                    Node {selectedWorker.name} ({selectedWorker.status}):{' '}
                    {selectedWorker.used_vcpus ?? 0}/{selectedWorker.allocated_vcpus || selectedWorker.total_vcpus} vCPU ·{' '}
                    {formatMemory(selectedWorker.used_mem_mb ?? 0)}/{formatMemory(selectedWorker.allocated_mem_mb || selectedWorker.total_mem_mb)} RAM ·{' '}
                    {Math.round((selectedWorker.used_disk_mb ?? 0) / 1024)}/{Math.round((selectedWorker.allocated_disk_mb || selectedWorker.total_disk_mb) / 1024)} GB Disk
                  </div>
                )}
              </div>
            )}
          </div>

          <div className="space-y-1.5">
            <label className="block text-[12px] text-muted-foreground">
              SSH key
            </label>
            <div className="relative">
              <select
                aria-label="SSH key"
                value={sshKeyId}
                onChange={(e) => setSshKeyId(e.target.value)}
                className="h-9 w-full appearance-none border border-border bg-background px-3 pr-8 text-[12.5px] text-foreground focus:border-primary focus:outline-none"
              >
                <option value="">
                  {defaultKey
                    ? `Default: ${defaultKey.name} (added automatically)`
                    : 'Account-wide keys only'}
                </option>
                {defaultKey ? (
                  <option value="none">No account key</option>
                ) : null}
                {sshKeys.map((key) => (
                  <option key={key.id} value={key.id}>
                    {key.name}
                    {key.all_sandboxes ? ' (all sandboxes)' : ''}
                  </option>
                ))}
              </select>
              <ChevronDown className="pointer-events-none absolute right-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
            </div>
            <p className="text-[10.5px] text-muted-foreground">
              {defaultKey
                ? `Your default key "${defaultKey.name}" is embedded automatically. Choose another key, or "No account key", to override.`
                : 'Add a default key from Account SSH Keys and it will be embedded automatically here. Keys marked "all sandboxes" are always embedded.'}
            </p>
          </div>

          <div className="space-y-1.5">
            <label className="block text-[12px] text-muted-foreground">
              SSH password (optional)
            </label>
            <input
              type="password"
              aria-label="SSH password"
              autoComplete="new-password"
              value={sshPassword}
              onChange={(e) => setSshPassword(e.target.value)}
              placeholder="Leave blank for public-key-only access"
              className="h-9 w-full border border-border bg-background px-3 font-mono text-[12.5px] text-foreground placeholder:text-muted-foreground focus:border-primary focus:outline-none"
            />
            <p className="text-[10.5px] text-muted-foreground">
              Minimum 8 characters. Applied to the compute and root accounts. If
              left blank, password login is disabled.
            </p>
          </div>

          <div className="space-y-1.5">
            <label className="block text-[12px] text-muted-foreground">
              Sandbox network access
            </label>
            <div className="grid grid-cols-2 gap-2">
              <button
                type="button"
                onClick={() => setNetworkIsolation('account')}
                className={cn(
                  'flex flex-col items-start border px-3 py-2 text-left transition-colors',
                  networkIsolation === 'account'
                    ? 'border-primary bg-selected text-selected-foreground'
                    : 'border-border bg-background text-foreground hover:border-foreground/40',
                )}
              >
                <span className="text-[12px] font-medium leading-tight">
                  Account sandboxes
                </span>
                <span className="mt-1 text-[10px] text-muted-foreground leading-tight">
                  Can communicate with all non-isolated sandboxes in your account
                </span>
              </button>
              <button
                type="button"
                onClick={() => setNetworkIsolation('isolated')}
                className={cn(
                  'flex flex-col items-start border px-3 py-2 text-left transition-colors',
                  networkIsolation === 'isolated'
                    ? 'border-primary bg-selected text-selected-foreground'
                    : 'border-border bg-background text-foreground hover:border-foreground/40',
                )}
              >
                <span className="text-[12px] font-medium leading-tight">
                  Isolated
                </span>
                <span className="mt-1 text-[10px] text-muted-foreground leading-tight">
                  Isolated on its own; blocked from other sandboxes
                </span>
              </button>
            </div>
          </div>

          <div className="flex items-center gap-2 pt-1 pb-1">
            <input
              id="enable-protection"
              type="checkbox"
              checked={protectedVM}
              onChange={(e) => setProtectedVM(e.target.checked)}
              className="h-3.5 w-3.5 accent-primary cursor-pointer"
            />
            <label
              htmlFor="enable-protection"
              className="text-[12px] text-foreground cursor-pointer select-none flex items-center gap-1.5"
            >
              <Shield className="h-3.5 w-3.5 text-primary" />
              <span>Enable deletion protection</span>
            </label>
          </div>

          <div className="space-y-1 text-[11px] text-muted-foreground">
            {stats?.host_memory_available_mb ? (
              <p>
                Host headroom: {formatMemory(stats.host_memory_available_mb)} available
                {stats.host_cpus ? ` across ${stats.host_cpus} vCPU` : ''}.
              </p>
            ) : null}
            {willQueue ? (
              <p className="flex items-start gap-1.5 text-amber-400">
                <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
                Requested memory exceeds current headroom. The sandbox will be
                queued until capacity frees up.
              </p>
            ) : null}
          </div>

          <div className="flex justify-end pt-2">
            <button
              type="submit"
              className="flex h-9 items-center gap-2 bg-primary hover:bg-primary-hover px-4 text-[13px] font-medium text-primary-foreground transition-colors disabled:opacity-50"
            >
              <Play className="h-3 w-3 fill-current" />
              <span>
                {count > 1 ? `Create ${count} sandboxes` : 'Create'}
              </span>
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
