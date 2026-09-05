BINARY  ?= oskar
MODULE  := github.com/alessandrocorsico/oskar
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X $(MODULE)/internal/cli.version=$(VERSION)
IMAGE   ?= ghcr.io/alessandrocorsico/oskar:$(VERSION)

GOLANGCI_LINT_VERSION ?= v2.13.2

.PHONY: build test cover vet lint vuln fmt tidy verify image install-plugin clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY) ./cmd/oskar

test:
	go test -race ./...

cover:
	go test -race -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -func=coverage.out | tail -1

vet:
	go vet ./...

lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

fmt:
	gofmt -l -w .

tidy:
	go mod tidy

# What CI enforces: go.sum verified, tidy changes nothing, gofmt clean.
verify:
	go mod verify
	go mod tidy
	git diff --exit-code -- go.mod go.sum
	test -z "$$(gofmt -l .)"

# Local image with the version baked in (see Dockerfile).
image:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE) .

# Install as a kubectl plugin without krew: a binary named kubectl-oskar on
# PATH is all a kubectl plugin is. After this: `kubectl oskar scan`.
install-plugin: build
	cp bin/$(BINARY) $(shell go env GOPATH)/bin/kubectl-$(BINARY)

clean:
	rm -rf bin dist coverage.out
