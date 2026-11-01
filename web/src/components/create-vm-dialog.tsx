import { useEffect, useMemo, useRef, useState } from 'react';
import { ChevronDown, Copy, Disc, Key, Layers, Loader2, Plus } from 'lucide-react';

import { Button } from '@/components/ui/button';
import { Checkbox } from '@/components/ui/checkbox';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import { Slider } from '@/components/ui/slider';
import { api } from '@/lib/api';
import { formatMemory } from '@/lib/format';
import type { CreateVMRequest, CustomImage, DaemonConfig, SSHKey, Template } from '@/lib/types';
import { cn } from '@/lib/utils';

type Preset = '1vcpu' | '2vcpu' | '4vcpu' | 'custom';

const PRESETS: Array<{ id: Preset; label: string; detail: string; vcpus: number; memory: number }> = [
  { id: '1vcpu', label: '1 vCPU', detail: 'Micro · 2 GiB RAM', vcpus: 1, memory: 2048 },
  { id: '2vcpu', label: '2 vCPU', detail: 'Small · 4 GiB RAM', vcpus: 2, memory: 4096 },
  { id: '4vcpu', label: '4 vCPU', detail: 'Medium · 8 GiB RAM', vcpus: 4, memory: 8192 },
  { id: 'custom', label: 'Custom RAM', detail: 'Fixed vCPU · custom RAM', vcpus: 2, memory: 4096 },
];

function randomName() {
  return `vm-${Math.random().toString(36).slice(2, 7)}`;
}

