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
