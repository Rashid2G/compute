package main

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"
)

var commandHelp = map[string]string{
	"sandbox": `Manage isolated Firecracker sandboxes.

Usage:
  compute sandbox list
  compute sandbox create [flags]
  compute sandbox start|stop|save-state|resume|delete <name|id|pattern>...
  compute sandbox protect|unprotect <name|id>
  compute sandbox info <name|id|hostname>
  compute sandbox url [flags] <name|id|hostname>
  compute sandbox exec [flags] [user@]<name|id> -- <command...>
  compute sandbox ssh [user@]<name|id|hostname> [<command...>]

Commands:
  list, ls                 List sandboxes with their URL or SSH name.
  create                   Create a sandbox (--name, --tier, --vcpus, --memory, --disk, --image, --template, --ssh-key <name|path>, --ssh-password, --protected, --start/--boot).
  start, run               Start one or more sandboxes.
  stop                     Stop one or more sandboxes.
  save-state, snapshot     Save a running sandbox snapshot.
  resume                   Resume a sandbox from its snapshot.
  delete, rm               Delete one or more sandboxes.
  protect                  Enable deletion protection on a sandbox.
  unprotect                Disable deletion protection on a sandbox.
  images                   Discover available base and custom rootfs images.
  templates                Discover available reusable sandbox templates.
  info, inspect, show      Display sandbox details, hostname, SSH command and URLs.
  url                      Print the best HTTP URL, or the SSH name when no URL resolves.
  exec                     Execute a command inside a sandbox, streaming output live.
  ssh                      Open an SSH session to a sandbox.

Exec flags (place before the sandbox target):
  --no-stream              Buffer output and print it only when the command finishes.
  --json                   Print the buffered result as JSON (implies --no-stream).
  --timeout <seconds>      Command timeout (0 = no timeout while streaming).
  --user <username>        SSH user (default: compute, or the user@target prefix).

URL flags:
  --json                   Print the full access model as JSON.
  --port <n>               Include the port-scoped ingress URL for guest port <n>.
  -u, --user <name>        SSH user (default: compute).
  -v, --verbose            Show every sandbox name plus SSH/URL hints.

Target patterns support shell-style matching. Quote '*' or other patterns:
  compute sandbox delete '*'
  compute sandbox stop 'dev-*'`,

	"url": `Print the best HTTP URL or SSH target for a sandbox.

Usage:
  compute url [flags] [user@]<name|id|hostname>

Flags:
  --json              Print the full access model as JSON.
  --port <n>          Include the port-scoped ingress URL for guest port <n>.
  -u, --user <name>   SSH user (default: compute).
  -v, --verbose       Show every sandbox name plus SSH/URL hints.

The HTTP URL is only printed when the sandbox DNS zone is expected to resolve.
Names under Tailscale's *.ts.net zone are answered by MagicDNS, so the command
prints the tailnet DNS name or sandbox IP instead and explains why.`,

	"job": `Run and monitor distributed jobs.

Usage:
  compute job run [flags] -- <command...>
  compute job list
  compute job info <id>
  compute job logs <id>
  compute job cancel <id>

Commands:
  run                      Submit and run a distributed compute job.
  list, ls                 List recent jobs.
  info                     Display detailed job metadata and status.
  logs                     Display or stream job output logs.
  cancel                   Cancel an active or queued job.

Run flags:
  --image <name>           Base image for job execution (default: daemon default image)
  --pool <pool>            Target worker pool (default: default)
  --vcpus <n>              Number of vCPUs required (default: 2)
  --mem <MiB>              Memory size in MiB (default: 2048)
  --files <path>           Local directory or files to package and transfer
  --cwd <path>             Working directory inside the job sandbox (default: /workspace)
  --timeout <seconds>      Job timeout (0 = no timeout)
  --follow                 Follow and stream logs in real time (default: true)

Jobs run on a connected worker node, or in a local microVM on the daemon host
when no worker is connected (config: jobs.local_execution).`,

	"worker": `Manage worker nodes and enrollment.

Usage:
  compute worker list
  compute worker token create [--pool <pool>] [--name <worker-name>]
  compute worker info <id>
  compute worker cordon <id>
  compute worker drain <id>
  compute worker delete <id>

Commands:
  list, ls                 List enrolled worker nodes and statuses.
  token create             Generate an enrollment token for a new worker node.
                           Unnamed tokens bind to the first worker that enrolls
                           and are accepted again when that same worker restarts;
                           --name pins the token to one worker name.
  info                     Display detailed worker specifications and capabilities.
  cordon                   Mark a worker as unschedulable.
  drain                    Evict running jobs and cordon worker.
  delete, rm               Remove a worker from the cluster.`,

	"pool": `Manage worker pools.

Usage:
  compute pool list
  compute pool create <name>
  compute pool delete <id>

Commands:
  list, ls                 List all worker pools.
  create                   Create a new worker pool.
  delete, rm               Delete an existing worker pool.`,

	"image": `Manage base and custom rootfs images.

Usage:
  compute image list
  compute image build --name <name> [--base <image>] [--packages <a,b>] [--size <MiB>] [--desc <text>]
  compute image inspect <name|id>
  compute image delete <name|id>

Commands:
  list, ls                 List all available base and custom images.
  build, create            Build a custom rootfs image with preinstalled packages.
  inspect, show            Display image details and installed packages.
  delete, rm               Delete an image.`,

	"template": `Manage reusable VM disk templates.

Usage:
  compute template list
  compute template create <name> --vm <name|id> [--desc <text>]
  compute template launch <name|id> [--name <vm-name>] [--tier <tier>]
  compute template inspect <name|id>
  compute template delete <name|id>

Commands:
  list, ls                 List all saved VM disk templates.
  create                   Create a reusable template snapshot from an existing VM.
  launch                   Provision and start a new VM from a template.
  inspect, show            Display template details and hardware specs.
  delete, rm               Delete a template.`,

	"gui": `Launch the Compute Web UI dashboard.

Usage:
  compute gui [--port <port>] [--addr <addr>] [--daemon <addr>]

Options:
  --port <port>            Port to serve Web UI on (default: 8080)
  --addr <addr>            Address to bind Web UI to (default: 0.0.0.0)
  --daemon <addr>          Compute daemon address (default: 127.0.0.1:18080)`,

	"version": `Display version, build, and release information.

Usage:
  compute version [flags]

Flags:
  --client                 Print client version only without querying daemon
  --short                  Print version string only
  --json                   Output version information in JSON format`,

	"completion": `Generate shell autocompletion script.

Usage:
  compute completion <bash|zsh|fish|install>

Commands:
  bash                     Generate Bash autocompletion script.
  zsh                      Generate Zsh autocompletion script.
  fish                     Generate Fish autocompletion script.
  install                  Install completion script to ~/.bash_completion.d/compute.`,

	"ssh": `Connect to a sandbox via SSH over WebSocket.

Usage:
  compute [sandbox] ssh [options] [user@]<name|id> [-- <cmd...>]

Options:
  -u, --user <username>       SSH user (default: compute)
  -p, --password <password>   SSH password
  -t                          Force pseudo-terminal allocation
  -T                          Disable pseudo-terminal allocation`,

	"keys": `Manage saved SSH public keys for your account.

Usage:
  compute keys list
  compute keys add --name <name> --key-file <path> [--all]
  compute keys update <name|id> [--name <name>] [--key-file <path>] [--all|--no-all]
  compute keys apply <name|id>
  compute keys show <name|id>
  compute keys delete <name|id>...

Commands:
  list, ls                 List saved public keys and their scope.
  add, create              Save a public key (--key-file, or pass the key directly).
  update, set              Rename, replace, or change the scope of a saved key.
  apply                    Mark a key account-wide and push it to every sandbox.
  show, info               Display a saved key's details.
  delete, rm               Delete one or more saved keys.

Flags:
  --name <name>            Descriptive key name.
  --key-file <path>        Public key file, or '-' to read from stdin.
  --all                    Apply to all current and future sandboxes.
  --no-all                 Stop applying the key to every sandbox.

Account-wide keys (--all) are embedded into every sandbox you create and are
pushed to existing running sandboxes over SSH and to stopped sandboxes offline.
Without --all, a key is only embedded when you select it at creation time with
'compute sandbox create --ssh-key <name|path>'.`,
}

