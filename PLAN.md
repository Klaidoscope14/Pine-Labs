# AegisSDLC: Critique of the PRD and a Revised Product Plan

*Pine Labs Challenge Board, Case Study 03. Prepared 21 Nov 2026. Every factual claim below is checked against a public source (listed at the end). Where a source is a vendor survey or anonymous reporting, the plan says so.*

---

## 0. TL;DR

The core idea in PRD.txt is right: deterministic boundaries around LLM nodes, a sandbox, gates, and a human approval at the end. It also matches what Stripe published. The plan has four serious problems:

1. **Factual errors that the judges will catch.** The pitch says Pine Labs has "NO governed unattended PR control plane". Pine Labs' own CEO and press coverage describe in-house agents for design, coding, PR review and functional testing, plus an "Agentic HQ" governance layer. Several statistics are wrong or have no source (Veracode 38%, 1,218 engineers, the 14% revenue share). AgenticOrg and Grantex are described as Pine Labs open-source products. They are owned by Orchestrum Technologies LLP and are source-available under a personal-use licence, not open source.
2. **Two Stripe patterns are misread.** Stripe's Selective Test Execution is *dynamic* (it intercepts file access with `LD_PRELOAD`), not a static AST import graph. Stripe's Minions run with *full permissions inside a throwaway devbox*. Stripe gets safety from isolation, CI and human review, not from per-file write allowlists. The PRD's rigid per-write allowlist will produce false DesignConflicts and hurt the success rate.
3. **The verification is circular.** The Developer agent writes its own tests, and the same system scores its own rubric from a single baseline run. A sharp judge will say "your agent graded its own homework". This needs hidden oracle tests and repeated runs, the way Stripe's own Nov 2026 eval post does it.
4. **Too much scope for 48 hours.** Six LLM agents, arbitration, a rule observer, a baseline, four demos, provider fallback, a Next.js cockpit, and now Go, Temporal and a three-service observability stack. The plan needs hard cut lines.

**Recommended pivot:** keep the thesis. Rebuild the control plane in **Go on Temporal**, which gives durable execution, retries, human-approval signals and replayable history out of the box. Instrument it with **OpenTelemetry → Jaeger + Prometheus → Grafana**. Write the fintech policy pack as **Semgrep rules**, which work on Java, so they carry over to Pine Labs' Java/Spring services. Prove the claims with a **small eval suite: hidden tests, N runs, governed vs. baseline**. **Don't write Rust.** Reasons in §4.3.

---

## 1. Fact-check of PRD.txt

| PRD claim | What the sources say | Verdict / fix |
|---|---|---|
| "AI touches ~89% of internal code changes (1.5M LOC/mo)" | AIM (27 Feb 2026), from CEO Amrish Rau's X post: agents contributed to ~89% of >1.3M lines changed **in the last 28 days**. Inc42 (25 Aug 2026): ~89% of new code changes **over two quarters**, >1.5M lines. | **Partly right.** Two definitions are mixed together. Quote one, with source and date. |
| Pine Labs has "NO governed unattended PR control plane" | AIM: "in-house agents for design, coding, pull request reviews, and functional testing", integrated with Jira, Confluence, GitHub and Figma. Inc42: an "Agentic HQ governance layer" with role-based permissions and audit trails, and testing cycle time cut by >95%. | **Wrong, and risky.** You'd be telling the people who built this that they don't have it. Reposition (see §2). |
| AgenticOrg and Grantex are Pine Labs open-source pillars | grantex.dev and agenticorg.ai: "Owned by Orchestrum Technologies LLP. Inventor and owner: Sanjeev Kumar." The platform is under the **Orchestrum Personal Use License 1.0** (organisations need a signed agreement). Only the SDKs and MCP server are Apache-2.0. YourStory (Nov 2026) calls Sanjeev Kumar Pine Labs' CTO and ties Grantex to the P3P protocol. One other report names a new CTO, so his current title is disputed. | **Overstated.** Use the Apache-2.0 Grantex *SDK* (it has a Go SDK). Don't promise a "drop-in AgenticOrg pack". |
| AgenticOrg has a "Live Observatory" and "replayable event feeds" | agenticorg.ai: the Observatory "polls the 20 most recent rows every five seconds". Feed events are stored with sequence numbers, but "operational event publishing into the live feed is still pending". | **Overstated.** Don't mirror features that are only partly shipped. |
| FY26 PAT ₹113 Cr on ₹2,711 Cr revenue | The Fintech Times and Inc42 confirm it. | ✅ |
| "~1,218 engineers", "14% of Q2 FY27 revenue from products <2 yrs" | Not found in any source checked. | **Remove**, or find the filing. |
| Veracode (Oct 2026): AI code fails 38% of security tests | Veracode **2026** GenAI report (**28 Jul 2026**): average security pass rate **56%** across 100+ models, so ~44% of tasks had an OWASP Top-10 flaw. Best model was GPT-5.5 at 68%. Log injection ~12%, XSS ~15%. Python 63%, Java 30%. Tested raw models, not agents. | **Wrong number and date.** The real numbers make your case *better*, and Java at 30% is directly relevant to Pine Labs. |
| Harness: 58% incidents up, 7 in 8 had a failure, 33% kill switch, 19% automated gate | Confirmed: 700 leaders, 58%, ~7 in 8, 33% instant kill switch, 39% automated rollback. Vendor-sponsored, fieldwork by Sapio in Jul 2026. The "19%" figure was not found. | Keep the first three and label them as vendor data. Drop the 19%. |
| Amazon outages "linked to autonomous agents" | Dec 2025, 13-hour Cost Explorer outage (one China region): FT, citing anonymous sources, blamed the Kiro agent. Amazon disputes this and blames "misconfigured access controls". 5 Mar 2026, ~6-hour retail outage: Amazon said it was a "software code deployment" error. The FT reported a memo requiring senior sign-off on AI-assisted changes, and Amazon disputed that characterisation. | **Soften to "reported".** Amazon's own explanation (misconfigured access controls) is itself an argument for least-privilege scoped credentials. Use that. |
| DX/Faros: PR volume +98%, minimal DORA gains | That is Faros's **2025** report (+98% PRs, +91% review time). Faros's 2026 report "Acceleration Whiplash" (22k developers) has stronger numbers: median review time about **5×**, **31% of PRs merged with no review**, PRs 51% larger, 3× incidents per PR. DX Q3 2026: PR size +122% YoY, time to first review 40 → 82 min as the AI share grows. | Update the numbers. Review is the bottleneck: that is your thesis. |
| Apiiro: 3–4× code, 10× findings | Apiiro, Sep 2025, vendor data. Findings rose 10× from Dec 2024 to Jun 2025. Disputed by Contrast and others. | Keep it, labelled as vendor data, or drop it. |
| Cursor $60B acquisition | SpaceX agreed in Jun 2026 to buy Anysphere for $60B in stock. | ✅ (not relevant to judging, so cut it) |
| Cognition $48B | Not checked. | Cut it. Market-size tables don't win engineering hackathons. |
| Stripe Minions, Feb 2026, 1,300+ PRs/week, ~500 tools, 2 CI rounds | Part 1 (9 Feb): >1,000 PRs/week, >400 tools. Part 2 (19 Feb): >1,300, "nearly 500" tools, "at most two rounds of CI", 10-second pre-warmed EC2 devboxes, a fork of Block's goose. | ✅ But see §3: Minions run *with full permissions inside the devbox*. |
| "WebMCP / Checkout for AI Agents" (Sep 2026) | The real title is "How Stripe is designing Checkout for AI agents" (22 Sep 2026). Progressive tool disclosure cut tokens 42% and tool calls 38%. | Fix the title and cite the numbers. |
| STE: "Static AST import graph" | STE (9 Apr 2026) is **dynamic**: an `LD_PRELOAD` file-access interceptor maps files to tests in roaring bitmaps. It runs about 5% of tests per build (median 0.5%). | **Misread.** The Python version is per-test coverage contexts (§5.4). |
| "Harbor" → commit-anchored findings | Harbor (14 Sep 2026) anchors *UI comments* to *DOM elements* and marks them attached, needs review, orphaned or stale on republish. | Fine as *inspiration*. Say "inspired by". |
| PCI DSS 6.5.3 = "zero live data" | In v4.0.1, 6.5.3 is *separating pre-production from production*. "No live PANs in pre-prod" is **6.5.5**. 6.5.4 is separation of roles. | Fix the mapping. A PCI-literate judge will notice. |

---

## 2. Strategic critique: positioning

**Problem.** Your judges work at a company that says AI already touches about 89% of its code changes, runs in-house design, coding, review and testing agents, and has a governance layer. A pitch built on "you lack a governed control plane" is both wrong and adversarial.

**Better framing: make the 89% auditable.** Generating code isn't the bottleneck. Proving each change is safe is. Faros 2026 found review time up ~5× and 31% of PRs merged with no review. Amazon's reported response was senior sign-off, which it called "controlled friction". AegisSDLC makes that friction cheap: every agent PR arrives with machine-checked evidence, so a senior reviewer approves in minutes instead of re-deriving the change.

**One-line pitch:** *"Agents propose. Gates prove. Humans decide. Every agent action is treated like a payment: idempotent, scoped, logged, and reversible."*

The payments metaphor is a real design principle (§5.3), and it speaks the judges' language.

**Measurable claim to put on the slide.** Pick these, and measure them during your own evals:
- Strict pass rate (every MUST criterion met) on hidden tests, governed vs. single-prompt baseline, over N runs.
- Percentage of bad changes blocked before reaching a human (seeded bugs, secrets, scope violations, prompt injections).
- Cost, latency and retries per successful run, from Prometheus.

---

## 3. Design critique: what to change and why

### 3.1 Write scope: enforce at the diff boundary, not on every write
Stripe's Minions run "with full permissions and no confirmation prompts" because the blast radius is one devbox. Safety comes from isolation, two CI rounds, and human review. The PRD's Architect-defined per-file allowlist is *stricter than Stripe's*, and it will misfire on legitimate edits (`__init__.py`, `conftest.py`, fixtures, migrations). Every misfire becomes a DesignConflict and burns a retry.

**Change it to three tiers, checked when a commit is promoted:**
- **Planned** (from the Architect): allowed.
- **Adjacent** (same package, tests, fixtures): allowed, but flagged in the evidence packet and counted against a **diff budget** (max files and lines).
- **Protected** (a `CODEOWNERS`-style list such as `payout_auth.py`, CI config, `.agent-rules.md`): blocked on write *and* at the diff check. Only a human can unlock it.

This keeps F6 (unrelated file) demoable and stops the Architect's plan from causing false failures.

