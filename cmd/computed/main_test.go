package main

import (
	"strings"
	"testing"

	"github.com/Rashid2G/compute/internal/config"
)

func TestIsLoopbackListen(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{addr: "127.0.0.1:18080", want: true},
		{addr: "127.0.1.1:18080", want: true},
		{addr: "[::1]:18080", want: true},
		{addr: "localhost:18080", want: true},
		{addr: "unix:///run/compute.sock", want: true},
		{addr: "0.0.0.0:18080", want: false},
		{addr: ":18080", want: false},
		{addr: "[::]:18080", want: false},
		{addr: "192.168.1.50:18080", want: false},
		{addr: "", want: false},
	}
	for _, tc := range cases {
		if got := isLoopbackListen(tc.addr); got != tc.want {
			t.Errorf("isLoopbackListen(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

func TestValidateStartupConfig(t *testing.T) {
	base := func() config.GlobalConfig {
		cfg := config.DefaultConfig()
		cfg.Server.AuthEnabled = true
		cfg.Server.ListenAddr = "127.0.0.1:18080"
		cfg.Server.AdminPassword = "admin"
		cfg.Server.AdminSecret = ""
		return cfg
	}

	t.Run("loopback allows default admin password", func(t *testing.T) {
		for _, addr := range []string{"127.0.0.1:18080", "localhost:18080", "[::1]:18080", "unix:///run/compute.sock"} {
			cfg := base()
			cfg.Server.ListenAddr = addr
			if err := validateStartupConfig(cfg); err != nil {
				t.Fatalf("expected loopback %q to allow default admin password, got %v", addr, err)
			}
		}
	})

	t.Run("non-loopback rejects default admin password", func(t *testing.T) {
		for _, addr := range []string{"0.0.0.0:18080", ":18080", "[::]:18080", "10.0.0.5:18080"} {
			cfg := base()
			cfg.Server.ListenAddr = addr
			err := validateStartupConfig(cfg)
			if err == nil || !strings.Contains(err.Error(), "default admin password") {
				t.Fatalf("expected default password error on %q, got %v", addr, err)
			}
		}
	})

	t.Run("non-loopback rejects default admin password even when admin secret is also set", func(t *testing.T) {
		cfg := base()
		cfg.Server.ListenAddr = "0.0.0.0:18080"
		cfg.Server.AdminSecret = "super-secret-token"
		err := validateStartupConfig(cfg)
		if err == nil || !strings.Contains(err.Error(), "default admin password") {
			t.Fatalf("expected default password error when AdminSecret is also set, got %v", err)
		}
	})

	t.Run("non-loopback allows strong password", func(t *testing.T) {
		cfg := base()
		cfg.Server.ListenAddr = "0.0.0.0:18080"
		cfg.Server.AdminPassword = "strong-random-password-123"
		if err := validateStartupConfig(cfg); err != nil {
			t.Fatalf("expected strong password to be allowed on non-loopback, got %v", err)
		}
	})

	t.Run("non-loopback allows empty password when admin secret is set", func(t *testing.T) {
		cfg := base()
		cfg.Server.ListenAddr = "0.0.0.0:18080"
		cfg.Server.AdminPassword = ""
		cfg.Server.AdminSecret = "super-secret-token"
		if err := validateStartupConfig(cfg); err != nil {
			t.Fatalf("expected empty password with AdminSecret to be allowed, got %v", err)
		}
	})

	t.Run("auth enabled with neither password nor secret fails", func(t *testing.T) {
		cfg := base()
		cfg.Server.AdminPassword = ""
		cfg.Server.AdminSecret = ""
		if err := validateStartupConfig(cfg); err == nil {
			t.Fatal("expected error when AuthEnabled=true and no admin credentials are configured")
		}
	})

	t.Run("auth disabled allows default password on any address", func(t *testing.T) {
		cfg := base()
		cfg.Server.AuthEnabled = false
		cfg.Server.ListenAddr = "0.0.0.0:18080"
		if err := validateStartupConfig(cfg); err != nil {
			t.Fatalf("expected AuthEnabled=false to skip credential checks, got %v", err)
		}
	})

	t.Run("mismatched TLS cert and key fails", func(t *testing.T) {
		cfg := base()
		cfg.Server.TLSCertFile = "/etc/compute/cert.pem"
		if err := validateStartupConfig(cfg); err == nil {
			t.Fatal("expected error when only TLSCertFile is set")
		}
	})
}
