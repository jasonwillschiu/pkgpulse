# 0.13.0 - Add: Platform selection, JSON output, cache freshness, and parser fixes
- Added `--platform` (default `linux/amd64`), `--json`, `--top N`, and `--jobs N` flags
- Cached tags are re-verified against the registry (cheap HEAD, at most hourly) and refetched when they move; digest references are never rechecked
- Cache keys are normalized (`alpine` = `docker.io/library/alpine:latest`) and include the platform; cache writes are atomic. Existing cache entries are not reused and will be refetched once
- `cache rm` removes all platforms of an image and reports when nothing was cached; `cache list` is sorted and shows the platform
- One failing image no longer aborts the run: other images are still reported, failures are listed, and the exit code is 1 (2 for usage errors)
- Fixed layer whiteout handling: directory whiteouts, opaque directories, and paths replaced by symlinks now remove lower-layer package databases and binaries
- Fixed Wolfi/Chainguard images (usr-merged `usr/lib/apk/db/installed`) and Fedora 36+/openSUSE (`usr/lib/sysimage/rpm`) reporting binaries instead of packages
- Fixed dpkg counting `not-installed`, `config-files`, and `half-installed` packages
- Fixed silent truncation of package databases with lines over 64 KiB
- Fixed compressed size showing N/A for cached images
- Fixed `--no-cache` downloading binary-only images twice
- Fixed Go binaries that mention BusyBox being reported as busybox
- Sizes are tracked in bytes so sub-KB packages are no longer dropped; same-name packages (multi-arch debs, multiple RPM versions) are merged so totals match the tables
- Output order is deterministic (size, then name); progress uses plain lines when stderr is not a terminal
- Stricter argument parsing: unknown flags and missing flag values are errors; duplicate images are analyzed once
- Auto-exported CSV never overwrites an existing file (`pkgpulse-2.csv`, ...); comparison CSV summary gains a `platform` column
- Version is now embedded at build time (Taskfile reads changelog.md) or from `go install` build info instead of a hardcoded constant
- Upgraded go-containerregistry to v0.22.1 and go-sqlite to v1.23.0
- Added unit tests (parsers, CLI, cache, overlay semantics) and `task check`/`task install`/`task deps`; `task lint` now fails on findings and `task release` runs all checks first
- Removed GoReleaser, Homebrew formula, and curl install script: pkgpulse is now distributed as source only (`go install`)
- Stopped tracking `pkgpulse.csv` and `.DS_Store`

# 0.12.5 - Update: Streamline README
- Rewrote README for clarity: added Quick Start, condensed sections
- Moved registry list into collapsible details block
- Reordered installation methods (Homebrew first)

# 0.12.4 - Update: Docs prompt and version sync
- Updated docs writer prompt with explicit versioning guidance
- Synced README `go install` example to the current release version
- Synced CLI `--version` output with changelog version

# 0.12.3 - Update: Readme installation v0.12.3
- Update version to include v, which was needed

# 0.12.2 - Update: Readme installation 0.12.2
- Update README version for installation 

# 0.12.1 - Update: Migrate release automation to global mdrelease
- Replaced Taskfile release/version commands to use global `mdrelease`
- Removed local `tools/release-tool` module in favor of shared cross-project tooling
- Updated README, AGENTS, and CONTRIBUTING docs to reflect the new release workflow

# 0.12.0 - Add: Live updates for each image comparison
- Added internal single-line live progress renderer for image analysis
- Added staged progress updates across cache load/save/reload and native parsing
- Added internal registry download byte tracking (`downloaded / estimated total`) with live throughput updates

# 0.11.0 - Add: Full comparison CSV export and automatic CSV for large comparisons
- `--csv` now exports the full comparison table for multi-image runs (summary block + package matrix)
- Comparison CSV columns use sanitized image-reference names with collision-safe hash suffixes
- Missing package entries in comparison CSV are emitted as `-`
- Comparing more than 3 images now auto-exports `pkgpulse.csv` when `--csv` is not provided

# 0.10.3 - Fix: Treat status.d entries as installed when Status is missing
- Parse dpkg status.d fragments that omit the Status field (distroless-style images)

# 0.10.2 - Add: Native parsing of dpkg status.d fragments
- Support split dpkg metadata in `var/lib/dpkg/status.d/*` (distroless-style images)

# 0.10.1 - Fix: Version constant mismatch
- Fixed version constant to match git tag (was showing 0.9.0)

# 0.10.0 - Add: Local image cache and dependency cleanup
- Local tarball-based image cache for faster repeated analysis
- New cache subcommand: list, clear, rm, path operations
- Breaking: Replaced --remote flag with --no-cache (inverted logic)
- Removed Docker daemon dependency (replaced with tarball cache)
- Dependency cleanup: removed unused OpenTelemetry, Docker SDK packages

# 0.9.0 - Add: Native package parsing and daemon support
- Native parsing of APK, DEB, and RPM databases (no syft dependency by default)
- Auto-detection of local Docker daemon (falls back to remote registry)
- Native Go binary detection using debug/buildinfo
- Breaking: Replaced --thorough with --use-syft, --local with --remote flags
- New dependencies: go-sqlite, go-rpmdb, docker SDK

# 0.7.0 - Update: Performance defaults and local source support
- Default mode is now fast (squashed scope), replaced --fast with --thorough/-t
- Added --local flag to use docker/podman daemon (skips registry pull)
- Added cataloger filtering (apk,deb,rpm,binary) to skip slow language scans
- Parallelized manifest fetch and syft scan within each image
- Uses WaitGroup.Go for modern goroutine patterns (Go 1.25+)

# 0.6.0 - Add: Performance optimizations and installation improvements
- Added --fast/-f flag for faster analysis (uses squashed scope, 2-3x faster)
- Added worker pool with concurrency limit (max 5 parallel analyses)
- Added GoReleaser configuration for automated cross-platform releases
- Added curl install script for one-liner installation
- Added Homebrew formula template for brew tap support
- Improved progress output to show scan mode (all-layers vs squashed)

# 0.5.0 - Add: CLI flags and project rename to pkgpulse
- Added --version/-v and --help/-h flags
- Renamed project from pkgpulse-com to pkgpulse
- Updated module path to github.com/jasonwillschiu/pkgpulse
- Comprehensive help text with usage examples
- All documentation updated with new binary name

# 0.4.0 - Add: go install support and GitHub module path
- Updated go.mod to use GitHub module path
- Added go install installation method (recommended)
- Users can now update with go install @latest
- Updated README with two installation options
- Added GitHub repository URL references

# 0.3.0 - Add: Build automation and community files
- Taskfile integration for build, lint, test automation
- gopls integration for modernization checks (CutPrefix patterns)
- Community files: MIT LICENSE, CODE_OF_CONDUCT, CONTRIBUTING
- Code modernization: strings.CutPrefix in release-tool
- Enhanced lint workflow with golangci-lint + gopls

# 0.2.0 - Add: Binary package size detection
- Added support for binary package size reporting
- Parses Syft artifact relationships and files array
- Detects static binaries (Go, Rust, busybox, redis, postgres)
- Updated README with registry support documentation
- Fixed images showing 0 MB when only containing binaries

# 0.1.0 - Initial commit
- Container image size analyzer using Syft SBOM
- Parallel multi-image comparison with breakdown tables
- CSV export for package size data
- Go release-tool for changelog-based versioning
- Comprehensive README and AGENTS documentation
