package main

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/vm"
)

var validRepoRegex = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// validateRepoSlug verifies that a GitHub repo slug strictly matches owner/repo
// without directory traversal, shell metacharacters, or flag options.
func validateRepoSlug(repo string) (owner string, name string, ok bool) {
	repo = strings.TrimSpace(repo)
	repo = strings.TrimSuffix(repo, ".git")
	if !validRepoRegex.MatchString(repo) {
		return "", "", false
	}
	if strings.Contains(repo, "..") {
		return "", "", false
	}
	parts := strings.Split(repo, "/")
	if len(parts) != 2 {
		return "", "", false
	}
	owner, name = parts[0], parts[1]
	if strings.HasPrefix(owner, "-") || strings.HasPrefix(name, "-") {
		return "", "", false
	}
	if owner == "." || owner == ".." || name == "." || name == ".." {
		return "", "", false
	}
	return owner, name, true
}

// applyEnvironment injects the selected environment profile into a freshly
// booted sandbox: secret variables become ~/.compute-env (sourced by bash), and
// enabled GitHub repositories are cloned into /workspace in the background.
func (s *Server) applyEnvironment(ctx context.Context, v *db.VM) {
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil || v == nil {
		return
	}
	st := s.vmMgr.GetStore()
	envs, err := st.ListEnvironmentsByOwner(ctx, v.OwnerID)
	if err != nil || len(envs) == 0 {
		return
	}
	var profile *db.EnvironmentProfile
	for _, candidate := range envs {
		if candidate.OwnerID == v.OwnerID && candidate.IsDefault {
			profile = candidate
			break
		}
	}
	if profile == nil {
		for _, candidate := range envs {
			if candidate.OwnerID == v.OwnerID {
				profile = candidate
				break
			}
		}
	}
	// Strictly enforce tenant boundary: never fall back to another tenant's environment.
	if profile == nil || (v.OwnerID != "" && profile.OwnerID != v.OwnerID) {
		return
	}

	if profile.SecretsEnabled && strings.TrimSpace(profile.Secrets) != "" {
		script := `umask 077
cat > "$HOME/.compute-env"
grep -q '.compute-env' "$HOME/.bashrc" 2>/dev/null || printf '\n[ -f "$HOME/.compute-env" ] && . "$HOME/.compute-env"\n' >> "$HOME/.bashrc"
`
		res, err := s.vmMgr.ExecVMInput(ctx, v.ID, vm.ExecRequest{
			Command: script,
			User:    "compute",
			Timeout: 20,
		}, strings.NewReader(profile.Secrets+"\n"))
		if err != nil || (res != nil && res.ExitCode != 0) {
			detail := ""
			if res != nil {
				detail = res.Stderr
			}
			log.Printf("[computed] environment %q: could not write secrets to %s: %v %s", profile.Name, v.Name, err, detail)
		}
	}

	if profile.GitHubReposEnabled && len(profile.GitHubRepos) > 0 {
		repos := append([]string(nil), profile.GitHubRepos...)
		go func() {
			cloneCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			for _, rawRepo := range repos {
				owner, name, ok := validateRepoSlug(rawRepo)
				if !ok {
					log.Printf("[computed] environment %q: rejecting invalid or unsafe repo slug %q", profile.Name, rawRepo)
					continue
				}
				slug := owner + "/" + name
				cmd := fmt.Sprintf(
					`sh -c 'sudo mkdir -p /workspace && sudo chown compute:compute /workspace && [ -d "/workspace/$2" ] || git clone --depth 1 -- "https://github.com/$1.git" "/workspace/$2"' _ %q %q`,
					slug, name,
				)
				res, err := s.vmMgr.ExecVM(cloneCtx, v.ID, vm.ExecRequest{Command: cmd, User: "compute", Timeout: 120})
				if err != nil || (res != nil && res.ExitCode != 0) {
					detail := ""
					if res != nil {
						detail = strings.TrimSpace(res.Stderr)
					}
					log.Printf("[computed] environment %q: clone %s failed: %v %s", profile.Name, slug, err, detail)
				}
			}
		}()
	}
}
