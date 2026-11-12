---
name: using-skills
description: >
  Meta skill: how agents must discover, load, and apply the repository skills in .agents/skills, and the
  mandatory rule for linking pull requests to GitHub issues (Closes when complete, progress comment when partial).
  Use at the start of every task and whenever you open or update a PR that references an issue.
---

# Using Repository Skills

## Discovery
- Repository skills live in `.agents/skills/<name>/SKILL.md` (some have `references/`).
- Load a skill with the `skill` tool when its `description` matches the task. Load before acting on anything the skill covers.
- Available skills (current):
  - `compute` — isolated microVM sandboxes, distributed build jobs, per-PR preview deployments, and recursive dogfooding.
  - `dev-workflow` — mandatory worktrees for code and non-simple tasks, `dev` branch, PR-to-`dev`, approval-gated `main`.
  - `github-issues` — filing, linking, triage, resuming issues.
  - `deployment` — deploy to `compute-lab`, health checks, remote logs.
  - `leader-evaluator-refinement` — multi-agent adversarial implementation/review.
  - `system-context-learning` — autonomous context learning, leaving operational context for other agents, safe skill updates.
  - `using-skills` — this file.
- Check `.agents/skills/` at task start in case new skills were added.
- When operational realities change (e.g., build locations, host IPs, ports), follow `system-context-learning` to update skills safely.
- A skill present in the conversation as `<skill_content>` is already loaded; do not reload it.

## Precedence
1. Explicit user instructions.
2. `AGENTS.md` (authoritative repo policy).
3. Loaded skill instructions (procedures/how-to).
4. General defaults.
If a skill conflicts with `AGENTS.md`, follow `AGENTS.md` and note the conflict.

## How to apply a skill
- Read the whole `SKILL.md` before starting; open `references/` files it points to when needed.
- Follow its checklists and templates; adapt paths/commands to the current checkout.
- Do not copy a skill's example values (hosts, SHAs) blindly — verify against the repo.
- If no skill covers a needed workflow, follow `AGENTS.md`; propose a new skill rather than editing unrelated ones.

## Pull request <-> issue linking (MANDATORY)
Every PR that references an issue must resolve it explicitly.

1. **PR fully satisfies the issue**
   - Put `Closes #<n>` in the PR body (or `Closes #12, #34` for several).
   - Merging then auto-closes the issue. This is the only acceptable way to close via PR.
2. **PR only partially satisfies the issue (or covers part of several)**
   - Use `Refs #<n>` in the PR body, keep the issue open.
   - **Leave a comment on the issue** describing exactly what was done, what remains, and links to the PR/commit:
     ```bash
     gh issue comment <n> --body "Partial progress in #<pr>: <what changed>. Remaining: <gaps>. Kept open."
     ```
3. **No issue yet** — if the work is out of scope or deferred, file one first (`github-issues` skill), then link.
4. Never claim an issue is fixed unless the PR demonstrably meets its acceptance criteria. Never close an issue without a linked commit/PR or a written reason.

### Checklist before opening/updating a PR
- [ ] Requirements planned and implemented in a git worktree based on `dev`.
- [ ] Tests written and passing locally (`make test`).
- [ ] Concrete evidence of the end change provided (screenshots for UI, verified command outputs for CLI).
- [ ] Temporary deployment preview sandbox named with the PR number (`pr-<PR_NUMBER>`) launched in the background on the main `dev` deployment on `compute-lab` (`make preview-deploy PR=<PR_NUMBER>`), with only the PR's changed services deployed into the sandbox (unchanged services reused from compute-lab), and its Tailscale URL linked.
- [ ] PR targets `dev` per `dev-workflow`.
- [ ] `Closes #n` if fully resolved, else `Refs #n`.
- [ ] Partial work has a progress comment on each referenced issue.

## Reminders
- Redact secrets, keys, tokens, and env values in PRs, comments, and issue bodies.
- Do not paste skill text verbatim into PRs; summarize behavior instead.
