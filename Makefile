.PHONY: all build install test race golden bench examples clean

all: build test

build:
	go build -o bin/ibn-5100 ./cmd/ibn-5100

# Symlink the binary onto PATH so `ibn-5100` works anywhere (rebuilds are picked up).
PREFIX ?= $(HOME)/.local
install: build
	mkdir -p $(PREFIX)/bin
	ln -sf $(CURDIR)/bin/ibn-5100 $(PREFIX)/bin/ibn-5100

test:
	go test ./...

race:
	go test -race ./...

# Re-record the expected output of every example after an intended change.
golden:
	go test ./pipeline -update

bench:
	go test ./pipeline -run xxx -bench .

examples: build
	@for f in examples/*.sern; do echo "=== $$f"; ./bin/ibn-5100 run $$f || exit 1; done

clean:
	rm -rf bin examples/*.sernbc
