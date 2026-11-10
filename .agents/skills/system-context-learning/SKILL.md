---
name: system-context-learning
description: >
  Autonomous context learning and safe skill evolution across agent sessions. Enables agents to capture
  and propagate operational and environmental realities (e.g. build locations, host IPs, port changes,
  tooling quirks) directly into relevant skills so future agents inherit context without user repetition.
  Enforces strict boundaries: factual operational updates are applied autonomously, while any change
  that alters workflows, safety guardrails, branching rules, or policies requires explicit user confirmation first.
  Triggers when operational facts change, when discovering environment/tooling drift, or when prompted with
  "system-auto-improving", "context learning", "update skill context", or "leave context for other agents".
---

# System Context Learning & Safe Skill Evolution

Maintains persistent, living context across autonomous agent sessions. When infrastructure, build pipelines, commands, or operational facts change, agents record these updates directly into the appropriate repository skill so subsequent agents inherit ground truth without user intervention.

To protect engineering integrity, updates are strictly partitioned between **autonomous factual edits** and **user-gated workflow changes**.

---

## 1. The "Context-Only and Only If Needed" Law

To prevent skills from becoming bloated, noisy, or distracting to future agents, all updates must adhere to three non-negotiable rules:

1. **Context-Only (No Narrative Bloat)**:
   - Record **only the verified operational fact, path, command, or prerequisite**.
   - NEVER write conversational logs, diary entries, agent introspections, timestamps, or stories (e.g. do NOT write: *"In session #42, an agent encountered an issue where..."*).
   - Update the canonical command, path, or checklist item **in-place**.

2. **Only If Needed (Durable Value Filter)**:
   - Ask: *"Will a future agent fail, run the wrong command, or need the user to re-explain this if this update is omitted?"*
   - If YES: update the skill.
   - If NO (e.g. transient file in `/tmp`, one-off experimental test, fleeting typo): do NOT update the skill.

3. **High Signal Density**:
   - Keep updates minimal, precise, and immediately actionable (exact flags, verified IP/domain, specific configuration line).

---

## 2. Permitted Boundaries: Autonomous vs. User-Gated

Every proposed update must be evaluated against this classification before touching any file:

| Category | Permitted Action | Trigger Examples |
| :--- | :--- | :--- |
| **Host & Network Topography** | **Autonomous Edit** | Host IP addresses, hostnames, SSH aliases, port assignments (e.g. API port changed), local URLs. |
| **Build & Runtime Locations** | **Autonomous Edit** | Where builds occur (e.g. building directly on the deployment VM instead of the local machine), output binary paths, container registry tags. |
| **Command Flags & Tool Invocations** | **Autonomous Edit** | Adjusted build/test flags, updated package manager commands (e.g. Bun vs. NPM flags), tool version requirements. |
| **Operational Quirks & Gotchas** | **Autonomous Edit** | Prerequisite scripts needed before a task (e.g. `make setup-host`), sleep delays for daemon sockets to drain, kernel modules or permissions required. |
| **Skill Registry & Internal Links** | **Autonomous Edit** | Updating `using-skills/SKILL.md` when skills are added/renamed, fixing broken file links. |
| **Workflow & Branching Policy** | **ASK USER FIRST (Strict Gate)** | Changing Git flow (`main` vs `dev` rules, worktree requirements, direct pushing policies, commit formats). |
| **Quality & Verification Gates** | **ASK USER FIRST (Strict Gate)** | Removing required tests, lowering test coverage, skipping build verification, altering evaluation rubrics. |
| **Security & Secrets Governance** | **ASK USER FIRST (Strict Gate)** | Changing credential handling, SSH key permissions, secret redaction policies, tenant authorization logic. |
| **Target Scope & Production Env** | **ASK USER FIRST (Strict Gate)** | Targeting production hosts, switching cloud providers, altering persistent database schema migrations destructively. |
| **Skill Deletion or Structural Gutting**| **ASK USER FIRST (Strict Gate)** | Deleting existing skills, removing core architectural mandates, changing a skill's fundamental methodology. |

---

