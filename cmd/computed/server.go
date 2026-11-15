package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"

	"github.com/Rashid2G/compute/internal/config"
	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/doctor"
	"github.com/Rashid2G/compute/internal/jobs"
	"github.com/Rashid2G/compute/internal/master/auth"
	"github.com/Rashid2G/compute/internal/master/pki"
	"github.com/Rashid2G/compute/internal/network"
	"github.com/Rashid2G/compute/internal/scheduler"
	"github.com/Rashid2G/compute/internal/store"
	storeDB "github.com/Rashid2G/compute/internal/store/db"
	storedb "github.com/Rashid2G/compute/internal/store/db"
	"github.com/Rashid2G/compute/internal/vm"
	"github.com/Rashid2G/compute/pkg/api"
	"github.com/Rashid2G/compute/pkg/version"
	"github.com/coder/websocket"
	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

type Server struct {
	vmMgr        *vm.Manager
	cfg          config.GlobalConfig
	mux          *http.ServeMux
	workers      *scheduler.WorkerRegistry
	scheduler    *scheduler.Scheduler
	jobRunner    *jobs.Runner
	authMgr      *auth.Manager
	metaStore    *store.Store
	fcVersion    string
	ingressProxy *network.IngressProxy
	dns          *dnsRuntime
	guiHandler   http.Handler

	// DNS/ingress caches (daemon-owned, response only).
	userIndexMu     sync.RWMutex
	userIndex       map[string]*storedb.User
	userIndexExpiry time.Time
	vmListMu        sync.RWMutex
	vmListCache     map[string]vmListEntry
	dnsHostMu       sync.RWMutex
	dnsHostIPs      []net.IP
	dnsHostExpiry   time.Time

	tailnetDomainMu     sync.RWMutex
	tailnetDomain       string
	tailnetDomainExpiry time.Time

	eventMu          sync.RWMutex
	eventSubs        map[chan *db.AuditEvent]struct{}
	portForwardMu    sync.Mutex
	resourceSampleMu sync.Mutex
	resourceSamples  resourceSampleState
}

// detectFirecrackerVersion returns the installed Firecracker version (e.g. "v1.17.0").
func detectFirecrackerVersion(bin string) string {
	if bin == "" {
		bin = "/usr/local/bin/firecracker"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return ""
	}
	for _, field := range strings.Fields(string(out)) {
		if len(field) > 1 && field[0] == 'v' && field[1] >= '0' && field[1] <= '9' {
			return field
		}
	}
	return ""
}

func NewServer(vmMgr *vm.Manager, cfg config.GlobalConfig) *Server {
	workers := scheduler.NewWorkerRegistry()
	fcVersion := detectFirecrackerVersion(cfg.Server.FirecrackerBin)
	var sched *scheduler.Scheduler
	var jobRunner *jobs.Runner
	if vmMgr != nil && vmMgr.GetStore() != nil {
		sched = scheduler.NewScheduler(workers, vmMgr.GetStore())
		if cfg.Jobs.LocalExecution {
			jobRunner = jobs.NewRunner(vmMgr, cfg.Jobs.MaxConcurrent, time.Duration(cfg.Jobs.BootTimeoutSeconds)*time.Second)
			sched.SetLocalExecutor(jobRunner)
			if n, err := vmMgr.GetStore().FailStaleJobs(context.Background()); err == nil && n > 0 {
				log.Printf("[computed] marked %d interrupted job(s) as failed", n)
			}
		}
		sched.Start(context.Background())

		st := vmMgr.GetStore()
		initCtx := context.Background()

		// 1. Ensure "default" WorkerPool exists in SQLite
		pools, _ := st.ListWorkerPools(initCtx)
		hasDefaultPool := false
		for _, p := range pools {
			if p.ID == "default" {
				hasDefaultPool = true
				break
			}
		}
		if !hasDefaultPool {
			_ = st.SaveWorkerPool(initCtx, &db.WorkerPool{
				ID:         "default",
				Name:       "default",
				LabelsJSON: `{"tier":"kvm-baremetal","region":"local"}`,
				PolicyJSON: `{}`,
			})
		}

		// 2. Register/update local host node in worker_nodes
		now := time.Now().UTC()
		hostname, err := os.Hostname()
		if err != nil || hostname == "" {
			hostname = "localhost"
		}
		status := "ready"
		if existing, err := st.GetWorkerNode(initCtx, "nod_host_local"); err == nil && existing != nil {
			if existing.Status == "cordoned" || existing.Status == "draining" {
				status = existing.Status
			}
		}
		underlayIP := "10.200.0.1"
		if cfg.Network.BridgeIP != "" {
			underlayIP = strings.Split(cfg.Network.BridgeIP, "/")[0]
		}
		hostVCPUs, hostMemMB, hostDiskMB := getHostResources(cfg.Resources.MaxTotalVCPUs, cfg.Resources.MaxTotalMemoryMB, int(cfg.Resources.MaxTotalDiskMB))
		localNode := &db.WorkerNode{
			ID:               "nod_host_local",
			Name:             hostname,
			PoolID:           "default",
			Role:             "control-plane+worker",
			Status:           status,
			UnderlayIP:       underlayIP,
			Arch:             runtime.GOARCH,
			TotalVCPUs:       hostVCPUs,
			TotalMemMB:       hostMemMB,
			TotalDiskMB:      hostDiskMB,
			LabelsJSON:       `{"kvm":"true","nvme":"true","role":"primary"}`,
			CapabilitiesJSON: `{"firecracker":true,"snapshots":true,"ebpf":true}`,
			AgentVersion:     version.Version,
			FCVersion:        fcVersion,
			LastHeartbeat:    &now,
		}
		_ = st.SaveWorkerNode(initCtx, localNode)

		// 3. Seed initial system.daemon.started audit event if audit_events is empty
		if evts, err := st.ListAuditEvents(initCtx, 1); err == nil && len(evts) == 0 {
			_ = st.SaveAuditEvent(initCtx, &db.AuditEvent{
				ID:         "evt_daemon_boot",
				Timestamp:  now,
				Event:      "system.daemon.started",
				Actor:      "system",
				Target:     hostname,
				Status:     "delivered",
				DurationMs: 12,
				Payload:    fmt.Sprintf(`{"version":"%s","arch":"%s"}`, version.Version, runtime.GOARCH),
			})
		}

		// 4. Auto-seed "base" default environment profile if environments table is empty
		if envs, err := st.ListEnvironments(initCtx); err == nil && len(envs) == 0 {
			_ = st.SaveEnvironment(initCtx, &db.EnvironmentProfile{
				Name:                "base",
				IsDefault:           true,
				Version:             1,
				SecretsEnabled:      true,
				ComputeCredsEnabled: true,
				BoatCredsEnabled:    true,
				AgentCredsEnabled:   true,
				CredentialProfile:   "Owner's default",
				GitHubReposEnabled:  false,
				GitHubRepos:         []string{},
				Secrets:             "",
				UpdatedAt:           now,
			})
		}
	}

	var authMgr *auth.Manager
	var metaStore *store.Store

	if cfg.Server.AuthEnabled || cfg.Server.AuthDBPath != "" {
		authDB := cfg.Server.AuthDBPath
		if authDB == "" {
			masterDir := filepath.Dir(cfg.Server.DBPath)
			if masterDir == "" || masterDir == "." {
				masterDir = "/var/lib/compute/master"
			}
			authDB = filepath.Join(masterDir, "auth.db")
		}
		masterDir := filepath.Dir(authDB)
		_ = os.MkdirAll(masterDir, 0700)
		masterKeyPath := filepath.Join(masterDir, "master.key")
		mk, err := pki.LoadOrCreateMasterKey(masterKeyPath)
		if err == nil {
			st, err := store.Open(authDB)
			if err == nil {
				metaStore = st
				queries := storeDB.New(st.Writer())
				adminSecret := cfg.Server.AdminSecret
				if adminSecret == "" {
					adminSecret = os.Getenv("COMPUTE_ADMIN_SECRET")
				}
				authMgr = auth.NewManager(queries, mk, 7*24*time.Hour, adminSecret)

				adminUsername := strings.TrimSpace(strings.ToLower(cfg.Server.AdminUsername))
				if adminUsername == "" {
					adminUsername = strings.TrimSpace(strings.ToLower(os.Getenv("COMPUTE_ADMIN_USERNAME")))
				}
				if adminUsername == "" {
					adminUsername = "admin"
				}
				adminEmail := strings.TrimSpace(cfg.Server.AdminEmail)
				if adminEmail == "" {
					adminEmail = strings.TrimSpace(os.Getenv("COMPUTE_ADMIN_EMAIL"))
				}
				if adminEmail == "" {
					adminEmail = "admin@compute.local"
				}
				authMgr.SetAdminIdentity(adminUsername, adminEmail)

				// Bootstrap or sync configured admin account on startup
				ctx := context.Background()
				adminPW := cfg.Server.AdminPassword
				if adminPW == "" {
					adminPW = os.Getenv("COMPUTE_ADMIN_PASSWORD")
				}
				users, _ := queries.ListUsers(ctx)
				hasAdmin := false
				adminRoleCount := 0
				for _, u := range users {
					if u == nil {
						continue
					}
					if u.Role == "admin" {
						adminRoleCount++
					}
					if u.Username == adminUsername || strings.EqualFold(u.Email, adminEmail) || u.ID == "usr_system_admin" ||
						(u.Role == "admin" && (u.Username == "admin" || strings.EqualFold(u.Email, "admin@compute.local"))) {
						hasAdmin = true
						break
					}
				}
				if !hasAdmin && adminRoleCount == 1 {
					hasAdmin = true
				}
				if !hasAdmin && adminPW == "" {
					pwBytes := make([]byte, 32)
					if _, err := rand.Read(pwBytes); err != nil {
						panic("failed to generate secure random admin password: " + err.Error())
					}
					adminPW = hex.EncodeToString(pwBytes)
					credPath := filepath.Join(masterDir, "admin-password")
					if err := os.WriteFile(credPath, []byte(adminPW+"\n"), 0600); err != nil {
						log.Printf("[computed] Warning: no admin password configured and the one-time credential file could not be written: %v", err)
					} else {
						log.Printf("[computed] SECURITY NOTICE: no admin password configured; a one-time credential was written to %s (mode 0600). Set COMPUTE_ADMIN_PASSWORD to manage it explicitly.", credPath)
					}
				}
				if _, err := authMgr.EnsureAdmin(ctx, adminEmail, adminUsername, "Administrator", adminPW); err != nil {
					log.Printf("[computed] Warning: failed to bootstrap admin account (%v)", err)
				}
			} else {
				log.Printf("[computed] Warning: failed to initialize auth store (%v)", err)
			}
		} else {
			log.Printf("[computed] Warning: failed to load master key (%v)", err)
		}
	}

	s := &Server{
		vmMgr:       vmMgr,
		cfg:         cfg,
		mux:         http.NewServeMux(),
		workers:     workers,
		scheduler:   sched,
		jobRunner:   jobRunner,
		authMgr:     authMgr,
		metaStore:   metaStore,
		fcVersion:   fcVersion,
		eventSubs:   make(map[chan *db.AuditEvent]struct{}),
		vmListCache: make(map[string]vmListEntry),
	}

	var domains []string
	if cfg.Network.BaseDomain != "" {
		domains = append(domains, cfg.Network.BaseDomain)
	}
	// The DNS zones double as ingress wildcard zones so HTTP requests to
	// <sandbox>[.<port>][.<username>].<zone> route to guest ports.
	for _, zone := range cfg.DNSBaseDomains() {
		dup := false
		for _, d := range domains {
			if strings.EqualFold(d, zone) {
				dup = true
				break
			}
		}
		if !dup {
			domains = append(domains, zone)
		}
	}
	domains = append(domains, "preview.localhost", "compute.internal", "compute.dev", "localhost")

	s.ingressProxy = network.NewIngressProxy(network.ProxyOptions{
		BaseDomains: domains,
		Resolver:    s,
		Auth: func(r *http.Request, ownerID string) bool {
			if !s.authEnabled() {
				return true
			}
			req, _, err := s.authenticateRequestIfNeeded(r)
			if err != nil {
				return false
			}
			return s.canAccessOwner(req, ownerID)
		},
	})

	s.configureAuthIntegrations()
	s.routes()
	return s
}

func (s *Server) recordEvent(event, actor, target, status string, durationMs int, payload interface{}) *db.AuditEvent {
	if actor == "" {
		actor = "system"
	}
	if target == "" {
		target = "cluster"
	}
	if status == "" {
		status = "delivered"
	}
	if durationMs <= 0 {
		durationMs = 15
	}
	payloadStr := "{}"
	if payload != nil {
		if str, ok := payload.(string); ok {
			payloadStr = str
		} else if b, err := json.Marshal(payload); err == nil {
			payloadStr = string(b)
		}
	}
	auditEvt := &db.AuditEvent{
		ID:         "evt_" + strings.ReplaceAll(uuid.NewString()[:12], "-", ""),
		Timestamp:  time.Now().UTC(),
		Event:      event,
		Actor:      actor,
		Target:     target,
		Status:     status,
		DurationMs: durationMs,
		Payload:    payloadStr,
	}

	if s.vmMgr != nil && s.vmMgr.GetStore() != nil {
		_ = s.vmMgr.GetStore().SaveAuditEvent(context.Background(), auditEvt)
	}

	s.eventMu.RLock()
	for ch := range s.eventSubs {
		select {
		case ch <- auditEvt:
		default:
		}
	}
	s.eventMu.RUnlock()

	// Fan the event out to subscribed webhook endpoints without blocking the
	// API caller.
	s.deliverWebhooks(auditEvt)

	return auditEvt
}

func (s *Server) addEventSubscriber(ch chan *db.AuditEvent) {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	if s.eventSubs == nil {
		s.eventSubs = make(map[chan *db.AuditEvent]struct{})
	}
	s.eventSubs[ch] = struct{}{}
}

func (s *Server) removeEventSubscriber(ch chan *db.AuditEvent) {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	delete(s.eventSubs, ch)
}

func getActor(r *http.Request) string {
	if r != nil {
		if a := auth.GetAuth(r.Context()); a != nil {
			if a.User != nil && a.User.Username != "" {
				return a.User.Username
			}
			if a.APIKey != nil && a.APIKey.Prefix != "" {
				return "api_key:" + a.APIKey.Prefix
			}
			if a.AuthType == "admin_secret" {
				return "admin"
			}
		}
	}
	return "system"
}

func (s *Server) SetAuthManager(mgr *auth.Manager) {
	s.authMgr = mgr
	if mgr != nil {
		mgr.SetAdminIdentity(s.cfg.Server.AdminUsername, s.cfg.Server.AdminEmail)
	}
	s.configureAuthIntegrations()
}

// volumeAttachedToVM reports whether vol is attached to v for the same owner.
// Names are not globally unique, so owner scoping is required.
func volumeAttachedToVM(vol *db.Volume, v *db.VM) bool {
	if vol == nil || v == nil || vol.OwnerID != v.OwnerID {
		return false
	}
	if vol.AttachedVM == "" {
		return false
	}
	return vol.AttachedVM == v.Name || vol.AttachedVM == v.ID || (v.InternalID != "" && vol.AttachedVM == v.InternalID)
}

// portForwardTargetsVM reports whether pf targets v for the same owner.
func portForwardTargetsVM(pf *db.PortForward, v *db.VM) bool {
	if pf == nil || v == nil || pf.OwnerID != v.OwnerID {
		return false
	}
	if pf.VMName != "" && pf.VMName == v.Name {
		return true
	}
	return pf.VMID == v.Name || pf.VMID == v.ID || (v.InternalID != "" && pf.VMID == v.InternalID)
}

