PREFIX      ?= /usr/local
BINDIR      ?= $(PREFIX)/bin
LICENSEDIR  ?= $(PREFIX)/share/licenses/runitctl
LOCALEDIR   ?= $(PREFIX)/share/runitctl/locales
BIN         := runitctl
JOURNAL_BIN := runit-journal
BASHCOMPDIR ?= $(PREFIX)/share/bash-completion/completions
ZSHCOMPDIR  ?= $(PREFIX)/share/zsh/site-functions
FISHCOMPDIR ?= $(PREFIX)/share/fish/vendor_completions.d
GO          ?= go
INSTALL     := install
VERSION     ?= 1.1
GOFLAGS_REL := -trimpath -ldflags "-s -w -X main.version=$(VERSION)"

.PHONY: setup-logs all build install uninstall clean fmt vet test race check run

all: build

build:
	$(GO) build $(GOFLAGS_REL) -o $(BIN) .
	$(GO) build $(GOFLAGS_REL) -o $(JOURNAL_BIN) ./cmd/runit-journal

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

check: fmt vet test

install: build
	$(INSTALL) -d $(DESTDIR)$(BINDIR)
	$(INSTALL) -m 0755 $(BIN) $(DESTDIR)$(BINDIR)/$(BIN)
	$(INSTALL) -m 0755 $(JOURNAL_BIN) $(DESTDIR)$(BINDIR)/$(JOURNAL_BIN)
	$(INSTALL) -d $(DESTDIR)$(LICENSEDIR)
	$(INSTALL) -m 0644 LICENSE $(DESTDIR)$(LICENSEDIR)/LICENSE
	$(INSTALL) -d $(DESTDIR)$(LOCALEDIR)
	$(INSTALL) -m 0644 locales/*.kn $(DESTDIR)$(LOCALEDIR)/
	$(INSTALL) -d $(DESTDIR)$(BASHCOMPDIR) $(DESTDIR)$(ZSHCOMPDIR) $(DESTDIR)$(FISHCOMPDIR)
	./$(BIN) completion bash > $(DESTDIR)$(BASHCOMPDIR)/$(BIN)
	cp $(DESTDIR)$(BASHCOMPDIR)/$(BIN) $(DESTDIR)$(BASHCOMPDIR)/$(JOURNAL_BIN)
	./$(BIN) completion zsh > $(DESTDIR)$(ZSHCOMPDIR)/_$(BIN)
	./$(BIN) completion fish > $(DESTDIR)$(FISHCOMPDIR)/$(BIN).fish
	cp $(DESTDIR)$(FISHCOMPDIR)/$(BIN).fish $(DESTDIR)$(FISHCOMPDIR)/$(JOURNAL_BIN).fish
	@echo "runitctl instalado en $(DESTDIR)$(BINDIR)/$(BIN)"

uninstall:
	rm -f $(DESTDIR)$(BINDIR)/$(BIN) $(DESTDIR)$(BINDIR)/$(JOURNAL_BIN)
	rm -rf $(DESTDIR)$(LOCALEDIR) $(DESTDIR)$(LICENSEDIR)
	rm -f $(DESTDIR)$(BASHCOMPDIR)/$(BIN) $(DESTDIR)$(BASHCOMPDIR)/$(JOURNAL_BIN) $(DESTDIR)$(ZSHCOMPDIR)/_$(BIN) $(DESTDIR)$(FISHCOMPDIR)/$(BIN).fish $(DESTDIR)$(FISHCOMPDIR)/$(JOURNAL_BIN).fish
	@echo "runitctl desinstalado."

clean:
	rm -f $(BIN) $(JOURNAL_BIN)

run: build
	./$(BIN) $(ARGS)

# Instala socklog-void y habilita socklog-unix y nanoklogd (Void Linux, requiere root).
setup-logs: build
	./$(BIN) setup-logs
