import { Fragment, useCallback, useEffect, useMemo, useState } from 'react';
import {
  ArrowRightLeft,
  Check,
  ChevronDown,
  ChevronRight,
  Copy,
  Cpu,
  Edit3,
  HardDrive,
  MemoryStick,
  Plus,
  RotateCw,
  Scale,
  Search,
  Server,
  ShieldCheck,
  Trash2,
  X,
} from 'lucide-react';
import { toast } from 'sonner';

import { api, errorMessage } from '@/lib/api';
import { formatMemory, formatRelativeTime } from '@/lib/format';
import type {
  ClusterSchedulingPolicy,
  DaemonConfig,
  StatsResult,
  VM,
  VolumeRecord,
  WorkerNodeRecord,
  WorkerPoolRecord,
  WorkerTokenRecord,
} from '@/lib/types';
import { cn } from '@/lib/utils';

interface WorkersPageProps {
  workers?: WorkerNodeRecord[];
  pools?: WorkerPoolRecord[];
  workerTokens?: WorkerTokenRecord[];
  schedulingPolicy?: ClusterSchedulingPolicy | null;
  stats?: StatsResult | null;
  vms?: VM[];
  volumes?: VolumeRecord[];
  config?: DaemonConfig | null;
  isAdmin?: boolean;
  wsConnected?: boolean;
  onRefresh?: () => void;
}

const DEFAULT_POLICY: ClusterSchedulingPolicy = {
  strategy: 'least_loaded',
  cpu_weight: 0.4,
  memory_weight: 0.4,
  disk_weight: 0.2,
  cpu_soft_overcommit_ratio: 1.5,
  cpu_hard_overcommit_ratio: 3.0,
  cpu_share_weight_base: 1024,
  memory_hard_guarantee: true,
  idle_ram_snapshot_enabled: false,
  idle_threshold_seconds: 900,
  auto_reclaim_on_pressure: true,
  host_reserved_vcpus: 1,
  host_reserved_memory_mb: 512,
  host_reserved_disk_mb: 4096,
  disk_hard_enforcement: true,
};

