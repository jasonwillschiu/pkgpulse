# AGENTS.md

AI agent guardrails for pkgpulse-com.

## Developer notes
- After edits, run `task build` and `task lint` and fix any errors and warnings
- Use `task gopls-check` to catch modernization warnings (like CutPrefix) not in golangci-lint


## Project Structure

```
pkgpulse-com/
├── main.go                  # Main CLI tool - image analysis logic
├── go.mod                   # Main project dependencies
├── Taskfile.yml             # Build automation (task runner)
├── tools/
│   └── release-tool/        # Release automation (separate Go module)
│       ├── main.go          # version & release commands
│       └── go.mod           # Isolated dependencies
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

- **Main tool**: Single-file Go CLI (`main.go`) - wraps Syft and go-containerregistry
- **External dependencies**: Requires `syft` and `gopls` binaries installed on system
- **Go version**: 1.21+ (see go.mod)
- **Build system**: Taskfile for automation (task runner)
- **Release tool**: Separate Go module in `tools/release-tool/` for portability
- **Documentation**: 3 docs maintained: README.md, AGENTS.md, changelog.md
- **Community files**: MIT LICENSE, CODE_OF_CONDUCT, CONTRIBUTING

## Key Patterns

### Main CLI (main.go)
- Uses `github.com/google/go-containerregistry` for manifest fetching from any OCI registry
- Shells out to `syft` for SBOM generation (`syft-json` format)
- Parallel image analysis via goroutines and channels
- Ordered progress output via buffered progress channel
- Package size logic:
  - **Traditional packages**: APK (bytes), RPM (bytes), DEB (KB)
  - **Binary packages**: Uses artifact relationships + files array to get file sizes
  - Handles static binaries (Go, Rust, busybox, redis, postgres, etc.)
- Single responsibility: size comparison, not vulnerability scanning

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
- Use `go-containerregistry` for registry operations (never shell to docker)
- Shell to `syft` only - parse JSON output, never scrape text
- Parse Syft's `artifactRelationships` and `files` arrays for binary package sizes

### Never
- Add dependencies without updating go.mod/go.sum
- Mix release-tool and main CLI concerns
- Commit binaries to git (bin/ is ignored)
- Parse Syft text output - always use `-o syft-json`
- Assume package size format - handle APK/RPM/DEB/binary differences explicitly
- Skip binary packages - they must be detected via artifact relationships
- Initialize git repo without user consent
