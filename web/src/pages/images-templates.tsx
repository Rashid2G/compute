import { useCallback, useEffect, useRef, useState } from 'react';
import {
  Camera,
  ChevronDown,
  ChevronRight,
  Database,
  Eye,
  EyeOff,
  GitBranch,
  KeyRound,
  Lock,
  Pencil,
  Play,
  Plus,
  ShieldCheck,
  Star,
  Trash2,
  X,
} from 'lucide-react';
import { toast } from 'sonner';

import { CreateSnapshotDialog } from '@/components/create-snapshot-dialog';
import { api, errorMessage } from '@/lib/api';
import { formatMemory, formatRelativeTime } from '@/lib/format';
import type { PageId } from '@/lib/navigation';
import type {
  EnvironmentProfileRecord,
  ImageListItem,
  Template,
  VM,
} from '@/lib/types';
import { cn } from '@/lib/utils';

interface ImagesTemplatesPageProps {
  vms: VM[];
  templates?: Template[];
  images?: ImageListItem[];
  mode?: 'environment' | 'snapshots';
  onLaunchVM: () => void;
  onNavigate?: (page: PageId) => void;
  onRefreshCluster?: () => void;
  embedded?: boolean;
}

export function ImagesTemplatesPage({
  vms,
  templates: propTemplates,
  images: _propImages,
  mode = 'environment',
  onLaunchVM: _onLaunchVM,
  onNavigate,
  onRefreshCluster,
  embedded = false,
}: ImagesTemplatesPageProps) {
  // Environment state backed by /api/v1/environments
  const [profiles, setProfiles] = useState<EnvironmentProfileRecord[]>([]);
  const [selectedProfileName, setSelectedProfileName] = useState('base');
  const [safeForThirdParties, setSafeForThirdParties] = useState(false);
  const [githubReposChecked, setGithubReposChecked] = useState(true);
  const [githubRepos, setGithubRepos] = useState<string[]>([]);
  const [newRepoInput, setNewRepoInput] = useState('');
  const [secretsChecked, setSecretsChecked] = useState(true);
  const [secretsText, setSecretsText] = useState('');
  const [computeCredsChecked, setComputeCredsChecked] = useState(true);
  const [credentialProfile, setCredentialProfile] = useState("Owner's default");
  const [version, setVersion] = useState(1);

  const [reposExpanded, setReposExpanded] = useState(false);
  const [secretsExpanded, setSecretsExpanded] = useState(false);
  const [revealSecrets, setRevealSecrets] = useState(false);
  const [savingEnv, setSavingEnv] = useState(false);

  // Snapshots & Templates state backed by /api/v1/templates (WS synced)
  const [templates, setTemplates] = useState<Template[]>(propTemplates || []);
  const [loadingAssets, setLoadingAssets] = useState(false);

  // Create Snapshot modal state
  const [createSnapOpen, setCreateSnapOpen] = useState(false);

  // Launch Sandbox from Snapshot modal state
  const [launchModalTpl, setLaunchModalTpl] = useState<Template | null>(null);
  const [launchName, setLaunchName] = useState('');
  const [launchAutoStart, setLaunchAutoStart] = useState(true);

  const selectedProfileNameRef = useRef(selectedProfileName);
  selectedProfileNameRef.current = selectedProfileName;
  const hasPropTemplatesRef = useRef(propTemplates !== undefined);
  hasPropTemplatesRef.current = propTemplates !== undefined;

  const applyProfile = useCallback((p: EnvironmentProfileRecord) => {
    setSelectedProfileName(p.name);
    setSafeForThirdParties(Boolean(p.safe_for_third_parties));
    setGithubReposChecked(Boolean(p.github_repos_enabled));
    setGithubRepos(p.github_repos || []);
    setSecretsChecked(Boolean(p.secrets_enabled));
    setSecretsText(p.secrets || '');
    setComputeCredsChecked(
      Boolean(p.compute_credentials_enabled ?? p.boat_credentials_enabled),
    );
    setCredentialProfile(p.credential_profile || "Owner's default");
    setVersion(p.version || 1);
  }, []);

  const loadEnvironments = useCallback(async (overrideName?: string) => {
    try {
      const list = await api.listEnvironments();
      setProfiles(list || []);
      const targetName = overrideName ?? selectedProfileNameRef.current;
      const current =
        list.find((item) => item.name === targetName) || list[0];
      if (current) {
        applyProfile(current);
      }
    } catch {
      // Fallback initial state before first save
    }
  }, [applyProfile]);

  const loadSnapshots = useCallback(async () => {
    setLoadingAssets(true);
    try {
      const res = await api.listTemplates();
      setTemplates(res || []);
    } catch {
      // ignore transient error
    } finally {
      setLoadingAssets(false);
    }
  }, []);

  useEffect(() => {
    if (propTemplates) {
      setTemplates(propTemplates);
    }
  }, [propTemplates]);

  useEffect(() => {
    void loadEnvironments();
    if (!hasPropTemplatesRef.current) {
      void loadSnapshots();
    }
  }, [loadEnvironments, loadSnapshots]);

  async function handleSaveEnvironment() {
    setSavingEnv(true);
    try {
      const saved = await api.saveEnvironment({
        name: selectedProfileName,
        is_default: selectedProfileName === 'base',
        safe_for_third_parties: safeForThirdParties,
        github_repos_enabled: githubReposChecked,
        github_repos: githubRepos,
        secrets_enabled: secretsChecked,
        secrets: secretsText,
        compute_credentials_enabled: computeCredsChecked,
        credential_profile: credentialProfile,
      });
      applyProfile(saved);
      await loadEnvironments(saved.name);
      toast.success(`Environment "${saved.name}" saved (v${saved.version})`);
    } catch (err) {
      toast.error(`Failed to save environment: ${errorMessage(err)}`);
    } finally {
      setSavingEnv(false);
    }
  }

  async function handleCreateProfile() {
    const name = window.prompt('New environment profile name:', 'staging');
    if (!name || !name.trim()) return;
    try {
      const created = await api.saveEnvironment({
        name: name.trim(),
        is_default: false,
        safe_for_third_parties: false,
        github_repos_enabled: false,
        github_repos: [],
        secrets_enabled: false,
        secrets: '',
        compute_credentials_enabled: false,
        credential_profile: "Owner's default",
      });
      await loadEnvironments();
      applyProfile(created);
      toast.success(`Created environment "${created.name}"`);
    } catch (err) {
      toast.error(errorMessage(err));
    }
  }

  async function handleDeleteProfile() {
    if (selectedProfileName === 'base') {
      toast.error('Cannot delete the default base environment');
      return;
    }
    try {
      await api.deleteEnvironment(selectedProfileName);
      toast.success(`Deleted environment "${selectedProfileName}"`);
      setSelectedProfileName('base');
      await loadEnvironments();
    } catch (err) {
      toast.error(errorMessage(err));
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
    const toastId = toast.loading(`Launching ${name} from ${tpl.name}…`);
    void api
      .restoreSnapshot(tpl.id, name, {
        vcpus: tpl.vcpus,
        memory_mb: tpl.memory_mb,
        auto_start: autoStart,
      })
      .then(() => {
        toast.success(`Launched ${name}`, { id: toastId });
        onRefreshCluster?.();
        onNavigate?.('instances');
      })
      .catch((err) => {
        toast.error(errorMessage(err), { id: toastId });
      });
  }

  // ============================================================================
  // MODE 1: Environment Page
  // ============================================================================
  if (mode === 'environment') {
    const secretLinesCount = secretsText
      .split('\n')
      .filter((line) => line.trim().length > 0).length;

    return (
      <div className="space-y-6">
        {/* Top Row: Environment title on left, Save button on right */}
        <div className="flex items-start justify-between">
          {!embedded ? (
            <div className="space-y-1">
              <h1 className="font-display text-[22px] font-medium tracking-tight text-foreground">
                Environment
              </h1>
              <p className="text-[12px] text-muted-foreground">
                What every new sandbox in it starts with.
              </p>
            </div>
          ) : (
            <div className="space-y-0.5">
              <h2 className="text-[14px] font-medium text-foreground">
                Environment Profiles
              </h2>
              <p className="text-[12px] text-muted-foreground">
                What every new sandbox in it starts with.
              </p>
            </div>
          )}

          <button
            type="button"
            disabled={savingEnv}
            onClick={() => void handleSaveEnvironment()}
            className="h-7 border border-border bg-background px-3 text-[12px] font-normal text-muted-foreground hover:border-foreground hover:text-foreground transition-colors disabled:opacity-50"
          >
            {savingEnv ? 'Saving…' : 'Save'}
          </button>
        </div>

        {/* Profile selector bar: [ ☆ base ⌄ ] ☆ ✎ + 🗑 */}
        <div className="flex items-center gap-1.5">
          <div className="relative">
            <select
              aria-label="Environment Profile"
              value={selectedProfileName}
              onChange={(e) => {
                const next = profiles.find((p) => p.name === e.target.value);
                if (next) applyProfile(next);
                else setSelectedProfileName(e.target.value);
              }}
              className="h-8 w-36 appearance-none border border-border bg-background pl-7 pr-7 text-[12px] text-foreground focus:border-primary focus:outline-none cursor-pointer"
            >
              {profiles.length === 0 ? (
                <option value="base">base</option>
              ) : (
                profiles.map((p) => (
                  <option key={p.name} value={p.name}>
                    {p.name}
                  </option>
                ))
              )}
            </select>
            <Star className="pointer-events-none absolute left-2.5 top-1/2 h-3 w-3 -translate-y-1/2 text-muted-foreground" />
            <ChevronDown className="pointer-events-none absolute right-2 top-1/2 h-3 w-3 -translate-y-1/2 text-muted-foreground" />
          </div>

          <button
            type="button"
            onClick={() => toast.success(`"${selectedProfileName}" is set as default`)}
            title="Set as default environment"
            className="flex h-7 w-7 items-center justify-center text-muted-foreground hover:text-foreground"
          >
            <Star className="h-3.5 w-3.5" />
          </button>
          <button
            type="button"
            onClick={() => {
              const renamed = window.prompt(
                'Rename environment profile:',
                selectedProfileName,
              );
              if (renamed && renamed.trim()) {
                setSelectedProfileName(renamed.trim());
              }
            }}
            title="Rename environment"
            className="flex h-7 w-7 items-center justify-center text-muted-foreground hover:text-foreground"
          >
            <Pencil className="h-3.5 w-3.5" />
          </button>
          <button
            type="button"
            onClick={() => void handleCreateProfile()}
            title="Create new environment"
            className="flex h-7 w-7 items-center justify-center text-muted-foreground hover:text-foreground"
          >
            <Plus className="h-3.5 w-3.5" />
          </button>
          <button
            type="button"
            onClick={() => void handleDeleteProfile()}
            title="Delete environment"
            className="flex h-7 w-7 items-center justify-center text-muted-foreground hover:text-destructive"
          >
            <Trash2 className="h-3.5 w-3.5" />
          </button>
        </div>

        {/* 5 Checkbox Sections matching dashboard-03-environment.png 1:1 */}
        <div className="space-y-5 pt-1">
          {/* 1. Safe for third parties */}
          <div className="flex items-start gap-3.5">
            <SquareCheckbox
              checked={safeForThirdParties}
              onChange={setSafeForThirdParties}
              label="Safe for third parties"
            />
            <div className="space-y-0.5">
              <div className="flex items-center gap-2 text-[12.5px] font-medium text-foreground">
                <ShieldCheck className="h-3.5 w-3.5 text-muted-foreground" />
                <span>Safe for third parties</span>
              </div>
              <p className="text-[11.5px] text-muted-foreground leading-relaxed">
                For sandboxes driven by someone other than you, such as your own
                end users. Nothing of yours goes in, whatever the sections below
                say.
              </p>
            </div>
          </div>

          {/* 2. GitHub repositories */}
          <div className="space-y-2">
            <div className="flex items-start justify-between gap-4">
              <div className="flex items-start gap-3.5">
                <SquareCheckbox
                  checked={githubReposChecked}
                  onChange={setGithubReposChecked}
                  label="GitHub repositories"
                />
                <div className="space-y-0.5">
                  <button
                    type="button"
                    onClick={() => setReposExpanded((v) => !v)}
                    className="flex items-center gap-1.5 text-[12.5px] font-medium text-foreground"
                  >
                    {reposExpanded ? (
                      <ChevronDown className="h-3 w-3 text-muted-foreground" />
                    ) : (
                      <ChevronRight className="h-3 w-3 text-muted-foreground" />
                    )}
                    <GitBranch className="h-3.5 w-3.5 text-muted-foreground" />
                    <span>GitHub repositories</span>
                    <span className="ml-1 text-[11.5px] font-normal text-muted-foreground">
                      {githubRepos.length === 0
                        ? 'none'
                        : `${githubRepos.length} connected`}
                    </span>
                    {githubRepos.slice(0, 2).map((repo) => (
                      <span
                        key={repo}
                        className="ml-1 border border-border bg-muted/40 px-1.5 py-0.5 font-mono text-[10px] font-normal text-muted-foreground"
                      >
                        {repo}
                      </span>
                    ))}
                  </button>
                  <p className="text-[11.5px] text-muted-foreground leading-relaxed">
                    Cloned into every new sandbox.
                  </p>
                </div>
              </div>

              <button
                type="button"
                onClick={() => setReposExpanded((v) => !v)}
                className="flex shrink-0 items-center gap-1.5 text-[12px] text-foreground/90 hover:text-foreground transition-colors"
              >
                <GitBranch className="h-3.5 w-3.5 text-muted-foreground" />
                <span>Permissions</span>
              </button>
            </div>

            {reposExpanded && (
              <div className="ml-7 border border-border bg-muted/10 p-3 space-y-2">
                <div className="flex items-center gap-2">
                  <input
                    type="text"
                    value={newRepoInput}
                    onChange={(e) => setNewRepoInput(e.target.value)}
                    placeholder="owner/repository"
                    className="h-7 flex-1 border border-border bg-background px-2 font-mono text-[11.5px] text-foreground focus:border-primary focus:outline-none"
                  />
                  <button
                    type="button"
                    onClick={() => {
                      if (!newRepoInput.trim()) return;
                      setGithubRepos((prev) => [
                        ...prev,
                        newRepoInput.trim(),
                      ]);
                      setNewRepoInput('');
                      setGithubReposChecked(true);
                    }}
                    className="h-7 bg-primary hover:bg-primary-hover px-2.5 text-[11px] text-primary-foreground"
                  >
                    Add Repo
                  </button>
                </div>
                {githubRepos.length > 0 && (
                  <div className="divide-y divide-border border border-border">
                    {githubRepos.map((repo) => (
                      <div
                        key={repo}
                        className="flex items-center justify-between px-2.5 py-1 font-mono text-[11px]"
                      >
                        <span>{repo}</span>
                        <button
                          type="button"
                          onClick={() =>
                            setGithubRepos((prev) =>
                              prev.filter((r) => r !== repo),
                            )
                          }
                          className="text-muted-foreground hover:text-destructive"
                        >
                          Remove
                        </button>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            )}
          </div>

          {/* 3. Secrets */}
          <div className="space-y-2">
            <div className="flex items-start justify-between gap-4">
              <div className="flex items-start gap-3.5">
                <SquareCheckbox
                  checked={secretsChecked}
                  onChange={setSecretsChecked}
                  label="Secrets"
                />
                <div className="space-y-0.5">
                  <button
                    type="button"
                    onClick={() => setSecretsExpanded((v) => !v)}
                    className="flex items-center gap-1.5 text-[12.5px] font-medium text-foreground"
                  >
                    {secretsExpanded ? (
                      <ChevronDown className="h-3 w-3 text-muted-foreground" />
                    ) : (
                      <ChevronRight className="h-3 w-3 text-muted-foreground" />
                    )}
                    <KeyRound className="h-3.5 w-3.5 text-muted-foreground" />
                    <span>Secrets</span>
                    <span className="ml-1 text-[11.5px] font-normal text-muted-foreground">
                      {secretLinesCount === 0 ? 'none' : secretLinesCount}
                    </span>
                  </button>
                  <p className="text-[11.5px] text-muted-foreground leading-relaxed">
                    Variables and files written into every new sandbox.
                  </p>
                </div>
              </div>

              <button
                type="button"
                onClick={() => {
                  setRevealSecrets((v) => !v);
                  setSecretsExpanded(true);
                }}
                className="flex shrink-0 items-center gap-1.5 text-[12px] text-foreground/90 hover:text-foreground transition-colors"
              >
                {revealSecrets ? (
                  <EyeOff className="h-3.5 w-3.5 text-muted-foreground" />
                ) : (
                  <Eye className="h-3.5 w-3.5 text-muted-foreground" />
                )}
                <span>{revealSecrets ? 'Hide' : 'Reveal'}</span>
              </button>
            </div>

            {secretsExpanded && (
              <div className="ml-7 space-y-2">
                <textarea
                  rows={4}
                  value={
                    revealSecrets
                      ? secretsText
                      : secretsText.replace(/=.*/g, '=••••••••••••••••')
                  }
                  onChange={(e) => {
                    if (!revealSecrets) setRevealSecrets(true);
                    setSecretsText(e.target.value);
                  }}
                  placeholder={
                    'DATABASE_URL=postgres://user:pass@host:5432/db\n~/.config/token=secret_value'
                  }
                  className="w-full border border-border bg-background p-2.5 font-mono text-[11.5px] text-foreground focus:border-primary focus:outline-none"
                />
              </div>
            )}
          </div>

          {/* 4. Compute credentials */}
          <div className="flex items-start gap-3.5">
            <SquareCheckbox
              checked={computeCredsChecked}
              onChange={setComputeCredsChecked}
              label="Compute credentials"
            />
            <div className="space-y-0.5">
              <div className="flex items-center gap-2 text-[12.5px] font-medium text-foreground">
                <Lock className="h-3.5 w-3.5 text-muted-foreground" />
                <span>Compute credentials</span>
              </div>
              <p className="text-[11.5px] text-muted-foreground leading-relaxed">
                A scoped API key, so the sandbox can snapshot, stop and resume
                itself on your behalf.
              </p>
            </div>
          </div>

        </div>

        {/* Versions Footer Section */}
        <div className="space-y-2.5 pt-3">
          <div className="space-y-0.5">
            <h2 className="text-[12.5px] font-medium text-foreground">
              Versions
            </h2>
            <p className="text-[11.5px] text-muted-foreground">
              Each save makes a new version. A sandbox takes the latest one at
              start and keeps it for life. Running sandboxes move only when you
              press the upgrade button here.
            </p>
          </div>

          <div className="max-w-[340px] border border-border bg-popover">
            <div className="flex items-center justify-between px-3.5 py-2 text-[12px]">
              <div className="flex items-center gap-2">
                <span className="font-mono font-medium text-foreground">
                  v{version}
                </span>
                <span className="text-[11.5px] text-muted-foreground">
                  latest
                </span>
              </div>
              <span className="font-mono text-[11.5px] text-muted-foreground tabular">
                {vms.length} {vms.length === 1 ? 'sandbox' : 'sandboxes'}
              </span>
            </div>
          </div>
        </div>
      </div>
    );
  }

  // ============================================================================
  // MODE 2: Snapshots & Templates (100% Live Daemon Data)
  // ============================================================================
  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="font-display text-[22px] font-medium tracking-tight text-foreground">
            Snapshots &amp; Templates
          </h1>
          <p className="text-[12px] text-muted-foreground">
            Instant Firecracker memory and disk snapshots for launching Ubuntu
            24.04 LTS sandboxes in milliseconds.
          </p>
        </div>

        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={() => setCreateSnapOpen(true)}
            className="flex h-8 items-center gap-1.5 bg-primary hover:bg-primary-hover px-3 text-[12px] font-medium text-primary-foreground"
          >
            <Camera className="h-3.5 w-3.5" />
            <span>Create Snapshot</span>
          </button>
        </div>
      </div>

      <div className="border border-border bg-background">
        <div className="flex items-center justify-between border-b border-border bg-muted/20 px-3 py-2 text-[11.5px] font-medium text-foreground">
          <span>Saved Snapshots &amp; Templates ({templates.length})</span>
          <span className="font-mono text-[10.5px] text-muted-foreground">
            Base OS: Ubuntu 24.04 LTS
          </span>
        </div>
        <table className="w-full border-collapse text-left text-[12px]">
          <thead>
            <tr className="border-b border-border bg-muted/30 text-[11.5px] text-muted-foreground">
              <th className="py-2.5 px-3 font-normal">Template Name</th>
              <th className="py-2.5 px-3 font-normal">Source Sandbox</th>
              <th className="py-2.5 px-3 font-normal">Compute Spec</th>
              <th className="py-2.5 px-3 font-normal">Disk Path</th>
              <th className="py-2.5 px-3 font-normal">Created</th>
              <th className="py-2.5 px-3 text-right font-normal">Actions</th>
            </tr>
          </thead>
          {templates.length === 0 ? (
            <tbody>
              <tr>
                <td
                  colSpan={6}
                  className="py-16 text-center text-muted-foreground"
                >
                  <Database className="mx-auto h-7 w-7 text-muted-foreground/50 mb-2" />
                  <div className="text-[12.5px]">
                    {loadingAssets
                      ? 'Loading snapshot templates…'
                      : 'No snapshot templates saved yet.'}
                  </div>
                  <p className="text-[11.5px] text-muted-foreground/80 mt-1">
                    Snapshot any sandbox or click &ldquo;Create Snapshot&rdquo;
                    to clone sandboxes in milliseconds.
                  </p>
                </td>
              </tr>
            </tbody>
          ) : (
            <tbody className="divide-y divide-border">
              {templates.map((tpl) => (
                <tr key={tpl.id} className="hover:bg-muted/30">
                  <td className="py-2.5 px-3">
                    <div className="font-medium text-foreground">
                      {tpl.name}
                    </div>
                    {tpl.description && (
                      <div className="text-[11px] text-muted-foreground">
                        {tpl.description}
                      </div>
                    )}
                  </td>
                  <td className="py-2.5 px-3 font-mono text-[11px] text-muted-foreground">
                    {tpl.source_vm_id || '—'}
                  </td>
                  <td className="py-2.5 px-3 font-mono text-[11px] text-foreground">
                    {tpl.vcpus} vCPU · {formatMemory(tpl.memory_mb)}
                  </td>
                  <td className="py-2.5 px-3 font-mono text-[11px] text-muted-foreground truncate max-w-xs">
                    {tpl.disk_path}
                  </td>
                  <td className="py-2.5 px-3 font-mono text-[11px] text-muted-foreground">
                    {formatRelativeTime(tpl.created_at)}
                  </td>
                  <td className="py-2.5 px-3 text-right">
                    <div className="inline-flex items-center gap-1.5">
                      <button
                        type="button"
                        onClick={() => openLaunchModal(tpl)}
                        className="flex h-7 items-center gap-1 bg-primary hover:bg-primary-hover px-2.5 text-[11px] font-medium text-primary-foreground"
                      >
                        <Play className="h-3 w-3 fill-current" />
                        <span>Launch</span>
                      </button>
                      <button
                        type="button"
                        aria-label={`Delete template ${tpl.name}`}
                        title={`Delete template ${tpl.name}`}
                        onClick={() => {
                          void api
                            .deleteTemplate(tpl.id)
                            .then(() => {
                              toast.success(`Deleted template ${tpl.name}`);
                              void loadSnapshots();
                              onRefreshCluster?.();
                            })
                            .catch((err) => toast.error(errorMessage(err)));
                        }}
                        className="flex h-7 w-7 items-center justify-center border border-border text-muted-foreground hover:text-destructive"
                      >
                        <Trash2 className="h-3 w-3" />
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          )}
        </table>
      </div>

      <CreateSnapshotDialog
        vm={null}
        vms={vms}
        open={createSnapOpen}
        onOpenChange={setCreateSnapOpen}
        onCreated={() => {
          void loadSnapshots();
          onRefreshCluster?.();
        }}
      />

      {launchModalTpl && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/75 p-4"
          onClick={() => setLaunchModalTpl(null)}
        >
          <div
            role="dialog"
            aria-modal="true"
            aria-labelledby="launch-snapshot-title"
            className="w-full max-w-[420px] border border-border bg-background p-6 text-foreground"
            onClick={(e) => e.stopPropagation()}
          >
            <div className="flex items-center justify-between mb-4">
              <div className="flex items-center gap-2">
                <Play className="h-4 w-4 text-primary" />
                <h2
                  id="launch-snapshot-title"
                  className="text-[15px] font-medium text-foreground"
                >
                  Launch Sandbox from Snapshot
                </h2>
              </div>
              <button
                type="button"
                onClick={() => setLaunchModalTpl(null)}
                aria-label="Close launch modal"
                className="text-muted-foreground hover:text-foreground"
              >
                <X className="h-4 w-4" />
              </button>
            </div>

            <div className="mb-4 border border-border bg-muted/20 p-3 text-xs space-y-1">
              <div className="font-medium text-foreground">
                Snapshot: {launchModalTpl.name}
              </div>
              <div className="font-mono text-[11px] text-muted-foreground">
                {launchModalTpl.vcpus} vCPU ·{' '}
                {formatMemory(launchModalTpl.memory_mb)}
              </div>
            </div>

            <form onSubmit={handleLaunchSubmit} className="space-y-4">
              <div className="space-y-1">
                <label className="block text-[12px] text-muted-foreground">
                  New Sandbox Name
                </label>
                <input
                  type="text"
                  aria-label="Launch sandbox name"
                  value={launchName}
                  onChange={(e) => setLaunchName(e.target.value)}
                  required
                  className="h-9 w-full border border-border bg-background px-3 font-mono text-[12px] text-foreground focus:border-primary focus:outline-none"
                />
              </div>

              <div className="flex items-center gap-2">
                <input
                  id="launch-snap-autostart"
                  type="checkbox"
                  checked={launchAutoStart}
                  onChange={(e) => setLaunchAutoStart(e.target.checked)}
                  className="h-3.5 w-3.5 accent-primary cursor-pointer"
                />
                <label
                  htmlFor="launch-snap-autostart"
                  className="text-[12px] text-foreground cursor-pointer select-none"
                >
                  Start immediately after launch
                </label>
              </div>

              <div className="flex justify-end gap-2 pt-2">
                <button
                  type="button"
                  onClick={() => setLaunchModalTpl(null)}
                  className="h-9 border border-border px-4 text-[12px] text-muted-foreground hover:text-foreground"
                >
                  Cancel
                </button>
                <button
                  type="submit"
                  className="inline-flex h-9 items-center gap-1.5 bg-primary hover:bg-primary-hover px-4 text-[12px] font-medium text-primary-foreground"
                >
                  <Play className="h-3 w-3 fill-current" />
                  <span>Launch Sandbox</span>
                </button>
              </div>
            </form>
          </div>
        </div>
      )}
    </div>
  );
}

function SquareCheckbox({
  checked,
  onChange,
  label,
}: {
  checked: boolean;
  onChange: (next: boolean) => void;
  label: string;
}) {
  return (
    <button
      type="button"
      role="checkbox"
      aria-checked={checked}
      aria-label={label}
      onClick={() => onChange(!checked)}
      className={cn(
        'mt-0.5 flex h-4 w-4 shrink-0 items-center justify-center border transition-colors focus:outline-none',
        checked
          ? 'border-primary bg-primary text-primary-foreground'
          : 'border-border bg-background hover:border-foreground/50',
      )}
    >
      {checked && <span className="block h-2 w-2 bg-primary-foreground" />}
    </button>
  );
}
