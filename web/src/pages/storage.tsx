import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  Camera,
  Database,
  HardDrive,
  Link2,
  Plus,
  RefreshCw,
  Search,
  Trash2,
  Unlink,
  X,
} from 'lucide-react';
import { toast } from 'sonner';

import { api, errorMessage } from '@/lib/api';
import { formatRelativeTime } from '@/lib/format';
import type { DaemonConfig, VM, VolumeRecord } from '@/lib/types';
import { cn } from '@/lib/utils';

interface StoragePageProps {
  volumes?: VolumeRecord[];
  vms?: VM[];
  config?: DaemonConfig | null;
  onRefresh?: () => void;
}

export function StoragePage({
  volumes: propVolumes,
  vms = [],
  config,
  onRefresh,
}: StoragePageProps) {
  const [volumes, setVolumes] = useState<VolumeRecord[]>(propVolumes || []);
  const [loading, setLoading] = useState(false);
  const [search, setSearch] = useState('');
  const [statusFilter, setStatusFilter] = useState('all');

  const [createOpen, setCreateOpen] = useState(false);
  const [volName, setVolName] = useState('');
  const [volSizeGB, setVolSizeGB] = useState(10);
  const [volPool, setVolPool] = useState('default');
  const [volFs, setVolFs] = useState('ext4');
  const [volAttachVM, setVolAttachVM] = useState('');
  const [pools, setPools] = useState<string[]>(['default']);

  const [attachModalVol, setAttachModalVol] = useState<VolumeRecord | null>(
    null,
  );
  const [selectedVMForAttach, setSelectedVMForAttach] = useState('');

  const loadVolumes = useCallback(async () => {
    setLoading(true);
    try {
      const list = await api.listVolumes();
      setVolumes(list || []);
    } catch {
      // Ignore transient errors
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (propVolumes) setVolumes(propVolumes);
  }, [propVolumes]);

  useEffect(() => {
    void loadVolumes();
  }, [loadVolumes]);

  useEffect(() => {
    void api
      .listPools()
      .then((res) => {
        const names = (res || []).map((p) => p.name || p.id).filter(Boolean);
        if (names.length) {
          setPools(names);
          setVolPool((current) => (names.includes(current) ? current : names[0]));
        }
      })
      .catch(() => setPools(['default']));
  }, []);

  const filteredVolumes = useMemo(() => {
    return volumes.filter((v) => {
      const q = search.trim().toLowerCase();
      const matchesQ =
        !q ||
        v.name.toLowerCase().includes(q) ||
        v.id.toLowerCase().includes(q) ||
        v.pool.toLowerCase().includes(q) ||
        (v.attached_vm && v.attached_vm.toLowerCase().includes(q));
      const matchesStatus =
        statusFilter === 'all' || v.status.toLowerCase() === statusFilter;
      return matchesQ && matchesStatus;
    });
  }, [volumes, search, statusFilter]);

  const totalProvisionedGB = useMemo(
    () => volumes.reduce((acc, v) => acc + (v.size_gb || 0), 0),
    [volumes],
  );
  const totalUsedGB = useMemo(
    () => volumes.reduce((acc, v) => acc + (v.used_gb || 0), 0),
    [volumes],
  );
  const maxPoolGB = Math.round(
    (config?.resources?.max_total_disk_mb || 512000) / 1024,
  );

  function handleCreateVolume(e: React.FormEvent) {
    e.preventDefault();
    const name = volName.trim();
    if (!name) return;
    const payload = {
      name,
      size_gb: volSizeGB,
      pool: volPool,
      filesystem: volFs,
      attached_vm: volAttachVM || undefined,
    };
    setCreateOpen(false);
    setVolName('');
    setVolAttachVM('');

    const toastID = toast.loading(`Provisioning volume ${name}…`);
    void api
      .createVolume(payload)
      .then(async (created) => {
        toast.success(`Provisioned persistent volume ${created.name}`, {
          id: toastID,
        });
        await loadVolumes();
        onRefresh?.();
      })
      .catch((err) => {
        toast.error(errorMessage(err), { id: toastID });
      });
  }

  function handleAttach(e: React.FormEvent) {
    e.preventDefault();
    if (!attachModalVol || !selectedVMForAttach) return;
    const vol = attachModalVol;
    const targetVM = selectedVMForAttach;
    setAttachModalVol(null);
    setSelectedVMForAttach('');

    const toastID = toast.loading(`Attaching ${vol.name} to ${targetVM}…`);
    void api
      .attachVolume(vol.id, targetVM)
      .then(async () => {
        toast.success(`Attached ${vol.name} to ${targetVM}`, { id: toastID });
        await loadVolumes();
        onRefresh?.();
      })
      .catch((err) => {
        toast.error(errorMessage(err), { id: toastID });
      });
  }

  async function handleDetach(vol: VolumeRecord) {
    const toastID = toast.loading(`Detaching ${vol.name}…`);
    try {
      await api.detachVolume(vol.id);
      toast.success(`Detached ${vol.name}`, { id: toastID });
      await loadVolumes();
      onRefresh?.();
    } catch (err) {
      toast.error(errorMessage(err), { id: toastID });
    }
  }

  async function handleExpand(vol: VolumeRecord) {
    const toastID = toast.loading(`Expanding ${vol.name} by +10 GB…`);
    try {
      await api.expandVolume(vol.id, 10);
      toast.success(`Expanded ${vol.name} by +10 GB`, { id: toastID });
      await loadVolumes();
      onRefresh?.();
    } catch (err) {
      toast.error(errorMessage(err), { id: toastID });
    }
  }

  async function handleSnapshot(vol: VolumeRecord) {
    const toastID = toast.loading(`Snapshotting ${vol.name}…`);
    try {
      await api.snapshotVolume(vol.id);
      toast.success(`Created block snapshot of ${vol.name}`, { id: toastID });
      await loadVolumes();
      onRefresh?.();
    } catch (err) {
      toast.error(errorMessage(err), { id: toastID });
    }
  }

  async function handleDelete(vol: VolumeRecord) {
    const toastID = toast.loading(`Deleting volume ${vol.name}…`);
    try {
      await api.deleteVolume(vol.id);
      toast.success(`Deleted volume ${vol.name}`, { id: toastID });
      await loadVolumes();
      onRefresh?.();
    } catch (err) {
      toast.error(errorMessage(err), { id: toastID });
    }
  }

  return (
    <div className="space-y-5">
      {/* Header */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="font-display text-[22px] font-medium tracking-tight text-foreground">
            Persistent Volumes
          </h1>
          <p className="text-[12px] text-muted-foreground">
            Attachable ext4/xfs block storage volumes, copy-on-write overlays,
            and live volume snapshots.
          </p>
        </div>

        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={() => void loadVolumes()}
            className="flex h-8 items-center gap-1.5 border border-border bg-background px-3 text-[12px] text-muted-foreground hover:text-foreground"
          >
            <RefreshCw className="h-3.5 w-3.5" />
            <span>Refresh</span>
          </button>
          <button
            type="button"
            onClick={() => setCreateOpen((v) => !v)}
            className="flex h-8 items-center gap-1.5 bg-[#263fa9] px-3 text-[12px] font-medium text-white hover:bg-[#1e3180]"
          >
            <Plus className="h-3.5 w-3.5" />
            <span>New Volume</span>
          </button>
        </div>
      </div>

      {/* Live Storage Telemetry Strip */}
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-4">
        <div className="border border-border bg-background p-3.5 space-y-1">
          <div className="text-[11px] text-muted-foreground">
            Total Volumes
          </div>
          <div className="font-mono text-[18px] font-medium text-foreground tabular">
            {volumes.length}
          </div>
          <div className="font-mono text-[11px] text-muted-foreground">
            {volumes.filter((v) => v.status === 'attached').length} attached ·{' '}
            {volumes.filter((v) => v.status === 'available').length} available
          </div>
        </div>

        <div className="border border-border bg-background p-3.5 space-y-1">
          <div className="text-[11px] text-muted-foreground">
            Provisioned Capacity
          </div>
          <div className="font-mono text-[18px] font-medium text-foreground tabular">
            {totalProvisionedGB} GB / {maxPoolGB} GB
          </div>
          <div className="font-mono text-[11px] text-muted-foreground">
            Used: {totalUsedGB.toFixed(1)} GB
          </div>
        </div>

        <div className="border border-border bg-background p-3.5 space-y-1">
          <div className="text-[11px] text-muted-foreground">
            Host Rootfs &amp; VMs Dir
          </div>
          <div className="truncate font-mono text-[12.5px] font-medium text-foreground">
            {config?.images?.vms_dir || '/var/lib/compute/vms'}
          </div>
          <div className="truncate font-mono text-[11px] text-muted-foreground">
            Images: {config?.images?.images_dir || '/var/lib/compute/images'}
          </div>
        </div>

        <div className="border border-border bg-background p-3.5 space-y-1">
          <div className="text-[11px] text-muted-foreground">
            Volume Snapshots
          </div>
          <div className="font-mono text-[18px] font-medium text-foreground tabular">
            {volumes.reduce((acc, v) => acc + (v.snapshots || 0), 0)}
          </div>
          <div className="font-mono text-[11px] text-muted-foreground">
            Copy-on-write block checkpoints
          </div>
        </div>
      </div>

      {/* Create Volume Drawer */}
      {createOpen && (
        <form
          onSubmit={handleCreateVolume}
          className="border border-border bg-muted/15 p-4 space-y-3"
        >
          <div className="flex items-center justify-between">
            <span className="text-[13px] font-medium text-foreground">
              Provision Persistent Block Volume
            </span>
            <button
              type="button"
              aria-label="Close volume form"
              onClick={() => setCreateOpen(false)}
              className="text-muted-foreground hover:text-foreground"
            >
              <X className="h-4 w-4" />
            </button>
          </div>

          <div className="grid grid-cols-1 gap-3 sm:grid-cols-5">
            <div>
              <label className="block text-[11px] text-muted-foreground mb-1">
                Volume Name
              </label>
              <input
                type="text"
                aria-label="Volume name"
                value={volName}
                onChange={(e) => setVolName(e.target.value)}
                placeholder="pgdata-prod-01"
                required
                className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px] text-foreground"
              />
            </div>
            <div>
              <label className="block text-[11px] text-muted-foreground mb-1">
                Size (GB)
              </label>
              <input
                type="number"
                aria-label="Volume size in gigabytes"
                min={1}
                max={2000}
                value={volSizeGB}
                onChange={(e) => setVolSizeGB(Number(e.target.value) || 10)}
                className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px] text-foreground"
              />
            </div>
            <div>
              <label className="block text-[11px] text-muted-foreground mb-1">
                Storage Pool
              </label>
              <select
                aria-label="Storage pool"
                value={volPool}
                onChange={(e) => setVolPool(e.target.value)}
                className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px] text-foreground"
              >
                {pools.map((pool) => (
                  <option key={pool} value={pool}>
                    {pool}
                  </option>
                ))}
              </select>
            </div>
            <div>
              <label className="block text-[11px] text-muted-foreground mb-1">
                Filesystem
              </label>
              <select
                aria-label="Volume filesystem"
                value={volFs}
                onChange={(e) => setVolFs(e.target.value)}
                className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px] text-foreground"
              >
                <option value="ext4">ext4</option>
                <option value="xfs">xfs</option>
              </select>
            </div>
            <div>
              <label className="block text-[11px] text-muted-foreground mb-1">
                Attach to Sandbox (optional, stopped only)
              </label>
              <select
                aria-label="Attach volume to sandbox"
                value={volAttachVM}
                onChange={(e) => setVolAttachVM(e.target.value)}
                className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px] text-foreground"
              >
                <option value="">Leave unattached</option>
                {vms
                  .filter((v) => v.status !== 'running' && v.status !== 'paused')
                  .map((v) => (
                    <option key={v.id} value={v.name}>
                      {v.name}
                    </option>
                  ))}
              </select>
            </div>
          </div>

          <div className="flex justify-end gap-2">
            <button
              type="button"
              onClick={() => setCreateOpen(false)}
              className="h-8 border border-border px-3 text-[12px] text-muted-foreground"
            >
              Cancel
            </button>
            <button
              type="submit"
              className="h-8 bg-[#263fa9] px-3 text-[12px] font-medium text-white"
            >
              Create Volume
            </button>
          </div>
        </form>
      )}

      {/* Attach Volume Modal */}
      {attachModalVol && (
        <form
          onSubmit={handleAttach}
          className="flex flex-wrap items-center gap-3 border border-[#263fa9] bg-[#0f1634]/40 p-3.5"
        >
          <span className="text-[12px] text-foreground">
            Attach <strong>{attachModalVol.name}</strong> to sandbox:
          </span>
          <select
            value={selectedVMForAttach}
            onChange={(e) => setSelectedVMForAttach(e.target.value)}
            required
            className="h-8 border border-border bg-background px-2.5 font-mono text-[12px] text-foreground"
          >
            <option value="">Select sandbox…</option>
            {vms
              .filter((v) => v.status !== 'running' && v.status !== 'paused')
              .map((v) => (
                <option key={v.id} value={v.name}>
                  {v.name} ({v.status})
                </option>
              ))}
          </select>
          <button
            type="submit"
            className="h-8 bg-[#263fa9] px-3 text-[12px] font-medium text-white"
          >
            Confirm Attach
          </button>
          <button
            type="button"
            onClick={() => setAttachModalVol(null)}
            className="text-muted-foreground hover:text-foreground"
          >
            <X className="h-4 w-4" />
          </button>
        </form>
      )}

      {/* Filter Bar */}
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative w-64">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
          <input
            type="text"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Filter by volume name, ID, VM…"
            className="h-8 w-full border border-border bg-background pl-8 pr-3 text-[12px] text-foreground placeholder:text-muted-foreground focus:border-[#263fa9] focus:outline-none"
          />
        </div>

        <div className="inline-flex border border-border bg-background h-8 text-[12px]">
          {(['all', 'attached', 'available'] as const).map((st) => (
            <button
              key={st}
              type="button"
              onClick={() => setStatusFilter(st)}
              className={cn(
                'px-3 capitalize transition-colors first:border-l-0 border-l border-border',
                statusFilter === st
                  ? 'compute-selected font-medium'
                  : 'text-muted-foreground hover:text-foreground',
              )}
            >
              {st}
            </button>
          ))}
        </div>
      </div>

      {/* Volumes Table */}
      <div className="border border-border bg-background overflow-x-auto">
        <table className="w-full border-collapse text-left text-[12px]">
          <thead>
            <tr className="border-b border-border bg-muted/30 text-[11.5px] text-muted-foreground">
              <th className="py-2.5 px-3 font-normal">Volume / ID</th>
              <th className="py-2.5 px-3 font-normal">Pool / FS</th>
              <th className="py-2.5 px-3 font-normal">Status</th>
              <th className="py-2.5 px-3 font-normal">Attached Sandbox</th>
              <th className="py-2.5 px-3 font-normal">Size / Used</th>
              <th className="py-2.5 px-3 font-normal">IOPS / Throughput</th>
              <th className="py-2.5 px-3 font-normal">Snapshots</th>
              <th className="py-2.5 px-3 font-normal">Created</th>
              <th className="py-2.5 px-3 text-right font-normal">Actions</th>
            </tr>
          </thead>

          {filteredVolumes.length === 0 ? (
            <tbody>
              <tr>
                <td
                  colSpan={9}
                  className="py-16 text-center text-muted-foreground"
                >
                  <HardDrive className="mx-auto h-7 w-7 text-muted-foreground/50 mb-2" />
                  <div className="text-[12.5px]">
                    {loading
                      ? 'Loading persistent block volumes…'
                      : 'No persistent block volumes provisioned yet.'}
                  </div>
                  <p className="text-[11.5px] text-muted-foreground/80 mt-1">
                    Click &ldquo;New Volume&rdquo; to allocate an ext4/xfs block
                    volume and attach it to a sandbox.
                  </p>
                </td>
              </tr>
            </tbody>
          ) : (
            <tbody className="divide-y divide-border font-mono text-[11.5px]">
              {filteredVolumes.map((vol) => (
                <tr key={vol.id} className="hover:bg-muted/30">
                  <td className="py-2.5 px-3">
                    <div className="font-sans font-medium text-foreground">
                      {vol.name}
                    </div>
                    <div className="text-[10.5px] text-muted-foreground">
                      {vol.id} · {vol.device_path}
                    </div>
                  </td>
                  <td className="py-2.5 px-3">
                    <span className="text-foreground">{vol.pool}</span>
                    <div className="text-[10.5px] text-muted-foreground">
                      {vol.filesystem} ({vol.mount_point})
                    </div>
                  </td>
                  <td className="py-2.5 px-3">
                    <span
                      className={cn(
                        'inline-flex items-center gap-1.5 border px-2 py-0.5 text-[10.5px] uppercase',
                        vol.status === 'attached'
                          ? 'border-success/40 bg-success/10 text-success'
                          : 'border-border bg-muted/40 text-muted-foreground',
                      )}
                    >
                      {vol.status}
                    </span>
                  </td>
                  <td className="py-2.5 px-3 text-foreground">
                    {vol.attached_vm || '—'}
                  </td>
                  <td className="py-2.5 px-3 tabular">
                    <div className="text-foreground">{vol.size_gb} GB</div>
                    <div className="text-[10.5px] text-muted-foreground">
                      {vol.used_gb.toFixed(1)} GB used
                    </div>
                  </td>
                  <td className="py-2.5 px-3 tabular">
                    <div className="text-foreground">{vol.iops} IOPS</div>
                    <div className="text-[10.5px] text-muted-foreground">
                      {vol.throughput}
                    </div>
                  </td>
                  <td className="py-2.5 px-3 text-foreground tabular">
                    {vol.snapshots}
                  </td>
                  <td className="py-2.5 px-3 text-muted-foreground">
                    {formatRelativeTime(vol.created_at)}
                  </td>
                  <td className="py-2.5 px-3 text-right">
                    <div className="inline-flex items-center gap-1">
                      {vol.id.startsWith('vol_root_') ? (
                        <span
                          className="text-[10.5px] text-muted-foreground"
                          title="Root filesystem volume; lifecycle follows its sandbox"
                        >
                          managed by sandbox
                        </span>
                      ) : (
                        <>
                      {vol.status === 'attached' ? (
                        <button
                          type="button"
                          onClick={() => void handleDetach(vol)}
                          title="Detach Volume"
                          className="inline-flex items-center gap-1 border border-border bg-background px-2 py-1 text-[11px] text-foreground hover:border-[#263fa9]"
                        >
                          <Unlink className="h-3 w-3" />
                          <span>Detach</span>
                        </button>
                      ) : (
                        <button
                          type="button"
                          onClick={() => {
                            setAttachModalVol(vol);
                            setSelectedVMForAttach(vms[0]?.name || '');
                          }}
                          title="Attach to Sandbox"
                          className="inline-flex items-center gap-1 border border-border bg-background px-2 py-1 text-[11px] text-foreground hover:border-[#263fa9]"
                        >
                          <Link2 className="h-3 w-3" />
                          <span>Attach</span>
                        </button>
                      )}
                      <button
                        type="button"
                        onClick={() => void handleExpand(vol)}
                        title="Expand +10 GB"
                        className="border border-border bg-background px-2 py-1 text-[11px] text-muted-foreground hover:text-foreground"
                      >
                        +10G
                      </button>
                      <button
                        type="button"
                        onClick={() => void handleSnapshot(vol)}
                        title="Snapshot Volume"
                        className="border border-border bg-background p-1 text-muted-foreground hover:text-foreground"
                      >
                        <Camera className="h-3.5 w-3.5" />
                      </button>
                      <button
                        type="button"
                        disabled={vol.status === 'attached'}
                        onClick={() => void handleDelete(vol)}
                        title="Delete Volume"
                        className="border border-border bg-background p-1 text-muted-foreground hover:text-destructive disabled:opacity-40"
                      >
                        <Trash2 className="h-3.5 w-3.5" />
                      </button>
                        </>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          )}
        </table>
      </div>
    </div>
  );
}
