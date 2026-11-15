import { useEffect, useState } from 'react';
import { Camera, X } from 'lucide-react';
import { toast } from 'sonner';

import { api, errorMessage } from '@/lib/api';
import type { VM } from '@/lib/types';

interface CreateSnapshotDialogProps {
  vm: VM | null;
  vms?: VM[];
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCreated?: () => void;
}

function shortTimestamp(): string {
  const now = new Date();
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${pad(now.getMonth() + 1)}${pad(now.getDate())}-${pad(now.getHours())}${pad(now.getMinutes())}`;
}

export function CreateSnapshotDialog({
  vm,
  vms = [],
  open,
  onOpenChange,
  onCreated,
}: CreateSnapshotDialogProps) {
  const [sourceVmId, setSourceVmId] = useState('');
  const [snapName, setSnapName] = useState('');
  const [nameTouched, setNameTouched] = useState(false);
  const [description, setDescription] = useState('');

  useEffect(() => {
    if (!open) return;
    const initialVm = vm || vms[0] || null;
    setSourceVmId(initialVm?.id || '');
    const prefix = initialVm?.name || 'sandbox';
    setSnapName(`${prefix}-snap-${shortTimestamp()}`);
    setNameTouched(false);
    setDescription('');
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, vm?.id]);

  if (!open) return null;

  const selectedSource = vm || vms.find((v) => v.id === sourceVmId) || null;
  const cleanName = snapName.trim();
  const nameEmpty = cleanName.length === 0;
  const missingSource = !vm && !sourceVmId;
  const submitDisabled = nameEmpty || missingSource;

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    const targetId = vm?.id || sourceVmId;
    if (!cleanName) {
      setNameTouched(true);
      return;
    }
    if (!targetId) return;

    const cleanDesc = description.trim();
    onOpenChange(false);

    const toastID = toast.loading(`Creating snapshot "${cleanName}"…`);
    void api
      .createSnapshot(targetId, cleanName, cleanDesc)
      .then(() => {
        toast.success(`Snapshot "${cleanName}" created`, { id: toastID });
        onCreated?.();
      })
      .catch((err) => {
        toast.error(`Snapshot failed: ${errorMessage(err)}`, { id: toastID });
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
        aria-labelledby="create-snapshot-title"
        className="w-full max-w-[440px] border border-border bg-background p-6 text-foreground my-auto"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between mb-4">
          <div className="flex items-center gap-2">
            <Camera className="h-4 w-4 text-[#5e7ff5]" />
            <h2
              id="create-snapshot-title"
              className="text-[15px] font-medium text-foreground"
            >
              Create Sandbox Snapshot
            </h2>
          </div>
          <button
            type="button"
            onClick={() => onOpenChange(false)}
            className="text-muted-foreground hover:text-foreground transition-colors"
            aria-label="Close snapshot modal"
          >
            <X className="h-4 w-4" />
          </button>
        </div>

        <form onSubmit={handleSubmit} className="space-y-4">
          {vm ? (
            <div className="border border-border bg-muted/20 px-3 py-2 text-xs">
              <div className="text-muted-foreground">Source Sandbox</div>
              <div className="font-mono font-medium text-foreground mt-0.5">
                {vm.name} ({vm.status})
              </div>
            </div>
          ) : (
            <div className="space-y-1">
              <label className="block text-[12px] text-muted-foreground">
                Source Sandbox
              </label>
              <select
                aria-label="Snapshot source sandbox"
                value={sourceVmId}
                onChange={(e) => {
                  const nextId = e.target.value;
                  setSourceVmId(nextId);
                  const found = vms.find((item) => item.id === nextId);
                  if (found) {
                    setSnapName(`${found.name}-snap-${shortTimestamp()}`);
                  }
                }}
                required
                className="h-9 w-full border border-border bg-background px-2.5 text-[12px] text-foreground focus:border-[#263fa9] focus:outline-none"
              >
                <option value="">Select a sandbox…</option>
                {vms.map((item) => (
                  <option key={item.id} value={item.id}>
                    {item.name} ({item.status})
                  </option>
                ))}
              </select>
            </div>
          )}

          <div className="space-y-1">
            <label className="block text-[12px] text-muted-foreground">
              Snapshot Name
            </label>
            <input
              type="text"
              aria-label="Snapshot name"
              aria-invalid={nameTouched && nameEmpty}
              value={snapName}
              onChange={(e) => {
                setSnapName(e.target.value);
                setNameTouched(true);
              }}
              placeholder={
                selectedSource
                  ? `${selectedSource.name}-snap`
                  : 'ubuntu-24.04-warm-snap'
              }
              required
              className="h-9 w-full border border-border bg-background px-3 font-mono text-[12px] text-foreground focus:border-[#263fa9] focus:outline-none"
            />
            {nameTouched && nameEmpty && (
              <p role="alert" className="text-[11px] text-destructive">
                Snapshot name is required.
              </p>
            )}
          </div>

          <div className="space-y-1">
            <label className="block text-[12px] text-muted-foreground">
              Description (optional)
            </label>
            <input
              type="text"
              aria-label="Snapshot description"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="Pre-seeded runtime checkpoint"
              className="h-9 w-full border border-border bg-background px-3 text-[12px] text-foreground placeholder:text-muted-foreground focus:border-[#263fa9] focus:outline-none"
            />
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
              disabled={submitDisabled}
              className="inline-flex h-9 items-center gap-2 bg-[#263fa9] hover:bg-[#1e3180] px-4 text-[12.5px] font-medium text-white transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
            >
              <Camera className="h-3.5 w-3.5" />
              <span>Create Snapshot</span>
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
