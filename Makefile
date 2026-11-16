GO ?= go
NPM ?= npm

.PHONY: all build control agent node edge web test vet clean dev-control dev-web

all: build

build: control agent node edge

control: web
	$(GO) build -trimpath -ldflags "-s -w" -o bin/control ./cmd/control

agent:
	$(GO) build -trimpath -ldflags "-s -w" -o bin/agent ./cmd/agent

node:
	$(GO) build -trimpath -ldflags "-s -w" -o bin/node ./cmd/node

edge:
	$(GO) build -trimpath -ldflags "-s -w" -o bin/edge ./cmd/edge

# The console is built into web/dist and embedded by the Go binary.
web:
	cd web && $(NPM) ci && $(NPM) run build

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

dev-control:
	$(GO) run ./cmd/control

dev-web:
	cd web && $(NPM) run dev

clean:
	rm -rf bin web/dist/assets
