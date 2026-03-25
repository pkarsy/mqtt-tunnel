#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BUILD_DIR="${SCRIPT_DIR}/build"

VERSION="0.5.1"

show_usage() {
    cat << EOF
Usage: $0 [target...]

Targets:
  all         Build all binaries (default)
  linux-amd64 Linux x86_64
  linux-arm64 Linux ARM64
  linux-arm   Linux ARMv7 (Raspberry Pi)
  termux      Android/Termux ARM64 (GOMAXPROCS=1, THREAD=10, DNS=builtin)
  windows     Windows x86_64
  darwin      macOS (Intel + Apple Silicon)
  clean       Remove build directory

Examples:
  $0                  # Build all
  $0 linux-amd64      # Build only Linux x86_64
  $0 termux windows   # Build Termux and Windows
  $0 clean            # Clean build artifacts
EOF
}

get_git_hash() {
    if [ -d "${SCRIPT_DIR}/.git" ]; then
        git -C "$SCRIPT_DIR" rev-parse --short HEAD 2>/dev/null || echo "unknown"
    else
        echo "unknown"
    fi
}

get_git_dirty() {
    if [ -d "${SCRIPT_DIR}/.git" ]; then
        git -C "$SCRIPT_DIR" status --porcelain | grep -q . && echo "-dirty" || true
    fi
}

get_version() {
    local git_hash=$(get_git_hash)
    local dirty=$(get_git_dirty)
    echo "${git_hash}${dirty}"
}

build_binary() {
    local os="$1"
    local arch="$2"
    local output_name="$3"
    local env_vars="$4"
    local extra_flags="${5:-}"

    local version=$(get_version)
    local ldflags="-s -w"

    local output_dir="${BUILD_DIR}/${os}-${arch}"
    local output_path="${output_dir}/${output_name}"

    echo -n "Building ${os}/${arch}... "

    mkdir -p "$output_dir"

    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
        go build -ldflags="$ldflags" -trimpath \
        -o "$output_path" \
        $extra_flags \
        "${SCRIPT_DIR}" 2>/dev/null

    if [ $? -eq 0 ]; then
        local size=$(du -h "$output_path" | cut -f1)
        echo "OK ($size)"
    else
        echo "FAILED"
        return 1
    fi
}

build_linux_amd64() {
    build_binary "linux" "amd64" "mqtt-tunnel"
}

build_linux_arm64() {
    build_binary "linux" "arm64" "mqtt-tunnel"
}

build_linux_arm() {
    build_binary "linux" "arm" "mqtt-tunnel"
}

build_termux() {
    echo -n "Building linux/arm64 (termux)... "

    local version=$(get_version)
    local ldflags="-s -w"
    local output_dir="${BUILD_DIR}/termux-arm64"
    local output_path="${output_dir}/mqtt-tunnel"

    mkdir -p "$output_dir"

    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
        go build -ldflags="$ldflags" -trimpath \
        -tags=termux \
        -o "$output_path" \
        "${SCRIPT_DIR}" 2>/dev/null

    if [ $? -eq 0 ]; then
        local size=$(du -h "$output_path" | cut -f1)
        echo "OK ($size)"
    else
        echo "FAILED"
        return 1
    fi
}

build_windows() {
    build_binary "windows" "amd64" "mqtt-tunnel.exe"
}

build_darwin_amd64() {
    build_binary "darwin" "amd64" "mqtt-tunnel"
}

build_darwin_arm64() {
    build_binary "darwin" "arm64" "mqtt-tunnel"
}

build_darwin() {
    build_darwin_amd64
    build_darwin_arm64
}

build_all() {
    echo "Building mqtt-tunnel v$VERSION"
    echo "================================"
    echo ""

    build_linux_amd64
    build_linux_arm64
    build_linux_arm
    build_termux
    build_windows
    build_darwin

    echo ""
    echo "Build complete: ${BUILD_DIR}"
    echo ""
    ls -la "${BUILD_DIR}"
}

clean() {
    if [ -d "${BUILD_DIR}" ]; then
        echo "Removing ${BUILD_DIR}"
        rm -rf "${BUILD_DIR}"
    else
        echo "Nothing to clean"
    fi
}

main() {
    if [ $# -eq 0 ]; then
        build_all
        return
    fi

    for arg in "$@"; do
        case "$arg" in
            all)
                build_all
                ;;
            linux-amd64)
                build_linux_amd64
                ;;
            linux-arm64)
                build_linux_arm64
                ;;
            linux-arm)
                build_linux_arm
                ;;
            termux)
                build_termux
                ;;
            windows)
                build_windows
                ;;
            darwin)
                build_darwin
                ;;
            clean)
                clean
                ;;
            help|--help|-h)
                show_usage
                ;;
            *)
                echo "Unknown target: $arg"
                show_usage
                exit 1
                ;;
        esac
    done
}

main "$@"
