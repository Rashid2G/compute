import { useCallback, useEffect, useState } from 'react';
import {
  Check,
  Copy,
  KeyRound,
  Plus,
  RefreshCw,
  Shield,
  Terminal,
  Trash2,
  X,
} from 'lucide-react';
import { toast } from 'sonner';

import { api, errorMessage } from '@/lib/api';
import { formatRelativeTime } from '@/lib/format';
import type {
  AccessAPIKeyRecord,
  AgentProfileRecord,
  SSHKey,
  WorkerTokenRecord,
} from '@/lib/types';
import { cn } from '@/lib/utils';

type AccessTab = 'api-keys' | 'ssh-keys' | 'agents' | 'worker-tokens';

export function AccessPage() {
  const [tab, setTab] = useState<AccessTab>('api-keys');

  const [apiKeys, setApiKeys] = useState<AccessAPIKeyRecord[]>([]);
  const [sshKeys, setSshKeys] = useState<SSHKey[]>([]);
  const [agentProfiles, setAgentProfiles] = useState<AgentProfileRecord[]>([]);
  const [workerTokens, setWorkerTokens] = useState<WorkerTokenRecord[]>([]);
  const [loading, setLoading] = useState(false);

  // Create API Key state
  const [createKeyOpen, setCreateKeyOpen] = useState(false);
  const [newKeyName, setNewKeyName] = useState('');
  const [newKeyScope, setNewKeyScope] = useState('full-access');
  const [newKeyExpiry, setNewKeyExpiry] = useState('90 days');
  const [createdPlainToken, setCreatedPlainToken] = useState<string | null>(
    null,
  );
  const [copiedText, setCopiedText] = useState<string | null>(null);

  // Add SSH Key state
  const [addSshOpen, setAddSshOpen] = useState(false);
  const [sshName, setSshName] = useState('');
  const [sshPub, setSshPub] = useState('');

  // Add Agent Profile state
  const [addAgentOpen, setAddAgentOpen] = useState(false);
  const [agentName, setAgentName] = useState('');
  const [agentProvider, setAgentProvider] = useState('Anthropic Claude');
  const [agentUpstream, setAgentUpstream] = useState(
    'https://api.anthropic.com/v1',
  );
  const [agentHeader, setAgentHeader] = useState('x-api-key');
  const [agentSecretRef, setAgentSecretRef] = useState('ANTHROPIC_API_KEY');

  // Create Worker Token state
  const [tokenPool, setTokenPool] = useState('default');
  const [createdWorkerToken, setCreatedWorkerToken] = useState<string | null>(
    null,
  );

  const loadAll = useCallback(async () => {
    setLoading(true);
    try {
      const [akRes, skRes, apRes, wtRes] = await Promise.allSettled([
        api.listAPIKeys(),
        api.listKeys(),
        api.listAgentProfiles(),
        api.listWorkerTokens(),
      ]);
      if (akRes.status === 'fulfilled') setApiKeys(akRes.value || []);
      if (skRes.status === 'fulfilled') setSshKeys(skRes.value || []);
      if (apRes.status === 'fulfilled') setAgentProfiles(apRes.value || []);
      if (wtRes.status === 'fulfilled') setWorkerTokens(wtRes.value || []);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void loadAll();
  }, [loadAll]);

  function copy(text: string) {
    void navigator.clipboard.writeText(text);
    setCopiedText(text);
    toast.success('Copied to clipboard');
    setTimeout(() => setCopiedText(null), 1500);
  }

  function handleCreateAPIKey(e: React.FormEvent) {
    e.preventDefault();
    const keyName = newKeyName.trim();
    if (!keyName) return;
    const payload = {
      name: keyName,
      scope: newKeyScope,
      expires_at: newKeyExpiry,
    };
    setNewKeyName('');
    setCreateKeyOpen(false);

    const toastID = toast.loading(`Creating API key "${keyName}"…`);
    void api
      .createAPIKey(payload)
      .then(async (created) => {
        if (created.token) setCreatedPlainToken(created.token);
        toast.success(`Created API key "${created.name}"`, { id: toastID });
        await loadAll();
      })
      .catch((err) => {
        toast.error(errorMessage(err), { id: toastID });
      });
  }

  function handleCreateSSHKey(e: React.FormEvent) {
    e.preventDefault();
    const keyName = sshName.trim();
    const publicKey = sshPub.trim();
    if (!keyName || !publicKey) return;
    const payload = {
      name: keyName,
      public_key: publicKey,
    };
    setSshName('');
    setSshPub('');
    setAddSshOpen(false);

    const toastID = toast.loading(`Adding SSH key "${keyName}"…`);
    void api
      .createKey(payload)
      .then(async (created) => {
        toast.success(`Added SSH key "${created.name}"`, { id: toastID });
        await loadAll();
      })
      .catch((err) => {
        toast.error(errorMessage(err), { id: toastID });
      });
  }

  function handleCreateAgentProfile(e: React.FormEvent) {
    e.preventDefault();
    const profileName = agentName.trim();
    if (!profileName) return;
    const payload = {
      name: profileName,
      provider: agentProvider,
      upstream_url: agentUpstream,
      inject_header: agentHeader,
      secret_ref: agentSecretRef,
    };
    setAgentName('');
    setAddAgentOpen(false);

    const toastID = toast.loading(
      `Creating agent proxy profile "${profileName}"…`,
    );
    void api
      .createAgentProfile(payload)
      .then(async () => {
        toast.success('Created agent credential proxy profile', {
          id: toastID,
        });
        await loadAll();
      })
      .catch((err) => {
        toast.error(errorMessage(err), { id: toastID });
      });
  }

  async function handleCreateWorkerToken() {
    try {
      const created = await api.createWorkerToken({
        name: 'cluster-enroll',
        pool_id: tokenPool,
      });
      setCreatedWorkerToken(created.token);
      toast.success('Generated worker bootstrap token');
      await loadAll();
    } catch (err) {
      toast.error(errorMessage(err));
    }
  }

  return (
    <div className="space-y-5">
      {/* Header */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="font-display text-[22px] font-medium tracking-tight text-foreground">
            API keys
          </h1>
          <p className="text-[12px] text-muted-foreground">
            Scoped programmatic API keys, guest SSH public keys, zero-trust
            agent proxy profiles, and worker tokens.
          </p>
        </div>

        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={() => void loadAll()}
            className="flex h-8 items-center gap-1.5 border border-border bg-background px-3 text-[12px] text-muted-foreground hover:text-foreground"
          >
            <RefreshCw className="h-3.5 w-3.5" />
            <span>Refresh</span>
          </button>
          {tab === 'api-keys' && (
            <button
              type="button"
              onClick={() => setCreateKeyOpen((v) => !v)}
              className="flex h-8 items-center gap-1.5 bg-[#263fa9] px-3 text-[12px] font-medium text-white hover:bg-[#1e3180]"
            >
              <Plus className="h-3.5 w-3.5" />
              <span>Create API Key</span>
            </button>
          )}
          {tab === 'ssh-keys' && (
            <button
              type="button"
              onClick={() => setAddSshOpen((v) => !v)}
              className="flex h-8 items-center gap-1.5 bg-[#263fa9] px-3 text-[12px] font-medium text-white hover:bg-[#1e3180]"
            >
              <Plus className="h-3.5 w-3.5" />
              <span>Add SSH Key</span>
            </button>
          )}
          {tab === 'agents' && (
            <button
              type="button"
              onClick={() => setAddAgentOpen((v) => !v)}
              className="flex h-8 items-center gap-1.5 bg-[#263fa9] px-3 text-[12px] font-medium text-white hover:bg-[#1e3180]"
            >
              <Plus className="h-3.5 w-3.5" />
              <span>New Proxy Profile</span>
            </button>
          )}
          {tab === 'worker-tokens' && (
            <button
              type="button"
              onClick={() => void handleCreateWorkerToken()}
              className="flex h-8 items-center gap-1.5 bg-[#263fa9] px-3 text-[12px] font-medium text-white hover:bg-[#1e3180]"
            >
              <Plus className="h-3.5 w-3.5" />
              <span>Generate Worker Token</span>
            </button>
          )}
        </div>
      </div>

      {/* Tabs */}
      <div className="flex items-center border-b border-border">
        <button
          type="button"
          onClick={() => setTab('api-keys')}
          className={cn(
            'border-b-2 px-4 py-2 text-[12.5px] transition-colors',
            tab === 'api-keys'
              ? 'border-[#263fa9] text-foreground font-medium'
              : 'border-transparent text-muted-foreground hover:text-foreground',
          )}
        >
          API Keys ({apiKeys.length})
        </button>
        <button
          type="button"
          onClick={() => setTab('ssh-keys')}
          className={cn(
            'border-b-2 px-4 py-2 text-[12.5px] transition-colors',
            tab === 'ssh-keys'
              ? 'border-[#263fa9] text-foreground font-medium'
              : 'border-transparent text-muted-foreground hover:text-foreground',
          )}
        >
          SSH Public Keys ({sshKeys.length})
        </button>
        <button
          type="button"
          onClick={() => setTab('agents')}
          className={cn(
            'border-b-2 px-4 py-2 text-[12.5px] transition-colors',
            tab === 'agents'
              ? 'border-[#263fa9] text-foreground font-medium'
              : 'border-transparent text-muted-foreground hover:text-foreground',
          )}
        >
          Agents &amp; Proxying ({agentProfiles.length})
        </button>
        <button
          type="button"
          onClick={() => setTab('worker-tokens')}
          className={cn(
            'border-b-2 px-4 py-2 text-[12.5px] transition-colors',
            tab === 'worker-tokens'
              ? 'border-[#263fa9] text-foreground font-medium'
              : 'border-transparent text-muted-foreground hover:text-foreground',
          )}
        >
          Worker Tokens ({workerTokens.length})
        </button>
      </div>

      {/* TAB 1: API Keys */}
      {tab === 'api-keys' && (
        <div className="space-y-4">
          {createdPlainToken && (
            <div className="border border-[#263fa9] bg-[#0f1634]/50 p-3.5 space-y-2">
              <div className="flex items-center justify-between text-[12px]">
                <span className="font-medium text-white">
                  Copy your new secret API key now (shown once):
                </span>
                <button
                  type="button"
                  onClick={() => setCreatedPlainToken(null)}
                  className="text-muted-foreground hover:text-white"
                >
                  <X className="h-4 w-4" />
                </button>
              </div>
              <div className="flex items-center justify-between border border-border bg-[#060606] px-3 py-2 font-mono text-[12px] text-white">
                <span>{createdPlainToken}</span>
                <button
                  type="button"
                  onClick={() => copy(createdPlainToken)}
                  className="inline-flex items-center gap-1 text-xs text-[#929fd3] hover:text-white"
                >
                  {copiedText === createdPlainToken ? (
                    <Check className="h-3.5 w-3.5 text-success" />
                  ) : (
                    <Copy className="h-3.5 w-3.5" />
                  )}
                  <span>Copy</span>
                </button>
              </div>
            </div>
          )}

          {createKeyOpen && (
            <form
              onSubmit={handleCreateAPIKey}
              className="border border-border bg-muted/15 p-4 space-y-3"
            >
              <div className="text-[13px] font-medium text-foreground">
                Create Scoped API Key
              </div>
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
                <div>
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    Key Name
                  </label>
                  <input
                    type="text"
                    aria-label="API key name"
                    value={newKeyName}
                    onChange={(e) => setNewKeyName(e.target.value)}
                    placeholder="ci-github-actions-runner"
                    required
                    className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px] text-foreground"
                  />
                </div>
                <div>
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    Permissions Scope
                  </label>
                  <select
                    aria-label="API key scope"
                    value={newKeyScope}
                    onChange={(e) => setNewKeyScope(e.target.value)}
                    className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px] text-foreground"
                  >
                    <option value="full-access">full-access</option>
                    <option value="sandboxes:write">sandboxes:write</option>
                    <option value="ci-runner">ci-runner</option>
                    <option value="read-only">read-only</option>
                  </select>
                </div>
                <div>
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    Expiration
                  </label>
                  <select
                    aria-label="API key expiration"
                    value={newKeyExpiry}
                    onChange={(e) => setNewKeyExpiry(e.target.value)}
                    className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px] text-foreground"
                  >
                    <option value="30 days">30 days</option>
                    <option value="90 days">90 days</option>
                    <option value="Never">Never</option>
                  </select>
                </div>
              </div>
              <div className="flex justify-end gap-2">
                <button
                  type="button"
                  onClick={() => setCreateKeyOpen(false)}
                  className="h-8 border border-border px-3 text-[12px] text-muted-foreground"
                >
                  Cancel
                </button>
                <button
                  type="submit"
                  className="h-8 bg-[#263fa9] px-3 text-[12px] font-medium text-white"
                >
                  Generate Key
                </button>
              </div>
            </form>
          )}

          <div className="border border-border bg-background overflow-x-auto">
            <table className="w-full border-collapse text-left text-[12px]">
              <thead>
                <tr className="border-b border-border bg-muted/30 text-[11.5px] text-muted-foreground">
                  <th className="py-2.5 px-3 font-normal">Name</th>
                  <th className="py-2.5 px-3 font-normal">Prefix</th>
                  <th className="py-2.5 px-3 font-normal">Scope</th>
                  <th className="py-2.5 px-3 font-normal">Expires</th>
                  <th className="py-2.5 px-3 font-normal">Created</th>
                  <th className="py-2.5 px-3 text-right font-normal">
                    Actions
                  </th>
                </tr>
              </thead>
              {apiKeys.length === 0 ? (
                <tbody>
                  <tr>
                    <td
                      colSpan={6}
                      className="py-16 text-center text-muted-foreground"
                    >
                      <KeyRound className="mx-auto h-7 w-7 text-muted-foreground/50 mb-2" />
                      <div className="text-[12.5px]">
                        {loading
                          ? 'Loading API keys…'
                          : 'No API keys created yet.'}
                      </div>
                      <p className="text-[11.5px] text-muted-foreground/80 mt-1">
                        Click &ldquo;Create API Key&rdquo; to authenticate the{' '}
                        <code className="font-mono text-foreground">compute</code>{' '}
                        CLI or REST API clients.
                      </p>
                    </td>
                  </tr>
                </tbody>
              ) : (
                <tbody className="divide-y divide-border font-mono text-[11.5px]">
                  {apiKeys.map((k) => (
                    <tr key={k.id} className="hover:bg-muted/30">
                      <td className="py-2.5 px-3 font-sans font-medium text-foreground">
                        {k.name}
                      </td>
                      <td className="py-2.5 px-3 text-muted-foreground">
                        {k.prefix}••••••
                      </td>
                      <td className="py-2.5 px-3">
                        <span className="border border-border bg-muted/30 px-1.5 py-0.5 text-[10.5px]">
                          {k.scope}
                        </span>
                      </td>
                      <td className="py-2.5 px-3 text-muted-foreground">
                        {k.expires_at || 'Never'}
                      </td>
                      <td className="py-2.5 px-3 text-muted-foreground">
                        {formatRelativeTime(k.created_at)}
                      </td>
                      <td className="py-2.5 px-3 text-right">
                        <button
                          type="button"
                          onClick={() => {
                            void api
                              .deleteAPIKey(k.id)
                              .then(() => {
                                toast.success(`Revoked API key ${k.name}`);
                                void loadAll();
                              })
                              .catch((err) => toast.error(errorMessage(err)));
                          }}
                          className="inline-flex items-center gap-1 border border-border px-2 py-0.5 text-[11px] text-destructive hover:border-destructive"
                        >
                          <Trash2 className="h-3 w-3" />
                          <span>Revoke</span>
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              )}
            </table>
          </div>
        </div>
      )}

      {/* TAB 2: SSH Public Keys */}
      {tab === 'ssh-keys' && (
        <div className="space-y-4">
          {addSshOpen && (
            <form
              onSubmit={handleCreateSSHKey}
              className="border border-border bg-muted/15 p-4 space-y-3"
            >
              <div className="text-[13px] font-medium text-foreground">
                Register SSH Public Key (/api/v1/keys)
              </div>
              <div className="space-y-2">
                <input
                  type="text"
                  value={sshName}
                  onChange={(e) => setSshName(e.target.value)}
                  placeholder="Key Name (e.g. work-laptop-ed25519)"
                  required
                  className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px] text-foreground"
                />
                <textarea
                  rows={3}
                  value={sshPub}
                  onChange={(e) => setSshPub(e.target.value)}
                  placeholder="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI... user@host"
                  required
                  className="w-full border border-border bg-background p-2.5 font-mono text-[11.5px] text-foreground"
                />
              </div>
              <div className="flex justify-end gap-2">
                <button
                  type="button"
                  onClick={() => setAddSshOpen(false)}
                  className="h-8 border border-border px-3 text-[12px] text-muted-foreground"
                >
                  Cancel
                </button>
                <button
                  type="submit"
                  className="h-8 bg-[#263fa9] px-3 text-[12px] font-medium text-white"
                >
                  Save SSH Key
                </button>
              </div>
            </form>
          )}

          <div className="border border-border bg-background overflow-x-auto">
            <table className="w-full border-collapse text-left text-[12px]">
              <thead>
                <tr className="border-b border-border bg-muted/30 text-[11.5px] text-muted-foreground">
                  <th className="py-2.5 px-3 font-normal">Key Name</th>
                  <th className="py-2.5 px-3 font-normal">
                    SHA256 Fingerprint
                  </th>
                  <th className="py-2.5 px-3 font-normal">Added</th>
                  <th className="py-2.5 px-3 text-right font-normal">
                    Actions
                  </th>
                </tr>
              </thead>
              {sshKeys.length === 0 ? (
                <tbody>
                  <tr>
                    <td
                      colSpan={4}
                      className="py-16 text-center text-muted-foreground"
                    >
                      <Terminal className="mx-auto h-7 w-7 text-muted-foreground/50 mb-2" />
                      <div className="text-[12.5px]">
                        No SSH public keys registered yet.
                      </div>
                    </td>
                  </tr>
                </tbody>
              ) : (
                <tbody className="divide-y divide-border font-mono text-[11.5px]">
                  {sshKeys.map((sk) => (
                    <tr key={sk.id} className="hover:bg-muted/30">
                      <td className="py-2.5 px-3 font-sans font-medium text-foreground">
                        {sk.name}
                      </td>
                      <td className="py-2.5 px-3 text-muted-foreground">
                        {sk.fingerprint}
                      </td>
                      <td className="py-2.5 px-3 text-muted-foreground">
                        {formatRelativeTime(sk.created_at)}
                      </td>
                      <td className="py-2.5 px-3 text-right">
                        <button
                          type="button"
                          onClick={() => {
                            void api
                              .deleteKey(sk.id)
                              .then(() => {
                                toast.success(`Deleted SSH key ${sk.name}`);
                                void loadAll();
                              })
                              .catch((err) => toast.error(errorMessage(err)));
                          }}
                          className="border border-border px-2 py-0.5 text-[11px] text-destructive hover:border-destructive"
                        >
                          Delete
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              )}
            </table>
          </div>
        </div>
      )}

      {/* TAB 3: Agents & Proxying */}
      {tab === 'agents' && (
        <div className="space-y-4">
          {addAgentOpen && (
            <form
              onSubmit={handleCreateAgentProfile}
              className="border border-border bg-muted/15 p-4 space-y-3"
            >
              <div className="text-[13px] font-medium text-foreground">
                Create Zero-Trust Agent Credential Proxy Profile
              </div>
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
                <div>
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    Profile Name
                  </label>
                  <input
                    type="text"
                    value={agentName}
                    onChange={(e) => setAgentName(e.target.value)}
                    placeholder="claude-code-proxy"
                    required
                    className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px] text-foreground"
                  />
                </div>
                <div>
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    Provider
                  </label>
                  <input
                    type="text"
                    value={agentProvider}
                    onChange={(e) => setAgentProvider(e.target.value)}
                    className="h-8 w-full border border-border bg-background px-2.5 text-[12px] text-foreground"
                  />
                </div>
                <div>
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    Upstream URL
                  </label>
                  <input
                    type="text"
                    value={agentUpstream}
                    onChange={(e) => setAgentUpstream(e.target.value)}
                    className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px] text-foreground"
                  />
                </div>
              </div>
              <div className="flex justify-end gap-2">
                <button
                  type="button"
                  onClick={() => setAddAgentOpen(false)}
                  className="h-8 border border-border px-3 text-[12px] text-muted-foreground"
                >
                  Cancel
                </button>
                <button
                  type="submit"
                  className="h-8 bg-[#263fa9] px-3 text-[12px] font-medium text-white"
                >
                  Save Profile
                </button>
              </div>
            </form>
          )}

          <div className="border border-border bg-background overflow-x-auto">
            <table className="w-full border-collapse text-left text-[12px]">
              <thead>
                <tr className="border-b border-border bg-muted/30 text-[11.5px] text-muted-foreground">
                  <th className="py-2.5 px-3 font-normal">Profile</th>
                  <th className="py-2.5 px-3 font-normal">Provider</th>
                  <th className="py-2.5 px-3 font-normal">Upstream Target</th>
                  <th className="py-2.5 px-3 font-normal">Injected Header</th>
                  <th className="py-2.5 px-3 text-right font-normal">
                    Actions
                  </th>
                </tr>
              </thead>
              {agentProfiles.length === 0 ? (
                <tbody>
                  <tr>
                    <td
                      colSpan={5}
                      className="py-16 text-center text-muted-foreground"
                    >
                      <Shield className="mx-auto h-7 w-7 text-muted-foreground/50 mb-2" />
                      <div className="text-[12.5px]">
                        No agent credential proxy profiles created yet.
                      </div>
                    </td>
                  </tr>
                </tbody>
              ) : (
                <tbody className="divide-y divide-border font-mono text-[11.5px]">
                  {agentProfiles.map((ap) => (
                    <tr key={ap.id} className="hover:bg-muted/30">
                      <td className="py-2.5 px-3 font-sans font-medium text-foreground">
                        {ap.name}
                      </td>
                      <td className="py-2.5 px-3 text-muted-foreground">
                        {ap.provider}
                      </td>
                      <td className="py-2.5 px-3 text-foreground">
                        {ap.upstream_url}
                      </td>
                      <td className="py-2.5 px-3 text-muted-foreground">
                        {ap.inject_header} ({ap.secret_ref})
                      </td>
                      <td className="py-2.5 px-3 text-right">
                        <button
                          type="button"
                          onClick={() => {
                            void api
                              .deleteAgentProfile(ap.id)
                              .then(() => {
                                toast.success('Deleted proxy profile');
                                void loadAll();
                              })
                              .catch((err) => toast.error(errorMessage(err)));
                          }}
                          className="border border-border px-2 py-0.5 text-[11px] text-destructive hover:border-destructive"
                        >
                          Delete
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              )}
            </table>
          </div>
        </div>
      )}

      {/* TAB 4: Worker Enrollment Tokens */}
      {tab === 'worker-tokens' && (
        <div className="space-y-4">
          <div className="flex items-center gap-3">
            <span className="text-[12px] text-muted-foreground">
              Target Pool:
            </span>
            <input
              type="text"
              value={tokenPool}
              onChange={(e) => setTokenPool(e.target.value)}
              className="h-8 w-44 border border-border bg-background px-2.5 font-mono text-[12px] text-foreground"
            />
          </div>

          {createdWorkerToken && (
            <div className="border border-[#263fa9] bg-[#0f1634]/50 p-3 font-mono text-[12px] text-white flex items-center justify-between">
              <span>{createdWorkerToken}</span>
              <button
                type="button"
                onClick={() => copy(createdWorkerToken)}
                className="text-xs underline"
              >
                Copy Token
              </button>
            </div>
          )}

          <div className="border border-border bg-background overflow-x-auto">
            <table className="w-full border-collapse text-left text-[12px]">
              <thead>
                <tr className="border-b border-border bg-muted/30 text-[11.5px] text-muted-foreground">
                  <th className="py-2.5 px-3 font-normal">Token Prefix</th>
                  <th className="py-2.5 px-3 font-normal">Pool</th>
                  <th className="py-2.5 px-3 font-normal">Status</th>
                  <th className="py-2.5 px-3 text-right font-normal">
                    Actions
                  </th>
                </tr>
              </thead>
              {workerTokens.length === 0 ? (
                <tbody>
                  <tr>
                    <td
                      colSpan={4}
                      className="py-12 text-center text-muted-foreground"
                    >
                      No active worker enrollment tokens.
                    </td>
                  </tr>
                </tbody>
              ) : (
                <tbody className="divide-y divide-border font-mono text-[11.5px]">
                  {workerTokens.map((wt) => (
                    <tr key={wt.token} className="hover:bg-muted/30">
                      <td className="py-2.5 px-3 text-foreground">
                        {wt.prefix || wt.token.slice(0, 12)}…
                      </td>
                      <td className="py-2.5 px-3 text-muted-foreground">
                        {wt.pool_id}
                      </td>
                      <td className="py-2.5 px-3 text-success">
                        {wt.used_at ? 'consumed' : 'ready'}
                      </td>
                      <td className="py-2.5 px-3 text-right">
                        <button
                          type="button"
                          onClick={() => {
                            void api
                              .deleteWorkerToken(wt.token)
                              .then(() => {
                                toast.success('Revoked worker token');
                                void loadAll();
                              })
                              .catch((err) => toast.error(errorMessage(err)));
                          }}
                          className="border border-border px-2 py-0.5 text-[11px] text-destructive"
                        >
                          Revoke
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              )}
            </table>
          </div>
        </div>
      )}
    </div>
  );
}
