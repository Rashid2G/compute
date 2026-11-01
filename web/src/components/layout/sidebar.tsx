import { PAGES, type PageId } from '@/lib/navigation';
import type { StatsResult } from '@/lib/types';
import { cn } from '@/lib/utils';

function BrandMark() {
  return (
    <span className="flex h-8 w-8 items-center justify-center rounded-md bg-primary text-primary-foreground">
      <svg
        viewBox="0 0 24 24"
        className="h-5 w-5"
        fill="none"
        stroke="currentColor"
        strokeWidth="2.2"
        strokeLinecap="round"
        strokeLinejoin="round"
        aria-hidden="true"
      >
        <path d="M4 18V7l8 5 8-5v11" />
      </svg>
    </span>
  );
}

export function Sidebar({
  page,
  onNavigate,
  stats,
  daemonOnline,
  className,
}: {
  page: PageId;
  onNavigate: (page: PageId) => void;
  stats: StatsResult | null;
  daemonOnline: boolean;
  theme?: 'light' | 'dark';
  onToggleTheme?: () => void;
  className?: string;
}) {
  const badges: Partial<Record<PageId, string | number>> = {
    instances: stats?.total_vms ?? 0,
    network: stats?.allocated_ips ?? 0,
  };

  return (
    <div className={cn('flex h-full flex-col bg-background', className)}>
      <div className="flex h-16 shrink-0 items-center gap-2.5 border-b px-5">
        <BrandMark />
        <div className="min-w-0">
          <div className="truncate text-sm font-semibold leading-tight">Compute</div>
          <div className="truncate text-[11px] text-muted-foreground">Fleet control</div>
        </div>
      </div>

      <div className="px-3 py-4">
        <div className="rounded-md border bg-card px-3 py-2.5">
          <div className="flex items-center justify-between">
            <span className="text-[10px] font-medium uppercase tracking-[0.08em] text-muted-foreground">
              Node
            </span>
            <span className="rounded border px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground">
              KVM
            </span>
          </div>
          <div className="mt-1.5 flex items-center gap-2">
            <span
              className={cn(
                'h-1.5 w-1.5 rounded-full',
                daemonOnline ? 'bg-success' : 'bg-destructive',
              )}
            />
            <span className="truncate font-mono text-xs">
              local-firecracker
            </span>
          </div>
        </div>
      </div>

      <nav className="flex-1 space-y-0.5 px-3">
        <div className="px-2 pb-1.5 text-[10px] font-medium uppercase tracking-[0.08em] text-muted-foreground">
          Workspace
        </div>
        {PAGES.map((item) => {
          const Icon = item.icon;
          const active = page === item.id;
          const badge = badges[item.id];

          return (
            <button
              key={item.id}
              type="button"
              onClick={() => onNavigate(item.id)}
              className={cn(
                'flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-sm transition-colors',
                active
                  ? 'bg-secondary font-medium text-foreground'
                  : 'text-muted-foreground hover:bg-secondary/60 hover:text-foreground',
              )}
              aria-current={active ? 'page' : undefined}
            >
              <Icon
                className={cn(
                  'h-4 w-4 shrink-0',
                  active ? 'text-primary' : 'text-muted-foreground',
                )}
              />
              <span className="flex-1 text-left">{item.label}</span>
              {badge !== undefined ? (
                <span className="tabular rounded border bg-card px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground">
                  {badge}
                </span>
              ) : null}
            </button>
          );
        })}
      </nav>

      <div className="border-t p-3">
        <div className="flex items-center justify-between px-2 font-mono text-[10px] text-muted-foreground">
          <span className="flex items-center gap-1.5">
            <span className={cn('h-1.5 w-1.5 rounded-full', daemonOnline ? 'bg-emerald-500' : 'bg-destructive')} />
            Firecracker v1.9+
          </span>
          <span>v2.0</span>
        </div>
      </div>
    </div>
  );
}
