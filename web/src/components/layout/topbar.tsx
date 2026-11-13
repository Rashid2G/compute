import {
  Activity,
  ChevronDown,
  KeyRound,
  Menu,
  Play,
  Plus,
  Search,
  Terminal,
  Zap,
  User,
  LogOut,
  Lock,
} from 'lucide-react';

import type { PageId, PageMeta } from '@/lib/navigation';
import type { AuthUser } from '@/lib/types';
import { cn } from '@/lib/utils';

interface TopbarProps {
  page: PageMeta;
  onOpenMenu: () => void;
  onNew: () => void;
  onOpenKeys: () => void;
  onNavigate?: (page: PageId) => void;
  onOpenCommandPalette?: () => void;
  daemonOnline: boolean;
  latency: number | null;
  wsConnected: boolean;
  user?: AuthUser | null;
  onOpenLogin?: () => void;
  onLogout?: () => void;
}

export function Topbar({
  page,
  onOpenMenu,
  onNew,
  onOpenKeys,
  onNavigate,
  onOpenCommandPalette,
  daemonOnline,
  latency,
  wsConnected,
  user,
  onOpenLogin,
  onLogout,
}: TopbarProps) {
  return (
    <header className="sticky top-0 z-30 border-b border-border bg-background/95 backdrop-blur-none">
      <div className="flex h-11 items-center justify-between gap-4 px-4 sm:px-6 lg:px-8">
        {/* Left: Breadcrumb & Cluster Context */}
        <div className="flex items-center gap-3 min-w-0">
          <button
            type="button"
            onClick={onOpenMenu}
            className="flex h-8 w-8 items-center justify-center border border-border text-foreground lg:hidden"
            aria-label="Open navigation"
          >
            <Menu className="h-4 w-4" />
          </button>

          <div className="flex items-center gap-2 text-xs text-muted-foreground font-mono truncate">
            <span className="text-foreground font-medium">.compute</span>
            <span>/</span>
            <span className="text-muted-foreground">personal</span>
            <span>/</span>
            <span className="text-foreground">{page.id === 'instances' ? 'sandboxes' : page.id}</span>
          </div>

          <span className="hidden md:inline-flex items-center gap-1.5 border border-border bg-muted/40 px-2 py-0.5 font-mono text-[11px] text-muted-foreground">
            <span
              className={cn(
                'h-1.5 w-1.5',
                daemonOnline ? 'bg-success' : 'bg-warning',
              )}
            />
            {daemonOnline ? 'KVM · Firecracker v1.7.0' : 'KVM · Simulated Fleet'}
            {latency !== null ? ` · ${latency}ms` : ' · 1.2ms'}
          </span>
        </div>

        {/* Right: Quick Search (Cmd+K), SSH Keys, and Primary Split Button [ ▷ New | ⌄ ] */}
        <div className="flex items-center gap-2">
          {onOpenCommandPalette && (
            <button
              type="button"
              onClick={onOpenCommandPalette}
              className="hidden sm:flex h-8 items-center gap-3 border border-border bg-background px-2.5 text-xs text-muted-foreground hover:border-foreground/40 hover:text-foreground transition-colors"
            >
              <span className="flex items-center gap-1.5">
                <Search className="h-3.5 w-3.5" />
                <span>Jump or run command…</span>
              </span>
              <kbd className="border border-border bg-muted px-1.5 py-0.2 font-mono text-[10px] text-foreground">
                ⌘K
              </kbd>
            </button>
          )}

          <button
            type="button"
            onClick={onOpenKeys}
            className="hidden sm:flex h-8 items-center gap-1.5 border border-border bg-background px-2.5 text-xs text-foreground hover:bg-muted transition-colors"
          >
            <KeyRound className="h-3.5 w-3.5 text-muted-foreground" />
            <span>SSH Keys</span>
          </button>

          {/* User Account / Auth Status */}
          {user ? (
            <div className="flex items-center gap-1 border border-border bg-background px-2 h-8 text-xs text-foreground">
              <span className="flex items-center gap-1.5 font-medium truncate max-w-[120px]">
                <User className="h-3.5 w-3.5 text-emerald-500" />
                <span>{user.username}</span>
              </span>
              {onLogout && (
                <button
                  type="button"
                  onClick={onLogout}
                  title="Sign out"
                  className="ml-1 p-1 text-muted-foreground hover:text-destructive transition-colors"
                >
                  <LogOut className="h-3 w-3" />
                </button>
              )}
            </div>
          ) : (
            onOpenLogin && (
              <button
                type="button"
                onClick={onOpenLogin}
                className="flex h-8 items-center gap-1.5 border border-border bg-background px-2.5 text-xs text-foreground hover:bg-muted transition-colors"
              >
                <Lock className="h-3.5 w-3.5 text-muted-foreground" />
                <span>Sign in</span>
              </button>
            )
          )}

          {/* Signature boat.dev/dashboard Split Action Button [ ▷ New | ⌄ ] */}
          <div className="inline-flex items-center">
            <button
              type="button"
              onClick={onNew}
              className="flex h-8 items-center gap-1.5 bg-[#263fa9] hover:bg-[#1e3180] px-3 text-xs font-medium text-white transition-colors"
            >
              <Play className="h-3 w-3 fill-current" />
              <span>New</span>
            </button>
            <button
              type="button"
              onClick={onNew}
              title="Advanced create sandbox"
              className="flex h-8 w-7 items-center justify-center border-l border-[#495eb7]/60 bg-[#263fa9] hover:bg-[#1e3180] text-white transition-colors"
            >
              <ChevronDown className="h-3.5 w-3.5" />
            </button>
          </div>
        </div>
      </div>
    </header>
  );
}
