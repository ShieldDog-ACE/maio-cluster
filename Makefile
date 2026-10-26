BIN := bin/maio
GO ?= go
VERSION ?= 0.1.0-dev

.PHONY: build test vet fmt install uninstall smoke clean

build:
	$(GO) build -trimpath -ldflags "-s -w -X github.com/ShieldDog-ACE/maio--cluster/internal/cli.Version=$(VERSION)" -o $(BIN) .

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -l -w .

install: build
	install -m 0755 $(BIN) /usr/local/bin/maio

uninstall:
	rm -f /usr/local/bin/maio

smoke: build
	bash scripts/smoke.sh

clean:
	rm -rf bin
