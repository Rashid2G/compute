package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/Rashid2G/on-demand-vms/internal/db"
	"golang.org/x/crypto/ssh"
)

// handleKeysCommand manages the account's saved SSH public keys. Keys marked
// account-wide ("all sandboxes") are embedded into every sandbox the account
// creates and propagated to existing sandboxes.
func handleKeysCommand(ctx context.Context, client *DaemonClient, args []string) {
	if printHelpIfRequested("keys", args) {
		return
	}
	if len(args) == 0 || args[0] == "list" || args[0] == "ls" {
		keys, err := client.ListKeys(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing SSH keys: %v\n", err)
			os.Exit(1)
		}
		if len(keys) == 0 {
			fmt.Println("No SSH keys saved. Add one with: compute keys add --name <name> --key-file ~/.ssh/id_ed25519.pub")
			return
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tALL SANDBOXES\tDEFAULT\tFINGERPRINT")
		for _, k := range keys {
			all := "no"
			if k.AllSandboxes {
				all = "yes"
			}
			def := "no"
			if k.IsDefault {
				def = "yes"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", k.ID, k.Name, all, def, k.Fingerprint)
		}
		w.Flush()
		return
	}

	switch args[0] {
	case "add", "create", "new":
		addCmd := flag.NewFlagSet("keys add", flag.ExitOnError)
		name := addCmd.String("name", "", "Descriptive key name")
		keyFile := addCmd.String("key-file", "", "Path to a public key file (or '-' for stdin)")
		all := addCmd.Bool("all", false, "Apply this key to all current and future sandboxes")
		def := addCmd.Bool("default", false, "Make this key the account default for new sandboxes")
		addCmd.Parse(reorderKeyFlags(args[1:], map[string]bool{"name": true, "key-file": true}))

		pubKey, err := readPublicKey(addCmd.Args(), *keyFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading public key: %v\n", err)
			os.Exit(1)
		}
		keyName := strings.TrimSpace(*name)
		if keyName == "" {
			keyName = "cli-key"
		}
		key, err := client.CreateKey(ctx, keyName, pubKey, *all, *def)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error adding SSH key: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("SSH key added: ID=%s Name=%s AllSandboxes=%t Default=%t\n", key.ID, key.Name, key.AllSandboxes, key.IsDefault)
		if key.AllSandboxes {
			fmt.Println("Applied to all existing sandboxes; new sandboxes will embed it automatically.")
		}

	case "apply":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: compute keys apply <name|id>")
			os.Exit(1)
		}
		res, err := client.ApplyKey(ctx, args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error applying SSH key: %v\n", err)
			os.Exit(1)
		}
		printKeySyncResult(res)

	case "reconcile", "unlock", "repair":
		reconcileCmd := flag.NewFlagSet("keys reconcile", flag.ExitOnError)
		allFlag := reconcileCmd.Bool("all", false, "Reconcile SSH access and unlock accounts across all sandboxes")
		reconcileCmd.Parse(args[1:])
		target := ""
		if reconcileCmd.NArg() > 0 {
			target = reconcileCmd.Arg(0)
		}
		if target != "" && !*allFlag {
			if err := client.ReconcileSandboxSSH(ctx, target); err != nil {
				fmt.Fprintf(os.Stderr, "Error reconciling SSH access for %s: %v\n", target, err)
				os.Exit(1)
			}
			fmt.Printf("Sandbox %s: SSH access and accounts reconciled successfully\n", target)
			return
		}
		res, err := client.ReconcileKeys(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reconciling SSH keys and accounts: %v\n", err)
			os.Exit(1)
		}
		printKeySyncResult(res)

	case "update", "set", "edit":
		updateCmd := flag.NewFlagSet("keys update", flag.ExitOnError)
		name := updateCmd.String("name", "", "New key name")
		keyFile := updateCmd.String("key-file", "", "Path to a replacement public key file (or '-' for stdin)")
		all := updateCmd.Bool("all", false, "Apply this key to all current and future sandboxes")
		noAll := updateCmd.Bool("no-all", false, "Stop applying this key to all sandboxes")
		def := updateCmd.Bool("default", false, "Make this key the account default for new sandboxes")
		noDef := updateCmd.Bool("no-default", false, "Stop using this key as the account default for new sandboxes")
		updateCmd.Parse(reorderKeyFlags(args[1:], map[string]bool{"name": true, "key-file": true}))
		if updateCmd.NArg() < 1 {
			fmt.Fprintln(os.Stderr, "Usage: compute keys update <name|id> [--name <name>] [--key-file <path>] [--all|--no-all] [--default|--no-default]")
			os.Exit(1)
		}
		if *all && *noAll {
			fmt.Fprintln(os.Stderr, "Error: --all and --no-all are mutually exclusive")
			os.Exit(1)
		}
		if *def && *noDef {
			fmt.Fprintln(os.Stderr, "Error: --default and --no-default are mutually exclusive")
			os.Exit(1)
		}
		req := UpdateKeyRequest{}
		if strings.TrimSpace(*name) != "" {
			req.Name = name
		}
		if *all || *noAll {
			v := *all
			req.AllSandboxes = &v
		}
		if *def || *noDef {
			v := *def
			req.IsDefault = &v
		}
		if strings.TrimSpace(*keyFile) != "" {
			pubKey, err := readPublicKey(nil, *keyFile)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error reading public key: %v\n", err)
				os.Exit(1)
			}
			req.PublicKey = &pubKey
		}
		key, err := client.UpdateKey(ctx, updateCmd.Arg(0), req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error updating SSH key: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("SSH key updated: ID=%s Name=%s AllSandboxes=%t\n", key.ID, key.Name, key.AllSandboxes)

	case "set-default":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: compute keys set-default <name|id>")
			os.Exit(1)
		}
		yes := true
		key, err := client.UpdateKey(ctx, args[1], UpdateKeyRequest{IsDefault: &yes})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error setting default SSH key: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Default SSH key set: ID=%s Name=%s\n", key.ID, key.Name)
		fmt.Println("New sandboxes will embed it automatically unless another key is chosen at creation.")

	case "show", "info", "get":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: compute keys show <name|id>")
			os.Exit(1)
		}
		key, err := client.GetKey(ctx, args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching SSH key: %v\n", err)
			os.Exit(1)
		}
		printKeyDetails(key)

	case "delete", "rm", "remove":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: compute keys delete <name|id>...")
			os.Exit(1)
		}
		for _, target := range args[1:] {
			if err := client.DeleteKey(ctx, target); err != nil {
				fmt.Fprintf(os.Stderr, "Error deleting SSH key %s: %v\n", target, err)
				os.Exit(1)
			}
			fmt.Printf("SSH key %s deleted.\n", target)
		}

	default:
		printCommandError(fmt.Sprintf("Unknown keys action '%s'.", args[0]), "", "keys")
	}
}

