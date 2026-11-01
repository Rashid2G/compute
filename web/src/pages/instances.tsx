import { useMemo, useState } from 'react';
import {
  Camera,
  ExternalLink,
  LayoutGrid,
  List,
  MoreHorizontal,
  Play,
  Plus,
  RotateCcw,
  Search,
  Server,
  Square,
  Terminal,
  Trash2,
} from 'lucide-react';

import { CopyButton } from '@/components/copy-button';
import { EmptyState } from '@/components/empty-state';
import { StatusBadge } from '@/components/status-badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent } from '@/components/ui/card';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { Input } from '@/components/ui/input';
import { Skeleton } from '@/components/ui/skeleton';
import { openSSHTerminalWindow } from '@/lib/terminal';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs';
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip';
import { formatDateTime, formatMemory, relativeTime } from '@/lib/format';
import type { VM, VMStatus } from '@/lib/types';

type Filter = 'all' | VMStatus;

const FILTERS: Array<{ value: Filter; label: string }> = [
  { value: 'all', label: 'All' },
  { value: 'running', label: 'Running' },
  { value: 'queued', label: 'Queued' },
  { value: 'paused', label: 'Snapshots' },
  { value: 'stopped', label: 'Stopped' },
  { value: 'failed', label: 'Failed' },
];

function VMInstanceCell({ vm }: { vm: VM }) {
  return (
    <div className="flex items-center gap-2.5">
      <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border bg-secondary text-muted-foreground">
        <Server className="h-4 w-4" />
      </span>
      <div className="min-w-0">
        <div className="truncate text-[13px] font-medium">{vm.name}</div>
        <div className="flex items-center gap-0.5">
          <span className="truncate font-mono text-[11px] text-muted-foreground">
            {vm.id}
          </span>
          <CopyButton value={vm.id} label="Copy instance ID" className="h-6 w-6" />
        </div>
      </div>
    </div>
  );
}

function PrimaryAction({
  vm,
  onRun,
  onStop,
  onResume,
}: {
  vm: VM;
  onRun: (vm: VM) => void;
  onStop: (vm: VM) => void;
  onResume: (vm: VM) => void;
}) {
  if (vm.status === 'running') {
    return (
      <Tooltip>
        <TooltipTrigger asChild>
          <Button
            variant="outline"
            size="icon-sm"
            className="text-destructive hover:bg-destructive/10 hover:text-destructive"
            onClick={() => onStop(vm)}
            aria-label={`Stop ${vm.name}`}
          >
            <Square />
          </Button>
        </TooltipTrigger>
        <TooltipContent>Stop</TooltipContent>
      </Tooltip>
    );
  }

  if (vm.status === 'paused') {
    return (
      <Tooltip>
        <TooltipTrigger asChild>
          <Button
            variant="outline"
            size="icon-sm"
            className="text-info hover:bg-info/10 hover:text-info"
            onClick={() => onResume(vm)}
            aria-label={`Resume ${vm.name}`}
          >
            <RotateCcw />
          </Button>
        </TooltipTrigger>
        <TooltipContent>Resume from snapshot</TooltipContent>
      </Tooltip>
    );
  }

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button
          variant="outline"
          size="icon-sm"
          className="text-success hover:bg-success/10 hover:text-success"
          onClick={() => onRun(vm)}
          aria-label={`Start ${vm.name}`}
        >
          <Play />
        </Button>
      </TooltipTrigger>
      <TooltipContent>Start</TooltipContent>
    </Tooltip>
  );
}

function RowMenu({
  vm,
  onSnapshot,
  onConsole,
  onInspect,
  onDelete,
}: {
  vm: VM;
  onSnapshot: (vm: VM) => void;
  onConsole: (vm: VM) => void;
  onInspect: (vm: VM) => void;
  onDelete: (vm: VM) => void;
}) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon-sm" aria-label={`Actions for ${vm.name}`}>
          <MoreHorizontal />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuItem onSelect={() => openSSHTerminalWindow(vm.id, vm.name)} disabled={vm.status !== 'running'}>
          <ExternalLink />
          Open SSH Terminal (New Window)
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={() => onConsole(vm)}>
          <Terminal />
          Console
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={() => onInspect(vm)}>
          <Search />
          Inspect
        </DropdownMenuItem>
        {vm.status === 'running' ? (
          <DropdownMenuItem onSelect={() => onSnapshot(vm)}>
            <Camera />
            Save snapshot
          </DropdownMenuItem>
        ) : null}
        <DropdownMenuSeparator />
        <DropdownMenuItem
          onSelect={() => onDelete(vm)}
          className="text-destructive focus:bg-destructive/10 focus:text-destructive"
        >
          <Trash2 />
          Delete
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

