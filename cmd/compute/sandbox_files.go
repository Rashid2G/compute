package main

import (
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"unicode/utf8"
)

// handleSandboxFileCommand implements `compute sandbox file [read|write|list|mkdir|remove]`.
func handleSandboxFileCommand(ctx context.Context, client *DaemonClient, args []string) {
	if printHelpIfRequested("file", args) || len(args) == 0 {
		printFileUsage()
		return
	}

	action := args[0]
	subArgs := args[1:]

	switch action {
	case "read", "cat":
		handleFileRead(ctx, client, subArgs)
	case "write":
		handleFileWrite(ctx, client, subArgs)
	case "list", "ls":
		handleFileList(ctx, client, subArgs)
	case "mkdir":
		handleFileMkdir(ctx, client, subArgs)
	case "remove", "rm", "delete":
		handleFileRemove(ctx, client, subArgs)
	default:
		printCommandError(fmt.Sprintf("unknown file action '%s'.", action), "Available actions: read, write, list, mkdir, remove", "file")
	}
}

func printFileUsage() {
	fmt.Fprintf(os.Stderr, "Usage: compute sandbox file <action> <sandbox> [args...]\n\n")
	fmt.Fprintf(os.Stderr, "Interact with the sandbox filesystem via high-speed daemon REST endpoints.\n\n")
	fmt.Fprintf(os.Stderr, "Actions:\n")
	fmt.Fprintf(os.Stderr, "  read <sandbox> <remote-path> [--format text|base64]\n")
	fmt.Fprintf(os.Stderr, "      Read file contents from the sandbox and print to stdout.\n")
	fmt.Fprintf(os.Stderr, "  write <sandbox> <remote-path> [--content <data> | --file <path> | -]\n")
	fmt.Fprintf(os.Stderr, "      Write data to a file inside the sandbox. Reads stdin if '-' or if no source flag is provided.\n")
	fmt.Fprintf(os.Stderr, "  list, ls <sandbox> [remote-path]\n")
	fmt.Fprintf(os.Stderr, "      List files and directories (default: /workspace).\n")
	fmt.Fprintf(os.Stderr, "  mkdir <sandbox> <remote-path>\n")
	fmt.Fprintf(os.Stderr, "      Create directory and parents inside the sandbox.\n")
	fmt.Fprintf(os.Stderr, "  remove, rm <sandbox> <remote-path>\n")
	fmt.Fprintf(os.Stderr, "      Remove a file or directory tree inside the sandbox.\n")
}

func handleFileRead(ctx context.Context, client *DaemonClient, args []string) {
	fs := flag.NewFlagSet("file read", flag.ExitOnError)
	format := fs.String("format", "", "Output format: 'text' or 'base64'")
	flags, pos := splitFlagAndPositionalArgs(args, map[string]bool{"format": true})
	_ = fs.Parse(flags)

	if len(pos) < 2 {
		printCommandError("missing required arguments for 'file read'.", "Usage: compute sandbox file read <sandbox> <remote-path> [--format text|base64]", "file")
	}

	sandboxRef := pos[0]
	remotePath := pos[1]

	v, err := resolveSandboxReference(ctx, client, sandboxRef)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving sandbox %q: %v\n", sandboxRef, err)
		os.Exit(1)
	}
	if v.Status != "running" {
		fmt.Fprintf(os.Stderr, "Sandbox %q is not running (status: %s). Start it with: compute sandbox start %s\n", v.Name, v.Status, v.Name)
		os.Exit(1)
	}

	res, err := client.ReadFileDetails(ctx, v.ID, remotePath, *format)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading file %s:%s: %v\n", v.Name, remotePath, err)
		os.Exit(1)
	}

	if *format == "base64" {
		fmt.Println(res.Content)
		return
	}

	if res.Format == "base64" {
		decoded, err := base64.StdEncoding.DecodeString(res.Content)
		if err != nil {
			fmt.Print(res.Content)
			return
		}
		os.Stdout.Write(decoded)
		return
	}

	fmt.Print(res.Content)
}

