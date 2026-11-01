import { useMemo, useState } from 'react';
import { Network, RefreshCw, Search, Trash2 } from 'lucide-react';
import { toast } from 'sonner';

import { CopyButton } from '@/components/copy-button';
import { EmptyState } from '@/components/empty-state';
import { StatusBadge } from '@/components/status-badge';
import { Button } from '@/components/ui/button';
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';
import { api, errorMessage } from '@/lib/api';
import { formatTime } from '@/lib/format';
import type { NetworkAllocation, NetworkConfig } from '@/lib/types';

function NetworkFact({
  label,
  value,
  hint,
}: {
  label: string;
  value: string;
  hint?: string;
}) {
  return (
    <Card className="p-4">
      <div className="text-[10px] font-medium uppercase tracking-[0.08em] text-muted-foreground">
        {label}
      </div>
      <div className="mt-1.5 truncate font-mono text-sm font-medium">{value}</div>
      {hint ? <div className="mt-1 text-[11px] text-muted-foreground">{hint}</div> : null}
    </Card>
  );
}

export function NetworkPage({
  allocations,
  networkConfig,
  onRefresh,
}: {
  allocations: NetworkAllocation[];
  networkConfig: NetworkConfig | null;
  onRefresh: () => void;
}) {
  const [query, setQuery] = useState('');
  const [pruning, setPruning] = useState(false);

  const filtered = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    if (!normalized) return allocations;
    return allocations.filter((allocation) =>
      [
        allocation.ip_address,
        allocation.vm_name,
        allocation.vm_id,
        allocation.mac_address,
        allocation.tap_device,
      ]
        .filter(Boolean)
        .some((value) => value.toLowerCase().includes(normalized)),
    );
  }, [allocations, query]);

  async function prune() {
    setPruning(true);
    try {
      const result = await api.pruneNetwork();
      const count = result.pruned_taps.length;
      toast.success(
        count === 0
          ? 'No orphaned TAP interfaces found'
          : `Removed ${count} orphaned TAP interface${count === 1 ? '' : 's'}`,
        {
          description:
            count > 0 ? result.pruned_taps.join(', ') : 'The network is already clean.',
        },
      );
      onRefresh();
    } catch (error) {
      toast.error('Could not reconcile network', {
        description: errorMessage(error),
      });
    } finally {
      setPruning(false);
    }
  }

  return (
    <div className="space-y-4 animate-fade-in">
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <NetworkFact
          label="Routing mode"
          value={(networkConfig?.mode || 'unknown').toUpperCase()}
          hint={
            networkConfig?.mode === 'nat'
              ? 'Host masquerade enabled'
              : 'Bridge routing'
          }
        />
        <NetworkFact
          label="Bridge"
          value={networkConfig?.bridge_name || '—'}
          hint="Host TAP aggregation"
        />
        <NetworkFact
          label="Gateway"
          value={networkConfig?.bridge_ip || '—'}
          hint="Default guest gateway"
        />
        <NetworkFact
          label="Subnet"
          value={networkConfig?.subnet || '—'}
          hint={
            networkConfig?.physical_nic
              ? `Uplink ${networkConfig.physical_nic}`
              : 'Virtual address pool'
          }
        />
      </div>

      <Card>
        <CardHeader className="flex-col gap-3 space-y-0 border-b pb-4 sm:flex-row sm:items-center sm:justify-between">
          <div className="flex items-center gap-2">
            <CardTitle>Address leases</CardTitle>
            <span className="tabular rounded-md border bg-secondary px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground">
              {allocations.length}
            </span>
          </div>

          <div className="flex items-center gap-2">
            <div className="relative w-full sm:w-60">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="Search address, instance, TAP…"
                className="pl-8"
                aria-label="Search leases"
              />
            </div>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => void prune()}
              disabled={pruning}
              className="shrink-0 text-destructive hover:bg-destructive/10 hover:text-destructive"
            >
              {pruning ? <RefreshCw className="animate-spin" /> : <Trash2 />}
              <span className="hidden sm:inline">Prune TAPs</span>
            </Button>
          </div>
        </CardHeader>

        <CardContent className="p-0">
          {filtered.length === 0 ? (
            <EmptyState
              icon={Network}
              title={allocations.length === 0 ? 'No address leases' : 'No leases match this search'}
              description={
                allocations.length === 0
                  ? 'Leases appear here as soon as instances reserve an address.'
                  : 'Clear the search to see all allocated addresses.'
              }
            />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Address</TableHead>
                  <TableHead>Instance</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>MAC</TableHead>
                  <TableHead>TAP</TableHead>
                  <TableHead>Updated</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {filtered.map((allocation) => (
                  <TableRow key={allocation.ip_address}>
                    <TableCell>
                      <div className="flex items-center gap-1.5">
                        <span className="font-mono text-xs font-medium">
                          {allocation.ip_address}
                        </span>
                        {allocation.public_ip_enabled ? (
                          <span className="rounded bg-primary/10 px-1 py-0.5 text-[9px] font-semibold text-primary uppercase">
                            Public
                          </span>
                        ) : (
                          <span className="rounded bg-secondary px-1 py-0.5 text-[9px] font-medium text-muted-foreground uppercase">
                            Private
                          </span>
                        )}
                        <CopyButton
                          value={allocation.ip_address}
                          label="Copy address"
                          className="h-6 w-6"
                        />
                      </div>
                    </TableCell>
                    <TableCell>
                      <div className="text-[13px] font-medium">
                        {allocation.vm_name || allocation.vm_id}
                      </div>
                      <div className="font-mono text-[11px] text-muted-foreground">
                        {allocation.vm_id}
                      </div>
                    </TableCell>
                    <TableCell>
                      <StatusBadge status={allocation.status} />
                    </TableCell>
                    <TableCell className="font-mono text-[11px] text-muted-foreground">
                      {allocation.mac_address || '—'}
                    </TableCell>
                    <TableCell className="font-mono text-xs">
                      {allocation.tap_device || '—'}
                    </TableCell>
                    <TableCell className="whitespace-nowrap font-mono text-[11px] text-muted-foreground">
                      {formatTime(allocation.updated_at)}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
