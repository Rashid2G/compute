import { useCallback, useEffect, useState } from 'react';
import {
  ChevronDown,
  GitBranch,
  Play,
  RefreshCw,
  Terminal,
  X,
} from 'lucide-react';
import { toast } from 'sonner';

import { api, errorMessage } from '@/lib/api';
import { formatRelativeTime } from '@/lib/format';
import type { JobLogRecord, JobRecord, QueueResult } from '@/lib/types';
import { cn } from '@/lib/utils';

interface JobsPageProps {
  jobs?: JobRecord[];
  queue?: QueueResult | null;
  onRefresh?: () => void;
}

export function JobsPage({
  jobs: propJobs,
  queue: propQueue,
  onRefresh,
}: JobsPageProps) {
  const [jobs, setJobs] = useState<JobRecord[]>(propJobs || []);
  const [queue, setQueue] = useState<QueueResult | null>(propQueue || null);
  const [helpOpen, setHelpOpen] = useState(false);
  const [dispatchOpen, setDispatchOpen] = useState(false);

  // Dispatch new job form
  const [jobCommand, setJobCommand] = useState(
    'uname -a && lscpu | head -n 12 && free -m',
  );
  const [jobRunnerTag, setJobRunnerTag] = useState('compute');
  const [jobPriority, setJobPriority] = useState(10);

  // Selected job logs drawer
  const [selectedJob, setSelectedJob] = useState<JobRecord | null>(null);
  const [jobLogs, setJobLogs] = useState<JobLogRecord[]>([]);
  const [logsLoading, setLogsLoading] = useState(false);

  const loadJobs = useCallback(async () => {
    try {
      const [jobsList, queueRes] = await Promise.allSettled([
        api.listJobs(),
        api.queue(),
      ]);
      if (jobsList.status === 'fulfilled') setJobs(jobsList.value || []);
      if (queueRes.status === 'fulfilled') setQueue(queueRes.value);
    } catch {
      // Ignore transient errors
    }
  }, []);

  useEffect(() => {
    if (propJobs) setJobs(propJobs);
  }, [propJobs]);

  useEffect(() => {
    if (propQueue !== undefined) setQueue(propQueue);
  }, [propQueue]);

  useEffect(() => {
    void loadJobs();
  }, [loadJobs]);

  useEffect(() => {
    if (!selectedJob) return;
    setLogsLoading(true);
    api
      .getJobLogs(selectedJob.id)
      .then((res) => setJobLogs(res || []))
      .catch(() => setJobLogs([]))
      .finally(() => setLogsLoading(false));
  }, [selectedJob]);

  function handleDispatchJob(e: React.FormEvent) {
    e.preventDefault();
    const command = jobCommand.trim();
    if (!command) return;
    const payload = {
      type: 'command',
      pool_id: 'default',
      priority: jobPriority,
      spec: {
        command,
        runs_on: jobRunnerTag,
      },
    };
    setDispatchOpen(false);

    const toastID = toast.loading('Dispatching CI job…');
    void api
      .createJob(payload)
      .then(async (created) => {
        toast.success(`Dispatched CI job ${created.id}`, { id: toastID });
        await loadJobs();
        onRefresh?.();
      })
      .catch((err) => {
        toast.error(`Failed to dispatch job: ${errorMessage(err)}`, {
          id: toastID,
        });
      });
  }

  async function handleCancelJob(id: string) {
    const toastID = toast.loading(`Canceling job ${id}…`);
    try {
      await api.cancelJob(id);
      toast.success(`Canceled job ${id}`, { id: toastID });
      await loadJobs();
      onRefresh?.();
    } catch (err) {
      toast.error(errorMessage(err), { id: toastID });
    }
  }

  return (
    <div className="space-y-6">
      {/* Top Page Title matching dashboard-04-ci-on-compute-vms.png 1:1 */}
      <div className="flex items-center justify-between">
        <h1 className="font-display text-[22px] font-medium tracking-tight text-foreground">
          CI on Compute VMs
        </h1>
      </div>

      {/* Main Onboarding Checklist Card matching dashboard-04-ci-on-compute-vms.png 1:1 */}
      <div className="w-full space-y-3">
        <div className="border border-border bg-background">
          <div className="border-b border-border px-5 py-4 space-y-1">
            <h2 className="text-[13px] font-medium text-foreground">
              Set up CI on Compute VMs
            </h2>
            <p className="text-[11.5px] text-muted-foreground">
              Each GitHub Actions job gets a fresh sandbox, billed per second,
              deleted when the job ends. It gets none of your Compute secrets.
            </p>
          </div>

          <div className="px-5 py-5">
            <div className="relative space-y-5">
              {/* Vertical connector line */}
              <div
                className="pointer-events-none absolute left-[10px] top-5 bottom-5 w-[1px] bg-border"
                aria-hidden="true"
              />

              {/* Step 1 */}
              <div className="relative flex items-start gap-3.5">
                <div className="relative z-10 flex h-5 w-5 shrink-0 items-center justify-center rounded-full border border-primary bg-background font-mono text-[10.5px] font-medium text-primary dark:text-[var(--accent-bright)]">
                  1
                </div>
                <div className="space-y-2">
                  <div className="space-y-0.5">
                    <div className="text-[12.5px] font-medium text-foreground">
                      Install the compute CI app on GitHub
                    </div>
                    <p className="text-[11.5px] text-muted-foreground leading-relaxed">
                      GitHub asks you to pick an organization and its
                      repositories.
                    </p>
                  </div>
                  <div className="flex flex-wrap items-center gap-2.5 pt-1">
                    <button
                      type="button"
                      onClick={() => setDispatchOpen(true)}
                      className="inline-flex h-8 items-center gap-1.5 bg-primary px-3.5 text-[12px] font-medium text-primary-foreground hover:bg-primary-hover transition-colors"
                    >
                      <GitBranch className="h-3.5 w-3.5" />
                      <span>Connect GitHub</span>
                    </button>
                    <span className="text-[11.5px] text-muted-foreground">
                      or run{' '}
                      <code className="font-mono text-[11.5px] text-foreground">
                        compute job run -- &lt;command&gt;
                      </code>
                    </span>
                  </div>
                </div>
              </div>

              {/* Step 2 */}
              <div className="relative flex items-start gap-3.5">
                <div className="relative z-10 flex h-5 w-5 shrink-0 items-center justify-center rounded-full border border-border bg-background font-mono text-[10.5px] text-muted-foreground">
                  2
                </div>
                <div className="space-y-0.5">
                  <div className="text-[12.5px] font-medium text-foreground">
                    Give it access to your repositories
                  </div>
                  <p className="text-[11.5px] text-muted-foreground leading-relaxed">
                    After the install.
                  </p>
                </div>
              </div>

              {/* Step 3 */}
              <div className="relative flex items-start gap-3.5">
                <div className="relative z-10 flex h-5 w-5 shrink-0 items-center justify-center rounded-full border border-border bg-background font-mono text-[10.5px] text-muted-foreground">
                  3
                </div>
                <div className="space-y-0.5 flex-1">
                  <div className="text-[12.5px] font-medium text-foreground">
                    Use <span className="font-mono">runs-on: compute</span> in a
                    workflow
                  </div>
                  <p className="text-[11.5px] text-muted-foreground leading-relaxed">
                    Replace{' '}
                    <span className="font-mono">runs-on: ubuntu-latest</span>{' '}
                    with <span className="font-mono">runs-on: compute</span>, then
                    push.
                  </p>
                </div>
              </div>

              {/* Step 4 */}
              <div className="relative flex items-start gap-3.5">
                <div className="relative z-10 flex h-5 w-5 shrink-0 items-center justify-center rounded-full border border-border bg-background font-mono text-[10.5px] text-muted-foreground">
                  4
                </div>
                <div className="space-y-0.5">
                  <div className="text-[12.5px] font-medium text-foreground">
                    A job passes on compute
                  </div>
                  <p className="text-[11.5px] text-muted-foreground leading-relaxed">
                    {jobs.length > 0
                      ? `${jobs.filter((j) => j.state === 'succeeded').length} succeeded of ${jobs.length} submitted.`
                      : 'No jobs submitted yet.'}
                  </p>
                </div>
              </div>
            </div>

            <div className="mt-5 border-t border-border pt-3.5 text-[11.5px] text-muted-foreground">
              Already installed on GitHub?{' '}
              <button
                type="button"
                onClick={() => setDispatchOpen(true)}
                className="text-foreground/90 hover:text-foreground hover:underline underline-offset-2"
              >
                Link it here
              </button>
            </div>
          </div>
        </div>

        {/* Collapsible Help Bar matching dashboard-04-ci-on-compute-vms.png 1:1 */}
        <div className="border border-border bg-background">
          <button
            type="button"
            onClick={() => setHelpOpen((v) => !v)}
            className="flex w-full items-center justify-between px-4 py-3 text-left text-[12px] font-medium text-foreground hover:bg-muted/30 transition-colors"
          >
            <span>Help</span>
            <ChevronDown
              className={cn(
                'h-3.5 w-3.5 text-muted-foreground transition-transform',
                helpOpen && 'rotate-180',
              )}
            />
          </button>
          {helpOpen && (
            <div className="border-t border-border px-4 py-3 text-[11.5px] text-muted-foreground space-y-2 leading-relaxed">
              <p>
                Each GitHub Actions workflow job with{' '}
                <code className="font-mono text-foreground">runs-on: compute</code>{' '}
                provisions an isolated KVM Firecracker microVM via the local
                compute scheduler, streams stdout/stderr logs in real time, and
                destroys the overlay disk on exit.
              </p>
              <p>
                You can also submit ephemeral jobs directly via{' '}
                <code className="font-mono text-foreground">
                  POST /api/v1/jobs
                </code>{' '}
                or{' '}
                <button
                  type="button"
                  onClick={() => setDispatchOpen(true)}
                  className="underline text-foreground"
                >
                  dispatch an ephemeral job now
                </button>
                .
              </p>
            </div>
          )}
        </div>
      </div>

      {/* Dispatch Ephemeral Job Modal/Drawer */}
      {dispatchOpen && (
        <form
          onSubmit={handleDispatchJob}
          className="max-w-2xl border border-border bg-muted/15 p-4 space-y-3"
        >
          <div className="flex items-center justify-between">
            <span className="text-[13px] font-medium text-foreground">
              Dispatch Ephemeral Runner Job (POST /api/v1/jobs)
            </span>
            <button
              type="button"
              onClick={() => setDispatchOpen(false)}
              className="text-muted-foreground hover:text-foreground"
            >
              <X className="h-4 w-4" />
            </button>
          </div>

          <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
            <div className="sm:col-span-2">
              <label className="block text-[11px] text-muted-foreground mb-1">
                Shell Command / CI Step
              </label>
              <input
                type="text"
                value={jobCommand}
                onChange={(e) => setJobCommand(e.target.value)}
                required
                className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px] text-foreground focus:border-primary focus:outline-none"
              />
            </div>
            <div>
              <label className="block text-[11px] text-muted-foreground mb-1">
                Runner Tier
              </label>
              <select
                aria-label="Runner tag"
                value={jobRunnerTag}
                onChange={(e) => setJobRunnerTag(e.target.value)}
                className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px] text-foreground"
              >
                <option value="compute-small">compute-small (2 vCPU · 4 GB)</option>
                <option value="compute">compute (4 vCPU · 8 GB)</option>
                <option value="compute-large">compute-large (8 vCPU · 16 GB)</option>
                <option value="compute-xlarge">
                  compute-xlarge (16 vCPU · 32 GB)
                </option>
              </select>
            </div>
          </div>

          <div className="flex items-center justify-between pt-1">
            <div className="flex items-center gap-2 text-[11.5px] text-muted-foreground">
              <span>Priority:</span>
              <input
                type="number"
                value={jobPriority}
                onChange={(e) => setJobPriority(Number(e.target.value) || 0)}
                className="h-7 w-16 border border-border bg-background px-2 font-mono text-[11.5px] text-foreground"
              />
            </div>
            <div className="flex items-center gap-2">
              <button
                type="button"
                onClick={() => setDispatchOpen(false)}
                className="h-8 border border-border px-3 text-[12px] text-muted-foreground"
              >
                Cancel
              </button>
              <button
                type="submit"
                className="flex h-8 items-center gap-1.5 bg-primary px-3 text-[12px] font-medium text-primary-foreground hover:bg-primary-hover disabled:opacity-50"
              >
                <Play className="h-3 w-3 fill-current" />
                <span>Run Job</span>
              </button>
            </div>
          </div>
        </form>
      )}

      {/* Live Daemon Jobs & Queue Table (shown when jobs or queued items exist) */}
      {(jobs.length > 0 || (queue && queue.count > 0)) && (
        <div className="space-y-3 pt-2">
          <div className="flex items-center justify-between">
            <h2 className="text-[13px] font-medium text-foreground">
              Ephemeral Runner Jobs ({jobs.length}) &amp; Admission Queue (
              {queue?.count ?? 0})
            </h2>
            <button
              type="button"
              onClick={() => void loadJobs()}
              className="flex h-7 items-center gap-1.5 border border-border bg-background px-2.5 text-[11.5px] text-muted-foreground hover:text-foreground"
            >
              <RefreshCw className="h-3 w-3" />
              <span>Refresh</span>
            </button>
          </div>

          <div className="border border-border bg-background overflow-x-auto">
            <table className="w-full border-collapse text-left text-[12px]">
              <thead>
                <tr className="border-b border-border bg-muted/30 text-[11.5px] text-muted-foreground">
                  <th className="py-2 px-3 font-normal">Job ID</th>
                  <th className="py-2 px-3 font-normal">Type / Pool</th>
                  <th className="py-2 px-3 font-normal">Spec</th>
                  <th className="py-2 px-3 font-normal">State</th>
                  <th className="py-2 px-3 font-normal">Queued</th>
                  <th className="py-2 px-3 text-right font-normal">Actions</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border font-mono text-[11.5px]">
                {jobs.map((job) => (
                  <tr
                    key={job.id}
                    onClick={() => setSelectedJob(job)}
                    className={cn(
                      'cursor-pointer hover:bg-muted/30',
                      selectedJob?.id === job.id && 'bg-selected/60',
                    )}
                  >
                    <td className="py-2 px-3 text-foreground font-medium">
                      {job.id}
                    </td>
                    <td className="py-2 px-3 text-muted-foreground">
                      {job.type} · {job.pool_id || 'default'}
                    </td>
                    <td className="py-2 px-3 text-muted-foreground truncate max-w-xs">
                      {job.spec_json}
                    </td>
                    <td className="py-2 px-3">
                      <span
                        className={cn(
                          'border px-1.5 py-0.5 text-[10.5px] uppercase',
                          job.state === 'completed' &&
                            'border-success/40 bg-success/10 text-success',
                          job.state === 'running' &&
                            'border-primary bg-selected text-selected-foreground',
                          job.state === 'queued' &&
                            'border-warning/40 bg-warning/10 text-warning',
                          (job.state === 'failed' ||
                            job.state === 'canceled') &&
                            'border-destructive/40 bg-destructive/10 text-destructive',
                        )}
                      >
                        {job.state}
                      </span>
                    </td>
                    <td className="py-2 px-3 text-muted-foreground">
                      {formatRelativeTime(job.queued_at)}
                    </td>
                    <td
                      className="py-2 px-3 text-right"
                      onClick={(e) => e.stopPropagation()}
                    >
                      <div className="inline-flex items-center gap-1.5">
                        <button
                          type="button"
                          onClick={() => setSelectedJob(job)}
                          className="border border-border px-2 py-0.5 text-[11px] text-foreground hover:border-primary"
                        >
                          Logs
                        </button>
                        {(job.state === 'queued' ||
                          job.state === 'running') && (
                          <button
                            type="button"
                            onClick={() => void handleCancelJob(job.id)}
                            className="border border-border px-2 py-0.5 text-[11px] text-destructive hover:border-destructive"
                          >
                            Cancel
                          </button>
                        )}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {selectedJob && (
        <div className="border border-border bg-background p-4 space-y-3">
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-2 font-mono text-[12px]">
              <Terminal className="h-3.5 w-3.5 text-[color:var(--link)]" />
              <span className="text-foreground font-medium">
                {selectedJob.id}
              </span>
              <span className="text-muted-foreground">
                ({selectedJob.state})
              </span>
            </div>
            <button
              type="button"
              onClick={() => setSelectedJob(null)}
              className="text-muted-foreground hover:text-foreground"
            >
              <X className="h-4 w-4" />
            </button>
          </div>
          <pre className="max-h-48 overflow-auto border border-border bg-[#0a0a0b] p-3 font-mono text-[11px] text-[#f5f5f4]">
            {logsLoading
              ? 'Loading job execution logs…'
              : jobLogs.length > 0
                ? jobLogs.map((l) => `[${l.stream}] ${l.data}`).join('\n')
                : selectedJob.result_json ||
                  `Job ${selectedJob.id} is ${selectedJob.state}. Spec: ${selectedJob.spec_json}`}
          </pre>
        </div>
      )}
    </div>
  );
}
