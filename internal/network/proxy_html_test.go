package network

import (
	"strings"
	"testing"
)

func TestRewriteHTMLForPrefix(t *testing.T) {
	in := `<!doctype html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>Compute</title>
<script type="module" crossorigin src="/app.js"></script>
<link rel="stylesheet" crossorigin href="/app.css">
<link rel="icon" href="/favicon.svg">
</head>
<body><div id="root"></div></body>
</html>`

	out := string(rewriteHTMLForPrefix([]byte(in), "/preview/pr-42/8080/"))

	// Absolute asset paths are rewritten to the preview prefix.
	for _, want := range []string{
		`src="/preview/pr-42/8080/app.js"`,
		`href="/preview/pr-42/8080/app.css"`,
		`href="/preview/pr-42/8080/favicon.svg"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected rewritten asset path %q in output:\n%s", want, out)
		}
	}

	// A <base href> is injected right after <head>.
	if !strings.Contains(out, `<head><base href="/preview/pr-42/8080/">`) {
		t.Errorf("expected <base href> injection after <head>, got:\n%s", out)
	}

	// The runtime patch redirects absolute API calls.
	if !strings.Contains(out, `P="/preview/pr-42/8080/"`) {
		t.Errorf("expected runtime patch with prefix, got:\n%s", out)
	}
	if !strings.Contains(out, "EventSource") || !strings.Contains(out, "WebSocket") {
		t.Errorf("expected EventSource/WebSocket patch in output:\n%s", out)
	}
}

func TestRewriteHTMLForPrefix_PreservesExternalURLs(t *testing.T) {
	in := `<html><head><link href="https://fonts.googleapis.com/css2?family=Inter"></head></html>`
	out := string(rewriteHTMLForPrefix([]byte(in), "/preview/vm/8080/"))
	if !strings.Contains(out, `href="https://fonts.googleapis.com/css2?family=Inter"`) {
		t.Errorf("external absolute URL must not be rewritten, got:\n%s", out)
	}
}

func TestRewriteHTMLForPrefix_NoHeadTag(t *testing.T) {
	in := `<html><body>hello</body></html>`
	out := string(rewriteHTMLForPrefix([]byte(in), "/preview/vm/8080/"))
	if !strings.HasPrefix(out, `<base href="/preview/vm/8080/">`) {
		t.Errorf("expected prepended base tag when no <head>, got:\n%s", out)
	}
}

func TestPreviewPrefixIsValid(t *testing.T) {
	cases := map[string]bool{
		"/preview/pr-42/8080":        true,
		"/preview/feat-my_branch/80": true,
		"/preview/a.b/8080":          true,
		"/preview/@alice/pr-42/8080": true,
		"/preview/<script>/8080":     false,
		"/preview/pr 42/8080":        false,
		"/preview//8080":             false,
		"/preview/ok/80 80":          false,
		"/preview/@alice/../8080":    false,
	}
	for in, want := range cases {
		if got := previewPrefixIsValid(in); got != want {
			t.Errorf("previewPrefixIsValid(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestRewriteHTMLForPrefixIsIdempotent(t *testing.T) {
	in := `<html><head><script src="/app.js"></script></head><body><a href="/docs">docs</a></body></html>`
	once := rewriteHTMLForPrefix([]byte(in), "/preview/@alice/web/8080/")
	twice := rewriteHTMLForPrefix(once, "/preview/@alice/web/8080/")

	// Re-processing an already rewritten document must not double-prefix URLs.
	if strings.Contains(string(twice), "/preview/@alice/web/8080/preview/") {
		t.Fatalf("prefix was applied twice:\n%s", twice)
	}
	for _, want := range []string{
		`src="/preview/@alice/web/8080/app.js"`,
		`href="/preview/@alice/web/8080/docs"`,
	} {
		if !strings.Contains(string(twice), want) {
			t.Errorf("expected %q in output:\n%s", want, twice)
		}
	}
}

func TestRewriteHTMLForPrefixRewritesForms(t *testing.T) {
	in := `<html><head></head><body><form action="/login" method="post"></form></body></html>`
	out := string(rewriteHTMLForPrefix([]byte(in), "/preview/web/8080/"))
	if !strings.Contains(out, `action="/preview/web/8080/login"`) {
		t.Errorf("form action should be rewritten, got:\n%s", out)
	}
}

func TestRewriteRedirectLocation(t *testing.T) {
	const prefix = "/preview/@alice/web/8080"
	cases := map[string]string{
		"/login":                    prefix + "/login",
		"/":                         prefix + "/",
		prefix + "/already":         prefix + "/already",
		"https://example.com/login": "https://example.com/login",
		"//cdn.example.com/app.js":  "//cdn.example.com/app.js",
		"relative/path":             "relative/path",
		"":                          "",
	}
	for in, want := range cases {
		if got := rewriteRedirectLocation(in, prefix); got != want {
			t.Errorf("rewriteRedirectLocation(%q) = %q, want %q", in, got, want)
		}
	}
	if got := rewriteRedirectLocation("/login", ""); got != "/login" {
		t.Errorf("empty prefix must leave location untouched, got %q", got)
	}
}
