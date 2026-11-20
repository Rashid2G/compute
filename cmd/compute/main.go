package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/term"

	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/gui"
	"github.com/Rashid2G/compute/internal/vm"
	"github.com/Rashid2G/compute/pkg/api"
	"github.com/Rashid2G/compute/pkg/version"
)

const defaultDaemonAddr = "127.0.0.1:18080"

func main() {
	daemonAddr := os.Getenv("COMPUTE_DAEMON")
	if daemonAddr == "" {
		daemonAddr = os.Getenv("COMPUTE_HOST")
		if daemonAddr != "" && !strings.Contains(daemonAddr, ":") {
			daemonAddr = daemonAddr + ":18080"
		}
	}
	if daemonAddr == "" {
		daemonAddr = defaultDaemonAddr
	}

	if len(os.Args) < 2 {
		printCommandError("no command specified.", "", "")
	}

	cmd := os.Args[1]
	if cmd == "-v" || cmd == "--version" || cmd == "-version" {
		cmd = "version"
	}
	client := NewDaemonClient(daemonAddr)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	switch cmd {
	case "version", "-v", "--version", "-version":
		handleVersionCommand(ctx, client, os.Args[2:])
		return

	case "ssh":
		handleSandboxSSH(context.Background(), client, os.Args[2:])
		return

	case "exec":
		handleExecCommand(ctx, client, os.Args[2:])
		return

	case "url":
		handleSandboxURL(ctx, client, os.Args[2:])
		return

	case "expose":
		handleExposeCommand(ctx, client, os.Args[2:])
		return

	case "ports":
		handlePortsCommand(ctx, client, os.Args[2:])
		return

	case "unexpose":
		handleUnexposeCommand(ctx, client, os.Args[2:])
		return

	case "auth":
		handleAuthCommand(ctx, client, os.Args[2:])
		return

	case "keys", "key", "ssh-keys":
		handleKeysCommand(ctx, client, os.Args[2:])
		return

	case "sandbox", "vm":
		handleSandboxCommand(ctx, client, os.Args[2:])
		return

	case "file", "files":
		handleSandboxFileCommand(ctx, client, os.Args[2:])
		return

	case "cp":
		handleSandboxCpCommand(ctx, client, os.Args[2:])
		return

	case "scp":
		handleSandboxScpCommand(ctx, client, os.Args[2:])
		return

	case "process", "processes", "ps":
		handleSandboxProcessCommand(ctx, client, os.Args[2:])
		return

	case "job", "jobs":
		handleJobCommand(ctx, client, os.Args[2:])
		return

	case "network", "net":
		handleNetworkCommand(ctx, client, os.Args[2:])
		return

	case "worker", "workers":
		handleWorkerCommand(ctx, client, os.Args[2:])
		return

	case "pool", "pools":
		handlePoolCommand(ctx, client, os.Args[2:])
		return

	case "image", "images":
		handleImageCommand(ctx, client, os.Args[2:])
		return

	case "doctor", "check":
		handleDoctorCommand(ctx, client, os.Args[2:])
		return

	case "template", "templates":
		handleTemplateCommand(ctx, client, os.Args[2:])
		return

	case "gui", "web", "ui":
		guiCmd := flag.NewFlagSet("gui", flag.ExitOnError)
		port := guiCmd.Int("port", 8080, "Port to serve the Web UI on")
		addr := guiCmd.String("addr", "0.0.0.0", "Address to bind the Web UI server to")
		targetDaemon := guiCmd.String("daemon", daemonAddr, "Compute daemon URL/address")
		guiCmd.Parse(os.Args[2:])

		listenAddr := fmt.Sprintf("%s:%d", *addr, *port)

		sigCtx, sigCancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer sigCancel()

		if err := gui.StartServer(sigCtx, listenAddr, *targetDaemon); err != nil {
			fmt.Fprintf(os.Stderr, "GUI server error: %v\n", err)
			os.Exit(1)
		}
		return

	case "completion", "autocomplete":
		handleCompletionCommand(os.Args[2:])
		return

	case "__complete":
		if len(os.Args) >= 3 {
			quickCtx, quickCancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
			defer quickCancel()
			switch os.Args[2] {
			case "vms", "sandboxes":
				vms, err := client.ListVMs(quickCtx)
				if err == nil {
					seen := make(map[string]bool)
					for _, v := range vms {
						if v.Name != "" && !seen[v.Name] {
							fmt.Println(v.Name)
							seen[v.Name] = true
						}
						if v.ID != "" && !seen[v.ID] {
							fmt.Println(v.ID)
							seen[v.ID] = true
						}
						if v.InternalID != "" && !seen[v.InternalID] {
							fmt.Println(v.InternalID)
							seen[v.InternalID] = true
						}
					}
				}
			case "images":
				images, err := client.ListImages(quickCtx)
				if err == nil {
					seen := make(map[string]bool)
					for _, img := range images {
						if img.Name != "" && !seen[img.Name] {
							fmt.Println(img.Name)
							seen[img.Name] = true
						}
						if img.ID != "" && !seen[img.ID] {
							fmt.Println(img.ID)
							seen[img.ID] = true
						}
					}
				}
			case "templates":
				tmpls, err := client.ListTemplates(quickCtx)
				if err == nil {
					seen := make(map[string]bool)
					for _, t := range tmpls {
						if t.Name != "" && !seen[t.Name] {
							fmt.Println(t.Name)
							seen[t.Name] = true
						}
						if t.ID != "" && !seen[t.ID] {
							fmt.Println(t.ID)
							seen[t.ID] = true
						}
					}
				}
			case "pools":
				pools, err := client.ListPools(quickCtx)
				if err == nil {
					seen := make(map[string]bool)
					for _, p := range pools {
						if p.Name != "" && !seen[p.Name] {
							fmt.Println(p.Name)
							seen[p.Name] = true
						}
						if p.ID != "" && !seen[p.ID] {
							fmt.Println(p.ID)
							seen[p.ID] = true
						}
					}
				}
			case "workers":
				workers, err := client.ListWorkers(quickCtx)
				if err == nil {
					seen := make(map[string]bool)
					for _, w := range workers {
						if w.Name != "" && !seen[w.Name] {
							fmt.Println(w.Name)
							seen[w.Name] = true
						}
						if w.ID != "" && !seen[w.ID] {
							fmt.Println(w.ID)
							seen[w.ID] = true
						}
					}
				}
			case "jobs":
				jobs, err := client.ListJobs(quickCtx)
				if err == nil {
					seen := make(map[string]bool)
					for _, j := range jobs {
						if j.ID != "" && !seen[j.ID] {
							fmt.Println(j.ID)
							seen[j.ID] = true
						}
					}
				}
			case "keys":
				keys, err := client.ListKeys(quickCtx)
				if err == nil {
					seen := make(map[string]bool)
					for _, k := range keys {
						if k.Name != "" && !seen[k.Name] {
							fmt.Println(k.Name)
							seen[k.Name] = true
						}
						if k.ID != "" && !seen[k.ID] {
							fmt.Println(k.ID)
							seen[k.ID] = true
						}
					}
				}
			}
			return
		}

	case "help", "-h", "--help":
		if len(os.Args) > 2 {
			if help, ok := commandHelp[os.Args[2]]; ok {
				fmt.Println(help)
				return
			}
		}
		printUsage()
		return

	default:
		printCommandError(fmt.Sprintf("Unknown command '%s'.", cmd), "", "")
	}
}

func printUsage() {
	printUsageTo(os.Stdout)
}

func printUsageTo(w io.Writer) {
	fmt.Fprintln(w, "Usage: compute <command> [subcommand] [options]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Core Commands:")
	fmt.Fprintln(w, "  auth               User accounts, authentication and API keys (login, signup, whoami, keys, logout)")
	fmt.Fprintln(w, "  sandbox, vm        Manage isolated compute sandboxes (list, create, start, stop, exec, ssh, delete, info)")
	fmt.Fprintln(w, "  job, jobs          Run and monitor distributed compute jobs (run, list, info, logs, cancel)")
	fmt.Fprintln(w, "  worker, workers    Manage worker nodes and enrollments (token create, list, info, cordon, drain, delete)")
	fmt.Fprintln(w, "  pool, pools        Manage worker pools (list, create, delete)")
	fmt.Fprintln(w, "  image, images      Manage base and custom rootfs images (list, build, inspect, delete)")
	fmt.Fprintln(w, "  template, templates Manage VM disk templates (list, create, launch, inspect, delete)")
	fmt.Fprintln(w, "  keys               Manage saved SSH public keys (list, add, update, apply, delete)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Tools & Utilities:")
	fmt.Fprintln(w, "  file               Read, write, list, mkdir, and remove files in sandboxes")
	fmt.Fprintln(w, "  cp                 Copy files between local host and sandboxes")
	fmt.Fprintln(w, "  scp                Copy files/directories to and from sandboxes (scp-style, -r for directories)")
	fmt.Fprintln(w, "  process, ps        List, start, and kill processes inside sandboxes")
	fmt.Fprintln(w, "  exec               Execute commands on a sandbox with live streaming feedback (--user, --timeout, --no-stream, --json)")
	fmt.Fprintln(w, "  ssh                Connect to sandbox via SSH over WebSocket")
	fmt.Fprintln(w, "  url                Print the best sandbox URL or SSH target (--json, --port)")
	fmt.Fprintln(w, "  expose             Publish a sandbox port on the daemon ingress URL (--private, --subdomain)")
	fmt.Fprintln(w, "  ports              List guest ports exposed on a sandbox")
	fmt.Fprintln(w, "  unexpose           Remove a sandbox port exposure")
	fmt.Fprintln(w, "  doctor, check      Print daemon readiness checks with remediation for failures")
	fmt.Fprintln(w, "  version, -v        Display version, build, and release information (--client, --short, --json)")
	fmt.Fprintln(w, "  gui, web, ui       Launch the Web UI dashboard (--port, --addr, --daemon)")
	fmt.Fprintln(w, "  completion         Generate shell autocompletion script (bash, zsh, fish)")
	fmt.Fprintln(w, "  help               Show help documentation")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Global Options:")
	fmt.Fprintln(w, "  COMPUTE_DAEMON      Daemon address (default: 127.0.0.1:18080)")
}

