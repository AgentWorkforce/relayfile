SHELL := /bin/sh

GO ?= go
BIN_DIR ?= bin
DIST_DIR ?= dist
INSTALL_DIR ?= /usr/local/bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GOFLAGS ?=
LDFLAGS ?= -s -w

CLI_PKG := ./cmd/relayfile-cli
SERVER_PKG := ./cmd/relayfile
MOUNT_PKG := ./cmd/relayfile-mount

CLI_BIN := relayfile-cli
SERVER_BIN := relayfile-server
MOUNT_BIN := relayfile-mount

TARGETS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64
RELEASE_BINS := $(CLI_BIN) $(SERVER_BIN) $(MOUNT_BIN)

.PHONY: build build-all install test release clean

build:
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(CLI_BIN) $(CLI_PKG)
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(SERVER_BIN) $(SERVER_PKG)
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(MOUNT_BIN) $(MOUNT_PKG)

build-all:
	rm -rf $(DIST_DIR)
	mkdir -p $(DIST_DIR)
	set -eu; \
	for target in $(TARGETS); do \
		goos=$${target%/*}; \
		goarch=$${target#*/}; \
		out_dir="$(DIST_DIR)/$${goos}_$${goarch}"; \
		mkdir -p "$${out_dir}"; \
		CGO_ENABLED=0 GOOS=$${goos} GOARCH=$${goarch} $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o "$${out_dir}/$(CLI_BIN)" $(CLI_PKG); \
		CGO_ENABLED=0 GOOS=$${goos} GOARCH=$${goarch} $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o "$${out_dir}/$(SERVER_BIN)" $(SERVER_PKG); \
		CGO_ENABLED=0 GOOS=$${goos} GOARCH=$${goarch} $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o "$${out_dir}/$(MOUNT_BIN)" $(MOUNT_PKG); \
	done

install: build
	install -d $(INSTALL_DIR)
	install -m 0755 $(BIN_DIR)/$(CLI_BIN) $(INSTALL_DIR)/relayfile
	install -m 0755 $(BIN_DIR)/$(SERVER_BIN) $(INSTALL_DIR)/$(SERVER_BIN)
	install -m 0755 $(BIN_DIR)/$(MOUNT_BIN) $(INSTALL_DIR)/$(MOUNT_BIN)

test:
	$(GO) test ./...

release: clean build-all
	set -eu; \
	for target in $(TARGETS); do \
		goos=$${target%/*}; \
		goarch=$${target#*/}; \
		work_dir="$(DIST_DIR)/$${goos}_$${goarch}"; \
		for bin in $(RELEASE_BINS); do \
			archive="$(DIST_DIR)/$${bin}_$${goos}_$${goarch}.tar.gz"; \
			LC_ALL=C tar -C "$${work_dir}" -czf "$${archive}" "$${bin}"; \
		done; \
	done
	cd $(DIST_DIR) && \
		(if command -v sha256sum >/dev/null 2>&1; then sha256sum *.tar.gz > checksums.txt; else shasum -a 256 *.tar.gz > checksums.txt; fi)

clean:
	rm -rf $(BIN_DIR) $(DIST_DIR)
