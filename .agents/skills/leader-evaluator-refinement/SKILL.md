---
name: leader-evaluator-refinement
description: >
  Universal multi-agent orchestration architecture using Leader-Evaluator dynamics with strict zero-context
  adversarial evaluation gates and iterative refinement. Use this skill whenever executing complex, high-stakes tasks
  across any domain (software development, system design, technical writing, research, analysis, strategy, or creative output)
  that require decomposing work into milestones, assigning dedicated leader agents to author deliverables, and stress-testing
  completed work with dual independent zero-context evaluators to continuously enhance results before acceptance. Triggers
  when the user mentions "leader-evaluator", "adversarial evaluation", "dual evaluator gate", "iterative refinement",
  "result enhancement", "multi-agent review gate", or asks to eliminate blindspots and false-positive green lights.
---

# Leader-Evaluator Multi-Agent Refinement

A universal, domain-agnostic orchestration pattern for high-assurance task execution and continuous result enhancement. It coordinates a **Master Orchestrator**, dedicated **Leader Agents** (creation and execution), and independent **Zero-Context Adversarial Evaluators** (verification and stress-testing) through a rigorous quality gate.

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
                       | (Deep Implementation /|
                       |       Authoring)      |
                       +-----------+-----------+
                                   |
                         [Deliverable Produced]
                                   v
             +---------------------+---------------------+
             |                                           |
             v                                           v
+--------------------------+               +--------------------------+
|  Zero-Context Evaluator 1| (Parallel)    |  Zero-Context Evaluator 2|
|   (Perspective / Model A)|               |   (Perspective / Model B)|
|   [Adversarial Probing]  |               |   [Adversarial Probing]  |
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
          [Accept Milestone]            [Remediation & Refinement]
                  |                     (Synthesize flaws -> Enhance)
                  v                                 |
        [Next Milestone] <--------------------------+