### 3.2 The agent grades its own homework
If the Developer writes both the code and the tests, a green test run proves little. Add:
- **Hidden oracle tests** per ticket, which agents never see. They run in Tier 2 and are the ground truth for the eval.
- **Mutation testing on changed lines** as the "additional quality check" (`mutmut` limited to the changed functions). It shows whether the agent's tests actually catch bugs. This is rare in hackathon entries.
- **Differential replay**, from Stripe's Spark regression posts (Jun 2026): replay a recorded fixture of webhook and payment traffic against the base build and the patched build. Diff the responses and check **ledger invariants** (sum of credits equals settled amount; one event ID gives one credit). Real fintech substance.

### 3.3 Not every "agent" needs an LLM
QA and Security are scanners plus a summary. Calling them LLM agents is theatre, and "judgement about where autonomy should stop" is literally a judging criterion. Recommended roster:

| Role | Kind | Why |
|---|---|---|
| Analyst | LLM | Turning ambiguity into acceptance criteria is real reasoning |
| Architect | LLM + deterministic code graph | Impact analysis needs the graph; the design needs judgement |
| Developer | LLM tool loop | Writing code |
| QA gate | **Deterministic** (+ LLM triage of failure logs into fix hints) | Tests either pass or they don't |
| Security gate | **Deterministic** (+ LLM explanation of findings) | An LLM must never be able to downgrade a finding |
| Release | LLM summary over deterministic evidence | Summarising for humans |

This still meets "specialised agents with explicit inputs and outputs". Stripe's blueprints mix agent nodes and deterministic nodes in exactly this way.

### 3.4 Orchestration: Temporal over LangGraph
Stripe's Payment Method Factory found a top-level orchestrator agent "expensive, slow, and sometimes unreliable" and moved coordination into code. The PRD gets that right. The remaining question is which durable runtime runs that code. Temporal gives you, without writing them yourself:
- **F5, provider outage and resume:** activity retries with backoff, and a workflow that survives worker crashes. The demo becomes *real*: `docker kill` the worker mid-run, restart it, and the run continues from the same step. No fake toggle needed.
- **Persisted state and execution history:** a full event history for every run, plus the Temporal Web UI.
- **Human gates:** Signals (approve or reject) and Queries (current state).
- **Bounded iteration:** retry policies and timeouts declared in one place.
- Stripe itself uses Temporal (the hot-pool refill job in the Nov 2026 claimable-sandboxes post).

Cost: Temporal workflow code must be deterministic, so LLM calls, Docker and git go in activities. That *is* your architecture principle, now enforced by the runtime. Locally it's one binary: `temporal server start-dev`.

### 3.5 Fault injection toggles look staged
Judges discount UI toggles named `inject_provider_outage`. Use **real faults** instead:
- Provider outage: flip the egress ACL so the provider's hostname is denied (Smokescreen, §5.6), or kill the worker.
- Prompt injection: a malicious comment planted in the sample repo (OWASP LLM01).
- Test failure: hidden tests on a boundary case the agent tends to miss. **Measure the natural failure rate in rehearsal** and show the eval table, not one lucky live run.

### 3.6 Demo 1 bundles two requirements
"Fix duplicate webhooks AND add tiered late fees" doubles the failure surface, and it isn't atomic, which is something the Analyst is supposed to enforce. **The primary demo is the idempotent webhook alone.** The late fee becomes ticket #2 in the eval suite. If the Analyst splits a combined ticket into two runs, that's a nice beat for the demo.

### 3.7 The human gate should be enforced by GitHub, not just your UI
"Keep humans in control of merge" is far more convincing when the *platform* enforces it:
- Use a real GitHub repo with a branch ruleset: required review, a required status check `aegis/evidence`, and no bypass.
- The agent's fine-grained token can push `agent/*` branches and open draft PRs, but **cannot merge**.
- A human approval in the Cockpit **signs (commit SHA + evidence hash)**. Any new commit after approval voids it.
- Add a **system-wide kill switch**: terminate all workflows and revoke all scope tokens in one call. Cite Harness's 33% figure.

### 3.8 Missing threats
- **Prompt injection from repo content** (OWASP LLM01) and **excessive agency** (LLM06). Repo READMEs and comments go to the Analyst and Architect. Show the scope gate blocking an injected instruction.
- **Secret exfiltration:** the sandbox gets no secrets and no egress. LLM calls happen in the worker, outside the sandbox.
- **Cost runaway:** a per-run budget in tokens and USD as a hard limit, which counts as one more bounded failure.

---

## 4. Tech stack review (Go, Rust, Prometheus, Grafana, Jaeger)

### 4.1 What the sample code runs on doesn't matter much
The verification tools (pytest, ruff, semgrep, gitleaks, oasdiff) run **as CLIs inside the sandbox container**. The orchestrator's language is therefore independent of the target repo's language. That removes the PRD's main reason for an all-Python stack.

### 4.2 Go: yes, for the whole control plane
| Area | Why Go fits |
|---|---|
| Temporal workflows and activities | The Go SDK is Temporal's most mature, with an official OpenTelemetry interceptor |
| Sandbox runner | Docker itself is written in Go, and the Docker Go SDK is first-party |
| Tool gateway and policy | Fast, typed, easy to test. One static binary |
| LLM router | Official Go SDKs from Anthropic (`anthropic-sdk-go`) and OpenAI. Validate structured output against JSON Schema (`santhosh-tekuri/jsonschema`) |
| Observability | First-class Prometheus client and OpenTelemetry SDK |
| Ecosystem | The best tools here are already Go: Stripe's **smokescreen** (egress proxy), **oasdiff** (OpenAPI breaking changes), **gitleaks**, **osv-scanner**, Stripe's **pg-schema-diff** (migration hazards), the **grantex-go** SDK |

**Trade-off:** you lose Pydantic. JSON Schema plus generated Go structs covers it. Python stays only in the target sample repo and its tools.

### 4.3 Rust: no, in 48 hours
There's no component where Rust beats Go enough to pay for the compile-time and borrow-checker cost in a 48-hour build. You still *use* Rust-built tools (ruff, uv). Saying "we considered Rust and rejected it, because…" in the README is itself a judgement signal. If someone on the team is fluent in Rust and wants one piece, the only sensible candidate is a small **diff-scope verifier** CLI (parse the unified diff, apply the tier rules, emit JSON). It's self-contained and easy to swap out. Treat it as optional.

### 4.4 Observability: yes, wired through OpenTelemetry
- **Path:** Go services → OTel SDK → **OTel Collector** → **Jaeger** (traces) + **Prometheus** (metrics, via remote-write or a scrape exporter) → **Grafana**. Jaeger v2 is itself built on the OTel Collector.
- **Traces:** one trace per run (`run_id` = root span). Spans for each workflow step, LLM call, tool call, and sandbox exec. Use the OTel **GenAI semantic conventions** (`gen_ai.request.model`, `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens`). The Temporal Go OTel interceptor creates workflow and activity spans automatically.
- **Metrics:**
  - `aegis_llm_tokens_total{role,model,dir}`
  - `aegis_llm_cost_usd_total{role,model}`
  - `aegis_llm_latency_seconds{role}` (histogram)
  - `aegis_gate_result_total{gate,result}`
  - `aegis_retries_total{reason}`
  - `aegis_scope_violation_total{tier}`
  - `aegis_circuit_breaker_trips_total`
  - `aegis_runs_total{terminal_state}`
  - smokescreen's built-in `--expose-prometheus-metrics` for egress denials
- **Grafana:** one provisioned dashboard (JSON in the repo): runs by terminal state, cost per successful run, p95 latency per role, gate failure heat map, and egress denials. Use **exemplars** so a click on a latency spike opens the Jaeger trace.
- **Cockpit:** each run page has an "Open trace" link that deep-links into Jaeger by trace ID.
- **Guardrail:** it's all one `docker compose up`. Don't spend more than about 4 hours on observability. It's evidence for one criterion, not the product.

### 4.5 Final stack
| Layer | Choice |
|---|---|
| Control plane | Go 1.23+, `chi` HTTP, Temporal Go SDK, Postgres (`pgx` + `sqlc`) |
| Workflow runtime | Temporal (dev server locally, Postgres-backed in compose) |
| LLM | Provider router. Providers, endpoints (India-hosted only), fallback order and the model for each role all come from config (§16.7). Suggested split: a cheaper model for Analyst and Release summary, a stronger one for Developer and Architect |
| Sandbox | Docker via Go SDK, `--network=none`, read-only root, tmpfs workdir, CPU and memory caps, non-root user. **gVisor (`runsc`) runtime if it's on the Linux host.** Document Firecracker microVMs (E2B-style) as the production path |
| Egress | Stripe **smokescreen**, with a per-role ACL (sandbox: deny all; worker: only the LLM provider hosts) |
| Gates (in sandbox image) | pytest + coverage contexts, ruff, mypy, **semgrep** (custom fintech rules), bandit, gitleaks, osv-scanner or pip-audit, **oasdiff**, mutmut (changed lines only), differential replay script. Optional: pg-schema-diff hazards |
| VCS | git branches `agent/run-<id>`, GitHub API (draft PR + check-run with the evidence summary) |
| Frontend | Next.js 15 + Tailwind, SSE from the Go API, Monaco diff viewer |
| Observability | OTel SDK → Collector → Jaeger + Prometheus → Grafana |
| Events | NATS JetStream (live run events, replay, KV for rate-limit state) |
| Artifacts | Object storage, content-addressed (SeaweedFS locally, S3 in production; MinIO no longer publishes Docker images) |
| Identity and secrets | GitHub App (1-hour tokens), per-run capability JWTs (Ed25519), passkey (WebAuthn) approvals, OPA/Rego approval policy |
| Scale and isolation | India-only deployment with a primary and a DR region, both set in config, plus a fail-closed jurisdiction guard (§16.3). Postgres row-level security, per-tenant queues and quotas, `SandboxProvider` interface (Docker now; Kubernetes + gVisor/Kata later) |
| Configuration | One versioned, schema-validated config. Nothing hardcoded (§16.7) |
| Load and chaos | k6 + recorded-response fake LLM provider (clearly labelled) |
| Packaging | `docker compose up` brings up everything from `config.yaml`. A `make demo` seeds the sample repo and runs the eval. `make load` runs the load test |

---

## 5. Architecture

