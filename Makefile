.PHONY: all build test lint clean setup plugin-install plugin-build plugin-dev release release-clean

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.0.0-dev")

BINARY     := upgrade-guardian
MODULE     := upgrade-guardian
BUILD_DIR  := bin
GO_FLAGS   := -trimpath

all: build plugin-build

## Go backend
build: build-server build-cli

build-server:
	go build $(GO_FLAGS) -o $(BUILD_DIR)/$(BINARY) ./cmd/server

build-cli:
	go build $(GO_FLAGS) -o $(BUILD_DIR)/$(BINARY)-cli ./cmd/cli

test:
	go test -race -count=1 ./...

lint:
	golangci-lint run ./...

clean:
	rm -rf $(BUILD_DIR)/ plugin/dist/ plugin/node_modules/

setup:
	@echo "Installing all checker dependencies..."
	bash scripts/setup.sh

PLUGIN_DEPLOY_DIR := $(HOME)/.config/Headlamp/plugins/upgrade-guardian

## Headlamp plugin
plugin-install:
	cd plugin && npm install

plugin-build: plugin-install
	cd plugin && node build.mjs
	mkdir -p $(PLUGIN_DEPLOY_DIR)
	cp plugin/dist/main.js $(PLUGIN_DEPLOY_DIR)/main.js
	cp plugin/package.json $(PLUGIN_DEPLOY_DIR)/package.json

plugin-dev: plugin-install
	cd plugin && node build.mjs
	mkdir -p $(PLUGIN_DEPLOY_DIR)
	cp plugin/dist/main.js $(PLUGIN_DEPLOY_DIR)/main.js
	cp plugin/package.json $(PLUGIN_DEPLOY_DIR)/package.json

## Update checker tool dependencies to latest versions.
## Run this before each Kubernetes minor release cycle.
update-deps:
	@echo "→ Updating Pluto (deprecated API database — embedded, must rebuild after)"
	go get github.com/fairwindsops/pluto/v5@latest
	@echo "→ Updating kubeconform (schema validator)"
	go get github.com/yannh/kubeconform@latest
	@echo "→ Updating Nova (Helm chart scanner)"
	go get github.com/fairwindsops/nova@main
	go mod tidy
	@echo "→ Rebuilding binary with updated databases"
	$(MAKE) build
	@echo "Done. Run 'bin/upgrade-guardian' to use updated databases."

## Run backend locally (uses in-cluster or KUBECONFIG)
dev:
	go run ./cmd/server \
		-addr :8090 \
		-log-level debug

## Build cross-platform release tarballs into dist/.
## Set VERSION=x.y.z to tag the artifacts; defaults to `git describe`.
release:
	VERSION=$(VERSION) bash scripts/release.sh

release-clean:
	rm -rf dist/
