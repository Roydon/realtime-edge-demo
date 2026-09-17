# Entry point for the demo stack. `make demo` is the 60-second path.

# Read .env so the banner and the helper scripts report the same ports Compose
# is actually using. `-include` rather than `include`: a missing .env is the
# normal case, not an error. Assignments below use ?= so anything set here
# wins, and an explicit `make HTTPS_PORT=...` still overrides both.
-include .env

COMPOSE ?= docker compose
TF      ?= terraform
GO      ?= go

# Image identity, overridable so CI can stamp a real registry and tag.
IMAGE_REPO ?= realtime-edge-demo/echo-service
IMAGE_TAG  ?= local
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

HTTPS_PORT ?= 8443
PROMETHEUS_PORT ?= 9090

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

# --- demo ---

.PHONY: demo
demo: certs up wait load-bg ## Bring up the whole stack, generate load, print URLs
	@echo ""
	@echo "  Stack is up and under load."
	@echo ""
	@echo "  Grafana     https://localhost:$(HTTPS_PORT)/grafana/  (admin / admin)"
	@echo "              -> Dashboards -> Realtime Edge"
	@echo "  Prometheus  http://localhost:$(PROMETHEUS_PORT)  (loopback only)"
	@echo "  Echo API    curl -sk -X POST https://localhost:$(HTTPS_PORT)/api/echo \\"
	@echo "                   -H 'content-type: application/json' -d '{\"message\":\"hi\"}'"
	@echo ""
	@echo "  The certificate is self-signed, so a browser will warn once."
	@echo "  Give the dashboards ~30s of traffic before reading percentiles."
	@echo ""
	@echo "  make logs    follow all logs"
	@echo "  make down    stop everything"
	@echo ""

.PHONY: certs
certs: ## Generate the self-signed TLS certificate for the edge
	@./scripts/gen-certs.sh

.PHONY: up
up: certs ## Build images and start the stack
	$(COMPOSE) up -d --build

.PHONY: wait
wait: ## Block until the edge is serving
	@./scripts/wait-ready.sh

.PHONY: load
load: ## Run the load generator in the foreground (ctrl-c to stop)
	$(COMPOSE) --profile load run --rm loadgen

.PHONY: load-bg
load-bg: ## Start the load generator in the background
	$(COMPOSE) --profile load up -d loadgen

.PHONY: down
down: ## Stop the stack, keeping metric history
	$(COMPOSE) --profile load down

.PHONY: clean
clean: ## Stop the stack and delete volumes and generated certificates
	$(COMPOSE) --profile load down -v
	rm -rf nginx/certs

.PHONY: logs
logs: ## Follow logs from all services
	$(COMPOSE) logs -f --tail=50

.PHONY: ps
ps: ## Show container status
	$(COMPOSE) ps

# --- verification ---

.PHONY: verify
verify: ## Prove the running stack works end to end
	@./scripts/verify.sh

.PHONY: test
test: ## Run the Go test suite with the race detector
	$(GO) test -race -count=1 ./...

.PHONY: lint
lint: fmt-check vet ## Run all static checks

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: fmt-check
fmt-check: ## Fail if any Go file is not gofmt-clean
	@unformatted=$$(gofmt -l . ); \
	if [ -n "$$unformatted" ]; then \
		echo "not gofmt-clean:"; echo "$$unformatted"; exit 1; \
	fi
	@echo "gofmt: clean"

.PHONY: build
build: ## Build the container images
	$(COMPOSE) build --build-arg VERSION=$(VERSION)

# --- terraform (plan-only: this repository never applies) ---

.PHONY: tf-init
tf-init: ## Initialise the Terraform working directory
	$(TF) -chdir=terraform init -backend=false

.PHONY: tf-fmt
tf-fmt: ## Check Terraform formatting
	$(TF) -chdir=terraform fmt -check -recursive

.PHONY: tf-validate
tf-validate: tf-init ## Validate the Terraform configuration
	$(TF) -chdir=terraform validate

.PHONY: tf-plan
tf-plan: tf-init ## Show the plan (needs DIGITALOCEAN_TOKEN; never applied)
	$(TF) -chdir=terraform plan -var-file=demo.tfvars

# --- operations ---

.PHONY: backup
backup: ## Snapshot Grafana and Prometheus state to ./backups
	@./scripts/backup.sh

.PHONY: restore
restore: ## Restore from a snapshot: make restore SNAPSHOT=backups/<file>.tar.gz
	@./scripts/restore.sh "$(SNAPSHOT)"

.PHONY: alerts
alerts: ## Show currently firing Prometheus alerts
	@curl -s localhost:$(PROMETHEUS_PORT)/api/v1/alerts \
		| python3 -c 'import json,sys; a=json.load(sys.stdin)["data"]["alerts"]; \
print("no alerts firing") if not a else [print(f"{x[\"labels\"][\"severity\"]:>8}  {x[\"labels\"][\"alertname\"]}  ({x[\"state\"]})") for x in a]'
