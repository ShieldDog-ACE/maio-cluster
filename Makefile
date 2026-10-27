BIN := bin/maio
GO ?= go
CGO_ENABLED ?= 0
VERSION ?= 2.0.0
ARCH ?= amd64

.PHONY: build test vet fmt install uninstall smoke clean deb repo

build:
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build -trimpath -ldflags "-s -w -X github.com/ShieldDog-ACE/maio-cluster/internal/cli.Version=$(VERSION)" -o $(BIN) .

test:
	$(GO) test ./...

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

deb: build
	VERSION=$(VERSION) ARCH=$(ARCH) bash deploy/build-deb.sh

repo: deb
	VERSION=$(VERSION) ARCH=$(ARCH) bash deploy/apt-repo.sh

clean:
	rm -rf bin dist
