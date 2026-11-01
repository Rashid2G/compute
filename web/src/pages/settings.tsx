import { Check, Laptop, Moon, Radio, Server, ShieldCheck, Sun } from 'lucide-react';

import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card';
import { Separator } from '@/components/ui/separator';
import type { DaemonConfig } from '@/lib/types';
import { cn } from '@/lib/utils';

export function SettingsPage({
  theme,
  onThemeChange,
  daemonOnline,
  latency,
  wsConnected,
  config,
}: {
  theme: 'light' | 'dark';
  onThemeChange: (theme: 'light' | 'dark') => void;
  daemonOnline: boolean;
  latency: number | null;
  wsConnected?: boolean;
  config: DaemonConfig | null;
}) {
  const wsProtocol = typeof window !== 'undefined' && window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  const wsHost = typeof window !== 'undefined' ? window.location.host : 'localhost:18080';
  const wsEndpoint = `${wsProtocol}//${wsHost}/api/v1/ws`;

  return (
    <div className="max-w-4xl space-y-6 animate-fade-in">
      {/* 1. Appearance / Theme */}
      <Card>
        <CardHeader>
          <div className="flex items-center gap-2">
            <Sun className="h-4 w-4 text-primary" />
            <CardTitle>Appearance & Theme</CardTitle>
          </div>
          <CardDescription>
            Choose how Compute looks and feels on your workspace display.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid gap-4 sm:grid-cols-2">
            {/* Light Mode Option */}
            <button
              type="button"
              onClick={() => onThemeChange('light')}
              className={cn(
                'group relative flex flex-col items-start rounded-xl border p-4 text-left transition-all hover:bg-secondary/50',
                theme === 'light'
                  ? 'border-primary ring-2 ring-primary/20 bg-primary/5'
                  : 'border-border bg-card',
              )}
            >
              <div className="flex w-full items-center justify-between">
                <span className="flex h-9 w-9 items-center justify-center rounded-lg border bg-background text-foreground shadow-sm">
                  <Sun className="h-4 w-4 text-amber-500" />
                </span>
                {theme === 'light' ? (
                  <span className="flex h-5 w-5 items-center justify-center rounded-full bg-primary text-primary-foreground">
                    <Check className="h-3 w-3" />
                  </span>
                ) : null}
              </div>
              <div className="mt-3">
                <div className="text-sm font-semibold text-foreground">Light Mode</div>
                <div className="mt-1 text-xs text-muted-foreground leading-normal">
                  Crisp, light background with sharp contrast suited for bright daylight environments.
                </div>
              </div>
            </button>

            {/* Dark Mode Option */}
            <button
              type="button"
              onClick={() => onThemeChange('dark')}
              className={cn(
                'group relative flex flex-col items-start rounded-xl border p-4 text-left transition-all hover:bg-secondary/50',
                theme === 'dark'
                  ? 'border-primary ring-2 ring-primary/20 bg-primary/5'
                  : 'border-border bg-card',
              )}
            >
              <div className="flex w-full items-center justify-between">
                <span className="flex h-9 w-9 items-center justify-center rounded-lg border bg-background text-foreground shadow-sm">
                  <Moon className="h-4 w-4 text-indigo-400" />
                </span>
                {theme === 'dark' ? (
                  <span className="flex h-5 w-5 items-center justify-center rounded-full bg-primary text-primary-foreground">
                    <Check className="h-3 w-3" />
                  </span>
                ) : null}
              </div>
              <div className="mt-3">
                <div className="text-sm font-semibold text-foreground">Dark Mode</div>
                <div className="mt-1 text-xs text-muted-foreground leading-normal">
                  High-contrast dark palette engineered for low light and prolonged operational monitoring.
                </div>
              </div>
            </button>
          </div>
        </CardContent>
      </Card>

      {/* 2. Real-Time WebSocket Connection */}
      <Card>
        <CardHeader>
          <div className="flex items-center gap-2">
            <Radio className="h-4 w-4 text-primary" />
            <CardTitle>Real-Time Communication</CardTitle>
          </div>
          <CardDescription>
            Compute utilizes a WebSocket stream for instantaneous telemetry and cluster updates without periodic polling.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="rounded-lg border bg-secondary/30 p-3.5 space-y-1">
              <div className="text-[10px] font-medium uppercase tracking-[0.08em] text-muted-foreground">
                WebSocket Status
              </div>
              <div className="flex items-center gap-2 pt-0.5">
                <span
                  className={cn(
                    'h-2 w-2 rounded-full',
                    daemonOnline
                      ? 'bg-emerald-500 shadow-[0_0_8px_rgba(16,185,129,0.5)] animate-pulse'
                      : 'bg-destructive',
                  )}
                />
                <span className="text-xs font-semibold text-foreground">
                  {daemonOnline ? (wsConnected ? 'Connected (Live)' : 'Connected') : 'Disconnected'}
                </span>
              </div>
            </div>

            <div className="rounded-lg border bg-secondary/30 p-3.5 space-y-1">
              <div className="text-[10px] font-medium uppercase tracking-[0.08em] text-muted-foreground">
                Latency / Round Trip
              </div>
              <div className="pt-0.5 font-mono text-xs font-semibold text-foreground">
                {daemonOnline && latency !== null ? `${latency} ms` : '—'}
              </div>
            </div>
          </div>

          <div className="rounded-lg border bg-card p-3.5 space-y-2 text-xs">
            <div className="flex justify-between items-center py-0.5">
              <span className="text-muted-foreground">WebSocket Endpoint</span>
              <code className="font-mono text-[11px] text-foreground">{wsEndpoint}</code>
            </div>
            <Separator />
            <div className="flex justify-between items-center py-0.5">
              <span className="text-muted-foreground">Protocol</span>
              <span className="font-mono text-[11px] text-foreground">compute.v1 (coder/websocket)</span>
            </div>
            <Separator />
            <div className="flex justify-between items-center py-0.5">
              <span className="text-muted-foreground">Update Mechanism</span>
              <span className="text-emerald-600 dark:text-emerald-400 font-medium">Event-driven live push</span>
            </div>
          </div>
        </CardContent>
      </Card>

      {/* 3. Host & Runtime Info */}
      <Card>
        <CardHeader>
          <div className="flex items-center gap-2">
            <Server className="h-4 w-4 text-primary" />
            <CardTitle>Daemon Runtime & Host Specs</CardTitle>
          </div>
          <CardDescription>
            Underlying virtualization environment and active allocation thresholds.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="rounded-lg border bg-card p-3.5 space-y-2 text-xs">
            <div className="flex justify-between items-center py-0.5">
              <span className="text-muted-foreground">Hypervisor</span>
              <span className="font-mono text-foreground">Firecracker v1.9+ (KVM)</span>
            </div>
            <Separator />
            <div className="flex justify-between items-center py-0.5">
              <span className="text-muted-foreground">Max Running Instances</span>
              <span className="font-mono text-foreground">
                {config?.resources.max_running_vms ?? 20}
              </span>
            </div>
            <Separator />
            <div className="flex justify-between items-center py-0.5">
              <span className="text-muted-foreground">Max Total vCPUs</span>
              <span className="font-mono text-foreground">
                {config?.resources.max_total_vcpus ?? 32} vCPU
              </span>
            </div>
            <Separator />
            <div className="flex justify-between items-center py-0.5">
              <span className="text-muted-foreground">Max Total Memory</span>
              <span className="font-mono text-foreground">
                {config?.resources.max_total_memory_mb
                  ? `${Math.round(config.resources.max_total_memory_mb / 1024)} GiB`
                  : '64 GiB'}
              </span>
            </div>
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