export function CreateVMDialog({
  open,
  onOpenChange,
  config,
  onCreate,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  config: DaemonConfig | null;
  onCreate: (payload: CreateVMRequest, autoStart: boolean) => Promise<void>;
}) {
  const [name, setName] = useState(randomName);
  const [preset, setPreset] = useState<Preset>('2vcpu');
  const [vcpus, setVcpus] = useState(2);
  const [memory, setMemory] = useState(4096);
  const [diskGB, setDiskGB] = useState<number>(50);
  const [sourceType, setSourceType] = useState<'standard' | 'custom-image' | 'template'>('standard');
  const [selectedImage, setSelectedImage] = useState('ubuntu-24');
  const [selectedTemplate, setSelectedTemplate] = useState('');
  const [availableImages, setAvailableImages] = useState<CustomImage[]>([]);
  const [availableTemplates, setAvailableTemplates] = useState<Template[]>([]);
  const [availableKeys, setAvailableKeys] = useState<SSHKey[]>([]);
  const [keyChoice, setKeyChoice] = useState<string>('default');
  const [customKey, setCustomKey] = useState<string>('');
  const [cloneFrom, setCloneFrom] = useState('');
  const [ipAddress, setIPAddress] = useState('');
  const [macAddress, setMACAddress] = useState('');
  const [publicIP, setPublicIP] = useState(true);
  const [autoStart, setAutoStart] = useState(true);
  const [advanced, setAdvanced] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [touched, setTouched] = useState(false);
  const initialized = useRef(false);

  useEffect(() => {
    if (!open) {
      initialized.current = false;
      return;
    }
    if (initialized.current) return;
    initialized.current = true;

    setName(randomName());
    setPreset('2vcpu');
    setVcpus(config?.defaults.vcpus || 2);
    setMemory(config?.defaults.memory_mb || 4096);
    setDiskGB(50);
    setSourceType('standard');
    setSelectedImage('ubuntu-24');
    setSelectedTemplate('');
    setCloneFrom('');
    setIPAddress('');
    setMACAddress('');
    setPublicIP(true);
    setAutoStart(true);
    setAdvanced(false);
    setSubmitting(false);
    setTouched(false);
    setKeyChoice('default');
    setCustomKey('');

    // Fetch images, templates, and keys
    void Promise.allSettled([api.listImages(), api.listTemplates(), api.listKeys()]).then(
      ([imgs, tmpls, keys]) => {
        if (imgs.status === 'fulfilled') setAvailableImages(imgs.value.filter((i) => i.status === 'ready'));
        if (tmpls.status === 'fulfilled') setAvailableTemplates(tmpls.value);
        if (keys.status === 'fulfilled') {
          setAvailableKeys(keys.value);
          if (keys.value.length > 0) {
            setKeyChoice(keys.value[0].id);
          }
        }
      },
    );
  }, [open, config]);

  const ipValid =
    !ipAddress.trim() ||
    ipAddress.trim().toLowerCase() === 'dhcp' ||
    /^(\d{1,3}\.){3}\d{1,3}$/.test(ipAddress.trim());
  const macValid =
    !macAddress.trim() ||
    /^([0-9a-fA-F]{2}:){5}[0-9a-fA-F]{2}$/.test(macAddress.trim());
  const nameValid = name.trim().length > 0;
  const vcpuValid = vcpus === 1 || vcpus === 2 || vcpus === 4;
  const diskValid = diskGB === 50 || diskGB === 100;
  const customKeyValid =
    keyChoice !== 'custom' ||
    customKey.trim().startsWith('ssh-') ||
    customKey.trim().startsWith('ecdsa-');
  const valid = nameValid && ipValid && macValid && vcpuValid && diskValid && memory >= 128 && customKeyValid;

  function choosePreset(next: Preset) {
    setPreset(next);
    const item = PRESETS.find((entry) => entry.id === next);
    if (item && next !== 'custom') {
      setVcpus(item.vcpus);
      setMemory(item.memory);
    }
  }

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    setTouched(true);
    if (!valid || submitting) return;

    setSubmitting(true);
    try {
      const payload: CreateVMRequest = {
        name: name.trim(),
        tier: preset !== 'custom' ? preset : `${vcpus}vcpu`,
        vcpus,
        memory_mb: memory,
        disk_mb: diskGB * 1024,
        clone_from: cloneFrom.trim() || undefined,
        ip_address: ipAddress.trim() || undefined,
        mac_address: macAddress.trim() || undefined,
        public_ip: publicIP,
      };

      if (sourceType === 'template' && selectedTemplate) {
        payload.template = selectedTemplate;
      } else if (sourceType === 'custom-image' && selectedImage) {
        payload.image = selectedImage;
      } else if (!cloneFrom.trim()) {
        payload.image = 'ubuntu-24';
      }

      if (keyChoice === 'custom' && customKey.trim()) {
        payload.ssh_public_key = customKey.trim();
      } else if (keyChoice !== 'default' && keyChoice !== 'custom') {
        payload.ssh_key_id = keyChoice;
      }

      await onCreate(payload, autoStart);
      onOpenChange(false);
    } catch {
      // The caller surfaces the error; keep the dialog open for correction.
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[92vh] max-w-2xl overflow-hidden p-0">
        <form onSubmit={submit} className="flex max-h-[92vh] flex-col">
          <DialogHeader className="border-b p-6 pb-4">
            <DialogTitle>New instance</DialogTitle>
            <DialogDescription>
              Provision a Firecracker microVM from standard Ubuntu, custom packages, or disk template.
            </DialogDescription>
          </DialogHeader>

          <div className="flex-1 space-y-5 overflow-y-auto p-6">
            <div className="space-y-1.5">
              <Label htmlFor="vm-name">Name</Label>
              <Input
                id="vm-name"
                value={name}
                onChange={(event) => setName(event.target.value)}
                placeholder="e.g. api-worker-1"
                autoFocus
                className="font-mono"
              />
              {touched && !nameValid ? (
                <p className="text-xs text-destructive">A name is required.</p>
              ) : null}
            </div>

            <div className="space-y-2">
              <Label>Fixed tiers (vCPU & RAM)</Label>
              <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
                {PRESETS.map((item) => {
                  const selected = preset === item.id;
                  return (
                    <button
                      key={item.id}
                      type="button"
                      onClick={() => choosePreset(item.id)}
                      className={cn(
                        'rounded-md border px-2.5 py-2 text-left transition-colors',
                        selected
                          ? 'border-primary bg-accent text-accent-foreground'
                          : 'bg-card hover:bg-secondary',
                      )}
                    >
                      <div className="text-xs font-medium">{item.label}</div>
                      <div
                        className={cn(
                          'mt-0.5 font-mono text-[10px]',
                          selected ? 'text-accent-foreground/80' : 'text-muted-foreground',
                        )}
                      >
                        {item.detail}
                      </div>
                    </button>
                  );
                })}
              </div>
            </div>

            {preset === 'custom' ? (
              <div className="space-y-4 rounded-md border bg-secondary/30 p-4">
                <div className="space-y-2">
                  <div className="flex items-center justify-between text-xs">
                    <Label htmlFor="vcpus" className="font-normal text-muted-foreground">
                      vCPUs (Fixed: 1, 2, or 4)
                    </Label>
                    <span className="font-mono">{vcpus} vCPU</span>
                  </div>
                  <div className="flex gap-2">
                    {[1, 2, 4].map((v) => (
                      <button
                        key={v}
                        type="button"
                        onClick={() => setVcpus(v)}
                        className={cn(
                          'flex-1 rounded border py-1.5 text-xs font-mono',
                          vcpus === v
                            ? 'border-primary bg-primary text-primary-foreground'
                            : 'bg-card hover:bg-secondary',
                        )}
                      >
                        {v} vCPU
                      </button>
                    ))}
                  </div>
                </div>

                <div className="space-y-2">
                  <div className="flex items-center justify-between text-xs">
                    <Label htmlFor="memory" className="font-normal text-muted-foreground">
                      RAM (Optional custom size)
                    </Label>
                    <span className="font-mono">{formatMemory(memory)}</span>
                  </div>
                  <Slider
                    id="memory"
                    min={512}
                    max={16384}
                    step={256}
                    value={[memory]}
                    onValueChange={([value]) => setMemory(value)}
                  />
                </div>
              </div>
            ) : null}

            <div className="space-y-2">
              <Label>Persistent disk size</Label>
              <div className="grid grid-cols-2 gap-3">
                {[50, 100].map((size) => (
                  <button
                    key={size}
                    type="button"
                    onClick={() => setDiskGB(size)}
                    className={cn(
                      'flex items-center justify-between rounded-md border p-3 text-left transition-colors',
                      diskGB === size
                        ? 'border-primary bg-accent text-accent-foreground'
                        : 'bg-card hover:bg-secondary',
                    )}
                  >
                    <div>
                      <div className="text-sm font-semibold">{size} GB</div>
                      <div className="text-xs text-muted-foreground">
                        {size === 50 ? 'Standard Boat.dev baseline' : 'High capacity storage'}
                      </div>
                    </div>
                    <div className="font-mono text-xs text-muted-foreground">
                      {size * 1024} MB
                    </div>
                  </button>
                ))}
              </div>
            </div>

            <div className="space-y-2">
              <Label>Boot Image / Disk Template</Label>
              <Select
                value={
                  sourceType === 'standard'
                    ? 'standard:ubuntu-24'
                    : sourceType === 'custom-image'
                    ? `image:${selectedImage}`
                    : `template:${selectedTemplate}`
                }
                onValueChange={(val) => {
                  const [type, id] = val.split(':');
                  if (type === 'standard') {
                    setSourceType('standard');
                    setSelectedImage('ubuntu-24');
                  } else if (type === 'image') {
                    setSourceType('custom-image');
                    setSelectedImage(id);
                  } else if (type === 'template') {
                    setSourceType('template');
                    setSelectedTemplate(id);
                  }
                }}
              >
                <SelectTrigger className="text-xs">
                  <SelectValue placeholder="Select base image or template" />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    <SelectLabel>Official Base Images</SelectLabel>
                    <SelectItem value="standard:ubuntu-24">
                      ubuntu-24 · Ubuntu 24.04 LTS (Noble Numbat)
                    </SelectItem>
                  </SelectGroup>

                  {availableImages.length > 0 ? (
                    <SelectGroup>
                      <SelectLabel>Pre-baked Custom Images</SelectLabel>
                      {availableImages.map((img) => (
                        <SelectItem key={img.id} value={`image:${img.name}`}>
                          {img.name} · {img.packages?.join(', ') || 'No extra packages'}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  ) : null}

                  {availableTemplates.length > 0 ? (
                    <SelectGroup>
                      <SelectLabel>Disk Templates</SelectLabel>
                      {availableTemplates.map((tmpl) => (
                        <SelectItem key={tmpl.id} value={`template:${tmpl.name}`}>
                          {tmpl.name} {tmpl.description ? `· ${tmpl.description}` : ''}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  ) : null}
                </SelectContent>
              </Select>
              <p className="text-[11px] text-muted-foreground">
                Choose the standard Ubuntu image, a custom pre-baked image with packages, or a frozen disk template.
              </p>
            </div>

            {/* SSH Key Selection */}
            <div className="space-y-2">
              <div className="flex items-center justify-between">
                <Label htmlFor="ssh-key-select">SSH Public Key</Label>
                <span className="text-[11px] text-muted-foreground">Embedded into authorized_keys</span>
              </div>
              <Select value={keyChoice} onValueChange={setKeyChoice}>
                <SelectTrigger id="ssh-key-select" className="text-xs">
                  <SelectValue placeholder="Select SSH public key" />
                </SelectTrigger>
                <SelectContent>
                  {availableKeys.length > 0 ? (
                    <SelectGroup>
                      <SelectLabel>Account Saved Keys</SelectLabel>
                      {availableKeys.map((k) => (
                        <SelectItem key={k.id} value={k.id}>
                          {k.name} ({k.fingerprint.slice(0, 16)}…)
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  ) : null}
                  <SelectGroup>
                    <SelectLabel>Other</SelectLabel>
                    <SelectItem value="default">
                      Default Daemon Key (internal access only)
                    </SelectItem>
                    <SelectItem value="custom">
                      Paste custom public key…
                    </SelectItem>
                  </SelectGroup>
                </SelectContent>
              </Select>

              {keyChoice === 'custom' ? (
                <div className="space-y-1.5 pt-1">
                  <textarea
                    value={customKey}
                    onChange={(e) => setCustomKey(e.target.value)}
                    placeholder="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI... user@machine"
                    rows={2}
                    className="w-full resize-none rounded-md border bg-background px-3 py-2 font-mono text-xs focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  />
                  {!customKeyValid && (
                    <p className="text-[11px] text-destructive">Key must start with ssh-ed25519, ssh-rsa, or ecdsa-.</p>
                  )}
                  <p className="text-[10px] text-muted-foreground">
                    Key will be injected into /home/compute/.ssh/authorized_keys and /root/.ssh/authorized_keys.
                  </p>
                </div>
              ) : (
                <p className="text-[11px] text-muted-foreground">
                  Allows immediate passwordless SSH to the microVM using <code className="font-mono text-[10px]">ssh compute@&lt;ip&gt;</code> or <code className="font-mono text-[10px]">compute ssh</code>.
                </p>
              )}
            </div>

            <div className="rounded-md border">
              <button
                type="button"
                onClick={() => setAdvanced((value) => !value)}
                className="flex w-full items-center justify-between px-3 py-2.5 text-left text-xs font-medium"
              >
                Disk clone & Advanced networking
                <ChevronDown
                  className={cn(
                    'h-4 w-4 text-muted-foreground transition-transform',
                    advanced && 'rotate-180',
                  )}
                />
              </button>

              {advanced ? (
                <div className="space-y-3 border-t p-3">
                  <div className="space-y-1.5">
                    <Label htmlFor="clone-from" className="text-[11px] font-normal text-muted-foreground">
                      Clone disk from existing VM (Optional)
                    </Label>
                    <div className="relative">
                      <Input
                        id="clone-from"
                        value={cloneFrom}
                        onChange={(event) => setCloneFrom(event.target.value)}
                        placeholder="e.g. vm-a1b2c3d4"
                        className="font-mono text-xs pl-8"
                      />
                      <Copy className="absolute left-2.5 top-2.5 h-3.5 w-3.5 text-muted-foreground" />
                    </div>
                    <p className="text-[10px] text-muted-foreground">
                      Clones the exact persistent disk state of an existing VM to run this new VM.
                    </p>
                  </div>

                  <div className="grid gap-3 sm:grid-cols-2">
                    <div className="space-y-1.5">
                      <Label htmlFor="ip" className="text-[11px] font-normal text-muted-foreground">
                        Static IPv4
                      </Label>
                      <Input
                        id="ip"
                        value={ipAddress}
                        onChange={(event) => setIPAddress(event.target.value)}
                        placeholder="Auto-assign or 'dhcp'"
                        className="font-mono text-xs"
                      />
                      {!ipValid ? (
                        <p className="text-[11px] text-destructive">Enter a valid IPv4 address or 'dhcp'.</p>
                      ) : null}
                    </div>
                    <div className="space-y-1.5">
                      <Label htmlFor="mac" className="text-[11px] font-normal text-muted-foreground">
                        MAC address
                      </Label>
                      <Input
                        id="mac"
                        value={macAddress}
                        onChange={(event) => setMACAddress(event.target.value)}
                        placeholder="Auto-assign"
                        className="font-mono text-xs"
                      />
                      {!macValid ? (
                        <p className="text-[11px] text-destructive">
                          Use the form aa:bb:cc:dd:ee:ff.
                        </p>
                      ) : null}
                    </div>
                  </div>
                </div>
              ) : null}
            </div>

            <div className="flex items-start gap-2.5 rounded-md border p-3 bg-secondary/15">
              <Checkbox
                id="public-ip"
                checked={publicIP}
                onCheckedChange={(value) => setPublicIP(value === true)}
                className="mt-0.5"
              />
              <div>
                <Label htmlFor="public-ip" className="cursor-pointer font-medium text-xs">
                  Dedicated Public IP (Main Network)
                </Label>
                <p className="mt-0.5 text-[11px] text-muted-foreground">
                  Assigns a dedicated IP on the main network considered as the VM's public IP. Can be defined on or off.
                </p>
              </div>
            </div>

            <div className="flex items-start gap-2.5 rounded-md border p-3 bg-secondary/15">
              <Checkbox
                id="auto-start"
                checked={autoStart}
                onCheckedChange={(value) => setAutoStart(value === true)}
                className="mt-0.5"
              />
              <div>
                <Label htmlFor="auto-start" className="cursor-pointer font-medium text-xs">
                  Start instance immediately
                </Label>
                <p className="mt-0.5 text-[11px] text-muted-foreground">
                  Boot the Firecracker microVM immediately after provisioning.
                </p>
              </div>
            </div>
          </div>

          <DialogFooter className="border-t p-4">
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => onOpenChange(false)}
              disabled={submitting}
            >
              Cancel
            </Button>
            <Button type="submit" size="sm" disabled={!valid || submitting}>
              {submitting ? (
                <>
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                  Provisioning…
                </>
              ) : (
                'Provision instance'
              )}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
