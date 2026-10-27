BIN := bin/maio
GO ?= go
CGO_ENABLED ?= 0
VERSION ?= 3.0.0
ARCH ?= amd64
JOBS ?= $(shell nproc 2>/dev/null || echo 4)

.PHONY: build test race vet fmt install uninstall smoke soak clean deb repo \
	llama llama-bundle deb-llama verify

build:
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build -trimpath -ldflags "-s -w -X github.com/ShieldDog-ACE/maio-cluster/internal/cli.Version=$(VERSION)" -o $(BIN) .

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -l -w .

install: build
	sudo bash deploy/install.sh

uninstall:
	sudo systemctl disable --now maio 2>/dev/null || true
	sudo rm -f /usr/local/bin/maio /etc/systemd/system/maio.service
	sudo systemctl daemon-reload 2>/dev/null || true

smoke: build
	bash scripts/smoke.sh

# The soak test uses the mock engine and a temporary state directory, so it
# needs no GPU and no root. DURATION=60 CONCURRENCY=64 by default.
soak: build
	DURATION="$(DURATION)" CONCURRENCY="$(CONCURRENCY)" bash scripts/soak.sh

deb: build
	VERSION=$(VERSION) ARCH=$(ARCH) bash deploy/build-deb.sh

repo: deb
	VERSION=$(VERSION) ARCH=$(ARCH) bash deploy/apt-repo.sh

# Minimal Linux x86-64 llama-server, statically linked against llama/ggml.
llama:
	CUDA="$(CUDA)" CUDA_ARCH="$(CUDA_ARCH)" JOBS="$(JOBS)" bash scripts/build-llama-minimal.sh

# Prune an existing llama.cpp build (no compiler needed).
llama-bundle:
	LLAMA_BUILD="$(LLAMA_BUILD)" bash scripts/bundle-llama.sh

deb-llama:
	VERSION=$(VERSION) ARCH=$(ARCH) bash deploy/build-llama-deb.sh

verify: vet test smoke
	@echo "verify: vet, unit tests and smoke test passed"

clean:
	rm -rf bin dist
