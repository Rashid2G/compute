package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Rashid2G/compute/internal/config"
	"github.com/Rashid2G/compute/internal/dns"
	"github.com/Rashid2G/compute/internal/doctor"
	"github.com/Rashid2G/compute/internal/image"
	"github.com/Rashid2G/compute/internal/master/audit"
	"github.com/Rashid2G/compute/internal/master/auth"
	"github.com/Rashid2G/compute/internal/master/pki"
	"github.com/Rashid2G/compute/internal/store"
	"github.com/Rashid2G/compute/internal/store/db"
	"github.com/Rashid2G/compute/pkg/version"
)

func handleSubcommands() bool {
	if len(os.Args) < 2 {
		return false
	}

	switch os.Args[1] {
	case "version", "-v", "--version", "-version":
		runVersion(os.Args[2:])
		return true
	case "init":
		runInit(os.Args[2:])
		return true
	case "check":
		runCheck(os.Args[2:])
		return true
	case "admin":
		runAdmin(os.Args[2:])
		return true
	default:
		return false
	}
}

func runInit(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	dataDir := fs.String("data-dir", "/var/lib/compute/master", "Directory for master data, keys, and SQLite state")
	images := fs.Bool("images", false, "Bootstrap base images and the internal SSH key, then exit")
	mode := fs.String("images-mode", "", "Image mode: auto, download, prebuilt, build, or none (default COMPUTE_IMAGES_MODE or auto)")
	manifestURL := fs.String("image-manifest-url", "", "Image manifest URL (default COMPUTE_IMAGE_MANIFEST_URL)")
	imagesDir := fs.String("images-dir", "", "Base images directory (default COMPUTE_IMAGES_DIR)")
	sshKeyPath := fs.String("ssh-key-path", "", "Internal SSH private key path (default COMPUTE_SSH_KEY_PATH)")
	_ = fs.Parse(args)

	if *images {
		runInitImages(*mode, *manifestURL, *imagesDir, *sshKeyPath)
		return
	}

	fmt.Printf("Initializing Compute master in %s...\n", *dataDir)
	if err := os.MkdirAll(*dataDir, 0700); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to create data directory: %v\n", err)
		os.Exit(1)
	}

	// 1. Master encryption key (master.key)
	keyPath := filepath.Join(*dataDir, "master.key")
	mk, err := pki.LoadOrCreateMasterKey(keyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to initialize master key: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ Master secret key initialized (%s)\n", keyPath)

	// 2. Internal CA (ca.crt, ca.key)
	_, err = pki.LoadOrCreateCA(*dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to initialize internal CA: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ Internal mTLS Root CA initialized (%s)\n", filepath.Join(*dataDir, "ca.crt"))

	// 3. SQLite state database & migrations (state.db)
	dbPath := filepath.Join(*dataDir, "state.db")
	st, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to initialize state database: %v\n", err)
		os.Exit(1)
	}
	_ = st.Close()
	fmt.Printf("✓ State database and schema migrated (%s)\n", dbPath)

	// 4. Immutable audit log (audit.db)
	auditDBPath := filepath.Join(*dataDir, "audit.db")
	logger, err := audit.OpenLogger(auditDBPath, mk)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to initialize audit database: %v\n", err)
		os.Exit(1)
	}
	_ = logger.Close()
	fmt.Printf("✓ Audit database initialized (%s)\n", auditDBPath)

	fmt.Println("\nCompute initialization complete!")
	fmt.Println("Create the initial administrator account with:")
	fmt.Printf("  computed admin user create --username admin --email admin@example.com --password <secret> --data-dir %s\n", *dataDir)
}

