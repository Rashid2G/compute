import {
  Cpu,
  Database,
  Gauge,
  HardDrive,
  List,
  Lock,
  LogOut,
  Moon,
  PanelLeftClose,
  PanelLeftOpen,
  Settings,
  SlidersHorizontal,
  Sun,
  Zap,
  type LucideIcon,
} from 'lucide-react';

import type { PageId, SettingsTabId } from '@/lib/navigation';
import type { AuthUser, StatsResult, WorkerNodeRecord } from '@/lib/types';
import { cn } from '@/lib/utils';

interface SidebarProps {
  page: PageId;
  onNavigate: (page: PageId) => void;
  onNavigateSettingsTab?: (tab: SettingsTabId) => void;
  stats: StatsResult | null;
  workers?: WorkerNodeRecord[];
  daemonOnline: boolean;
  theme: 'light' | 'dark';
  onToggleTheme: () => void;
  onSetTheme?: (theme: 'light' | 'dark') => void;
  onOpenCommandPalette?: () => void;
  user?: AuthUser | null;
  authEnabled?: boolean;
  onOpenLogin?: () => void;
  onLogout?: () => void;
  collapsed?: boolean;
  onToggleCollapse?: () => void;
}

function getUserInitials(name: string): string {
  const parts = name.trim().split(/[\s._-]+/).filter(Boolean);
  if (parts.length === 0) return 'AD';
  if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase();
  return `${parts[0][0]}${parts[1][0]}`.toUpperCase();
}

