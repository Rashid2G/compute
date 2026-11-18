.PHONY: help all build build-ui dev-ui dev run test test-web test-coverage test-features vet docker-build setup up down clean preflight host-prepare setup-host build-images validate-compose benchmark bench install systemd release-tarballs

VERSION ?= 1.0.0
RELEASE_NAME ?= Compute
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "dev")
BUILD_DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
PKG_VERSION = github.com/Rashid2G/compute/pkg/version
LDFLAGS = -ldflags "-X '$(PKG_VERSION).Version=$(VERSION)' -X '$(PKG_VERSION).ReleaseName=$(RELEASE_NAME)' -X '$(PKG_VERSION).GitCommit=$(GIT_COMMIT)' -X '$(PKG_VERSION).BuildDate=$(BUILD_DATE)'"
IMAGES_DIR ?= ./data/images

.DEFAULT_GOAL := help

help: ## Show available Make targets
	@echo "Compute v$(VERSION) — Build & Operations Targets"
	@echo ""
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z0-9_-]+:.*?## / {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

all: build ## Build Go binaries

build: ## Build compute, computed, compute-worker, and compute-bench binaries
	@command -v go >/dev/null 2>&1 || { echo "Error: 'go' is not installed or not in PATH (Go 1.26+ required)" >&2; exit 127; }
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
	COMPUTE_ADMIN_PASSWORD=$${COMPUTE_ADMIN_PASSWORD:-admin} ./bin/computed -listen 127.0.0.1:18080

run: dev ## Alias for dev

test: ## Run Go unit and integration tests
	go test -count=1 ./cmd/... ./internal/... ./pkg/...

test-web: ## Typecheck and run Web UI test suite
	cd web && npm run typecheck && npm test

test-coverage: ## Run Go tests with coverage report
	go test -coverprofile=coverage.out ./cmd/... ./internal/... ./pkg/...
	go tool cover -func=coverage.out

test-features: build ## Run end-to-end Firecracker feature test suite (requires KVM + running daemon)
	./scripts/test-all-features.sh ./bin/compute 127.0.0.1:18080

vet: ## Run go vet across all packages
	go vet ./cmd/... ./internal/... ./pkg/...

docker-build: ## Build Docker Compose images for daemon and worker
	docker compose build

setup: ## Prepare host and build Firecracker kernel, Ubuntu 24.04 rootfs, and SSH key
	./scripts/install.sh

up: ## Start Compute stack via Docker Compose (bootstraps host/images if missing)
	@set -e; \
	if [ -f .env ]; then set -a; . ./.env; set +a; fi; \
	img_dir="$${COMPUTE_IMAGES_DIR:-$${COMPUTE_HOST_DATA_DIR:+$$COMPUTE_HOST_DATA_DIR/images}}"; \
	img_dir="$${img_dir:-$(IMAGES_DIR)}"; \
	if [ ! -f "$$img_dir/vmlinux" ] || [ ! -f "$$img_dir/ubuntu-24.04.ext4" ] || [ ! -f "$$img_dir/ssh/id_ed25519" ]; then \
		echo "==> Base images or internal SSH key missing in $$img_dir; running ./scripts/install.sh..."; \
		./scripts/install.sh; \
	fi
	docker compose up -d

down: ## Stop Compute Docker Compose stack
	docker compose --profile gui --profile worker down

clean: ## Remove build artifacts and temporary coverage/benchmark outputs
	rm -rf bin/ dist/ coverage.out benchmark-results.json

preflight: ## Run read-only host readiness checks for KVM, Docker, and cgroups
	./scripts/preflight.sh

host-prepare: ## Apply idempotent Linux host kernel modules and sysctl settings (requires root)
	sudo ./scripts/host-prepare.sh

setup-host: ## Configure host bridge and NAT rules for Firecracker sandboxes
	./scripts/setup-host.sh