// mountAttachedVolumes mounts data volumes inside the guest after boot.
func (s *Server) mountAttachedVolumes(ctx context.Context, v *db.VM) {
	if s.vmMgr == nil || v == nil {
		return
	}
	vols, err := s.vmMgr.GetStore().ListVolumes(ctx)
	if err != nil {
		return
	}
	index := 0
	for _, vol := range vols {
		if vol.Path == "" || !volumeAttachedToVM(vol, v) {
			continue
		}
		device := vol.DevicePath
		if device == "" {
			device = fmt.Sprintf("/dev/vd%c", 'b'+rune(index))
		}
		mountPoint := "/mnt/data"
		if index > 0 {
			mountPoint = fmt.Sprintf("/mnt/data-%d", index+1)
		}
		index++
		cmd := fmt.Sprintf("sudo mkdir -p %s && (mountpoint -q %s || sudo mount %s %s) && sudo chown 1000:1000 %s", mountPoint, mountPoint, device, mountPoint, mountPoint)
		res, err := s.vmMgr.ExecVM(ctx, v.ID, vm.ExecRequest{Command: cmd, Timeout: 30})
		if err != nil || res.ExitCode != 0 {
			detail := ""
			if res != nil {
				detail = res.Stderr
			}
			log.Printf("[computed] could not mount volume %s at %s on %s: %v %s", vol.Name, mountPoint, v.Name, err, detail)
			continue
		}
		if vol.MountPoint != mountPoint {
			vol.MountPoint = mountPoint
			_ = s.vmMgr.GetStore().SaveVolume(ctx, vol)
		}
	}
}

// applyPortForwardsForVM installs every host forwarding rule that belongs to v.
// It is safe to call repeatedly; AddPortForward is idempotent.
func (s *Server) applyPortForwardsForVM(ctx context.Context, v *db.VM) {
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil || s.vmMgr.NetManager() == nil || v == nil || v.IPAddress == "" {
		return
	}
	st := s.vmMgr.GetStore()
	pfs, err := st.ListPortForwards(ctx)
	if err != nil {
		log.Printf("[computed] port-forward reconcile: list failed: %v", err)
		return
	}
	for _, pf := range pfs {
		if !portForwardTargetsVM(pf, v) {
			continue
		}
		if err := s.vmMgr.NetManager().AddPortForward(ctx, pf.Protocol, pf.HostPort, v.IPAddress, pf.GuestPort); err != nil {
			log.Printf("[computed] port-forward %s (%d->%d): %v", pf.ID, pf.HostPort, pf.GuestPort, err)
			continue
		}
		if pf.Status != "active" {
			pf.Status = "active"
			_ = st.SavePortForward(ctx, pf)
		}
	}
}

// removePortForwardsForVM removes host rules for a sandbox. When deleteRecords
// is true the stored forwarding definitions are deleted too (sandbox deletion);
// otherwise they are kept as "pending" so a later start can re-install them.
func (s *Server) removePortForwardsForVM(ctx context.Context, v *db.VM, deleteRecords bool) {
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil || v == nil {
		return
	}
	st := s.vmMgr.GetStore()
	pfs, err := st.ListPortForwards(ctx)
	if err != nil {
		return
	}
	for _, pf := range pfs {
		if !portForwardTargetsVM(pf, v) {
			continue
		}
		if v.IPAddress != "" && s.vmMgr.NetManager() != nil {
			_ = s.vmMgr.NetManager().RemovePortForward(ctx, pf.Protocol, pf.HostPort, v.IPAddress, pf.GuestPort)
		}
		if deleteRecords {
			_ = st.DeletePortForward(ctx, pf.ID)
			continue
		}
		if pf.Status != "pending" {
			pf.Status = "pending"
			_ = st.SavePortForward(ctx, pf)
		}
	}
}

// ReconcileRunningVMState re-applies post-boot guest state (data volume mounts
// and environment secrets) to sandboxes that are already running, including
// sandboxes restored automatically after a daemon or host restart.
func (s *Server) ReconcileRunningVMState(ctx context.Context) {
	if s.vmMgr == nil {
		return
	}
	vms, err := s.vmMgr.ListVMs(ctx)
	if err != nil {
		return
	}
	for _, v := range vms {
		if v.Status != db.StatusRunning {
			continue
		}
		s.mountAttachedVolumes(ctx, v)
		s.applyEnvironment(ctx, v)
	}
}

// ReconcilePortForwards re-installs stored host forwarding rules at startup so
// rules survive daemon restarts and host reboots.
func (s *Server) ReconcilePortForwards(ctx context.Context) {
	if s.vmMgr == nil {
		return
	}
	vms, err := s.vmMgr.ListVMs(ctx)
	if err != nil {
		return
	}
	for _, v := range vms {
		if v.Status == db.StatusRunning {
			s.applyPortForwardsForVM(ctx, v)
		}
	}

	// Keep the UI honest: forwards whose sandbox is not running are pending.
	if pfs, err := s.vmMgr.GetStore().ListPortForwards(ctx); err == nil {
		for _, pf := range pfs {
			v, _ := s.vmMgr.GetStore().GetVMByOwner(ctx, pf.OwnerID, pf.VMID)
			if v == nil || v.Status != db.StatusRunning {
				if pf.Status != "pending" {
					pf.Status = "pending"
					_ = s.vmMgr.GetStore().SavePortForward(ctx, pf)
				}
			}
		}
	}
}

func (s *Server) wrapAuth(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.cfg.Server.AuthEnabled {
			handler(w, r)
			return
		}
		// Auth is configured: never fall back to the legacy open mode when the
		// backend failed to initialize. Fail closed with a retryable error.
		if s.authUnavailable(w) {
			return
		}
		s.authMgr.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
			if !s.enforceScope(w, r) {
				return
			}
			handler(w, r)
		})(w, r)
	}
}

func (s *Server) routes() {
	s.mux.HandleFunc("/api/v1/health", s.handleHealth)
	s.mux.HandleFunc("/api/v1/ready", s.handleReady)
	s.mux.HandleFunc("/api/v1/version", s.handleVersion)
	s.mux.HandleFunc("/api/v1/stats", s.handleStats)
	s.mux.HandleFunc("/api/v1/tiers", s.handleTiers)

	// Auth & Identity
	s.mux.HandleFunc("/api/v1/auth/status", s.handleAuthStatus)
	s.mux.HandleFunc("/api/v1/auth/signup", s.handleAuthSignup)
	s.mux.HandleFunc("/api/v1/auth/login", s.handleAuthLogin)
	s.mux.HandleFunc("/api/v1/auth/logout", s.handleAuthLogout)
	s.mux.HandleFunc("/api/v1/auth/me", s.wrapAuth(s.handleAuthMe))
	s.mux.HandleFunc("/api/v1/auth/keys", s.wrapAuth(s.handleAuthKeys))
	s.mux.HandleFunc("/api/v1/auth/keys/", s.wrapAuth(s.handleAuthKeyActions))
	s.mux.HandleFunc("/api/v1/users", s.wrapAuth(s.handleUsers))
	s.mux.HandleFunc("/api/v1/users/", s.wrapAuth(s.handleUserActions))
	s.mux.HandleFunc("/api/v1/orgs", s.wrapAuth(s.handleOrgs))
	s.mux.HandleFunc("/api/v1/orgs/", s.wrapAuth(s.handleOrgActions))
	s.mux.HandleFunc("/api/v1/limits", s.wrapAuth(s.handleLimits))

	// Sandboxes / VMs
	s.mux.HandleFunc("/api/v1/vms", s.wrapAuth(s.handleVMs))
	s.mux.HandleFunc("/api/v1/vms/", s.wrapAuth(s.handleVMActions))
	s.mux.HandleFunc("/api/v1/sandboxes", s.wrapAuth(s.handleVMs))
	s.mux.HandleFunc("/api/v1/sandboxes/", s.wrapAuth(s.handleVMActions))

	s.mux.HandleFunc("/api/v1/queue", s.wrapAuth(s.handleQueue))
	s.mux.HandleFunc("/api/v1/config", s.wrapAuth(s.handleConfig))
	s.mux.HandleFunc("/api/v1/network", s.wrapAuth(s.handleNetworkInfo))
	s.mux.HandleFunc("/api/v1/network/allocations", s.wrapAuth(s.handleNetworkAllocations))
	s.mux.HandleFunc("/api/v1/network/inspect/", s.wrapAuth(s.handleNetworkInspect))
	s.mux.HandleFunc("/api/v1/network/prune", s.wrapAuth(s.handleNetworkPrune))
	s.mux.HandleFunc("/api/v1/network/config", s.wrapAuth(s.handleNetworkConfig))
	s.mux.HandleFunc("/api/v1/images", s.wrapAuth(s.handleImages))
	s.mux.HandleFunc("/api/v1/images/", s.wrapAuth(s.handleImageActions))
	s.mux.HandleFunc("/api/v1/templates", s.wrapAuth(s.handleTemplates))
	s.mux.HandleFunc("/api/v1/templates/", s.wrapAuth(s.handleTemplateActions))
	s.mux.HandleFunc("/api/v1/keys", s.wrapAuth(s.handleKeys))
	s.mux.HandleFunc("/api/v1/keys/", s.wrapAuth(s.handleKeyActions))
	s.mux.HandleFunc("/api/v1/ws", s.wrapAuth(s.handleWS))
	s.mux.HandleFunc("/api/v1/worker/connect", s.handleWorkerConnect)
	s.mux.HandleFunc("/api/v1/worker-tokens", s.wrapAuth(s.handleWorkerTokens))
	s.mux.HandleFunc("/api/v1/worker-tokens/", s.wrapAuth(s.handleWorkerTokens))
	s.mux.HandleFunc("/api/v1/workers", s.wrapAuth(s.handleWorkers))
	s.mux.HandleFunc("/api/v1/workers/", s.wrapAuth(s.handleWorkerActions))
	s.mux.HandleFunc("/api/v1/pools", s.wrapAuth(s.handlePools))
	s.mux.HandleFunc("/api/v1/pools/", s.wrapAuth(s.handlePoolActions))
	s.mux.HandleFunc("/api/v1/jobs", s.wrapAuth(s.handleJobs))
	s.mux.HandleFunc("/api/v1/jobs/", s.wrapAuth(s.handleJobActions))
	s.mux.HandleFunc("/api/v1/ci/repos", s.wrapAuth(s.handleCIRepos))
	s.mux.HandleFunc("/api/v1/ci/repos/", s.wrapAuth(s.handleCIRepoActions))
	s.mux.HandleFunc("/api/v1/ci/webhooks/github", s.handleGitHubWebhook)
	s.mux.HandleFunc("/api/v1/ssh/", s.wrapAuth(s.handleSSHDirect))
	s.mux.HandleFunc("/preview/", s.handlePreviewProxy)
	s.mux.HandleFunc("/", s.handleRoot)

	// Volumes
	s.mux.HandleFunc("/api/v1/volumes", s.wrapAuth(s.handleVolumes))
	s.mux.HandleFunc("/api/v1/volumes/", s.wrapAuth(s.handleVolumeActions))

	// Webhooks
	s.mux.HandleFunc("/api/v1/webhooks", s.wrapAuth(s.handleWebhooks))
	s.mux.HandleFunc("/api/v1/webhooks/", s.wrapAuth(s.handleWebhookActions))

	// OTel
	s.mux.HandleFunc("/api/v1/otel", s.wrapAuth(s.handleOTel))
	s.mux.HandleFunc("/api/v1/otel/test", s.wrapAuth(s.handleOTelTest))

	// Audit Events & SSE Stream
	s.mux.HandleFunc("/api/v1/events", s.wrapAuth(s.handleEvents))
	s.mux.HandleFunc("/api/v1/events/stream", s.wrapAuth(s.handleEventsStream))

	// Scoped Access API Keys
	s.mux.HandleFunc("/api/v1/api-keys", s.wrapAuth(s.handleAccessAPIKeys))
	s.mux.HandleFunc("/api/v1/api-keys/", s.wrapAuth(s.handleAccessAPIKeyActions))

	// Environments
	s.mux.HandleFunc("/api/v1/environments", s.wrapAuth(s.handleEnvironments))
	s.mux.HandleFunc("/api/v1/environments/", s.wrapAuth(s.handleEnvironmentActions))

	// Admin Resource Monitoring
	s.mux.HandleFunc("/api/v1/admin/resources", s.wrapAuth(s.handleAdminResources))

	// Port Forwards
	s.mux.HandleFunc("/api/v1/network/port-forwards", s.wrapAuth(s.handlePortForwards))
	s.mux.HandleFunc("/api/v1/network/port-forwards/", s.wrapAuth(s.handlePortForwardActions))

	// DNS responder status (sandbox hostname routing)
	s.mux.HandleFunc("/api/v1/dns/status", s.wrapAuth(s.handleDNSStatus))
}

func (s *Server) SetGUIHandler(h http.Handler) {
	s.guiHandler = h
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if (r.URL.Path == "/" || r.URL.Path == "/deployments") && deploymentsIndexEnabled() {
		s.handleDeploymentsIndex(w, r)
		return
	}
	if s.guiHandler != nil {
		s.guiHandler.ServeHTTP(w, r)
		return
	}
	http.NotFound(w, r)
}

// ResolveTarget resolves target microVM details for the unified ingress reverse proxy.
func (s *Server) ResolveTarget(ctx context.Context, idOrName string) (*network.TargetInfo, error) {
	if s.vmMgr == nil {
		return nil, fmt.Errorf("vm manager not available")
	}
	v, err := s.vmMgr.GetVM(ctx, idOrName)
	if err != nil || v == nil {
		if s.vmMgr.GetStore() != nil {
			v, err = s.vmMgr.GetStore().GetVM(ctx, idOrName)
		}
	}
	if err != nil || v == nil {
		return nil, fmt.Errorf("sandbox %q not found", idOrName)
	}
	return &network.TargetInfo{
		ID:        v.ID,
		Name:      v.Name,
		OwnerID:   v.OwnerID,
		Status:    string(v.Status),
		IPAddress: v.IPAddress,
	}, nil
}

// ResolveTargetForOwner resolves a sandbox within a specific owner's
// namespace, backing the "<sandbox>.<username>.<zone>" ingress wildcard form.
// ownerLabel is the username DNS label; the resolved VM must belong to that
// user (or their personal organization), keeping tenant isolation intact.
func (s *Server) ResolveTargetForOwner(ctx context.Context, idOrName, ownerLabel string) (*network.TargetInfo, error) {
	if s.vmMgr == nil {
		return nil, fmt.Errorf("vm manager not available")
	}
	if strings.TrimSpace(idOrName) == "" || strings.TrimSpace(ownerLabel) == "" {
		return nil, fmt.Errorf("sandbox %q not found", idOrName)
	}

	var ownerIDs []string
	if !s.authEnabled() {
		ownerIDs = []string{"default"}
	} else {
		user, ok := s.dnsUserForLabel(ownerLabel)
		if !ok {
			return nil, fmt.Errorf("unknown owner %q", ownerLabel)
		}
		ownerIDs = []string{user.ID, auth.PersonalOrgID(user.ID)}
	}

	want := strings.ToLower(strings.TrimSpace(idOrName))
	for _, v := range s.listVMsForOwnersCached(ctx, ownerIDs) {
		if v == nil {
			continue
		}
		if strings.EqualFold(v.Name, idOrName) || strings.EqualFold(v.ID, idOrName) ||
			strings.EqualFold(v.InternalID, idOrName) || sandboxLabelForVM(v) == want {
			return &network.TargetInfo{
				ID:        v.ID,
				Name:      v.Name,
				OwnerID:   v.OwnerID,
				Status:    string(v.Status),
				IPAddress: v.IPAddress,
			}, nil
		}
	}
	return nil, fmt.Errorf("sandbox %q not found", idOrName)
}

// IsPortPublic checks whether a given guest port on a VM is configured for public exposure.
func (s *Server) IsPortPublic(ctx context.Context, idOrName string, port int) bool {
	if s.vmMgr != nil && s.vmMgr.GetStore() != nil {
		if s.vmMgr.GetStore().IsPortForwardPublic(ctx, idOrName, port) {
			return true
		}
	}
	return false
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.ingressProxy != nil && s.ingressProxy.ShouldHandle(r) {
		s.ingressProxy.ServeHTTP(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") && r.Header.Get("Upgrade") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "ok",
		"version": version.Version,
		"name":    "Compute Daemon",
	})
}

