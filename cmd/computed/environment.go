package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/db"
	"github.com/Rashid2G/on-demand-vms/internal/vm"
)

// applyEnvironment injects the selected environment profile into a freshly
// booted sandbox: secret variables become ~/.compute-env (sourced by bash), and
// enabled GitHub repositories are cloned into /workspace in the background.
func (s *Server) applyEnvironment(ctx context.Context, v *db.VM) {
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil || v == nil {
		return
	}
	envs, err := s.vmMgr.GetStore().ListEnvironments(ctx)
	if err != nil || len(envs) == 0 {
		return
	}
	var profile *db.EnvironmentProfile
	for _, candidate := range envs {
		if candidate.IsDefault {
			profile = candidate
			break
		}
	}
	if profile == nil {
		profile = envs[0]
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
			for _, repo := range repos {
				repo = strings.TrimSpace(repo)
				repo = strings.TrimSuffix(repo, ".git")
				if repo == "" || strings.ContainsAny(repo, " \t\n;'\"") {
					continue
				}
				parts := strings.Split(repo, "/")
				name := parts[len(parts)-1]
				cmd := fmt.Sprintf(
					`sudo mkdir -p /workspace && sudo chown compute:compute /workspace && [ -d /workspace/%s ] || git clone --depth 1 https://github.com/%s.git /workspace/%s`,
					name, repo, name,
				)
				res, err := s.vmMgr.ExecVM(cloneCtx, v.ID, vm.ExecRequest{Command: cmd, User: "compute", Timeout: 120})
				if err != nil || (res != nil && res.ExitCode != 0) {
					detail := ""
					if res != nil {
						detail = strings.TrimSpace(res.Stderr)
					}
					log.Printf("[computed] environment %q: clone %s failed: %v %s", profile.Name, repo, err, detail)
				}
			}
		}()
	}
}
