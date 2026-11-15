import {
  Activity,
  KeyRound,
  Moon,
  Network,
  Palette,
  Radio,
  RefreshCw,
  Server,
  Sliders,
  Sun,
} from 'lucide-react';

import { formatMemory } from '@/lib/format';
import type { PageId, SettingsTabId } from '@/lib/navigation';
import type {
  AuditEventRecord,
  AuthUser,
  DaemonConfig,
  NetworkAllocation,
  NetworkConfig,
  StatsResult,
  VM,
  VolumeRecord,
} from '@/lib/types';
import { cn } from '@/lib/utils';
import { AccessPage } from '@/pages/access';
import { ApiPage } from '@/pages/api';
import { EventsPage } from '@/pages/events';
import { ImagesTemplatesPage } from '@/pages/images-templates';
import { NetworkPage } from '@/pages/network';

interface SettingsPageProps {
  activeTab?: SettingsTabId;
  onTabChange?: (tab: SettingsTabId) => void;
  theme: 'light' | 'dark';
  onThemeChange: (theme: 'light' | 'dark') => void;
  daemonOnline: boolean;
  latency: number | null;
  wsConnected: boolean;
  config: DaemonConfig | null;
  stats?: StatsResult | null;
  vms?: VM[];
  volumes?: VolumeRecord[];
  allocations?: NetworkAllocation[];
  networkConfig?: NetworkConfig | null;
  events?: AuditEventRecord[];
  currentUser?: AuthUser | null;
  onRefresh?: () => void;
  onLaunchVM?: () => void;
  onNavigate?: (page: PageId) => void;
}

const SETTINGS_TAB_ITEMS: Array<{
  id: SettingsTabId;
  label: string;
  icon: typeof Server;
}> = [
  { id: 'general', label: 'General', icon: Server },
  { id: 'appearance', label: 'Appearance', icon: Palette },
  { id: 'access', label: 'Access & Keys', icon: KeyRound },
  { id: 'environment', label: 'Environment', icon: Sliders },
  { id: 'network', label: 'Network & NAT', icon: Network },
  { id: 'events', label: 'Webhooks & OTel', icon: Radio },
];