// runInitImages performs the first-run image/SSH-key bootstrap. It is used by
// the compute-init compose service and is safe to run on every start.
func runInitImages(mode, manifestURL, imagesDir, sshKeyPath string) {
	cfg, _, err := config.ResolveConfig("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid configuration: %v\n", err)
		os.Exit(1)
	}
	resolvedImagesDir := firstNonEmpty(imagesDir, cfg.Images.ImagesDir)
	resolvedKeyPath := firstNonEmpty(sshKeyPath, os.Getenv("COMPUTE_SSH_KEY_PATH"))
	if resolvedKeyPath == "" {
		resolvedKeyPath = cfg.Images.SSHKeyPath
		// Keep the key inside the selected images directory unless the operator
		// pinned an explicit path; --images-dir is often a scratch/bootstrap dir.
		if imagesDir != "" && imagesDir != cfg.Images.ImagesDir {
			resolvedKeyPath = filepath.Join(resolvedImagesDir, "ssh", "id_ed25519")
		}
	}
	var rootfsSizeMB int
	if rawSize := strings.TrimSpace(os.Getenv("COMPUTE_ROOTFS_SIZE_MB")); rawSize != "" {
		parsed, err := strconv.Atoi(rawSize)
		if err != nil || parsed <= 0 {
			fmt.Fprintf(os.Stderr, "Error: invalid COMPUTE_ROOTFS_SIZE_MB=%q: expected a positive integer\n", rawSize)
			os.Exit(1)
		}
		rootfsSizeMB = parsed
	}
	opts := image.BootstrapOptions{
		Mode:           firstNonEmpty(mode, os.Getenv("COMPUTE_IMAGES_MODE")),
		ManifestURL:    firstNonEmpty(manifestURL, os.Getenv("COMPUTE_IMAGE_MANIFEST_URL")),
		ImagesDir:      resolvedImagesDir,
		KernelName:     cfg.Images.DefaultKernel,
		RootfsName:     cfg.Images.DefaultRootfs,
		SSHKeyPath:     resolvedKeyPath,
		KernelSrc:      strings.TrimSpace(os.Getenv("COMPUTE_KERNEL_SRC")),
		KernelURL:      strings.TrimSpace(os.Getenv("COMPUTE_KERNEL_URL")),
		KernelSHA256:   strings.TrimSpace(os.Getenv("COMPUTE_KERNEL_SHA256")),
		RootfsSizeMB:   rootfsSizeMB,
		Logf:           log.Printf,
		AllowPlainHTTP: envTruthy("COMPUTE_IMAGE_ALLOW_HTTP"),
	}
	res, err := image.Bootstrap(context.Background(), opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Image bootstrap failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Image bootstrap complete (mode=%s)\n", res.State.Mode)
	if res.State.Version != "" {
		fmt.Printf("  manifest version: %s\n", res.State.Version)
	}
	if opts.Mode != image.ModeNone {
		fmt.Printf("  kernel: %s\n", filepath.Join(opts.ImagesDir, cfg.Images.DefaultKernel))
		fmt.Printf("  rootfs: %s\n", filepath.Join(opts.ImagesDir, cfg.Images.DefaultRootfs))
	}
	fmt.Printf("  ssh key: %s%s\n", res.KeyPath, map[bool]string{true: " (generated)", false: ""}[res.GeneratedKey])
	for _, path := range res.Downloaded {
		fmt.Printf("  installed: %s\n", path)
	}
}

// runCheck prints the same diagnostics as the readiness endpoint without
// requiring a running daemon. `compute doctor` renders the endpoint version.
func runCheck(args []string) {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "Print the report as JSON")
	_ = fs.Parse(args)

	cfg, _, err := config.ResolveConfig("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid configuration: %v\n", err)
		os.Exit(1)
	}
	report := doctor.Run(cfg)
	if *jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(report)
	} else {
		printDoctorReport(report)
	}
	if !report.OK {
		os.Exit(1)
	}
}

func printDoctorReport(report doctor.Report) {
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
	if report.OK {
		fmt.Println("\nAll critical checks passed.")
	} else {
		fmt.Println("\nCritical checks failed; fix the items above before creating sandboxes.")
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func envTruthy(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func runAdmin(args []string) {
	if len(args) < 1 {
		printAdminUsage()
		os.Exit(1)
	}

	switch args[0] {
	case "user":
		runAdminUser(args[1:])
	case "apikey":
		runAdminAPIKey(args[1:])
	case "audit":
		runAdminAudit(args[1:])
	default:
		printAdminUsage()
		os.Exit(1)
	}
}

func printAdminUsage() {
	fmt.Println("Usage: computed admin <command> [arguments]")
	fmt.Println("\nCommands:")
	fmt.Println("  user create     Create a new user or administrator account")
	fmt.Println("  apikey create   Generate an API key for a user")
	fmt.Println("  audit verify    Verify cryptographic hash chain integrity of the audit log")
}

func runAdminUser(args []string) {
	if len(args) < 1 || args[0] != "create" {
		fmt.Println("Usage: computed admin user create --username <name> --email <email> --password <pass> [--role admin|user] [--data-dir <path>]")
		os.Exit(1)
	}

	fs := flag.NewFlagSet("user create", flag.ExitOnError)
	username := fs.String("username", "", "Username (DNS label, unique)")
	email := fs.String("email", "", "User email address")
	password := fs.String("password", "", "Password")
	role := fs.String("role", "admin", "User role ('admin' or 'user')")
	dataDir := fs.String("data-dir", "/var/lib/compute/master", "Master data directory")
	_ = fs.Parse(args[1:])

	if *username == "" || *email == "" || *password == "" {
		fmt.Fprintf(os.Stderr, "Error: --username, --email, and --password are required\n")
		os.Exit(1)
	}
	// Usernames become the tenant label in sandbox DNS names
	// (<sandbox>.<username>.<base-domain>), so they must be DNS-safe.
	if !dns.ValidLabel(*username) {
		fmt.Fprintf(os.Stderr, "Error: invalid username %q: must be lowercase letters, digits, and dashes (a DNS label, max %d chars)\n", *username, dns.MaxLabelLength)
		os.Exit(1)
	}

	dbPath := filepath.Join(*dataDir, "state.db")
	st, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to open database: %v\n", err)
		os.Exit(1)
	}
	defer st.Close()

	passwordHash, err := auth.HashPassword(*password)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to hash password: %v\n", err)
		os.Exit(1)
	}

	now := time.Now().UTC().UnixMilli()
	userID := store.NewUserID()
	user := &db.User{
		ID:           userID,
		Email:        *email,
		Username:     *username,
		DisplayName:  *username,
		PasswordHash: passwordHash,
		Role:         *role,
		Status:       "active",
		CreatedAt:    now,
		UpdatedAt:    now,
		Revision:     1,
	}

	queries := db.New(st.Writer())
	ctx := context.Background()
	if err := queries.CreateUser(ctx, user); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to create user: %v\n", err)
		os.Exit(1)
	}

	// Audit record
	keyPath := filepath.Join(*dataDir, "master.key")
	if mk, err := pki.LoadOrCreateMasterKey(keyPath); err == nil {
		auditPath := filepath.Join(*dataDir, "audit.db")
		if logger, err := audit.OpenLogger(auditPath, mk); err == nil {
			_ = logger.Log(ctx, &audit.Entry{
				TraceID:    "cli-admin-bootstrap",
				ActorID:    "system:cli",
				ActorName:  "cli",
				ActorIP:    "127.0.0.1",
				Action:     "user.create",
				TargetKind: "user",
				TargetID:   userID,
				TargetName: *username,
				Status:     "success",
			})
			_ = logger.Close()
		}
	}

	fmt.Printf("✓ User %s (%s) created successfully with ID %s\n", *username, *role, userID)
}

