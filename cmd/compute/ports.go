package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/Rashid2G/compute/internal/db"
)

// splitPortArgs separates flags from positional tokens so
// `compute expose web 8080 --private` works like `--private web 8080`.
func splitPortArgs(args []string) (flags, positional []string) {
	valueFlags := map[string]bool{
		"--subdomain": true,
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

func printExposeUsage() {
	fmt.Fprintln(os.Stderr, "Usage: compute expose [flags] <name|id> <guest-port>")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Publish a port from a sandbox at its own origin (subdomain or dedicated port).")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Flags:")
	fmt.Fprintln(os.Stderr, "  --public            Publish without authentication (default)")
	fmt.Fprintln(os.Stderr, "  --private           Require tenant authentication")
	fmt.Fprintln(os.Stderr, "  --subdomain <name>  Preferred serve label (DNS-safe, made unique)")
	fmt.Fprintln(os.Stderr, "  --json              Print the port-forward record as JSON")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Each exposure gets its own origin (subdomain or dedicated serve port), so")
	fmt.Fprintln(os.Stderr, "apps that redirect or use absolute paths keep working unchanged.")
}

func printPortsUsage() {
	fmt.Fprintln(os.Stderr, "Usage: compute ports <name|id>")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "List the guest ports exposed on the daemon ingress for a sandbox.")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Flags:")
	fmt.Fprintln(os.Stderr, "  --json              Print the port-forward records as JSON")
}

func printUnexposeUsage() {
	fmt.Fprintln(os.Stderr, "Usage: compute unexpose <name|id> <guest-port>")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Remove a sandbox's ingress exposure for a guest port.")
}

// ingressURL returns an exposure URL. Serve URLs are absolute; legacy relative
// records are resolved against the daemon's advertised public base URL.
func ingressURL(client *DaemonClient, publicURL string) string {
	if publicURL == "" {
		return ""
	}
	if strings.HasPrefix(publicURL, "http://") || strings.HasPrefix(publicURL, "https://") {
		return publicURL
	}
	base := client.BaseURL()
	if public := client.ingressBaseURL(); public != "" {
		base = public
	}
	if base == "" {
		return publicURL
	}
	if !strings.HasPrefix(publicURL, "/") {
		return base + "/" + publicURL
	}
	return base + publicURL
}

func visibilityLabel(pf *db.PortForward) string {
	if pf.Public {
		return "public"
	}
	return "private"
}

func handleExposeCommand(ctx context.Context, client *DaemonClient, args []string) {
	fs := flag.NewFlagSet("expose", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	private := fs.Bool("private", false, "Require tenant authentication (default: public)")
	public := fs.Bool("public", true, "Publish without authentication (default)")
	asJSON := fs.Bool("json", false, "Print the port-forward record as JSON")
	subdomain := fs.String("subdomain", "", "Serve under <name>.compute.dev when DNS is configured")
	fs.Usage = printExposeUsage

	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			printExposeUsage()
			return
		}
	}
	flagArgs, positional := splitPortArgs(args)
	if err := fs.Parse(flagArgs); err != nil {
		printCommandError("invalid expose flags.", "Usage: compute expose [flags] <name|id> <guest-port>", "expose")
	}
	if len(positional) < 2 {
		printCommandError("missing sandbox name/ID or guest port.", "Usage: compute expose [flags] <name|id> <guest-port>", "expose")
	}

	guestPort, err := strconv.Atoi(positional[1])
	if err != nil || guestPort <= 0 || guestPort > 65535 {
		printCommandError("invalid guest port (expected 1-65535).", "Usage: compute expose [flags] <name|id> <guest-port>", "expose")
	}

	v, err := resolveSandboxReference(ctx, client, positional[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving sandbox %s: %v\n", positional[0], err)
		os.Exit(1)
	}

	makePublic := *public
	if *private {
		makePublic = false
	}
	pf, err := client.ExposePort(ctx, v.ID, guestPort, makePublic, strings.TrimSpace(*subdomain))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error exposing port: %v\n", err)
		os.Exit(1)
	}

	if *asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(pf)
		return
	}
	fmt.Printf("Exposed %s:%d (%s)\n", v.Name, guestPort, visibilityLabel(pf))
	client.DiscoverPublicURL(ctx)
	if url := ingressURL(client, pf.PublicURL); url != "" {
		fmt.Println(url)
	}
	if pf.Public && pf.Subdomain == "" {
		fmt.Fprintln(os.Stderr, "Note: public URLs are reachable by anyone who knows them. Use --private to require tenant auth.")
	}
}

func handlePortsCommand(ctx context.Context, client *DaemonClient, args []string) {
	fs := flag.NewFlagSet("ports", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Print the port-forward records as JSON")
	fs.Usage = printPortsUsage

	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			printPortsUsage()
			return
		}
	}
	flagArgs, positional := splitPortArgs(args)
	if err := fs.Parse(flagArgs); err != nil {
		printCommandError("invalid ports flags.", "Usage: compute ports <name|id>", "ports")
	}
	if len(positional) < 1 {
		printCommandError("missing sandbox name or ID.", "Usage: compute ports <name|id>", "ports")
	}
	v, err := resolveSandboxReference(ctx, client, positional[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving sandbox %s: %v\n", positional[0], err)
		os.Exit(1)
	}

	pfs, err := client.ListPortForwards(ctx, v.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing exposed ports: %v\n", err)
		os.Exit(1)
	}
	if *asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(pfs)
		return
	}
	if len(pfs) == 0 {
		fmt.Fprintf(os.Stderr, "No ports are exposed on sandbox %s.\n", v.Name)
		return
	}
	client.DiscoverPublicURL(ctx)
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "PORT\tVISIBILITY\tHOST PORT\tURL")
	for _, pf := range pfs {
		if pf == nil {
			continue
		}
		hostPort := "—"
		if pf.HostPort > 0 {
			hostPort = fmt.Sprintf("%d", pf.HostPort)
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", pf.GuestPort, visibilityLabel(pf), hostPort, ingressURL(client, pf.PublicURL))
	}
	tw.Flush()
}

func handleUnexposeCommand(ctx context.Context, client *DaemonClient, args []string) {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			printUnexposeUsage()
			return
		}
	}
	if len(args) < 2 {
		printCommandError("missing sandbox name/ID or guest port.", "Usage: compute unexpose <name|id> <guest-port>", "unexpose")
	}
	guestPort, err := strconv.Atoi(args[1])
	if err != nil || guestPort <= 0 || guestPort > 65535 {
		printCommandError("invalid guest port (expected 1-65535).", "Usage: compute unexpose <name|id> <guest-port>", "unexpose")
	}
	v, err := resolveSandboxReference(ctx, client, args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving sandbox %s: %v\n", args[0], err)
		os.Exit(1)
	}
	if err := client.UnexposePort(ctx, v.ID, guestPort); err != nil {
		fmt.Fprintf(os.Stderr, "Error unexposing port: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Unexposed %s:%d\n", v.Name, guestPort)
}
