import type { VM } from './types';

export type SandboxSSHHostKind =
  | 'hostname'
  | 'ip_address'
  | 'public_ip'
  | 'none';

export type SandboxURLKind = 'hostname' | 'port' | 'legacy_port';

export interface SandboxAccess {
  sandbox: string;
  hostname?: string;
  zone?: string;

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
 * hostname, ip_address and public_ip. Mirrors cmd/compute/sandboxurl.go.
 */
export function sandboxAccess(
  vm: VM | null | undefined,
  options: SandboxAccessOptions = {},
): SandboxAccess {
  const user = options.user?.trim() || 'compute';
  const port = options.port ?? 0;
  const access: SandboxAccess = {
    sandbox: '',
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

  const parsed = splitSandboxHostname(access.hostname);
  access.zone = parsed?.zone ?? '';
  const zoneDelegated = Boolean(parsed && access.zone);

  const guestIp = (vm.ip_address ?? '').trim();
  const publicIp = (vm.public_ip ?? '').trim();

  // SSH precedence: hostname → guest IP → public IP.
  if (access.hostname) {
    access.sshHost = access.hostname;
    access.sshHostKind = 'hostname';
    access.sshResolvable = zoneDelegated;
    access.sshNote = `${access.hostname} requires ${access.zone} to be delegated to this daemon's DNS responder.`;
  } else if (publicIp) {
    access.sshHost = publicIp;
    access.sshHostKind = 'public_ip';
    access.sshResolvable = true;
    access.sshNote = 'No DNS name yet; using the public IP.';
  } else if (guestIp) {
    access.sshHost = guestIp;
    access.sshHostKind = 'ip_address';
    access.sshResolvable = true;
    access.sshNote = 'No DNS name yet; using the sandbox guest IP.';
  } else {
    access.sshNote = 'No address is assigned to this sandbox yet.';
  }
  if (access.sshHost) access.ssh = `ssh ${user}@${access.sshHost}`;

  // HTTP ingress.
  if (!access.hostname) {
    access.urlNote =
      'No sandbox DNS name is configured. Enable the daemon DNS responder with a base domain to publish an HTTP URL.';
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
