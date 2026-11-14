import { useEffect, useMemo, useState } from 'react';
import { ShieldOff, TriangleAlert } from 'lucide-react';

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
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import type { VM } from '@/lib/types';

interface UnprotectVMDialogProps {
  vms: VM[];
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onConfirm: (ids: string[]) => Promise<void> | void;
}

export function UnprotectVMDialog({
  vms,
  open,
  onOpenChange,
  onConfirm,
}: UnprotectVMDialogProps) {
  const [confirmation, setConfirmation] = useState('');

  const isBulk = vms.length > 1;
  const expected = useMemo(
    () => (isBulk ? `DELETE ALL ${vms.length} ITEMS` : (vms[0]?.name ?? '')),
    [isBulk, vms],
  );

  // Clear the typed confirmation whenever the dialog closes so a later
  // request cannot be confirmed with stale input.
  useEffect(() => {
    if (!open) {
      setConfirmation('');
    }
  }, [open]);

  const matches = expected.length > 0 && confirmation.trim() === expected;

  function confirm(event: React.MouseEvent) {
    event.preventDefault();
    if (!matches || vms.length === 0) return;

    const targetIds = vms.map((vm) => vm.id);
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
            <ShieldOff className="h-5 w-5" />
          </div>
          <AlertDialogTitle>
            {isBulk
              ? `Remove deletion protection from ${vms.length} sandboxes?`
              : 'Remove deletion protection?'}
          </AlertDialogTitle>
          <AlertDialogDescription>
            {isBulk
              ? `The ${vms.length} selected sandboxes will no longer be guarded against accidental deletion.`
              : 'This sandbox will no longer be guarded against accidental deletion.'}
          </AlertDialogDescription>
        </AlertDialogHeader>

        <div className="my-2 flex items-start gap-2 border border-destructive/40 bg-destructive/10 p-3 text-xs text-destructive">
          <TriangleAlert className="h-4 w-4 shrink-0 mt-0.5" />
          <div>
            <span className="font-semibold">This weakens a safety guard.</span>{' '}
            Only continue if you are sure these sandboxes may be deleted.
          </div>
        </div>

        {isBulk && (
          <div className="my-2 max-h-48 space-y-1.5 overflow-y-auto rounded-lg border border-border bg-secondary/30 p-2.5 text-xs">
            {vms.map((vm) => (
              <div
                key={vm.id}
                className="flex items-center justify-between gap-2 border-b border-border/40 py-1.5 last:border-b-0 min-w-0"
              >
                <div className="min-w-0 flex-1">
                  <div className="truncate font-medium text-foreground">
                    {vm.name}
                  </div>
                  <div className="truncate font-mono text-[10px] text-muted-foreground">
                    {vm.id}
                  </div>
                </div>
                <span className="shrink-0 text-[10px] uppercase text-destructive">
                  Protected
                </span>
              </div>
            ))}
          </div>
        )}

        <div className="my-2 space-y-2">
          <Label
            htmlFor="unprotect-confirmation"
            className="text-xs font-medium text-foreground"
          >
            {isBulk
              ? 'Type the confirmation phrase to continue'
              : 'Type the sandbox name to continue'}
          </Label>
          <p className="text-xs text-muted-foreground">
            Type{' '}
            <code className="rounded border border-border bg-secondary/50 px-1.5 py-0.5 font-mono text-[11px] text-foreground">
              {expected}
            </code>{' '}
            exactly to confirm.
          </p>
          <Input
            id="unprotect-confirmation"
            value={confirmation}
            onChange={(event) => setConfirmation(event.target.value)}
            placeholder={expected}
            autoComplete="off"
            autoCorrect="off"
            autoCapitalize="off"
            spellCheck={false}
            autoFocus
            aria-invalid={confirmation.length > 0 && !matches}
            className="font-mono"
          />
        </div>

        <AlertDialogFooter className="mt-3 flex flex-row justify-end gap-2">
          <AlertDialogCancel className="border-border bg-background hover:bg-muted text-foreground px-4">
            Cancel
          </AlertDialogCancel>
          <AlertDialogAction
            onClick={confirm}
            disabled={!matches}
            className="bg-destructive hover:bg-destructive/90 text-destructive-foreground font-medium px-4 inline-flex items-center disabled:opacity-40 disabled:cursor-not-allowed"
          >
            Remove protection
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