// handleReady is the readiness probe: it verifies that the host can actually
// run sandboxes (KVM, firecracker, images, SSH key, database, cgroups, bridge)
// and returns 503 with remediation when a critical component is missing.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	report := doctor.Run(s.cfg)
	w.Header().Set("Content-Type", "application/json")
	if !report.OK {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(report)
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	info := version.Get("computed", "Compute Daemon")
	_ = json.NewEncoder(w).Encode(info)
}

func (s *Server) handleTiers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	json.NewEncoder(w).Encode(config.GetSupportedTiers())
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, isAdmin := s.currentOwnerID(r)
	json.NewEncoder(w).Encode(s.safeConfig(isAdmin))
}

func isJobQueueItem(q *vm.QueueItem) bool {
	if q == nil {
		return true
	}
	if q.OwnerID == "jobs" {
		return true
	}
	name := strings.ToLower(strings.TrimSpace(q.VMName))
	id := strings.ToLower(strings.TrimSpace(q.VMID))
	return strings.HasPrefix(name, "job-") || strings.HasPrefix(id, "job-")
}

func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rawQueue, err := s.vmMgr.GetQueue(r.Context())
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, isAdmin := s.currentOwnerID(r)
	var owners map[string]bool
	if !isAdmin {
		owners = s.accessibleOwnerSet(r)
	}
	queue := make([]*vm.QueueItem, 0, len(rawQueue))
	for _, q := range rawQueue {
		if isJobQueueItem(q) {
			continue
		}
		if !isAdmin && !owners[q.OwnerID] {
			continue
		}
		queue = append(queue, q)
	}
	stats := s.nonJobStats(r.Context(), r)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"queued_vms":      queue,
		"count":           len(queue),
		"running_vms":     stats.RunningVMs,
		"running_vcpus":   stats.RunningVCPUs,
		"running_mem_mb":  stats.RunningMemoryMB,
		"resource_limits": s.vmMgr.GetResourceLimits(),
	})
}

func (s *Server) nonJobStats(ctx context.Context, r *http.Request) *vm.StatsResult {
	stats, err := s.vmMgr.GetStats(ctx)
	if err != nil || stats == nil {
		stats = &vm.StatsResult{}
	}
	if vms, vErr := s.listAccessibleVMs(ctx, r); vErr == nil {
		sub := snapshotStats(vms, s.vmMgr.GetResourceLimits())
		stats.TotalVMs = sub.TotalVMs
		stats.RunningVMs = sub.RunningVMs
		stats.StoppedVMs = sub.StoppedVMs
		stats.CreatedVMs = sub.CreatedVMs
		stats.QueuedVMs = sub.QueuedVMs
		stats.SnapshottedVMs = sub.SnapshottedVMs
		stats.TotalVCPUs = sub.TotalVCPUs
		stats.TotalMemoryMB = sub.TotalMemoryMB
		stats.RunningVCPUs = sub.RunningVCPUs
		stats.RunningMemoryMB = sub.RunningMemoryMB
	}
	return stats
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, err := s.vmMgr.GetStats(r.Context()); err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(s.nonJobStats(r.Context(), r))
}

func (s *Server) handleNetworkInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	info, err := s.vmMgr.GetNetworkInfo(r.Context())
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(info)
}

func isJobNetworkAllocation(a *db.NetworkAllocation) bool {
	if a == nil {
		return true
	}
	if a.OwnerID == "jobs" {
		return true
	}
	name := strings.ToLower(strings.TrimSpace(a.VMName))
	id := strings.ToLower(strings.TrimSpace(a.VMID))
	return strings.HasPrefix(name, "job-") || strings.HasPrefix(id, "job-")
}

func (s *Server) handleNetworkAllocations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rawAllocs, err := s.vmMgr.GetStore().GetNetworkAllocations(r.Context())
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, isAdmin := s.currentOwnerID(r)
	var owners map[string]bool
	if !isAdmin {
		owners = s.accessibleOwnerSet(r)
	}
	allocs := make([]*db.NetworkAllocation, 0, len(rawAllocs))
	for _, a := range rawAllocs {
		if isJobNetworkAllocation(a) {
			continue
		}
		if !isAdmin && !owners[a.OwnerID] {
			continue
		}
		allocs = append(allocs, a)
	}
	json.NewEncoder(w).Encode(allocs)
}

func (s *Server) handleNetworkInspect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/network/inspect/")
	vmID := strings.Trim(path, "/")
	if vmID == "" {
		httpError(w, "missing vm id", http.StatusBadRequest)
		return
	}
	v, err := s.resolveAccessibleVM(r.Context(), r, vmID)
	if err != nil || v == nil {
		httpError(w, "vm not found", http.StatusNotFound)
		return
	}
	// Inspect by unique internal ID and verify the resolved VM's owner so a
	// duplicate name can never expose another tenant's network metadata.
	res, err := s.vmMgr.InspectNetworkForOwner(r.Context(), vmIdentifier(v), v.OwnerID)
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(res)
}

// waitGuestSSH polls the guest SSH port until it accepts connections or the
// timeout elapses. It makes "sandbox start" return a genuinely usable VM.
func (s *Server) waitGuestSSH(ctx context.Context, v *db.VM, timeout time.Duration) {
	if v == nil || v.IPAddress == "" || v.IPAddress == "dhcp" {
		return
	}
	addr := net.JoinHostPort(v.IPAddress, "22")
	deadline := time.Now().Add(timeout)
	dialer := &net.Dialer{Timeout: 500 * time.Millisecond}
	for time.Now().Before(deadline) {
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err == nil {
			_ = conn.Close()
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func (s *Server) handleNetworkPrune(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireAdminAccess(w, r) {
		return
	}
	pruned, err := s.vmMgr.ReconcileAndPrune(r.Context())
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if pruned == nil {
		pruned = []string{}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":      "ok",
		"pruned_taps": pruned,
	})
}

func (s *Server) handleNetworkConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg := s.vmMgr.GetNetworkManager().GetConfig()
	json.NewEncoder(w).Encode(cfg)
}

func (s *Server) handleVMs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	switch r.Method {
	case http.MethodGet:
		vms, err := s.listAccessibleVMs(ctx, r)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if vms == nil {
			vms = []*db.VM{}
		}
		s.decorateVMHostnames(vms)
		json.NewEncoder(w).Encode(vms)

	case http.MethodPost:
		var req vm.CreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, "invalid request payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(req.Name)), "job-") {
			httpError(w, "name prefix 'job-' is reserved for ephemeral CI jobs", http.StatusBadRequest)
			return
		}
		if ownerID, _, ok := s.authorizeCreateRequest(w, r, &req); !ok {
			return
		} else if req.SSHPublicKey == "" && req.SSHKeyID == "" && !req.SSHNoDefaultKey && req.OwnerID == ownerID {
			// Include the caller's default account key unless the request
			// chose a key explicitly or opted out. Admins creating sandboxes
			// for another owner do not get their own default key injected.
			if def, err := s.vmMgr.GetStore().GetDefaultSSHKey(ctx, ownerID); err != nil {
				log.Printf("[ssh-keys] default key lookup for %s: %v", ownerID, err)
			} else if def != nil {
				req.SSHKeyID = def.ID
			}
		}

		if req.CloneFrom != "" {
			src, err := s.resolveAccessibleVM(ctx, r, req.CloneFrom)
			if err != nil || src == nil || !s.canAccessOwner(r, src.OwnerID) {
				httpError(w, "clone source not found", http.StatusNotFound)
				return
			}
			// Pin to the unique ID so a same-named VM owned by another tenant
			// can never be cloned by the manager's global lookup.
			req.CloneFrom = vmIdentifier(src)
		}
		if req.Template != "" {
			tpl, err := s.resolveAccessibleTemplate(ctx, r, req.Template)
			if err != nil || tpl == nil || !s.canAccessOwner(r, tpl.OwnerID) {
				httpError(w, "template not found", http.StatusNotFound)
				return
			}
			req.Template = templateIdentifier(tpl)
		}
		if req.Image != "" {
			// The image field may name either a custom image or a template; both
			// are owner-scoped. Mirror the manager's resolution order so a
			// non-admin cannot reach another tenant's template through it.
			img, err := s.resolveAccessibleImage(ctx, r, req.Image)
			if err != nil {
				httpError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if img != nil {
				// Pin to the unique ID so the manager cannot resolve a
				// same-named image belonging to another tenant.
				req.Image = imageIdentifier(img)
			} else {
				tpl, tplErr := s.resolveAccessibleTemplate(ctx, r, req.Image)
				if tplErr != nil {
					httpError(w, tplErr.Error(), http.StatusInternalServerError)
					return
				}
				if tpl != nil {
					req.Image = templateIdentifier(tpl)
				} else if !s.vmMgr.IsPlatformBaseImage(req.Image) {
					// An image/template with this name may exist for another
					// tenant. Preserve the non-enumerating 404 contract, but
					// never let a foreign custom image shadow a platform base
					// image name such as "ubuntu-24.04".
					if globalImg, _ := s.vmMgr.GetImage(ctx, req.Image); globalImg != nil {
						httpError(w, "image not found", http.StatusNotFound)
						return
					}
					if globalTpl, _ := s.vmMgr.GetTemplate(ctx, req.Image); globalTpl != nil {
						httpError(w, "image not found", http.StatusNotFound)
						return
					}
				}
			}
		}

		vcpus, memoryMB, diskMB := s.estimateRequestResources(ctx, req)
		if err := s.enforceCreateQuota(ctx, r, req.OwnerID, vcpus, memoryMB, diskMB); err != nil {
			httpError(w, err.Error(), http.StatusTooManyRequests)
			return
		}

		created, err := s.vmMgr.CreateVM(ctx, req)
		if err != nil {
			if strings.Contains(err.Error(), "ssh key not found") {
				httpError(w, err.Error(), http.StatusNotFound)
				return
			}
			if strings.Contains(err.Error(), "base rootfs image not found") || strings.Contains(err.Error(), "image not found") || strings.Contains(err.Error(), "custom image rootfs not found") || (strings.Contains(err.Error(), "template '") && strings.Contains(err.Error(), "' not found")) {
				httpError(w, err.Error(), http.StatusNotFound)
				return
			}
			if strings.Contains(err.Error(), "disk space issue") || strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "unsupported") || strings.Contains(err.Error(), "conflicting") {
				httpError(w, err.Error(), http.StatusBadRequest)
				return
			}
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		s.recordEvent("sandbox.created", getActor(r), created.Name, "delivered", 85, map[string]interface{}{
			"vm_id":     created.ID,
			"vcpus":     created.VCPUs,
			"memory_mb": created.MemoryMB,
		})
		s.encodeVM(w, created)

	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleVMActions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/sandboxes/")
	path = strings.TrimPrefix(path, "/api/v1/vms/")
	parts := strings.Split(strings.Trim(path, "/"), "/")

	if len(parts) == 0 || parts[0] == "" {
		httpError(w, "missing vm id", http.StatusBadRequest)
		return
	}

	vmID := parts[0]

	v, err := s.resolveAccessibleVM(ctx, r, vmID)
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if v == nil {
		httpError(w, "vm not found", http.StatusNotFound)
		return
	}

	// Use the unique internal ID for every downstream manager operation. Names
	// and external IDs can be duplicated by different tenants, and a global
	// lookup would otherwise resolve to another tenant's VM.
	requestedID := vmID
	vmID = vmIdentifier(v)
	displayName := v.Name
	if displayName == "" {
		displayName = requestedID
	}

	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			s.encodeVM(w, v)
		case http.MethodDelete:
			if v.Protected {
				httpError(w, fmt.Sprintf("item %s is protected against deletion", displayName), http.StatusBadRequest)
				return
			}
			s.removePortForwardsForVM(ctx, v, true)
			var delErr error
			if _, isAdmin := s.currentOwnerID(r); isAdmin {
				delErr = s.vmMgr.DeleteVM(ctx, vmID)
			} else {
				delErr = s.vmMgr.DeleteVMForOwner(ctx, v.OwnerID, vmID)
			}
			if delErr != nil {
				if strings.Contains(delErr.Error(), "protected against deletion") {
					httpError(w, fmt.Sprintf("item %s is protected against deletion", displayName), http.StatusBadRequest)
					return
				}
				httpError(w, delErr.Error(), http.StatusInternalServerError)
				return
			}
			s.recordEvent("sandbox.deleted", getActor(r), requestedID, "delivered", 40, map[string]string{"id": requestedID})
			json.NewEncoder(w).Encode(map[string]string{"status": "deleted", "id": requestedID})

		case http.MethodPatch, http.MethodPut:
			var req vm.UpdateVMConfigRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				httpError(w, "invalid request payload: "+err.Error(), http.StatusBadRequest)
				return
			}
			updated, err := s.vmMgr.UpdateVMConfig(ctx, vmID, req)
			if err != nil {
				if strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "conflicting") {
					httpError(w, err.Error(), http.StatusBadRequest)
					return
				}
				httpError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			s.recordEvent("sandbox.config_updated", getActor(r), vmID, "delivered", 20, req)
			s.encodeVM(w, updated)

		default:
			httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	action := parts[1]
	if action == "ssh" {
		s.handleVMSSH(w, r, vmID)
		return
	}
	if action == "terminal" {
		s.handleVMTerminal(w, r, vmID)
		return
	}
	if action == "files" {
		s.handleVMFiles(w, r, vmID, parts[2:])
		return
	}
	if action == "processes" || action == "process" {
		s.handleVMProcesses(w, r, vmID, parts[2:])
		return
	}
	if r.Method != http.MethodPost {
		httpError(w, "action requires POST", http.StatusMethodNotAllowed)
		return
	}

	switch action {
	case "clone":
		var req vm.CreateRequest
		if r.Body != nil {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
				httpError(w, "invalid request payload: "+err.Error(), http.StatusBadRequest)
				return
			}
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(req.Name)), "job-") {
			httpError(w, "sandbox name prefix 'job-' is reserved for ephemeral CI jobs", http.StatusBadRequest)
			return
		}
		if _, _, ok := s.authorizeCreateRequest(w, r, &req); !ok {
			return
		}
		vcpus, memoryMB, diskMB := s.estimateRequestResources(ctx, req)
		if err := s.enforceCreateQuota(ctx, r, req.OwnerID, vcpus, memoryMB, diskMB); err != nil {
			httpError(w, err.Error(), http.StatusTooManyRequests)
			return
		}
		cloned, err := s.vmMgr.CloneVM(ctx, vmID, req)
		if err != nil {
			if strings.Contains(err.Error(), "ssh key not found") {
				httpError(w, err.Error(), http.StatusNotFound)
				return
			}
			if strings.Contains(err.Error(), "disk space issue") || strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "unsupported") || strings.Contains(err.Error(), "conflicting") {
				httpError(w, err.Error(), http.StatusBadRequest)
				return
			}
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if req.AutoStart {
			if err := s.enforceRunQuota(ctx, r, cloned); err != nil {
				httpError(w, err.Error(), http.StatusTooManyRequests)
				return
			}
			if runErr := s.vmMgr.RunVM(ctx, cloned.ID); runErr != nil {
				log.Printf("[computed] clone %s auto_start skipped/failed: %v", cloned.ID, runErr)
			} else if started, getErr := s.vmMgr.GetVM(ctx, cloned.ID); getErr == nil && started != nil {
				cloned = started
				if cloned.Status == db.StatusRunning {
					s.mountAttachedVolumes(ctx, cloned)
					s.applyEnvironment(ctx, cloned)
					s.applyPortForwardsForVM(ctx, cloned)
				}
			}
		}
		s.recordEvent("sandbox.cloned", getActor(r), cloned.Name, "delivered", 60, map[string]string{"source_id": vmID, "cloned_id": cloned.ID})
		w.WriteHeader(http.StatusCreated)
		s.encodeVM(w, cloned)

	case "run", "start":
		if err := s.enforceRunQuota(ctx, r, v); err != nil {
			httpError(w, err.Error(), http.StatusTooManyRequests)
			return
		}
		if err := s.vmMgr.RunVM(ctx, vmID); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		v, _ := s.vmMgr.GetVM(ctx, vmID)
		if v != nil && v.Status == db.StatusRunning {
			// Give operators and automation a sandbox that is actually reachable.
			s.waitGuestSSH(ctx, v, 30*time.Second)
			s.mountAttachedVolumes(ctx, v)
			s.applyEnvironment(ctx, v)
			s.applyPortForwardsForVM(ctx, v)
		}
		s.recordEvent("sandbox.started", getActor(r), vmID, "delivered", 35, nil)
		s.encodeVM(w, v)

	case "save-state", "snapshot":
		if err := s.vmMgr.SaveState(ctx, vmID); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		v, _ := s.vmMgr.GetVM(ctx, vmID)
		s.recordEvent("snapshot.created", getActor(r), vmID, "delivered", 50, nil)
		s.encodeVM(w, v)

	case "resume":
		if err := s.enforceRunQuota(ctx, r, v); err != nil {
			httpError(w, err.Error(), http.StatusTooManyRequests)
			return
		}
		if err := s.vmMgr.ResumeVM(ctx, vmID); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		v, _ := s.vmMgr.GetVM(ctx, vmID)
		if v != nil && v.Status == db.StatusRunning {
			s.waitGuestSSH(ctx, v, 30*time.Second)
			s.mountAttachedVolumes(ctx, v)
			s.applyEnvironment(ctx, v)
			s.applyPortForwardsForVM(ctx, v)
		}
		s.recordEvent("sandbox.resumed", getActor(r), vmID, "delivered", 25, nil)
		s.encodeVM(w, v)

	case "stop":
		if _, isAdmin := s.currentOwnerID(r); isAdmin {
			if err := s.vmMgr.StopVM(ctx, vmID); err != nil {
				httpError(w, err.Error(), http.StatusInternalServerError)
				return
			}
		} else if err := s.vmMgr.StopVMForOwner(ctx, v.OwnerID, vmID); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		v, _ := s.vmMgr.GetVM(ctx, vmID)
		if v != nil {
			s.removePortForwardsForVM(ctx, v, false)
		}
		s.recordEvent("sandbox.stopped", getActor(r), vmID, "delivered", 30, nil)
		s.encodeVM(w, v)

	case "exec":
		var req vm.ExecRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, "invalid request payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		res, err := s.vmMgr.ExecVM(ctx, vmID, req)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.recordEvent("sandbox.command_executed", getActor(r), vmID, "delivered", 45, map[string]interface{}{"command": req.Command, "exit_code": res.ExitCode})
		json.NewEncoder(w).Encode(res)

	case "exec-stream":
		var req vm.ExecRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, "invalid request payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		if v, err := s.vmMgr.GetVM(ctx, vmID); err != nil || v == nil {
			httpError(w, "vm not found", http.StatusNotFound)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			httpError(w, "streaming is not supported by this server", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		send := func(ev vm.ExecStreamEvent) error {
			payload, err := json.Marshal(ev)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
				return err
			}
			flusher.Flush()
			return nil
		}
		if err := s.vmMgr.ExecVMStream(r.Context(), vmID, req, send); err != nil {
			code := 1
			_ = send(vm.ExecStreamEvent{Error: err.Error(), ExitCode: &code})
		}

	case "public-ip":
		var req vm.UpdateVMConfigRequest
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&req)
		}
		q := r.URL.Query()
		if val := q.Get("public_ip"); val != "" {
			b := strings.ToLower(val) == "on" || strings.ToLower(val) == "true" || val == "1"
			req.PublicIP = &b
			req.PublicIPEnabled = &b
		} else if val := q.Get("enabled"); val != "" {
			b := strings.ToLower(val) == "on" || strings.ToLower(val) == "true" || val == "1"
			req.PublicIP = &b
			req.PublicIPEnabled = &b
		}
		updated, err := s.vmMgr.UpdateVMConfig(ctx, vmID, req)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(updated)

	case "network-isolation":
		var req vm.UpdateVMConfigRequest
		if r.Body != nil {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
				httpError(w, "invalid request payload: "+err.Error(), http.StatusBadRequest)
				return
			}
		}
		q := r.URL.Query()
		if val := strings.TrimSpace(q.Get("network_isolation")); val != "" {
			req.NetworkIsolation = &val
		} else if val := strings.TrimSpace(q.Get("mode")); val != "" {
			req.NetworkIsolation = &val
		}
		if val := strings.TrimSpace(q.Get("isolated")); val != "" {
			lower := strings.ToLower(val)
			switch lower {
			case "on", "true", "1", "yes", "enabled":
				b := true
				req.Isolated = &b
			case "off", "false", "0", "no", "disabled":
				b := false
				req.Isolated = &b
			default:
				httpError(w, fmt.Sprintf("invalid isolated query parameter %q: expected boolean", val), http.StatusBadRequest)
				return
			}
		}
		if req.NetworkIsolation == nil && req.Isolated == nil {
			httpError(w, "missing network_isolation or isolated parameter", http.StatusBadRequest)
			return
		}
		updated, err := s.vmMgr.UpdateVMConfig(ctx, vmID, req)
		if err != nil {
			if strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "conflicting") {
				httpError(w, err.Error(), http.StatusBadRequest)
				return
			}
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.recordEvent("sandbox.network_isolation_updated", getActor(r), vmID, "delivered", 20, map[string]string{"network_isolation": updated.NetworkIsolation})
		s.encodeVM(w, updated)

	case "config":
		var req vm.UpdateVMConfigRequest
		if r.Body != nil {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
				httpError(w, "invalid request payload: "+err.Error(), http.StatusBadRequest)
				return
			}
		}
		updated, err := s.vmMgr.UpdateVMConfig(ctx, vmID, req)
		if err != nil {
			if strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "conflicting") {
				httpError(w, err.Error(), http.StatusBadRequest)
				return
			}
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.recordEvent("sandbox.config_updated", getActor(r), vmID, "delivered", 20, req)
		s.encodeVM(w, updated)

	case "protect":
		t := true
		updated, err := s.vmMgr.UpdateVMConfig(ctx, vmID, vm.UpdateVMConfigRequest{Protected: &t})
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.recordEvent("sandbox.protected", getActor(r), vmID, "delivered", 20, map[string]bool{"protected": true})
		json.NewEncoder(w).Encode(updated)

	case "unprotect":
		f := false
		updated, err := s.vmMgr.UpdateVMConfig(ctx, vmID, vm.UpdateVMConfigRequest{Protected: &f})
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.recordEvent("sandbox.unprotected", getActor(r), vmID, "delivered", 20, map[string]bool{"protected": false})
		json.NewEncoder(w).Encode(updated)

	case "reconcile-ssh", "reconcile":
		if err := s.vmMgr.SyncSSHKeysForVM(ctx, v.ID); err != nil {
			httpError(w, "failed to reconcile SSH access: "+err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "reconciled",
			"sandbox": v.Name,
			"id":      v.ID,
		})

	default:
		httpError(w, "unknown action: "+action, http.StatusBadRequest)
	}
}

