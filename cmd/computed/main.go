package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/config"
	"github.com/Rashid2G/on-demand-vms/internal/db"
	"github.com/Rashid2G/on-demand-vms/internal/gui"
	"github.com/Rashid2G/on-demand-vms/internal/network"
	"github.com/Rashid2G/on-demand-vms/internal/storage"
	"github.com/Rashid2G/on-demand-vms/internal/tailscale"
	"github.com/Rashid2G/on-demand-vms/internal/vm"
	"github.com/Rashid2G/on-demand-vms/pkg/version"
)

func main() {
	if handleSubcommands() {
		return
	}

	configFile := flag.String("config", "", "Path to global daemon configuration JSON file (e.g. config/daemon.json)")
	configPathFlag := flag.Bool("config-path", false, "Display the path to the resolved configuration file and exit")
	flag.BoolVar(configPathFlag, "show-config-path", false, "Display the path to the resolved configuration file and exit")
	listenAddr := flag.String("listen", "", "Address to listen on (e.g. 0.0.0.0:18080 or unix:///run/compute.sock)")
	dbPath := flag.String("db-path", "", "Path to SQLite database file")
	imagesDir := flag.String("images-dir", "", "Base images directory")
	vmsDir := flag.String("vms-dir", "", "VM workspace directory")
	netConfigFile := flag.String("network-config", "", "Path to network configuration JSON file (legacy)")
	networkMode := flag.String("network-mode", "", "Network mode: 'nat', 'wired', 'nic', or 'bridge' (overrides config file)")
	bridgeName := flag.String("bridge", "", "Bridge interface name (overrides config file)")
	bridgeIP := flag.String("bridge-ip", "", "Bridge IPv4 CIDR (overrides config file)")
	subnet := flag.String("subnet", "", "Internal VM subnet CIDR (overrides config file)")
	physicalNIC := flag.String("physical-nic", "", "Host physical NIC name for 'wired'/'nic' mode (e.g. eth0)")
	gatewayIP := flag.String("gateway-ip", "", "Default IPv4 gateway for wired microVMs (overrides config file)")
	firecrackerBin := flag.String("firecracker-bin", "", "Path to firecracker executable")
	jailerFlag := flag.Bool("jailer", false, "Enable Firecracker jailer chroot sandbox isolation")
	jailerBinFlag := flag.String("jailer-bin", "", "Path to jailer executable")
	jailerDirFlag := flag.String("jailer-dir", "", "Base directory for jailer chroot environments")
	cgroupEnforceFlag := flag.Bool("cgroup-enforce", true, "Fail-closed enforcement of Cgroups v2 resource limits")
	versionFlag := flag.Bool("version", false, "Display version information and exit")
	flag.BoolVar(versionFlag, "v", false, "Display version information and exit")
	flag.Parse()

	if *versionFlag {
		info := version.Get("computed", "Compute Daemon")
		fmt.Printf("%s\n\n", info.String())
		fmt.Print(info.Detailed(""))
		fmt.Println()
		return
	}

	if *configPathFlag {
		_, loadedPath, err := config.ResolveConfig(*configFile)
		if err != nil && *configFile != "" {
			fmt.Fprintf(os.Stderr, "Error loading specified configuration from %s: %v\n", *configFile, err)
			os.Exit(1)
		}
		if loadedPath != "" {
			fmt.Println(loadedPath)
		} else {
			fmt.Println("No configuration file found (using default configuration)")
		}
		return
	}

	// 1. Resolve master configuration file
	globalCfg, loadedPath, err := config.ResolveConfig(*configFile)
	if err != nil && *configFile != "" {
		log.Fatalf("Failed to load specified configuration from %s: %v", *configFile, err)
	}
	globalCfg.ConfigPath = loadedPath

	// Legacy network config flag override
	if *netConfigFile != "" {
		if netCfg, err := network.LoadConfigFile(*netConfigFile); err == nil {
			globalCfg.Network = netCfg
			loadedPath = *netConfigFile
			globalCfg.ConfigPath = loadedPath
		} else {
			log.Fatalf("Failed to load specified network config from %s: %v", *netConfigFile, err)
		}
	}

	// Apply CLI overrides if explicitly passed
	if *listenAddr != "" {
		globalCfg.Server.ListenAddr = *listenAddr
	}
	if *dbPath != "" {
		globalCfg.Server.DBPath = *dbPath
	}
	if *imagesDir != "" {
		globalCfg.Images.ImagesDir = *imagesDir
	}
	if *vmsDir != "" {
		globalCfg.Images.VMsDir = *vmsDir
	}
	if *firecrackerBin != "" {
		globalCfg.Server.FirecrackerBin = *firecrackerBin
	}
	if *networkMode != "" {
		globalCfg.Network.Mode = network.Mode(*networkMode)
	}
	if *bridgeName != "" {
		globalCfg.Network.BridgeName = *bridgeName
	}
	if *bridgeIP != "" {
		globalCfg.Network.BridgeIP = *bridgeIP
	}
	if *subnet != "" {
		globalCfg.Network.SubnetCIDR = *subnet
	}
	if *physicalNIC != "" {
		globalCfg.Network.PhysicalNIC = *physicalNIC
	}
	if *gatewayIP != "" {
		globalCfg.Network.GatewayIP = *gatewayIP
	}

	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "jailer":
			globalCfg.Server.JailerEnabled = *jailerFlag
		case "jailer-bin":
			globalCfg.Server.JailerBinary = *jailerBinFlag
		case "jailer-dir":
			globalCfg.Server.JailerBaseDir = *jailerDirFlag
		case "cgroup-enforce":
			globalCfg.Server.CgroupEnforce = *cgroupEnforceFlag
		}
	})

	isWired := globalCfg.Network.Mode == network.ModeNIC || globalCfg.Network.Mode == network.ModeWired

	log.Printf("[computed] Starting daemon...")
	log.Printf("[computed] Config file: %s", loadedPath)
	log.Printf("[computed] Listening on: %s", globalCfg.Server.ListenAddr)
	log.Printf("[computed] Database: %s", globalCfg.Server.DBPath)
	log.Printf("[computed] Images dir: %s", globalCfg.Images.ImagesDir)
	log.Printf("[computed] VMs dir: %s", globalCfg.Images.VMsDir)
	log.Printf("[computed] Default VM specs: %d vCPUs, %d MB RAM, %d MB Disk, Image: %s",
		globalCfg.Defaults.VCPUs, globalCfg.Defaults.MemoryMB, globalCfg.Defaults.DiskSizeMB, globalCfg.Defaults.Image)
	log.Printf("[computed] Resource limits: max_running_vms=%d max_vcpus=%d max_ram=%dMB max_disk=%dMB min_free_disk=%dMB",
		globalCfg.Resources.MaxRunningVMs, globalCfg.Resources.MaxTotalVCPUs, globalCfg.Resources.MaxTotalMemoryMB,
		globalCfg.Resources.MaxTotalDiskMB, globalCfg.Resources.MinFreeDiskSpaceMB)
	log.Printf("[computed] Network: mode=%s bridge=%s ip=%s subnet=%s",
		globalCfg.Network.Mode, globalCfg.Network.BridgeName, globalCfg.Network.BridgeIP, globalCfg.Network.SubnetCIDR)
	log.Printf("[computed] Jailer: enabled=%v bin=%s base_dir=%s",
		globalCfg.Server.JailerEnabled, globalCfg.Server.JailerBinary, globalCfg.Server.JailerBaseDir)
	log.Printf("[computed] Cgroups: enforce=%v", globalCfg.Server.CgroupEnforce)
	if isWired {
		log.Printf("[computed] Wired dedicated IPs enabled on physical interface: %s (gateway: %s)",
			globalCfg.Network.PhysicalNIC, globalCfg.Network.GatewayIP)
	}

	// 2. Initialize SQLite Store
	store, err := db.NewStore(globalCfg.Server.DBPath)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer store.Close()

	// 3. Initialize Network Manager
	netMgr, err := network.NewManager(globalCfg.Network, nil)
	if err != nil {
		log.Fatalf("Failed to initialize network manager: %v", err)
	}

	// Ensure network topology is ready on startup
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := netMgr.EnsureNetwork(ctx); err != nil {
		log.Printf("[computed] Warning: could not initialize network (%v). Ensure running with appropriate permissions or sudo.", err)
	}
	cancel()

	// 4. Initialize Storage Manager
	var imageProfiles []storage.ImageProfile
	for _, img := range globalCfg.Images.Available {
		imageProfiles = append(imageProfiles, storage.ImageProfile{
			Name:        img.Name,
			Kernel:      img.Kernel,
			Rootfs:      img.Rootfs,
			Description: img.Description,
		})
	}
	storMgr, err := storage.NewManager(storage.Config{
		BaseImagesDir:   globalCfg.Images.ImagesDir,
		VMsDir:          globalCfg.Images.VMsDir,
		AvailableImages: imageProfiles,
	})
	if err != nil {
		log.Fatalf("Failed to initialize storage manager: %v", err)
	}

	// 5. Initialize VM Manager with Defaults, Resource Limits, Jailer, and Cgroups
	vmMgr := vm.NewManager(vm.Config{
		FirecrackerBinary:         globalCfg.Server.FirecrackerBin,
		JailerBinary:              globalCfg.Server.JailerBinary,
		JailerEnabled:             globalCfg.Server.JailerEnabled,
		JailerBaseDir:             globalCfg.Server.JailerBaseDir,
		CgroupEnforce:             globalCfg.Server.CgroupEnforce,
		CgroupDevOverride:         !globalCfg.Server.CgroupEnforce,
		Defaults:                  globalCfg.Defaults,
		Resources:                 globalCfg.Resources,
		TailscaleDefaultSandboxes: globalCfg.Tailscale.Enabled && globalCfg.Tailscale.DefaultSandboxes && !globalCfg.Tailscale.ServiceGatewayEnabled(),
	}, store, netMgr, storMgr)

	// Wire the tailnet integration.
	//
	// The Service gateway is the default model: this host is the only tailnet
	// device and sandboxes stay on the local bridge. Per-sandbox device
	// provisioning is legacy and only wired when explicitly requested.
	gatewayEnabled := globalCfg.Tailscale.ServiceGatewayEnabled()
	if gatewayEnabled && globalCfg.Tailscale.APIKey != "" {
		log.Printf("[computed] Tailscale Service gateway mode: sandboxes stay on the local bridge; this host exposes per-sandbox Services")
	} else if globalCfg.Tailscale.Enabled && globalCfg.Tailscale.APIKey != "" &&
		(globalCfg.Tailscale.LegacyPerSandboxDevices || globalCfg.Tailscale.DefaultSandboxes) {
		tsClient := tailscale.NewClient(globalCfg.Tailscale.APIKey, globalCfg.Tailscale.Tailnet)
		vmMgr.SetTailscaleProvisioner(&tailscale.Provisioner{
			Client:        tsClient,
			SandboxTag:    globalCfg.Tailscale.ResolvedSandboxTag(),
			KeyExpirySecs: globalCfg.Tailscale.KeyExpirySeconds,
			AutoInstall:   globalCfg.Tailscale.AutoInstallResolved(),
			Logger:        log.Default(),
		})
		log.Printf("[computed] Tailscale legacy per-sandbox device mode enabled (sandbox tag %s)", globalCfg.Tailscale.ResolvedSandboxTag())
	} else if globalCfg.Tailscale.Enabled {
		if globalCfg.Tailscale.APIKey == "" {
			log.Printf("[computed] Tailscale enabled but no API key configured; sandbox tailnet addressing unavailable")
		} else {
			log.Printf("[computed] Tailscale enabled but no Service gateway and no legacy mode; tailnet addressing disabled for sandboxes")
		}
	}

	// Reconcile and prune any leftover orphan TAPs from previous runs
	reconcileCtx, reconcileCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if pruned, err := vmMgr.ReconcileAndPrune(reconcileCtx); err != nil {
		log.Printf("[computed] Warning: reconciliation failed: %v", err)
	} else if len(pruned) > 0 {
		log.Printf("[computed] Pruned %d orphaned TAP interface(s): %v", len(pruned), pruned)
	}
	_ = vmMgr.RecoverQueue(reconcileCtx)
	reconcileCancel()

	// 6. Setup Server
	server := NewServer(vmMgr, globalCfg)

	// Restore sandboxes that were running before the daemon/host restarted.
	desiredCtx, desiredCancel := context.WithTimeout(context.Background(), 120*time.Second)
	vmMgr.StartDesiredVMs(desiredCtx)
	desiredCancel()

	// Re-apply stored host port-forwarding and guest runtime state.
	pfCtx, pfCancel := context.WithTimeout(context.Background(), 60*time.Second)
	server.ReconcilePortForwards(pfCtx)
	server.ReconcileRunningVMState(pfCtx)
	pfCancel()

	// Start continuous background status monitor to detect microVM exits and maintain synced status
	vmMgr.StartStatusMonitor(context.Background(), 3*time.Second)

	// Single-host Tailscale Service gateway: one tailnet device (this host)
	// exposes every sandbox as a Service while sandboxes stay on the local
	// bridge. Started before DNS so VIP answers are available.
	server.startTailscaleGateway()

	// Built-in DNS responder: gives every sandbox a routable
	// <sandbox>.<username>.<base-domain> name. SSH carries no hostname, so the
	// wildcard dispatch for SSH happens here, at the DNS layer.
	server.startDNS()

	// HTTP edge listener (default :80 with DNS enabled): dispatches by Host
	// header — the reserved "api" and apex/gui/www labels are served by the
	// daemon, every other subdomain goes to the sandbox ingress wildcard.
	startEdge(server, globalCfg)

	// Mount embedded Web GUI
	daemonTarget := globalCfg.Server.ListenAddr
	if !strings.HasPrefix(daemonTarget, "unix://") {
		if guiHandler, err := gui.NewStaticAssetHandler(); err == nil {
			server.SetGUIHandler(guiHandler)
			log.Printf("[computed] Web GUI mounted and active at http://%s/", daemonTarget)
		}
	}

	var listener net.Listener
	if strings.HasPrefix(globalCfg.Server.ListenAddr, "unix://") {
		sockPath := strings.TrimPrefix(globalCfg.Server.ListenAddr, "unix://")
		_ = os.Remove(sockPath)
		listener, err = net.Listen("unix", sockPath)
		if err != nil {
			log.Fatalf("Failed to listen on unix socket %s: %v", sockPath, err)
		}
		defer os.Remove(sockPath)
		_ = os.Chmod(sockPath, 0666)
	} else {
		listener, err = net.Listen("tcp", globalCfg.Server.ListenAddr)
		if err != nil {
			log.Fatalf("Failed to listen on TCP %s: %v", globalCfg.Server.ListenAddr, err)
		}
	}
	defer listener.Close()

	httpServer := &http.Server{
		Handler: server,
	}

	go func() {
		if err := httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	fmt.Printf("[computed] Daemon is ready and serving requests on %s\n", globalCfg.Server.ListenAddr)

	// Graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Printf("[computed] Shutting down...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = httpServer.Shutdown(shutdownCtx)
	log.Printf("[computed] Stopped.")
}
