package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"text/tabwriter"
	"time"

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
	case "create":
		createCmd := flag.NewFlagSet("create", flag.ExitOnError)
		name := createCmd.String("name", "", "Human-readable VM name")
		vcpus := createCmd.Int("vcpus", 1, "Number of vCPUs")
		mem := createCmd.Int("memory", 512, "Memory size in MiB")
		createCmd.Parse(os.Args[2:])

		v, err := client.CreateVM(ctx, vm.CreateRequest{
			Name:     *name,
			VCPUs:    *vcpus,
			MemoryMB: *mem,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating VM: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("VM Created: ID=%s Name=%s IP=%s Status=%s\n", v.ID, v.Name, v.IPAddress, v.Status)

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
		fmt.Printf("VM Started: ID=%s IP=%s PID=%d Status=%s\n", v.ID, v.IPAddress, v.PID, v.Status)

	case "save-state", "snapshot":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: mforge %s <vm_id>\n", cmd)
			os.Exit(1)
		}
		vmID := os.Args[2]
		v, err := client.SaveState(ctx, vmID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error saving VM state %s: %v\n", vmID, err)
			os.Exit(1)
		}
		fmt.Printf("VM State Saved: ID=%s Status=%s Snapshot=%s\n", v.ID, v.Status, v.SnapshotPath)

	case "resume":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: mforge resume <vm_id>\n", )
			os.Exit(1)
		}
		vmID := os.Args[2]
		v, err := client.ResumeVM(ctx, vmID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resuming VM %s: %v\n", vmID, err)
			os.Exit(1)
		}
		fmt.Printf("VM Resumed: ID=%s Status=%s PID=%d\n", v.ID, v.Status, v.PID)

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
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tSTATUS\tIP\tVCPUS\tMEM\tTAP\tPID\tCREATED")
		for _, v := range vms {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%dMB\t%s\t%d\t%s\n",
				v.ID, v.Name, v.Status, v.IPAddress, v.VCPUs, v.MemoryMB, v.TAPDevice, v.PID,
				v.CreatedAt.Format("2006-01-02 15:04:05"))
		}
		w.Flush()

	case "ssh":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: mforge ssh <vm_id> [-- <command...>]\n")
			os.Exit(1)
		}
		vmID := os.Args[2]
		v, err := client.GetVM(ctx, vmID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting VM details: %v\n", err)
			os.Exit(1)
		}
		if v.Status != "running" {
			fmt.Fprintf(os.Stderr, "VM %s is not running (status: %s). Run 'mforge run %s' first.\n", vmID, v.Status, vmID)
			os.Exit(1)
		}

		sshArgs := []string{
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "LogLevel=ERROR",
		}
		if v.SSHKeyPath != "" {
			sshArgs = append(sshArgs, "-i", v.SSHKeyPath)
		}

		target := fmt.Sprintf("root@%s", v.IPAddress)
		sshArgs = append(sshArgs, target)

		// Check for extra commands after "--"
		extraArgs := []string{}
		for i, arg := range os.Args[3:] {
			if arg == "--" {
				extraArgs = os.Args[3+i+1:]
				break
			}
			extraArgs = append(extraArgs, arg)
		}
		sshArgs = append(sshArgs, extraArgs...)

		sshCmd := exec.Command("ssh", sshArgs...)
		sshCmd.Stdin = os.Stdin
		sshCmd.Stdout = os.Stdout
		sshCmd.Stderr = os.Stderr

		if err := sshCmd.Run(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				os.Exit(exitErr.ExitCode())
			}
			fmt.Fprintf(os.Stderr, "SSH connection error: %v\n", err)
			os.Exit(1)
		}

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

func printUsage() {
	fmt.Println("Usage: mforge <command> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  create       Provision a new VM (--name, --vcpus, --memory)")
	fmt.Println("  run          Start a microVM (mforge run <vm_id>)")
	fmt.Println("  save-state   Save running VM state & memory snapshot (mforge save-state <vm_id>)")
	fmt.Println("  resume       Restore VM from saved state (mforge resume <vm_id>)")
	fmt.Println("  stop         Stop a running VM (mforge stop <vm_id>)")
	fmt.Println("  list, ps     List all managed VMs")
	fmt.Println("  ssh          SSH into running VM (mforge ssh <vm_id> [-- <cmd>])")
	fmt.Println("  delete, rm   Delete VM and cleanup its resources (mforge delete <vm_id>)")
	fmt.Println()
	fmt.Println("Environment Variables:")
	fmt.Println("  MFORGE_DAEMON   Daemon address (default: 127.0.0.1:18080 or unix:///path.sock)")
}
