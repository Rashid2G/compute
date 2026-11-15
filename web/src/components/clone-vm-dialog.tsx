import { useEffect, useState } from 'react';
import { Copy, Play, Shield, X } from 'lucide-react';
import { toast } from 'sonner';

import { api, errorMessage } from '@/lib/api';
import { formatMemory } from '@/lib/format';
import type { NetworkIsolationMode, VM } from '@/lib/types';
import { cn } from '@/lib/utils';

interface CloneVMDialogProps {
  vm: VM | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCloned?: () => void;
}

export function CloneVMDialog({
  vm,
  open,
  onOpenChange,
  onCloned,
}: CloneVMDialogProps) {
  const [baseName, setBaseName] = useState('');
  const [replicaCount, setReplicaCount] = useState(1);
  const [vcpus, setVcpus] = useState(2);
  const [memoryMb, setMemoryMb] = useState(2048);
  const [diskGb, setDiskGb] = useState(20);
  const [networkIsolation, setNetworkIsolation] =
    useState<NetworkIsolationMode>('account');
  const [description, setDescription] = useState('');
  const [autoStart, setAutoStart] = useState(false);

  useEffect(() => {
    if (!open || !vm) return;
    setBaseName(`${vm.name}-clone`);
    setReplicaCount(1);
    setVcpus(vm.vcpus || 2);
    setMemoryMb(vm.memory_mb || 2048);
    setDiskGb(Math.max(1, Math.round((vm.disk_mb || 20480) / 1024)));
    setNetworkIsolation(vm.network_isolation || 'account');
    setDescription(vm.description || '');
    setAutoStart(false);
  }, [open, vm]);

  if (!open || !vm) return null;

  const sourceVM = vm;
  const sourceDiskGb = Math.max(
    1,
    Math.round((sourceVM.disk_mb || 20480) / 1024),
  );

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    const cleanBase =
      baseName
        .trim()
        .toLowerCase()
        .replace(/[^a-z0-9-]+/g, '-')
        .replace(/^-+|-+$/g, '') || `${sourceVM.name}-clone`;
    const count = Math.min(5, Math.max(1, replicaCount));
    const names = Array.from({ length: count }, (_, idx) =>
      count > 1 ? `${cleanBase}-${idx + 1}` : cleanBase,
    );

    const payloadBase = {
      vcpus,
      memory_mb: memoryMb,
      disk_gb: diskGb,
      network_isolated: networkIsolation === 'isolated',
      description: description.trim() || undefined,
      auto_start: autoStart,
    };

    onOpenChange(false);

    const toastID = toast.loading(
      count > 1
        ? `Replicating ${count} sandboxes from ${sourceVM.name}…`
        : `Replicating ${sourceVM.name} as ${names[0]}…`,
    );

    void Promise.all(
      names.map((name) =>
        api.cloneVM(sourceVM.id, {
          name,
          ...payloadBase,
        }),
      ),
    )
      .then(() => {
        toast.success(
          count > 1
            ? `Created ${count} replicas (${names[0]} … ${names[names.length - 1]})`
            : `Replicated ${names[0]}`,
          { id: toastID },
        );
        onCloned?.();
      })
      .catch((err) => {
        toast.error(`Replication failed: ${errorMessage(err)}`, {
          id: toastID,
        });
        onCloned?.();
      });
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/75 p-4 overflow-y-auto"
      onClick={() => onOpenChange(false)}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="clone-vm-title"
        className="w-full max-w-[480px] border border-border bg-background p-6 text-foreground my-auto"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between mb-4">
          <div className="flex items-center gap-2">
            <Copy className="h-4 w-4 text-[#5e7ff5]" />
            <h2
              id="clone-vm-title"
              className="text-[15px] font-medium text-foreground"
            >
              Replicate Sandbox
            </h2>
          </div>
          <button
            type="button"
            onClick={() => onOpenChange(false)}
            className="text-muted-foreground hover:text-foreground transition-colors"
            aria-label="Close replicate modal"
          >
            <X className="h-4 w-4" />
          </button>
        </div>

        {/* Source summary strip */}
        <div className="mb-4 border border-border bg-muted/20 p-3 text-xs space-y-1">
          <div className="flex items-center justify-between">
            <span className="font-medium text-foreground">{sourceVM.name}</span>
            <span className="border border-border bg-background px-1.5 py-0.5 font-mono text-[10px] uppercase text-muted-foreground">
              {sourceVM.status}
            </span>
          </div>
          <div className="font-mono text-[11px] text-muted-foreground">
            {sourceVM.ip_address || 'no IP'} · {sourceVM.vcpus} vCPU ·{' '}
            {formatMemory(sourceVM.memory_mb)} · {sourceDiskGb} GB disk
          </div>
        </div>

        <form onSubmit={handleSubmit} className="space-y-4">
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
            <div className="sm:col-span-2 space-y-1">
              <label className="block text-[12px] text-muted-foreground">
                Base Name
              </label>
              <input
                type="text"
                aria-label="Replica base name"
                value={baseName}
                onChange={(e) =>
                  setBaseName(
                    e.target.value
                      .toLowerCase()
                      .replace(/[^a-z0-9-]+/g, '-')
                      .replace(/-{2,}/g, '-')
                      .slice(0, 56),
                  )
                }
                required
                className="h-9 w-full border border-border bg-background px-3 font-mono text-[12px] text-foreground focus:border-[#263fa9] focus:outline-none"
              />
            </div>

            <div className="space-y-1">
              <label className="block text-[12px] text-muted-foreground">
                Replicas (1–5)
              </label>
              <div className="grid grid-cols-5 border border-border h-9">
                {[1, 2, 3, 4, 5].map((cnt) => (
                  <button
                    key={cnt}
                    type="button"
                    aria-label={`Replica count ${cnt}`}
                    onClick={() => setReplicaCount(cnt)}
                    className={cn(
                      'font-mono text-[12px] border-l first:border-l-0 border-border transition-colors',
                      replicaCount === cnt
                        ? 'bg-[#263fa9] text-white font-medium'
                        : 'bg-background text-muted-foreground hover:text-foreground',
                    )}
                  >
                    {cnt}
                  </button>
                ))}
              </div>
            </div>
          </div>

          {replicaCount > 1 && (
            <div className="border border-border bg-muted/15 px-3 py-2 font-mono text-[11px] text-muted-foreground">
              Will provision:{' '}
              <span className="text-foreground">
                {Array.from(
                  { length: replicaCount },
                  (_, i) => `${baseName || `${sourceVM.name}-clone`}-${i + 1}`,
                ).join(', ')}
              </span>
            </div>
          )}

          {/* Resource Overrides */}
          <div className="grid grid-cols-3 gap-3">
            <div className="space-y-1">
              <label className="block text-[12px] text-muted-foreground">
                vCPUs
              </label>
              <select
                aria-label="Replica vCPUs"
                value={vcpus}
                onChange={(e) => setVcpus(Number(e.target.value))}
                className="h-9 w-full border border-border bg-background px-2.5 font-mono text-[12px] text-foreground focus:border-[#263fa9] focus:outline-none"
              >
                {[1, 2, 4, 8, 16].map((c) => (
                  <option key={c} value={c}>
                    {c} vCPU
                  </option>
                ))}
              </select>
            </div>

            <div className="space-y-1">
              <label className="block text-[12px] text-muted-foreground">
                Memory (MB)
              </label>
              <select
                aria-label="Replica memory MB"
                value={memoryMb}
                onChange={(e) => setMemoryMb(Number(e.target.value))}
                className="h-9 w-full border border-border bg-background px-2.5 font-mono text-[12px] text-foreground focus:border-[#263fa9] focus:outline-none"
              >
                {[512, 1024, 2048, 4096, 8192, 16384].map((mb) => (
                  <option key={mb} value={mb}>
                    {mb} MB
                  </option>
                ))}
              </select>
            </div>

            <div className="space-y-1">
              <label className="block text-[12px] text-muted-foreground">
                Disk (GB)
              </label>
              <input
                type="number"
                aria-label="Replica disk GB"
                min={1}
                max={2000}
                value={diskGb}
                onChange={(e) =>
                  setDiskGb(Math.max(1, Number(e.target.value) || 10))
                }
                className="h-9 w-full border border-border bg-background px-2.5 font-mono text-[12px] text-foreground focus:border-[#263fa9] focus:outline-none"
              />
            </div>
          </div>

          <div className="space-y-1">
            <label className="block text-[12px] text-muted-foreground">
              Description (optional)
            </label>
            <input
              type="text"
              aria-label="Replica description"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="Worker pool replica or staging branch"
              className="h-9 w-full border border-border bg-background px-3 text-[12px] text-foreground placeholder:text-muted-foreground focus:border-[#263fa9] focus:outline-none"
            />
          </div>

          <div className="space-y-1.5">
            <label className="block text-[12px] text-muted-foreground">
              Network Isolation
            </label>
            <div className="grid grid-cols-2 gap-2">
              <button
                type="button"
                onClick={() => setNetworkIsolation('account')}
                className={cn(
                  'border px-3 py-2 text-left text-[12px] transition-colors',
                  networkIsolation === 'account'
                    ? 'border-[#495eb7] bg-[#0f1634] text-white'
                    : 'border-border bg-background text-foreground hover:border-foreground/40',
                )}
              >
                <div className="font-medium">Account network</div>
                <div className="text-[10px] text-muted-foreground">
                  Reachable from account sandboxes
                </div>
              </button>
              <button
                type="button"
                onClick={() => setNetworkIsolation('isolated')}
                className={cn(
                  'border px-3 py-2 text-left text-[12px] transition-colors',
                  networkIsolation === 'isolated'
                    ? 'border-[#495eb7] bg-[#0f1634] text-white'
                    : 'border-border bg-background text-foreground hover:border-foreground/40',
                )}
              >
                <div className="font-medium flex items-center gap-1">
                  <Shield className="h-3 w-3" />
                  Isolated
                </div>
                <div className="text-[10px] text-muted-foreground">
                  Blocked from other sandboxes
                </div>
              </button>
            </div>
          </div>

          <div className="flex items-center gap-2 pt-0.5">
            <input
              id="clone-auto-start"
              type="checkbox"
              checked={autoStart}
              onChange={(e) => setAutoStart(e.target.checked)}
              className="h-3.5 w-3.5 accent-[#263fa9] cursor-pointer"
            />
            <label
              htmlFor="clone-auto-start"
              className="text-[12px] text-foreground cursor-pointer select-none"
            >
              Start immediately after cloning
            </label>
          </div>

          <div className="flex justify-end gap-2 pt-2">
            <button
              type="button"
              onClick={() => onOpenChange(false)}
              className="h-9 border border-border px-4 text-[12px] text-muted-foreground hover:text-foreground"
            >
              Cancel
            </button>
            <button
              type="submit"
              className="inline-flex h-9 items-center gap-2 bg-[#263fa9] hover:bg-[#1e3180] px-4 text-[12.5px] font-medium text-white transition-colors"
            >
              <Play className="h-3 w-3 fill-current" />
              <span>
                {replicaCount > 1
                  ? `Replicate ${replicaCount} Sandboxes`
                  : 'Replicate Sandbox'}
              </span>
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
