package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Rashid2G/compute/internal/vm"
)

// scpTimeoutSeconds bounds each per-operation remote command (tar, extract,
// test -d). Directory transfers of large trees may legitimately take minutes.
const scpExecTimeoutSeconds = 600

// handleSandboxScpCommand implements `compute scp <src>... <dst>` with
// scp-style local<->sandbox file transfers.
func handleSandboxScpCommand(_ context.Context, client *DaemonClient, args []string) {
	if printHelpIfRequested("scp", args) || len(args) == 0 {
		printScpUsage()
		if len(args) == 0 || (len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help")) {
			return
		}
		os.Exit(1)
	}

	srcs, dst, recursive, err := parseScpArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		printScpUsage()
		os.Exit(1)
	}

	// scp transfers can outlive the default 60s CLI request budget, so run on
	// a fresh context that is only cancelled by Ctrl-C / SIGTERM.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := runScp(ctx, client, srcs, dst, recursive); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func printScpUsage() {
	fmt.Fprintf(os.Stderr, "Usage: compute scp [-r] <src> [<src>...] <dst>\n\n")
	fmt.Fprintf(os.Stderr, "Copy files and directories between the local host and sandboxes (scp-style).\n")
	fmt.Fprintf(os.Stderr, "Remote paths are written as <sandbox>:<path>; local paths as plain paths.\n\n")
	fmt.Fprintf(os.Stderr, "Flags:\n")
	fmt.Fprintf(os.Stderr, "  -r, --recursive    Recursively copy directories (required for directory sources)\n\n")
	fmt.Fprintf(os.Stderr, "Examples:\n")
	fmt.Fprintf(os.Stderr, "  compute scp ./file trigger:/home/\n")
	fmt.Fprintf(os.Stderr, "  compute scp trigger:/home/file ./\n")
	fmt.Fprintf(os.Stderr, "  compute scp -r ./mydir trigger:/home/\n")
	fmt.Fprintf(os.Stderr, "  compute scp -r trigger:/home/logs ./\n")
	fmt.Fprintf(os.Stderr, "  compute scp a.txt b.txt trigger:/tmp/\n")
}

// parseScpArgs splits scp arguments into sources, destination, and the
// recursive flag. The last positional argument is the destination.
func parseScpArgs(args []string) (srcs []string, dst string, recursive bool, err error) {
	var positional []string
	flagParsingDone := false
	for _, arg := range args {
		if !flagParsingDone && arg == "--" {
			flagParsingDone = true
			continue
		}
		if !flagParsingDone && strings.HasPrefix(arg, "-") && arg != "-" {
			switch arg {
			case "-r", "-R", "--recursive":
				recursive = true
			default:
				return nil, "", false, fmt.Errorf("unsupported flag %q", arg)
			}
			continue
		}
		positional = append(positional, arg)
	}

	if len(positional) < 2 {
		return nil, "", false, fmt.Errorf("scp requires at least a source and a destination")
	}

	return positional[:len(positional)-1], positional[len(positional)-1], recursive, nil
}

// runScp performs the copy. It is separated from the command handler so tests
// can exercise it without os.Exit.
func runScp(ctx context.Context, client *DaemonClient, srcs []string, dst string, recursive bool) error {
	dstBox, dstPath, dstRemote := splitRemoteRef(dst)
	if dstRemote {
		dstBox = stripUserPrefix(dstBox)
	}

	// Neither side is remote.
	if !dstRemote {
		allLocal := true
		for _, src := range srcs {
			if _, _, remote := splitRemoteRef(src); remote {
				allLocal = false
				break
			}
		}
		if allLocal {
			return fmt.Errorf("neither source nor destination specifies a sandbox (use <sandbox>:<path>)")
		}
		return scpPull(ctx, client, srcs, dstPath, recursive)
	}

	dstVM, err := resolveScpSandbox(ctx, client, dstBox)
	if err != nil {
		return fmt.Errorf("resolving destination sandbox %q: %w", dstBox, err)
	}
	if dstVM.Status != "running" {
		return fmt.Errorf("sandbox %q is not running (status: %s). Start it with: compute sandbox start %s", dstVM.Name, dstVM.Status, dstVM.Name)
	}

	dstIsDir := strings.HasSuffix(dstPath, "/")
	if !dstIsDir && len(srcs) > 1 {
		dstIsDir, err = remotePathIsDir(ctx, client, dstVM.ID, dstPath)
		if err != nil {
			return fmt.Errorf("checking destination %s:%s: %w", dstVM.Name, dstPath, err)
		}
	}
	if len(srcs) > 1 && !dstIsDir {
		return fmt.Errorf("destination %s:%s is not a directory (multiple sources require a directory target)", dstVM.Name, dstPath)
	}

	// Multiple sources: push/pull each one into the destination directory.
	if len(srcs) > 1 {
		for _, src := range srcs {
			if err := scpOne(ctx, client, src, dstVM, dstPath, true, recursive); err != nil {
				return err
			}
		}
		return nil
	}

	return scpOne(ctx, client, srcs[0], dstVM, dstPath, dstIsDir, recursive)
}

