package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/gui"
	"github.com/Rashid2G/on-demand-vms/internal/vm"
)

const defaultDaemonAddr = "127.0.0.1:18080"

func main() {
	daemonAddr := os.Getenv("MFORGE_DAEMON")
	if daemonAddr == "" {
		daemonAddr = defaultDaemonAddr
	}

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	client := NewDaemonClient(daemonAddr)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	switch cmd {
	case "gui", "web", "ui":
		guiCmd := flag.NewFlagSet("gui", flag.ExitOnError)
		port := guiCmd.Int("port", 8080, "Port to serve the Web UI on")
		addr := guiCmd.String("addr", "0.0.0.0", "Address to bind the Web UI server to")
		targetDaemon := guiCmd.String("daemon", daemonAddr, "MicroForge daemon URL/address")
		guiCmd.Parse(os.Args[2:])

		listenAddr := fmt.Sprintf("%s:%d", *addr, *port)

		sigCtx, sigCancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer sigCancel()

		if err := gui.StartServer(sigCtx, listenAddr, *targetDaemon); err != nil {
			fmt.Fprintf(os.Stderr, "GUI server error: %v\n", err)
			os.Exit(1)
		}
		return

	case "create":
		createCmd := flag.NewFlagSet("create", flag.ExitOnError)
		name := createCmd.String("name", "", "Human-readable VM name")
		vcpus := createCmd.Int("vcpus", 1, "Number of vCPUs")
		mem := createCmd.Int("memory", 512, "Memory size in MiB")
		ipAddr := createCmd.String("ip", "", "Static IPv4 address in subnet (e.g. 172.16.0.50)")
		macAddr := createCmd.String("mac", "", "Custom MAC address (e.g. 06:00:ac:10:00:32)")
		createCmd.Parse(os.Args[2:])

		v, err := client.CreateVM(ctx, vm.CreateRequest{
			Name:       *name,
			VCPUs:      *vcpus,
			MemoryMB:   *mem,
			IPAddress:  *ipAddr,
			MACAddress: *macAddr,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating VM: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("VM Created: ID=%s Name=%s IP=%s MAC=%s Status=%s\n", v.ID, v.Name, v.IPAddress, v.MACAddress, v.Status)

	case "run", "start":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: mforge %s <vm_id>\n", cmd)
			os.Exit(1)
		}
		vmID := os.Args[2]
		v, err := client.RunVM(ctx, vmID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error starting VM %s: %v\n", vmID, err)
			os.Exit(1)
		}
		fmt.Printf("VM Started: ID=%s Name=%s IP=%s Status=%s PID=%d\n", v.ID, v.Name, v.IPAddress, v.Status, v.PID)

	case "save-state", "snapshot":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: mforge %s <vm_id>\n", cmd)
			os.Exit(1)
		}
		vmID := os.Args[2]
		v, err := client.SaveState(ctx, vmID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating snapshot for VM %s: %v\n", vmID, err)
			os.Exit(1)
		}
		fmt.Printf("VM Snapshotted: ID=%s Status=%s Snapshot=%s\n", v.ID, v.Status, v.SnapshotPath)

	case "resume":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: mforge resume <vm_id>\n")
			os.Exit(1)
		}
		vmID := os.Args[2]
		v, err := client.ResumeVM(ctx, vmID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resuming VM %s: %v\n", vmID, err)
			os.Exit(1)
		}
		fmt.Printf("VM Resumed: ID=%s Name=%s Status=%s PID=%d\n", v.ID, v.Name, v.Status, v.PID)

	case "stop":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: mforge stop <vm_id>\n")
			os.Exit(1)
		}
		vmID := os.Args[2]
		v, err := client.StopVM(ctx, vmID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error stopping VM %s: %v\n", vmID, err)
			os.Exit(1)
		}
		fmt.Printf("VM Stopped: ID=%s Status=%s\n", v.ID, v.Status)

	case "list", "ps":
		vms, err := client.ListVMs(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing VMs: %v\n", err)
			os.Exit(1)
		}
		if len(vms) == 0 {
			fmt.Println("No VMs found.")
			return
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tSTATUS\tIP\tVCPUS\tMEM\tTAP\tPID\tCREATED")
		for _, v := range vms {
			created := v.CreatedAt.Format("2006-01-02 15:04:05")
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%dMB\t%s\t%d\t%s\n",
				v.ID, v.Name, v.Status, v.IPAddress, v.VCPUs, v.MemoryMB, v.TAPDevice, v.PID, created)
		}
		w.Flush()

	case "ssh":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: mforge ssh [user@]<vm_id> [-- <command...>]\n")
			os.Exit(1)
		}
		targetArg := os.Args[2]
		targetUser := "mforge"
		vmID := targetArg
		if strings.Contains(targetArg, "@") {
			parts := strings.SplitN(targetArg, "@", 2)
			targetUser = parts[0]
			vmID = parts[1]
		}

		v, err := client.GetVM(ctx, vmID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting VM details: %v\n", err)
			os.Exit(1)
		}
		if v.Status != "running" {
			fmt.Fprintf(os.Stderr, "VM %s is not running (status: %s). Run 'mforge run %s' first.\n", vmID, v.Status, vmID)
			os.Exit(1)
		}

		// Check for extra commands after "--"
		extraArgs := []string{}
		for i, arg := range os.Args[3:] {
			if arg == "--" {
				extraArgs = os.Args[3+i+1:]
				break
			}
			extraArgs = append(extraArgs, arg)
		}

		sshArgs := []string{
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "LogLevel=ERROR",
			"-o", "ConnectTimeout=2",
		}
		if v.SSHKeyPath != "" {
			sshArgs = append(sshArgs, "-i", v.SSHKeyPath)
		}

		isInteractive := len(extraArgs) == 0
		if isInteractive {
			// Allocate pseudo-terminal for interactive session
			sshArgs = append(sshArgs, "-t")
			fmt.Fprintf(os.Stderr, "Connecting to %s@%s (%s)...\n", targetUser, v.IPAddress, vmID)
		}

		target := fmt.Sprintf("%s@%s", targetUser, v.IPAddress)
		sshArgs = append(sshArgs, target)
		sshArgs = append(sshArgs, extraArgs...)

		// Wait for guest SSH port 22 to accept connections before launching SSH
		deadline := time.Now().Add(15 * time.Second)
		var portOpen bool
		for time.Now().Before(deadline) {
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:22", v.IPAddress), 1*time.Second)
			if err == nil {
				conn.Close()
				portOpen = true
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		if !portOpen {
			fmt.Fprintf(os.Stderr, "SSH connection error: guest SSH service on %s:22 did not become reachable within 15s\n", v.IPAddress)
			os.Exit(1)
		}

		// Execute SSH command directly
		sshCmd := exec.Command("ssh", sshArgs...)
		sshCmd.Stdin = os.Stdin
		sshCmd.Stdout = os.Stdout
		sshCmd.Stderr = os.Stderr

		if err := sshCmd.Run(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				os.Exit(exitErr.ExitCode())
			}
			fmt.Fprintf(os.Stderr, "SSH error: %v\n", err)
			os.Exit(1)
		}
		return

	case "network", "net":
		handleNetworkCommand(ctx, client, os.Args[2:])

	case "delete", "rm":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: mforge %s <vm_id>\n", cmd)
			os.Exit(1)
		}
		vmID := os.Args[2]
		if err := client.DeleteVM(ctx, vmID); err != nil {
			fmt.Fprintf(os.Stderr, "Error deleting VM %s: %v\n", vmID, err)
			os.Exit(1)
		}
		fmt.Printf("VM Deleted: ID=%s\n", vmID)

	case "help", "-h", "--help":
		printUsage()

	default:
		fmt.Fprintf(os.Stderr, "Unknown command '%s'\n\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func handleNetworkCommand(ctx context.Context, client *DaemonClient, args []string) {
	subcmd := "status"
	if len(args) > 0 {
		subcmd = args[0]
	}

	switch subcmd {
	case "status", "info":
		info, err := client.GetNetworkInfo(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting network info: %v\n", err)
			os.Exit(1)
		}

		bridgeState := "DOWN"
		if info.BridgeActive {
			bridgeState = "UP"
		}
		natState := "Disabled"
		if info.NATEnabled {
			natState = "Active (MASQUERADE)"
		}
		nicInfo := "<none>"
		if info.PhysicalNIC != "" {
			nicInfo = info.PhysicalNIC
		}

		fmt.Println("==================================================")
		fmt.Println("          MICROFORGE NETWORK STATUS               ")
		fmt.Println("==================================================")
		fmt.Printf("Mode:              %s\n", strings.ToUpper(string(info.Mode)))
		fmt.Printf("Bridge Interface:  %s (%s)\n", info.BridgeName, bridgeState)
		fmt.Printf("Bridge IPv4/CIDR:  %s\n", info.BridgeIP)
		fmt.Printf("Internal Subnet:   %s\n", info.SubnetCIDR)
		fmt.Printf("Default Gateway:   %s (Netmask: %s)\n", info.GatewayIP, info.Netmask)
		fmt.Printf("Physical NIC:      %s\n", nicInfo)
		fmt.Printf("NAT Masquerading:  %s\n", natState)
		fmt.Printf("Allocated IPs:     %d\n", info.AllocatedIPs)
		fmt.Printf("Active Host TAPs:  %d (%s)\n", len(info.ActiveTAPs), strings.Join(info.ActiveTAPs, ", "))
		fmt.Println("==================================================")

	case "list", "allocations", "leases":
		allocs, err := client.GetNetworkAllocations(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing network allocations: %v\n", err)
			os.Exit(1)
		}
		if len(allocs) == 0 {
			fmt.Println("No active network allocations found.")
			return
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "IP ADDRESS\tMAC ADDRESS\tTAP DEVICE\tSTATUS\tVM ID\tVM NAME")
		for _, a := range allocs {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				a.IPAddress, a.MACAddress, a.TAPDevice, a.Status, a.VMID, a.VMName)
		}
		w.Flush()

	case "prune", "clean":
		pruned, err := client.PruneNetwork(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error pruning network: %v\n", err)
			os.Exit(1)
		}
		if len(pruned) == 0 {
			fmt.Println("Network is clean. No orphaned TAP interfaces or dead leases found.")
		} else {
			fmt.Printf("Pruned %d orphaned TAP interface(s):\n", len(pruned))
			for _, tap := range pruned {
				fmt.Printf("  - %s (deleted)\n", tap)
			}
		}

	case "inspect":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "Usage: mforge network inspect <vm_id>\n")
			os.Exit(1)
		}
		vmID := args[1]
		v, err := client.GetVM(ctx, vmID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting VM details: %v\n", err)
			os.Exit(1)
		}

		info, _ := client.GetNetworkInfo(ctx)
		bridgeName := "mft3br0"
		gwIP := "172.16.0.1"
		if info != nil {
			bridgeName = info.BridgeName
			gwIP = info.GatewayIP
		}

		// Test reachability if running
		pingStatus := "Not reachable (VM is not running)"
		if v.Status == "running" {
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:22", v.IPAddress), 1*time.Second)
			if err == nil {
				conn.Close()
				pingStatus = "Reachable (SSH port 22 OPEN)"
			} else {
				pingStatus = fmt.Sprintf("Unreachable (SSH port 22 closed or unresponsive: %v)", err)
			}
		}

		fmt.Printf("VM Network Inspection: %s (%s)\n", v.ID, v.Name)
		fmt.Printf("  Status:        %s\n", v.Status)
		fmt.Printf("  IPv4 Address:  %s\n", v.IPAddress)
		fmt.Printf("  MAC Address:   %s\n", v.MACAddress)
		fmt.Printf("  TAP Interface: %s (master: %s)\n", v.TAPDevice, bridgeName)
		fmt.Printf("  Gateway:       %s\n", gwIP)
		fmt.Printf("  Connectivity:  %s\n", pingStatus)

	default:
		fmt.Fprintf(os.Stderr, "Unknown network subcommand '%s'. Usage: mforge network [status|list|prune|inspect <vm_id>]\n", subcmd)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Usage: mforge <command> [options]")
	fmt.Println()
	fmt.Println("Web GUI Command:")
	fmt.Println("  gui                Launch the Web UI server (--port, --addr, --daemon)")
	fmt.Println()
	fmt.Println("VM Lifecycle Commands:")
	fmt.Println("  create             Provision a new VM (--name, --vcpus, --memory, --ip, --mac)")
	fmt.Println("  run, start         Start an existing VM (<vm_id>)")
	fmt.Println("  stop               Gracefully shut down a VM (<vm_id>)")
	fmt.Println("  save-state         Create a memory + CPU snapshot (<vm_id>)")
	fmt.Println("  resume             Restore a paused/snapshotted VM (<vm_id>)")
	fmt.Println("  delete, rm         Delete VM, storage, and clean up network resources (<vm_id>)")
	fmt.Println("  list, ps           List all VMs")
	fmt.Println("  ssh                Connect to VM via SSH or run remote commands ([user@]<vm_id> [-- <cmd...>])")
	fmt.Println()
	fmt.Println("Network Management Commands:")
	fmt.Println("  network status     Display network topology, mode, bridge status, and NAT info")
	fmt.Println("  network list       List all allocated IPs, MACs, and TAP devices")
	fmt.Println("  network inspect    Inspect VM network configuration and reachability (<vm_id>)")
	fmt.Println("  network prune      Clean up orphaned TAP devices and inactive state")
	fmt.Println()
	fmt.Println("Global Options:")
	fmt.Println("  MFORGE_DAEMON      Daemon address (default: 127.0.0.1:18080)")
}
