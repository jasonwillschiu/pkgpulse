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
