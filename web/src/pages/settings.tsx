import { formatMemory } from '@/lib/format';
import type {
  DaemonConfig,
  StatsResult,
  VM,
  VolumeRecord,
} from '@/lib/types';
import { cn } from '@/lib/utils';

interface SettingsPageProps {
  theme: 'light' | 'dark';
  onThemeChange: (theme: 'light' | 'dark') => void;
  daemonOnline: boolean;
  latency: number | null;
  wsConnected: boolean;
  config: DaemonConfig | null;
  stats?: StatsResult | null;
  vms?: VM[];
  volumes?: VolumeRecord[];
}

export function SettingsPage({
  theme,
  onThemeChange,
  daemonOnline,
  latency,
  wsConnected,
  config,
  stats,
  vms = [],
  volumes = [],
}: SettingsPageProps) {
  const runningVMs =
    stats?.running_vms ?? vms.filter((v) => v.status === 'running').length;
  const totalVMs = stats?.total_vms ?? vms.length;
  const snapshottedCount =
    stats?.snapshotted_vms ?? vms.filter((v) => v.status === 'paused').length;
  const volumeGB = volumes.reduce((acc, v) => acc + (v.size_gb || 0), 0);

  // Compute hourly burn rate from live running VMs
  const hourlyBurnRate = vms
    .filter((v) => v.status === 'running')
    .reduce((acc, v) => {
      if (v.vcpus <= 2) return acc + 0.018;
      if (v.vcpus <= 4) return acc + 0.036;
      if (v.vcpus <= 8) return acc + 0.072;
      return acc + 0.144;
    }, 0);

  return (
    <div className="space-y-6">
      <div className="space-y-1">
        <h1 className="font-display text-[22px] font-medium tracking-tight text-foreground">
          Organization &amp; Billing
        </h1>
        <p className="text-[12px] text-muted-foreground">
          Live per-second Firecracker usage telemetry, cluster resource
          guardrails, and daemon runtime configuration.
        </p>
      </div>

      {/* Live Billing & Usage Summary */}
      <div className="grid grid-cols-1 md:grid-cols-3 border border-border bg-background divide-y md:divide-y-0 md:divide-x divide-border">
        <div className="p-4 space-y-1">
          <div className="text-xs text-muted-foreground">
            Active Hourly Burn Rate
          </div>
          <div className="font-display text-2xl font-medium text-foreground tabular">
            ${hourlyBurnRate.toFixed(3)}/h
          </div>
          <div className="font-mono text-[11px] text-muted-foreground">
            {runningVMs} running / {totalVMs} total sandboxes
          </div>
        </div>
        <div className="p-4 space-y-1">
          <div className="text-xs text-muted-foreground">
            Provisioned Block &amp; Snapshot Storage
          </div>
          <div className="font-display text-2xl font-medium text-foreground tabular">
            {volumeGB} GB
          </div>
          <div className="font-mono text-[11px] text-muted-foreground">
            {volumes.length} volumes · {snapshottedCount}{' '}
            {snapshottedCount === 1 ? 'paused snapshot' : 'paused snapshots'}
          </div>
        </div>
        <div className="p-4 space-y-1">
          <div className="text-xs text-muted-foreground">
            Host Resource Guardrails
          </div>
          <div className="font-display text-2xl font-medium text-foreground tabular">
            {config?.resources?.max_total_vcpus &&
            config.resources.max_total_vcpus > 0
              ? `${config.resources.max_total_vcpus} vCPU`
              : 'Unlimited vCPU'}
          </div>
          <div className="font-mono text-[11px] text-success">
            Max RAM:{' '}
            {config?.resources?.max_total_memory_mb &&
            config.resources.max_total_memory_mb > 0
              ? formatMemory(config.resources.max_total_memory_mb)
              : 'Unlimited'}{' '}
            · Max VMs:{' '}
            {config?.resources?.max_running_vms &&
            config.resources.max_running_vms > 0
              ? config.resources.max_running_vms
              : 'Unlimited'}
          </div>
        </div>
      </div>

      {/* Appearance & Control Plane Runtime */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
        <div className="border border-border bg-background p-5 space-y-4">
          <div className="space-y-1">
            <h2 className="text-[13.5px] font-medium text-foreground">
              Dashboard Theme Polarity
            </h2>
            <p className="text-xs text-muted-foreground">
              High-contrast pitch-black (#060606) or daylight crisp paper
              (#fefefe) with Maritime Cobalt (#263fa9).
            </p>
          </div>

          <div className="grid grid-cols-2 border border-border h-9">
            <button
              type="button"
              onClick={() => onThemeChange('light')}
              className={cn(
                'text-xs font-medium transition-colors',
                theme === 'light'
                  ? 'bg-[#f1f3f9] text-[#263fa9]'
                  : 'text-muted-foreground hover:text-foreground',
              )}
            >
              ☼ Light (#fefefe)
            </button>
            <button
              type="button"
              onClick={() => onThemeChange('dark')}
              className={cn(
                'border-l border-border text-xs font-medium transition-colors',
                theme === 'dark'
                  ? 'bg-[#0f1634] text-white'
                  : 'text-muted-foreground hover:text-foreground',
              )}
            >
              ☾ Dark (#060606)
            </button>
          </div>
        </div>

        <div className="border border-border bg-background p-5 space-y-2.5 font-mono text-xs">
          <div className="font-sans text-[13.5px] font-medium text-foreground">
            Control-Plane Daemon &amp; Runtime Paths (/api/v1/config)
          </div>
          <div className="flex justify-between">
            <span className="text-muted-foreground">Daemon Status:</span>
            <span
              className={daemonOnline ? 'text-success' : 'text-destructive'}
            >
              {daemonOnline ? 'ONLINE' : 'OFFLINE'} ({latency ?? 1}ms)
            </span>
          </div>
          <div className="flex justify-between">
            <span className="text-muted-foreground">WebSocket Stream:</span>
            <span>{wsConnected ? 'CONNECTED (/api/v1/ws)' : 'RECONNECTING'}</span>
          </div>
          <div className="flex justify-between">
            <span className="text-muted-foreground">Firecracker Binary:</span>
            <span>
              {config?.server?.firecracker_bin || '/usr/local/bin/firecracker'}
            </span>
          </div>
          <div className="flex justify-between">
            <span className="text-muted-foreground">SQLite Database:</span>
            <span className="truncate max-w-[240px]">
              {config?.server?.db_path || '/var/lib/compute/compute.db'}
            </span>
          </div>
          <div className="flex justify-between">
            <span className="text-muted-foreground">Default VM Specs:</span>
            <span>
              {config?.defaults?.vcpus ?? 2} vCPU ·{' '}
              {formatMemory(config?.defaults?.memory_mb ?? 2048)}
            </span>
          </div>
        </div>
      </div>
    </div>
  );
}
