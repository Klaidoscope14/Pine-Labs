# Aegis (AegisSDLC)

**AI agents write the code. Automated checks prove it's safe. A human signs off.**

Aegis lets a payments company hand routine engineering tickets to AI agents without
giving up control. Every change goes through the same steps a careful senior team would
follow: understand the ticket, plan it, build it, run the checks, then wait for people to
approve it. Nothing reaches the main branch without that.

> **Live demo:** [LIVE LINK — coming soon](#)
> **Access code:** shared on request

Built for the Pine Labs Challenge Board, Case Study 03 ("Autonomous Enterprise
Engineering Organisation"). The full design and build history is in [PLAN.md](PLAN.md).

---

## What it does, in one minute

1. **You write a ticket** in plain English, e.g. *"The bank retries settlement webhooks
   and merchants get credited twice. Fix it."*
2. **Four AI agents take it in turn:** one clarifies it, one plans the change, one writes
   the code and tests, one writes the summary for reviewers.
3. **Eleven automated checks** run on every change: tests (including hidden ones the agent
   never sees), lint, security scans, fintech rules (no floats for money, no logged card
   numbers, signatures compared safely), secret scanning, API compatibility and more.
4. **Humans approve.** Risky changes (payments, ledger, webhooks) need two reviewers, and
   the person who asked for the change can never approve it. Approvals can be signed with
   a passkey, so a stolen login can't approve anything.
5. **It lands safely.** Approved changes join a merge queue, get re-tested against the
   latest main, and only then go in.

If anything goes wrong — checks keep failing, the AI provider goes down, someone hits the
kill switch — the run stops cleanly and the reason is recorded.

## Why it's safe to trust

| Worry | What Aegis does |
|---|---|
| The AI deletes or weakens a failing test | Blocked. Existing tests can only be added to, never removed, skipped or loosened |
| The AI silences a security scanner | Blocked. New `noqa`/`nosec` comments and edits to checker config are refused |
| The AI touches money-critical files it shouldn't | Protected files can't be edited at all; everything else must be in the plan |
| Card numbers or Aadhaar leak to the AI provider | Every outgoing prompt is scanned and redacted first; if anything slips through, the call is refused |
| Two AI changes clash | Changes to the same files wait their turn; the merge queue re-tests against the latest main |
| Someone approves their own change | Impossible. Approval rules are written as code (OPA/Rego) and enforced by the system |
| Runaway cost | Per-run and per-day budgets; the kill switch stops everything instantly |
| "What actually happened?" | Every step is written to a tamper-evident, hash-chained audit log |

## Results so far

Both models pass every live check on the same pipeline:

| | Claude Sonnet 5.5 | DeepSeek V4.1 Flash |
|---|---|---|
| Approved on first try | 5 of 5 | 5 of 5 |
| Model calls per change | ~9 | 17–33 |
| Cost per successful change | ~$0.64 (list price) | ~$0.03 |

DeepSeek went from **0 of 5** to **5 of 5** after we added a self-check before the agent can
finish, a live diff-size counter and parallel checks — the harness made the cheap model
reliable, without loosening a single check.

---

## Run it locally

You need Go 1.26+, Docker, Make and Node 20+.

```bash
make up             # start Postgres, Temporal, monitoring (once)
make migrate seed-repo
make worker CONFIG=config.replay.yaml   # in one terminal: recorded AI responses, no cost
make web CONFIG=config.replay.yaml      # in another: Go console at http://localhost:8480
```

For the Next.js frontend, copy `web/.env.example` to `web/.env.local`, fill it in, then:

```bash
cd web && npm install && npm run dev    # http://localhost:3000
```

Useful checks: `make test` (all unit tests), `make phase1` (full deterministic pipeline),
`make phase2-offline` (real agents on recorded responses).

## How it's built

```text
┌────────────────────────────────── Reviewer experience ──────────────────────────────────┐
│  Browser  ── HTTPS ──▶  Next.js reviewer app (Vercel)                                   │
│                                  │ server-to-server only                                │
└──────────────────────────────────┼──────────────────────────────────────────────────────┘
                                   HTTPS + service token
                                           │
                                  CloudFront (origin guard)
                                           │
┌────────────────────────── Aegis control plane · AWS Mumbai ─────────────────────────────┐
│                              ▼                                                           │
│  Go API ──▶ Temporal workflow ──▶ Go worker                                              │
│    │                 │                  │                                                │
│    │                 │                  ├──▶ Policy: OPA/Rego + WebAuthn approvals       │
│    │                 │                  ├──▶ Restricted agent gateway ──▶ LLM providers  │
│    │                 │                  └──▶ Merge queue ──▶ protected main branch       │
│    │                 │                                                                   │
│    └─────────────────┴──▶ Postgres ledger: tenant isolation + hash-chained audit log     │
└──────────────────────────────────────────────────────────────────────────────────────────┘
                                           │
┌────────────────────────────── Execution trust boundary ──────────────────────────────────┐
│  Fresh gVisor sandbox · no network · non-root · CPU/memory/output limits                 │
│       └──▶ tests · hidden oracle tests · Semgrep · secrets · OpenAPI · replay checks     │
└──────────────────────────────────────────────────────────────────────────────────────────┘
                                           │
              OpenTelemetry ──▶ Jaeger + Prometheus + Grafana
```

The browser never talks directly to the Go API. The Next.js app holds the service token,
CloudFront authenticates the origin request, and the API accepts only that protected path.
For local development, the same control-plane services run with Docker Compose and the
reviewer app runs on localhost.

- **Go + Temporal**: durable workflow execution, retries, and human-approval waits.
- **Postgres**: row-level tenant isolation and a tamper-evident, hash-chained audit record.
- **Gateway + gVisor**: scoped agent tools and a no-network sandbox for every check.
- **OPA/Rego + WebAuthn**: review policy and optionally passkey-bound approvals.
- **Next.js + TypeScript**: reviewer interface; **OpenTelemetry, Prometheus, Grafana, Jaeger**: observability.

| Folder | What's inside |
|---|---|
| `cmd/` | The programs: API, worker, CLI tools, benchmark |
| `internal/` | Workflow, agents, gates, ledger, policy, web console |
| `web/` | Next.js frontend |
| `policies/` | Approval policy (Rego) and its tests |
| `rules/semgrep/` | Fintech security rules |
| `deploy/` | Local stack and AWS template |
| `samples/` | The demo payment service with a planted bug |
| `evals/` | Test scenarios and hidden tests |

## Deploying

- **AWS:** `scripts/aws.sh help` lists every command (`check`, `deploy`, `public`, `web`, `stop`…).
- **Vercel:** import the repo with root directory `web`; environment variables are listed in
  [web/README.md](web/README.md).
