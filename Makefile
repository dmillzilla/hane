GOCACHE ?= /tmp/hane-go-build
export GOCACHE

PREFIX ?= $(HOME)/.local

.PHONY: build run install test check
build:
	go build -o hane .

run: build
	./hane

install: build
	install -Dm755 hane "$(DESTDIR)$(PREFIX)/bin/hane"

test:
	go test ./...

check:
	go test ./...
	go vet ./...
