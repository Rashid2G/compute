import {
  Cpu,
  Database,
  Gauge,
  HardDrive,
  Layers,
  List,
  Settings,
  Zap,
  type LucideIcon,
} from 'lucide-react';

export type PageId =
  | 'instances'
  | 'image'
  | 'snapshots'
  | 'storage'
  | 'jobs'
  | 'workers'
  | 'resources'
  | 'settings';

export type SettingsTabId =
  | 'general'
  | 'appearance'
  | 'access'
  | 'environment'
  | 'network'
  | 'events';

export const SETTINGS_TABS: SettingsTabId[] = [
  'general',
  'appearance',
  'access',
  'environment',
  'network',
  'events',
];

export interface PageMeta {
  id: PageId;
  label: string;
  shortLabel?: string;
  description: string;
  icon: LucideIcon;
  section?: 'core' | 'infrastructure' | 'platform';
  adminOnly?: boolean;
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
    id: 'image',
    label: 'Images',
    shortLabel: 'Images',
    description: 'Prepare custom OS images with pre-configured software and build reusable templates.',
    icon: Layers,
    section: 'core',
  },
  {
    id: 'snapshots',
    label: 'Snapshots',
    shortLabel: 'Snapshots',
    description: 'Instant memory/disk snapshots and reusable sandbox templates.',
    icon: Database,
    section: 'core',
  },
  {
    id: 'storage',
    label: 'Volumes',
    shortLabel: 'Volumes',
    description: 'Attachable ext4/xfs block volumes, IOPS telemetry, and snapshots.',
    icon: HardDrive,
    section: 'infrastructure',
  },
  {
    id: 'jobs',
    label: 'CI Jobs',
    shortLabel: 'CI Jobs',
    description: 'Ephemeral GitHub Actions runners, CI job steps, and workload queue.',
    icon: Zap,
    section: 'core',
  },
  {
    id: 'workers',
    label: 'Workers',
    shortLabel: 'Workers',
    description: 'Distributed bare-metal KVM hosts, quotas, mTLS leases, and pool capacity.',
    icon: Cpu,
    section: 'infrastructure',
  },
  {
    id: 'resources',
    label: 'Resources',
    shortLabel: 'Resources',
    description: 'Cluster-wide compute allocation, host guardrails, and capacity utilization.',
    icon: Gauge,
    section: 'platform',
    adminOnly: true,
  },
  {
    id: 'settings',
    label: 'Settings',
    shortLabel: 'Settings',
    description: 'Daemon settings, appearance, access keys, environment, network, and telemetry.',
    icon: Settings,
    section: 'platform',
  },
];

export const ROUTE_ALIASES: Record<string, PageId> = {
  sandboxes: 'instances',
  dashboard: 'instances',
  overview: 'instances',
  console: 'instances',
  snapshots: 'snapshots',
  templates: 'snapshots',
  image: 'image',
  images: 'image',
  build: 'image',
  volumes: 'storage',
  queue: 'jobs',
  ci: 'jobs',
  organization: 'workers',
  env: 'settings',
  environment: 'settings',
  keys: 'settings',
  access: 'settings',
  api: 'settings',
  network: 'settings',
  webhooks: 'settings',
  audit: 'settings',
  events: 'settings',
  billing: 'settings',
  account: 'settings',
};

const SETTINGS_TAB_ALIASES: Record<string, SettingsTabId> = {
  access: 'access',
  api: 'access',
  keys: 'access',
  network: 'network',
  events: 'events',
  webhooks: 'events',
  audit: 'events',
  environment: 'environment',
  env: 'environment',
  appearance: 'appearance',
  general: 'general',
};

export function isPageId(value: string): value is PageId {
  return PAGES.some((page) => page.id === value);
}

export function isSettingsTabId(value: string): value is SettingsTabId {
  return SETTINGS_TABS.includes(value as SettingsTabId);
}

export function resolvePageId(raw: string): PageId {
  const clean = raw.split('?')[0].replace(/^\/+|\/+$/g, '').toLowerCase();
  if (isPageId(clean)) return clean;
  if (clean in ROUTE_ALIASES) return ROUTE_ALIASES[clean];
  return 'instances';
}

export function resolveRoute(raw: string): {
  page: PageId;
  settingsTab?: SettingsTabId;
  redirectTo?: PageId;
} {
  const [pathPart, queryPart = ''] = raw.split('?');
  const clean = pathPart.replace(/^\/+|\/+$/g, '').toLowerCase();
  const params = new URLSearchParams(queryPart);

  if (clean === '' || clean === 'dashboard' || clean === 'overview' || clean === 'console') {
    return {
      page: 'instances',
      redirectTo: clean === '' ? undefined : 'instances',
    };
  }

  if (clean === 'organization') {
    return { page: 'workers', redirectTo: 'workers' };
  }

  if (clean in SETTINGS_TAB_ALIASES) {
    return {
      page: 'settings',
      settingsTab: SETTINGS_TAB_ALIASES[clean],
    };
  }

  if (clean === 'settings') {
    const tabParam = (params.get('tab') || '').toLowerCase();
    return {
      page: 'settings',
      settingsTab: isSettingsTabId(tabParam) ? tabParam : 'general',
    };
  }

  return { page: resolvePageId(clean) };
}
