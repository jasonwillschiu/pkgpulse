#!/bin/sh
# pkgpulse installer script
# Usage: curl -sSL https://raw.githubusercontent.com/jasonwillschiu/pkgpulse/main/scripts/install.sh | sh
#    or: curl -sSL ... | sh -s -- -b /custom/path
#    or: curl -sSL ... | sh -s -- v0.6.0  (specific version)

set -e

REPO="jasonwillschiu/pkgpulse"
BINARY="pkgpulse"
INSTALL_DIR="/usr/local/bin"

# Colors for output (if terminal supports it)
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

info() {
    printf "${GREEN}[INFO]${NC} %s\n" "$1"
}

warn() {
    printf "${YELLOW}[WARN]${NC} %s\n" "$1"
}

error() {
    printf "${RED}[ERROR]${NC} %s\n" "$1" >&2
    exit 1
}

# Parse arguments
VERSION=""
while [ $# -gt 0 ]; do
    case "$1" in
        -b|--bin-dir)
            INSTALL_DIR="$2"
            shift 2
            ;;
        -v|--version)
            VERSION="$2"
            shift 2
            ;;
        v*)
            VERSION="$1"
            shift
            ;;
        *)
            shift
            ;;
    esac
done

# Detect OS
detect_os() {
    OS=$(uname -s | tr '[:upper:]' '[:lower:]')
    case "$OS" in
        linux) OS="linux" ;;
        darwin) OS="darwin" ;;
        mingw*|msys*|cygwin*) OS="windows" ;;
        *) error "Unsupported operating system: $OS" ;;
    esac
    echo "$OS"
}

# Detect architecture
detect_arch() {
    ARCH=$(uname -m)
    case "$ARCH" in
        x86_64|amd64) ARCH="amd64" ;;
        aarch64|arm64) ARCH="arm64" ;;
        *) error "Unsupported architecture: $ARCH" ;;
    esac
    echo "$ARCH"
}

# Get latest version from GitHub
get_latest_version() {
    curl -sI "https://github.com/$REPO/releases/latest" 2>/dev/null | \
        grep -i "^location:" | \
        sed 's/.*tag\///' | \
        tr -d '\r\n'
}

# Download and install
install_pkgpulse() {
    OS=$(detect_os)
    ARCH=$(detect_arch)

    # Get version
    if [ -z "$VERSION" ]; then
        info "Fetching latest version..."
        VERSION=$(get_latest_version)
        if [ -z "$VERSION" ]; then
            error "Could not determine latest version. Please specify a version with -v flag."
        fi
    fi

    # Ensure version starts with 'v'
    case "$VERSION" in
        v*) ;;
        *) VERSION="v$VERSION" ;;
    esac

    info "Installing pkgpulse $VERSION for $OS/$ARCH"

    # Determine file extension
    EXT="tar.gz"
    if [ "$OS" = "windows" ]; then
        EXT="zip"
    fi

    # Build download URL
    FILENAME="${BINARY}_${OS}_${ARCH}.${EXT}"
    URL="https://github.com/$REPO/releases/download/${VERSION}/${FILENAME}"

    info "Downloading from $URL"

    # Create temp directory
    TMP_DIR=$(mktemp -d)
    trap 'rm -rf "$TMP_DIR"' EXIT

    # Download archive
    if ! curl -sL "$URL" -o "$TMP_DIR/$FILENAME"; then
        error "Failed to download $URL"
    fi

    # Extract binary
    info "Extracting..."
    cd "$TMP_DIR"
    if [ "$EXT" = "zip" ]; then
        unzip -q "$FILENAME"
    else
        tar xzf "$FILENAME"
    fi

    # Install binary
    if [ ! -f "$BINARY" ] && [ ! -f "${BINARY}.exe" ]; then
        error "Binary not found in archive"
    fi

    # Check if we can write to install dir
    if [ ! -w "$INSTALL_DIR" ]; then
        warn "Cannot write to $INSTALL_DIR, trying with sudo..."
        sudo mkdir -p "$INSTALL_DIR"
        sudo mv "$BINARY"* "$INSTALL_DIR/"
        sudo chmod +x "$INSTALL_DIR/$BINARY"*
    else
        mkdir -p "$INSTALL_DIR"
        mv "$BINARY"* "$INSTALL_DIR/"
        chmod +x "$INSTALL_DIR/$BINARY"*
    fi

    info "Successfully installed pkgpulse to $INSTALL_DIR/$BINARY"

    # Verify installation
    if command -v pkgpulse >/dev/null 2>&1; then
        info "Installed version: $(pkgpulse --version)"
    else
        warn "pkgpulse installed but not in PATH. Add $INSTALL_DIR to your PATH."
    fi

    # Check for syft dependency
    echo ""
    if ! command -v syft >/dev/null 2>&1; then
        warn "syft is required but not installed."
        info "Install syft with: brew install syft"
        info "Or: curl -sSfL https://raw.githubusercontent.com/anchore/syft/main/install.sh | sh -s -- -b /usr/local/bin"
    else
        info "syft dependency found: $(syft --version 2>/dev/null | head -1)"
    fi
}

# Run installation
install_pkgpulse
