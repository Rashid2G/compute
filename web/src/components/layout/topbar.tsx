import { Key, Menu, Plus } from 'lucide-react';

import { Button } from '@/components/ui/button';
import type { PageMeta } from '@/lib/navigation';
import { cn } from '@/lib/utils';

export function Topbar({
  page,
  onOpenMenu,
  daemonOnline,
  latency,
  wsConnected = false,
  onNew,
  onOpenKeys,
}: {
  page: PageMeta;
  onOpenMenu: () => void;
  daemonOnline: boolean;
  latency: number | null;
  wsConnected?: boolean;
  onNew: () => void;
  onOpenKeys?: () => void;
}) {
  return (
    <header className="sticky top-0 z-30 flex min-h-16 items-center gap-3 border-b bg-background/95 px-4 backdrop-blur sm:px-6 lg:px-8">
      <Button
        type="button"
        variant="ghost"
        size="icon-sm"
        className="lg:hidden"
        onClick={onOpenMenu}
        aria-label="Open navigation"
      >
        <Menu />
      </Button>

      <div className="min-w-0 flex-1">
        <h1 className="truncate text-sm font-semibold leading-tight">{page.label}</h1>
        <p className="mt-0.5 hidden truncate text-xs text-muted-foreground sm:block">
          {page.description}
        </p>
      </div>

      <div className="hidden items-center gap-2 rounded-md border bg-card px-2.5 py-1.5 md:flex">
        <span
          className={cn(
            'h-2 w-2 rounded-full',
            daemonOnline ? 'bg-emerald-500 shadow-[0_0_8px_rgba(16,185,129,0.6)] animate-pulse' : 'bg-destructive',
          )}
        />
        <span className="text-xs font-medium text-muted-foreground">
          {daemonOnline ? (wsConnected ? 'Live WebSocket' : 'Daemon online') : 'Daemon offline'}
        </span>
        {daemonOnline && latency !== null ? (
          <span className="border-l pl-2 font-mono text-[11px] text-muted-foreground">
            {latency} ms
          </span>
        ) : null}
      </div>

      {onOpenKeys ? (
        <Button type="button" variant="outline" size="sm" onClick={onOpenKeys} title="Manage Account SSH Keys">
          <Key className="h-4 w-4" />
          <span className="hidden sm:inline">SSH Keys</span>
        </Button>
      ) : null}

      <Button type="button" size="sm" onClick={onNew}>
        <Plus />
        <span className="hidden sm:inline">New instance</span>
      </Button>
    </header>
  );
}
