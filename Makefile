.PHONY: all build test test-coverage test-features bench bench-unit clean setup-host build-rootfs

all: build

build:
	mkdir -p bin
	go build -o bin/mforge ./cmd/mforge
	go build -o bin/microforged ./cmd/microforged
	go build -o bin/mforge-bench ./cmd/mforge-bench

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
	./scripts/test-all-features.sh ./bin/mforge 127.0.0.1:18080

setup-host:
	sudo ./scripts/setup-host.sh

build-rootfs:
	./scripts/build-rootfs.sh /var/lib/microforge/images 1024

clean:
	rm -rf bin/ coverage.out benchmark-results.json
