#!/usr/bin/env bash
# ============================================================================
# TollVault - Cross-Platform Build Script
# ============================================================================
# Builds executables for Windows, macOS, and Linux across all architectures.
#
# Usage:
#   chmod +x build.sh
#   ./build.sh              # Build all platforms & architectures
#   ./build.sh windows      # Build only Windows targets
#   ./build.sh linux amd64  # Build Linux amd64 only
#
# Output: ./build/<os>_<arch>/tollvault[.exe]
# ============================================================================

set -euo pipefail

APP_NAME="tollvault"
MODULE="tollvault"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BUILD_DIR="${SCRIPT_DIR}/../TollVault_builds"
VERSION="${VERSION:-dev}"
BUILD_TIME=$(date -u '+%Y-%m-%dT%H:%M:%SZ')

# LDFLAGS for smaller binaries and version embedding
LDFLAGS="-s -w -X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME}"

# ─── Color helpers ──────────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m' # No Color

info()    { echo -e "${CYAN}[INFO]${NC}  $*"; }
success() { echo -e "${GREEN}[OK]${NC}    $*"; }
warn()    { echo -e "${YELLOW}[WARN]${NC}  $*"; }
fail()    { echo -e "${RED}[FAIL]${NC}  $*"; }

# ─── Platform / Arch Matrix ────────────────────────────────────────────────
# Format: "GOOS:GOARCH"
ALL_TARGETS=(
    # Windows
    "windows:386"
    "windows:amd64"
    "windows:arm64"

    # Linux
    "linux:386"
    "linux:amd64"
    "linux:arm64"

    # macOS (Apple dropped 32-bit support; 386 is not available)
    "darwin:amd64"
    "darwin:arm64"
)

# ─── Functions ──────────────────────────────────────────────────────────────

print_banner() {
    echo -e "${BOLD}${CYAN}"
    echo "╔══════════════════════════════════════════════════╗"
    echo "║          TollVault Cross-Platform Builder        ║"
    echo "╚══════════════════════════════════════════════════╝"
    echo -e "${NC}"
    echo -e "  Version  : ${YELLOW}${VERSION}${NC}"
    echo -e "  Time     : ${YELLOW}${BUILD_TIME}${NC}"
    echo ""
}

should_build() {
    local target_os="$1"
    local target_arch="$2"
    local filter_os="${3:-}"
    local filter_arch="${4:-}"

    if [[ -n "$filter_os" && "$target_os" != "$filter_os" ]]; then
        return 1
    fi
    if [[ -n "$filter_arch" && "$target_arch" != "$filter_arch" ]]; then
        return 1
    fi
    return 0
}

build_target() {
    local target_os="$1"
    local target_arch="$2"

    local output_name="${APP_NAME}"
    local ext=""
    if [[ "$target_os" == "windows" ]]; then
        ext=".exe"
    fi

    local out_dir="${BUILD_DIR}/${target_os}_${target_arch}"
    local out_path="${out_dir}/${output_name}${ext}"

    mkdir -p "$out_dir"

    printf "  %-12s %-8s ... " "$target_os" "$target_arch"

    # CGO_ENABLED=0 because we use modernc.org/sqlite (pure Go)
    if CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" \
        go build -ldflags "${LDFLAGS}" -o "$out_path" . 2>/tmp/tollvault_build_err_$$; then
        local size
        size=$(du -h "$out_path" | awk '{print $1}')
        echo -e "${GREEN}✓${NC}  (${size})"
        return 0
    else
        echo -e "${RED}✗${NC}"
        cat /tmp/tollvault_build_err_$$ 2>/dev/null || true
        rm -f /tmp/tollvault_build_err_$$
        return 1
    fi
}

# ─── Main ───────────────────────────────────────────────────────────────────

main() {
    local filter_os="${1:-}"
    local filter_arch="${2:-}"

    print_banner

    # Verify Go is installed
    if ! command -v go &>/dev/null; then
        fail "Go is not installed. Please install Go first: https://go.dev/dl/"
        exit 1
    fi
    info "Go version: $(go version)"
    echo ""

    # Clean previous builds
    if [[ -d "$BUILD_DIR" ]]; then
        info "Cleaning previous build directory..."
        rm -rf "$BUILD_DIR"
    fi
    mkdir -p "$BUILD_DIR"

    # Download dependencies
    info "Downloading dependencies..."
    go mod download
    echo ""

    # Build
    info "Starting builds..."
    echo -e "  ${BOLD}OS           ARCH     STATUS${NC}"
    echo "  ──────────── ──────── ──────────"

    local total=0
    local passed=0
    local failed=0
    local skipped=0

    for target in "${ALL_TARGETS[@]}"; do
        local target_os="${target%%:*}"
        local target_arch="${target##*:}"

        if ! should_build "$target_os" "$target_arch" "$filter_os" "$filter_arch"; then
            ((skipped++))
            continue
        fi

        ((total++))
        if build_target "$target_os" "$target_arch"; then
            ((passed++))
        else
            ((failed++))
        fi
    done

    # Clean up temp files
    rm -f /tmp/tollvault_build_err_$$

    # Summary
    echo ""
    echo -e "  ${BOLD}──────────────────────────────${NC}"
    echo -e "  ${BOLD}Build Summary${NC}"
    echo -e "  Total   : ${total}"
    echo -e "  ${GREEN}Passed${NC}  : ${passed}"
    if [[ $failed -gt 0 ]]; then
        echo -e "  ${RED}Failed${NC}  : ${failed}"
    fi
    if [[ $skipped -gt 0 ]]; then
        echo -e "  ${YELLOW}Skipped${NC} : ${skipped}"
    fi
    echo ""

    if [[ $failed -eq 0 ]]; then
        success "All builds completed successfully!"
        echo ""
        info "Output directory: ${BUILD_DIR}/"
        echo ""
        echo "  Directory structure:"
        if command -v tree &>/dev/null; then
            tree "$BUILD_DIR"
        else
            find "$BUILD_DIR" -type f | sort | while read -r f; do
                echo "    $f"
            done
        fi
    else
        fail "${failed} build(s) failed."
        exit 1
    fi
}

main "$@"
