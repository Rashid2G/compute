import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  Activity,
  Check,
  Copy,
  Eye,
  EyeOff,
  Play,
  Plus,
  Radio,
  RefreshCw,
  Search,
  Send,
  Trash2,
  X,
} from 'lucide-react';
import { toast } from 'sonner';

import { api, errorMessage } from '@/lib/api';
import { formatTimestamp } from '@/lib/format';
import type {
  AuditEventRecord,
  OTelConfigRecord,
  WebhookEndpointRecord,
} from '@/lib/types';
import { cn } from '@/lib/utils';

interface EventsPageProps {
  events?: AuditEventRecord[];
  onRefresh?: () => void;
}

const AVAILABLE_EVENT_TYPES = [
  'sandbox.created',
  'sandbox.started',
  'sandbox.stopped',
  'sandbox.snapshotted',
  'sandbox.deleted',
  'job.completed',
  'worker.heartbeat',
  'volume.attached',
];

export function EventsPage({ events: propEvents, onRefresh }: EventsPageProps) {
  const [webhooks, setWebhooks] = useState<WebhookEndpointRecord[]>([]);
  const [otel, setOtel] = useState<OTelConfigRecord>({
    enabled: true,
    endpoint: 'https://otel.compute.internal:4318/v1/traces',
    protocol: 'HTTP/JSON (4318)',
    auth_header: '',
    service_name: 'compute-control-plane',
    sample_rate: '1.0',
    export_interval: '5000',
    metrics_enabled: true,
    traces_enabled: true,
    logs_enabled: false,
    spans_exported: 0,
    last_export_at: '',
  });
  const [auditEvents, setAuditEvents] = useState<AuditEventRecord[]>(
    propEvents || [],
  );
  const [activeTab, setActiveTab] = useState<'webhooks' | 'otel' | 'audit'>(
    'webhooks',
  );

  // Add Webhook form
  const [addWhOpen, setAddWhOpen] = useState(false);
  const [whUrl, setWhUrl] = useState('');
  const [whDesc, setWhDesc] = useState('');
  const [whSecret, setWhSecret] = useState('');
  const [whEvents, setWhEvents] = useState<string[]>([
    'sandbox.created',
    'sandbox.stopped',
  ]);
  const [revealedSecrets, setRevealedSecrets] = useState<Record<string, boolean>>(
    {},
  );
  const [copiedId, setCopiedId] = useState<string | null>(null);

  // Audit filter
  const [auditSearch, setAuditSearch] = useState('');
  const [selectedEvent, setSelectedEvent] = useState<AuditEventRecord | null>(
    null,
  );

  const loadAll = useCallback(async () => {
    const [whRes, otelRes, evtRes] = await Promise.allSettled([
      api.listWebhooks(),
      api.getOTelConfig(),
      api.listEvents(),
    ]);
    if (whRes.status === 'fulfilled') setWebhooks(whRes.value || []);
    if (otelRes.status === 'fulfilled' && otelRes.value) setOtel(otelRes.value);
    if (evtRes.status === 'fulfilled') setAuditEvents(evtRes.value || []);
  }, []);

  useEffect(() => {
    if (propEvents) setAuditEvents(propEvents);
  }, [propEvents]);

  useEffect(() => {
    void loadAll();
  }, [loadAll]);

  // Connect to live SSE /api/v1/events/stream
  useEffect(() => {
    let es: EventSource | null = null;
    try {
      es = new EventSource('/api/v1/events/stream');
      es.onmessage = (msg) => {
        try {
          const ev = JSON.parse(msg.data) as AuditEventRecord;
          if (ev && ev.id) {
            setAuditEvents((prev) =>
              prev.some((x) => x.id === ev.id) ? prev : [ev, ...prev],
            );
          }
        } catch {
          // Ignore malformed frame
        }
      };
    } catch {
      // Ignore SSE unsupported
    }
    return () => {
      if (es) es.close();
    };
  }, []);

  const filteredEvents = useMemo(() => {
    const q = auditSearch.trim().toLowerCase();
    if (!q) return auditEvents;
    return auditEvents.filter(
      (e) =>
        e.event.toLowerCase().includes(q) ||
        e.target.toLowerCase().includes(q) ||
        e.actor.toLowerCase().includes(q) ||
        e.id.toLowerCase().includes(q),
    );
  }, [auditEvents, auditSearch]);

  async function handleCreateWebhook(e: React.FormEvent) {
    e.preventDefault();
    if (!whUrl.trim()) return;
    try {
      const created = await api.createWebhook({
        url: whUrl.trim(),
        description: whDesc.trim(),
        secret: whSecret.trim() || undefined,
        events: whEvents,
      });
      toast.success(`Registered webhook endpoint ${created.id}`);
      setAddWhOpen(false);
      setWhUrl('');
      setWhDesc('');
      setWhSecret('');
      await loadAll();
      onRefresh?.();
    } catch (err) {
      toast.error(errorMessage(err));
    }
  }

  async function handleTestWebhook(wh: WebhookEndpointRecord) {
    try {
      const res = await api.testWebhook(wh.id);
      toast.success(`Test ping delivered (${res.code} OK)`);
      await loadAll();
    } catch (err) {
      toast.error(errorMessage(err));
    }
  }

  async function handleSaveOTel(e: React.FormEvent) {
    e.preventDefault();
    try {
      const saved = await api.saveOTelConfig(otel);
      setOtel(saved);
      toast.success('Saved OpenTelemetry OTLP exporter configuration');
      await loadAll();
    } catch (err) {
      toast.error(errorMessage(err));
    }
  }

  async function handleTestOTel() {
    try {
      const updated = await api.testOTelExport();
      setOtel(updated);
      toast.success(
        `Exported verification trace span (Total spans: ${updated.spans_exported})`,
      );
      await loadAll();
    } catch (err) {
      toast.error(errorMessage(err));
    }
  }

  return (
    <div className="space-y-5">
      {/* Header */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="font-display text-[22px] font-medium tracking-tight text-foreground">
            Webhooks &amp; OTel
          </h1>
          <p className="text-[12px] text-muted-foreground">
            HMAC-SHA256 webhook subscriptions, OpenTelemetry OTLP exporter, and
            live control-plane audit event stream.
          </p>
        </div>

        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={() => void loadAll()}
            className="flex h-8 items-center gap-1.5 border border-border bg-background px-3 text-[12px] text-muted-foreground hover:text-foreground"
          >
            <RefreshCw className="h-3.5 w-3.5" />
            <span>Refresh</span>
          </button>
          <button
            type="button"
            onClick={() => {
              setActiveTab('webhooks');
              setAddWhOpen(true);
            }}
            className="flex h-8 items-center gap-1.5 bg-[#263fa9] px-3 text-[12px] font-medium text-white hover:bg-[#1e3180]"
          >
            <Plus className="h-3.5 w-3.5" />
            <span>Add Webhook</span>
          </button>
        </div>
      </div>

      {/* Sub-navigation tabs */}
      <div className="flex items-center border-b border-border">
        <button
          type="button"
          onClick={() => setActiveTab('webhooks')}
          className={cn(
            'border-b-2 px-4 py-2 text-[12.5px] transition-colors',
            activeTab === 'webhooks'
              ? 'border-[#263fa9] text-foreground font-medium'
              : 'border-transparent text-muted-foreground hover:text-foreground',
          )}
        >
          Webhook Endpoints ({webhooks.length})
        </button>
        <button
          type="button"
          onClick={() => setActiveTab('otel')}
          className={cn(
            'border-b-2 px-4 py-2 text-[12.5px] transition-colors',
            activeTab === 'otel'
              ? 'border-[#263fa9] text-foreground font-medium'
              : 'border-transparent text-muted-foreground hover:text-foreground',
          )}
        >
          OpenTelemetry OTLP Exporter
        </button>
        <button
          type="button"
          onClick={() => setActiveTab('audit')}
          className={cn(
            'border-b-2 px-4 py-2 text-[12.5px] transition-colors',
            activeTab === 'audit'
              ? 'border-[#263fa9] text-foreground font-medium'
              : 'border-transparent text-muted-foreground hover:text-foreground',
          )}
        >
          Live Audit Stream ({auditEvents.length})
        </button>
      </div>

      {/* TAB 1: Webhooks */}
      {activeTab === 'webhooks' && (
        <div className="space-y-4">
          {addWhOpen && (
            <form
              onSubmit={handleCreateWebhook}
              className="border border-border bg-muted/15 p-4 space-y-3"
            >
              <div className="flex items-center justify-between">
                <span className="text-[13px] font-medium text-foreground">
                  Register Outbound Webhook Subscription
                </span>
                <button
                  type="button"
                  aria-label="Close webhook form"
                  onClick={() => setAddWhOpen(false)}
                  className="text-muted-foreground hover:text-foreground"
                >
                  <X className="h-4 w-4" />
                </button>
              </div>
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
                <div>
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    Target HTTPS URL
                  </label>
                  <input
                    type="url"
                    value={whUrl}
                    onChange={(e) => setWhUrl(e.target.value)}
                    placeholder="https://api.example.com/webhooks/compute"
                    required
                    className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px] text-foreground"
                  />
                </div>
                <div>
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    Description
                  </label>
                  <input
                    type="text"
                    value={whDesc}
                    onChange={(e) => setWhDesc(e.target.value)}
                    placeholder="Lifecycle notifications"
                    className="h-8 w-full border border-border bg-background px-2.5 text-[12px] text-foreground"
                  />
                </div>
                <div>
                  <label className="block text-[11px] text-muted-foreground mb-1">
                    HMAC Signing Secret (auto-generated if empty)
                  </label>
                  <input
                    type="text"
                    value={whSecret}
                    onChange={(e) => setWhSecret(e.target.value)}
                    placeholder="whsec_..."
                    className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px] text-foreground"
                  />
                </div>
              </div>

              <div className="space-y-1.5">
                <span className="text-[11px] text-muted-foreground">
                  Subscribed Event Topics
                </span>
                <div className="flex flex-wrap gap-1.5">
                  {AVAILABLE_EVENT_TYPES.map((ev) => {
                    const checked = whEvents.includes(ev);
                    return (
                      <button
                        key={ev}
                        type="button"
                        onClick={() =>
                          setWhEvents((prev) =>
                            checked
                              ? prev.filter((x) => x !== ev)
                              : [...prev, ev],
                          )
                        }
                        className={cn(
                          'border px-2 py-0.5 font-mono text-[11px] transition-colors',
                          checked
                            ? 'border-[#263fa9] bg-[#0f1634] text-white'
                            : 'border-border bg-background text-muted-foreground',
                        )}
                      >
                        {ev}
                      </button>
                    );
                  })}
                </div>
              </div>

              <div className="flex justify-end gap-2">
                <button
                  type="button"
                  onClick={() => setAddWhOpen(false)}
                  className="h-8 border border-border px-3 text-[12px] text-muted-foreground"
                >
                  Cancel
                </button>
                <button
                  type="submit"
                  className="h-8 bg-[#263fa9] px-3 text-[12px] font-medium text-white"
                >
                  Save Endpoint
                </button>
              </div>
            </form>
          )}

          <div className="border border-border bg-background overflow-x-auto">
            <table className="w-full border-collapse text-left text-[12px]">
              <thead>
                <tr className="border-b border-border bg-muted/30 text-[11.5px] text-muted-foreground">
                  <th className="py-2.5 px-3 font-normal">Endpoint URL</th>
                  <th className="py-2.5 px-3 font-normal">Events</th>
                  <th className="py-2.5 px-3 font-normal">Signing Secret</th>
                  <th className="py-2.5 px-3 font-normal">Last Delivery</th>
                  <th className="py-2.5 px-3 font-normal">Status</th>
                  <th className="py-2.5 px-3 text-right font-normal">
                    Actions
                  </th>
                </tr>
              </thead>
              {webhooks.length === 0 ? (
                <tbody>
                  <tr>
                    <td
                      colSpan={6}
                      className="py-16 text-center text-muted-foreground"
                    >
                      <Radio className="mx-auto h-7 w-7 text-muted-foreground/50 mb-2" />
                      <div className="text-[12.5px]">
                        No outbound webhook endpoints configured yet.
                      </div>
                      <p className="text-[11.5px] text-muted-foreground/80 mt-1">
                        Click &ldquo;Add Webhook&rdquo; to subscribe to sandbox
                        and job lifecycle events.
                      </p>
                    </td>
                  </tr>
                </tbody>
              ) : (
                <tbody className="divide-y divide-border font-mono text-[11.5px]">
                  {webhooks.map((wh) => {
                    const revealed = Boolean(revealedSecrets[wh.id]);
                    return (
                      <tr key={wh.id} className="hover:bg-muted/30">
                        <td className="py-2.5 px-3">
                          <div className="text-foreground font-medium">
                            {wh.url}
                          </div>
                          {wh.description && (
                            <div className="font-sans text-[11px] text-muted-foreground">
                              {wh.description}
                            </div>
                          )}
                        </td>
                        <td className="py-2.5 px-3">
                          <div className="flex flex-wrap gap-1">
                            {(wh.events || []).map((ev) => (
                              <span
                                key={ev}
                                className="border border-border bg-muted/30 px-1.5 py-0.2 text-[10px]"
                              >
                                {ev}
                              </span>
                            ))}
                          </div>
                        </td>
                        <td className="py-2.5 px-3">
                          <div className="inline-flex items-center gap-1.5">
                            <span>
                              {revealed ? wh.secret : 'whsec_••••••••••••'}
                            </span>
                            <button
                              type="button"
                              aria-label={revealed ? 'Hide signing secret' : 'Reveal signing secret'}
                              title={revealed ? 'Hide signing secret' : 'Reveal signing secret'}
                              onClick={() =>
                                setRevealedSecrets((prev) => ({
                                  ...prev,
                                  [wh.id]: !revealed,
                                }))
                              }
                              className="text-muted-foreground hover:text-foreground"
                            >
                              {revealed ? (
                                <EyeOff className="h-3 w-3" />
                              ) : (
                                <Eye className="h-3 w-3" />
                              )}
                            </button>
                            <button
                              type="button"
                              aria-label="Copy signing secret"
                              title="Copy signing secret"
                              onClick={() => {
                                void navigator.clipboard.writeText(wh.secret);
                                setCopiedId(wh.id);
                                setTimeout(() => setCopiedId(null), 1500);
                              }}
                              className="text-muted-foreground hover:text-foreground"
                            >
                              {copiedId === wh.id ? (
                                <Check className="h-3 w-3 text-success" />
                              ) : (
                                <Copy className="h-3 w-3" />
                              )}
                            </button>
                          </div>
                        </td>
                        <td className="py-2.5 px-3 text-muted-foreground">
                          {wh.last_delivery || 'Never'} (
                          {wh.deliveries_count || 0} sent)
                        </td>
                        <td className="py-2.5 px-3">
                          <span
                            className={cn(
                              'border px-1.5 py-0.5 text-[10px] uppercase',
                              wh.active
                                ? 'border-success/40 bg-success/10 text-success'
                                : 'border-border bg-muted/40 text-muted-foreground',
                            )}
                          >
                            {wh.active ? 'active' : 'paused'}
                          </span>
                        </td>
                        <td className="py-2.5 px-3 text-right">
                          <div className="inline-flex items-center gap-1">
                            <button
                              type="button"
                              onClick={() => void handleTestWebhook(wh)}
                              className="inline-flex items-center gap-1 border border-border px-2 py-0.5 text-[11px] text-foreground hover:border-[#263fa9]"
                            >
                              <Send className="h-3 w-3" />
                              <span>Ping</span>
                            </button>
                            <button
                              type="button"
                              onClick={() => {
                                void api
                                  .updateWebhook(wh.id, { active: !wh.active })
                                  .then(() => void loadAll());
                              }}
                              className="border border-border px-2 py-0.5 text-[11px] text-muted-foreground hover:text-foreground"
                            >
                              {wh.active ? 'Disable' : 'Enable'}
                            </button>
                            <button
                              type="button"
                              aria-label="Delete webhook"
                              title="Delete webhook"
                              onClick={() => {
                                void api
                                  .deleteWebhook(wh.id)
                                  .then(() => {
                                    toast.success('Deleted webhook');
                                    void loadAll();
                                  })
                                  .catch((err) =>
                                    toast.error(errorMessage(err)),
                                  );
                              }}
                              className="border border-border p-1 text-muted-foreground hover:text-destructive"
                            >
                              <Trash2 className="h-3 w-3" />
                            </button>
                          </div>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              )}
            </table>
          </div>
        </div>
      )}

      {/* TAB 2: OpenTelemetry OTLP Exporter */}
      {activeTab === 'otel' && (
        <form
          onSubmit={handleSaveOTel}
          className="max-w-2xl border border-border bg-background p-5 space-y-4"
        >
          <div className="flex items-center justify-between border-b border-border pb-3">
            <div>
              <h2 className="text-[14px] font-medium text-foreground">
                OpenTelemetry OTLP Pipeline
              </h2>
              <p className="text-[11.5px] text-muted-foreground">
                Export Firecracker VMM spans, scheduler latency histograms, and
                guest metrics via OTLP.
              </p>
            </div>
            <span className="font-mono text-[11px] text-muted-foreground">
              Exported spans: {otel.spans_exported || 0}
            </span>
          </div>

          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <div className="sm:col-span-2">
              <label className="block text-[11.5px] text-muted-foreground mb-1">
                OTLP Collector Endpoint
              </label>
              <input
                type="text"
                value={otel.endpoint}
                onChange={(e) =>
                  setOtel((prev) => ({ ...prev, endpoint: e.target.value }))
                }
                className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px] text-foreground"
              />
            </div>

            <div>
              <label className="block text-[11.5px] text-muted-foreground mb-1">
                Transport Protocol
              </label>
              <select
                aria-label="OTLP protocol"
                value={otel.protocol}
                onChange={(e) =>
                  setOtel((prev) => ({ ...prev, protocol: e.target.value }))
                }
                className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px] text-foreground"
              >
                <option value="HTTP/JSON (4318)">
                  HTTP/JSON (4318)
                </option>
              </select>
            </div>

            <div>
              <label className="block text-[11.5px] text-muted-foreground mb-1">
                Service Name Attribute
              </label>
              <input
                type="text"
                value={otel.service_name}
                onChange={(e) =>
                  setOtel((prev) => ({ ...prev, service_name: e.target.value }))
                }
                className="h-8 w-full border border-border bg-background px-2.5 font-mono text-[12px] text-foreground"
              />
            </div>
          </div>

          <div className="flex flex-wrap items-center gap-6 pt-1 text-[12px]">
            <label className="inline-flex items-center gap-2 cursor-pointer">
              <input
                type="checkbox"
                checked={otel.traces_enabled}
                onChange={(e) =>
                  setOtel((prev) => ({
                    ...prev,
                    traces_enabled: e.target.checked,
                  }))
                }
                className="accent-[#263fa9]"
              />
              <span>Traces</span>
            </label>
            <label className="inline-flex items-center gap-2 cursor-pointer">
              <input
                type="checkbox"
                checked={otel.metrics_enabled}
                onChange={(e) =>
                  setOtel((prev) => ({
                    ...prev,
                    metrics_enabled: e.target.checked,
                  }))
                }
                className="accent-[#263fa9]"
              />
              <span>Metrics</span>
            </label>
            <label className="inline-flex items-center gap-2 cursor-pointer">
              <input
                type="checkbox"
                checked={otel.logs_enabled}
                onChange={(e) =>
                  setOtel((prev) => ({
                    ...prev,
                    logs_enabled: e.target.checked,
                  }))
                }
                className="accent-[#263fa9]"
              />
              <span>Logs</span>
            </label>
          </div>

          <div className="flex items-center justify-between border-t border-border pt-3">
            <button
              type="button"
              onClick={() => void handleTestOTel()}
              className="flex h-8 items-center gap-1.5 border border-border bg-background px-3 text-[12px] text-foreground hover:border-[#263fa9]"
            >
              <Play className="h-3 w-3" />
              <span>Send Test Span</span>
            </button>
            <button
              type="submit"
              className="h-8 bg-[#263fa9] px-4 text-[12px] font-medium text-white hover:bg-[#1e3180]"
            >
              Save OTel Config
            </button>
          </div>
        </form>
      )}

      {/* TAB 3: Live Audit Event Stream */}
      {activeTab === 'audit' && (
        <div className="space-y-3">
          <div className="flex items-center justify-between gap-2">
            <div className="relative w-72">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
              <input
                type="text"
                value={auditSearch}
                onChange={(e) => setAuditSearch(e.target.value)}
                placeholder="Search event, actor, target…"
                className="h-8 w-full border border-border bg-background pl-8 pr-3 text-[12px] text-foreground placeholder:text-muted-foreground focus:border-[#263fa9] focus:outline-none"
              />
            </div>
            <span className="flex items-center gap-1.5 font-mono text-[11px] text-success">
              <Activity className="h-3.5 w-3.5" />
              <span>SSE STREAM CONNECTED</span>
            </span>
          </div>

          <div className="border border-border bg-background overflow-x-auto">
            <table className="w-full border-collapse text-left text-[12px]">
              <thead>
                <tr className="border-b border-border bg-muted/30 text-[11.5px] text-muted-foreground">
                  <th className="py-2 px-3 font-normal">Event ID</th>
                  <th className="py-2 px-3 font-normal">Topic</th>
                  <th className="py-2 px-3 font-normal">Actor</th>
                  <th className="py-2 px-3 font-normal">Target</th>
                  <th className="py-2 px-3 font-normal">Status</th>
                  <th className="py-2 px-3 font-normal">Timestamp</th>
                </tr>
              </thead>
              {filteredEvents.length === 0 ? (
                <tbody>
                  <tr>
                    <td
                      colSpan={6}
                      className="py-12 text-center text-muted-foreground"
                    >
                      No audit events recorded yet. Perform any sandbox or
                      control-plane action to stream events live.
                    </td>
                  </tr>
                </tbody>
              ) : (
                <tbody className="divide-y divide-border font-mono text-[11.5px]">
                  {filteredEvents.map((ev) => (
                    <tr
                      key={ev.id}
                      onClick={() => setSelectedEvent(ev)}
                      className="cursor-pointer hover:bg-muted/30"
                    >
                      <td className="py-2 px-3 text-muted-foreground">
                        {ev.id}
                      </td>
                      <td className="py-2 px-3 text-foreground font-medium">
                        {ev.event}
                      </td>
                      <td className="py-2 px-3 text-muted-foreground">
                        {ev.actor}
                      </td>
                      <td className="py-2 px-3 text-foreground">{ev.target}</td>
                      <td className="py-2 px-3">
                        <span className="border border-success/40 bg-success/10 px-1.5 py-0.5 text-[10px] uppercase text-success">
                          {ev.status || 'recorded'}
                        </span>
                      </td>
                      <td className="py-2 px-3 text-muted-foreground">
                        {formatTimestamp(ev.timestamp)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              )}
            </table>
          </div>

          {selectedEvent && (
            <div className="border border-border bg-[#060606] p-3.5 space-y-2 font-mono text-[11.5px]">
              <div className="flex items-center justify-between text-muted-foreground">
                <span>
                  Event Payload: <strong className="text-white">{selectedEvent.id}</strong> ({selectedEvent.event})
                </span>
                <button
                  type="button"
                  onClick={() => setSelectedEvent(null)}
                  className="text-muted-foreground hover:text-white"
                >
                  <X className="h-4 w-4" />
                </button>
              </div>
              <pre className="overflow-x-auto text-[#fefefe]">
                {selectedEvent.payload || '{}'}
              </pre>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