func httpError(w http.ResponseWriter, message string, code int) {
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func (s *Server) handleImages(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, isAdmin := s.currentOwnerID(r)
	switch r.Method {
	case http.MethodGet:
		customImgs, err := s.vmMgr.ListImages(ctx)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if customImgs == nil {
			customImgs = []*db.CustomImage{}
		}

		type ImageListItem struct {
			ID          string   `json:"id"`
			Name        string   `json:"name"`
			Description string   `json:"description,omitempty"`
			BaseImage   string   `json:"base_image,omitempty"`
			Packages    []string `json:"packages,omitempty"`
			SizeMB      int      `json:"size_mb"`
			Status      string   `json:"status"`
			Kind        string   `json:"kind"`
			CreatedAt   string   `json:"created_at,omitempty"`
		}

		var result []ImageListItem

		result = append(result, ImageListItem{
			ID:          "img-base-ubuntu-24",
			Name:        "ubuntu-24",
			Description: "Ubuntu 24.04 LTS (Noble Numbat) official MicroVM base image",
			BaseImage:   "ubuntu-24.04",
			Packages:    []string{"openssh-server", "sudo", "curl", "wget", "git", "iputils-ping", "iproute2", "bind9-dnsutils", "netcat-openbsd", "python3", "nodejs", "npm", "jq", "tar", "unzip"},
			SizeMB:      4096,
			Status:      "ready",
			Kind:        "base",
		})

		for _, c := range customImgs {
			if !isAdmin && !s.canAccessOwner(r, c.OwnerID) {
				continue
			}
			createdStr := ""
			if !c.CreatedAt.IsZero() {
				createdStr = c.CreatedAt.Format(time.RFC3339)
			}
			result = append(result, ImageListItem{
				ID:          c.ID,
				Name:        c.Name,
				Description: c.Description,
				BaseImage:   c.BaseImage,
				Packages:    c.Packages,
				SizeMB:      c.SizeMB,
				Status:      string(c.Status),
				Kind:        "custom",
				CreatedAt:   createdStr,
			})
		}

		json.NewEncoder(w).Encode(result)

	case http.MethodPost:
		var req vm.BuildImageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, "invalid request payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		ownerID, isAdmin, ok := s.callerIdentity(r)
		if !ok {
			httpError(w, "authentication required", http.StatusUnauthorized)
			return
		}
		if isAdmin {
			if strings.TrimSpace(req.OwnerID) == "" {
				req.OwnerID = ownerID
			}
		} else {
			req.OwnerID = ownerID
		}
		img, err := s.vmMgr.BuildImage(ctx, req)
		if err != nil {
			httpError(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(img)

	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleImageActions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/images/")
	nameOrID := strings.Trim(path, "/")
	if nameOrID == "" {
		httpError(w, "missing image name or id", http.StatusBadRequest)
		return
	}
	if strings.EqualFold(nameOrID, "build") {
		httpError(w, "not found", http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodGet:
		img, err := s.resolveAccessibleImage(ctx, r, nameOrID)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if img == nil {
			defaultRootfs := s.vmMgr.GetStorageManager().GetConfig().DefaultRootfs
			cleanBaseName := strings.TrimSuffix(defaultRootfs, ".ext4")
			if strings.EqualFold(nameOrID, cleanBaseName) ||
				strings.EqualFold(nameOrID, "ubuntu-24") ||
				strings.EqualFold(nameOrID, "ubuntu-24.04") ||
				strings.EqualFold(nameOrID, "ubuntu-2404") ||
				strings.EqualFold(nameOrID, "ubuntu") ||
				strings.EqualFold(nameOrID, "img-base-ubuntu-24") {
				json.NewEncoder(w).Encode(map[string]interface{}{
					"id":          "img-base-ubuntu-24",
					"name":        cleanBaseName,
					"base_image":  "ubuntu-24.04",
					"description": "Ubuntu 24.04 LTS (Noble Numbat) official MicroVM base image",
					"status":      "ready",
					"kind":        "base",
				})
				return
			}
			httpError(w, "image not found", http.StatusNotFound)
			return
		}
		if !s.canAccessOwner(r, img.OwnerID) {
			httpError(w, "image not found", http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(img)

	case http.MethodDelete:
		img, err := s.resolveAccessibleImage(ctx, r, nameOrID)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if img == nil || !s.canAccessOwner(r, img.OwnerID) {
			httpError(w, "image not found", http.StatusNotFound)
			return
		}
		// Delete by the unique identifier so a same-named image owned by
		// another tenant is never removed.
		if err := s.vmMgr.DeleteImage(ctx, imageIdentifier(img)); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted", "id": nameOrID})

	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleTemplates(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, isAdmin := s.currentOwnerID(r)
	switch r.Method {
	case http.MethodGet:
		tpls, err := s.vmMgr.ListTemplates(ctx)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !isAdmin {
			filtered := make([]*db.Template, 0, len(tpls))
			for _, tpl := range tpls {
				if tpl != nil && s.canAccessOwner(r, tpl.OwnerID) {
					filtered = append(filtered, tpl)
				}
			}
			tpls = filtered
		}
		if tpls == nil {
			tpls = []*db.Template{}
		}
		json.NewEncoder(w).Encode(tpls)

	case http.MethodPost:
		var req vm.CreateTemplateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, "invalid request payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		ownerID, isAdmin, ok := s.callerIdentity(r)
		if !ok {
			httpError(w, "authentication required", http.StatusUnauthorized)
			return
		}
		if isAdmin {
			if strings.TrimSpace(req.OwnerID) == "" {
				req.OwnerID = ownerID
			}
		} else {
			req.OwnerID = ownerID
		}
		if req.VMID != "" {
			src, err := s.resolveAccessibleVM(ctx, r, req.VMID)
			if err != nil || src == nil || !s.canAccessOwner(r, src.OwnerID) {
				httpError(w, "source vm not found", http.StatusNotFound)
				return
			}
			// Pin to the unique internal ID so a same-named VM owned by another
			// tenant can never be snapshotted.
			req.VMID = vmIdentifier(src)
		}
		tpl, err := s.vmMgr.CreateTemplate(ctx, req)
		if err != nil {
			httpError(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(tpl)

	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleTemplateActions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/templates/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		httpError(w, "missing template name or id", http.StatusBadRequest)
		return
	}

	nameOrID := parts[0]

	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			tpl, err := s.resolveAccessibleTemplate(ctx, r, nameOrID)
			if err != nil {
				httpError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if tpl == nil || !s.canAccessOwner(r, tpl.OwnerID) {
				httpError(w, "template not found", http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(tpl)

		case http.MethodDelete:
			tpl, err := s.resolveAccessibleTemplate(ctx, r, nameOrID)
			if err != nil {
				httpError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if tpl == nil || !s.canAccessOwner(r, tpl.OwnerID) {
				httpError(w, "template not found", http.StatusNotFound)
				return
			}
			// Delete by the unique identifier so a same-named template owned by
			// another tenant is never removed.
			if err := s.vmMgr.DeleteTemplate(ctx, templateIdentifier(tpl)); err != nil {
				httpError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"status": "deleted", "id": nameOrID})

		default:
			httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	action := parts[1]
	if action == "launch" {
		if r.Method != http.MethodPost {
			httpError(w, "launch requires POST", http.StatusMethodNotAllowed)
			return
		}
		tpl, err := s.resolveAccessibleTemplate(ctx, r, nameOrID)
		if err != nil || tpl == nil || !s.canAccessOwner(r, tpl.OwnerID) {
			httpError(w, "template not found", http.StatusNotFound)
			return
		}
		var req vm.CreateRequest
		if r.Body != nil {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
				httpError(w, "invalid request payload: "+err.Error(), http.StatusBadRequest)
				return
			}
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(req.Name)), "job-") {
			httpError(w, "sandbox name prefix 'job-' is reserved for ephemeral CI jobs", http.StatusBadRequest)
			return
		}
		if _, _, ok := s.authorizeCreateRequest(w, r, &req); !ok {
			return
		}
		// Apply any template defaults before quota estimation so limits reflect
		// the resources the launched sandbox will actually consume.
		if req.VCPUs <= 0 && tpl.VCPUs > 0 {
			req.VCPUs = tpl.VCPUs
		}
		if req.MemoryMB <= 0 && tpl.MemoryMB > 0 {
			req.MemoryMB = tpl.MemoryMB
		}
		if req.DiskMB <= 0 && tpl.DiskMB > 0 {
			req.DiskMB = tpl.DiskMB
		}
		vcpus, memoryMB, diskMB := s.estimateRequestResources(ctx, req)
		if err := s.enforceCreateQuota(ctx, r, req.OwnerID, vcpus, memoryMB, diskMB); err != nil {
			httpError(w, err.Error(), http.StatusTooManyRequests)
			return
		}
		// Templates launch (and run) immediately, so the running quota applies.
		if err := s.enforceRunQuota(ctx, r, &db.VM{OwnerID: req.OwnerID, VCPUs: vcpus, MemoryMB: memoryMB, DiskMB: diskMB}); err != nil {
			httpError(w, err.Error(), http.StatusTooManyRequests)
			return
		}
		// Pin to the unique internal ID so duplicate template names cannot
		// resolve to another tenant's template inside the manager.
		launchedVM, err := s.vmMgr.LaunchTemplate(ctx, templateIdentifier(tpl), req)
		if err != nil {
			httpError(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(launchedVM)
		return
	}

	httpError(w, "unknown action: "+action, http.StatusBadRequest)
}

type ClusterSnapshot struct {
	Type          string                  `json:"type"`
	Stats         *vm.StatsResult         `json:"stats,omitempty"`
	VMs           []*db.VM                `json:"vms"`
	Allocations   []*db.NetworkAllocation `json:"allocations"`
	NetworkConfig network.Config          `json:"network_config"`
	Config        config.GlobalConfig     `json:"config"`
	Queue         map[string]interface{}  `json:"queue"`
	Workers       []*db.WorkerNode        `json:"workers"`
	Jobs          []*db.Job               `json:"jobs"`
	Volumes       []*db.Volume            `json:"volumes"`
	Events        []*db.AuditEvent        `json:"events"`
	Timestamp     time.Time               `json:"timestamp"`
}

func (s *Server) getClusterSnapshot(ctx context.Context, authCtx *auth.AuthContext) *ClusterSnapshot {
	scoped := s.authEnabled() && authCtx != nil && authCtx.User != nil && !isAdminAuthContext(authCtx)

	store := s.vmMgr.GetStore()
	var stats *vm.StatsResult
	var vms []*db.VM
	if scoped {
		vms = filterNonJobVMs(s.listVMsForOwners(ctx, append([]string{authCtx.User.ID}, orgIDIfDifferent(authCtx)...)))
		stats = snapshotStats(vms, s.vmMgr.GetResourceLimits())
	} else {
		rawVMs, _ := s.vmMgr.ListVMs(ctx)
		vms = filterNonJobVMs(rawVMs)
		stats = snapshotStats(vms, s.vmMgr.GetResourceLimits())
	}
	if vms == nil {
		vms = []*db.VM{}
	}

	owners := make(map[string]bool)
	if scoped {
		for _, oid := range append([]string{authCtx.User.ID}, orgIDIfDifferent(authCtx)...) {
			if oid != "" {
				owners[oid] = true
			}
		}
	}

	rawAllocs, _ := store.GetNetworkAllocations(ctx)
	allocs := make([]*db.NetworkAllocation, 0, len(rawAllocs))
	for _, a := range rawAllocs {
		if isJobNetworkAllocation(a) {
			continue
		}
		if scoped && !owners[a.OwnerID] {
			continue
		}
		allocs = append(allocs, a)
	}
	netCfg := s.vmMgr.GetNetworkManager().GetConfig()

	cfg := s.safeConfig(!scoped)

	rawQueue, _ := s.vmMgr.GetQueue(ctx)
	queue := make([]*vm.QueueItem, 0, len(rawQueue))
	for _, q := range rawQueue {
		if isJobQueueItem(q) {
			continue
		}
		if scoped && !owners[q.OwnerID] {
			continue
		}
		queue = append(queue, q)
	}
	queueData := map[string]interface{}{
		"queued_vms":      queue,
		"count":           len(queue),
		"running_vms":     0,
		"running_vcpus":   0,
		"running_mem_mb":  0,
		"resource_limits": s.vmMgr.GetResourceLimits(),
	}
	if stats != nil {
		queueData["running_vms"] = stats.RunningVMs
		queueData["running_vcpus"] = stats.RunningVCPUs
		queueData["running_mem_mb"] = stats.RunningMemoryMB
	}

	var workers []*db.WorkerNode
	var jobsList []*db.Job
	var vols []*db.Volume
	var eventsList []*db.AuditEvent
	if s.vmMgr != nil && store != nil {
		if !scoped {
			// Worker topology is infrastructure metadata and is admin-only.
			workers, _ = store.ListWorkerNodes(ctx)
			s.enrichWorkerNodes(workers)
		}
		if scoped {
			jobsList = s.listJobsForOwners(ctx, append([]string{authCtx.User.ID}, orgIDIfDifferent(authCtx)...))
			vols = filterNonJobVolumes(s.listVolumesForOwners(ctx, append([]string{authCtx.User.ID}, orgIDIfDifferent(authCtx)...)))
			allEvents, _ := store.ListAuditEvents(ctx, 100)
			for _, evt := range allEvents {
				if evt != nil && evt.Actor == authCtx.User.Username {
					eventsList = append(eventsList, evt)
					if len(eventsList) >= 50 {
						break
					}
				}
			}
		} else {
			jobsList, _ = store.ListJobs(ctx, "")
			rawVols, _ := store.ListVolumes(ctx)
			vols = filterNonJobVolumes(rawVols)
			eventsList, _ = store.ListAuditEvents(ctx, 50)
		}
	}
	if workers == nil {
		workers = []*db.WorkerNode{}
	}
	if jobsList == nil {
		jobsList = []*db.Job{}
	}
	if vols == nil {
		vols = []*db.Volume{}
	}
	if eventsList == nil {
		eventsList = []*db.AuditEvent{}
	}

	return &ClusterSnapshot{
		Type:          "snapshot",
		Stats:         stats,
		VMs:           vms,
		Allocations:   allocs,
		NetworkConfig: netCfg,
		Config:        cfg,
		Queue:         queueData,
		Workers:       workers,
		Jobs:          jobsList,
		Volumes:       vols,
		Events:        eventsList,
		Timestamp:     time.Now().UTC(),
	}
}

func orgIDIfDifferent(authCtx *auth.AuthContext) []string {
	if authCtx == nil || authCtx.OrgID == "" || authCtx.User == nil || authCtx.OrgID == authCtx.User.ID {
		return nil
	}
	return []string{authCtx.OrgID}
}

func snapshotStats(vms []*db.VM, limits config.ResourceLimits) *vm.StatsResult {
	stats := &vm.StatsResult{ResourceLimits: &limits}
	for _, v := range vms {
		if v == nil {
			continue
		}
		stats.TotalVMs++
		stats.TotalVCPUs += v.VCPUs
		stats.TotalMemoryMB += v.MemoryMB
		switch v.Status {
		case db.StatusRunning:
			stats.RunningVMs++
			stats.RunningVCPUs += v.VCPUs
			stats.RunningMemoryMB += v.MemoryMB
		case db.StatusQueued:
			stats.QueuedVMs++
		case db.StatusStopped:
			stats.StoppedVMs++
		case db.StatusCreated:
			stats.CreatedVMs++
		case db.StatusPaused:
			stats.SnapshottedVMs++
		}
	}
	return stats
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	opts := &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	}
	conn, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusInternalError, "server closed")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Initial snapshot
	authCtx := auth.GetAuth(r.Context())
	snapshot := s.getClusterSnapshot(ctx, authCtx)
	if data, err := json.Marshal(snapshot); err == nil {
		_ = conn.Write(ctx, websocket.MessageText, data)
	}

	// Incoming message loop
	go func() {
		defer cancel()
		for {
			msgType, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if msgType == websocket.MessageText {
				var req map[string]interface{}
				_ = json.Unmarshal(data, &req)
				snap := s.getClusterSnapshot(ctx, authCtx)
				if respBytes, err := json.Marshal(snap); err == nil {
					_ = conn.Write(ctx, websocket.MessageText, respBytes)
				}
			}
		}
	}()

	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			_ = conn.Close(websocket.StatusNormalClosure, "")
			return
		case <-ticker.C:
			snap := s.getClusterSnapshot(ctx, authCtx)
			data, err := json.Marshal(snap)
			if err != nil {
				continue
			}
			if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
				return
			}
		}
	}
}

type CreateKeyRequest struct {
	Name         string `json:"name"`
	PublicKey    string `json:"public_key"`
	AllSandboxes bool   `json:"all_sandboxes"`
	// IsDefault makes the new key the owner's default: it is embedded into
	// every new sandbox unless the creation request chooses otherwise. When
	// omitted, the first key on an account becomes the default automatically.
	IsDefault bool `json:"is_default,omitempty"`
}

type UpdateKeyRequest struct {
	Name         *string `json:"name"`
	PublicKey    *string `json:"public_key"`
	AllSandboxes *bool   `json:"all_sandboxes"`
	IsDefault    *bool   `json:"is_default"`
}

// syncAccountSSHKeys reconciles account-wide keys for a key's owner. It never
// fails the key operation: per-sandbox failures are reported in the response
// payload and logged.
func (s *Server) syncAccountSSHKeys(ctx context.Context, key *db.SSHKey) *vm.SSHKeySyncResult {
	if key == nil || s.vmMgr == nil {
		return nil
	}
	result, err := s.vmMgr.SyncSSHKeysForOwner(ctx, key.OwnerID)
	if err != nil {
		log.Printf("[ssh-keys] reconcile owner %s: %v", key.OwnerID, err)
		return &vm.SSHKeySyncResult{Failed: []vm.SSHKeySyncFailure{{Error: err.Error()}}}
	}
	if len(result.Failed) > 0 {
		log.Printf("[ssh-keys] reconcile owner %s: %d applied, %d failed", key.OwnerID, len(result.Applied), len(result.Failed))
	}
	return result
}

func (s *Server) handleKeys(w http.ResponseWriter, r *http.Request) {
	ownerID, isAdmin, ok := s.callerIdentity(r)
	if !ok {
		httpError(w, "authentication required", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet:
		var keys []*db.SSHKey
		var err error
		if isAdmin {
			keys, err = s.vmMgr.GetStore().ListSSHKeys(r.Context(), "")
		} else {
			seen := make(map[string]bool)
			for _, oid := range s.ownerIDsFor(r) {
				owned, listErr := s.vmMgr.GetStore().ListSSHKeys(r.Context(), oid)
				if listErr != nil {
					err = listErr
					break
				}
				for _, k := range owned {
					if k == nil || seen[k.ID] {
						continue
					}
					seen[k.ID] = true
					keys = append(keys, k)
				}
			}
		}
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"failed to list keys: %v"}`, err), http.StatusInternalServerError)
			return
		}
		if keys == nil {
			keys = []*db.SSHKey{}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"keys": keys})

	case http.MethodPost:
		var req CreateKeyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request payload"}`, http.StatusBadRequest)
			return
		}
		req.Name = strings.TrimSpace(req.Name)
		req.PublicKey = strings.TrimSpace(req.PublicKey)
		if req.Name == "" || req.PublicKey == "" {
			http.Error(w, `{"error":"name and public_key are required"}`, http.StatusBadRequest)
			return
		}

		parsedKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(req.PublicKey))
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"invalid public key format: %v"}`, err), http.StatusBadRequest)
			return
		}

		fingerprint := ssh.FingerprintSHA256(parsedKey)
		b := make([]byte, 4)
		_, _ = rand.Read(b)
		keyID := fmt.Sprintf("ssh_%s", hex.EncodeToString(b))

		// The first key an account saves becomes its default automatically so
		// new sandboxes embed it without any extra configuration.
		isDefault := req.IsDefault
		if !isDefault {
			existing, err := s.vmMgr.GetStore().ListSSHKeys(r.Context(), ownerID)
			if err != nil {
				http.Error(w, fmt.Sprintf(`{"error":"failed to list keys: %v"}`, err), http.StatusInternalServerError)
				return
			}
			isDefault = len(existing) == 0
		}

		key := &db.SSHKey{
			ID:           keyID,
			OwnerID:      ownerID,
			Name:         req.Name,
			PublicKey:    req.PublicKey,
			Fingerprint:  fingerprint,
			AllSandboxes: req.AllSandboxes,
			IsDefault:    isDefault,
			CreatedAt:    time.Now().UTC(),
		}

		if err := s.vmMgr.GetStore().SaveSSHKey(r.Context(), key); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"failed to save key: %v"}`, err), http.StatusInternalServerError)
			return
		}
		if key.IsDefault {
			if err := s.vmMgr.GetStore().SetDefaultSSHKey(r.Context(), key.OwnerID, key.ID); err != nil {
				http.Error(w, fmt.Sprintf(`{"error":"failed to set default key: %v"}`, err), http.StatusInternalServerError)
				return
			}
		}

		if key.AllSandboxes {
			s.syncAccountSSHKeys(r.Context(), key)
		}
		s.recordEvent("ssh_key.created", getActor(r), key.Name, "delivered", 15, map[string]interface{}{
			"name":          key.Name,
			"fingerprint":   key.Fingerprint,
			"all_sandboxes": key.AllSandboxes,
			"is_default":    key.IsDefault,
		})
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(key)

	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleKeyActions(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/keys/"), "/")
	parts := strings.Split(rest, "/")
	idOrName := ""
	if len(parts) > 0 {
		idOrName = strings.TrimSpace(parts[0])
	}
	action := ""
	if len(parts) > 1 {
		action = strings.ToLower(strings.TrimSpace(parts[1]))
	}
	if idOrName == "" {
		http.Error(w, `{"error":"key id or name required"}`, http.StatusBadRequest)
		return
	}

	if idOrName == "reconcile" {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		ownerID, _, _ := s.callerIdentity(r)
		res, err := s.vmMgr.SyncSSHKeysForOwner(r.Context(), ownerID)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"reconciliation failed: %v"}`, err), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"status": "reconciled", "sync": res})
		return
	}

	key, err := s.resolveAccessibleSSHKey(r.Context(), r, idOrName)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"failed to get key: %v"}`, err), http.StatusInternalServerError)
		return
	}
	if key == nil {
		http.Error(w, fmt.Sprintf(`{"error":"key not found: %s"}`, idOrName), http.StatusNotFound)
		return
	}

	if action != "" {
		if action != "apply" {
			http.Error(w, `{"error":"unknown key action"}`, http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		key.AllSandboxes = true
		if err := s.vmMgr.GetStore().SaveSSHKey(r.Context(), key); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"failed to save key: %v"}`, err), http.StatusInternalServerError)
			return
		}
		sync := s.syncAccountSSHKeys(r.Context(), key)
		s.recordEvent("ssh_key.applied", getActor(r), key.Name, "delivered", 25, map[string]interface{}{
			"id": key.ID, "name": key.Name, "all_sandboxes": true,
		})
		json.NewEncoder(w).Encode(map[string]interface{}{"key": key, "sync": sync})
		return
	}

	switch r.Method {
	case http.MethodGet:
		json.NewEncoder(w).Encode(key)

	case http.MethodPatch, http.MethodPut:
		var req UpdateKeyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request payload"}`, http.StatusBadRequest)
			return
		}
		oldKey := key.PublicKey
		if req.Name != nil {
			name := strings.TrimSpace(*req.Name)
			if name == "" {
				http.Error(w, `{"error":"name cannot be empty"}`, http.StatusBadRequest)
				return
			}
			key.Name = name
		}
		keyChanged := false
		if req.PublicKey != nil {
			pub := strings.TrimSpace(*req.PublicKey)
			if pub == "" {
				http.Error(w, `{"error":"public_key cannot be empty"}`, http.StatusBadRequest)
				return
			}
			parsed, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pub))
			if err != nil {
				http.Error(w, fmt.Sprintf(`{"error":"invalid public key format: %v"}`, err), http.StatusBadRequest)
				return
			}
			if pub != key.PublicKey {
				keyChanged = true
			}
			key.PublicKey = pub
			key.Fingerprint = ssh.FingerprintSHA256(parsed)
		}
		if req.AllSandboxes != nil {
			key.AllSandboxes = *req.AllSandboxes
		}
		if req.IsDefault != nil {
			key.IsDefault = *req.IsDefault
		}
		if err := s.vmMgr.GetStore().SaveSSHKey(r.Context(), key); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"failed to save key: %v"}`, err), http.StatusInternalServerError)
			return
		}
		if req.IsDefault != nil && *req.IsDefault {
			// Clear the flag from the owner's other keys; SaveSSHKey above
			// already persisted is_default for this row.
			if err := s.vmMgr.GetStore().SetDefaultSSHKey(r.Context(), key.OwnerID, key.ID); err != nil {
				http.Error(w, fmt.Sprintf(`{"error":"failed to set default key: %v"}`, err), http.StatusInternalServerError)
				return
			}
		}

		// Keep existing sandboxes in sync: update per-sandbox references when the
		// key material changed, then reconcile the owner's managed key set.
		if keyChanged && !key.AllSandboxes {
			if affected, err := s.vmMgr.ReplaceSandboxSSHKey(r.Context(), key.OwnerID, oldKey, key.PublicKey); err != nil {
				log.Printf("[ssh-keys] update sandbox references for %s: %v", key.ID, err)
			} else {
				for _, vmID := range affected {
					if err := s.vmMgr.SyncSSHKeysForVM(r.Context(), vmID); err != nil {
						log.Printf("[ssh-keys] sync %s after key update: %v", vmID, err)
					}
				}
			}
		}
		if keyChanged || (req.AllSandboxes != nil) {
			s.syncAccountSSHKeys(r.Context(), key)
		}
		s.recordEvent("ssh_key.updated", getActor(r), key.Name, "delivered", 15, map[string]interface{}{
			"id": key.ID, "name": key.Name, "all_sandboxes": key.AllSandboxes, "is_default": key.IsDefault,
		})
		json.NewEncoder(w).Encode(key)

	case http.MethodDelete:
		if err := s.vmMgr.GetStore().DeleteSSHKey(r.Context(), key.ID); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"failed to delete key: %v"}`, err), http.StatusInternalServerError)
			return
		}
		// Removing an account-wide key also removes it from existing sandboxes.
		if key.AllSandboxes {
			s.syncAccountSSHKeys(r.Context(), key)
		}
		s.recordEvent("ssh_key.deleted", getActor(r), key.Name, "delivered", 15, map[string]interface{}{"id": key.ID, "name": key.Name})
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted", "id": key.ID})

	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

