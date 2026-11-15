import { useState } from 'react';
import { ExternalLink, Play, Terminal } from 'lucide-react';
import { toast } from 'sonner';

import { SSHTerminal } from '@/components/ssh-terminal';
import { api, errorMessage } from '@/lib/api';
import type { VM } from '@/lib/types';
import { cn } from '@/lib/utils';

interface ConsolePageProps {
  vms: VM[];
  selectedVmID: string | null;
  onSelectVM: (id: string) => void;
  onNew: () => void;
}

export function ConsolePage({
  vms,
  selectedVmID,
  onSelectVM,
  onNew,
}: ConsolePageProps) {
  const [mode, setMode] = useState<'pty' | 'exec'>('pty');
  const [commandInput, setCommandInput] = useState('uname -a && free -m && df -h /');
  const [running, setRunning] = useState(false);
  const [execLog, setExecLog] = useState<
    Array<{
      cmd: string;
      stdout: string;
      stderr: string;
      exitCode: number;
      durationMs: number;
    }>
  >([]);

  const selectedVM =
    vms.find((v) => v.id === selectedVmID) ?? vms[0] ?? null;

  async function handleRunExec(e: React.FormEvent) {
    e.preventDefault();
    if (!selectedVM || !commandInput.trim()) return;
    const cmd = commandInput.trim();
    setRunning(true);
    try {
      const res = await api.execVM(selectedVM.id, {
        command: cmd,
        timeout: 30,
      });
      setExecLog((prev) => [
        ...prev,
        {
          cmd,
          stdout: res.stdout,
          stderr: res.stderr,
          exitCode: res.exit_code,
          durationMs: res.duration_ms,
        },
      ]);
    } catch (err) {
      toast.error(errorMessage(err));
    } finally {
      setRunning(false);
    }
  }

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="font-display text-[22px] font-medium tracking-tight text-foreground">
            Console &amp; SSH
          </h1>
          <p className="text-[12px] text-muted-foreground">
            Interactive WebSocket SSH PTY terminal and one-shot remote command
            execution.
          </p>
        </div>

        <div className="flex items-center gap-2">
          {vms.length > 0 && (
            <select
              aria-label="Target sandbox"
              value={selectedVM?.id ?? ''}
              onChange={(e) => onSelectVM(e.target.value)}
              className="h-8 border border-border bg-background px-2.5 font-mono text-[12px] text-foreground focus:border-primary focus:outline-none"
            >
              {vms.map((vm) => (
                <option key={vm.id} value={vm.id}>
                  {vm.name} ({vm.ip_address || 'no-ip'}) · {vm.status}
                </option>
              ))}
            </select>
          )}

          {selectedVM && (
            <a
              href={`#/terminal/${encodeURIComponent(selectedVM.id)}`}
              target="_blank"
              rel="noreferrer"
              className="flex h-8 items-center gap-1.5 border border-border bg-background px-2.5 text-[12px] text-foreground hover:border-primary"
            >
              <span>Popout</span>
              <ExternalLink className="h-3 w-3" />
            </a>
          )}

          <button
            type="button"
            onClick={onNew}
            className="flex h-8 items-center gap-1.5 bg-primary hover:bg-primary-hover px-3 text-[12px] font-medium text-primary-foreground"
          >
            <Play className="h-3 w-3 fill-current" />
            <span>New Sandbox</span>
          </button>
        </div>
      </div>

      {!selectedVM ? (
        <div className="border border-border bg-background py-20 text-center space-y-3">
          <Terminal className="mx-auto h-8 w-8 text-muted-foreground/50" />
          <p className="text-[12.5px] text-muted-foreground">
            No sandboxes provisioned yet. Create a sandbox to open a live SSH
            PTY session.
          </p>
          <button
            type="button"
            onClick={onNew}
            className="inline-flex h-8 items-center gap-1.5 bg-primary hover:bg-primary-hover px-3.5 text-[12px] font-medium text-primary-foreground"
          >
            <Play className="h-3 w-3 fill-current" />
            <span>New Sandbox</span>
          </button>
        </div>
      ) : (
        <div className="space-y-4">
          <div className="flex items-center justify-between border-b border-border">
            <div className="flex items-center">
              <button
                type="button"
                onClick={() => setMode('pty')}
                className={cn(
                  'border-b-2 px-4 py-2 text-[12.5px] transition-colors',
                  mode === 'pty'
                    ? 'border-primary text-foreground font-medium'
                    : 'border-transparent text-muted-foreground hover:text-foreground',
                )}
              >
                Interactive WebSocket PTY (/api/v1/vms/{selectedVM.id}/terminal)
              </button>
              <button
                type="button"
                onClick={() => setMode('exec')}
                className={cn(
                  'border-b-2 px-4 py-2 text-[12.5px] transition-colors',
                  mode === 'exec'
                    ? 'border-primary text-foreground font-medium'
                    : 'border-transparent text-muted-foreground hover:text-foreground',
                )}
              >
                Non-Interactive Exec (/api/v1/vms/{selectedVM.id}/exec)
              </button>
            </div>

            <span className="font-mono text-[11px] text-muted-foreground">
              ssh compute@{selectedVM.ip_address || '172.16.0.2'}
            </span>
          </div>

          {mode === 'pty' ? (
            <div className="h-[460px] border border-border bg-[#0a0a0b]">
              <SSHTerminal
                vmId={selectedVM.id}
                user="compute"
                theme="dark"
                className="h-full"
              />
            </div>
          ) : (
            <div className="border border-border bg-[#0a0a0b] text-[#f5f5f4]">
              <form
                onSubmit={handleRunExec}
                className="flex items-center border-b border-[#27272a] bg-[#131315] px-3 py-2 gap-2 font-mono text-[12px]"
              >
                <span className="text-[color:var(--link)] shrink-0">
                  compute@{selectedVM.name}:~$
                </span>
                <input
                  type="text"
                  value={commandInput}
                  onChange={(e) => setCommandInput(e.target.value)}
                  placeholder="Enter command to execute via SSH…"
                  className="flex-1 bg-transparent text-[#f5f5f4] focus:outline-none"
                />
                <button
                  type="submit"
                  disabled={running || selectedVM.status !== 'running'}
                  className="bg-primary hover:bg-primary-hover px-3 py-1 text-[11px] text-primary-foreground disabled:opacity-50"
                >
                  {running ? 'Running…' : 'Execute'}
                </button>
              </form>

              <div className="h-[380px] overflow-y-auto p-4 font-mono text-[11.5px] space-y-3">
                {execLog.length === 0 ? (
                  <div className="text-muted-foreground">
                    {selectedVM.status === 'running'
                      ? 'Press Execute to run command on guest over SSH.'
                      : `Sandbox "${selectedVM.name}" is currently ${selectedVM.status}. Start the sandbox to execute commands.`}
                  </div>
                ) : (
                  execLog.map((entry, idx) => (
                    <div
                      key={idx}
                      className="space-y-1 border-b border-[#27272a] pb-2"
                    >
                      <div className="flex items-center justify-between text-[#fb923c]">
                        <span>$ {entry.cmd}</span>
                        <span className="text-[10.5px]">
                          exit={entry.exitCode} · {entry.durationMs}ms
                        </span>
                      </div>
                      {entry.stdout && (
                        <pre className="whitespace-pre-wrap text-[#f5f5f4]">
                          {entry.stdout}
                        </pre>
                      )}
                      {entry.stderr && (
                        <pre className="whitespace-pre-wrap text-[#ef4444]">
                          {entry.stderr}
                        </pre>
                      )}
                    </div>
                  ))
                )}
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