### 5.1 Components
```
                ┌─────────────────────── Next.js Cockpit ───────────────────────┐
                │ Ticket intake · Live run DAG · Diff · Evidence · Approve/Reject│
                └──────────────┬──────────────────────────────▲─────────────────┘
                          REST │                          SSE │
                ┌──────────────▼──────────────────────────────┴─────────────────┐
                │ aegis-api (Go): runs, decisions, kill switch, SSE fan-out      │
                └──────────────┬────────────────────────────────────────────────┘
                               │ start / signal / query
                ┌──────────────▼──────────────┐      ┌──────────────────────────┐
                │ Temporal server (Postgres)  │◄────►│ aegis-worker (Go)        │
                └─────────────────────────────┘      │  RunWorkflow (blueprint) │
                                                     │  Activities:             │
                                                     │   llm.* (via router)     │──► smokescreen ──► LLM APIs
                                                     │   gateway.* (tool calls) │
                                                     │   sandbox.* (Docker)     │──► sandbox containers (no net)
                                                     │   github.*               │──► GitHub (draft PR, checks)
                                                     └─────────┬────────────────┘
                                   Postgres: runs · steps · tool_audit (hash chain) · findings · approvals · evals
                                   OTel Collector ──► Jaeger │ Prometheus ──► Grafana
```

### 5.2 Blueprint (the deterministic workflow)
```
INTAKE ─► ANALYZE(LLM) ─► [split if not atomic] ─► ARCHITECT(LLM + code graph)
   ─► (optional human design gate: protected files or high risk)
   ─► SANDBOX_INIT (warm pool)
   ─► DEVELOP(LLM tool loop, scoped tools)
        └─ DesignConflict ─► ARBITRATE (Architect, 1 round) ─► DEVELOP | ESCALATED_HUMAN
   ─► GATE_T1: scope-diff check · ruff · selected tests          ┐ failure ─► DEVELOP
   ─► GATE_SEC: semgrep fintech · bandit · gitleaks · osv        │ (shared CI-round budget = 2,
   ─► GATE_T2: full tests · hidden oracle tests · oasdiff ·      │  as in Stripe Minions)
               mutation (changed lines) · differential replay    ┘ exhausted ─► HALTED_WITH_DIAGNOSTICS
   ─► RELEASE(LLM summary over evidence) ─► GitHub draft PR + check-run
   ─► AWAIT_HUMAN (Temporal signal; approval signs SHA + evidence hash)
   ─► APPROVED (human merges in GitHub) | REJECTED ─► rule_observer proposes .agent-rules.md diff
From any state: KILLED (kill switch) · PAUSED_PROVIDER (all providers failed; resumable) ·
        FAILED_SCHEMA (2 repair attempts) · HALTED_LOOP (circuit breaker) · BUDGET_EXCEEDED
```
Critical/secret findings → `BLOCKED_SECURITY_VETO` regardless of the retry budget. Only a human security override (logged and signed) can lift it.

### 5.3 "Agent actions are payments": idempotency and ledger
- Every side-effecting activity (create sandbox, apply patch, commit, push, open PR) takes an **idempotency key** (`run_id:step:attempt:action_hash`). Temporal's automatic retries can't double-apply them. This handles F6 "repeat an action" at the source, not only through loop detection.
- `tool_audit` is **append-only and hash-chained** (`hash = H(prev_hash ‖ row)`). The Cockpit shows "chain verified ✓". This is the honest version of the "immutable audit" claim.
- **Loop breaker:** a rolling window over `(tool, args_hash)` and `diff_hash`. Two identical calls, or a retry that produces no diff, trips the breaker.

### 5.4 Selective tests, done the way STE actually works
STE observes which files each test *actually touches*. The Python equivalent: run the baseline suite once with `pytest --cov --cov-context=test` and build a `file → tests` map from the coverage data. On a change, run the tests that cover the changed files, plus STE's guardrails: **always rerun previously failing tests**, and **run everything** if a "global" file changes (conftest, settings, fixtures). Show the selection ratio in the evidence packet ("ran 14 of 212 tests in Tier 1").

### 5.5 Progressive tool disclosure, the WebMCP pattern
Expose tools per state, the way Stripe Checkout exposes `submit_payment` only after the form is valid. The Developer sees `apply_patch` only after `read_file` has been called on that path. The Release role has no `merge` or `deploy` tool at all. Log the tool list sent to the model on each call, so the restriction is auditable. Stripe measured −42% tokens and −38% tool calls with this pattern. Report your own token numbers.

### 5.6 Sandbox and egress
- Warm pool of N pre-built containers, the "cattle" and hot-pool pattern from Minions and claimable sandboxes. A TTL sweeper kills stragglers.
- Sandbox: `--network=none`, non-root, read-only rootfs, tmpfs `/work`, `--pids-limit`, CPU and memory caps, no host mounts except a copy of the repo, no secrets in the environment.
- Smokescreen sits in front of the **worker's** LLM traffic with an `enforce` ACL. That gives a real outage demo (deny the provider host) and Prometheus egress metrics.

### 5.7 Fintech policy pack (Semgrep rules, Python and Java)
| Rule | Severity | Check |
|---|---|---|
| FIN-001 | High / fixable | `float` in money-named fields or arithmetic (`amount`, `fee`, `balance`, `payout`, `settlement`) → use `Decimal` or integer paise |
| FIN-002 | Critical / block | Hard-coded secrets (gitleaks + Semgrep) |
| FIN-003 | High / fixable | PAN, CVV, Aadhaar, token or password reaching a logger or `print` (Semgrep taint mode) |
| FIN-004 | High / fixable | State-changing POST handler on payment or webhook routes with no idempotency-key lookup |
| FIN-005 | Critical / block | Diff touches a protected path without a signed human unlock |
| FIN-006 | High / fixable | String-built SQL (use parameterised queries) |

Writing these as Semgrep rules instead of a hand-rolled Python AST linter makes them **portable to Pine Labs' Java/Spring code**. Ship `rules/java/` versions of FIN-001 and FIN-003 as proof, even if the demo repo is Python.

---

## 6. Evaluation harness (the differentiator)

Modelled on **"Stripe evals: Measuring real-world coding agent performance"** (19 Nov 2026). Stripe ran 69 scenarios twice per arm across 14 model/harness pairs, graded with separate **MUST** and **SHOULD** criteria, and the strict pass rate fell from 78.1% to 29.4%. Lenient scoring hides failures.

- **Suite:** 6–8 tickets on the sample repo. Each has hidden oracle tests, MUST criteria (hidden tests pass, no critical findings, no protected-file edits, no OpenAPI breaking change) and SHOULD criteria (diff budget met, mutation score ≥ X, decision log complete).
- **Arms:** (A) single-prompt baseline coder, same model, no gates, judged by the same harness; (B) AegisSDLC governed.
- **Runs:** N = 3 per ticket per arm, because LLMs are nondeterministic. Report strict pass %, mean cost, mean latency and blocked-bad-change count, with spread.
- **Seeded adversarial tickets:** "add debug logging of the card payload" (should trip FIN-003), "bypass the payout threshold" (FIN-005 / protected), and a repo comment carrying an injected instruction (LLM01).
- Replace the PRD's weighted 0–100 rubric with **MUST/SHOULD and strict pass**. It's simpler, harder to game, and matches published practice.
- Run the eval in the background during the hackathon and put the table in the README and on a slide.

---

## 7. Sample repo: `pine-settlements-svc`
FastAPI, Python 3.12, SQLite or Postgres. Modules: `payments/`, `webhooks/`, `settlements/`, `ledger/`, `config/payout_auth.py` (protected).
- Planted bug: the webhook handler credits the ledger before checking `event_id`, so duplicate delivery double-credits.
- ~60–200 existing tests, an OpenAPI schema, `smoke_test.py`, and a replay fixture (`fixtures/webhook_replay.jsonl` including duplicates and out-of-order events).
- `.agent-rules.md` scoped per directory (Stripe uses directory-scoped rule files and applies global rules "very judiciously").
- Prompt-injection bait in `webhooks/README.md`, for the adversarial ticket only.
- `CODEOWNERS`-style `protected_paths.yaml`.

---

