import { useEffect, useState } from 'react';
import { Copy, Key, Loader2, Plus, Trash2 } from 'lucide-react';

import { Button } from '@/components/ui/button';
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
  const [adding, setAdding] = useState(false);
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const [name, setName] = useState('');
  const [publicKey, setPublicKey] = useState('');
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

  async function handleAddKey(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim() || !publicKey.trim() || adding) return;

    setAdding(true);
    setError(null);
    try {
      const created = await api.createKey({
        name: name.trim(),
        public_key: publicKey.trim(),
      });
      setName('');
      setPublicKey('');
      setKeys((prev) => [created, ...prev]);
      if (onKeyAdded) {
        onKeyAdded(created);
      }
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setAdding(false);
    }
  }

  async function handleDeleteKey(id: string) {
    if (deletingId) return;
    setDeletingId(id);
    try {
      await api.deleteKey(id);
      setKeys((prev) => prev.filter((k) => k.id !== id));
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setDeletingId(null);
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
                Manage public keys that can be embedded into newly created microVMs.
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
                disabled={adding}
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
                disabled={adding}
                className="w-full resize-none rounded-md border bg-background px-3 py-2 font-mono text-xs focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              />
            </div>
            <div className="flex justify-end pt-1">
              <Button type="submit" size="sm" disabled={!name.trim() || !publicKey.trim() || adding}>
                {adding ? <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" /> : <Plus className="mr-1.5 h-3.5 w-3.5" />}
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
                      </div>
                      <div className="mt-1 truncate font-mono text-[11px] text-muted-foreground">
                        {k.fingerprint || k.public_key.slice(0, 32) + '...'}
                      </div>
                    </div>
                    <div className="flex shrink-0 items-center gap-1.5">
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
                        disabled={deletingId === k.id}
                        onClick={() => handleDeleteKey(k.id)}
                        title="Delete key"
                      >
                        {deletingId === k.id ? (
                          <Loader2 className="h-3.5 w-3.5 animate-spin" />
                        ) : (
                          <Trash2 className="h-3.5 w-3.5" />
                        )}
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
