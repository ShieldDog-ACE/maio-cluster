# maio v4.1 — three platform products plus a shared cluster core.
#
#   make build             Linux product (maio + optional RPC runtime)
#   make build-all         Linux, Windows zip and macOS tarballs
#   make package-linux     .deb (needs dpkg-deb)
#   make apk               quick-view Android APK (needs JAVA_HOME + ANDROID_SDK)
#   make verify            vet + race tests + two-node e2e
#
# Bundle llama.cpp RPC binaries with RPC_BIN=... LLAMA_BIN=...
VERSION ?= 4.1.2
RPC_BIN ?=
LLAMA_BIN ?=
ANDROID_SDK ?=
JAVA_HOME ?=

.PHONY: all build build-all linux windows mac apk web package-linux package-windows \
	package-apt package-mac test race vet fmt e2e verify runtime clean dist

all: build-all

# Rebuild the optional web console (requires tsc); assets are committed already.
web:
	./web/build.sh

build: linux

linux:
	VERSION=$(VERSION) RPC_BIN="$(RPC_BIN)" LLAMA_BIN="$(LLAMA_BIN)" ./linux/build.sh

windows:
	VERSION=$(VERSION) RPC_BIN="$(RPC_BIN)" LLAMA_BIN="$(LLAMA_BIN)" ./windows/build-cross.sh

mac:
	VERSION=$(VERSION) RPC_BIN="$(RPC_BIN)" LLAMA_BIN="$(LLAMA_BIN)" ./mac/build-cross.sh

apk:
	ANDROID_SDK="$(ANDROID_SDK)" JAVA_HOME="$(JAVA_HOME)" ./android/build-apk.sh

build-all: linux windows mac

package-linux:
	VERSION=$(VERSION) RPC_BIN="$(RPC_BIN)" LLAMA_BIN="$(LLAMA_BIN)" ./linux/packaging/build-deb.sh

package-apt:
	VERSION=$(VERSION) ARCH=$(ARCH) RPC_BIN="$(RPC_BIN)" LLAMA_BIN="$(LLAMA_BIN)" ./linux/packaging/build-apt-repo.sh

package-windows: windows
	@echo "Windows installer: dist/maio-windows-amd64.zip"
	@echo "MSI/codesign can be produced with the WiX toolset on Windows."

package-mac: mac
	@echo "macOS tarballs: dist/maio-mac-*.tar.gz"
	@echo "Run mac/packaging/build-pkg.sh on macOS for a signed .pkg."

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

e2e:
	./scripts/e2e.sh

verify: vet race e2e

# Rebuild llama.cpp with the RPC backend (CPU, CUDA or Metal).
runtime:
	./scripts/build-rpc.sh

dist: package-linux windows mac apk

clean:
	rm -rf dist android/build
