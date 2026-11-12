package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/db"
	servicegateway "github.com/Rashid2G/on-demand-vms/internal/gateway"
	"github.com/Rashid2G/on-demand-vms/internal/tailscale"
)

// startTailscaleGateway launches the single-host Service gateway: one tailnet
// device (this host) exposes every local sandbox through per-sandbox Tailscale
// Services. Sandboxes never join the tailnet themselves.
func (s *Server) startTailscaleGateway() {
	cfg := s.cfg.Tailscale
	if !cfg.ServiceGatewayEnabled() {
		return
	}
	if cfg.APIKey == "" {
		log.Printf("[computed] tailscale service gateway disabled: no API key configured")
		return
	}

	admin := s.tailscaleAdminClient()
	serve := tailscale.NewServeRunner(cfg.GatewayTailscaleBinary(), cfg.GatewayTailscaleSocket())
	scanner := servicegateway.NewSSHScanner(cfg.GatewaySSHUser())
	if cfg.ServiceGateway != nil {
		scanner.ExposeLoopback = cfg.ServiceGateway.ExposeLoopback
	}

	opts := servicegateway.Options{
		ServicePrefix: cfg.GatewayServicePrefix(),
		ServiceTag:    cfg.ResolvedServiceTag(),
		TargetHost:    cfg.GatewayTargetAddress(),
		ExcludePorts:  cfg.GatewayExcludePorts(),
		IncludeSSH:    true,
		AutoApprove:   cfg.AutoApproveResolved(),
		Logger:        log.Default(),
	}

	mgr := servicegateway.NewManager(opts, admin, serve)
	s.gateway = mgr
	log.Printf("[computed] tailscale service gateway enabled (host tag %s, service tag %s, target %s)",
		cfg.ResolvedHostTag(), cfg.ResolvedServiceTag(), mgr.Target())

	go s.runGatewayLoop(mgr, scanner)
}

func (s *Server) runGatewayLoop(mgr *servicegateway.Manager, scanner servicegateway.Scanner) {
	interval := s.cfg.Tailscale.GatewayScanInterval()
	if interval < 10*time.Second {
		interval = 10 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	portCache := make(map[string][]int)
	var cacheMu sync.Mutex

	reconcile := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*interval+30*time.Second)
		defer cancel()
		specs, err := s.collectGatewaySpecs(ctx, scanner, portCache, &cacheMu)
		if err != nil {
			log.Printf("[computed] tailscale gateway reconcile skipped: %v", err)
			return
		}
		if err := mgr.Sync(ctx, specs); err != nil {
			log.Printf("[computed] tailscale gateway reconcile: %v", err)
		}
	}

	reconcile()
	for {
		select {
		case <-ticker.C:
			reconcile()
		case <-s.gatewayKick:
			reconcile()
		}
	}
}

// collectGatewaySpecs gathers running sandboxes and the TCP ports they listen
// on. A failed scan keeps the last successful port set (or SSH) so a transient
// SSH error never tears down a working Service.
func (s *Server) collectGatewaySpecs(ctx context.Context, scanner servicegateway.Scanner, cache map[string][]int, cacheMu *sync.Mutex) ([]servicegateway.SandboxSpec, error) {
	vms, err := s.vmMgr.ListVMs(ctx)
	if err != nil {
		return nil, err
	}

	var mu sync.Mutex
	specs := make([]servicegateway.SandboxSpec, 0, len(vms))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, v := range vms {
		if v == nil {
			continue
		}
		key := v.InternalID
		if key == "" {
			key = v.ID
			if v.OwnerID != "" {
				key += "-" + v.OwnerID
			}
		}
		spec := servicegateway.SandboxSpec{
			ID:         v.ID,
			Name:       v.Name,
			InternalID: key,
			GuestIP:    v.IPAddress,
			Running:    v.Status == db.StatusRunning,
		}
		if !spec.Running || spec.GuestIP == "" || spec.GuestIP == "dhcp" {
			mu.Lock()
			specs = append(specs, spec)
			mu.Unlock()
			continue
		}

		ports := make([]int, 0)
		wg.Add(1)
		go func(v *db.VM, spec servicegateway.SandboxSpec, key string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			// The SSH key path is per-sandbox; the guest account's key is
			// embedded at creation time.
			scanned, err := scanner.ListenPorts(ctx, v.IPAddress, v.SSHKeyPath)
			cacheMu.Lock()
			if err != nil {
				if cached, ok := cache[key]; ok {
					ports = append(ports, cached...)
				} else {
					ports = append(ports, 22)
				}
			} else {
				cache[key] = scanned
				ports = append(ports, scanned...)
			}
			cacheMu.Unlock()
			if err != nil {
				log.Printf("[computed] tailscale gateway: %s: scan ports: %v", v.ID, err)
			}
			spec.Ports = ports
			mu.Lock()
			specs = append(specs, spec)
			mu.Unlock()
		}(v, spec, key)
	}
	wg.Wait()
	return specs, nil
}

