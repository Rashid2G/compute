import { useEffect, useState } from 'react';
import {
  Activity,
  AlertTriangle,
  Cpu,
  HardDrive,
  MemoryStick,
  Network,
  Server,
  Shield,
} from 'lucide-react';

import { api } from '@/lib/api';
import { formatMemory, formatTimestamp } from '@/lib/format';
import type {
  AdminResourcesResponse,
  AuthUser,
  DaemonConfig,
  StatsResult,
  VM,
  VolumeRecord,
  WorkerNodeRecord,
} from '@/lib/types';
import { cn } from '@/lib/utils';

interface ResourcesPageProps {
  stats: StatsResult | null;
  vms: VM[];
  workers: WorkerNodeRecord[];
  volumes: VolumeRecord[];
  config: DaemonConfig | null;
  resources?: AdminResourcesResponse | null;
  wsConnected?: boolean;
  currentUser: AuthUser | null;
  authEnabled: boolean;
}

function formatUptime(seconds: number): string {
  if (!seconds || seconds < 0) return '—';
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor((seconds % 86400) / 3600);
  const mins = Math.floor((seconds % 3600) / 60);
  if (days > 0) return `${days}d ${hours}h ${mins}m`;
  if (hours > 0) return `${hours}h ${mins}m`;
  return `${mins}m`;
}

function UsageBar({
  percent,
  tone = 'ember',
}: {
  percent: number;
  tone?: 'ember' | 'warning' | 'danger';
}) {
  const clamped = Math.max(0, Math.min(100, percent || 0));
  const autoTone =
    clamped >= 90 ? 'danger' : clamped >= 75 ? 'warning' : tone;
  return (
    <div className="h-1.5 w-full bg-muted overflow-hidden">
      <div
        className={cn(
          'h-full transition-all duration-300',
          autoTone === 'danger' && 'bg-destructive',
          autoTone === 'warning' && 'bg-warning',
          autoTone === 'ember' && 'bg-primary',
        )}
        style={{ width: `${clamped}%` }}
      />
    </div>
  );
}

