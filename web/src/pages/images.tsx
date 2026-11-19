import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  AlertCircle,
  AlertTriangle,
  Bookmark,
  CheckCircle2,
  Clock,
  Copy,
  Cpu,
  Database,
  Download,
  Filter,
  HardDrive,
  History as HistoryIcon,
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
import {
  deleteStoredDraft,
  getStoredDrafts,
  reconcileDraftsWithJobsAndTemplates,
  saveStoredDraft,
} from '@/lib/image-drafts';
import type { PageId } from '@/lib/navigation';
import type {
  ImageDraft,
  ImageListItem,
  JobLogRecord,
  JobRecord,
  Template,
  VM,
} from '@/lib/types';

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
  const [activeTab, setActiveTab] = useState<'templates' | 'drafts' | 'builds'>('templates');
  const [templates, setTemplates] = useState<Template[]>(propTemplates || []);
  const [images, setImages] = useState<ImageListItem[]>(propImages || []);
  const [jobs, setJobs] = useState<JobRecord[]>([]);
  const [drafts, setDrafts] = useState<ImageDraft[]>(() => getStoredDrafts());
  const [searchQuery, setSearchQuery] = useState('');
  const [loadingJobs, setLoadingJobs] = useState(false);

  // Dialog states
  const [prepareDialogOpen, setPrepareDialogOpen] = useState(false);
  const [prepareDialogKey, setPrepareDialogKey] = useState<string>('new');
  const [prepareInitialValues, setPrepareInitialValues] = useState<Partial<ImageDraft> | null>(null);

  const [launchModalTpl, setLaunchModalTpl] = useState<Template | null>(null);
  const [launchName, setLaunchName] = useState('');
  const [launchAutoStart, setLaunchAutoStart] = useState(true);

  const [runJobModalTpl, setRunJobModalTpl] = useState<Template | null>(null);

  // Log viewer modal
  const [viewLogJob, setViewLogJob] = useState<JobRecord | null>(null);
  const [jobLogs, setJobLogs] = useState<JobLogRecord[]>([]);
  const [loadingLogs, setLoadingLogs] = useState(false);
  const [logFilter, setLogFilter] = useState('');
  const [autoScrollLogs, setAutoScrollLogs] = useState(true);
  const logTerminalRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (propTemplates) setTemplates(propTemplates);
  }, [propTemplates]);

  useEffect(() => {
    if (propImages) setImages(propImages);
  }, [propImages]);

  // Sync and reconcile drafts whenever jobs or templates change
  const refreshDrafts = useCallback((currentJobs: JobRecord[], currentTemplates: Template[]) => {
    const stored = getStoredDrafts();
    const reconciled = reconcileDraftsWithJobsAndTemplates(stored, currentJobs, currentTemplates);
    setDrafts(reconciled);
  }, []);

  const loadJobs = useCallback(async () => {
    setLoadingJobs(true);
    try {
      const allJobs = await api.listJobs();
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
      refreshDrafts(imageJobs, templates);
    } catch {
      // ignore transient error
    } finally {
      setLoadingJobs(false);
    }
  }, [refreshDrafts, templates]);

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
      setLogFilter('');
      return;
    }
    void loadJobLogs(viewLogJob.id);

    const isRunning = viewLogJob.state === 'queued' || viewLogJob.state === 'running';
    if (!isRunning) return;

    const interval = setInterval(() => {
      void loadJobLogs(viewLogJob.id);
      void api.listJobs().then((all) => {
        const updated = (all || []).find((j) => j.id === viewLogJob.id);
        if (updated) setViewLogJob(updated);
      });
    }, 1500);
    return () => clearInterval(interval);
  }, [viewLogJob, loadJobLogs]);

  // Auto scroll logs
  useEffect(() => {
    if (autoScrollLogs && logTerminalRef.current) {
      logTerminalRef.current.scrollTop = logTerminalRef.current.scrollHeight;
    }
  }, [jobLogs, autoScrollLogs]);

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

  // Handle saving a draft from dialog
  function handleSaveDraft(draftData: Partial<ImageDraft> & { name: string }) {
    const saved = saveStoredDraft(draftData);
    refreshDrafts(jobs, templates);
    return saved;
  }

  // Handle deleting a draft
  function handleDeleteDraft(idOrName: string) {
    deleteStoredDraft(idOrName);
    setDrafts((prev) => prev.filter((d) => d.id !== idOrName && d.name !== idOrName));
    toast.success(`Draft "${idOrName}" deleted`);
  }

  // Open Prepare Image Dialog for a fresh configuration
  function openNewImageDialog() {
    setPrepareInitialValues(null);
    setPrepareDialogKey(`new-${Date.now()}`);
    setPrepareDialogOpen(true);
  }

  // Open Prepare Image Dialog pre-populated with a draft or past job
  function openRebuildDialog(draftOrJob: ImageDraft | JobRecord) {
    let draftValues: Partial<ImageDraft>;
    if ('spec_json' in draftOrJob) {
      const job = draftOrJob as JobRecord;
      const spec = extractJobSpec(job);
      const res = extractJobResult(job);
      draftValues = {
        name: spec.target_template || job.repo_name || '',
        description: spec.target_description || '',
        base_image: spec.image_id || 'ubuntu-24',
        packages: spec.packages || [],
        script: spec.script || (spec.command && spec.command.length > 2 ? spec.command[2] : ''),
        vcpus: spec.vcpus || 2,
        memory_mb: spec.memory_mb || 2048,
        disk_mb: spec.disk_mb || 10240,
        status: job.state === 'failed' ? 'failed' : 'draft',
        last_job_id: job.id,
        last_error: res.error,
        last_exit_code: res.exit_code,
      };
    } else {
      draftValues = draftOrJob as ImageDraft;
    }

    setPrepareInitialValues(draftValues);
    setPrepareDialogKey(`${draftValues.name || 'rebuild'}-${Date.now()}`);
    setPrepareDialogOpen(true);
  }

  // View logs by job ID
  function handleOpenJobLogsById(jobId: string) {
    const found = jobs.find((j) => j.id === jobId);
    if (found) {
      setViewLogJob(found);
    } else {
      void api.listJobs().then((all) => {
        const j = (all || []).find((item) => item.id === jobId);
        if (j) setViewLogJob(j);
      });
    }
  }

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

  // Filter drafts
  const filteredDrafts = useMemo(() => {
    const q = searchQuery.trim().toLowerCase();
    if (!q) return drafts;
    return drafts.filter((d) => {
      return (
        d.name.toLowerCase().includes(q) ||
        (d.description || '').toLowerCase().includes(q) ||
        (d.packages || []).some((p) => p.toLowerCase().includes(q))
      );
    });
  }, [drafts, searchQuery]);

  // Filter build jobs
  const filteredJobs = useMemo(() => {
    const q = searchQuery.trim().toLowerCase();
    if (!q) return jobs;
    return jobs.filter((j) => {
      const spec = extractJobSpec(j);
      const name = spec.target_template || j.repo_name || '';
      return (
        name.toLowerCase().includes(q) ||
        j.id.toLowerCase().includes(q) ||
        j.state.toLowerCase().includes(q)
      );
    });
  }, [jobs, searchQuery]);

  // Filter log lines in terminal
  const filteredLogLines = useMemo(() => {
    if (!logFilter.trim()) return jobLogs;
    const q = logFilter.toLowerCase();
    return jobLogs.filter((l) => (l.data || '').toLowerCase().includes(q));
  }, [jobLogs, logFilter]);

  return (
    <div className="space-y-6">
      {/* ==================================================================== */}
      {/* Header & Primary Actions */}
      {/* ==================================================================== */}
      <div className="flex flex-col gap-4 border-b border-border pb-5 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <div className="flex items-center gap-2.5">
            <Layers className="h-6 w-6 text-primary" />
            <h1 className="text-xl font-bold tracking-tight text-foreground">Images &amp; Templates</h1>
          </div>
          <p className="mt-1 text-[13px] text-muted-foreground leading-relaxed">
            Prepare custom OS images with pre-configured packages, save drafts, run isolated Firecracker builds, and produce reusable microVM templates.
          </p>
        </div>

        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={openNewImageDialog}
            className="flex h-8 items-center gap-1.5 bg-primary hover:bg-primary-hover px-3.5 text-[12px] font-medium text-primary-foreground transition-colors shadow-sm"
          >
            <Plus className="h-4 w-4" />
            <span>Prepare Image</span>
          </button>
        </div>
      </div>

      {/* ==================================================================== */}
      {/* Overview Stat Badges & Tab Navigation */}
      {/* ==================================================================== */}
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex border-b border-border space-x-1 sm:space-x-2">
          <button
            type="button"
            onClick={() => setActiveTab('templates')}
            className={`flex items-center gap-2 border-b-2 px-3.5 py-2 text-[13px] font-medium transition-colors ${
              activeTab === 'templates'
                ? 'border-primary text-foreground'
                : 'border-transparent text-muted-foreground hover:text-foreground'
            }`}
          >
            <Package className="h-4 w-4" />
            <span>Built Image Templates</span>
            <span className="rounded-full bg-muted/60 px-2 py-0.5 text-[11px] font-mono text-muted-foreground">
              {templates.length}
            </span>
          </button>

          <button
            type="button"
            onClick={() => setActiveTab('drafts')}
            className={`flex items-center gap-2 border-b-2 px-3.5 py-2 text-[13px] font-medium transition-colors ${
              activeTab === 'drafts'
                ? 'border-primary text-foreground'
                : 'border-transparent text-muted-foreground hover:text-foreground'
            }`}
          >
            <Bookmark className="h-4 w-4" />
            <span>Configurations &amp; Drafts</span>
            <span className="rounded-full bg-muted/60 px-2 py-0.5 text-[11px] font-mono text-muted-foreground">
              {drafts.length}
            </span>
          </button>

          <button
            type="button"
            onClick={() => setActiveTab('builds')}
            className={`flex items-center gap-2 border-b-2 px-3.5 py-2 text-[13px] font-medium transition-colors ${
              activeTab === 'builds'
                ? 'border-primary text-foreground'
                : 'border-transparent text-muted-foreground hover:text-foreground'
            }`}
          >
            <HistoryIcon className="h-4 w-4" />
            <span>Build History &amp; Logs</span>
            {hasActiveBuilds && (
              <span className="h-2 w-2 rounded-full bg-blue-500 animate-pulse" />
            )}
            <span className="rounded-full bg-muted/60 px-2 py-0.5 text-[11px] font-mono text-muted-foreground">
              {jobs.length}
            </span>
          </button>
        </div>

        {/* Search */}
        <div className="relative w-full sm:w-64">
          <Search className="absolute left-2.5 top-2.5 h-3.5 w-3.5 text-muted-foreground" />
          <input
            type="text"
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            placeholder={`Search ${activeTab}…`}
            className="h-8 w-full border border-border bg-background pl-8 pr-3 text-[12px] text-foreground focus:border-primary focus:outline-none"
          />
        </div>
      </div>

      {/* ==================================================================== */}
      {/* TAB 1: Built Image Templates */}
      {/* ==================================================================== */}
      {activeTab === 'templates' && (
        <div className="space-y-4">
          {filteredTemplates.length === 0 ? (
            <div className="border border-dashed border-border p-12 text-center space-y-3">
              <Package className="mx-auto h-8 w-8 text-muted-foreground opacity-50" />
              <div className="text-[14px] font-medium text-foreground">No image templates available</div>
              <p className="text-[12px] text-muted-foreground max-w-md mx-auto">
                Prepare a custom OS image with your required packages and configurations. Building will automatically register a reusable template here.
              </p>
              <button
                type="button"
                onClick={openNewImageDialog}
                className="inline-flex items-center gap-1.5 bg-primary px-3 py-1.5 text-[12px] font-medium text-primary-foreground hover:bg-primary-hover transition-colors"
              >
                <Plus className="h-3.5 w-3.5" />
                <span>Prepare Your First Image</span>
              </button>
            </div>
          ) : (
            <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-3">
              {filteredTemplates.map((tpl) => {
                const matchingImg = images.find((i) => i.name === tpl.name);
                const matchingDraft = drafts.find((d) => d.name === tpl.name);
                const pkgs = matchingImg?.packages || matchingDraft?.packages || [];

                return (
                  <div
                    key={tpl.id}
                    className="flex flex-col justify-between border border-border bg-card p-4 hover:border-foreground/30 transition-colors space-y-4"
                  >
                    <div className="space-y-2">
                      <div className="flex items-start justify-between">
                        <div className="space-y-1">
                          <div className="flex items-center gap-2">
                            <span className="font-mono text-[13px] font-semibold text-foreground">
                              {tpl.name}
                            </span>
                            <span className="inline-flex items-center gap-1 rounded bg-emerald-500/10 px-1.5 py-0.5 text-[10.5px] font-medium text-emerald-400">
                              <CheckCircle2 className="h-3 w-3" />
                              Ready
                            </span>
                          </div>
                          {tpl.description ? (
                            <p className="text-[12px] text-muted-foreground line-clamp-2">
                              {tpl.description}
                            </p>
                          ) : (
                            <p className="text-[12px] text-muted-foreground/60 italic">
                              Reusable template disk image
                            </p>
                          )}
                        </div>
                      </div>

                      {/* Specs */}
                      <div className="flex flex-wrap items-center gap-3 pt-1 text-[11px] text-muted-foreground font-mono">
                        <span className="flex items-center gap-1">
                          <Cpu className="h-3 w-3 text-primary" /> {tpl.vcpus || 2} vCPU
                        </span>
                        <span className="flex items-center gap-1">
                          <Database className="h-3 w-3 text-primary" /> {formatMemory(tpl.memory_mb || 2048)}
                        </span>
                        <span className="flex items-center gap-1">
                          <HardDrive className="h-3 w-3 text-primary" />{' '}
                          {Math.round((tpl.disk_mb || tpl.disk_size_mb || 10240) / 1024)} GB Disk
                        </span>
                      </div>

                      {/* Packages preview */}
                      {pkgs.length > 0 && (
                        <div className="space-y-1 pt-1">
                          <div className="text-[10.5px] text-muted-foreground uppercase font-mono tracking-wider">
                            Pre-installed ({pkgs.length})
                          </div>
                          <div className="flex flex-wrap gap-1">
                            {pkgs.slice(0, 5).map((pkg) => (
                              <span
                                key={pkg}
                                className="border border-border bg-muted/30 px-1.5 py-0.5 font-mono text-[10px] text-foreground"
                              >
                                {pkg}
                              </span>
                            ))}
                            {pkgs.length > 5 && (
                              <span className="border border-border bg-muted/10 px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground">
                                +{pkgs.length - 5} more
                              </span>
                            )}
                          </div>
                        </div>
                      )}
                    </div>

                    {/* Actions */}
                    <div className="border-t border-border pt-3 flex items-center justify-between gap-2">
                      <div className="flex items-center gap-1.5">
                        <button
                          type="button"
                          onClick={() => openLaunchModal(tpl)}
                          className="flex h-7 items-center gap-1 bg-primary hover:bg-primary-hover px-2.5 text-[11.5px] font-medium text-primary-foreground transition-colors"
                        >
                          <Play className="h-3 w-3 fill-current" />
                          <span>Launch Sandbox</span>
                        </button>
                        <button
                          type="button"
                          onClick={() => setRunJobModalTpl(tpl)}
                          className="flex h-7 items-center gap-1 border border-border px-2 text-[11.5px] font-medium text-foreground hover:bg-muted/40 transition-colors"
                        >
                          <Zap className="h-3 w-3 text-primary" />
                          <span>Run Job</span>
                        </button>
                      </div>

                      <div className="flex items-center gap-1">
                        <button
                          type="button"
                          title="Rebuild image with updated packages or script"
                          onClick={() => {
                            if (matchingDraft) {
                              openRebuildDialog(matchingDraft);
                            } else {
                              openRebuildDialog({
                                id: tpl.name,
                                name: tpl.name,
                                description: tpl.description,
                                base_image: matchingImg?.base_image || 'ubuntu-24',
                                packages: pkgs,
                                script: '',
                                vcpus: tpl.vcpus,
                                memory_mb: tpl.memory_mb,
                                disk_mb: tpl.disk_mb || 10240,
                                status: 'succeeded',
                                created_at: tpl.created_at,
                                updated_at: tpl.updated_at,
                              });
                            }
                          }}
                          className="flex h-7 w-7 items-center justify-center border border-border text-muted-foreground hover:text-foreground hover:bg-muted/40 transition-colors"
                        >
                          <RotateCw className="h-3.5 w-3.5" />
                        </button>
                        <button
                          type="button"
                          title="Delete template"
                          onClick={() => {
                            if (!confirm(`Are you sure you want to delete template "${tpl.name}"?`)) return;
                            void api
                              .deleteTemplate(tpl.id)
                              .then(() => {
                                toast.success(`Template ${tpl.name} deleted`);
                                onRefreshCluster?.();
                              })
                              .catch((err: unknown) => toast.error(errorMessage(err)));
                          }}
                          className="flex h-7 w-7 items-center justify-center border border-border text-muted-foreground hover:text-destructive hover:bg-destructive/10 transition-colors"
                        >
                          <Trash2 className="h-3.5 w-3.5" />
                        </button>
                      </div>
                    </div>
                  </div>
                );
              })}
            </div>
          )}
        </div>
      )}

      {/* ==================================================================== */}
      {/* TAB 2: Configurations & Drafts */}
      {/* ==================================================================== */}
      {activeTab === 'drafts' && (
        <div className="space-y-4">
          {filteredDrafts.length === 0 ? (
            <div className="border border-dashed border-border p-12 text-center space-y-3">
              <Bookmark className="mx-auto h-8 w-8 text-muted-foreground opacity-50" />
              <div className="text-[14px] font-medium text-foreground">No image configurations or drafts</div>
              <p className="text-[12px] text-muted-foreground max-w-md mx-auto">
                Save your software package recipes and build scripts as drafts. If an image build fails, you can re-attempt it anytime without losing your configurations.
              </p>
              <button
                type="button"
                onClick={openNewImageDialog}
                className="inline-flex items-center gap-1.5 bg-primary px-3 py-1.5 text-[12px] font-medium text-primary-foreground hover:bg-primary-hover transition-colors"
              >
                <Plus className="h-3.5 w-3.5" />
                <span>Create Image Draft</span>
              </button>
            </div>
          ) : (
            <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
              {filteredDrafts.map((draft) => {
                const isFailed = draft.status === 'failed';
                const isBuilding = draft.status === 'building';
                const isSucceeded = draft.status === 'succeeded';
                const isDraft = draft.status === 'draft';

                return (
                  <div
                    key={draft.id}
                    className={`flex flex-col justify-between border bg-card p-4 transition-colors space-y-3.5 ${
                      isFailed
                        ? 'border-red-500/40 bg-red-950/10'
                        : isBuilding
                        ? 'border-blue-500/40 bg-blue-950/10'
                        : 'border-border'
                    }`}
                  >
                    <div className="space-y-2.5">
                      {/* Card Header & Status */}
                      <div className="flex items-start justify-between gap-2">
                        <div className="space-y-1">
                          <div className="flex items-center gap-2">
                            <span className="font-mono text-[13.5px] font-semibold text-foreground">
                              {draft.name}
                            </span>
                            {isDraft && (
                              <span className="rounded bg-muted/60 px-2 py-0.5 text-[10.5px] font-medium text-muted-foreground">
                                Draft (Not Built)
                              </span>
                            )}
                            {isBuilding && (
                              <span className="flex items-center gap-1 rounded bg-blue-500/20 px-2 py-0.5 text-[10.5px] font-medium text-blue-300">
                                <Loader2 className="h-3 w-3 animate-spin" />
                                Building…
                              </span>
                            )}
                            {isFailed && (
                              <span className="flex items-center gap-1 rounded bg-red-500/20 px-2 py-0.5 text-[10.5px] font-medium text-red-300">
                                <AlertTriangle className="h-3 w-3" />
                                Build Failed
                              </span>
                            )}
                            {isSucceeded && (
                              <span className="flex items-center gap-1 rounded bg-emerald-500/20 px-2 py-0.5 text-[10.5px] font-medium text-emerald-300">
                                <CheckCircle2 className="h-3 w-3" />
                                Succeeded
                              </span>
                            )}
                          </div>
                          {draft.description && (
                            <p className="text-[12px] text-muted-foreground">{draft.description}</p>
                          )}
                        </div>
                        <span className="text-[11px] text-muted-foreground whitespace-nowrap font-mono">
                          {formatRelativeTime(draft.updated_at || draft.created_at)}
                        </span>
                      </div>

                      {/* Diagnostic Error Banner on Failure */}
                      {isFailed && (
                        <div className="border border-red-500/30 bg-red-950/30 p-2.5 text-[11.5px] text-red-200 flex items-start justify-between gap-2">
                          <div className="space-y-0.5">
                            <div className="font-medium text-red-100 flex items-center gap-1.5">
                              <AlertCircle className="h-3.5 w-3.5 text-red-400" />
                              <span>Last attempt failed</span>
                              {draft.last_exit_code !== undefined && (
                                <span className="font-mono text-[10px] bg-red-900/60 text-red-300 px-1 rounded">
                                  Exit {draft.last_exit_code}
                                </span>
                              )}
                            </div>
                            <div className="font-mono text-[11px] text-red-300/80">
                              {draft.last_error || 'Configuration command exited with a non-zero exit code.'}
                            </div>
                          </div>
                        </div>
                      )}

                      {/* Specs */}
                      <div className="flex flex-wrap items-center gap-3 text-[11px] text-muted-foreground font-mono">
                        <span>Base: {draft.base_image || 'ubuntu-24'}</span>
                        <span>•</span>
                        <span>{draft.vcpus || 2} vCPU</span>
                        <span>•</span>
                        <span>{formatMemory(draft.memory_mb || 2048)}</span>
                        <span>•</span>
                        <span>{Math.round((draft.disk_mb || 10240) / 1024)} GB Disk</span>
                      </div>

                      {/* Packages */}
                      {draft.packages && draft.packages.length > 0 && (
                        <div className="space-y-1">
                          <div className="text-[10px] uppercase font-mono text-muted-foreground tracking-wider">
                            Packages ({draft.packages.length}):
                          </div>
                          <div className="flex flex-wrap gap-1">
                            {draft.packages.map((pkg) => (
                              <span
                                key={pkg}
                                className="border border-border bg-muted/20 px-1.5 py-0.5 font-mono text-[10.5px] text-foreground"
                              >
                                {pkg}
                              </span>
                            ))}
                          </div>
                        </div>
                      )}

                      {/* Script snippet */}
                      {draft.script && draft.script.trim() && (
                        <div className="space-y-1">
                          <div className="text-[10px] uppercase font-mono text-muted-foreground tracking-wider">
                            Custom Script:
                          </div>
                          <pre className="max-h-20 overflow-y-auto border border-border bg-muted/20 p-2 font-mono text-[10.5px] text-muted-foreground whitespace-pre-wrap leading-tight">
                            {draft.script}
                          </pre>
                        </div>
                      )}
                    </div>

                    {/* Card Actions */}
                    <div className="border-t border-border pt-3 flex items-center justify-between gap-2">
                      <div className="flex items-center gap-2">
                        {isFailed ? (
                          <button
                            type="button"
                            onClick={() => openRebuildDialog(draft)}
                            className="flex h-7 items-center gap-1.5 bg-red-600 hover:bg-red-500 px-3 text-[11.5px] font-medium text-white transition-colors"
                          >
                            <RotateCw className="h-3 w-3" />
                            <span>Re-attempt Build</span>
                          </button>
                        ) : isBuilding ? (
                          <button
                            type="button"
                            onClick={() => {
                              if (draft.last_job_id) {
                                handleOpenJobLogsById(draft.last_job_id);
                              } else {
                                setActiveTab('builds');
                              }
                            }}
                            className="flex h-7 items-center gap-1 bg-blue-600 hover:bg-blue-500 px-2.5 text-[11.5px] font-medium text-white transition-colors"
                          >
                            <Terminal className="h-3 w-3" />
                            <span>View Live Logs</span>
                          </button>
                        ) : isSucceeded ? (
                          <button
                            type="button"
                            onClick={() => {
                              const tpl = templates.find((t) => t.name === (draft.template_name || draft.name));
                              if (tpl) {
                                openLaunchModal(tpl);
                              } else {
                                openRebuildDialog(draft);
                              }
                            }}
                            className="flex h-7 items-center gap-1 bg-emerald-600 hover:bg-emerald-500 px-2.5 text-[11.5px] font-medium text-white transition-colors"
                          >
                            <Play className="h-3 w-3 fill-current" />
                            <span>Launch Sandbox</span>
                          </button>
                        ) : (
                          <button
                            type="button"
                            onClick={() => openRebuildDialog(draft)}
                            className="flex h-7 items-center gap-1.5 bg-primary hover:bg-primary-hover px-3 text-[11.5px] font-medium text-primary-foreground transition-colors"
                          >
                            <Plus className="h-3 w-3" />
                            <span>Build Image</span>
                          </button>
                        )}

                        {draft.last_job_id && (
                          <button
                            type="button"
                            onClick={() => handleOpenJobLogsById(draft.last_job_id!)}
                            className="flex h-7 items-center gap-1 border border-border px-2 text-[11.5px] font-medium text-foreground hover:bg-muted/40 transition-colors"
                          >
                            <Terminal className="h-3 w-3 text-muted-foreground" />
                            <span>Logs</span>
                          </button>
                        )}
                      </div>

                      <div className="flex items-center gap-1">
                        <button
                          type="button"
                          title="Edit configuration"
                          onClick={() => openRebuildDialog(draft)}
                          className="flex h-7 px-2 items-center gap-1 border border-border text-[11px] text-muted-foreground hover:text-foreground hover:bg-muted/40 transition-colors"
                        >
                          <span>Edit Config</span>
                        </button>
                        <button
                          type="button"
                          title="Delete draft"
                          onClick={() => handleDeleteDraft(draft.id)}
                          className="flex h-7 w-7 items-center justify-center border border-border text-muted-foreground hover:text-destructive hover:bg-destructive/10 transition-colors"
                        >
                          <Trash2 className="h-3.5 w-3.5" />
                        </button>
                      </div>
                    </div>
                  </div>
                );
              })}
            </div>
          )}
        </div>
      )}

      {/* ==================================================================== */}
      {/* TAB 3: Build History & Logs */}
      {/* ==================================================================== */}
      {activeTab === 'builds' && (
        <div className="space-y-4">
          <div className="border border-border bg-card overflow-hidden">
            <table className="w-full text-left text-[12.5px]">
              <thead className="border-b border-border bg-muted/30 font-medium text-muted-foreground">
                <tr>
                  <th className="px-4 py-2.5">Target Template</th>
                  <th className="px-4 py-2.5">Base Image</th>
                  <th className="px-4 py-2.5">Status</th>
                  <th className="px-4 py-2.5">Duration</th>
                  <th className="px-4 py-2.5">Created</th>
                  <th className="px-4 py-2.5 text-right">Actions</th>
                </tr>
              </thead>
              {filteredJobs.length === 0 ? (
                <tbody>
                  <tr>
                    <td colSpan={6} className="px-4 py-12 text-center text-muted-foreground">
                      No image builds recorded yet. Prepare an image to start building.
                    </td>
                  </tr>
                </tbody>
              ) : (
                <tbody className="divide-y divide-border">
                  {filteredJobs.map((job) => {
                    const spec = extractJobSpec(job);
                    const res = extractJobResult(job);
                    const targetName = spec.target_template || job.repo_name || job.id;
                    const durationMs = res.duration_ms;
                    const isRunning = job.state === 'running' || job.state === 'queued';
                    const isFailed = job.state === 'failed';
                    const isSucceeded = job.state === 'succeeded';

                    return (
                      <tr key={job.id} className="hover:bg-muted/15 transition-colors">
                        <td className="px-4 py-3 font-mono font-medium text-foreground">
                          <div className="flex items-center gap-2">
                            <span>{targetName}</span>
                            <span className="text-[10px] text-muted-foreground/60 font-mono">
                              ({job.id})
                            </span>
                          </div>
                        </td>
                        <td className="px-4 py-3 text-muted-foreground font-mono text-[11.5px]">
                          {spec.image_id || 'ubuntu-24'}
                        </td>
                        <td className="px-4 py-3">
                          <span
                            className={`inline-flex items-center gap-1 rounded px-2 py-0.5 text-[11px] font-medium font-mono ${
                              isSucceeded
                                ? 'bg-emerald-500/10 text-emerald-400'
                                : isFailed
                                ? 'bg-red-500/10 text-red-400'
                                : 'bg-blue-500/10 text-blue-400'
                            }`}
                          >
                            {isRunning && <Loader2 className="h-3 w-3 animate-spin" />}
                            {isSucceeded && <CheckCircle2 className="h-3 w-3" />}
                            {isFailed && <AlertCircle className="h-3 w-3" />}
                            <span>{job.state}</span>
                          </span>
                        </td>
                        <td className="px-4 py-3 font-mono text-[11.5px] text-muted-foreground">
                          {durationMs ? `${Math.round(durationMs / 1000)}s` : isRunning ? 'running…' : '—'}
                        </td>
                        <td className="px-4 py-3 text-muted-foreground font-mono text-[11.5px]">
                          {formatRelativeTime(job.created_at || job.queued_at)}
                        </td>
                        <td className="px-4 py-3 text-right">
                          <div className="flex items-center justify-end gap-1.5">
                            <button
                              type="button"
                              onClick={() => setViewLogJob(job)}
                              className="flex h-7 items-center gap-1 border border-border px-2 text-[11px] font-medium text-foreground hover:bg-muted/40 transition-colors"
                            >
                              <Terminal className="h-3 w-3 text-muted-foreground" />
                              <span>View Logs</span>
                            </button>

                            {isFailed && (
                              <button
                                type="button"
                                onClick={() => openRebuildDialog(job)}
                                className="flex h-7 items-center gap-1 bg-red-600/90 hover:bg-red-600 px-2 text-[11px] font-medium text-white transition-colors"
                              >
                                <RotateCw className="h-3 w-3" />
                                <span>Re-attempt</span>
                              </button>
                            )}

                            {isSucceeded && (
                              <button
                                type="button"
                                onClick={() => openRebuildDialog(job)}
                                className="flex h-7 items-center gap-1 border border-border px-2 text-[11px] font-medium text-foreground hover:bg-muted/40 transition-colors"
                              >
                                <RotateCw className="h-3 w-3 text-muted-foreground" />
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
        key={prepareDialogKey}
        open={prepareDialogOpen}
        onOpenChange={setPrepareDialogOpen}
        templates={templates}
        initialValues={prepareInitialValues}
        onSaveDraft={handleSaveDraft}
        onViewLogs={handleOpenJobLogsById}
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
              <div>
                <h2 id="launch-template-title" className="text-[16px] font-semibold text-foreground">
                  Launch Sandbox from Template
                </h2>
                <p className="mt-1 text-[12px] text-muted-foreground">
                  Spawns a new isolated MicroVM using template disk &ldquo;{launchModalTpl.name}&rdquo;.
                </p>
              </div>
              <button
                type="button"
                onClick={() => setLaunchModalTpl(null)}
                className="text-muted-foreground hover:text-foreground"
              >
                <X className="h-4 w-4" />
              </button>
            </div>

            <form onSubmit={handleLaunchSubmit} className="mt-4 space-y-4 text-[12.5px]">
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
                  id="auto-start"
                  type="checkbox"
                  checked={launchAutoStart}
                  onChange={(e) => setLaunchAutoStart(e.target.checked)}
                  className="h-4 w-4 border-border text-primary focus:ring-0"
                />
                <label htmlFor="auto-start" className="text-[12px] text-foreground cursor-pointer">
                  Auto-start sandbox immediately after cloning
                </label>
              </div>

              <div className="flex items-center justify-end gap-2 pt-3 border-t border-border">
                <button
                  type="button"
                  onClick={() => setLaunchModalTpl(null)}
                  className="h-8 border border-border px-3 text-[12px] text-muted-foreground hover:text-foreground"
                >
                  Cancel
                </button>
                <button
                  type="submit"
                  className="flex h-8 items-center gap-1.5 bg-primary px-3.5 text-[12px] font-medium text-primary-foreground hover:bg-primary-hover"
                >
                  <Play className="h-3.5 w-3.5 fill-current" />
                  <span>Launch Sandbox</span>
                </button>
              </div>
            </form>
          </div>
        </div>
      )}

      {/* ==================================================================== */}
      {/* Real-time Build Log Viewer Modal */}
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
            className="w-full max-w-[920px] h-[85vh] flex flex-col border border-[#30363d] bg-[#0d1117] text-[#c9d1d9] shadow-2xl overflow-hidden"
            onClick={(e) => e.stopPropagation()}
          >
            {/* Terminal Header */}
            <div className="flex items-center justify-between border-b border-[#30363d] bg-[#161b22] px-4 py-3">
              <div className="flex items-center gap-2.5">
                <Terminal className="h-4 w-4 text-primary" />
                <h3 id="build-log-title" className="font-mono text-[13px] font-semibold text-white">
                  Build Logs:{' '}
                  <span className="text-primary">
                    {extractJobSpec(viewLogJob).target_template || viewLogJob.repo_name || viewLogJob.id}
                  </span>
                </h3>
                <span
                  className={`inline-flex items-center gap-1 rounded px-2 py-0.5 text-[10.5px] font-mono font-medium ${
                    viewLogJob.state === 'succeeded'
                      ? 'bg-emerald-500/20 text-emerald-400'
                      : viewLogJob.state === 'failed'
                      ? 'bg-red-500/20 text-red-400'
                      : 'bg-blue-500/20 text-blue-400'
                  }`}
                >
                  {(viewLogJob.state === 'running' || viewLogJob.state === 'queued') && (
                    <Loader2 className="h-3 w-3 animate-spin" />
                  )}
                  {viewLogJob.state}
                </span>
              </div>

              <div className="flex items-center gap-2">
                {/* Search filter in logs */}
                <div className="relative">
                  <Filter className="absolute left-2 top-2 h-3 w-3 text-[#8b949e]" />
                  <input
                    type="text"
                    value={logFilter}
                    onChange={(e) => setLogFilter(e.target.value)}
                    placeholder="Filter logs…"
                    className="h-7 w-36 sm:w-48 rounded border border-[#30363d] bg-[#0d1117] pl-6 pr-2 font-mono text-[11px] text-[#c9d1d9] focus:border-primary focus:outline-none"
                  />
                  {logFilter && (
                    <button
                      type="button"
                      onClick={() => setLogFilter('')}
                      className="absolute right-1.5 top-1.5 text-[#8b949e] hover:text-white"
                    >
                      <X className="h-3 w-3" />
                    </button>
                  )}
                </div>

                <button
                  type="button"
                  title="Copy logs"
                  onClick={() => {
                    const text = jobLogs.map((l) => l.data).join('\n');
                    void navigator.clipboard.writeText(text);
                    toast.success('Logs copied to clipboard');
                  }}
                  className="flex h-7 items-center gap-1 border border-[#30363d] bg-[#21262d] px-2 text-[11px] text-[#c9d1d9] hover:bg-[#30363d] transition-colors"
                >
                  <Copy className="h-3 w-3" />
                  <span className="hidden sm:inline">Copy</span>
                </button>

                <button
                  type="button"
                  title="Download logs"
                  onClick={() => {
                    const text = jobLogs.map((l) => `[${l.stream}] ${l.data}`).join('\n');
                    const blob = new Blob([text], { type: 'text/plain' });
                    const url = URL.createObjectURL(blob);
                    const a = document.createElement('a');
                    a.href = url;
                    a.download = `build-${viewLogJob.id}.log`;
                    a.click();
                    URL.revokeObjectURL(url);
                  }}
                  className="flex h-7 items-center gap-1 border border-[#30363d] bg-[#21262d] px-2 text-[11px] text-[#c9d1d9] hover:bg-[#30363d] transition-colors"
                >
                  <Download className="h-3 w-3" />
                  <span className="hidden sm:inline">Download</span>
                </button>

                <button
                  type="button"
                  aria-label="Close build logs"
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
                    <div className="text-[12.5px] font-medium text-red-200 flex items-center gap-2">
                      <span>Image build failed</span>
                      {extractJobResult(viewLogJob).exit_code !== undefined && (
                        <span className="font-mono text-[10.5px] bg-red-900/60 text-red-300 px-1.5 py-0.2 rounded">
                          Exit code {extractJobResult(viewLogJob).exit_code}
                        </span>
                      )}
                    </div>
                    <div className="text-[11.5px] text-red-300/80 font-mono">
                      {extractJobResult(viewLogJob).error || 'Command exited with a non-zero exit code.'}
                    </div>
                  </div>
                </div>
                <button
                  type="button"
                  onClick={() => {
                    const job = viewLogJob;
                    setViewLogJob(null);
                    openRebuildDialog(job);
                  }}
                  className="flex items-center gap-1.5 shrink-0 bg-red-600 hover:bg-red-500 px-3 py-1.5 text-[11.5px] font-medium text-white transition-colors"
                >
                  <RotateCw className="h-3.5 w-3.5" />
                  <span>Re-attempt / Fix Config</span>
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
              ) : filteredLogLines.length === 0 ? (
                <div className="text-[#8b949e] italic py-8 text-center">
                  No log lines match filter &ldquo;{logFilter}&rdquo;
                </div>
              ) : (
                filteredLogLines.map((log) => {
                  const isErr = log.stream === 'stderr';
                  const isStep = log.data.startsWith('==>');
                  const isCompute = log.data.startsWith('[compute]');

                  return (
                    <div
                      key={log.seq}
                      className={`break-all whitespace-pre-wrap ${
                        isErr
                          ? 'text-[#ff7b72] font-semibold'
                          : isStep
                          ? 'text-[#79c0ff] font-semibold'
                          : isCompute
                          ? 'text-[#d2a8ff]'
                          : 'text-[#c9d1d9]'
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
                <span>
                  Lines: {jobLogs.length}
                  {logFilter && ` (${filteredLogLines.length} filtered)`}
                </span>
                <span>•</span>
                <span>State: {viewLogJob.state}</span>
              </div>
              <div className="flex items-center gap-3">
                <button
                  type="button"
                  onClick={() => setAutoScrollLogs(!autoScrollLogs)}
                  className={`text-[10.5px] px-1.5 py-0.5 rounded border border-[#30363d] ${
                    autoScrollLogs ? 'bg-primary/20 text-primary border-primary/30' : 'text-[#8b949e]'
                  }`}
                >
                  Auto-scroll: {autoScrollLogs ? 'ON' : 'OFF'}
                </button>
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
        </div>
      )}
    </div>
  );
}
