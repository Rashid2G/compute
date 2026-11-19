import { useState } from 'react';
import { Play, Terminal, X, Zap } from 'lucide-react';
import { toast } from 'sonner';

import { api, errorMessage } from '@/lib/api';
import type { JobRecord, Template } from '@/lib/types';

interface RunJobDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  template: Template | null;
  onJobStarted?: (job: JobRecord) => void;
}

export function RunJobDialog({
  open,
  onOpenChange,
  template,
  onJobStarted,
}: RunJobDialogProps) {
  const [command, setCommand] = useState('echo "Hello from template!" && uname -a');
  const [cwd, setCwd] = useState('/workspace');
  const [timeout, setTimeoutSec] = useState(900);
  const [submitting, setSubmitting] = useState(false);

  if (!open || !template) return null;

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!template) return;
    const cleanCmd = command.trim();
    if (!cleanCmd) {
      toast.error('Command cannot be empty');
      return;
    }

    setSubmitting(true);
    const toastId = toast.loading(`Starting job with template "${template.name}"…`);
    onOpenChange(false);

    try {
      const job = await api.createJob({
        type: 'command',
        source: 'template-launch',
        spec: {
          image_id: template.name,
          command: ['bash', '-c', cleanCmd],
          cwd: cwd.trim() || '/workspace',
          timeout_seconds: timeout,
          vcpus: template.vcpus || 2,
          memory_mb: template.memory_mb || 2048,
          disk_mb: template.disk_mb || 10240,
        },
      });
      toast.success(`Job ${job.id} dispatched with template "${template.name}"`, { id: toastId });
      onJobStarted?.(job);
    } catch (err) {
      toast.error(`Failed to dispatch job: ${errorMessage(err)}`, { id: toastId });
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/75 p-4"
      onClick={() => onOpenChange(false)}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="run-job-title"
        className="w-full max-w-[480px] border border-border bg-background p-6 text-foreground shadow-2xl space-y-5"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-start justify-between">
          <div className="space-y-1">
            <div className="flex items-center gap-2">
              <Zap className="h-5 w-5 text-primary" />
              <h2 id="run-job-title" className="text-[17px] font-semibold text-foreground">
                Launch Job from Template
              </h2>
            </div>
            <p className="text-[12px] text-muted-foreground">
              Run an ephemeral CI/workload job inside a microVM booted from{' '}
              <span className="font-mono font-medium text-foreground">{template.name}</span>.
            </p>
          </div>
          <button
            type="button"
            onClick={() => onOpenChange(false)}
            className="flex h-7 w-7 items-center justify-center text-muted-foreground hover:text-foreground"
          >
            <X className="h-4 w-4" />
          </button>
        </div>

        <form onSubmit={handleSubmit} className="space-y-4 text-[12.5px]">
          <div className="space-y-1.5">
            <label className="text-[11.5px] font-medium text-foreground flex items-center gap-1.5">
              <Terminal className="h-3.5 w-3.5 text-muted-foreground" />
              <span>Job Command</span>
            </label>
            <textarea
              rows={3}
              required
              value={command}
              onChange={(e) => setCommand(e.target.value)}
              placeholder="e.g. pytest tests/ or npm test"
              className="w-full border border-border bg-background p-2.5 font-mono text-[11.5px] text-foreground focus:border-primary focus:outline-none"
            />
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-1.5">
              <label className="text-[11.5px] font-medium text-foreground">
                Working Directory
              </label>
              <input
                type="text"
                value={cwd}
                onChange={(e) => setCwd(e.target.value)}
                placeholder="/workspace"
                className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[11.5px] text-foreground focus:border-primary focus:outline-none"
              />
            </div>
            <div className="space-y-1.5">
              <label className="text-[11.5px] font-medium text-foreground">
                Timeout (seconds)
              </label>
              <input
                type="number"
                min={10}
                max={3600}
                value={timeout}
                onChange={(e) => setTimeoutSec(Number(e.target.value))}
                className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[11.5px] text-foreground focus:border-primary focus:outline-none"
              />
            </div>
          </div>

          <div className="border border-border bg-muted/10 p-3 text-[11.5px] text-muted-foreground space-y-1">
            <div className="font-medium text-foreground">Template Specifications:</div>
            <div>
              {template.vcpus} vCPUs · {template.memory_mb} MB RAM · {template.disk_mb ? `${template.disk_mb} MB` : '50 GB'} Disk
            </div>
          </div>

          <div className="flex items-center justify-end gap-2 pt-2 border-t border-border">
            <button
              type="button"
              disabled={submitting}
              onClick={() => onOpenChange(false)}
              className="h-8 border border-border px-3 text-[12px] font-normal text-muted-foreground hover:border-foreground hover:text-foreground transition-colors disabled:opacity-50"
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={submitting}
              className="flex h-8 items-center gap-1.5 bg-primary hover:bg-primary-hover px-4 text-[12px] font-medium text-primary-foreground transition-colors disabled:opacity-50"
            >
              <Play className="h-3.5 w-3.5 fill-current" />
              <span>Launch Job</span>
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
