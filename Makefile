GOCACHE ?= /tmp/hane-go-build
export GOCACHE

PREFIX ?= $(HOME)/.local
VERSION ?= $(shell sed -n 's/^var Version = "\(.*\)"/\1/p' internal/buildinfo/version.go)
LDFLAGS = -s -w -X github.com/dmillzilla/hane/internal/buildinfo.Version=$(VERSION)

.PHONY: build run install test check release
build:
	go build -ldflags "$(LDFLAGS)" -o hane ./cmd/hane

run: build
	./hane

install: build
	install -d "$(DESTDIR)$(PREFIX)/bin"
	install -m755 hane "$(DESTDIR)$(PREFIX)/bin/hane"

test:
	go test ./...

check:
	go test ./...
	go vet ./...

release:
	./scripts/release.sh "$(VERSION)"