// queuedVMReason returns the admission-queue reason for a VM, or "" when the
// VM is not queued (or the queue cannot be read).
func queuedVMReason(ctx context.Context, client *DaemonClient, vmID string) string {
	queue, err := client.GetQueue(ctx)
	if err != nil {
		return ""
	}
	for _, item := range queue.QueuedVMs {
		if item != nil && item.VMID == vmID {
			return item.Reason
		}
	}
	return ""
}

// printQueuedHint explains why a sandbox is waiting instead of booting.
func printQueuedHint(ctx context.Context, client *DaemonClient, v *db.VM) {
	reason := queuedVMReason(ctx, client, v.ID)
	if reason == "" {
		reason = "host capacity is insufficient for the requested resources"
	}
	fmt.Printf("  Queue reason:  %s\n", reason)
	fmt.Printf("  Hint:          stop a running sandbox or free host resources; for a smaller\n")
	fmt.Printf("                 request use --memory/--vcpus, then run 'compute sandbox start %s'\n", v.Name)
}

// Sandbox command implementation
func handleSandboxCommand(ctx context.Context, client *DaemonClient, args []string) {
	if printHelpIfRequested("sandbox", args) {
		return
	}
	if len(args) == 0 {
		fmt.Printf("%s\n\n", commandHelp["sandbox"])
		return
	}
	if args[0] == "list" || args[0] == "ls" {
		vms, err := client.ListVMs(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing sandboxes: %v\n", err)
			os.Exit(1)
		}
		if len(vms) == 0 {
			fmt.Println("No sandboxes found. Create one with: compute sandbox create --name <name>")
			return
		}
		writeSandboxList(os.Stdout, vms)
		return
	}

	sub := args[0]
	subArgs := args[1:]
	switch sub {
	case "create":
		createCmd := flag.NewFlagSet("sandbox create", flag.ExitOnError)
		name := createCmd.String("name", "", "Human-readable sandbox name")
		tier := createCmd.String("tier", "", "VM tier ('1vcpu', '2vcpu', '4vcpu')")
		vcpus := createCmd.Int("vcpus", 0, "Number of vCPUs: 1, 2, or 4")
		mem := createCmd.Int("memory", 0, "Memory size in MiB")
		disk := createCmd.Int("disk", 0, "Disk size in GiB or MiB")
		image := createCmd.String("image", "", "Base image name")
		template := createCmd.String("template", "", "Template name or ID")
		sshKey := createCmd.String("ssh-key", "", "SSH public key name or path")
		noSSHKey := createCmd.Bool("no-ssh-key", false, "Exclude the account's default SSH key from this sandbox")
		sshPassword := createCmd.String("ssh-password", "", "Optional SSH password for compute/root (min 8 chars; use '-' to read from stdin)")
		networkIsolation := createCmd.String("network-isolation", "", "Sandbox network isolation mode: 'account' (communicate with account sandboxes) or 'isolated' (own sandbox only)")
		isolated := createCmd.Bool("isolated", false, "Isolate this sandbox on its own network (shorthand for --network-isolation isolated)")
		protected := createCmd.Bool("protected", false, "Enable deletion protection")
		startupScript := createCmd.String("startup-script", "", "Bash startup script string, '-' to read from stdin, or path to script file")
		startupScriptFile := createCmd.String("startup-script-file", "", "Path to bash startup script file")
		buildCommand := createCmd.String("command", "", "Command to execute inside the sandbox on build/start")
		createCmd.StringVar(buildCommand, "build-command", "", "Alias for --command")
		execUser := createCmd.String("user", "compute", "User to run the build command as (default: compute)")
		startNow := createCmd.Bool("start", false, "Immediately boot/start the sandbox after creation")
		bootNow := createCmd.Bool("boot", false, "Alias for --start")
		createCmd.Parse(subArgs)

		isolatedSet := false
		createCmd.Visit(func(f *flag.Flag) {
			if f.Name == "isolated" {
				isolatedSet = true
			}
		})
		var isolatedPtr *bool
		if isolatedSet {
			val := *isolated
			isolatedPtr = &val
		}

		scriptContent := ""
		if strings.TrimSpace(*startupScriptFile) != "" {
			filePath := expandHome(strings.TrimSpace(*startupScriptFile))
			data, err := os.ReadFile(filePath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error reading startup script file %s: %v\n", *startupScriptFile, err)
				os.Exit(1)
			}
			scriptContent = string(data)
		} else if strings.TrimSpace(*startupScript) != "" {
			candidate := strings.TrimSpace(*startupScript)
			if candidate == "-" {
				data, err := io.ReadAll(os.Stdin)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error reading startup script from stdin: %v\n", err)
					os.Exit(1)
				}
				scriptContent = string(data)
			} else if info, err := os.Stat(expandHome(candidate)); err == nil && !info.IsDir() {
				data, err := os.ReadFile(expandHome(candidate))
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error reading startup script file %s: %v\n", candidate, err)
					os.Exit(1)
				}
				scriptContent = string(data)
			} else {
				scriptContent = *startupScript
			}
		}

		password := *sshPassword
		if password == "-" {
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error reading SSH password from stdin: %v\n", err)
				os.Exit(1)
			}
			password = strings.TrimRight(string(data), "\r\n")
		}
		// --ssh-key accepts a saved key name/ID, a literal public key, or a path
		// to a public key file. Files and literals are sent inline; names are
		// resolved by the daemon against the caller's account keys.
		sshKeyID := ""
		sshPublicKey := ""
		if strings.TrimSpace(*sshKey) != "" {
			candidate := strings.TrimSpace(*sshKey)
			if looksLikePublicKey(candidate) {
				sshPublicKey = candidate
			} else if data, err := os.ReadFile(expandHome(candidate)); err == nil && len(data) > 0 {
				sshPublicKey = strings.TrimSpace(string(data))
			} else {
				sshKeyID = candidate
			}
			if sshPublicKey != "" {
				if err := validatePublicKey(sshPublicKey); err != nil {
					fmt.Fprintf(os.Stderr, "Error reading SSH key %s: %v\n", candidate, err)
					os.Exit(1)
				}
			}
		}
		v, err := client.CreateVM(ctx, vm.CreateRequest{
			Name:             *name,
			Tier:             *tier,
			VCPUs:            *vcpus,
			MemoryMB:         *mem,
			DiskMB:           *disk,
			Image:            *image,
			Template:         *template,
			SSHKeyID:         sshKeyID,
			SSHPublicKey:     sshPublicKey,
			SSHNoDefaultKey:  *noSSHKey,
			SSHPassword:      password,
			Protected:        protected,
			StartupScript:    scriptContent,
			Command:          *buildCommand,
			ExecUser:         *execUser,
			NetworkIsolation: *networkIsolation,
			Isolated:         isolatedPtr,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating sandbox: %v\n", err)
			os.Exit(1)
		}
		protMsg := ""
		if v.Protected {
			protMsg = " [protected]"
		}
		netMode := v.NetworkIsolation
		if netMode == "" {
			netMode = db.NetworkIsolationAccount
		}
		fmt.Printf("Sandbox Created: ID=%s Name=%s IP=%s Status=%s Network=%s%s (vCPUs=%d, RAM=%dMB)\n",
			v.ID, v.Name, v.IPAddress, v.Status, netMode, protMsg, v.VCPUs, v.MemoryMB)
		if v.SSHPasswordEnabled {
			fmt.Println("SSH auth:        password enabled for compute and root (embedded public keys also accepted)")
		} else {
			fmt.Println("SSH auth:        public key only (no SSH password configured)")
		}
		if v.StartupScript != "" {
			fmt.Printf("Startup script:  configured (%d bytes bash script)\n", len(v.StartupScript))
		}

		if *startNow || *bootNow {
			fmt.Printf("Booting sandbox %s...\n", v.Name)
			runningVM, err := client.RunVM(ctx, v.ID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error booting sandbox %s: %v\n", v.Name, err)
				os.Exit(1)
			}
			v = runningVM
			fmt.Printf("Sandbox Started: ID=%s Name=%s IP=%s Status=%s\n",
				v.ID, v.Name, v.IPAddress, v.Status)
			if v.Status == db.StatusQueued {
				printQueuedHint(ctx, client, v)
			}
		}

		if *buildCommand != "" {
			if !(*startNow || *bootNow) {
				fmt.Printf("Booting sandbox %s for build command execution...\n", v.Name)
				runningVM, err := client.RunVM(ctx, v.ID)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error booting sandbox %s: %v\n", v.Name, err)
					os.Exit(1)
				}
				v = runningVM
				fmt.Printf("Sandbox Started: ID=%s Name=%s IP=%s Status=%s\n",
					v.ID, v.Name, v.IPAddress, v.Status)
			}
			fmt.Printf("Executing build command on %s (%s):\n", v.Name, *buildCommand)
			exitCode := 0
			streamErr := ""
			err := client.ExecVMStream(ctx, v.ID, vm.ExecRequest{
				Command: *buildCommand,
				User:    *execUser,
			}, func(ev vm.ExecStreamEvent) error {
				if ev.Data != "" {
					if ev.Stream == "stderr" {
						fmt.Fprint(os.Stderr, ev.Data)
					} else {
						fmt.Fprint(os.Stdout, ev.Data)
					}
				}
				if ev.Error != "" {
					streamErr = ev.Error
				}
				if ev.ExitCode != nil {
					exitCode = *ev.ExitCode
				}
				return nil
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Build command error: %v\n", err)
				os.Exit(1)
			}
			if streamErr != "" {
				fmt.Fprintf(os.Stderr, "Build command error: %s\n", streamErr)
			}
			if exitCode != 0 {
				fmt.Fprintf(os.Stderr, "Build command exited with code %d\n", exitCode)
				os.Exit(exitCode)
			}
			fmt.Printf("Build command completed successfully (exit 0).\n")
		}

	case "start", "run":
		if len(subArgs) < 1 {
			printCommandError("missing required argument for 'sandbox start'.", "Usage: compute sandbox start <name|id|pattern>...", "sandbox")
		}
		targets, err := resolveSandboxTargets(ctx, client, subArgs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving sandbox targets: %v\n", err)
			os.Exit(1)
		}
		var wg sync.WaitGroup
		type startResult struct {
			v   *db.VM
			err error
			id  string
		}
		results := make([]startResult, len(targets))
		for i, id := range targets {
			wg.Add(1)
			go func(idx int, targetID string) {
				defer wg.Done()
				v, err := client.RunVM(ctx, targetID)
				results[idx] = startResult{v: v, err: err, id: targetID}
			}(i, id)
		}
		wg.Wait()
		hasErr := false
		for _, res := range results {
			if res.err != nil {
				fmt.Fprintf(os.Stderr, "Error starting sandbox %s: %v\n", res.id, res.err)
				hasErr = true
			} else if res.v != nil {
				fmt.Printf("Sandbox Started: ID=%s Name=%s IP=%s Status=%s", res.v.ID, res.v.Name, res.v.IPAddress, res.v.Status)
				if res.v.PID > 0 {
					fmt.Printf(" PID=%d", res.v.PID)
				}
				fmt.Println()
			}
		}
		if hasErr {
			os.Exit(1)
		}

	case "stop":
		if len(subArgs) < 1 {
			printCommandError("missing required argument for 'sandbox stop'.", "Usage: compute sandbox stop <name|id|pattern>...", "sandbox")
		}
		targets, err := resolveSandboxTargets(ctx, client, subArgs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving sandbox targets: %v\n", err)
			os.Exit(1)
		}
		var wg sync.WaitGroup
		type stopResult struct {
			v   *db.VM
			err error
			id  string
		}
		results := make([]stopResult, len(targets))
		for i, id := range targets {
			wg.Add(1)
			go func(idx int, targetID string) {
				defer wg.Done()
				v, err := client.StopVM(ctx, targetID)
				results[idx] = stopResult{v: v, err: err, id: targetID}
			}(i, id)
		}
		wg.Wait()
		hasErr := false
		for _, res := range results {
			if res.err != nil {
				fmt.Fprintf(os.Stderr, "Error stopping sandbox %s: %v\n", res.id, res.err)
				hasErr = true
			} else if res.v != nil {
				fmt.Printf("Sandbox Stopped: ID=%s Name=%s Status=%s\n", res.v.ID, res.v.Name, res.v.Status)
			}
		}
		if hasErr {
			os.Exit(1)
		}

	case "save-state", "snapshot":
		if len(subArgs) < 1 {
			printCommandError("missing required argument for 'sandbox save-state'.", "Usage: compute sandbox save-state <name|id>", "sandbox")
		}
		targets, err := resolveSandboxTargets(ctx, client, subArgs[:1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving sandbox target: %v\n", err)
			os.Exit(1)
		}
		v, err := client.SaveState(ctx, targets[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error saving sandbox state: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Sandbox Snapshot Saved: ID=%s Name=%s Status=%s Snapshot=%s\n",
			v.ID, v.Name, v.Status, v.SnapshotPath)

	case "resume":
		if len(subArgs) < 1 {
			printCommandError("missing required argument for 'sandbox resume'.", "Usage: compute sandbox resume <name|id>", "sandbox")
		}
		targets, err := resolveSandboxTargets(ctx, client, subArgs[:1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving sandbox target: %v\n", err)
			os.Exit(1)
		}
		v, err := client.ResumeVM(ctx, targets[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resuming sandbox: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Sandbox Resumed: ID=%s Name=%s Status=%s\n", v.ID, v.Name, v.Status)

	case "delete", "rm":
		if len(subArgs) < 1 {
			printCommandError("missing required argument for 'sandbox delete'.", "Usage: compute sandbox delete <name|id|pattern>...", "sandbox")
		}
		targets, err := resolveSandboxTargets(ctx, client, subArgs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving sandbox targets: %v\n", err)
			os.Exit(1)
		}
		var wg sync.WaitGroup
		type deleteResult struct {
			err error
			id  string
		}
		results := make([]deleteResult, len(targets))
		for i, id := range targets {
			wg.Add(1)
			go func(idx int, targetID string) {
				defer wg.Done()
				err := client.DeleteVM(ctx, targetID)
				results[idx] = deleteResult{err: err, id: targetID}
			}(i, id)
		}
		wg.Wait()
		hasErr := false
		for _, res := range results {
			if res.err != nil {
				fmt.Fprintf(os.Stderr, "Error deleting sandbox %s: %v\n", res.id, res.err)
				hasErr = true
			} else {
				fmt.Printf("Sandbox Deleted: ID=%s\n", res.id)
			}
		}
		if hasErr {
			os.Exit(1)
		}

	case "exec":
		handleExecCommand(ctx, client, subArgs)

	case "ssh":
		handleSandboxSSH(ctx, client, subArgs)

	case "info", "inspect", "show":
		if len(subArgs) < 1 {
			printCommandError("missing required sandbox name or ID.", "Usage: compute sandbox info <name|id|hostname>", "sandbox")
		}
		v, err := resolveSandboxReference(ctx, client, subArgs[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error inspecting sandbox: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Sandbox %s:\n", v.Name)
		fmt.Printf("  ID:            %s\n", v.ID)
		fmt.Printf("  Status:        %s\n", v.Status)
		fmt.Printf("  vCPUs:         %d\n", v.VCPUs)
		fmt.Printf("  Memory:        %d MB\n", v.MemoryMB)
		fmt.Printf("  IP Address:    %s\n", v.IPAddress)
		if v.PublicIP != "" {
			fmt.Printf("  Public IP:     %s\n", v.PublicIP)
		}
		if v.TailnetIP != "" {
			fmt.Printf("  Tailnet IP:    %s\n", v.TailnetIP)
		} else if v.TailscaleEnabled {
			fmt.Printf("  Tailnet:       provisioning in progress\n")
		}
		for _, line := range sandboxAccessLines(sandboxAccessForVM(v, "compute", 0)) {
			fmt.Println(line)
		}
		if v.SSHPasswordEnabled {
			fmt.Printf("  SSH auth:      password enabled for compute and root\n")
		} else {
			fmt.Printf("  SSH auth:      public key only\n")
		}
		netMode := v.NetworkIsolation
		if netMode == "" {
			netMode = db.NetworkIsolationAccount
		}
		if netMode == db.NetworkIsolationIsolated {
			fmt.Printf("  Network:       isolated (own sandbox only)\n")
		} else {
			fmt.Printf("  Network:       account (can communicate with all account sandboxes)\n")
		}
		fmt.Printf("  Protected:     %t\n", v.Protected)
		if v.StartupScript != "" {
			fmt.Printf("  Startup Script: configured (%d bytes bash script)\n", len(v.StartupScript))
		}
		fmt.Printf("  Created:       %s\n", v.CreatedAt.Format(time.RFC3339))
		if v.Status == db.StatusQueued {
			printQueuedHint(ctx, client, v)
		}

	case "url":
		handleSandboxURL(ctx, client, subArgs)

	case "expose":
		handleExposeCommand(ctx, client, subArgs)

	case "ports":
		handlePortsCommand(ctx, client, subArgs)

	case "unexpose":
		handleUnexposeCommand(ctx, client, subArgs)

	case "protect":
		if len(subArgs) < 1 {
			printCommandError("missing required sandbox name or ID.", "Usage: compute sandbox protect <name|id>", "sandbox")
		}
		targetID := subArgs[0]
		v, err := client.ProtectVM(ctx, targetID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error enabling protection for sandbox %s: %v\n", targetID, err)
			os.Exit(1)
		}
		fmt.Printf("Sandbox %s: deletion protection enabled\n", v.Name)

	case "unprotect":
		if len(subArgs) < 1 {
			printCommandError("missing required sandbox name or ID.", "Usage: compute sandbox unprotect <name|id>", "sandbox")
		}
		targetID := subArgs[0]
		v, err := client.UnprotectVM(ctx, targetID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error disabling protection for sandbox %s: %v\n", targetID, err)
			os.Exit(1)
		}
		fmt.Printf("Sandbox %s: deletion protection disabled\n", v.Name)

	case "network-isolation":
		if len(subArgs) < 2 {
			printCommandError("missing required sandbox or mode.", "Usage: compute sandbox network-isolation <name|id> <account|isolated>", "sandbox")
		}
		targetRef, err := resolveSandboxReference(ctx, client, subArgs[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving sandbox %s: %v\n", subArgs[0], err)
			os.Exit(1)
		}
		mode, ok := db.NormalizeNetworkIsolation(subArgs[1])
		if !ok {
			printCommandError(fmt.Sprintf("invalid network isolation mode %q (expected 'account' or 'isolated').", subArgs[1]), "Usage: compute sandbox network-isolation <name|id> <account|isolated>", "sandbox")
		}
		updated, err := client.SetVMNetworkIsolation(ctx, targetRef.ID, mode)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error updating network isolation for sandbox %s: %v\n", subArgs[0], err)
			os.Exit(1)
		}
		fmt.Printf("Sandbox %s: network isolation set to %s\n", updated.Name, updated.NetworkIsolation)

	case "images", "image":
		handleImageCommand(ctx, client, append([]string{"list"}, subArgs...))

	case "templates", "template":
		handleTemplateCommand(ctx, client, append([]string{"list"}, subArgs...))

	case "file", "files":
		handleSandboxFileCommand(ctx, client, subArgs)

	case "cp":
		handleSandboxCpCommand(ctx, client, subArgs)

	case "process", "processes", "ps":
		handleSandboxProcessCommand(ctx, client, subArgs)

	default:
		printCommandError(fmt.Sprintf("unknown sandbox command '%s'.", sub), "", "sandbox")
	}
}

func printSSHUsage() {
	fmt.Fprintf(os.Stderr, "Usage: compute [sandbox] ssh [options] [user@]<name|id|hostname> [-- <cmd...> ]\n\n")
	fmt.Fprintf(os.Stderr, "Connect to sandbox SSH via WebSocket connection through the daemon.\n\n")
	fmt.Fprintf(os.Stderr, "Options:\n")
	fmt.Fprintf(os.Stderr, "  -u, --user <username>       SSH user (default: compute)\n")
	fmt.Fprintf(os.Stderr, "      --proxy, --stdio        Run in raw stream mode bridging stdin/stdout (OpenSSH ProxyCommand)\n")
	fmt.Fprintf(os.Stderr, "  -p, --password <password>   SSH password\n")
	fmt.Fprintf(os.Stderr, "  -t                          Force pseudo-terminal allocation\n")
	fmt.Fprintf(os.Stderr, "  -T                          Disable pseudo-terminal allocation\n")
}

func handleSandboxSSH(ctx context.Context, client *DaemonClient, args []string) {
	if len(args) < 1 {
		printCommandError("missing sandbox target for SSH.", "Usage: compute [sandbox] ssh [options] [user@]<name|id|hostname> [-- <cmd...> ]", "ssh")
	}

	var user string
	var target string
	var password string
	var forcePTY bool
	var disablePTY bool
	var proxyMode bool
	var command []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			command = args[i+1:]
			break
		}
		if arg == "-h" || arg == "--help" {
			printSSHUsage()
			return
		}
		if arg == "--proxy" || arg == "--stdio" {
			proxyMode = true
			continue
		}
		if arg == "-u" || arg == "--user" {
			if i+1 < len(args) {
				user = args[i+1]
				i++
				continue
			}
		}
		if strings.HasPrefix(arg, "--user=") {
			user = strings.TrimPrefix(arg, "--user=")
			continue
		}
		if arg == "-p" || arg == "--password" {
			if i+1 < len(args) {
				password = args[i+1]
				i++
				continue
			}
		}
		if strings.HasPrefix(arg, "--password=") {
			password = strings.TrimPrefix(arg, "--password=")
			continue
		}
		if arg == "-t" {
			forcePTY = true
			continue
		}
		if arg == "-T" {
			disablePTY = true
			continue
		}
		if strings.HasPrefix(arg, "-") {
			if (arg == "-i" || arg == "-o") && i+1 < len(args) {
				i++
			}
			continue
		}
		if target == "" {
			target = arg
			continue
		}
		command = args[i:]
		break
	}

	if target == "" {
		printCommandError("missing sandbox target for SSH.", "Usage: compute [sandbox] ssh [options] [user@]<name|id|hostname> [-- <cmd...> ]", "ssh")
	}

	vmID := target
	if strings.Contains(target, "@") {
		parts := strings.SplitN(target, "@", 2)
		if user == "" {
			user = parts[0]
		}
		vmID = parts[1]
	}
	if user == "" {
		user = "compute"
	}

	quickCtx, quickCancel := context.WithTimeout(ctx, 10*time.Second)
	v, err := resolveSandboxReference(quickCtx, client, vmID)
	quickCancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting sandbox %s: %v\n", vmID, err)
		os.Exit(1)
	}

	if v.Status != "running" {
		fmt.Fprintf(os.Stderr, "Sandbox '%s' is not running (status: %s). Start it with: compute sandbox start %s\n", v.Name, v.Status, v.Name)
		os.Exit(1)
	}

	canonicalID := v.ID

	if proxyMode {
		query := url.Values{}
		query.Set("stream", "raw")
		query.Set("mode", "stdio")
		if user != "" {
			query.Set("user", user)
		}

		wsCtx, wsCancel := context.WithCancel(context.Background())
		defer wsCancel()

		wsConn, err := client.DialSSHWebSocket(wsCtx, canonicalID, query)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error connecting to sandbox SSH via daemon: %v\n", err)
			os.Exit(1)
		}
		defer wsConn.Close(websocket.StatusNormalClosure, "client exit")

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
		go func() {
			<-sigCh
			_ = wsConn.Close(websocket.StatusNormalClosure, "interrupted")
			os.Exit(130)
		}()

		done := make(chan struct{})

		// Stdin -> WebSocket
		go func() {
			defer close(done)
			buf := make([]byte, 32768)
			for {
				n, rErr := os.Stdin.Read(buf)
				if n > 0 {
					if wErr := wsConn.Write(wsCtx, websocket.MessageBinary, buf[:n]); wErr != nil {
						return
					}
				}
				if rErr != nil {
					_ = wsConn.Close(websocket.StatusNormalClosure, "stdin EOF")
					return
				}
			}
		}()

		// WebSocket -> Stdout
		for {
			_, data, rErr := wsConn.Read(wsCtx)
			if rErr != nil {
				break
			}
			if len(data) > 0 {
				if _, wErr := os.Stdout.Write(data); wErr != nil {
					break
				}
			}
		}

		wsCancel()
		<-done
		return
	}

	isTerminal := term.IsTerminal(int(os.Stdin.Fd()))
	usePTY := isTerminal
	if len(command) > 0 {
		usePTY = false
	}
	if forcePTY {
		usePTY = true
	} else if disablePTY {
		usePTY = false
	}

	cols := 80
	rows := 24
	if isTerminal {
		if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 && h > 0 {
			cols = w
			rows = h
		}
	}

	query := url.Values{}
	query.Set("user", user)
	query.Set("cols", strconv.Itoa(cols))
	query.Set("rows", strconv.Itoa(rows))
	if termEnv := os.Getenv("TERM"); termEnv != "" {
		query.Set("term", termEnv)
	} else {
		query.Set("term", "xterm-256color")
	}
	if len(command) > 0 {
		query.Set("cmd", strings.Join(command, " "))
	}
	if usePTY {
		query.Set("pty", "true")
	} else {
		query.Set("pty", "false")
	}
	if password != "" {
		query.Set("password", password)
	}

	wsCtx, wsCancel := context.WithCancel(context.Background())
	defer wsCancel()

	wsConn, err := client.DialSSHWebSocket(wsCtx, canonicalID, query)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to sandbox SSH via daemon: %v\n", err)
		os.Exit(1)
	}
	defer wsConn.Close(websocket.StatusNormalClosure, "client exit")

	var oldState *term.State
	if isTerminal && usePTY {
		oldState, err = term.MakeRaw(int(os.Stdin.Fd()))
		if err == nil {
			defer term.Restore(int(os.Stdin.Fd()), oldState)
		}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		<-sigCh
		if oldState != nil {
			_ = term.Restore(int(os.Stdin.Fd()), oldState)
		}
		_ = wsConn.Close(websocket.StatusNormalClosure, "interrupted")
		os.Exit(130)
	}()

	if isTerminal && usePTY {
		winchCh := make(chan os.Signal, 1)
		notifyWinch(winchCh)
		go func() {
			for range winchCh {
				if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 && h > 0 {
					resizeMsg, _ := json.Marshal(map[string]interface{}{
						"type": "resize",
						"cols": w,
						"rows": h,
					})
					_ = wsConn.Write(wsCtx, websocket.MessageText, resizeMsg)
				}
			}
		}()
	}

	go func() {
		buf := make([]byte, 4096)
		for {
			n, rErr := os.Stdin.Read(buf)
			if n > 0 {
				if wErr := wsConn.Write(wsCtx, websocket.MessageBinary, buf[:n]); wErr != nil {
					return
				}
			}
			if rErr != nil {
				return
			}
		}
	}()

	exitCode := 0
	for {
		msgType, data, rErr := wsConn.Read(wsCtx)
		if rErr != nil {
			break
		}
		if msgType == websocket.MessageText && len(data) > 0 && data[0] == '{' {
			var ctrl struct {
				Type string `json:"type"`
				Code int    `json:"code"`
			}
			if jErr := json.Unmarshal(data, &ctrl); jErr == nil && ctrl.Type == "exit" {
				exitCode = ctrl.Code
				continue
			}
		}
		_, _ = os.Stdout.Write(data)
	}

	if oldState != nil {
		_ = term.Restore(int(os.Stdin.Fd()), oldState)
		oldState = nil
	}
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

func handleExecCommand(ctx context.Context, client *DaemonClient, args []string) {
	execCmd := flag.NewFlagSet("sandbox exec", flag.ContinueOnError)
	execCmd.SetOutput(os.Stderr)
	noStream := execCmd.Bool("no-stream", false, "Buffer output and print it only when the command finishes")
	jsonOut := execCmd.Bool("json", false, "Print the buffered result as JSON (implies --no-stream)")
	timeout := execCmd.Int("timeout", 0, "Command timeout in seconds (0 = no timeout while streaming)")
	userFlag := execCmd.String("user", "", "SSH user (default: compute)")

	const usage = "Usage: compute sandbox exec [flags] [user@]<name|id> [--] <command...>"
	if err := execCmd.Parse(args); err != nil {
		printCommandError("invalid exec flags.", usage, "sandbox")
	}
	rest := execCmd.Args()
	if len(rest) < 1 {
		printCommandError("missing sandbox target for exec.", usage, "sandbox")
	}

	target := rest[0]
	user := "compute"
	if *userFlag != "" {
		user = *userFlag
	}
	vmID := target
	if strings.Contains(target, "@") {
		parts := strings.SplitN(target, "@", 2)
		user = parts[0]
		vmID = parts[1]
	}
	if resolved, err := resolveSandboxReference(ctx, client, vmID); err == nil && resolved != nil {
		vmID = resolved.ID
	}

	cmdArgs := rest[1:]
	if len(cmdArgs) > 0 && cmdArgs[0] == "--" {
		cmdArgs = cmdArgs[1:]
	}
	if len(cmdArgs) == 0 {
		printCommandError("no command specified to execute inside sandbox.", usage, "sandbox")
	}

	req := vm.ExecRequest{
		Command: buildExecCommand(cmdArgs),
		User:    user,
		Timeout: *timeout,
	}

	if *jsonOut {
		*noStream = true
	}
	if *noStream {
		resp, err := client.ExecVM(ctx, vmID, req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Exec error: %v\n", err)
			os.Exit(1)
		}
		if *jsonOut {
			_ = json.NewEncoder(os.Stdout).Encode(resp)
		} else {
			if resp.Stdout != "" {
				fmt.Print(resp.Stdout)
			}
			if resp.Stderr != "" {
				fmt.Fprint(os.Stderr, resp.Stderr)
			}
		}
		if resp.ExitCode != 0 {
			os.Exit(resp.ExitCode)
		}
		return
	}

	exitCode := 0
	streamErr := ""
	err := client.ExecVMStream(ctx, vmID, req, func(ev vm.ExecStreamEvent) error {
		if ev.Data != "" {
			if ev.Stream == "stderr" {
				fmt.Fprint(os.Stderr, ev.Data)
			} else {
				fmt.Fprint(os.Stdout, ev.Data)
			}
		}
		if ev.Error != "" {
			streamErr = ev.Error
		}
		if ev.ExitCode != nil {
			exitCode = *ev.ExitCode
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Exec error: %v\n", err)
		os.Exit(1)
	}
	if streamErr != "" {
		fmt.Fprintf(os.Stderr, "Exec error: %s\n", streamErr)
	}
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

// quoteForRemoteShell wraps an argument in single quotes if it contains spaces
// or shell metacharacters, escaping any inner single quotes, so that compound
// arguments (e.g. bash -c "mkdir -p /path && cmd") survive remote SSH shell execution.
func quoteForRemoteShell(arg string) string {
	if len(arg) == 0 {
		return "''"
	}
	safe := true
	for i := 0; i < len(arg); i++ {
		c := arg[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '_' || c == '-' || c == '.' || c == '/' || c == ':' || c == '=' || c == '@' {
			continue
		}
		safe = false
		break
	}
	if safe {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// buildExecCommand combines command arguments into a remote shell string.
// If a single argument is supplied, it is treated as a raw shell command string.
// If multiple arguments are supplied, each argument is safely shell-quoted so
// subcommands, flags, and compound commands preserve their boundaries over SSH.
func buildExecCommand(cmdArgs []string) string {
	if len(cmdArgs) == 0 {
		return ""
	}
	if len(cmdArgs) == 1 {
		return cmdArgs[0]
	}
	quoted := make([]string, len(cmdArgs))
	for i, a := range cmdArgs {
		quoted[i] = quoteForRemoteShell(a)
	}
	return strings.Join(quoted, " ")
}

// Job Command Implementation
func handleJobCommand(ctx context.Context, client *DaemonClient, args []string) {
	if printHelpIfRequested("job", args) {
		return
	}
	if len(args) == 0 {
		fmt.Printf("%s\n\n", commandHelp["job"])
		return
	}
	if args[0] == "list" || args[0] == "ls" {
		jobs, err := client.ListJobs(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing jobs: %v\n", err)
			os.Exit(1)
		}
		if len(jobs) == 0 {
			fmt.Println("No jobs found. Run one with: compute job run -- <command>")
			return
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "ID\tTYPE\tPOOL\tSTATE\tPRIORITY\tQUEUED")
		for _, j := range jobs {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\n",
				j.ID, j.Type, j.PoolID, j.State, j.Priority, j.QueuedAt.Format("2006-01-02 15:04:05"))
		}
		w.Flush()
		return
	}

	sub := args[0]
	switch sub {
	case "run":
		runCmd := flag.NewFlagSet("job run", flag.ExitOnError)
		image := runCmd.String("image", "", "Base image for job execution (default: daemon default image)")
		pool := runCmd.String("pool", "default", "Target worker pool")
		arch := runCmd.String("arch", runtime.GOARCH, "Target architecture (amd64 or arm64)")
		vcpus := runCmd.Int("vcpus", 2, "vCPUs required")
		mem := runCmd.Int("mem", 2048, "Memory in MB required")
		files := runCmd.String("files", "", "Path to directory or files to package and sync to the job sandbox")
		cwd := runCmd.String("cwd", "/workspace", "Working directory inside the job sandbox")
		timeout := runCmd.Int("timeout", 0, "Job timeout in seconds (0 = no timeout)")
		follow := runCmd.Bool("follow", true, "Follow and stream job logs")

		var cmdArgs []string
		for i, arg := range args[1:] {
			if arg == "--" {
				runCmd.Parse(args[1 : 1+i])
				cmdArgs = args[1+i+1:]
				break
			}
		}
		if cmdArgs == nil {
			runCmd.Parse(args[1:])
			cmdArgs = runCmd.Args()
		}
		if len(cmdArgs) == 0 {
			printCommandError("no command specified for job.", "Usage: compute job run [flags] -- <command...>", "job")
		}

		var filesPayload string
		if *files != "" {
			var err error
			filesPayload, err = packageFilesToTarGzBase64(*files)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error packaging files from %s: %v\n", *files, err)
				os.Exit(1)
			}
		}

		targetArch := strings.ToLower(strings.TrimSpace(*arch))
		switch targetArch {
		case "":
			targetArch = runtime.GOARCH
		case "x86_64", "x64":
			targetArch = "amd64"
		case "aarch64":
			targetArch = "arm64"
		}

		spec := api.JobSpec{
			ImageID:        *image,
			Architecture:   targetArch,
			VCPUs:          *vcpus,
			MemoryMB:       *mem,
			Command:        cmdArgs,
			CWD:            *cwd,
			Files:          filesPayload,
			TimeoutSeconds: *timeout,
		}

		job, err := client.CreateJob(ctx, api.JobCreateRequest{
			Type:     "command",
			PoolID:   *pool,
			Priority: 10,
			Source:   "cli",
			Spec:     spec,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating job: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Job submitted: ID=%s State=%s\n", job.ID, job.State)

		if !*follow {
			return
		}

		seenSeqs := make(map[string]bool)
		ticker := time.NewTicker(400 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				logs, _ := client.GetJobLogs(ctx, job.ID)
				for _, l := range logs {
					key := fmt.Sprintf("%s:%d", l.Stream, l.Seq)
					if !seenSeqs[key] {
						seenSeqs[key] = true
						if l.Stream == "stderr" {
							fmt.Fprintln(os.Stderr, l.Data)
						} else {
							fmt.Println(l.Data)
						}
					}
				}

				currentJob, err := client.GetJob(ctx, job.ID)
				if err == nil && currentJob != nil {
					if currentJob.State == "succeeded" {
						fmt.Printf("\n[Job completed successfully]\n")
						return
					} else if currentJob.State == "failed" {
						fmt.Fprintf(os.Stderr, "\n[Job failed]\n")
						os.Exit(1)
					} else if currentJob.State == "canceled" {
						fmt.Fprintf(os.Stderr, "\n[Job canceled]\n")
						os.Exit(130)
					}
				}
			}
		}

	case "info":
		if len(args) < 2 {
			printCommandError("missing required job ID.", "Usage: compute job info <id>", "job")
		}
		job, err := client.GetJob(ctx, args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting job: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Job %s:\n", job.ID)
		fmt.Printf("  State:    %s\n", job.State)
		fmt.Printf("  Pool:     %s\n", job.PoolID)
		fmt.Printf("  Type:     %s\n", job.Type)
		fmt.Printf("  Queued:   %s\n", job.QueuedAt.Format(time.RFC3339))
		if job.StartedAt != nil {
			fmt.Printf("  Started:  %s\n", job.StartedAt.Format(time.RFC3339))
		}
		if job.FinishedAt != nil {
			fmt.Printf("  Finished: %s\n", job.FinishedAt.Format(time.RFC3339))
		}
		if job.ResultJSON != "" {
			fmt.Printf("  Result:   %s\n", job.ResultJSON)
		}

	case "logs":
		if len(args) < 2 {
			printCommandError("missing required job ID.", "Usage: compute job logs <id>", "job")
		}
		logs, err := client.GetJobLogs(ctx, args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting logs: %v\n", err)
			os.Exit(1)
		}
		for _, l := range logs {
			if l.Stream == "stderr" {
				fmt.Fprintln(os.Stderr, l.Data)
			} else {
				fmt.Println(l.Data)
			}
		}

	case "cancel":
		if len(args) < 2 {
			printCommandError("missing required job ID.", "Usage: compute job cancel <id>", "job")
		}
		if err := client.CancelJob(ctx, args[1]); err != nil {
			fmt.Fprintf(os.Stderr, "Error canceling job: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Job %s canceled\n", args[1])

	default:
		printCommandError(fmt.Sprintf("unknown job command '%s'.", sub), "", "job")
	}
}

// Worker Command Implementation
func handleWorkerCommand(ctx context.Context, client *DaemonClient, args []string) {
	if printHelpIfRequested("worker", args) {
		return
	}
	if len(args) == 0 {
		fmt.Printf("%s\n\n", commandHelp["worker"])
		return
	}
	if args[0] == "list" || args[0] == "ls" {
		workers, err := client.ListWorkers(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing workers: %v\n", err)
			os.Exit(1)
		}
		if len(workers) == 0 {
			fmt.Println("No workers connected. Generate an enrollment token with: compute worker token create")
			return
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tPOOL\tSTATUS\tARCH\tVCPUS\tMEM(MB)\tLAST HEARTBEAT")
		for _, wrk := range workers {
			hb := "-"
			if wrk.LastHeartbeat != nil {
				hb = wrk.LastHeartbeat.Format("15:04:05")
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\t%d\t%s\n",
				wrk.ID, wrk.Name, wrk.PoolID, wrk.Status, wrk.Arch, wrk.TotalVCPUs, wrk.TotalMemMB, hb)
		}
		w.Flush()
		return
	}

	sub := args[0]
	switch sub {
	case "token":
		if len(args) < 2 || args[1] != "create" {
			printCommandError("missing or invalid action for 'worker token'.", "Usage: compute worker token create [--pool <pool>]", "worker")
		}
		tokenCmd := flag.NewFlagSet("worker token create", flag.ExitOnError)
		pool := tokenCmd.String("pool", "default", "Worker pool ID")
		name := tokenCmd.String("name", "", "Worker name to bind the token to (default: bind to the first worker that enrolls)")
		tokenCmd.Parse(args[2:])

		resp, err := client.CreateWorkerToken(ctx, api.WorkerTokenCreateRequest{
			Name:   *name,
			PoolID: *pool,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating worker token: %v\n", err)
			os.Exit(1)
		}

		fmt.Println("Worker Enrollment Token Generated:")
		fmt.Printf("  Token: %s\n", resp.Token)
		fmt.Println()
		fmt.Println("Run this command on your worker machine to enroll:")
		fmt.Printf("  compute-worker run --control-plane %s --token %s --pool %s\n", client.baseURL, resp.Token, *pool)

	case "info":
		if len(args) < 2 {
			printCommandError("missing required worker ID.", "Usage: compute worker info <id>", "worker")
		}
		wrk, err := client.GetWorker(ctx, args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting worker: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Worker Details: %s\n", wrk.ID)
		fmt.Printf("  Name:         %s\n", wrk.Name)
		fmt.Printf("  Pool:         %s\n", wrk.PoolID)
		fmt.Printf("  Status:       %s\n", wrk.Status)
		fmt.Printf("  Arch:         %s\n", wrk.Arch)
		fmt.Printf("  Capabilities: %s\n", wrk.CapabilitiesJSON)

	case "cordon":
		if len(args) < 2 {
			printCommandError("missing required worker ID.", "Usage: compute worker cordon <id>", "worker")
		}
		if err := client.CordonWorker(ctx, args[1]); err != nil {
			fmt.Fprintf(os.Stderr, "Error cordoning worker: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Worker %s cordoned\n", args[1])

	case "drain":
		if len(args) < 2 {
			printCommandError("missing required worker ID.", "Usage: compute worker drain <id>", "worker")
		}
		if err := client.DrainWorker(ctx, args[1]); err != nil {
			fmt.Fprintf(os.Stderr, "Error draining worker: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Worker %s draining\n", args[1])

	case "delete", "rm":
		if len(args) < 2 {
			printCommandError("missing required worker ID.", "Usage: compute worker delete <id>", "worker")
		}
		if err := client.DeleteWorker(ctx, args[1]); err != nil {
			fmt.Fprintf(os.Stderr, "Error deleting worker: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Worker %s removed\n", args[1])

	default:
		printCommandError(fmt.Sprintf("unknown worker command '%s'.", sub), "", "worker")
	}
}

// Pool Command Implementation
func handlePoolCommand(ctx context.Context, client *DaemonClient, args []string) {
	if printHelpIfRequested("pool", args) {
		return
	}
	if len(args) == 0 {
		fmt.Printf("%s\n\n", commandHelp["pool"])
		return
	}
	if args[0] == "list" || args[0] == "ls" {
		pools, err := client.ListPools(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing pools: %v\n", err)
			os.Exit(1)
		}
		if len(pools) == 0 {
			fmt.Println("No worker pools defined. Create one with: compute pool create <name>")
			return
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tCREATED")
		for _, p := range pools {
			fmt.Fprintf(w, "%s\t%s\t%s\n", p.ID, p.Name, p.CreatedAt.Format("2006-01-02 15:04:05"))
		}
		w.Flush()
		return
	}

	sub := args[0]
	switch sub {
	case "create":
		if len(args) < 2 {
			printCommandError("missing required pool name.", "Usage: compute pool create <name>", "pool")
		}
		pool, err := client.CreatePool(ctx, args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating pool: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Pool created: ID=%s Name=%s\n", pool.ID, pool.Name)

	case "delete", "rm":
		if len(args) < 2 {
			printCommandError("missing required pool ID.", "Usage: compute pool delete <id>", "pool")
		}
		if err := client.DeletePool(ctx, args[1]); err != nil {
			fmt.Fprintf(os.Stderr, "Error deleting pool: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Pool %s deleted\n", args[1])

	default:
		printCommandError(fmt.Sprintf("unknown pool command '%s'.", sub), "", "pool")
	}
}

// handleDoctorCommand renders the daemon readiness report with remediation.
func handleDoctorCommand(ctx context.Context, client *DaemonClient, args []string) {
	_ = args
	report, err := client.GetReadiness(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot reach the Compute daemon: %v\n", err)
		fmt.Fprintln(os.Stderr, "Check that the daemon is running and COMPUTE_DAEMON points at it (default 127.0.0.1:18080).")
		os.Exit(1)
	}
	for _, check := range report.Checks {
		status := "OK"
		if !check.OK {
			status = strings.ToUpper(string(check.Severity))
		}
		fmt.Printf("[%-8s] %-14s %s\n", status, check.Name, check.Detail)
		if !check.OK && check.Remediation != "" {
			fmt.Printf("           fix: %s\n", check.Remediation)
		}
	}
	if !report.OK {
		fmt.Println("\nReadiness failed. Fix the critical items above.")
		os.Exit(1)
	}
	fmt.Println("\nReadiness: all critical checks passed.")
}

// Image Command Implementation
func handleImageCommand(ctx context.Context, client *DaemonClient, args []string) {
	if printHelpIfRequested("image", args) {
		return
	}
	if len(args) == 0 {
		fmt.Printf("%s\n\n", commandHelp["image"])
		return
	}
	subcmd := args[0]

	switch subcmd {
	case "list", "ls":
		images, err := client.ListImages(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing images: %v\n", err)
			os.Exit(1)
		}
		if len(images) == 0 {
			fmt.Println("No images available.")
			return
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "NAME\tKIND\tSTATUS\tSIZE (MB)\tPACKAGES")
		for _, img := range images {
			pkgsStr := strings.Join(img.Packages, ", ")
			if len(pkgsStr) > 35 {
				pkgsStr = pkgsStr[:32] + "..."
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n",
				img.Name, img.Kind, img.Status, img.SizeMB, pkgsStr)
		}
		w.Flush()

	case "build", "create":
		buildCmd := flag.NewFlagSet("image build", flag.ExitOnError)
		name := buildCmd.String("name", "", "Name for custom image")
		pkgs := buildCmd.String("packages", "", "Comma-separated packages to install")
		base := buildCmd.String("base", "ubuntu-24", "Base image to derive from")
		size := buildCmd.Int("size", 2048, "Image disk size in MiB")
		desc := buildCmd.String("desc", "", "Optional description")
		cmdFlag := buildCmd.String("command", "", "Optional build command or script to execute during image build")
		buildCmd.StringVar(cmdFlag, "run", "", "Alias for --command")
		buildCmd.Parse(args[1:])

		if *name == "" {
			printCommandError("--name is required to build an image.", "Usage: compute image build --name <name> [--base <image>] [--packages <a,b>] [--command <cmd>] [--size <MiB>] [--desc <text>]", "image")
		}
		var pkgList []string
		if *pkgs != "" {
			for _, p := range strings.Split(*pkgs, ",") {
				clean := strings.TrimSpace(p)
				if clean != "" {
					pkgList = append(pkgList, clean)
				}
			}
		}

		img, err := client.BuildImage(ctx, vm.BuildImageRequest{
			Name:        *name,
			BaseImage:   *base,
			Packages:    pkgList,
			Command:     *cmdFlag,
			SizeMB:      *size,
			Description: *desc,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error building image: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Image Built Successfully: Name=%s Status=%s Size=%dMB\n",
			img.Name, img.Status, img.SizeMB)

	case "inspect", "show":
		if len(args) < 2 {
			printCommandError("missing required image name or ID.", "Usage: compute image inspect <name|id>", "image")
		}
		img, err := client.GetImage(ctx, args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error inspecting image: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Image Details: %s\n", img.Name)
		fmt.Printf("  ID:       %s\n", img.ID)
		kind := img.Kind
		if kind == "" {
			kind = "custom"
		}
		fmt.Printf("  Kind:     %s\n", kind)
		fmt.Printf("  Status:   %s\n", img.Status)
		fmt.Printf("  Size:     %d MB\n", img.SizeMB)
		fmt.Printf("  Packages: %s\n", strings.Join(img.Packages, ", "))

	case "delete", "rm":
		if len(args) < 2 {
			printCommandError("missing required image name or ID.", "Usage: compute image delete <name|id>", "image")
		}
		if err := client.DeleteImage(ctx, args[1]); err != nil {
			fmt.Fprintf(os.Stderr, "Error deleting image: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Image Deleted: %s\n", args[1])

	default:
		printCommandError(fmt.Sprintf("unknown image command '%s'.", subcmd), "", "image")
	}
}

// Template Command Implementation
func handleTemplateCommand(ctx context.Context, client *DaemonClient, args []string) {
	if printHelpIfRequested("template", args) {
		return
	}
	if len(args) == 0 {
		fmt.Printf("%s\n\n", commandHelp["template"])
		return
	}
	subcmd := args[0]

	switch subcmd {
	case "list", "ls":
		templates, err := client.ListTemplates(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing templates: %v\n", err)
			os.Exit(1)
		}
		if len(templates) == 0 {
			fmt.Println("No saved templates. Create one with: compute template create <name> --vm <vm_id>")
			return
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "NAME\tSOURCE VM\tVCPUS\tMEM(MB)\tDISK(MB)\tCREATED")
		for _, tpl := range templates {
			fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%s\n",
				tpl.Name, tpl.SourceVMID, tpl.VCPUs, tpl.MemoryMB, tpl.DiskMB,
				tpl.CreatedAt.Format("2006-01-02 15:04:05"))
		}
		w.Flush()

	case "create":
		createCmd := flag.NewFlagSet("template create", flag.ExitOnError)
		nameFlag := createCmd.String("name", "", "Template name")
		sourceVM := createCmd.String("vm", "", "Source VM ID or Name")
		desc := createCmd.String("desc", "", "Optional description")

		var flagArgs, positional []string
		valueFlags := map[string]bool{
			"--vm": true, "-vm": true,
			"--name": true, "-name": true,
			"--desc": true, "-desc": true,
		}
		for i := 1; i < len(args); i++ {
			arg := args[i]
			if arg == "--" {
				positional = append(positional, args[i+1:]...)
				break
			}
			if !strings.HasPrefix(arg, "-") || arg == "-" {
				positional = append(positional, arg)
				continue
			}
			flagArgs = append(flagArgs, arg)
			if valueFlags[arg] && i+1 < len(args) {
				i++
				flagArgs = append(flagArgs, args[i])
			}
		}
		createCmd.Parse(flagArgs)

		tplName := strings.TrimSpace(*nameFlag)
		if tplName == "" && len(positional) > 0 {
			tplName = strings.TrimSpace(positional[0])
		}
		if tplName == "" {
			printCommandError("missing required template name.", "Usage: compute template create <template_name> --vm <vm_id> [--desc <description>]", "template")
		}
		if *sourceVM == "" {
			printCommandError("--vm is required to specify source VM.", "Usage: compute template create <template_name> --vm <vm_id> [--desc <description>]", "template")
		}

		tpl, err := client.CreateTemplate(ctx, vm.CreateTemplateRequest{
			Name:        tplName,
			VMID:        *sourceVM,
			Description: *desc,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating template: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Template Created Successfully: Name=%s SourceVM=%s Specs=(%dvCPU, %dMB RAM, %dMB Disk)\n",
			tpl.Name, tpl.SourceVMID, tpl.VCPUs, tpl.MemoryMB, tpl.DiskMB)

	case "inspect", "show":
		if len(args) < 2 {
			printCommandError("missing required template name or ID.", "Usage: compute template inspect <name|id>", "template")
		}
		tpl, err := client.GetTemplate(ctx, args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error inspecting template: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Template Details: %s\n", tpl.Name)
		fmt.Printf("  ID:          %s\n", tpl.ID)
		fmt.Printf("  Source VM:   %s\n", tpl.SourceVMID)
		fmt.Printf("  Specs:       %d vCPUs, %d MB RAM, %d MB Disk\n", tpl.VCPUs, tpl.MemoryMB, tpl.DiskMB)
		if tpl.Description != "" {
			fmt.Printf("  Description: %s\n", tpl.Description)
		}
		fmt.Printf("  Created:     %s\n", tpl.CreatedAt.Format(time.RFC3339))

	case "launch":
		if len(args) < 2 {
			printCommandError("missing required template name or ID.", "Usage: compute template launch <template_name|id> [--name <vm_name>] [--tier <tier>]", "template")
		}
		tplName := args[1]
		launchCmd := flag.NewFlagSet("template launch", flag.ExitOnError)
		name := launchCmd.String("name", "", "Name for launched VM")
		tier := launchCmd.String("tier", "", "VM tier ('1vcpu', '2vcpu', '4vcpu')")
		networkIsolation := launchCmd.String("network-isolation", "", "Sandbox network isolation mode: 'account' or 'isolated'")
		isolated := launchCmd.Bool("isolated", false, "Isolate this sandbox on its own network")
		launchCmd.Parse(args[2:])

		isolatedSet := false
		launchCmd.Visit(func(f *flag.Flag) {
			if f.Name == "isolated" {
				isolatedSet = true
			}
		})
		var isolatedPtr *bool
		if isolatedSet {
			val := *isolated
			isolatedPtr = &val
		}

		v, err := client.LaunchTemplate(ctx, tplName, vm.CreateRequest{
			Name:             *name,
			Tier:             *tier,
			NetworkIsolation: *networkIsolation,
			Isolated:         isolatedPtr,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error launching VM from template %s: %v\n", tplName, err)
			os.Exit(1)
		}
		fmt.Printf("Sandbox Launched from template '%s': ID=%s Name=%s IP=%s Status=%s\n",
			tplName, v.ID, v.Name, v.IPAddress, v.Status)

	case "delete", "rm":
		if len(args) < 2 {
			printCommandError("missing required template name or ID.", "Usage: compute template delete <name|id>", "template")
		}
		if err := client.DeleteTemplate(ctx, args[1]); err != nil {
			fmt.Fprintf(os.Stderr, "Error deleting template: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Template Deleted: %s\n", args[1])

	default:
		printCommandError(fmt.Sprintf("unknown template command '%s'.", subcmd), "", "template")
	}
}

func packageFilesToTarGzBase64(srcPath string) (string, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	info, err := os.Stat(srcPath)
	if err != nil {
		return "", err
	}

	baseDir := srcPath
	if !info.IsDir() {
		baseDir = filepath.Dir(srcPath)
	}

	err = filepath.Walk(srcPath, func(file string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(baseDir, file)
		if err != nil {
			return err
		}
		if relPath == "." {
			return nil
		}

		header, err := tar.FileInfoHeader(fi, fi.Name())
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relPath)

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		if fi.Mode().IsRegular() {
			data, err := os.Open(file)
			if err != nil {
				return err
			}
			defer data.Close()
			if _, err := io.Copy(tw, data); err != nil {
				return err
			}
		}
		return nil
	})

	if err != nil {
		return "", err
	}

	if err := tw.Close(); err != nil {
		return "", err
	}
	if err := gw.Close(); err != nil {
		return "", err
	}

	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

func handleNetworkCommand(ctx context.Context, client *DaemonClient, args []string) {
	if len(args) == 0 {
		args = []string{"status"}
	}
	switch args[0] {
	case "status", "info", "show":
		info, err := client.GetNetworkInfo(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching network status: %v\n", err)
			os.Exit(1)
		}
		nat := "disabled"
		if info.NATEnabled {
			nat = "enabled"
		}
		fmt.Printf("Mode:             %s\n", info.Mode)
		fmt.Printf("Bridge Interface: %s\n", info.BridgeName)
		fmt.Printf("Bridge IP:        %s\n", info.BridgeIP)
		fmt.Printf("Subnet:           %s\n", info.SubnetCIDR)
		fmt.Printf("Gateway:          %s\n", info.GatewayIP)
		fmt.Printf("Netmask:          %s\n", info.Netmask)
		fmt.Printf("NAT Masquerading: %s\n", nat)
		fmt.Printf("Bridge Active:    %t\n", info.BridgeActive)
		fmt.Printf("Allocated IPs:    %d\n", info.AllocatedIPs)
		if len(info.ActiveTAPs) > 0 {
			fmt.Printf("Active TAPs:      %s\n", strings.Join(info.ActiveTAPs, ", "))
		}

	case "list", "ls":
		allocs, err := client.GetNetworkAllocations(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing network allocations: %v\n", err)
			os.Exit(1)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "VM ID\tNAME\tSTATUS\tIP ADDRESS\tPUBLIC IP\tMAC\tTAP")
		for _, a := range allocs {
			pub := a.PublicIP
			if pub == "" {
				pub = "-"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", a.VMID, a.VMName, a.Status, a.IPAddress, pub, a.MACAddress, a.TAPDevice)
		}
		_ = w.Flush()

	case "inspect":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: compute network inspect <sandbox-id|name>")
			os.Exit(1)
		}
		res, err := client.InspectNetwork(ctx, args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error inspecting network: %v\n", err)
			os.Exit(1)
		}
		reach := "Unreachable"
		if res.Reachable {
			reach = "Reachable"
		}
		fmt.Printf("VM:             %s (%s)\n", res.VMName, res.VMID)
		fmt.Printf("Status:         %s\n", res.Status)
		fmt.Printf("IPv4 Address:  %s\n", res.IPAddress)
		fmt.Printf("Public IP:      %s\n", res.PublicIP)
		fmt.Printf("MAC Address:    %s\n", res.MACAddress)
		fmt.Printf("TAP Device:     %s\n", res.TAPDevice)
		fmt.Printf("Bridge:         %s\n", res.BridgeName)
		fmt.Printf("Gateway:        %s\n", res.Gateway)
		fmt.Printf("Connectivity:   %s\n", reach)
		if res.Connectivity != "" {
			fmt.Printf("Details:        %s\n", res.Connectivity)
		}

	case "prune":
		pruned, err := client.PruneNetwork(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error pruning network: %v\n", err)
			os.Exit(1)
		}
		if len(pruned) == 0 {
			fmt.Println("Network is clean; no orphaned TAP devices or stale allocations found.")
		} else {
			for _, item := range pruned {
				fmt.Printf("Pruned: %s\n", item)
			}
			fmt.Printf("Network is clean; pruned %d orphaned item(s).\n", len(pruned))
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown network command %q.\n\n", args[0])
		fmt.Fprintln(os.Stderr, "Usage: compute network status|list|inspect <id>|prune")
		os.Exit(1)
	}
}

func handleVersionCommand(ctx context.Context, client *DaemonClient, args []string) {
	fs := flag.NewFlagSet("version", flag.ExitOnError)
	clientOnly := fs.Bool("client", false, "Display client version only (do not query daemon)")
	fs.BoolVar(clientOnly, "c", false, "Display client version only")
	short := fs.Bool("short", false, "Print only the version number")
	fs.BoolVar(short, "s", false, "Print only the version number")
	asJSON := fs.Bool("json", false, "Output version information in JSON format")
	fs.BoolVar(asJSON, "j", false, "Output version information in JSON format")
	targetDaemon := fs.String("daemon", "", "Compute daemon address to query")
	_ = fs.Parse(args)

	if *targetDaemon != "" {
		client = NewDaemonClient(*targetDaemon)
	}

	clientInfo := version.Get("compute", "Compute CLI")

	if *short {
		fmt.Println(clientInfo.Version)
		return
	}

	var daemonInfo *DaemonVersion
	var daemonErr error
	if !*clientOnly && client != nil {
		queryCtx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		daemonInfo, daemonErr = client.GetVersion(queryCtx)
	}

	if *asJSON {
		res := map[string]interface{}{
			"client": clientInfo,
		}
		if daemonInfo != nil {
			res["daemon"] = daemonInfo
		} else if daemonErr != nil && !*clientOnly {
			res["daemon"] = map[string]string{
				"error": daemonErr.Error(),
			}
		}
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))
		return
	}

	if *clientOnly || (daemonInfo == nil && daemonErr != nil) {
		fmt.Printf("%s\n\n", clientInfo.String())
		fmt.Println("Client Details:")
		fmt.Print(clientInfo.Detailed("  "))
		fmt.Println()
		if !*clientOnly && daemonErr != nil {
			fmt.Printf("\nDaemon (%s):\n  Status:      unreachable (%v)\n", client.baseURL, daemonErr)
		}
		return
	}

	fmt.Printf("%s\n\n", clientInfo.String())
	fmt.Println("Client:")
	fmt.Print(clientInfo.Detailed("  "))
	fmt.Println()

	if daemonInfo != nil {
		fmt.Printf("\nDaemon (%s):\n", client.baseURL)
		fmt.Print(daemonInfo.Detailed("  "))
		fmt.Println()
	}
}
