# AGENTS.md

AI agent guardrails for pkgpulse.

## Developer notes
- After edits, run `task build` and `task lint` and fix any errors and warnings
- Use `task gopls-check` to catch modernization warnings (like CutPrefix) not in golangci-lint


## Project Structure

```
pkgpulse/
├── main.go                  # Main CLI tool - image analysis logic
├── go.mod                   # Main project dependencies
├── Taskfile.yml             # Build automation (task runner)
├── .goreleaser.yaml         # GoReleaser config for cross-platform releases
├── tools/
│   └── release-tool/        # Release automation (separate Go module)
│       ├── main.go          # version & release commands
│       └── go.mod           # Isolated dependencies
├── scripts/
│   ├── install.sh           # Curl install script for quick installation
│   └── pkgpulse.rb          # Homebrew formula template
├── bin/                     # Built binaries (git-ignored)
├── docs/
│   └── prompt-docs-writer.md
├── README.md                # Human-facing documentation
├── AGENTS.md                # This file - AI guardrails
├── changelog.md             # SemVer changelog
├── LICENSE                  # MIT License
├── CODE_OF_CONDUCT.md       # Community code of conduct
└── CONTRIBUTING.md          # Contribution guidelines
```

## Invariants

- **Main tool**: Single-file Go CLI (`main.go`) - native package parsing + go-containerregistry
- **External dependencies**: Optional `syft` (with `--use-syft` flag), `gopls` for development
- **Go version**: 1.25+ (see go.mod)
- **Build system**: Taskfile for automation (task runner)
- **Release tool**: Separate Go module in `tools/release-tool/` for portability
- **Documentation**: 3 docs maintained: README.md, AGENTS.md, changelog.md
- **Community files**: MIT LICENSE, CODE_OF_CONDUCT, CONTRIBUTING

## Key Patterns

### Main CLI (main.go)
- Uses `github.com/google/go-containerregistry` for image operations (daemon + registry)
- **Default mode**: Native package database parsing (no external dependencies)
- **Syft fallback** (`--use-syft`): Shells out to syft for SBOM generation
- Parallel image analysis via goroutines with bounded concurrency (semaphore pattern)
- Ordered progress output via buffered progress channel
- **Auto-detection**: Checks local Docker daemon first, falls back to remote registry
- **Force remote** (`--remote`): Skip daemon check, always use registry
- Native parsing:
  - **APK**: Parses `lib/apk/db/installed` text format directly
  - **DEB**: Parses `var/lib/dpkg/status` text format directly
  - **RPM**: Uses `github.com/knqyf263/go-rpmdb` for SQLite/BDB/NDB databases
  - **Go binaries**: Uses `debug/buildinfo` for version/module detection
- Reads image layers as tar archives to extract package databases
- Package size logic:
  - **Traditional packages**: APK (bytes), RPM (bytes), DEB (KB)
  - **Go binaries**: File size from tar headers
- Single responsibility: size comparison, not vulnerability scanning

### Release & Distribution
- **GoReleaser**: Cross-platform builds (linux/darwin, amd64/arm64)
- **Homebrew**: Formula template in `scripts/pkgpulse.rb`, auto-updated via GoReleaser
- **Curl install**: One-liner installation via `scripts/install.sh`

### Release Tool (tools/release-tool/)
- Parses changelog.md for version & summary
- Supports full SemVer (pre-release, build metadata)
- Commands: `version` (print latest), `release` (tag & push)
- Git workflow: add → commit → tag → push
- Standalone Go module - can be extracted to separate repo later

## File Locations

- **Main logic**: `main.go` (all-in-one CLI)
- **Tool scripts**: `tools/release-tool/` (versioning automation)
- **Built binaries**: `bin/` (git-ignored, built on demand)
- **Documentation**: `README.md` (users), `AGENTS.md` (agents), `changelog.md` (versions)

## Rules

### Always
- Use structured error handling (`check()`, explicit returns)
- Sort packages by size descending for display
- Maintain separate go.mod for tools/release-tool/
- Update all 3 docs (README, AGENTS, changelog) when adding features
- Use `go-containerregistry` for image operations (daemon + registry)
- Use native parsing by default, syft only with `--use-syft` flag
- Parse package databases directly from tar layers
- Handle whiteout files (deletion markers) in overlay filesystems
- Detect Go binaries using `debug/buildinfo`

### Never
- Add dependencies without updating go.mod/go.sum
- Mix release-tool and main CLI concerns
- Commit binaries to git (bin/ is ignored)
- Shell to docker/podman - use `go-containerregistry/pkg/v1/daemon` instead
- Assume package size format - handle APK/RPM/DEB/binary differences explicitly
- Skip whiteout handling - files can be deleted in later layers
- Initialize git repo without user consent