export function ResourcesPage({
  stats,
  vms,
  workers,
  volumes,
  config,
  resources: propResources = null,
  wsConnected = false,
  currentUser,
  authEnabled,
}: ResourcesPageProps) {
  const isAdmin = !authEnabled || currentUser?.role === 'admin';

  const [telemetry, setTelemetry] = useState<AdminResourcesResponse | null>(
    propResources,
  );
  const [fetchError, setFetchError] = useState<string | null>(null);

  useEffect(() => {
    if (propResources) {
      setTelemetry(propResources);
      setFetchError(null);
    }
  }, [propResources]);

  useEffect(() => {
    if (!isAdmin || propResources) return;
    let cancelled = false;
    api
      .adminResources()
      .then((data) => {
        if (!cancelled) {
          setTelemetry(data);
          setFetchError(null);
        }
      })
      .catch((err) => {
        if (!cancelled) {
          setFetchError(err instanceof Error ? err.message : 'Telemetry unavailable');
        }
      });
    return () => {
      cancelled = true;
    };
  }, [isAdmin, propResources]);

  if (!isAdmin) {
    return (
      <div className="border border-border bg-muted/20 p-6 space-y-2">
        <div className="flex items-center gap-2 text-[14px] font-medium text-foreground">
          <Shield className="h-4 w-4 text-primary" />
          <span>Administrator access required</span>
        </div>
        <p className="text-[12px] text-muted-foreground">
          Host hardware utilization, disk I/O, network bandwidth, and platform
          allocation telemetry are restricted to administrator accounts.
        </p>
      </div>
    );
  }

  // Fallback metrics computed from props if /api/v1/admin/resources is unavailable
  const fallbackRunningVMs =
    stats?.running_vms ?? vms.filter((v) => v.status === 'running').length;
  const fallbackTotalVMs = stats?.total_vms ?? vms.length;
  const fallbackAllocatedVcpus = vms.reduce((acc, v) => acc + (v.vcpus || 0), 0);
  const fallbackRunningVcpus =
    stats?.running_vcpus ??
    vms
      .filter((v) => v.status === 'running')
      .reduce((acc, v) => acc + (v.vcpus || 0), 0);
  const fallbackAllocatedRamMB = vms.reduce(
    (acc, v) => acc + (v.memory_mb || 0),
    0,
  );
  const fallbackRunningRamMB = stats?.running_memory_mb ?? 0;
  const fallbackDiskGB = Math.round(
    vms.reduce((acc, v) => acc + (v.disk_mb || 20480), 0) / 1024,
  );
  const fallbackVolumeGB = volumes.reduce((acc, v) => acc + (v.size_gb || 0), 0);
  const fallbackLimits = config?.resources ?? stats?.resource_limits;

  const cpu = telemetry?.cpu;
  const mem = telemetry?.memory;
  const disk = telemetry?.disk;
  const diskIO = telemetry?.disk_io;
  const net = telemetry?.network;
  const sys = telemetry?.system;
  const alloc = telemetry?.allocations;
  const warnings = telemetry?.capacity_warnings ?? [];

  const mounts = disk?.mounts ?? [];
  const devices = diskIO?.devices ?? [];
  const netIfaces = net?.interfaces ?? [];

  const allocVcpus = alloc?.allocated_vcpus ?? fallbackAllocatedVcpus;
  const maxVcpus =
    alloc?.max_total_vcpus ?? fallbackLimits?.max_total_vcpus ?? 0;
  const allocMemMb = alloc?.allocated_memory_mb ?? fallbackAllocatedRamMB;
  const maxMemMb =
    alloc?.max_total_memory_mb ?? fallbackLimits?.max_total_memory_mb ?? 0;
  const allocVmDiskGb = alloc?.allocated_vm_disk_gb ?? fallbackDiskGB;
  const allocVolDiskGb = alloc?.allocated_volume_disk_gb ?? fallbackVolumeGB;
  const totalPlatformDiskGb = allocVmDiskGb + allocVolDiskGb;
  const maxDiskGb =
    alloc?.max_total_disk_gb ??
    (fallbackLimits?.max_total_disk_mb
      ? Math.round(fallbackLimits.max_total_disk_mb / 1024)
      : 0);

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="space-y-1">
          <div className="flex items-center gap-2">
            <h1 className="font-display text-[22px] font-medium tracking-tight text-foreground">
              Resources
            </h1>
            <span className="border border-primary/40 bg-primary/10 px-1.5 py-0.5 font-mono text-[10px] uppercase text-primary dark:text-[var(--accent-bright)]">
              Admin
            </span>
          </div>
          <p className="text-[12px] text-muted-foreground">
            Live host CPU, memory, disk space, block I/O, network throughput, and
            platform allocations across {alloc?.workers.total ?? (workers.length || 1)}{' '}
            node(s).
          </p>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          {telemetry?.timestamp && (
            <span className="font-mono text-[11px] text-muted-foreground">
              Updated {formatTimestamp(telemetry.timestamp)}
            </span>
          )}
          <span
            className={cn(
              'inline-flex h-8 items-center gap-1.5 border px-2.5 font-mono text-[11px]',
              wsConnected
                ? 'border-success/40 bg-success/10 text-success'
                : 'border-border bg-background text-muted-foreground',
            )}
          >
            <span
              className={cn(
                'h-1.5 w-1.5',
                wsConnected ? 'bg-success' : 'bg-muted-foreground',
              )}
            />
            <span>Live · WS</span>
          </span>
        </div>
      </div>

      {/* Capacity Warnings Banner */}
      {warnings.length > 0 && (
        <div
          data-testid="capacity-warnings-banner"
          className="border border-warning/50 bg-warning/10 p-4 space-y-1.5"
        >
          <div className="flex items-center gap-2 text-[12.5px] font-medium text-warning">
            <AlertTriangle className="h-4 w-4 shrink-0" />
            <span>Capacity Warnings ({warnings.length})</span>
          </div>
          <ul className="list-disc pl-5 font-mono text-[11.5px] text-foreground space-y-0.5">
            {warnings.map((w, idx) => (
              <li key={idx}>{w}</li>
            ))}
          </ul>
        </div>
      )}

      {fetchError && !telemetry && (
        <div className="border border-border bg-muted/20 px-4 py-2.5 font-mono text-[11.5px] text-muted-foreground">
          Live host `/api/v1/admin/resources` unavailable ({fetchError}) — showing
          cluster summary fallback.
        </div>
      )}

      {/* Top 4 Host Telemetry Cards */}
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
        {/* Host CPU */}
        <div className="border border-border bg-background p-4 space-y-2">
          <div className="flex items-center justify-between text-[11px] text-muted-foreground">
            <span>Host CPU</span>
            <Cpu className="h-3.5 w-3.5" />
          </div>
          <div className="font-mono text-[20px] font-medium text-foreground tabular">
            {cpu ? `${cpu.usage_percent.toFixed(1)}%` : `${fallbackRunningVcpus} vCPU`}
          </div>
          {cpu && <UsageBar percent={cpu.usage_percent} />}
          <div className="font-mono text-[11px] text-muted-foreground">
            {cpu
              ? `${cpu.cores} cores · load ${cpu.load_1m.toFixed(2)} / ${cpu.load_5m.toFixed(2)} / ${cpu.load_15m.toFixed(2)}`
              : `${fallbackAllocatedVcpus} vCPU provisioned`}
          </div>
          {cpu && (
            <div className="font-mono text-[10.5px] text-muted-foreground">
              user {cpu.user_percent.toFixed(1)}% · sys{' '}
              {cpu.system_percent.toFixed(1)}% · iowait{' '}
              {cpu.iowait_percent.toFixed(1)}%
            </div>
          )}
        </div>

        {/* Host Memory */}
        <div className="border border-border bg-background p-4 space-y-2">
          <div className="flex items-center justify-between text-[11px] text-muted-foreground">
            <span>Host Memory</span>
            <MemoryStick className="h-3.5 w-3.5" />
          </div>
          <div className="font-mono text-[20px] font-medium text-foreground tabular">
            {mem ? `${mem.used_percent.toFixed(1)}%` : formatMemory(fallbackRunningRamMB)}
          </div>
          {mem && <UsageBar percent={mem.used_percent} />}
          <div className="font-mono text-[11px] text-muted-foreground">
            {mem
              ? `${formatMemory(mem.used_mb)} / ${formatMemory(mem.total_mb)} (${formatMemory(mem.available_mb)} free)`
              : `${formatMemory(fallbackAllocatedRamMB)} provisioned`}
          </div>
          {mem && (
            <div className="font-mono text-[10.5px] text-muted-foreground">
              cached {formatMemory(mem.cached_mb)} · buffers{' '}
              {formatMemory(mem.buffers_mb)} · swap{' '}
              {mem.swap_total_mb > 0
                ? `${formatMemory(mem.swap_used_mb)}/${formatMemory(mem.swap_total_mb)}`
                : '0'}
            </div>
          )}
        </div>

        {/* Host Disk Space */}
        <div className="border border-border bg-background p-4 space-y-2">
          <div className="flex items-center justify-between text-[11px] text-muted-foreground">
            <span>Host Disk Space</span>
            <HardDrive className="h-3.5 w-3.5" />
          </div>
          <div className="font-mono text-[20px] font-medium text-foreground tabular">
            {disk ? `${disk.used_percent.toFixed(1)}%` : `${totalPlatformDiskGb} GB`}
          </div>
          {disk && <UsageBar percent={disk.used_percent} />}
          <div className="font-mono text-[11px] text-muted-foreground">
            {disk
              ? `${disk.used_gb.toFixed(1)} GB / ${disk.total_gb.toFixed(1)} GB (${disk.available_gb.toFixed(1)} GB avail)`
              : `${allocVmDiskGb} GB overlays · ${allocVolDiskGb} GB volumes`}
          </div>
          {disk && (
            <div className="truncate font-mono text-[10.5px] text-muted-foreground">
              Path: {disk.path}
            </div>
          )}
        </div>

        {/* Live Disk I/O & Network */}
        <div className="border border-border bg-background p-4 space-y-2">
          <div className="flex items-center justify-between text-[11px] text-muted-foreground">
            <span>Live I/O &amp; Network</span>
            <Activity className="h-3.5 w-3.5" />
          </div>
          <div className="font-mono text-[15px] font-medium text-foreground tabular">
            {diskIO
              ? `R ${diskIO.read_mb_per_sec.toFixed(2)} · W ${diskIO.write_mb_per_sec.toFixed(2)} MB/s`
              : `${fallbackRunningVMs} running sandboxes`}
          </div>
          <div className="font-mono text-[11px] text-muted-foreground">
            {diskIO
              ? `IOPS: ${diskIO.read_iops.toFixed(0)} read / ${diskIO.write_iops.toFixed(0)} write · queue ${diskIO.io_in_progress}`
              : `${fallbackTotalVMs} total sandboxes`}
          </div>
          <div className="font-mono text-[10.5px] text-muted-foreground">
            {net
              ? `Net: ↓ ${net.rx_mb_per_sec.toFixed(2)} MB/s · ↑ ${net.tx_mb_per_sec.toFixed(2)} MB/s`
              : `${workers.filter((w) => w.status === 'ready').length} ready workers`}
          </div>
        </div>
      </div>

      {/* Platform Allocations & Quota Guardrails */}
      <div className="border border-border bg-background">
        <div className="border-b border-border bg-muted/20 px-4 py-2.5 text-[12.5px] font-medium text-foreground">
          Platform Allocations &amp; Quota Utilization
        </div>
        <div className="grid grid-cols-1 divide-y divide-border md:grid-cols-3 md:divide-y-0 md:divide-x">
          {/* vCPUs */}
          <div className="p-4 space-y-2">
            <div className="flex items-center justify-between text-[11.5px] text-muted-foreground">
              <span>Allocated vCPUs</span>
              <span className="font-mono text-foreground">
                {allocVcpus} / {maxVcpus > 0 ? maxVcpus : '∞'} vCPU
              </span>
            </div>
            {maxVcpus > 0 && <UsageBar percent={(allocVcpus / maxVcpus) * 100} />}
            <div className="font-mono text-[11px] text-muted-foreground">
              Sandboxes: {alloc?.sandboxes.vcpus_allocated ?? fallbackAllocatedVcpus}{' '}
              vCPU ({alloc?.sandboxes.running_vcpus ?? fallbackRunningVcpus} running)
              {' · '}CI Jobs: {alloc?.job_vcpus ?? 0} vCPU
            </div>
          </div>

          {/* Memory */}
          <div className="p-4 space-y-2">
            <div className="flex items-center justify-between text-[11.5px] text-muted-foreground">
              <span>Allocated Memory</span>
              <span className="font-mono text-foreground">
                {formatMemory(allocMemMb)} /{' '}
                {maxMemMb > 0 ? formatMemory(maxMemMb) : '∞'}
              </span>
            </div>
            {maxMemMb > 0 && <UsageBar percent={(allocMemMb / maxMemMb) * 100} />}
            <div className="font-mono text-[11px] text-muted-foreground">
              Sandboxes:{' '}
              {formatMemory(
                alloc?.sandboxes.memory_mb_allocated ?? fallbackAllocatedRamMB,
              )}{' '}
              ({formatMemory(alloc?.running_memory_mb ?? fallbackRunningRamMB)}{' '}
              running) · CI Jobs: {formatMemory(alloc?.job_memory_mb ?? 0)}
            </div>
          </div>

          {/* Storage */}
          <div className="p-4 space-y-2">
            <div className="flex items-center justify-between text-[11.5px] text-muted-foreground">
              <span>Allocated Disk &amp; Volumes</span>
              <span className="font-mono text-foreground">
                {totalPlatformDiskGb.toFixed(1)} /{' '}
                {maxDiskGb > 0 ? `${maxDiskGb} GB` : '∞'}
              </span>
            </div>
            {maxDiskGb > 0 && (
              <UsageBar percent={(totalPlatformDiskGb / maxDiskGb) * 100} />
            )}
            <div className="font-mono text-[11px] text-muted-foreground">
              Sandbox disks: {allocVmDiskGb.toFixed(1)} GB · Persistent volumes:{' '}
              {allocVolDiskGb.toFixed(1)} GB ({alloc?.volumes.total ?? volumes.length})
            </div>
          </div>
        </div>

        {/* Sandboxes / CI Jobs / Workers Summary Strip */}
        <div className="grid grid-cols-1 divide-y divide-border border-t border-border sm:grid-cols-3 sm:divide-y-0 sm:divide-x font-mono text-[11.5px]">
          <div className="p-3.5 flex items-center justify-between">
            <span className="text-muted-foreground">Sandboxes</span>
            <span className="text-foreground">
              {alloc?.sandboxes.running ?? fallbackRunningVMs} running ·{' '}
              {alloc?.sandboxes.stopped ?? 0} stopped ·{' '}
              {alloc?.sandboxes.total ?? fallbackTotalVMs} total
            </span>
          </div>
          <div className="p-3.5 flex items-center justify-between">
            <span className="text-muted-foreground">CI Jobs</span>
            <span className="text-foreground">
              {alloc?.ci_jobs.running ?? 0} running · {alloc?.ci_jobs.queued ?? 0}{' '}
              queued · {alloc?.ci_jobs.succeeded ?? 0} ok ·{' '}
              {alloc?.ci_jobs.failed ?? 0} failed
            </span>
          </div>
          <div className="p-3.5 flex items-center justify-between">
            <span className="text-muted-foreground">Workers &amp; Volumes</span>
            <span className="text-foreground">
              {alloc?.workers.ready ??
                workers.filter((w) => w.status === 'ready').length}
              /{alloc?.workers.total ?? (workers.length || 1)} workers ready ·{' '}
              {alloc?.volumes.attached ?? 0} vols attached
            </span>
          </div>
        </div>
      </div>

      {/* Disk Mounts & Block Devices I/O */}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        {/* Filesystem Mounts */}
        <div className="border border-border bg-background">
          <div className="border-b border-border bg-muted/20 px-4 py-2.5 text-[12.5px] font-medium text-foreground">
            Storage Mounts ({mounts.length})
          </div>
          {mounts.length === 0 ? (
            <div className="p-4 font-mono text-[11.5px] text-muted-foreground">
              {disk
                ? `${disk.path}: ${disk.used_gb.toFixed(1)} GB / ${disk.total_gb.toFixed(1)} GB (${disk.used_percent.toFixed(1)}%)`
                : 'No mount breakdown reported.'}
            </div>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-left font-mono text-[11px]">
                <thead>
                  <tr className="border-b border-border bg-muted/30 text-muted-foreground">
                    <th className="py-2 px-3 font-normal">Mount / Label</th>
                    <th className="py-2 px-3 font-normal">Used</th>
                    <th className="py-2 px-3 font-normal">Available</th>
                    <th className="py-2 px-3 font-normal">Total</th>
                    <th className="py-2 px-3 text-right font-normal">Usage</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-border">
                  {mounts.map((m) => (
                    <tr key={`${m.label}-${m.path}`}>
                      <td className="py-2 px-3">
                        <div className="text-foreground font-medium">{m.label}</div>
                        <div className="text-[10px] text-muted-foreground truncate max-w-[200px]">
                          {m.path}
                        </div>
                      </td>
                      <td className="py-2 px-3 text-foreground">
                        {m.used_gb.toFixed(1)} GB
                      </td>
                      <td className="py-2 px-3 text-muted-foreground">
                        {m.available_gb.toFixed(1)} GB
                      </td>
                      <td className="py-2 px-3 text-muted-foreground">
                        {m.total_gb.toFixed(1)} GB
                      </td>
                      <td className="py-2 px-3 text-right text-foreground">
                        {m.used_percent.toFixed(1)}%
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>

        {/* Block Device I/O */}
        <div className="border border-border bg-background">
          <div className="border-b border-border bg-muted/20 px-4 py-2.5 text-[12.5px] font-medium text-foreground">
            Block Device I/O ({devices.length})
          </div>
          {devices.length === 0 ? (
            <div className="p-4 font-mono text-[11.5px] text-muted-foreground">
              {diskIO
                ? `Total read ${diskIO.read_mb.toFixed(1)} MB (${diskIO.read_ops} ops) · write ${diskIO.write_mb.toFixed(1)} MB (${diskIO.write_ops} ops)`
                : 'No block device statistics reported.'}
            </div>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-left font-mono text-[11px]">
                <thead>
                  <tr className="border-b border-border bg-muted/30 text-muted-foreground">
                    <th className="py-2 px-3 font-normal">Device</th>
                    <th className="py-2 px-3 font-normal">Read</th>
                    <th className="py-2 px-3 font-normal">Write</th>
                    <th className="py-2 px-3 text-right font-normal">In-Flight</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-border">
                  {devices.map((dev) => (
                    <tr key={dev.device}>
                      <td className="py-2 px-3 text-foreground font-medium">
                        {dev.device}
                      </td>
                      <td className="py-2 px-3 text-muted-foreground">
                        {dev.read_mb.toFixed(1)} MB ({dev.read_ops} ops)
                      </td>
                      <td className="py-2 px-3 text-muted-foreground">
                        {dev.write_mb.toFixed(1)} MB ({dev.write_ops} ops)
                      </td>
                      <td className="py-2 px-3 text-right text-foreground">
                        {dev.io_in_progress}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      </div>

      {/* Host Network & System Information */}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        {/* Network */}
        <div className="border border-border bg-background p-4 space-y-3">
          <div className="flex items-center justify-between">
            <span className="text-[12.5px] font-medium text-foreground">
              Host Network Counters
            </span>
            <Network className="h-3.5 w-3.5 text-muted-foreground" />
          </div>
          <div className="grid grid-cols-2 gap-2 font-mono text-[11.5px]">
            <span className="text-muted-foreground">Inbound (RX)</span>
            <span className="text-right text-foreground">
              {net
                ? `${net.rx_mb_per_sec.toFixed(2)} MB/s (${net.rx_mb.toFixed(1)} MB total · ${net.rx_packets.toLocaleString()} pkts)`
                : '—'}
            </span>
            <span className="text-muted-foreground">Outbound (TX)</span>
            <span className="text-right text-foreground">
              {net
                ? `${net.tx_mb_per_sec.toFixed(2)} MB/s (${net.tx_mb.toFixed(1)} MB total · ${net.tx_packets.toLocaleString()} pkts)`
                : '—'}
            </span>
            <span className="text-muted-foreground">Interfaces</span>
            <span className="text-right text-foreground truncate">
              {netIfaces.length > 0 ? netIfaces.join(', ') : '—'}
            </span>
          </div>
        </div>

        {/* System Information */}
        <div className="border border-border bg-background p-4 space-y-3">
          <div className="flex items-center justify-between">
            <span className="text-[12.5px] font-medium text-foreground">
              Host System &amp; Hypervisor
            </span>
            <Server className="h-3.5 w-3.5 text-muted-foreground" />
          </div>
          <div className="grid grid-cols-2 gap-2 font-mono text-[11.5px]">
            <span className="text-muted-foreground">Hostname</span>
            <span className="text-right text-foreground">
              {sys?.hostname || '—'}
            </span>
            <span className="text-muted-foreground">OS / Architecture</span>
            <span className="text-right text-foreground">
              {sys ? `${sys.os} / ${sys.arch}` : '—'}
              {sys?.kernel_version ? ` (${sys.kernel_version})` : ''}
            </span>
            <span className="text-muted-foreground">CPU Model</span>
            <span className="text-right text-foreground truncate">
              {cpu?.model || '—'}
            </span>
            <span className="text-muted-foreground">Uptime &amp; KVM</span>
            <span className="text-right text-foreground">
              {sys ? `${formatUptime(sys.uptime_seconds)} · ` : ''}
              {sys ? (
                <span
                  className={
                    sys.kvm_available ? 'text-success' : 'text-warning'
                  }
                >
                  {sys.kvm_available ? 'KVM Ready' : 'KVM Unavailable'}
                </span>
              ) : (
                '—'
              )}
            </span>
          </div>
        </div>
      </div>
    </div>
  );
}
