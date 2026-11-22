# AegisSDLC developer commands. Every version comes from versions.env and every
# limit from the config file. Nothing is hardcoded here.
SHELL   := /bin/bash
CONFIG  ?= config.local.yaml
SAMPLE  := $(CURDIR)/samples/pine-settlements-svc
HIDDEN  := $(CURDIR)/evals/hidden/pine-settlements-svc
include deploy/compose/versions.env

COMPOSE = docker compose --env-file deploy/compose/versions.env --env-file deploy/compose/.env.secrets -f deploy/compose/docker-compose.yml
AEGIS_CONFIG_BIN = bin/aegis-config
SANDBOX_ARGS  = $(shell $(AEGIS_CONFIG_BIN) sandbox-args $(CONFIG))
SANDBOX_IMAGE = $(shell $(AEGIS_CONFIG_BIN) sandbox-image $(CONFIG))
SANDBOX_RUN   = docker run --rm $(SANDBOX_ARGS) -v $(SAMPLE):/work:ro $(SANDBOX_IMAGE)

.PHONY: phase0 test build validate-config secrets sandbox-image sample-test hidden-test sandbox-egress-check up down health

phase0: test validate-config sandbox-image sample-test hidden-test sandbox-egress-check up ## Run every Phase 0 exit check

build:
	@mkdir -p bin && go build -o bin/ ./cmd/...

test: ## Go unit tests
	go vet ./... && go test ./...

validate-config: build ## Good config passes, every bad fixture is rejected
	@$(AEGIS_CONFIG_BIN) validate $(CONFIG)
	@for f in internal/config/testdata/bad-*.yaml; do \
	  if $(AEGIS_CONFIG_BIN) validate $$f >/dev/null 2>&1; then echo "ACCEPTED (should be rejected): $$f"; exit 1; \
	  else echo "rejected as expected: $$f"; fi; done

secrets:
	@scripts/secrets.sh

sandbox-image: build ## Build the hardened gate-runner image
	DOCKER_BUILDKIT=1 docker build -f sandbox/Dockerfile -t $(SANDBOX_IMAGE) \
	  --build-arg GO_IMAGE=$(GO_IMAGE) --build-arg PYTHON_IMAGE=$(PYTHON_IMAGE) \
	  --build-arg GITLEAKS_VERSION=$(GITLEAKS_VERSION) --build-arg OSV_SCANNER_VERSION=$(OSV_SCANNER_VERSION) \
	  --build-arg OASDIFF_VERSION=$(OASDIFF_VERSION) .

sample-test: build ## Lint, unit tests and live smoke test inside the locked-down sandbox
	$(SANDBOX_RUN) ruff check --no-cache .
	$(SANDBOX_RUN) pytest
	$(SANDBOX_RUN) python smoke_test.py

hidden-test: build ## Hidden oracle tests must FAIL on the base code (proves the planted bug)
	@if docker run --rm $(SANDBOX_ARGS) -v $(SAMPLE):/work:ro -v $(HIDDEN):/hidden:ro -e PYTHONPATH=/work \
	    $(SANDBOX_IMAGE) pytest /hidden -c /hidden/pytest.ini --rootdir=/hidden; then \
	  echo "UNEXPECTED: hidden tests passed on base code"; exit 1; \
	else echo "hidden tests fail on base code, as expected"; fi

sandbox-egress-check: build ## An outbound request from the sandbox must fail
	@if $(SANDBOX_RUN) python -c "import urllib.request; urllib.request.urlopen('https://pypi.org', timeout=5)" 2>/dev/null; then \
	  echo "UNEXPECTED: sandbox reached the internet"; exit 1; \
	else echo "sandbox has no network, as expected"; fi
	@for tool in ruff bandit semgrep gitleaks osv-scanner oasdiff pytest; do \
	  docker run --rm $(SANDBOX_ARGS) $(SANDBOX_IMAGE) sh -c "command -v $$tool >/dev/null" && echo "tool present: $$tool" || { echo "MISSING tool: $$tool"; exit 1; }; done

up: secrets ## Start the local stack and verify it
	$(COMPOSE) up -d --build --wait --wait-timeout 300
	@scripts/stack-health.sh

health:
	@scripts/stack-health.sh

down: ## Stop the stack (keeps volumes)
	$(COMPOSE) down

# ---------------- Phase 1 ----------------
SECRETS_ENV = set -a; source deploy/compose/.env.secrets; set +a;
LOGS = .data/logs

