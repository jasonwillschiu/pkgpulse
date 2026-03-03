# pkgpulse

Need help deciding which container image to use? Find out image and package sizes, then compare them side by side. Great for picking a small and capable runtime image. Answer your own question, should I use distroless, wolfi or scratch, see what's inside and how big the base image is.

## Quick Start

```bash
# Install (requires Go 1.25+)
go install github.com/jasonwillschiu/pkgpulse@v0.12.5

# Analyze a single image
pkgpulse alpine:latest

# Compare multiple images
pkgpulse cgr.dev/chainguard/wolfi-base redhat/ubi9-micro gcr.io/distroless/cc-debian12
```

## Installation

### Homebrew (macOS/Linux)

```bash
brew tap jasonwillschiu/tap
brew install pkgpulse
```

### Curl install

```bash
curl -sSL https://raw.githubusercontent.com/jasonwillschiu/pkgpulse/main/scripts/install.sh | sh
```

### Build from source

```bash
git clone https://github.com/jasonwillschiu/pkgpulse
cd pkgpulse
go mod download
go build -o pkgpulse main.go
sudo mv pkgpulse /usr/local/bin/
```

## Usage

### Analyze and compare

```bash
# Single image - see pull size, installed size, and every package
pkgpulse cgr.dev/chainguard/wolfi-base

# Multiple images - get a side-by-side comparison table
pkgpulse cgr.dev/chainguard/wolfi-base redhat/ubi9-micro gcr.io/distroless/cc-debian12
```

### Image cache

Images are cached locally as tarballs for instant repeated analysis:
```bash
pkgpulse alpine:latest            # first run fetches and caches
pkgpulse alpine:latest            # second run loads from cache (instant)
pkgpulse --no-cache alpine:latest # skip cache for a fresh pull
```

### Cache management

```bash
pkgpulse cache list               # list cached images
pkgpulse cache path               # show cache directory
pkgpulse cache rm alpine:latest   # remove specific image
pkgpulse cache clear              # clear entire cache
```

Cache location follows XDG Base Directory specification (`$XDG_CACHE_HOME/pkgpulse` or `~/.cache/pkgpulse`).

### CSV export

```bash
pkgpulse alpine:latest --csv packages.csv
```

For multi-image comparisons, `--csv` exports a summary comparison block and the full package version + size comparison table. When comparing more than 3 images, pkgpulse automatically writes `pkgpulse.csv` if `--csv` is not provided.

### Syft fallback

