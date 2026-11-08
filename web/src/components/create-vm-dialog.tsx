import { useEffect, useState } from 'react';
import { ChevronDown, Play, X } from 'lucide-react';

import { api } from '@/lib/api';
import type {
  CreateVMRequest,
  CustomImage,
  DaemonConfig,
  EnvironmentProfileRecord,
  Template,
} from '@/lib/types';
import { cn } from '@/lib/utils';

interface CreateVMDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  config: DaemonConfig | null;
  onCreate: (payload: CreateVMRequest, autoStart: boolean) => Promise<void>;
}

interface BoatTier {
  id: 'small' | 'default' | 'large' | 'xlarge';
  label: string;
  vcpus: number;
  ramGb: number;
  memoryMb: number;
  diskGb: number;
  pricePerHour: string;
}

const BOAT_TIERS: BoatTier[] = [
  {
    id: 'small',
    label: 'small',
    vcpus: 2,
    ramGb: 4,
    memoryMb: 4096,
    diskGb: 25,
    pricePerHour: '$0.018/h',
  },
  {
    id: 'default',
    label: 'default',
    vcpus: 4,
    ramGb: 8,
    memoryMb: 8192,
    diskGb: 51,
    pricePerHour: '$0.036/h',
  },
  {
    id: 'large',
    label: 'large',
    vcpus: 8,
    ramGb: 16,
    memoryMb: 16384,
    diskGb: 102,
    pricePerHour: '$0.072/h',
  },
  {
    id: 'xlarge',
    label: 'xlarge',
    vcpus: 16,
    ramGb: 32,
    memoryMb: 32768,
    diskGb: 204,
    pricePerHour: '$0.144/h',
  },
];