func handleFileWrite(ctx context.Context, client *DaemonClient, args []string) {
	fs := flag.NewFlagSet("file write", flag.ExitOnError)
	content := fs.String("content", "", "Inline string content to write (use '-' to read from stdin)")
	filePath := fs.String("file", "", "Local file path to read content from")
	format := fs.String("format", "", "Data format: 'text' or 'base64'")
	flags, pos := splitFlagAndPositionalArgs(args, map[string]bool{"content": true, "file": true, "format": true})
	_ = fs.Parse(flags)

	contentSet := false
	fileSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "content" {
			contentSet = true
		}
		if f.Name == "file" {
			fileSet = true
		}
	})

	if len(pos) < 2 {
		printCommandError("missing required arguments for 'file write'.", "Usage: compute sandbox file write <sandbox> <remote-path> [--content <data> | --file <path> | -]", "file")
	}

	sandboxRef := pos[0]
	remotePath := pos[1]

	v, err := resolveSandboxReference(ctx, client, sandboxRef)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving sandbox %q: %v\n", sandboxRef, err)
		os.Exit(1)
	}
	if v.Status != "running" {
		fmt.Fprintf(os.Stderr, "Sandbox %q is not running (status: %s). Start it with: compute sandbox start %s\n", v.Name, v.Status, v.Name)
		os.Exit(1)
	}

	var data []byte
	if fileSet && *filePath != "" {
		d, err := os.ReadFile(*filePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading local file %q: %v\n", *filePath, err)
			os.Exit(1)
		}
		data = d
	} else if contentSet {
		if *content == "-" {
			d, err := io.ReadAll(os.Stdin)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error reading from stdin: %v\n", err)
				os.Exit(1)
			}
			data = d
		} else {
			data = []byte(*content)
		}
	} else {
		// Read from stdin by default
		d, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading from stdin: %v\n", err)
			os.Exit(1)
		}
		data = d
	}

	writeFormat := *format
	var payloadContent string
	if writeFormat == "base64" || (!utf8.Valid(data) && writeFormat != "text") {
		writeFormat = "base64"
		payloadContent = base64.StdEncoding.EncodeToString(data)
	} else {
		payloadContent = string(data)
	}

	if err := client.WriteFile(ctx, v.ID, remotePath, payloadContent, writeFormat); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing file %s:%s: %v\n", v.Name, remotePath, err)
		os.Exit(1)
	}

	fmt.Printf("Successfully wrote %d bytes to %s:%s\n", len(data), v.Name, remotePath)
}

func handleFileList(ctx context.Context, client *DaemonClient, args []string) {
	_, pos := splitFlagAndPositionalArgs(args, nil)
	if len(pos) < 1 {
		printCommandError("missing required sandbox argument for 'file list'.", "Usage: compute sandbox file list <sandbox> [remote-path]", "file")
	}

	sandboxRef := pos[0]
	remotePath := "/workspace"
	if len(pos) > 1 {
		remotePath = pos[1]
	}

	v, err := resolveSandboxReference(ctx, client, sandboxRef)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving sandbox %q: %v\n", sandboxRef, err)
		os.Exit(1)
	}
	if v.Status != "running" {
		fmt.Fprintf(os.Stderr, "Sandbox %q is not running (status: %s). Start it with: compute sandbox start %s\n", v.Name, v.Status, v.Name)
		os.Exit(1)
	}

	entries, err := client.ListFilesDetails(ctx, v.ID, remotePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing files in %s:%s: %v\n", v.Name, remotePath, err)
		os.Exit(1)
	}

	if len(entries) == 0 {
		fmt.Printf("Directory %s:%s is empty.\n", v.Name, remotePath)
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tTYPE\tSIZE\tPATH")
	for _, e := range entries {
		t := "FILE"
		if e.IsDir {
			t = "DIR"
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", e.Name, t, e.Size, e.Path)
	}
	w.Flush()
}

func handleFileMkdir(ctx context.Context, client *DaemonClient, args []string) {
	_, pos := splitFlagAndPositionalArgs(args, nil)
	if len(pos) < 2 {
		printCommandError("missing required arguments for 'file mkdir'.", "Usage: compute sandbox file mkdir <sandbox> <remote-path>", "file")
	}

	sandboxRef := pos[0]
	remotePath := pos[1]

	v, err := resolveSandboxReference(ctx, client, sandboxRef)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving sandbox %q: %v\n", sandboxRef, err)
		os.Exit(1)
	}
	if v.Status != "running" {
		fmt.Fprintf(os.Stderr, "Sandbox %q is not running (status: %s). Start it with: compute sandbox start %s\n", v.Name, v.Status, v.Name)
		os.Exit(1)
	}

	if err := client.MkdirFile(ctx, v.ID, remotePath); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating directory %s:%s: %v\n", v.Name, remotePath, err)
		os.Exit(1)
	}

	fmt.Printf("Directory created: %s:%s\n", v.Name, remotePath)
}

