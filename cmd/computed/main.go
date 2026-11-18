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

	"github.com/Rashid2G/compute/internal/config"
	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/doctor"
	"github.com/Rashid2G/compute/internal/gui"
	"github.com/Rashid2G/compute/internal/network"
	"github.com/Rashid2G/compute/internal/storage"
	"github.com/Rashid2G/compute/internal/vm"
	"github.com/Rashid2G/compute/pkg/version"
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
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
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

	// Authentication is on by default. Refuse to start without a credential
	// instead of falling back to a generated password that nobody can read, and
	// refuse to expose the quickstart default "admin" password beyond loopback.
	if err := validateStartupConfig(globalCfg); err != nil {
		log.Fatalf("%v", err)
	}

	// Fail before creating any host state if KVM, firecracker, images or the
	// internal SSH key are missing. The readiness endpoint repeats these checks
	// with remediation after startup.
	if report := doctor.Preflight(globalCfg); !report.OK && !envTruthy("COMPUTE_ALLOW_UNREADY_STARTUP") {
		for _, c := range report.Failures() {
			if c.Severity == doctor.Critical {
				log.Printf("[computed] preflight failed: %s: %s", c.Name, c.Detail)
				if c.Remediation != "" {
					log.Printf("[computed]   fix: %s", c.Remediation)
				}
			}
		}
		log.Fatalf("Startup preflight failed. Fix the critical checks above or set COMPUTE_ALLOW_UNREADY_STARTUP=true to start in a degraded state.")
	}

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

	// Enforce physical ownership ceilings for RAM and Disk on daemon startup.
	_, physMemMB, physDiskMB := getHostResources(0, 0, 0)
	if physMemMB > 0 && globalCfg.Resources.MaxTotalMemoryMB > physMemMB {
		log.Printf("[computed] Warning: configured max_total_memory_mb (%d MB) exceeds physical RAM (%d MB); clamping to physical capacity", globalCfg.Resources.MaxTotalMemoryMB, physMemMB)
		globalCfg.Resources.MaxTotalMemoryMB = physMemMB
	}
	if physDiskMB > 0 && globalCfg.Resources.MaxTotalDiskMB > int64(physDiskMB) {
		log.Printf("[computed] Warning: configured max_total_disk_mb (%d MB) exceeds physical disk (%d MB); clamping to physical capacity", globalCfg.Resources.MaxTotalDiskMB, physDiskMB)
		globalCfg.Resources.MaxTotalDiskMB = int64(physDiskMB)
	}

	// 5. Initialize VM Manager with Defaults, Resource Limits, Jailer, and Cgroups
	vmMgr := vm.NewManager(vm.Config{
		FirecrackerBinary: globalCfg.Server.FirecrackerBin,
		JailerBinary:      globalCfg.Server.JailerBinary,
		JailerEnabled:     globalCfg.Server.JailerEnabled,
		JailerBaseDir:     globalCfg.Server.JailerBaseDir,
		CgroupEnforce:     globalCfg.Server.CgroupEnforce,
		CgroupDevOverride: !globalCfg.Server.CgroupEnforce,
		Defaults:          globalCfg.Defaults,
		Resources:         globalCfg.Resources,
	}, store, netMgr, storMgr)

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
		var serveErr error
		if globalCfg.Server.TLSCertFile != "" {
			serveErr = httpServer.ServeTLS(listener, globalCfg.Server.TLSCertFile, globalCfg.Server.TLSKeyFile)
		} else {
			serveErr = httpServer.Serve(listener)
		}
		if serveErr != nil && serveErr != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", serveErr)
		}
	}()

	fmt.Printf("[computed] Daemon is ready and serving requests on %s\n", globalCfg.Server.ListenAddr)

	// Graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Printf("[computed] Shutting down...")
	// Firecracker children die with this container's PID namespace. Give
	// running guests a moment to flush dirty pages so sandbox disks stay
	// consistent and recent writes are not lost on restart.
	flushCtx, flushCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if flushed := vmMgr.FlushGuestFilesystems(flushCtx); flushed > 0 {
		log.Printf("[computed] flushed %d running sandbox filesystem(s)", flushed)
	}
	flushCancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = httpServer.Shutdown(shutdownCtx)
	log.Printf("[computed] Stopped.")
}

func validateStartupConfig(cfg config.GlobalConfig) error {
	if cfg.Server.AuthEnabled && cfg.Server.AdminPassword == "" && cfg.Server.AdminSecret == "" {
		return fmt.Errorf("Authentication is enabled but no admin credential is configured. Set COMPUTE_ADMIN_PASSWORD (recommended) or COMPUTE_ADMIN_SECRET in .env, or set COMPUTE_AUTH_ENABLED=false for a loopback-only development host.")
	}
	if cfg.Server.AuthEnabled && cfg.Server.AdminPassword == "admin" && !isLoopbackListen(cfg.Server.ListenAddr) {
		return fmt.Errorf("Refusing to start on non-loopback address %s with default admin password \"admin\". Set COMPUTE_ADMIN_PASSWORD to a strong random value in .env, or leave COMPUTE_ADMIN_PASSWORD= empty when authenticating with COMPUTE_ADMIN_SECRET.", cfg.Server.ListenAddr)
	}
	if (cfg.Server.TLSCertFile == "") != (cfg.Server.TLSKeyFile == "") {
		return fmt.Errorf("COMPUTE_TLS_CERT_FILE and COMPUTE_TLS_KEY_FILE must both be set (or both be empty).")
	}
	return nil
}

func isLoopbackListen(addr string) bool {
	if strings.HasPrefix(addr, "unix://") {
		return true
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
