.PHONY: all build build-ui dev-ui dev run test test-coverage test-features bench bench-unit clean setup-host install systemd build-rootfs deploy deploy-ui deploy-restart remote-status remote-logs deploy-lab deploy-lab-ui remote-status-lab

all: build

build-ui:
	cd web && (bun run build || npm run build)

dev-ui:
	cd web && (bun run dev || npm run dev)

VERSION ?= 2.0.0
RELEASE_NAME ?= Vulcan
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "dev")
BUILD_DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
PKG_VERSION = github.com/Rashid2G/on-demand-vms/pkg/version
LDFLAGS = -ldflags "-X '$(PKG_VERSION).Version=$(VERSION)' -X '$(PKG_VERSION).ReleaseName=$(RELEASE_NAME)' -X '$(PKG_VERSION).GitCommit=$(GIT_COMMIT)' -X '$(PKG_VERSION).BuildDate=$(BUILD_DATE)'"

build:
	mkdir -p bin
	go build $(LDFLAGS) -o bin/compute ./cmd/compute
	go build $(LDFLAGS) -o bin/computed ./cmd/computed
	go build $(LDFLAGS) -o bin/compute-worker ./cmd/compute-worker
	go build $(LDFLAGS) -o bin/compute-bench ./cmd/compute-bench

dev: build setup-host
	./bin/computed -listen 127.0.0.1:18080

run: dev

install: build
	sudo install -d /usr/local/bin
	sudo install -m 755 bin/compute /usr/local/bin/compute
	sudo install -m 755 bin/computed /usr/local/bin/computed
	@if [ -f bin/compute-worker ]; then sudo install -m 755 bin/compute-worker /usr/local/bin/compute-worker; fi

systemd:
	sudo install -d /etc/systemd/system
	sudo install -m 644 config/systemd/computed.service /etc/systemd/system/computed.service
	sudo install -m 644 config/systemd/user/compute-worker.service /etc/systemd/system/compute-worker.service
	-sudo systemctl daemon-reload

test:
	go test -v ./...

test-coverage:
	go test -v -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

bench-unit:
	go test -bench=. -benchmem ./...

bench: build
	./scripts/benchmark.sh 127.0.0.1:18080 benchmark-results.json

test-features:
	./scripts/test-all-features.sh ./bin/compute 127.0.0.1:18080

setup-host:
	./scripts/setup-host.sh

build-rootfs:
	./scripts/build-rootfs.sh /var/lib/compute/images 1024

deploy:
	./scripts/deploy.sh

deploy-ui:
	./scripts/deploy.sh --ui

deploy-restart:
	./scripts/deploy.sh --restart-only

remote-status:
	./scripts/deploy.sh --status

# Local lab VM (192.168.1.70) that serves the daemon, Web GUI and CLI tests.
LAB_HOST ?= compute-lab

deploy-lab:
	./scripts/deploy.sh

deploy-lab-ui:
	./scripts/deploy.sh --ui

remote-status-lab:
	./scripts/deploy.sh --status

remote-logs:
	ssh $(LAB_HOST) "sudo journalctl -u computed.service -u compute-gui.service -n 50 -f --no-pager"

clean:
	rm -rf bin/ coverage.out benchmark-results.json
