import type { VMStatus } from '@/lib/types';
import { cn } from '@/lib/utils';

const STATUS: Record<
  VMStatus,
  { label: string; dot: string; className: string }
> = {
  running: {
    label: 'Running',
    dot: 'bg-success',
    className: 'border-success/25 bg-success/10 text-success',
  },
  queued: {
    label: 'Queued',
    dot: 'bg-warning',
    className: 'border-warning/25 bg-warning/10 text-warning',
  },
  paused: {
    label: 'Snapshotted',
    dot: 'bg-info',
    className: 'border-info/25 bg-info/10 text-info',
  },
  stopped: {
    label: 'Stopped',
    dot: 'bg-muted-foreground/60',
    className: 'border-border bg-secondary text-muted-foreground',
  },
  created: {
    label: 'Created',
    dot: 'bg-muted-foreground/60',
    className: 'border-border bg-card text-muted-foreground',
  },
  failed: {
    label: 'Failed',
    dot: 'bg-destructive',
    className: 'border-destructive/25 bg-destructive/10 text-destructive',
  },
};

export function StatusBadge({
  status,
  className,
}: {
  status: VMStatus;
  className?: string;
}) {
  const config = STATUS[status] ?? {
    label: status,
    dot: 'bg-muted-foreground',
    className: 'border-border bg-secondary text-muted-foreground',
  };

  return (
    <span
      className={cn(
        'inline-flex items-center gap-1.5 rounded-md border px-2 py-0.5 text-xs font-medium',
        config.className,
        className,
      )}
    >
      <span className={cn('h-1.5 w-1.5 rounded-full', config.dot)} />
      {config.label}
    </span>
  );
}
