import {
  Activity,
  ArrowUpRight,
  ChevronDown,
  Cpu,
  Layers,
  Play,
  Server,
  Zap,
} from 'lucide-react';

import { formatMemory, formatRelativeTime, formatTimestamp } from '@/lib/format';
import type { PageId } from '@/lib/navigation';
import type {
  AuditEventRecord,
  NetworkConfig,
  QueueResult,
  StatsResult,
  VM,
  WorkerNodeRecord,
} from '@/lib/types';

interface DashboardPageProps {
  stats: StatsResult | null;
  vms: VM[];
  workers?: WorkerNodeRecord[];
  events?: AuditEventRecord[];
  queue: QueueResult | null;
  networkConfig: NetworkConfig | null;
  isLoading: boolean;
  onNew: () => void;
  onViewInstances: () => void;
  onViewNetwork: () => void;
  onNavigate?: (page: PageId) => void;
  onOpenConsole: (vm: VM) => void;
  onStop: (vm: VM) => void;
}

export function DashboardPage({
  stats,
  vms,
  workers = [],
  events = [],
  queue,
  networkConfig,
  onNew,
  onViewInstances,
  onViewNetwork,
  onNavigate,
}: DashboardPageProps) {
  const runningVMs =
    stats?.running_vms ?? vms.filter((v) => v.status === 'running').length;
  const totalVMs = stats?.total_vms ?? vms.length;
  const snapshottedVMs =
    stats?.snapshotted_vms ?? vms.filter((v) => v.status === 'paused').length;
  const queuedCount = queue?.count ?? stats?.queued_vms ?? 0;

  const usedVcpus = stats?.running_vcpus ?? 0;
  const maxVcpus =
    stats?.resource_limits?.max_total_vcpus ||
    workers.reduce((acc, w) => acc + (w.total_vcpus || 0), 0) ||
    16;

  const usedRamMb = stats?.running_memory_mb ?? 0;
  const maxRamMb =
    stats?.resource_limits?.max_total_memory_mb ||
    workers.reduce((acc, w) => acc + (w.total_mem_mb || 0), 0) ||
    32768;

  return (
    <div className="space-y-6">
      {/* Header matching boat.dev/dashboard */}
      <div className="flex flex-wrap items-center justify-between gap-4">
        <div className="space-y-1">
          <h1 className="font-display text-[22px] font-medium tracking-tight text-foreground">
            Overview
          </h1>
          <p className="text-[12px] text-muted-foreground">
            Distributed Firecracker control plane, KVM worker fleet capacity,
            and live sandbox telemetry.
          </p>
        </div>

        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={onViewInstances}
            className="h-8 border border-border bg-background px-3 text-[12px] text-foreground hover:bg-muted transition-colors"
          >
            View All Sandboxes →
          </button>
          <div className="inline-flex items-stretch">
            <button
              type="button"
              onClick={onNew}
              className="flex h-8 items-center gap-1.5 bg-[#263fa9] hover:bg-[#1e3180] px-3 text-[12px] font-medium text-white transition-colors"
            >
              <Play className="h-3 w-3 fill-current" />
              <span>New</span>
            </button>
            <button
              type="button"
              onClick={onNew}
              aria-label="Advanced create"
              className="flex h-8 w-7 items-center justify-center border-l border-white/20 bg-[#263fa9] hover:bg-[#1e3180] text-white"
            >
              <ChevronDown className="h-3.5 w-3.5" />
            </button>
          </div>
        </div>
      </div>

      {/* Top 4-Column 1px-Bordered Metric Strip */}
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 border border-border bg-background divide-y sm:divide-y-0 sm:divide-x divide-border">
        <div className="p-4 space-y-2">
          <div className="flex items-center justify-between text-xs text-muted-foreground">
            <span>Active Sandboxes</span>
            <Server className="h-3.5 w-3.5 text-[#263fa9]" />
          </div>
          <div className="flex items-baseline justify-between">
            <span className="font-display text-2xl font-medium text-foreground tabular">
              {runningVMs}{' '}
              <span className="text-sm font-normal text-muted-foreground">
                / {totalVMs} total
              </span>
            </span>
            <span className="font-mono text-[11px] text-success">
              {snapshottedVMs} warm snaps
            </span>
          </div>
          <div className="h-1.5 w-full bg-muted overflow-hidden">
            <div
              className="h-full bg-[#263fa9]"
              style={{
                width: `${totalVMs > 0 ? Math.min(100, (runningVMs / totalVMs) * 100) : 0}%`,
              }}
            />
          </div>
        </div>

        <div className="p-4 space-y-2">
          <div className="flex items-center justify-between text-xs text-muted-foreground">
            <span>Cluster vCPU Allocation</span>
            <Cpu className="h-3.5 w-3.5 text-[#263fa9]" />
          </div>
          <div className="flex items-baseline justify-between">
            <span className="font-display text-2xl font-medium text-foreground tabular">
              {usedVcpus}{' '}
              <span className="text-sm font-normal text-muted-foreground">
                / {maxVcpus} vCPU
              </span>
            </span>
            <span className="font-mono text-[11px] text-muted-foreground">
              {Math.round((usedVcpus / Math.max(1, maxVcpus)) * 100)}% committed
            </span>
          </div>
          <div className="h-1.5 w-full bg-muted overflow-hidden">
            <div
              className="h-full bg-[#263fa9]"
              style={{
                width: `${Math.min(100, (usedVcpus / Math.max(1, maxVcpus)) * 100)}%`,
              }}
            />
          </div>
        </div>

        <div className="p-4 space-y-2">
          <div className="flex items-center justify-between text-xs text-muted-foreground">
            <span>Cluster Memory (KVM RSS)</span>
            <Layers className="h-3.5 w-3.5 text-[#263fa9]" />
          </div>
          <div className="flex items-baseline justify-between">
            <span className="font-display text-2xl font-medium text-foreground tabular">
              {formatMemory(usedRamMb)}{' '}
              <span className="text-sm font-normal text-muted-foreground">
                / {formatMemory(maxRamMb)}
              </span>
            </span>
            <span className="font-mono text-[11px] text-muted-foreground">
              {stats?.allocated_ips ?? vms.length} IPs leased
            </span>
          </div>
          <div className="h-1.5 w-full bg-muted overflow-hidden">
            <div
              className="h-full bg-[#263fa9]"
              style={{
                width: `${Math.min(100, (usedRamMb / Math.max(1, maxRamMb)) * 100)}%`,
              }}
            />
          </div>
        </div>

        <div className="p-4 space-y-2">
          <div className="flex items-center justify-between text-xs text-muted-foreground">
            <span>Admission Queue &amp; Bridge</span>
            <Zap className="h-3.5 w-3.5 text-[#263fa9]" />
          </div>
          <div className="flex items-baseline justify-between">
            <span className="font-display text-2xl font-medium text-foreground tabular">
              {queuedCount}{' '}
              <span className="text-sm font-normal text-muted-foreground">
                queued
              </span>
            </span>
            <span className="font-mono text-[11px] text-success">
              {networkConfig?.mode || 'nat'} mode
            </span>
          </div>
          <div className="font-mono text-[11px] text-muted-foreground truncate">
            Bridge {networkConfig?.bridge_name || 'fcbr0'} ·{' '}
            {networkConfig?.subnet || networkConfig?.subnet_cidr || '172.16.0.0/24'}
          </div>
        </div>
      </div>

      {/* Middle 2-Column Section: Live Worker Fleet + Platform Shortcuts */}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
        <div className="lg:col-span-2 border border-border bg-background">
          <div className="flex items-center justify-between border-b border-border px-4 py-3">
            <div>
              <h2 className="text-[13px] font-medium text-foreground">
                Registered KVM Worker Nodes
              </h2>
              <p className="text-[11.5px] text-muted-foreground">
                Live host hypervisors connected to the compute scheduler
              </p>
            </div>
            <button
              type="button"
              onClick={() => onNavigate?.('workers')}
              className="font-mono text-xs text-[color:var(--link)] hover:underline"
            >
              Manage Workers ({workers.length}) →
            </button>
          </div>

          {workers.length === 0 ? (
            <div className="p-8 text-center text-[12px] text-muted-foreground">
              No worker nodes enrolled yet.
            </div>
          ) : (
            <div className="divide-y divide-border text-xs">
              {workers.map((node) => (
                <div
                  key={node.id}
                  className="flex flex-wrap items-center justify-between gap-4 px-4 py-3 hover:bg-muted/30"
                >
                  <div className="space-y-0.5 min-w-[200px]">
                    <div className="flex items-center gap-2">
                      <span className="h-1.5 w-1.5 bg-success" />
                      <span className="font-mono font-medium text-foreground">
                        {node.name}
                      </span>
                      <span className="border border-border bg-muted/40 px-1.5 py-0.2 font-mono text-[10px] text-muted-foreground">
                        pool:{node.pool_id}
                      </span>
                    </div>
                    <div className="font-mono text-[11px] text-muted-foreground">
                      {node.underlay_ip || '127.0.0.1'} · {node.arch || 'amd64'}{' '}
                      · Heartbeat{' '}
                      {node.last_heartbeat
                        ? formatRelativeTime(node.last_heartbeat)
                        : 'live'}
                    </div>
                  </div>

                  <div className="flex items-center gap-6 font-mono text-[11.5px]">
                    <div>
                      <div className="text-muted-foreground text-[10px]">
                        vCPU
                      </div>
                      <div>{node.total_vcpus} vCPU</div>
                    </div>
                    <div>
                      <div className="text-muted-foreground text-[10px]">
                        RAM
                      </div>
                      <div>{formatMemory(node.total_mem_mb)}</div>
                    </div>
                    <div>
                      <div className="text-muted-foreground text-[10px]">
                        Status
                      </div>
                      <div className="uppercase text-success">{node.status}</div>
                    </div>
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>

        {/* Right 1 col: Quick Platform Workflows */}
        <div className="border border-border bg-background flex flex-col justify-between">
          <div>
            <div className="border-b border-border px-4 py-3">
              <h2 className="text-[13px] font-medium text-foreground">
                Quick Platform Workflows
              </h2>
              <p className="text-[11.5px] text-muted-foreground">
                Direct navigation across the compute control plane
              </p>
            </div>

            <div className="divide-y divide-border text-xs">
              <button
                type="button"
                onClick={onViewInstances}
                className="flex w-full items-center justify-between px-4 py-3 text-left hover:bg-selected transition-colors"
              >
                <div>
                  <div className="font-medium text-foreground">
                    Sandboxes ({totalVMs})
                  </div>
                  <div className="text-[11px] text-muted-foreground">
                    Provision, SSH, exec, and snapshot Firecracker VMs
                  </div>
                </div>
                <ArrowUpRight className="h-4 w-4 text-muted-foreground" />
              </button>

              <button
                type="button"
                onClick={() => onNavigate?.('environment')}
                className="flex w-full items-center justify-between px-4 py-3 text-left hover:bg-selected transition-colors"
              >
                <div>
                  <div className="font-medium text-foreground">
                    Environment &amp; Secrets
                  </div>
                  <div className="text-[11px] text-muted-foreground">
                    GitHub repos, secrets, and agent credential profiles
                  </div>
                </div>
                <ArrowUpRight className="h-4 w-4 text-muted-foreground" />
              </button>

              <button
                type="button"
                onClick={() => onNavigate?.('jobs')}
                className="flex w-full items-center justify-between px-4 py-3 text-left hover:bg-selected transition-colors"
              >
                <div>
                  <div className="font-medium text-foreground">
                    CI on Compute VMs
                  </div>
                  <div className="text-[11px] text-muted-foreground">
                    Ephemeral GitHub Actions microVM runners
                  </div>
                </div>
                <ArrowUpRight className="h-4 w-4 text-muted-foreground" />
              </button>

              <button
                type="button"
                onClick={onViewNetwork}
                className="flex w-full items-center justify-between px-4 py-3 text-left hover:bg-selected transition-colors"
              >
                <div>
                  <div className="font-medium text-foreground">
                    Overlay Network &amp; Port Forwards
                  </div>
                  <div className="text-[11px] text-muted-foreground">
                    TAP leases, DNAT rules, and public IP routing
                  </div>
                </div>
                <ArrowUpRight className="h-4 w-4 text-muted-foreground" />
              </button>
            </div>
          </div>
        </div>
      </div>

      {/* Bottom Audit & Control Plane Lifecycle Stream */}
      <div className="border border-border bg-background">
        <div className="flex items-center justify-between border-b border-border px-4 py-3">
          <div className="flex items-center gap-2">
            <Activity className="h-3.5 w-3.5 text-[#263fa9]" />
            <h2 className="text-[13px] font-medium text-foreground">
              Recent Control-Plane Audit Events ({events.length})
            </h2>
          </div>
          <button
            type="button"
            onClick={() => onNavigate?.('events')}
            className="font-mono text-xs text-[color:var(--link)] hover:underline"
          >
            Open Audit Stream &amp; Webhooks →
          </button>
        </div>

        {events.length === 0 ? (
          <div className="p-6 text-center text-[12px] text-muted-foreground">
            No audit events recorded yet.
          </div>
        ) : (
          <div className="divide-y divide-border font-mono text-xs">
            {events.slice(0, 6).map((ev) => (
              <div
                key={ev.id}
                className="flex flex-wrap items-center justify-between gap-2 px-4 py-2.5 hover:bg-muted/20"
              >
                <div className="flex items-center gap-3">
                  <span className="text-muted-foreground">
                    {formatTimestamp(ev.timestamp)}
                  </span>
                  <span className="border border-border bg-muted/40 px-1.5 py-0.5 text-[11px] text-foreground">
                    {ev.event}
                  </span>
                  <span className="text-foreground">{ev.target}</span>
                </div>
                <span className="text-muted-foreground text-[11px]">
                  actor: {ev.actor}
                </span>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