func handleFileRemove(ctx context.Context, client *DaemonClient, args []string) {
	_, pos := splitFlagAndPositionalArgs(args, nil)
	if len(pos) < 2 {
		printCommandError("missing required arguments for 'file remove'.", "Usage: compute sandbox file remove <sandbox> <remote-path>", "file")
	}

	sandboxRef := pos[0]
	remotePath := pos[1]

	v, err := resolveSandboxReference(ctx, client, sandboxRef)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving sandbox %q: %v\n", sandboxRef, err)
		os.Exit(1)
	}
	if v.Status != "running" {
		fmt.Fprintf(os.Stderr, "Sandbox %q is not running (status: %s). Start it with: compute sandbox start %s\n", v.Name, v.Status, v.Name)
		os.Exit(1)
	}

	if err := client.RemoveFile(ctx, v.ID, remotePath); err != nil {
		fmt.Fprintf(os.Stderr, "Error removing %s:%s: %v\n", v.Name, remotePath, err)
		os.Exit(1)
	}

	fmt.Printf("Removed: %s:%s\n", v.Name, remotePath)
}

// splitRemoteRef checks if a path refers to a remote sandbox ("<sandbox>:<path>").
func splitRemoteRef(target string) (sandbox string, remotePath string, isRemote bool) {
	if strings.Contains(target, ":") {
		parts := strings.SplitN(target, ":", 2)
		// Avoid interpreting Windows drive letters (e.g. C:\path) as remote
		if len(parts[0]) == 1 && ((parts[0][0] >= 'a' && parts[0][0] <= 'z') || (parts[0][0] >= 'A' && parts[0][0] <= 'Z')) {
			return "", target, false
		}
		// Avoid interpreting local paths with path separators in prefix (e.g. ./logs/2026-11-13:00.log) as remote
		if strings.ContainsAny(parts[0], "/\\") {
			return "", target, false
		}
		return parts[0], parts[1], true
	}
	return "", target, false
}