## 3. Escalation Protocol: When Asking the User is Mandatory

If a proposed change falls into a **User-Gated** category (alters workflow, governance, safety, or architecture):

1. **DO NOT edit the skill file.**
2. Stop and present a concise proposal to the user with this exact format:
   ```markdown
   ### Proposed Skill Workflow Change (User Approval Required)
   - **Target Skill**: `.agents/skills/<skill-name>/SKILL.md`
   - **Proposed Change**: <Exact workflow change>
   - **Rationale**: <Why this change is suggested based on runtime observations>
   - **Impact / Risks**: <Effect on safety, verification, or release flow>
   - **Question**: Do you approve updating the skill with this workflow change?
   ```
3. Only proceed with the edit if the user explicitly gives approval.

---

## 4. Execution Workflow for Autonomous Updates

When an operational fact or environmental reality changes (e.g., user says: *"we changed where we build the app to where the same vm that serves the app"* or an agent verifies a port change):

```
[Operational Fact Discovered / Informed]
                  │
                  ▼
   Is it Tier 1 (Factual/Operational) ──No──► [Escalate to User: Ask First]
                  │
                 Yes
                  ▼
     [Locate Canonical Target Skill]
                  │
                  ▼
    [Apply Minimal In-Place Edit]
                  │
                  ▼
  [Verify Diff & Format: git diff --check]
                  │
                  ▼
   [Commit: docs(skills): update ...]
```

### Step 1: Identify the Canonical Target Skill
Locate the specific skill that owns the domain:
- **Deployment, hosts, remote paths, lab execution**: `.agents/skills/deployment/SKILL.md`
- **Development workflow, worktrees, local build/test tools**: `.agents/skills/dev-workflow/SKILL.md`
- **Issue tracking, bug triage, linking rules**: `.agents/skills/github-issues/SKILL.md`
- **Adversarial evaluation, multi-agent review**: `.agents/skills/leader-evaluator-refinement/SKILL.md`
- **Skill discovery, loading, meta-rules**: `.agents/skills/using-skills/SKILL.md`
- **Cross-cutting repository mandates**: `AGENTS.md`

### Step 2: Make the Minimal In-Place Edit
- Open the target skill.
- Locate the existing section (e.g. `## Target`, `## Commands`, `## Prerequisites`).
- Edit the content in-place to reflect the new ground truth.
- Do NOT add redundant append-only sections.

### Step 3: Verify and Commit
- Run `git diff <target-skill>` to confirm only the intended operational fact changed.
- Run `git diff --check` to ensure no whitespace or formatting issues.
- Commit using a clear scoped commit message:
  ```bash
  git commit -m "docs(skills): update deployment build location to compute-lab VM"
  ```

---

## 5. Walkthrough Examples

### Example A: Build Location Shift (Autonomous)
- **Scenario**: User states: *"we changed where we build the app to where the same vm that serves the app"*.
- **Classification**: Tier 1 (Operational & Build Location).
- **Action**:
  1. Open `.agents/skills/deployment/SKILL.md`.
  2. In `## Commands`, update the build instruction so that building happens on `compute-lab` via SSH or remote make target rather than locally before rsyncing.
  3. Keep the change concise and factual.
  4. Commit without interrupting the user.

### Example B: Port or Host Alias Update (Autonomous)
- **Scenario**: The daemon port on `compute-lab` is updated from `18080` to `19080`.
- **Classification**: Tier 1 (Host & Network Topography).
- **Action**:
  1. Update `Daemon API: http://127.0.0.1:19080` in `deployment/SKILL.md`.
  2. Commit: `docs(skills): update daemon API port to 19080 in deployment skill`.

### Example C: Attempting to Skip PRs to Dev (User-Gated)
- **Scenario**: An agent finds running `gh pr create` tedious and wants to edit `dev-workflow/SKILL.md` to allow pushing directly to `main`.
- **Classification**: Tier 2 (Workflow & Branching Governance).
- **Action**:
  1. The agent MUST NOT edit `dev-workflow/SKILL.md`.
  2. The agent asks the user for permission. If denied, the workflow remains intact.
