package network

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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

func gzipBody(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zlibBody(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const previewTestPage = `<!doctype html><html><head><script src="/app.js"></script></head><body>ok</body></html>`

func runPrefixWriter(t *testing.T, encoding string, body []byte) *http.Response {
	t.Helper()
	rec := httptest.NewRecorder()
	hw := newHTMLPrefixWriter(rec, "/preview/vm/8080/")
	hw.Header().Set("Content-Type", "text/html; charset=utf-8")
	if encoding != "" {
		hw.Header().Set("Content-Encoding", encoding)
	}
	hw.Header().Set("Content-Length", strconv.Itoa(len(body)))
	hw.WriteHeader(http.StatusOK)
	if _, err := hw.Write(body); err != nil {
		t.Fatal(err)
	}
	hw.finalize()
	return rec.Result()
}

// A compressing upstream must never leave the client with compressed bytes
// labelled as plain HTML. Regression for the preview URL that rendered binary
// garbage in the browser.
func TestHTMLPrefixWriterDecodesGzipHTML(t *testing.T) {
	res := runPrefixWriter(t, "gzip", gzipBody(t, previewTestPage))
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if enc := res.Header.Get("Content-Encoding"); enc != "" {
		t.Fatalf("Content-Encoding must be cleared after decoding, got %q", enc)
	}
	if !strings.HasPrefix(string(body), "<!doctype html>") {
		t.Fatalf("expected decoded HTML, got %q", string(body[:min(len(body), 64)]))
	}
	if !strings.Contains(string(body), `<base href="/preview/vm/8080/">`) {
		t.Errorf("expected base href injection, got:\n%s", body)
	}
	if !strings.Contains(string(body), `src="/preview/vm/8080/app.js"`) {
		t.Errorf("expected asset path rewrite, got:\n%s", body)
	}
	if got := res.Header.Get("Content-Length"); got != strconv.Itoa(len(body)) {
		t.Errorf("Content-Length = %q, want %d", got, len(body))
	}
}

func TestHTMLPrefixWriterDecodesDeflateHTML(t *testing.T) {
	res := runPrefixWriter(t, "deflate", zlibBody(t, previewTestPage))
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), `<base href="/preview/vm/8080/">`) {
		t.Fatalf("expected rewritten deflate HTML, got %q", string(body[:min(len(body), 64)]))
	}
	if enc := res.Header.Get("Content-Encoding"); enc != "" {
		t.Fatalf("Content-Encoding must be cleared after decoding, got %q", enc)
	}
}

// Encodings the writer cannot decode must be forwarded byte-for-byte with
// their Content-Encoding intact instead of being silently corrupted.
func TestHTMLPrefixWriterPassesThroughUnsupportedEncoding(t *testing.T) {
	raw := []byte{0x1b, 0x2f, 0x00, 0xff, 0x80}
	res := runPrefixWriter(t, "br", raw)
	body, _ := io.ReadAll(res.Body)
	if res.Header.Get("Content-Encoding") != "br" {
		t.Errorf("Content-Encoding = %q, want br", res.Header.Get("Content-Encoding"))
	}
	if !bytes.Equal(body, raw) {
		t.Errorf("body was altered: %x, want %x", body, raw)
	}
}

func TestHTMLPrefixWriterRewritesIdentityHTML(t *testing.T) {
	res := runPrefixWriter(t, "", []byte(previewTestPage))
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), `<base href="/preview/vm/8080/">`) {
		t.Fatalf("expected rewritten identity HTML, got:\n%s", body)
	}
}

func TestAcceptsRewritableHTML(t *testing.T) {
	for _, accept := range []string{
		"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"*/*",
		"application/json, text/plain, */*",
		"",
	} {
		if !acceptsRewritableHTML(accept) {
			t.Errorf("acceptsRewritableHTML(%q) = false, want true", accept)
		}
	}
	if acceptsRewritableHTML("application/json") {
		t.Error("explicit non-HTML Accept must keep client compression negotiation")
	}
}

func TestDecodeHTMLBodyRejectsUnsupportedEncoding(t *testing.T) {
	if _, err := decodeHTMLBody([]byte("x"), "br"); err == nil {
		t.Fatal("expected error for unsupported content encoding")
	}
	if got, err := decodeHTMLBody([]byte("plain"), ""); err != nil || string(got) != "plain" {
		t.Fatalf("identity decode = %q, %v", got, err)
	}
}