func runAdminAPIKey(args []string) {
	if len(args) < 1 || args[0] != "create" {
		fmt.Println("Usage: computed admin apikey create --username <name> --name <key-name> [--data-dir <path>]")
		os.Exit(1)
	}

	fs := flag.NewFlagSet("apikey create", flag.ExitOnError)
	username := fs.String("username", "", "Username")
	name := fs.String("name", "cli-key", "API Key display name")
	dataDir := fs.String("data-dir", "/var/lib/compute/master", "Master data directory")
	_ = fs.Parse(args[1:])

	if *username == "" {
		fmt.Fprintf(os.Stderr, "Error: --username is required\n")
		os.Exit(1)
	}

	dbPath := filepath.Join(*dataDir, "state.db")
	st, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to open database: %v\n", err)
		os.Exit(1)
	}
	defer st.Close()

	keyPath := filepath.Join(*dataDir, "master.key")
	mk, err := pki.LoadOrCreateMasterKey(keyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to load master key: %v\n", err)
		os.Exit(1)
	}

	queries := db.New(st.Writer())
	ctx := context.Background()

	user, err := queries.GetUserByEmailOrUsername(ctx, *username)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: user not found: %v\n", err)
		os.Exit(1)
	}

	km := auth.NewKeyManager(queries, mk)
	token, record, err := km.GenerateAPIKey(ctx, user.ID, *name, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to generate API key: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✓ API Key created for user %s:\n", *username)
	fmt.Printf("  Key ID: %s\n", record.ID)
	fmt.Printf("  Token:  %s\n", token)
	fmt.Println("  (Save this token now; it will never be displayed again)")
}

func runAdminAudit(args []string) {
	if len(args) < 1 || args[0] != "verify" {
		fmt.Println("Usage: computed admin audit verify [--data-dir <path>]")
		os.Exit(1)
	}

	fs := flag.NewFlagSet("audit verify", flag.ExitOnError)
	dataDir := fs.String("data-dir", "/var/lib/compute/master", "Master data directory")
	_ = fs.Parse(args[1:])

	keyPath := filepath.Join(*dataDir, "master.key")
	mk, err := pki.LoadOrCreateMasterKey(keyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to load master key: %v\n", err)
		os.Exit(1)
	}

	auditPath := filepath.Join(*dataDir, "audit.db")
	logger, err := audit.OpenLogger(auditPath, mk)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to open audit log: %v\n", err)
		os.Exit(1)
	}
	defer logger.Close()

	ctx := context.Background()
	if err := logger.VerifyChain(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "✗ AUDIT VERIFICATION FAILED: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("✓ Audit log verification passed: all hash chains intact and untampered.")
}

func runVersion(args []string) {
	fs := flag.NewFlagSet("version", flag.ExitOnError)
	short := fs.Bool("short", false, "Print only the version number")
	fs.BoolVar(short, "s", false, "Print only the version number")
	asJSON := fs.Bool("json", false, "Print version details as JSON")
	fs.BoolVar(asJSON, "j", false, "Print version details as JSON")
	_ = fs.Parse(args)

	info := version.Get("computed", "Compute Daemon")
	if *short {
		fmt.Println(info.Version)
		return
	}
	if *asJSON {
		out, _ := info.JSON()
		fmt.Println(out)
		return
	}
	fmt.Printf("%s\n\n", info.String())
	fmt.Print(info.Detailed(""))
	fmt.Println()
}
