import { useEffect, useMemo, useState } from 'react';
import { AlertTriangle, ChevronDown, Play, X } from 'lucide-react';

import { api } from '@/lib/api';
import type {
  CreateVMRequest,
  CustomImage,
  DaemonConfig,
  StatsResult,
  Template,
} from '@/lib/types';
import { cn } from '@/lib/utils';

interface CreateVMDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  config: DaemonConfig | null;
  onCreate: (
    payload: CreateVMRequest,
    autoStart: boolean,
    options?: { setupScript?: string },
  ) => Promise<void>;
}

interface Preset {
  vcpus: 1 | 2 | 4;
  label: string;
  defaultMemoryMb: number;
}

const PRESETS: Preset[] = [
  { vcpus: 1, label: 'micro', defaultMemoryMb: 512 },
  { vcpus: 2, label: 'small', defaultMemoryMb: 1024 },
  { vcpus: 4, label: 'medium', defaultMemoryMb: 2048 },
];

const HOST_RESERVE_MB = 256;

function formatMemory(mb: number): string {
  if (mb >= 1024) {
    const gb = mb / 1024;
    return Number.isInteger(gb) ? `${gb} GB` : `${gb.toFixed(1)} GB`;
  }
  return `${mb} MB`;
}

export function CreateVMDialog({
  open,
  onOpenChange,
  config,
  onCreate,
}: CreateVMDialogProps) {
  const [vcpus, setVcpus] = useState<1 | 2 | 4>(1);
  const [memoryMb, setMemoryMb] = useState(512);
  const [diskGb, setDiskGb] = useState(50);
  const [sandboxName, setSandboxName] = useState('');
  const [fromSource, setFromSource] = useState('fresh');
  const [setupScript, setSetupScript] = useState('');
  const [submitting, setSubmitting] = useState(false);

  const [templates, setTemplates] = useState<Template[]>([]);
  const [images, setImages] = useState<CustomImage[]>([]);
  const [stats, setStats] = useState<StatsResult | null>(null);

  useEffect(() => {
    if (!open) return;
    void api
      .listTemplates()
      .then((res) => setTemplates(res || []))
      .catch(() => setTemplates([]));
    void api
      .listImages()
      .then((res) => setImages(res || []))
      .catch(() => setImages([]));
    void api
      .stats()
      .then((res) => setStats(res))
      .catch(() => setStats(null));
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onOpenChange(false);
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [open, onOpenChange]);

  // Keep the default memory inside the host's actual headroom so the first
  // click does not immediately queue the sandbox for lack of RAM.
  const maxUsableMb = useMemo(() => {
    const available = stats?.host_memory_available_mb ?? 0;
    if (!available) return null;
    return Math.max(128, available - HOST_RESERVE_MB);
  }, [stats]);

  useEffect(() => {
    if (!open || !maxUsableMb) return;
    setMemoryMb((current) => Math.min(current, maxUsableMb));
  }, [open, maxUsableMb]);

  if (!open) return null;

  const availableImages = config?.images?.available ?? [];
  const willQueue = maxUsableMb !== null && memoryMb > maxUsableMb;

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setSubmitting(true);
    try {
      const generatedName =
        sandboxName.trim() ||
        `sbx-${PRESETS.find((p) => p.vcpus === vcpus)?.label ?? 'micro'}-${Math.random()
          .toString(36)
          .slice(2, 6)}`;

      const payload: CreateVMRequest = {
        name: generatedName,
        vcpus,
        memory_mb: memoryMb,
        disk_mb: diskGb === 100 ? 102400 : 51200,
        public_ip_enabled: true,
      };

      if (fromSource.startsWith('tpl:')) {
        payload.template = fromSource.slice(4);
      } else if (fromSource.startsWith('img:')) {
        payload.image = fromSource.slice(4);
      } else {
        payload.image = 'ubuntu-24';
      }

      await onCreate(payload, true, {
        setupScript: setupScript.trim() || undefined,
      });
      onOpenChange(false);
      setSandboxName('');
      setSetupScript('');
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
        aria-labelledby="advanced-create-title"
        className="w-full max-w-[440px] border border-border bg-background p-6 text-foreground my-auto"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between mb-5">
          <h2
            id="advanced-create-title"
            className="text-[15px] font-medium text-foreground"
          >
            New sandbox
          </h2>
          <button
            type="button"
            onClick={() => onOpenChange(false)}
            className="text-muted-foreground hover:text-foreground transition-colors"
            aria-label="Close modal"
          >
            <X className="h-4 w-4" />
          </button>
        </div>

        <form onSubmit={handleSubmit} className="space-y-4">
          <div className="space-y-1.5">
            <label className="block text-[12px] text-muted-foreground">
              Name (optional)
            </label>
            <input
              type="text"
              aria-label="Sandbox name"
              value={sandboxName}
              onChange={(e) => setSandboxName(e.target.value)}
              placeholder="sbx-prod-api-01"
              className="h-9 w-full border border-border bg-background px-3 font-mono text-[12.5px] text-foreground placeholder:text-muted-foreground focus:border-[#263fa9] focus:outline-none"
            />
          </div>

          <div className="space-y-2">
            <label className="block text-[12px] text-muted-foreground">
              Type
            </label>
            <div className="grid grid-cols-3 gap-2">
              {PRESETS.map((preset) => {
                const selected = preset.vcpus === vcpus;
                return (
                  <button
                    key={preset.vcpus}
                    type="button"
                    onClick={() => {
                      setVcpus(preset.vcpus);
                      setMemoryMb(
                        maxUsableMb
                          ? Math.min(preset.defaultMemoryMb, maxUsableMb)
                          : preset.defaultMemoryMb,
                      );
                    }}
                    className={cn(
                      'flex flex-col items-center justify-center border py-2.5 px-1.5 text-center transition-colors',
                      selected
                        ? 'border-[#495eb7] bg-[#0f1634] text-white'
                        : 'border-border bg-background text-foreground hover:border-foreground/40',
                    )}
                  >
                    <span className="text-[12px] font-medium leading-tight">
                      {preset.label}
                    </span>
                    <span className="mt-1 text-[10px] text-muted-foreground leading-tight">
                      {preset.vcpus} vCPU · {formatMemory(preset.defaultMemoryMb)}
                    </span>
                  </button>
                );
              })}
            </div>
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-1.5">
              <label className="block text-[12px] text-muted-foreground">
                Memory (MB)
              </label>
              <input
                type="number"
                aria-label="Memory in megabytes"
                min={128}
                step={128}
                value={memoryMb}
                onChange={(e) => setMemoryMb(Math.max(128, Number(e.target.value) || 128))}
                className="h-9 w-full border border-border bg-background px-3 font-mono text-[12.5px] text-foreground focus:border-[#263fa9] focus:outline-none"
              />
            </div>
            <div className="space-y-1.5">
              <label className="block text-[12px] text-muted-foreground">
                Disk
              </label>
              <div className="relative">
                <select
                  aria-label="Disk size"
                  value={diskGb}
                  onChange={(e) => setDiskGb(Number(e.target.value))}
                  className="h-9 w-full appearance-none border border-border bg-background px-3 pr-8 text-[12.5px] text-foreground focus:border-[#263fa9] focus:outline-none"
                >
                  <option value={50}>50 GB</option>
                  <option value={100}>100 GB</option>
                </select>
                <ChevronDown className="pointer-events-none absolute right-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
              </div>
            </div>
          </div>

          <div className="space-y-1.5">
            <label className="block text-[12px] text-muted-foreground">
              From
            </label>
            <div className="relative">
              <select
                aria-label="Sandbox source"
                value={fromSource}
                onChange={(e) => setFromSource(e.target.value)}
                className="h-9 w-full appearance-none border border-border bg-background px-3 pr-8 text-[12.5px] text-foreground focus:border-[#263fa9] focus:outline-none"
              >
                <option value="fresh">Fresh sandbox (ubuntu-24)</option>
                {availableImages
                  .filter((img) => img.name !== 'ubuntu-24')
                  .map((img) => (
                    <option key={`avail-${img.name}`} value={`img:${img.name}`}>
                      Image: {img.name}
                    </option>
                  ))}
                {images
                  .filter((i) => i.status === 'ready')
                  .map((img) => (
                    <option key={`custom-${img.id}`} value={`img:${img.name}`}>
                      Custom: {img.name}
                    </option>
                  ))}
                {templates.map((tpl) => (
                  <option key={`tpl-${tpl.id}`} value={`tpl:${tpl.name}`}>
                    Snapshot: {tpl.name}
                  </option>
                ))}
              </select>
              <ChevronDown className="pointer-events-none absolute right-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
            </div>
          </div>

          <div className="space-y-1.5">
            <label className="block text-[12px] text-muted-foreground">
              Setup script (optional, runs after boot)
            </label>
            <textarea
              rows={3}
              aria-label="Setup script"
              value={setupScript}
              onChange={(e) => setSetupScript(e.target.value)}
              placeholder="#!/bin/bash&#10;apt-get update && apt-get install -y nginx"
              className="w-full border border-border bg-background p-3 font-mono text-[12px] text-foreground placeholder:text-muted-foreground focus:border-[#263fa9] focus:outline-none"
            />
          </div>

          <div className="space-y-1 text-[11px] text-muted-foreground">
            {stats?.host_memory_available_mb ? (
              <p>
                Host headroom: {formatMemory(stats.host_memory_available_mb)} available
                {stats.host_cpus ? ` across ${stats.host_cpus} vCPU` : ''}.
              </p>
            ) : null}
            {willQueue ? (
              <p className="flex items-start gap-1.5 text-amber-400">
                <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
                Requested memory exceeds current headroom. The sandbox will be
                queued until capacity frees up.
              </p>
            ) : null}
          </div>

          <div className="flex justify-end pt-2">
            <button
              type="submit"
              disabled={submitting}
              className="flex h-9 items-center gap-2 bg-[#263fa9] hover:bg-[#1e3180] px-4 text-[13px] font-medium text-white transition-colors disabled:opacity-50"
            >
              <Play className="h-3 w-3 fill-current" />
              <span>{submitting ? 'Creating…' : 'Create'}</span>
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
