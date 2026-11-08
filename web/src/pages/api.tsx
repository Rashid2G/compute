import { useCallback, useEffect, useState } from 'react';
import { Plus, RefreshCw, Shield, Trash2 } from 'lucide-react';
import { toast } from 'sonner';

import { api, errorMessage } from '@/lib/api';
import type { AgentProfileRecord, DaemonConfig, VMTier } from '@/lib/types';

interface ApiPageProps {
  config: DaemonConfig | null;
}

export function ApiPage({ config }: ApiPageProps) {
  const [profiles, setProfiles] = useState<AgentProfileRecord[]>([]);
  const [tiers, setTiers] = useState<VMTier[]>([]);
  const [addOpen, setAddOpen] = useState(false);
  const [name, setName] = useState('');
  const [provider, setProvider] = useState('Anthropic Claude');
  const [upstreamUrl, setUpstreamUrl] = useState(
    'https://api.anthropic.com/v1',
  );
  const [injectHeader, setInjectHeader] = useState('x-api-key');
  const [secretRef, setSecretRef] = useState('ANTHROPIC_API_KEY');

  const loadData = useCallback(async () => {
    const [pRes, tRes] = await Promise.allSettled([
      api.listAgentProfiles(),
      api.tiers(),
    ]);
    if (pRes.status === 'fulfilled') setProfiles(pRes.value || []);
    if (tRes.status === 'fulfilled') setTiers(tRes.value || []);
  }, []);

  useEffect(() => {
    void loadData();
  }, [loadData]);

  async function handleCreateProfile(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim()) return;
    try {
      await api.createAgentProfile({
        name: name.trim(),
        provider,
        upstream_url: upstreamUrl,
        inject_header: injectHeader,
        secret_ref: secretRef,
      });
      toast.success(`Created agent proxy profile "${name.trim()}"`);
      setName('');
      setAddOpen(false);
      await loadData();
    } catch (err) {
      toast.error(errorMessage(err));
    }
  }

  const endpoints = [
    {
      method: 'GET',
      path: '/api/v1/vms',
      desc: 'List all Firecracker microVM sandboxes across worker nodes',
    },
    {
      method: 'POST',
      path: '/api/v1/vms',
      desc: 'Provision a new sandbox from base image, template, or clone source',
    },
    {
      method: 'POST',
      path: '/api/v1/vms/:id/save-state',
      desc: 'Pause & snapshot guest memory and disk overlay',
    },
    {
      method: 'POST',
      path: '/api/v1/vms/:id/resume',
      desc: 'Resume a snapshotted microVM',
    },
    {
      method: 'POST',
      path: '/api/v1/vms/:id/exec',
      desc: 'Execute non-interactive command inside guest over SSH',
    },
    {
      method: 'GET',
      path: '/api/v1/workers',
      desc: 'List enrolled bare-metal KVM workers, heartbeats, and pool capacity',
    },
    {
      method: 'POST',
      path: '/api/v1/jobs',
      desc: 'Dispatch an ephemeral CI job on the compute scheduler',
    },
    {
      method: 'GET',
      path: '/api/v1/volumes',
      desc: 'List persistent block storage volumes and attachments',
    },
    {
      method: 'GET',
      path: '/api/v1/environments',
      desc: 'List sandbox environment profiles, secrets, and versions',
    },
  ];

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="font-display text-[22px] font-medium tracking-tight text-foreground">
            Agents &amp; Proxying
          </h1>
          <p className="text-[12px] text-muted-foreground">
            Zero-trust agent credential injection proxy, supported VM tiers, and
            live REST control-plane reference.
          </p>
        </div>

        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={() => void loadData()}
            className="flex h-8 items-center gap-1.5 border border-border bg-background px-3 text-[12px] text-muted-foreground hover:text-foreground"
          >
            <RefreshCw className="h-3.5 w-3.5" />
            <span>Refresh</span>
          </button>
          <button
            type="button"
            onClick={() => setAddOpen((v) => !v)}
            className="flex h-8 items-center gap-1.5 bg-[#263fa9] px-3 text-[12px] font-medium text-white hover:bg-[#1e3180]"
          >
            <Plus className="h-3.5 w-3.5" />
            <span>New Proxy Profile</span>
          </button>
        </div>
      </div>

      {addOpen && (
        <form
          onSubmit={handleCreateProfile}
          className="border border-border bg-muted/15 p-4 space-y-3"
        >
          <div className="text-[13px] font-medium text-foreground">
            Create Agent Credential Proxy Profile (/api/v1/agent-profiles)
          </div>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-4">
            <div>
              <label className="block text-[11px] text-muted-foreground mb-1">
                Profile Name
              </label>
              <input
                type="text"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="claude-code-default"
                required
                className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px] text-foreground"
              />
            </div>
            <div>
              <label className="block text-[11px] text-muted-foreground mb-1">
                Provider
              </label>
              <select
                aria-label="Agent provider"
                value={provider}
                onChange={(e) => setProvider(e.target.value)}
                className="h-8 w-full border border-border bg-background px-2.5 text-[12px] text-foreground"
              >
                <option value="Anthropic Claude">Anthropic Claude</option>
                <option value="OpenAI Codex">OpenAI Codex</option>
                <option value="Google Gemini">Google Gemini</option>
              </select>
            </div>
            <div>
              <label className="block text-[11px] text-muted-foreground mb-1">
                Upstream URL
              </label>
              <input
                type="text"
                value={upstreamUrl}
                onChange={(e) => setUpstreamUrl(e.target.value)}
                className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px] text-foreground"
              />
            </div>
            <div>
              <label className="block text-[11px] text-muted-foreground mb-1">
                Injected Header / Secret Ref
              </label>
              <div className="flex gap-1">
                <input
                  type="text"
                  value={injectHeader}
                  onChange={(e) => setInjectHeader(e.target.value)}
                  className="h-8 w-1/2 border border-border bg-background px-2 font-mono text-[11px] text-foreground"
                />
                <input
                  type="text"
                  value={secretRef}
                  onChange={(e) => setSecretRef(e.target.value)}
                  className="h-8 w-1/2 border border-border bg-background px-2 font-mono text-[11px] text-foreground"
                />
              </div>
            </div>
          </div>
          <div className="flex justify-end gap-2">
            <button
              type="button"
              onClick={() => setAddOpen(false)}
              className="h-8 border border-border px-3 text-[12px] text-muted-foreground"
            >
              Cancel
            </button>
            <button
              type="submit"
              className="h-8 bg-[#263fa9] px-3 text-[12px] font-medium text-white"
            >
              Save Proxy Profile
            </button>
          </div>
        </form>
      )}

      {/* Agent Credential Proxy Profiles Table */}
      <div className="border border-border bg-background overflow-x-auto">
        <div className="border-b border-border px-4 py-2.5 text-[12.5px] font-medium text-foreground">
          Zero-Trust Agent Credential Proxy Profiles ({profiles.length})
        </div>
        <table className="w-full border-collapse text-left text-[12px]">
          <thead>
            <tr className="border-b border-border bg-muted/30 text-[11.5px] text-muted-foreground">
              <th className="py-2 px-3 font-normal">Profile</th>
              <th className="py-2 px-3 font-normal">Provider</th>
              <th className="py-2 px-3 font-normal">Upstream Target</th>
              <th className="py-2 px-3 font-normal">Header Injection</th>
              <th className="py-2 px-3 font-normal">Status</th>
              <th className="py-2 px-3 text-right font-normal">Actions</th>
            </tr>
          </thead>
          {profiles.length === 0 ? (
            <tbody>
              <tr>
                <td
                  colSpan={6}
                  className="py-12 text-center text-muted-foreground"
                >
                  <Shield className="mx-auto h-6 w-6 text-muted-foreground/50 mb-1.5" />
                  <div className="text-[12px]">
                    No agent proxy profiles configured yet. Click &ldquo;New
                    Proxy Profile&rdquo; to keep real API keys on the host proxy.
                  </div>
                </td>
              </tr>
            </tbody>
          ) : (
            <tbody className="divide-y divide-border font-mono text-[11.5px]">
              {profiles.map((p) => (
                <tr key={p.id} className="hover:bg-muted/30">
                  <td className="py-2.5 px-3 font-sans font-medium text-foreground">
                    {p.name}
                  </td>
                  <td className="py-2.5 px-3 text-muted-foreground">
                    {p.provider}
                  </td>
                  <td className="py-2.5 px-3 text-foreground">
                    {p.upstream_url}
                  </td>
                  <td className="py-2.5 px-3 text-muted-foreground">
                    {p.inject_header}: ${p.secret_ref}
                  </td>
                  <td className="py-2.5 px-3">
                    <span className="border border-success/40 bg-success/10 px-1.5 py-0.5 text-[10px] uppercase text-success">
                      {p.status || 'active'}
                    </span>
                  </td>
                  <td className="py-2.5 px-3 text-right">
                    <button
                      type="button"
                      onClick={() => {
                        void api
                          .deleteAgentProfile(p.id)
                          .then(() => {
                            toast.success('Deleted proxy profile');
                            void loadData();
                          })
                          .catch((err) => toast.error(errorMessage(err)));
                      }}
                      className="border border-border p-1 text-muted-foreground hover:text-destructive"
                    >
                      <Trash2 className="h-3 w-3" />
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          )}
        </table>
      </div>

      {/* Live Daemon Supported Tiers */}
      {tiers.length > 0 && (
        <div className="border border-border bg-background">
          <div className="border-b border-border px-4 py-2.5 text-[12.5px] font-medium text-foreground">
            Supported Hardware Tiers (/api/v1/tiers)
          </div>
          <div className="grid grid-cols-2 sm:grid-cols-4 divide-y sm:divide-y-0 sm:divide-x divide-border font-mono text-xs">
            {tiers.map((t) => (
              <div key={t.id} className="p-3.5 space-y-1">
                <div className="text-foreground font-medium">{t.name}</div>
                <div className="text-[10.5px] text-muted-foreground font-sans">
                  {t.description}
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Control Plane REST API */}
      <div className="border border-border bg-background">
        <div className="border-b border-border px-4 py-2.5 text-[12.5px] font-medium text-foreground">
          Control Plane REST API ({config?.server?.listen || '0.0.0.0:18080'})
        </div>
        <div className="divide-y divide-border font-mono text-xs">
          {endpoints.map((ep, idx) => (
            <div
              key={idx}
              className="flex flex-wrap items-center justify-between gap-2 px-4 py-2.5"
            >
              <div className="flex items-center gap-3">
                <span className="border border-[#263fa9] bg-[#0f1634] px-2 py-0.5 text-[10px] text-white">
                  {ep.method}
                </span>
                <span className="text-foreground">{ep.path}</span>
              </div>
              <span className="font-sans text-muted-foreground text-xs">
                {ep.desc}
              </span>
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}
