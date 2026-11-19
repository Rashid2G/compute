import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  AlertCircle,
  AlertTriangle,
  CheckCircle2,
  Clock,
  Copy,
  Database,
  History,
  Layers,
  Loader2,
  Package,
  Play,
  Plus,
  RefreshCw,
  RotateCw,
  Search,
  Terminal,
  Trash2,
  X,
  Zap,
} from 'lucide-react';
import { toast } from 'sonner';

import { PrepareImageDialog } from '@/components/prepare-image-dialog';
import { RunJobDialog } from '@/components/run-job-dialog';
import { api, errorMessage } from '@/lib/api';
import { formatMemory, formatRelativeTime } from '@/lib/format';
import type { PageId } from '@/lib/navigation';
import type { ImageListItem, JobLogRecord, JobRecord, Template, VM } from '@/lib/types';

interface ImagesPageProps {
  vms: VM[];
  templates?: Template[];
  images?: ImageListItem[];
  onLaunchVM?: () => void;
  onNavigate?: (page: PageId) => void;
  onRefreshCluster?: () => void;
}

export function ImagesPage({
  vms: _vms,
  templates: propTemplates,
  images: propImages,
  onLaunchVM: _onLaunchVM,
  onNavigate,
  onRefreshCluster,
}: ImagesPageProps) {
  const [activeTab, setActiveTab] = useState<'templates' | 'builds'>('templates');
  const [templates, setTemplates] = useState<Template[]>(propTemplates || []);
  const [images, setImages] = useState<ImageListItem[]>(propImages || []);
  const [jobs, setJobs] = useState<JobRecord[]>([]);
  const [searchQuery, setSearchQuery] = useState('');
  const [loadingJobs, setLoadingJobs] = useState(false);

  // Dialog states
  const [prepareDialogOpen, setPrepareDialogOpen] = useState(false);
  const [prepareInitialValues, setPrepareInitialValues] = useState<{
    name?: string;
    description?: string;
    base_image?: string;
    packages?: string[];
    script?: string;
    vcpus?: number;
    memory_mb?: number;
    disk_mb?: number;
  }>({});

  const [launchModalTpl, setLaunchModalTpl] = useState<Template | null>(null);
  const [launchName, setLaunchName] = useState('');
  const [launchAutoStart, setLaunchAutoStart] = useState(true);

  const [runJobModalTpl, setRunJobModalTpl] = useState<Template | null>(null);

  // Log viewer modal
  const [viewLogJob, setViewLogJob] = useState<JobRecord | null>(null);
  const [jobLogs, setJobLogs] = useState<JobLogRecord[]>([]);
  const [loadingLogs, setLoadingLogs] = useState(false);
  const logTerminalRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (propTemplates) setTemplates(propTemplates);
  }, [propTemplates]);

  useEffect(() => {
    if (propImages) setImages(propImages);
  }, [propImages]);

  const loadJobs = useCallback(async () => {
    setLoadingJobs(true);
    try {
      const allJobs = await api.listJobs();
      // Filter for image build jobs or jobs targeting a template
      const imageJobs = (allJobs || []).filter((j) => {
        if (j.type === 'image-build' || j.type === 'image.build' || j.source === 'image-builder') {
          return true;
        }
        try {
          const spec = JSON.parse(j.spec_json || '{}');
          return Boolean(spec.target_template);
        } catch {
          return false;
        }
      });
      setJobs(imageJobs);
    } catch {
      // ignore transient error
    } finally {
      setLoadingJobs(false);
    }
  }, []);

  useEffect(() => {
    void loadJobs();
  }, [loadJobs]);

  // Polling while any build is active
  const hasActiveBuilds = useMemo(() => {
    return jobs.some((j) => j.state === 'queued' || j.state === 'running' || j.state === 'assigned');
  }, [jobs]);

  useEffect(() => {
    if (!hasActiveBuilds && !viewLogJob) return;
    const interval = setInterval(() => {
      void loadJobs();
    }, 2000);
    return () => clearInterval(interval);
  }, [hasActiveBuilds, loadJobs, viewLogJob]);

  // Load logs when viewLogJob changes or polls
  const loadJobLogs = useCallback(async (jobId: string) => {
    setLoadingLogs(true);
    try {
      const logs = await api.getJobLogs(jobId);
      setJobLogs(logs || []);
    } catch {
      // ignore
    } finally {
      setLoadingLogs(false);
    }
  }, []);

  useEffect(() => {
    if (!viewLogJob) {
      setJobLogs([]);
      return;
    }
    void loadJobLogs(viewLogJob.id);

    const isRunning = viewLogJob.state === 'queued' || viewLogJob.state === 'running';
    if (!isRunning) return;

    const interval = setInterval(() => {
      void loadJobLogs(viewLogJob.id);
      // Also refresh job state
      void api.listJobs().then((all) => {
        const updated = (all || []).find((j) => j.id === viewLogJob.id);
        if (updated) setViewLogJob(updated);
      });
    }, 1500);
    return () => clearInterval(interval);
  }, [viewLogJob, loadJobLogs]);

  // Auto scroll logs
  useEffect(() => {
    if (logTerminalRef.current) {
      logTerminalRef.current.scrollTop = logTerminalRef.current.scrollHeight;
    }
  }, [jobLogs]);

  // Filter templates
  const filteredTemplates = useMemo(() => {
    const q = searchQuery.trim().toLowerCase();
    if (!q) return templates;
    return templates.filter((tpl) => {
      return (
        tpl.name.toLowerCase().includes(q) ||
        (tpl.description || '').toLowerCase().includes(q) ||
        tpl.id.toLowerCase().includes(q)
      );
    });
  }, [templates, searchQuery]);

  function openLaunchModal(tpl: Template) {
    setLaunchModalTpl(tpl);
    setLaunchName(`${tpl.name}-${Math.random().toString(36).slice(2, 6)}`);
    setLaunchAutoStart(true);
  }

  function handleLaunchSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!launchModalTpl) return;
    const tpl = launchModalTpl;
    const name = launchName.trim() || `${tpl.name}-spawn`;
    const autoStart = launchAutoStart;

    setLaunchModalTpl(null);
    const toastId = toast.loading(`Launching ${name} from template ${tpl.name}…`);
    void api
      .restoreSnapshot(tpl.id, name, {
        vcpus: tpl.vcpus,
        memory_mb: tpl.memory_mb,
        auto_start: autoStart,
      })
      .then(() => {
        toast.success(`Launched sandbox ${name}`, { id: toastId });
        onRefreshCluster?.();
        onNavigate?.('instances');
      })
      .catch((err) => {
        toast.error(errorMessage(err), { id: toastId });
      });
  }

  function handleRebuild(job: JobRecord) {
    try {
      const spec = JSON.parse(job.spec_json || '{}');
      setPrepareInitialValues({
        name: spec.target_template || job.repo_name || '',
        description: spec.target_description || '',
        base_image: spec.image_id || 'ubuntu-24',
        packages: spec.packages || [],
        script: spec.script || (spec.command && spec.command.length > 2 ? spec.command[2] : ''),
        vcpus: spec.vcpus || 2,
        memory_mb: spec.memory_mb || 2048,
        disk_mb: spec.disk_mb || 10240,
      });
    } catch {
      setPrepareInitialValues({
        name: job.repo_name || '',
      });
    }
    setPrepareDialogOpen(true);
  }

  function extractJobSpec(job: JobRecord) {
    try {
      return JSON.parse(job.spec_json || '{}');
    } catch {
      return {};
    }
  }

  function extractJobResult(job: JobRecord) {
    try {
      return JSON.parse(job.result_json || '{}');
    } catch {
      return {};
    }
  }

  // Format packages for display
  const templatePackageMap = useMemo(() => {
    const map = new Map<string, string[]>();
    // Look in custom images
    for (const img of images) {
      if (img.packages && img.packages.length > 0) {
        map.set(img.name, img.packages);
      }
    }
    // Also look in succeeded jobs
    for (const j of jobs) {
      if (j.state === 'succeeded') {
        const spec = extractJobSpec(j);
        const name = spec.target_template || j.repo_name;
        if (name && spec.packages && spec.packages.length > 0) {
          map.set(name, spec.packages);
        }
      }
    }
    return map;
  }, [images, jobs]);

  return (
    <div className="space-y-6">
      {/* Top Header */}
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="space-y-1">
          <div className="flex items-center gap-2">
            <Layers className="h-6 w-6 text-foreground" />
            <h1 className="font-display text-[22px] font-medium tracking-tight text-foreground">
              Images
            </h1>
          </div>
          <p className="text-[12px] text-muted-foreground max-w-2xl leading-relaxed">
            Prepare custom OS images with pre-configured software, packages, and setup scripts. Each build executes in an isolated sandbox and produces a reusable template to launch sandboxes and jobs in milliseconds.
          </p>
        </div>

        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={() => {
              setPrepareInitialValues({});
              setPrepareDialogOpen(true);
            }}
            className="flex h-8 items-center gap-1.5 bg-primary hover:bg-primary-hover px-3 text-[12px] font-medium text-primary-foreground shadow-sm transition-colors"
          >
            <Plus className="h-3.5 w-3.5" />
            <span>Prepare Image</span>
          </button>
        </div>
      </div>

      {/* Tabs Navigation */}
      <div className="flex items-center justify-between border-b border-border">
        <div className="flex items-center gap-6">
          <button
            type="button"
            onClick={() => setActiveTab('templates')}
            className={`pb-2.5 text-[12.5px] font-medium transition-colors relative ${
              activeTab === 'templates'
                ? 'text-foreground font-semibold'
                : 'text-muted-foreground hover:text-foreground'
            }`}
          >
            <div className="flex items-center gap-2">
              <Package className="h-4 w-4" />
              <span>Built Image Templates</span>
              <span className="rounded-full border border-border bg-muted/30 px-1.5 py-0.2 font-mono text-[10px] text-muted-foreground">
                {templates.length}
              </span>
            </div>
            {activeTab === 'templates' && (
              <span className="absolute bottom-0 left-0 right-0 h-0.5 bg-primary" />
            )}
          </button>

          <button
            type="button"
            onClick={() => setActiveTab('builds')}
            className={`pb-2.5 text-[12.5px] font-medium transition-colors relative ${
              activeTab === 'builds'
                ? 'text-foreground font-semibold'
                : 'text-muted-foreground hover:text-foreground'
            }`}
          >
            <div className="flex items-center gap-2">
              <History className="h-4 w-4" />
              <span>Build History &amp; Logs</span>
              {hasActiveBuilds ? (
                <span className="flex items-center gap-1 rounded-full bg-primary/10 px-2 py-0.5 font-mono text-[10px] text-primary font-medium animate-pulse">
                  <Loader2 className="h-2.5 w-2.5 animate-spin" />
                  Building
                </span>
              ) : (
                <span className="rounded-full border border-border bg-muted/30 px-1.5 py-0.2 font-mono text-[10px] text-muted-foreground">
                  {jobs.length}
                </span>
              )}
            </div>
            {activeTab === 'builds' && (
              <span className="absolute bottom-0 left-0 right-0 h-0.5 bg-primary" />
            )}
          </button>
        </div>

        {activeTab === 'templates' && (
          <div className="relative mb-2">
            <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
            <input
              type="text"
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              placeholder="Search templates…"
              className="h-7 w-48 border border-border bg-background pl-8 pr-2.5 text-[11.5px] text-foreground focus:border-primary focus:outline-none"
            />
          </div>
        )}
      </div>

      {/* ==================================================================== */}
      {/* TAB 1: Templates & Images */}
      {/* ==================================================================== */}
      {activeTab === 'templates' && (
        <div className="space-y-4">
          <div className="border border-border bg-background">
            <div className="flex items-center justify-between border-b border-border bg-muted/20 px-3 py-2 text-[11.5px] font-medium text-foreground">
              <span>Ready Image Templates ({filteredTemplates.length})</span>
              <span className="font-mono text-[10.5px] text-muted-foreground">
                Base OS: Ubuntu 24.04 LTS
              </span>
            </div>
            <table className="w-full border-collapse text-left text-[12px]">
              <thead>
                <tr className="border-b border-border bg-muted/30 text-[11.5px] text-muted-foreground">
                  <th className="py-2.5 px-3 font-normal">Template / Image Name</th>
                  <th className="py-2.5 px-3 font-normal">Pre-configured Software &amp; Packages</th>
                  <th className="py-2.5 px-3 font-normal">Hardware Spec</th>
                  <th className="py-2.5 px-3 font-normal">Created</th>
                  <th className="py-2.5 px-3 text-right font-normal">Actions</th>
                </tr>
              </thead>
              {filteredTemplates.length === 0 ? (
                <tbody>
                  <tr>
                    <td colSpan={5} className="py-16 text-center text-muted-foreground">
                      <Layers className="mx-auto h-8 w-8 text-muted-foreground/40 mb-2" />
                      <div className="text-[13px] font-medium text-foreground">
                        No image templates prepared yet
                      </div>
                      <p className="text-[11.5px] text-muted-foreground mt-1 max-w-sm mx-auto">
                        Click &ldquo;Prepare Image&rdquo; to configure software packages and build your first reusable template.
                      </p>
                      <button
                        type="button"
                        onClick={() => {
                          setPrepareInitialValues({});
                          setPrepareDialogOpen(true);
                        }}
                        className="mt-4 inline-flex items-center gap-1.5 bg-primary hover:bg-primary-hover px-3 py-1.5 text-[11.5px] font-medium text-primary-foreground"
                      >
                        <Plus className="h-3 w-3" />
                        <span>Prepare Image</span>
                      </button>
                    </td>
                  </tr>
                </tbody>
              ) : (
                <tbody className="divide-y divide-border">
                  {filteredTemplates.map((tpl) => {
                    const pkgs = templatePackageMap.get(tpl.name) || [];
                    return (
                      <tr key={tpl.id} className="hover:bg-muted/30 transition-colors">
                        <td className="py-3 px-3">
                          <div className="flex items-center gap-2">
                            <span className="font-mono font-medium text-foreground">{tpl.name}</span>
                            <span className="rounded bg-emerald-500/10 px-1.5 py-0.2 font-mono text-[10px] text-emerald-500 font-medium">
                              ready
                            </span>
                          </div>
                          {tpl.description ? (
                            <div className="text-[11px] text-muted-foreground mt-0.5">
                              {tpl.description}
                            </div>
                          ) : (
                            <div className="text-[10.5px] font-mono text-muted-foreground/70 mt-0.5">
                              id: {tpl.id}
                            </div>
                          )}
                        </td>

                        <td className="py-3 px-3">
                          {pkgs.length > 0 ? (
                            <div className="flex flex-wrap gap-1 max-w-sm">
                              {pkgs.slice(0, 5).map((p) => (
                                <span
                                  key={p}
                                  className="border border-border bg-muted/40 px-1.5 py-0.5 font-mono text-[10px] text-foreground"
                                >
                                  {p}
                                </span>
                              ))}
                              {pkgs.length > 5 && (
                                <span className="text-[10px] text-muted-foreground self-center">
                                  +{pkgs.length - 5} more
                                </span>
                              )}
                            </div>
                          ) : (
                            <span className="text-[11px] text-muted-foreground">Ubuntu 24 base image</span>
                          )}
                        </td>

                        <td className="py-3 px-3 font-mono text-[11px] text-foreground">
                          {tpl.vcpus} vCPU · {formatMemory(tpl.memory_mb)} · {tpl.disk_mb ? `${tpl.disk_mb} MB` : '50 GB'}
                        </td>

                        <td className="py-3 px-3 font-mono text-[11px] text-muted-foreground">
                          {formatRelativeTime(tpl.created_at)}
                        </td>

                        <td className="py-3 px-3 text-right">
                          <div className="inline-flex items-center gap-1.5">
                            {/* Launch Sandbox Button */}
                            <button
                              type="button"
                              onClick={() => openLaunchModal(tpl)}
                              title={`Launch a new Sandbox from ${tpl.name}`}
                              className="flex h-7 items-center gap-1 bg-primary hover:bg-primary-hover px-2.5 text-[11px] font-medium text-primary-foreground transition-colors"
                            >
                              <Play className="h-3 w-3 fill-current" />
                              <span>Launch Sandbox</span>
                            </button>

                            {/* Run Job Button */}
                            <button
                              type="button"
                              onClick={() => setRunJobModalTpl(tpl)}
                              title={`Execute a workload job using ${tpl.name} as base`}
                              className="flex h-7 items-center gap-1 border border-border bg-background hover:bg-muted px-2 text-[11px] font-medium text-foreground transition-colors"
                            >
                              <Zap className="h-3 w-3 text-primary" />
                              <span>Run Job</span>
                            </button>

                            {/* Delete Template Button */}
                            <button
                              type="button"
                              aria-label={`Delete template ${tpl.name}`}
                              title={`Delete template ${tpl.name}`}
                              onClick={() => {
                                if (window.confirm(`Delete template "${tpl.name}"? This cannot be undone.`)) {
                                  void api
                                    .deleteTemplate(tpl.id)
                                    .then(() => {
                                      toast.success(`Deleted template "${tpl.name}"`);
                                      setTemplates((prev) => prev.filter((t) => t.id !== tpl.id));
                                      onRefreshCluster?.();
                                    })
                                    .catch((err) => toast.error(errorMessage(err)));
                                }
                              }}
                              className="flex h-7 w-7 items-center justify-center border border-border text-muted-foreground hover:text-destructive hover:border-destructive transition-colors"
                            >
                              <Trash2 className="h-3 w-3" />
                            </button>
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
      )}

      {/* ==================================================================== */}
      {/* TAB 2: Build History & Logs */}
      {/* ==================================================================== */}
      {activeTab === 'builds' && (
        <div className="space-y-4">
          <div className="flex items-center justify-between">
            <p className="text-[12px] text-muted-foreground">
              History of all image preparation and configuration jobs. Click &ldquo;View Logs&rdquo; for full terminal output.
            </p>
            <button
              type="button"
              onClick={() => void loadJobs()}
              disabled={loadingJobs}
              className="flex items-center gap-1 text-[11.5px] text-muted-foreground hover:text-foreground transition-colors"
            >
              <RefreshCw className={`h-3 w-3 ${loadingJobs ? 'animate-spin' : ''}`} />
              <span>Refresh</span>
            </button>
          </div>

          <div className="border border-border bg-background">
            <table className="w-full border-collapse text-left text-[12px]">
              <thead>
                <tr className="border-b border-border bg-muted/30 text-[11.5px] text-muted-foreground">
                  <th className="py-2.5 px-3 font-normal">Target Template</th>
                  <th className="py-2.5 px-3 font-normal">Build Status</th>
                  <th className="py-2.5 px-3 font-normal">Base Image</th>
                  <th className="py-2.5 px-3 font-normal">Configured Packages</th>
                  <th className="py-2.5 px-3 font-normal">Duration</th>
                  <th className="py-2.5 px-3 font-normal">Triggered</th>
                  <th className="py-2.5 px-3 text-right font-normal">Actions</th>
                </tr>
              </thead>
              {jobs.length === 0 ? (
                <tbody>
                  <tr>
                    <td colSpan={7} className="py-16 text-center text-muted-foreground">
                      <History className="mx-auto h-7 w-7 text-muted-foreground/40 mb-2" />
                      <div className="text-[12.5px]">No image builds recorded yet</div>
                      <p className="text-[11.5px] text-muted-foreground mt-1">
                        Build your first custom image to see execution logs and diagnostics here.
                      </p>
                    </td>
                  </tr>
                </tbody>
              ) : (
                <tbody className="divide-y divide-border">
                  {jobs.map((job) => {
                    const spec = extractJobSpec(job);
                    const result = extractJobResult(job);
                    const targetName = spec.target_template || job.repo_name || 'custom-image';
                    const pkgs: string[] = spec.packages || [];
                    const isRunning = job.state === 'running' || job.state === 'queued' || job.state === 'assigned';
                    const isSucceeded = job.state === 'succeeded' || job.state === 'completed';
                    const isFailed = job.state === 'failed';

                    return (
                      <tr key={job.id} className="hover:bg-muted/30 transition-colors">
                        <td className="py-3 px-3">
                          <div className="font-mono font-medium text-foreground">{targetName}</div>
                          <div className="font-mono text-[10.5px] text-muted-foreground">
                            job: {job.id}
                          </div>
                        </td>

                        <td className="py-3 px-3">
                          {isRunning && (
                            <span className="inline-flex items-center gap-1.5 rounded bg-blue-500/10 px-2 py-0.5 font-mono text-[11px] font-medium text-blue-500">
                              <Loader2 className="h-3 w-3 animate-spin" />
                              <span>building</span>
                            </span>
                          )}
                          {isSucceeded && (
                            <span className="inline-flex items-center gap-1 rounded bg-emerald-500/10 px-2 py-0.5 font-mono text-[11px] font-medium text-emerald-500">
                              <CheckCircle2 className="h-3 w-3" />
                              <span>succeeded</span>
                            </span>
                          )}
                          {isFailed && (
                            <div className="space-y-0.5">
                              <span className="inline-flex items-center gap-1 rounded bg-destructive/10 px-2 py-0.5 font-mono text-[11px] font-medium text-destructive">
                                <AlertTriangle className="h-3 w-3" />
                                <span>failed</span>
                              </span>
                              {result.error && (
                                <div className="text-[10.5px] text-destructive max-w-xs truncate" title={result.error}>
                                  {result.error}
                                </div>
                              )}
                            </div>
                          )}
                        </td>

                        <td className="py-3 px-3 font-mono text-[11px] text-muted-foreground">
                          {spec.image_id || 'ubuntu-24'}
                        </td>

                        <td className="py-3 px-3">
                          {pkgs.length > 0 ? (
                            <div className="flex flex-wrap gap-1 max-w-xs">
                              {pkgs.map((p) => (
                                <span
                                  key={p}
                                  className="border border-border bg-muted/40 px-1.5 py-0.2 font-mono text-[10px] text-foreground"
                                >
                                  {p}
                                </span>
                              ))}
                            </div>
                          ) : (
                            <span className="text-[11px] text-muted-foreground">None</span>
                          )}
                        </td>

                        <td className="py-3 px-3 font-mono text-[11px] text-muted-foreground">
                          {result.duration_ms ? `${Math.round(result.duration_ms / 1000)}s` : '—'}
                        </td>

                        <td className="py-3 px-3 font-mono text-[11px] text-muted-foreground">
                          {formatRelativeTime(job.created_at)}
                        </td>

                        <td className="py-3 px-3 text-right">
                          <div className="inline-flex items-center gap-1.5">
                            <button
                              type="button"
                              onClick={() => setViewLogJob(job)}
                              className="flex h-7 items-center gap-1 border border-border bg-background hover:bg-muted px-2.5 text-[11px] font-medium text-foreground transition-colors"
                            >
                              <Terminal className="h-3 w-3 text-muted-foreground" />
                              <span>View Logs</span>
                            </button>

                            {isFailed && (
                              <button
                                type="button"
                                onClick={() => handleRebuild(job)}
                                title="Adjust parameters and rebuild"
                                className="flex h-7 items-center gap-1 bg-primary hover:bg-primary-hover px-2 text-[11px] font-medium text-primary-foreground transition-colors"
                              >
                                <RotateCw className="h-3 w-3" />
                                <span>Rebuild</span>
                              </button>
                            )}

                            {isRunning && (
                              <button
                                type="button"
                                onClick={() => {
                                  void api
                                    .cancelJob(job.id)
                                    .then(() => {
                                      toast.info(`Canceling job ${job.id}`);
                                      void loadJobs();
                                    })
                                    .catch((err) => toast.error(errorMessage(err)));
                                }}
                                className="flex h-7 items-center gap-1 border border-border text-muted-foreground hover:text-destructive px-2 text-[11px] transition-colors"
                              >
                                <X className="h-3 w-3" />
                                <span>Cancel</span>
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
      )}

      {/* ==================================================================== */}
      {/* Prepare Image Dialog */}
      {/* ==================================================================== */}
      <PrepareImageDialog
        open={prepareDialogOpen}
        onOpenChange={setPrepareDialogOpen}
        templates={templates}
        initialValues={prepareInitialValues}
        onBuildStarted={(job) => {
          setActiveTab('builds');
          void loadJobs();
          setViewLogJob(job);
        }}
      />

      {/* ==================================================================== */}
      {/* Launch Job from Template Dialog */}
      {/* ==================================================================== */}
      <RunJobDialog
        open={Boolean(runJobModalTpl)}
        onOpenChange={(open) => {
          if (!open) setRunJobModalTpl(null);
        }}
        template={runJobModalTpl}
        onJobStarted={() => {
          onRefreshCluster?.();
          onNavigate?.('jobs');
        }}
      />

      {/* ==================================================================== */}
      {/* Launch Sandbox from Template Dialog */}
      {/* ==================================================================== */}
      {launchModalTpl && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/75 p-4"
          onClick={() => setLaunchModalTpl(null)}
        >
          <div
            role="dialog"
            aria-modal="true"
            aria-labelledby="launch-template-title"
            className="w-full max-w-[420px] border border-border bg-background p-6 text-foreground shadow-2xl"
            onClick={(e) => e.stopPropagation()}
          >
            <div className="flex items-start justify-between">
              <div className="space-y-1">
                <h2 id="launch-template-title" className="text-[17px] font-semibold text-foreground">
                  Launch Sandbox
                </h2>
                <p className="text-[12px] text-muted-foreground">
                  Instantiate a new microVM from template{' '}
                  <span className="font-mono font-medium text-foreground">{launchModalTpl.name}</span>.
                </p>
              </div>
              <button
                type="button"
                onClick={() => setLaunchModalTpl(null)}
                className="flex h-7 w-7 items-center justify-center text-muted-foreground hover:text-foreground"
              >
                <X className="h-4 w-4" />
              </button>
            </div>

            <form onSubmit={handleLaunchSubmit} className="mt-4 space-y-4 text-[12px]">
              <div className="space-y-1.5">
                <label className="text-[11.5px] font-medium text-foreground">Sandbox Name</label>
                <input
                  type="text"
                  required
                  value={launchName}
                  onChange={(e) => setLaunchName(e.target.value)}
                  className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px] text-foreground focus:border-primary focus:outline-none"
                />
              </div>

              <div className="flex items-center gap-2 pt-1">
                <input
                  type="checkbox"
                  id="launch-auto-start"
                  checked={launchAutoStart}
                  onChange={(e) => setLaunchAutoStart(e.target.checked)}
                  className="h-4 w-4 border-border bg-background text-primary focus:ring-primary cursor-pointer"
                />
                <label htmlFor="launch-auto-start" className="text-[12px] text-foreground cursor-pointer">
                  Boot and start sandbox immediately
                </label>
              </div>

              <div className="flex items-center justify-end gap-2 pt-3 border-t border-border">
                <button
                  type="button"
                  onClick={() => setLaunchModalTpl(null)}
                  className="h-8 border border-border px-3 text-[12px] font-normal text-muted-foreground hover:border-foreground hover:text-foreground transition-colors"
                >
                  Cancel
                </button>
                <button
                  type="submit"
                  className="flex h-8 items-center gap-1.5 bg-primary hover:bg-primary-hover px-4 text-[12px] font-medium text-primary-foreground transition-colors"
                >
                  <Play className="h-3.5 w-3.5 fill-current" />
                  <span>Launch</span>
                </button>
              </div>
            </form>
          </div>
        </div>
      )}

      {/* ==================================================================== */}
      {/* Build Log Viewer Modal / Terminal */}
      {/* ==================================================================== */}
      {viewLogJob && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/80 p-4"
          onClick={() => setViewLogJob(null)}
        >
          <div
            role="dialog"
            aria-modal="true"
            aria-labelledby="build-log-title"
            className="flex flex-col w-full max-w-3xl max-h-[85vh] border border-border bg-[#0d1117] text-[#c9d1d9] shadow-2xl overflow-hidden"
            onClick={(e) => e.stopPropagation()}
          >
            {/* Header */}
            <div className="flex items-center justify-between border-b border-[#30363d] px-4 py-3 bg-[#161b22]">
              <div className="flex items-center gap-2">
                <Terminal className="h-4 w-4 text-primary" />
                <h3 id="build-log-title" className="font-mono text-[13px] font-semibold text-white">
                  Build Logs — {extractJobSpec(viewLogJob).target_template || viewLogJob.repo_name || viewLogJob.id}
                </h3>
                <span className="font-mono text-[11px] text-muted-foreground">({viewLogJob.id})</span>
              </div>

              <div className="flex items-center gap-2">
                <button
                  type="button"
                  onClick={() => {
                    const text = jobLogs.map((l) => l.data).join('\n');
                    void navigator.clipboard.writeText(text);
                    toast.success('Logs copied to clipboard');
                  }}
                  className="flex h-7 items-center gap-1 border border-[#30363d] bg-[#21262d] px-2 text-[11px] text-[#c9d1d9] hover:bg-[#30363d] transition-colors"
                >
                  <Copy className="h-3 w-3" />
                  <span>Copy</span>
                </button>
                <button
                  type="button"
                  onClick={() => setViewLogJob(null)}
                  className="flex h-7 w-7 items-center justify-center text-[#8b949e] hover:text-white"
                >
                  <X className="h-4 w-4" />
                </button>
              </div>
            </div>

            {/* Diagnostic Banner */}
            {viewLogJob.state === 'failed' && (
              <div className="border-b border-red-500/20 bg-red-950/40 p-3.5 flex items-start justify-between gap-3">
                <div className="flex items-start gap-2.5">
                  <AlertCircle className="h-4 w-4 text-red-400 mt-0.5 shrink-0" />
                  <div className="space-y-0.5">
                    <div className="text-[12.5px] font-medium text-red-200">
                      Image build failed
                    </div>
                    <div className="text-[11.5px] text-red-300/80 font-mono">
                      {extractJobResult(viewLogJob).error || 'Command exited with a non-zero exit code.'}
                    </div>
                  </div>
                </div>
                <button
                  type="button"
                  onClick={() => {
                    setViewLogJob(null);
                    handleRebuild(viewLogJob);
                  }}
                  className="flex items-center gap-1.5 shrink-0 bg-red-600 hover:bg-red-500 px-3 py-1 text-[11px] font-medium text-white transition-colors"
                >
                  <RotateCw className="h-3 w-3" />
                  <span>Edit &amp; Rebuild</span>
                </button>
              </div>
            )}

            {viewLogJob.state === 'succeeded' && (
              <div className="border-b border-emerald-500/20 bg-emerald-950/30 p-3.5 flex items-center justify-between gap-3">
                <div className="flex items-center gap-2.5">
                  <CheckCircle2 className="h-4 w-4 text-emerald-400 shrink-0" />
                  <div className="text-[12.5px] font-medium text-emerald-200">
                    Image build completed successfully. Template is ready to launch.
                  </div>
                </div>
                <div className="flex items-center gap-2">
                  <button
                    type="button"
                    onClick={() => {
                      const res = extractJobResult(viewLogJob);
                      const targetName = res.template_name || extractJobSpec(viewLogJob).target_template;
                      const tpl = templates.find((t) => t.name === targetName);
                      if (tpl) {
                        setViewLogJob(null);
                        openLaunchModal(tpl);
                      } else {
                        onRefreshCluster?.();
                        toast.info(`Template ${targetName} created. Refreshing list…`);
                      }
                    }}
                    className="flex items-center gap-1 bg-emerald-600 hover:bg-emerald-500 px-2.5 py-1 text-[11px] font-medium text-white transition-colors"
                  >
                    <Play className="h-3 w-3 fill-current" />
                    <span>Launch Sandbox</span>
                  </button>
                </div>
              </div>
            )}

            {(viewLogJob.state === 'running' || viewLogJob.state === 'queued') && (
              <div className="border-b border-blue-500/20 bg-blue-950/30 p-3 flex items-center gap-2.5 text-[12px] text-blue-200">
                <Loader2 className="h-3.5 w-3.5 animate-spin text-blue-400" />
                <span>Build in progress… Running configuration commands inside ephemeral sandbox.</span>
              </div>
            )}

            {/* Terminal Body */}
            <div
              ref={logTerminalRef}
              className="flex-1 p-4 font-mono text-[11px] leading-relaxed overflow-y-auto space-y-1 bg-[#090d13] text-[#c9d1d9] select-text min-h-[300px]"
            >
              {loadingLogs && jobLogs.length === 0 ? (
                <div className="flex items-center justify-center py-12 text-[#8b949e]">
                  <Loader2 className="h-4 w-4 animate-spin mr-2" />
                  <span>Loading logs…</span>
                </div>
              ) : jobLogs.length === 0 ? (
                <div className="text-[#8b949e] italic py-8 text-center">
                  Waiting for log output from builder sandbox…
                </div>
              ) : (
                jobLogs.map((log) => {
                  const isErr = log.stream === 'stderr';
                  return (
                    <div
                      key={log.seq}
                      className={`break-all whitespace-pre-wrap ${
                        isErr ? 'text-[#ff7b72]' : 'text-[#c9d1d9]'
                      }`}
                    >
                      {log.data}
                    </div>
                  );
                })
              )}
            </div>

            {/* Terminal Footer */}
            <div className="border-t border-[#30363d] px-4 py-2 bg-[#161b22] flex items-center justify-between text-[11px] text-[#8b949e] font-mono">
              <div className="flex items-center gap-3">
                <span>Lines: {jobLogs.length}</span>
                <span>State: {viewLogJob.state}</span>
              </div>
              <div className="flex items-center gap-1.5">
                <Clock className="h-3 w-3" />
                <span>
                  {extractJobResult(viewLogJob).duration_ms
                    ? `${Math.round(extractJobResult(viewLogJob).duration_ms / 1000)}s`
                    : 'running'}
                </span>
              </div>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