// handleSandboxCpCommand implements `compute sandbox cp <src> <dst>` or `compute cp <src> <dst>`.
func handleSandboxCpCommand(ctx context.Context, client *DaemonClient, args []string) {
	if printHelpIfRequested("cp", args) || len(args) < 2 {
		printCpUsage()
		if len(args) == 0 || (len(args) > 0 && (args[0] == "-h" || args[0] == "--help")) {
			return
		}
		os.Exit(1)
	}

	src := args[0]
	dst := args[1]

	srcBox, srcPath, srcRemote := splitRemoteRef(src)
	dstBox, dstPath, dstRemote := splitRemoteRef(dst)

	if !srcRemote && !dstRemote {
		printCommandError("neither source nor destination specifies a sandbox.", "Usage: compute sandbox cp <sandbox>:<remote-path> <local-path> OR compute sandbox cp <local-path> <sandbox>:<remote-path>", "cp")
	}

	// Remote to Remote copy
	if srcRemote && dstRemote {
		srcVM, err := resolveSandboxReference(ctx, client, srcBox)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving source sandbox %q: %v\n", srcBox, err)
			os.Exit(1)
		}
		dstVM, err := resolveSandboxReference(ctx, client, dstBox)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving destination sandbox %q: %v\n", dstBox, err)
			os.Exit(1)
		}

		res, err := client.ReadFileDetails(ctx, srcVM.ID, srcPath, "base64")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading from source %s:%s: %v\n", srcVM.Name, srcPath, err)
			os.Exit(1)
		}
		targetPath := dstPath
		if strings.HasSuffix(targetPath, "/") {
			targetPath = path.Join(targetPath, path.Base(srcPath))
		}
		if err := client.WriteFile(ctx, dstVM.ID, targetPath, res.Content, "base64"); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing to destination %s:%s: %v\n", dstVM.Name, targetPath, err)
			os.Exit(1)
		}
		fmt.Printf("Copied %s:%s to %s:%s (%d bytes)\n", srcVM.Name, srcPath, dstVM.Name, targetPath, res.Size)
		return
	}

	// Remote to Local copy
	if srcRemote && !dstRemote {
		srcVM, err := resolveSandboxReference(ctx, client, srcBox)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving sandbox %q: %v\n", srcBox, err)
			os.Exit(1)
		}
		if srcVM.Status != "running" {
			fmt.Fprintf(os.Stderr, "Sandbox %q is not running (status: %s). Start it with: compute sandbox start %s\n", srcVM.Name, srcVM.Status, srcVM.Name)
			os.Exit(1)
		}

		res, err := client.ReadFileDetails(ctx, srcVM.ID, srcPath, "base64")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading remote file %s:%s: %v\n", srcVM.Name, srcPath, err)
			os.Exit(1)
		}

		decoded, err := base64.StdEncoding.DecodeString(res.Content)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error decoding file payload: %v\n", err)
			os.Exit(1)
		}

		localTarget := dstPath
		if fi, err := os.Stat(localTarget); err == nil && fi.IsDir() {
			localTarget = filepath.Join(localTarget, path.Base(srcPath))
		} else if strings.HasSuffix(localTarget, "/") || strings.HasSuffix(localTarget, "\\") {
			_ = os.MkdirAll(localTarget, 0755)
			localTarget = filepath.Join(localTarget, path.Base(srcPath))
		}

		if err := os.WriteFile(localTarget, decoded, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing local file %q: %v\n", localTarget, err)
			os.Exit(1)
		}

		fmt.Printf("Copied %s:%s to %s (%d bytes)\n", srcVM.Name, srcPath, localTarget, len(decoded))
		return
	}

	// Local to Remote copy
	if !srcRemote && dstRemote {
		localBytes, err := os.ReadFile(srcPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading local source %q: %v\n", srcPath, err)
			os.Exit(1)
		}

		dstVM, err := resolveSandboxReference(ctx, client, dstBox)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving sandbox %q: %v\n", dstBox, err)
			os.Exit(1)
		}
		if dstVM.Status != "running" {
			fmt.Fprintf(os.Stderr, "Sandbox %q is not running (status: %s). Start it with: compute sandbox start %s\n", dstVM.Name, dstVM.Status, dstVM.Name)
			os.Exit(1)
		}

		remoteTarget := dstPath
		if strings.HasSuffix(remoteTarget, "/") {
			remoteTarget = path.Join(remoteTarget, filepath.Base(srcPath))
		}

		b64Content := base64.StdEncoding.EncodeToString(localBytes)
		if err := client.WriteFile(ctx, dstVM.ID, remoteTarget, b64Content, "base64"); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing remote file %s:%s: %v\n", dstVM.Name, remoteTarget, err)
			os.Exit(1)
		}

		fmt.Printf("Copied %s to %s:%s (%d bytes)\n", srcPath, dstVM.Name, remoteTarget, len(localBytes))
		return
	}
}

func printCpUsage() {
	fmt.Fprintf(os.Stderr, "Usage: compute sandbox cp <src> <dst>\n\n")
	fmt.Fprintf(os.Stderr, "Copy files between local filesystem and sandboxes via fast daemon REST APIs.\n\n")
	fmt.Fprintf(os.Stderr, "Examples:\n")
	fmt.Fprintf(os.Stderr, "  compute sandbox cp ./app.py my-box:/workspace/app.py\n")
	fmt.Fprintf(os.Stderr, "  compute sandbox cp my-box:/workspace/results.json ./results.json\n")
	fmt.Fprintf(os.Stderr, "  compute sandbox cp box-1:/workspace/data.bin box-2:/workspace/data.bin\n")
}

