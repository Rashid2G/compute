import { useEffect, useState } from 'react';
import {
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
import { StatusBadge } from '@/components/status-badge';
import { openSSHTerminalWindow } from '@/lib/terminal';
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
import { api } from '@/lib/api';
import { formatDateTime, formatMemory } from '@/lib/format';
import type { NetworkInspectResult, VM } from '@/lib/types';

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

export function InspectVMDialog({
  vm,
  open,
  onOpenChange,
}: {
  vm: VM | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const [inspecting, setInspecting] = useState(false);
  const [network, setNetwork] = useState<NetworkInspectResult | null>(null);

  useEffect(() => {
    if (!open || !vm) return;

    let cancelled = false;
    setInspecting(true);
    setNetwork(null);

    api
      .inspectNetwork(vm.id)
      .then((result) => {
        if (!cancelled) setNetwork(result);
      })
      .catch(() => {
        if (!cancelled) setNetwork(null);
      })
      .finally(() => {
        if (!cancelled) setInspecting(false);
      });

    return () => {
      cancelled = true;
    };
  }, [open, vm]);

  if (!vm) return null;

  const json = JSON.stringify(vm, null, 2);

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
            <StatusBadge status={vm.status} />
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
                <Fact label="Dedicated IP" value={vm.ip_address} />
                <Fact label="Public IP" value={vm.public_ip_enabled ? (vm.public_ip || vm.ip_address) : 'Disabled (off)'} />
                <Fact label="Tailnet IP" value={vm.tailnet_ip || (vm.tailscale_enabled ? 'Provisioning…' : '—')} mono />
                <Fact label="MAC" value={vm.mac_address} />
                <Fact label="TAP" value={vm.tap_device} />
                <Fact label="SSH Key" value={vm.ssh_key_name || (vm.ssh_public_key ? 'Custom Key' : 'Default Key')} mono={false} />
                <Fact label="Created" value={formatDateTime(vm.created_at)} mono={false} />
                <Fact label="Updated" value={formatDateTime(vm.updated_at)} mono={false} />
                <Fact label="Snapshot" value={vm.snapshot_path ? 'Available' : 'None'} mono={false} />
              </div>

              <div className="mt-4 flex items-center justify-between rounded-md border p-3.5 bg-secondary/15">
                <div>
                  <div className="text-xs font-medium flex items-center gap-1.5">
                    <Terminal className="h-3.5 w-3.5 text-primary" />
                    SSH Quick Connect
                  </div>
                  <div className="mt-1 font-mono text-xs text-muted-foreground select-all">
                    ssh compute@{vm.ip_address || '<ip>'}
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
                  <CopyButton value={`ssh compute@${vm.ip_address}`} label="Copy SSH" />
                  <CopyButton value={`compute ssh ${vm.name}`} label="Copy CLI" />
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
                <Fact label="Tailnet IP" value={vm.tailnet_ip || (vm.tailscale_enabled ? 'Provisioning…' : '—')} mono />
                {vm.tailnet_dns ? <Fact label="Tailnet DNS" value={vm.tailnet_dns} mono /> : null}
                <Fact label="MAC" value={vm.mac_address} />
                <Fact label="TAP" value={vm.tap_device} />
                <Fact label="Bridge" value={network?.bridge_name || '—'} />
                <Fact label="Gateway" value={network?.gateway || '—'} />
                <Fact label="Status" value={network?.status || vm.status} />
              </div>

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
                  onClick={async () => {
                    const next = !vm.public_ip_enabled;
                    await api.setVMPublicIP(vm.id, next);
                    vm.public_ip_enabled = next;
                    vm.public_ip = next ? vm.ip_address : '';
                    onOpenChange(false);
                  }}
                >
                  {vm.public_ip_enabled ? "Turn Off Public IP" : "Turn On Public IP"}
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