export function InstancesPage({
  vms,
  isLoading,
  onNew,
  onRun,
  onStop,
  onSnapshot,
  onResume,
  onDelete,
  onInspect,
  onConsole,
}: {
  vms: VM[];
  isLoading: boolean;
  onNew: () => void;
  onRun: (vm: VM) => void;
  onStop: (vm: VM) => void;
  onSnapshot: (vm: VM) => void;
  onResume: (vm: VM) => void;
  onDelete: (vm: VM) => void;
  onInspect: (vm: VM) => void;
  onConsole: (vm: VM) => void;
}) {
  const [filter, setFilter] = useState<Filter>('all');
  const [query, setQuery] = useState('');
  const [view, setView] = useState<'table' | 'grid'>('table');

  const counts = useMemo(
    () =>
      FILTERS.reduce<Record<Filter, number>>(
        (acc, item) => {
          acc[item.value] =
            item.value === 'all'
              ? vms.length
              : vms.filter((vm) => vm.status === item.value).length;
          return acc;
        },
        {} as Record<Filter, number>,
      ),
    [vms],
  );

  const filtered = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    return vms
      .filter((vm) => (filter === 'all' ? true : vm.status === filter))
      .filter((vm) => {
        if (!normalized) return true;
        return [vm.name, vm.id, vm.ip_address, vm.mac_address, vm.tap_device]
          .filter(Boolean)
          .some((value) => value.toLowerCase().includes(normalized));
      })
      .sort(
        (a, b) =>
          new Date(b.created_at).getTime() - new Date(a.created_at).getTime(),
      );
  }, [vms, filter, query]);

  return (
    <div className="space-y-4 animate-fade-in">
      <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
        <Tabs value={filter} onValueChange={(value) => setFilter(value as Filter)}>
          <TabsList className="h-auto w-full flex-wrap justify-start gap-0.5 bg-transparent p-0 lg:w-auto">
            {FILTERS.map((item) => (
              <TabsTrigger
                key={item.value}
                value={item.value}
                className="rounded-md border border-transparent px-2.5 py-1.5 data-[state=active]:border-border data-[state=active]:bg-card"
              >
                {item.label}
                <span className="tabular rounded bg-secondary px-1 font-mono text-[10px] text-muted-foreground">
                  {counts[item.value]}
                </span>
              </TabsTrigger>
            ))}
          </TabsList>
        </Tabs>

        <div className="flex items-center gap-2">
          <div className="relative w-full sm:w-64">
            <Search className="pointer-events-none absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder="Search name, ID, IP, MAC…"
              className="pl-8"
              aria-label="Search instances"
            />
          </div>
          <div className="flex shrink-0 items-center rounded-md border bg-card p-0.5">
            <Button
              type="button"
              variant={view === 'table' ? 'secondary' : 'ghost'}
              size="icon-sm"
              onClick={() => setView('table')}
              aria-label="Table view"
              aria-pressed={view === 'table'}
            >
              <List />
            </Button>
            <Button
              type="button"
              variant={view === 'grid' ? 'secondary' : 'ghost'}
              size="icon-sm"
              onClick={() => setView('grid')}
              aria-label="Grid view"
              aria-pressed={view === 'grid'}
            >
              <LayoutGrid />
            </Button>
          </div>
        </div>
      </div>

      {isLoading && vms.length === 0 ? (
        <Card>
          <CardContent className="space-y-3 p-5">
            <Skeleton className="h-10 w-full" />
            <Skeleton className="h-10 w-full" />
            <Skeleton className="h-10 w-full" />
          </CardContent>
        </Card>
      ) : filtered.length === 0 ? (
        <Card>
          <CardContent className="p-0">
            <EmptyState
              icon={Server}
              title={vms.length === 0 ? 'No instances yet' : 'No instances match this view'}
              description={
                vms.length === 0
                  ? 'Provision a Firecracker microVM to get started. Instances boot in well under a second.'
                  : 'Adjust the status filter or clear the search to see other instances.'
              }
              action={
                vms.length === 0 ? (
                  <Button size="sm" onClick={onNew}>
                    <Plus />
                    New instance
                  </Button>
                ) : (
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => {
                      setFilter('all');
                      setQuery('');
                    }}
                  >
                    Clear filters
                  </Button>
                )
              }
            />
          </CardContent>
        </Card>
      ) : view === 'table' ? (
        <Card>
          <CardContent className="p-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Instance</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Resources</TableHead>
                  <TableHead>Network</TableHead>
                  <TableHead>Created</TableHead>
                  <TableHead className="w-28 text-right">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {filtered.map((vm) => (
                  <TableRow key={vm.id}>
                    <TableCell>
                      <VMInstanceCell vm={vm} />
                    </TableCell>
                    <TableCell>
                      <StatusBadge status={vm.status} />
                    </TableCell>
                    <TableCell className="tabular whitespace-nowrap font-mono text-xs text-muted-foreground">
                      {vm.vcpus} vCPU · {formatMemory(vm.memory_mb)}
                    </TableCell>
                    <TableCell>
                      <div className="font-mono text-xs flex items-center gap-1.5">
                        <span>{vm.ip_address || <span className="text-muted-foreground">No address</span>}</span>
                        {vm.public_ip_enabled ? (
                          <span className="rounded bg-primary/10 px-1 py-0.5 text-[9px] font-semibold text-primary uppercase">
                            Public
                          </span>
                        ) : (
                          <span className="rounded bg-secondary px-1 py-0.5 text-[9px] font-medium text-muted-foreground uppercase">
                            Private
                          </span>
                        )}
                      </div>
                      <div className="font-mono text-[11px] text-muted-foreground">
                        {vm.public_ip_enabled ? `Public IP: ${vm.public_ip || vm.ip_address}` : (vm.tap_device || 'no tap')}
                      </div>
                    </TableCell>
                    <TableCell
                      className="whitespace-nowrap text-xs text-muted-foreground"
                      title={formatDateTime(vm.created_at)}
                    >
                      {relativeTime(vm.created_at)}
                    </TableCell>
                    <TableCell>
                      <div className="flex items-center justify-end gap-1">
                        <PrimaryAction
                          vm={vm}
                          onRun={onRun}
                          onStop={onStop}
                          onResume={onResume}
                        />
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
                              onClick={() => onConsole(vm)}
                              aria-label={`Open console for ${vm.name}`}
                            >
                              <Terminal />
                            </Button>
                          </TooltipTrigger>
                          <TooltipContent>Console</TooltipContent>
                        </Tooltip>
                        <RowMenu
                          vm={vm}
                          onSnapshot={onSnapshot}
                          onConsole={onConsole}
                          onInspect={onInspect}
                          onDelete={onDelete}
                        />
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      ) : (
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
          {filtered.map((vm) => (
            <Card key={vm.id} className="flex flex-col">
              <div className="flex items-start justify-between gap-3 p-4 pb-3">
                <VMInstanceCell vm={vm} />
                <StatusBadge status={vm.status} />
              </div>

              <div className="grid grid-cols-2 gap-px border-y bg-border">
                <div className="bg-card px-4 py-3">
                  <div className="text-[10px] uppercase tracking-[0.08em] text-muted-foreground">
                    Compute
                  </div>
                  <div className="mt-1 font-mono text-xs">
                    {vm.vcpus} vCPU · {formatMemory(vm.memory_mb)}
                  </div>
                </div>
                <div className="bg-card px-4 py-3">
                  <div className="text-[10px] uppercase tracking-[0.08em] text-muted-foreground flex items-center justify-between">
                    <span>Address</span>
                    {vm.public_ip_enabled ? (
                      <span className="text-[9px] font-semibold text-primary uppercase">Public</span>
                    ) : (
                      <span className="text-[9px] text-muted-foreground uppercase">Private</span>
                    )}
                  </div>
                  <div className="mt-1 truncate font-mono text-xs">
                    {vm.public_ip_enabled ? (vm.public_ip || vm.ip_address) : (vm.ip_address || '—')}
                  </div>
                </div>
              </div>

              <div className="flex flex-1 items-end justify-between gap-2 p-3 pt-3">
                <div className="text-[11px] text-muted-foreground">
                  Created {relativeTime(vm.created_at)}
                </div>
                <div className="flex items-center gap-1">
                  <PrimaryAction
                    vm={vm}
                    onRun={onRun}
                    onStop={onStop}
                    onResume={onResume}
                  />
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    onClick={() => openSSHTerminalWindow(vm.id, vm.name)}
                    disabled={vm.status !== 'running'}
                    aria-label={`Open SSH terminal for ${vm.name}`}
                    className="text-primary hover:text-primary"
                    title="Open SSH Terminal (New Window)"
                  >
                    <ExternalLink />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    onClick={() => onConsole(vm)}
                    aria-label={`Open console for ${vm.name}`}
                  >
                    <Terminal />
                  </Button>
                  <RowMenu
                    vm={vm}
                    onSnapshot={onSnapshot}
                    onConsole={onConsole}
                    onInspect={onInspect}
                    onDelete={onDelete}
                  />
                </div>
              </div>
            </Card>
          ))}
        </div>
      )}
    </div>
  );
}