export function SettingsPage({
  activeTab = 'general',
  onTabChange,
  theme,
  onThemeChange,
  daemonOnline,
  latency,
  wsConnected,
  config,
  vms = [],
  volumes = [],
  allocations = [],
  networkConfig = null,
  events = [],
  currentUser = null,
  onRefresh,
  onLaunchVM,
  onNavigate,
}: SettingsPageProps) {
  const selectTab = (tab: SettingsTabId) => {
    onTabChange?.(tab);
  };

  const totalVolumeGB = volumes.reduce((acc, v) => acc + (v.size_gb || 0), 0);

  return (
    <div className="space-y-6">
      <div className="space-y-1">
        <h1 className="font-display text-[22px] font-medium tracking-tight text-foreground">
          Settings
        </h1>
        <p className="text-[12px] text-muted-foreground">
          Control-plane configuration, appearance, access keys, sandbox
          environments, networking, and observability.
        </p>
      </div>

      {/* Settings Tabs */}
      <div
        role="tablist"
        aria-label="Settings sections"
        className="flex items-center overflow-x-auto border-b border-border"
      >
        {SETTINGS_TAB_ITEMS.map((item) => {
          const Icon = item.icon;
          const active = activeTab === item.id;
          return (
            <button
              key={item.id}
              type="button"
              role="tab"
              aria-selected={active}
              data-testid={`settings-tab-${item.id}`}
              onClick={() => selectTab(item.id)}
              className={cn(
                'flex items-center gap-2 border-b-2 px-4 py-2 text-[12.5px] transition-colors whitespace-nowrap',
                active
                  ? 'border-[#263fa9] text-foreground font-medium'
                  : 'border-transparent text-muted-foreground hover:text-foreground',
              )}
            >
              <Icon className="h-3.5 w-3.5" />
              <span>{item.label}</span>
            </button>
          );
        })}
      </div>

      {/* TAB 1: General (Daemon, Storage, Default VM resources, Maintenance) */}
      {activeTab === 'general' && (
        <div className="space-y-6">
          <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
            {/* Daemon Runtime */}
            <div className="border border-border bg-background p-5 space-y-3 font-mono text-xs">
              <div className="font-sans text-[13.5px] font-medium text-foreground">
                Daemon &amp; Control-Plane Runtime
              </div>
              <div className="flex justify-between gap-4">
                <span className="text-muted-foreground">Daemon Status:</span>
                <span
                  className={daemonOnline ? 'text-success' : 'text-destructive'}
                >
                  {daemonOnline ? 'ONLINE' : 'OFFLINE'} ({latency ?? 1}ms)
                </span>
              </div>
              <div className="flex justify-between gap-4">
                <span className="text-muted-foreground">WebSocket Stream:</span>
                <span>
                  {wsConnected ? 'CONNECTED (/api/v1/ws)' : 'RECONNECTING'}
                </span>
              </div>
              <div className="flex justify-between gap-4">
                <span className="text-muted-foreground">Listen Address:</span>
                <span>{config?.server?.listen || '0.0.0.0:18080'}</span>
              </div>
              <div className="flex justify-between gap-4">
                <span className="text-muted-foreground">
                  Firecracker Binary:
                </span>
                <span className="truncate max-w-[240px]">
                  {config?.server?.firecracker_bin ||
                    '/usr/local/bin/firecracker'}
                </span>
              </div>
              <div className="flex justify-between gap-4">
                <span className="text-muted-foreground">SQLite Database:</span>
                <span className="truncate max-w-[240px]">
                  {config?.server?.db_path || '/var/lib/compute/compute.db'}
                </span>
              </div>
            </div>

            {/* Default VM Resources */}
            <div className="border border-border bg-background p-5 space-y-3 font-mono text-xs">
              <div className="font-sans text-[13.5px] font-medium text-foreground">
                Default VM Resources
              </div>
              <div className="flex justify-between gap-4">
                <span className="text-muted-foreground">Default vCPUs:</span>
                <span>{config?.defaults?.vcpus ?? 2} vCPU</span>
              </div>
              <div className="flex justify-between gap-4">
                <span className="text-muted-foreground">Default Memory:</span>
                <span>{formatMemory(config?.defaults?.memory_mb ?? 2048)}</span>
              </div>
              <div className="flex justify-between gap-4">
                <span className="text-muted-foreground">
                  Default Disk Size:
                </span>
                <span>
                  {Math.round((config?.defaults?.disk_size_mb ?? 20480) / 1024)}{' '}
                  GB
                </span>
              </div>
              <div className="flex justify-between gap-4">
                <span className="text-muted-foreground">Default Image:</span>
                <span>{config?.defaults?.image || 'Ubuntu 24.04 LTS (ubuntu-24)'}</span>
              </div>
              <div className="flex justify-between gap-4">
                <span className="text-muted-foreground">Host Guardrails:</span>
                <span>
                  {config?.resources?.max_running_vms &&
                  config.resources.max_running_vms > 0
                    ? `Max ${config.resources.max_running_vms} VMs`
                    : 'Unlimited VMs'}
                </span>
              </div>
            </div>

            {/* Storage Paths */}
            <div className="border border-border bg-background p-5 space-y-3 font-mono text-xs">
              <div className="font-sans text-[13.5px] font-medium text-foreground">
                Storage &amp; Image Directories
              </div>
              <div className="flex justify-between gap-4">
                <span className="text-muted-foreground">Images Directory:</span>
                <span className="truncate max-w-[240px]">
                  {config?.images?.images_dir || '/var/lib/compute/images'}
                </span>
              </div>
              <div className="flex justify-between gap-4">
                <span className="text-muted-foreground">VMs Directory:</span>
                <span className="truncate max-w-[240px]">
                  {config?.images?.vms_dir || '/var/lib/compute/vms'}
                </span>
              </div>
              <div className="flex justify-between gap-4">
                <span className="text-muted-foreground">Default Kernel:</span>
                <span className="truncate max-w-[240px]">
                  {config?.images?.default_kernel || 'vmlinux'}
                </span>
              </div>
              <div className="flex justify-between gap-4">
                <span className="text-muted-foreground">Default Rootfs:</span>
                <span className="truncate max-w-[240px]">
                  {config?.images?.default_rootfs || 'ubuntu-24.04.ext4'}
                </span>
              </div>
              <div className="flex justify-between gap-4">
                <span className="text-muted-foreground">
                  Persistent Volumes:
                </span>
                <span>
                  {volumes.length} volume(s) · {totalVolumeGB} GB
                </span>
              </div>
            </div>

            {/* Maintenance */}
            <div className="border border-border bg-background p-5 space-y-4">
              <div className="space-y-1">
                <div className="text-[13.5px] font-medium text-foreground">
                  Maintenance &amp; Diagnostics
                </div>
                <p className="text-xs text-muted-foreground">
                  Reconcile live control-plane state, refresh cluster telemetry,
                  or jump to worker quotas.
                </p>
              </div>
              <div className="flex flex-wrap items-center gap-2">
                {onRefresh && (
                  <button
                    type="button"
                    onClick={onRefresh}
                    className="flex h-8 items-center gap-1.5 border border-border bg-background px-3 text-[12px] text-foreground hover:border-[#263fa9] transition-colors"
                  >
                    <RefreshCw className="h-3.5 w-3.5" />
                    <span>Refresh Cluster State</span>
                  </button>
                )}
                {onNavigate && (
                  <button
                    type="button"
                    onClick={() => onNavigate('workers')}
                    className="flex h-8 items-center gap-1.5 border border-border bg-background px-3 text-[12px] text-muted-foreground hover:text-foreground transition-colors"
                  >
                    <Activity className="h-3.5 w-3.5" />
                    <span>View Workers &amp; Quotas</span>
                  </button>
                )}
              </div>
            </div>
          </div>
        </div>
      )}

      {/* TAB 2: Appearance (Item #5) */}
      {activeTab === 'appearance' && (
        <div className="space-y-6 max-w-3xl">
          <div className="border border-border bg-background p-5 space-y-4">
            <div className="space-y-1">
              <h2 className="text-[14px] font-medium text-foreground">
                Theme Polarity
              </h2>
              <p className="text-xs text-muted-foreground">
                Choose between Daylight crisp paper (#fefefe) and Pitch-black
                high-contrast (#060606) with Maritime Cobalt (#263fa9) accents.
              </p>
            </div>

            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
              <button
                type="button"
                onClick={() => onThemeChange('light')}
                className={cn(
                  'border p-4 text-left transition-colors space-y-3',
                  theme === 'light'
                    ? 'border-[#263fa9] bg-[#f1f3f9]'
                    : 'border-border bg-background hover:border-foreground/40',
                )}
              >
                <div className="flex items-center justify-between">
                  <span className="inline-flex items-center gap-2 text-[13px] font-medium text-[#060606]">
                    <Sun className="h-4 w-4 text-[#263fa9]" />
                    <span>Light Mode</span>
                  </span>
                  {theme === 'light' && (
                    <span className="border border-[#263fa9] bg-[#263fa9] px-1.5 py-0.5 font-mono text-[10px] uppercase text-white">
                      Active
                    </span>
                  )}
                </div>
                <div className="border border-[#e0e0e0] bg-[#fefefe] p-2.5 space-y-1.5">
                  <div className="h-2 w-20 bg-[#263fa9]" />
                  <div className="h-2 w-32 bg-[#e0e0e0]" />
                  <div className="h-2 w-24 bg-[#f2f2f2]" />
                </div>
                <p className="text-[11px] text-[#4d4d4d]">
                  Crisp daylight paper (#fefefe) surface with neutral hairlines.
                </p>
              </button>

              <button
                type="button"
                onClick={() => onThemeChange('dark')}
                className={cn(
                  'border p-4 text-left transition-colors space-y-3',
                  theme === 'dark'
                    ? 'border-[#263fa9] bg-[#0f1634]'
                    : 'border-border bg-background hover:border-foreground/40',
                )}
              >
                <div className="flex items-center justify-between">
                  <span className="inline-flex items-center gap-2 text-[13px] font-medium text-foreground">
                    <Moon className="h-4 w-4 text-[#6b7cc4]" />
                    <span>Dark Mode</span>
                  </span>
                  {theme === 'dark' && (
                    <span className="border border-[#263fa9] bg-[#263fa9] px-1.5 py-0.5 font-mono text-[10px] uppercase text-white">
                      Active
                    </span>
                  )}
                </div>
                <div className="border border-[#2e2e2e] bg-[#060606] p-2.5 space-y-1.5">
                  <div className="h-2 w-20 bg-[#263fa9]" />
                  <div className="h-2 w-32 bg-[#2e2e2e]" />
                  <div className="h-2 w-24 bg-[#1a1a1a]" />
                </div>
                <p className="text-[11px] text-muted-foreground">
                  High-contrast obsidian (#060606) for low-glare operations.
                </p>
              </button>
            </div>
          </div>

          <div className="border border-border bg-background p-5 space-y-3">
            <div className="text-[13.5px] font-medium text-foreground">
              UI Scale &amp; Interface Preferences
            </div>
            <div className="grid grid-cols-1 sm:grid-cols-3 gap-3 font-mono text-xs">
              <div className="border border-border p-3 space-y-1">
                <div className="text-muted-foreground text-[10.5px] uppercase">
                  Base Root Scale
                </div>
                <div className="text-foreground font-medium">100% (16px)</div>
              </div>
              <div className="border border-border p-3 space-y-1">
                <div className="text-muted-foreground text-[10.5px] uppercase">
                  Grid Geometry
                </div>
                <div className="text-foreground font-medium">
                  0px Sharp Hairline
                </div>
              </div>
              <div className="border border-border p-3 space-y-1">
                <div className="text-muted-foreground text-[10.5px] uppercase">
                  Telemetry Numerals
                </div>
                <div className="text-foreground font-medium">
                  Tabular Monospace
                </div>
              </div>
            </div>
          </div>
        </div>
      )}

      {/* TAB 3: Access & Keys */}
      {activeTab === 'access' && (
        <div className="space-y-8">
          <AccessPage currentUser={currentUser} embedded />
          <ApiPage config={config} embedded />
        </div>
      )}

      {/* TAB 4: Environment */}
      {activeTab === 'environment' && (
        <ImagesTemplatesPage
          vms={vms}
          mode="environment"
          onLaunchVM={onLaunchVM ?? (() => {})}
          onNavigate={onNavigate}
          onRefreshCluster={onRefresh}
          embedded
        />
      )}

      {/* TAB 5: Network & NAT */}
      {activeTab === 'network' && (
        <NetworkPage
          allocations={allocations}
          networkConfig={networkConfig}
          onRefresh={onRefresh ?? (() => {})}
          embedded
        />
      )}

      {/* TAB 6: Webhooks & OTel */}
      {activeTab === 'events' && (
        <EventsPage events={events} onRefresh={onRefresh} embedded />
      )}
    </div>
  );
}