func init() {
	commandHelp["vm"] = commandHelp["sandbox"]
	commandHelp["jobs"] = commandHelp["job"]
	commandHelp["workers"] = commandHelp["worker"]
	commandHelp["pools"] = commandHelp["pool"]
	commandHelp["images"] = commandHelp["image"]
	commandHelp["templates"] = commandHelp["template"]
	commandHelp["autocomplete"] = commandHelp["completion"]
	commandHelp["ui"] = commandHelp["gui"]
	commandHelp["web"] = commandHelp["gui"]
	commandHelp["key"] = commandHelp["keys"]
	commandHelp["ssh-keys"] = commandHelp["keys"]
}

func printCommandError(reason string, usage string, group string) {
	if reason != "" {
		fmt.Fprintf(os.Stderr, "Error: %s\n", reason)
	}
	if usage != "" {
		fmt.Fprintf(os.Stderr, "%s\n", usage)
	}
	fmt.Fprintln(os.Stderr)
	if group != "" {
		if help, ok := commandHelp[group]; ok && help != "" {
			fmt.Fprintln(os.Stderr, help)
			os.Exit(1)
		}
	}
	printUsageTo(os.Stderr)
	os.Exit(1)
}

func printHelpIfRequested(command string, args []string) bool {
	if len(args) == 0 {
		return false
	}
	if len(args) > 1 && (args[0] == "ssh") {
		for _, arg := range args[1:] {
			if arg == "--" {
				break
			}
			if arg == "help" || arg == "-h" || arg == "--help" {
				printSSHUsage()
				return true
			}
		}
		return false
	}
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "help" || arg == "-h" || arg == "--help" {
			fmt.Printf("%s\n\n", commandHelp[command])
			return true
		}
	}
	return false
}

func resolveSandboxTargets(ctx context.Context, client *DaemonClient, args []string) ([]string, error) {
	var targets []string
	seen := make(map[string]bool)
	for _, arg := range args {
		if !strings.ContainsAny(arg, "*?[") {
			if !seen[arg] {
				targets = append(targets, arg)
				seen[arg] = true
			}
			continue
		}

		vms, err := client.ListVMs(ctx)
		if err != nil {
			return nil, err
		}
		for _, v := range vms {
			for _, candidate := range []string{v.ID, v.Name, v.InternalID} {
				if candidate == "" {
					continue
				}
				matched, err := path.Match(arg, candidate)
				if err != nil {
					return nil, fmt.Errorf("invalid sandbox pattern %q: %w", arg, err)
				}
				if matched && !seen[v.ID] {
					targets = append(targets, v.ID)
					seen[v.ID] = true
					break
				}
			}
		}
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("no sandboxes matched")
	}
	return targets, nil
}
