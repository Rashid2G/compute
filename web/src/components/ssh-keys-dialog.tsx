import { useEffect, useState } from 'react';
import { Copy, Key, Loader2, Plus, Trash2 } from 'lucide-react';
import { toast } from 'sonner';

import { Button } from '@/components/ui/button';
import { Checkbox } from '@/components/ui/checkbox';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { api, errorMessage } from '@/lib/api';
import type { SSHKey } from '@/lib/types';

export function SSHKeysDialog({
  open,
  onOpenChange,
  onKeyAdded,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onKeyAdded?: (key: SSHKey) => void;
}) {
  const [keys, setKeys] = useState<SSHKey[]>([]);
  const [loading, setLoading] = useState(false);
  const [togglingId, setTogglingId] = useState<string | null>(null);
  const [togglingDefaultId, setTogglingDefaultId] = useState<string | null>(null);
  const [name, setName] = useState('');
  const [publicKey, setPublicKey] = useState('');
  const [allSandboxes, setAllSandboxes] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [copiedId, setCopiedId] = useState<string | null>(null);

  async function loadKeys() {
    setLoading(true);
    try {
      const data = await api.listKeys();
      setKeys(data);
    } catch (err) {
      console.error('Failed to load SSH keys:', err);
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    if (open) {
      loadKeys();
      setError(null);
    }
  }, [open]);

  function handleAddKey(e: React.FormEvent) {
    e.preventDefault();
    const keyName = name.trim();
    const pubKey = publicKey.trim();
    if (!keyName || !pubKey) return;

    const payload = {
      name: keyName,
      public_key: pubKey,
      all_sandboxes: allSandboxes,
    };

    setError(null);
    setName('');
    setPublicKey('');
    setAllSandboxes(false);
    onOpenChange(false);

    const toastID = toast.loading(`Adding SSH key "${keyName}"…`);
    void api
      .createKey(payload)
      .then((created) => {
        setKeys((prev) => [created, ...prev]);
        onKeyAdded?.(created);
        toast.success(`Added SSH key "${created.name}"`, { id: toastID });
      })
      .catch((err) => {
        toast.error(`Could not add SSH key "${keyName}"`, {
          id: toastID,
          description: errorMessage(err),
        });
      });
  }

  function handleDeleteKey(key: SSHKey) {
    const label = key.name || key.id;
    onOpenChange(false);
    const toastID = toast.loading(`Deleting SSH key "${label}"…`);
    void api
      .deleteKey(key.id)
      .then(() => {
        setKeys((prev) => prev.filter((k) => k.id !== key.id));
        toast.success(`Deleted SSH key "${label}"`, { id: toastID });
      })
      .catch((err) => {
        toast.error(`Could not delete SSH key "${label}"`, {
          id: toastID,
          description: errorMessage(err),
        });
      });
  }

  async function handleToggleAll(key: SSHKey) {
    if (togglingId) return;
    setTogglingId(key.id);
    setError(null);
    try {
      const updated = await api.updateKey(key.id, {
        all_sandboxes: !key.all_sandboxes,
      });
      setKeys((prev) => prev.map((k) => (k.id === key.id ? updated : k)));
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setTogglingId(null);
    }
  }

  // Toggling the default reassigns the account's single default key, so the
  // whole list is reloaded to reflect which key carries the flag.
  async function handleToggleDefault(key: SSHKey) {
    if (togglingDefaultId) return;
    setTogglingDefaultId(key.id);
    setError(null);
    try {
      await api.updateKey(key.id, { is_default: !key.is_default });
      await loadKeys();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setTogglingDefaultId(null);
    }
  }

  function copyKey(key: SSHKey) {
    navigator.clipboard.writeText(key.public_key);
    setCopiedId(key.id);
    setTimeout(() => setCopiedId(null), 2000);
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] max-w-xl overflow-hidden p-0">
        <DialogHeader className="border-b p-6 pb-4">
          <div className="flex items-center gap-2">
            <div className="flex h-8 w-8 items-center justify-center rounded-md border bg-accent text-accent-foreground">
              <Key className="h-4 w-4" />
            </div>
            <div>
              <DialogTitle>Account SSH Keys</DialogTitle>
              <DialogDescription className="mt-0.5 text-xs">
                Manage public keys for your sandboxes. The "default" key is added
                to every new sandbox automatically; keys marked "all sandboxes"
                are added to every current and future sandbox.
              </DialogDescription>
            </div>
          </div>
        </DialogHeader>

        <div className="max-h-[calc(90vh-140px)] space-y-6 overflow-y-auto p-6">
          {/* Add Key Form */}
          <form onSubmit={handleAddKey} className="space-y-3 rounded-lg border bg-secondary/30 p-4">
            <div className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
              Add New Public Key
            </div>
            {error && (
              <div className="rounded border border-destructive/30 bg-destructive/10 p-2.5 text-xs text-destructive">
                {error}
              </div>
            )}
            <div className="space-y-1.5">
              <Label htmlFor="key-name" className="text-xs">
                Key Name
              </Label>
              <Input
                id="key-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="e.g. macbook-pro or id_ed25519"
                className="text-xs"
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="key-content" className="text-xs">
                Public Key (ssh-ed25519 or ssh-rsa)
              </Label>
              <textarea
                id="key-content"
                value={publicKey}
                onChange={(e) => setPublicKey(e.target.value)}
                placeholder="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI... user@machine"
                rows={3}
                className="w-full resize-none rounded-md border bg-background px-3 py-2 font-mono text-xs focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              />
            </div>
            <div className="flex items-center gap-2">
              <Checkbox
                id="key-all-sandboxes"
                checked={allSandboxes}
                onCheckedChange={(checked) => setAllSandboxes(checked === true)}
              />
              <Label htmlFor="key-all-sandboxes" className="text-xs font-normal">
                Apply to all current and future sandboxes
              </Label>
            </div>
            <div className="flex justify-end pt-1">
              <Button type="submit" size="sm" disabled={!name.trim() || !publicKey.trim()}>
                <Plus className="mr-1.5 h-3.5 w-3.5" />
                Add Public Key
              </Button>
            </div>
          </form>

          {/* Key List */}
          <div className="space-y-3">
            <div className="flex items-center justify-between text-xs font-semibold uppercase tracking-wider text-muted-foreground">
              <span>Saved Keys ({keys.length})</span>
              {loading && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
            </div>

            {keys.length === 0 && !loading ? (
              <div className="rounded-lg border border-dashed p-6 text-center text-xs text-muted-foreground">
                No SSH keys saved yet. Add a public key above to easily choose it when launching VMs.
              </div>
            ) : (
              <div className="divide-y rounded-lg border bg-card">
                {keys.map((k) => (
                  <div key={k.id} className="flex items-center justify-between gap-3 p-3 text-xs">
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2">
                        <span className="font-semibold text-foreground">{k.name}</span>
                        <span className="rounded bg-secondary px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground">
                          {k.id}
                        </span>
                        {k.all_sandboxes && (
                          <span className="rounded bg-emerald-500/15 px-1.5 py-0.5 text-[10px] font-medium text-emerald-600 dark:text-emerald-400">
                            all sandboxes
                          </span>
                        )}
                        {k.is_default && (
                          <span className="rounded bg-[#263fa9]/15 px-1.5 py-0.5 text-[10px] font-medium text-[#263fa9] dark:text-[#8ea4ff]">
                            default
                          </span>
                        )}
                      </div>
                      <div className="mt-1 truncate font-mono text-[11px] text-muted-foreground">
                        {k.fingerprint || k.public_key.slice(0, 32) + '...'}
                      </div>
                    </div>
                    <div className="flex shrink-0 items-center gap-1.5">
                      <div className="flex items-center gap-1.5">
                        <Checkbox
                          id={`key-default-${k.id}`}
                          checked={k.is_default === true}
                          onCheckedChange={() => handleToggleDefault(k)}
                          disabled={togglingDefaultId === k.id}
                          aria-label={`Use ${k.name} as the default key for new sandboxes`}
                        />
                        <Label
                          htmlFor={`key-default-${k.id}`}
                          className="text-[10.5px] font-normal text-muted-foreground"
                        >
                          {togglingDefaultId === k.id ? (
                            <Loader2 className="h-3 w-3 animate-spin" />
                          ) : (
                            'default'
                          )}
                        </Label>
                      </div>
                      <div className="flex items-center gap-1.5">
                        <Checkbox
                          id={`key-all-${k.id}`}
                          checked={k.all_sandboxes}
                          onCheckedChange={() => handleToggleAll(k)}
                          disabled={togglingId === k.id}
                          aria-label={`Apply ${k.name} to all sandboxes`}
                        />
                        <Label
                          htmlFor={`key-all-${k.id}`}
                          className="text-[10.5px] font-normal text-muted-foreground"
                        >
                          {togglingId === k.id ? (
                            <Loader2 className="h-3 w-3 animate-spin" />
                          ) : (
                            'all'
                          )}
                        </Label>
                      </div>
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon-sm"
                        onClick={() => copyKey(k)}
                        title="Copy public key"
                      >
                        <Copy className="h-3.5 w-3.5" />
                      </Button>
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon-sm"
                        className="text-destructive hover:bg-destructive/10 hover:text-destructive"
                        onClick={() => handleDeleteKey(k)}
                        title="Delete key"
                      >
                        <Trash2 className="h-3.5 w-3.5" />
                      </Button>
                      {copiedId === k.id && (
                        <span className="text-[10px] text-emerald-500">Copied!</span>
                      )}
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
