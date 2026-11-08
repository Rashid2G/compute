package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
)

type CLIConfigFile struct {
	APIKey string `json:"api_key,omitempty"`
	Email  string `json:"email,omitempty"`
	UserID string `json:"user_id,omitempty"`
	OrgID  string `json:"org_id,omitempty"`
}

func saveCLIConfig(conf CLIConfigFile) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".compute")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(conf, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600)
}

func handleAuthCommand(ctx context.Context, client *DaemonClient, args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: compute auth <login|signup|whoami|keys|logout> [options]")
		return
	}

	sub := args[0]
	subArgs := args[1:]

	switch sub {
	case "login":
		loginCmd := flag.NewFlagSet("auth login", flag.ExitOnError)
		username := loginCmd.String("username", "", "Username or email")
		password := loginCmd.String("password", "", "User password")
		loginCmd.Parse(subArgs)

		if *username == "" || *password == "" {
			fmt.Print("Username/Email: ")
			fmt.Scanln(username)
			fmt.Print("Password: ")
			fmt.Scanln(password)
		}

		if *username == "" || *password == "" {
			fmt.Fprintln(os.Stderr, "Error: username and password are required")
			os.Exit(1)
		}

		token, user, err := client.Login(ctx, *username, *password)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Login failed: %v\n", err)
			os.Exit(1)
		}

		// Set token in client so we can create an API key
		client.SetAPIKey(token)

		// Create a long-lived machine API key for the CLI
		apiKey, err := client.CreateAPIKey(ctx, "cli-"+*username, 0)
		if err != nil {
			// Fallback to session token
			apiKey = token
		}

		userID := ""
		email := ""
		if user != nil {
			if id, ok := user["id"].(string); ok {
				userID = id
			}
			if em, ok := user["email"].(string); ok {
				email = em
			}
		}

		if err := saveCLIConfig(CLIConfigFile{
			APIKey: apiKey,
			Email:  email,
			UserID: userID,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to save credentials locally: %v\n", err)
		}

		fmt.Printf("Login successful! Logged in as %s\n", email)
		if apiKey != "" && apiKey != token {
			fmt.Println("New CLI API key configured in ~/.compute/config.json")
		}

	case "signup":
		signupCmd := flag.NewFlagSet("auth signup", flag.ExitOnError)
		email := signupCmd.String("email", "", "User email")
		username := signupCmd.String("username", "", "Username")
		name := signupCmd.String("name", "", "Display name")
		password := signupCmd.String("password", "", "Password")
		signupCmd.Parse(subArgs)

		if *email == "" || *username == "" || *password == "" {
			fmt.Fprintln(os.Stderr, "Usage: compute auth signup --email <email> --username <username> --password <pw> [--name <name>]")
			os.Exit(1)
		}

		if *name == "" {
			*name = *username
		}

		if err := client.Signup(ctx, *email, *username, *name, *password); err != nil {
			fmt.Fprintf(os.Stderr, "Signup failed: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Signup successful for %s! You can now log in with: compute auth login\n", *email)

	case "whoami", "me":
		me, err := client.GetMe(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v (Are you logged in?)\n", err)
			os.Exit(1)
		}

		userObj, _ := me["user"].(map[string]interface{})
		fmt.Println("Current User:")
		if userObj != nil {
			fmt.Printf("  ID:           %v\n", userObj["id"])
			fmt.Printf("  Email:        %v\n", userObj["email"])
			fmt.Printf("  Username:     %v\n", userObj["username"])
			fmt.Printf("  Display Name: %v\n", userObj["display_name"])
			fmt.Printf("  System Role:  %v\n", userObj["role"])
		}
		if orgID, ok := me["org_id"].(string); ok && orgID != "" {
			fmt.Printf("  Org ID:       %s\n", orgID)
		}
		if role, ok := me["role"].(string); ok && role != "" {
			fmt.Printf("  Org Role:     %s\n", role)
		}

	case "keys":
		if len(subArgs) == 0 || subArgs[0] == "list" || subArgs[0] == "ls" {
			keys, err := client.ListAPIKeys(ctx)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error listing API keys: %v\n", err)
				os.Exit(1)
			}
			if len(keys) == 0 {
				fmt.Println("No API keys found. Create one with: compute auth keys create --name <name>")
				return
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
			fmt.Fprintln(w, "ID\tNAME\tKEY PREFIX\tCREATED AT")
			for _, k := range keys {
				prefix := ""
				if p, ok := k["prefix"].(string); ok {
					prefix = p
				} else if p, ok := k["key_prefix"].(string); ok {
					prefix = p
				}
				fmt.Fprintf(w, "%v\t%v\t%s...\t%v\n", k["id"], k["name"], prefix, k["created_at"])
			}
			w.Flush()
			return
		}

		keyAction := subArgs[0]
		switch keyAction {
		case "create":
			createCmd := flag.NewFlagSet("auth keys create", flag.ExitOnError)
			name := createCmd.String("name", "cli-key", "Descriptive key name")
			createCmd.Parse(subArgs[1:])

			key, err := client.CreateAPIKey(ctx, *name, 0)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to create API key: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("API Key Created Successfully!")
			fmt.Printf("Key: %s\n", key)
			fmt.Println("Save this key securely. It will not be shown again.")

		case "revoke", "delete", "rm":
			if len(subArgs) < 2 {
				fmt.Fprintln(os.Stderr, "Usage: compute auth keys revoke <key-id>")
				os.Exit(1)
			}
			keyID := subArgs[1]
			if err := client.RevokeAPIKey(ctx, keyID); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to revoke key: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("API key %s revoked.\n", keyID)

		default:
			fmt.Fprintf(os.Stderr, "Unknown keys action: %s\n", keyAction)
			os.Exit(1)
		}

	case "logout":
		home, _ := os.UserHomeDir()
		cfgFile := filepath.Join(home, ".compute", "config.json")
		_ = os.Remove(cfgFile)
		fmt.Println("Logged out successfully. Removed credentials.")

	default:
		fmt.Fprintf(os.Stderr, "Unknown auth command '%s'. Available: login, signup, whoami, keys, logout\n", sub)
		os.Exit(1)
	}
}
