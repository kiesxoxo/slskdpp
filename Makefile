.PHONY: build build-go ui ui-check test vet fmt clean

BIN := bin/slskdpp
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/kiesxoxo/slskdpp/internal/version.Version=$(VERSION)

# build compiles the UI and embeds it into the binary.
build: ui build-go

build-go:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN) ./cmd/slskdpp

ui: web/node_modules
	cd web && npm run build

ui-check: web/node_modules
	cd web && npm run check

web/node_modules: web/package.json web/package-lock.json
	cd web && npm ci
	touch web/node_modules

test:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

clean:
	rm -rf bin
	find web/dist -mindepth 1 ! -name .gitkeep -delete
