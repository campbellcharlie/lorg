# lorg build targets — concentrate the cgo / build-tag flags so users
# don't have to remember them.

GO          ?= go
GO_LDFLAGS  ?= -s -w
APP_CGO_LDFLAGS ?= -framework UniformTypeIdentifiers
APP_TAGS    ?= desktop production

.PHONY: all
all: lorg-bin lorg-app

# ----- main backend / CLI -----

.PHONY: lorg-bin
lorg-bin:
	$(GO) build -ldflags '$(GO_LDFLAGS)' -o lorg-bin ./cmd/lorg/

# ----- desktop wrapper (Wails v2) -----

.PHONY: lorg-app
lorg-app: lorg-bin
	CGO_LDFLAGS='$(APP_CGO_LDFLAGS)' \
	CGO_CFLAGS='-Wno-deprecated-declarations' \
	$(GO) build -tags '$(APP_TAGS)' -ldflags '$(GO_LDFLAGS)' -o lorg-app ./cmd/lorg-app/

# Build a real macOS .app bundle with dock icon + double-click launch.
# Requires the Wails CLI: go install github.com/wailsapp/wails/v2/cmd/wails@latest
# (often installs to ~/go/bin which may not be on PATH — use WAILS=path/to/wails
# to override).
WAILS ?= $(shell command -v wails || echo $(shell go env GOPATH)/bin/wails)
APP_BUNDLE = cmd/lorg-app/build/bin/lorg.app

.PHONY: lorg-app-bundle
lorg-app-bundle: lorg-bin
	@test -x "$(WAILS)" || { echo "wails CLI not found at '$(WAILS)' — run: go install github.com/wailsapp/wails/v2/cmd/wails@latest"; exit 1; }
	cd cmd/lorg-app && $(WAILS) build -clean -platform darwin/arm64 -ldflags '$(GO_LDFLAGS)' -skipbindings
	cp lorg-bin $(APP_BUNDLE)/Contents/MacOS/lorg-bin
ifneq ($(SIGN_IDENTITY),)
	@echo "Signing with: $(SIGN_IDENTITY)"
	codesign --deep --force --options runtime --sign '$(SIGN_IDENTITY)' $(APP_BUNDLE)
	@echo "Signature:"
	@codesign -dv $(APP_BUNDLE) 2>&1 | sed 's/^/  /'
endif
	@echo
	@echo "Bundle ready: $(APP_BUNDLE)"
	@echo "Try it:    open $(APP_BUNDLE)"
	@echo "Install:   cp -R $(APP_BUNDLE) /Applications/"
ifeq ($(SIGN_IDENTITY),)
	@echo
	@echo "Unsigned. To sign for local use, run:"
	@echo "  make lorg-app-bundle SIGN_IDENTITY='Apple Development: Your Name (TEAMID)'"
	@echo "For distribution to other Macs without Gatekeeper warnings, sign"
	@echo "with a Developer ID Application cert and run 'make notarize'."
endif

# Notarize the (signed) bundle with Apple. Needed for Gatekeeper-clean
# distribution on other Macs running macOS 10.14.5+.
#
# Prereqs (one-time): generate an app-specific password at
# https://account.apple.com → Sign-In and Security → App-Specific
# Passwords, then save it to Keychain so you don't have to keep
# pasting it:
#
#   xcrun notarytool store-credentials lorg-notarize \
#       --apple-id <your-apple-id> \
#       --team-id  VPQDX6CMQS \
#       --password <app-specific-password>
#
# After that, plain `make notarize` re-uses the stored credentials.
NOTARY_PROFILE ?= lorg-notarize
NOTARY_ZIP     := cmd/lorg-app/build/bin/lorg.zip

.PHONY: notarize
notarize:
	@test -d $(APP_BUNDLE) || { echo "$(APP_BUNDLE) not found — run 'make lorg-app-bundle' first"; exit 1; }
	@codesign -dvv $(APP_BUNDLE) 2>&1 | grep -q 'Authority=Developer ID Application' || { echo "Bundle isn't signed with a Developer ID Application cert. Notarization will be rejected."; exit 1; }
	@echo "Zipping bundle for upload..."
	@cd $(dir $(APP_BUNDLE)) && /usr/bin/ditto -c -k --keepParent --sequesterRsrc $(notdir $(APP_BUNDLE)) $(notdir $(NOTARY_ZIP))
	@echo "Submitting to Apple Notary Service (waits up to ~10 minutes for result)..."
	xcrun notarytool submit $(NOTARY_ZIP) --keychain-profile $(NOTARY_PROFILE) --wait
	@echo "Stapling notarization ticket to the bundle..."
	xcrun stapler staple $(APP_BUNDLE)
	@echo
	@echo "Final spctl assessment:"
	@spctl -a -vv $(APP_BUNDLE) 2>&1 | sed 's/^/  /'

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
#   make release VERSION=v2026.4.25 SIGN_IDENTITY="Developer ID Application: ..."
#     → builds + signs + notarizes lorg-app, packages everything into ./dist/
#       (no GitHub upload, no tag pushed). Inspect, then:
#
#   make publish VERSION=v2026.4.25
#     → creates the matching GitHub Release with artifacts.
#
# `release` and `publish` are split so you can verify the artifacts
# locally before they go live. `make publish` is idempotent — if the
# release already exists at $(VERSION) it'll error, just delete it
# from gh and re-run.

