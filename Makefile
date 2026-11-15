.PHONY: help all build build-ui dev-ui dev run test test-web test-coverage test-features vet docker-build up down clean preflight host-prepare setup-host build-images validate-compose benchmark install systemd

VERSION ?= 1.0.0
RELEASE_NAME ?= Compute
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "dev")
BUILD_DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
PKG_VERSION = github.com/Rashid2G/on-demand-vms/pkg/version
LDFLAGS = -ldflags "-X '$(PKG_VERSION).Version=$(VERSION)' -X '$(PKG_VERSION).ReleaseName=$(RELEASE_NAME)' -X '$(PKG_VERSION).GitCommit=$(GIT_COMMIT)' -X '$(PKG_VERSION).BuildDate=$(BUILD_DATE)'"
IMAGES_DIR ?= ./data/images

.DEFAULT_GOAL := help

help: ## Show available Make targets
	@echo "Compute v$(VERSION) — Build & Operations Targets"
	@echo ""
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z0-9_-]+:.*?## / {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

all: build ## Build Go binaries

build: ## Build compute, computed, compute-worker, and compute-bench binaries
	mkdir -p bin
	go build $(LDFLAGS) -o bin/compute ./cmd/compute
	go build $(LDFLAGS) -o bin/computed ./cmd/computed
	go build $(LDFLAGS) -o bin/compute-worker ./cmd/compute-worker
	go build $(LDFLAGS) -o bin/compute-bench ./cmd/compute-bench

build-ui: ## Build the React/Vite Web UI into internal/gui/assets
	cd web && (bun run build || npm run build)

dev-ui: ## Run the Vite Web UI development server
	cd web && (bun run dev || npm run dev)

dev: build setup-host ## Build binaries, configure host networking, and start computed locally
	./bin/computed -listen 127.0.0.1:18080

run: dev ## Alias for dev

test: ## Run Go unit and integration tests
	go test -count=1 ./...

test-web: ## Typecheck and run Web UI test suite
	cd web && npm run typecheck && npm test

test-coverage: ## Run Go tests with coverage report
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

test-features: build ## Run end-to-end Firecracker feature test suite (requires KVM + running daemon)
	./scripts/test-all-features.sh ./bin/compute 127.0.0.1:18080

vet: ## Run go vet across all packages
	go vet ./...

docker-build: ## Build Docker Compose images for daemon and worker
	docker compose build

up: ## Start Compute stack via Docker Compose
	docker compose up -d

down: ## Stop Compute Docker Compose stack
	docker compose --profile gui --profile worker down

clean: ## Remove build artifacts and temporary coverage/benchmark outputs
	rm -rf bin/ coverage.out benchmark-results.json

preflight: ## Run read-only host readiness checks for KVM, Docker, and cgroups
	./scripts/preflight.sh

host-prepare: ## Apply idempotent Linux host kernel modules and sysctl settings (requires root)
	sudo ./scripts/host-prepare.sh

setup-host: ## Configure host bridge and NAT rules for Firecracker sandboxes
	./scripts/setup-host.sh

build-images: ## Build Firecracker rootfs and kernel artifacts into IMAGES_DIR
	sudo ./scripts/build-rootfs.sh $(IMAGES_DIR) 1024

validate-compose: ## Validate docker-compose.yml and .env.example contract
	./scripts/validate-compose.sh

benchmark: build ## Run automated control-plane and microVM lifecycle benchmarks
	./scripts/benchmark.sh 127.0.0.1:18080 benchmark-results.json

install: build ## Install compiled binaries to /usr/local/bin
	sudo install -d /usr/local/bin
	sudo install -m 755 bin/compute /usr/local/bin/compute
	sudo install -m 755 bin/computed /usr/local/bin/computed
	@if [ -f bin/compute-worker ]; then sudo install -m 755 bin/compute-worker /usr/local/bin/compute-worker; fi

systemd: ## Install systemd unit files to /etc/systemd/system
	sudo install -d /etc/systemd/system
	sudo install -m 644 config/systemd/computed.service /etc/systemd/system/computed.service
	sudo install -m 644 config/systemd/user/compute-worker.service /etc/systemd/system/compute-worker.service
	-sudo systemctl daemon-reload
