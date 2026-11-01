import type { LucideIcon } from 'lucide-react';
import type { ReactNode } from 'react';

import { Card } from '@/components/ui/card';
import { cn } from '@/lib/utils';

export function StatCard({
  label,
  value,
  suffix,
  icon: Icon,
  detail,
  progress,
  footer,
  className,
}: {
  label: string;
  value: ReactNode;
  suffix?: ReactNode;
  icon?: LucideIcon;
  detail?: ReactNode;
  progress?: number;
  footer?: ReactNode;
  className?: string;
}) {
  return (
    <Card className={cn('p-5', className)}>
      <div className="flex items-start justify-between gap-3">
        <span className="text-[11px] font-medium uppercase tracking-[0.08em] text-muted-foreground">
          {label}
        </span>
        {Icon ? <Icon className="h-4 w-4 text-muted-foreground" /> : null}
      </div>

      <div className="mt-3 flex items-baseline gap-1.5">
        <span className="tabular text-[28px] font-semibold leading-none tracking-tight">
          {value}
        </span>
        {suffix ? (
          <span className="tabular text-xs text-muted-foreground">{suffix}</span>
        ) : null}
      </div>

      {detail ? (
        <div className="mt-2 text-xs text-muted-foreground">{detail}</div>
      ) : null}

      {typeof progress === 'number' ? (
        <div className="mt-3 h-1 w-full overflow-hidden rounded-full bg-secondary">
          <div
            className={cn(
              'h-full rounded-full bg-primary transition-[width] duration-500',
              progress >= 90 && 'bg-destructive',
              progress >= 70 && progress < 90 && 'bg-warning',
            )}
            style={{ width: `${Math.min(100, Math.max(0, progress))}%` }}
          />
        </div>
      ) : null}

      {footer ? <div className="mt-3 text-xs text-muted-foreground">{footer}</div> : null}
    </Card>
  );
}
