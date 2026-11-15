---
name: github-issues
description: >
  Issue-driven development for on-demand-vms. Persist bugs, tasks, and follow-ups as GitHub issues
  so work can be resumed at any time, and tie branches/PRs to issues. Use when discovering a problem
  that is out of scope, planning multi-step work, tracking follow-ups, or asked to file/reopen/triage issues.
---

# GitHub Issue-Driven Workflow

Purpose: no lost work. Any problem, gap, or future task becomes a GitHub issue with enough
context to resume months later without re-investigation.

## When to file an issue
- You find a bug, flaky test, security concern, or missing behavior that is **out of scope** for the current task.
- A task is big enough to need milestones/subtasks.
- You defer something (known gap, follow-up, cleanup, docs).
- A user reports a problem but does not want it fixed immediately.
- A deploy/test failure needs tracking.

Do **not** file issues for work you are completing right now in the same task/PR.

## Creating an issue
```bash
gh issue create \
  --title "fix: <short imperative summary>" \
  --body-file /tmp/issue.md \
  --label bug \
  --assignee @me
```
Title prefixes mirror branch/commit scopes: `feat:`, `fix:`, `chore:`, `docs:`, `refactor:`, `perf:`, `test:`, `security:`.

### Required body template
```markdown
## Problem
What is broken/missing and the observed vs expected behavior.

## Reproduction
1. Steps / command
2. Environment (branch, commit SHA, host, daemon version)

## Suspected cause / affected areas
- file paths, packages, services

## Acceptance criteria
- [ ] concrete, testable outcome(s)
- [ ] test command(s) that must pass

## Notes / links
- related issues, PRs, logs (redact secrets)
```
Redact secrets, tokens, private keys, and env values.

## Linking work to issues
- Branch name includes the issue: `fix/42-queue-starvation`.
- PR body references it: `Closes #42` (only if the PR fully resolves it; `Closes #12, #34` for multiple) or `Refs #42`.
- **If the PR only partially satisfies the issue, leave a comment on the issue** describing what was done and what remains, and keep the issue open:
  ```bash
  gh issue comment <n> --body "Partial progress in #<pr>: <what changed>. Remaining: <gaps>. Kept open."
  ```
- Never claim an issue is fixed unless the PR demonstrably meets its acceptance criteria.

## Triage and lifecycle
- Labels: `bug`, `enhancement`, `docs`, `chore`, `security`, `good first issue`, `blocked`.
- Priority labels if used: `p1`–`p3`.
- State machine: open -> in progress (assignee + branch) -> in review (PR linked) -> closed on merge.
- Reopen with a comment explaining why instead of filing a duplicate.

## Resuming work later
Before starting, `gh issue view <n> --comments` to recover context. Update the issue with new
findings instead of starting a fresh one.

## Listing and searching
```bash
gh issue list --state open
gh issue list --label bug --assignee @me
gh issue list --search "queue starvation"
gh issue view <n>
```

## Automating in PRs
- All agent PRs target `dev`. Use `Closes #n` when the PR fully satisfies the issue's acceptance criteria (unless the user explicitly asks to keep it open until promotion to `main`); otherwise use `Refs #n` and leave a progress comment.

## Rules
- Never close an issue without a linked commit/PR or a written reason.
- Never put credentials in issue bodies or comments.
- Keep one issue per distinct problem; use task lists for subtasks.
