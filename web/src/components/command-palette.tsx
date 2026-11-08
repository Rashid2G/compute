import { useEffect, useMemo, useState } from 'react';
import {
  ArrowRight,
  Cpu,
  Database,
  HardDrive,
  KeyRound,
  LayoutDashboard,
  Network,
  Play,
  Plus,
  Radio,
  RefreshCw,
  Search,
  Server,
  Settings,
  Terminal,
  X,
  Zap,
} from 'lucide-react';

import { PAGES, type PageId } from '@/lib/navigation';
import type { VM } from '@/lib/types';

interface CommandPaletteProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onNavigate: (page: PageId) => void;
  onNewSandbox: () => void;
  onOpenKeys: () => void;
  onRefresh: () => void;
  vms: VM[];
  onSelectVM?: (vm: VM) => void;
}

export function CommandPalette({
  open,
  onOpenChange,
  onNavigate,
  onNewSandbox,
  onOpenKeys,
  onRefresh,
  vms,
  onSelectVM,
}: CommandPaletteProps) {
  const [query, setQuery] = useState('');

  useEffect(() => {
    if (!open) setQuery('');
  }, [open]);

  const actions = useMemo(
    () => [
      {
        id: 'act-new',
        group: 'Quick Actions',
        label: 'Advanced create sandbox (Firecracker microVM)',
        shortcut: 'C',
        icon: Play,
        run: () => {
          onOpenChange(false);
          onNewSandbox();
        },
      },
      {
        id: 'act-ci',
        group: 'Quick Actions',
        label: 'Set up CI on Compute VMs / Dispatch Ephemeral Runner',
        shortcut: 'J',
        icon: Zap,
        run: () => {
          onOpenChange(false);
          onNavigate('jobs');
        },
      },
      {
        id: 'act-env',
        group: 'Quick Actions',
        label: 'Configure Base Environment, Secrets & Snapshots',
        shortcut: 'E',
        icon: Database,
        run: () => {
          onOpenChange(false);
          onNavigate('images');
        },
      },
      {
        id: 'act-keys',
        group: 'Quick Actions',
        label: 'Manage SSH Public Keys & Scoped API Keys',
        shortcut: 'K',
        icon: KeyRound,
        run: () => {
          onOpenChange(false);
          onOpenKeys();
        },
      },
      {
        id: 'act-refresh',
        group: 'Quick Actions',
        label: 'Reconcile Cluster State & Refresh Telemetry',
        shortcut: 'R',
        icon: RefreshCw,
        run: () => {
          onOpenChange(false);
          onRefresh();
        },
      },
      ...PAGES.map((p) => ({
        id: `nav-${p.id}`,
        group: 'Navigation',
        label: `Go to ${p.label} — ${p.description}`,
        shortcut: `/${p.id}`,
        icon: p.icon,
        run: () => {
          onOpenChange(false);
          onNavigate(p.id);
        },
      })),
      ...vms.map((vm) => ({
        id: `vm-${vm.id}`,
        group: 'Sandboxes',
        label: `${vm.name} (${vm.id}) · ${vm.status.toUpperCase()} · ${vm.ip_address}`,
        shortcut: `${vm.vcpus}vCPU/${vm.memory_mb}MB`,
        icon: Server,
        run: () => {
          onOpenChange(false);
          if (onSelectVM) onSelectVM(vm);
          else onNavigate('instances');
        },
      })),
    ],
    [onNavigate, onNewSandbox, onOpenChange, onOpenKeys, onRefresh, onSelectVM, vms],
  );

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return actions;
    return actions.filter(
      (item) =>
        item.label.toLowerCase().includes(q) ||
        item.group.toLowerCase().includes(q) ||
        item.shortcut.toLowerCase().includes(q),
    );
  }, [actions, query]);

  if (!open) return null;

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center bg-black/75 pt-20 px-4"
      onClick={() => onOpenChange(false)}
    >
      <div
        className="w-full max-w-xl border border-border bg-background text-foreground"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center border-b border-border px-4 h-12 gap-3">
          <Search className="h-4 w-4 text-muted-foreground shrink-0" />
          <input
            type="text"
            autoFocus
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Type a command, sandbox ID, or route (e.g. sandboxes, ci, workers, environment)..."
            className="w-full bg-transparent text-sm text-foreground placeholder:text-muted-foreground focus:outline-none"
          />
          <button
            type="button"
            onClick={() => onOpenChange(false)}
            className="text-muted-foreground hover:text-foreground"
          >
            <X className="h-4 w-4" />
          </button>
        </div>

        <div className="max-h-96 overflow-y-auto divide-y divide-border">
          {filtered.length === 0 ? (
            <div className="py-10 text-center text-xs text-muted-foreground font-mono">
              No matching commands or sandboxes for "{query}".
            </div>
          ) : (
            filtered.map((item) => {
              const Icon = item.icon;
              return (
                <button
                  key={item.id}
                  type="button"
                  onClick={item.run}
                  className="flex w-full items-center justify-between px-4 py-2.5 text-left text-xs hover:bg-selected transition-colors group"
                >
                  <div className="flex items-center gap-3 min-w-0">
                    <Icon className="h-3.5 w-3.5 text-muted-foreground group-hover:text-foreground shrink-0" />
                    <div className="truncate">
                      <span className="text-[10px] uppercase tracking-wider text-muted-foreground mr-2 font-mono">
                        [{item.group}]
                      </span>
                      <span className="text-foreground">{item.label}</span>
                    </div>
                  </div>
                  <span className="ml-3 shrink-0 border border-border bg-muted/50 px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground">
                    {item.shortcut}
                  </span>
                </button>
              );
            })
          )}
        </div>

        <div className="flex items-center justify-between border-t border-border bg-muted/30 px-4 py-2 font-mono text-[11px] text-muted-foreground">
          <span>↑↓ to navigate · ↵ to select · ESC to dismiss</span>
          <span>.compute control-plane v2.4</span>
        </div>
      </div>
    </div>
  );
}
