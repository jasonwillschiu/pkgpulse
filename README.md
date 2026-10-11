# pkgpulse

Need help deciding which container image to use? Find out image and package sizes, then compare them side by side. Great for picking a small and capable runtime image. Answer your own question, should I use distroless, wolfi or scratch, see what's inside and how big the base image is.

## Quick Start

```bash
# Install (requires Go 1.25+)
go install github.com/jasonwillschiu/pkgpulse@v0.13.0

# Analyze a single image
pkgpulse alpine:latest

# Compare multiple images
pkgpulse cgr.dev/chainguard/wolfi-base redhat/ubi9-micro gcr.io/distroless/cc-debian12
```

## Installation

pkgpulse is distributed as source only; there are no prebuilt binaries. You need [Go 1.25+](https://go.dev/dl/).

### go install

```bash
go install github.com/jasonwillschiu/pkgpulse@v0.13.0
```

The binary lands in `$(go env GOPATH)/bin` (or `$GOBIN`). Make sure that directory is on your `PATH`.

### Build from source

```bash
git clone https://github.com/jasonwillschiu/pkgpulse
cd pkgpulse
task build             # builds bin/pkgpulse with the version from changelog.md
# or, without Task:
go build -o pkgpulse .
```

## Usage

### Analyze and compare

```bash
# Single image - see pull size, installed size, and every package
pkgpulse cgr.dev/chainguard/wolfi-base

# Multiple images - get a side-by-side comparison table
pkgpulse cgr.dev/chainguard/wolfi-base redhat/ubi9-micro gcr.io/distroless/cc-debian12

# Only show the 10 largest packages
pkgpulse --top 10 debian:12 ubuntu:24.04
```

### Flags

| Flag | Description |
|------|-------------|
| `--platform <os/arch[/variant]>` | Platform to analyze for multi-arch images (default `linux/amd64`) |
| `--no-cache` | Bypass the cache and always fetch from the registry |
| `--use-syft` | Use [Syft](https://github.com/anchore/syft) instead of native parsing |
| `--csv <file>` | Export package data to CSV |
| `--json` | Print results as JSON to stdout |
| `--top <n>` | Show only the `n` largest packages (text output only) |
| `--jobs <n>` | Number of images analyzed concurrently (default 5) |
| `--version`, `-v` | Show version |
| `--help`, `-h` | Show help |

Flags can go before or after image references, as `--flag value` or `--flag=value`. Unknown flags are an error.

Exit status is `0` when every image was analyzed, `1` when one or more images failed (the others are still reported), and `2` for usage errors.

### Platforms

Multi-arch images are analyzed as `linux/amd64` unless you choose another platform:

```bash
pkgpulse --platform linux/arm64 alpine:latest
```

If an image doesn't publish the requested platform, pkgpulse warns and reports the platform it actually analyzed.

### Image cache

Images are cached locally as tarballs, one per image and platform:
```bash
pkgpulse alpine:latest            # first run fetches and caches
pkgpulse alpine:latest            # second run loads from cache
pkgpulse --no-cache alpine:latest # skip cache for a fresh pull
```

Tags like `:latest` can move. When a cached tag hasn't been verified for an hour, pkgpulse makes a cheap `HEAD` request to the registry and refetches the image if the tag now points somewhere else. If the registry is unreachable, the cached copy is used with a warning. Digest references (`image@sha256:...`) never change, so they're always served from cache.

References are normalized, so `alpine`, `alpine:latest`, and `docker.io/library/alpine:latest` share one cache entry.

### Cache management

```bash
pkgpulse cache list               # list cached images
pkgpulse cache path               # show cache directory
pkgpulse cache rm alpine:latest   # remove an image (all platforms)
pkgpulse cache clear              # clear entire cache
```

Cache location follows XDG Base Directory specification (`$XDG_CACHE_HOME/pkgpulse` or `~/.cache/pkgpulse`).

### CSV export

```bash
pkgpulse alpine:latest --csv packages.csv
```

A single image exports `package,version,installed_MB` rows. For multi-image comparisons, `--csv` exports a summary block (`image,platform,source,compressed_MB,installed_MB,packages`) and the full package version + size comparison table. Missing packages are written as `-`.

When comparing more than 3 images without `--csv` (and without `--json`), pkgpulse automatically writes `pkgpulse.csv`. It never overwrites an existing export; it picks `pkgpulse-2.csv`, `pkgpulse-3.csv`, and so on instead.

### JSON output

```bash
pkgpulse alpine:latest debian:12 --json > sizes.json
```

JSON goes to stdout and progress goes to stderr, so the output is safe to pipe:

```json
{
  "pkgpulse_version": "0.13.0",
  "images": [
    {
      "image": "alpine:latest",
      "platform": "linux/amd64",
      "source": "registry",
      "compressed_bytes": 3850349,
      "installed_bytes": 8399794,
      "package_count": 16,
      "packages": [
        { "name": "libcrypto3", "version": "3.5.8-r0", "installed_bytes": 5217878, "installed_mb": 4.98 },
        ...
      ]
    }
  ]
}
```

Images that fail are included with an `error` field.

### Syft fallback

By default, pkgpulse uses native package database parsing (no external dependencies). To use [Syft](https://github.com/anchore/syft) instead:
```bash
pkgpulse --use-syft python:3.12
```

## Features

- **Detailed Size Metrics** - Compressed (pull) size and installed (on-disk) size
- **Package Breakdown** - Every package listed with its individual size
- **Multi-Image Comparison** - Side-by-side comparison table across images
- **Platform Selection** - Analyze any platform of a multi-arch image with `--platform`
- **Parallel Analysis** - Multiple images analyzed concurrently; one failing image doesn't stop the rest
- **Local Image Cache** - Tarball-based caching, with automatic refresh when a tag moves
- **Live Progress** - Stage updates and download progress on a terminal, plain log lines in CI or pipes
- **CSV and JSON Export** - Export package data, full comparison tables, or machine-readable JSON
- **Binary Package Support** - Detects Go binaries and BusyBox in images without a package database
- **Universal Registry Support** - Works with any OCI-compliant registry

## Example Output

```
$ pkgpulse cgr.dev/chainguard/wolfi-base redhat/ubi9-micro gcr.io/distroless/cc-debian12 --top 10
Analyzing 3 images in parallel...
[1/3] ✓ cgr.dev/chainguard/wolfi-base
[2/3] ✓ redhat/ubi9-micro
[3/3] ✓ gcr.io/distroless/cc-debian12

================================================================================
COMPARISON
================================================================================

Summary Comparison:
Image                                        Platform         Source   Compressed    Installed  Packages
--------------------------------------------------------------------------------------------------------
cgr.dev/chainguard/wolfi-base                linux/amd64       cache      7.78 MB     18.18 MB        19
redhat/ubi9-micro                            linux/amd64       cache      6.92 MB     21.98 MB        18
gcr.io/distroless/cc-debian12                linux/amd64       cache      8.76 MB     25.10 MB        11

Package Version & Size Comparison (top 10 by size):
Package                                  | Image 1 Ver              MB | Image 2 Ver              MB | Image 3 Ver              MB
----------------------------------------------------------------------------------------------------------------------------------
libc6                                    | -                         - | -                         - | 2.36-9+deb12u14       12.70
bash                                     | -                         - | 5.1.8-9.el9            7.38 | -                         -
glibc-2.44                               | 2.44-r8                6.29 | -                         - | -                         -
glibc                                    | -                         - | 2.34-275.el9_8         6.15 | -                         -
libssl3                                  | -                         - | -                         - | 3.0.20-1~deb12u2       5.89
openssl-4.0-libcrypto                    | 4.0.3-r6               5.84 | -                         - | -                         -
libstdc++6                               | -                         - | -                         - | 12.2.0-14+deb12u1      2.62
tzdata                                   | -                         - | 2026c-1.el9_8          1.83 | 2026b-0+deb12u1        2.51
coreutils-single                         | -                         - | 8.32-41.el9_8.1        1.34 | -                         -
openssl-4.0-libssl                       | 4.0.3-r6               1.18 | -                         - | -                         -

Image 1: cgr.dev/chainguard/wolfi-base
Image 2: redhat/ubi9-micro
Image 3: gcr.io/distroless/cc-debian12
```

Without `--top`, the comparison lists every package alphabetically. A single image shows a per-package breakdown instead:

```
$ pkgpulse cgr.dev/chainguard/wolfi-base --top 8
Image: cgr.dev/chainguard/wolfi-base
Platform: linux/amd64
Source: cache
Compressed size (pull): 7.78 MB
Installed size (on disk): 18.18 MB
Packages: 19

Packages by installed size (on-disk MB):
  glibc-2.44                               2.44-r8                  6.29 MB
  openssl-4.0-libcrypto                    4.0.3-r6                 5.84 MB
  openssl-4.0-libssl                       4.0.3-r6                 1.18 MB
  libbrotlienc1                            1.2.0-r5                 0.89 MB
  openssl-provider-legacy-allowed          4.0.3-r1                 0.79 MB
  busybox                                  1.38.0-r3                0.71 MB
  apk-tools                                2.14.10-r17              0.44 MB
  glibc-2.44-locale-posix                  2.44-r8                  0.40 MB
  ... 11 more packages (showing top 8)
```

`Source` is `cache` when the image was loaded from the local cache and `registry` when it was fetched during this run.

## Supported Registries

Works with **any OCI-compliant container registry**, including Docker Hub, GCR, GHCR, Quay.io, Amazon ECR, Azure ACR, and more.

```bash
# Docker Hub (default registry)
pkgpulse nginx:alpine

# Google registries
pkgpulse gcr.io/distroless/base

# AWS ECR Public
pkgpulse public.ecr.aws/nginx/nginx:alpine

# Compare across registries
pkgpulse alpine:latest gcr.io/distroless/static:latest quay.io/prometheus/alertmanager:latest
```

For private registries, authenticate with `docker login <registry-url>`. The tool uses your local Docker/Podman credential store.

<details>
<summary>Full registry compatibility list</summary>

| Registry | Example Image | Notes |
|----------|---------------|-------|
| **Docker Hub** | `alpine:latest` | Default registry, prefix optional |
| **Google Container Registry (GCR)** | `gcr.io/distroless/static-debian12:latest` | Google Cloud Platform |
| **GitHub Container Registry (GHCR)** | `ghcr.io/distroless/static:latest` | GitHub Packages |
| **Quay.io** | `quay.io/prometheus/node-exporter:latest` | Red Hat's container registry |
| **Kubernetes Registry** | `registry.k8s.io/pause:3.9` | Official Kubernetes images |
| **Chainguard Registry (CGR)** | `cgr.dev/chainguard/wolfi-base` | Minimal, secure images |
| **Amazon ECR Public** | `public.ecr.aws/amazonlinux/amazonlinux:latest` | AWS Public Gallery |
| **Amazon ECR Private** | `<account-id>.dkr.ecr.<region>.amazonaws.com/my-image:tag` | Requires auth |
| **Azure Container Registry** | `<registry-name>.azurecr.io/my-image:tag` | Requires auth |
| **Google Artifact Registry** | `<region>-docker.pkg.dev/<project>/<repo>/image:tag` | Newer than GCR |
| **GitLab Container Registry** | `registry.gitlab.com/<namespace>/<project>:tag` | GitLab CI/CD |
| **JFrog Artifactory** | `<server>.jfrog.io/<repo>/image:tag` | Enterprise |
| **Harbor** | `<harbor-host>/library/image:tag` | Self-hosted |
| **Nexus Repository** | `<nexus-host>:<port>/repository/<repo>/image:tag` | Sonatype Nexus |
| **DigitalOcean Registry** | `registry.digitalocean.com/<namespace>/image:tag` | Requires auth |
| **IBM Cloud Registry** | `<region>.icr.io/<namespace>/image:tag` | Requires auth |
| **Oracle Cloud (OCIR)** | `<region>.ocir.io/<tenancy>/image:tag` | Requires auth |
| **Alibaba Cloud Registry** | `registry.<region>.aliyuncs.com/<namespace>/image:tag` | Requires auth |
| **Tencent Cloud Registry** | `ccr.ccs.tencentyun.com/<namespace>/image:tag` | Requires auth |

</details>

## How It Works

1. Checks the local tarball cache (refreshing moved tags), or fetches the requested platform from the registry
2. Replays image layers as an overlay filesystem, honoring whiteouts and opaque directories, to find the final package databases
3. Natively parses APK, DEB, and RPM databases; images without one get their executables inspected (Go build info, BusyBox)
4. Calculates compressed and installed sizes, merging same-name packages (e.g. multi-arch debs)
5. Presents results as text tables, CSV, or JSON

Package databases are found at both classic and usr-merged locations:

| Format | Paths |
|--------|-------|
| APK (Alpine, Wolfi) | `lib/apk/db/installed`, `usr/lib/apk/db/installed` |
| DEB (Debian, Ubuntu) | `var/lib/dpkg/status`, `var/lib/dpkg/status.d/*` (distroless) |
| RPM (RHEL, Fedora, openSUSE) | `var/lib/rpm/*`, `usr/lib/sysimage/rpm/*` (SQLite, BerkeleyDB, NDB) |

All Syft-supported types are available via `--use-syft`.

## Development

### Prerequisites

- **Go 1.25+**
- [Task](https://taskfile.dev) - `brew install go-task`
- [gopls](https://pkg.go.dev/golang.org/x/tools/gopls) - `go install golang.org/x/tools/gopls@latest`
- [mdrelease](https://github.com/jasonwillschiu/mdrelease) - `go install github.com/jasonwillschiu/mdrelease@latest`

### Available Tasks

```bash
task --list        # view all tasks
task check         # lint + test + build: run before every commit or release
task lint          # format, vet, golangci-lint, gopls (fails on any finding)
task test          # run tests with the race detector
task build         # build bin/pkgpulse with the changelog version embedded
task install       # go install with the changelog version embedded
task deps          # upgrade direct dependencies and tidy
task clean         # clean build artifacts
task release       # run checks, then tag and push via mdrelease
```

There is no CI; `task check` is the quality gate and `task release` runs it before tagging.

### Code Quality

- `go fmt` - standard formatting
- `go vet` - built-in static analyzer
- `golangci-lint` - comprehensive linting
- `gopls` - language server diagnostics, including the modernize analyzer
- `go test -race` - unit tests for parsers, CLI parsing, cache, and layer/whiteout handling

### Releasing

Releases are git tags only; users install with `go install ...@vX.Y.Z`. Add a new entry at the top of `changelog.md`, update the version in the README install commands, then run `task release`.

### Dependencies

Managed via `go.mod`:
- `github.com/google/go-containerregistry` - container registry client
- `github.com/glebarez/go-sqlite` - SQLite driver for RPM databases
- `github.com/knqyf263/go-rpmdb` - native RPM database parser

Optional: [Syft](https://github.com/anchore/syft) (only needed with `--use-syft` flag)

## License

MIT License - see [LICENSE](LICENSE) file for details.

## Contributing

Contributions are welcome! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

Please also review our [Code of Conduct](CODE_OF_CONDUCT.md).