export function CreateVMDialog({
  open,
  onOpenChange,
  config,
  onCreate,
}: CreateVMDialogProps) {
  const [selectedTierId, setSelectedTierId] = useState<BoatTier['id']>('default');
  const [sandboxName, setSandboxName] = useState('');
  const [fromSource, setFromSource] = useState('fresh');
  const [shareCode, setShareCode] = useState('');
  const [environment, setEnvironment] = useState('Default');
  const [autoStopHours, setAutoStopHours] = useState('1');
  const [showAllSizes, setShowAllSizes] = useState(false);

  const [templates, setTemplates] = useState<Template[]>([]);
  const [images, setImages] = useState<CustomImage[]>([]);
  const [environments, setEnvironments] = useState<EnvironmentProfileRecord[]>([]);

  // Exact 4 toggles from boat.dev/dashboard Advanced create modal
  const [neverAutoStop, setNeverAutoStop] = useState(false);
  const [noEnv, setNoEnv] = useState(false);
  const [snapshotsOff, setSnapshotsOff] = useState(false);
  const [monitoringOff, setMonitoringOff] = useState(false);

  const [setupScript, setSetupScript] = useState('#!/bin/bash\nnpm install');
  const [submitting, setSubmitting] = useState(false);

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
      .listEnvironments()
      .then((res) => setEnvironments(res || []))
      .catch(() => setEnvironments([]));
  }, [open]);

  if (!open) return null;

  const activeTier =
    BOAT_TIERS.find((t) => t.id === selectedTierId) ?? BOAT_TIERS[1];

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setSubmitting(true);
    try {
      const generatedName =
        sandboxName.trim() ||
        `sbx-${activeTier.id}-${Math.random().toString(36).slice(2, 6)}`;

      const payload: CreateVMRequest = {
        name: generatedName,
        vcpus: activeTier.vcpus,
        memory_mb: activeTier.memoryMb,
        public_ip_enabled: true,
      };

      if (shareCode.trim()) {
        payload.clone_from = shareCode.trim();
      } else if (fromSource.startsWith('tpl:')) {
        payload.template = fromSource.slice(4);
      } else if (fromSource.startsWith('img:')) {
        payload.image = fromSource.slice(4);
      }

      await onCreate(payload, true);
      onOpenChange(false);
      setSandboxName('');
      setShareCode('');
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
        {/* Header matching dashboard-02-advanced-create-modal.png */}
        <div className="flex items-center justify-between mb-5">
          <h2
            id="advanced-create-title"
            className="text-[15px] font-medium text-foreground"
          >
            Advanced create
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
          {/* Type: 4-column rectangular tier cards matching dashboard-02 */}
          <div className="space-y-2">
            <label className="block text-[12px] text-muted-foreground">
              Type
            </label>
            <div className="grid grid-cols-4 gap-2">
              {BOAT_TIERS.map((tier) => {
                const selected = tier.id === selectedTierId;
                return (
                  <button
                    key={tier.id}
                    type="button"
                    onClick={() => setSelectedTierId(tier.id)}
                    className={cn(
                      'flex flex-col items-center justify-center border py-2.5 px-1.5 text-center transition-colors',
                      selected
                        ? 'border-[#495eb7] bg-[#0f1634] text-white'
                        : 'border-border bg-background text-foreground hover:border-foreground/40',
                    )}
                  >
                    <span className="text-[12px] font-medium leading-tight">
                      {tier.label}
                    </span>
                    <span className="mt-1 text-[10px] text-muted-foreground leading-tight">
                      {tier.vcpus} vCPU · {tier.ramGb} GB
                    </span>
                  </button>
                );
              })}
            </div>
            <p className="text-[11.5px] text-muted-foreground pt-0.5">
              {activeTier.vcpus} vCPU · {activeTier.ramGb} GB RAM ·{' '}
              {activeTier.diskGb} GB disk · {activeTier.pricePerHour}.{' '}
              <button
                type="button"
                onClick={() => setShowAllSizes((v) => !v)}
                className="underline underline-offset-2 text-muted-foreground hover:text-foreground"
              >
                All sizes
              </button>
            </p>
          </div>

          {showAllSizes && (
            <div className="border border-border bg-muted/20 p-2.5 text-[11px]">
              <label className="block text-muted-foreground mb-1">
                Sandbox Name (optional)
              </label>
              <input
                type="text"
                value={sandboxName}
                onChange={(e) => setSandboxName(e.target.value)}
                placeholder="sbx-prod-api-01"
                className="h-8 w-full border border-border bg-background px-2 font-mono text-xs text-foreground focus:border-[#263fa9] focus:outline-none"
              />
            </div>
          )}

          {/* From: 2-column row [ Fresh sandbox ⌄ ] [ or paste bsh_... share code ] */}
          <div className="space-y-1.5">
            <label className="block text-[12px] text-muted-foreground">
              From
            </label>
            <div className="grid grid-cols-2 gap-2">
              <div className="relative">
                <select
                  value={fromSource}
                  onChange={(e) => setFromSource(e.target.value)}
                  className="h-9 w-full appearance-none border border-border bg-background px-3 pr-8 text-[12.5px] text-foreground focus:border-[#263fa9] focus:outline-none"
                >
                  <option value="fresh">Fresh sandbox</option>
                  {config?.images?.available?.map((img) => (
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
              <input
                type="text"
                value={shareCode}
                onChange={(e) => setShareCode(e.target.value)}
                placeholder="or paste bsh_... share co"
                className="h-9 w-full border border-border bg-background px-2.5 font-mono text-[11.5px] text-foreground placeholder:text-muted-foreground focus:border-[#263fa9] focus:outline-none"
              />
            </div>
          </div>

          {/* Environment dropdown */}
          <div className="space-y-1.5">
            <label className="block text-[12px] text-muted-foreground">
              Environment
            </label>
            <div className="relative">
              <select
                value={environment}
                onChange={(e) => setEnvironment(e.target.value)}
                disabled={noEnv}
                className="h-9 w-full appearance-none border border-border bg-background px-3 pr-8 text-[12.5px] text-foreground focus:border-[#263fa9] focus:outline-none disabled:opacity-50"
              >
                <option value="Default">Default</option>
                {environments.map((env) => (
                  <option key={env.name} value={env.name}>
                    {env.name} (v{env.version})
                  </option>
                ))}
              </select>
              <ChevronDown className="pointer-events-none absolute right-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
            </div>
          </div>

          {/* Auto-stop after [ 1 ] hours */}
          <div className="space-y-1.5">
            <label className="block text-[12px] text-muted-foreground">
              Auto-stop after
            </label>
            <div className="flex items-center gap-2.5">
              <input
                type="number"
                min={1}
                max={720}
                value={autoStopHours}
                disabled={neverAutoStop}
                onChange={(e) => setAutoStopHours(e.target.value)}
                className="h-9 w-24 border border-border bg-background px-3 font-mono text-[12.5px] text-foreground focus:border-[#263fa9] focus:outline-none disabled:opacity-50"
              />
              <span className="text-[12px] text-muted-foreground">hours</span>
            </div>
          </div>

          {/* 4 Rectangular Toggle Switch Rows matching dashboard-02 1:1 */}
          <div className="divide-y divide-border border-t border-border pt-1">
            <ToggleRow
              title="Never auto-stop"
              description="The sandbox runs until you stop it."
              checked={neverAutoStop}
              onChange={setNeverAutoStop}
            />
            <ToggleRow
              title="No env"
              description="No secret or login of yours reaches the sandbox."
              checked={noEnv}
              onChange={setNoEnv}
            />
            <ToggleRow
              title="Snapshots off"
              description="Never saved. A stop erases the sandbox."
              checked={snapshotsOff}
              onChange={setSnapshotsOff}
            />
            <ToggleRow
              title="Monitoring off"
              description="No command or process output, process list or CPU/RAM graphs kept."
              checked={monitoringOff}
              onChange={setMonitoringOff}
            />
          </div>

          {/* Setup script monospace textarea */}
          <div className="space-y-1.5 pt-1">
            <label className="block text-[12px] text-muted-foreground">
              Setup script
            </label>
            <textarea
              rows={3}
              value={setupScript}
              onChange={(e) => setSetupScript(e.target.value)}
              className="w-full border border-border bg-[#1a1a1a] p-3 font-mono text-[12px] text-foreground placeholder:text-muted-foreground focus:border-[#263fa9] focus:outline-none"
            />
          </div>

          {/* Bottom-right [ ▷ Create ] cobalt button */}
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

function ToggleRow({
  title,
  description,
  checked,
  onChange,
}: {
  title: string;
  description: string;
  checked: boolean;
  onChange: (next: boolean) => void;
}) {
  return (
    <div className="flex items-center justify-between gap-4 py-3">
      <div className="space-y-0.5">
        <div className="text-[12.5px] font-medium text-foreground">{title}</div>
        <div className="text-[11.5px] text-muted-foreground leading-snug">
          {description}
        </div>
      </div>
      <button
        type="button"
        role="switch"
        aria-checked={checked}
        onClick={() => onChange(!checked)}
        className="flex items-center gap-2 shrink-0 focus:outline-none"
      >
        <span
          className={cn(
            'relative inline-flex h-5 w-9 items-center border transition-colors p-0.5',
            checked
              ? 'border-[#263fa9] bg-[#263fa9]'
              : 'border-border bg-[#2e2e2e]',
          )}
        >
          <span
            className={cn(
              'inline-block h-3.5 w-3.5 bg-white transition-transform',
              checked ? 'translate-x-3.5' : 'translate-x-0',
            )}
          />
        </span>
        <span className="w-6 text-left text-[11.5px] text-muted-foreground font-mono">
          {checked ? 'On' : 'Off'}
        </span>
      </button>
    </div>
  );
}
