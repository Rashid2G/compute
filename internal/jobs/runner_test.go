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
