export function formatDateTime(value?: string): string {
  if (!value) return '—';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '—';
  return new Intl.DateTimeFormat(undefined, {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  }).format(date);
}

export function formatTime(value?: string): string {
  if (!value) return '—';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '—';
  return new Intl.DateTimeFormat(undefined, {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  }).format(date);
}

export function formatTimestamp(value?: string): string {
  return formatDateTime(value);
}

export function relativeTime(value?: string): string {
  if (!value) return '—';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '—';

  const seconds = Math.round((date.getTime() - Date.now()) / 1000);
  const formatter = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' });
  const ranges: Array<[Intl.RelativeTimeFormatUnit, number]> = [
    ['year', 60 * 60 * 24 * 365],
    ['month', 60 * 60 * 24 * 30],
    ['day', 60 * 60 * 24],
    ['hour', 60 * 60],
    ['minute', 60],
    ['second', 1],
  ];

  for (const [unit, unitSeconds] of ranges) {
    if (Math.abs(seconds) >= unitSeconds || unit === 'second') {
      return formatter.format(Math.round(seconds / unitSeconds), unit);
    }
  }

  return 'just now';
}

export function formatRelativeTime(value?: string): string {
  return relativeTime(value);
}

export function formatMemory(megabytes: number): string {
  if (!Number.isFinite(megabytes) || megabytes <= 0) return '0 MB';
  if (megabytes >= 1024) {
    const gib = megabytes / 1024;
    return `${gib % 1 === 0 ? gib.toFixed(0) : gib.toFixed(1)} GB`;
  }
  return `${Math.round(megabytes)} MB`;
}

export function clampPercent(value: number, max: number): number {
  if (!max || max <= 0) return 0;
  return Math.min(100, Math.max(0, Math.round((value / max) * 100)));
}