// scpOne copies a single source to an already-resolved remote destination.
// dstIsDir reports whether the destination resolves to a directory (or the
// caller already knows multiple sources land in one).
func scpOne(ctx context.Context, client *DaemonClient, src string, dstVM *vmRef, dstPath string, dstIsDir, recursive bool) error {
	srcBox, srcPath, srcRemote := splitRemoteRef(src)

	// Local -> remote
	if !srcRemote {
		info, err := os.Stat(srcPath)
		if err != nil {
			return fmt.Errorf("reading local source %q: %w", srcPath, err)
		}
		if info.IsDir() {
			if !recursive {
				return fmt.Errorf("%q is a directory (use -r to copy directories)", srcPath)
			}
			return scpPushDir(ctx, client, srcPath, dstVM, dstPath, dstIsDir)
		}
		return scpPushFile(ctx, client, srcPath, dstVM, dstPath, dstIsDir)
	}

	// Remote -> remote or remote -> local
	srcBox = stripUserPrefix(srcBox)
	srcVM, err := resolveScpSandbox(ctx, client, srcBox)
	if err != nil {
		return fmt.Errorf("resolving source sandbox %q: %w", srcBox, err)
	}
	if srcVM.Status != "running" {
		return fmt.Errorf("sandbox %q is not running (status: %s). Start it with: compute sandbox start %s", srcVM.Name, srcVM.Status, srcVM.Name)
	}

	if srcVM.ID == dstVM.ID {
		return fmt.Errorf("source and destination sandbox are the same (%s)", srcVM.Name)
	}

	// A trailing slash on the source path means "copy the contents of the
	// directory" rather than the directory itself (scp semantics).
	srcContents := strings.HasSuffix(srcPath, "/")
	srcPath = strings.TrimRight(srcPath, "/")

	srcIsDir, err := remotePathIsDir(ctx, client, srcVM.ID, srcPath)
	if err != nil {
		return fmt.Errorf("checking source %s:%s: %w", srcVM.Name, srcPath, err)
	}
	if srcIsDir && !recursive {
		return fmt.Errorf("%s:%s is a directory (use -r to copy directories)", srcVM.Name, srcPath)
	}

	if srcIsDir {
		return scpCopyDir(ctx, client, srcVM, srcPath, srcContents, dstVM, dstPath, dstIsDir, false)
	}
	return scpCopyFile(ctx, client, srcVM, srcPath, dstVM, dstPath, dstIsDir)
}

