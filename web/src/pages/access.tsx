import { useCallback, useEffect, useState } from 'react';
import {
  Check,
  Copy,
  KeyRound,
  Plus,
  Shield,
  Terminal,
  Trash2,
  Users,
  X,
} from 'lucide-react';
import { toast } from 'sonner';

import { api, errorMessage } from '@/lib/api';
import { formatRelativeTime } from '@/lib/format';
import type {
  AccessAPIKeyRecord,
  AuthStatusResponse,
  AuthUser,
  SSHKey,
  WorkerTokenRecord,
} from '@/lib/types';
import { cn } from '@/lib/utils';

type AccessTab =
  | 'api-keys'
  | 'ssh-keys'
  | 'worker-tokens'
  | 'users';

interface AccessPageProps {
  currentUser?: AuthUser | null;
  workerTokens?: WorkerTokenRecord[];
  embedded?: boolean;
}

export function AccessPage({
  currentUser: currentUserProp,
  workerTokens: workerTokensProp,
  embedded = false,
}: AccessPageProps = {}) {
  const [tab, setTab] = useState<AccessTab>('api-keys');

  const [apiKeys, setApiKeys] = useState<AccessAPIKeyRecord[]>([]);
  const [sshKeys, setSshKeys] = useState<SSHKey[]>([]);
  const [workerTokens, setWorkerTokens] = useState<WorkerTokenRecord[]>(
    () => workerTokensProp ?? [],
  );
  const [users, setUsers] = useState<AuthUser[]>([]);
  const [authStatus, setAuthStatus] = useState<AuthStatusResponse | null>(null);
  const [meUser, setMeUser] = useState<AuthUser | null>(
    () => currentUserProp ?? api.getAuthUser(),
  );
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (currentUserProp !== undefined) {
      setMeUser(currentUserProp);
    }
  }, [currentUserProp]);

  useEffect(() => {
    if (workerTokensProp !== undefined) {
      setWorkerTokens(workerTokensProp);
    }
  }, [workerTokensProp]);

  const effectiveUser = meUser ?? currentUserProp ?? api.getAuthUser();
  const isAdmin = Boolean(
    (authStatus !== null && authStatus.auth_enabled === false) ||
      effectiveUser?.role === 'admin',
  );

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

  // Create Worker Token state
  const [tokenPool, setTokenPool] = useState('default');
  const [createdWorkerToken, setCreatedWorkerToken] = useState<string | null>(
    null,
  );

  // Create User state
  const [createUserOpen, setCreateUserOpen] = useState(false);
  const [userUsername, setUserUsername] = useState('');
  const [userEmail, setUserEmail] = useState('');
  const [userDisplayName, setUserDisplayName] = useState('');
  const [userPassword, setUserPassword] = useState('');
  const [userRole, setUserRole] = useState<'user' | 'admin'>('user');

  const loadAll = useCallback(async (forceWorkerTokens = false) => {
    setLoading(true);
    try {
      const fetchWorkerTokens =
        forceWorkerTokens || workerTokensProp === undefined;
      const [akRes, skRes, wtRes, authRes] = await Promise.allSettled([
        api.listAPIKeys(),
        api.listKeys(),
        fetchWorkerTokens ? api.listWorkerTokens() : Promise.resolve(null),
        api.authStatus(),
      ]);
      if (akRes.status === 'fulfilled') setApiKeys(akRes.value || []);
      if (skRes.status === 'fulfilled') setSshKeys(skRes.value || []);
      if (wtRes.status === 'fulfilled' && wtRes.value !== null) {
        setWorkerTokens(wtRes.value || []);
      }

      const statusVal = authRes.status === 'fulfilled' ? authRes.value : null;
      if (statusVal) {
        setAuthStatus(statusVal);
      }

      const authEnabled = statusVal ? statusVal.auth_enabled : true;
      let resolvedUser = currentUserProp ?? api.getAuthUser();
      if (authEnabled) {
        try {
          const meRes = await api.me();
          if (meRes?.user) {
            resolvedUser = meRes.user;
            setMeUser(meRes.user);
          }
        } catch {
          // Keep fallback stored user if /auth/me fails
        }
      }

      if (!authEnabled || resolvedUser?.role === 'admin') {
        try {
          const usersVal = await api.listUsers();
          setUsers(usersVal || []);
        } catch {
          // Non-admin or unavailable
        }
      } else {
        setUsers([]);
      }
    } finally {
      setLoading(false);
    }
  }, [currentUserProp, workerTokensProp]);

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

  async function handleCreateWorkerToken() {
    try {
      const created = await api.createWorkerToken({
        name: 'cluster-enroll',
        pool_id: tokenPool,
      });
      setCreatedWorkerToken(created.token);
      toast.success('Generated worker bootstrap token');
      await loadAll(true);
    } catch (err) {
      toast.error(errorMessage(err));
    }
  }

  function handleCreateUser(e: React.FormEvent) {
    e.preventDefault();
    const username = userUsername.trim();
    const email = userEmail.trim();
    const password = userPassword;
    if (!username || !email || !password) return;

    const payload = {
      username,
      email,
      display_name: userDisplayName.trim() || undefined,
      password,
      role: userRole,
    };
    setUserUsername('');
    setUserEmail('');
    setUserDisplayName('');
    setUserPassword('');
    setUserRole('user');
    setCreateUserOpen(false);

    const toastID = toast.loading(`Creating user "${username}"…`);
    void api
      .createUser(payload)
      .then(async (res) => {
        toast.success(`Created user "${res.user?.username || username}"`, {
          id: toastID,
        });
        await loadAll();
      })
      .catch((err) => {
        toast.error(errorMessage(err), { id: toastID });
      });
  }

  return (
    <div className="space-y-5">
      {/* Header */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        {!embedded ? (
          <div>
            <h1 className="font-display text-[22px] font-medium tracking-tight text-foreground">
              Access &amp; Identity
            </h1>
            <p className="text-[12px] text-muted-foreground">
              Scoped programmatic API keys, guest SSH public keys, worker tokens,
              and user accounts.
            </p>
          </div>
        ) : (
          <div>
            <h2 className="text-[14px] font-medium text-foreground">
              Access, Keys &amp; User Accounts
            </h2>
            <p className="text-[12px] text-muted-foreground">
              Scoped programmatic API keys, guest SSH public keys, worker tokens,
              and user accounts.
            </p>
          </div>
        )}

        <div className="flex items-center gap-2">
          {tab === 'api-keys' && (
            <button
              type="button"
              onClick={() => setCreateKeyOpen((v) => !v)}
              className="flex h-8 items-center gap-1.5 bg-primary hover:bg-primary-hover px-3 text-[12px] font-medium text-primary-foreground"
            >
              <Plus className="h-3.5 w-3.5" />
              <span>Create API Key</span>
            </button>
          )}
          {tab === 'ssh-keys' && (
            <button
              type="button"
              onClick={() => setAddSshOpen((v) => !v)}
              className="flex h-8 items-center gap-1.5 bg-primary hover:bg-primary-hover px-3 text-[12px] font-medium text-primary-foreground"
            >
              <Plus className="h-3.5 w-3.5" />
              <span>Add SSH Key</span>
            </button>
          )}
          {tab === 'worker-tokens' && (
            <button
              type="button"
              onClick={() => void handleCreateWorkerToken()}
              className="flex h-8 items-center gap-1.5 bg-primary hover:bg-primary-hover px-3 text-[12px] font-medium text-primary-foreground"
            >
              <Plus className="h-3.5 w-3.5" />
              <span>Generate Worker Token</span>
            </button>
          )}
          {tab === 'users' && isAdmin && (
            <button
              type="button"
              onClick={() => setCreateUserOpen((v) => !v)}
              className="flex h-8 items-center gap-1.5 bg-primary hover:bg-primary-hover px-3 text-[12px] font-medium text-primary-foreground"
            >
              <Plus className="h-3.5 w-3.5" />
              <span>Create User</span>
            </button>
          )}
        </div>
      </div>

      {/* Tabs */}
      <div
        role="tablist"
        aria-label="Access & Identity sections"
        className="flex items-center overflow-x-auto border-b border-border"
      >
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'api-keys'}
          data-testid="access-tab-api-keys"
          onClick={() => setTab('api-keys')}
          className={cn(
            'border-b-2 px-4 py-2 text-[12.5px] transition-colors whitespace-nowrap',
            tab === 'api-keys'
              ? 'border-primary text-foreground font-medium'
              : 'border-transparent text-muted-foreground hover:text-foreground',
          )}
        >
          API Keys ({apiKeys.length})
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'ssh-keys'}
          data-testid="access-tab-ssh-keys"
          onClick={() => setTab('ssh-keys')}
          className={cn(
            'border-b-2 px-4 py-2 text-[12.5px] transition-colors whitespace-nowrap',
            tab === 'ssh-keys'
              ? 'border-primary text-foreground font-medium'
              : 'border-transparent text-muted-foreground hover:text-foreground',
          )}
        >
          SSH Public Keys ({sshKeys.length})
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'worker-tokens'}
          data-testid="access-tab-worker-tokens"
          onClick={() => setTab('worker-tokens')}
          className={cn(
            'border-b-2 px-4 py-2 text-[12.5px] transition-colors whitespace-nowrap',
            tab === 'worker-tokens'
              ? 'border-primary text-foreground font-medium'
              : 'border-transparent text-muted-foreground hover:text-foreground',
          )}
        >
          Worker Tokens ({workerTokens.length})
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'users'}
          data-testid="access-tab-users"
          onClick={() => setTab('users')}
          className={cn(
            'border-b-2 px-4 py-2 text-[12.5px] transition-colors whitespace-nowrap',
            tab === 'users'
              ? 'border-primary text-foreground font-medium'
              : 'border-transparent text-muted-foreground hover:text-foreground',
          )}
        >
          Users ({users.length})
        </button>
      </div>

      {/* TAB 1: API Keys */}
      {tab === 'api-keys' && (
        <div className="space-y-4">
          {createdPlainToken && (
            <div className="border border-primary bg-selected/60 p-3.5 space-y-2">
              <div className="flex items-center justify-between text-[12px]">
                <span className="font-medium text-foreground">
                  Copy your new secret API key now (shown once):
                </span>
                <button
                  type="button"
                  onClick={() => setCreatedPlainToken(null)}
                  className="text-muted-foreground hover:text-foreground"
                >
                  <X className="h-4 w-4" />
                </button>
              </div>
              <div className="flex items-center justify-between border border-border bg-[#0a0a0b] px-3 py-2 font-mono text-[12px] text-foreground">
                <span>{createdPlainToken}</span>
                <button
                  type="button"
                  onClick={() => copy(createdPlainToken)}
                  className="inline-flex items-center gap-1 text-xs text-primary dark:text-[var(--accent-bright)] hover:underline"
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
                  className="h-8 bg-primary hover:bg-primary-hover px-3 text-[12px] font-medium text-primary-foreground"
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
                  className="h-8 bg-primary hover:bg-primary-hover px-3 text-[12px] font-medium text-primary-foreground"
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

      {/* TAB 3: Worker Enrollment Tokens */}
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
            <div className="border border-primary bg-selected/60 p-3 font-mono text-[12px] text-foreground flex items-center justify-between">
              <span>{createdWorkerToken}</span>
              <button
                type="button"
                onClick={() => copy(createdWorkerToken)}
                className="text-xs text-primary dark:text-[var(--accent-bright)] underline"
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
                    <tr key={wt.id || wt.token} className="hover:bg-muted/30">
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
                              .deleteWorkerToken(wt.id || wt.token)
                              .then(() => {
                                toast.success('Revoked worker token');
                                void loadAll(true);
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

      {/* TAB 5: Users */}
      {tab === 'users' && (
        <div className="space-y-4">
          {!isAdmin ? (
            <div className="border border-border bg-muted/20 p-6 space-y-2">
              <div className="flex items-center gap-2 text-[13px] font-medium text-foreground">
                <Shield className="h-4 w-4 text-primary" />
                <span>Admin privileges required to manage users</span>
              </div>
              <p className="text-[12px] text-muted-foreground">
                Your current account ({effectiveUser?.username || 'user'}) does
                not have administrator permissions to list, provision, or
                delete user accounts.
              </p>
            </div>
          ) : (
            <>
              {/* Public Signup Policy Status Banner */}
              <div className="flex flex-wrap items-center justify-between gap-2 border border-border bg-muted/20 px-3.5 py-2.5 text-[12px]">
                <div className="flex items-center gap-2">
                  <Users className="h-3.5 w-3.5 text-primary" />
                  <span className="font-medium text-foreground">
                    Public Signup:
                  </span>
                  <span
                    className={cn(
                      'border px-1.5 py-0.5 font-mono text-[10.5px] uppercase',
                      authStatus?.signup_enabled
                        ? 'border-success/40 bg-success/10 text-success'
                        : 'border-border bg-background text-muted-foreground',
                    )}
                  >
                    {authStatus?.signup_enabled
                      ? 'Enabled'
                      : 'Disabled (Admin-Only Provisioning)'}
                  </span>
                </div>
                <span className="font-mono text-[11px] text-muted-foreground">
                  Controlled by COMPUTE_SIGNUP_ENABLED
                </span>
              </div>

              {createUserOpen && (
                <form
                  onSubmit={handleCreateUser}
                  className="border border-border bg-muted/15 p-4 space-y-3"
                >
                  <div className="text-[13px] font-medium text-foreground">
                    Provision User Account
                  </div>
                  <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-5">
                    <div>
                      <label className="block text-[11px] text-muted-foreground mb-1">
                        Username
                      </label>
                      <input
                        type="text"
                        aria-label="New username"
                        value={userUsername}
                        onChange={(e) => setUserUsername(e.target.value)}
                        placeholder="alice"
                        required
                        className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px] text-foreground"
                      />
                    </div>
                    <div>
                      <label className="block text-[11px] text-muted-foreground mb-1">
                        Email
                      </label>
                      <input
                        type="email"
                        aria-label="New email"
                        value={userEmail}
                        onChange={(e) => setUserEmail(e.target.value)}
                        placeholder="alice@example.local"
                        required
                        className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px] text-foreground"
                      />
                    </div>
                    <div>
                      <label className="block text-[11px] text-muted-foreground mb-1">
                        Display Name (optional)
                      </label>
                      <input
                        type="text"
                        aria-label="New display name"
                        value={userDisplayName}
                        onChange={(e) => setUserDisplayName(e.target.value)}
                        placeholder="Alice Dev"
                        className="h-8 w-full border border-border bg-background px-2.5 text-[12px] text-foreground"
                      />
                    </div>
                    <div>
                      <label className="block text-[11px] text-muted-foreground mb-1">
                        Password
                      </label>
                      <input
                        type="password"
                        aria-label="New password"
                        value={userPassword}
                        onChange={(e) => setUserPassword(e.target.value)}
                        placeholder="••••••••"
                        required
                        minLength={6}
                        className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px] text-foreground"
                      />
                    </div>
                    <div>
                      <label className="block text-[11px] text-muted-foreground mb-1">
                        Role
                      </label>
                      <select
                        aria-label="New user role"
                        value={userRole}
                        onChange={(e) =>
                          setUserRole(
                            e.target.value === 'admin' ? 'admin' : 'user',
                          )
                        }
                        className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px] text-foreground"
                      >
                        <option value="user">user</option>
                        <option value="admin">admin</option>
                      </select>
                    </div>
                  </div>
                  <div className="flex justify-end gap-2">
                    <button
                      type="button"
                      onClick={() => setCreateUserOpen(false)}
                      className="h-8 border border-border px-3 text-[12px] text-muted-foreground"
                    >
                      Cancel
                    </button>
                    <button
                      type="submit"
                      className="h-8 bg-primary px-3 text-[12px] font-medium text-primary-foreground hover:bg-primary-hover"
                    >
                      Provision User
                    </button>
                  </div>
                </form>
              )}

              <div className="border border-border bg-background overflow-x-auto">
                <table className="w-full border-collapse text-left text-[12px]">
                  <thead>
                    <tr className="border-b border-border bg-muted/30 text-[11.5px] text-muted-foreground">
                      <th className="py-2.5 px-3 font-normal">Username</th>
                      <th className="py-2.5 px-3 font-normal">Email</th>
                      <th className="py-2.5 px-3 font-normal">Role</th>
                      <th className="py-2.5 px-3 font-normal">Status</th>
                      <th className="py-2.5 px-3 font-normal">Created</th>
                      <th className="py-2.5 px-3 text-right font-normal">
                        Actions
                      </th>
                    </tr>
                  </thead>
                  {users.length === 0 ? (
                    <tbody>
                      <tr>
                        <td
                          colSpan={6}
                          className="py-16 text-center text-muted-foreground"
                        >
                          <Users className="mx-auto h-7 w-7 text-muted-foreground/50 mb-2" />
                          <div className="text-[12.5px]">
                            {loading
                              ? 'Loading users…'
                              : 'No user accounts found.'}
                          </div>
                        </td>
                      </tr>
                    </tbody>
                  ) : (
                    <tbody className="divide-y divide-border font-mono text-[11.5px]">
                      {users.map((u) => {
                        const configuredAdminUsername =
                          authStatus?.admin_username?.trim().toLowerCase();
                        const isProtectedUser =
                          u.id === effectiveUser?.id ||
                          u.id === 'usr_system_admin' ||
                          u.id === 'usr_admin' ||
                          (configuredAdminUsername
                            ? u.username.toLowerCase() === configuredAdminUsername
                            : u.username.toLowerCase() === 'admin');
                        return (
                          <tr key={u.id} className="hover:bg-muted/30">
                            <td className="py-2.5 px-3 font-sans">
                              <div className="font-medium text-foreground">
                                {u.username}
                                {u.id === effectiveUser?.id && (
                                  <span className="ml-1.5 border border-primary/40 bg-primary/10 px-1 py-0.5 font-mono text-[9.5px] text-primary dark:text-[var(--accent-bright)] uppercase">
                                    you
                                  </span>
                                )}
                              </div>
                              {u.display_name &&
                                u.display_name !== u.username && (
                                  <div className="text-[11px] text-muted-foreground">
                                    {u.display_name}
                                  </div>
                                )}
                            </td>
                            <td className="py-2.5 px-3 text-muted-foreground">
                              {u.email}
                            </td>
                            <td className="py-2.5 px-3">
                              <span
                                className={cn(
                                  'border px-1.5 py-0.5 text-[10.5px] uppercase',
                                  u.role === 'admin'
                                    ? 'border-primary/40 bg-primary/10 text-primary dark:text-[var(--accent-bright)]'
                                    : 'border-border bg-muted/30 text-muted-foreground',
                                )}
                              >
                                {u.role}
                              </span>
                            </td>
                            <td className="py-2.5 px-3 text-success">
                              {u.status || 'active'}
                            </td>
                            <td className="py-2.5 px-3 text-muted-foreground">
                              {u.created_at
                                ? formatRelativeTime(
                                    new Date(
                                      u.created_at > 1e12
                                        ? u.created_at
                                        : u.created_at * 1000,
                                    ).toISOString(),
                                  )
                                : '—'}
                            </td>
                            <td className="py-2.5 px-3 text-right">
                              {isAdmin && !isProtectedUser ? (
                                <button
                                  type="button"
                                  aria-label={`Delete user ${u.username}`}
                                  onClick={() => {
                                    void api
                                      .deleteUser(u.id)
                                      .then(() => {
                                        toast.success(
                                          `Deleted user ${u.username}`,
                                        );
                                        void loadAll();
                                      })
                                      .catch((err) =>
                                        toast.error(errorMessage(err)),
                                      );
                                  }}
                                  className="inline-flex items-center gap-1 border border-border px-2 py-0.5 text-[11px] text-destructive hover:border-destructive"
                                >
                                  <Trash2 className="h-3 w-3" />
                                  <span>Delete</span>
                                </button>
                              ) : (
                                <span className="text-[10.5px] text-muted-foreground">
                                  Protected
                                </span>
                              )}
                            </td>
                          </tr>
                        );
                      })}
                    </tbody>
                  )}
                </table>
              </div>
            </>
          )}
        </div>
      )}
    </div>
  );
}
