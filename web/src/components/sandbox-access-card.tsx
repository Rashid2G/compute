import { AlertTriangle, ExternalLink, Globe, Info, Terminal } from 'lucide-react';

import { CopyButton } from '@/components/copy-button';
import { sandboxAccess } from '@/lib/sandbox-access';
import type { PortForwardRecord, VM } from '@/lib/types';
import { cn } from '@/lib/utils';

function AccessValue({ value, href }: { value: string; href?: string }) {
  if (href) {
    return (
      <a
        href={href}
        target="_blank"
        rel="noopener noreferrer"
        className="min-w-0 flex-1 truncate font-mono text-xs text-[color:var(--link)] underline-offset-2 hover:underline"
        title={value}
      >
        {value}
      </a>
    );
  }
  return (
    <span className="min-w-0 flex-1 truncate font-mono text-xs text-foreground" title={value}>
      {value}
    </span>
  );
}

function AccessRow({
  label,
  value,
  href,
  copyLabel,
  hint,
}: {
  label: string;
  value: string;
  href?: string;
  copyLabel: string;
  hint?: string;
}) {
  return (
    <div className="min-w-0">
      <div className="flex items-center gap-2">
        <span className="w-10 shrink-0 text-[10px] font-medium uppercase tracking-[0.08em] text-muted-foreground">
          {label}
        </span>
        <AccessValue value={value} href={href} />
        <CopyButton value={value} label={copyLabel} className="h-6 w-6 shrink-0" />
      </div>
      {hint ? (
        <p className="mt-1 pl-12 pr-8 text-[10px] leading-relaxed text-muted-foreground">{hint}</p>
      ) : null}
    </div>
  );
}

/**
 * Primary SSH command / HTTP URL / CLI command for a sandbox, each with a copy
 * button. The HTTP URL is only rendered when the sandbox DNS zone is expected
 * to resolve; otherwise the server is left out and explained.
 */
export function SandboxAccessCard({
  vm,
  user = 'compute',
  portForwards,
  className,
}: {
  vm: VM;
  user?: string;
  portForwards?: PortForwardRecord[];
  className?: string;
}) {
  const access = sandboxAccess(vm, { user });
  const running = vm.status === 'running';
  const activeForwards = (portForwards ?? []).filter(
    (pf) =>
      (pf.vm_id === vm.id || pf.vm_name === vm.name || (!pf.vm_id && !pf.vm_name)) &&
      pf.status !== 'inactive',
  );

  return (
    <div
      className={cn('space-y-2.5 rounded-md border bg-secondary/15 p-3.5', className)}
      aria-label={`Access details for ${vm.name}`}
    >
      <div className="flex items-center gap-1.5 text-xs font-medium">
        <Terminal className="h-3.5 w-3.5 text-primary" />
        Access
      </div>

      {access.ssh ? (
        <div>
          <AccessRow
            label="SSH"
            value={access.ssh}
            copyLabel="Copy SSH command"
            hint={access.sshResolvable ? undefined : access.sshNote}
          />
          {!access.sshResolvable ? (
            <p className="mt-1 flex items-start gap-1 pl-12 text-[10px] leading-relaxed text-warning">
              <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
              <span>Not resolvable from here until DNS delegation is in place.</span>
            </p>
          ) : null}
        </div>
      ) : (
        <p className="text-[11px] text-muted-foreground">{access.sshNote}</p>
      )}

      {access.url ? (
        <div>
          <div className="flex items-center gap-2">
            <span className="w-10 shrink-0 text-[10px] font-medium uppercase tracking-[0.08em] text-muted-foreground">
              URL
            </span>
            {running ? (
              <>
                <AccessValue value={access.url} href={access.url} />
                <ExternalLink className="h-3 w-3 shrink-0 text-muted-foreground" aria-hidden="true" />
              </>
            ) : (
              <AccessValue value={access.url} />
            )}
            <CopyButton value={access.url} label="Copy URL" className="h-6 w-6 shrink-0" />
          </div>
          {running ? null : (
            <p className="mt-1 flex items-start gap-1 pl-12 text-[10px] leading-relaxed text-muted-foreground">
              <Info className="mt-0.5 h-3 w-3 shrink-0" />
              <span>Start the sandbox to serve this URL.</span>
            </p>
          )}
          {access.portURL ? (
            <p className="mt-1 pl-12 font-mono text-[10px] text-muted-foreground">
              port {access.port}: {access.portURL}
            </p>
          ) : null}
        </div>
      ) : (
        <div className="flex items-start gap-2">
          <span className="w-10 shrink-0 pt-0.5 text-[10px] font-medium uppercase tracking-[0.08em] text-muted-foreground">
            URL
          </span>
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
              <Globe className="h-3.5 w-3.5 shrink-0" />
              <span>HTTP URL unavailable</span>
            </div>
            {access.urlNote ? (
              <p className="mt-0.5 text-[10px] leading-relaxed text-muted-foreground">{access.urlNote}</p>
            ) : null}
          </div>
        </div>
      )}

      {activeForwards.length > 0 ? (
        <div className="space-y-1.5 pt-0.5">
          {activeForwards.map((pf) => {
            const url =
              pf.public_url ||
              (pf.host_port > 0
                ? `${window.location.protocol}//${window.location.hostname}:${pf.host_port}`
                : '');
            return (
              <div key={pf.id} className="flex items-center gap-2 min-w-0">
                <span className="w-10 shrink-0 font-mono text-[10px] font-medium uppercase tracking-[0.04em] text-muted-foreground">
                  :{pf.guest_port}
                </span>
                <span
                  className={cn(
                    'shrink-0 border px-1.5 py-0.5 font-mono text-[9.5px] uppercase',
                    pf.public
                      ? 'border-success/40 bg-success/10 text-success'
                      : 'border-border bg-muted/40 text-muted-foreground',
                  )}
                >
                  {pf.public ? 'public' : 'private'}
                </span>
                {url ? (
                  <>
                    <AccessValue value={url} href={url} />
                    <ExternalLink
                      className="h-3 w-3 shrink-0 text-muted-foreground"
                      aria-hidden="true"
                    />
                    <CopyButton
                      value={url}
                      label={`Copy exposed URL for port ${pf.guest_port}`}
                      className="h-6 w-6 shrink-0"
                    />
                  </>
                ) : (
                  <span className="min-w-0 flex-1 truncate font-mono text-xs text-muted-foreground">
                    {pf.host_port > 0 ? `:${pf.host_port} → :${pf.guest_port}` : `:${pf.guest_port}`}
                  </span>
                )}
              </div>
            );
          })}
        </div>
      ) : null}

      {access.cliSSH ? (
        <AccessRow label="CLI" value={access.cliSSH} copyLabel="Copy CLI command" />
      ) : null}
    </div>
  );
}