// scpPull copies remote sources to a local destination path.
func scpPull(ctx context.Context, client *DaemonClient, srcs []string, dstPath string, recursive bool) error {
	localIsDir := false
	if fi, err := os.Stat(dstPath); err == nil && fi.IsDir() {
		localIsDir = true
	}
	if len(srcs) > 1 && !localIsDir && !strings.HasSuffix(dstPath, "/") {
		return fmt.Errorf("destination %q is not a directory (multiple sources require a directory target)", dstPath)
	}
	if strings.HasSuffix(dstPath, "/") {
		if err := os.MkdirAll(dstPath, 0o755); err != nil {
			return fmt.Errorf("creating local destination %q: %w", dstPath, err)
		}
		localIsDir = true
		dstPath = strings.TrimRight(dstPath, "/")
	}

	for _, src := range srcs {
		srcBox, srcPath, srcRemote := splitRemoteRef(src)
		if !srcRemote {
			return fmt.Errorf("neither source nor destination specifies a sandbox (use <sandbox>:<path>)")
		}
		srcBox = stripUserPrefix(srcBox)
		srcVM, err := resolveScpSandbox(ctx, client, srcBox)
		if err != nil {
			return fmt.Errorf("resolving source sandbox %q: %w", srcBox, err)
		}
		if srcVM.Status != "running" {
			return fmt.Errorf("sandbox %q is not running (status: %s). Start it with: compute sandbox start %s", srcVM.Name, srcVM.Status, srcVM.Name)
		}

		// A trailing slash on the source path means "copy the directory
		// contents" rather than the directory itself (scp semantics).
		srcContents := strings.HasSuffix(srcPath, "/")
		srcPath = strings.TrimRight(srcPath, "/")

		srcIsDir, err := remotePathIsDir(ctx, client, srcVM.ID, srcPath)
		if err != nil {
			return fmt.Errorf("checking source %s:%s: %w", srcVM.Name, srcPath, err)
		}
		if srcIsDir {
			if !recursive {
				return fmt.Errorf("%s:%s is a directory (use -r to copy directories)", srcVM.Name, srcPath)
			}
			if err := scpCopyDir(ctx, client, srcVM, srcPath, srcContents, nil, dstPath, localIsDir, true); err != nil {
				return err
			}
			continue
		}

		res, err := client.ReadFileDetails(ctx, srcVM.ID, srcPath, "base64")
		if err != nil {
			return fmt.Errorf("reading %s:%s: %w", srcVM.Name, srcPath, err)
		}
		decoded, err := base64.StdEncoding.DecodeString(res.Content)
		if err != nil {
			return fmt.Errorf("decoding %s:%s: %w", srcVM.Name, srcPath, err)
		}

		localTarget := dstPath
		if localIsDir {
			localTarget = filepath.Join(dstPath, path.Base(srcPath))
		}
		if err := os.WriteFile(localTarget, decoded, 0o644); err != nil {
			return fmt.Errorf("writing local file %q: %w", localTarget, err)
		}
		fmt.Printf("Copied %s:%s to %s (%d bytes)\n", srcVM.Name, srcPath, localTarget, len(decoded))
	}
	return nil
}

// scpPushFile copies one local file to the remote destination.
func scpPushFile(ctx context.Context, client *DaemonClient, srcPath string, dstVM *vmRef, dstPath string, dstIsDir bool) error {
	// scp semantics: an existing remote directory target receives the file
	// under its own name.
	if !dstIsDir {
		var err error
		dstIsDir, err = remotePathIsDir(ctx, client, dstVM.ID, dstPath)
		if err != nil {
			return fmt.Errorf("checking destination %s:%s: %w", dstVM.Name, dstPath, err)
		}
	}
	remoteTarget := dstPath
	if dstIsDir {
		remoteTarget = path.Join(dstPath, filepath.Base(srcPath))
	}

	data, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("reading local source %q: %w", srcPath, err)
	}

	b64 := base64.StdEncoding.EncodeToString(data)
	if err := client.WriteFile(ctx, dstVM.ID, remoteTarget, b64, "base64"); err != nil {
		return fmt.Errorf("writing %s:%s: %w", dstVM.Name, remoteTarget, err)
	}
	fmt.Printf("Copied %s to %s:%s (%d bytes)\n", srcPath, dstVM.Name, remoteTarget, len(data))
	return nil
}

