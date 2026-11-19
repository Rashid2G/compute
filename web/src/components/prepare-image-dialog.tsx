import { useState } from 'react';
import {
  ChevronDown,
  ChevronUp,
  Cpu,
  Database,
  HardDrive,
  Layers,
  Plus,
  Terminal,
  X,
} from 'lucide-react';
import { toast } from 'sonner';

import { api, errorMessage } from '@/lib/api';
import type { JobRecord, Template } from '@/lib/types';

interface PrepareImageDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  templates?: Template[];
  initialValues?: {
    name?: string;
    description?: string;
    base_image?: string;
    packages?: string[];
    script?: string;
    vcpus?: number;
    memory_mb?: number;
    disk_mb?: number;
  };
  onBuildStarted?: (job: JobRecord) => void;
}

const POPULAR_PACKAGES = [
  { id: 'curl', label: 'curl', category: 'Common' },
  { id: 'git', label: 'git', category: 'Common' },
  { id: 'build-essential', label: 'build-essential', category: 'Common' },
  { id: 'python3-pip', label: 'python3-pip', category: 'Python' },
  { id: 'python3-venv', label: 'python3-venv', category: 'Python' },
  { id: 'nodejs', label: 'nodejs', category: 'Node' },
  { id: 'npm', label: 'npm', category: 'Node' },
  { id: 'docker.io', label: 'docker.io', category: 'Containers' },
  { id: 'rustc', label: 'rustc', category: 'Rust' },
  { id: 'golang', label: 'golang', category: 'Go' },
  { id: 'htop', label: 'htop', category: 'Tools' },
  { id: 'tmux', label: 'tmux', category: 'Tools' },
  { id: 'zsh', label: 'zsh', category: 'Tools' },
  { id: 'sqlite3', label: 'sqlite3', category: 'Database' },
  { id: 'postgresql-client', label: 'postgresql-client', category: 'Database' },
];

