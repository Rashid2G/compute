package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/Rashid2G/compute/internal/db"
)

// SSH host kinds returned in SandboxAccess.SSHHostKind.
const (
	SSHHostHostname = "hostname"
	SSHHostIP       = "ip_address"
	SSHHostPublicIP = "public_ip"
	SSHHostNone     = "none"
)

// Immutable labels for the URL source.
const (
	URLKindHostname   = "hostname"
	URLKindPort       = "port"
	URLKindLegacyPort = "legacy_port"
)

// SandboxAccess is the display model for reaching a sandbox over SSH and
// HTTP.
type SandboxAccess struct {
	Sandbox string `json:"sandbox"`

	// Hostname is the transient "<sandbox>.<username>.<zone>" name returned by
	// the API when the DNS responder is configured.
	Hostname string `json:"hostname,omitempty"`
	Zone     string `json:"zone,omitempty"`

	SSH           string `json:"ssh,omitempty"`
	SSHHost       string `json:"ssh_host,omitempty"`
	SSHHostKind   string `json:"ssh_host_kind"`
	SSHResolvable bool   `json:"ssh_resolvable"`
	SSHNote       string `json:"ssh_note,omitempty"`

	URL           string `json:"url,omitempty"`
	URLHost       string `json:"url_host,omitempty"`
	URLKind       string `json:"url_kind,omitempty"`
	URLResolvable bool   `json:"url_resolvable"`
	URLNote       string `json:"url_note,omitempty"`

	Port          int    `json:"port,omitempty"`
	PortURL       string `json:"port_url,omitempty"`
	LegacyPortURL string `json:"legacy_port_url,omitempty"`

	CLISSH string `json:"cli_ssh,omitempty"`
	CLIURL string `json:"cli_url,omitempty"`
}

// sandboxAccessForVM builds the access model for a sandbox.
//
// SSH precedence: tailnet DNS (MagicDNS, reachable as soon as the sandbox joins
// the tailnet) → sandbox hostname (only when the zone is delegated) → tailnet
// IP → guest IP → public IP. Guest IPs live inside the daemon's network
// namespace on the lab deployment, so the tailnet address is preferred.
//
// HTTP precedence: "<hostname>/" when the configured zone is not owned by
// MagicDNS. Port-scoped ingress URLs are added when port > 0.
func sandboxAccessForVM(v *db.VM, sshUser string, port int) SandboxAccess {
	if sshUser == "" {
		sshUser = "compute"
	}
	acc := SandboxAccess{SSHHostKind: SSHHostNone}
	if v == nil {
		acc.SSHNote = "Sandbox details are unavailable."
		acc.URLNote = acc.SSHNote
		return acc
	}

	acc.Sandbox = strings.TrimSpace(v.Name)
	if acc.Sandbox == "" {
		acc.Sandbox = strings.TrimSpace(v.ID)
	}
	acc.Hostname = normalizeHostname(v.Hostname)

	sandboxLabel, tenant, zone, hostnameOK := splitSandboxHostname(acc.Hostname)
	acc.Zone = zone
	zoneDelegated := hostnameOK && zone != ""

	// SSH target selection.
	switch {
	case acc.Hostname != "":
		acc.SSHHost = acc.Hostname
		acc.SSHHostKind = SSHHostHostname
		acc.SSHResolvable = zoneDelegated
		acc.SSHNote = fmt.Sprintf("%s requires %s to be delegated to this daemon's DNS responder.", acc.Hostname, acc.Zone)
	case strings.TrimSpace(v.PublicIP) != "":
		acc.SSHHost = strings.TrimSpace(v.PublicIP)
		acc.SSHHostKind = SSHHostPublicIP
		acc.SSHResolvable = true
		acc.SSHNote = "No DNS name yet; using the public IP."
	case strings.TrimSpace(v.IPAddress) != "":
		acc.SSHHost = strings.TrimSpace(v.IPAddress)
		acc.SSHHostKind = SSHHostIP
		acc.SSHResolvable = true
		acc.SSHNote = "No DNS name yet; using the sandbox guest IP."
	default:
		acc.SSHNote = "No address is assigned to this sandbox yet."
	}
	if acc.SSHHost != "" {
		acc.SSH = fmt.Sprintf("ssh %s@%s", sshUser, acc.SSHHost)
	}

	// HTTP URL selection.
	switch {
	case acc.Hostname == "":
		acc.URLNote = "No sandbox DNS name is configured. Enable the daemon DNS responder with a base domain to publish an HTTP URL."
	case !hostnameOK:
		acc.URLNote = fmt.Sprintf("Could not derive a zone from the sandbox hostname %q.", acc.Hostname)
	default:
		acc.URL = "http://" + strings.ToLower(acc.Hostname) + "/"
		acc.URLHost = acc.Hostname
		acc.URLKind = URLKindHostname
		acc.URLResolvable = true
		acc.URLNote = fmt.Sprintf("Requires %s to be delegated to this daemon's DNS responder.", acc.Zone)
	}

	// Port-scoped ingress URLs (the edge proxy accepts "<port>-<vm>" labels).
	if port > 0 && acc.URLResolvable && hostnameOK && sandboxLabel != "" && zone != "" {
		head := fmt.Sprintf("%d-%s", port, sandboxLabel)
		acc.Port = port
		if tenant != "" {
			acc.PortURL = fmt.Sprintf("http://%s.%s.%s/", head, tenant, zone)
			// The pre-tenant single-label form only addresses the legacy
			// "default" owner; for real usernames it would resolve to a
			// different (or no) sandbox, so it is not offered.
			if tenant == "default" {
				acc.LegacyPortURL = fmt.Sprintf("http://%s.%s/", head, zone)
			}
		} else {
			acc.PortURL = fmt.Sprintf("http://%s.%s/", head, zone)
		}
		acc.PortURL = strings.ToLower(acc.PortURL)
		acc.LegacyPortURL = strings.ToLower(acc.LegacyPortURL)
		acc.URLKind = URLKindPort
	}

	if acc.Sandbox != "" {
		acc.CLISSH = "compute ssh " + acc.Sandbox
		acc.CLIURL = "compute url " + acc.Sandbox
	}
	return acc
}

