import { useCallback, useEffect, useState } from 'react';
import { RefreshCw } from 'lucide-react';

import { api } from '@/lib/api';
import type { DaemonConfig, VMTier } from '@/lib/types';

interface ApiPageProps {
  config: DaemonConfig | null;
  embedded?: boolean;
}

export function ApiPage({ config, embedded = false }: ApiPageProps) {
  const [tiers, setTiers] = useState<VMTier[]>([]);

  const loadData = useCallback(async () => {
    const tRes = await Promise.allSettled([api.tiers()]);
    if (tRes[0].status === 'fulfilled') setTiers(tRes[0].value || []);
  }, []);

  useEffect(() => {
    void loadData();
  }, [loadData]);

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
      {!embedded && (
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h1 className="font-display text-[22px] font-medium tracking-tight text-foreground">
              Control Plane API
            </h1>
            <p className="text-[12px] text-muted-foreground">
              Supported VM tiers and live REST control-plane reference.
            </p>
          </div>

          <button
            type="button"
            onClick={() => void loadData()}
            className="flex h-8 items-center gap-1.5 border border-border bg-background px-3 text-[12px] text-muted-foreground hover:text-foreground"
          >
            <RefreshCw className="h-3.5 w-3.5" />
            <span>Refresh</span>
          </button>
        </div>
      )}

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
                <span className="border border-primary bg-selected px-2 py-0.5 text-[10px] text-selected-foreground">
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
