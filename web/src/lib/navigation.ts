import {
  Braces,
  Disc,
  LayoutDashboard,
  Network,
  Server,
  Settings,
  Terminal,
  type LucideIcon,
} from 'lucide-react';

export type PageId =
  | 'overview'
  | 'instances'
  | 'images'
  | 'console'
  | 'network'
  | 'api'
  | 'settings';

export interface PageMeta {
  id: PageId;
  label: string;
  description: string;
  icon: LucideIcon;
}

export const PAGES: PageMeta[] = [
  {
    id: 'overview',
    label: 'Overview',
    description: 'Capacity, health and the running fleet at a glance.',
    icon: LayoutDashboard,
  },
  {
    id: 'instances',
    label: 'Instances',
    description: 'Provision, start and manage Firecracker microVMs.',
    icon: Server,
  },
  {
    id: 'images',
    label: 'Images & Templates',
    description: 'Pre-baked images and reusable disk templates.',
    icon: Disc,
  },
  {
    id: 'console',
    label: 'Console',
    description: 'Run commands inside a running instance.',
    icon: Terminal,
  },
  {
    id: 'network',
    label: 'Network',
    description: 'Bridge configuration and address leases.',
    icon: Network,
  },
  {
    id: 'api',
    label: 'API & CLI',
    description: 'Endpoints, command-line usage and runtime configuration.',
    icon: Braces,
  },
  {
    id: 'settings',
    label: 'Settings',
    description: 'Theme appearance, WebSocket connectivity and system preferences.',
    icon: Settings,
  },
];

export function isPageId(value: string): value is PageId {
  return PAGES.some((page) => page.id === value);
}
