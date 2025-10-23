# pkgpulse-com

A CLI tool for analyzing and comparing container image sizes and package contents using SBOM (Software Bill of Materials) data. Ideal for evaluating small production images like distroless, minimal base images, and micro containers.

## Features

- **Parallel Analysis**: Analyze multiple container images concurrently
- **Detailed Size Metrics**: Shows both compressed (pull) size and installed (on-disk) size
- **Package Breakdown**: Lists all packages with their individual sizes
- **Multi-Image Comparison**: Side-by-side comparison table when analyzing multiple images
- **CSV Export**: Export package data for further analysis
- **Real Registry Support**: Works with any OCI-compliant registry (Docker Hub, GCR, ECR, etc.)

## Dependencies

### Required

- **[Syft](https://github.com/anchore/syft)** - SBOM generation tool
  ```bash
  # Install via Homebrew (macOS/Linux)
  brew install syft

  # Or via curl
  curl -sSfL https://raw.githubusercontent.com/anchore/syft/main/install.sh | sh -s -- -b /usr/local/bin
  ```

- **Go 1.25.1+** - For building the application
  ```bash
  # Check your version
  go version
  ```

### Go Dependencies

The following Go modules are automatically managed via `go.mod`:
- `github.com/google/go-containerregistry` - Container registry client

## Installation

```bash
# Clone the repository
git clone <repository-url>
cd pkgpulse-com

# Install Go dependencies
go mod download

# Build the binary (optional)
go build -o pkgpulse main.go
```

## Usage

### Basic Usage

Analyze a single image:
```bash
go run main.go cgr.dev/chainguard/wolfi-base
```

Compare multiple images:
```bash
go run main.go cgr.dev/chainguard/wolfi-base redhat/ubi9-micro gcr.io/distroless/cc-debian12
```

### CSV Export

Export package data to CSV:
```bash
go run main.go alpine:latest --csv packages.csv
```

### Using the Built Binary

```bash
# After building
./pkgpulse cgr.dev/chainguard/wolfi-base
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
go run main.go cgr.dev/chainguard/wolfi-base redhat/ubi9-micro gcr.io/distroless/cc-debian12
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

1. **Manifest Fetch**: Uses `go-containerregistry` to fetch image manifests and calculate compressed layer sizes
2. **SBOM Generation**: Invokes `syft` with `--scope all-layers` to extract package information
3. **Size Calculation**: Parses Syft's JSON output to extract installed sizes (handles APK, RPM, and DEB formats)
4. **Parallel Processing**: Analyzes multiple images concurrently for faster comparisons
5. **Output Formatting**: Presents data in human-readable tables with aligned columns

## License

[Add your license here]

## Contributing

[Add contribution guidelines here]
