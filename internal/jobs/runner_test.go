package jobs

import (
	"strings"
	"testing"

	"github.com/Rashid2G/compute/pkg/api"
)

func TestBuildJobCommand(t *testing.T) {
	got := buildJobCommand("/workspace", api.JobSpec{
		Command: []string{"python3", "test.py"},
		Env:     map[string]string{"MODE": "test"},
	})
	if !strings.Contains(got, "cd '/workspace'") {
		t.Fatalf("command missing working directory: %s", got)
	}
	if !strings.Contains(got, "export MODE='test';") {
		t.Fatalf("command missing environment: %s", got)
	}
	if !strings.HasSuffix(got, "python3 test.py") {
		t.Fatalf("command missing payload: %s", got)
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("it's fine"); got != `'it'\''s fine'` {
		t.Fatalf("shellQuote = %s", got)
	}
}

func TestJobSandboxName(t *testing.T) {
	if got := jobSandboxName("job_ab12/cd34"); got != "job-job-ab12-cd34" {
		t.Fatalf("jobSandboxName = %s", got)
	}
}

func TestBuildJobCommandQuotesMultilineScript(t *testing.T) {
	script := "set -e\nmkdir -p /workspace\ngit clone \"$REPO_URL\" repo"
	got := buildJobCommand("/workspace", api.JobSpec{
		Command: []string{"bash", "-lc", script},
		Env:     map[string]string{"GITHUB_TOKEN": "ghp_secret123"},
	})
	wantSuffix := "bash -lc 'set -e\nmkdir -p /workspace\ngit clone \"$REPO_URL\" repo'"
	if !strings.HasSuffix(got, wantSuffix) {
		t.Fatalf("expected script to be single-quoted, got: %s", got)
	}
}

func TestBuildImageConfigCommand(t *testing.T) {
	cmd := buildImageConfigCommand([]string{"curl", "git,nodejs", "npm"}, "npm install -g pnpm\necho ready")
	if len(cmd) != 3 || cmd[0] != "bash" || cmd[1] != "-c" {
		t.Fatalf("unexpected cmd structure: %+v", cmd)
	}
	script := cmd[2]
	if !strings.Contains(script, "set -euo pipefail") {
		t.Fatalf("missing set -euo pipefail in %s", script)
	}
	if !strings.Contains(script, "apt-get update -y") {
		t.Fatalf("missing apt-get update in %s", script)
	}
	if !strings.Contains(script, "apt-get install -y --no-install-recommends 'curl' 'git' 'nodejs' 'npm'") {
		t.Fatalf("missing packages installation in %s", script)
	}
	if !strings.Contains(script, "npm install -g pnpm") {
		t.Fatalf("missing user script in %s", script)
	}
	if !strings.Contains(script, "sync") {
		t.Fatalf("missing sync in %s", script)
	}
}

func TestBuildImageConfigCommandEmpty(t *testing.T) {
	cmd := buildImageConfigCommand(nil, "")
	if len(cmd) != 3 {
		t.Fatalf("unexpected cmd len: %d", len(cmd))
	}
	script := cmd[2]
	if !strings.Contains(script, "apt-get update -y") {
		t.Fatalf("missing apt-get update in %s", script)
	}
	if strings.Contains(script, "apt-get install") {
		t.Fatalf("unexpected apt-get install when packages empty in %s", script)
	}
}

func TestBuildImageConfigCommand_DiverseScenarios(t *testing.T) {
	scenarios := []struct {
		name         string
		packages     []string
		script       string
		wantPackages []string
		wantInScript []string
	}{
		{
			name:         "only packages without script",
			packages:     []string{"python3-pip", "htop", "tmux"},
			script:       "",
			wantPackages: []string{"'python3-pip'", "'htop'", "'tmux'"},
			wantInScript: []string{"[1/2] Updating package repositories", "[2/2] Installing pre-configured packages"},
		},
		{
			name:         "only custom script without packages",
			packages:     nil,
			script:       "curl -fsSL https://get.pnpm.io/install.sh | sh -\nexport PNPM_HOME=\"$HOME/.local/share/pnpm\"",
			wantPackages: nil,
			wantInScript: []string{"[1/2] Updating package repositories", "[2/2] Running custom configuration script", "https://get.pnpm.io/install.sh"},
		},
		{
			name:         "comma separated and messy whitespace packages",
			packages:     []string{"  curl , git  ", "", "docker.io,  rustc ", " "},
			script:       "echo 'hello world'",
			wantPackages: []string{"'curl'", "'git'", "'docker.io'", "'rustc'"},
			wantInScript: []string{"[1/3] Updating package repositories", "[2/3] Installing pre-configured packages", "[3/3] Running custom configuration script", "echo 'hello world'"},
		},
		{
			name:         "special character quotes and multiline script",
			packages:     []string{"libssl-dev"},
			script:       "cat << 'EOF' > /etc/motd\nWelcome to custom image!\nEOF\nchmod 644 /etc/motd",
			wantPackages: []string{"'libssl-dev'"},
			wantInScript: []string{"Welcome to custom image!", "chmod 644 /etc/motd"},
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			cmd := buildImageConfigCommand(sc.packages, sc.script)
			if len(cmd) != 3 || cmd[0] != "bash" || cmd[1] != "-c" {
				t.Fatalf("unexpected command structure: %+v", cmd)
			}
			scriptText := cmd[2]
			if !strings.Contains(scriptText, "set -euo pipefail") {
				t.Fatalf("expected set -euo pipefail in script")
			}
			if !strings.Contains(scriptText, "export DEBIAN_FRONTEND=noninteractive") {
				t.Fatalf("expected DEBIAN_FRONTEND=noninteractive in script")
			}
			if !strings.Contains(scriptText, "sync") {
				t.Fatalf("expected sync in script")
			}
			for _, wp := range sc.wantPackages {
				if !strings.Contains(scriptText, wp) {
					t.Fatalf("missing expected package %s in script:\n%s", wp, scriptText)
				}
			}
			for _, ws := range sc.wantInScript {
				if !strings.Contains(scriptText, ws) {
					t.Fatalf("missing expected text %s in script:\n%s", ws, scriptText)
				}
			}
		})
	}
}
