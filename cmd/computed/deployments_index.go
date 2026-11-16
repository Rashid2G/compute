package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Rashid2G/compute/internal/db"
)

func deploymentsIndexEnabled() bool {
	v := strings.TrimSpace(os.Getenv("COMPUTE_DEPLOYMENTS_INDEX"))
	if v == "" {
		return true
	}
	return !strings.EqualFold(v, "false") && v != "0" && !strings.EqualFold(v, "no") && !strings.EqualFold(v, "off")
}

// handleDeploymentsIndex serves the preview landing page at the daemon root.
// It lists the main (dev) GUI plus every Docker preview manifest and sandbox,
// so reviewers can pick a deployment without knowing preview ports or names.
//
// The page is intentionally unauthenticated: it only exposes deployment names,
// owners/branches, and status (no data), and the previews it links to still
// enforce their own authorization rules.
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

	// Link each sandbox to its published serve origin (prefer port 8080).
	serveURLs := map[string]string{}
	if pfs, err := s.vmMgr.GetStore().ListPortForwards(r.Context()); err == nil {
		for _, pf := range pfs {
			if pf == nil || pf.PublicURL == "" {
				continue
			}
			if cur, ok := serveURLs[pf.VMID]; !ok || (pf.GuestPort == 8080 && cur != "") {
				serveURLs[pf.VMID] = pf.PublicURL
			}
		}
	}

	// Docker-based preview stacks register themselves with a manifest file in
	// <db dir>/../previews/<name>.json (the deployed checkout's ./data/previews).
	previews := loadPreviewManifests(previewsDirFor(s.cfg.Server.DBPath))

	// When the daemon hosts the embedded Web GUI, the app shell is still
	// reachable at /index.html (the homepage owns "/"). Otherwise point at the
	// standalone GUI service on the host's port 8080.
	guiURL := "/index.html"
	if s.guiHandler == nil {
		guiURL = fmt.Sprintf("http://%s:8080/", requestHostOnly(r))
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = fmt.Fprint(w, renderDeploymentsPage(vms, guiURL, previews, serveURLs))
}

// previewsDirFor derives the preview manifest directory from the daemon's DB
// path: the data root's previews/ dir (grandparent of the db file, so the
// default /var/lib/compute/data/compute.db maps to /var/lib/compute/previews,
// which is the deployed checkout's ./data/previews on the host).
func previewsDirFor(dbPath string) string {
	if strings.TrimSpace(dbPath) == "" {
		dbPath = "/var/lib/compute/data/compute.db"
	}
	return filepath.Join(filepath.Dir(filepath.Dir(dbPath)), "previews")
}

// previewManifest describes a docker-based preview deployment, written by
// scripts/preview-docker.sh at deploy time.
type previewManifest struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Port    int    `json:"port"`
	Branch  string `json:"branch"`
	Commit  string `json:"commit"`
	Created string `json:"created"`
}

// loadPreviewManifests reads all preview manifests; failures are non-fatal
// (a missing or unreadable dir simply yields no docker previews).
func loadPreviewManifests(dir string) []*previewManifest {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []*previewManifest
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var m previewManifest
		if json.Unmarshal(data, &m) != nil || m.Name == "" || m.URL == "" {
			continue
		}
		out = append(out, &m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func requestHostOnly(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	return host
}

// renderDeploymentsPage builds the deployments landing page HTML. It is a pure
// function of the sandbox list, the main GUI URL, and the docker preview
// manifests so it can be tested without a live server.
func renderDeploymentsPage(vms []*db.VM, guiURL string, previews []*previewManifest, serveURLs map[string]string) string {
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
a.card:hover{border-color:#d94e1f}
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
<p class="sub">Live deployments on this host. Pick one to open it.</p>
`)
	b.WriteString(fmt.Sprintf(`<a class="card" href="%s"><div><div class="name">dev</div><div class="meta">main branch deployment — Web UI dashboard</div></div><span class="badge running">main</span></a>`, htmlEscape(guiURL)))

	for _, p := range previews {
		branch := p.Branch
		if p.Commit != "" {
			branch = strings.TrimSpace(branch + " @ " + p.Commit)
		}
		b.WriteString(fmt.Sprintf(`<a class="card" href="%s"><div><div class="name">%s</div><div class="meta">docker preview — %s</div></div><span class="badge running">preview</span></a>`,
			htmlEscape(p.URL), htmlEscape(p.Name), htmlEscape(branch)))
	}

	if len(vms) == 0 && len(previews) == 0 {
		b.WriteString(`<p class="sub" style="margin-top:24px">No previews or sandboxes found.</p>`)
	}
	for _, v := range vms {
		status := string(v.Status)
		if v.Status == db.StatusRunning {
			url := serveURLs[v.ID]
			if url == "" {
				b.WriteString(fmt.Sprintf(`<div class="card" style="opacity:.7"><div><div class="name">%s</div><div class="meta">sandbox — owner %s (no published port)</div></div><span class="badge running">%s</span></div>`,
					htmlEscape(v.Name), htmlEscape(v.OwnerID), htmlEscape(status)))
				continue
			}
			b.WriteString(fmt.Sprintf(`<a class="card" href="%s"><div><div class="name">%s</div><div class="meta">sandbox serve — owner %s</div></div><span class="badge running">%s</span></a>`,
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
