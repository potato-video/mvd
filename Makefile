# mvd's optional parts are build tags:
#
#   webui      the web pages (html/template, htmx)
#   files      getting things in from elsewhere: uploads (/api/uploads, mvd
#              upload), the Incoming page (with webui) and the Needs you API.
#              Without it, mvd import at a terminal is the way in.
#   downloads  downloads, through Transmission: mvd torrent, /api/downloads
#              (shown on the Incoming page with webui and files)
#   unpack     unpacking .zip and .rar archives on import (and uploading
#              them, with files)
#   (ffmpeg    transcoding: not yet)
#
# FEATURES picks them; PRESET is a shorthand:
#
#   make                          the standard build: webui files downloads unpack
#   make PRESET=minimal           import, refresh, the API and streaming only
#   make FEATURES="webui"         any mix
#   make install                  build, then symlink mvd into ~/.local/bin (BINDIR=...)
#   make router                   the same, for the GL-AR750S (big-endian MIPS)
#   make sizes                    how much each feature adds, on the router
#
# Plain go build works too: go build -tags 'webui files downloads unpack' ./cmd/mvd
#
# The tags are independent, but downloads and unpack are helpers for getting
# things in, and without files they lose their part of the website (the
# Incoming page) and archive uploads: make warns.

GO       ?= go
PRESET   ?= standard
ALL      := webui files downloads unpack

preset_minimal  :=
preset_standard := webui files downloads unpack
preset_full     := $(ALL)
ifeq ($(origin preset_$(PRESET)),undefined)
$(error unknown PRESET $(PRESET): minimal, standard or full)
endif
FEATURES ?= $(preset_$(PRESET))
ifeq ($(filter files,$(FEATURES)),)
ifneq ($(filter downloads,$(FEATURES)),)
$(warning downloads without files: downloads work from mvd torrent and the API, but the website has no Incoming page to show them)
endif
ifneq ($(filter unpack,$(FEATURES)),)
$(warning unpack without files: mvd import unpacks archives, but nothing can upload them)
endif
endif

VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)
BUILD    = $(GO) build -trimpath -ldflags='$(LDFLAGS)' -tags '$(FEATURES)'
ROUTER   := CGO_ENABLED=0 GOOS=linux GOARCH=mips GOMIPS=softfloat
ROUTER_OUT ?= mvd-linux-mips

.PHONY: all mvd install router mips arm64 release test sizes clean

all: mvd

mvd:
	$(BUILD) -o mvd ./cmd/mvd

# Builds mvd, then links it into $(BINDIR) (default ~/.local/bin).
BINDIR ?= $(HOME)/.local/bin

install: mvd
	mkdir -p '$(BINDIR)'
	ln -sf '$(CURDIR)/mvd' '$(BINDIR)/mvd'

router:
	$(ROUTER) $(BUILD) -o $(ROUTER_OUT) ./cmd/mvd

mips:
	CGO_ENABLED=0 GOOS=linux GOARCH=mips GOMIPS=softfloat $(BUILD) -o mvd-linux-mips ./cmd/mvd

arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(BUILD) -o mvd-linux-arm64 ./cmd/mvd


# What a release holds, in dist/: a build for each kind of machine, the
# installer (scripts/install.sh, which fetches them) and their checksums.
# The builds have the website and uploads but not downloads or unpack: for
# downloads, build mvd yourself. .github/workflows/release.yml runs this for
# a v* tag.
RELEASE_FEATURES := webui files
RELEASE_TARGETS  := mips mipsle arm arm64 amd64
goenv_mips   := GOARCH=mips GOMIPS=softfloat
goenv_mipsle := GOARCH=mipsle GOMIPS=softfloat
goenv_arm    := GOARCH=arm GOARM=6
goenv_arm64  := GOARCH=arm64
goenv_amd64  := GOARCH=amd64

release:
	rm -rf dist && mkdir dist
	$(foreach t,$(RELEASE_TARGETS),CGO_ENABLED=0 GOOS=linux $(goenv_$(t)) $(GO) build -trimpath -ldflags='$(LDFLAGS)' -tags '$(RELEASE_FEATURES)' -o dist/mvd-linux-$(t) ./cmd/mvd &&) true
	cp scripts/install.sh dist/
	cd dist && (sha256sum mvd-linux-* 2>/dev/null || shasum -a 256 mvd-linux-*) > SHA256SUMS

# Every combination of features: none, webui, files, webui+files, ... (Pure make:
# macOS's make 3.81 treats a # inside $(shell ...) as a comment.)
_join    = $(if $(filter none,$(2)),$(1),$(1)+$(2))
powerset = $(if $(1),$(foreach r,$(call powerset,$(wordlist 2,$(words $(1)),$(1))),$(r) $(call _join,$(firstword $(1)),$(r))),none)
COMBOS  := $(call powerset,$(ALL))

# Vet and test each combination, so a stub that's drifted from the real thing
# shows up.
test:
	@for c in $(COMBOS); do t=$$(echo $$c | sed 's/none//; s/+/ /g'); \
		echo "== features: $${t:-none}"; \
		$(GO) vet -tags "$$t" ./... && $(GO) test -tags "$$t" ./... || exit 1; done

sizes:
	@d=$$(mktemp -d); for c in $(COMBOS); do t=$$(echo $$c | sed 's/none//; s/+/ /g'); \
		$(ROUTER) $(GO) build -trimpath -ldflags='-s -w' -tags "$$t" -o $$d/$$c ./cmd/mvd || exit 1; \
		done; \
	base=$$(wc -c < $$d/none); \
	printf '%-24s %9s %9s\n' features bytes added; \
	for c in $(COMBOS); do s=$$(wc -c < $$d/$$c); \
		printf '%-24s %9d %+9d\n' "$$(echo $$c | tr + ' ')" $$s $$((s-base)); done; \
	rm -rf $$d

clean:
	rm -f mvd mvd-linux-*
	rm -rf dist