// Primary returns the best single value for compact lists: the HTTP URL when
// one resolves, otherwise the SSH name, otherwise nothing.
func (a SandboxAccess) Primary() string {
	if a.URL != "" {
		return a.URL
	}
	return a.SSHHost
}

// sandboxAccessLines renders the SSH/URL block for `compute sandbox info` and
// `compute url --verbose`. Returning lines keeps the formatting testable.
func sandboxAccessLines(a SandboxAccess) []string {
	line := func(label, value string) string {
		return fmt.Sprintf("  %-14s %s", label+":", value)
	}
	lines := []string{
		line("Hostname", valueOrDash(a.Hostname)),
		line("SSH", valueOrDash(a.SSH)),
	}
	if a.SSHNote != "" {
		lines = append(lines, fmt.Sprintf("  %-14s %s", "SSH note:", a.SSHNote))
	}
	lines = append(lines, line("URL", valueOrDash(a.URL)))
	if a.URLNote != "" {
		lines = append(lines, fmt.Sprintf("  %-14s %s", "URL note:", a.URLNote))
	}
	if a.PortURL != "" {
		lines = append(lines, line("Port URL", a.PortURL))
	}
	if a.LegacyPortURL != "" {
		lines = append(lines, line("Legacy URL", a.LegacyPortURL))
	}
	lines = append(lines, line("CLI", valueOrDash(a.CLISSH)))
	return lines
}

// writeSandboxList renders the `compute sandbox list` table. The URL/SSH
// column is appended so existing columns keep their positions.
func writeSandboxList(w io.Writer, vms []*db.VM) {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tSTATUS\tVCPUS\tMEM(MB)\tIP ADDRESS\tURL / SSH NAME\tPROTECTED")
	for _, v := range vms {
		prot := "no"
		if v.Protected {
			prot = "yes"
		}
		primary := valueOrDash(sandboxAccessForVM(v, "compute", 0).Primary())
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%s\t%s\t%s\n",
			v.ID, v.Name, v.Status, v.VCPUs, v.MemoryMB, v.IPAddress, primary, prot)
	}
	tw.Flush()
}

// splitSandboxHostname splits "<sandbox>.<tenant>.<zone>" (the API hostname
// shape) into its parts. A two-label name is treated as "<sandbox>.<zone>"
// for legacy single-label deployments.
func splitSandboxHostname(hostname string) (sandbox, tenant, zone string, ok bool) {
	h := normalizeHostname(hostname)
	if h == "" {
		return "", "", "", false
	}
	labels := strings.Split(h, ".")
	switch {
	case len(labels) >= 3:
		return labels[0], labels[1], strings.Join(labels[2:], "."), true
	case len(labels) == 2:
		return labels[0], "", labels[1], true
	}
	return "", "", "", false
}

func normalizeHostname(hostname string) string {
	return strings.TrimSuffix(strings.TrimSpace(hostname), ".")
}

func valueOrDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
}

// matchSandboxReference picks the sandbox matching a CLI reference. It accepts
// the external ID, name, internal ID, DNS hostname, tailnet DNS name, and
// addresses so `compute ssh`/`url`/`info` can consume the names the GUI shows.
func matchSandboxReference(vms []*db.VM, ref string) *db.VM {
	wanted := normalizeReference(ref)
	if wanted == "" {
		return nil
	}
	for _, v := range vms {
		if v == nil {
			continue
		}
		for _, candidate := range []string{
			v.ID, v.Name, v.InternalID, v.Hostname,
			v.IPAddress, v.PublicIP,
		} {
			if candidate != "" && normalizeReference(candidate) == wanted {
				return v
			}
		}
	}
	return nil
}

func normalizeReference(ref string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(ref)), ".")
}