RELEASE_DIR    := dist
LORG_APP_ZIP   := $(RELEASE_DIR)/lorg-app-$(VERSION)-darwin-arm64.zip
LORG_BIN_TGZ   := $(RELEASE_DIR)/lorg-bin-$(VERSION)-darwin-arm64.tar.gz
SHASUM_FILE    := $(RELEASE_DIR)/SHA256SUMS-$(VERSION).txt

.PHONY: release
release: _check-version _check-sign _check-gh
	$(MAKE) lorg-app-bundle SIGN_IDENTITY='$(SIGN_IDENTITY)'
	$(MAKE) notarize
	mkdir -p $(RELEASE_DIR)
	# Desktop bundle as a zip the way macOS expects (ditto preserves
	# code signature attributes that plain `zip` strips).
	cd $(dir $(APP_BUNDLE)) && /usr/bin/ditto -c -k --keepParent --sequesterRsrc \
	    $(notdir $(APP_BUNDLE)) $(CURDIR)/$(LORG_APP_ZIP)
	# Headless server binary as a tar.gz (preserves the executable bit
	# better than zip on Unix; what most users wget + tar -xzf expect).
	tar czf $(LORG_BIN_TGZ) lorg-bin
	cd $(RELEASE_DIR) && shasum -a 256 \
	    lorg-app-$(VERSION)-darwin-arm64.zip \
	    lorg-bin-$(VERSION)-darwin-arm64.tar.gz \
	    > SHA256SUMS-$(VERSION).txt
	@echo
	@echo "================================================================"
	@echo "Release artifacts staged in $(RELEASE_DIR)/:"
	@ls -lh $(RELEASE_DIR)/lorg-app-$(VERSION)*.zip \
	         $(RELEASE_DIR)/lorg-bin-$(VERSION)*.tar.gz \
	         $(SHASUM_FILE)
	@echo
	@echo "Verify locally first:"
	@echo "  spctl -a -vv $(APP_BUNDLE)"
	@echo "  shasum -c $(SHASUM_FILE)"
	@echo
	@echo "Then publish to GitHub:"
	@echo "  make publish VERSION=$(VERSION)"
	@echo "================================================================"

.PHONY: publish
publish: _check-version _check-gh
	@test -f $(LORG_APP_ZIP) || { echo "$(LORG_APP_ZIP) missing — run 'make release VERSION=$(VERSION) SIGN_IDENTITY=...' first"; exit 1; }
	gh release create $(VERSION) \
	    --title 'lorg $(VERSION)' \
	    --notes "Built from $$(git rev-parse --short HEAD).\n\nDownloads:\n- lorg-app: signed + notarized macOS .app bundle (drag into Applications)\n- lorg-bin: headless backend for browser-only / CLI use\n- SHA256SUMS: verify with shasum -c" \
	    $(LORG_APP_ZIP) $(LORG_BIN_TGZ) $(SHASUM_FILE)

# Internal pre-flight checks, kept terse to not clutter the recipes.
.PHONY: _check-version _check-sign _check-gh
_check-version:
	@test -n "$(VERSION)" || { echo "VERSION not set. Example: make release VERSION=v2026.4.25 SIGN_IDENTITY='...'"; exit 1; }
	@echo "$(VERSION)" | grep -qE '^v[0-9]{4}\.[0-9]+\.[0-9]+(-[a-z0-9]+)?$$' || { echo "VERSION should look like vYYYY.M.D (CalVer) — got '$(VERSION)'"; exit 1; }
_check-sign:
	@test -n "$(SIGN_IDENTITY)" || { echo "SIGN_IDENTITY not set. Need a Developer ID Application identity, e.g.\n  make release VERSION=$(VERSION) SIGN_IDENTITY=\"Developer ID Application: Your Name (TEAMID)\""; exit 1; }
_check-gh:
	@command -v gh >/dev/null || { echo "gh CLI required. brew install gh && gh auth login"; exit 1; }
	@gh auth status >/dev/null 2>&1 || { echo "gh not authenticated. Run: gh auth login"; exit 1; }

# ----- cleanup -----

.PHONY: clean
clean:
	rm -f lorg-bin lorg-app lorg-launcher lorg-server lorg-tool
	rm -rf cmd/lorg-app/build/bin
	rm -rf $(RELEASE_DIR)