.PHONY: phase1 semgrep-test gate-test ledger-test migrate seed-repo worker run run-all crash-test ledger-verify

phase1: test semgrep-test gate-test ledger-test migrate seed-repo run-all crash-test ledger-verify ## Every Phase 1 exit check

semgrep-test: build ## FIN rule pack: positive and negative cases
	docker run --rm $(SANDBOX_ARGS) --mount type=bind,source=$(CURDIR)/rules/semgrep,target=/rules,readonly \
	  $(SANDBOX_IMAGE) semgrep scan --test --metrics off --disable-version-check /rules

gate-test: build ## Every gate, for real in the sandbox, on base code and each scenario patch
	AEGIS_SANDBOX_IMAGE=$(SANDBOX_IMAGE) go test ./internal/gaterun ./internal/sandbox -count=1 -timeout 20m

ledger-test: ## Ledger against Postgres: RLS, append-only audit, chain tamper detection, Once()
	$(SECRETS_ENV) go test ./internal/ledger -count=1

migrate: build ## Create/update the app role and ledger schema
	$(SECRETS_ENV) bin/aegis-ledger -config $(CONFIG) migrate

seed-repo: build ## Create the local code-host repo from the sample
	scripts/seed-repo.sh $(CONFIG) $(SAMPLE)

worker: build ## Run a worker in the foreground
	$(SECRETS_ENV) bin/aegis-worker -config $(CONFIG)

run: build ## Run one scenario against a running worker: make run S=happy
	$(SECRETS_ENV) bin/aegis-run -config $(CONFIG) -scenario $(S)

run-all: build ## Start a worker, run all six scenarios concurrently, verify outcomes
	@$(SECRETS_ENV) scripts/run-all.sh $(CONFIG) $(LOGS)

crash-test: build ## Kill the worker mid-run; the run must finish with no duplicates
	@$(SECRETS_ENV) scripts/crash-test.sh $(CONFIG) $(LOGS)

ledger-verify: build ## Verify every tenant's audit chain
	$(SECRETS_ENV) bin/aegis-ledger -config $(CONFIG) verify

# ---------------- Phase 2 ----------------
.PHONY: phase2 phase2-offline phase2-live cassettes

phase2: test ledger-test migrate seed-repo phase2-offline run-all ledger-verify ## Phase 2 checks that need no model access

cassettes: ## Regenerate the scripted offline cassettes for live-happy
	python3 scripts/make-scripted-cassettes.py .

phase2-offline: build ## Real agents on replayed responses; card number must never leave the machine
	@$(SECRETS_ENV) scripts/phase2-offline.sh $(LOGS)

phase2-live: build ## SPENDS MONEY: live runs (5x Demo 1, protected file, kill switch). LIVE_CONFIG=config.deepseek.yaml for DeepSeek, PARALLEL=1 to run them at once
	@$(SECRETS_ENV) PARALLEL=$(PARALLEL) scripts/phase2-live.sh $(LOGS) $(or $(RUNS),5) $(or $(LIVE_CONFIG),config.bedrock.yaml)

# ---------------- Phase 4: web app (PLAN.md §10.7) ----------------
.PHONY: web benchmark

benchmark: build ## SPENDS MONEY/QUOTA: same tickets on each model, report in evals/benchmark.md. RUNS=5
	@$(SECRETS_ENV) scripts/benchmark.sh $(or $(RUNS),5) "Claude Sonnet 5.5=config.claude-code.yaml" "DeepSeek V4.1 Flash=config.deepseek.yaml"

web: build ## Run the web app on api.listen (needs a worker for the same config): make web CONFIG=config.replay.yaml
	$(SECRETS_ENV) bin/aegis-api -config $(CONFIG)

# ---------------- AWS (PLAN.md §10.4) ----------------
.PHONY: aws deploy deploy-tunnel

aws: ## All AWS operations in one script: make aws CMD="check|key|deploy|stack|verify|result|logs|shell|tunnel 3000|start|stop|drift"
	@scripts/aws.sh $(CMD)

deploy: ## Create/update the EC2 stack and (re)deploy this working tree: AWS_REGION=<india-region> make deploy
	@scripts/aws-deploy.sh

deploy-tunnel: ## Forward a UI from the instance: AWS_REGION=... make deploy-tunnel INSTANCE=i-... PORT=3000
	@scripts/aws-tunnel.sh $(INSTANCE) $(PORT)
