// Package dns implements the authoritative DNS responder that gives every
// sandbox a stable, routable hostname of the form
//
//	<sandbox>.<username>.<base-domain>
//
// SSH connections carry no hostname (there is no SNI or Host header), so the
// wildcard dispatch for SSH happens here: the DNS layer resolves each sandbox
// name directly to the sandbox's own address (tailnet IP first, guest IP
// second) and the network does the routing. This works for any base domain —
// Tailscale split DNS, any DNS provider's delegation, or a LAN resolver — as
// long as queries for the zone reach the daemon.
package dns

import (
	"strings"
	"unicode"
)

// MaxLabelLength is the maximum length of a single DNS label.
const MaxLabelLength = 63

// SanitizeLabel converts an arbitrary string into a valid DNS label:
// lowercased ASCII, invalid characters replaced with '-', trimmed of leading
// and trailing dashes, and capped at maxLen characters. An empty result means
// the input had no usable characters.
func SanitizeLabel(s string, maxLen int) string {
	if maxLen <= 0 || maxLen > MaxLabelLength {
		maxLen = MaxLabelLength
	}
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-':
			b.WriteRune('-')
		default:
			b.WriteRune('-')
		}
	}
	out := b.String()
	// Collapse runs of dashes and trim the edges.
	var collapsed strings.Builder
	prevDash := false
	for i := 0; i < len(out); i++ {
		d := out[i] == '-'
		if d && prevDash {
			continue
		}
		collapsed.WriteByte(out[i])
		prevDash = d
	}
	out = strings.Trim(collapsed.String(), "-")
	// Some Unicode inputs (e.g. "café") may leave stray letters; drop anything
	// non-ASCII that survived.
	if !isASCII(out) {
		var clean strings.Builder
		for _, r := range out {
			if r < unicode.MaxASCII {
				clean.WriteRune(r)
			}
		}
		out = clean.String()
	}
	if len(out) > maxLen {
		out = strings.TrimRight(out[:maxLen], "-")
	}
	return out
}

// ValidLabel reports whether s is already a valid, DNS-safe label.
func ValidLabel(s string) bool {
	if s == "" || len(s) > MaxLabelLength {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-':
			if i == 0 || i == len(s)-1 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 0x7f {
			return false
		}
	}
	return true
}