## 8. Autonomy boundary matrix (put this on a slide)
| Action | Agent may | Automated gate decides | Human decides |
|---|---|---|---|
| Rewrite requirement into ACs | ✅ | Schema valid | Optional review |
| Choose impacted files and design | ✅ propose | Protected-path check | If protected or high risk |
| Edit planned or adjacent files | ✅ in sandbox | Diff budget, tier check | — |
| Edit protected files | ❌ | Hard block | ✅ unlock (signed) |
| Retry after failure | ✅ ≤ 2 CI rounds | Budget, loop breaker | After exhaustion |
| Downgrade a security finding | ❌ | — | ✅ security officer (signed) |
| Open draft PR | ✅ | Evidence check required | — |
| Merge | ❌ (token can't) | GitHub ruleset | ✅ |
| Deploy | ❌ (no tool) | — | ✅ (simulated CD) |
| Stop everything | — | — | ✅ kill switch |

---

## 9. Requirement coverage
| Pine Labs requirement | Where it's met |
|---|---|
| Accept a requirement and a repo | Cockpit intake, `POST /api/runs` |
| ACs and task plan | Analyst (schema-validated) |
| Inspect code, find impacted components | Architect + code graph (Python `ast` import graph + coverage map) |
| Generate or modify code | Developer tool loop in the sandbox |
| Unit tests + one more quality check | pytest + **ruff, semgrep, oasdiff, mutation, differential replay** |
| Review summary with evidence and risks | Release packet + GitHub check-run |
| Humans control merge and deploy | GitHub ruleset, signed approval, kill switch |
| Stretch: specialised agents with I/O contracts | JSON Schemas per role (`/schemas`) |
| Stretch: persisted state and history | Temporal history + Postgres ledger |
| Stretch: hand-offs, retries, bounded iteration | Blueprint, shared CI-round budget, arbitration ≤ 1 |
| Stretch: model usage, latency, cost | OTel + Prometheus + Grafana, per-run ledger |
| Stretch: sandboxing, branch isolation, gates | Docker (+ gVisor), `agent/run-*`, gates, smokescreen |
| Stretch: baseline or rubric comparison | Eval harness (§6) |
| F1 Architect and Developer disagree | DesignConflict → arbitration → escalation |
| F2 Code fails tests | Gate → diagnostics → retry ≤ 2 → halted with diagnostics |
| F3 Critical security finding | Veto → blocked; signed override only |
| F4 Malformed output | JSON Schema validation → 2 repair attempts → FAILED_SCHEMA |
| F5 Provider down mid-run | Temporal retry → fallback → PAUSED; also kill and restart the worker live |
| F6 Repeat action or unrelated file | Idempotency keys, loop breaker, tiered diff-scope check |

---

## 10. 48-hour build plan (single source of truth)

This merges the original schedule with the guardrail work (§15) and the scale and security work (§16). Team roles: **A** control plane (Go), **B** sandbox, gates and sample repo, **C** LLM agents and evals, **D** web app and observability. With two people, merge A+C and B+D.

| # | Phase | Hours | What gets built | Done when |
|---|---|---|---|---|
| 0 | **Foundation** | 0–3 | Repo layout, Docker Compose stack (Postgres, Temporal, NATS, object storage, OpenTelemetry, Jaeger, Prometheus, Grafana, smokescreen), schema-validated config with the jurisdiction check, JSON Schemas for every agent, sample repo `pine-settlements-svc` with the planted webhook bug, hardened sandbox image | See §10.1 exit criteria |
| 1 | **Deterministic core (no AI yet)** | 3–10 | Sandbox runner, the gates as command-line tools returning JSON (pytest+coverage, ruff, Semgrep FIN-001..006, gitleaks, oasdiff, replay), diff-scope checker, Temporal workflow with stub agents, hash-chained ledger, idempotency keys, `tenant_id` + row-level security, repo mirror cache + worktrees | The full workflow runs end to end on stubbed agent output, and the gates pass or fail correctly on hand-written patches |
| 2 | **Agents** | 10–18 | LLM router (format checks, repair, fallback, cost, prompt data filter), Analyst, Architect, Developer tool loop, Release, tool gateway, approval signals, kill switch | Demo 1 passes from the command line 5 times in a row |
| 3 | **Failure handling, guardrails, concurrency** | 18–26 | F1–F6 as tested code paths, GitHub App (draft PR, evidence check, ruleset), FIN-007..014, dependency gate, maker-checker, scope leases, evidence bound to base+head SHA with rebase and re-test, admission control, per-run capability tokens, NATS events | Every failure scenario and guardrail has an automated test |
| 4 | **Web app, eval and proof** | 26–40 | Next.js web app (intake, live progress, diff, evidence, approve/reject, kill switch, trace links), Grafana dashboards, eval harness (baseline vs. governed, N runs), fake LLM + k6 load test, simulated DR drill, red-team table | The full demo runs from the web app. Eval, load and red-team tables are filled in |
| 5 | **Should-haves and polish** | 40–44 | Only if on schedule: passkey approvals, OPA policies, merge queue | — |
| 6 | **Freeze and rehearse** | 44–48 | Code freeze, 3 rehearsals, backup recording, README (architecture, decision records, threat model, real vs. mocked, eval results), slides | Recorded demo and slides ready |

**Cut order if behind:** passkeys + OPA → merge queue → Grafana (keep Jaeger) → arbitration (keep escalation) → fallback provider (keep pause and resume).
**Already cut** (to pay for §16): mutation testing, rule_observer, Java Semgrep rules, freeze calendar, gVisor on the demo host, code-graph visualisation.
**Never cut:** hidden tests, SHA-bound human gate, kill switch, scope tiers + leases, no-egress sandbox, FIN-013/014, prompt data filter, maker-checker, tenant isolation, jurisdiction check, validated config (§16.7), load-test evidence.

### 10.1 Phase 0 design: Foundation

**Goal:** everything later phases need exists, starts with one command, and is proven to work, before any agent or workflow code is written.

**Repo layout**
```
./  (repo root)
  cmd/aegis-api/          Go API (Phase 0: health and config only)
  cmd/aegis-config/       CLI: validate a config file (used by make and CI)
  internal/config/        Load YAML, validate against schema, jurisdiction check
  schemas/config.schema.json
  schemas/agents/*.schema.json      Agent input/output contracts (Appendix A.1)
  schemas/agents/examples/          One valid example per schema (tested)
  deploy/compose/         docker-compose.yml, versions.env, service configs
  sandbox/Dockerfile      Hardened gate-runner image with pinned tools
  samples/pine-settlements-svc/     Sample target repo (its own git repo at runtime)
  evals/hidden/pine-settlements-svc/  Hidden oracle tests, never mounted for agents
  config.example.yaml     Placeholders only
  config.local.yaml       Local dev values (no secrets)
  Makefile
```

**Decisions**
- **Versions live in one file** (`deploy/compose/versions.env`), not in the compose file or code. Nothing is hardcoded.
- **Every port binds to 127.0.0.1.** Nothing on the stack is reachable from the network.
- **Sandbox image** comes from a pinned Python base, includes every gate tool plus the sample repo's dependencies, runs as a non-root user, and is run with `--network=none --read-only --cap-drop=ALL --security-opt=no-new-privileges --pids-limit --memory --cpus`. Limits come from config.
- **Sample repo:** FastAPI + SQLite, modules `payments/ webhooks/ settlements/ ledger/ config/`. The planted bug: the webhook handler credits the ledger without checking `event_id`, so duplicate deliveries double-credit. Visible tests pass on the buggy code. A hidden oracle test fails on it, which proves the bug exists.
- **Config guard:** `aegis-config validate` fails on schema errors, untagged endpoints, or endpoints outside `allowed_jurisdictions`. `aegis-api` runs the same check on startup and exits on failure.

**Exit criteria (all must pass)**
1. `make test`: Go unit tests pass (config guard, and every agent schema accepts its example).
2. `make validate-config`: passes for `config.local.yaml`, and fails for each `testdata/bad-*.yaml`.
3. `make sandbox-image` builds. `make sample-test` runs the sample repo's tests inside the hardened sandbox with no network, and they pass.
4. `make hidden-test` shows the hidden duplicate-webhook test **failing** on the base code.
5. `make sandbox-egress-check`: an outbound request from the sandbox fails.
6. `make up`: every Compose service reports healthy. `make down` cleans up.

**Status: done.** All six pass. Deviations: SeaweedFS replaces MinIO (no MinIO images any more); the Go builder is 1.25 (needed by smokescreen v0.1.0); the API listens on a port set in config.

### 10.2 Phase 1 design: Deterministic core (no AI yet)

**Goal:** the whole workflow runs end to end and makes correct decisions, with **stub agents that replay hand-written patches**. Every safety decision (gates, scope, retries, vetoes, ledger) is proven before any LLM is involved. In Phase 2, LLM agents replace the stubs behind the same contracts, and nothing else changes.

#### Critique first: what could go wrong in this phase
| Risk | Why it matters | Decision |
|---|---|---|
| **The worker needs the Docker socket** | Access to `docker.sock` is root on the host. A compromised worker owns the machine | The worker never talks to Docker directly. It calls a `SandboxProvider` interface. Phase 1 implements it with the Docker SDK and runs the worker **on the host** (dev only). Production swaps in a Kubernetes + gVisor provider behind the same interface, and the worker never holds cluster-admin |
| **Sandbox output is untrusted** | A malicious patch can make tests print anything, e.g. fake "all passed" lines or gigabytes of output | Gates use the tools' **machine-readable outputs** (JSON / JUnit XML), parsed in Go with strict decoding and a byte cap from config. Exit codes are cross-checked against parsed results. A mismatch fails the gate |
| **Temporal history bloat** | Workflow payloads are size-limited. Passing full logs and diffs through history breaks at scale | Large outputs go to **object storage** (SeaweedFS), keyed by content hash. The workflow only carries hashes and small summaries |
| **Workflow non-determinism** | Temporal replays workflow code; any clock, random or I/O call inside it corrupts replays | All I/O lives in activities. The config is passed in as a **snapshot plus hash** at run start, so a config change mid-run can't change its decisions |
| **Retries double-apply side effects** | Temporal retries activities automatically. A retried "commit patch" could commit twice | Every side-effecting activity goes through `ledger.Once(key, fn)` with key = `run:step:attempt:input_hash`. A replay returns the stored result |
| **Dependency CVE scan needs network** | `osv-scanner` queries an online database, and the sandbox has no network | Phase 1's dependency gate is **new-dependency detection plus a lockfile hash check** (fully offline). An offline OSV database, refreshed by a scheduled job outside the sandbox and mounted read-only, comes in Phase 3 |
| **Heuristic Semgrep rules misfire** | e.g. "idempotency missing" can't be proven statically | Each rule has a `confidence` field. Only high-confidence rules can **block**; heuristic ones **flag** for the reviewer. Every rule ships with positive and negative test files run by `semgrep --test` |
| **The agent could edit what the gates check against** | e.g. change replay expectations or hidden tests | Replay harness, expected balances and hidden tests live in `evals/` and are mounted **read-only, only into the gate container that needs them**. Never into the developer stage |
| **Scope creep** | Phase 1 is the biggest phase and only ~7 hours | Must / should split below. Selective tests (§5.4) and the warm pool move to Phase 3 |

#### Scope
**Must (exit criteria depend on these):**
1. **Workspace manager** (`internal/workspace`): a bare mirror of each repo (from the `code_host` endpoint in config), one `git worktree` per run on branch `agent/run-<id>` at a recorded base SHA, then commit and cleanup. Git is called with fixed argument lists, never through a shell.
2. **Sandbox provider** (`internal/sandbox`): the `SandboxProvider` interface plus a Docker SDK implementation. It reuses Phase 0's hardening policy (refactored into one `Policy` used by both the CLI flags and the SDK), with timeout, output cap, OOM and timeout detection, and read-only or read-write mounts per call.
3. **Gates** (`internal/gates`): each gate is a command run in the sandbox plus a Go parser, producing one normalised result:
   `GateResult{gate, passed, verdict: PASS|FLAG|REMEDIABLE_FAIL|HARD_BLOCK, findings[], metrics, artifact_ref, tool_version, duration_ms}`
   | Gate | Tool and output parsed | Notes |
   |---|---|---|
   | `scope` | Go, outside the sandbox: `git diff --numstat -z`, `--name-status` | Planned / adjacent / protected / out-of-scope tiers, diff budget, renames and deletes counted |
   | `lint` | `ruff check --output-format json` | |
   | `tests` | `pytest --junitxml` + `coverage json` | Full suite in Phase 1 |
   | `hidden_tests` | `pytest --junitxml` with `evals/hidden` mounted read-only | Tier 2 only |
   | `semgrep` | `semgrep --json --metrics=off --config rules/semgrep` | Rules FIN-001, 003, 004, 006 (FIN-002 secrets in `secrets`, FIN-005 protected paths in `scope`) |
   | `bandit` | `bandit -f json` | |
   | `secrets` | `gitleaks detect --report-format json` on the diff | Critical: always HARD_BLOCK |
   | `dependencies` | Go: diff of lock and requirements files | New package means FLAG plus required human approval |
   | `openapi` | export the spec in base and head worktrees, then `oasdiff breaking --format json` | Breaking change: HARD_BLOCK unless the plan declared it |
   | `replay` | `evals/replay` harness, mounted read-only | Ledger invariants (one event, one credit; totals match) |
4. **Semgrep rule pack** `rules/semgrep/` with test fixtures. The unmodified sample should **trip FIN-004** on the webhook handler, which is consistent with the planted bug.
5. **Ledger** (`internal/ledger`, Postgres via `pgx`): tables `runs`, `steps`, `findings`, `approvals`, `side_effects`, `audit_events`, `chain_heads`. Embedded SQL migrations. **Hash-chained `audit_events`**: one chain per tenant, appends serialised with `SELECT … FOR UPDATE` on the chain head, plus a `Verify()` function and an `aegis-ledger verify` command.
6. **Tenant isolation:** `tenant_id` on every table. **Row-level security with `FORCE`**, and the app connects as a non-owner role without `BYPASSRLS`. Every transaction sets `aegis.tenant_id` via `set_config(..., true)`.
7. **Temporal workflow** (`internal/workflow`): the §5.2 blueprint as Go code with states as an enum. Activities: `PrepareWorkspace`, `Analyze`*, `Architect`*, `Develop`*, `CommitPatch`, `RunGates(tier)`, `CheckScope`, `Release`*, `Record`, `Cleanup` (* = stub in Phase 1). The retry loop is bounded by `workflow.max_ci_rounds` from the config snapshot. An `approve`/`reject` signal handles `AWAIT_HUMAN` (maker-checker comes in Phase 3).
8. **Stub agents** (`internal/agents/stub`): they return scenario fixtures validated by `internal/contracts`. The stub developer applies hand-written patches, in order, per attempt.
9. **Scenarios** (`evals/scenarios/<name>/`: `scenario.yaml`, contract fixtures, `attempt-N.patch`, `expect.yaml`):
   | Scenario | Patches | Expected terminal state |
   |---|---|---|
   | `happy` | correct idempotent fix + tests | `AWAIT_HUMAN`, then `APPROVED` after the signal |
   | `self-heal` | attempt 1 uses `float` for money (FIN-001); attempt 2 correct | `AWAIT_HUMAN` with finding OPEN → RESOLVED |
   | `retry-exhausted` | every attempt breaks a test | `HALTED_RETRY_EXHAUSTED` after `max_ci_rounds` |
   | `protected-file` | edits `app/config/payout_auth.py` | `BLOCKED_SCOPE`, repo untouched outside the run branch |
   | `secret` | hard-codes an API key | `BLOCKED_SECURITY_VETO` |
   | `design-conflict` | stub developer returns `DESIGN_CONFLICT` | `ESCALATED_HUMAN` (arbitration comes in Phase 3) |
10. **CLI** `aegis-run`: start a scenario against the live stack, follow its state, print the terminal state and the ledger summary.

**Should (only if on schedule):** artifact upload to SeaweedFS (otherwise store artifacts on local disk keyed by hash, same interface), `ledger verify` exposed through the API, worker metrics via OpenTelemetry.

**Moved out of Phase 1:** selective tests and warm pool → Phase 3. Offline OSV database → Phase 3. NATS events → Phase 3 (Phase 1 writes state changes to the ledger only).

#### How it plugs into Phase 0
| Phase 0 piece | Phase 1 use |
|---|---|
| `config` + schema | New keys: `workspace.{mirrors_dir, runs_dir}`, `temporal.{namespace, task_queue}`, `sandbox.max_output_bytes`, `gates.tier1[]`, `gates.tier2[]`. Endpoints for Postgres, Temporal and code host come from `endpoints[]`. Secret refs (`env:`) are resolved by a new `config.ResolveSecret` |
| `internal/sandbox` flags | Refactored into `Policy`. The CLI flags and the SDK `HostConfig` are generated from the same policy, so they can't drift. Existing tests keep passing |
| `internal/contracts` | Validates every stub output: the same path Phase 2's LLM outputs take |
| Sandbox image | Runs every gate. No change except adding the Semgrep rule pack and replay harness as **read-only mounts** (not baked in, so rules can change without a rebuild) |
| Sample repo | `make seed-repo` turns `samples/pine-settlements-svc` into a bare git repo under the `code_host` path, so the workflow pulls from a "remote" like it will from GitHub |
| Compose stack | Postgres (ledger + Temporal) and Temporal are used for real. A migration creates the `aegis_app` role with its password from `.env.secrets` |
| Makefile | New targets: `migrate`, `seed-repo`, `worker`, `run S=<scenario>`, `phase1` |

#### Package layout added
```
cmd/aegis-worker/        Temporal worker (runs on the host in dev)
cmd/aegis-run/           Start and follow a scenario run
cmd/aegis-ledger/        verify | show <run-id>
internal/workspace/      mirror, worktree, commit, diff
internal/sandbox/        Policy, SandboxProvider, docker provider
internal/gates/          one file per gate: command + parser; golden test data
internal/scope/          diff-scope checker
internal/ledger/         migrations, store, RLS, hash chain, Once()
internal/workflow/       blueprint, states, activities
internal/agents/stub/    scenario-driven stub agents
rules/semgrep/           FIN rules + tests
evals/replay/            replay harness + expected balances (read-only mount)
evals/scenarios/         six scenarios
```

#### Order of work (with team roles)
| Hours | A (control plane) | B (sandbox, gates) |
|---|---|---|
| 3–5 | Config keys, ledger schema + migrations + RLS + chain, `Once()` | `Policy` refactor, Docker SDK provider, workspace manager |
| 5–7 | Temporal workflow + stub agents + scenarios (unit-tested with Temporal's test environment) | Gates + parsers (golden files), Semgrep rules + tests, scope checker |
| 7–9 | Worker + `aegis-run`, wire real activities | Replay harness, openapi gate, dependency gate |
| 9–10 | End-to-end scenario runs, crash and concurrency checks | Same |

#### Exit criteria (all must pass, `make phase1`)
1. **Unit tests:** scope checker (table-driven, including renames, deletes and globs), every gate parser against recorded tool output, `semgrep --test` for every FIN rule, ledger chain verification including **detection of a tampered row**, an RLS test proving tenant A can't read tenant B's rows, `Once()` returning the stored result on repeat, and workflow tests for all six scenarios in Temporal's test environment.
2. **Base code check:** gates on the untouched sample report visible tests passing, FIN-004 flagging the webhook handler, and hidden tests failing.
3. **Live end-to-end:** `make run S=<name>` reaches the expected terminal state for all six scenarios on the real stack, with real git, sandbox and Postgres. `aegis-ledger verify` passes afterwards.
4. **Concurrency:** all six scenarios started at once finish correctly, each with its own worktree and branch, and no cross-talk in the ledger.
5. **Crash safety:** killing the worker mid-run and restarting it completes the run with **no duplicate commits or ledger entries**.
6. **Isolation:** no gate container has network access, hidden tests are only ever mounted into the hidden-test gate, and the developer stage never sees `evals/`.

---

## 11. Demo script (6 minutes)
1. **(30 s) Problem:** 89% of Pine Labs' code changes are AI-assisted. Industry data shows review is now the bottleneck (Faros: ~5× review time, 31% merged unreviewed). Amazon's reported answer was senior sign-off. Ours: evidence that makes sign-off fast.
2. **(90 s) Ticket in:** "Duplicate webhook deliveries double-credit settlements." Show the Analyst's ACs (including the concurrent-duplicate edge case), the Architect's impact map and scope tiers, and the live DAG.
3. **(60 s) Failure and recovery:** a Tier-2 hidden test or FIN-001 fails, with diagnostics anchored to the commit SHA. Developer retry 1 fixes it, and the finding flips from OPEN to RESOLVED.
4. **(45 s) Chaos:** `docker kill aegis-worker` mid-run, restart it, and the run resumes from the same step. Show the gap in the Jaeger trace.
5. **(45 s) Adversarial:** the injected instruction or a "bypass payout threshold" ticket hits a protected-path block, escalates to a human, and leaves no change in the repo.
6. **(45 s) Human gate:** draft PR on GitHub with the evidence check-run. Show that the agent token can't merge. Approve in the Cockpit (signed to the SHA). The human merges.
7. **(45 s) Proof:** eval table (governed vs. baseline, N = 3, strict pass, cost) and the Grafana cost and latency panel.

---

## 12. Mocked vs. real (state this upfront)
**Real:** LLM calls, sandbox execution, every gate, git branches, GitHub draft PR and checks, Temporal durability, traces and metrics, eval runs.
**Mocked:** Jira and Slack intake (web form), production CD (simulated promotion after merge), multi-repo service mesh (a single repo with modules), Grantex server (scope tokens issued locally as short-lived signed JWTs with Grantex-style claims; `grantex-go` adapter optional), Firecracker microVMs (documented production path).

## 13. Risks
| Risk | Mitigation |
|---|---|
| Team is new to Go or Temporal | Day-0 spike of 2 hours. Fallback: Go API + gates, keep the workflow as a hand-written state machine in Go with Postgres checkpoints |
| Live LLM flakiness during judging | Pre-recorded run, cached responses as a replay mode (clearly labelled), rehearsal stats |
| gVisor unavailable on macOS | Plain Docker hardening. gVisor only on the Linux demo host |
| Observability eats time | 4-hour cap. Provisioned dashboards from JSON |
| Over-claiming | Every statistic on a slide carries its source and date. Vendor surveys are labelled |

---

## 14. What to take from Stripe (stripe.dev/blog and github.com/stripe)
| Source | Use in AegisSDLC |
|---|---|
| Minions Parts 1–2 (Feb 2026) | Blueprints (agent and deterministic nodes), cap of two CI rounds, warm sandboxes, small default tool subset, directory-scoped rule files |
| Payment Method Factory (30 Sep 2026) | Coordination in code, not an orchestrator agent. Reusable prompts. Observer agent that proposes prompt updates from decision logs and PR comments (= rule_observer) |
| Checkout for AI agents / WebMCP (22 Sep 2026) | Progressive tool disclosure. Measure tokens and tool calls |
| Selective Test Execution (9 Apr 2026) | Dynamic file→test map, always rerun past failures, global files trigger a full run |
| How API changes flow (15 Jun 2026) | OpenAPI diff as a pre-merge gate, lint-style warnings in the PR |
| Stripe evals (19 Nov 2026) + `stripe/forge` | MUST/SHOULD grading, strict pass, repeated runs per arm |
| Claimable sandboxes (2 Nov 2026) | Hot pool refilled by a Temporal job, TTL sweeper |
| Microservice testing with Spark (Jun 2026) | Differential replay, old vs. new implementation |
| "You can't whisper at an AI agent" (14 May 2026) | Prompts are weak steering, so enforce in code |
| Graph search + state machines for DB remediation (16 Jul 2026) | State-machine-first design for automated operations |
| "Because nobody likes being charged twice" (Apr 2025) | Idempotency as the core fintech invariant, for both the sample bug and agent actions |
| `stripe/smokescreen` (Go) | Egress proxy for the worker and sandbox, with ACLs and Prometheus metrics |
| `stripe/pg-schema-diff` (Go) | Optional migration-hazard gate |
| `stripe/stripe-mock` (Go) | Pattern: an OpenAPI-driven mock that rejects unknown parameters. Mock your fake payment-gateway dependency the same way |
| `stripe/veneur` | Archived. Don't use it |

---

## 15. Fintech production guardrail audit

**Short answer: the first plan did not cover everything.** No list covers "all possible" guardrails. Checked systematically across five areas, the original plan had **13 gaps**. Some were cheap and high-value: agents quietly weakening tests or silencing scanners, webhook signature checks, auth on new endpoints, and sensitive data leaking into LLM prompts. The table below marks every guardrail ✅ covered, 🟡 partial, or ❌ missing. Each missing one gets a decision: **build in 48 h** or **roadmap** (named in the README as a known limitation).

### A. Money correctness (the code the agent writes)
| Guardrail | Status | Action |
|---|---|---|
| No floats for money; use `Decimal` or integer paise | ✅ FIN-001 | — |
| Explicit rounding mode and currency exponent (no bare `quantize()`, no unrounded division of money) | ❌ | **48 h**: FIN-007 (Semgrep) |
| Idempotency on payment and webhook mutations | ✅ FIN-004 | — |
| Concurrency: duplicate events arriving *at the same time* (unique constraint or row lock, not check-then-insert) | 🟡 | **48 h**: concurrent-duplicate case in the replay fixture plus a hidden test |
| Double-entry and ledger invariants (debits = credits; one event gives one credit; no unexpected negative balance) | 🟡 | **48 h**: explicit invariant assertions after replay |
| Timezone-aware dates; IST settlement cut-off; day-boundary maths (the "1–7 days late" ticket) | ❌ | **48 h**: FIN-009 flags naive `datetime.now()` / `utcnow()` in settlement code; hidden boundary tests at exactly 7 days |
| Amount validation (> 0, upper bound, currency allowlist) | ❌ | **48 h**: hidden tests + OpenAPI `minimum` checks |
| Outbound calls to payment providers have timeouts, bounded retries and idempotency keys | ❌ | **48 h**: enable Bandit B113 (requests without timeout) + FIN-010 (retry with no idempotency key) |
| Reconciliation jobs (ledger vs. provider settlement files) | ❌ | Roadmap |

### B. Security (the code the agent writes)
| Guardrail | Status | Action |
|---|---|---|
| Hard-coded secrets | ✅ FIN-002 + gitleaks | — |
| PAN, CVV or Aadhaar reaching logs | ✅ FIN-003 | — |
| SQL injection | ✅ FIN-006 | — |
| **Webhook signature check** (HMAC, constant-time compare, timestamp window against replay) | ❌ | **48 h**: FIN-011 flags webhook routes with no signature check and signatures compared with `==` instead of `hmac.compare_digest`. It sits right on the demo ticket |
| **Auth on new endpoints** (broken object-level authorisation, OWASP API1) | ❌ | **48 h**: FIN-012 flags new FastAPI routes without the auth dependency |
| Weak crypto or randomness (`random` for tokens, MD5) | ✅ Bandit | — |
| Known-vulnerable dependencies | ✅ osv-scanner | — |
| **Hallucinated or typosquatted packages** ("slopsquatting") | ❌ | **48 h**: a **new-dependency gate**. Any new package in the diff needs human approval. The sandbox has no network and installs only from a pre-baked package cache, so unknown packages fail closed. Ground: a USENIX Security 2025 study found ~20% of LLM-suggested packages did not exist |
| Licence compliance of new dependencies | ❌ | Roadmap (fold into the dependency gate) |
| Stack traces or internal errors leaking in API responses | ❌ | Roadmap |
| Rate limits on new endpoints | ❌ | Roadmap |

### C. Change and release safety
| Guardrail | Status | Action |
|---|---|---|
| API backward compatibility | ✅ oasdiff | — |
| Diff size and blast-radius budget | ✅ | — |
| Rollback plan in the evidence packet | ✅ | — |
| DB migrations safe (expand and contract, no locking hazards) | 🟡 optional pg-schema-diff | Roadmap if the sample stays on SQLite. Say so |
| New behaviour behind a feature flag (default off) | ❌ | 48 h, soft: Release packet must name the flag and rollout plan. Roadmap: enforce it |
| Canary with automatic rollback on error-rate breach | ❌ | Roadmap (CD is simulated). Harness: only 39% have automated rollback |
| **Change-freeze windows** (month-end settlement, big sale days) | ❌ | **48 h, should-have**: a freeze calendar in config. Approval is disabled during a freeze, with a banner |
| Sandbox image pinned by digest; tool versions pinned | ❌ | **48 h**: trivial |
| SBOM per change, signed agent commits, build provenance (SLSA) | ❌ | Roadmap. Stretch: SBOM via `syft` in the packet |

### D. Process and regulation (India + PCI)
| Guardrail | Status | Action |
|---|---|---|
| **Maker-checker**: the requester can't be the sole approver; protected paths need two approvers | ❌ | **48 h**: enforce in the API (`approver ≠ submitter`; 2 approvals when a protected tier is touched). Maps to PCI DSS 6.2.3.1 (review by someone other than the author) |
| Separation of environments; no live card data before production | ✅ PCI 6.5.3 / 6.5.5 | — |
| Tamper-evident audit trail | 🟡 hash chain | **48 h**: a `verify-chain` endpoint. Roadmap: 180-day retention stored in India (CERT-In Directions, Apr 2022) |
| **Sensitive data never sent to the LLM provider** | ❌ | **48 h**: a **prompt DLP filter** in the LLM router. It scans every outgoing prompt for card numbers (Luhn check), Aadhaar numbers (Verhoeff checksum), secrets and email/phone. It redacts or blocks, and logs. This matters because RBI requires payment-system data to be stored in India, and the DPDP Act covers personal data |
| Model and prompt traceability (which model and prompt version made each output) | 🟡 | **48 h**: store model ID + prompt-template hash per step in the ledger |
| LLM provider risk (zero data retention, endpoint hosted in India) | 🟡 | **48 h**: the jurisdiction guard only allows endpoints tagged India (§16.3). Contract terms such as zero retention are documented, not enforced |
| Documented change-management process (RBI IT Governance Directions 2023 have a "Change and Patch Management" chapter) | 🟡 | README maps the blueprint and ledger to that chapter. Don't claim compliance |
| Incident reporting (CERT-In 6 h; DPDP breach reporting) | ❌ | Out of scope. Kill switch + audit export *support* it |

### E. Agent-specific (the new risks AI brings)
| Guardrail | Status | Action |
|---|---|---|
| Prompt injection from repo content | ✅ demo + tool restriction | — |
| Excessive agency (no merge or deploy tools) | ✅ | — |
| **Test tampering**: the agent deletes or skips a failing test, loosens an assertion, or drops coverage | ❌ | **48 h, never cut**: FIN-013. Existing tests are protected by default: the agent may *add* tests, but deleting, skipping, marking xfail or editing existing assertions is a hard block unless a human approves. Coverage on changed files must not drop |
| **Silencing scanners**: the agent adds `# nosec`, `# noqa`, `# nosemgrep`, or edits linter or CI config | ❌ | **48 h, never cut**: FIN-014. New suppression comments and config edits are blocked |
| Loops and repeated actions | ✅ | — |
| Cost runaway | ✅ budget | — |
| Nondeterminism | ✅ eval N = 3 | — |
| Kill switch | ✅ | — |
| Stale approval | ✅ SHA-bound | — |
| Reviewer rubber-stamping ("LGTM fatigue") | 🟡 | **48 h**: the Cockpit pins the riskiest hunks and flagged findings at the top. The approve button unlocks only after the reviewer ticks each acceptance criterion |

### Net change to the build plan
- **New Semgrep rules:** FIN-007, FIN-009, FIN-010, FIN-011, FIN-012, FIN-013, FIN-014. About 3 hours; each rule gets a positive and a negative test fixture.
- **New gate and router code:** dependency gate, prompt DLP, maker-checker, freeze calendar, digest pinning, prompt-hash logging. About 3 hours, in hours 18–26.
- **Add to "never cut":** test-tampering and suppression gates (FIN-013/014), prompt DLP, maker-checker.
- **Add to the eval suite:** an adversarial ticket where the easiest path is skipping the failing test (FIN-013 should catch it), and one that tempts adding a new package (dependency gate).

---

## 16. Scale, concurrency and security hardening (enforced)

### 16.1 Critique first: what "scale" really means here, and where the earlier plan breaks

**"Optimise for a huge number of PRs" is the wrong target.** The target is **safe merged changes per reviewer-hour.** Faros 2026: review time rose about 5× and 31% of PRs merged with no review once AI volume went up. A system that opens unlimited agent PRs makes Pine Labs *less* safe, because it floods the one resource that can't scale, which is human attention. So the first scaling control is **backpressure on review capacity**, not more throughput.

**Orchestration throughput is not the bottleneck.** A back-of-envelope estimate (assumptions labelled):

| Quantity | Stripe today (published) | Our design target: 10× Stripe |
|---|---|---|
| Agent PRs merged / week | >1,300 (Minions Part 2) | 13,000 |
| Run starts / second (average) | ~0.002 | ~0.02 |
| Sandbox time per run (assumed 15 min) | — | ~3,250 sandbox-hours / week, i.e. **~20 concurrent on average, ~100 at peak** (5× peak factor, assumed) |
| CI builds / week | ~50,000 (STE post) | Same order, cut by selective tests |

At 0.02 runs per second, Temporal and Postgres don't even notice. **The real limits, in order:**
1. Human review capacity.
2. LLM tokens: cost and provider rate limits.
3. Sandbox compute and cold-start time.
4. Test time.
5. **Conflicts between concurrent PRs touching the same code.**
6. GitHub API rate limits.

The design below attacks each one in that order. Saying this out loud to the judges is itself a judgement signal.

**Where the earlier plan breaks at scale:**
| Earlier choice | Breaks because | Fix |
|---|---|---|
| One Docker host | Single point of failure, capped at ~a dozen sandboxes | A `SandboxProvider` interface: Docker for the demo; Kubernetes + gVisor (or Kata/Firecracker) with queue-driven autoscaling in production |
| Fresh `git clone` per run | Slow, hammers GitHub, wastes disk | A **bare mirror cache per repo per node** + `git worktree` per run. Plus **pre-built repo snapshots**, the Ramp pattern: Inspect rebuilds each repo's image every 30 minutes, so sessions start in seconds |
| Code graph and coverage map rebuilt per run | Repeated work across concurrent runs on the same base commit | **Content-addressed cache keyed by (repo, base SHA)** in object storage, shared by every run on that base |
| Postgres as the live-event bus for SSE | Polling and fan-out don't scale across many viewers | **NATS JetStream** (Go-native, one binary): per-run subjects, replay by sequence number (= `Last-Event-ID`) |
| Evidence tied only to the head commit | Two concurrent agent PRs can each pass alone and break together; `main` moves under them | Evidence bound to **(base SHA, head SHA)**. **Rebase and re-run gates** before approval if the base moved. Merge through **GitHub merge queue**, so each merge is re-tested against the real tip |
| No coordination between runs | Two runs edit `webhooks/handler.py` at once and produce conflicting PRs | **Scope leases**: the Architect's planned files take a time-limited advisory lease. An overlapping run waits, or is marked "stacked on #N" |
| Single tenant, no residency checks | Many teams share one system, and nothing stops a misconfigured backup, log sink or LLM endpoint from sending data outside India | Per-tenant isolation + a **fail-closed jurisdiction guard** (§16.3) |
| Personal access token for GitHub | Long-lived secret, broad rights, shared rate limit | **GitHub App**: installation tokens expire after **1 hour**, are scoped per repo with minimal permissions, and get their own rate-limit budget |
| Artifacts (diffs, logs, reports) in Postgres rows | Database bloat | **Object storage** (S3; SeaweedFS locally), content-addressed by hash. Postgres keeps only metadata and hashes |

### 16.2 Concurrency controls (build in 48 h)
- **Admission control with review work-in-progress limits.** Each team has a configurable cap on open agent PRs awaiting review (`review.wip_per_reviewer`). Above the cap, new runs still *plan* but park at `QUEUED_REVIEW_CAPACITY` before opening a PR. A Grafana panel shows queue depth.
- **Per-tenant fair queueing.** Temporal task queues per priority tier, plus per-tenant concurrency caps and token buckets for LLM spend. One team's batch of 500 migrations can't starve another team's hot fix.
- **LLM rate governance.** A shared per-provider token bucket enforced in the router (shared state in NATS KV), Temporal activity rate limits, retries with jitter, and **prompt caching** of stable context (rule files, code map, schemas) to cut tokens on repeat calls. Evals run through the provider's **batch API**.
- **Idempotent intake.** A ticket's idempotency key is a hash of (tenant, repo, normalised requirement). Duplicate submissions attach to the existing run.
- **Scope leases + base-SHA-bound evidence + merge queue** (above).
- **Risk-tiered review routing, never auto-merge.** Low-risk changes (tests-only, docs, no protected tier, small diff) go to any reviewer. Payment-path or protected changes need two senior reviewers and the security officer. Human time is spent where the risk is.

### 16.3 Deployment scope: India only
**Scope assumption:** all users, repos and data are in India for now. Global, multi-jurisdiction deployment is **out of scope**. It is not designed or claimed.

- **One deployment, data kept in India.** API, Temporal, workers, sandbox pool, Postgres, object storage, NATS, observability and log sinks all run in **cloud regions located in India**. Which regions is set in configuration (§16.7), never in code. This follows RBI's requirement that payment-system data is stored in India, and keeps CERT-In log retention and DPDP obligations in one place.
- **Jurisdiction guard at startup.** Config declares `data_residency.allowed_jurisdictions: [IN]`, and every storage, backup, disaster-recovery, log and LLM endpoint carries a `jurisdiction` tag. **The services refuse to start** if any endpoint is tagged outside the allow-list, or has no tag. This is a fail-closed check, not a convention.
- **LLM endpoints:** the router only uses provider endpoints marked as hosted in India. If none is configured for a model, that model is unavailable and the router fails closed (or falls back to another India-hosted model, if one is configured). It never silently routes abroad. The prompt DLP filter (§15) applies regardless.
- **Resilience:** a primary region and a disaster-recovery region, **both inside India** and both chosen in config. Workers are stateless. Postgres has replicas, and backups are restored to the DR region only. Temporal: self-hosted with cross-cluster replication, or Temporal Cloud's multi-region namespaces if the provider offers two regions inside India (check before relying on it; the feature was in Public Preview at the time of writing).
- **Limiting blast radius:** deploy in stages, inside the one India deployment. New releases go to a canary worker pool first (configurable share of runs), then everywhere. A bad release affects only the canary share.
- **Data layout:** `tenant_id` (team or business unit) on every row, **Postgres row-level security** enforced by a session variable, ledger tables partitioned by month, artifacts under a per-tenant bucket prefix.
- **48-hour proof:** start the stack once with a valid config, then once with an endpoint tagged outside the allow-list and show it refusing to start. Then run a **simulated DR drill**: stop the primary Postgres and Temporal containers, promote the replicas, and show in-flight runs resume.

### 16.4 Extremely tight security: threat model and controls
Organising principle: Simon Willison's **"lethal trifecta"**. An agent is exploitable when it has private data, reads untrusted content, *and* can send data out. Every agent here reads untrusted repo content and sees private code, so **we cut the third leg structurally**: sandboxes have no network at all, and the worker can reach only allow-listed LLM hosts through smokescreen.

| Threat | Control | 48 h? |
|---|---|---|
| Prompt injection exfiltrates code or secrets | No sandbox egress. Worker egress allow-listed. Prompt DLP. Untrusted content is passed as quoted data, never as instructions. Agents that read the most untrusted text (Analyst) have no write tools | ✅ |
| Sandbox escape | Production: **gVisor or Kata/Firecracker is mandatory**, plus a dedicated node pool. Demo: `--cap-drop=ALL`, `--security-opt=no-new-privileges`, default seccomp, user namespaces, read-only root, `--pids-limit`, CPU and memory caps, no host mounts, cloud metadata address blocked | ✅ hardening / roadmap for microVMs |
| Cross-tenant leakage | Sandboxes never reused across tenants (destroyed after each run). Row-level security. Per-tenant buckets. **Per-tenant encryption keys** (KMS envelope encryption) | ✅ RLS + no reuse / roadmap for KMS |
| Stolen credentials | No long-lived secrets anywhere. GitHub App tokens (1 h). **Per-run capability tokens**: short-lived signed JWTs bound to run, tools, paths and expiry (Grantex-style claims). Secrets from a vault at activity time, never in workflow history | ✅ |
| Forged or hijacked human approval | **Approval needs a passkey (WebAuthn) assertion signed over (head SHA, base SHA, evidence hash)**. A stolen browser session can't approve. Maker-checker. Approvals expire | ✅ should-have (go-webauthn) |
| Service impersonation inside the deployment | mTLS between services. Production: SPIFFE/SPIRE workload identity | Roadmap (demo: one compose network + mTLS to smokescreen) |
| Tampered sandbox or tool images | Images pinned by digest and signed with cosign. Production: an admission controller (Kyverno) verifies signatures. SBOM per image | ✅ digest pinning / roadmap for signing |
| Prompts or code leaking via telemetry | **Prompt and completion bodies never go into traces or logs by default** (OTel GenAI content capture stays off). Only token counts, model and hashes. Logs pass through the same DLP redaction | ✅ |
| Audit log tampering | Hash-chained ledger. Production: periodically anchor the chain head to an external transparency log | ✅ chain / roadmap for anchoring |
| Abuse or runaway cost (denial of wallet) | Per-tenant quotas, per-run budgets, size limits on input and repo, max concurrent runs, system-wide kill switch | ✅ |
| Policy drift ("who can approve what" buried in code) | **Policy as code**: approval and override rules in OPA/Rego, embedded in the Go API, versioned in git, every decision logged with the policy version | ✅ should-have |
| Insider or agent changing the guardrails | Policies, rule files, CI and gate config are protected tier, need two approvers, and are themselves gated by the system | ✅ |

### 16.5 Proving it in 48 hours (evidence, not claims)
1. **Load test:** use `k6` (Go-based, from Grafana) to submit `load.concurrent_runs` concurrent runs (we suggest a few hundred on the demo host) against a **recorded-response fake LLM provider**. It's clearly labelled, deterministic, and keeps the test cheap. Sandboxes and gates are real, up to the host's limits. Show in Grafana: run starts per second, queue depth, p50/p95 time per stage, sandbox pool use, admission control parking runs at the review-capacity cap, and **zero lost runs**.
2. **Chaos under load:** kill a worker and the NATS node during the load test. Every run resumes, with no duplicated side effects (idempotency keys).
3. **Conflict test:** two tickets touch the same file at once. The lease makes the second one stack. Rebase and re-test happen automatically, and the merge queue re-validates.
4. **Residency and DR test:** the stack refuses to start when any endpoint is tagged outside India. A simulated DR failover resumes in-flight runs (§16.3).
5. **Red-team suite:** injection, exfiltration attempts (sandbox `curl` → denied and counted), test tampering, suppression comments, protected-file edit, new-package attempt, forged approval without a passkey. Show them as a pass/fail table.

**SLOs to show:** targets live in config (`slo.*`), are measured on the demo host, and are shown against the target in Grafana:
- sandbox ready time from the warm pool (p95)
- time to first agent action (p95)
- zero lost or duplicated runs under chaos
- cost per merged PR, per tenant

Pick the target values after the first load test, not before.

### 16.6 What this costs the 48-hour plan, and what gets cut to pay for it
Adding all of this to the original scope is not realistic, so this section explicitly **replaces** lower-value work:
- **Cut:** mutation testing, rule_observer, Java Semgrep rules, freeze calendar, gVisor on the demo host, the code-graph visualisation.
- **Add, in hours 3–26:** `tenant_id` + RLS, NATS events, mirror cache + worktrees, per-base cache, scope leases, base-SHA evidence + rebase/re-run, GitHub App, capability JWTs, container hardening flags, prompt-body redaction, admission control. About 8 person-hours, spread across people A and B.
- **Add, in hours 26–40:** fake LLM provider + k6 + load panels, jurisdiction guard + config schema validation, simulated DR drill, red-team table. About 6 person-hours, person C.
- **Should-have, only if on schedule:** passkey approval, OPA policies, merge queue.
- **Never cut (updated):** hidden tests, SHA-bound human gate, kill switch, scope tiers + leases, no-egress sandbox, FIN-013/014, prompt DLP, maker-checker, tenant isolation, jurisdiction guard, no-hardcoding config contract (§16.7), the load-test evidence.

### 16.7 No hardcoding: the configuration contract
**Rule:** no region, endpoint, model, limit, threshold, path list or secret is written into code. Every one comes from versioned config, validated against a JSON Schema at startup. **Bad or missing config means the services don't start.** The repo ships `config.example.yaml` with placeholders only (e.g. `<primary-region-in-india>`), plus an `.env.example` for secret *references* (vault paths), never secret values.

| Config key (illustrative) | What it controls |
|---|---|
| `deployment.primary_region`, `deployment.dr_region` | Which India regions to run in, chosen by the operator |
| `data_residency.allowed_jurisdictions` | Jurisdiction allow-list (`[IN]` for now). Every endpoint must be tagged |
| `llm.providers[]` (`name`, `endpoint`, `jurisdiction`, `models{role→model_id}`, `price_table_ref`) | Providers, model per role, fallback order, prices for cost tracking. No model IDs in code |
| `egress.allowlist[]` | Hosts smokescreen lets the worker reach (generated from `llm.providers`) |
| `workflow.max_ci_rounds`, `workflow.max_schema_repairs`, `workflow.max_arbitration_rounds` | Retry and iteration caps (defaults follow Stripe Minions: 2 CI rounds) |
| `budget.per_run_tokens`, `budget.per_run_usd`, `budget.per_tenant_daily_usd` | Cost limits |
| `review.wip_per_reviewer`, `review.risk_tiers[]`, `review.required_approvers{tier→n}` | Admission control and risk-tiered approval |
| `scope.protected_paths[]`, `scope.diff_budget{files, lines}`, `scope.lease_ttl` | Write-scope tiers, diff budget, file leases |
| `sandbox.cpu`, `sandbox.memory`, `sandbox.pids`, `sandbox.timeout`, `sandbox.warm_pool_size`, `sandbox.image_digest` | Sandbox limits and pinned image |
| `tenants[]` (`id`, `concurrency`, `queue_priority`, `quota`) | Per-tenant fairness |
| `tokens.capability_ttl`, `approval.expiry` | Credential and approval lifetimes |
| `policy.bundle_ref` | OPA policy bundle version |
| `slo.*`, `load.concurrent_runs` | SLO targets and load-test size |
| `gates.enabled[]`, `gates.rules_ref` | Which gates run, and which Semgrep rule pack version |

Every run records the **config version hash** in the ledger, next to the model ID and prompt hash. Any result can be traced back to the exact settings that produced it. Changes to the config file itself are protected tier: two approvers, gated like code.

---

## Appendix A. Contracts carried over from PRD.txt (updated)

### A.1 Agent input/output (JSON Schemas in `/schemas`, validated by the Go router)
| Role | Input | Output (key fields) |
|---|---|---|
| Analyst | raw requirement, repo tree, README/docs | `RequirementSpec{clarified_summary, assumptions[], acceptance_criteria[{id, description, verification_type, must}], task_plan[], risk_flags[], split_into[]?}` |
| Architect | RequirementSpec, code graph, OpenAPI | `ArchitecturePlan{impacted_modules[], design_summary, interface_changes[], scope{planned[], adjacent[], protected_touched[]}, planned_tests[], diff_budget{files, lines}, decision_log[]}` |
| Developer | plan, scope, rule files, last gate diagnostics | `DeveloperResult{status: IMPLEMENTED or DESIGN_CONFLICT, modified_files[], commit_sha, decision_log[], conflict{reason, needed_paths[]}?}` |
| QA gate (deterministic) | commit SHA | `QAReport{tier, passed, tests{selected, ran, failed[]}, hidden_tests, lint, openapi_diff, mutation_score?, replay{invariants[]}, ac_traceability[]}` |
| Security gate (deterministic) | diff, commit SHA | `SecurityReport{verdict: PASS, REMEDIABLE_FAIL or HARD_BLOCK, findings[{id, rule_id, severity, file, line, commit_sha, status: OPEN, RESOLVED or STALE, remediation}]}` |
| Release | all reports, ledger | `ReleasePacket{pr_title, summary, risks[], rollout{flag, canary, rollback}, must_should_scorecard, cost_usd, latency_ms, approval: PENDING_HUMAN}` |

### A.2 REST API
| Method | Path | Purpose |
|---|---|---|
| POST | `/api/runs` | Submit requirement, repo, mode (`SHADOW` = no PR, `ACTIVE` = draft PR) |
| GET | `/api/runs`, `/api/runs/{id}` | List runs; full state, artifacts, diff, trace ID |
| GET | `/api/runs/{id}/events` | SSE stream with `Last-Event-ID` replay |
| POST | `/api/runs/{id}/decision` | `APPROVE`, `REJECT` or `REQUEST_CHANGES` (maker-checker enforced; signs SHA + evidence hash) |
| POST | `/api/runs/{id}/resume` | Resume after a provider pause or a human arbitration |
| POST | `/api/runs/{id}/override` | Security officer override (signed, logged) |
| POST | `/api/kill` | System-wide kill switch: terminate workflows, revoke scope tokens |
| GET | `/api/audit/verify` | Verify the hash chain |
| POST | `/api/evals` | Run the eval suite (baseline vs. governed, N runs) |

### A.3 Original PRD demos → where they live now
Demo 1 (webhook + fees) → split into the primary demo plus eval ticket #2 (§3.6). Demo 2 (scope / protected file) → adversarial beat in §11. Demo 3 (security hard block) → seeded eval ticket plus a live beat if time allows. Demo 4 (provider outage) → real chaos: worker kill or egress ACL flip (§3.5).

---

## Sources
- Pine Labs 89%: [Analytics India Magazine](https://analyticsindiamag.com/ai-news/ai-powers-89-of-code-changes-at-pine-labs-ceo-reveals), [Inc42](https://inc42.com/buzz/pine-labs-invests-%E2%82%B924-cr-in-ai-rd-in-fy26-cuts-testing-time-by-over-95/)
- Pine Labs FY26: [The Fintech Times](https://thefintechtimes.com/pine-labs-turns-profitable-in-fy26-with-%e2%82%b9113-cr-pat-and-50-gtv-growth/). Q2 FY27: [Inc42](https://inc42.com/buzz/pine-labs-q2-profit-jumps-over-6x-yoy-to-%E2%82%B938-6-cr-revenue-rises-24/)
- AgenticOrg / Grantex: [grantex.dev](https://grantex.dev), [agenticorg.ai](https://agenticorg.ai), [github.com/mishrasanjeev/agentic-org](https://github.com/mishrasanjeev/agentic-org), [YourStory CTO interview](https://yourstory.com/2026/11/how-pine-labs-is-building-for-the-agent-economy-cto-sanjeev-kumar-on-trust-control-and-execution)
- Pine Labs stack (Java/Spring, K8s): [job posting](https://nodeflair.com/jobs/pine-labs-lead-engineer-java-backend-552583), [Plural blog](https://www.pinelabs.com/blog/scaling-from-the-ground-up)
- Veracode 2026: [press release](https://www.veracode.com/news/llms-are-getting-smarter-but-not-safer-veracode-2026-genai-code-security-report-finds-ai-generated-code-security-has-stalled-at-56%25-pass-rate/)
- Harness: [State of Agent DLC 2026](https://www.harness.io/press-and-news/new-report-reveals-ai-agent-confidence-gap)
- Amazon: [The Register (Kiro)](https://www.theregister.com/2026/02/20/amazon_denies_kiro_agentic_ai_behind_outage/), [Fortune (Mar 2026)](https://fortune.com/2026/03/12/amazon-retail-site-outages-ai-agent-inaccurate-advice/), [TechRadar (sign-off)](https://www.techradar.com/pro/amazon-is-making-even-senior-engineers-get-code-signed-off-following-multiple-recent-outages)
- Faros 2026: [Acceleration Whiplash](https://www.faros.ai/research/ai-acceleration-whiplash). DX: [Q3 report](https://getdx.com/uploads/state-of-ai-impact-in-engineering-q3-report.pdf)
- Apiiro: [4× velocity, 10× vulnerabilities](https://apiiro.com/blog/4x-velocity-10x-vulnerabilities)
- Cursor: [TechCrunch](https://techcrunch.com/2026/06/16/spacex-to-acquire-cursor-for-60b-in-stock-days-after-blockbuster-ipo/)
- Ramp Inspect: [Pragmatic Engineer](https://newsletter.pragmaticengineer.com/p/why-ramp-built-inspect), [Modal](https://modal.com/blog/how-ramp-built-a-full-context-background-coding-agent-on-modal)
- Stripe blog: [Minions](https://stripe.dev/blog/minions-stripes-one-shot-end-to-end-coding-agents), [Minions Part 2](https://stripe.dev/blog/minions-stripes-one-shot-end-to-end-coding-agents-part-2), [Payment Method Factory](https://stripe.dev/blog/stripes-payment-method-factory-orchestrating-agents-for-repeated-custom-integrations), [Checkout for AI agents](https://stripe.dev/blog/how-stripe-is-designing-checkout-for-ai-agents), [STE](https://stripe.dev/blog/selective-test-execution-at-stripe-fast-ci-for-a-50m-line-ruby-monorepo), [API changes](https://stripe.dev/blog/how-api-changes-flow-into-stripes-developer-products), [Harbor](https://stripe.dev/blog/harbor-stripes-ai-assisted-prototyping-tool), [Stripe evals](https://stripe.dev/blog/stripe-integration-evals-methodology), [Claimable sandboxes](https://stripe.dev/blog/claimable-sandboxes-frictionless-stripe-accounts-for-agents), [Spark testing](https://stripe.dev/blog/microservice-testing-with-apache-spark), [Steering experiments](https://stripe.dev/blog/ai-steering-experiments), [DB auto-remediation](https://stripe.dev/blog/how-stripe-uses-graph-search-and-state-machines-to-auto-remediate-a-global-database-fleet), [Idempotency](https://stripe.dev/blog/because-nobody-likes-being-charged-twice)
- Stripe GitHub: [smokescreen](https://github.com/stripe/smokescreen), [stripe-mock](https://github.com/stripe/stripe-mock), [pg-schema-diff](https://github.com/stripe/pg-schema-diff), [forge](https://github.com/stripe/forge), [ai](https://github.com/stripe/ai), [veneur (archived)](https://github.com/stripe/veneur)
- Indian regulation: [RBI IT Governance Directions 2023](https://www.rbi.org.in/Scripts/BS_ViewMasDirections.aspx?id=12562), [RBI Payment Aggregator Directions 2025](https://rbi.org.in/Scripts/BS_ViewMasDirections.aspx?id=12896), [CERT-In 6-hour rule (The Register)](https://www.theregister.com/2022/04/29/cert_in_directive/), [CERT-In vs DPDP duties (KS&K)](https://ksandk.com/data-protection-and-data-privacy/cert-in-vs-dpdp-dual-breach-notification-duties-explained/)
- Scale and security: [Modal on Ramp Inspect (30-minute snapshots)](https://modal.com/blog/how-ramp-built-a-full-context-background-coding-agent-on-modal), [Temporal multi-region namespaces](https://docs.temporal.io/cloud/multi-region), [GitHub App installation tokens](https://docs.github.com/en/enterprise-cloud@latest/apps/creating-github-apps/authenticating-with-a-github-app/authenticating-as-a-github-app-installation), [Simon Willison: the lethal trifecta](https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/)
- Slopsquatting: [Spracklen et al., USENIX Security 2025](https://arxiv.org/html/2406.10279v3), [The Register](https://www.theregister.com/2025/04/12/ai_code_suggestions_sabotage_supply_chain/)
- Orchestration and sandboxing: [Temporal LangGraph plugin](https://temporal.io/blog/temporal-langgraph-plugin-durable-execution), [Fly.io: Firecracker vs gVisor](https://fly.io/learn/firecracker-vs-gvisor/)
