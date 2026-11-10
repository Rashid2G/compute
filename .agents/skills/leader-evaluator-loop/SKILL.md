---
name: leader-evaluator-loop
description: >
  Multi-agent software engineering workflow using a Leader-Evaluator architecture with strict zero-context
  adversarial review gates. Use this skill whenever orchestrating complex codebase implementations, multi-agent
  refactors, or security-critical transitions that require decomposing work into milestones, spawning dedicated
  leader agents, and validating completed tasks with dual zero-context red-team evaluators before merging. Also
  triggers when the user mentions "leader-evaluator loop", "adversarial evaluation", "dual evaluator gate",
  "multi-agent code generation with review", or asks to prevent false-positive green lights during autonomous coding.
---

# Leader-Evaluator Multi-Agent Loop

A rigorous, battle-tested orchestration pattern for complex autonomous software engineering tasks. It coordinates a **Master Orchestrator**, dedicated **Leader Agents** (implementation), and independent **Zero-Context Adversarial Evaluators** (verification) through a formal review gate.

```
       +-------------------------------------------------------+
       |               Master Orchestrator                     |
       |  (Task Decomposition -> Milestone Roadmap -> Gating)  |
       +---------------------------+---------------------------+
                                   |
                         [Milestone Dispatch]
                                   v
                       +-----------------------+
                       |     Leader Agent      |
                       |  (Deep Implementation)|
                       +-----------+-----------+
                                   |
                           [Diff Generated]
                                   v
             +---------------------+---------------------+
             |                                           |
             v                                           v
+--------------------------+               +--------------------------+
|  Zero-Context Evaluator 1| (Parallel)    |  Zero-Context Evaluator 2|
| (Model A, e.g. DeepSeek) |               |  (Model B, e.g. Gemini)  |
| [Red-Team Adversarial]   |               | [Red-Team Adversarial]   |
+------------+-------------+               +-------------+------------+
             |                                           |
             +---------------------+---------------------+
                                   v
                      +-------------------------+
                      |     Acceptance Gate     |
                      | Score > 8.1 & Dual PASS?|
                      +------------+------------+
                                   |
                  +----------------+----------------+
                  | YES                             | NO
                  v                                 v
          [Merge Milestone]             [Remediation Feedback Loop]
                  |                     (Aggregate flaws -> Re-prompt Leader)
                  v                                 |
        [Next Milestone] <--------------------------+
```

---

## Why This Architecture Exists

Standard single-agent or cooperative multi-agent workflows suffer from two fatal failure modes in large codebases:
1. **Context Pollution & Drift**: An agent writing thousands of lines of code rationalizes its own bugs, accumulates noisy context, and misses regressions.
2. **The "Happy-Path Mirage" (False Green Lights)**: Typical evaluator models act as passive compliance checkers—if the author's unit tests pass (`go test` / `pytest`), the evaluator gives a 10/10 PASS score, completely overlooking gaping security flaws, race conditions, and bypasses.

The Leader-Evaluator Loop eliminates these failure modes through **strict context isolation**, **heterogeneous model pairings**, and **mandatory adversarial red-teaming**.

---

## Core Architectural Invariants

### 1. Strict Context Isolation
Evaluators must receive **ONLY**:
- The milestone technical specification / acceptance criteria.
- The resulting code / git diff (`git diff HEAD~1` or patch file).
- The target repository path.

Evaluators must have **zero context** from previous evaluation rounds, the Leader's internal thinking, or the conversation history. This ensures objective judgment without confirmation bias.

### 2. Heterogeneous Evaluator Models
Never use two evaluators from the same model family. Always pair complementary architectures:
- **Evaluator 1**: DeepSeek reasoning model (e.g., `deepseek-v4-pro` / `deepseek-flash` with high reasoning variant) — excels at tracing edge-case logic, SQL semantics, and adversarial attack vectors.
- **Evaluator 2**: Gemini / Claude frontier model (e.g., `gemini-3.8-flash` / `claude-3-7-sonnet` with thinking enabled) — excels at architecture verification, type safety, and standard compliance.

### 3. Adversarial Red-Team Mandate (Active Probing)
Evaluators are **strictly forbidden** from relying solely on the author's passing test suite. Evaluators are mandated to:
- Formulate negative hypotheses ("How can Tenant B access or delete Tenant A's data?").
- Author and execute temporary reproduction test scripts in the shell.
- Check edge-case inputs (namespace collisions, duplicate names, fail-open startup states).
- Delete temporary probe files after recording findings.

### 4. High-Bar Binary Acceptance Gate
- Acceptance requires **BOTH**:
  1. **Score > 8.1 / 10.0** from Evaluator 1.
  2. **Score > 8.1 / 10.0** from Evaluator 2.
  3. **Dual PASS** verdicts.
- If either evaluator fails or scores $\le 8.1$, the milestone is **REJECTED**. The orchestrator aggregates the exact findings into a remediation brief and re-prompts the Leader.

### 5. Sequential Leaders vs. Parallel Evaluators
- **Leaders MUST execute sequentially** for dependent milestones in the same repository checkout to prevent git merge conflicts, database schema collisions, and race conditions.
- **Evaluators MUST execute concurrently** (in parallel threads/tasks) to minimize evaluation latency.