export function PrepareImageDialog({
  open,
  onOpenChange,
  templates = [],
  initialValues,
  onBuildStarted,
}: PrepareImageDialogProps) {
  const [name, setName] = useState(initialValues?.name || '');
  const [description, setDescription] = useState(initialValues?.description || '');
  const [baseImage, setBaseImage] = useState(initialValues?.base_image || 'ubuntu-24');
  const [packages, setPackages] = useState<string[]>(initialValues?.packages || ['curl', 'git']);
  const [newPackageInput, setNewPackageInput] = useState('');
  const [script, setScript] = useState(
    initialValues?.script ||
      '# Custom configuration commands executed inside the guest\n# Example: install global tools or customize the environment\n',
  );
  const [vcpus, setVcpus] = useState(initialValues?.vcpus || 2);
  const [memoryMB, setMemoryMB] = useState(initialValues?.memory_mb || 2048);
  const [diskMB, setDiskMB] = useState(initialValues?.disk_mb || 10240);
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [submitting, setSubmitting] = useState(false);

  if (!open) return null;

  function togglePackage(pkg: string) {
    if (packages.includes(pkg)) {
      setPackages(packages.filter((p) => p !== pkg));
    } else {
      setPackages([...packages, pkg]);
    }
  }

  function addCustomPackage() {
    const trimmed = newPackageInput.trim();
    if (!trimmed) return;
    const parts = trimmed.split(/[\s,]+/).filter(Boolean);
    const added = parts.filter((p) => !packages.includes(p));
    if (added.length > 0) {
      setPackages([...packages, ...added]);
    }
    setNewPackageInput('');
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    const cleanName = name.trim().toLowerCase();
    if (!cleanName) {
      toast.error('Image & Template name is required');
      return;
    }

    // Validate characters: alphanumeric, dashes, dots, underscores
    if (!/^[a-z0-9_.-]+$/.test(cleanName)) {
      toast.error('Name must contain only lowercase letters, numbers, dashes, underscores, and dots');
      return;
    }

    setSubmitting(true);
    const toastId = toast.loading(`Starting build for image "${cleanName}"…`);
    onOpenChange(false);

    try {
      const job = await api.prepareImage({
        name: cleanName,
        description: description.trim(),
        base_image: baseImage,
        packages,
        script: script.trim(),
        vcpus,
        memory_mb: memoryMB,
        disk_mb: diskMB,
      });
      toast.success(`Image build started for "${cleanName}" (Job ${job.id})`, { id: toastId });
      onBuildStarted?.(job);
    } catch (err) {
      toast.error(`Failed to start image build: ${errorMessage(err)}`, { id: toastId });
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/75 p-4 overflow-y-auto"
      onClick={() => onOpenChange(false)}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="prepare-image-title"
        className="w-full max-w-[560px] border border-border bg-background p-6 text-foreground shadow-2xl space-y-5 my-8"
        onClick={(e) => e.stopPropagation()}
      >
        {/* Header */}
        <div className="flex items-start justify-between">
          <div className="space-y-1">
            <div className="flex items-center gap-2">
              <Layers className="h-5 w-5 text-primary" />
              <h2 id="prepare-image-title" className="text-[17px] font-semibold text-foreground">
                Prepare New Image
              </h2>
            </div>
            <p className="text-[12px] text-muted-foreground leading-relaxed">
              Configure packages and setup commands. A dedicated build job executes in an isolated sandbox and saves a reusable template on success.
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
          {/* 1. Name & Description */}
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <div className="space-y-1.5">
              <label className="text-[11.5px] font-medium text-foreground">
                Image / Template Name <span className="text-destructive">*</span>
              </label>
              <input
                id="target-template"
                type="text"
                required
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="e.g. node-dev, python-ml"
                className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px] text-foreground focus:border-primary focus:outline-none"
              />
            </div>
            <div className="space-y-1.5">
              <label className="text-[11.5px] font-medium text-foreground">
                Description <span className="text-[10.5px] text-muted-foreground">(optional)</span>
              </label>
              <input
                id="target-description"
                type="text"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                placeholder="e.g. Node.js 22 LTS with pnpm and git"
                className="h-8 w-full border border-border bg-background px-2.5 text-[12px] text-foreground focus:border-primary focus:outline-none"
              />
            </div>
          </div>

          {/* 2. Base Image */}
          <div className="space-y-1.5">
            <label className="text-[11.5px] font-medium text-foreground">
              Base Image / Template
            </label>
            <select
              value={baseImage}
              onChange={(e) => setBaseImage(e.target.value)}
              className="h-8 w-full border border-border bg-background px-2.5 text-[12px] text-foreground focus:border-primary focus:outline-none cursor-pointer"
            >
              <option value="ubuntu-24">Ubuntu 24.04 LTS (Default MicroVM Base)</option>
              {templates.map((tpl) => (
                <option key={tpl.id} value={tpl.name}>
                  Template: {tpl.name} {tpl.description ? `(${tpl.description})` : ''}
                </option>
              ))}
            </select>
          </div>

          {/* 3. Packages */}
          <div className="space-y-2">
            <label className="text-[11.5px] font-medium text-foreground">
              Pre-configured Packages
            </label>
            <div className="flex flex-wrap gap-1.5">
              {POPULAR_PACKAGES.map((pkg) => {
                const active = packages.includes(pkg.id);
                return (
                  <button
                    key={pkg.id}
                    type="button"
                    onClick={() => togglePackage(pkg.id)}
                    className={`h-6 rounded px-2 text-[11px] font-mono transition-colors ${
                      active
                        ? 'bg-primary text-primary-foreground font-medium'
                        : 'border border-border bg-muted/30 text-muted-foreground hover:border-foreground/40 hover:text-foreground'
                    }`}
                  >
                    {active ? '✓ ' : '+ '}
                    {pkg.label}
                  </button>
                );
              })}
            </div>

            <div className="flex items-center gap-2 pt-1">
              <input
                type="text"
                value={newPackageInput}
                onChange={(e) => setNewPackageInput(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    e.preventDefault();
                    addCustomPackage();
                  }
                }}
                placeholder="Add other apt packages (space or comma separated)"
                className="h-7 flex-1 border border-border bg-background px-2.5 font-mono text-[11.5px] text-foreground focus:border-primary focus:outline-none"
              />
              <button
                type="button"
                onClick={addCustomPackage}
                className="h-7 border border-border bg-muted/40 px-2.5 text-[11.5px] font-medium text-foreground hover:bg-muted"
              >
                Add
              </button>
            </div>

            {packages.length > 0 && (
              <div className="flex flex-wrap items-center gap-1.5 pt-1">
                <span className="text-[10.5px] text-muted-foreground">Selected:</span>
                {packages.map((pkg) => (
                  <span
                    key={pkg}
                    className="inline-flex items-center gap-1 border border-border bg-muted/20 px-1.5 py-0.5 font-mono text-[10.5px] text-foreground"
                  >
                    <span>{pkg}</span>
                    <button
                      type="button"
                      onClick={() => togglePackage(pkg)}
                      className="text-muted-foreground hover:text-destructive"
                    >
                      <X className="h-3 w-3" />
                    </button>
                  </span>
                ))}
              </div>
            )}
          </div>

          {/* 4. Configuration Script */}
          <div className="space-y-1.5">
            <div className="flex items-center justify-between">
              <label className="text-[11.5px] font-medium text-foreground flex items-center gap-1.5">
                <Terminal className="h-3.5 w-3.5 text-muted-foreground" />
                <span>Configuration Script / Commands</span>
              </label>
              <span className="text-[10.5px] text-muted-foreground">Runs as root / sudo</span>
            </div>
            <textarea
              id="build-script"
              rows={4}
              value={script}
              onChange={(e) => setScript(e.target.value)}
              placeholder="npm install -g pnpm yarn\npip install fastapi uvicorn"
              className="w-full border border-border bg-background p-2.5 font-mono text-[11px] text-foreground focus:border-primary focus:outline-none resize-y leading-relaxed"
            />
          </div>

          {/* 5. Advanced hardware resources toggle */}
          <div>
            <button
              type="button"
              onClick={() => setShowAdvanced(!showAdvanced)}
              className="flex items-center gap-1 text-[11.5px] text-muted-foreground hover:text-foreground transition-colors"
            >
              {showAdvanced ? <ChevronUp className="h-3.5 w-3.5" /> : <ChevronDown className="h-3.5 w-3.5" />}
              <span>Hardware Specs for Build Sandbox</span>
            </button>
            {showAdvanced && (
              <div className="mt-2.5 grid grid-cols-3 gap-3 border border-border bg-muted/10 p-3">
                <div className="space-y-1">
                  <label className="text-[10.5px] text-muted-foreground flex items-center gap-1">
                    <Cpu className="h-3 w-3" /> vCPUs
                  </label>
                  <select
                    value={vcpus}
                    onChange={(e) => setVcpus(Number(e.target.value))}
                    className="h-7 w-full border border-border bg-background px-2 text-[11.5px] text-foreground focus:border-primary focus:outline-none"
                  >
                    <option value={1}>1 vCPU</option>
                    <option value={2}>2 vCPUs</option>
                    <option value={4}>4 vCPUs</option>
                  </select>
                </div>
                <div className="space-y-1">
                  <label className="text-[10.5px] text-muted-foreground flex items-center gap-1">
                    <Database className="h-3 w-3" /> RAM (MB)
                  </label>
                  <select
                    value={memoryMB}
                    onChange={(e) => setMemoryMB(Number(e.target.value))}
                    className="h-7 w-full border border-border bg-background px-2 text-[11.5px] text-foreground focus:border-primary focus:outline-none"
                  >
                    <option value={1024}>1024 MB (1 GB)</option>
                    <option value={2048}>2048 MB (2 GB)</option>
                    <option value={4096}>4096 MB (4 GB)</option>
                    <option value={8192}>8192 MB (8 GB)</option>
                  </select>
                </div>
                <div className="space-y-1">
                  <label className="text-[10.5px] text-muted-foreground flex items-center gap-1">
                    <HardDrive className="h-3 w-3" /> Disk (MB)
                  </label>
                  <select
                    value={diskMB}
                    onChange={(e) => setDiskMB(Number(e.target.value))}
                    className="h-7 w-full border border-border bg-background px-2 text-[11.5px] text-foreground focus:border-primary focus:outline-none"
                  >
                    <option value={5120}>5120 MB (5 GB)</option>
                    <option value={10240}>10240 MB (10 GB)</option>
                    <option value={20480}>20480 MB (20 GB)</option>
                    <option value={51200}>51200 MB (50 GB)</option>
                  </select>
                </div>
              </div>
            )}
          </div>

          {/* Footer buttons */}
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
              <Plus className="h-3.5 w-3.5" />
              <span>Start Build</span>
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
