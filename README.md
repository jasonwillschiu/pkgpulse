# pkgpulse

A CLI tool for analyzing and comparing container image sizes and package contents using SBOM (Software Bill of Materials) data. Ideal for evaluating small production images like distroless, minimal base images, and micro containers.

## Features

- **Parallel Analysis**: Analyze multiple container images concurrently (bounded concurrency for stability)
- **Fast by Default**: Uses squashed scope and filtered catalogers for optimal speed
- **Detailed Size Metrics**: Shows both compressed (pull) size and installed (on-disk) size
- **Package Breakdown**: Lists all packages with their individual sizes, including binary packages
- **Multi-Image Comparison**: Side-by-side comparison table when analyzing multiple images
- **CSV Export**: Export package data for further analysis
- **Universal Registry Support**: Works with any OCI-compliant registry across many major cloud providers and registries
- **Binary Package Support**: Detects and reports sizes for static binaries (Go, Rust, etc.) in addition to traditional packages (APK, RPM, DEB)
- **Easy Installation**: Install via curl script, Homebrew, or go install

## Dependencies

### Required

- **[Syft](https://github.com/anchore/syft)** - SBOM generation tool
  ```bash
  # Install via Homebrew (macOS/Linux)
  brew install syft

  # Or via curl
  curl -sSfL https://raw.githubusercontent.com/anchore/syft/main/install.sh | sh -s -- -b /usr/local/bin
  ```

- **Go 1.25+** - For building the application
  ```bash
  # Check your version
  go version
  ```

### Go Dependencies

The following Go modules are automatically managed via `go.mod`:
- `github.com/google/go-containerregistry` - Container registry client

## Installation

### Option 1: Curl install (Quick start)

```bash
# Install pkgpulse
curl -sSL https://raw.githubusercontent.com/jasonwillschiu/pkgpulse/main/scripts/install.sh | sh

# Install syft dependency
brew install syft
```

### Option 2: Homebrew (macOS/Linux)

```bash
# Add the tap and install
brew tap jasonwillschiu/tap
brew install pkgpulse
```

This automatically installs syft as a dependency.

### Option 3: Using go install

Requires Go 1.21+ and syft installed:

```bash
# Install syft first
brew install syft

# Install pkgpulse
go install github.com/jasonwillschiu/pkgpulse@latest
```

The binary will be installed to `~/go/bin/pkgpulse` (ensure `~/go/bin` is in your `$PATH`).

### Option 4: Build from source

```bash
# Clone the repository
git clone https://github.com/jasonwillschiu/pkgpulse
cd pkgpulse

# Install Go dependencies
go mod download

# Build the binary
go build -o pkgpulse main.go

# Optionally install to PATH
sudo mv pkgpulse /usr/local/bin/
```

## Usage

### Basic Usage

Analyze a single image:
```bash
pkgpulse cgr.dev/chainguard/wolfi-base
```

Compare multiple images:
```bash
pkgpulse cgr.dev/chainguard/wolfi-base redhat/ubi9-micro gcr.io/distroless/cc-debian12
```

### Thorough Mode

By default, pkgpulse uses fast analysis (squashed scope + filtered catalogers). For comprehensive layer-by-layer analysis, use `--thorough`:
```bash
# Thorough analysis (scans all image layers)
pkgpulse --thorough python:3.12

# Compare with thorough scanning
pkgpulse -t node:20 node:22 python:3.12
```

Thorough mode uses Syft's `all-layers` scope which scans every image layer. This catches packages that were installed then removed in later layers, but is slower.

### Local Source

Skip registry pulls by using locally cached images:
```bash
# Use image from local Docker daemon
pkgpulse --local postgres:latest

# Use image from Podman
pkgpulse --local=podman postgres:latest
```

This is significantly faster for repeated analysis of the same image.

### CSV Export

Export package data to CSV:
```bash
pkgpulse alpine:latest --csv packages.csv
```

### Using the Built Binary

```bash
# Show help
pkgpulse --help

# Show version
pkgpulse --version

# Analyze images
pkgpulse cgr.dev/chainguard/wolfi-base
```

## Supported Registries

This tool works with **any OCI-compliant container registry**. Below are examples of tested and supported registries:

### Tested Registries

The following registries have been verified to work:

| Registry | Example Image | Notes |
|----------|---------------|-------|
| **Docker Hub** | `docker.io/library/alpine:latest` or `alpine:latest` | Default registry, prefix optional |
| **Google Container Registry (GCR)** | `gcr.io/distroless/static-debian12:latest` | Google Cloud Platform |
| **GitHub Container Registry (GHCR)** | `ghcr.io/distroless/static:latest` | GitHub Packages |
| **Quay.io** | `quay.io/prometheus/node-exporter:latest` | Red Hat's container registry |
| **Kubernetes Registry** | `registry.k8s.io/pause:3.9` | Official Kubernetes images |
| **Chainguard Registry (CGR)** | `cgr.dev/chainguard/wolfi-base` | Minimal, secure images |

### Additional Supported Registries

These registries follow OCI standards and should work without issues:

| Registry | Example Format | Provider |
|----------|----------------|----------|
| **Amazon ECR Public** | `public.ecr.aws/amazonlinux/amazonlinux:latest` | AWS Public Gallery |
| **Amazon ECR Private** | `<account-id>.dkr.ecr.<region>.amazonaws.com/my-image:tag` | AWS Private Registry (requires auth) |
| **Azure Container Registry (ACR)** | `<registry-name>.azurecr.io/my-image:tag` | Microsoft Azure (requires auth) |
| **Google Artifact Registry** | `<region>-docker.pkg.dev/<project>/<repo>/image:tag` | Google Cloud (newer than GCR) |
| **GitLab Container Registry** | `registry.gitlab.com/<namespace>/<project>:tag` | GitLab CI/CD |
| **JFrog Artifactory** | `<server>.jfrog.io/<repo>/image:tag` | Enterprise artifact management |
| **Harbor** | `<harbor-host>/library/image:tag` | Self-hosted registry |
| **Nexus Repository** | `<nexus-host>:<port>/repository/<repo>/image:tag` | Sonatype Nexus |
| **DigitalOcean Registry** | `registry.digitalocean.com/<namespace>/image:tag` | DigitalOcean (requires auth) |
| **IBM Cloud Registry** | `<region>.icr.io/<namespace>/image:tag` | IBM Cloud (requires auth) |
| **Oracle Cloud (OCIR)** | `<region>.ocir.io/<tenancy>/image:tag` | Oracle Cloud (requires auth) |
| **Alibaba Cloud Registry** | `registry.<region>.aliyuncs.com/<namespace>/image:tag` | Alibaba Cloud (requires auth) |
| **Tencent Cloud Registry** | `ccr.ccs.tencentyun.com/<namespace>/image:tag` | Tencent Cloud (requires auth) |

### Authentication

For private registries, ensure you're authenticated using Docker/Podman credentials:

```bash
# Docker Hub or other registries
docker login <registry-url>

# Or use credential helpers
export DOCKER_CONFIG=~/.docker
```

The tool uses the same authentication as Docker/Podman from your local credential store.

### Registry Format Examples

```bash
# Docker Hub (default registry)
pkgpulse nginx:alpine
pkgpulse docker.io/library/nginx:alpine

# Google registries
pkgpulse gcr.io/distroless/base
pkgpulse us-docker.pkg.dev/my-project/my-repo/my-image:v1.0

# AWS ECR Public
pkgpulse public.ecr.aws/nginx/nginx:alpine

# Multiple registries in one comparison
pkgpulse \
  alpine:latest \
  gcr.io/distroless/static:latest \
  quay.io/prometheus/alertmanager:latest \
  ghcr.io/homeassistant/home-assistant:stable
```

## Output

### Single Image

For a single image, the tool displays:
- Compressed size (download/pull size)
- Installed size (on-disk size after extraction)
- Total package count
- Detailed package list sorted by size (largest first)

### Multiple Images

When comparing multiple images, you get:
- Individual breakdowns for each image
- **Summary Comparison Table**: Quick overview of sizes and package counts
- **Package Version & Size Comparison**: Side-by-side comparison showing which packages are present in each image, their versions, and sizes

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

## Use Cases

- **Image Selection**: Compare minimal base images to choose the smallest for your application
- **Size Optimization**: Identify which packages contribute most to image size
- **Distro Comparison**: Compare package ecosystems across different Linux distributions
- **Supply Chain**: Maintain SBOM awareness for compliance and security

## How It Works

1. **Manifest Fetch**: Uses `go-containerregistry` to fetch image manifests and calculate compressed layer sizes from any OCI-compliant registry (runs in parallel with SBOM generation)
2. **SBOM Generation**: Invokes `syft` with `--scope squashed` (default) or `--scope all-layers` (thorough mode), using filtered catalogers (apk, deb, rpm, binary) for speed
3. **Size Calculation**: Parses Syft's JSON output to extract installed sizes from multiple sources:
   - **Traditional packages**: APK (Alpine), RPM (Red Hat/Fedora), DEB (Debian/Ubuntu)
   - **Binary packages**: Static binaries detected by Syft's binary classifier (Go, Rust, busybox, etc.)
   - **File metadata**: Uses artifact relationships to correlate binaries with file sizes
4. **Parallel Processing**: Analyzes multiple images concurrently for faster comparisons
5. **Output Formatting**: Presents data in human-readable tables with aligned columns

## Package Types Supported

The tool detects and reports sizes for:

- **APK packages** (Alpine Linux) - Uses `installedSize` or `size` metadata
- **RPM packages** (Red Hat, Fedora, CentOS) - Uses `size` metadata in bytes
- **DEB packages** (Debian, Ubuntu) - Uses `installedSize` metadata in KB
- **Binary packages** (Go, Rust, C/C++, etc.) - Uses file size from Syft's artifact relationships
- **Static binaries** (busybox, redis, postgresql, prometheus, etc.) - Detected via binary classifier

## Development

### Prerequisites

- [Task](https://taskfile.dev) - Build automation tool
  ```bash
  # Install via Homebrew (macOS/Linux)
  brew install go-task
  
  # Or via Go
  go install github.com/go-task/task/v3/cmd/task@latest
  ```

- [gopls](https://pkg.go.dev/golang.org/x/tools/gopls) - Go language server for enhanced linting
  ```bash
  go install golang.org/x/tools/gopls@latest
  ```

### Available Tasks

```bash
# View all available tasks
task --list

# Format, vet, and lint code
task lint

# Run tests
task test

# Build binaries
task build          # Main CLI tool
task build-all      # All binaries including release-tool

# Run gopls diagnostics
task gopls-check    # Catches modernization patterns

# Clean build artifacts
task clean
```

### Code Quality

The project uses multiple linting tools:
- `go fmt` - Standard Go formatting
- `go vet` - Go's built-in static analyzer
- `golangci-lint` - Comprehensive linting suite
- `gopls` - Language server diagnostics (modernization checks)

Run `task lint` to execute all checks.

## License

MIT License - see [LICENSE](LICENSE) file for details.

## Contributing

Contributions are welcome! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

Please also review our [Code of Conduct](CODE_OF_CONDUCT.md).