// EnsureHostSSHKey auto-generates or loads the master host Ed25519 SSH key.
func (s *Server) EnsureHostSSHKey() (ssh.Signer, string, error) {
	var candidates []string
	if s.cfg.Images.SSHKeyPath != "" {
		candidates = append(candidates, s.cfg.Images.SSHKeyPath)
	}
	if s.cfg.Images.ImagesDir != "" {
		candidates = append(candidates, filepath.Join(s.cfg.Images.ImagesDir, "ssh", "id_ed25519"))
	}
	candidates = append(candidates, "/var/lib/compute/images/ssh/id_ed25519")
	if s.cfg.Server.DBPath != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(s.cfg.Server.DBPath), "ssh", "id_ed25519"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".ssh", "id_ed25519"))
	}

	// 1. Check if an existing key is already present
	for _, cand := range candidates {
		if cand == "" {
			continue
		}
		if data, err := os.ReadFile(cand); err == nil {
			signer, err := ssh.ParsePrivateKey(data)
			if err == nil {
				pubData, _ := os.ReadFile(cand + ".pub")
				return signer, string(pubData), nil
			}
		}
	}

	// 2. Fallback to temporary directory if system directories are not writable
	candidates = append(candidates, filepath.Join(os.TempDir(), "compute-ssh", "id_ed25519"))

	var genErr error
	for _, targetPath := range candidates {
		if targetPath == "" {
			continue
		}
		signer, pubBytes, err := generateHostEd25519(targetPath)
		if err == nil {
			return signer, string(pubBytes), nil
		}
		genErr = err
	}

	return nil, "", fmt.Errorf("failed to auto-generate master host ssh key: %w", genErr)
}

