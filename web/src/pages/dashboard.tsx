import {
  Activity,
  ArrowRight,
  ExternalLink,
  Cpu,
  HardDrive,
  Network as NetworkIcon,
  Plus,
  Server,
  Square,
  Terminal,
} from 'lucide-react';

import { EmptyState } from '@/components/empty-state';
import { StatCard } from '@/components/stat-card';
import { openSSHTerminalWindow } from '@/lib/terminal';
import { Button } from '@/components/ui/button';
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from '@/components/ui/card';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip';
import { clampPercent, formatMemory, relativeTime } from '@/lib/format';
import type {
  NetworkConfig,
  QueueResult,
  StatsResult,
  VM,
} from '@/lib/types';

function CapacityRow({
  label,
  value,
  max,
  used,
}: {
  label: string;
  value: string;
  max: string;
  used: number;
}) {
  return (
    <div className="space-y-1.5">
      <div className="flex items-baseline justify-between gap-3 text-xs">
        <span className="text-muted-foreground">{label}</span>
        <span className="tabular font-mono text-[11px] text-muted-foreground">
          <span className="text-foreground">{value}</span> / {max}
        </span>
      </div>
      <div className="h-1.5 overflow-hidden rounded-full bg-secondary">
        <div
          className="h-full rounded-full bg-primary transition-[width] duration-500"
          style={{ width: `${used}%` }}
        />
      </div>
    </div>
  );
}