```

---

## Why This Architecture Exists

Standard single-agent or unstructured multi-agent workflows suffer from two fatal failure modes across any domain or project:
1. **Context Pollution & Author Bias**: An agent creating complex deliverables rationalizes its own oversights, accumulates noisy conversational context, and develops blindspots to regressions, omissions, or flawed assumptions.
2. **The "Happy-Path Mirage" (False Green Lights)**: Typical evaluators act as passive checklist checkers—if self-reported checks or basic tests pass, the evaluator gives an unearned high score, overlooking subtle vulnerabilities, logical fallacies, edge-case failures, and unhandled boundary conditions.

The Leader-Evaluator Refinement framework eliminates these failure modes through **strict context isolation**, **complementary evaluation perspectives**, and **mandatory adversarial stress-testing**, driving systematic enhancement of deliverables.

---

## Core Architectural Invariants

### 1. Universal Applicability (Domain-, Project-, and Tool-Agnostic)
The framework applies universally to any discipline, deliverable type, and tool stack:
- **Domains**: Software engineering, architecture specifications, research papers, data analyses, policy documentation, UX/UI designs, curriculum authoring, operational procedures, or strategic plans.
- **Tools**: Code compilers, test runners, linters, terminal utilities, API clients, browser inspectors, mathematical tools, document processors, or pure analytical reasoning.
- **Deliverables**: Code patches, document files, diagrams, structured schemas, configuration sets, analytical models, or synthesized reports.

### 2. Strict Context Isolation (Zero-Context Evaluation)
Evaluators must receive **ONLY**:
- The milestone technical specification / requirements, constraints, and acceptance criteria.
- The resulting deliverable artifact (diff, file, document, schema, or work product).
- The reference environment or contextual materials needed to independently evaluate the output.

Evaluators must have **zero context** from the Leader's internal drafting process, prior refinement passes, or the broader conversational history. This ensures objective judgment free from confirmation bias.

### 3. Strictly Heterogeneous Evaluator Models (Zero Duplicate Models)
**NEVER use duplicate models for evaluations.**
Using the same model family, provider, or architecture for both evaluators completely undermines independent verification—duplicate models share identical blindspots, inductive biases, training corpora, and reasoning failure modes.
- **Strict Invariance**: Evaluator 1 and Evaluator 2 MUST ALWAYS be powered by entirely distinct, non-duplicate model families (e.g., DeepSeek paired with Gemini, or Claude paired with OpenAI/DeepSeek).
- **No Same-Family Variants**: Pairing different sizes, quantization levels, or versions of the same model family (e.g., `model-flash` with `model-pro` from the same provider) is strictly forbidden as a duplicate pairing.
- **Perspective & Cognitive Decoupling**:
  - **Evaluator 1 (Model Family A - Adversarial Stress-Testing & Rigor)**: Deep reasoning architecture specialized in edge-case tracing, counter-examples, boundary probing, and security/logical attack vectors.
  - **Evaluator 2 (Model Family B - Architectural Coherence & Conformance)**: Distinct frontier architecture specialized in structural integrity, requirements alignment, holistic consistency, and standards compliance.

### 4. Adversarial Mandate (Active Probing & Empirical Verification)
Evaluators are **strictly forbidden** from relying solely on self-reported author claims, existing baseline tests, or passive document scanning. Evaluators are mandated to actively probe the deliverable:
- Formulate negative hypotheses ("In what scenario does this deliverable fail, produce incorrect output, contradict itself, or violate constraints?").
- Author and execute independent verification probes (e.g., dynamic reproduction scripts, boundary-value stress tests, counter-factual analysis, or consistency checks).
- Probe edge cases: collision handling, unhandled edge states, malformed inputs, missing prerequisites, ambiguous definitions.
- Clean up any temporary probe files or artifacts after recording findings.

### 5. High-Bar Binary Acceptance Gate
- Acceptance requires **BOTH**:
  1. **Score > 8.1 / 10.0** from Evaluator 1.
  2. **Score > 8.1 / 10.0** from Evaluator 2.
  3. **Dual PASS** verdicts.
- If either evaluator fails or scores $\le 8.1$, the milestone is **REJECTED**. The orchestrator aggregates the exact findings into a remediation brief and re-dispatches the Leader for refinement.

### 6. Sequential Execution vs. Concurrent Evaluation
- **Leaders MUST execute sequentially** for dependent milestones to prevent conflicting changes, state corruption, or logical contradictions.
- **Evaluators MUST execute concurrently** (in parallel threads/tasks) to maximize velocity and guarantee mutual independence without cross-contamination.

### 7. Integrity, Objectivity & Strict Honesty
Be completely honest, objective, and evidence-based. If active stress-testing and thorough inspection reveal no defects and the implementation satisfies all technical requirements, **report plainly that there are no issues found**.

**Do not invent, fabricate, or exaggerate hypothetical defects.** Any defect reported MUST be backed by verified, reproducible evidence (exact file citation, concrete line/section reference, and reproducible probe output or counter-example). If all requirements are met and all probes pass, award an honest passing score (`> 8.0`) and verdict `PASS`.

### 8. Iterative Refinement (Continuous Result Enhancement)
Refinement is the active mechanism of results enhancement:
- Evaluator feedback is not merely a pass/fail verdict; it is diagnostic signal.
- The Orchestrator converts failing findings into an actionable enhancement agenda.
- The Leader refines the deliverable specifically targeting diagnosed weaknesses.
- Fresh zero-context evaluators re-assess the refined output, ensuring progressive elevation of quality until excellence is proven.

---

## Step-by-Step Orchestration Lifecycle

### Phase 1: Scope Audit & Milestone Decomposition
Before generating deliverables, the Master Orchestrator audits the target project/domain and decomposes the overall objective into 3–5 decoupled, self-contained milestones.

Each milestone specification must define:
1. **Scope of Deliverables**: Exact files, sections, modules, or artifacts to be created or modified.
2. **Behavioral Invariants**: Non-negotiable constraints, safety rules, consistency guarantees, or domain requirements.
3. **Acceptance Criteria**: Measurable, verifiable criteria that evaluators will independently test against.

### Phase 2: Leader Execution
Spawn a dedicated Leader Agent:
- Pass the milestone specification, target project context, and constraints.
- Leader performs deep work: generates code, writes text, drafts schemas, or builds analyses, running baseline verification as appropriate.
- When complete, Leader packages the deliverable artifact (e.g. patch diff, output document, artifact file) and summarizes key outcomes.

### Phase 3: Parallel Zero-Context Evaluators
Spawn two independent evaluator tasks concurrently with the **Adversarial Evaluator Prompt**:
- **Evaluator 1**: Model Family A (e.g. DeepSeek reasoning architecture) — Rigorous Stress-Testing / Red-Team Perspective.
- **Evaluator 2**: Model Family B (e.g. Gemini / Claude frontier architecture — **strictly distinct model family from Evaluator 1**) — Architectural Coherence / Requirements Compliance Perspective.

> [!IMPORTANT]
> **Zero Duplicate Models Mandate**: Under no circumstances should duplicate models, duplicate providers, or sibling variants of the same model family be assigned to both evaluators. True adversarial independence requires cognitive and architectural heterogeneity. Never use duplicate models for evaluations.

Both evaluators receive identical inputs:
- Technical Milestone Specification.
- The Deliverable Artifact or resulting state.
- Adversarial evaluation instructions (see [references/evaluator-prompt-template.md](./references/evaluator-prompt-template.md)).

### Phase 4: Gate Evaluation & Score Calibration
When both evaluators complete:
- **Case 1: Both Score > 8.1 & PASS** $\rightarrow$ Milestone accepted. Orchestrator integrates the artifact and advances to the next milestone.
- **Case 2: Either Evaluator $\le 8.1$ or FAIL** $\rightarrow$ Remediation & Refinement triggered.
- **Case 3: High Variance (Score Gap $> 2.5$)** $\rightarrow$ The orchestrator challenges the lenient evaluator with the concrete findings/counter-examples of the strict evaluator before making a gate determination.

### Phase 5: Remediation & Refinement (Result Enhancement)
1. Extract all verified deficiencies, failing edge cases, counter-examples, and exact citations from the failing evaluator(s).
2. Format into an actionable **Enhancement Brief**:
   - List the exact reproducibility steps or failure demonstrations.
   - Specify the required remediation and regression verification.
3. Launch the Leader in a dedicated refinement round (e.g. `leader-m1-refinement-r2`).
4. Once refined, produce an updated deliverable and spawn **fresh, zero-context Evaluators**. Repeat until both pass.

---

## Universal Stress-Testing Checklist

When evaluating any deliverable, evaluators must proactively stress-test against universal failure patterns:

| Failure Category | Stress-Testing Probe Method | Failure Symptom |
| :--- | :--- | :--- |
| **Boundary & Collision Failures** | Test overlapping identifiers, duplicate names, extreme scale values, zero/null/infinite boundaries. | Name collisions overwrite data; extreme values cause overflow or layout breakage; shared state leaks between distinct entities. |
| **Untrusted Input & Manipulation** | Inject malformed payloads, unusual character encodings, unexpected formats, out-of-spec parameters. | System crashes, unhandled exceptions, incorrect parsing, injection vulnerability, or silent corruption. |
| **Fail-Open & Fallback Errors** | Induce failure states (unreachable dependency, broken configuration, missing asset, unauthorized state). | System defaults to insecure open state, exposes raw internals, or silently continues with invalid data instead of failing safely. |
| **Omission & Blindspots** | Compare deliverable against unstated prerequisite conditions, edge cases, downstream dependencies, or counter-examples. | Solution only works in the idealized happy path; breaks when real-world conditions diverge. |
| **Factual, Logical & Semantic Integrity** | Cross-check claims, data citations, formulas, logical syllogisms, or type contracts against source truth. | Hallucinated facts, faulty mathematical deductions, circular logic, or incompatible contract interfaces. |

---

## Hard-Cap Scoring Rubric

To prevent false-positive green lights and ensure genuine result enhancement:

- **Score $\le 6.5$ (Mandatory FAIL)**: Any reproducible critical defect, security breach, catastrophic data loss, unauthorized access, severe factual hallucination, or fundamental specification violation.
- **Score $\le 7.5$ (Mandatory FAIL)**: Any unhandled boundary condition, fail-open error path, unaddressed core requirement, or verified logic breakdown.
- **Passing the Author's Self-Reported Checks**: **Adds 0 bonus points** (passing baseline checks is a minimum prerequisite, never proof of robust quality).
- **Score 8.2 – 10.0 (PASS)**: Awarded ONLY if all specification requirements are satisfied AND the deliverable withstands active adversarial probing with verified robustness.

---

## Reference Templates

- [Adversarial Evaluator Prompt Template](./references/evaluator-prompt-template.md)
- [Leader Execution Prompt Template](./references/leader-prompt-template.md)
