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
	    $(SANDBOX_IMAGE) pytest -p tests.conftest /hidden --rootdir=/work -q; then \
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
