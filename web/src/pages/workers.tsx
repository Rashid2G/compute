import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  Ban,
  Check,
  Copy,
  Cpu,
  HardDrive,
  MemoryStick,
  Plus,
  RefreshCw,
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
  stats?: StatsResult | null;
  vms?: VM[];
  volumes?: VolumeRecord[];
  config?: DaemonConfig | null;
  onRefresh?: () => void;
}

export function WorkersPage({
  workers: propWorkers,
  stats,
  vms = [],
  volumes = [],
  config = null,
  onRefresh,
}: WorkersPageProps) {
  const [workers, setWorkers] = useState<WorkerNodeRecord[]>(propWorkers || []);
  const [pools, setPools] = useState<WorkerPoolRecord[]>([]);
  const [tokens, setTokens] = useState<WorkerTokenRecord[]>([]);
  const [loading, setLoading] = useState(false);

  const [search, setSearch] = useState('');
  const [poolFilter, setPoolFilter] = useState('all');
  const [enrollOpen, setEnrollOpen] = useState(false);
  const [newTokenName, setNewTokenName] = useState('');
  const [newTokenPool, setNewTokenPool] = useState('default');
  const [generatedToken, setGeneratedToken] = useState<string | null>(null);
  const [copiedToken, setCopiedToken] = useState(false);

  const [createPoolOpen, setCreatePoolOpen] = useState(false);
  const [newPoolName, setNewPoolName] = useState('');

  const loadData = useCallback(async () => {
    setLoading(true);
    try {
      const [wRes, pRes, tRes] = await Promise.allSettled([
        api.listWorkers(),
        api.listPools(),
        api.listWorkerTokens(),
      ]);
      if (wRes.status === 'fulfilled') setWorkers(wRes.value || []);
      if (pRes.status === 'fulfilled') setPools(pRes.value || []);
      if (tRes.status === 'fulfilled') setTokens(tRes.value || []);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (propWorkers) setWorkers(propWorkers);
  }, [propWorkers]);

  useEffect(() => {
    void loadData();
  }, [loadData]);

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
      workers.reduce((acc, w) => acc + (w.total_vcpus || 0), 0) ||
      stats?.resource_limits?.max_total_vcpus ||
      0,
    [workers, stats],
  );
  const usedVCPUs = stats?.running_vcpus ?? 0;

  const totalMemMB = useMemo(
    () =>
      workers.reduce((acc, w) => acc + (w.total_mem_mb || 0), 0) ||
      stats?.resource_limits?.max_total_memory_mb ||
      0,
    [workers, stats],
  );
  const usedMemMB = stats?.running_memory_mb ?? 0;

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
            <RefreshCw className="h-3.5 w-3.5" />
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
                {tokens.map((t) => (
                  <div
                    key={t.token}
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
                          .deleteWorkerToken(t.token)
                          .then(() => {
                            toast.success('Revoked worker token');
                            void loadData();
                          })
                          .catch((err) => toast.error(errorMessage(err)));
                      }}
                      className="text-muted-foreground hover:text-destructive"
                    >
                      Revoke
                    </button>
                  </div>
                ))}
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
              <th className="py-2.5 px-3 font-normal">Hardware Capacity</th>
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
                return (
                  <tr key={node.id} className="hover:bg-muted/30">
                    <td className="py-3 px-3">
                      <div className="font-sans font-medium text-foreground">
                        {node.name}
                      </div>
                      <div className="text-[10.5px] text-muted-foreground">
                        {node.id}
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
                        {node.total_vcpus} vCPU ·{' '}
                        {formatMemory(node.total_mem_mb)}
                      </div>
                      <div className="text-[10.5px] text-muted-foreground">
                        Disk: {Math.round((node.total_disk_mb || 0) / 1024)} GB
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
                          <RefreshCw className="h-3.5 w-3.5" />
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
                );
              })}
            </tbody>
          )}
        </table>
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
    </div>
  );
}