func generateHostEd25519(privPath string) (ssh.Signer, []byte, error) {
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	block, err := ssh.MarshalPrivateKey(privKey, "compute-host-master-key")
	if err != nil {
		return nil, nil, err
	}
	privPEM := pem.EncodeToMemory(block)

	sshPubKey, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		return nil, nil, err
	}
	pubBytes := ssh.MarshalAuthorizedKey(sshPubKey)

	if err := os.MkdirAll(filepath.Dir(privPath), 0700); err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(privPath, privPEM, 0600); err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(privPath+".pub", pubBytes, 0644); err != nil {
		return nil, nil, err
	}

	signer, err := ssh.ParsePrivateKey(privPEM)
	if err != nil {
		return nil, nil, err
	}

	return signer, pubBytes, nil
}

func (s *Server) resolveSSHKey(keyPath, pubKey, keyName string) []ssh.AuthMethod {
	var methods []ssh.AuthMethod

	candidates := []string{}
	if keyPath != "" {
		candidates = append(candidates, keyPath)
	}
	if s.cfg.Images.SSHKeyPath != "" {
		candidates = append(candidates, s.cfg.Images.SSHKeyPath)
	}
	candidates = append(candidates, "/var/lib/compute/images/ssh/id_ed25519")

	if home, err := os.UserHomeDir(); err == nil {
		sshDir := filepath.Join(home, ".ssh")
		if keyName != "" {
			for _, ext := range []string{"", ".pem", ".id"} {
				candidates = append(candidates, filepath.Join(sshDir, keyName+ext))
			}
		}
		for _, name := range []string{"id_ed25519", "prox", "id_rsa", "id_ecdsa"} {
			candidates = append(candidates, filepath.Join(sshDir, name))
		}
	}

	seen := make(map[string]bool)
	var signers []ssh.Signer
	for _, cand := range candidates {
		if cand == "" || seen[cand] {
			continue
		}
		seen[cand] = true
		data, err := os.ReadFile(cand)
		if err != nil {
			continue
		}
		signer, err := ssh.ParsePrivateKey(data)
		if err != nil {
			continue
		}
		signers = append(signers, signer)
	}

	// Auto-provision master host Ed25519 private key if none exists on host
	if len(signers) == 0 {
		if signer, _, err := s.EnsureHostSSHKey(); err == nil && signer != nil {
			signers = append(signers, signer)
		}
	}

	if len(signers) > 0 {
		methods = append(methods, ssh.PublicKeys(signers...))
	}

	return methods
}

func (s *Server) handleSSHDirect(w http.ResponseWriter, r *http.Request) {
	vmID := strings.TrimPrefix(r.URL.Path, "/api/v1/ssh/")
	vmID = strings.Trim(vmID, "/")
	if vmID == "" {
		httpError(w, "missing vm id", http.StatusBadRequest)
		return
	}
	s.handleVMSSH(w, r, vmID)
}

func (s *Server) handleVMSSH(w http.ResponseWriter, r *http.Request, vmID string) {
	// 1. Scoped API key enforcement: must possess ssh:vms, *, or admin
	if s.authEnabled() {
		authCtx := auth.GetAuth(r.Context())
		if authCtx != nil && authCtx.AuthType == "api_key" {
			if !s.scopePermits(authCtx, "ssh", "vms") {
				s.recordEvent("ssh.tunnel_attempt", getActor(r), vmID, "rejected", 0, map[string]interface{}{
					"reason": "insufficient_scope",
					"error":  "ssh:vms scope required",
				})
				httpError(w, "forbidden: insufficient scope for ssh:vms", http.StatusForbidden)
				return
			}
		}
	}

	v, err := s.resolveAccessibleVM(r.Context(), r, vmID)
	isWS := strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
	if err != nil || v == nil {
		s.recordEvent("ssh.tunnel_attempt", getActor(r), vmID, "rejected", 0, map[string]interface{}{
			"reason": "vm_not_found",
		})
		if isWS {
			opts := &websocket.AcceptOptions{InsecureSkipVerify: true}
			if conn, wsErr := websocket.Accept(w, r, opts); wsErr == nil {
				_ = conn.Write(r.Context(), websocket.MessageText, []byte("\r\n\x1b[31;1m[Compute Error]\x1b[0m VM not found: "+vmID+"\r\n"))
				_ = conn.Close(websocket.StatusNormalClosure, "vm not found")
			}
		} else {
			httpError(w, "vm not found", http.StatusNotFound)
		}
		return
	}

	if !s.canAccessOwner(r, v.OwnerID) {
		s.recordEvent("ssh.tunnel_attempt", getActor(r), vmID, "rejected", 0, map[string]interface{}{
			"reason": "forbidden_cross_tenant",
		})
		if isWS {
			opts := &websocket.AcceptOptions{InsecureSkipVerify: true}
			if conn, wsErr := websocket.Accept(w, r, opts); wsErr == nil {
				_ = conn.Write(r.Context(), websocket.MessageText, []byte("\r\n\x1b[31;1m[Compute Error]\x1b[0m VM not found: "+vmID+"\r\n"))
				_ = conn.Close(websocket.StatusNormalClosure, "vm not found")
			}
		} else {
			httpError(w, "vm not found", http.StatusNotFound)
		}
		return
	}

	if v.Status != "running" {
		if isWS {
			opts := &websocket.AcceptOptions{InsecureSkipVerify: true}
			if conn, wsErr := websocket.Accept(w, r, opts); wsErr == nil {
				msg := fmt.Sprintf("\r\n\x1b[33;1m[Compute Notice]\x1b[0m VM '%s' is currently %s.\r\nPlease start the instance to open an SSH session.\r\n", v.Name, v.Status)
				_ = conn.Write(r.Context(), websocket.MessageText, []byte(msg))
				_ = conn.Close(websocket.StatusNormalClosure, "vm not running")
			}
		} else {
			httpError(w, fmt.Sprintf("vm %s is not running (status: %s)", vmID, v.Status), http.StatusBadRequest)
		}
		return
	}

	if v.IPAddress == "" {
		if isWS {
			opts := &websocket.AcceptOptions{InsecureSkipVerify: true}
			if conn, wsErr := websocket.Accept(w, r, opts); wsErr == nil {
				msg := fmt.Sprintf("\r\n\x1b[31;1m[Compute Error]\x1b[0m VM '%s' has no allocated IP address.\r\n", v.Name)
				_ = conn.Write(r.Context(), websocket.MessageText, []byte(msg))
				_ = conn.Close(websocket.StatusNormalClosure, "no ip address")
			}
		} else {
			httpError(w, "vm has no allocated IP address", http.StatusBadRequest)
		}
		return
	}

	user := r.URL.Query().Get("user")
	if user == "" {
		user = "compute"
	}

	sshPort := "22"
	if p := r.URL.Query().Get("port"); p != "" {
		sshPort = p
	}
	targetAddr := net.JoinHostPort(v.IPAddress, sshPort)
	if host, port, err := net.SplitHostPort(v.IPAddress); err == nil {
		targetAddr = net.JoinHostPort(host, port)
	}

	isRawStream := r.URL.Query().Get("stream") == "raw" || r.URL.Query().Get("mode") == "stdio"

	// 2. Raw Stream / ProxyCommand Mode: Full-duplex WebSocket <-> TCP bridging
	if isRawStream {
		opts := &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		}
		wsConn, err := websocket.Accept(w, r, opts)
		if err != nil {
			return
		}
		defer wsConn.Close(websocket.StatusInternalError, "session closed")

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		s.recordEvent("ssh.tunnel_attempt", getActor(r), vmID, "connected", 0, map[string]interface{}{
			"mode":        "raw_stream",
			"target":      v.ID,
			"user":        user,
			"remote_addr": r.RemoteAddr,
			"timestamp":   time.Now().UTC().Format(time.RFC3339),
		})

		tcpConn, err := net.DialTimeout("tcp", targetAddr, 10*time.Second)
		if err != nil {
			errMsg := fmt.Sprintf("\r\n\x1b[31;1m[Compute SSH Error]\x1b[0m Connection to %s failed: %v\r\n", targetAddr, err)
			_ = wsConn.Write(ctx, websocket.MessageBinary, []byte(errMsg))
			_ = wsConn.Close(websocket.StatusInternalError, "tcp dial failed")
			return
		}
		defer tcpConn.Close()

		errc := make(chan error, 2)

		// WebSocket -> TCP
		go func() {
			for {
				msgType, data, rErr := wsConn.Read(ctx)
				if rErr != nil {
					errc <- rErr
					return
				}
				if len(data) > 0 {
					if _, wErr := tcpConn.Write(data); wErr != nil {
						errc <- wErr
						return
					}
				}
				_ = msgType
			}
		}()

		// TCP -> WebSocket
		go func() {
			buf := make([]byte, 32768)
			for {
				n, rErr := tcpConn.Read(buf)
				if n > 0 {
					if wErr := wsConn.Write(ctx, websocket.MessageBinary, buf[:n]); wErr != nil {
						errc <- wErr
						return
					}
				}
				if rErr != nil {
					errc <- rErr
					return
				}
			}
		}()

		select {
		case <-ctx.Done():
		case <-errc:
		}

		_ = tcpConn.Close()
		_ = wsConn.Close(websocket.StatusNormalClosure, "tunnel closed")
		return
	}

	// 3. Interactive PTY mode
	opts := &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	}
	wsConn, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}
	defer wsConn.Close(websocket.StatusInternalError, "session closed")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	s.recordEvent("ssh.tunnel_attempt", getActor(r), vmID, "connected", 0, map[string]interface{}{
		"mode":        "interactive",
		"target":      v.ID,
		"user":        user,
		"remote_addr": r.RemoteAddr,
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
	})

	var writeMu sync.Mutex
	safeWrite := func(typ websocket.MessageType, data []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return wsConn.Write(ctx, typ, data)
	}

	cmd := r.URL.Query().Get("cmd")
	if cmd == "" {
		cmd = r.URL.Query().Get("command")
	}

	ptyParam := r.URL.Query().Get("pty")
	usePTY := true
	if cmd != "" {
		usePTY = (ptyParam == "true" || ptyParam == "1")
	} else {
		usePTY = (ptyParam != "false" && ptyParam != "0")
	}

	cols := 80
	rows := 24
	if c := r.URL.Query().Get("cols"); c != "" {
		if val, convErr := strconv.Atoi(c); convErr == nil && val > 0 {
			cols = val
		}
	}
	if rw := r.URL.Query().Get("rows"); rw != "" {
		if val, convErr := strconv.Atoi(rw); convErr == nil && val > 0 {
			rows = val
		}
	}
	termType := r.URL.Query().Get("term")
	if termType == "" {
		termType = "xterm-256color"
	}

	authMethods := s.resolveSSHKey(v.SSHKeyPath, v.SSHPublicKey, v.SSHKeyName)
	if pwd := r.URL.Query().Get("password"); pwd != "" {
		authMethods = append(authMethods, ssh.Password(pwd))
	}

	if len(authMethods) == 0 {
		errMsg := "\r\n\x1b[31;1m[Compute SSH Error]\x1b[0m No usable SSH private key found on host to authenticate into VM.\r\n"
		_ = safeWrite(websocket.MessageText, []byte(errMsg))
		return
	}

	sshConfig := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	dest := targetAddr
	sshClient, err := ssh.Dial("tcp", dest, sshConfig)
	if err != nil {
		errMsg := fmt.Sprintf("\r\n\x1b[31;1m[Compute SSH Connection Failed]\x1b[0m %v\r\n\r\n\x1b[33mEnsure the VM guest SSH service has finished initializing.\x1b[0m\r\n", err)
		_ = safeWrite(websocket.MessageText, []byte(errMsg))
		return
	}
	defer sshClient.Close()

	session, err := sshClient.NewSession()
	if err != nil {
		errMsg := fmt.Sprintf("\r\n\x1b[31;1m[Compute SSH Session Failed]\x1b[0m %v\r\n", err)
		_ = safeWrite(websocket.MessageText, []byte(errMsg))
		return
	}
	defer session.Close()

	if usePTY {
		modes := ssh.TerminalModes{
			ssh.ECHO:          1,
			ssh.TTY_OP_ISPEED: 14400,
			ssh.TTY_OP_OSPEED: 14400,
		}
		if err := session.RequestPty(termType, rows, cols, modes); err != nil {
			errMsg := fmt.Sprintf("\r\n\x1b[31;1m[Compute PTY Allocation Failed]\x1b[0m %v\r\n", err)
			_ = safeWrite(websocket.MessageText, []byte(errMsg))
			return
		}
	}

	stdinPipe, err := session.StdinPipe()
	if err != nil {
		return
	}
	defer stdinPipe.Close()

	stdoutPipe, err := session.StdoutPipe()
	if err != nil {
		return
	}
	stderrPipe, err := session.StderrPipe()
	if err != nil {
		return
	}

	if cmd != "" {
		err = session.Start(cmd)
	} else {
		err = session.Shell()
	}
	if err != nil {
		errMsg := fmt.Sprintf("\r\n\x1b[31;1m[Compute Exec Failed]\x1b[0m %v\r\n", err)
		_ = safeWrite(websocket.MessageText, []byte(errMsg))
		return
	}

	var outWG sync.WaitGroup
	outWG.Add(2)
	pipeOutput := func(rd io.Reader) {
		defer outWG.Done()
		buf := make([]byte, 4096)
		for {
			n, rErr := rd.Read(buf)
			if n > 0 {
				if wErr := safeWrite(websocket.MessageBinary, buf[:n]); wErr != nil {
					cancel()
					return
				}
			}
			if rErr != nil {
				cancel()
				return
			}
		}
	}
	go pipeOutput(stdoutPipe)
	go pipeOutput(stderrPipe)

	// Inbound loop: WebSocket -> SSH stdin / resize
	go func() {
		defer cancel()
		for {
			msgType, data, rErr := wsConn.Read(ctx)
			if rErr != nil {
				return
			}
			if msgType == websocket.MessageText && len(data) > 0 && data[0] == '{' {
				var ctrl struct {
					Type string `json:"type"`
					Cols int    `json:"cols"`
					Rows int    `json:"rows"`
				}
				if jErr := json.Unmarshal(data, &ctrl); jErr == nil && ctrl.Type == "resize" {
					if usePTY && ctrl.Cols > 0 && ctrl.Rows > 0 {
						_ = session.WindowChange(ctrl.Rows, ctrl.Cols)
					}
					continue
				}
			}
			if _, wErr := stdinPipe.Write(data); wErr != nil {
				return
			}
		}
	}()

	waitErr := session.Wait()
	// Flush all buffered stdout/stderr before signalling exit so the client
	// never loses the tail of a short command.
	outWG.Wait()
	exitCode := 0
	if waitErr != nil {
		if exitErr, ok := waitErr.(*ssh.ExitError); ok {
			exitCode = exitErr.ExitStatus()
		} else {
			exitCode = 1
		}
	}
	exitMsg, _ := json.Marshal(map[string]interface{}{
		"type": "exit",
		"code": exitCode,
	})
	_ = safeWrite(websocket.MessageText, exitMsg)
	_ = wsConn.Close(websocket.StatusNormalClosure, fmt.Sprintf("exit %d", exitCode))
}

