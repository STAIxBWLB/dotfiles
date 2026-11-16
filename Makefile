.PHONY: build test lint clean install docs changelog

VERSION ?= dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)
BIN     := bin/dot

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/dot/

test:
	go test ./... -race -count=1

lint:
	golangci-lint run ./...

docs:
	go run ./tools/gendocs docs/commands

changelog:
	bash scripts/gen-changelog.sh

clean:
	rm -rf bin/

install: build
	@mkdir -p $(HOME)/.local/bin
	install -m 0755 $(BIN) $(HOME)/.local/bin/.dot.new
	mv $(HOME)/.local/bin/.dot.new $(HOME)/.local/bin/dot
	ln -sf $(HOME)/.local/bin/dot $(HOME)/.local/bin/dotfiles
	@for rel in /opt/homebrew/bin/dot /usr/local/bin/dot; do \
	  [ -x "$$rel" ] && echo "note: $(HOME)/.local/bin/dot ($(VERSION)) shadows $$rel ($$($$rel --version 2>/dev/null)) wherever ~/.local/bin comes first on PATH; peer runs on the other Mac use the newest release unless remote_dot pins one"; \
	done; true
