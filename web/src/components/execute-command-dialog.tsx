import { useCallback, useEffect, useRef, useState } from 'react';
import {
  Check,
  Clock,
  Copy,
  Play,
  RotateCcw,
  Square,
  Terminal,
  Trash2,
  X,
} from 'lucide-react';
import { toast } from 'sonner';

import { api, errorMessage } from '@/lib/api';
import type { VM } from '@/lib/types';
import { cn } from '@/lib/utils';

interface ExecuteCommandDialogProps {
  vm: VM | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  initialCommand?: string;
  autoRun?: boolean;
}

interface OutputLine {
  id: string;
  stream: 'stdout' | 'stderr' | 'system';
  data: string;
}

const PRESETS = [
  { label: 'System info', cmd: 'uname -a && uptime' },
  { label: 'Disk & memory', cmd: 'df -h && free -m' },
  { label: 'Top processes', cmd: 'ps aux --sort=-%mem | head -n 10' },
  { label: 'Network', cmd: 'ip -br a && ping -c 2 8.8.8.8' },
  { label: 'User & env', cmd: 'id && whoami && pwd' },
];

export function ExecuteCommandDialog({
  vm,
  open,
  onOpenChange,
  initialCommand = '',
  autoRun = false,
}: ExecuteCommandDialogProps) {
  const [command, setCommand] = useState(initialCommand);
  const [user, setUser] = useState<'compute' | 'root'>('compute');
  const [lines, setLines] = useState<OutputLine[]>([]);
  const [isRunning, setIsRunning] = useState(false);
  const [exitCode, setExitCode] = useState<number | null>(null);
  const [durationMs, setDurationMs] = useState<number | null>(null);
  const [copied, setCopied] = useState(false);
  const [history, setHistory] = useState<string[]>([]);
  const [startingVM, setStartingVM] = useState(false);

  const abortControllerRef = useRef<AbortController | null>(null);
  const terminalEndRef = useRef<HTMLDivElement | null>(null);
  const inputRef = useRef<HTMLInputElement | null>(null);

  useEffect(() => {
    if (open) {
      if (initialCommand) {
        setCommand(initialCommand);
      }
      setTimeout(() => {
        inputRef.current?.focus();
      }, 50);
    } else {
      if (abortControllerRef.current) {
        abortControllerRef.current.abort();
        abortControllerRef.current = null;
      }
      setIsRunning(false);
    }
  }, [open, initialCommand]);

  useEffect(() => {
    terminalEndRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [lines]);

  const handleRun = useCallback(async (cmdToRun?: string) => {
    const rawCmd = (cmdToRun ?? command).trim();
    if (!rawCmd || !vm) return;

    if (abortControllerRef.current) {
      abortControllerRef.current.abort();
    }
    const ac = new AbortController();
    abortControllerRef.current = ac;

    setLines([
      {
        id: `sys-${Date.now()}`,
        stream: 'system',
        data: `[execute] ${user}@${vm.name || vm.id}: $ ${rawCmd}`,
      },
    ]);
    setIsRunning(true);
    setExitCode(null);
    setDurationMs(null);

    setHistory((prev) => {
      const next = [rawCmd, ...prev.filter((c) => c !== rawCmd)];
      return next.slice(0, 10);
    });

    const startTime = performance.now();

    try {
      await api.execVMStream(
        vm.id,
        { command: rawCmd, user },
        (ev) => {
          if (ev.data) {
            setLines((prev) => [
              ...prev,
              {
                id: `line-${Date.now()}-${Math.random().toString(36).slice(2, 6)}`,
                stream: ev.stream === 'stderr' ? 'stderr' : 'stdout',
                data: ev.data || '',
              },
            ]);
          }
          if (ev.error) {
            setLines((prev) => [
              ...prev,
              {
                id: `err-${Date.now()}`,
                stream: 'stderr',
                data: `[error] ${ev.error}`,
              },
            ]);
          }
          if (ev.exit_code !== undefined && ev.exit_code !== null) {
            setExitCode(ev.exit_code);
          }
        },
        ac.signal,
      );

      const elapsed = Math.round(performance.now() - startTime);
      setDurationMs(elapsed);
    } catch (err: unknown) {
      if (ac.signal.aborted) {
        setLines((prev) => [
          ...prev,
          {
            id: `sys-abort-${Date.now()}`,
            stream: 'system',
            data: '[aborted by user]',
          },
        ]);
      } else {
        const msg = errorMessage(err);
        setLines((prev) => [
          ...prev,
          {
            id: `err-catch-${Date.now()}`,
            stream: 'stderr',
            data: `[execution failure] ${msg}`,
          },
        ]);
        setExitCode(1);
      }
    } finally {
      setIsRunning(false);
      abortControllerRef.current = null;
    }
  }, [command, user, vm]);

  // Auto-run if requested on open
  useEffect(() => {
    if (open && autoRun && initialCommand && vm?.status === 'running') {
      void handleRun(initialCommand);
    }
  }, [open, autoRun, initialCommand, vm?.status, handleRun]);

  const handleStop = () => {
    if (abortControllerRef.current) {
      abortControllerRef.current.abort();
      abortControllerRef.current = null;
    }
    setIsRunning(false);
  };

  const handleCopyOutput = () => {
    const fullText = lines.map((l) => l.data).join('');
    if (!fullText) return;
    navigator.clipboard.writeText(fullText).then(() => {
      setCopied(true);
      toast.success('Command output copied');
      setTimeout(() => setCopied(false), 2000);
    });
  };

  const handleClearOutput = () => {
    setLines([]);
    setExitCode(null);
    setDurationMs(null);
  };

  const handleStartSandbox = async () => {
    if (!vm) return;
    setStartingVM(true);
    try {
      await api.runVM(vm.id);
      toast.success(`Sandbox ${vm.name || vm.id} is starting…`);
    } catch (err) {
      toast.error(`Could not start sandbox: ${errorMessage(err)}`);
    } finally {
      setStartingVM(false);
    }
  };

  if (!open || !vm) return null;

  const isVMRunning = vm.status === 'running';

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/75 p-4 overflow-y-auto"
      onClick={() => onOpenChange(false)}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="exec-dialog-title"
        className="w-full max-w-[700px] border border-border bg-background p-5 text-foreground shadow-2xl my-auto flex flex-col max-h-[92vh]"
        onClick={(e) => e.stopPropagation()}
      >
        {/* Header */}
        <div className="flex items-center justify-between pb-3 border-b border-border">
          <div className="flex items-center gap-2.5">
            <div className="flex h-7 w-7 items-center justify-center bg-primary/10 text-primary border border-primary/20">
              <Terminal className="h-3.5 w-3.5" />
            </div>
            <div>
              <div className="flex items-center gap-2">
                <h2
                  id="exec-dialog-title"
                  className="text-[14px] font-medium text-foreground tracking-tight"
                >
                  Execute Command
                </h2>
                <span className="font-mono text-[11px] text-muted-foreground px-1.5 py-0.2 border border-border bg-muted/30">
                  {vm.name || vm.id}
                </span>
                <span
                  className={cn(
                    'text-[10px] font-mono px-1.5 py-0.2 border',
                    isVMRunning
                      ? 'border-emerald-500/30 bg-emerald-500/10 text-emerald-400'
                      : 'border-amber-500/30 bg-amber-500/10 text-amber-400',
                  )}
                >
                  {vm.status}
                </span>
              </div>
              <p className="text-[11px] text-muted-foreground mt-0.5">
                Send commands to the machine and inspect feedback in real time.
              </p>
            </div>
          </div>
          <button
            type="button"
            onClick={() => onOpenChange(false)}
            className="text-muted-foreground hover:text-foreground p-1 transition-colors"
            aria-label="Close dialog"
          >
            <X className="h-4 w-4" />
          </button>
        </div>

        {/* Warning if VM stopped */}
        {!isVMRunning && (
          <div className="my-3 flex items-center justify-between bg-amber-500/10 border border-amber-500/30 p-2.5 text-[11.5px] text-amber-300">
            <span>
              This sandbox is currently <strong>{vm.status}</strong>. It must be running to execute commands.
            </span>
            <button
              type="button"
              disabled={startingVM}
              onClick={handleStartSandbox}
              className="inline-flex items-center gap-1 bg-amber-500 text-black px-2 py-1 text-[11px] font-medium hover:bg-amber-400 transition-colors disabled:opacity-50"
            >
              <Play className="h-3 w-3" />
              <span>{startingVM ? 'Starting…' : 'Start Sandbox'}</span>
            </button>
          </div>
        )}

        {/* User selector & Command input */}
        <div className="py-3 space-y-2.5">
          <div className="flex items-center justify-between">
            <label className="text-[11.5px] text-muted-foreground font-medium">
              Command
            </label>
            <div className="flex items-center gap-1 text-[11px]">
              <span className="text-muted-foreground mr-1">User:</span>
              <button
                type="button"
                onClick={() => setUser('compute')}
                className={cn(
                  'px-2 py-0.5 font-mono text-[10.5px] border transition-colors',
                  user === 'compute'
                    ? 'border-primary bg-primary/10 text-primary font-medium'
                    : 'border-border text-muted-foreground hover:text-foreground',
                )}
              >
                compute (sudo)
              </button>
              <button
                type="button"
                onClick={() => setUser('root')}
                className={cn(
                  'px-2 py-0.5 font-mono text-[10.5px] border transition-colors',
                  user === 'root'
                    ? 'border-primary bg-primary/10 text-primary font-medium'
                    : 'border-border text-muted-foreground hover:text-foreground',
                )}
              >
                root
              </button>
            </div>
          </div>

          <div className="flex gap-2">
            <div className="relative flex-1">
              <input
                ref={inputRef}
                type="text"
                value={command}
                onChange={(e) => setCommand(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && !isRunning) {
                    e.preventDefault();
                    void handleRun();
                  }
                }}
                placeholder="e.g. uname -a && uptime"
                disabled={isRunning}
                className="w-full h-9 bg-black/60 border border-border px-3 font-mono text-[12.5px] text-foreground placeholder:text-muted-foreground/60 focus:border-primary focus:outline-none"
              />
            </div>
            {isRunning ? (
              <button
                type="button"
                onClick={handleStop}
                className="h-9 px-3.5 bg-destructive text-destructive-foreground hover:bg-destructive/90 text-[12px] font-medium flex items-center gap-1.5 transition-colors"
              >
                <Square className="h-3 w-3" />
                <span>Stop</span>
              </button>
            ) : (
              <button
                type="button"
                onClick={() => void handleRun()}
                disabled={!command.trim() || !isVMRunning}
                className="h-9 px-4 bg-primary text-primary-foreground hover:bg-primary-hover text-[12px] font-medium flex items-center gap-1.5 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
              >
                <Play className="h-3 w-3" />
                <span>Execute</span>
              </button>
            )}
          </div>

          {/* Quick presets */}
          <div className="flex flex-wrap items-center gap-1.5 pt-0.5">
            <span className="text-[10px] text-muted-foreground mr-0.5">Presets:</span>
            {PRESETS.map((p) => (
              <button
                key={p.cmd}
                type="button"
                onClick={() => {
                  setCommand(p.cmd);
                  if (isVMRunning && !isRunning) {
                    void handleRun(p.cmd);
                  }
                }}
                className="px-2 py-0.5 text-[10.5px] font-mono border border-border/80 hover:border-primary hover:text-foreground text-muted-foreground bg-muted/20 transition-colors"
              >
                {p.label}
              </button>
            ))}
          </div>

          {/* Recent History */}
          {history.length > 0 && (
            <div className="flex flex-wrap items-center gap-1 pt-0.5">
              <span className="text-[10px] text-muted-foreground mr-0.5">Recent:</span>
              {history.slice(0, 4).map((hCmd, i) => (
                <button
                  key={`${hCmd}-${i}`}
                  type="button"
                  onClick={() => setCommand(hCmd)}
                  className="px-1.5 py-0.5 text-[10px] font-mono text-muted-foreground hover:text-foreground hover:underline truncate max-w-[150px]"
                  title={hCmd}
                >
                  {hCmd}
                </button>
              ))}
            </div>
          )}
        </div>

        {/* Console / Output Terminal */}
        <div className="flex-1 min-h-[220px] max-h-[400px] flex flex-col border border-border bg-[#0d1117] text-[#c9d1d9] font-mono text-[11.5px] overflow-hidden">
          {/* Terminal toolbar */}
          <div className="flex items-center justify-between px-3 py-1.5 bg-[#161b22] border-b border-[#30363d] select-none text-[11px]">
            <div className="flex items-center gap-2">
              <span className="text-muted-foreground">Output:</span>
              {isRunning && (
                <span className="flex items-center gap-1 text-primary">
                  <span className="h-2 w-2 rounded-full bg-primary animate-pulse" />
                  Running…
                </span>
              )}
              {exitCode !== null && (
                <span
                  className={cn(
                    'px-1.5 py-0.2 text-[10px] font-mono border',
                    exitCode === 0
                      ? 'border-emerald-500/40 bg-emerald-500/10 text-emerald-400'
                      : 'border-rose-500/40 bg-rose-500/10 text-rose-400',
                  )}
                >
                  Exit {exitCode}
                </span>
              )}
              {durationMs !== null && (
                <span className="flex items-center gap-1 text-muted-foreground text-[10.5px]">
                  <Clock className="h-2.5 w-2.5" />
                  {durationMs}ms
                </span>
              )}
            </div>

            <div className="flex items-center gap-1.5">
              <button
                type="button"
                onClick={handleCopyOutput}
                disabled={lines.length === 0}
                className="p-1 hover:text-foreground text-muted-foreground transition-colors disabled:opacity-40"
                title="Copy output"
                aria-label="Copy output"
              >
                {copied ? <Check className="h-3 w-3 text-emerald-400" /> : <Copy className="h-3 w-3" />}
              </button>
              <button
                type="button"
                onClick={handleClearOutput}
                disabled={lines.length === 0}
                className="p-1 hover:text-foreground text-muted-foreground transition-colors disabled:opacity-40"
                title="Clear output"
                aria-label="Clear output"
              >
                <Trash2 className="h-3 w-3" />
              </button>
            </div>
          </div>

          {/* Lines stream */}
          <div className="flex-1 p-3 overflow-y-auto space-y-0.5 select-text">
            {lines.length === 0 ? (
              <div className="h-full flex items-center justify-center text-muted-foreground/50 py-8 select-none">
                <span>Output will appear here after execution.</span>
              </div>
            ) : (
              lines.map((l) => (
                <div
                  key={l.id}
                  className={cn(
                    'whitespace-pre-wrap break-all leading-relaxed',
                    l.stream === 'stderr'
                      ? 'text-[#f85149]'
                      : l.stream === 'system'
                        ? 'text-[#58a6ff]'
                        : 'text-[#e6edf3]',
                  )}
                >
                  {l.data}
                </div>
              ))
            )}
            <div ref={terminalEndRef} />
          </div>
        </div>

        {/* Footer */}
        <div className="flex items-center justify-between pt-3 border-t border-border mt-3 text-[11px] text-muted-foreground">
          <span>Commands execute over isolated Firecracker guest SSH.</span>
          <div className="flex items-center gap-2">
            <button
              type="button"
              onClick={() => onOpenChange(false)}
              className="px-3 py-1 border border-border hover:text-foreground transition-colors"
            >
              Close
            </button>
            <button
              type="button"
              onClick={() => void handleRun()}
              disabled={!command.trim() || !isVMRunning || isRunning}
              className="px-3.5 py-1 bg-primary text-primary-foreground hover:bg-primary-hover font-medium transition-colors disabled:opacity-50"
            >
              Re-run
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}
