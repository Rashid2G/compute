import { useCallback, useEffect, useRef, useState } from 'react';
import {
  Activity,
  Network,
  Plus,
  RotateCw,
  Trash2,
  X,
} from 'lucide-react';
import { toast } from 'sonner';

import { api, errorMessage } from '@/lib/api';
import { formatRelativeTime } from '@/lib/format';
import type {
  NetworkAllocation,
  NetworkConfig,
  NetworkInspectResult,
  PortForwardRecord,
} from '@/lib/types';
import { cn } from '@/lib/utils';

interface NetworkPageProps {
  allocations: NetworkAllocation[];
  networkConfig: NetworkConfig | null;
  portForwards?: PortForwardRecord[];
  onRefresh: () => void;
  embedded?: boolean;
}

export function NetworkPage({
  allocations,
  networkConfig,
  portForwards: portForwardsProp,
  onRefresh,
  embedded = false,
}: NetworkPageProps) {
  const [subTab, setSubTab] = useState<'leases' | 'port-forwarding'>('leases');
  const [pruning, setPruning] = useState(false);

  const [inspectResult, setInspectResult] =
    useState<NetworkInspectResult | null>(null);
  const [inspectingId, setInspectingId] = useState<string | null>(null);

  const [portForwards, setPortForwards] = useState<PortForwardRecord[]>(
    () => portForwardsProp ?? [],
  );
  const [addPfOpen, setAddPfOpen] = useState(false);
  const [pfHostPort, setPfHostPort] = useState(18081);
  const [pfGuestPort, setPfGuestPort] = useState(8080);
  const [pfVmId, setPfVmId] = useState('');
  const [pfProto, setPfProto] = useState('tcp');

  const hasPortForwardsPropRef = useRef(portForwardsProp !== undefined);
  hasPortForwardsPropRef.current = portForwardsProp !== undefined;

  useEffect(() => {
    if (portForwardsProp !== undefined) {
      setPortForwards(portForwardsProp);
    }
  }, [portForwardsProp]);

  const loadPortForwards = useCallback(async (force = false) => {
    if (!force && hasPortForwardsPropRef.current) return;
    try {
      const list = await api.listPortForwards();
      setPortForwards(list || []);
    } catch {
      // Ignore if empty
    }
  }, []);

  useEffect(() => {
    void loadPortForwards();
  }, [loadPortForwards]);

  async function handlePrune() {
    setPruning(true);
    const toastID = toast.loading('Pruning orphaned TAP interfaces…');
    try {
      const res = await api.pruneNetwork();
      const count = res.pruned_taps?.length ?? 0;
      toast.success(
        count > 0
          ? `Pruned ${count} orphaned TAP interface(s)`
          : '0 orphaned TAP interfaces found · Bridge clean',
        { id: toastID },
      );
      onRefresh();
    } catch (err) {
      toast.error(errorMessage(err), { id: toastID });
    } finally {
      setPruning(false);
    }
  }

  async function handleInspect(vmId: string) {
    setInspectingId(vmId);
    try {
      const res = await api.inspectNetwork(vmId);
      setInspectResult(res);
    } catch (err) {
      toast.error(errorMessage(err));
    } finally {
      setInspectingId(null);
    }
  }

  function handleCreatePortForward(e: React.FormEvent) {
    e.preventDefault();
    if (!pfVmId) return;
    const vmAlloc = allocations.find((a) => a.vm_id === pfVmId);
    const payload = {
      host_port: pfHostPort,
      guest_port: pfGuestPort,
      vm_id: pfVmId,
      vm_name: vmAlloc?.vm_name || pfVmId,
      protocol: pfProto,
    };
    setAddPfOpen(false);

    const toastID = toast.loading(
      `Adding DNAT port forward :${payload.host_port} -> :${payload.guest_port}…`,
    );
    void api
      .createPortForward(payload)
      .then(async () => {
        toast.success(
          `Added DNAT port forward :${payload.host_port} -> :${payload.guest_port}`,
          { id: toastID },
        );
        await loadPortForwards(true);
      })
      .catch((err) => {
        toast.error(errorMessage(err), { id: toastID });
      });
  }

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        {!embedded ? (
          <div>
            <h1 className="font-display text-[22px] font-medium tracking-tight text-foreground">
              Network &amp; NAT
            </h1>
            <p className="text-[12px] text-muted-foreground">
              Linux bridge topology ({networkConfig?.bridge_name || 'fcbr0'}), TAP
              interface leases, and DNAT port-forwarding rules.
            </p>
          </div>
        ) : (
          <div>
            <h2 className="text-[14px] font-medium text-foreground">
              Bridge Topology, DNS &amp; NAT Port Forwarding
            </h2>
            <p className="text-[12px] text-muted-foreground">
              Linux bridge topology ({networkConfig?.bridge_name || 'fcbr0'}), TAP
              interface leases, and DNAT port-forwarding rules.
            </p>
          </div>
        )}

        <div className="flex items-center gap-2">
          <button
            type="button"
            disabled={pruning}
            onClick={() => void handlePrune()}
            className="h-8 border border-border bg-background px-3 text-[12px] text-foreground hover:bg-muted/40 disabled:opacity-50"
          >
            {pruning ? 'Pruning…' : 'Prune Orphaned TAPs'}
          </button>
          <button
            type="button"
            onClick={() => {
              onRefresh();
              void loadPortForwards(true);
            }}
            className="flex h-8 items-center gap-1.5 bg-primary hover:bg-primary-hover px-3 text-[12px] font-medium text-primary-foreground"
          >
            <RotateCw className="h-3.5 w-3.5" />
            <span>Reconcile Bridge</span>
          </button>
        </div>
      </div>

      {/* Bridge Topology Summary Bar */}
      <div className="grid grid-cols-1 sm:grid-cols-4 border border-border bg-background divide-y sm:divide-y-0 sm:divide-x divide-border font-mono text-xs">
        <div className="p-3.5 space-y-1">
          <div className="text-muted-foreground text-[10px] uppercase">
            Bridge Device
          </div>
          <div className="text-[13px] font-medium text-foreground">
            {networkConfig?.bridge_name || 'fcbr0'} (
            {networkConfig?.mode || 'nat'})
          </div>
          <div className="text-muted-foreground text-[11px]">
            IPv4 Forwarding Enabled
          </div>
        </div>
        <div className="p-3.5 space-y-1">
          <div className="text-muted-foreground text-[10px] uppercase">
            Subnet &amp; Gateway
          </div>
          <div className="text-[13px] font-medium text-foreground">
            {networkConfig?.subnet ||
              networkConfig?.subnet_cidr ||
              '172.16.0.0/24'}
          </div>
          <div className="text-muted-foreground text-[11px]">
            Gateway: {networkConfig?.bridge_ip || '172.16.0.1'}
          </div>
        </div>
        <div className="p-3.5 space-y-1">
          <div className="text-muted-foreground text-[10px] uppercase">
            IP Leases
          </div>
          <div className="text-[13px] font-medium text-foreground tabular">
            {allocations.length} / 253 IPs
          </div>
          <div className="text-success text-[11px]">
            Live SQLite IPAM Allocations
          </div>
        </div>
        <div className="p-3.5 space-y-1">
          <div className="text-muted-foreground text-[10px] uppercase">
            DNAT Port Forwards
          </div>
          <div className="text-[13px] font-medium text-foreground tabular">
            {portForwards.length} {portForwards.length === 1 ? 'rule' : 'rules'}
          </div>
          <div className="text-muted-foreground text-[11px]">
            Host ingress routing
          </div>
        </div>
      </div>

      {/* Sub-navigation */}
      <div className="flex items-center justify-between border-b border-border">
        <div className="flex items-center">
          <button
            type="button"
            onClick={() => setSubTab('leases')}
            className={cn(
              'border-b-2 px-4 py-2 text-[12.5px] transition-colors',
              subTab === 'leases'
                ? 'border-primary text-foreground font-medium'
                : 'border-transparent text-muted-foreground hover:text-foreground',
            )}
          >
            IP &amp; TAP Leases ({allocations.length})
          </button>
          <button
            type="button"
            onClick={() => setSubTab('port-forwarding')}
            className={cn(
              'border-b-2 px-4 py-2 text-[12.5px] transition-colors',
              subTab === 'port-forwarding'
                ? 'border-primary text-foreground font-medium'
                : 'border-transparent text-muted-foreground hover:text-foreground',
            )}
          >
            DNAT Port Forwarding ({portForwards.length})
          </button>
        </div>

        {subTab === 'port-forwarding' && (
          <button
            type="button"
            onClick={() => setAddPfOpen((v) => !v)}
            className="flex h-7 items-center gap-1 bg-primary hover:bg-primary-hover px-2.5 text-[11.5px] font-medium text-primary-foreground"
          >
            <Plus className="h-3 w-3" />
            <span>Add Port Rule</span>
          </button>
        )}
      </div>

      {subTab === 'leases' && (
        <div className="space-y-4">
          <div className="border border-border bg-background overflow-x-auto">
            <table className="w-full border-collapse text-left text-[12px]">
              <thead>
                <tr className="border-b border-border bg-muted/30 text-[11.5px] text-muted-foreground">
                  <th className="py-2.5 px-3 font-normal">Sandbox</th>
                  <th className="py-2.5 px-3 font-normal">Internal IPv4</th>
                  <th className="py-2.5 px-3 font-normal">Public IP / NAT</th>
                  <th className="py-2.5 px-3 font-normal">MAC Address</th>
                  <th className="py-2.5 px-3 font-normal">TAP Interface</th>
                  <th className="py-2.5 px-3 font-normal">Sandbox Access</th>
                  <th className="py-2.5 px-3 font-normal">State</th>
                  <th className="py-2.5 px-3 text-right font-normal">
                    Actions
                  </th>
                </tr>
              </thead>
              {allocations.length === 0 ? (
                <tbody>
                  <tr>
                    <td
                      colSpan={8}
                      className="py-16 text-center text-muted-foreground"
                    >
                      <Network className="mx-auto h-7 w-7 text-muted-foreground/50 mb-2" />
                      <div className="text-[12.5px]">
                        No active network IP/TAP leases allocated.
                      </div>
                    </td>
                  </tr>
                </tbody>
              ) : (
                <tbody className="divide-y divide-border font-mono text-[11.5px]">
                  {allocations.map((lease) => (
                    <tr key={lease.vm_id} className="hover:bg-muted/30">
                      <td className="py-2.5 px-3 font-sans font-medium text-foreground">
                        {lease.vm_name}
                      </td>
                      <td className="py-2.5 px-3 text-foreground">
                        {lease.ip_address}
                      </td>
                      <td className="py-2.5 px-3 text-muted-foreground">
                        {lease.public_ip ||
                          (lease.public_ip_enabled
                            ? 'SNAT Egress'
                            : 'Isolated')}
                      </td>
                      <td className="py-2.5 px-3 text-muted-foreground">
                        {lease.mac_address}
                      </td>
                      <td className="py-2.5 px-3 text-muted-foreground">
                        {lease.tap_device}
                      </td>
                      <td className="py-2.5 px-3">
                        <span className="border border-border bg-muted/40 px-1.5 py-0.5 text-[10px]">
                          {lease.network_isolation === 'isolated'
                            ? 'Isolated'
                            : 'Account'}
                        </span>
                      </td>
                      <td className="py-2.5 px-3">
                        <span className="border border-border bg-muted/40 px-1.5 py-0.5 text-[10px] uppercase">
                          {lease.status}
                        </span>
                      </td>
                      <td className="py-2.5 px-3 text-right">
                        <button
                          type="button"
                          onClick={() => void handleInspect(lease.vm_id)}
                          className="border border-border px-2 py-0.5 text-[11px] text-foreground hover:border-primary"
                        >
                          {inspectingId === lease.vm_id
                            ? 'Probing…'
                            : 'Inspect'}
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              )}
            </table>
          </div>

          {inspectResult && (
            <div className="border border-primary bg-background p-4 space-y-2 font-mono text-[11.5px]">
              <div className="flex items-center justify-between">
                <div className="flex items-center gap-2">
                  <Activity className="h-3.5 w-3.5 text-[color:var(--link)]" />
                  <span className="text-foreground font-medium">
                    Network Inspection: {inspectResult.vm_name} (
                    {inspectResult.ip_address})
                  </span>
                </div>
                <button
                  type="button"
                  onClick={() => setInspectResult(null)}
                  className="text-muted-foreground hover:text-foreground"
                >
                  <X className="h-4 w-4" />
                </button>
              </div>
              <div className="grid grid-cols-2 sm:grid-cols-5 gap-3 pt-1">
                <div>
                  <span className="text-muted-foreground block text-[10.5px]">
                    Bridge
                  </span>
                  <span>{inspectResult.bridge_name}</span>
                </div>
                <div>
                  <span className="text-muted-foreground block text-[10.5px]">
                    Gateway
                  </span>
                  <span>{inspectResult.gateway}</span>
                </div>
                <div>
                  <span className="text-muted-foreground block text-[10.5px]">
                    TAP Device
                  </span>
                  <span>{inspectResult.tap_device}</span>
                </div>
                <div>
                  <span className="text-muted-foreground block text-[10.5px]">
                    Sandbox Access
                  </span>
                  <span>
                    {inspectResult.network_isolation === 'isolated'
                      ? 'Isolated (own only)'
                      : 'Account sandboxes'}
                  </span>
                </div>
                <div>
                  <span className="text-muted-foreground block text-[10.5px]">
                    Connectivity
                  </span>
                  <span className="text-success">
                    {inspectResult.connectivity}
                  </span>
                </div>
              </div>
            </div>
          )}
        </div>
      )}

      {subTab === 'port-forwarding' && (
        <div className="space-y-4">
          {addPfOpen && (
            <form
              onSubmit={handleCreatePortForward}
              className="border border-border bg-muted/15 p-4 space-y-3"
            >
              <div className="text-[13px] font-medium text-foreground">
                Add DNAT Port Forward Rule
              </div>
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-4">
                <div>
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    Target Sandbox
                  </label>
                  <select
                    aria-label="Sandbox to forward"
                    value={pfVmId}
                    onChange={(e) => setPfVmId(e.target.value)}
                    required
                    className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px] text-foreground"
                  >
                    <option value="">Select sandbox…</option>
                    {allocations.map((a) => (
                      <option key={a.vm_id} value={a.vm_id}>
                        {a.vm_name} ({a.ip_address})
                      </option>
                    ))}
                  </select>
                </div>
                <div>
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    Host Port
                  </label>
                  <input
                    type="number"
                    aria-label="Host port"
                    value={pfHostPort}
                    onChange={(e) => setPfHostPort(Number(e.target.value))}
                    className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px] text-foreground"
                  />
                </div>
                <div>
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    Guest Port
                  </label>
                  <input
                    type="number"
                    aria-label="Guest port"
                    value={pfGuestPort}
                    onChange={(e) => setPfGuestPort(Number(e.target.value))}
                    className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px] text-foreground"
                  />
                </div>
                <div>
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    Protocol
                  </label>
                  <select
                    aria-label="Forwarding protocol"
                    value={pfProto}
                    onChange={(e) => setPfProto(e.target.value)}
                    className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px] text-foreground"
                  >
                    <option value="tcp">TCP</option>
                    <option value="udp">UDP</option>
                  </select>
                </div>
              </div>
              <div className="flex justify-end gap-2">
                <button
                  type="button"
                  onClick={() => setAddPfOpen(false)}
                  className="h-8 border border-border px-3 text-[12px] text-muted-foreground"
                >
                  Cancel
                </button>
                <button
                  type="submit"
                  className="h-8 bg-primary hover:bg-primary-hover px-3 text-[12px] font-medium text-primary-foreground"
                >
                  Save Rule
                </button>
              </div>
            </form>
          )}

          <div className="border border-border bg-background overflow-x-auto">
            <table className="w-full border-collapse text-left text-[12px]">
              <thead>
                <tr className="border-b border-border bg-muted/30 text-[11.5px] text-muted-foreground">
                  <th className="py-2.5 px-3 font-normal">Protocol</th>
                  <th className="py-2.5 px-3 font-normal">Host Port</th>
                  <th className="py-2.5 px-3 font-normal">Sandbox</th>
                  <th className="py-2.5 px-3 font-normal">Guest Port</th>
                  <th className="py-2.5 px-3 font-normal">Public URL</th>
                  <th className="py-2.5 px-3 font-normal">Created</th>
                  <th className="py-2.5 px-3 text-right font-normal">
                    Actions
                  </th>
                </tr>
              </thead>
              {portForwards.length === 0 ? (
                <tbody>
                  <tr>
                    <td
                      colSpan={7}
                      className="py-12 text-center text-muted-foreground"
                    >
                      No custom DNAT port-forwarding rules configured.
                    </td>
                  </tr>
                </tbody>
              ) : (
                <tbody className="divide-y divide-border font-mono text-[11.5px]">
                  {portForwards.map((pf) => (
                    <tr key={pf.id} className="hover:bg-muted/30">
                      <td className="py-2.5 px-3">{pf.protocol}</td>
                      <td className="py-2.5 px-3 text-foreground font-medium">
                        :{pf.host_port}
                      </td>
                      <td className="py-2.5 px-3 text-foreground">
                        {pf.vm_name || pf.vm_id}
                      </td>
                      <td className="py-2.5 px-3 text-muted-foreground">
                        :{pf.guest_port}
                      </td>
                      <td className="py-2.5 px-3 text-muted-foreground">
                        {pf.public_url ? (
                          <a
                            href={pf.public_url}
                            target="_blank"
                            rel="noreferrer"
                            className="text-[color:var(--link)] hover:underline"
                          >
                            {pf.public_url}
                          </a>
                        ) : (
                          '-'
                        )}
                      </td>
                      <td className="py-2.5 px-3 text-muted-foreground">
                        {formatRelativeTime(pf.created_at)}
                      </td>
                      <td className="py-2.5 px-3 text-right">
                        <button
                          type="button"
                          title="Delete port-forward rule"
                          aria-label="Delete port-forward rule"
                          onClick={() => {
                            void api
                              .deletePortForward(pf.id)
                              .then(() => {
                                toast.success('Deleted port forward rule');
                                void loadPortForwards(true);
                              })
                              .catch((err) => toast.error(errorMessage(err)));
                          }}
                          className="border border-border p-1 text-muted-foreground hover:text-destructive"
                        >
                          <Trash2 className="h-3 w-3" />
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              )}
            </table>
          </div>
        </div>
      )}
    </div>
  );
}

