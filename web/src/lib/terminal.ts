export function openSSHTerminalWindow(
  vmId: string,
  _vmName?: string,
  user: 'compute' | 'root' = 'compute',
): Window | null {
  const query = user && user !== 'compute' ? `?user=${encodeURIComponent(user)}` : '';
  const url = `${window.location.origin}${window.location.pathname}#/terminal/${encodeURIComponent(vmId)}${query}`;
  const title = `compute_term_${vmId.replace(/[^a-zA-Z0-9_-]/g, '_')}`;

  // Choose sensible default dimensions for a terminal window (80x24 standard is ~800x500, 120x35 is ~1040x700)
  const width = Math.min(1060, Math.max(800, window.screen.availWidth - 100));
  const height = Math.min(720, Math.max(520, window.screen.availHeight - 100));
  const left = Math.max(0, Math.round((window.screen.availWidth - width) / 2));
  const top = Math.max(0, Math.round((window.screen.availHeight - height) / 2));

  const features = [
    `width=${width}`,
    `height=${height}`,
    `left=${left}`,
    `top=${top}`,
    'menubar=no',
    'toolbar=no',
    'location=no',
    'status=no',
    'resizable=yes',
    'scrollbars=no',
  ].join(',');

  const win = window.open(url, title, features);
  if (win) {
    win.focus();
  }
  return win;
}