### 6. Integrity, Objectivity & Strict Honesty
**Integrity, Objectivity & Strict Honesty**:

Be completely honest, objective, and evidence-based. If an adversarial probe or code inspection reveals no defects and the implementation satisfies all technical requirements, **report plainly that there are no issues found**.

**Do not invent, fabricate, or exaggerate hypothetical defects.** Any defect reported MUST be backed by verified, reproducible evidence (exact code citation and reproducible behavior from a live probe). If all requirements are met and all probes pass, award an honest passing score (`> 8.0`) and verdict `PASS`.

---

## Step-by-Step Orchestration Lifecycle

### Phase 1: Deep Repository Audit & Milestone Decomposition
Before writing code, the Master Orchestrator performs a comprehensive repository audit and decomposes the work into 3–5 decoupled milestones.

Each milestone specification must define:
1. **Scope of Changes**: Exact packages, modules, and database tables to be modified.
2. **Behavioral Invariants**: Security boundaries, quota rules, concurrency guarantees.
3. **Acceptance Criteria**: Concrete requirements that evaluators will test against.

### Phase 2: Leader Implementation
Spawn a dedicated Leader Agent (`delegate_task` or background subagent):
- Pass the milestone technical specification and repository path.
- Leader implements code, writes migrations, adds baseline unit tests, and formats clean commits.
- When done, Leader produces a diff or patch file (e.g. `/tmp/milestoneN.diff`).

### Phase 3: Parallel Zero-Context Evaluators
Spawn two independent evaluator tasks concurrently with the **Adversarial Evaluator Prompt**:
- Evaluator 1: Model Family A (e.g. `opencode/deepseek-flash`, variant: `high`).
- Evaluator 2: Model Family B (e.g. `antigravity/gemini-3.8-flash`, thinking: `high`).

Both evaluators receive identical inputs:
- Technical Milestone Spec.
- The Git Diff or clean repo state.
- Adversarial evaluation instructions (see [references/evaluator-prompt-template.md](./references/evaluator-prompt-template.md)).

### Phase 4: Gate Evaluation & Score Calibration
When both evaluators complete:
- **Case 1: Both Score > 8.1 & PASS** $\rightarrow$ Milestone accepted. Orchestrator commits, tags, and advances to the next milestone.
- **Case 2: Either Evaluator $\le 8.1$ or FAIL** $\rightarrow$ Remediation Loop triggered.
- **Case 3: High Variance (Score Gap $> 2.5$)** $\rightarrow$ The orchestrator challenges the lenient evaluator with the concrete findings/exploits of the strict evaluator before proceeding.

### Phase 5: Leader Remediation Loop
1. Extract all concrete vulnerabilities, failing edge cases, and code citations from the failing evaluator(s).
2. Format into an actionable **Remediation Task**:
   - List the exact reproducibility steps.
   - Specify the required fix and regression tests.
3. Launch the Leader in a remediation round (e.g. `leader-m1-remediation-r2`).
4. Once remediated, generate a new diff and spawn **fresh, zero-context Evaluators** (Cycle 2). Repeat until both pass.

---

## Adversarial Threat-Modeling Checklist

When evaluating multi-tenant or secure systems, evaluators must affirmatively test:

| Threat Category | Attack Vector / Probe Method | Failure Symptom |
| :--- | :--- | :--- |
| **Namespace Collisions** | Two tenants create resources with identical common names (`web`, `prod`, `shared`). Call DELETE or GET by name. | Raw SQL `WHERE id = ? OR name = ?` deletes or leaks both tenants' resources. |
| **Header / Parameter Tampering** | Pass untrusted headers (e.g. `X-Compute-Org`) or reference another tenant's template/volume in VM creation. | Resource created from victim's private disk; header trusted without checking database membership. |
| **Fail-Open Error Paths** | Induce initialization failure (unwritable auth DB, invalid secret permissions). | System defaults to unauthenticated legacy admin mode instead of halting with 5xx/401. |
| **Subsystem Bypasses** | Trigger background workers, batch jobs, or queue processors with tenant payloads. | Job sandboxes bypass HTTP layer quota checks and image ownership validation. |
| **Lazy Quota Blindspots** | Check whether quotas track live VM rootfs disks or only explicit data volumes. | Unlimited VM allocations allowed without triggering HTTP 429 quota limits. |

---

## Hard-Cap Scoring Rubric

To prevent evaluators from giving false-positive green lights:

- **Score $\le 6.5$ (Mandatory FAIL)**: Any reproducible cross-tenant unauthorized data access, resource deletion, identity spoofing, or authentication bypass.
- **Score $\le 7.5$ (Mandatory FAIL)**: Any unmetered quota bypass, fail-open error path, or namespace collision leak.
- **Passing the Author's Unit Tests**: **Adds 0 bonus points** (it is a baseline prerequisite, not proof of security).
- **Score 8.2 – 10.0 (PASS)**: Awarded ONLY if all functional requirements are satisfied AND the system withstands dynamic adversarial probe scripts.

---

## Reference Templates

- [Adversarial Evaluator Prompt Template](./references/evaluator-prompt-template.md)
- [Leader Implementation Prompt Template](./references/leader-prompt-template.md)
