import { useEffect, useRef, useState, useCallback } from 'react';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import '@xterm/xterm/css/xterm.css';
import { Button } from '@/components/ui/button';
import { RefreshCw, Trash2, Wifi, WifiOff, Loader2 } from 'lucide-react';
import { cn } from '@/lib/utils';

export interface SSHTerminalProps {
  vmId: string;
  user?: string;
  theme?: 'dark' | 'light';
  className?: string;
  onStatusChange?: (status: 'connecting' | 'connected' | 'disconnected' | 'error') => void;
  autoFocus?: boolean;
}

export function SSHTerminal({
  vmId,
  user = 'compute',
  theme = 'dark',
  className,
  onStatusChange,
  autoFocus = true,
}: SSHTerminalProps) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const termRef = useRef<Terminal | null>(null);
  const fitAddonRef = useRef<FitAddon | null>(null);
  const wsRef = useRef<WebSocket | null>(null);

  const [status, setStatus] = useState<'connecting' | 'connected' | 'disconnected' | 'error'>('connecting');
  const [connectCount, setConnectCount] = useState(0);

  const updateStatus = useCallback((next: 'connecting' | 'connected' | 'disconnected' | 'error') => {
    setStatus(next);
    onStatusChange?.(next);
  }, [onStatusChange]);

  const reconnect = useCallback(() => {
    if (wsRef.current) {
      try {
        wsRef.current.close();
      } catch {
        // ignore
      }
      wsRef.current = null;
    }
    if (termRef.current) {
      termRef.current.reset();
    }
    updateStatus('connecting');
    setConnectCount((c) => c + 1);
  }, [updateStatus]);

  const clearTerminal = useCallback(() => {
    termRef.current?.clear();
  }, []);

  useEffect(() => {
    if (!containerRef.current) return;

    const isDark = theme === 'dark';
    const term = new Terminal({
      cursorBlink: true,
      cursorStyle: 'block',
      fontSize: 13,
      fontFamily: 'JetBrains Mono, Geist Mono, ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
      lineHeight: 1.25,
      scrollback: 5000,
      theme: {
        background: isDark ? '#0a0a0b' : '#131315',
        foreground: '#f5f5f4',
        cursor: '#fb923c',
        cursorAccent: isDark ? '#0a0a0b' : '#131315',
        selectionBackground: 'rgba(217, 78, 31, 0.35)',
        black: '#18181b',
        red: '#ef4444',
        green: '#22c55e',
        yellow: '#eab308',
        blue: '#3b82f6',
        magenta: '#a855f7',
        cyan: '#06b6d4',
        white: '#f4f4f5',
        brightBlack: '#71717a',
        brightRed: '#f87171',
        brightGreen: '#4ade80',
        brightYellow: '#fde047',
        brightBlue: '#60a5fa',
        brightMagenta: '#c084fc',
        brightCyan: '#22d3ee',
        brightWhite: '#ffffff',
      },
    });

    const fitAddon = new FitAddon();
    term.loadAddon(fitAddon);
    term.open(containerRef.current);

    termRef.current = term;
    fitAddonRef.current = fitAddon;

    try {
      fitAddon.fit();
    } catch {
      // container might not be measured yet
    }

    if (autoFocus) {
      term.focus();
    }

    // Connect WebSocket
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const cols = term.cols || 100;
    const rows = term.rows || 30;
    const wsUrl = `${protocol}//${window.location.host}/api/v1/vms/${encodeURIComponent(vmId)}/terminal?user=${encodeURIComponent(user)}&cols=${cols}&rows=${rows}`;

    updateStatus('connecting');
    const ws = new WebSocket(wsUrl);
    ws.binaryType = 'arraybuffer';
    wsRef.current = ws;

    ws.onopen = () => {
      updateStatus('connected');
      try {
        fitAddon.fit();
        ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }));
      } catch {
        // ignore
      }
      if (autoFocus) {
        term.focus();
      }
    };

    ws.onmessage = (event) => {
      if (typeof event.data === 'string') {
        term.write(event.data);
      } else if (event.data instanceof ArrayBuffer) {
        term.write(new Uint8Array(event.data));
      }
    };

    ws.onerror = () => {
      updateStatus('error');
    };

    ws.onclose = () => {
      updateStatus('disconnected');
    };

    const dataListener = term.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(data);
      }
    });

    const binaryListener = term.onBinary((data) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(data);
      }
    });

    // ResizeObserver
    const resizeObserver = new ResizeObserver(() => {
      try {
        fitAddon.fit();
        if (ws.readyState === WebSocket.OPEN) {
          ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }));
        }
      } catch {
        // ignore
      }
    });
    resizeObserver.observe(containerRef.current);

    return () => {
      resizeObserver.disconnect();
      dataListener.dispose();
      binaryListener.dispose();
      if (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING) {
        ws.close();
      }
      term.dispose();
      termRef.current = null;
      fitAddonRef.current = null;
      wsRef.current = null;
    };
  }, [vmId, user, theme, autoFocus, connectCount, updateStatus]);

  return (
    <div className={cn('relative flex flex-col h-full w-full bg-zinc-950 select-none overflow-hidden', className)}>
      <div className="absolute top-2 right-3 z-10 flex items-center gap-1.5 opacity-60 hover:opacity-100 transition-opacity">
        <span
          className={cn(
            'inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[10px] font-medium tracking-wide backdrop-blur-md',
            status === 'connected' && 'bg-emerald-500/15 text-[#6ee7b7] border border-emerald-500/25',
            status === 'connecting' && 'bg-amber-500/15 text-[#fcd34d] border border-amber-500/25',
            (status === 'disconnected' || status === 'error') && 'bg-rose-500/15 text-[#fda4af] border border-rose-500/25',
          )}
        >
          {status === 'connected' && <Wifi className="h-2.5 w-2.5" />}
          {status === 'connecting' && <Loader2 className="h-2.5 w-2.5 animate-spin" />}
          {(status === 'disconnected' || status === 'error') && <WifiOff className="h-2.5 w-2.5" />}
          <span className="capitalize">{status}</span>
        </span>

        {status !== 'connected' && status !== 'connecting' && (
          <Button
            size="icon-xs"
            variant="outline"
            onClick={reconnect}
            title="Reconnect SSH terminal"
            className="h-6 w-6 border-zinc-700 bg-zinc-900/80 text-zinc-300 hover:bg-zinc-800"
          >
            <RefreshCw className="h-3 w-3" />
          </Button>
        )}

        <Button
          size="icon-xs"
          variant="outline"
          onClick={clearTerminal}
          title="Clear terminal screen"
          className="h-6 w-6 border-zinc-700 bg-zinc-900/80 text-zinc-300 hover:bg-zinc-800"
        >
          <Trash2 className="h-3 w-3" />
        </Button>
      </div>

      <div
        ref={containerRef}
        className="flex-1 w-full h-full p-2.5 overflow-hidden text-left"
        onClick={() => termRef.current?.focus()}
      />
    </div>
  );
}