// scpPushDir copies a local directory tree into the remote destination using a
// base64 tar.gz staged in the guest's temp directory.
func scpPushDir(ctx context.Context, client *DaemonClient, srcPath string, dstVM *vmRef, dstPath string, dstIsDir bool) error {
	// scp semantics: copying into an existing/implicit directory places the
	// source inside it under its own name; otherwise the destination itself
	// becomes the copy.
	if !dstIsDir {
		var err error
		dstIsDir, err = remotePathIsDir(ctx, client, dstVM.ID, dstPath)
		if err != nil {
			return fmt.Errorf("checking destination %s:%s: %w", dstVM.Name, dstPath, err)
		}
	}
	copyInto := dstPath
	if dstIsDir {
		copyInto = path.Join(dstPath, filepath.Base(srcPath))
	}

	tarB64, err := packageFilesToTarGzBase64(srcPath)
	if err != nil {
		return fmt.Errorf("packaging %q: %w", srcPath, err)
	}

	tmp := scpTmpPath("tgz")
	if err := client.WriteFile(ctx, dstVM.ID, tmp, tarB64, "base64"); err != nil {
		return fmt.Errorf("staging archive on %s: %w", dstVM.Name, err)
	}

	cmd := fmt.Sprintf("mkdir -p %q && tar -xzf %q -C %q && rm -f %q", copyInto, tmp, copyInto, tmp)
	res, err := client.ExecVM(ctx, dstVM.ID, vm.ExecRequest{Command: cmd, Timeout: scpExecTimeoutSeconds})
	if err != nil {
		return fmt.Errorf("extracting archive on %s: %w", dstVM.Name, err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("extracting archive on %s: %s", dstVM.Name, strings.TrimSpace(res.Stderr))
	}

	fmt.Printf("Copied directory %s to %s:%s\n", srcPath, dstVM.Name, copyInto)
	return nil
}

// scpCopyFile copies a file between two remote sandboxes.
func scpCopyFile(ctx context.Context, client *DaemonClient, srcVM *vmRef, srcPath string, dstVM *vmRef, dstPath string, dstIsDir bool) error {
	res, err := client.ReadFileDetails(ctx, srcVM.ID, srcPath, "base64")
	if err != nil {
		return fmt.Errorf("reading %s:%s: %w", srcVM.Name, srcPath, err)
	}
	// scp semantics: an existing remote directory target receives the file
	// under its own name.
	if !dstIsDir {
		dstIsDir, err = remotePathIsDir(ctx, client, dstVM.ID, dstPath)
		if err != nil {
			return fmt.Errorf("checking destination %s:%s: %w", dstVM.Name, dstPath, err)
		}
	}
	remoteTarget := dstPath
	if dstIsDir {
		remoteTarget = path.Join(dstPath, path.Base(srcPath))
	}
	if err := client.WriteFile(ctx, dstVM.ID, remoteTarget, res.Content, "base64"); err != nil {
		return fmt.Errorf("writing %s:%s: %w", dstVM.Name, remoteTarget, err)
	}
	fmt.Printf("Copied %s:%s to %s:%s (%d bytes)\n", srcVM.Name, srcPath, dstVM.Name, remoteTarget, res.Size)
	return nil
}

// scpCopyDir copies a directory between remote sandboxes, or from a remote
// sandbox to the local host, by shipping a base64 tar.gz of the tree.
func scpCopyDir(ctx context.Context, client *DaemonClient, srcVM *vmRef, srcPath string, srcContents bool, dstVM *vmRef, dstPath string, dstIsDir, dstLocal bool) error {
	// Produce a base64-encoded tar.gz of the source tree on the source
	// sandbox. The temp-file hop keeps exit-code detection accurate (a plain
	// pipe would report the base64 stage's status, not tar's).
	tmp := scpTmpPath("tgz")
	packCmd := fmt.Sprintf("tar -czf %q -C %q . && base64 %q && rm -f %q", tmp, srcPath, tmp, tmp)
	res, err := client.ExecVM(ctx, srcVM.ID, vm.ExecRequest{Command: packCmd, Timeout: scpExecTimeoutSeconds})
	if err != nil {
		return fmt.Errorf("packing %s:%s: %w", srcVM.Name, srcPath, err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("packing %s:%s: %s", srcVM.Name, srcPath, strings.TrimSpace(res.Stderr))
	}

	tarB64 := stripBase64Whitespace(res.Stdout)
	if tarB64 == "" {
		return fmt.Errorf("packing %s:%s: no archive data returned", srcVM.Name, srcPath)
	}

	if dstLocal {
		localTarget := dstPath
		if dstIsDir && !srcContents {
			localTarget = filepath.Join(dstPath, path.Base(srcPath))
		}
		if err := extractTarGzBase64(tarB64, localTarget); err != nil {
			return fmt.Errorf("extracting %s:%s to %q: %w", srcVM.Name, srcPath, localTarget, err)
		}
		fmt.Printf("Copied directory %s:%s to %s\n", srcVM.Name, srcPath, localTarget)
		return nil
	}

	// Remote -> remote: stage the archive on the destination and extract.
	if !dstIsDir {
		dstIsDir, err = remotePathIsDir(ctx, client, dstVM.ID, dstPath)
		if err != nil {
			return fmt.Errorf("checking destination %s:%s: %w", dstVM.Name, dstPath, err)
		}
	}
	copyInto := dstPath
	if dstIsDir && !srcContents {
		copyInto = path.Join(dstPath, path.Base(srcPath))
	}

	tmpDst := scpTmpPath("tgz")
	if err := client.WriteFile(ctx, dstVM.ID, tmpDst, tarB64, "base64"); err != nil {
		return fmt.Errorf("staging archive on %s: %w", dstVM.Name, err)
	}
	extractCmd := fmt.Sprintf("mkdir -p %q && tar -xzf %q -C %q && rm -f %q", copyInto, tmpDst, copyInto, tmpDst)
	res, err = client.ExecVM(ctx, dstVM.ID, vm.ExecRequest{Command: extractCmd, Timeout: scpExecTimeoutSeconds})
	if err != nil {
		return fmt.Errorf("extracting archive on %s: %w", dstVM.Name, err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("extracting archive on %s: %s", dstVM.Name, strings.TrimSpace(res.Stderr))
	}
	fmt.Printf("Copied directory %s:%s to %s:%s\n", srcVM.Name, srcPath, dstVM.Name, copyInto)
	return nil
}

// remotePathIsDir reports whether a path inside a sandbox is a directory.
func remotePathIsDir(ctx context.Context, client *DaemonClient, sandboxID, remotePath string) (bool, error) {
	res, err := client.ExecVM(ctx, sandboxID, vm.ExecRequest{
		Command: fmt.Sprintf("test -d %q", remotePath),
		Timeout: 15,
	})
	if err != nil {
		return false, err
	}
	return res.ExitCode == 0, nil
}

// scpTmpPath returns a unique staging path inside the guest's /tmp.
func scpTmpPath(ext string) string {
	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	return fmt.Sprintf("/tmp/.compute-scp-%s.%s", hex.EncodeToString(buf), ext)
}

// stripBase64Whitespace removes newlines that guest base64(1) may insert.
func stripBase64Whitespace(s string) string {
	s = strings.ReplaceAll(s, "\n", "")
	return strings.ReplaceAll(s, "\r", "")
}

// stripUserPrefix drops a scp-style "user@" prefix from a sandbox reference.
// Transfers run as the sandbox's default user regardless.
func stripUserPrefix(ref string) string {
	if i := strings.Index(ref, "@"); i > 0 {
		return ref[i+1:]
	}
	return ref
}

// extractTarGzBase64 decodes a base64 tar.gz and unpacks it under dest,
// creating dest if needed.
func extractTarGzBase64(tarB64, dest string) error {
	raw, err := base64.StdEncoding.DecodeString(tarB64)
	if err != nil {
		return fmt.Errorf("decoding archive: %w", err)
	}
	gz, err := gzip.NewReader(strings.NewReader(string(raw)))
	if err != nil {
		return fmt.Errorf("opening archive: %w", err)
	}
	defer gz.Close()

	if err := os.MkdirAll(dest, 0o755); err != nil {
		return fmt.Errorf("creating %q: %w", dest, err)
	}

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading archive: %w", err)
		}

		// Sanitize archive member names to prevent path traversal.
		name := header.Name
		if name == "" || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "..") || strings.Contains(name, "/../") {
			return fmt.Errorf("unsafe path in archive: %q", name)
		}
		target := filepath.Join(dest, filepath.FromSlash(name))
		if !strings.HasPrefix(target, filepath.Clean(dest)+string(os.PathSeparator)) && filepath.Clean(target) != filepath.Clean(dest) {
			return fmt.Errorf("unsafe path in archive: %q", name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)|0o700); err != nil {
				return fmt.Errorf("creating directory %q: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("creating parent of %q: %w", target, err)
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode)&0o777|0o600)
			if err != nil {
				return fmt.Errorf("creating file %q: %w", target, err)
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return fmt.Errorf("writing file %q: %w", target, err)
			}
			if err := out.Close(); err != nil {
				return fmt.Errorf("closing file %q: %w", target, err)
			}
		case tar.TypeSymlink:
			if strings.HasPrefix(header.Linkname, "/") || strings.HasPrefix(header.Linkname, "..") {
				return fmt.Errorf("unsafe symlink target in archive: %q -> %q", name, header.Linkname)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("creating parent of %q: %w", target, err)
			}
			_ = os.Remove(target)
			if err := os.Symlink(header.Linkname, target); err != nil {
				return fmt.Errorf("creating symlink %q: %w", target, err)
			}
		default:
			// Skip unsupported member types (devices, hard links, ...).
		}
	}
}

// vmRef is a minimal view of a resolved sandbox used by the scp helpers.
// It keeps the helpers decoupled from db.VM so they can be unit tested.
type vmRef struct {
	ID     string
	Name   string
	Status string
}

// resolveScpSandbox resolves a CLI reference and converts it to a vmRef.
func resolveScpSandbox(ctx context.Context, client *DaemonClient, ref string) (*vmRef, error) {
	v, err := resolveSandboxReference(ctx, client, ref)
	if err != nil {
		return nil, err
	}
	return &vmRef{ID: v.ID, Name: v.Name, Status: string(v.Status)}, nil
}