// handleSandboxProcessCommand implements `compute sandbox process [list|start|kill]`.
func handleSandboxProcessCommand(ctx context.Context, client *DaemonClient, args []string) {
	if printHelpIfRequested("process", args) || len(args) == 0 {
		printProcessUsage()
		return
	}

	action := args[0]
	subArgs := args[1:]

	switch action {
	case "list", "ls", "ps":
		handleProcessList(ctx, client, subArgs)
	case "start", "run":
		handleProcessStart(ctx, client, subArgs)
	case "kill", "signal":
		handleProcessKill(ctx, client, subArgs)
	default:
		printCommandError(fmt.Sprintf("unknown process action '%s'.", action), "Available actions: list, start, kill", "process")
	}
}

func printProcessUsage() {
	fmt.Fprintf(os.Stderr, "Usage: compute sandbox process <action> <sandbox> [args...]\n\n")
	fmt.Fprintf(os.Stderr, "Inspect and manage background processes inside running sandboxes.\n\n")
	fmt.Fprintf(os.Stderr, "Actions:\n")
	fmt.Fprintf(os.Stderr, "  list, ls, ps <sandbox>\n")
	fmt.Fprintf(os.Stderr, "      List running processes inside the sandbox.\n")
	fmt.Fprintf(os.Stderr, "  start <sandbox> [--cwd <path>] [--env KEY=VAL] [--] <command...>\n")
	fmt.Fprintf(os.Stderr, "      Launch a detached background process inside the sandbox.\n")
	fmt.Fprintf(os.Stderr, "  kill <sandbox> <pid> [--signal SIGTERM|SIGKILL]\n")
	fmt.Fprintf(os.Stderr, "      Send a signal to terminate a process inside the sandbox.\n")
}

func handleProcessList(ctx context.Context, client *DaemonClient, args []string) {
	_, pos := splitFlagAndPositionalArgs(args, nil)
	if len(pos) < 1 {
		printCommandError("missing required sandbox argument for 'process list'.", "Usage: compute sandbox process list <sandbox>", "process")
	}

	sandboxRef := pos[0]
	v, err := resolveSandboxReference(ctx, client, sandboxRef)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving sandbox %q: %v\n", sandboxRef, err)
		os.Exit(1)
	}
	if v.Status != "running" {
		fmt.Fprintf(os.Stderr, "Sandbox %q is not running (status: %s). Start it with: compute sandbox start %s\n", v.Name, v.Status, v.Name)
		os.Exit(1)
	}

	processes, err := client.ListProcessesDetails(ctx, v.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing processes in %s: %v\n", v.Name, err)
		os.Exit(1)
	}

	if len(processes) == 0 {
		fmt.Printf("No running processes reported in sandbox %s.\n", v.Name)
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
	fmt.Fprintln(w, "PID\tPPID\tUSER\t%CPU\t%MEM\tSTAT\tCOMMAND")
	for _, p := range processes {
		cmdStr := p.Command
		if p.Args != "" && p.Args != p.Command {
			cmdStr = p.Args
		}
		fmt.Fprintf(w, "%d\t%d\t%s\t%.1f\t%.1f\t%s\t%s\n",
			p.PID, p.PPID, p.User, p.CPU, p.Memory, p.Status, cmdStr)
	}
	w.Flush()
}