export function DashboardPage({
  stats,
  vms,
  queue,
  networkConfig,
  isLoading,
  onNew,
  onViewInstances,
  onViewNetwork,
  onOpenConsole,
  onStop,
}: {
  stats: StatsResult | null;
  vms: VM[];
  queue: QueueResult | null;
  networkConfig: NetworkConfig | null;
  isLoading: boolean;
  onNew: () => void;
  onViewInstances: () => void;
  onViewNetwork: () => void;
  onOpenConsole: (vm: VM) => void;
  onStop: (vm: VM) => void;
}) {
  const runningVMs = vms.filter((vm) => vm.status === 'running');
  const limits = stats?.resource_limits;

  const total = stats?.total_vms ?? vms.length;
  const running = stats?.running_vms ?? runningVMs.length;
  const queued = stats?.queued_vms ?? queue?.count ?? 0;
  const stopped = stats?.stopped_vms ?? 0;
  const snapshotted = stats?.snapshotted_vms ?? 0;
  const runningVcpus = stats?.running_vcpus ?? 0;
  const runningMemory = stats?.running_memory_mb ?? 0;
  const allocatedIPs = stats?.allocated_ips ?? 0;

  const maxVMs = limits?.max_running_vms ?? 0;
  const maxVcpus = limits?.max_total_vcpus ?? 0;
  const maxMemory = limits?.max_total_memory_mb ?? 0;

  const vmsPercent = clampPercent(running, maxVMs);
  const vcpuPercent = clampPercent(runningVcpus, maxVcpus);
  const memoryPercent = clampPercent(runningMemory, maxMemory);

  return (
    <div className="space-y-6 animate-fade-in">
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatCard
          label="Instances"
          value={isLoading && !stats ? '—' : total}
          icon={Server}
          detail={
            <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
              <span>{running} running</span>
              <span className="text-border">·</span>
              <span>{stopped} stopped</span>
              {queued > 0 ? (
                <>
                  <span className="text-border">·</span>
                  <span className="text-warning">{queued} queued</span>
                </>
              ) : null}
            </span>
          }
          progress={maxVMs > 0 ? vmsPercent : undefined}
          footer={
            maxVMs > 0 ? `${vmsPercent}% of ${maxVMs} running slots` : 'No running limit configured'
          }
        />

        <StatCard
          label="vCPU allocated"
          value={isLoading && !stats ? '—' : runningVcpus}
          suffix={maxVcpus > 0 ? `/ ${maxVcpus}` : 'vCPU'}
          icon={Cpu}
          detail="Across running instances"
          progress={maxVcpus > 0 ? vcpuPercent : undefined}
          footer={maxVcpus > 0 ? `${vcpuPercent}% of host vCPU budget` : 'Uncapped'}
        />

        <StatCard
          label="Memory allocated"
          value={isLoading && !stats ? '—' : formatMemory(runningMemory)}
          suffix={maxMemory > 0 ? `/ ${formatMemory(maxMemory)}` : undefined}
          icon={HardDrive}
          detail="Guest RAM currently reserved"
          progress={maxMemory > 0 ? memoryPercent : undefined}
          footer={maxMemory > 0 ? `${memoryPercent}% of host memory budget` : 'Uncapped'}
        />

        <StatCard
          label="IP leases"
          value={isLoading && !stats ? '—' : allocatedIPs}
          icon={NetworkIcon}
          detail={networkConfig?.subnet || '—'}
          footer={
            snapshotted > 0
              ? `${snapshotted} instance${snapshotted === 1 ? '' : 's'} snapshotted`
              : 'No memory snapshots'
          }
        />
      </div>

      <div className="grid items-start gap-6 lg:grid-cols-3">
        <Card className="lg:col-span-2">
          <CardHeader className="flex-row items-center justify-between space-y-0">
            <div className="flex items-center gap-2">
              <CardTitle>Running instances</CardTitle>
              <span className="tabular rounded-md border bg-secondary px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground">
                {runningVMs.length}
              </span>
            </div>
            <Button variant="ghost" size="sm" onClick={onViewInstances}>
              View all
              <ArrowRight />
            </Button>
          </CardHeader>
          <CardContent className="p-0">
            {runningVMs.length === 0 ? (
              <EmptyState
                icon={Activity}
                title="No instances are running"
                description="Start an existing instance or provision a new microVM to see live capacity here."
                action={
                  <div className="flex items-center gap-2">
                    <Button size="sm" onClick={onNew}>
                      <Plus />
                      New instance
                    </Button>
                    <Button size="sm" variant="outline" onClick={onViewInstances}>
                      Browse instances
                    </Button>
                  </div>
                }
              />
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Instance</TableHead>
                    <TableHead>Resources</TableHead>
                    <TableHead>Address</TableHead>
                    <TableHead>Created</TableHead>
                    <TableHead className="w-24 text-right">Actions</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {runningVMs.slice(0, 6).map((vm) => (
                    <TableRow key={vm.id}>
                      <TableCell>
                        <div className="flex items-center gap-2.5">
                          <span className="flex h-7 w-7 items-center justify-center rounded-md border bg-secondary text-muted-foreground">
                            <Server className="h-3.5 w-3.5" />
                          </span>
                          <div className="min-w-0">
                            <div className="truncate text-[13px] font-medium">{vm.name}</div>
                            <div className="truncate font-mono text-[11px] text-muted-foreground">
                              {vm.id}
                            </div>
                          </div>
                        </div>
                      </TableCell>
                      <TableCell className="tabular whitespace-nowrap font-mono text-xs text-muted-foreground">
                        {vm.vcpus} vCPU · {formatMemory(vm.memory_mb)}
                      </TableCell>
                      <TableCell className="font-mono text-xs">
                        {vm.ip_address || <span className="text-muted-foreground">—</span>}
                      </TableCell>
                      <TableCell className="whitespace-nowrap text-xs text-muted-foreground">
                        {relativeTime(vm.created_at)}
                      </TableCell>
                      <TableCell>
                        <div className="flex items-center justify-end gap-1">
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <Button
                                variant="ghost"
                                size="icon-sm"
                                onClick={() => openSSHTerminalWindow(vm.id, vm.name)}
                                disabled={vm.status !== 'running'}
                                aria-label={`Open SSH terminal for ${vm.name}`}
                                className="text-primary hover:text-primary"
                              >
                                <ExternalLink />
                              </Button>
                            </TooltipTrigger>
                            <TooltipContent>Open SSH Terminal (New Window)</TooltipContent>
                          </Tooltip>
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <Button
                                variant="ghost"
                                size="icon-sm"
                                onClick={() => onOpenConsole(vm)}
                                aria-label={`Open console for ${vm.name}`}
                              >
                                <Terminal />
                              </Button>
                            </TooltipTrigger>
                            <TooltipContent>Open console</TooltipContent>
                          </Tooltip>
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <Button
                                variant="ghost"
                                size="icon-sm"
                                onClick={() => onStop(vm)}
                                className="text-destructive hover:bg-destructive/10 hover:text-destructive"
                                aria-label={`Stop ${vm.name}`}
                              >
                                <Square />
                              </Button>
                            </TooltipTrigger>
                            <TooltipContent>Stop instance</TooltipContent>
                          </Tooltip>
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </CardContent>
        </Card>

        <div className="space-y-6">
          <Card>
            <CardHeader className="pb-4">
              <CardTitle>Capacity</CardTitle>
            </CardHeader>
            <CardContent className="space-y-4">
              <CapacityRow
                label="Running slots"
                value={String(running)}
                max={maxVMs > 0 ? String(maxVMs) : '∞'}
                used={maxVMs > 0 ? vmsPercent : 0}
              />
              <CapacityRow
                label="vCPU"
                value={String(runningVcpus)}
                max={maxVcpus > 0 ? String(maxVcpus) : '∞'}
                used={maxVcpus > 0 ? vcpuPercent : 0}
              />
              <CapacityRow
                label="Memory"
                value={formatMemory(runningMemory)}
                max={maxMemory > 0 ? formatMemory(maxMemory) : '∞'}
                used={maxMemory > 0 ? memoryPercent : 0}
              />
              {maxVMs === 0 && maxVcpus === 0 && maxMemory === 0 ? (
                <p className="pt-1 text-xs text-muted-foreground">
                  No resource limits are configured on this daemon.
                </p>
              ) : null}
            </CardContent>
          </Card>

          <Card>
            <CardHeader className="flex-row items-center justify-between space-y-0 pb-4">
              <CardTitle>Queue</CardTitle>
              <span className="tabular rounded-md border bg-secondary px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground">
                {queued}
              </span>
            </CardHeader>
            <CardContent>
              {queue && queue.queued_vms.length > 0 ? (
                <div className="space-y-3">
                  {queue.queued_vms.slice(0, 4).map((item) => (
                    <div key={item.vm_id} className="flex items-start gap-2.5">
                      <span className="tabular mt-0.5 flex h-5 w-5 shrink-0 items-center justify-center rounded border bg-secondary font-mono text-[10px] text-muted-foreground">
                        {item.position}
                      </span>
                      <div className="min-w-0">
                        <div className="truncate text-xs font-medium">{item.vm_name}</div>
                        <div className="text-[11px] text-muted-foreground">{item.reason}</div>
                      </div>
                    </div>
                  ))}
                </div>
              ) : (
                <p className="text-xs text-muted-foreground">
                  No instances waiting. New workloads start immediately while capacity allows.
                </p>
              )}
            </CardContent>
          </Card>

          <Card>
            <CardHeader className="flex-row items-center justify-between space-y-0 pb-4">
              <CardTitle>Node network</CardTitle>
              <Button variant="ghost" size="sm" onClick={onViewNetwork}>
                Details
              </Button>
            </CardHeader>
            <CardContent className="space-y-3 text-xs">
              <div className="flex items-center justify-between gap-3">
                <span className="text-muted-foreground">Mode</span>
                <span className="font-mono uppercase">{networkConfig?.mode || '—'}</span>
              </div>
              <div className="flex items-center justify-between gap-3">
                <span className="text-muted-foreground">Bridge</span>
                <span className="font-mono">{networkConfig?.bridge_name || '—'}</span>
              </div>
              <div className="flex items-center justify-between gap-3">
                <span className="text-muted-foreground">Gateway</span>
                <span className="font-mono">{networkConfig?.bridge_ip || '—'}</span>
              </div>
              <div className="flex items-center justify-between gap-3">
                <span className="text-muted-foreground">Subnet</span>
                <span className="font-mono">{networkConfig?.subnet || '—'}</span>
              </div>
            </CardContent>
          </Card>
        </div>
      </div>
    </div>
  );
}
