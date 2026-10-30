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

	"github.com/Rashid2G/on-demand-vms/internal/db"
	"github.com/Rashid2G/on-demand-vms/internal/network"
	"github.com/Rashid2G/on-demand-vms/internal/storage"
	"github.com/Rashid2G/on-demand-vms/internal/vm"
)

func main() {
	listenAddr := flag.String("listen", "0.0.0.0:18080", "Address to listen on (e.g. 0.0.0.0:18080 or unix:///run/mforge.sock)")
	dbPath := flag.String("db-path", "/var/lib/microforge/data/microforge.db", "Path to SQLite database file")
	imagesDir := flag.String("images-dir", "/var/lib/microforge/images", "Base images directory")
	vmsDir := flag.String("vms-dir", "/var/lib/microforge/vms", "VM workspace directory")
	netConfigFile := flag.String("network-config", "", "Path to network configuration JSON file")
	networkMode := flag.String("network-mode", "", "Network mode: 'nat', 'bridge', or 'nic' (overrides config file)")
	bridgeName := flag.String("bridge", "", "Bridge interface name (overrides config file)")
	bridgeIP := flag.String("bridge-ip", "", "Bridge IPv4 CIDR (overrides config file)")
	subnet := flag.String("subnet", "", "Internal VM subnet CIDR (overrides config file)")
	physicalNIC := flag.String("physical-nic", "", "Host physical NIC name for 'nic' mode (e.g. eth0)")
	firecrackerBin := flag.String("firecracker-bin", "/usr/local/bin/firecracker", "Path to firecracker executable")
	flag.Parse()

	// 1. Resolve network configuration
	netCfg := network.DefaultNATConfig()
	// Try loading from explicit config path or default locations
	cfgLoaded := false
	if *netConfigFile != "" {
		if loaded, err := network.LoadConfigFile(*netConfigFile); err == nil {
			netCfg = loaded
			cfgLoaded = true
		} else {
			log.Fatalf("Failed to load specified network config from %s: %v", *netConfigFile, err)
		}
	} else if loaded, err := network.LoadConfigFile("config/network.json"); err == nil {
		netCfg = loaded
		cfgLoaded = true
	} else if loaded, err := network.LoadConfigFile("/etc/microforge/network.json"); err == nil {
		netCfg = loaded
		cfgLoaded = true
	}

	// Apply CLI flag overrides if provided
	if *networkMode != "" {
		netCfg.Mode = network.Mode(*networkMode)
	}
	if *bridgeName != "" {
		netCfg.BridgeName = *bridgeName
	}
	if *bridgeIP != "" {
		netCfg.BridgeIP = *bridgeIP
	}
	if *subnet != "" {
		netCfg.SubnetCIDR = *subnet
	}
	if *physicalNIC != "" {
		netCfg.PhysicalNIC = *physicalNIC
	}

	log.Printf("[microforged] Starting daemon...")
	log.Printf("[microforged] Listening on: %s", *listenAddr)
	log.Printf("[microforged] Database: %s", *dbPath)
	log.Printf("[microforged] Images: %s", *imagesDir)
	log.Printf("[microforged] VMs directory: %s", *vmsDir)
	log.Printf("[microforged] Network: mode=%s bridge=%s ip=%s subnet=%s (from_file=%v)",
		netCfg.Mode, netCfg.BridgeName, netCfg.BridgeIP, netCfg.SubnetCIDR, cfgLoaded)
	if netCfg.Mode == network.ModeNIC {
		log.Printf("[microforged] Enslaved Physical NIC: %s", netCfg.PhysicalNIC)
	}

	// 2. Initialize SQLite Store
	store, err := db.NewStore(*dbPath)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer store.Close()

	// 3. Initialize Network Manager
	netMgr, err := network.NewManager(netCfg, nil)
	if err != nil {
		log.Fatalf("Failed to initialize network manager: %v", err)
	}

	// Ensure network topology is ready on startup
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := netMgr.EnsureNetwork(ctx); err != nil {
		log.Printf("[microforged] Warning: could not initialize network (%v). Ensure running with appropriate permissions or sudo.", err)
	}
	cancel()

	// 4. Initialize Storage Manager
	storMgr, err := storage.NewManager(storage.Config{
		BaseImagesDir: *imagesDir,
		VMsDir:        *vmsDir,
	})
	if err != nil {
		log.Fatalf("Failed to initialize storage manager: %v", err)
	}

	// 5. Initialize VM Manager
	vmMgr := vm.NewManager(vm.Config{
		FirecrackerBinary: *firecrackerBin,
	}, store, netMgr, storMgr)

	// Reconcile and prune any leftover orphan TAPs from previous runs
	reconcileCtx, reconcileCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if pruned, err := vmMgr.ReconcileAndPrune(reconcileCtx); err != nil {
		log.Printf("[microforged] Warning: reconciliation failed: %v", err)
	} else if len(pruned) > 0 {
		log.Printf("[microforged] Pruned %d orphaned TAP interface(s): %v", len(pruned), pruned)
	}
	reconcileCancel()

	// 6. Setup Server
	server := NewServer(vmMgr)

	var listener net.Listener
	if strings.HasPrefix(*listenAddr, "unix://") {
		sockPath := strings.TrimPrefix(*listenAddr, "unix://")
		_ = os.Remove(sockPath)
		listener, err = net.Listen("unix", sockPath)
		if err != nil {
			log.Fatalf("Failed to listen on unix socket %s: %v", sockPath, err)
		}
		defer os.Remove(sockPath)
		_ = os.Chmod(sockPath, 0666)
	} else {
		listener, err = net.Listen("tcp", *listenAddr)
		if err != nil {
			log.Fatalf("Failed to listen on TCP %s: %v", *listenAddr, err)
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

	fmt.Printf("[microforged] Daemon is ready and serving requests on %s\n", *listenAddr)

	// Graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Printf("[microforged] Shutting down...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = httpServer.Shutdown(shutdownCtx)
	log.Printf("[microforged] Stopped.")
}
