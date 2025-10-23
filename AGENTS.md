# AGENTS.md

AI agent guardrails for pkgpulse-com.

## Project Structure

```
pkgpulse-com/
├── main.go                  # Main CLI tool - image analysis logic
├── go.mod                   # Main project dependencies
├── tools/
│   └── release-tool/        # Release automation (separate Go module)
│       ├── main.go          # version & release commands
│       └── go.mod           # Isolated dependencies
├── bin/                     # Built binaries (git-ignored)
├── docs/
│   └── prompt-docs-writer.md
├── README.md                # Human-facing documentation
├── AGENTS.md                # This file - AI guardrails
└── changelog.md             # SemVer changelog
```

## Invariants

- **Main tool**: Single-file Go CLI (`main.go`) - wraps Syft and go-containerregistry
- **External dependencies**: Requires `syft` binary installed on system
- **Go version**: 1.21+ (see go.mod)
- **Release tool**: Separate Go module in `tools/release-tool/` for portability
- **Documentation**: 3 docs maintained: README.md, AGENTS.md, changelog.md
- **No git repo yet**: Project is uninitialized; first commit pending

## Key Patterns

### Main CLI (main.go)
- Uses `github.com/google/go-containerregistry` for manifest fetching
- Shells out to `syft` for SBOM generation (`syft-json` format)
- Parallel image analysis via goroutines and channels
- Ordered progress output via buffered progress channel
- Package size logic handles APK (bytes), RPM (bytes), DEB (KB) formats
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

### Never
- Add dependencies without updating go.mod/go.sum
- Mix release-tool and main CLI concerns
- Commit binaries to git (bin/ is ignored)
- Parse Syft text output - always use `-o syft-json`
- Assume package size format - handle APK/RPM/DEB differences explicitly
- Initialize git repo without user consent

## Checklists

### Add New Feature
- [ ] Update main.go or tools/release-tool/ as appropriate
- [ ] Test with real container images (wolfi-base, ubi9-micro, distroless)
- [ ] Update README.md usage examples if CLI changes
- [ ] Update AGENTS.md if patterns or invariants change
- [ ] Bump changelog.md (major.minor.patch per SemVer rules)

### Update Documentation (via prompt-docs-writer.md)
- [ ] Read relevant files & check git status/diff
- [ ] Assess: Is it major.minor.patch?
- [ ] Update changelog.md with new version entry (prepend at top)
- [ ] Update README.md if usage/features changed
- [ ] Update AGENTS.md if architecture/patterns changed
- [ ] Ensure no duplicate/conflicting info across docs

### Release Workflow
- [ ] Update changelog.md with next version
- [ ] Run `./bin/release-tool version` to verify parsing
- [ ] Run `./bin/release-tool release` (tags, commits, pushes)
- [ ] Verify tag on GitHub

## Ignore Patterns

- `bin/` - built binaries
- `go.sum` - lockfile (inspect but don't hand-edit)
- `.gitignore`, `.gitattributes` - VCS config
- `*.csv` - generated output files
