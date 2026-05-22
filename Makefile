# lorg build targets — concentrate the build flags so users don't have
# to remember them.

GO          ?= go
GO_LDFLAGS  ?= -s -w

.PHONY: all
all: lorg-bin

# ----- main backend / CLI -----

.PHONY: lorg-bin
lorg-bin:
	$(GO) build -ldflags '$(GO_LDFLAGS)' -o lorg-bin ./cmd/lorg/

# ----- testing -----

.PHONY: test
test:
	$(GO) test ./apps/app/ -count=1

.PHONY: vet
vet:
	$(GO) vet ./apps/app/ ./lrx/browser/

# ----- release -----
#
# Two-step workflow:
#
#   make release VERSION=v2026.4.25
#     → builds lorg-bin and packages it into ./dist/ (no GitHub upload,
#       no tag pushed). Inspect, then:
#
#   make publish VERSION=v2026.4.25
#     → creates the matching GitHub Release with artifacts.
#
# `release` and `publish` are split so you can verify the artifacts
# locally before they go live. `make publish` is idempotent — if the
# release already exists at $(VERSION) it'll error, just delete it
# from gh and re-run.

RELEASE_DIR    := dist
LORG_BIN_TGZ   := $(RELEASE_DIR)/lorg-bin-$(VERSION)-darwin-arm64.tar.gz
SHASUM_FILE    := $(RELEASE_DIR)/SHA256SUMS-$(VERSION).txt

.PHONY: release
release: _check-version _check-gh lorg-bin
	mkdir -p $(RELEASE_DIR)
	# Headless server binary as a tar.gz (preserves the executable bit
	# better than zip on Unix; what most users wget + tar -xzf expect).
	tar czf $(LORG_BIN_TGZ) lorg-bin
	cd $(RELEASE_DIR) && shasum -a 256 \
	    lorg-bin-$(VERSION)-darwin-arm64.tar.gz \
	    > SHA256SUMS-$(VERSION).txt
	@echo
	@echo "================================================================"
	@echo "Release artifacts staged in $(RELEASE_DIR)/:"
	@ls -lh $(LORG_BIN_TGZ) $(SHASUM_FILE)
	@echo
	@echo "Verify locally first:"
	@echo "  shasum -c $(SHASUM_FILE)"
	@echo
	@echo "Then publish to GitHub:"
	@echo "  make publish VERSION=$(VERSION)"
	@echo "================================================================"

.PHONY: publish
publish: _check-version _check-gh
	@test -f $(LORG_BIN_TGZ) || { echo "$(LORG_BIN_TGZ) missing — run 'make release VERSION=$(VERSION)' first"; exit 1; }
	gh release create $(VERSION) \
	    --title 'lorg $(VERSION)' \
	    --notes "Built from $$(git rev-parse --short HEAD).\n\nDownloads:\n- lorg-bin: headless backend for browser-only / CLI use\n- SHA256SUMS: verify with shasum -c" \
	    $(LORG_BIN_TGZ) $(SHASUM_FILE)

# Internal pre-flight checks, kept terse to not clutter the recipes.
.PHONY: _check-version _check-gh
_check-version:
	@test -n "$(VERSION)" || { echo "VERSION not set. Example: make release VERSION=v2026.4.25"; exit 1; }
	@echo "$(VERSION)" | grep -qE '^v[0-9]{4}\.[0-9]+\.[0-9]+(-[a-z0-9]+)?$$' || { echo "VERSION should look like vYYYY.M.D (CalVer) — got '$(VERSION)'"; exit 1; }
_check-gh:
	@command -v gh >/dev/null || { echo "gh CLI required. brew install gh && gh auth login"; exit 1; }
	@gh auth status >/dev/null 2>&1 || { echo "gh not authenticated. Run: gh auth login"; exit 1; }

# ----- cleanup -----

.PHONY: clean
clean:
	rm -f lorg-bin
	rm -rf $(RELEASE_DIR)