func (s *Server) handleVMTerminal(w http.ResponseWriter, r *http.Request, vmID string) {
	v, err := s.resolveAccessibleVM(r.Context(), r, vmID)
	if err != nil || v == nil {
		opts := &websocket.AcceptOptions{InsecureSkipVerify: true}
		if conn, wsErr := websocket.Accept(w, r, opts); wsErr == nil {
			_ = conn.Write(r.Context(), websocket.MessageText, []byte("\r\n\x1b[31;1m[Compute Error]\x1b[0m VM not found: "+vmID+"\r\n"))
			_ = conn.Close(websocket.StatusNormalClosure, "vm not found")
		} else {
			httpError(w, "vm not found", http.StatusNotFound)
		}
		return
	}

	if !s.canAccessOwner(r, v.OwnerID) {
		opts := &websocket.AcceptOptions{InsecureSkipVerify: true}
		if conn, wsErr := websocket.Accept(w, r, opts); wsErr == nil {
			_ = conn.Write(r.Context(), websocket.MessageText, []byte("\r\n\x1b[31;1m[Compute Error]\x1b[0m VM not found: "+vmID+"\r\n"))
			_ = conn.Close(websocket.StatusNormalClosure, "vm not found")
		} else {
			httpError(w, "vm not found", http.StatusNotFound)
		}
		return
	}

	if v.Status != "running" {
		opts := &websocket.AcceptOptions{InsecureSkipVerify: true}
		if conn, wsErr := websocket.Accept(w, r, opts); wsErr == nil {
			msg := fmt.Sprintf("\r\n\x1b[33;1m[Compute Notice]\x1b[0m VM '%s' is currently %s.\r\nPlease start the instance to open an SSH terminal session.\r\n", v.Name, v.Status)
			_ = conn.Write(r.Context(), websocket.MessageText, []byte(msg))
			_ = conn.Close(websocket.StatusNormalClosure, "vm not running")
		} else {
			httpError(w, fmt.Sprintf("vm %s is not running (status: %s)", vmID, v.Status), http.StatusBadRequest)
		}
		return
	}

	if v.IPAddress == "" {
		opts := &websocket.AcceptOptions{InsecureSkipVerify: true}
		if conn, wsErr := websocket.Accept(w, r, opts); wsErr == nil {
			msg := fmt.Sprintf("\r\n\x1b[31;1m[Compute Error]\x1b[0m VM '%s' has no allocated IP address.\r\n", v.Name)
			_ = conn.Write(r.Context(), websocket.MessageText, []byte(msg))
			_ = conn.Close(websocket.StatusNormalClosure, "no ip address")
		} else {
			httpError(w, "vm has no allocated IP address", http.StatusBadRequest)
		}
		return
	}

	opts := &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	}
	wsConn, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}
	defer wsConn.Close(websocket.StatusInternalError, "session closed")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	var writeMu sync.Mutex
	safeWrite := func(typ websocket.MessageType, data []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return wsConn.Write(ctx, typ, data)
	}

	user := r.URL.Query().Get("user")
	if user == "" {
		user = "compute"
	}

	authMethods := s.resolveSSHKey(v.SSHKeyPath, v.SSHPublicKey, v.SSHKeyName)
	if pwd := r.URL.Query().Get("password"); pwd != "" {
		authMethods = append(authMethods, ssh.Password(pwd))
	}

	if len(authMethods) == 0 {
		errMsg := "\r\n\x1b[31;1m[Compute SSH Error]\x1b[0m No usable SSH private key found on host to authenticate into VM.\r\n"
		_ = safeWrite(websocket.MessageText, []byte(errMsg))
		return
	}

	sshConfig := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	dest := net.JoinHostPort(v.IPAddress, "22")
	_ = safeWrite(websocket.MessageText, []byte(fmt.Sprintf("\x1b[36m[Compute]\x1b[0m Establishing SSH terminal session to %s@%s...\r\n", user, dest)))

	sshClient, err := ssh.Dial("tcp", dest, sshConfig)
	if err != nil {
		errMsg := fmt.Sprintf("\r\n\x1b[31;1m[Compute SSH Connection Failed]\x1b[0m %v\r\n\r\n\x1b[33mEnsure the VM guest SSH service has finished initializing.\x1b[0m\r\n", err)
		_ = safeWrite(websocket.MessageText, []byte(errMsg))
		return
	}
	defer sshClient.Close()

	session, err := sshClient.NewSession()
	if err != nil {
		errMsg := fmt.Sprintf("\r\n\x1b[31;1m[Compute SSH Session Failed]\x1b[0m %v\r\n", err)
		_ = safeWrite(websocket.MessageText, []byte(errMsg))
		return
	}
	defer session.Close()

	cols := 100
	rows := 30
	if c := r.URL.Query().Get("cols"); c != "" {
		if val, convErr := strconv.Atoi(c); convErr == nil && val > 0 {
			cols = val
		}
	}
	if rw := r.URL.Query().Get("rows"); rw != "" {
		if val, convErr := strconv.Atoi(rw); convErr == nil && val > 0 {
			rows = val
		}
	}

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	termType := r.URL.Query().Get("term")
	if termType == "" {
		termType = "xterm-256color"
	}
	if err := session.RequestPty(termType, rows, cols, modes); err != nil {
		errMsg := fmt.Sprintf("\r\n\x1b[31;1m[Compute PTY Allocation Failed]\x1b[0m %v\r\n", err)
		_ = safeWrite(websocket.MessageText, []byte(errMsg))
		return
	}

	stdinPipe, err := session.StdinPipe()
	if err != nil {
		return
	}
	defer stdinPipe.Close()

	stdoutPipe, err := session.StdoutPipe()
	if err != nil {
		return
	}
	stderrPipe, err := session.StderrPipe()
	if err != nil {
		return
	}

	if err := session.Shell(); err != nil {
		errMsg := fmt.Sprintf("\r\n\x1b[31;1m[Compute Shell Exec Failed]\x1b[0m %v\r\n", err)
		_ = safeWrite(websocket.MessageText, []byte(errMsg))
		return
	}

	var outWG sync.WaitGroup
	outWG.Add(2)
	pipeOutput := func(rd io.Reader) {
		defer outWG.Done()
		buf := make([]byte, 4096)
		for {
			n, rErr := rd.Read(buf)
			if n > 0 {
				if wErr := safeWrite(websocket.MessageBinary, buf[:n]); wErr != nil {
					cancel()
					return
				}
			}
			if rErr != nil {
				cancel()
				return
			}
		}
	}
	go pipeOutput(stdoutPipe)
	go pipeOutput(stderrPipe)

	// Inbound loop: WebSocket -> SSH stdin / resize
	go func() {
		defer cancel()
		for {
			msgType, data, rErr := wsConn.Read(ctx)
			if rErr != nil {
				return
			}
			if msgType == websocket.MessageText && len(data) > 0 && data[0] == "{"[0] {
				var ctrl struct {
					Type string `json:"type"`
					Cols int    `json:"cols"`
					Rows int    `json:"rows"`
				}
				if jErr := json.Unmarshal(data, &ctrl); jErr == nil && ctrl.Type == "resize" {
					if ctrl.Cols > 0 && ctrl.Rows > 0 {
						_ = session.WindowChange(ctrl.Rows, ctrl.Cols)
					}
					continue
				}
			}
			if _, wErr := stdinPipe.Write(data); wErr != nil {
				return
			}
		}
	}()

	_ = session.Wait()
	_ = safeWrite(websocket.MessageText, []byte("\r\n\x1b[33m[Session closed by remote host]\x1b[0m\r\n"))
	_ = wsConn.Close(websocket.StatusNormalClosure, "session exited")
}

// Worker and Job endpoints

func (s *Server) handleWorkerConnect(w http.ResponseWriter, r *http.Request) {
	var workerToken string
	if t := r.Header.Get("X-Worker-Token"); t != "" {
		workerToken = t
	} else if authHdr := r.Header.Get("Authorization"); strings.HasPrefix(authHdr, "Bearer ") {
		workerToken = strings.TrimPrefix(authHdr, "Bearer ")
	} else if t := r.URL.Query().Get("token"); t != "" {
		workerToken = t
	}

	if s.authEnabled() {
		if workerToken == "" {
			http.Error(w, "unauthorized: worker token required", http.StatusUnauthorized)
			return
		}

		if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
			http.Error(w, "unauthorized: store unavailable", http.StatusUnauthorized)
			return
		}

		if err := s.vmMgr.GetStore().AuthorizeWorkerToken(r.Context(), workerToken, r.Header.Get("X-Worker-Name")); err != nil {
			http.Error(w, "unauthorized: "+err.Error(), http.StatusUnauthorized)
			return
		}
	}

	opts := &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	}
	conn, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	_, msgBytes, err := conn.Read(ctx)
	if err != nil {
		return
	}
	var hello api.WorkerHello
	if err := json.Unmarshal(msgBytes, &hello); err != nil {
		return
	}

	tokenToConsume := hello.Token
	if tokenToConsume == "" {
		tokenToConsume = workerToken
	}
	if !s.authEnabled() && tokenToConsume != "" && s.vmMgr != nil && s.vmMgr.GetStore() != nil {
		_, _ = s.vmMgr.GetStore().ConsumeWorkerToken(ctx, tokenToConsume)
	}

	workerID := hello.ID
	if workerID == "" {
		workerID = hello.Name
	}
	if workerID == "" {
		workerID = fmt.Sprintf("nod_%s", uuid.NewString()[:8])
	}

	ack := api.WorkerHelloAck{
		WorkerMessage: api.WorkerMessage{
			Version:  1,
			Type:     "worker.hello_ack",
			ID:       uuid.NewString(),
			WorkerID: workerID,
		},
		Accepted:            true,
		WorkerID:            workerID,
		HeartbeatIntervalMS: 10000,
	}
	ackBytes, _ := json.Marshal(ack)
	if err := conn.Write(ctx, websocket.MessageText, ackBytes); err != nil {
		return
	}

	workerObj := scheduler.NewWorker(workerID, "default", hello.Capabilities, conn)
	if err := s.workers.Register(workerObj); err != nil {
		return
	}
	if s.vmMgr != nil && s.vmMgr.GetStore() != nil {
		now := time.Now().UTC()
		capJSON, _ := json.Marshal(hello.Capabilities)
		node := &db.WorkerNode{
			ID:               workerID,
			Name:             workerID,
			PoolID:           "default",
			Role:             "worker",
			Status:           "ready",
			Arch:             hello.Architecture,
			AgentVersion:     hello.AgentVersion,
			FCVersion:        s.fcVersion,
			UnderlayIP:       workerUnderlayIP(hello.Capabilities),
			CapabilitiesJSON: string(capJSON),
			LastHeartbeat:    &now,
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		s.enrichWorkerNodes([]*db.WorkerNode{node})
		_ = s.vmMgr.GetStore().SaveWorkerNode(ctx, node)
	}
	if s.scheduler != nil {
		go func() {
			_, _ = s.scheduler.ScheduleNext(context.Background())
		}()
	}

	var handler func(context.Context, *scheduler.Worker, api.WorkerMessage, json.RawMessage) error
	if s.scheduler != nil {
		handler = func(c context.Context, wrk *scheduler.Worker, env api.WorkerMessage, raw json.RawMessage) error {
			return s.scheduler.HandleWorkerMessage(c, wrk, env, []byte(raw))
		}
	}

	_ = s.workers.ServeStream(ctx, workerObj, handler)
}

func (s *Server) handleWorkerTokens(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAccess(w, r) {
		return
	}
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		httpError(w, "store not available", http.StatusInternalServerError)
		return
	}
	ctx := r.Context()
	st := s.vmMgr.GetStore()

	// Check for DELETE /api/v1/worker-tokens/:id
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/worker-tokens")
	path = strings.TrimPrefix(path, "/")

	if r.Method == http.MethodDelete && path != "" {
		if err := st.DeleteWorkerToken(ctx, path); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.recordEvent("worker_token.deleted", getActor(r), path, "delivered", 10, nil)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		tokens, err := st.ListWorkerTokens(ctx)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if tokens == nil {
			tokens = []*db.WorkerToken{}
		}
		out := make([]api.WorkerTokenResponse, 0, len(tokens))
		for _, t := range tokens {
			prefix := t.Token
			if len(prefix) > 8 {
				prefix = prefix[:8]
			}
			expUnix := t.ExpiresAt.Unix()
			out = append(out, api.WorkerTokenResponse{
				ID:        t.Token,
				Name:      t.Name,
				Token:     t.Token,
				Prefix:    prefix,
				PoolID:    t.PoolID,
				ExpiresAt: &expUnix,
				CreatedAt: t.CreatedAt.Unix(),
			})
		}
		json.NewEncoder(w).Encode(out)

	case http.MethodPost:
		var req api.WorkerTokenCreateRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		poolID := req.PoolID
		if poolID == "" {
			poolID = "default"
		}
		name := req.Name
		// An empty name means "unbound": the daemon binds the token to the
		// first worker that enrolls with it, and that worker may reconnect
		// later. A caller-supplied name pins enrollment to a worker name.
		tokenStr := fmt.Sprintf("mft_%s", uuid.NewString()[:16])
		expiresAt := time.Now().UTC().Add(30 * time.Minute)
		now := time.Now().UTC()

		token := &db.WorkerToken{
			Token:      tokenStr,
			Name:       name,
			PoolID:     poolID,
			LabelsJSON: "{}",
			ExpiresAt:  expiresAt,
			CreatedAt:  now,
		}
		if err := st.SaveWorkerToken(ctx, token); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}

		s.recordEvent("worker_token.created", getActor(r), name, "delivered", 15, map[string]interface{}{
			"pool_id": poolID,
			"token":   tokenStr,
		})

		w.WriteHeader(http.StatusCreated)
		expUnix := expiresAt.Unix()
		_ = json.NewEncoder(w).Encode(api.WorkerTokenResponse{
			ID:        tokenStr,
			Name:      name,
			Token:     tokenStr,
			Prefix:    tokenStr[:8],
			PoolID:    poolID,
			ExpiresAt: &expUnix,
			CreatedAt: now.Unix(),
		})

	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) enrichWorkerNodes(nodes []*db.WorkerNode) {
	var fallbackVCPUs, fallbackMemMB, fallbackDiskMB int
	if s.vmMgr != nil {
		limits := s.vmMgr.GetResourceLimits()
		fallbackVCPUs, fallbackMemMB, fallbackDiskMB = getHostResources(limits.MaxTotalVCPUs, limits.MaxTotalMemoryMB, int(limits.MaxTotalDiskMB))
	}
	for _, n := range nodes {
		if n.TotalVCPUs == 0 && n.CapabilitiesJSON != "" {
			var cap struct {
				VCPUs        int    `json:"vcpus"`
				CPUs         int    `json:"cpus"`
				MemoryMB     int    `json:"memory_mb"`
				DiskMB       int    `json:"disk_mb"`
				Architecture string `json:"architecture"`
			}
			if err := json.Unmarshal([]byte(n.CapabilitiesJSON), &cap); err == nil {
				if cap.VCPUs > 0 {
					n.TotalVCPUs = cap.VCPUs
				} else if cap.CPUs > 0 {
					n.TotalVCPUs = cap.CPUs
				}
				if cap.MemoryMB > 0 {
					n.TotalMemMB = cap.MemoryMB
				}
				if cap.DiskMB > 0 {
					n.TotalDiskMB = cap.DiskMB
				}
				if n.Arch == "" && cap.Architecture != "" {
					n.Arch = cap.Architecture
				}
			}
		}
		if n.TotalVCPUs == 0 && fallbackVCPUs > 0 {
			n.TotalVCPUs = fallbackVCPUs
		}
		if n.TotalMemMB == 0 && fallbackMemMB > 0 {
			n.TotalMemMB = fallbackMemMB
		}
		if n.TotalDiskMB == 0 && fallbackDiskMB > 0 {
			n.TotalDiskMB = fallbackDiskMB
		}
		if n.Arch == "" {
			n.Arch = runtime.GOARCH
		}
		if n.AgentVersion == "" {
			n.AgentVersion = version.Version
		}
		if n.FCVersion == "" {
			n.FCVersion = s.fcVersion
		}
		if n.UnderlayIP == "" {
			n.UnderlayIP = "127.0.0.1"
		}
	}
}

