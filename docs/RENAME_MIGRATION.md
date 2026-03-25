# Repository Rename: `lorg-db` -> `lorg`

**Date:** 2026-03-06
**Status:** Planned
**Old:** `github.com/campbellcharlie/lorg-db`
**New:** `github.com/campbellcharlie/lorg`

---

## Migration Order

### Step 1: Update codebase (BEFORE renaming the repo)

Do all code changes while the repo is still named `lorg-db` so everything builds and tests pass.

#### 1a. Update Go module path

- **`go.mod`** — Change `module github.com/campbellcharlie/lorg-db` to `module github.com/campbellcharlie/lorg`

#### 1b. Update all Go imports (106 files)

Every `.go` file importing `github.com/campbellcharlie/lorg-db/...` must change to `github.com/campbellcharlie/lorg/...`.

**Affected packages (non-exhaustive):**
- `apps/app/` (17+ files)
- `apps/launcher/` (10+ files)
- `apps/tools/` (5+ files)
- `cmd/lorg/`, `cmd/lorg/`, `cmd/lorg-tool/`, `cmd/lorg-chrome/`, `cmd/lorg-search/`, `cmd/grx-fuzzer/`, `cmd/grxp/`, `cmd/test/`
- `grx/fuzzer/`, `grx/rawhttp/`, `grx/templates/`
- `internal/config/`, `internal/process/`, `internal/save/`, `internal/sdk/`, `internal/types/`
- `examples/`

**Quick fix:** `find . -name '*.go' -exec sed -i '' 's|github.com/campbellcharlie/lorg-db|github.com/campbellcharlie/lorg|g' {} +`

#### 1c. Update hardcoded GitHub URLs

| File | What to change |
|------|---------------|
| `internal/updater/updater.go:15` | `releasesURL` — GitHub API releases URL |
| `apps/launcher/update.go` | Any GitHub repo references |
| `docs/process_management.md` | Documentation references |
| `docs/rawproxy.md` | Documentation references |
| `cmd/grx-fuzzer/README.md` | Documentation references |
| `cmd/lorg/README.md` | Documentation references |
| `README.md` | Root readme references |

#### 1d. Verify build & tests

```bash
go mod tidy
go build ./...
go test ./...
```

#### 1e. Commit all changes

```bash
git add -A
git commit -m "rename: update module path from lorg-db to lorg"
git push origin develop
```

---

### Step 2: Rename the GitHub repository

1. Go to **GitHub > Settings > General > Repository name**
2. Change `lorg-db` to `lorg`
3. GitHub will automatically set up a redirect from the old URL

---

### Step 3: Post-rename tasks

#### 3a. Update local git remote

```bash
git remote set-url origin git@github.com:campbellcharlie/lorg.git
```

#### 3b. Update local directory (optional but recommended)

```bash
cd ..
mv lorg-db lorg
```

Or re-clone:
```bash
git clone git@github.com:campbellcharlie/lorg.git
```

#### 3c. Update Go module proxy cache

```bash
GOPROXY=proxy.golang.org go list -m github.com/campbellcharlie/lorg@latest
```

#### 3d. Update external references

- Any CI/CD pipelines (GitHub Actions, etc.)
- Electron app config (`cmd/electron/`) if it references the repo
- Any external documentation, wikis, or links
- `install.sh` and `release.sh` — currently don't reference the repo name directly (they're fine)
- Homebrew formulae, package managers, or install scripts hosted elsewhere
- Notify users/collaborators of the new URL

---

## Impact Summary

| Area | Files affected | Risk |
|------|---------------|------|
| Go module path (`go.mod`) | 1 | **High** — breaks all imports if not done |
| Go import statements | ~106 | **High** — mechanical but must be complete |
| GitHub API URL (updater) | 1 | **High** — self-update breaks if missed |
| Documentation | ~5 | Low — cosmetic |
| Git remote | 1 (local config) | Low — GitHub redirects old URL |
| Local directory path | N/A | Low — optional rename |

## Rollback

If something goes wrong after the GitHub rename:
- GitHub repo can be renamed back in Settings
- Git redirects work both ways
- Go module path change can be reverted with another commit