func printKeyDetails(key *db.SSHKey) {
	if key == nil {
		return
	}
	fmt.Printf("ID:            %s\n", key.ID)
	fmt.Printf("Name:          %s\n", key.Name)
	fmt.Printf("Fingerprint:   %s\n", key.Fingerprint)
	fmt.Printf("All sandboxes: %t\n", key.AllSandboxes)
	fmt.Printf("Default:       %t\n", key.IsDefault)
	fmt.Printf("Created:       %s\n", key.CreatedAt.Format("2006-01-02 15:04:05 MST"))
	fmt.Printf("Public key:    %s\n", key.PublicKey)
}

func printKeySyncResult(res *KeyApplyResult) {
	if res == nil {
		fmt.Println("Key applied.")
		return
	}
	if res.Key != nil {
		fmt.Printf("SSH key applied: ID=%s Name=%s AllSandboxes=%t\n", res.Key.ID, res.Key.Name, res.Key.AllSandboxes)
	}
	if res.Sync != nil {
		fmt.Printf("Sandboxes updated: %d, failed: %d\n", len(res.Sync.Applied), len(res.Sync.Failed))
		for _, f := range res.Sync.Failed {
			name := f.Name
			if name == "" {
				name = f.VMID
			}
			fmt.Fprintf(os.Stderr, "  failed: %s: %s\n", name, f.Error)
		}
	}
}

// readPublicKey resolves a public key from an explicit file, an argument that
// is either a key string or a file path, or stdin when path is "-".
func readPublicKey(args []string, path string) (string, error) {
	var source string
	if strings.TrimSpace(path) == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", err
		}
		key := strings.TrimSpace(string(data))
		if err := validatePublicKey(key); err != nil {
			return "", err
		}
		return key, nil
	}
	switch {
	case strings.TrimSpace(path) != "":
		source = path
	case len(args) > 0:
		source = args[0]
	default:
		return "", errors.New("provide a public key file with --key-file or pass the key as an argument")
	}

	source = expandHome(source)
	key := strings.TrimSpace(source)
	if !looksLikePublicKey(key) {
		data, err := os.ReadFile(source)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", source, err)
		}
		key = strings.TrimSpace(string(data))
	}
	if key == "" {
		return "", fmt.Errorf("%s is empty", source)
	}
	if err := validatePublicKey(key); err != nil {
		return "", fmt.Errorf("%s: %w", source, err)
	}
	return key, nil
}

func looksLikePublicKey(value string) bool {
	return strings.HasPrefix(value, "ssh-") || strings.HasPrefix(value, "ecdsa-") || strings.HasPrefix(value, "sk-")
}

// validatePublicKey rejects private keys and other non-public-key input before
// it can be embedded into a sandbox's authorized_keys.
func validatePublicKey(raw string) error {
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(raw))); err != nil {
		return fmt.Errorf("not a valid SSH public key: %w", err)
	}
	return nil
}

// reorderKeyFlags moves flags (and their values) ahead of positional arguments
// so Go's flag package, which stops at the first non-flag token, accepts
// commands like `compute keys update laptop --no-all`.
func reorderKeyFlags(args []string, valueFlags map[string]bool) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			flags = append(flags, arg)
			if !strings.Contains(arg, "=") {
				name := strings.TrimLeft(arg, "-")
				if valueFlags[name] && i+1 < len(args) {
					i++
					flags = append(flags, args[i])
				}
			}
			continue
		}
		positional = append(positional, arg)
	}
	return append(flags, positional...)
}

func expandHome(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}
