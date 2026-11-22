GO ?= go
PREFIX ?= $(HOME)/.local

.PHONY: build install test vet bench clean

build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/hostdb ./cmd/hostdb

install: build
	install -d "$(DESTDIR)$(PREFIX)/bin"
	install -m 0755 bin/hostdb "$(DESTDIR)$(PREFIX)/bin/hostdb"

test:
	CGO_ENABLED=0 $(GO) test ./...

vet:
	CGO_ENABLED=0 $(GO) vet ./...

bench:
	CGO_ENABLED=0 $(GO) test ./tests -run='^$$' -bench=. -benchmem

clean:
	rm -f bin/hostdb
