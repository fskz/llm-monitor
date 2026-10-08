.PHONY: build test vet release clean

BINARY := llm-monitor
PLATFORMS := windows/amd64 windows/arm64 linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

build:
	go build -o $(BINARY) ./cmd/llm-monitor

test:
	go test ./... -race -count=1

vet:
	go vet ./...

release:
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		ext=""; if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		echo "building $$os/$$arch -> dist/$(BINARY)-$$os-$$arch$$ext"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "-s -w" \
			-o dist/$(BINARY)-$$os-$$arch$$ext ./cmd/llm-monitor || exit 1; \
	done
	@echo "release artifacts in dist/"

clean:
	rm -rf dist $(BINARY)
