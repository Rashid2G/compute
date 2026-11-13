import { useState } from 'react';
import {
  BookOpen,
  Braces,
  Building2,
  ChevronDown,
  Cpu,
  Database,
  ExternalLink,
  HardDrive,
  Headphones,
  KeyRound,
  LayoutDashboard,
  List,
  Moon,
  Network,
  Radio,
  Sliders,
  Sun,
  Terminal,
  Zap,
  Lock,
  LogOut,
  type LucideIcon,
} from 'lucide-react';

import type { PageId } from '@/lib/navigation';
import type { AuthUser, StatsResult, WorkerNodeRecord } from '@/lib/types';
import { cn } from '@/lib/utils';

interface SidebarProps {
  page: PageId;
  onNavigate: (page: PageId) => void;
  stats: StatsResult | null;
  workers?: WorkerNodeRecord[];
  daemonOnline: boolean;
  theme: 'light' | 'dark';
  onToggleTheme: () => void;
  onSetTheme?: (theme: 'light' | 'dark') => void;
  onOpenCommandPalette?: () => void;
  user?: AuthUser | null;
  onOpenLogin?: () => void;
  onLogout?: () => void;
}

const ORG_CONTEXTS = [
  {
    id: 'personal',
    label: 'Personal',
    sandboxBill: 'Personal',
    cliBill: 'Personal',
  },
  {
    id: 'default-cluster',
    label: 'Default KVM Cluster',
    sandboxBill: 'Default KVM Cluster',
    cliBill: 'Default KVM Cluster',
  },
];

export function Sidebar({
  page,
  onNavigate,
  stats,
  workers = [],
  daemonOnline,
  theme,
  onToggleTheme,
  onSetTheme,
  user,
  onOpenLogin,
  onLogout,
}: SidebarProps) {
  const [selectedOrgId, setSelectedOrgId] = useState('personal');
  const currentOrg =
    ORG_CONTEXTS.find((o) => o.id === selectedOrgId) ?? ORG_CONTEXTS[0];

  const totalVMs = stats?.total_vms ?? 0;
  const runningVMs = stats?.running_vms ?? 0;

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
      id: 'environment',
      label: 'Environment',
      icon: Sliders,
    },
    {
      id: 'access',
      label: 'API keys',
      icon: KeyRound,
    },
    {
      id: 'events',
      label: 'Webhooks & OTel',
      icon: Radio,
    },
    {
      id: 'api',
      label: 'Agents & Proxying',
      icon: Braces,
    },
    {
      id: 'jobs',
      label: 'CI on Compute VMs',
      icon: Zap,
    },
    {
      id: 'overview',
      label: 'Overview',
      icon: LayoutDashboard,
    },
    {
      id: 'workers',
      label: 'Worker Nodes',
      icon: Cpu,
    },
    {
      id: 'storage',
      label: 'Persistent Volumes',
      icon: HardDrive,
    },
    {
      id: 'network',
      label: 'Network & NAT',
      icon: Network,
    },
    {
      id: 'console',
      label: 'Console & SSH',
      icon: Terminal,
    },
    {
      id: 'settings',
      label: 'Organization',
      icon: Building2,
    },
  ];

  const handleLight = () => {
    if (onSetTheme) onSetTheme('light');
    else if (theme !== 'light') onToggleTheme();
  };

  const handleDark = () => {
    if (onSetTheme) onSetTheme('dark');
    else if (theme !== 'dark') onToggleTheme();
  };

  return (
    <div className="flex h-full flex-col justify-between bg-background text-foreground select-none px-3.5 py-4">
      <div className="space-y-3.5 overflow-y-auto">
        {/* Top Brand Header matching boat.dev/dashboard */}
        <div className="flex items-center justify-between px-0.5">
          <button
            type="button"
            onClick={() => onNavigate('instances')}
            className="flex items-center gap-2 text-left focus:outline-none group"
          >
            {/* Compute geometric mark */}
            <svg
              viewBox="0 0 32 32"
              className="h-6 w-6 shrink-0 text-foreground"
              fill="currentColor"
              aria-hidden="true"
            >
              <path d="M15 5L6 21h9V5zm2 4v12h8L17 9zM5 23l3 4h16l3-4H5z" />
            </svg>
            <span className="font-display text-[21px] font-medium tracking-tight text-foreground leading-none">
              Compute
            </span>
          </button>
        </div>

        {/* Organization / Cluster Selector matching boat.dev/dashboard */}
        <div className="space-y-2 border-b border-border pb-3">
          <div className="relative">
            <select
              aria-label="Organization Context"
              value={selectedOrgId}
              onChange={(e) => setSelectedOrgId(e.target.value)}
              className="h-8 w-full appearance-none border border-border bg-background px-2.5 pr-7 text-[12px] font-normal text-foreground focus:border-[#263fa9] focus:outline-none cursor-pointer"
            >
              {ORG_CONTEXTS.map((org) => (
                <option
                  key={org.id}
                  value={org.id}
                  className="bg-background text-foreground"
                >
                  {org.label}
                </option>
              ))}
            </select>
            <ChevronDown className="pointer-events-none absolute right-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
          </div>

          <div className="space-y-0.5 px-0.5 text-[11px] leading-[1.4] text-muted-foreground">
            <p>
              Sandboxes you create here bill{' '}
              <span className="text-foreground font-medium">
                {currentOrg.sandboxBill}
              </span>
              .
            </p>
            <p>
              CLI and API keys bill{' '}
              <span className="text-foreground font-medium">
                {currentOrg.cliBill}
              </span>
              .{' '}
              <button
                type="button"
                onClick={() => onNavigate('settings')}
                className="underline underline-offset-2 text-muted-foreground hover:text-foreground"
              >
                Change
              </button>
            </p>
          </div>
        </div>

        {/* Primary Navigation List matching boat.dev/dashboard */}
        <nav className="space-y-0.5" aria-label="Main Navigation">
          {navItems.map((item) => {
            const Icon = item.icon;
            const active = item.id === page;
            return (
              <button
                key={item.id}
                type="button"
                onClick={() => onNavigate(item.id)}
                className={cn(
                  'flex h-7 w-full items-center justify-between px-2 text-[12.5px] font-normal transition-colors',
                  active
                    ? 'boat-selected text-foreground font-medium'
                    : 'text-muted-foreground hover:bg-muted/50 hover:text-foreground',
                )}
              >
                <span className="flex items-center gap-2.5 truncate">
                  <Icon
                    className={cn(
                      'h-3.5 w-3.5 shrink-0',
                      active ? 'text-foreground' : 'text-[var(--icon-quiet)]',
                    )}
                  />
                  <span className="truncate">{item.label}</span>
                </span>
                {item.count !== undefined && (
                  <span
                    className={cn(
                      'font-mono text-[10.5px] tabular',
                      active ? 'text-foreground/90' : 'text-muted-foreground',
                    )}
                  >
                    {item.count}
                  </span>
                )}
              </button>
            );
          })}

          <div className="space-y-0.5">
            <button
              type="button"
              onClick={() => onNavigate('api')}
              className="flex h-7 w-full items-center justify-between px-2 text-[12.5px] text-muted-foreground hover:bg-muted/50 hover:text-foreground transition-colors"
            >
              <span className="flex items-center gap-2.5">
                <BookOpen className="h-3.5 w-3.5 text-[var(--icon-quiet)]" />
                <span>Docs</span>
              </span>
              <ExternalLink className="h-3 w-3 text-muted-foreground/60" />
            </button>
            <button
              type="button"
              onClick={() => onNavigate('events')}
              className="flex h-7 w-full items-center justify-between px-2 text-[12.5px] text-muted-foreground hover:bg-muted/50 hover:text-foreground transition-colors"
            >
              <span className="flex items-center gap-2.5">
                <Headphones className="h-3.5 w-3.5 text-[var(--icon-quiet)]" />
                <span>Support</span>
              </span>
              <ExternalLink className="h-3 w-3 text-muted-foreground/60" />
            </button>
          </div>
        </nav>
      </div>

      {/* User Session & Authentication Status */}
      <div className="pt-2 border-t border-border mt-2">
        {user ? (
          <div className="flex items-center justify-between px-2 py-1.5 text-xs text-foreground bg-muted/40 border border-border">
            <span className="flex items-center gap-2 truncate">
              <span className="h-2 w-2 rounded-full bg-emerald-500 shrink-0" />
              <span className="truncate font-medium">{user.display_name || user.username}</span>
            </span>
            {onLogout && (
              <button
                type="button"
                onClick={onLogout}
                title="Sign out"
                className="p-1 text-muted-foreground hover:text-destructive transition-colors shrink-0"
              >
                <LogOut className="h-3.5 w-3.5" />
              </button>
            )}
          </div>
        ) : (
          onOpenLogin && (
            <button
              type="button"
              onClick={onOpenLogin}
              className="flex h-8 w-full items-center justify-center gap-2 border border-border bg-background px-2 text-xs font-medium text-foreground hover:bg-muted transition-colors"
            >
              <Lock className="h-3.5 w-3.5 text-muted-foreground" />
              <span>Sign in</span>
            </button>
          )
        )}
      </div>

      {/* Bottom Segmented [ ☼ Light | ☾ Dark ] Switcher matching boat.dev/dashboard */}
      <div className="pt-3">
        <div className="grid grid-cols-2 border border-border bg-background h-8">
          <button
            type="button"
            onClick={handleLight}
            className={cn(
              'flex items-center justify-center gap-1.5 text-[12px] transition-colors',
              theme === 'light'
                ? 'bg-[#f1f3f9] text-[#263fa9] font-medium'
                : 'text-muted-foreground hover:text-foreground',
            )}
          >
            <Sun className="h-3.5 w-3.5" />
            <span>Light</span>
          </button>
          <button
            type="button"
            onClick={handleDark}
            className={cn(
              'flex items-center justify-center gap-1.5 text-[12px] transition-colors border-l border-border',
              theme === 'dark'
                ? 'bg-[#0f1634] text-[#fefefe] font-medium'
                : 'text-muted-foreground hover:text-foreground',
            )}
          >
            <Moon className="h-3.5 w-3.5" />
            <span>Dark</span>
          </button>
        </div>
      </div>
    </div>
  );
}
