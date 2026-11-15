import { useCallback, useEffect, useState } from 'react';
import {
  Check,
  ChevronDown,
  Clock,
  Copy,
  Edit3,
  GitBranch,
  GitCommit,
  KeyRound,
  Play,
  Plus,
  RefreshCw,
  Terminal,
  Trash2,
  Webhook,
  X,
} from 'lucide-react';
import { toast } from 'sonner';

import { CopyButton } from '@/components/copy-button';
import { api, errorMessage } from '@/lib/api';
import { formatMemory, formatRelativeTime } from '@/lib/format';
import type {
  GitHubConnectionRecord,
  JobLogRecord,
  JobRecord,
  QueueResult,
} from '@/lib/types';
import { cn } from '@/lib/utils';

export interface CIPageProps {
  jobs?: JobRecord[];
  queue?: QueueResult | null;
  onRefresh?: () => void;
}

function formatJobDuration(job: JobRecord): string {
  if (!job.started_at) return '—';
  const start = Date.parse(job.started_at);
  if (Number.isNaN(start)) return '—';
  const end = job.finished_at ? Date.parse(job.finished_at) : Date.now();
  const sec = Math.max(0, Math.round((end - start) / 1000));
  if (sec < 60) return `${sec}s`;
  const min = Math.floor(sec / 60);
  const rem = sec % 60;
  return `${min}m ${rem}s`;
}

function isJobActive(state: string): boolean {
  const s = state.toLowerCase();
  return s === 'queued' || s === 'assigned' || s === 'running';
}

function isJobSuccess(state: string): boolean {
  const s = state.toLowerCase();
  return s === 'succeeded' || s === 'completed';
}