export function WorkersPage({
  workers: propWorkers,
  pools: propPools,
  workerTokens: propTokens,
  schedulingPolicy: propPolicy,
  stats,
  vms = [],
  volumes = [],
  config = null,
  isAdmin = true,
  wsConnected = false,
  onRefresh,
}: WorkersPageProps) {
  const [workers, setWorkers] = useState<WorkerNodeRecord[]>(propWorkers || []);
  const [pools, setPools] = useState<WorkerPoolRecord[]>(propPools || []);
  const [tokens, setTokens] = useState<WorkerTokenRecord[]>(propTokens || []);
  const [policy, setPolicy] = useState<ClusterSchedulingPolicy>(
    propPolicy ?? DEFAULT_POLICY,
  );
  const [savingPolicy, setSavingPolicy] = useState(false);
  const [loading, setLoading] = useState(false);

  const [search, setSearch] = useState('');
  const [poolFilter, setPoolFilter] = useState('all');
  const [expandedNodeIds, setExpandedNodeIds] = useState<Record<string, boolean>>({});
  const [enrollOpen, setEnrollOpen] = useState(false);
  const [newTokenName, setNewTokenName] = useState('');
  const [newTokenPool, setNewTokenPool] = useState('default');
  const [generatedToken, setGeneratedToken] = useState<string | null>(null);
  const [copiedToken, setCopiedToken] = useState(false);

  const [createPoolOpen, setCreatePoolOpen] = useState(false);
  const [newPoolName, setNewPoolName] = useState('');

  // Edit Node modal state
  const [editNodeTarget, setEditNodeTarget] = useState<WorkerNodeRecord | null>(null);
  const [editPoolId, setEditPoolId] = useState('default');
  const [editStatus, setEditStatus] = useState('ready');
  const [editAllocVcpus, setEditAllocVcpus] = useState(0);
  const [editAllocMemMb, setEditAllocMemMb] = useState(0);
  const [editAllocDiskGb, setEditAllocDiskGb] = useState(0);

  // Pool Migration modal state
  const [migratePoolTarget, setMigratePoolTarget] = useState<WorkerPoolRecord | null>(null);
  const [migrateDestPool, setMigrateDestPool] = useState('');
  const [migrateDestWorker, setMigrateDestWorker] = useState('');
  const [migrateWorkersFlag, setMigrateWorkersFlag] = useState(true);
  const [migrateSandboxesFlag, setMigrateSandboxesFlag] = useState(true);
  const [migrateForceFlag, setMigrateForceFlag] = useState(false);

  const loadData = useCallback(async () => {
    if (wsConnected) return;
    setLoading(true);
    try {
      const [wRes, pRes, tRes, polRes] = await Promise.allSettled([
        api.listWorkers(),
        api.listPools(),
        api.listWorkerTokens(),
        isAdmin ? api.getSchedulingPolicy() : Promise.resolve(null),
      ]);
      if (wRes.status === 'fulfilled') setWorkers(wRes.value || []);
      if (pRes.status === 'fulfilled') setPools(pRes.value || []);
      if (tRes.status === 'fulfilled') setTokens(tRes.value || []);
      if (polRes.status === 'fulfilled' && polRes.value) setPolicy(polRes.value);
    } finally {
      setLoading(false);
    }
  }, [isAdmin, wsConnected]);

  useEffect(() => {
    if (propWorkers) setWorkers(propWorkers);
  }, [propWorkers]);

  useEffect(() => {
    if (propPools) setPools(propPools);
  }, [propPools]);

  useEffect(() => {
    if (propTokens) setTokens(propTokens);
  }, [propTokens]);

  useEffect(() => {
    if (propPolicy) setPolicy(propPolicy);
  }, [propPolicy]);

  useEffect(() => {
    if (!wsConnected) {
      void loadData();
    }
  }, [loadData, wsConnected]);

  const sandboxesByWorker = useMemo(() => {
    const map: Record<string, VM[]> = {};
    for (const vm of vms) {
      const key = vm.worker_id || 'nod_host_local';
      if (!map[key]) map[key] = [];
      map[key].push(vm);
    }
    return map;
  }, [vms]);

  const filteredWorkers = useMemo(() => {
    return workers.filter((w) => {
      const q = search.trim().toLowerCase();
      const matchesQ =
        !q ||
        w.name.toLowerCase().includes(q) ||
        w.id.toLowerCase().includes(q) ||
        w.underlay_ip.toLowerCase().includes(q) ||
        w.pool_id.toLowerCase().includes(q);
      const matchesPool = poolFilter === 'all' || w.pool_id === poolFilter;
      return matchesQ && matchesPool;
    });
  }, [workers, search, poolFilter]);

  const totalVCPUs = useMemo(
    () =>
      workers.reduce((acc, w) => acc + (w.allocated_vcpus || w.total_vcpus || 0), 0) ||
      stats?.resource_limits?.max_total_vcpus ||
      0,
    [workers, stats],
  );
  const usedVCPUs = useMemo(() => {
    const sum = workers.reduce((acc, w) => acc + (w.used_vcpus ?? 0), 0);
    return sum || stats?.running_vcpus || 0;
  }, [workers, stats]);

  const totalMemMB = useMemo(
    () =>
      workers.reduce((acc, w) => acc + (w.allocated_mem_mb || w.total_mem_mb || 0), 0) ||
      stats?.resource_limits?.max_total_memory_mb ||
      0,
    [workers, stats],
  );
  const usedMemMB = useMemo(() => {
    const sum = workers.reduce((acc, w) => acc + (w.used_mem_mb ?? 0), 0);
    return sum || stats?.running_memory_mb || 0;
  }, [workers, stats]);

  function openEditNodeModal(node: WorkerNodeRecord) {
    setEditNodeTarget(node);
    setEditPoolId(node.pool_id || 'default');
    setEditStatus(node.status || 'ready');
    setEditAllocVcpus(node.allocated_vcpus || node.total_vcpus || 0);
    setEditAllocMemMb(node.allocated_mem_mb || node.total_mem_mb || 0);
    setEditAllocDiskGb(
      Math.round((node.allocated_disk_mb || node.total_disk_mb || 0) / 1024),
    );
  }

  function handleSaveNodeEdit(e: React.FormEvent) {
    e.preventDefault();
    if (!editNodeTarget) return;
    const node = editNodeTarget;
    const payload = {
      pool_id: editPoolId.trim() || 'default',
      status: editStatus,
      allocated_vcpus: Math.max(0, editAllocVcpus),
      allocated_mem_mb: Math.max(0, editAllocMemMb),
      allocated_disk_mb: Math.max(0, Math.round(editAllocDiskGb * 1024)),
    };
    setEditNodeTarget(null);

    const toastID = toast.loading(`Updating node ${node.name}…`);
    void api
      .updateWorker(node.id, payload)
      .then(async () => {
        toast.success(`Updated node ${node.name}`, { id: toastID });
        await loadData();
        onRefresh?.();
      })
      .catch((err) => {
        toast.error(`Update failed: ${errorMessage(err)}`, { id: toastID });
      });
  }

  async function handleSavePolicy(e: React.FormEvent) {
    e.preventDefault();
    setSavingPolicy(true);
    const toastID = toast.loading('Saving cluster scheduling policy…');
    try {
      const saved = await api.updateSchedulingPolicy(policy);
      setPolicy(saved);
      toast.success('Scheduling & load-balancer policy updated', { id: toastID });
      onRefresh?.();
    } catch (err) {
      toast.error(`Policy update failed: ${errorMessage(err)}`, { id: toastID });
    } finally {
      setSavingPolicy(false);
    }
  }

  function openMigratePoolModal(pool: WorkerPoolRecord) {
    setMigratePoolTarget(pool);
    const otherPool = pools.find((p) => p.name !== pool.name);
    setMigrateDestPool(otherPool?.name || '');
    setMigrateDestWorker('');
    setMigrateWorkersFlag(true);
    setMigrateSandboxesFlag(true);
    setMigrateForceFlag(false);
  }

  function handleMigratePoolSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!migratePoolTarget) return;
    const src = migratePoolTarget;
    const payload = {
      target_pool_id: migrateDestPool.trim() || undefined,
      target_worker_id: migrateDestWorker.trim() || undefined,
      migrate_workers: migrateWorkersFlag,
      migrate_sandboxes: migrateSandboxesFlag,
      force: migrateForceFlag,
    };
    setMigratePoolTarget(null);

    const toastID = toast.loading(`Migrating pool "${src.name}"…`);
    void api
      .migratePool(src.name || src.id, payload)
      .then(async (res) => {
        const desc = `${res.workers_moved} worker(s) moved, ${res.sandboxes_migrated} sandbox(es) migrated${
          res.errors && res.errors.length > 0 ? ` · errors: ${res.errors.join('; ')}` : ''
        }`;
        if (res.workers_failed > 0 || res.sandboxes_failed > 0) {
          toast.warning(`Pool migration completed with warnings`, {
            id: toastID,
            description: desc,
          });
        } else {
          toast.success(`Migrated pool "${src.name}"`, {
            id: toastID,
            description: desc,
          });
        }
        await loadData();
        onRefresh?.();
      })
      .catch((err) => {
        toast.error(`Pool migration failed: ${errorMessage(err)}`, {
          id: toastID,
        });
      });
  }

  async function handleCordonToggle(node: WorkerNodeRecord) {
    try {
      if (node.status === 'cordoned') {
        await api.uncordonWorker(node.id);
        toast.success(`Uncordoned worker ${node.name}`);
      } else {
        await api.cordonWorker(node.id);
        toast.success(`Cordoned worker ${node.name}`);
      }
      await loadData();
      onRefresh?.();
    } catch (err) {
      toast.error(errorMessage(err));
    }
  }

  async function handleDrain(node: WorkerNodeRecord) {
    try {
      await api.drainWorker(node.id);
      toast.success(`Draining worker ${node.name}`);
      await loadData();
      onRefresh?.();
    } catch (err) {
      toast.error(errorMessage(err));
    }
  }

  async function handleReconcile(node: WorkerNodeRecord) {
    try {
      await api.reconcileWorker(node.id);
      toast.success(`Reconciled worker ${node.name}`);
      await loadData();
      onRefresh?.();
    } catch (err) {
      toast.error(errorMessage(err));
    }
  }

  async function handleReconcileAll() {
    try {
      await api.reconcileAllWorkers();
      toast.success('Reconciled all cluster worker nodes');
      await loadData();
      onRefresh?.();
    } catch (err) {
      toast.error(errorMessage(err));
    }
  }

  async function handleGenerateToken(e: React.FormEvent) {
    e.preventDefault();
    try {
      const res = await api.createWorkerToken({
        name: newTokenName.trim() || 'worker-enroll',
        pool_id: newTokenPool || 'default',
      });
      setGeneratedToken(res.token);
      setNewTokenName('');
      toast.success('Generated mTLS worker enrollment token');
      await loadData();
      onRefresh?.();
    } catch (err) {
      toast.error(errorMessage(err));
    }
  }

  function handleCreatePool(e: React.FormEvent) {
    e.preventDefault();
    const name = newPoolName.trim();
    if (!name) return;
    setNewPoolName('');
    setCreatePoolOpen(false);

    const toastID = toast.loading(`Creating worker pool "${name}"…`);
    void api
      .createPool({ name })
      .then(async () => {
        toast.success(`Created worker pool "${name}"`, { id: toastID });
        await loadData();
      })
      .catch((err) => {
        toast.error(errorMessage(err), { id: toastID });
      });
  }

  return (
    <div className="space-y-5">
      {/* Header */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="font-display text-[22px] font-medium tracking-tight text-foreground">
            Worker Nodes
          </h1>
          <p className="text-[12px] text-muted-foreground">
            Bare-metal KVM hypervisor nodes, placement pools, and mTLS lease
            heartbeats.
          </p>
        </div>

        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={() => void handleReconcileAll()}
            className="flex h-8 items-center gap-1.5 border border-border bg-background px-3 text-[12px] text-muted-foreground hover:text-foreground transition-colors"
          >
            <RotateCw className="h-3.5 w-3.5" />
            <span>Reconcile All</span>
          </button>
          <button
            type="button"
            onClick={() => setCreatePoolOpen((v) => !v)}
            className="flex h-8 items-center gap-1.5 border border-border bg-background px-3 text-[12px] text-foreground hover:bg-muted/40 transition-colors"
          >
            <Plus className="h-3.5 w-3.5" />
            <span>New Pool</span>
          </button>
          <button
            type="button"
            onClick={() => setEnrollOpen((v) => !v)}
            className="flex h-8 items-center gap-1.5 bg-primary px-3 text-[12px] font-medium text-primary-foreground hover:bg-primary-hover transition-colors"
          >
            <ShieldCheck className="h-3.5 w-3.5" />
            <span>Enroll Worker</span>
          </button>
        </div>
      </div>

      {/* Live Cluster Capacity Strip */}
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-4">
        <div className="border border-border bg-background p-3.5 space-y-1">
          <div className="flex items-center justify-between text-[11px] text-muted-foreground">
            <span>Active KVM Nodes</span>
            <Server className="h-3.5 w-3.5" />
          </div>
          <div className="font-mono text-[18px] font-medium text-foreground tabular">
            {workers.filter((w) => w.status === 'ready').length} /{' '}
            {workers.length}
          </div>
          <div className="font-mono text-[11px] text-muted-foreground">
            Pools: {pools.length || 1} configured
          </div>
        </div>

        <div className="border border-border bg-background p-3.5 space-y-1">
          <div className="flex items-center justify-between text-[11px] text-muted-foreground">
            <span>Cluster vCPU Allocation</span>
            <Cpu className="h-3.5 w-3.5" />
          </div>
          <div className="font-mono text-[18px] font-medium text-foreground tabular">
            {usedVCPUs} / {totalVCPUs} vCPU
          </div>
          <div className="h-1.5 w-full bg-muted overflow-hidden">
            <div
              className="h-full bg-primary"
              style={{
                width: `${totalVCPUs > 0 ? Math.min(100, Math.round((usedVCPUs / totalVCPUs) * 100)) : 0}%`,
              }}
            />
          </div>
        </div>

        <div className="border border-border bg-background p-3.5 space-y-1">
          <div className="flex items-center justify-between text-[11px] text-muted-foreground">
            <span>Cluster Memory Allocation</span>
            <MemoryStick className="h-3.5 w-3.5" />
          </div>
          <div className="font-mono text-[18px] font-medium text-foreground tabular">
            {formatMemory(usedMemMB)} / {formatMemory(totalMemMB)}
          </div>
          <div className="h-1.5 w-full bg-muted overflow-hidden">
            <div
              className="h-full bg-primary"
              style={{
                width: `${totalMemMB > 0 ? Math.min(100, Math.round((usedMemMB / totalMemMB) * 100)) : 0}%`,
              }}
            />
          </div>
        </div>

        <div className="border border-border bg-background p-3.5 space-y-1">
          <div className="flex items-center justify-between text-[11px] text-muted-foreground">
            <span>Running MicroVMs</span>
            <HardDrive className="h-3.5 w-3.5" />
          </div>
          <div className="font-mono text-[18px] font-medium text-foreground tabular">
            {stats?.running_vms ?? vms.filter((v) => v.status === 'running').length}{' '}
            /{' '}
            {stats?.resource_limits?.max_running_vms
              ? stats.resource_limits.max_running_vms
              : 'Unlimited'}
          </div>
          <div className="font-mono text-[11px] text-muted-foreground">
            Total provisioned: {stats?.total_vms ?? vms.length}
          </div>
        </div>
      </div>

      {/* Create Pool Drawer */}
      {createPoolOpen && (
        <form
          onSubmit={handleCreatePool}
          className="flex items-center gap-3 border border-border bg-muted/15 p-3.5"
        >
          <span className="text-[12px] font-medium text-foreground">
            New Placement Pool:
          </span>
          <input
            type="text"
            aria-label="New pool name"
            value={newPoolName}
            onChange={(e) => setNewPoolName(e.target.value)}
            placeholder="e.g. nvme-high-iops"
            required
            className="h-8 w-64 border border-border bg-background px-2.5 font-mono text-[12px] text-foreground focus:border-primary focus:outline-none"
          />
          <button
            type="submit"
            className="h-8 bg-primary hover:bg-primary-hover px-3 text-[12px] font-medium text-primary-foreground"
          >
            Create Pool
          </button>
          <button
            type="button"
            aria-label="Close pool form"
            onClick={() => setCreatePoolOpen(false)}
            className="text-muted-foreground hover:text-foreground"
          >
            <X className="h-4 w-4" />
          </button>
        </form>
      )}

      {/* Enroll Worker Modal/Drawer */}
      {enrollOpen && (
        <div className="border border-border bg-muted/15 p-4 space-y-4">
          <div className="flex items-center justify-between">
            <div>
              <h2 className="text-[13.5px] font-medium text-foreground">
                Enroll Bare-Metal KVM Worker Node
              </h2>
              <p className="text-[11.5px] text-muted-foreground">
                Generate a one-time bootstrap token to join a host via{' '}
                <code className="font-mono text-foreground">
                  compute-worker run
                </code>
                .
              </p>
            </div>
            <button
              type="button"
              aria-label="Close worker enrollment form"
              onClick={() => setEnrollOpen(false)}
              className="text-muted-foreground hover:text-foreground"
            >
              <X className="h-4 w-4" />
            </button>
          </div>

          <form
            onSubmit={handleGenerateToken}
            className="flex flex-wrap items-end gap-3"
          >
            <div>
              <label className="block text-[11px] text-muted-foreground mb-1">
                Token Description
              </label>
              <input
                type="text"
                value={newTokenName}
                onChange={(e) => setNewTokenName(e.target.value)}
                placeholder="us-east-baremetal-02"
                className="h-8 w-56 border border-border bg-background px-2.5 font-mono text-[12px] text-foreground"
              />
            </div>
            <div>
              <label className="block text-[11px] text-muted-foreground mb-1">
                Target Pool
              </label>
              <select
                aria-label="Worker pool"
                value={newTokenPool}
                onChange={(e) => setNewTokenPool(e.target.value)}
                className="h-8 border border-border bg-background px-2.5 font-mono text-[12px] text-foreground"
              >
                <option value="default">default</option>
                {pools
                  .filter((p) => p.name !== 'default')
                  .map((p) => (
                    <option key={p.id} value={p.name}>
                      {p.name}
                    </option>
                  ))}
              </select>
            </div>
            <button
              type="submit"
              className="h-8 bg-primary px-3 text-[12px] font-medium text-primary-foreground hover:bg-primary-hover"
            >
              Generate Bootstrap Token
            </button>
          </form>

          {generatedToken && (
            <div className="border border-primary bg-selected/60 p-3 space-y-2">
              <div className="flex items-center justify-between text-[11.5px]">
                <span className="font-medium text-foreground">
                  Run on target KVM host:
                </span>
                <button
                  type="button"
                  onClick={() => {
                    void navigator.clipboard.writeText(
                      `sudo compute-worker run --control-plane http://${window.location.hostname}:18080 --token ${generatedToken} --pool ${newTokenPool}`,
                    );
                    setCopiedToken(true);
                    setTimeout(() => setCopiedToken(false), 1500);
                  }}
                  className="inline-flex items-center gap-1 text-foreground hover:underline"
                >
                  {copiedToken ? (
                    <Check className="h-3.5 w-3.5 text-success" />
                  ) : (
                    <Copy className="h-3.5 w-3.5" />
                  )}
                  <span>Copy Command</span>
                </button>
              </div>
              <pre className="overflow-x-auto bg-[#0a0a0b] p-2.5 font-mono text-[11.5px] text-[#f5f5f4]">
                sudo compute-worker run --control-plane http://
                {window.location.hostname}:18080 --token {generatedToken} --pool{' '}
                {newTokenPool}
              </pre>
            </div>
          )}

          {tokens.length > 0 && (
            <div className="space-y-1.5">
              <div className="text-[11px] font-medium text-muted-foreground">
                Active Enrollment Tokens ({tokens.length})
              </div>
              <div className="divide-y divide-border border border-border bg-background font-mono text-[11px]">
                {tokens.map((t) => {
                  const tokenIdentifier = t.id || t.token;
                  return (
                    <div
                      key={tokenIdentifier}
                      className="flex items-center justify-between px-3 py-1.5"
                    >
                      <div className="flex items-center gap-3">
                        <span className="text-foreground">
                          {t.prefix || t.token.slice(0, 12)}…
                        </span>
                        <span className="text-muted-foreground">
                          pool:{t.pool_id}
                        </span>
                      </div>
                      <button
                        type="button"
                        onClick={() => {
                          void api
                            .deleteWorkerToken(tokenIdentifier)
                            .then(() => {
                              toast.success('Revoked worker token');
                              void loadData();
                              onRefresh?.();
                            })
                            .catch((err) => toast.error(errorMessage(err)));
                        }}
                        className="text-muted-foreground hover:text-destructive"
                      >
                        Revoke
                      </button>
                    </div>
                  );
                })}
              </div>
            </div>
          )}
        </div>
      )}

      {/* Filter Bar */}
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative w-64">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
          <input
            type="text"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Filter by hostname, ID, IP, pool…"
            className="h-8 w-full border border-border bg-background pl-8 pr-3 text-[12px] text-foreground placeholder:text-muted-foreground focus:border-primary focus:outline-none"
          />
        </div>

        <div className="inline-flex border border-border bg-background h-8 text-[12px]">
          <button
            type="button"
            onClick={() => setPoolFilter('all')}
            className={cn(
              'px-3 transition-colors',
              poolFilter === 'all'
                ? 'compute-selected font-medium'
                : 'text-muted-foreground hover:text-foreground',
            )}
          >
            All Pools
          </button>
          {pools.map((p) => (
            <button
              key={p.id}
              type="button"
              onClick={() => setPoolFilter(p.name)}
              className={cn(
                'border-l border-border px-3 transition-colors',
                poolFilter === p.name
                  ? 'compute-selected font-medium'
                  : 'text-muted-foreground hover:text-foreground',
              )}
            >
              {p.name}
            </button>
          ))}
        </div>
      </div>

      {/* Worker Nodes Table */}
      <div className="border border-border bg-background overflow-x-auto">
        <table className="w-full border-collapse text-left text-[12px]">
          <thead>
            <tr className="border-b border-border bg-muted/30 text-[11.5px] text-muted-foreground">
              <th className="py-2.5 px-3 font-normal">Node / ID</th>
              <th className="py-2.5 px-3 font-normal">Pool / Role</th>
              <th className="py-2.5 px-3 font-normal">Status</th>
              <th className="py-2.5 px-3 font-normal">Underlay IP / Arch</th>
              <th className="py-2.5 px-3 font-normal">
                Physical / Allocated / Used
              </th>
              <th className="py-2.5 px-3 font-normal">VMM / Agent</th>
              <th className="py-2.5 px-3 font-normal">Last Heartbeat</th>
              <th className="py-2.5 px-3 text-right font-normal">Actions</th>
            </tr>
          </thead>

          {filteredWorkers.length === 0 ? (
            <tbody>
              <tr>
                <td
                  colSpan={8}
                  className="py-16 text-center text-muted-foreground"
                >
                  {loading
                    ? 'Querying cluster worker registry…'
                    : 'No worker nodes match the current filter.'}
                </td>
              </tr>
            </tbody>
          ) : (
            <tbody className="divide-y divide-border font-mono text-[11.5px]">
              {filteredWorkers.map((node) => {
                const isCordoned = node.status === 'cordoned';
                const isDraining = node.status === 'draining';
                const nodeSandboxes = sandboxesByWorker[node.id] || [];
                const isExpanded = !!expandedNodeIds[node.id];

                const physVcpu = node.physical_vcpus || node.total_vcpus || 0;
                const allocVcpu = node.allocated_vcpus || node.total_vcpus || 0;
                const usedVcpu = node.used_vcpus ?? 0;

                const physMem = node.physical_mem_mb || node.total_mem_mb || 0;
                const allocMem = node.allocated_mem_mb || node.total_mem_mb || 0;
                const usedMem = node.used_mem_mb ?? 0;
                const availMem =
                  node.available_mem_mb ?? Math.max(0, allocMem - usedMem);

                const physDiskGb = Math.round(
                  (node.physical_disk_mb || node.total_disk_mb || 0) / 1024,
                );
                const allocDiskGb = Math.round(
                  (node.allocated_disk_mb || node.total_disk_mb || 0) / 1024,
                );
                const usedDiskGb = Math.round((node.used_disk_mb ?? 0) / 1024);
                const availDiskGb =
                  node.available_disk_mb !== undefined
                    ? Math.round(node.available_disk_mb / 1024)
                    : Math.max(0, allocDiskGb - usedDiskGb);

                return (
                  <Fragment key={node.id}>
                    <tr className="hover:bg-muted/30">
                      <td className="py-3 px-3">
                        <div className="flex items-start gap-1.5">
                          <button
                            type="button"
                            onClick={() =>
                              setExpandedNodeIds((prev) => ({
                                ...prev,
                                [node.id]: !prev[node.id],
                              }))
                            }
                            aria-label={`Toggle sandboxes for ${node.name}`}
                            className="mt-0.5 text-muted-foreground hover:text-foreground"
                          >
                            {isExpanded ? (
                              <ChevronDown className="h-3.5 w-3.5" />
                            ) : (
                              <ChevronRight className="h-3.5 w-3.5" />
                            )}
                          </button>
                          <div>
                            <div className="font-sans font-medium text-foreground">
                              {node.name}
                            </div>
                            <div className="text-[10.5px] text-muted-foreground">
                              {node.id} · {nodeSandboxes.length} sandbox
                              {nodeSandboxes.length === 1 ? '' : 'es'} (
                              {node.running_vms ??
                                nodeSandboxes.filter(
                                  (v) => v.status === 'running',
                                ).length}{' '}
                              running)
                            </div>
                          </div>
                        </div>
                      </td>
                      <td className="py-3 px-3">
                        <span className="border border-border bg-muted/30 px-1.5 py-0.5 text-foreground">
                          {node.pool_id}
                        </span>
                        <div className="mt-0.5 text-[10.5px] text-muted-foreground">
                          {node.role}
                        </div>
                      </td>
                      <td className="py-3 px-3">
                        <span
                          className={cn(
                            'inline-flex items-center gap-1.5 border px-2 py-0.5 text-[10.5px] uppercase',
                            node.status === 'ready' &&
                              'border-success/40 bg-success/10 text-success',
                            isCordoned &&
                              'border-destructive/40 bg-destructive/10 text-destructive',
                            isDraining &&
                              'border-warning/40 bg-warning/10 text-warning',
                          )}
                        >
                          <span
                            className={cn(
                              'h-1.5 w-1.5',
                              node.status === 'ready' && 'bg-success',
                              isCordoned && 'bg-destructive',
                              isDraining && 'bg-warning',
                            )}
                          />
                          {node.status}
                        </span>
                      </td>
                      <td className="py-3 px-3">
                        <div className="text-foreground">
                          {node.underlay_ip || '127.0.0.1'}
                        </div>
                        <div className="text-[10.5px] text-muted-foreground">
                          {node.arch || 'amd64'}
                        </div>
                      </td>
                      <td className="py-3 px-3 tabular">
                        <div className="text-foreground">
                          {usedVcpu}/{allocVcpu} vCPU (phys {physVcpu}) ·{' '}
                          {formatMemory(usedMem)}/{formatMemory(allocMem)} (phys{' '}
                          {formatMemory(physMem)})
                        </div>
                        <div className="text-[10.5px] text-muted-foreground">
                          Avail: {formatMemory(availMem)} RAM · Disk:{' '}
                          {usedDiskGb}/{allocDiskGb} GB (phys {physDiskGb} GB,
                          avail {availDiskGb} GB)
                        </div>
                      </td>
                      <td className="py-3 px-3">
                        <div className="text-foreground">
                          FC {node.fc_version || '—'}
                        </div>
                        <div className="text-[10.5px] text-muted-foreground">
                          Agent {node.agent_version || '—'}
                        </div>
                      </td>
                      <td className="py-3 px-3 text-muted-foreground">
                        {node.last_heartbeat
                          ? formatRelativeTime(node.last_heartbeat)
                          : 'live'}
                      </td>
                      <td className="py-3 px-3 text-right">
                        <div className="inline-flex items-center gap-1">
                          {isAdmin && (
                            <button
                              type="button"
                              onClick={() => openEditNodeModal(node)}
                              title="Edit Node Allocation & Pool"
                              className="inline-flex items-center gap-1 border border-border bg-background px-2 py-1 text-[11px] text-foreground hover:border-primary"
                            >
                              <Edit3 className="h-3 w-3" />
                              <span>Edit</span>
                            </button>
                          )}
                          <button
                            type="button"
                            onClick={() => void handleCordonToggle(node)}
                            className="border border-border bg-background px-2 py-1 text-[11px] text-foreground hover:border-primary"
                          >
                            {isCordoned ? 'Uncordon' : 'Cordon'}
                          </button>
                          <button
                            type="button"
                            onClick={() => void handleDrain(node)}
                            disabled={isDraining}
                            className="border border-border bg-background px-2 py-1 text-[11px] text-muted-foreground hover:text-foreground disabled:opacity-40"
                          >
                            Drain
                          </button>
                          <button
                            type="button"
                            onClick={() => void handleReconcile(node)}
                            title="Reconcile Node"
                            className="border border-border bg-background p-1 text-muted-foreground hover:text-foreground"
                          >
                            <RotateCw className="h-3.5 w-3.5" />
                          </button>
                          {node.id !== 'nod_host_local' && (
                            <button
                              type="button"
                              onClick={() => {
                                void api
                                  .deleteWorker(node.id)
                                  .then(() => {
                                    toast.success(`Removed worker ${node.name}`);
                                    void loadData();
                                  })
                                  .catch((err) => toast.error(errorMessage(err)));
                              }}
                              title="Remove Worker"
                              className="border border-border bg-background p-1 text-muted-foreground hover:text-destructive"
                            >
                              <Trash2 className="h-3.5 w-3.5" />
                            </button>
                          )}
                        </div>
                      </td>
                    </tr>
                    {isExpanded && (
                      <tr key={`${node.id}-sandboxes`} className="bg-muted/15">
                        <td colSpan={8} className="px-6 py-3">
                          <div className="text-[11px] font-sans font-medium text-muted-foreground mb-2">
                            Sandboxes on {node.name} ({nodeSandboxes.length})
                          </div>
                          {nodeSandboxes.length === 0 ? (
                            <div className="text-[11px] font-sans text-muted-foreground">
                              No sandboxes scheduled on this worker node.
                            </div>
                          ) : (
                            <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-2">
                              {nodeSandboxes.map((vm) => (
                                <div
                                  key={vm.id}
                                  className="border border-border bg-background px-2.5 py-2 flex items-center justify-between gap-2 text-[11px]"
                                >
                                  <div className="min-w-0">
                                    <div className="font-sans font-medium text-foreground truncate">
                                      {vm.name || vm.id}
                                    </div>
                                    <div className="text-[10px] text-muted-foreground truncate">
                                      {vm.id} · {vm.vcpus} vCPU ·{' '}
                                      {formatMemory(vm.memory_mb)}
                                      {vm.pool_id ? ` · pool:${vm.pool_id}` : ''}
                                    </div>
                                  </div>
                                  <div className="flex items-center gap-1 shrink-0">
                                    {vm.auto_hibernated && (
                                      <span
                                        title={
                                          vm.hibernate_reason ||
                                          'Auto-hibernated to SSD'
                                        }
                                        className="border border-warning/40 bg-warning/10 px-1.5 py-0.5 text-[9.5px] uppercase text-warning"
                                      >
                                        SSD Hibernated
                                      </span>
                                    )}
                                    <span className="border border-border bg-muted/30 px-1.5 py-0.5 text-[10px] uppercase text-foreground">
                                      {vm.status}
                                    </span>
                                  </div>
                                </div>
                              ))}
                            </div>
                          )}
                        </td>
                      </tr>
                    )}
                  </Fragment>
                );
              })}
            </tbody>
          )}
        </table>
      </div>

      {/* Pools & Pool Migration */}
      <div className="space-y-3 pt-2">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div>
            <h2 className="text-[15px] font-medium text-foreground">
              Pools &amp; Pool Migration
            </h2>
            <p className="text-[12px] text-muted-foreground">
              Placement pools grouping worker nodes and sandboxes. Migrate
              workers or sandboxes across pools.
            </p>
          </div>
          <span className="border border-border bg-muted/30 px-2 py-0.5 font-mono text-[11px] text-muted-foreground">
            {pools.length} pool{pools.length === 1 ? '' : 's'}
          </span>
        </div>

        <div className="border border-border bg-background overflow-x-auto">
          <table className="w-full border-collapse text-left text-[12px]">
            <thead>
              <tr className="border-b border-border bg-muted/30 text-[11.5px] text-muted-foreground">
                <th className="py-2 px-3 font-normal">Pool</th>
                <th className="py-2 px-3 font-normal">Workers</th>
                <th className="py-2 px-3 font-normal">Sandboxes</th>
                <th className="py-2 px-3 font-normal">Allocated Capacity</th>
                <th className="py-2 px-3 text-right font-normal">Actions</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border font-mono text-[11.5px]">
              {(pools.length > 0
                ? pools
                : [
                    {
                      id: 'default',
                      name: 'default',
                      labels_json: '{}',
                      policy_json: '{}',
                      created_at: '',
                      updated_at: '',
                    },
                  ]
              ).map((pool) => {
                const poolWorkers = workers.filter(
                  (w) => w.pool_id === pool.name || w.pool_id === pool.id,
                );
                const poolVms = vms.filter(
                  (v) =>
                    v.pool_id === pool.name ||
                    v.pool_id === pool.id ||
                    poolWorkers.some((w) => w.id === v.worker_id),
                );
                const poolVcpus = poolWorkers.reduce(
                  (acc, w) => acc + (w.allocated_vcpus || w.total_vcpus || 0),
                  0,
                );
                const poolMemMb = poolWorkers.reduce(
                  (acc, w) => acc + (w.allocated_mem_mb || w.total_mem_mb || 0),
                  0,
                );
                return (
                  <tr key={pool.id || pool.name} className="hover:bg-muted/30">
                    <td className="py-2.5 px-3 font-sans font-medium text-foreground">
                      {pool.name}
                    </td>
                    <td className="py-2.5 px-3 tabular text-foreground">
                      {poolWorkers.length}
                    </td>
                    <td className="py-2.5 px-3 tabular text-foreground">
                      {poolVms.length}
                    </td>
                    <td className="py-2.5 px-3 tabular text-muted-foreground">
                      {poolVcpus} vCPU · {formatMemory(poolMemMb)}
                    </td>
                    <td className="py-2.5 px-3 text-right">
                      {isAdmin && (
                        <button
                          type="button"
                          onClick={() => openMigratePoolModal(pool)}
                          className="inline-flex items-center gap-1 border border-border bg-background px-2.5 py-1 text-[11px] text-foreground hover:border-primary"
                        >
                          <ArrowRightLeft className="h-3 w-3" />
                          <span>Migrate Pool</span>
                        </button>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </div>

      {/* Cluster Load Balancer & Resource Policy */}
      <div className="space-y-3 pt-2">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex items-center gap-2">
            <Scale className="h-4 w-4 text-primary" />
            <div>
              <h2 className="text-[15px] font-medium text-foreground">
                Cluster Load Balancer &amp; Resource Policy
              </h2>
              <p className="text-[12px] text-muted-foreground">
                Placement scoring weights, CPU soft/hard overcommit, memory
                guarantees, SSD auto-hibernation, and reserved host headroom.
              </p>
            </div>
          </div>
          <span className="border border-border bg-muted/30 px-2 py-0.5 font-mono text-[11px] text-muted-foreground">
            strategy:{policy.strategy}
          </span>
        </div>

        <form
          onSubmit={(e) => void handleSavePolicy(e)}
          className="border border-border bg-background p-4 space-y-4"
        >
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-3 text-[12px]">
            <div>
              <label className="block text-[11px] text-muted-foreground mb-1">
                Placement Strategy
              </label>
              <select
                aria-label="Placement Strategy"
                disabled={!isAdmin}
                value={policy.strategy}
                onChange={(e) =>
                  setPolicy((p) => ({
                    ...p,
                    strategy: e.target
                      .value as ClusterSchedulingPolicy['strategy'],
                  }))
                }
                className="h-8 w-full border border-border bg-background px-2 font-mono text-[12px] text-foreground focus:border-primary focus:outline-none disabled:opacity-60"
              >
                <option value="least_loaded">least_loaded</option>
                <option value="round_robin">round_robin</option>
                <option value="bin_pack">bin_pack</option>
                <option value="spread">spread</option>
              </select>
            </div>

            <div>
              <label className="block text-[11px] text-muted-foreground mb-1">
                CPU Soft Overcommit Ratio
              </label>
              <input
                type="number"
                step="0.1"
                min="1"
                disabled={!isAdmin}
                value={policy.cpu_soft_overcommit_ratio}
                onChange={(e) =>
                  setPolicy((p) => ({
                    ...p,
                    cpu_soft_overcommit_ratio: Number(e.target.value) || 1,
                  }))
                }
                className="h-8 w-full border border-border bg-background px-2 font-mono text-[12px] text-foreground tabular focus:border-primary focus:outline-none disabled:opacity-60"
              />
            </div>

            <div>
              <label className="block text-[11px] text-muted-foreground mb-1">
                CPU Hard Overcommit Ratio
              </label>
              <input
                type="number"
                step="0.1"
                min="1"
                disabled={!isAdmin}
                value={policy.cpu_hard_overcommit_ratio}
                onChange={(e) =>
                  setPolicy((p) => ({
                    ...p,
                    cpu_hard_overcommit_ratio: Number(e.target.value) || 1,
                  }))
                }
                className="h-8 w-full border border-border bg-background px-2 font-mono text-[12px] text-foreground tabular focus:border-primary focus:outline-none disabled:opacity-60"
              />
            </div>

            <div>
              <label className="block text-[11px] text-muted-foreground mb-1">
                CPU Share Weight Base
              </label>
              <input
                type="number"
                min="1"
                disabled={!isAdmin}
                value={policy.cpu_share_weight_base}
                onChange={(e) =>
                  setPolicy((p) => ({
                    ...p,
                    cpu_share_weight_base: Number(e.target.value) || 1024,
                  }))
                }
                className="h-8 w-full border border-border bg-background px-2 font-mono text-[12px] text-foreground tabular focus:border-primary focus:outline-none disabled:opacity-60"
              />
            </div>

            <div>
              <label className="block text-[11px] text-muted-foreground mb-1">
                CPU Weight
              </label>
              <input
                type="number"
                step="0.05"
                min="0"
                disabled={!isAdmin}
                value={policy.cpu_weight}
                onChange={(e) =>
                  setPolicy((p) => ({
                    ...p,
                    cpu_weight: Number(e.target.value) || 0,
                  }))
                }
                className="h-8 w-full border border-border bg-background px-2 font-mono text-[12px] text-foreground tabular focus:border-primary focus:outline-none disabled:opacity-60"
              />
            </div>

            <div>
              <label className="block text-[11px] text-muted-foreground mb-1">
                Memory Weight
              </label>
              <input
                type="number"
                step="0.05"
                min="0"
                disabled={!isAdmin}
                value={policy.memory_weight}
                onChange={(e) =>
                  setPolicy((p) => ({
                    ...p,
                    memory_weight: Number(e.target.value) || 0,
                  }))
                }
                className="h-8 w-full border border-border bg-background px-2 font-mono text-[12px] text-foreground tabular focus:border-primary focus:outline-none disabled:opacity-60"
              />
            </div>

            <div>
              <label className="block text-[11px] text-muted-foreground mb-1">
                Disk Weight
              </label>
              <input
                type="number"
                step="0.05"
                min="0"
                disabled={!isAdmin}
                value={policy.disk_weight}
                onChange={(e) =>
                  setPolicy((p) => ({
                    ...p,
                    disk_weight: Number(e.target.value) || 0,
                  }))
                }
                className="h-8 w-full border border-border bg-background px-2 font-mono text-[12px] text-foreground tabular focus:border-primary focus:outline-none disabled:opacity-60"
              />
            </div>

            <div>
              <label className="block text-[11px] text-muted-foreground mb-1">
                Idle Hibernate Threshold (sec)
              </label>
              <input
                type="number"
                min="0"
                disabled={!isAdmin}
                value={policy.idle_threshold_seconds}
                onChange={(e) =>
                  setPolicy((p) => ({
                    ...p,
                    idle_threshold_seconds: Number(e.target.value) || 0,
                  }))
                }
                className="h-8 w-full border border-border bg-background px-2 font-mono text-[12px] text-foreground tabular focus:border-primary focus:outline-none disabled:opacity-60"
              />
            </div>

            <div>
              <label className="block text-[11px] text-muted-foreground mb-1">
                Host Reserved vCPUs
              </label>
              <input
                type="number"
                min="0"
                disabled={!isAdmin}
                value={policy.host_reserved_vcpus}
                onChange={(e) =>
                  setPolicy((p) => ({
                    ...p,
                    host_reserved_vcpus: Number(e.target.value) || 0,
                  }))
                }
                className="h-8 w-full border border-border bg-background px-2 font-mono text-[12px] text-foreground tabular focus:border-primary focus:outline-none disabled:opacity-60"
              />
            </div>

            <div>
              <label className="block text-[11px] text-muted-foreground mb-1">
                Host Reserved Memory (MB)
              </label>
              <input
                type="number"
                min="0"
                disabled={!isAdmin}
                value={policy.host_reserved_memory_mb}
                onChange={(e) =>
                  setPolicy((p) => ({
                    ...p,
                    host_reserved_memory_mb: Number(e.target.value) || 0,
                  }))
                }
                className="h-8 w-full border border-border bg-background px-2 font-mono text-[12px] text-foreground tabular focus:border-primary focus:outline-none disabled:opacity-60"
              />
            </div>

            <div>
              <label className="block text-[11px] text-muted-foreground mb-1">
                Host Reserved Disk (MB)
              </label>
              <input
                type="number"
                min="0"
                disabled={!isAdmin}
                value={policy.host_reserved_disk_mb}
                onChange={(e) =>
                  setPolicy((p) => ({
                    ...p,
                    host_reserved_disk_mb: Number(e.target.value) || 0,
                  }))
                }
                className="h-8 w-full border border-border bg-background px-2 font-mono text-[12px] text-foreground tabular focus:border-primary focus:outline-none disabled:opacity-60"
              />
            </div>
          </div>

          <div className="flex flex-wrap items-center justify-between gap-3 border-t border-border pt-3">
            <div className="flex flex-wrap items-center gap-4 text-[12px]">
              <label className="inline-flex items-center gap-2 text-foreground">
                <input
                  type="checkbox"
                  disabled={!isAdmin}
                  checked={policy.memory_hard_guarantee}
                  onChange={(e) =>
                    setPolicy((p) => ({
                      ...p,
                      memory_hard_guarantee: e.target.checked,
                    }))
                  }
                  className="accent-primary"
                />
                <span>Memory Hard Guarantee</span>
              </label>
              <label className="inline-flex items-center gap-2 text-foreground">
                <input
                  type="checkbox"
                  disabled={!isAdmin}
                  checked={policy.idle_ram_snapshot_enabled}
                  onChange={(e) =>
                    setPolicy((p) => ({
                      ...p,
                      idle_ram_snapshot_enabled: e.target.checked,
                    }))
                  }
                  className="accent-primary"
                />
                <span>Idle RAM → SSD Hibernation</span>
              </label>
              <label className="inline-flex items-center gap-2 text-foreground">
                <input
                  type="checkbox"
                  disabled={!isAdmin}
                  checked={policy.auto_reclaim_on_pressure}
                  onChange={(e) =>
                    setPolicy((p) => ({
                      ...p,
                      auto_reclaim_on_pressure: e.target.checked,
                    }))
                  }
                  className="accent-primary"
                />
                <span>Auto Reclaim on Pressure</span>
              </label>
              <label className="inline-flex items-center gap-2 text-foreground">
                <input
                  type="checkbox"
                  disabled={!isAdmin}
                  checked={policy.disk_hard_enforcement}
                  onChange={(e) =>
                    setPolicy((p) => ({
                      ...p,
                      disk_hard_enforcement: e.target.checked,
                    }))
                  }
                  className="accent-primary"
                />
                <span>Disk Hard Enforcement</span>
              </label>
            </div>

            {isAdmin && (
              <button
                type="submit"
                disabled={savingPolicy}
                className="h-8 bg-primary hover:bg-primary-hover px-3 text-[12px] font-medium text-primary-foreground disabled:opacity-50"
              >
                {savingPolicy ? 'Saving…' : 'Save Policy'}
              </button>
            )}
          </div>
        </form>
      </div>

      {/* Quotas & Resource Limits (moved from Organization & Quotas) */}
      <div className="space-y-3 pt-2">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div>
            <h2 className="text-[15px] font-medium text-foreground">
              Quotas &amp; Resource Limits
            </h2>
            <p className="text-[12px] text-muted-foreground">
              Cluster and tenant resource allocation, storage usage, and
              control-plane guardrails.
            </p>
          </div>
          <span className="border border-border bg-muted/30 px-2 py-0.5 font-mono text-[11px] text-muted-foreground">
            Default VM: {config?.defaults?.vcpus ?? 2} vCPU ·{' '}
            {formatMemory(config?.defaults?.memory_mb ?? 2048)}
          </span>
        </div>

        {(() => {
          const runningVMs =
            stats?.running_vms ??
            vms.filter((v) => v.status === 'running').length;
          const totalVMs = stats?.total_vms ?? vms.length;
          const snapshottedCount =
            stats?.snapshotted_vms ??
            vms.filter((v) => v.status === 'paused').length;
          const volumeGB = volumes.reduce(
            (acc, v) => acc + (v.size_gb || 0),
            0,
          );
          const allocatedVcpus = vms.reduce(
            (acc, v) => acc + (v.vcpus || 0),
            0,
          );
          const allocatedRamMB = vms.reduce(
            (acc, v) => acc + (v.memory_mb || 0),
            0,
          );
          const allocatedDiskGB = Math.round(
            vms.reduce((acc, v) => acc + (v.disk_mb || 20480), 0) / 1024,
          );
          const runningVcpus =
            stats?.running_vcpus ??
            vms
              .filter((v) => v.status === 'running')
              .reduce((acc, v) => acc + (v.vcpus || 0), 0);
          const limits = config?.resources ?? stats?.resource_limits;

          return (
            <div className="grid grid-cols-1 md:grid-cols-3 border border-border bg-background divide-y md:divide-y-0 md:divide-x divide-border">
              <div className="p-4 space-y-1">
                <div className="text-xs text-muted-foreground">
                  Allocated Compute &amp; Memory
                </div>
                <div className="font-display text-xl font-medium text-foreground tabular">
                  {allocatedVcpus} vCPU · {formatMemory(allocatedRamMB)}
                </div>
                <div className="font-mono text-[11px] text-muted-foreground">
                  {runningVMs} running ({runningVcpus} vCPU active) / {totalVMs}{' '}
                  total sandboxes
                </div>
              </div>

              <div className="p-4 space-y-1">
                <div className="text-xs text-muted-foreground">
                  Provisioned Storage &amp; Saved State
                </div>
                <div className="font-display text-xl font-medium text-foreground tabular">
                  {allocatedDiskGB + volumeGB} GB
                </div>
                <div className="font-mono text-[11px] text-muted-foreground">
                  {allocatedDiskGB} GB sandbox disks · {volumeGB} GB volumes (
                  {volumes.length}) · {snapshottedCount}{' '}
                  {snapshottedCount === 1 ? 'paused state' : 'paused states'}
                </div>
              </div>

              <div className="p-4 space-y-1">
                <div className="text-xs text-muted-foreground">
                  Host Resource Guardrails
                </div>
                <div className="font-display text-xl font-medium text-foreground tabular">
                  {limits?.max_total_vcpus && limits.max_total_vcpus > 0
                    ? `${limits.max_total_vcpus} vCPU`
                    : 'Unlimited vCPU'}
                </div>
                <div className="font-mono text-[11px] text-success">
                  Max RAM:{' '}
                  {limits?.max_total_memory_mb &&
                  limits.max_total_memory_mb > 0
                    ? formatMemory(limits.max_total_memory_mb)
                    : 'Unlimited'}{' '}
                  · Max VMs:{' '}
                  {limits?.max_running_vms && limits.max_running_vms > 0
                    ? limits.max_running_vms
                    : 'Unlimited'}
                </div>
                <div className="font-mono text-[10.5px] text-muted-foreground">
                  Max Disk:{' '}
                  {limits?.max_total_disk_mb && limits.max_total_disk_mb > 0
                    ? `${Math.round(limits.max_total_disk_mb / 1024)} GB`
                    : 'Unlimited'}{' '}
                  · Min Free Disk:{' '}
                  {limits?.min_free_disk_space_mb &&
                  limits.min_free_disk_space_mb > 0
                    ? `${Math.round(limits.min_free_disk_space_mb / 1024)} GB`
                    : 'Default'}
                </div>
              </div>
            </div>
          );
        })()}
      </div>

      {/* Edit Node Allocation & Pool Modal */}
      {editNodeTarget && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
          role="dialog"
          aria-modal="true"
          aria-label="Edit Worker Node"
        >
          <form
            onSubmit={handleSaveNodeEdit}
            className="w-full max-w-md border border-border bg-background p-5 space-y-4"
          >
            <div className="flex items-center justify-between border-b border-border pb-3">
              <div>
                <h3 className="text-[14px] font-medium text-foreground">
                  Edit Node Allocation &amp; Pool
                </h3>
                <p className="font-mono text-[11px] text-muted-foreground">
                  {editNodeTarget.name} ({editNodeTarget.id})
                </p>
              </div>
              <button
                type="button"
                aria-label="Close edit node dialog"
                onClick={() => setEditNodeTarget(null)}
                className="text-muted-foreground hover:text-foreground"
              >
                <X className="h-4 w-4" />
              </button>
            </div>

            <div className="space-y-3 text-[12px]">
              <div className="grid grid-cols-2 gap-3">
                <div>
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    Pool
                  </label>
                  <select
                    aria-label="Node Pool"
                    value={editPoolId}
                    onChange={(e) => setEditPoolId(e.target.value)}
                    className="h-8 w-full border border-border bg-background px-2 font-mono text-[12px] text-foreground focus:border-primary focus:outline-none"
                  >
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

                <div>
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    Status
                  </label>
                  <select
                    aria-label="Node Status"
                    value={editStatus}
                    onChange={(e) => setEditStatus(e.target.value)}
                    className="h-8 w-full border border-border bg-background px-2 font-mono text-[12px] text-foreground focus:border-primary focus:outline-none"
                  >
                    <option value="ready">ready</option>
                    <option value="cordoned">cordoned</option>
                    <option value="draining">draining</option>
                    <option value="offline">offline</option>
                  </select>
                </div>
              </div>

              <div className="grid grid-cols-3 gap-2">
                <div>
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    Alloc vCPUs (max{' '}
                    {editNodeTarget.physical_vcpus || editNodeTarget.total_vcpus}
                    )
                  </label>
                  <input
                    type="number"
                    min={0}
                    max={
                      editNodeTarget.physical_vcpus ||
                      editNodeTarget.total_vcpus ||
                      undefined
                    }
                    value={editAllocVcpus}
                    onChange={(e) =>
                      setEditAllocVcpus(Number(e.target.value) || 0)
                    }
                    className="h-8 w-full border border-border bg-background px-2 font-mono text-[12px] text-foreground tabular focus:border-primary focus:outline-none"
                  />
                </div>
                <div>
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    Alloc RAM MB (max{' '}
                    {editNodeTarget.physical_mem_mb || editNodeTarget.total_mem_mb}
                    )
                  </label>
                  <input
                    type="number"
                    min={0}
                    max={
                      editNodeTarget.physical_mem_mb ||
                      editNodeTarget.total_mem_mb ||
                      undefined
                    }
                    value={editAllocMemMb}
                    onChange={(e) =>
                      setEditAllocMemMb(Number(e.target.value) || 0)
                    }
                    className="h-8 w-full border border-border bg-background px-2 font-mono text-[12px] text-foreground tabular focus:border-primary focus:outline-none"
                  />
                </div>
                <div>
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    Alloc Disk GB (max{' '}
                    {Math.round(
                      (editNodeTarget.physical_disk_mb ||
                        editNodeTarget.total_disk_mb ||
                        0) / 1024,
                    )}
                    )
                  </label>
                  <input
                    type="number"
                    min={0}
                    max={
                      Math.round(
                        (editNodeTarget.physical_disk_mb ||
                          editNodeTarget.total_disk_mb ||
                          0) / 1024,
                      ) || undefined
                    }
                    value={editAllocDiskGb}
                    onChange={(e) =>
                      setEditAllocDiskGb(Number(e.target.value) || 0)
                    }
                    className="h-8 w-full border border-border bg-background px-2 font-mono text-[12px] text-foreground tabular focus:border-primary focus:outline-none"
                  />
                </div>
              </div>
            </div>

            <div className="flex justify-end gap-2 border-t border-border pt-3">
              <button
                type="button"
                onClick={() => setEditNodeTarget(null)}
                className="h-8 border border-border bg-background px-3 text-[12px] text-foreground hover:bg-muted/40"
              >
                Cancel
              </button>
              <button
                type="submit"
                className="h-8 bg-primary hover:bg-primary-hover px-3 text-[12px] font-medium text-primary-foreground"
              >
                Save Allocation
              </button>
            </div>
          </form>
        </div>
      )}

      {/* Migrate Pool Modal */}
      {migratePoolTarget && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
          role="dialog"
          aria-modal="true"
          aria-label="Migrate Pool"
        >
          <form
            onSubmit={handleMigratePoolSubmit}
            className="w-full max-w-md border border-border bg-background p-5 space-y-4"
          >
            <div className="flex items-center justify-between border-b border-border pb-3">
              <div>
                <h3 className="text-[14px] font-medium text-foreground">
                  Migrate Pool &ldquo;{migratePoolTarget.name}&rdquo;
                </h3>
                <p className="text-[11px] text-muted-foreground">
                  Move workers and/or sandboxes to a destination pool or worker
                  node.
                </p>
              </div>
              <button
                type="button"
                aria-label="Close migrate pool dialog"
                onClick={() => setMigratePoolTarget(null)}
                className="text-muted-foreground hover:text-foreground"
              >
                <X className="h-4 w-4" />
              </button>
            </div>

            <div className="space-y-3 text-[12px]">
              <div>
                <label className="block text-[11px] text-muted-foreground mb-1">
                  Destination Pool
                </label>
                <select
                  aria-label="Destination Pool"
                  value={migrateDestPool}
                  onChange={(e) => setMigrateDestPool(e.target.value)}
                  className="h-8 w-full border border-border bg-background px-2 font-mono text-[12px] text-foreground focus:border-primary focus:outline-none"
                >
                  <option value="">(keep / select target worker)</option>
                  {pools
                    .filter((p) => p.name !== migratePoolTarget.name)
                    .map((p) => (
                      <option key={p.id || p.name} value={p.name}>
                        {p.name}
                      </option>
                    ))}
                </select>
              </div>

              <div>
                <label className="block text-[11px] text-muted-foreground mb-1">
                  Target Worker Node (optional)
                </label>
                <select
                  aria-label="Target Worker Node"
                  value={migrateDestWorker}
                  onChange={(e) => setMigrateDestWorker(e.target.value)}
                  className="h-8 w-full border border-border bg-background px-2 font-mono text-[12px] text-foreground focus:border-primary focus:outline-none"
                >
                  <option value="">Auto-select in destination pool</option>
                  {workers.map((w) => (
                    <option key={w.id} value={w.id}>
                      {w.name} ({w.pool_id} · {w.status})
                    </option>
                  ))}
                </select>
              </div>

              <div className="space-y-1.5 pt-1">
                <label className="flex items-center gap-2 text-foreground">
                  <input
                    type="checkbox"
                    checked={migrateWorkersFlag}
                    onChange={(e) => setMigrateWorkersFlag(e.target.checked)}
                    className="accent-primary"
                  />
                  <span>Migrate worker nodes into destination pool</span>
                </label>
                <label className="flex items-center gap-2 text-foreground">
                  <input
                    type="checkbox"
                    checked={migrateSandboxesFlag}
                    onChange={(e) => setMigrateSandboxesFlag(e.target.checked)}
                    className="accent-primary"
                  />
                  <span>Migrate sandboxes to destination pool/worker</span>
                </label>
                <label className="flex items-center gap-2 text-foreground">
                  <input
                    type="checkbox"
                    checked={migrateForceFlag}
                    onChange={(e) => setMigrateForceFlag(e.target.checked)}
                    className="accent-primary"
                  />
                  <span>Force stop &amp; migrate running sandboxes</span>
                </label>
              </div>
            </div>

            <div className="flex justify-end gap-2 border-t border-border pt-3">
              <button
                type="button"
                onClick={() => setMigratePoolTarget(null)}
                className="h-8 border border-border bg-background px-3 text-[12px] text-foreground hover:bg-muted/40"
              >
                Cancel
              </button>
              <button
                type="submit"
                className="h-8 bg-primary hover:bg-primary-hover px-3 text-[12px] font-medium text-primary-foreground"
              >
                Start Pool Migration
              </button>
            </div>
          </form>
        </div>
      )}
    </div>
  );
}