export function Sidebar({
  page,
  onNavigate,
  onNavigateSettingsTab,
  stats,
  theme,
  onToggleTheme,
  user,
  authEnabled = true,
  onOpenLogin,
  onLogout,
  collapsed = false,
  onToggleCollapse,
}: SidebarProps) {
  const totalVMs = stats?.total_vms ?? 0;
  const runningVMs = stats?.running_vms ?? 0;
  const isAdmin = !authEnabled || user?.role === 'admin';

  const navItems: Array<{
    id: PageId;
    label: string;
    icon: LucideIcon;
    count?: string | number;
  }> = [
    {
      id: 'instances',
      label: 'Sandboxes',
      icon: List,
      count: totalVMs > 0 ? `${runningVMs}/${totalVMs}` : undefined,
    },
    {
      id: 'images',
      label: 'Snapshots',
      icon: Database,
      count:
        stats && stats.snapshotted_vms > 0 ? stats.snapshotted_vms : undefined,
    },
    {
      id: 'storage',
      label: 'Volumes',
      icon: HardDrive,
    },
    {
      id: 'jobs',
      label: 'CI Jobs',
      icon: Zap,
    },
    {
      id: 'workers',
      label: 'Workers',
      icon: Cpu,
    },
    ...(isAdmin
      ? [
          {
            id: 'resources' as PageId,
            label: 'Resources',
            icon: Gauge,
          },
        ]
      : []),
    {
      id: 'settings',
      label: 'Settings',
      icon: Settings,
    },
  ];

  const displayName = user?.display_name || user?.username || 'Administrator';
  const roleLabel = user?.role || 'admin';
  const subtitle = user?.email ? user.email : roleLabel;
  const initials = getUserInitials(displayName);

  const openQuickSettings = () => {
    if (onNavigateSettingsTab) {
      onNavigateSettingsTab('appearance');
    } else {
      onNavigate('settings');
    }
  };

  return (
    <div
      className={cn(
        'flex h-full flex-col justify-between bg-background text-foreground select-none py-4',
        collapsed ? 'px-2' : 'px-3.5',
      )}
    >
      <div className="space-y-4 overflow-y-auto">
        {/* Clean Brand Header & Collapse Toggle */}
        <div
          className={cn(
            'flex items-center border-b border-border pb-3',
            collapsed ? 'justify-center' : 'justify-between px-0.5',
          )}
        >
          {!collapsed && (
            <button
              type="button"
              onClick={() => onNavigate('instances')}
              className="flex items-center gap-2 text-left focus:outline-none group"
            >
              <svg
                viewBox="0 0 32 32"
                className="h-6 w-6 shrink-0 text-foreground"
                fill="none"
                stroke="currentColor"
                strokeWidth="2"
                aria-hidden="true"
              >
                <polygon points="16,4 27,10.5 27,21.5 16,28 5,21.5 5,10.5" />
                <polyline points="5,10.5 16,17 27,10.5" />
                <line x1="16" y1="17" x2="16" y2="28" />
              </svg>
              <span className="font-display text-[20px] font-medium tracking-tight text-foreground leading-none">
                Compute
              </span>
            </button>
          )}

          {onToggleCollapse ? (
            <button
              type="button"
              onClick={onToggleCollapse}
              title={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
              aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
              className="flex h-7 w-7 items-center justify-center border border-transparent text-muted-foreground hover:border-border hover:bg-muted/50 hover:text-foreground transition-colors"
            >
              {collapsed ? (
                <PanelLeftOpen className="h-4 w-4" />
              ) : (
                <PanelLeftClose className="h-4 w-4" />
              )}
            </button>
          ) : (
            collapsed && (
              <button
                type="button"
                onClick={() => onNavigate('instances')}
                title="Compute"
                className="flex items-center justify-center focus:outline-none"
              >
                <svg
                  viewBox="0 0 32 32"
                  className="h-6 w-6 shrink-0 text-foreground"
                  fill="none"
                  stroke="currentColor"
                  strokeWidth="2"
                  aria-hidden="true"
                >
                  <polygon points="16,4 27,10.5 27,21.5 16,28 5,21.5 5,10.5" />
                  <polyline points="5,10.5 16,17 27,10.5" />
                  <line x1="16" y1="17" x2="16" y2="28" />
                </svg>
              </button>
            )
          )}
        </div>

        {/* Streamlined Core Navigation List */}
        <nav className="space-y-0.5" aria-label="Main Navigation">
          {navItems.map((item) => {
            const Icon = item.icon;
            const active = item.id === page;
            return (
              <button
                key={item.id}
                type="button"
                onClick={() => onNavigate(item.id)}
                title={collapsed ? item.label : undefined}
                aria-label={item.label}
                className={cn(
                  'flex h-8 w-full items-center text-[13px] font-normal transition-colors',
                  collapsed ? 'justify-center px-1' : 'justify-between px-2.5',
                  active
                    ? 'compute-selected text-foreground font-medium'
                    : 'text-muted-foreground hover:bg-muted/50 hover:text-foreground',
                )}
              >
                <span
                  className={cn(
                    'flex items-center truncate',
                    collapsed ? 'justify-center' : 'gap-2.5',
                  )}
                >
                  <Icon
                    className={cn(
                      'h-4 w-4 shrink-0',
                      active ? 'text-foreground' : 'text-[var(--icon-quiet)]',
                    )}
                  />
                  {!collapsed && <span className="truncate">{item.label}</span>}
                </span>
                {!collapsed && item.count !== undefined && (
                  <span
                    className={cn(
                      'font-mono text-[11px] tabular',
                      active ? 'text-foreground/90' : 'text-muted-foreground',
                    )}
                  >
                    {item.count}
                  </span>
                )}
              </button>
            );
          })}
        </nav>
      </div>

      {/* Cohesive Footer User Box */}
      <div className="pt-3 border-t border-border mt-3">
        {user || !authEnabled ? (
          collapsed ? (
            <div className="flex flex-col items-center gap-1.5">
              <div
                title={`${displayName} (${roleLabel}${user?.email ? ` · ${user.email}` : ''})`}
                className="flex h-8 w-8 items-center justify-center border border-border bg-muted/40 font-mono text-[11px] font-medium text-foreground"
              >
                {initials}
              </div>
              <button
                type="button"
                onClick={onToggleTheme}
                title={
                  theme === 'dark'
                    ? 'Switch to Light mode'
                    : 'Switch to Dark mode'
                }
                aria-label="Toggle theme"
                className="flex h-7 w-7 items-center justify-center border border-border bg-background text-muted-foreground hover:text-foreground transition-colors"
              >
                {theme === 'dark' ? (
                  <Sun className="h-3.5 w-3.5" />
                ) : (
                  <Moon className="h-3.5 w-3.5" />
                )}
              </button>
              <button
                type="button"
                onClick={openQuickSettings}
                title="Appearance & Settings"
                aria-label="Open Settings"
                className="flex h-7 w-7 items-center justify-center border border-border bg-background text-muted-foreground hover:text-foreground transition-colors"
              >
                <SlidersHorizontal className="h-3.5 w-3.5" />
              </button>
              {user && onLogout && (
                <button
                  type="button"
                  onClick={onLogout}
                  title="Sign out"
                  aria-label="Sign out"
                  className="flex h-7 w-7 items-center justify-center border border-border bg-background text-muted-foreground hover:text-destructive transition-colors"
                >
                  <LogOut className="h-3.5 w-3.5" />
                </button>
              )}
            </div>
          ) : (
            <div className="border border-border bg-muted/30 p-2.5 space-y-2.5">
              <div className="flex items-center gap-2.5 min-w-0">
                <div className="flex h-8 w-8 shrink-0 items-center justify-center border border-border bg-background font-mono text-[11px] font-medium text-foreground">
                  {initials}
                </div>
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-1.5 min-w-0">
                    <span className="truncate text-[12.5px] font-medium text-foreground">
                      {displayName}
                    </span>
                    <span className="shrink-0 border border-border bg-background px-1 py-0.5 font-mono text-[9.5px] uppercase text-muted-foreground">
                      {roleLabel}
                    </span>
                  </div>
                  <div className="truncate font-mono text-[10.5px] text-muted-foreground">
                    {subtitle}
                  </div>
                </div>
              </div>

              <div className="flex items-center justify-between gap-1 border-t border-border pt-2">
                <div className="flex items-center gap-1">
                  <button
                    type="button"
                    onClick={onToggleTheme}
                    title={
                      theme === 'dark'
                        ? 'Switch to Light mode'
                        : 'Switch to Dark mode'
                    }
                    aria-label="Toggle theme"
                    className="flex h-7 w-7 items-center justify-center border border-border bg-background text-muted-foreground hover:text-foreground transition-colors"
                  >
                    {theme === 'dark' ? (
                      <Sun className="h-3.5 w-3.5" />
                    ) : (
                      <Moon className="h-3.5 w-3.5" />
                    )}
                  </button>
                  <button
                    type="button"
                    onClick={openQuickSettings}
                    title="Appearance & Settings"
                    aria-label="Open Settings"
                    className="flex h-7 w-7 items-center justify-center border border-border bg-background text-muted-foreground hover:text-foreground transition-colors"
                  >
                    <SlidersHorizontal className="h-3.5 w-3.5" />
                  </button>
                </div>

                {user && onLogout && (
                  <button
                    type="button"
                    onClick={onLogout}
                    title="Sign out"
                    aria-label="Sign out"
                    className="flex h-7 items-center gap-1 border border-border bg-background px-2 text-[11px] text-muted-foreground hover:text-destructive transition-colors"
                  >
                    <LogOut className="h-3 w-3" />
                    <span>Sign out</span>
                  </button>
                )}
              </div>
            </div>
          )
        ) : (
          onOpenLogin && (
            <div className="space-y-2">
              <button
                type="button"
                onClick={onOpenLogin}
                title="Sign in"
                className="flex h-8 w-full items-center justify-center gap-2 border border-border bg-background px-2 text-xs font-medium text-foreground hover:bg-muted transition-colors"
              >
                <Lock className="h-3.5 w-3.5 text-muted-foreground" />
                {!collapsed && <span>Sign in</span>}
              </button>
            </div>
          )
        )}
      </div>
    </div>
  );
}