export function CIPage({
  jobs: propJobs,
  queue: propQueue,
  onRefresh,
}: CIPageProps) {
  const [jobs, setJobs] = useState<JobRecord[]>(propJobs || []);
  const [queue, setQueue] = useState<QueueResult | null>(propQueue || null);
  const [repos, setRepos] = useState<GitHubConnectionRecord[]>([]);
  const [loadingRepos, setLoadingRepos] = useState(true);
  const [guideOpen, setGuideOpen] = useState(false);

  // Connect / Edit GitHub Repository dialog
  const [repoModalOpen, setRepoModalOpen] = useState(false);
  const [editingRepo, setEditingRepo] = useState<GitHubConnectionRecord | null>(null);
  const [repoName, setRepoName] = useState('');
  const [repoUrl, setRepoUrl] = useState('');
  const [defaultBranch, setDefaultBranch] = useState('main');
  const [githubToken, setGithubToken] = useState('');
  const [webhookSecret, setWebhookSecret] = useState('');
  const [defaultCommand, setDefaultCommand] = useState('npm test');
  const [repoVcpus, setRepoVcpus] = useState(2);
  const [repoMemoryMb, setRepoMemoryMb] = useState(2048);
  const [repoTimeoutSec, setRepoTimeoutSec] = useState(600);

  // One-time revealed secret after connecting a repo
  const [createdRepoBanner, setCreatedRepoBanner] = useState<{
    name: string;
    webhookSecret: string;
  } | null>(null);

  // Trigger Repo Job dialog
  const [triggerTarget, setTriggerTarget] =
    useState<GitHubConnectionRecord | null>(null);
  const [triggerBranch, setTriggerBranch] = useState('');
  const [triggerCommit, setTriggerCommit] = useState('');
  const [triggerCommand, setTriggerCommand] = useState('');

  // Run Ad-hoc Job dialog
  const [dispatchOpen, setDispatchOpen] = useState(false);
  const [jobCommand, setJobCommand] = useState(
    'uname -a && lscpu | head -n 12 && free -m',
  );
  const [jobVcpus, setJobVcpus] = useState(2);
  const [jobMemoryMb, setJobMemoryMb] = useState(2048);
  const [jobTimeoutSec, setJobTimeoutSec] = useState(300);
  const [jobPriority, setJobPriority] = useState(10);

  // Selected job logs drawer
  const [selectedJob, setSelectedJob] = useState<JobRecord | null>(null);
  const [jobLogs, setJobLogs] = useState<JobLogRecord[]>([]);
  const [logsLoading, setLogsLoading] = useState(false);
  const [copiedKey, setCopiedKey] = useState<string | null>(null);

  const webhookUrl = `${window.location.origin}/api/v1/ci/webhooks/github`;

  const loadAll = useCallback(async () => {
    const [jobsRes, queueRes, reposRes] = await Promise.allSettled([
      api.listJobs(),
      api.queue(),
      api.listCIRepos(),
    ]);
    if (jobsRes.status === 'fulfilled') setJobs(jobsRes.value || []);
    if (queueRes.status === 'fulfilled') setQueue(queueRes.value);
    if (reposRes.status === 'fulfilled') setRepos(reposRes.value || []);
    setLoadingRepos(false);
  }, []);

  useEffect(() => {
    if (propJobs) setJobs(propJobs);
  }, [propJobs]);

  useEffect(() => {
    if (propQueue !== undefined) setQueue(propQueue);
  }, [propQueue]);

  useEffect(() => {
    void loadAll();
  }, [loadAll]);

  // Keep selectedJob synced when jobs list updates
  useEffect(() => {
    if (!selectedJob) return;
    const latest = jobs.find((j) => j.id === selectedJob.id);
    if (latest) {
      setSelectedJob(latest);
    }
  }, [jobs, selectedJob]);

  const fetchSelectedLogs = useCallback(async (jobId: string, showSpinner: boolean) => {
    if (showSpinner) setLogsLoading(true);
    try {
      const res = await api.getJobLogs(jobId);
      setJobLogs(res || []);
    } catch {
      if (showSpinner) setJobLogs([]);
    } finally {
      if (showSpinner) setLogsLoading(false);
    }
  }, []);

  useEffect(() => {
    if (!selectedJob) return;
    void fetchSelectedLogs(selectedJob.id, true);
    if (!isJobActive(selectedJob.state)) return;

    const timer = window.setInterval(() => {
      void fetchSelectedLogs(selectedJob.id, false);
      void loadAll();
    }, 2500);
    return () => window.clearInterval(timer);
  }, [selectedJob?.id, selectedJob?.state, fetchSelectedLogs, loadAll]);

  function openConnectModal() {
    setEditingRepo(null);
    setRepoName('');
    setRepoUrl('');
    setDefaultBranch('main');
    setGithubToken('');
    setWebhookSecret('');
    setDefaultCommand('npm test');
    setRepoVcpus(2);
    setRepoMemoryMb(2048);
    setRepoTimeoutSec(600);
    setRepoModalOpen(true);
  }

  function openEditModal(repo: GitHubConnectionRecord) {
    setEditingRepo(repo);
    setRepoName(repo.name);
    setRepoUrl(repo.repo_url);
    setDefaultBranch(repo.default_branch || 'main');
    setGithubToken('');
    setWebhookSecret('');
    setDefaultCommand(repo.default_command || '');
    setRepoVcpus(repo.vcpus || 2);
    setRepoMemoryMb(repo.memory_mb || 2048);
    setRepoTimeoutSec(repo.timeout_sec || repo.timeout_seconds || 600);
    setRepoModalOpen(true);
  }

  function handleSaveRepo(e: React.FormEvent) {
    e.preventDefault();
    const urlTrim = repoUrl.trim();
    const nameTrim = repoName.trim();
    if (!urlTrim && !nameTrim) {
      toast.error('Repository URL or owner/name is required');
      return;
    }

    const editing = editingRepo;
    setRepoModalOpen(false);

    if (editing) {
      const toastID = toast.loading(`Updating ${editing.name}…`);
      void api
        .updateCIRepo(editing.id, {
          name: nameTrim || undefined,
          repo_url: urlTrim || undefined,
          default_branch: defaultBranch.trim() || 'main',
          github_token: githubToken.trim() || undefined,
          webhook_secret: webhookSecret.trim() || undefined,
          default_command: defaultCommand.trim(),
          vcpus: repoVcpus,
          memory_mb: repoMemoryMb,
          timeout_sec: repoTimeoutSec,
        })
        .then(async (updated) => {
          toast.success(`Updated ${updated.name}`, { id: toastID });
          await loadAll();
        })
        .catch((err) => {
          toast.error(`Update failed: ${errorMessage(err)}`, { id: toastID });
        });
      return;
    }

    const toastID = toast.loading(`Connecting GitHub repository…`);
    void api
      .createCIRepo({
        name: nameTrim || undefined,
        repo_url: urlTrim,
        default_branch: defaultBranch.trim() || 'main',
        github_token: githubToken.trim() || undefined,
        webhook_secret: webhookSecret.trim() || undefined,
        default_command: defaultCommand.trim() || "echo 'CI job completed'",
        vcpus: repoVcpus,
        memory_mb: repoMemoryMb,
        timeout_sec: repoTimeoutSec,
      })
      .then(async (created) => {
        toast.success(`Connected ${created.name}`, { id: toastID });
        if (created.webhook_secret) {
          setCreatedRepoBanner({
            name: created.name,
            webhookSecret: created.webhook_secret,
          });
        }
        await loadAll();
      })
      .catch((err) => {
        toast.error(`Connection failed: ${errorMessage(err)}`, { id: toastID });
      });
  }

  function handleDeleteRepo(repo: GitHubConnectionRecord) {
    const toastID = toast.loading(`Disconnecting ${repo.name}…`);
    void api
      .deleteCIRepo(repo.id)
      .then(async () => {
        toast.success(`Disconnected ${repo.name}`, { id: toastID });
        await loadAll();
      })
      .catch((err) => {
        toast.error(`Failed to disconnect: ${errorMessage(err)}`, {
          id: toastID,
        });
      });
  }

  function openTriggerModal(repo: GitHubConnectionRecord) {
    setTriggerTarget(repo);
    setTriggerBranch(repo.default_branch || 'main');
    setTriggerCommit('');
    setTriggerCommand(repo.default_command || '');
  }

  function handleTriggerRepo(e: React.FormEvent) {
    e.preventDefault();
    if (!triggerTarget) return;
    const repo = triggerTarget;
    setTriggerTarget(null);

    const toastID = toast.loading(`Triggering CI job for ${repo.name}…`);
    void api
      .triggerCIRepo(repo.id, {
        branch: triggerBranch.trim() || undefined,
        commit_sha: triggerCommit.trim() || undefined,
        command: triggerCommand.trim() || undefined,
        source: 'manual',
      })
      .then(async (job) => {
        toast.success(`Triggered job ${job.id} on ${repo.name}`, { id: toastID });
        setSelectedJob(job);
        await loadAll();
        onRefresh?.();
      })
      .catch((err) => {
        toast.error(`Trigger failed: ${errorMessage(err)}`, { id: toastID });
      });
  }

  function handleDispatchJob(e: React.FormEvent) {
    e.preventDefault();
    const command = jobCommand.trim();
    if (!command) return;
    setDispatchOpen(false);

    const toastID = toast.loading('Dispatching CI job…');
    void api
      .createJob({
        type: 'command',
        pool_id: 'default',
        priority: jobPriority,
        source: 'manual',
        spec: {
          image_id: 'ubuntu-24',
          command: ['bash', '-lc', command],
          vcpus: jobVcpus,
          memory_mb: jobMemoryMb,
          timeout_seconds: jobTimeoutSec,
        },
      })
      .then(async (created) => {
        toast.success(`Dispatched CI job ${created.id}`, { id: toastID });
        setSelectedJob(created);
        await loadAll();
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
      await loadAll();
      onRefresh?.();
    } catch (err) {
      toast.error(errorMessage(err), { id: toastID });
    }
  }

  function copyWithState(key: string, value: string, label: string) {
    void navigator.clipboard.writeText(value);
    setCopiedKey(key);
    toast.success(`Copied ${label}`);
    setTimeout(() => setCopiedKey(null), 1500);
  }

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="font-display text-[22px] font-medium tracking-tight text-foreground">
            CI Jobs
          </h1>
          <p className="text-[12px] text-muted-foreground">
            Connect GitHub repositories for automatic webhook or manual runs inside
            ephemeral Ubuntu 24.04 Firecracker sandboxes.
          </p>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <button
            type="button"
            onClick={() => void loadAll()}
            className="inline-flex h-8 items-center gap-1.5 border border-border bg-background px-3 text-[12px] text-muted-foreground hover:text-foreground"
          >
            <RefreshCw className="h-3.5 w-3.5" />
            <span>Refresh</span>
          </button>
          <button
            type="button"
            onClick={() => setDispatchOpen(true)}
            className="inline-flex h-8 items-center gap-1.5 border border-border bg-background px-3 text-[12px] font-medium text-foreground hover:border-primary"
          >
            <Play className="h-3 w-3" />
            <span>Run Ad-hoc Job</span>
          </button>
          <button
            type="button"
            onClick={openConnectModal}
            className="inline-flex h-8 items-center gap-1.5 bg-primary px-3.5 text-[12px] font-medium text-primary-foreground hover:bg-primary-hover"
          >
            <Plus className="h-3.5 w-3.5" />
            <span>Connect GitHub Repository</span>
          </button>
        </div>
      </div>

      {/* One-time Webhook Secret Reveal Banner */}
      {createdRepoBanner && (
        <div className="border border-success/40 bg-success/10 p-4 space-y-2.5">
          <div className="flex items-start justify-between gap-3">
            <div className="space-y-0.5">
              <div className="text-[13px] font-medium text-foreground">
                Webhook configured for {createdRepoBanner.name}
              </div>
              <p className="text-[11.5px] text-muted-foreground">
                Add this webhook URL and signing secret to GitHub &rarr; Repository
                Settings &rarr; Webhooks (Content type:{' '}
                <code className="font-mono text-foreground">application/json</code>).
                This secret is only shown once.
              </p>
            </div>
            <button
              type="button"
              onClick={() => setCreatedRepoBanner(null)}
              className="text-muted-foreground hover:text-foreground"
            >
              <X className="h-4 w-4" />
            </button>
          </div>
          <div className="grid grid-cols-1 gap-2 sm:grid-cols-2 font-mono text-[11.5px]">
            <div className="flex items-center justify-between gap-2 border border-border bg-background px-3 py-2">
              <span className="truncate text-foreground">{webhookUrl}</span>
              <CopyButton value={webhookUrl} label="Copy webhook URL" />
            </div>
            <div className="flex items-center justify-between gap-2 border border-border bg-background px-3 py-2">
              <span className="truncate text-foreground">
                {createdRepoBanner.webhookSecret}
              </span>
              <CopyButton
                value={createdRepoBanner.webhookSecret}
                label="Copy webhook secret"
              />
            </div>
          </div>
        </div>
      )}

      {/* Connected GitHub Repositories Section */}
      <div className="border border-border bg-background">
        <div className="flex flex-wrap items-center justify-between gap-2 border-b border-border bg-muted/20 px-4 py-3">
          <div className="flex items-center gap-2">
            <GitBranch className="h-4 w-4 text-primary" />
            <h2 className="text-[13px] font-medium text-foreground">
              Connected GitHub Repositories ({repos.length})
            </h2>
          </div>
          <div className="flex items-center gap-2 font-mono text-[11px] text-muted-foreground">
            <Webhook className="h-3.5 w-3.5" />
            <span className="hidden sm:inline truncate max-w-[320px]">
              {webhookUrl}
            </span>
            <button
              type="button"
              onClick={() =>
                copyWithState('webhook-url', webhookUrl, 'webhook endpoint URL')
              }
              className="inline-flex items-center gap-1 border border-border bg-background px-2 py-0.5 text-foreground hover:border-primary"
            >
              {copiedKey === 'webhook-url' ? (
                <Check className="h-3 w-3 text-success" />
              ) : (
                <Copy className="h-3 w-3" />
              )}
              <span>Copy Webhook URL</span>
            </button>
          </div>
        </div>

        {loadingRepos && repos.length === 0 ? (
          <div className="p-6 font-mono text-[12px] text-muted-foreground">
            Loading connected repositories…
          </div>
        ) : repos.length === 0 ? (
          <div className="p-6 flex flex-wrap items-center justify-between gap-4">
            <div className="space-y-1">
              <div className="text-[13px] font-medium text-foreground">
                No GitHub repositories connected yet
              </div>
              <p className="text-[12px] text-muted-foreground">
                Connect a repository to run CI commands automatically on push events
                or trigger on-demand builds with a single click.
              </p>
            </div>
            <button
              type="button"
              onClick={openConnectModal}
              className="inline-flex h-8 items-center gap-1.5 bg-primary px-3 text-[12px] font-medium text-primary-foreground hover:bg-primary-hover"
            >
              <GitBranch className="h-3.5 w-3.5" />
              <span>Connect GitHub Repository</span>
            </button>
          </div>
        ) : (
          <div className="divide-y divide-border">
            {repos.map((repo) => (
              <div
                key={repo.id}
                className="flex flex-wrap items-center justify-between gap-4 px-4 py-3"
              >
                <div className="min-w-0 space-y-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-mono text-[13px] font-medium text-foreground">
                      {repo.name}
                    </span>
                    <span className="border border-border bg-muted/30 px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground">
                      {repo.default_branch || 'main'}
                    </span>
                    {repo.has_token && (
                      <span className="inline-flex items-center gap-1 border border-primary/40 bg-primary/10 px-1.5 py-0.5 font-mono text-[10px] text-primary dark:text-[var(--accent-bright)]">
                        <KeyRound className="h-2.5 w-2.5" />
                        token
                      </span>
                    )}
                    {repo.has_webhook_secret && (
                      <span className="inline-flex items-center gap-1 border border-success/40 bg-success/10 px-1.5 py-0.5 font-mono text-[10px] text-success">
                        webhook signed
                      </span>
                    )}
                  </div>
                  <div className="flex flex-wrap items-center gap-3 font-mono text-[11px] text-muted-foreground">
                    <span className="truncate max-w-sm">{repo.repo_url}</span>
                    <span>·</span>
                    <span>
                      {repo.vcpus || 2} vCPU · {formatMemory(repo.memory_mb || 2048)} ·{' '}
                      {repo.timeout_sec || repo.timeout_seconds || 600}s
                    </span>
                    {(repo.last_triggered_at || repo.last_delivery_at) && (
                      <>
                        <span>·</span>
                        <span>
                          Last run{' '}
                          {formatRelativeTime(
                            repo.last_triggered_at || repo.last_delivery_at || '',
                          )}
                        </span>
                      </>
                    )}
                  </div>
                  <div className="font-mono text-[11px] text-foreground/80">
                    $ {repo.default_command || "echo 'CI job completed'"}
                  </div>
                </div>

                <div className="flex items-center gap-1.5 shrink-0">
                  <button
                    type="button"
                    onClick={() => openTriggerModal(repo)}
                    className="inline-flex h-7 items-center gap-1 bg-primary px-2.5 text-[11.5px] font-medium text-primary-foreground hover:bg-primary-hover"
                  >
                    <Play className="h-3 w-3" />
                    <span>Trigger</span>
                  </button>
                  <button
                    type="button"
                    onClick={() => openEditModal(repo)}
                    aria-label={`Edit ${repo.name}`}
                    className="inline-flex h-7 items-center gap-1 border border-border bg-background px-2 text-[11.5px] text-foreground hover:border-primary"
                  >
                    <Edit3 className="h-3 w-3" />
                    <span>Edit</span>
                  </button>
                  <button
                    type="button"
                    onClick={() => handleDeleteRepo(repo)}
                    aria-label={`Delete ${repo.name}`}
                    className="inline-flex h-7 w-7 items-center justify-center border border-border bg-background text-muted-foreground hover:border-destructive hover:text-destructive"
                  >
                    <Trash2 className="h-3 w-3" />
                  </button>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>

      {/* Job Runs Table */}
      <div className="space-y-3">
        <div className="flex items-center justify-between">
          <h2 className="text-[13px] font-medium text-foreground">
            Job Runs ({jobs.length}) · Queue ({queue?.count ?? 0})
          </h2>
          <div className="font-mono text-[11px] text-muted-foreground">
            {jobs.filter((j) => isJobSuccess(j.state)).length} passed ·{' '}
            {jobs.filter((j) => isJobActive(j.state)).length} active ·{' '}
            {jobs.filter((j) => j.state === 'failed').length} failed
          </div>
        </div>

        <div className="border border-border bg-background overflow-x-auto">
          <table className="w-full border-collapse text-left text-[12px]">
            <thead>
              <tr className="border-b border-border bg-muted/30 text-[11px] text-muted-foreground whitespace-nowrap">
                <th className="py-2 px-3 font-normal">Job ID</th>
                <th className="py-2 px-3 font-normal">Source</th>
                <th className="py-2 px-3 font-normal">Repository / Branch / Commit</th>
                <th className="py-2 px-3 font-normal">State</th>
                <th className="py-2 px-3 font-normal">Sandbox / Worker</th>
                <th className="py-2 px-3 font-normal">Duration</th>
                <th className="py-2 px-3 font-normal">Queued</th>
                <th className="py-2 px-3 text-right font-normal">Actions</th>
              </tr>
            </thead>
            {jobs.length === 0 ? (
              <tbody>
                <tr>
                  <td
                    colSpan={8}
                    className="py-10 text-center font-mono text-[11.5px] text-muted-foreground"
                  >
                    No CI jobs recorded yet. Trigger a connected repository or click
                    &ldquo;Run Ad-hoc Job&rdquo;.
                  </td>
                </tr>
              </tbody>
            ) : (
              <tbody className="divide-y divide-border font-mono text-[11.5px]">
                {jobs.map((job) => {
                  const sourceLabel =
                    job.source || (job.repo_name ? 'github' : 'manual');
                  const shortSha = job.commit_sha ? job.commit_sha.slice(0, 7) : '';

                  return (
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
                      <td className="py-2 px-3">
                        <span className="border border-border bg-muted/30 px-1.5 py-0.5 text-[10px] uppercase text-muted-foreground">
                          {sourceLabel}
                        </span>
                      </td>
                      <td className="py-2 px-3 text-muted-foreground">
                        {job.repo_name || job.repo_url ? (
                          <div className="flex flex-wrap items-center gap-1.5">
                            <span className="text-foreground">
                              {job.repo_name || job.repo_url}
                            </span>
                            {job.branch && (
                              <span className="border border-border px-1 py-0.5 text-[10px]">
                                {job.branch}
                              </span>
                            )}
                            {shortSha && (
                              <span className="inline-flex items-center gap-0.5 text-[10px]">
                                <GitCommit className="h-3 w-3" />
                                {shortSha}
                              </span>
                            )}
                          </div>
                        ) : (
                          <span className="truncate block max-w-xs">
                            {job.spec_json || `${job.type} · ${job.pool_id || 'default'}`}
                          </span>
                        )}
                      </td>
                      <td className="py-2 px-3">
                        <span
                          className={cn(
                            'border px-1.5 py-0.5 text-[10.5px] uppercase',
                            isJobSuccess(job.state) &&
                              'border-success/40 bg-success/10 text-success',
                            (job.state === 'running' || job.state === 'assigned') &&
                              'border-primary bg-selected text-selected-foreground',
                            job.state === 'queued' &&
                              'border-warning/40 bg-warning/10 text-warning',
                            (job.state === 'failed' ||
                              job.state === 'canceled' ||
                              job.state === 'lost') &&
                              'border-destructive/40 bg-destructive/10 text-destructive',
                          )}
                        >
                          {job.state}
                        </span>
                      </td>
                      <td className="py-2 px-3 text-muted-foreground">
                        {job.vm_id || job.worker_id || 'ephemeral'}
                      </td>
                      <td className="py-2 px-3 text-muted-foreground tabular">
                        <span className="inline-flex items-center gap-1">
                          <Clock className="h-3 w-3" />
                          {formatJobDuration(job)}
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
                          {isJobActive(job.state) && (
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
                  );
                })}
              </tbody>
            )}
          </table>
        </div>
      </div>

      {/* Live Job Logs Drawer */}
      {selectedJob && (
        <div
          data-testid="job-logs-drawer"
          className="border border-border bg-background p-4 space-y-3"
        >
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div className="flex flex-wrap items-center gap-2 font-mono text-[12px]">
              <Terminal className="h-3.5 w-3.5 text-[color:var(--link)]" />
              <span className="text-foreground font-medium">{selectedJob.id}</span>
              <span className="text-muted-foreground">({selectedJob.state})</span>
              {selectedJob.repo_name && (
                <span className="text-muted-foreground">
                  · {selectedJob.repo_name}
                  {selectedJob.branch ? `@${selectedJob.branch}` : ''}
                </span>
              )}
              {isJobActive(selectedJob.state) && (
                <span className="border border-primary/40 bg-primary/10 px-1.5 py-0.5 text-[10px] text-primary dark:text-[var(--accent-bright)]">
                  LIVE STREAM
                </span>
              )}
            </div>
            <div className="flex items-center gap-2">
              <button
                type="button"
                onClick={() => void fetchSelectedLogs(selectedJob.id, true)}
                className="inline-flex h-6 items-center gap-1 border border-border px-2 font-mono text-[11px] text-muted-foreground hover:text-foreground"
              >
                <RefreshCw className="h-3 w-3" />
                <span>Refresh Logs</span>
              </button>
              <button
                type="button"
                onClick={() => setSelectedJob(null)}
                aria-label="Close job logs"
                className="text-muted-foreground hover:text-foreground"
              >
                <X className="h-4 w-4" />
              </button>
            </div>
          </div>
          <pre className="max-h-72 overflow-auto border border-border bg-[#060606] p-3 font-mono text-[11.5px] leading-relaxed text-[#fefefe]">
            {logsLoading
              ? 'Loading job execution logs…'
              : jobLogs.length > 0
                ? jobLogs.map((l) => `[${l.stream}] ${l.data}`).join('\n')
                : selectedJob.result_json ||
                  `Job ${selectedJob.id} is ${selectedJob.state}.\nSpec: ${selectedJob.spec_json}`}
          </pre>
        </div>
      )}

      {/* Collapsible CLI & Webhook Setup Guide */}
      <div className="border border-border bg-background">
        <button
          type="button"
          onClick={() => setGuideOpen((v) => !v)}
          className="flex w-full items-center justify-between px-4 py-3 text-left text-[12px] font-medium text-foreground hover:bg-muted/30 transition-colors"
        >
          <span>CLI &amp; Webhook Reference</span>
          <ChevronDown
            className={cn(
              'h-3.5 w-3.5 text-muted-foreground transition-transform',
              guideOpen && 'rotate-180',
            )}
          />
        </button>
        {guideOpen && (
          <div className="border-t border-border px-4 py-3 text-[11.5px] text-muted-foreground space-y-2 leading-relaxed">
            <p>
              Each CI job provisions an isolated ephemeral Ubuntu 24.04 microVM,
              clones the repository at the target branch/commit (using the stored
              GitHub token when configured), executes the command, streams logs, and
              destroys the sandbox disk automatically on completion.
            </p>
            <p>
              Run ad-hoc CLI jobs with{' '}
              <code className="font-mono text-foreground">
                compute job run -- make test
              </code>{' '}
              or point GitHub Webhooks to{' '}
              <code className="font-mono text-foreground">{webhookUrl}</code>.
            </p>
          </div>
        )}
      </div>

      {/* Connect / Edit GitHub Repository Modal */}
      {repoModalOpen && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/75 p-4 overflow-y-auto"
          onClick={() => setRepoModalOpen(false)}
        >
          <form
            role="dialog"
            aria-modal="true"
            aria-labelledby="ci-repo-modal-title"
            onClick={(e) => e.stopPropagation()}
            onSubmit={handleSaveRepo}
            className="w-full max-w-lg border border-border bg-background p-5 space-y-4 text-foreground my-auto"
          >
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                <GitBranch className="h-4 w-4 text-primary" />
                <h2 id="ci-repo-modal-title" className="text-[15px] font-medium">
                  {editingRepo
                    ? `Edit ${editingRepo.name}`
                    : 'Connect GitHub Repository'}
                </h2>
              </div>
              <button
                type="button"
                onClick={() => setRepoModalOpen(false)}
                className="text-muted-foreground hover:text-foreground"
              >
                <X className="h-4 w-4" />
              </button>
            </div>

            <div className="space-y-3">
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
                <div className="sm:col-span-2">
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    Repository URL
                  </label>
                  <input
                    type="text"
                    aria-label="Repository URL"
                    value={repoUrl}
                    onChange={(e) => setRepoUrl(e.target.value)}
                    placeholder="https://github.com/org/repo"
                    required={!repoName.trim()}
                    className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px]"
                  />
                </div>
                <div>
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    Default Branch
                  </label>
                  <input
                    type="text"
                    aria-label="Default branch"
                    value={defaultBranch}
                    onChange={(e) => setDefaultBranch(e.target.value)}
                    placeholder="main"
                    className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px]"
                  />
                </div>
              </div>

              <div>
                <label className="block text-[11px] text-muted-foreground mb-1">
                  Display Name (optional)
                </label>
                <input
                  type="text"
                  aria-label="Repository name"
                  value={repoName}
                  onChange={(e) => setRepoName(e.target.value)}
                  placeholder="org/repo (auto-derived from URL)"
                  className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px]"
                />
              </div>

              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                <div>
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    GitHub Token (optional)
                  </label>
                  <input
                    type="password"
                    aria-label="GitHub token"
                    value={githubToken}
                    onChange={(e) => setGithubToken(e.target.value)}
                    placeholder={
                      editingRepo?.has_token
                        ? 'Leave blank to keep existing'
                        : 'ghp_... (for private repos)'
                    }
                    className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px]"
                  />
                </div>
                <div>
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    Webhook Secret (optional)
                  </label>
                  <input
                    type="text"
                    aria-label="Webhook secret"
                    value={webhookSecret}
                    onChange={(e) => setWebhookSecret(e.target.value)}
                    placeholder={
                      editingRepo?.has_webhook_secret
                        ? 'Leave blank to keep existing'
                        : 'Auto-generated if blank'
                    }
                    className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px]"
                  />
                </div>
              </div>

              <div>
                <label className="block text-[11px] text-muted-foreground mb-1">
                  Default CI Command
                </label>
                <input
                  type="text"
                  aria-label="Default CI command"
                  value={defaultCommand}
                  onChange={(e) => setDefaultCommand(e.target.value)}
                  placeholder="npm ci && npm test"
                  className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px]"
                />
              </div>

              <div className="grid grid-cols-3 gap-2">
                <div>
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    vCPUs
                  </label>
                  <select
                    value={repoVcpus}
                    onChange={(e) => setRepoVcpus(Number(e.target.value))}
                    className="h-8 w-full border border-border bg-background px-2 font-mono text-[12px]"
                  >
                    {[2, 4, 8, 16].map((c) => (
                      <option key={c} value={c}>
                        {c} vCPU
                      </option>
                    ))}
                  </select>
                </div>
                <div>
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    Memory
                  </label>
                  <select
                    value={repoMemoryMb}
                    onChange={(e) => setRepoMemoryMb(Number(e.target.value))}
                    className="h-8 w-full border border-border bg-background px-2 font-mono text-[12px]"
                  >
                    {[1024, 2048, 4096, 8192, 16384].map((m) => (
                      <option key={m} value={m}>
                        {formatMemory(m)}
                      </option>
                    ))}
                  </select>
                </div>
                <div>
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    Timeout (s)
                  </label>
                  <input
                    type="number"
                    min={30}
                    max={7200}
                    value={repoTimeoutSec}
                    onChange={(e) =>
                      setRepoTimeoutSec(Number(e.target.value) || 600)
                    }
                    className="h-8 w-full border border-border bg-background px-2 font-mono text-[12px]"
                  />
                </div>
              </div>
            </div>

            <div className="flex justify-end gap-2 pt-2 border-t border-border">
              <button
                type="button"
                onClick={() => setRepoModalOpen(false)}
                className="h-8 border border-border px-3 text-[12px] text-muted-foreground hover:text-foreground"
              >
                Cancel
              </button>
              <button
                type="submit"
                className="h-8 bg-primary px-3.5 text-[12px] font-medium text-primary-foreground hover:bg-primary-hover"
              >
                {editingRepo ? 'Save Repository' : 'Connect Repository'}
              </button>
            </div>
          </form>
        </div>
      )}

      {/* Trigger Repository CI Job Modal */}
      {triggerTarget && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/75 p-4"
          onClick={() => setTriggerTarget(null)}
        >
          <form
            role="dialog"
            aria-modal="true"
            onClick={(e) => e.stopPropagation()}
            onSubmit={handleTriggerRepo}
            className="w-full max-w-md border border-border bg-background p-5 space-y-4 text-foreground"
          >
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                <Play className="h-4 w-4 text-primary" />
                <h2 className="text-[15px] font-medium">
                  Trigger CI Run · {triggerTarget.name}
                </h2>
              </div>
              <button
                type="button"
                onClick={() => setTriggerTarget(null)}
                className="text-muted-foreground hover:text-foreground"
              >
                <X className="h-4 w-4" />
              </button>
            </div>

            <div className="space-y-3">
              <div className="grid grid-cols-2 gap-2">
                <div>
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    Branch
                  </label>
                  <input
                    type="text"
                    value={triggerBranch}
                    onChange={(e) => setTriggerBranch(e.target.value)}
                    placeholder="main"
                    className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px]"
                  />
                </div>
                <div>
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    Commit SHA (optional)
                  </label>
                  <input
                    type="text"
                    value={triggerCommit}
                    onChange={(e) => setTriggerCommit(e.target.value)}
                    placeholder="HEAD"
                    className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px]"
                  />
                </div>
              </div>
              <div>
                <label className="block text-[11px] text-muted-foreground mb-1">
                  Command Override
                </label>
                <textarea
                  rows={3}
                  value={triggerCommand}
                  onChange={(e) => setTriggerCommand(e.target.value)}
                  className="w-full border border-border bg-background p-2.5 font-mono text-[12px]"
                />
              </div>
            </div>

            <div className="flex justify-end gap-2 pt-2 border-t border-border">
              <button
                type="button"
                onClick={() => setTriggerTarget(null)}
                className="h-8 border border-border px-3 text-[12px] text-muted-foreground hover:text-foreground"
              >
                Cancel
              </button>
              <button
                type="submit"
                className="inline-flex h-8 items-center gap-1.5 bg-primary px-3.5 text-[12px] font-medium text-primary-foreground hover:bg-primary-hover"
              >
                <Play className="h-3 w-3" />
                <span>Start CI Run</span>
              </button>
            </div>
          </form>
        </div>
      )}

      {/* Run Ad-hoc Ephemeral Job Modal */}
      {dispatchOpen && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/75 p-4"
          onClick={() => setDispatchOpen(false)}
        >
          <form
            role="dialog"
            aria-modal="true"
            onClick={(e) => e.stopPropagation()}
            onSubmit={handleDispatchJob}
            className="w-full max-w-lg border border-border bg-background p-5 space-y-4 text-foreground"
          >
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                <Terminal className="h-4 w-4 text-primary" />
                <h2 className="text-[15px] font-medium">
                  Run Ad-hoc Ephemeral Job
                </h2>
              </div>
              <button
                type="button"
                onClick={() => setDispatchOpen(false)}
                className="text-muted-foreground hover:text-foreground"
              >
                <X className="h-4 w-4" />
              </button>
            </div>

            <div className="flex items-center justify-between border border-border bg-muted/20 px-3 py-2 font-mono text-[11px]">
              <span className="text-muted-foreground">Base Environment</span>
              <span className="text-foreground">Ubuntu 24.04 LTS (ubuntu-24)</span>
            </div>

            <div className="space-y-3">
              <div>
                <label className="block text-[11px] text-muted-foreground mb-1">
                  Shell Command
                </label>
                <textarea
                  rows={3}
                  value={jobCommand}
                  onChange={(e) => setJobCommand(e.target.value)}
                  required
                  className="w-full border border-border bg-background p-2.5 font-mono text-[12px]"
                />
              </div>
              <div className="grid grid-cols-4 gap-2">
                <div>
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    vCPUs
                  </label>
                  <select
                    value={jobVcpus}
                    onChange={(e) => setJobVcpus(Number(e.target.value))}
                    className="h-8 w-full border border-border bg-background px-2 font-mono text-[12px]"
                  >
                    {[2, 4, 8, 16].map((c) => (
                      <option key={c} value={c}>
                        {c} vCPU
                      </option>
                    ))}
                  </select>
                </div>
                <div>
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    Memory
                  </label>
                  <select
                    value={jobMemoryMb}
                    onChange={(e) => setJobMemoryMb(Number(e.target.value))}
                    className="h-8 w-full border border-border bg-background px-2 font-mono text-[12px]"
                  >
                    {[1024, 2048, 4096, 8192, 16384].map((m) => (
                      <option key={m} value={m}>
                        {formatMemory(m)}
                      </option>
                    ))}
                  </select>
                </div>
                <div>
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    Timeout (s)
                  </label>
                  <input
                    type="number"
                    min={10}
                    max={7200}
                    value={jobTimeoutSec}
                    onChange={(e) =>
                      setJobTimeoutSec(Number(e.target.value) || 300)
                    }
                    className="h-8 w-full border border-border bg-background px-2 font-mono text-[12px]"
                  />
                </div>
                <div>
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    Priority
                  </label>
                  <input
                    type="number"
                    value={jobPriority}
                    onChange={(e) => setJobPriority(Number(e.target.value) || 0)}
                    className="h-8 w-full border border-border bg-background px-2 font-mono text-[12px]"
                  />
                </div>
              </div>
            </div>

            <div className="flex justify-end gap-2 pt-2 border-t border-border">
              <button
                type="button"
                onClick={() => setDispatchOpen(false)}
                className="h-8 border border-border px-3 text-[12px] text-muted-foreground hover:text-foreground"
              >
                Cancel
              </button>
              <button
                type="submit"
                className="inline-flex h-8 items-center gap-1.5 bg-primary px-3.5 text-[12px] font-medium text-primary-foreground hover:bg-primary-hover"
              >
                <Play className="h-3 w-3" />
                <span>Run Job</span>
              </button>
            </div>
          </form>
        </div>
      )}
    </div>
  );
}
