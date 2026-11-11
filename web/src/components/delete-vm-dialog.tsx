import { useState } from 'react';
import { Loader2, Shield, TriangleAlert } from 'lucide-react';

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog';
import type { VM } from '@/lib/types';

export function DeleteVMDialog({
  vm,
  open,
  onOpenChange,
  onConfirm,
}: {
  vm: VM | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onConfirm: (id: string) => Promise<void>;
}) {
  const [deleting, setDeleting] = useState(false);

  async function confirm(event: React.MouseEvent) {
    event.preventDefault();
    if (!vm || deleting || vm.protected) return;

    setDeleting(true);
    try {
      await onConfirm(vm.id);
      onOpenChange(false);
    } catch {
      // The caller reports the failure; keep the dialog available for retry.
    } finally {
      setDeleting(false);
    }
  }

  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent className="max-w-lg w-[calc(100%-2rem)] overflow-hidden">
        <AlertDialogHeader>
          <div className="mb-1 flex h-10 w-10 items-center justify-center rounded-lg border border-destructive/25 bg-destructive/10 text-destructive">
            <TriangleAlert className="h-5 w-5" />
          </div>
          <AlertDialogTitle>Delete this instance?</AlertDialogTitle>
          <AlertDialogDescription>
            <span className="font-semibold text-foreground">{vm?.name}</span> will
            be terminated and its resources released. This cannot be undone.
          </AlertDialogDescription>
        </AlertDialogHeader>

        {vm?.protected && (
          <div className="my-2 flex items-start gap-2 border border-destructive/40 bg-destructive/10 p-3 text-xs text-destructive">
            <Shield className="h-4 w-4 shrink-0 mt-0.5" />
            <div>
              <span className="font-semibold">Deletion Protected:</span> This sandbox has deletion protection enabled and cannot be deleted. Unprotect it first to remove it.
            </div>
          </div>
        )}

        <div className="my-2 space-y-2 rounded-lg border border-border bg-secondary/40 p-3.5 text-xs text-muted-foreground w-full min-w-0 overflow-hidden">
          <div className="flex items-center justify-between gap-3 min-w-0">
            <span className="shrink-0">Firecracker process</span>
            <span className="font-mono text-foreground">{vm?.pid ? `PID ${vm.pid}` : 'not running'}</span>
          </div>
          <div className="flex items-center justify-between gap-3 min-w-0">
            <span className="shrink-0">Network lease</span>
            <span className="font-mono text-foreground truncate text-right">
              {vm?.ip_address || 'none'} &middot; {vm?.tap_device || 'no tap'}
            </span>
          </div>
          <div className="flex items-center justify-between gap-3 min-w-0">
            <span className="shrink-0">Overlay disk</span>
            <span className="min-w-0 truncate font-mono text-foreground text-right" title={vm?.disk_path || 'none'}>
              {vm?.disk_path || 'none'}
            </span>
          </div>
        </div>

        <AlertDialogFooter className="mt-3 flex flex-row justify-end gap-2">
          <AlertDialogCancel disabled={deleting} className="border-border bg-background hover:bg-muted text-foreground px-4">
            Cancel
          </AlertDialogCancel>
          <AlertDialogAction
            onClick={confirm}
            disabled={deleting || !!vm?.protected}
            className="bg-destructive hover:bg-destructive/90 text-destructive-foreground font-medium px-4 inline-flex items-center disabled:opacity-40 disabled:cursor-not-allowed"
          >
            {deleting ? <Loader2 className="mr-1.5 h-4 w-4 animate-spin" /> : null}
            {deleting ? 'Deleting…' : 'Delete instance'}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
