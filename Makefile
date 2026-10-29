.PHONY: all build test clean setup-host build-rootfs

all: build

build:
	mkdir -p bin
	go build -o bin/mforge ./cmd/mforge
	go build -o bin/microforged ./cmd/microforged

test:
	go test -v ./...

setup-host:
	sudo ./scripts/setup-host.sh

build-rootfs:
	sudo ./scripts/build-rootfs.sh /var/lib/microforge/images 512

clean:
	rm -rf bin/
