import { useEffect, useMemo, useRef, useState } from 'react';
import {
  CornerDownLeft,
  ExternalLink,
  Loader2,
  Plus,
  Server,
  Terminal,
  Trash2,
} from 'lucide-react';
import { toast } from 'sonner';

import { CopyButton } from '@/components/copy-button';
import { EmptyState } from '@/components/empty-state';
import { StatusBadge } from '@/components/status-badge';
import { Button } from '@/components/ui/button';
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import { SSHTerminal } from '@/components/ssh-terminal';
import { api, errorMessage } from '@/lib/api';
import { openSSHTerminalWindow } from '@/lib/terminal';
import type { ExecResponse, VM } from '@/lib/types';
import { cn } from '@/lib/utils';

interface HistoryEntry {
  id: string;
  vmID: string;
  vmName: string;
  command: string;
  time: string;
  pending?: boolean;
  response?: ExecResponse;
  error?: string;
}

const PRESETS = [
  { label: 'Kernel', command: 'uname -a' },
  { label: 'Addresses', command: 'ip -4 addr show' },
  { label: 'Memory', command: 'free -m' },
  { label: 'Disk', command: 'df -h' },
  { label: 'Uptime', command: 'uptime' },
  { label: 'Processes', command: 'ps aux' },
  { label: 'OS release', command: 'cat /etc/os-release' },
];

export function ConsolePage({
  vms,
  selectedVmID,
  onSelectVM,
  onNew,
}: {
  vms: VM[];
  selectedVmID: string | null;
  onSelectVM: (id: string) => void;
  onNew: () => void;
}) {
  const [command, setCommand] = useState('uname -a');
  const [history, setHistory] = useState<HistoryEntry[]>([]);
  const [running, setRunning] = useState(false);
  const [historyIndex, setHistoryIndex] = useState(-1);
  const [viewMode, setViewMode] = useState<'interactive' | 'exec'>('interactive');
  const endRef = useRef<HTMLDivElement | null>(null);

  const activeVM = useMemo(
    () => vms.find((vm) => vm.id === selectedVmID) ?? null,
    [vms, selectedVmID],
  );
  const canExec = Boolean(activeVM && activeVM.status === 'running' && !running);

  useEffect(() => {
    endRef.current?.scrollIntoView({ behavior: 'smooth', block: 'end' });
  }, [history]);

  async function execute(rawCommand?: string) {
    const finalCommand = (rawCommand ?? command).trim();
    if (!finalCommand || !activeVM) return;

    if (activeVM.status !== 'running') {
      toast.error('The instance must be running to execute commands.');
      return;
    }

    const id = crypto.randomUUID();
    const entry: HistoryEntry = {
      id,
      vmID: activeVM.id,
      vmName: activeVM.name,
      command: finalCommand,
      time: new Date().toLocaleTimeString(),
      pending: true,
    };

    setHistory((current) => [...current, entry]);
    setHistoryIndex(-1);
    setRunning(true);

    try {
      const response = await api.execVM(activeVM.id, {
        command: finalCommand,
        timeout_seconds: 30,
      });
      setHistory((current) =>
        current.map((item) =>
          item.id === id ? { ...item, pending: false, response } : item,
        ),
      );
    } catch (error) {
      setHistory((current) =>
        current.map((item) =>
          item.id === id
            ? { ...item, pending: false, error: errorMessage(error) }
            : item,
        ),
      );
    } finally {
      setRunning(false);
    }
  }

  function onKeyDown(event: React.KeyboardEvent<HTMLInputElement>) {
    if (event.key === 'Enter') {
      event.preventDefault();
      void execute();
      return;
    }

    const commands = history.map((item) => item.command);
    if (event.key === 'ArrowUp' && commands.length > 0) {
      event.preventDefault();
      const next = Math.min(commands.length - 1, historyIndex + 1);
      setHistoryIndex(next);
      setCommand(commands[commands.length - 1 - next]);
    }
    if (event.key === 'ArrowDown' && historyIndex >= 0) {
      event.preventDefault();
      const next = historyIndex - 1;
      setHistoryIndex(next);
      setCommand(next >= 0 ? commands[commands.length - 1 - next] : '');
    }
  }

  const transcript = history
    .map((item) => {
      const output =
        item.response?.stdout ||
        item.response?.stderr ||
        item.error ||
        (item.pending ? '(pending)' : '');
      return `root@${item.vmName}:~$ ${item.command}\n${output}`;
    })
    .join('\n\n');

  return (
    <div className="grid gap-4 animate-fade-in lg:grid-cols-[280px_minmax(0,1fr)]">
      <div className="space-y-4">
        <Card>
          <CardHeader className="pb-3">
            <CardTitle>Target instance</CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            <Select
              value={activeVM?.id ?? ''}
              onValueChange={(value) => {
                onSelectVM(value);
                setHistoryIndex(-1);
              }}
            >
              <SelectTrigger aria-label="Select instance">
                <SelectValue placeholder="Select an instance" />
              </SelectTrigger>
              <SelectContent>
                {vms.map((vm) => (
                  <SelectItem key={vm.id} value={vm.id}>
                    {vm.name} · {vm.status.charAt(0).toUpperCase() + vm.status.slice(1)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>

            {activeVM ? (
              <>
                <div className="flex items-center justify-between gap-2">
                  <StatusBadge status={activeVM.status} />
                  <span className="font-mono text-[11px] text-muted-foreground">
                    {activeVM.id}
                  </span>
                </div>
                <div className="space-y-1.5 border-t pt-3 text-xs">
                  <div className="flex justify-between gap-2">
                    <span className="text-muted-foreground">Address</span>
                    <span className="font-mono">{activeVM.ip_address || '—'}</span>
                  </div>
                  <div className="flex justify-between gap-2">
                    <span className="text-muted-foreground">TAP</span>
                    <span className="font-mono">{activeVM.tap_device || '—'}</span>
                  </div>
                  <div className="flex justify-between gap-2">
                    <span className="text-muted-foreground">PID</span>
                    <span className="font-mono">{activeVM.pid || '—'}</span>
                  </div>
                </div>

                <div className="border-t pt-3">
                  <Button
                    type="button"
                    size="sm"
                    className="w-full gap-2 text-xs font-medium shadow-xs"
                    disabled={activeVM.status !== 'running'}
                    onClick={() => openSSHTerminalWindow(activeVM.id, activeVM.name)}
                  >
                    <Terminal className="h-3.5 w-3.5" />
                    <span>Open SSH Terminal (New Window)</span>
                    <ExternalLink className="h-3 w-3 ml-auto opacity-70" />
                  </Button>
                </div>
              </>
            ) : (
              <p className="text-xs text-muted-foreground">
                No instance selected. Choose one to open a command session.
              </p>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader className="pb-3">
            <CardTitle>Quick commands</CardTitle>
          </CardHeader>
          <CardContent className="grid gap-1">
            {PRESETS.map((preset) => (
              <Button
                key={preset.command}
                type="button"
                variant="ghost"
                className="h-auto justify-between px-2 py-1.5 font-normal"
                disabled={!canExec}
                onClick={() => {
                  setViewMode('exec');
                  setCommand(preset.command);
                  void execute(preset.command);
                }}
              >
                <span className="text-xs">{preset.label}</span>
                <span className="font-mono text-[10px] text-muted-foreground">
                  {preset.command}
                </span>
              </Button>
            ))}
          </CardContent>
        </Card>

        <p className="px-1 text-[11px] leading-relaxed text-muted-foreground">
          Interactive sessions run over native SSH PTY. You can open a full-featured terminal in a separate window or toggle command logs.
        </p>
      </div>

      <Card className="flex min-h-[560px] flex-col overflow-hidden">
        {/* Terminal Header */}
        <div className="flex flex-wrap items-center justify-between gap-2 border-b px-4 py-2.5 bg-card">
          <div className="flex min-w-0 items-center gap-2">
            <Terminal className="h-4 w-4 text-muted-foreground" />
            <span className="truncate font-mono text-xs font-medium">
              {activeVM ? `${activeVM.name} @ ${activeVM.ip_address || 'no-ip'}` : 'no-session'}
            </span>
          </div>

          <div className="flex items-center gap-2">
            {/* View Mode Toggle */}
            <div className="flex items-center rounded-md border bg-muted/40 p-0.5 text-[11px]">
              <button
                type="button"
                onClick={() => setViewMode('interactive')}
                className={cn(
                  'px-2.5 py-1 rounded transition-colors font-medium',
                  viewMode === 'interactive'
                    ? 'bg-background text-foreground shadow-xs'
                    : 'text-muted-foreground hover:text-foreground',
                )}
              >
                Interactive SSH
              </button>
              <button
                type="button"
                onClick={() => setViewMode('exec')}
                className={cn(
                  'px-2.5 py-1 rounded transition-colors font-medium',
                  viewMode === 'exec'
                    ? 'bg-background text-foreground shadow-xs'
                    : 'text-muted-foreground hover:text-foreground',
                )}
              >
                Command Runner
              </button>
            </div>

            {/* Open in New Window Button */}
            {activeVM && (
              <Button
                variant="default"
                size="sm"
                className="h-7 gap-1.5 text-xs font-medium"
                disabled={activeVM.status !== 'running'}
                onClick={() => openSSHTerminalWindow(activeVM.id, activeVM.name)}
                title="Open interactive SSH terminal in a new pop-up window"
              >
                <ExternalLink className="h-3.5 w-3.5" />
                <span>Open in New Window</span>
              </Button>
            )}

            {viewMode === 'exec' && (
              <>
                {history.length > 0 ? (
                  <CopyButton value={transcript} label="Copy transcript" />
                ) : null}
                <Button
                  variant="ghost"
                  size="icon-sm"
                  onClick={() => setHistory([])}
                  disabled={history.length === 0}
                  aria-label="Clear terminal"
                >
                  <Trash2 />
                </Button>
              </>
            )}
          </div>
        </div>

        {/* Terminal Content */}
        {vms.length === 0 ? (
          <EmptyState
            icon={Server}
            title="No instances available"
            description="Provision an instance first, then return here to open a console."
            action={
              <Button size="sm" onClick={onNew}>
                <Plus />
                New instance
              </Button>
            }
            className="flex-1 border-0 py-16"
          />
        ) : !activeVM ? (
          <div className="flex flex-1 items-center justify-center p-8 text-center text-xs text-muted-foreground">
            Select an instance on the left to start an SSH terminal session.
          </div>
        ) : viewMode === 'interactive' ? (
          activeVM.status !== 'running' ? (
            <div className="flex flex-1 flex-col items-center justify-center p-8 text-center space-y-3 bg-zinc-950">
              <div className="h-10 w-10 rounded-full bg-amber-500/10 flex items-center justify-center text-amber-500">
                <Terminal className="h-5 w-5" />
              </div>
              <div className="space-y-1">
                <p className="text-xs font-medium text-zinc-300">
                  Instance {activeVM.name} is currently {activeVM.status}
                </p>
                <p className="text-[11px] text-zinc-500 max-w-sm">
                  Start the instance from the Instances page to open an interactive SSH terminal session.
                </p>
              </div>
            </div>
          ) : (
            <div className="flex-1 min-h-[480px] bg-zinc-950">
              <SSHTerminal
                key={activeVM.id}
                vmId={activeVM.id}
                user="compute"
                className="h-full w-full min-h-[480px]"
              />
            </div>
          )
        ) : (
          /* Command Runner View */
          <>
            <div className="min-h-0 flex-1 overflow-y-auto bg-zinc-950 px-4 py-4 font-mono text-[12.5px] leading-relaxed text-zinc-300">
              <div className="border-b border-zinc-800 pb-3 text-[11px] text-zinc-500">
                Compute exec session. Commands run as the configured guest user.
              </div>

              {history.length === 0 ? (
                <div className="py-16 text-center text-xs text-zinc-500">
                  No output yet. Run a command below or choose a quick command.
                </div>
              ) : (
                <div className="space-y-4 pt-3">
                  {history.map((item) => (
                    <div key={item.id} className="group">
                      <div className="flex flex-wrap items-baseline gap-x-2 text-zinc-400">
                        <span className="text-primary">
                          root@{item.vmName}:~$
                        </span>
                        <span className="text-zinc-100">{item.command}</span>
                        <span className="ml-auto text-[10px] text-zinc-600">
                          {item.time}
                        </span>
                        {item.response ? (
                          <span
                            className={cn(
                              'rounded border px-1.5 py-0.5 text-[10px]',
                              item.response.exit_code === 0
                                ? 'border-emerald-500/25 bg-emerald-500/10 text-emerald-400'
                                : 'border-rose-500/25 bg-rose-500/10 text-rose-400',
                            )}
                          >
                            exit {item.response.exit_code} · {item.response.duration_ms}ms
                          </span>
                        ) : null}
                      </div>

                      {item.pending ? (
                        <div className="mt-1.5 flex items-center gap-2 text-zinc-500">
                          <Loader2 className="h-3.5 w-3.5 animate-spin" />
                          Waiting for guest…
                        </div>
                      ) : null}

                      {item.response?.stdout ? (
                        <pre className="mt-1.5 whitespace-pre-wrap break-words text-zinc-300">
                          {item.response.stdout}
                        </pre>
                      ) : null}

                      {item.response?.stderr ? (
                        <pre className="mt-1.5 whitespace-pre-wrap break-words text-rose-400">
                          {item.response.stderr}
                        </pre>
                      ) : null}

                      {item.error ? (
                        <pre className="mt-1.5 whitespace-pre-wrap break-words text-rose-400">
                          {item.error}
                        </pre>
                      ) : null}
                    </div>
                  ))}
                </div>
              )}
              <div ref={endRef} />
            </div>

            <div className="flex items-center gap-2 border-t p-3">
              <span className="pl-1 font-mono text-sm text-primary">$</span>
              <Input
                value={command}
                onChange={(event) => setCommand(event.target.value)}
                onKeyDown={onKeyDown}
                placeholder={
                  !activeVM
                    ? 'Select an instance to begin'
                    : activeVM.status !== 'running'
                      ? 'Instance is not running'
                      : 'Enter a command'
                }
                disabled={!activeVM || activeVM.status !== 'running'}
                className="h-9 flex-1 border-transparent bg-transparent font-mono text-xs shadow-none focus-visible:ring-0"
                aria-label="Command"
              />
              <Button
                type="button"
                size="sm"
                onClick={() => void execute()}
                disabled={!canExec || !command.trim()}
              >
                {running ? <Loader2 className="animate-spin" /> : <CornerDownLeft />}
                Run
              </Button>
            </div>
          </>
        )}
      </Card>
    </div>
  );
}
