import type { VM } from './types';

export type SandboxSSHHostKind =
  | 'tailnet_dns'
  | 'hostname'
  | 'tailnet_ip'
  | 'ip_address'
  | 'public_ip'
  | 'none';

export type SandboxURLKind = 'hostname' | 'port' | 'legacy_port';

export interface SandboxAccess {
  sandbox: string;
  hostname?: string;
  tailnetDns?: string;
  zone?: string;
  /** The zone belongs to Tailscale's MagicDNS namespace (*.ts.net). */
  zoneIsMagicDNS: boolean;

  ssh?: string;
  sshHost?: string;
  sshHostKind: SandboxSSHHostKind;
  sshResolvable: boolean;
  sshNote?: string;

  url?: string;
  urlHost?: string;
  urlKind?: SandboxURLKind;
  urlResolvable: boolean;
  urlNote?: string;

  port?: number;
  portURL?: string;
  legacyPortURL?: string;

  cliSSH?: string;
  cliURL?: string;
}

export interface SandboxAccessOptions {
  user?: string;
  port?: number;
}

function normalizeHostname(hostname?: string): string {
  return (hostname ?? '').trim().replace(/\.$/, '');
}

/**
 * MagicDNS answers every *.ts.net query itself, so the daemon's authoritative
 * records for a sandbox zone under ts.net never reach tailnet clients.
 */
export function isMagicDNSZone(zone?: string): boolean {
  const normalized = (zone ?? '').trim().toLowerCase().replace(/\.$/, '');
  return normalized === 'ts.net' || normalized.endsWith('.ts.net');
}

/**
 * Split the API hostname shape "<sandbox>.<tenant>.<zone>". A two-label name is
 * treated as "<sandbox>.<zone>" for legacy single-label deployments.
 */
export function splitSandboxHostname(hostname?: string): {
  sandbox: string;
  tenant: string;
  zone: string;
} | null {
  const normalized = normalizeHostname(hostname);
  if (!normalized) return null;
  const labels = normalized.split('.');
  if (labels.length >= 3) {
    return {
      sandbox: labels[0],
      tenant: labels[1],
      zone: labels.slice(2).join('.'),
    };
  }
  if (labels.length === 2) {
    return { sandbox: labels[0], tenant: '', zone: labels[1] };
  }
  return null;
}

/**
 * Derive the SSH and HTTP access model for a sandbox from API data only:
 * tailnet_dns, hostname, tailnet_ip, ip_address and public_ip. Mirrors
 * cmd/compute/sandboxurl.go; keep both in sync.
 */
export function sandboxAccess(
  vm: VM | null | undefined,
  options: SandboxAccessOptions = {},
): SandboxAccess {
  const user = options.user?.trim() || 'compute';
  const port = options.port ?? 0;
  const access: SandboxAccess = {
    sandbox: '',
    zoneIsMagicDNS: false,
    sshHostKind: 'none',
    sshResolvable: false,
    urlResolvable: false,
  };

  if (!vm) {
    access.sshNote = 'Sandbox details are unavailable.';
    access.urlNote = access.sshNote;
    return access;
  }

  access.sandbox = (vm.name ?? '').trim() || (vm.id ?? '').trim();
  access.hostname = normalizeHostname(vm.hostname);
  access.tailnetDns = normalizeHostname(vm.tailnet_dns);

  const parsed = splitSandboxHostname(access.hostname);
  access.zone = parsed?.zone ?? '';
  access.zoneIsMagicDNS = isMagicDNSZone(access.zone);
  const zoneDelegated = Boolean(parsed && access.zone && !access.zoneIsMagicDNS);

  const tailnetIp = (vm.tailnet_ip ?? '').trim();
  const guestIp = (vm.ip_address ?? '').trim();
  const publicIp = (vm.public_ip ?? '').trim();

  // SSH precedence: tailnet DNS → hostname → tailnet IP → guest IP → public IP.
  if (access.tailnetDns) {
    access.sshHost = access.tailnetDns;
    access.sshHostKind = 'tailnet_dns';
    access.sshResolvable = true;
  } else if (access.hostname) {
    access.sshHost = access.hostname;
    access.sshHostKind = 'hostname';
    access.sshResolvable = zoneDelegated;
    access.sshNote = access.zoneIsMagicDNS
      ? `${access.hostname} is under ${access.zone}, which MagicDNS answers itself; it does not resolve from tailnet clients until the zone is delegated to this daemon.`
      : `${access.hostname} requires ${access.zone} to be delegated to this daemon's DNS responder.`;
  } else if (tailnetIp) {
    access.sshHost = tailnetIp;
    access.sshHostKind = 'tailnet_ip';
    access.sshResolvable = true;
    access.sshNote = 'No DNS name yet; using the sandbox tailnet IP.';
  } else if (guestIp) {
    access.sshHost = guestIp;
    access.sshHostKind = 'ip_address';
    access.sshResolvable = true;
    access.sshNote =
      'No DNS name yet; using the sandbox guest IP, which needs routing or a subnet route.';
  } else if (publicIp) {
    access.sshHost = publicIp;
    access.sshHostKind = 'public_ip';
    access.sshResolvable = true;
    access.sshNote = 'No DNS name yet; using the public IP.';
  } else {
    access.sshNote = 'No address is assigned to this sandbox yet.';
  }
  if (access.sshHost) access.ssh = `ssh ${user}@${access.sshHost}`;

  // HTTP ingress: only publish a URL when the sandbox zone is expected to
  // resolve. ts.net names are owned by MagicDNS, so they stay hidden.
  if (!access.hostname) {
    access.urlNote =
      'No sandbox DNS name is configured. Enable the daemon DNS responder with a base domain to publish an HTTP URL.';
  } else if (access.zoneIsMagicDNS) {
    access.urlHost = access.hostname;
    access.urlNote = `Sandbox HTTP names under ${access.zone} are intercepted by MagicDNS and do not resolve from tailnet clients. HTTP ingress stays hidden until the zone is delegated to this daemon (see issue #16 for the domain decision).`;
  } else if (!parsed) {
    access.urlNote = `Could not derive a zone from the sandbox hostname "${access.hostname}".`;
  } else {
    access.url = `http://${access.hostname.toLowerCase()}/`;
    access.urlHost = access.hostname;
    access.urlKind = 'hostname';
    access.urlResolvable = true;
    access.urlNote = `Requires ${access.zone} to be delegated to this daemon's DNS responder.`;
  }

  // Port-scoped ingress: the edge accepts "<port>-<sandbox>" labels.
  if (port > 0 && port <= 65535 && access.urlResolvable && parsed && access.zone) {
    const head = `${port}-${parsed.sandbox}`;
    access.port = port;
    if (parsed.tenant) {
      access.portURL = `http://${head}.${parsed.tenant}.${access.zone}/`.toLowerCase();
      // The pre-tenant form only addresses the legacy "default" owner; for a
      // real username it would resolve to a different (or no) sandbox.
      if (parsed.tenant === 'default') {
        access.legacyPortURL = `http://${head}.${access.zone}/`.toLowerCase();
      }
    } else {
      access.portURL = `http://${head}.${access.zone}/`.toLowerCase();
    }
    access.urlKind = 'port';
  }

  if (access.sandbox) {
    access.cliSSH = `compute ssh ${access.sandbox}`;
    access.cliURL = `compute url ${access.sandbox}`;
  }
  return access;
}

/** Best single value for compact lists: URL when available, else SSH name. */
export function primarySandboxAddress(access: SandboxAccess): string {
  return access.url || access.sshHost || '';
}
