import { TriangleAlert } from 'lucide-react';

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

interface BulkDeleteVMDialogProps {
  vms: VM[];
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onConfirm: (ids: string[]) => Promise<void> | void;
}

export function BulkDeleteVMDialog({
  vms,
  open,
  onOpenChange,
  onConfirm,
}: BulkDeleteVMDialogProps) {
  function confirm(event: React.MouseEvent) {
    event.preventDefault();
    if (vms.length === 0) return;

    const targetIds = vms.map((v) => v.id);
    onOpenChange(false);
    void Promise.resolve(onConfirm(targetIds)).catch(() => {
      // The caller reports failures via toast notifications.
    });
  }

  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent className="max-w-lg w-[calc(100%-2rem)] overflow-hidden">
        <AlertDialogHeader>
          <div className="mb-1 flex h-10 w-10 items-center justify-center rounded-lg border border-destructive/25 bg-destructive/10 text-destructive">
            <TriangleAlert className="h-5 w-5" />
          </div>
          <AlertDialogTitle>
            Delete {vms.length} {vms.length === 1 ? 'instance' : 'instances'}?
          </AlertDialogTitle>
          <AlertDialogDescription>
            The {vms.length} selected sandboxes will be terminated and their
            resources released. This cannot be undone.
          </AlertDialogDescription>
        </AlertDialogHeader>

        <div className="my-2 max-h-48 space-y-1.5 overflow-y-auto rounded-lg border border-border bg-secondary/30 p-2.5 text-xs">
          {vms.map((vm) => (
            <div
              key={vm.id}
              className="flex items-center justify-between gap-2 border-b border-border/40 py-1.5 last:border-b-0 min-w-0"
            >
              <div className="min-w-0 flex-1">
                <div className="truncate font-medium text-foreground">{vm.name}</div>
                <div className="truncate font-mono text-[10px] text-muted-foreground">
                  {vm.id} {vm.ip_address ? `· ${vm.ip_address}` : ''}
                </div>
              </div>
              <span className="shrink-0 font-mono text-[10px] uppercase text-muted-foreground">
                {vm.status}
              </span>
            </div>
          ))}
        </div>

        <AlertDialogFooter className="mt-3 flex flex-row justify-end gap-2">
          <AlertDialogCancel className="border-border bg-background hover:bg-muted text-foreground px-4">
            Cancel
          </AlertDialogCancel>
          <AlertDialogAction
            onClick={confirm}
            className="bg-destructive hover:bg-destructive/90 text-destructive-foreground font-medium px-4 inline-flex items-center"
          >
            {vms.length === 1 ? 'Delete 1 instance' : `Delete ${vms.length} instances`}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
