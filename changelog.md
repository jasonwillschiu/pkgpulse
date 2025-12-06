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