// resolveSandboxReference resolves a CLI reference (ID, name, DNS hostname,
// or IP) against the daemon. The daemon's own lookup only
// understands IDs and names, so hostnames fall back to a client-side match
// against the list response.
func resolveSandboxReference(ctx context.Context, client *DaemonClient, ref string) (*db.VM, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("empty sandbox reference")
	}
	if v, err := client.GetVM(ctx, ref); err == nil && v != nil && v.ID != "" {
		return v, nil
	}
	vms, err := client.ListVMs(ctx)
	if err != nil {
		return nil, err
	}
	if v := matchSandboxReference(vms, ref); v != nil {
		return v, nil
	}
	return nil, fmt.Errorf("sandbox %q not found (tried ID, name, hostname, and IP)", ref)
}

// splitURLArgs separates flag tokens from positional tokens so `compute url
// <sandbox> --json` works like `compute url --json <sandbox>`. Flags that take
// a value consume the following token unless written as --flag=value.
func splitURLArgs(args []string) (flags, positional []string) {
	valueFlags := map[string]bool{
		"--port": true, "-p": true,
		"--user": true, "-u": true,
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}
		flags = append(flags, arg)
		if valueFlags[arg] && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return flags, positional
}

func printSandboxURLUsage() {
	fmt.Fprintln(os.Stderr, "Usage: compute url [flags] [user@]<name|id|hostname>")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Print the best HTTP URL or SSH target for a sandbox.")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Flags:")
	fmt.Fprintln(os.Stderr, "  --json              Print the full access model as JSON")
	fmt.Fprintln(os.Stderr, "  --port <n>          Include the port-scoped ingress URL for guest port <n>")
	fmt.Fprintln(os.Stderr, "  -u, --user <name>   SSH user (default: compute)")
	fmt.Fprintln(os.Stderr, "  -v, --verbose       Show every sandbox name plus SSH/URL hints")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "HTTP URLs are only printed when the sandbox DNS zone is expected to resolve;")
	fmt.Fprintln(os.Stderr, "when the zone is served by Tailscale MagicDNS the SSH name or IP is printed instead.")
}

// handleSandboxURL implements `compute url` / `compute sandbox url`.
func handleSandboxURL(ctx context.Context, client *DaemonClient, args []string) {
	fs := flag.NewFlagSet("url", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Print the full access model as JSON")
	port := fs.Int("port", 0, "Include the port-scoped ingress URL for a guest port")
	fs.IntVar(port, "p", 0, "Include the port-scoped ingress URL for a guest port")
	user := "compute"
	fs.StringVar(&user, "user", "compute", "SSH user")
	fs.StringVar(&user, "u", "compute", "SSH user")
	verbose := fs.Bool("verbose", false, "Show every sandbox name plus SSH/URL hints")
	fs.BoolVar(verbose, "v", false, "Show every sandbox name plus SSH/URL hints")
	fs.Usage = printSandboxURLUsage

	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			printSandboxURLUsage()
			return
		}
	}
	// Go's flag package stops at the first positional argument. Sandbox
	// commands read most naturally as `compute url <sandbox> --json`, so
	// split flags from positionals first and accept flags in any order.
	flagArgs, positionalArgs := splitURLArgs(args)
	if err := fs.Parse(flagArgs); err != nil {
		printCommandError("invalid url flags.", "Usage: compute url [flags] [user@]<name|id|hostname>", "url")
	}
	rest := positionalArgs
	if len(rest) < 1 {
		printCommandError("missing sandbox name or ID.", "Usage: compute url [flags] [user@]<name|id|hostname>", "url")
	}
	if *port < 0 || *port > 65535 {
		printCommandError("invalid --port value (expected 0-65535).", "Usage: compute url [flags] [user@]<name|id|hostname>", "url")
	}

	ref := rest[0]
	if strings.Contains(ref, "@") {
		parts := strings.SplitN(ref, "@", 2)
		if parts[0] != "" {
			user = parts[0]
		}
		ref = parts[1]
	}

	v, err := resolveSandboxReference(ctx, client, ref)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving sandbox %s: %v\n", ref, err)
		os.Exit(1)
	}
	acc := sandboxAccessForVM(v, user, *port)

	if *asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(acc)
		return
	}
	if *verbose {
		fmt.Printf("Sandbox %s:\n", v.Name)
		for _, line := range sandboxAccessLines(acc) {
			fmt.Println(line)
		}
		return
	}
	if acc.URL != "" {
		fmt.Println(acc.URL)
		if acc.PortURL != "" {
			fmt.Fprintf(os.Stderr, "Port %d: %s\n", acc.Port, acc.PortURL)
		}
		return
	}
	if acc.SSH != "" {
		fmt.Println(acc.SSH)
		if acc.URLNote != "" {
			fmt.Fprintf(os.Stderr, "Note: %s\n", acc.URLNote)
		}
		return
	}
	fmt.Fprintf(os.Stderr, "No URL or SSH target is available for sandbox %s: %s\n", v.Name, acc.SSHNote)
	os.Exit(1)
}
