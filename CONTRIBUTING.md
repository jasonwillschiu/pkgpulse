# Contributing to pkgpulse

Contributions are welcome via pull requests.

## How to Contribute

### Fork & Pull Request Workflow

1. Fork this repository to your GitHub account
2. Clone your fork locally
3. Create a branch for your feature or fix: `git checkout -b my-feature`
4. Make your changes with clear, descriptive commits
5. Push to your fork: `git push origin my-feature`
6. Open a pull request from your fork to this repo's `main` branch

### Before Submitting a Pull Request

Run the full quality gate and ensure it passes (there is no CI, so this is the check):

```bash
task check   # lint (fmt, vet, golangci-lint, gopls) + race-enabled tests + build
```

Fix all errors and warnings before submitting. Add or extend tests in `main_test.go` for behavior changes, and try your change against real images.

Update documentation as needed:
- `README.md` for user-facing changes
- `AGENTS.md` for architectural or coding guideline changes
- `changelog.md` for version-tracked changes

### Pull Request Review Process

All pull requests will be reviewed for:
- Alignment with project goals (minimal CLI tool, single-file main.go)
- Code quality and consistency with existing patterns
- Functionality and correctness
- Test coverage and passing builds

Reviews may include questions or requests for changes. Pull requests will be merged once approved.

### What Makes a Good Pull Request

- **Focused scope**: Addresses one concern or feature
- **Clear purpose**: Includes description of what and why
- **Clean implementation**: Follows existing code patterns
- **Passing tests**: All checks pass successfully
- **No breaking changes** without prior discussion

## Project Structure

```
pkgpulse/
├── main.go              # Main CLI - keep it simple & single-file
├── main_test.go         # Unit tests
├── Taskfile.yml         # Build, lint, test, and release shortcuts
├── README.md            # User docs
├── AGENTS.md            # AI/dev guidelines
└── changelog.md         # Version history
```

## Coding Guidelines

- **Main tool stays in `main.go`** - single-file CLI design
- **Use `go-containerregistry`** for registry operations (never shell to docker)
- **Native parsing first** - package databases are parsed directly from image layers; `syft` is only an opt-in fallback (`--use-syft`, parsed from `-o syft-json`)
- **Structured errors**: Return errors explicitly; never exit from inside per-image workers
- **Sort output**: Packages by size descending
- **Follow existing patterns**: Check how similar features are implemented

## Types of Contributions

### Bug Fixes
Open an issue describing the bug before submitting a fix. Reference the issue number in your pull request.

### New Features
Discuss new features in an issue before implementation. Ensure the feature aligns with project scope (size analysis, not vulnerability scanning). Keep the tool focused and simple.

### Documentation
Documentation improvements including typo fixes, clarity enhancements, and additional examples are welcome. Issues are not required for documentation-only changes.

### Testing
Test coverage improvements and additional test cases are encouraged. Layer and whiteout behavior can be tested without a registry by building in-memory images (see `testImage` in `main_test.go`).

### Distribution
pkgpulse is distributed as source only (`go install` or build from source). Please don't add prebuilt-binary tooling such as GoReleaser, Homebrew formulas, install scripts, or CI workflows.

## Questions

Open an issue for questions or clarifications. Work-in-progress feedback can be requested in draft pull requests.

## Code of Conduct

See [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) for community guidelines.
