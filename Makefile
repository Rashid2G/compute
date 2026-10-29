.PHONY: all build test test-coverage test-features clean setup-host build-rootfs

all: build

build:
	mkdir -p bin
	go build -o bin/mforge ./cmd/mforge
	go build -o bin/microforged ./cmd/microforged

test:
	go test -v ./...

test-coverage:
	go test -v -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

test-features:
	./scripts/test-all-features.sh ./bin/mforge 127.0.0.1:18080

setup-host:
	sudo ./scripts/setup-host.sh

build-rootfs:
	sudo ./scripts/build-rootfs.sh /var/lib/microforge/images 512

clean:
	rm -rf bin/ coverage.out
