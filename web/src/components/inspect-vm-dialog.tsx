import { useEffect, useState } from 'react';
import {
  ArrowRightLeft,
  CheckCircle2,
  ExternalLink,
  Globe,
  CircleSlash,
  HardDrive,
  Loader2,
  Network,
  Server,
  Terminal,
} from 'lucide-react';

import { Button } from '@/components/ui/button';
import { CopyButton } from '@/components/copy-button';
import { SandboxAccessCard } from '@/components/sandbox-access-card';
import { StatusBadge } from '@/components/status-badge';
import { openSSHTerminalWindow } from '@/lib/terminal';
import { splitSandboxHostname } from '@/lib/sandbox-access';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Separator } from '@/components/ui/separator';
import {
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from '@/components/ui/tabs';
import { toast } from 'sonner';
import { api, errorMessage } from '@/lib/api';
import { formatDateTime, formatMemory } from '@/lib/format';
import type {
  NetworkInspectResult,
  PortForwardRecord,
  VM,
  WorkerNodeRecord,
  WorkerPoolRecord,
} from '@/lib/types';

function Fact({
  label,
  value,
  mono = true,
}: {
  label: string;
  value: string;
  mono?: boolean;
}) {
  return (
    <div className="min-w-0">
      <div className="text-[10px] font-medium uppercase tracking-[0.08em] text-muted-foreground">
        {label}
      </div>
      <div className={mono ? 'mt-1 truncate font-mono text-xs' : 'mt-1 truncate text-xs'}>
        {value || '—'}
      </div>
    </div>
  );
}

function sandboxZoneLabel(vm: VM): string {
  return splitSandboxHostname(vm.hostname)?.zone || '—';
}

export function InspectVMDialog({
  vm,
  workers = [],
  pools = [],
  portForwards: portForwardsProp,
  isAdmin = false,
  open,
  onOpenChange,
  onUpdated,
}: {
  vm: VM | null;
  workers?: WorkerNodeRecord[];
  pools?: WorkerPoolRecord[];
  portForwards?: PortForwardRecord[];
  isAdmin?: boolean;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onUpdated?: () => void;
}) {
  const [inspecting, setInspecting] = useState(false);
  const [network, setNetwork] = useState<NetworkInspectResult | null>(null);
  const [fetchedPortForwards, setFetchedPortForwards] = useState<PortForwardRecord[]>([]);
  const [migrateOpen, setMigrateOpen] = useState(false);
  const [targetWorkerId, setTargetWorkerId] = useState('');
  const [targetPoolId, setTargetPoolId] = useState('');
  const [migrateForce, setMigrateForce] = useState(false);

  const vmId = vm?.id ?? null;

  useEffect(() => {
    if (!open || !vmId) return;

    let cancelled = false;
    setInspecting(true);
    setNetwork(null);
    setMigrateOpen(false);
    setTargetWorkerId('');
    setTargetPoolId('');
    setMigrateForce(false);

    api
      .inspectNetwork(vmId)
      .then((result) => {
        if (!cancelled) setNetwork(result);
      })
      .catch(() => {
        if (!cancelled) setNetwork(null);
      })
      .finally(() => {
        if (!cancelled) setInspecting(false);
      });

    if (portForwardsProp === undefined && typeof api.listPortForwards === 'function') {
      void api
        .listPortForwards()
        .then((list) => {
          if (!cancelled) setFetchedPortForwards(list || []);
        })
        .catch(() => {
          if (!cancelled) setFetchedPortForwards([]);
        });
    }

    return () => {
      cancelled = true;
    };
  }, [open, vmId, portForwardsProp]);

  if (!vm) return null;

  const json = JSON.stringify(vm, null, 2);
  const workerNode = workers.find((w) => w.id === vm.worker_id);
  const workerLabel = workerNode
    ? `${workerNode.name} (${workerNode.id})`
    : vm.worker_id || 'nod_host_local';
  const vmPortForwards = (portForwardsProp ?? fetchedPortForwards).filter(
    (pf) => pf.vm_id === vm.id || pf.vm_name === vm.name,
  );

  function handleMigrateSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!vm) return;
    const targetVM = vm;
    const vmName = targetVM.name || targetVM.id;
    const payload = {
      target_worker_id: targetWorkerId || undefined,
      target_pool_id: targetPoolId || undefined,
      force: migrateForce,
    };
    onOpenChange(false);
    const toastID = toast.loading(`Migrating sandbox ${vmName}…`);
    void api
      .migrateVM(targetVM.id, payload)
      .then(() => {
        toast.success(`Migrated sandbox ${vmName}`, { id: toastID });
        onUpdated?.();
      })
      .catch((err) => {
        toast.error(`Migration failed for ${vmName}`, {
          id: toastID,
          description: errorMessage(err),
        });
      });
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[92vh] max-w-3xl overflow-hidden p-0">
        <DialogHeader className="border-b p-6 pb-4">
          <div className="flex items-start justify-between gap-4 pr-6">
            <div className="flex items-center gap-3">
              <span className="flex h-9 w-9 items-center justify-center rounded-md border bg-secondary text-muted-foreground">
                <Server className="h-4 w-4" />
              </span>
              <div className="min-w-0">
                <DialogTitle className="truncate">{vm.name}</DialogTitle>
                <DialogDescription className="mt-0.5 flex items-center gap-1 font-mono text-xs">
                  {vm.id}
                  <CopyButton value={vm.id} label="Copy ID" className="h-5 w-5" />
                </DialogDescription>
              </div>
            </div>
            <div className="flex items-center gap-2">
              {vm.auto_hibernated && (
                <span
                  title={vm.hibernate_reason || 'Auto-hibernated to SSD'}
                  className="border border-warning/40 bg-warning/10 px-2 py-0.5 font-mono text-[10.5px] uppercase text-warning"
                >
                  SSD Hibernated
                </span>
              )}
              <StatusBadge status={vm.status} />
            </div>
          </div>
        </DialogHeader>

        <div className="max-h-[calc(92vh-9.5rem)] overflow-y-auto p-6">
          <Tabs defaultValue="overview">
            <TabsList>
              <TabsTrigger value="overview">Overview</TabsTrigger>
              <TabsTrigger value="network">Network</TabsTrigger>
              <TabsTrigger value="paths">Paths</TabsTrigger>
              <TabsTrigger value="json">JSON</TabsTrigger>
            </TabsList>

            <TabsContent value="overview">
              <div className="grid grid-cols-2 gap-x-4 gap-y-4 rounded-md border p-4 sm:grid-cols-3">
                <Fact label="vCPUs" value={String(vm.vcpus)} />
                <Fact label="Memory" value={formatMemory(vm.memory_mb)} />
                <Fact label="PID" value={vm.pid ? String(vm.pid) : '—'} />
                <Fact label="Worker Node" value={workerLabel} />
                <Fact label="Pool" value={vm.pool_id || 'default'} />
                <Fact
                  label="Hibernation"
                  value={
                    vm.auto_hibernated
                      ? `SSD Hibernated${vm.hibernate_reason ? ` (${vm.hibernate_reason})` : ''}`
                      : 'Active'
                  }
                  mono={false}
                />
                <Fact label="Dedicated IP" value={vm.ip_address} />
                <Fact label="Public IP" value={vm.public_ip_enabled ? (vm.public_ip || vm.ip_address) : 'Disabled (off)'} />
                <Fact label="Sandbox Network" value={vm.network_isolation === 'isolated' ? 'Isolated (own sandbox only)' : 'Account (all account sandboxes)'} mono={false} />
                <Fact label="MAC" value={vm.mac_address} />
                <Fact label="TAP" value={vm.tap_device} />
                <Fact label="SSH Key" value={vm.ssh_key_name || (vm.ssh_public_key ? 'Custom Key' : 'Default Key')} mono={false} />
                <Fact label="SSH auth" value={vm.ssh_password_enabled ? 'Password + key' : 'Public key only'} mono={false} />
                <Fact label="Created" value={formatDateTime(vm.created_at)} mono={false} />
                <Fact label="Updated" value={formatDateTime(vm.updated_at)} mono={false} />
                <Fact label="Snapshot" value={vm.snapshot_path ? 'Available' : 'None'} mono={false} />
              </div>

              <div className="mt-4 grid grid-cols-2 gap-x-4 gap-y-4 rounded-md border p-4 sm:grid-cols-2">
                <Fact label="Hostname" value={vm.hostname || '—'} />
                <Fact label="Zone" value={sandboxZoneLabel(vm)} mono />
              </div>

              <SandboxAccessCard vm={vm} portForwards={vmPortForwards} className="mt-4" />

              {isAdmin && (
                <div className="mt-4 rounded-md border p-3.5 bg-secondary/15 space-y-3">
                  <div className="flex items-center justify-between gap-2">
                    <div>
                      <div className="text-xs font-medium flex items-center gap-1.5">
                        <ArrowRightLeft className="h-3.5 w-3.5 text-primary" />
                        Migrate Sandbox Placement
                      </div>
                      <div className="mt-0.5 text-[11px] text-muted-foreground">
                        Move this sandbox to another worker node or placement pool.
                      </div>
                    </div>
                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      className="h-8 text-xs"
                      onClick={() => setMigrateOpen((v) => !v)}
                    >
                      {migrateOpen ? 'Hide Migration' : 'Migrate Sandbox'}
                    </Button>
                  </div>

                  {migrateOpen && (
                    <form
                      onSubmit={handleMigrateSubmit}
                      className="space-y-3 border-t border-border pt-3 text-xs"
                    >
                      <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                        <div>
                          <label className="block text-[11px] text-muted-foreground mb-1">
                            Target Worker Node
                          </label>
                          <select
                            aria-label="Target Worker Node"
                            value={targetWorkerId}
                            onChange={(e) => setTargetWorkerId(e.target.value)}
                            className="h-8 w-full border border-border bg-background px-2 font-mono text-xs text-foreground focus:border-primary focus:outline-none"
                          >
                            <option value="">Auto-select in target pool</option>
                            {workers
                              .filter((w) => w.id !== vm.worker_id)
                              .map((w) => (
                                <option key={w.id} value={w.id}>
                                  {w.name} ({w.pool_id} · {w.status})
                                </option>
                              ))}
                          </select>
                        </div>

                        <div>
                          <label className="block text-[11px] text-muted-foreground mb-1">
                            Target Pool
                          </label>
                          <select
                            aria-label="Target Pool"
                            value={targetPoolId}
                            onChange={(e) => setTargetPoolId(e.target.value)}
                            className="h-8 w-full border border-border bg-background px-2 font-mono text-xs text-foreground focus:border-primary focus:outline-none"
                          >
                            <option value="">Keep current / match worker</option>
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
                      </div>

                      <div className="flex flex-wrap items-center justify-between gap-2">
                        <label className="inline-flex items-center gap-2 text-foreground">
                          <input
                            type="checkbox"
                            checked={migrateForce}
                            onChange={(e) => setMigrateForce(e.target.checked)}
                            className="accent-primary"
                          />
                          <span>Force stop &amp; migrate if currently running</span>
                        </label>
                        <Button type="submit" size="sm" className="h-8 text-xs">
                          Confirm Migration
                        </Button>
                      </div>
                    </form>
                  )}
                </div>
              )}

              <div className="mt-4 flex items-center justify-between rounded-md border p-3.5 bg-secondary/15">
                <div>
                  <div className="text-xs font-medium flex items-center gap-1.5">
                    <Terminal className="h-3.5 w-3.5 text-primary" />
                    SSH Terminal Window
                  </div>
                  <div className="mt-0.5 text-[11px] text-muted-foreground">
                    Opens the browser terminal connected through the daemon (no local SSH client needed).
                  </div>
                </div>
                <div className="flex items-center gap-2">
                  <Button
                    size="sm"
                    className="h-8 gap-1.5 text-xs font-medium"
                    onClick={() => openSSHTerminalWindow(vm.id, vm.name)}
                    disabled={vm.status !== 'running'}
                  >
                    <ExternalLink className="h-3.5 w-3.5" />
                    Open Terminal Window
                  </Button>
                </div>
              </div>
            </TabsContent>

            <TabsContent value="network">
              <div className="flex items-center justify-between gap-3 rounded-md border p-4">
                <div className="flex items-center gap-3">
                  {inspecting ? (
                    <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
                  ) : network?.reachable ? (
                    <CheckCircle2 className="h-4 w-4 text-success" />
                  ) : (
                    <CircleSlash className="h-4 w-4 text-muted-foreground" />
                  )}
                  <div>
                    <div className="text-sm font-medium">
                      {inspecting
                        ? 'Checking reachability…'
                        : network?.reachable
                          ? 'Guest is reachable'
                          : 'Guest is not reachable'}
                    </div>
                    <div className="text-xs text-muted-foreground">
                      {network?.connectivity || 'No connectivity result available.'}
                    </div>
                  </div>
                </div>
                <Network className="h-4 w-4 text-muted-foreground" />
              </div>

              <div className="mt-4 grid grid-cols-2 gap-x-4 gap-y-4 rounded-md border p-4 sm:grid-cols-3">
                <Fact label="Dedicated IP" value={vm.ip_address} />
                <Fact label="Public IP" value={vm.public_ip_enabled ? (vm.public_ip || vm.ip_address) : 'Disabled (off)'} />
                <Fact label="Public Access" value={vm.public_ip_enabled ? 'ON (Dedicated on main network)' : 'OFF (Private only)'} mono={false} />
                <Fact
                  label="Sandbox Network"
                  value={(network?.network_isolation || vm.network_isolation) === 'isolated'
                    ? 'Isolated (own sandbox only)'
                    : 'Account (all account sandboxes)'}
                  mono={false}
                />
                <Fact label="MAC" value={vm.mac_address} />
                <Fact label="TAP" value={vm.tap_device} />
                <Fact label="Bridge" value={network?.bridge_name || '—'} />
                <Fact label="Gateway" value={network?.gateway || '—'} />
                <Fact label="Status" value={network?.status || vm.status} />
              </div>

              {vmPortForwards.length > 0 && (
                <div className="mt-4 rounded-md border p-3.5 bg-secondary/15 space-y-2">
                  <div className="text-xs font-medium flex items-center gap-1.5">
                    <Globe className="h-3.5 w-3.5 text-primary" />
                    Exposed Ports ({vmPortForwards.length})
                  </div>
                  <div className="divide-y divide-border border border-border bg-background">
                    {vmPortForwards.map((pf) => {
                      const url =
                        pf.public_url ||
                        (pf.host_port > 0
                          ? `${window.location.protocol}//${window.location.hostname}:${pf.host_port}`
                          : '');
                      return (
                        <div
                          key={pf.id}
                          className="flex items-center justify-between gap-2 px-3 py-2 font-mono text-xs"
                        >
                          <div className="flex items-center gap-2 min-w-0">
                            <span className="text-foreground font-medium shrink-0">
                              :{pf.guest_port}
                            </span>
                            <span
                              className={
                                pf.public
                                  ? 'shrink-0 border border-success/40 bg-success/10 px-1.5 py-0.5 text-[10px] uppercase text-success'
                                  : 'shrink-0 border border-border bg-muted/40 px-1.5 py-0.5 text-[10px] uppercase text-muted-foreground'
                              }
                            >
                              {pf.public ? 'public' : 'private'}
                            </span>
                            {url && (
                              <a
                                href={url}
                                target="_blank"
                                rel="noopener noreferrer"
                                className="truncate text-[color:var(--link)] hover:underline"
                              >
                                {url}
                              </a>
                            )}
                          </div>
                          {url && (
                            <CopyButton
                              value={url}
                              label={`Copy URL for port ${pf.guest_port}`}
                              className="h-6 w-6 shrink-0"
                            />
                          )}
                        </div>
                      );
                    })}
                  </div>
                </div>
              )}

              <div className="mt-4 flex items-center justify-between rounded-md border p-3.5 bg-secondary/15">
                <div>
                  <div className="text-xs font-medium flex items-center gap-1.5">
                    <Globe className="h-3.5 w-3.5 text-primary" />
                    Dedicated Public IP
                  </div>
                  <div className="mt-0.5 text-[11px] text-muted-foreground">
                    {vm.public_ip_enabled
                      ? 'VM takes a dedicated IP on the main network considered as its public IP.'
                      : 'Public IP is turned off. VM is isolated from public ingress/egress.'}
                  </div>
                </div>
                <Button
                  type="button"
                  size="sm"
                  variant={vm.public_ip_enabled ? "outline" : "default"}
                  onClick={() => {
                    const targetVM = vm;
                    const vmName = targetVM.name || targetVM.id;
                    const next = !targetVM.public_ip_enabled;
                    onOpenChange(false);
                    const toastID = toast.loading(
                      `${next ? 'Enabling' : 'Disabling'} public IP for ${vmName}…`,
                    );
                    void api
                      .setVMPublicIP(targetVM.id, next)
                      .then(() => {
                        toast.success(
                          `Public IP ${next ? 'enabled' : 'disabled'} for ${vmName}`,
                          { id: toastID },
                        );
                        onUpdated?.();
                      })
                      .catch((err) => {
                        toast.error(
                          `Could not update public IP for ${vmName}`,
                          { id: toastID, description: errorMessage(err) },
                        );
                      });
                  }}
                >
                  {vm.public_ip_enabled ? "Turn Off Public IP" : "Turn On Public IP"}
                </Button>
              </div>

              <div className="mt-3 flex items-center justify-between rounded-md border p-3.5 bg-secondary/15">
                <div>
                  <div className="text-xs font-medium flex items-center gap-1.5">
                    <Network className="h-3.5 w-3.5 text-primary" />
                    Sandbox-to-Sandbox Network
                  </div>
                  <div className="mt-0.5 text-[11px] text-muted-foreground">
                    {(vm.network_isolation || 'account') === 'isolated'
                      ? 'Isolated: blocked from communicating with all other sandboxes.'
                      : 'Account: can communicate with other non-isolated sandboxes in your account.'}
                  </div>
                </div>
                <Button
                  type="button"
                  size="sm"
                  variant={(vm.network_isolation || 'account') === 'isolated' ? 'default' : 'outline'}
                  onClick={() => {
                    const targetVM = vm;
                    const vmName = targetVM.name || targetVM.id;
                    const nextMode =
                      (targetVM.network_isolation || 'account') === 'isolated'
                        ? 'account'
                        : 'isolated';
                    onOpenChange(false);
                    const toastID = toast.loading(
                      `Switching ${vmName} to ${nextMode === 'isolated' ? 'isolated' : 'account'} network…`,
                    );
                    void api
                      .setVMNetworkIsolation(targetVM.id, nextMode)
                      .then(() => {
                        toast.success(
                          nextMode === 'isolated'
                            ? `${vmName} is now isolated on its own`
                            : `${vmName} can now communicate with account sandboxes`,
                          { id: toastID },
                        );
                        onUpdated?.();
                      })
                      .catch((err) => {
                        toast.error(
                          `Could not update network isolation for ${vmName}`,
                          { id: toastID, description: errorMessage(err) },
                        );
                      });
                  }}
                >
                  {(vm.network_isolation || 'account') === 'isolated'
                    ? 'Allow Account Sandboxes'
                    : 'Isolate Sandbox'}
                </Button>
              </div>
            </TabsContent>

            <TabsContent value="paths">
              <div className="flex items-center gap-2 pb-3 text-xs font-medium text-muted-foreground">
                <HardDrive className="h-4 w-4" />
                Host paths
              </div>
              <div className="overflow-hidden rounded-md border">
                {[
                  ['Root filesystem', vm.rootfs_path],
                  ['Kernel', vm.kernel_path],
                  ['Overlay disk', vm.disk_path],
                  ['Firecracker socket', vm.socket_path || '—'],
                  ['Snapshot state', vm.snapshot_path || '—'],
                  ['Memory file', vm.mem_file_path || '—'],
                  ['SSH key', vm.ssh_key_path || '—'],
                  ['Embedded key', vm.ssh_key_name || (vm.ssh_public_key ? 'Custom Key' : 'Daemon default')],
                ].map(([label, value], index) => (
                  <div
                    key={label}
                    className={
                      index === 0 ? 'px-3 py-2.5' : 'border-t px-3 py-2.5'
                    }
                  >
                    <div className="text-[10px] uppercase tracking-[0.08em] text-muted-foreground">
                      {label}
                    </div>
                    <div className="mt-0.5 flex items-center gap-1">
                      <code className="min-w-0 flex-1 break-all font-mono text-[11px]">
                        {value}
                      </code>
                      {value && value !== '—' ? (
                        <CopyButton value={value} label={`Copy ${label}`} />
                      ) : null}
                    </div>
                  </div>
                ))}
              </div>
            </TabsContent>

            <TabsContent value="json">
              <div className="relative">
                <pre className="max-h-[52vh] overflow-auto rounded-md border bg-secondary/40 p-3 font-mono text-[11px] leading-relaxed">
                  {json}
                </pre>
                <div className="absolute right-2 top-2">
                  <CopyButton value={json} label="Copy JSON" />
                </div>
              </div>
            </TabsContent>
          </Tabs>

          <Separator className="my-5" />
          <div className="flex flex-wrap gap-x-6 gap-y-1 font-mono text-[10px] text-muted-foreground">
            <span>created {formatDateTime(vm.created_at)}</span>
            <span>updated {formatDateTime(vm.updated_at)}</span>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
