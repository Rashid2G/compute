import { useCallback, useEffect, useRef, useState } from 'react';

import { api } from '@/lib/api';
import type {
  AdminResourcesResponse,
  AuditEventRecord,
  ClusterSchedulingPolicy,
  DaemonConfig,
  ImageListItem,
  JobRecord,
  NetworkAllocation,
  NetworkConfig,
  PortForwardRecord,
  QueueResult,
  StatsResult,
  Template,
  VM,
  VolumeRecord,
  WorkerNodeRecord,
  WorkerPoolRecord,
  WorkerTokenRecord,
} from '@/lib/types';

export interface ClusterSnapshot {
  stats: StatsResult | null;
  vms: VM[];
  allocations: NetworkAllocation[];
  networkConfig: NetworkConfig | null;
  config: DaemonConfig | null;
  queue: QueueResult | null;
  workers: WorkerNodeRecord[];
  pools: WorkerPoolRecord[];
  workerTokens: WorkerTokenRecord[];
  schedulingPolicy: ClusterSchedulingPolicy | null;
  resources: AdminResourcesResponse | null;
  templates: Template[];
  images: ImageListItem[];
  portForwards: PortForwardRecord[];
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

export function useCluster(options: { enabled?: boolean } = {}): ClusterSnapshot {
  const enabled = options.enabled ?? true;
  const [stats, setStats] = useState<StatsResult | null>(null);
  const [vms, setVMs] = useState<VM[]>([]);
  const [allocations, setAllocations] = useState<NetworkAllocation[]>([]);
  const [networkConfig, setNetworkConfig] = useState<NetworkConfig | null>(null);
  const [config, setConfig] = useState<DaemonConfig | null>(null);
  const [queue, setQueue] = useState<QueueResult | null>(null);
  const [workers, setWorkers] = useState<WorkerNodeRecord[]>([]);
  const [pools, setPools] = useState<WorkerPoolRecord[]>([]);
  const [workerTokens, setWorkerTokens] = useState<WorkerTokenRecord[]>([]);
  const [schedulingPolicy, setSchedulingPolicy] =
    useState<ClusterSchedulingPolicy | null>(null);
  const [resources, setResources] = useState<AdminResourcesResponse | null>(
    null,
  );
  const [templates, setTemplates] = useState<Template[]>([]);
  const [images, setImages] = useState<ImageListItem[]>([]);
  const [portForwards, setPortForwards] = useState<PortForwardRecord[]>([]);
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
  const inFlightPromise = useRef<Promise<void> | null>(null);
  const queuedRefresh = useRef(false);
  const enabledRef = useRef(enabled);
  enabledRef.current = enabled;
  const wsConnectedRef = useRef(false);
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
      pools?: WorkerPoolRecord[];
      worker_tokens?: WorkerTokenRecord[];
      scheduling_policy?: ClusterSchedulingPolicy;
      resources?: AdminResourcesResponse;
      templates?: Template[];
      images?: ImageListItem[];
      port_forwards?: PortForwardRecord[];
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
      if (data.pools) setPools(data.pools);
      if (data.worker_tokens) setWorkerTokens(data.worker_tokens);
      if (data.scheduling_policy) setSchedulingPolicy(data.scheduling_policy);
      if (data.resources) setResources(data.resources);
      if (data.templates) setTemplates(data.templates);
      if (data.images) setImages(data.images);
      if (data.port_forwards) setPortForwards(data.port_forwards);
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

  const applySnapshotDataRef = useRef(applySnapshotData);
  applySnapshotDataRef.current = applySnapshotData;

  const refresh = useCallback(
    async (silent = false) => {
      if (!enabled) return;

      if (wsRef.current && wsRef.current.readyState === WebSocket.OPEN) {
        try {
          wsRef.current.send(JSON.stringify({ type: 'refresh' }));
          return;
        } catch {
          // Fall through to HTTP fetch if send failed
        }
      }

      if (inFlight.current) {
        queuedRefresh.current = true;
        return inFlightPromise.current ?? Promise.resolve();
      }
      inFlight.current = true;
      if (!silent) setIsRefreshing(true);

      const run = (async () => {
        try {
          do {
            queuedRefresh.current = false;
            const [
              health,
              statsRes,
              vmsRes,
              allocRes,
              netRes,
              configRes,
              queueRes,
              workersRes,
              poolsRes,
              tokensRes,
              policyRes,
              resourcesRes,
              templatesRes,
              imagesRes,
              portForwardsRes,
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
              api.listPools(),
              api.listWorkerTokens(),
              api.getSchedulingPolicy(),
              api.adminResources(),
              api.listTemplates(),
              api.listImages(),
              api.listPortForwards(),
              api.listJobs(),
              api.listVolumes(),
              api.listEvents(),
            ]);

            if (health.status === 'fulfilled') {
              setDaemonOnline(true);
              setLatency(health.value.latencyMs);
            } else if (!wsConnectedRef.current) {
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
            if (poolsRes.status === 'fulfilled') setPools(poolsRes.value);
            if (tokensRes.status === 'fulfilled') setWorkerTokens(tokensRes.value);
            if (policyRes.status === 'fulfilled') setSchedulingPolicy(policyRes.value);
            if (resourcesRes.status === 'fulfilled') setResources(resourcesRes.value);
            if (templatesRes.status === 'fulfilled') setTemplates(templatesRes.value);
            if (imagesRes.status === 'fulfilled') setImages(imagesRes.value);
            if (portForwardsRes.status === 'fulfilled') setPortForwards(portForwardsRes.value);
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
                poolsRes,
                templatesRes,
                jobsRes,
                volumesRes,
                eventsRes,
              ].some((result) => result.status === 'fulfilled')
            ) {
              setLastUpdated(new Date());
            }
          } while (queuedRefresh.current && enabledRef.current);
        } finally {
          inFlight.current = false;
          inFlightPromise.current = null;
          setIsLoading(false);
          setIsRefreshing(false);
        }
      })();

      inFlightPromise.current = run;
      return run;
    },
    [enabled],
  );

  const refreshRef = useRef(refresh);
  refreshRef.current = refresh;

  useEffect(() => {
    if (!enabled) {
      wsConnectedRef.current = false;
      setWsConnected(false);
      setIsLoading(false);
      if (wsRef.current) {
        try {
          wsRef.current.close();
        } catch {
          // Ignore
        }
        wsRef.current = null;
      }
      return;
    }

    let unmounted = false;
    let reconnectTimer: number | null = null;
    let fallbackTimer: number | null = null;
    let retryAttempt = 0;
    let receivedSnapshot = false;

    function triggerFallbackRefresh() {
      if (unmounted) return;
      if (!wsRef.current || wsRef.current.readyState !== WebSocket.OPEN) {
        void refreshRef.current(true);
      }
    }

    function connect() {
      if (unmounted) return;

      const wsProtocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const token = api.getAuthToken();
      const wsUrl = `${wsProtocol}//${window.location.host}/api/v1/ws${token ? `?token=${encodeURIComponent(token)}` : ''}`;

      try {
        const ws = new WebSocket(wsUrl);
        wsRef.current = ws;

        ws.onopen = () => {
          if (unmounted) return;
          if (fallbackTimer !== null) {
            clearTimeout(fallbackTimer);
            fallbackTimer = null;
          }
          retryAttempt = 0;
          wsConnectedRef.current = true;
          setWsConnected(true);
          setDaemonOnline(true);
          setIsLoading(false);
        };

        ws.onmessage = (event) => {
          if (unmounted) return;
          try {
            const data = JSON.parse(event.data);
            receivedSnapshot = true;
            applySnapshotDataRef.current(data);
          } catch {
            // Ignore malformed WS frames
          }
        };

        ws.onclose = () => {
          if (unmounted) return;
          wsConnectedRef.current = false;
          setWsConnected(false);
          if (wsRef.current === ws) {
            wsRef.current = null;
          }
          if (!receivedSnapshot) {
            if (fallbackTimer !== null) {
              clearTimeout(fallbackTimer);
              fallbackTimer = null;
            }
            void refreshRef.current(true);
          }
          const delay = Math.min(10000, 1000 * Math.pow(1.5, retryAttempt));
          retryAttempt += 1;
          reconnectTimer = window.setTimeout(connect, delay);
        };

        ws.onerror = () => {
          if (unmounted) return;
          wsConnectedRef.current = false;
          setWsConnected(false);
          try {
            ws.close();
          } catch {
            // Ignore
          }
        };
      } catch {
        if (!unmounted) {
          if (!receivedSnapshot) {
            void refreshRef.current(true);
          }
          const delay = Math.min(10000, 1000 * Math.pow(1.5, retryAttempt));
          retryAttempt += 1;
          reconnectTimer = window.setTimeout(connect, delay);
        }
      }
    }

    setIsLoading(true);
    fallbackTimer = window.setTimeout(triggerFallbackRefresh, 400);
    connect();

    return () => {
      unmounted = true;
      if (reconnectTimer !== null) clearTimeout(reconnectTimer);
      if (fallbackTimer !== null) clearTimeout(fallbackTimer);
      wsConnectedRef.current = false;
      if (wsRef.current) {
        try {
          wsRef.current.close();
        } catch {
          // Ignore
        }
        wsRef.current = null;
      }
    };
  }, [enabled]);

  return {
    stats,
    vms,
    allocations,
    networkConfig,
    config,
    queue,
    workers,
    pools,
    workerTokens,
    schedulingPolicy,
    resources,
    templates,
    images,
    portForwards,
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
