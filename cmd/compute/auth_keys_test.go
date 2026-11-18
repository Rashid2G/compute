package main

import (
	"encoding/json"
	"testing"
)

func TestAPIKeyStatus(t *testing.T) {
	cases := []struct {
		name string
		raw  interface{}
		want string
	}{
		{name: "nil", raw: nil, want: "active"},
		{name: "empty string", raw: "", want: "active"},
		{name: "zero time", raw: "0001-01-01T00:00:00Z", want: "active"},
		{name: "zero number", raw: float64(0), want: "active"},
		{name: "json zero", raw: json.Number("0"), want: "active"},
		{name: "revoked timestamp", raw: "2026-11-18T10:00:00Z", want: "revoked"},
		{name: "revoked number", raw: float64(1763450000000), want: "revoked"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := apiKeyStatus(tc.raw); got != tc.want {
				t.Fatalf("apiKeyStatus(%v) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
