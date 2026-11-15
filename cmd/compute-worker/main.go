package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Rashid2G/compute/internal/config"
	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/network"
	"github.com/Rashid2G/compute/internal/storage"
	"github.com/Rashid2G/compute/internal/vm"
	"github.com/Rashid2G/compute/internal/worker"
	"github.com/Rashid2G/compute/pkg/version"
)

func main() {
	if len(os.Args) >= 2 && (os.Args[1] == "version" || os.Args[1] == "-v" || os.Args[1] == "--version") {
		info := version.Get("compute-worker", "Compute Worker")
		fmt.Printf("%s\n\n", info.String())
		fmt.Print(info.Detailed(""))
		fmt.Println()
		return
	}
	if len(os.Args) < 2 || os.Args[1] != "run" {
		log.Fatal("usage: compute-worker run --control-plane URL --token TOKEN --name NAME --pool POOL")
	}
	f := flag.NewFlagSet("run", flag.ExitOnError)
	configPath := f.String("config", "", "config file")
	cp := f.String("control-plane", "", "control plane URL")
	token := f.String("token", "", "worker token")
	name := f.String("name", "", "worker name")
	pool := f.String("pool", "", "pool ID")
	_ = f.Parse(os.Args[2:])
	c, e := worker.LoadConfig(*configPath)
	if e != nil && *cp == "" {
		log.Fatal(e)
	}
	if *cp != "" {
		c.ControlPlaneURL = *cp
	}
	if *token != "" {
		c.Token = *token
	}
	if *name != "" {
		c.Name = *name
	}
	if *pool != "" {
		c.PoolID = *pool
	}
	if c.Token == "" && !c.AllowLocalExec {
		log.Fatal("COMPUTE_WORKER_TOKEN is required to enroll a worker; create one with `compute worker token --name <name>`")
	}

	client := &worker.Client{Config: c}
	if c.AllowLocalExec {
		log.Printf("[worker] WARNING: COMPUTE_WORKER_ALLOW_LOCAL_EXEC=true runs assignments with the host shell. Use only for development; sandbox isolation is disabled.")
		client.Supervisor = worker.NewLocalSandboxSupervisor(c.StateDir)
	} else {
		supervisor, cleanup, err := newMicroVMSupervisor(c)
		if err != nil {
			log.Fatalf("[worker] failed to initialize the Firecracker runtime: %v", err)
		}
		defer cleanup()
		client.Supervisor = supervisor
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if e = client.Run(ctx); e != nil && ctx.Err() == nil {
		log.Fatal(e)
	}
}

// newMicroVMSupervisor builds the worker-local database, network, storage and
// VM managers. A worker never shares the control-plane database: it keeps its
// own SQLite state and re-adopts its running microVMs after a restart through
// the VM manager's reconciliation path.
func newMicroVMSupervisor(c worker.Config) (*worker.MicroVMSupervisor, func(), error) {
	stateDir := c.StateDir
	if stateDir == "" {
		stateDir = "/var/lib/compute-worker"
	}
	if err := os.MkdirAll(stateDir, 0750); err != nil {
		return nil, nil, fmt.Errorf("create state directory %s: %w", stateDir, err)
	}

	store, err := db.NewStore(filepath.Join(stateDir, "worker.db"))
	if err != nil {
		return nil, nil, fmt.Errorf("open worker database: %w", err)
	}
	cleanup := func() { _ = store.Close() }

	netCfg := network.DefaultNATConfig()
	if strings.TrimSpace(c.NetworkMode) != "" {
		netCfg.Mode = network.Mode(strings.ToLower(strings.TrimSpace(c.NetworkMode)))
	}
	if c.BridgeName != "" {
		netCfg.BridgeName = c.BridgeName
	}
	if c.BridgeIP != "" {
		netCfg.BridgeIP = c.BridgeIP
	}
	if c.SubnetCIDR != "" {
		netCfg.SubnetCIDR = c.SubnetCIDR
	}
	if c.GatewayIP != "" {
		netCfg.GatewayIP = c.GatewayIP
	}
	netMgr, err := network.NewManager(netCfg, nil)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("initialize network manager: %w", err)
	}
	netCtx, netCancel := context.WithTimeout(context.Background(), 15*time.Second)
	err = netMgr.EnsureNetwork(netCtx)
	netCancel()
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("initialize sandbox network: %w", err)
	}

	storMgr, err := storage.NewManager(storage.Config{
		BaseImagesDir: c.ImagesDir,
		VMsDir:        c.VMsDir,
		DefaultKernel: c.DefaultKernel,
		DefaultRootfs: c.DefaultRootfs,
		SSHKeyPath:    c.SSHKeyPath,
		AvailableImages: []storage.ImageProfile{
			{Name: "ubuntu-24", Kernel: c.DefaultKernel, Rootfs: c.DefaultRootfs, Description: "Compute worker base image"},
		},
	})
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("initialize storage manager: %w", err)
	}

	vmMgr := vm.NewManager(vm.Config{
		FirecrackerBinary: c.FirecrackerBin,
		CgroupEnforce:     c.CgroupEnforce,
		CgroupDevOverride: !c.CgroupEnforce,
		Defaults: config.DefaultsConfig{
			VCPUs:      1,
			MemoryMB:   2048,
			DiskSizeMB: config.DiskSize50GB,
			Image:      "ubuntu-24",
		},
	}, store, netMgr, storMgr)

	// Re-adopt microVMs that survived a worker restart. ReconcileAndPrune keeps
	// live processes and removes stale records so a restarted worker does not
	// leak disks or TAP devices.
	reconcileCtx, reconcileCancel := context.WithTimeout(context.Background(), 15*time.Second)
	if _, err := vmMgr.ReconcileAndPrune(reconcileCtx); err != nil {
		log.Printf("[worker] VM reconciliation warning: %v", err)
	}
	reconcileCancel()

	bootTimeout := time.Duration(c.BootTimeout) * time.Second
	if bootTimeout <= 0 {
		bootTimeout = 90 * time.Second
	}
	return worker.NewMicroVMSupervisor(vmMgr, bootTimeout), cleanup, nil
}
