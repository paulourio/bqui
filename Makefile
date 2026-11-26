# bqui build/test loop. Everything is pure Go (CGO_ENABLED=0) and cross-compiles from Linux.

VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X main.version=$(VERSION)
DIST      := dist

# QEMU macOS VM (x86_64). Override on the command line as needed.
VM_HOST   ?= mac@localhost
VM_PORT   ?= 2222
VM_DIR    ?= bqui-dev
SSH       := ssh -p $(VM_PORT) -o BatchMode=yes $(VM_HOST)

export CGO_ENABLED := 0

.PHONY: all test vet build-linux vm-deploy vm-run vm-smoke clean

all: test build-darwin-arm64 build-darwin-amd64

test:
	go test ./...

vet:
	go vet ./...
	GOOS=darwin GOARCH=arm64 go vet ./...
	GOOS=darwin GOARCH=amd64 go vet ./...

build-linux:
	go build -o $(DIST)/linux/ ./cmd/...

build-darwin-%: FORCE
	GOOS=darwin GOARCH=$* go build -trimpath -ldflags '$(LDFLAGS)' -o $(DIST)/darwin-$*/bqui ./cmd/bqui

bundle-%: build-darwin-%
	go run ./tools/bundle -bin $(DIST)/darwin-$*/bqui -o $(DIST)/darwin-$*/bqui.app -version $(VERSION:v%=%)

# Copy the amd64 bundle into the VM.
vm-deploy: bundle-amd64
	$(SSH) 'mkdir -p $(VM_DIR) && rm -rf $(VM_DIR)/bqui.app'
	scp -q -P $(VM_PORT) -r $(DIST)/darwin-amd64/bqui.app $(VM_HOST):$(VM_DIR)/

# Launch the app in the VM's GUI session.
vm-run: vm-deploy
	$(SSH) 'open -n $(VM_DIR)/bqui.app'

# Run the offscreen smoke test in the VM and fetch screenshots + report.
vm-smoke: vm-deploy
	$(SSH) 'rm -rf $(VM_DIR)/smoke && open -W -n --stdout $$HOME/$(VM_DIR)/smoke.log --stderr $$HOME/$(VM_DIR)/smoke.log \
		$(VM_DIR)/bqui.app --args --smoke-test=$$HOME/$(VM_DIR)/smoke; rc=$$?; cat $(VM_DIR)/smoke.log; exit $$rc'
	rm -rf $(DIST)/smoke-vm && scp -q -P $(VM_PORT) -r $(VM_HOST):$(VM_DIR)/smoke $(DIST)/smoke-vm
	cat $(DIST)/smoke-vm/report.json

clean:
	rm -rf $(DIST)

FORCE:
