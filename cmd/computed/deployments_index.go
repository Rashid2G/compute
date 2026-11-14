package main

import (
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"

	"github.com/Rashid2G/on-demand-vms/internal/db"
)

// handleDeploymentsIndex serves the preview landing page at the daemon root.
// It lists the main (dev) GUI plus every sandbox with a link to its
// path-based preview URL, so reviewers can pick a deployment without knowing
// sandbox names or PR numbers.
//
// The page is intentionally unauthenticated: it only exposes sandbox names,
// owners, and status (no data), and the previews it links to still enforce
// their own private/public authorization rules. Register it on the catch-all
// "/" mux pattern; anything other than the index paths gets a 404.
func (s *Server) handleDeploymentsIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/deployments" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	vms, err := s.vmMgr.GetStore().ListVMs(r.Context())
	if err != nil {
		http.Error(w, "failed to list sandboxes: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// The main deployment's GUI listens on the lab host at port 8080.
	guiURL := fmt.Sprintf("http://%s:8080/", requestHostOnly(r))

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = fmt.Fprint(w, renderDeploymentsPage(vms, guiURL))
}

func requestHostOnly(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	return host
}

// renderDeploymentsPage builds the deployments landing page HTML. It is a pure
// function of the sandbox list so it can be tested without a live server.
func renderDeploymentsPage(vms []*db.VM, guiURL string) string {
	sort.Slice(vms, func(i, j int) bool {
		if (vms[i].Status == db.StatusRunning) != (vms[j].Status == db.StatusRunning) {
			return vms[i].Status == db.StatusRunning
		}
		return strings.ToLower(vms[i].Name) < strings.ToLower(vms[j].Name)
	})

	var b strings.Builder
	b.WriteString(`<!doctype html>
<html lang="en" class="dark">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<meta name="theme-color" content="#060606">
<title>Deployments — Compute</title>
<style>
:root{color-scheme:dark}
body{margin:0;background:#060606;color:#e7e9ee;font-family:Inter,system-ui,sans-serif}
main{max-width:760px;margin:0 auto;padding:48px 24px}
h1{font-family:"Funnel Display",Inter,sans-serif;font-weight:500;font-size:28px;margin:0 0 4px}
p.sub{color:#8a919e;margin:0 0 32px;font-size:14px}
a.card{display:flex;justify-content:space-between;align-items:center;gap:16px;text-decoration:none;color:inherit;
  border:1px solid #1d2330;border-radius:10px;padding:14px 18px;margin-bottom:10px;background:#0b0e14;transition:border-color .15s}
a.card:hover{border-color:#263fa9}
.card .name{font-family:"IBM Plex Mono",monospace;font-size:15px}
.card .meta{color:#8a919e;font-size:12px;margin-top:2px}
.badge{font-size:11px;padding:3px 10px;border-radius:999px;border:1px solid #2a3140;color:#8a919e;white-space:nowrap}
.badge.running{color:#7ce38b;border-color:#1f4633}
.badge.stopped{color:#8a919e}
</style>
</head>
<body>
<main>
<h1>Deployments</h1>
<p class="sub">Live deployments on this host. Previews run in isolated sandboxes; pick one to open it.</p>
`)
	b.WriteString(fmt.Sprintf(`<a class="card" href="%s"><div><div class="name">dev</div><div class="meta">main branch deployment — Web UI dashboard</div></div><span class="badge running">main</span></a>`, htmlEscape(guiURL)))

	if len(vms) == 0 {
		b.WriteString(`<p class="sub" style="margin-top:24px">No sandboxes found.</p>`)
	}
	for _, v := range vms {
		status := string(v.Status)
		if v.Status == db.StatusRunning {
			url := fmt.Sprintf("/preview/%s/8080/", v.Name)
			b.WriteString(fmt.Sprintf(`<a class="card" href="%s"><div><div class="name">%s</div><div class="meta">sandbox preview — owner %s</div></div><span class="badge running">%s</span></a>`,
				htmlEscape(url), htmlEscape(v.Name), htmlEscape(v.OwnerID), htmlEscape(status)))
		} else {
			b.WriteString(fmt.Sprintf(`<div class="card" style="opacity:.55"><div><div class="name">%s</div><div class="meta">sandbox — owner %s</div></div><span class="badge stopped">%s</span></div>`,
				htmlEscape(v.Name), htmlEscape(v.OwnerID), htmlEscape(status)))
		}
	}
	b.WriteString("</main>\n</body>\n</html>\n")
	return b.String()
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;")
	return r.Replace(s)
}
