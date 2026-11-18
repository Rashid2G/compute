package network

import (
	"strings"
	"testing"
	"time"
)

func TestServeTokenRoundTrip(t *testing.T) {
	key := []byte("test-key")
	token := SignServeToken(key, "pf_1", "user_1", time.Hour)
	id, owner, err := VerifyServeToken(key, token)
	if err != nil {
		t.Fatalf("VerifyServeToken: %v", err)
	}
	if id != "pf_1" || owner != "user_1" {
		t.Fatalf("scope = %q/%q", id, owner)
	}
}

func TestServeTokenRejectsTamperingAndExpiry(t *testing.T) {
	key := []byte("test-key")
	token := SignServeToken(key, "pf_1", "user_1", time.Hour)
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		t.Fatalf("unexpected token format %q", token)
	}
	if _, _, err := VerifyServeToken(key, parts[0]+"."+parts[1][:len(parts[1])-2]+"aa"); err == nil {
		t.Fatal("tampered signature should be rejected")
	}
	if _, _, err := VerifyServeToken([]byte("other-key"), token); err == nil {
		t.Fatal("wrong key should be rejected")
	}

	expired := signServeTokenExpiring(key, "pf_1", "user_1", time.Now().Add(-time.Hour))
	if _, _, err := VerifyServeToken(key, expired); err == nil {
		t.Fatal("expired token should be rejected")
	}
}

func TestServeTokenKeyDerivation(t *testing.T) {
	if got := ServeTokenKey("explicit", "admin"); string(got) != "explicit" {
		t.Fatalf("explicit key = %q", got)
	}
	a := ServeTokenKey("", "admin")
	b := ServeTokenKey("", "admin")
	if len(a) == 0 || string(a) != string(b) {
		t.Fatal("admin-derived key should be stable")
	}
	if c := ServeTokenKey("", ""); len(c) == 0 {
		t.Fatal("fallback key must not be empty")
	}
}
