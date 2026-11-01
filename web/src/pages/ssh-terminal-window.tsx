import { useEffect, useState, useMemo, useCallback } from 'react';
import {
  Terminal as TerminalIcon,
  Copy,
  Check,
  Moon,
  Sun,
  X,
  Play,
  RotateCw,
  ExternalLink,
  Loader2,
} from 'lucide-react';
import { Button } from '@/components/ui/button';
import { SSHTerminal } from '@/components/ssh-terminal';
import { StatusBadge } from '@/components/status-badge';
import { api, errorMessage } from '@/lib/api';
import type { VM } from '@/lib/types';
import { toast } from 'sonner';

interface SSHTerminalWindowProps {
  vmId: string;
  vms?: VM[];
  theme?: 'dark' | 'light';
  onToggleTheme?: () => void;
}

export function SSHTerminalWindow({
  vmId,
  vms = [],
  theme = 'dark',
  onToggleTheme,
}: SSHTerminalWindowProps) {
  const [vm, setVM] = useState<VM | null>(() => vms.find((v) => v.id === vmId) ?? null);
  const [loading, setLoading] = useState(!vm);
  const [starting, setStarting] = useState(false);
  const [currentUser, setCurrentUser] = useState<'compute' | 'root'>('compute');
  const [copied, setCopied] = useState(false);
  const [reconnectKey, setReconnectKey] = useState(0);

  const fetchVM = useCallback(async () => {
    try {
      const data = await api.getVM(vmId);
      setVM(data);
    } catch (err) {
      toast.error('Failed to load VM details: ' + errorMessage(err));
    } finally {
      setLoading(false);
    }
  }, [vmId]);

  useEffect(() => {
    void fetchVM();
    const interval = setInterval(fetchVM, 4000);
    return () => clearInterval(interval);
  }, [fetchVM]);

  const sshCommand = useMemo(() => {
    if (!vm) return `ssh ${currentUser}@<ip>`;
    const ip = vm.ip_address || vm.public_ip || '<ip>';
    return `ssh ${currentUser}@${ip}`;
  }, [vm, currentUser]);

  const copySSH = useCallback(() => {
    void navigator.clipboard.writeText(sshCommand);
    setCopied(true);
    toast.success('SSH command copied to clipboard');
    setTimeout(() => setCopied(false), 2000);
  }, [sshCommand]);

  const startInstance = useCallback(async () => {
    if (!vm) return;
    setStarting(true);
    try {
      await api.runVM(vm.id);
      toast.success(`Instance ${vm.name} started`);
      await fetchVM();
      setReconnectKey((k) => k + 1);
    } catch (err) {
      toast.error('Failed to start instance: ' + errorMessage(err));
    } finally {
      setStarting(false);
    }
  }, [vm, fetchVM]);

  const isRunning = vm?.status === 'running';

  return (
    <div className="flex flex-col h-screen w-screen bg-background text-foreground overflow-hidden select-none font-sans">
      {/* Top Window Bar */}
      <header className="flex h-12 items-center justify-between border-b bg-card/80 px-3.5 backdrop-blur-md shrink-0">
        <div className="flex items-center gap-2.5 min-w-0">
          <div className="flex h-7 w-7 items-center justify-center rounded-md bg-primary/10 text-primary">
            <TerminalIcon className="h-4 w-4" />
          </div>

          <div className="flex items-baseline gap-2 min-w-0">
            <span className="font-semibold text-xs truncate max-w-[160px] sm:max-w-[240px]">
              {vm?.name || vmId}
            </span>
            {vm ? <StatusBadge status={vm.status} /> : null}
          </div>

          <span className="hidden sm:inline-block font-mono text-[11px] text-muted-foreground">
            {vm?.ip_address || vm?.public_ip || 'no-ip'}
          </span>
        </div>

        {/* Center / Right controls */}
        <div className="flex items-center gap-1.5">
          {/* User toggle */}
          <div className="flex items-center rounded-md border bg-muted/30 p-0.5 text-[11px]">
            <button
              type="button"
              onClick={() => setCurrentUser('compute')}
              className={`px-2 py-0.5 rounded font-mono transition-colors ${
                currentUser === 'compute'
                  ? 'bg-background text-foreground shadow-xs font-semibold'
                  : 'text-muted-foreground hover:text-foreground'
              }`}
            >
              compute
            </button>
            <button
              type="button"
              onClick={() => setCurrentUser('root')}
              className={`px-2 py-0.5 rounded font-mono transition-colors ${
                currentUser === 'root'
                  ? 'bg-background text-foreground shadow-xs font-semibold'
                  : 'text-muted-foreground hover:text-foreground'
              }`}
            >
              root
            </button>
          </div>

          {/* Copy SSH command */}
          <Button
            variant="outline"
            size="sm"
            onClick={copySSH}
            className="h-7 px-2 text-xs gap-1 font-mono hidden md:inline-flex"
            title="Copy SSH command"
          >
            {copied ? <Check className="h-3.5 w-3.5 text-emerald-500" /> : <Copy className="h-3.5 w-3.5" />}
            <span>{sshCommand}</span>
          </Button>

          {/* Native SSH protocol link */}
          {vm?.ip_address && (
            <Button
              variant="ghost"
              size="icon-xs"
              asChild
              className="h-7 w-7"
              title="Open in native desktop SSH client (ssh://)"
            >
              <a href={`ssh://${currentUser}@${vm.ip_address}`} target="_blank" rel="noopener noreferrer">
                <ExternalLink className="h-3.5 w-3.5 text-muted-foreground hover:text-foreground" />
              </a>
            </Button>
          )}

          {/* Reconnect button */}
          <Button
            variant="ghost"
            size="icon-xs"
            onClick={() => setReconnectKey((k) => k + 1)}
            disabled={!isRunning}
            className="h-7 w-7"
            title="Reconnect terminal session"
          >
            <RotateCw className="h-3.5 w-3.5 text-muted-foreground hover:text-foreground" />
          </Button>

          {/* Theme toggle */}
          {onToggleTheme && (
            <Button
              variant="ghost"
              size="icon-xs"
              onClick={onToggleTheme}
              className="h-7 w-7"
              title="Toggle theme"
            >
              {theme === 'dark' ? (
                <Sun className="h-3.5 w-3.5 text-muted-foreground" />
              ) : (
                <Moon className="h-3.5 w-3.5 text-muted-foreground" />
              )}
            </Button>
          )}

          {/* Close button */}
          <Button
            variant="ghost"
            size="icon-xs"
            onClick={() => window.close()}
            className="h-7 w-7 text-muted-foreground hover:text-destructive"
            title="Close window"
          >
            <X className="h-4 w-4" />
          </Button>
        </div>
      </header>

      {/* Main Content Area */}
      <div className="flex-1 min-h-0 relative bg-zinc-950">
        {loading ? (
          <div className="flex h-full items-center justify-center text-xs text-muted-foreground gap-2">
            <Loader2 className="h-4 w-4 animate-spin text-primary" />
            Loading instance details...
          </div>
        ) : !isRunning ? (
          <div className="flex flex-col h-full items-center justify-center p-6 text-center space-y-4">
            <div className="h-12 w-12 rounded-full bg-amber-500/10 flex items-center justify-center text-amber-500">
              <TerminalIcon className="h-6 w-6" />
            </div>
            <div className="space-y-1.5 max-w-sm">
              <h3 className="font-semibold text-sm">Instance is not running</h3>
              <p className="text-xs text-muted-foreground leading-relaxed">
                MicroVM <span className="font-mono font-medium text-foreground">{vm?.name || vmId}</span> is currently{' '}
                <span className="font-semibold text-amber-400">{vm?.status || 'stopped'}</span>. Start it to open an SSH
                session.
              </p>
            </div>
            <Button
              size="sm"
              onClick={startInstance}
              disabled={starting}
              className="gap-1.5 font-medium"
            >
              {starting ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Play className="h-3.5 w-3.5 fill-current" />}
              {starting ? 'Starting VM...' : 'Start Instance & Connect'}
            </Button>
          </div>
        ) : (
          <SSHTerminal
            key={`${vmId}-${currentUser}-${reconnectKey}`}
            vmId={vmId}
            user={currentUser}
            theme={theme}
            className="h-full w-full"
            autoFocus
          />
        )}
      </div>
    </div>
  );
}
