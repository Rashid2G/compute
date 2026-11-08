import {
  Braces,
  Cpu,
  Database,
  HardDrive,
  KeyRound,
  LayoutDashboard,
  List,
  Network,
  Radio,
  Settings,
  Sliders,
  Terminal,
  Zap,
  type LucideIcon,
} from 'lucide-react';

export type PageId =
  | 'instances'
  | 'images'
  | 'environment'
  | 'access'
  | 'events'
  | 'api'
  | 'jobs'
  | 'overview'
  | 'workers'
  | 'storage'
  | 'network'
  | 'console'
  | 'settings';

export interface PageMeta {
  id: PageId;
  label: string;
  shortLabel?: string;
  description: string;
  icon: LucideIcon;
  section?: 'core' | 'infrastructure' | 'platform';
  badge?: string;
}

export const PAGES: PageMeta[] = [
  {
    id: 'instances',
    label: 'Sandboxes',
    shortLabel: 'Sandboxes',
    description: 'Provision, snapshot, and orchestrate Firecracker microVM sandboxes.',
    icon: List,
    section: 'core',
  },
  {
    id: 'images',
    label: 'Snapshots',
    shortLabel: 'Snapshots',
    description: 'Instant memory/disk snapshots and reusable custom rootfs images.',
    icon: Database,
    section: 'core',
  },
  {
    id: 'environment',
    label: 'Environment',
    shortLabel: 'Environment',
    description: 'What every new sandbox in it starts with.',
    icon: Sliders,
    section: 'core',
  },
  {
    id: 'access',
    label: 'API keys',
    shortLabel: 'API keys',
    description: 'Scoped API keys, SSH public keys, agent credentials, and worker tokens.',
    icon: KeyRound,
    section: 'core',
  },
  {
    id: 'events',
    label: 'Webhooks & OTel',
    shortLabel: 'Webhooks & OTel',
    description: 'Outbound webhook subscriptions, OpenTelemetry export, and audit stream.',
    icon: Radio,
    section: 'core',
  },
  {
    id: 'api',
    label: 'Agents & Proxying',
    shortLabel: 'Agents & CLI',
    description: 'Zero-trust credential injection proxy, REST API, and CLI reference.',
    icon: Braces,
    section: 'core',
  },
  {
    id: 'jobs',
    label: 'CI on Compute VMs',
    shortLabel: 'CI on Compute VMs',
    description: 'Ephemeral GitHub Actions runners, CI job steps, and workload queue.',
    icon: Zap,
    section: 'core',
  },
  {
    id: 'overview',
    label: 'Overview',
    shortLabel: 'Overview',
    description: 'Cluster capacity, scheduler health, and active Firecracker fleet.',
    icon: LayoutDashboard,
    section: 'infrastructure',
  },
  {
    id: 'workers',
    label: 'Worker Nodes',
    shortLabel: 'Workers',
    description: 'Distributed bare-metal KVM hosts, mTLS leases, and pool capacity.',
    icon: Cpu,
    section: 'infrastructure',
  },
  {
    id: 'storage',
    label: 'Persistent Volumes',
    shortLabel: 'Volumes',
    description: 'Attachable ext4/xfs block volumes, IOPS telemetry, and snapshots.',
    icon: HardDrive,
    section: 'infrastructure',
  },
  {
    id: 'network',
    label: 'Network & NAT',
    shortLabel: 'Network',
    description: 'Bridge topology, TAP leases, port-forwarding rules, and public IPs.',
    icon: Network,
    section: 'infrastructure',
  },
  {
    id: 'console',
    label: 'Console & SSH',
    shortLabel: 'Console',
    description: 'Interactive web PTY terminal and non-interactive command execution.',
    icon: Terminal,
    section: 'infrastructure',
  },
  {
    id: 'settings',
    label: 'Organization & Billing',
    shortLabel: 'Organization',
    description: 'Organization quotas, billing usage, alerts, and daemon runtime.',
    icon: Settings,
    section: 'platform',
  },
];

export const ROUTE_ALIASES: Record<string, PageId> = {
  sandboxes: 'instances',
  snapshots: 'images',
  templates: 'images',
  env: 'environment',
  queue: 'jobs',
  ci: 'jobs',
  volumes: 'storage',
  keys: 'access',
  webhooks: 'events',
  audit: 'events',
  agents: 'api',
  billing: 'settings',
  account: 'settings',
  organization: 'settings',
};

export function isPageId(value: string): value is PageId {
  return PAGES.some((page) => page.id === value);
}

export function resolvePageId(raw: string): PageId {
  const clean = raw.split('?')[0].replace(/^\/+|\/+$/g, '').toLowerCase();
  if (isPageId(clean)) return clean;
  if (clean in ROUTE_ALIASES) return ROUTE_ALIASES[clean];
  return 'instances';
}
