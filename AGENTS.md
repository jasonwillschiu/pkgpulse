# AGENTS.md

AI agent guardrails for pkgpulse.

## Developer notes
- After edits, run `task check` (lint + race tests + build); it must pass with zero findings
- `task lint` fails on any golangci-lint or gopls finding (gopls exits 0, so the task checks its output)
- No CI and no prebuilt binaries: the Taskfile is the only quality gate, users `go install` or build from source


## Project Structure

```
pkgpulse/
├── main.go          # Entire CLI - image analysis logic
├── main_test.go     # Unit tests (parsers, CLI args, cache, overlay/whiteout handling)
├── go.mod           # Dependencies
├── Taskfile.yml     # Build/lint/test/release automation (task runner)
├── bin/             # Built binaries (git-ignored)
├── docs/            # Prompts: prompt-docs-writing.md, prompt-plan1.md, prompt-plan2.md
├── README.md        # Human-facing documentation
├── AGENTS.md        # This file (CLAUDE.md symlinks here)
├── changelog.md     # SemVer changelog, drives version + releases
└── LICENSE, CODE_OF_CONDUCT.md, CONTRIBUTING.md
```

## Invariants

- **Main tool**: Single-file Go CLI (`main.go`) - native package parsing + go-containerregistry
- **External dependencies**: Optional `syft` (with `--use-syft`), `gopls` + `golangci-lint` for development
- **Go version**: 1.25+ (see go.mod; uses `sync.WaitGroup.Go`, `bytes.Lines`)
- **Version**: `var version` set via `-ldflags -X main.version` (Taskfile reads changelog.md); falls back to `debug.ReadBuildInfo` (`go install`), then `dev`. Never hardcode it
- **Release tool**: Global `mdrelease` CLI; `task release` runs `task check` first. Releases are git tags only
- **Cache**: Tarball + JSON metadata in `$XDG_CACHE_HOME/pkgpulse` or `~/.cache/pkgpulse`

## Key Patterns

### Analysis (main.go)
- Per-image errors: `analyzeImage` returns `(imageResult, error)`; failures land in `imageResult.Err`, other images still report; exit 1 if any failed, 2 for usage errors
- Never `log.Fatal`/`os.Exit` inside workers; collect warnings in `imageResult.Warnings` (printed after the progress renderer stops)
- Platform: `--platform` (default `linux/amd64`) via `remote.WithPlatform`; actual platform read from image config, mismatch warns
- Progress: ANSI live redraw only when stderr is a TTY (`isTerminal`), else one plain line per stage change
- Bounded concurrency via semaphore (`--jobs`, default 5); duplicate image args deduped

### Layer walking
- `walkLayers`/`readLayer` replay layers as an overlay: whiteouts (`.wh.<name>`) and opaque dirs (`.wh..wh..opq`) remove lower-layer entries (files and subtrees) only; any non-dir entry replaces lower paths
- Tracked files only: package DBs (contents kept) and executables in `binaryDirs` (size + inspection)
- Binaries inspected inline until a package DB is seen; leftovers get a top-down second pass (`inspectPendingBinaries`)
- DB paths (priority order): APK `usr/lib/apk/db/installed`, `lib/apk/db/installed`; DEB `var/lib/dpkg/status` else `status.d/*` (missing Status = installed); RPM `usr/lib/sysimage/rpm/*` then `var/lib/rpm/*`
- dpkg: count only states with files on disk (`dpkgStatesOnDisk`), not `not-installed`/`config-files`/`half-installed`
- Binary detection: Go build info first, then BusyBox signature, then getconf glibc version

### Sizes and rows
- Sizes stored in **bytes** everywhere (`pkg.SizeBytes`, `row.Bytes`): APK `I:` bytes (`S:` fallback), RPM bytes, DEB `Installed-Size` KiB ×1024, binaries tar size
- `buildRows` merges same-name packages (sum sizes, join distinct versions) and sorts by bytes desc, name asc (deterministic)
- Parsers iterate with `bytes.Lines` (no line-length limit); never `bufio.Scanner` with default buffer

### Cache
- Key: `ref.Name()` (normalized) + platform; file name = sanitized ref + platform + 16-hex hash
- Writes are atomic (`writeFileAtomic`: temp file + rename)
- Metadata: `ref_digest` (what the tag resolved to), `compressed_bytes`, `cached_at`, `checked_at`
- Freshness: tags not verified within `cacheRecheckAfter` (1h) get `remote.Head` (GET fallback); moved tag refetches; unreachable registry uses cache with warning; digest refs never rechecked
- `cache rm` removes all platforms of a ref (also matches legacy raw refs); reports not-found with exit 1

### Output
- Single successful image: breakdown + package CSV (`package,version,installed_MB`)
- Multiple: comparison + CSV summary (`image,platform,source,compressed_MB,installed_MB,packages`) + matrix; missing cells `-`; sanitized collision-safe column names
- Auto-export CSV when >3 images succeed (not with `--json`); never overwrites (`nextFreePath`)
- `--json` to stdout (bytes + rounded MB, failures carry `error`); status messages go to stderr
- `--top N` limits text output only (comparison picks top N by max size across images)
- `Source`: `cache` or `registry`

## Rules

### Always
- Return errors explicitly; keep stdout for results and stderr for progress/status
- Sort packages by size descending for display
- Update all 3 docs (README, AGENTS, changelog) when adding features; bump README `go install @vX.Y.Z`
- Add/extend table tests in `main_test.go` for parser or overlay changes (build test images with `mutate.AppendLayers` + `tarball.LayerFromOpener`)
- Use `go-containerregistry` for image operations (registry + tarball)
- Use native parsing by default, syft only with `--use-syft` (syft mode skips the cache)
- Use `path` (not `filepath`) for tar entry paths; `filepath` for local files
- Follow XDG Base Directory spec for cache location via `getCacheDir()`

### Never
- Add dependencies without updating go.mod/go.sum
- Commit binaries or CSV exports (bin/, /pkgpulse, pkgpulse*.csv are ignored)
- Add GitHub Actions, GoReleaser, Homebrew formulas, or install scripts
- Shell to docker/podman - use `go-containerregistry` for all image ops
- Skip whiteout/opaque handling - files can be deleted in later layers
- Initialize git repo without user consent
- Hardcode cache paths or the version string
