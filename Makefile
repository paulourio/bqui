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

.PHONY: all test vet lint update-assets build-linux vm-deploy vm-run vm-smoke vm-screen vm-test clean

all: test build-darwin-arm64 build-darwin-amd64

test:
	go test ./...

vet:
	go vet ./...
	GOOS=darwin GOARCH=arm64 go vet ./...
	GOOS=darwin GOARCH=amd64 go vet ./...

lint:
	golangci-lint run ./...
	GOOS=darwin GOARCH=arm64 golangci-lint run ./...
	GOOS=darwin GOARCH=amd64 golangci-lint run ./...

build-linux:
	go build -o $(DIST)/linux/ ./cmd/...

# Maintainer-only: download the pinned Monaco and font releases directly
# (npm, GitHub), verify their digests and rewrite the vendored files under
# internal/editor/web. Never run by builds or CI; review and commit the result.
update-assets:
	go run ./tools/fetchassets -web internal/editor/web

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
	$(SSH) 'rm -rf $(VM_DIR)/smoke && : > $(VM_DIR)/smoke.log && open -W -n --stdout $$HOME/$(VM_DIR)/smoke.log --stderr $$HOME/$(VM_DIR)/smoke.log \
		$(VM_DIR)/bqui.app --args --smoke-test=$$HOME/$(VM_DIR)/smoke; rc=$$?; cat $(VM_DIR)/smoke.log; \
		n=$$(grep -c "WARNING: Application performed" $(VM_DIR)/smoke.log); \
		if [ "$$n" -gt 0 ]; then echo "note: $$n AppKit table reentrancy warnings (known issue, see ROADMAP M8)"; fi; exit $$rc'
	rm -rf $(DIST)/smoke-vm && scp -q -P $(VM_PORT) -r $(VM_HOST):$(VM_DIR)/smoke $(DIST)/smoke-vm
	cat $(DIST)/smoke-vm/report.json

# Cross-compile every package's tests for darwin/amd64 and run them in the VM.
VMTEST := $(DIST)/vmtest
vm-test:
	mkdir -p $(VMTEST)
	for p in $$(GOOS=darwin go list ./...); do \
		GOOS=darwin GOARCH=amd64 go test -c -o $(VMTEST)/$$(echo $$p | tr / _).test $$p || exit 1; \
	done
	$(SSH) 'mkdir -p $(VM_DIR)/vmtest'
	scp -q -P $(VM_PORT) $(VMTEST)/*.test $(VM_HOST):$(VM_DIR)/vmtest/
	$(SSH) 'cd $(VM_DIR)/vmtest && rc=0; for t in *.test; do echo "== $$t"; ./$$t -test.count=1 || rc=1; done; exit $$rc'

# Real on-screen capture (Liquid Glass included). Needs Screen Recording
# permission for /usr/libexec/sshd-keygen-wrapper in the VM (one-time, GUI).
# Runs the smoke flow (fixture data) and captures the state it leaves behind.
vm-screen: vm-deploy
	$(SSH) 'pkill -x bqui; rm -f $(VM_DIR)/shot/report.json; \
		: > $(VM_DIR)/shot.log; \
		open -n --stdout $$HOME/$(VM_DIR)/shot.log --stderr $$HOME/$(VM_DIR)/shot.log \
			$(VM_DIR)/bqui.app --args --smoke-test=$$HOME/$(VM_DIR)/shot --smoke-hold=20s; \
		for i in $$(seq 360); do test -f $(VM_DIR)/shot/report.json && break; sleep 0.5; done; \
		sleep 1; screencapture -x $(VM_DIR)/screen.png; rc=$$?; pkill -x bqui; tail -3 $(VM_DIR)/shot.log; exit $$rc'
	scp -q -P $(VM_PORT) $(VM_HOST):$(VM_DIR)/screen.png $(DIST)/screen-vm.png
	@echo "wrote $(DIST)/screen-vm.png"

clean:
	rm -rf $(DIST)

FORCE:
