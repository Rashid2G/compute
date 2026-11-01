package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/master/audit"
	"github.com/Rashid2G/on-demand-vms/internal/master/auth"
	"github.com/Rashid2G/on-demand-vms/internal/master/pki"
	"github.com/Rashid2G/on-demand-vms/internal/store"
	"github.com/Rashid2G/on-demand-vms/internal/store/db"
	"github.com/Rashid2G/on-demand-vms/pkg/version"
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
	_ = fs.Parse(args)

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

	fmt.Println("\nMicroForge initialization complete!")
	fmt.Println("Create the initial administrator account with:")
	fmt.Printf("  microforged admin user create --username admin --email admin@example.com --password <secret> --data-dir %s\n", *dataDir)
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
	fmt.Println("Usage: microforged admin <command> [arguments]")
	fmt.Println("\nCommands:")
	fmt.Println("  user create     Create a new user or administrator account")
	fmt.Println("  apikey create   Generate an API key for a user")
	fmt.Println("  audit verify    Verify cryptographic hash chain integrity of the audit log")
}

func runAdminUser(args []string) {
	if len(args) < 1 || args[0] != "create" {
		fmt.Println("Usage: microforged admin user create --username <name> --email <email> --password <pass> [--role admin|user] [--data-dir <path>]")
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
		fmt.Println("Usage: microforged admin apikey create --username <name> --name <key-name> [--data-dir <path>]")
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
		fmt.Println("Usage: microforged admin audit verify [--data-dir <path>]")
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

	info := version.Get("microforged", "Compute Daemon")
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