// kickTailscaleGateway requests an immediate reconcile (for example after a
// port forward is created or deleted).
func (s *Server) kickTailscaleGateway() {
	if s == nil || s.gateway == nil || s.gatewayKick == nil {
		return
	}
	select {
	case s.gatewayKick <- struct{}{}:
	default:
	}
}

// gatewayServiceDNS returns the MagicDNS name of a sandbox Service, or "".
func (s *Server) gatewayServiceDNS(ctx context.Context, v *db.VM) string {
	if s == nil || s.gateway == nil || v == nil {
		return ""
	}
	domain := s.tailnetDomainCached(ctx)
	if domain == "" {
		return ""
	}
	key := v.InternalID
	if key == "" {
		key = v.ID
		if v.OwnerID != "" {
			key += "-" + v.OwnerID
		}
	}
	name := s.gateway.ServiceNameFor(v.ID)
	if name == "" {
		name = servicegateway.ServiceName(s.cfg.Tailscale.GatewayServicePrefix(), v.Name, key)
	}
	if name == "" {
		return ""
	}
	return name + "." + domain
}

// gatewayServiceURL returns the stable Service URL for a sandbox port, or ""
// when the gateway is disabled or the tailnet domain is unknown. The Service
// definition is eventually consistent; the URL is valid once the reconcile
// creates it (kicked by the port-forward handler).
func (s *Server) gatewayServiceURL(ctx context.Context, v *db.VM, guestPort int) string {
	if s == nil || s.gateway == nil || v == nil || guestPort <= 0 {
		return ""
	}
	domain := s.tailnetDomainCached(ctx)
	if domain == "" {
		return ""
	}
	key := v.InternalID
	if key == "" {
		key = v.ID
		if v.OwnerID != "" {
			key += "-" + v.OwnerID
		}
	}
	name := servicegateway.ServiceName(s.cfg.Tailscale.GatewayServicePrefix(), v.Name, key)
	if name == "" {
		return ""
	}
	scheme := "http"
	if guestPort == 443 || guestPort == 8443 {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s:%d", scheme, name+"."+domain, guestPort)
}

// tailnetDomainCached resolves and caches the tailnet's MagicDNS suffix.
func (s *Server) tailnetDomainCached(ctx context.Context) string {
	s.tailnetDomainMu.RLock()
	domain, expiry := s.tailnetDomain, s.tailnetDomainExpiry
	s.tailnetDomainMu.RUnlock()
	if domain != "" && time.Now().Before(expiry) {
		return domain
	}

	if st, err := tailscale.DetectLocal(ctx); err == nil && st != nil && st.Running {
		if d := st.TailnetDomain(); d != "" {
			domain = d
		}
	}
	if domain == "" {
		if client := s.tailscaleAdminClient(); client != nil {
			if devices, err := client.ListDevices(ctx); err == nil {
				for _, d := range devices {
					if d.Name != "" {
						domain = strings.TrimSuffix(d.TailnetDomain(), ".")
						if domain != "" {
							break
						}
					}
				}
			}
		}
	}
	if domain == "" {
		return ""
	}
	s.tailnetDomainMu.Lock()
	s.tailnetDomain = domain
	s.tailnetDomainExpiry = time.Now().Add(5 * time.Minute)
	s.tailnetDomainMu.Unlock()
	return domain
}
