PREFIX      ?= /usr/local
BINDIR      ?= $(PREFIX)/bin
LOCALEDIR   ?= $(PREFIX)/share/runitctl/locales
BIN         := runitctl
JOURNAL_BIN := runit-journal
GO          ?= go
INSTALL     := install
VERSION     ?= 1.0
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
	$(INSTALL) -d $(DESTDIR)$(LOCALEDIR)
	$(INSTALL) -m 0644 locales/*.kn $(DESTDIR)$(LOCALEDIR)/
	@echo "runitctl instalado en $(DESTDIR)$(BINDIR)/$(BIN)"

uninstall:
	rm -f $(DESTDIR)$(BINDIR)/$(BIN) $(DESTDIR)$(BINDIR)/$(JOURNAL_BIN)
	rm -rf $(DESTDIR)$(LOCALEDIR)
	@echo "runitctl desinstalado."

clean:
	rm -f $(BIN) $(JOURNAL_BIN)

run: build
	./$(BIN) $(ARGS)

# Instala socklog-void y habilita socklog-unix y nanoklogd (Void Linux, requiere root).
setup-logs: build
	./$(BIN) setup-logs