// workerUnderlayIP extracts the node's network address from its capability map.
func workerUnderlayIP(caps map[string]interface{}) string {
	if caps != nil {
		if v, ok := caps["underlay_ip"].(string); ok && v != "" {
			return v
		}
	}
	return "127.0.0.1"
}

func (s *Server) handleWorkers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireAdminAccess(w, r) {
		return
	}
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		json.NewEncoder(w).Encode([]*db.WorkerNode{})
		return
	}
	ctx := r.Context()
	st := s.vmMgr.GetStore()

	// Refresh nod_host_local heartbeat & limits live from host
	if localNode, err := st.GetWorkerNode(ctx, "nod_host_local"); err == nil && localNode != nil {
		now := time.Now().UTC()
		localNode.LastHeartbeat = &now
		limits := s.vmMgr.GetResourceLimits()
		vcpus, memMB, diskMB := getHostResources(limits.MaxTotalVCPUs, limits.MaxTotalMemoryMB, int(limits.MaxTotalDiskMB))
		localNode.TotalVCPUs = vcpus
		localNode.TotalMemMB = memMB
		localNode.TotalDiskMB = diskMB
		_ = st.SaveWorkerNode(ctx, localNode)
	}

	nodes, err := st.ListWorkerNodes(ctx)
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.enrichWorkerNodes(nodes)
	if nodes == nil {
		nodes = []*db.WorkerNode{}
	}
	json.NewEncoder(w).Encode(nodes)
}

func (s *Server) handleWorkerActions(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAccess(w, r) {
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/workers/")
	path = strings.Trim(path, "/")
	if path == "" {
		httpError(w, "worker id required", http.StatusBadRequest)
		return
	}

	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		httpError(w, "store not available", http.StatusInternalServerError)
		return
	}
	ctx := r.Context()
	st := s.vmMgr.GetStore()

	// Support POST /api/v1/workers/reconcile
	if path == "reconcile" {
		if r.Method != http.MethodPost {
			httpError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		nodes, err := st.ListWorkerNodes(ctx)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		now := time.Now().UTC()
		for _, n := range nodes {
			n.LastHeartbeat = &now
			if n.Status != "cordoned" {
				n.Status = "ready"
			}
			_ = st.SaveWorkerNode(ctx, n)
		}
		s.recordEvent("worker.reconciled_all", getActor(r), "all", "delivered", 20, map[string]interface{}{"count": len(nodes)})
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "reconciled",
			"count":  len(nodes),
		})
		return
	}

	parts := strings.Split(path, "/")
	id := parts[0]

	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			node, err := st.GetWorkerNode(ctx, id)
			if err != nil {
				httpError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if node == nil {
				httpError(w, "worker not found", http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(node)
		case http.MethodDelete:
			if err := st.DeleteWorkerNode(ctx, id); err != nil {
				httpError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			s.recordEvent("worker.deleted", getActor(r), id, "delivered", 15, nil)
			w.WriteHeader(http.StatusNoContent)
		default:
			httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	action := parts[1]
	if r.Method != http.MethodPost {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	switch action {
	case "cordon":
		if err := st.UpdateWorkerNodeStatus(ctx, id, "cordoned"); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.recordEvent("worker.cordon", getActor(r), id, "delivered", 15, nil)
		json.NewEncoder(w).Encode(map[string]string{"status": "cordoned"})
	case "uncordon":
		if err := st.UpdateWorkerNodeStatus(ctx, id, "ready"); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.recordEvent("worker.uncordon", getActor(r), id, "delivered", 15, nil)
		json.NewEncoder(w).Encode(map[string]string{"status": "ready"})
	case "drain":
		if err := st.UpdateWorkerNodeStatus(ctx, id, "draining"); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.recordEvent("worker.drain", getActor(r), id, "delivered", 15, nil)
		json.NewEncoder(w).Encode(map[string]string{"status": "draining"})
	case "reconcile":
		node, err := st.GetWorkerNode(ctx, id)
		if err != nil || node == nil {
			httpError(w, "worker not found", http.StatusNotFound)
			return
		}
		now := time.Now().UTC()
		node.LastHeartbeat = &now
		node.Status = "ready"
		if err := st.SaveWorkerNode(ctx, node); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.recordEvent("worker.reconcile", getActor(r), id, "delivered", 15, nil)
		json.NewEncoder(w).Encode(map[string]string{"status": "ready"})
	default:
		httpError(w, "unknown action", http.StatusNotFound)
	}
}

func (s *Server) handlePools(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAccess(w, r) {
		return
	}
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		httpError(w, "store not available", http.StatusInternalServerError)
		return
	}
	switch r.Method {
	case http.MethodGet:
		pools, err := s.vmMgr.GetStore().ListWorkerPools(r.Context())
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if pools == nil {
			pools = []*db.WorkerPool{}
		}
		json.NewEncoder(w).Encode(pools)
	case http.MethodPost:
		var req api.PoolCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, err.Error(), http.StatusBadRequest)
			return
		}
		pool := &db.WorkerPool{
			ID:         uuid.NewString(),
			Name:       req.Name,
			LabelsJSON: "{}",
			PolicyJSON: "{}",
			CreatedAt:  time.Now().UTC(),
			UpdatedAt:  time.Now().UTC(),
		}
		if err := s.vmMgr.GetStore().SaveWorkerPool(r.Context(), pool); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(pool)
	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handlePoolActions(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAccess(w, r) {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/pools/")
	if id == "" {
		httpError(w, "pool id required", http.StatusBadRequest)
		return
	}
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		httpError(w, "store not available", http.StatusInternalServerError)
		return
	}
	switch r.Method {
	case http.MethodGet:
		pool, err := s.vmMgr.GetStore().GetWorkerPool(r.Context(), id)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if pool == nil {
			httpError(w, "pool not found", http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(pool)
	case http.MethodDelete:
		if err := s.vmMgr.GetStore().DeleteWorkerPool(r.Context(), id); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		httpError(w, "store not available", http.StatusInternalServerError)
		return
	}
	switch r.Method {
	case http.MethodGet:
		jobs, err := s.listAccessibleJobs(r.Context(), r)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if jobs == nil {
			jobs = []*db.Job{}
		}
		json.NewEncoder(w).Encode(jobs)
	case http.MethodPost:
		ctx := r.Context()
		var req api.JobCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, err.Error(), http.StatusBadRequest)
			return
		}
		ownerID, isAdmin, ok := s.callerIdentity(r)
		if !ok {
			httpError(w, "authentication required", http.StatusUnauthorized)
			return
		}
		// Normalize default image to Ubuntu 24.04 LTS (Item #14)
		req.Spec.ImageID = normalizeCIImageID(req.Spec.ImageID)

		// Verify caller ownership of any referenced GitHub connection so a tenant
		// can never attach another tenant's RepoID and extract their GITHUB_TOKEN.
		repoID := strings.TrimSpace(req.RepoID)
		if repoID != "" {
			conn, connErr := s.vmMgr.GetStore().GetGitHubConnection(ctx, repoID)
			if connErr != nil {
				httpError(w, connErr.Error(), http.StatusInternalServerError)
				return
			}
			if conn == nil || !s.canAccessOwner(r, conn.OwnerID) {
				httpError(w, "connected repository not found", http.StatusNotFound)
				return
			}
			if strings.TrimSpace(req.RepoURL) == "" && strings.TrimSpace(req.Spec.RepoURL) == "" {
				req.RepoURL = conn.RepoURL
			}
			if strings.TrimSpace(req.RepoName) == "" && strings.TrimSpace(req.Spec.RepoName) == "" {
				req.RepoName = conn.Name
			}
		}

		// The job image is owner-scoped when it names a custom image or a
		// template. Without this check a tenant could boot another tenant's
		// private image through the worker/job pipeline.
		if req.Spec.ImageID != "" {
			ctx := r.Context()
			img, imgErr := s.resolveAccessibleImage(ctx, r, req.Spec.ImageID)
			if imgErr != nil {
				httpError(w, imgErr.Error(), http.StatusInternalServerError)
				return
			}
			if img != nil {
				req.Spec.ImageID = imageIdentifier(img)
			} else {
				tpl, tplErr := s.resolveAccessibleTemplate(ctx, r, req.Spec.ImageID)
				if tplErr != nil {
					httpError(w, tplErr.Error(), http.StatusInternalServerError)
					return
				}
				if tpl != nil {
					req.Spec.ImageID = templateIdentifier(tpl)
				} else if !s.vmMgr.IsPlatformBaseImage(req.Spec.ImageID) {
					if globalImg, _ := s.vmMgr.GetImage(ctx, req.Spec.ImageID); globalImg != nil {
						httpError(w, "image not found", http.StatusNotFound)
						return
					} else if globalTpl, _ := s.vmMgr.GetTemplate(ctx, req.Spec.ImageID); globalTpl != nil {
						httpError(w, "image not found", http.StatusNotFound)
						return
					}
				}
			}
		}
		// Jobs provision and run a sandbox, so both creation and concurrent-run
		// quotas apply before the job is accepted.
		jobVCPUs, jobMemoryMB, jobDiskMB := s.estimateRequestResources(ctx, vm.CreateRequest{
			Image:    req.Spec.ImageID,
			VCPUs:    req.Spec.VCPUs,
			MemoryMB: req.Spec.MemoryMB,
			DiskMB:   req.Spec.DiskMB,
		})
		if err := s.enforceCreateQuota(ctx, r, ownerID, jobVCPUs, jobMemoryMB, jobDiskMB); err != nil {
			httpError(w, err.Error(), http.StatusTooManyRequests)
			return
		}
		if err := s.enforceRunQuota(ctx, r, &db.VM{OwnerID: ownerID, VCPUs: jobVCPUs, MemoryMB: jobMemoryMB, DiskMB: jobDiskMB}); err != nil {
			httpError(w, err.Error(), http.StatusTooManyRequests)
			return
		}

		source := strings.TrimSpace(req.Source)
		if source == "" {
			source = strings.TrimSpace(req.Spec.Source)
		}
		if source == "" {
			source = "cli"
		}
		repoURL := strings.TrimSpace(req.RepoURL)
		if repoURL == "" {
			repoURL = strings.TrimSpace(req.Spec.RepoURL)
		}
		repoName := strings.TrimSpace(req.RepoName)
		if repoName == "" {
			repoName = strings.TrimSpace(req.Spec.RepoName)
		}
		branch := strings.TrimSpace(req.Branch)
		if branch == "" {
			branch = strings.TrimSpace(req.Spec.Branch)
		}
		commitSHA := strings.TrimSpace(req.CommitSHA)
		if commitSHA == "" {
			commitSHA = strings.TrimSpace(req.Spec.CommitSHA)
		}

		// Persist the control-plane authorization decision with the job so the
		// worker can honour the same owner scope after a restart. Client-supplied
		// values are always overwritten here.
		req.Spec.AllowCrossOwner = isAdmin
		req.Spec.AuthorizedOwnerIDs = nil
		if !isAdmin {
			req.Spec.AuthorizedOwnerIDs = s.ownerIDsFor(r)
		}
		req.Spec.Source = source
		if repoURL != "" {
			req.Spec.RepoURL = repoURL
		}
		if repoName != "" {
			req.Spec.RepoName = repoName
		}
		if branch != "" {
			req.Spec.Branch = branch
		}
		if commitSHA != "" {
			req.Spec.CommitSHA = commitSHA
		}

		specJSON, _ := json.Marshal(req.Spec)
		jobID := fmt.Sprintf("job_%s", uuid.NewString()[:8])
		now := time.Now().UTC()
		job := &db.Job{
			ID:        jobID,
			OwnerID:   ownerID,
			PoolID:    req.PoolID,
			Type:      req.Type,
			State:     "queued",
			Priority:  req.Priority,
			SpecJSON:  string(specJSON),
			RepoID:    strings.TrimSpace(req.RepoID),
			RepoURL:   repoURL,
			RepoName:  repoName,
			Branch:    branch,
			CommitSHA: commitSHA,
			Source:    source,
			QueuedAt:  now,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if job.Type == "" {
			job.Type = "command"
		}
		if err := s.vmMgr.GetStore().SaveJob(r.Context(), job); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}

		s.recordEvent("ci.job.queued", getActor(r), job.ID, "delivered", 15, map[string]interface{}{
			"type":     job.Type,
			"priority": job.Priority,
			"source":   job.Source,
			"repo_url": job.RepoURL,
		})

		if s.scheduler != nil {
			go func() {
				_, _ = s.scheduler.ScheduleNext(context.Background())
			}()
		} else if s.jobRunner != nil {
			go func() {
				s.jobRunner.TryStart(context.Background(), job, req.Spec)
			}()
		}

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(job)
	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleJobActions(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/jobs/")
	parts := strings.Split(path, "/")
	id := parts[0]
	if id == "" {
		httpError(w, "job id required", http.StatusBadRequest)
		return
	}
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		httpError(w, "store not available", http.StatusInternalServerError)
		return
	}

	job, err := s.vmMgr.GetStore().GetJob(r.Context(), id)
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if job == nil {
		httpError(w, "job not found", http.StatusNotFound)
		return
	}
	// Multi-tenant authorization check
	if !s.canAccessOwner(r, job.OwnerID) {
		httpError(w, "job not found", http.StatusNotFound)
		return
	}

	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			httpError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		json.NewEncoder(w).Encode(job)
		return
	}

	action := parts[1]
	switch action {
	case "cancel":
		if r.Method != http.MethodPost {
			httpError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		_ = s.vmMgr.GetStore().UpdateJobState(r.Context(), id, "canceled", "")
		if s.jobRunner != nil {
			s.jobRunner.Cancel(id)
		}
		s.recordEvent("ci.job.cancelled", getActor(r), id, "delivered", 15, nil)
		json.NewEncoder(w).Encode(map[string]string{"status": "canceled"})
	case "logs":
		if r.Method != http.MethodGet {
			httpError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		logs, err := s.vmMgr.GetStore().GetJobLogs(r.Context(), id)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if logs == nil {
			logs = []*db.JobLog{}
		}
		json.NewEncoder(w).Encode(logs)
	default:
		httpError(w, "unknown action", http.StatusNotFound)
	}
}
