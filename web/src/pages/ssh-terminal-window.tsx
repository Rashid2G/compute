import { useEffect, useState } from 'react';
import { SSHTerminal } from '@/components/ssh-terminal';
import type { VM } from '@/lib/types';

interface SSHTerminalWindowProps {
  vmId: string;
  vms?: VM[];
  theme?: 'dark' | 'light';
  onToggleTheme?: () => void;
}

function parseHashUser(): 'compute' | 'root' {
  const hash = window.location.hash || '';
  const queryIdx = hash.indexOf('?');
  if (queryIdx < 0) return 'compute';
  const params = new URLSearchParams(hash.slice(queryIdx + 1));
  return params.get('user') === 'root' ? 'root' : 'compute';
}

export function SSHTerminalWindow({ vmId }: SSHTerminalWindowProps) {
  const [guestUser, setGuestUser] = useState<'compute' | 'root'>(parseHashUser);

  useEffect(() => {
    const syncUser = () => setGuestUser(parseHashUser());
    window.addEventListener('hashchange', syncUser);
    return () => window.removeEventListener('hashchange', syncUser);
  }, []);

  return (
    <div
      data-testid="raw-terminal-window"
      className="h-screen w-screen overflow-hidden bg-[#0a0a0a]"
    >
      <SSHTerminal
        vmId={vmId}
        user={guestUser}
        raw
        autoFocus
        className="h-full w-full"
      />
    </div>
  );
}