build-images: ## Install the Firecracker kernel and build the rootfs into IMAGES_DIR
	COMPUTE_KERNEL_SRC="$(COMPUTE_KERNEL_SRC)" \
	COMPUTE_KERNEL_URL="$(COMPUTE_KERNEL_URL)" \
	COMPUTE_KERNEL_SHA256="$(COMPUTE_KERNEL_SHA256)" \
	./scripts/build-kernel.sh $(IMAGES_DIR)
	./scripts/build-rootfs.sh $(IMAGES_DIR) 2048

validate-compose: ## Validate docker-compose.yml and .env.example contract
	./scripts/validate-compose.sh

benchmark: build ## Run automated control-plane and microVM lifecycle benchmarks
	./scripts/benchmark.sh 127.0.0.1:18080 benchmark-results.json

bench: ## Run Go microbenchmarks for control-plane hot paths
	go test ./cmd/computed ./internal/scheduler ./internal/db -run '^$$' -bench . -benchmem

install: build ## Install compiled binaries, helper scripts, and default config
	sudo install -d /usr/local/bin /usr/local/share/compute /etc/compute
	sudo install -m 755 bin/compute /usr/local/bin/compute
	sudo install -m 755 bin/computed /usr/local/bin/computed
	@if [ -f bin/compute-worker ]; then sudo install -m 755 bin/compute-worker /usr/local/bin/compute-worker; fi
	@if [ -f scripts/build-rootfs-chroot.sh ]; then sudo install -m 755 scripts/build-rootfs-chroot.sh /usr/local/share/compute/build-rootfs-chroot.sh; fi
	@if [ -f config/daemon.json ] && [ ! -f /etc/compute/daemon.json ]; then sudo install -m 644 config/daemon.json /etc/compute/daemon.json; fi

systemd: ## Install systemd unit files to /etc/systemd/system
	sudo install -d /etc/systemd/system
	sudo install -m 644 config/systemd/computed.service /etc/systemd/system/computed.service
	sudo install -m 644 config/systemd/user/compute-worker.service /etc/systemd/system/compute-worker.service
	-sudo systemctl daemon-reload

release-tarballs: ## Build cross-platform release tarballs and SHA-256 checksums in dist/
	mkdir -p dist
	@for arch in amd64 arm64; do \
		echo "Building linux/$${arch}..."; \
		mkdir -p dist/linux-$${arch}; \
		GOOS=linux GOARCH=$${arch} CGO_ENABLED=0 go build $(LDFLAGS) -o dist/linux-$${arch}/compute ./cmd/compute; \
		GOOS=linux GOARCH=$${arch} CGO_ENABLED=0 go build $(LDFLAGS) -o dist/linux-$${arch}/computed ./cmd/computed; \
		GOOS=linux GOARCH=$${arch} CGO_ENABLED=0 go build $(LDFLAGS) -o dist/linux-$${arch}/compute-worker ./cmd/compute-worker; \
		GOOS=linux GOARCH=$${arch} CGO_ENABLED=0 go build $(LDFLAGS) -o dist/linux-$${arch}/compute-bench ./cmd/compute-bench; \
		tar -czf dist/compute_$(VERSION)_linux_$${arch}.tar.gz -C dist/linux-$${arch} compute; \
		tar -czf dist/computed_$(VERSION)_linux_$${arch}.tar.gz -C dist/linux-$${arch} computed; \
		tar -czf dist/compute-worker_$(VERSION)_linux_$${arch}.tar.gz -C dist/linux-$${arch} compute-worker; \
		tar -czf dist/compute-bench_$(VERSION)_linux_$${arch}.tar.gz -C dist/linux-$${arch} compute-bench; \
		tar -czf dist/compute-suite_$(VERSION)_linux_$${arch}.tar.gz -C dist/linux-$${arch} compute computed compute-worker compute-bench; \
	done
	cd dist && sha256sum *.tar.gz > checksums.txt

