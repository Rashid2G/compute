import { useCallback, useEffect, useRef, useState } from 'react';

import { api } from '@/lib/api';
import type {
  AuditEventRecord,
  DaemonConfig,
  JobRecord,
  NetworkAllocation,
  NetworkConfig,
  QueueResult,
  StatsResult,
  VM,
  VolumeRecord,
  WorkerNodeRecord,
} from '@/lib/types';

export interface ClusterSnapshot {
  stats: StatsResult | null;
  vms: VM[];
  allocations: NetworkAllocation[];
  networkConfig: NetworkConfig | null;
  config: DaemonConfig | null;
  queue: QueueResult | null;
  workers: WorkerNodeRecord[];
  jobs: JobRecord[];
  volumes: VolumeRecord[];
  events: AuditEventRecord[];
  daemonOnline: boolean;
  latency: number | null;
  isLoading: boolean;
  isRefreshing: boolean;
  wsConnected: boolean;
  lastUpdated: Date | null;
  refresh: (silent?: boolean) => Promise<void>;
}

export function useCluster(): ClusterSnapshot {
  const [stats, setStats] = useState<StatsResult | null>(null);
  const [vms, setVMs] = useState<VM[]>([]);
  const [allocations, setAllocations] = useState<NetworkAllocation[]>([]);
  const [networkConfig, setNetworkConfig] = useState<NetworkConfig | null>(null);
  const [config, setConfig] = useState<DaemonConfig | null>(null);
  const [queue, setQueue] = useState<QueueResult | null>(null);
  const [workers, setWorkers] = useState<WorkerNodeRecord[]>([]);
  const [jobs, setJobs] = useState<JobRecord[]>([]);
  const [volumes, setVolumes] = useState<VolumeRecord[]>([]);
  const [events, setEvents] = useState<AuditEventRecord[]>([]);
  const [daemonOnline, setDaemonOnline] = useState(true);
  const [latency, setLatency] = useState<number | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const [isRefreshing, setIsRefreshing] = useState(false);
  const [wsConnected, setWsConnected] = useState(false);
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null);

  const inFlight = useRef(false);
  const wsRef = useRef<WebSocket | null>(null);

  const applySnapshotData = useCallback(
    (data: {
      stats?: StatsResult;
      vms?: VM[];
      allocations?: NetworkAllocation[];
      network_config?: NetworkConfig;
      networkConfig?: NetworkConfig;
      config?: DaemonConfig;
      queue?: QueueResult;
      workers?: WorkerNodeRecord[];
      jobs?: JobRecord[];
      volumes?: VolumeRecord[];
      events?: AuditEventRecord[];
      timestamp?: string;
    }) => {
      if (data.stats) setStats(data.stats);
      if (data.vms) setVMs(data.vms);
      if (data.allocations) setAllocations(data.allocations);
      if (data.network_config || data.networkConfig) {
        setNetworkConfig(data.network_config || data.networkConfig || null);
      }
      if (data.config) setConfig(data.config);
      if (data.queue) setQueue(data.queue);
      if (data.workers) setWorkers(data.workers);
      if (data.jobs) setJobs(data.jobs);
      if (data.volumes) setVolumes(data.volumes);
      if (data.events) setEvents(data.events);
      setLastUpdated(new Date());
      setDaemonOnline(true);
      setIsLoading(false);

      if (data.timestamp) {
        const serverTime = new Date(data.timestamp).getTime();
        const now = Date.now();
        const diff = Math.max(1, now - serverTime);
        setLatency(diff < 2000 ? diff : 2);
      }
    },
    [],
  );

  const refresh = useCallback(
    async (silent = false) => {
      if (wsRef.current && wsRef.current.readyState === WebSocket.OPEN) {
        try {
          wsRef.current.send(JSON.stringify({ type: 'refresh' }));
        } catch {
          // Fall through to HTTP fetch
        }
      }

      if (inFlight.current) return;
      inFlight.current = true;
      if (!silent) setIsRefreshing(true);

      try {
        const [
          health,
          statsRes,
          vmsRes,
          allocRes,
          netRes,
          configRes,
          queueRes,
          workersRes,
          jobsRes,
          volumesRes,
          eventsRes,
        ] = await Promise.allSettled([
          api.health(),
          api.stats(),
          api.listVMs(),
          api.networkAllocations(),
          api.networkConfig(),
          api.config(),
          api.queue(),
          api.listWorkers(),
          api.listJobs(),
          api.listVolumes(),
          api.listEvents(),
        ]);

        if (health.status === 'fulfilled') {
          setDaemonOnline(true);
          setLatency(health.value.latencyMs);
        } else if (!wsConnected) {
          setDaemonOnline(false);
          setLatency(null);
        }

        if (statsRes.status === 'fulfilled') setStats(statsRes.value);
        if (vmsRes.status === 'fulfilled') setVMs(vmsRes.value);
        if (allocRes.status === 'fulfilled') setAllocations(allocRes.value);
        if (netRes.status === 'fulfilled') setNetworkConfig(netRes.value);
        if (configRes.status === 'fulfilled') setConfig(configRes.value);
        if (queueRes.status === 'fulfilled') setQueue(queueRes.value);
        if (workersRes.status === 'fulfilled') setWorkers(workersRes.value);
        if (jobsRes.status === 'fulfilled') setJobs(jobsRes.value);
        if (volumesRes.status === 'fulfilled') setVolumes(volumesRes.value);
        if (eventsRes.status === 'fulfilled') setEvents(eventsRes.value);

        if (
          [
            statsRes,
            vmsRes,
            allocRes,
            netRes,
            configRes,
            queueRes,
            workersRes,
            jobsRes,
            volumesRes,
            eventsRes,
          ].some((result) => result.status === 'fulfilled')
        ) {
          setLastUpdated(new Date());
        }
      } finally {
        inFlight.current = false;
        setIsLoading(false);
        setIsRefreshing(false);
      }
    },
    [wsConnected],
  );

  useEffect(() => {
    let unmounted = false;
    let reconnectTimer: number | null = null;
    let retryAttempt = 0;

    function connect() {
      if (unmounted) return;

      const wsProtocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const wsUrl = `${wsProtocol}//${window.location.host}/api/v1/ws`;

      try {
        const ws = new WebSocket(wsUrl);
        wsRef.current = ws;

        ws.onopen = () => {
          if (unmounted) return;
          retryAttempt = 0;
          setWsConnected(true);
          setDaemonOnline(true);
          setIsLoading(false);
        };

        ws.onmessage = (event) => {
          if (unmounted) return;
          try {
            const data = JSON.parse(event.data);
            applySnapshotData(data);
          } catch {
            // Ignore malformed WS frames
          }
        };

        ws.onclose = () => {
          if (unmounted) return;
          setWsConnected(false);
          wsRef.current = null;
          const delay = Math.min(10000, 1000 * Math.pow(1.5, retryAttempt));
          retryAttempt += 1;
          reconnectTimer = window.setTimeout(connect, delay);
        };

        ws.onerror = () => {
          if (unmounted) return;
          setWsConnected(false);
          try {
            ws.close();
          } catch {
            // Ignore
          }
        };
      } catch {
        if (!unmounted) {
          const delay = Math.min(10000, 1000 * Math.pow(1.5, retryAttempt));
          retryAttempt += 1;
          reconnectTimer = window.setTimeout(connect, delay);
        }
      }
    }

    void refresh(true);
    connect();

    return () => {
      unmounted = true;
      if (reconnectTimer) clearTimeout(reconnectTimer);
      if (wsRef.current) {
        try {
          wsRef.current.close();
        } catch {
          // Ignore
        }
      }
    };
  }, [applySnapshotData, refresh]);

  return {
    stats,
    vms,
    allocations,
    networkConfig,
    config,
    queue,
    workers,
    jobs,
    volumes,
    events,
    daemonOnline,
    latency,
    isLoading,
    isRefreshing,
    wsConnected,
    lastUpdated,
    refresh,
  };
}
