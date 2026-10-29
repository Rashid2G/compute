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
	bridgeName := flag.String("bridge", "mft3br0", "Bridge interface name")
	bridgeIP := flag.String("bridge-ip", "172.16.0.1/16", "Bridge IPv4 CIDR")
	subnet := flag.String("subnet", "172.16.0.0/16", "Internal VM subnet CIDR")
	firecrackerBin := flag.String("firecracker-bin", "/usr/local/bin/firecracker", "Path to firecracker executable")
	flag.Parse()

	log.Printf("[microforged] Starting daemon...")
	log.Printf("[microforged] Listening on: %s", *listenAddr)
	log.Printf("[microforged] Database: %s", *dbPath)
	log.Printf("[microforged] Images: %s", *imagesDir)
	log.Printf("[microforged] VMs directory: %s", *vmsDir)
	log.Printf("[microforged] Bridge: %s (%s, subnet: %s)", *bridgeName, *bridgeIP, *subnet)

	// 1. Initialize SQLite Store
	store, err := db.NewStore(*dbPath)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer store.Close()

	// 2. Initialize Network Manager
	netMgr, err := network.NewManager(network.Config{
		BridgeName: *bridgeName,
		BridgeIP:   *bridgeIP,
		SubnetCIDR: *subnet,
	}, nil)
	if err != nil {
		log.Fatalf("Failed to initialize network manager: %v", err)
	}

	// Ensure bridge and NAT are ready on startup
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := netMgr.EnsureBridge(ctx); err != nil {
		log.Printf("[microforged] Warning: could not initialize bridge (%v). Ensure running with appropriate permissions or sudo.", err)
	}
	cancel()

	// 3. Initialize Storage Manager
	storMgr, err := storage.NewManager(storage.Config{
		BaseImagesDir: *imagesDir,
		VMsDir:        *vmsDir,
	})
	if err != nil {
		log.Fatalf("Failed to initialize storage manager: %v", err)
	}

	// 4. Initialize VM Manager
	vmMgr := vm.NewManager(vm.Config{
		FirecrackerBinary: *firecrackerBin,
	}, store, netMgr, storMgr)

	// 5. Setup Server
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