By default, pkgpulse uses native package database parsing (no external dependencies). To use [Syft](https://github.com/anchore/syft) instead:
```bash
pkgpulse --use-syft python:3.12
```

## Features

- **Detailed Size Metrics** - Compressed (pull) size and installed (on-disk) size
- **Package Breakdown** - Every package listed with its individual size
- **Multi-Image Comparison** - Side-by-side comparison table across images
- **Parallel Analysis** - Multiple images analyzed concurrently
- **Local Image Cache** - Tarball-based caching for instant repeated analysis
- **Live Progress** - Stage updates and download byte progress during long operations
- **CSV Export** - Export package data or full comparison tables
- **Binary Package Support** - Detects Go, Rust, and other static binaries alongside traditional packages (APK, RPM, DEB)
- **Universal Registry Support** - Works with any OCI-compliant registry

## Example Output

```
pkgpulse cgr.dev/chainguard/wolfi-base redhat/ubi9-micro gcr.io/distroless/cc-debian12
Analyzing 3 images in parallel...

[1/3] [cgr.dev/chainguard/wolfi-base] Fetching manifest...
[2/3] [redhat/ubi9-micro] Fetching manifest...
[3/3] [gcr.io/distroless/cc-debian12] Fetching manifest...

[1/3] ✓ cgr.dev/chainguard/wolfi-base
[2/3] ✓ redhat/ubi9-micro
[3/3] ✓ gcr.io/distroless/cc-debian12

================================================================================
RESULTS
================================================================================

Image: cgr.dev/chainguard/wolfi-base
Compressed size (pull): 6.62 MB
Installed size (on disk): 14.42 MB
Packages: 15

Packages by installed size (on-disk MB):
  glibc                                    2.42-r2                  5.18 MB
  libcrypto3                               3.6.0-r1                 5.18 MB
  libssl3                                  3.6.0-r1                 1.11 MB
  busybox                                  1.37.0-r50               0.73 MB
  apk-tools                                2.14.10-r8               0.51 MB
  glibc-locale-posix                       2.42-r2                  0.40 MB
  ld-linux                                 2.42-r2                  0.26 MB
  ca-certificates-bundle                   20250911-r0              0.26 MB
  libxcrypt                                4.4.38-r4                0.23 MB
  libgcc                                   15.2.0-r2                0.18 MB
  zlib                                     1.3.1-r51                0.17 MB
  wolfi-baselayout                         20230201-r24             0.12 MB
  wolfi-keys                               1-r12                    0.06 MB
  libcrypt1                                2.42-r2                  0.02 MB
  wolfi-base                               1-r7                     0.02 MB


================================================================================

Image: redhat/ubi9-micro
Compressed size (pull): 6.93 MB
Installed size (on disk): 22.69 MB
Packages: 17

Packages by installed size (on-disk MB):
  bash                                     5.1.8-9.el9              7.36 MB
  glibc                                    2.34-168.el9_6.23        6.12 MB
  tzdata                                   2025b-1.el9              1.59 MB
  coreutils-single                         8.32-39.el9              1.44 MB
  glibc-common                             2.34-168.el9_6.23        1.29 MB
  ncurses-libs                             6.2-10.20210508.el9…     1.17 MB
  libsepol                                 3.6-2.el9                0.79 MB
  setup                                    2.13.7-10.el9            0.69 MB
  pcre2                                    10.40-6.el9              0.63 MB
  libcap                                   2.48-9.el9_2             0.48 MB
  ncurses-base                             6.2-10.20210508.el9…     0.29 MB
  pcre2-syntax                             10.40-6.el9              0.22 MB
  libgcc                                   11.5.0-5.el9_5           0.22 MB
  libselinux                               3.6-3.el9                0.20 MB
  libattr                                  2.5.1-3.el9              0.07 MB
  libacl                                   2.3.1-4.el9              0.07 MB
  redhat-release                           9.6-0.1.el9              0.06 MB


================================================================================

Image: gcr.io/distroless/cc-debian12
Compressed size (pull): 10.18 MB
Installed size (on disk): 34.58 MB
Packages: 10

Packages by installed size (on-disk MB):
  libc6                                    2.36-9+deb12u13         22.59 MB
  libssl3                                  3.0.17-1~deb12u3         5.84 MB
  libstdc++6                               12.2.0-14+deb12u1        2.61 MB
  tzdata                                   2025b-0+deb12u2          2.50 MB
  libgomp1                                 12.2.0-14+deb12u1        0.34 MB
  base-files                               12.4+deb12u12            0.33 MB
  libgcc-s1                                12.2.0-14+deb12u1        0.14 MB
  gcc-12-base                              12.2.0-14+deb12u1        0.10 MB
  media-types                              10.0.0                   0.09 MB
  netbase                                  6.4                      0.04 MB


================================================================================
COMPARISON TABLE
================================================================================

Summary Comparison:
Image                                                        Compressed       Installed   Packages
-------------------------------------------------------------------------------------------------
cgr.dev/chainguard/wolfi-base                                   6.62 MB        14.42 MB         15
redhat/ubi9-micro                                               6.93 MB        22.69 MB         17
gcr.io/distroless/cc-debian12                                  10.18 MB        34.58 MB         10

Package Version & Size Comparison:
Package                                  | Image 1 Ver              MB | Image 2 Ver              MB | Image 3 Ver              MB
----------------------------------------------------------------------------------------------------------------------------------
apk-tools                                | 2.14.10-r8             0.51 | -                         - | -                         -
base-files                               | -                         - | -                         - | 12.4+deb12u12          0.33
bash                                     | -                         - | 5.1.8-9.el9            7.36 | -                         -
busybox                                  | 1.37.0-r50             0.73 | -                         - | -                         -
ca-certificates-bundle                   | 20250911-r0            0.26 | -                         - | -                         -
coreutils-single                         | -                         - | 8.32-39.el9            1.44 | -                         -
gcc-12-base                              | -                         - | -                         - | 12.2.0-14+deb12u1      0.10
glibc                                    | 2.42-r2                5.18 | 2.34-168.el9_6.23      6.12 | -                         -
glibc-common                             | -                         - | 2.34-168.el9_6.23      1.29 | -                         -
glibc-locale-posix                       | 2.42-r2                0.40 | -                         - | -                         -
ld-linux                                 | 2.42-r2                0.26 | -                         - | -                         -
libacl                                   | -                         - | 2.3.1-4.el9            0.07 | -                         -
libattr                                  | -                         - | 2.5.1-3.el9            0.07 | -                         -
libc6                                    | -                         - | -                         - | 2.36-9+deb12u13       22.59
libcap                                   | -                         - | 2.48-9.el9_2           0.48 | -                         -
libcrypt1                                | 2.42-r2                0.02 | -                         - | -                         -
libcrypto3                               | 3.6.0-r1               5.18 | -                         - | -                         -
libgcc                                   | 15.2.0-r2              0.18 | 11.5.0-5.el9_5         0.22 | -                         -
libgcc-s1                                | -                         - | -                         - | 12.2.0-14+deb12u1      0.14
libgomp1                                 | -                         - | -                         - | 12.2.0-14+deb12u1      0.34
libselinux                               | -                         - | 3.6-3.el9              0.20 | -                         -
libsepol                                 | -                         - | 3.6-2.el9              0.79 | -                         -
libssl3                                  | 3.6.0-r1               1.11 | -                         - | 3.0.17-1~deb12u3       5.84
libstdc++6                               | -                         - | -                         - | 12.2.0-14+deb12u1      2.61
libxcrypt                                | 4.4.38-r4              0.23 | -                         - | -                         -
media-types                              | -                         - | -                         - | 10.0.0                 0.09
ncurses-base                             | -                         - | 6.2-10.20210508.e…     0.29 | -                         -
ncurses-libs                             | -                         - | 6.2-10.20210508.e…     1.17 | -                         -
netbase                                  | -                         - | -                         - | 6.4                    0.04
pcre2                                    | -                         - | 10.40-6.el9            0.63 | -                         -
pcre2-syntax                             | -                         - | 10.40-6.el9            0.22 | -                         -
redhat-release                           | -                         - | 9.6-0.1.el9            0.06 | -                         -
setup                                    | -                         - | 2.13.7-10.el9          0.69 | -                         -
tzdata                                   | -                         - | 2025b-1.el9            1.59 | 2025b-0+deb12u2        2.50
wolfi-base                               | 1-r7                   0.02 | -                         - | -                         -
wolfi-baselayout                         | 20230201-r24           0.12 | -                         - | -                         -
wolfi-keys                               | 1-r12                  0.06 | -                         - | -                         -
zlib                                     | 1.3.1-r51              0.17 | -                         - | -                         -

Image 1: cgr.dev/chainguard/wolfi-base
Image 2: redhat/ubi9-micro
Image 3: gcr.io/distroless/cc-debian12
```

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

1. Checks local tarball cache (or fetches from registry if not cached)
2. Reads image layers as tar archives to extract package databases
3. Natively parses APK, DEB, RPM databases and detects Go binaries
4. Calculates compressed and installed sizes
5. Presents results in formatted tables (or CSV)

Supports APK (Alpine), RPM (Red Hat/Fedora/CentOS), DEB (Debian/Ubuntu), Go binaries, and all Syft-supported types via `--use-syft`.

## Development

### Prerequisites

- **Go 1.25+**
- [Task](https://taskfile.dev) - `brew install go-task`
- [gopls](https://pkg.go.dev/golang.org/x/tools/gopls) - `go install golang.org/x/tools/gopls@latest`
- [mdrelease](https://github.com/jasonwillschiu/mdrelease) - `go install github.com/jasonwillschiu/mdrelease@latest`

### Available Tasks

```bash
task --list        # view all tasks
task lint          # format, vet, and lint
task test          # run tests
task build         # build CLI binary
task gopls-check   # modernization diagnostics
task clean         # clean build artifacts
```

### Code Quality

- `go fmt` - standard formatting
- `go vet` - built-in static analyzer
- `golangci-lint` - comprehensive linting
- `gopls` - language server diagnostics

Run `task lint` to execute all checks.

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
