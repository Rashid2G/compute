import { useState } from 'react';
import { Loader2, TriangleAlert } from 'lucide-react';

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
    if (!vm || deleting) return;

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
      <AlertDialogContent>
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

        <div className="my-2 space-y-1.5 rounded-lg border bg-secondary/40 p-4 text-xs text-muted-foreground">
          <div className="flex justify-between gap-3 py-0.5">
            <span>Firecracker process</span>
            <span className="font-mono text-foreground">{vm?.pid ? `PID ${vm.pid}` : 'not running'}</span>
          </div>
          <div className="flex justify-between gap-3 py-0.5">
            <span>Network lease</span>
            <span className="font-mono text-foreground">
              {vm?.ip_address || 'none'} &middot; {vm?.tap_device || 'no tap'}
            </span>
          </div>
          <div className="flex justify-between gap-3 py-0.5">
            <span>Overlay disk</span>
            <span className="truncate font-mono text-foreground">{vm?.disk_path || 'none'}</span>
          </div>
        </div>

        <AlertDialogFooter>
          <AlertDialogCancel disabled={deleting}>Cancel</AlertDialogCancel>
          <AlertDialogAction onClick={confirm} disabled={deleting}>
            {deleting ? <Loader2 className="mr-1.5 h-4 w-4 animate-spin" /> : null}
            {deleting ? 'Deleting…' : 'Delete instance'}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