func handleProcessStart(ctx context.Context, client *DaemonClient, args []string) {
	if len(args) < 2 {
		printCommandError("missing arguments for 'process start'.", "Usage: compute sandbox process start <sandbox> [--cwd <path>] [--env KEY=VAL] [--] <command...>", "process")
	}

	sandboxRef := args[0]
	remaining := args[1:]

	var cwd string
	env := make(map[string]string)
	var cmdTokens []string

	for i := 0; i < len(remaining); i++ {
		tok := remaining[i]
		if tok == "--" {
			cmdTokens = append(cmdTokens, remaining[i+1:]...)
			break
		}
		if tok == "--cwd" && i+1 < len(remaining) {
			cwd = remaining[i+1]
			i++
			continue
		}
		if strings.HasPrefix(tok, "--cwd=") {
			cwd = strings.TrimPrefix(tok, "--cwd=")
			continue
		}
		if (tok == "--env" || tok == "-e") && i+1 < len(remaining) {
			pair := remaining[i+1]
			if parts := strings.SplitN(pair, "=", 2); len(parts) == 2 {
				env[parts[0]] = parts[1]
			}
			i++
			continue
		}
		if strings.HasPrefix(tok, "--env=") {
			pair := strings.TrimPrefix(tok, "--env=")
			if parts := strings.SplitN(pair, "=", 2); len(parts) == 2 {
				env[parts[0]] = parts[1]
			}
			continue
		}
		cmdTokens = append(cmdTokens, tok)
	}

	if len(cmdTokens) == 0 {
		printCommandError("no command specified to run in process start.", "Usage: compute sandbox process start <sandbox> [--cwd <path>] [--] <command...>", "process")
	}

	fullCommand := buildExecCommand(cmdTokens)

	v, err := resolveSandboxReference(ctx, client, sandboxRef)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving sandbox %q: %v\n", sandboxRef, err)
		os.Exit(1)
	}
	if v.Status != "running" {
		fmt.Fprintf(os.Stderr, "Sandbox %q is not running (status: %s). Start it with: compute sandbox start %s\n", v.Name, v.Status, v.Name)
		os.Exit(1)
	}

	pid, err := client.StartProcess(ctx, v.ID, fullCommand, cwd, env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error starting process in %s: %v\n", v.Name, err)
		os.Exit(1)
	}

	fmt.Printf("Process started: PID=%d Command=%q Sandbox=%s\n", pid, fullCommand, v.Name)
}

func handleProcessKill(ctx context.Context, client *DaemonClient, args []string) {
	if len(args) < 2 {
		printCommandError("missing arguments for 'process kill'.", "Usage: compute sandbox process kill <sandbox> <pid> [--signal SIGTERM|SIGKILL]", "process")
	}

	sandboxRef := args[0]
	remaining := args[1:]

	signal := "SIGTERM"
	var pidStr string

	for i := 0; i < len(remaining); i++ {
		tok := remaining[i]
		if (tok == "--signal" || tok == "-s") && i+1 < len(remaining) {
			signal = remaining[i+1]
			i++
			continue
		}
		if strings.HasPrefix(tok, "--signal=") {
			signal = strings.TrimPrefix(tok, "--signal=")
			continue
		}
		if pidStr == "" && !strings.HasPrefix(tok, "-") {
			pidStr = tok
		}
	}

	if pidStr == "" {
		printCommandError("missing PID argument for 'process kill'.", "Usage: compute sandbox process kill <sandbox> <pid> [--signal SIGTERM|SIGKILL]", "process")
	}

	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 0 {
		fmt.Fprintf(os.Stderr, "Invalid PID %q: must be a positive integer\n", pidStr)
		os.Exit(1)
	}

	v, err := resolveSandboxReference(ctx, client, sandboxRef)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving sandbox %q: %v\n", sandboxRef, err)
		os.Exit(1)
	}
	if v.Status != "running" {
		fmt.Fprintf(os.Stderr, "Sandbox %q is not running (status: %s). Start it with: compute sandbox start %s\n", v.Name, v.Status, v.Name)
		os.Exit(1)
	}

	if err := client.KillProcess(ctx, v.ID, pid, signal); err != nil {
		fmt.Fprintf(os.Stderr, "Error killing process %d in %s: %v\n", pid, v.Name, err)
		os.Exit(1)
	}

	fmt.Printf("Signal %s sent to process PID %d in sandbox %s\n", signal, pid, v.Name)
}

func splitFlagAndPositionalArgs(args []string, valueFlags map[string]bool) (flags, positional []string) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			flags = append(flags, arg)
			if strings.Contains(arg, "=") {
				continue
			}
			flagName := strings.TrimLeft(arg, "-")
			if valueFlags != nil && (valueFlags[flagName] || valueFlags[arg]) {
				if i+1 < len(args) && (!strings.HasPrefix(args[i+1], "-") || args[i+1] == "-") {
					flags = append(flags, args[i+1])
					i++
				}
			}
		} else {
			positional = append(positional, arg)
		}
	}
	return flags, positional
}
