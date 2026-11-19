import type { VMStatus } from '@/lib/types';
import { cn } from '@/lib/utils';

const STATUS: Record<
  string,
  { label: string; dot: string; className: string }
> = {
  running: {
    label: 'Running',
    dot: 'bg-emerald-500',
    className: 'border-emerald-500/25 bg-emerald-500/10 text-emerald-400',
  },
  running_startup_script: {
    label: 'Running Startup Script',
    dot: 'bg-cyan-400 animate-pulse',
    className: 'border-cyan-500/30 bg-cyan-500/10 text-cyan-300',
  },
  running_build_script: {
    label: 'Running Build Script',
    dot: 'bg-indigo-400 animate-pulse',
    className: 'border-indigo-500/30 bg-indigo-500/10 text-indigo-300',
  },
  running_build_command: {
    label: 'Running Build Script',
    dot: 'bg-indigo-400 animate-pulse',
    className: 'border-indigo-500/30 bg-indigo-500/10 text-indigo-300',
  },
  booting: {
    label: 'Booting',
    dot: 'bg-amber-400 animate-pulse',
    className: 'border-amber-400/25 bg-amber-400/10 text-amber-300',
  },
  provisioning: {
    label: 'Provisioning',
    dot: 'bg-amber-500 animate-pulse',
    className: 'border-amber-500/25 bg-amber-500/10 text-amber-400',
  },
  queued: {
    label: 'Queued',
    dot: 'bg-amber-500',
    className: 'border-amber-500/25 bg-amber-500/10 text-amber-400',
  },
  paused: {
    label: 'Snapshotted',
    dot: 'bg-sky-500',
    className: 'border-sky-500/25 bg-sky-500/10 text-sky-400',
  },
  snapshotting: {
    label: 'Snapshotting',
    dot: 'bg-sky-400 animate-pulse',
    className: 'border-sky-400/25 bg-sky-400/10 text-sky-300',
  },
  restoring: {
    label: 'Restoring',
    dot: 'bg-sky-400 animate-pulse',
    className: 'border-sky-400/25 bg-sky-400/10 text-sky-300',
  },
  migrating: {
    label: 'Migrating',
    dot: 'bg-purple-400 animate-pulse',
    className: 'border-purple-400/25 bg-purple-400/10 text-purple-300',
  },
  stopping: {
    label: 'Stopping',
    dot: 'bg-zinc-400 animate-pulse',
    className: 'border-zinc-500/25 bg-zinc-500/10 text-zinc-300',
  },
  stopped: {
    label: 'Stopped',
    dot: 'bg-zinc-500',
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

function formatStatusFallback(status: string): string {
  return status
    .replace(/[_-]+/g, ' ')
    .replace(/\b\w/g, (char) => char.toUpperCase());
}

export function StatusBadge({
  status,
  className,
}: {
  status: VMStatus;
  className?: string;
}) {
  const config = STATUS[status] ?? {
    label: formatStatusFallback(status),
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
