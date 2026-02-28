#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
VENDOR_DIR="$PROJECT_DIR/third-party/moonshine"

# ─── Parse flags ─────────────────────────────────────────────────────────────

usage() {
    cat <<EOF
Usage: $(basename "$0") [OPTIONS]

Options:
  -h, --help    Show this help

Bundles both English (base-en) and Spanish (base-es) models.
EOF
    exit 0
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        -h|--help)   usage ;;
        *)           echo "Unknown option: $1"; usage ;;
    esac
done

# Models to bundle
MODELS=("base-es" "base-en")

# Map model name to download URL and expected files
declare -A MODEL_URLS=(
    ["tiny-en"]="https://download.moonshine.ai/model/tiny-en/quantized/tiny-en"
    ["base-en"]="https://download.moonshine.ai/model/base-en/quantized/base-en"
    ["base-es"]="https://download.moonshine.ai/model/base-es/quantized/base-es"
    ["base-ar"]="https://download.moonshine.ai/model/base-ar/quantized/base-ar"
    ["base-ja"]="https://download.moonshine.ai/model/base-ja/quantized/base-ja"
    ["base-zh"]="https://download.moonshine.ai/model/base-zh/quantized/base-zh"
    ["base-uk"]="https://download.moonshine.ai/model/base-uk/quantized/base-uk"
    ["base-vi"]="https://download.moonshine.ai/model/base-vi/quantized/base-vi"
    ["tiny-streaming-en"]="https://download.moonshine.ai/model/tiny-streaming-en/quantized"
    ["small-streaming-en"]="https://download.moonshine.ai/model/small-streaming-en/quantized"
    ["medium-streaming-en"]="https://download.moonshine.ai/model/medium-streaming-en/quantized"
)

# Non-streaming models use these files
NON_STREAMING_FILES="encoder_model.ort decoder_model_merged.ort tokenizer.bin"
# Streaming models use these files
STREAMING_FILES="encoder.ort decoder_kv.ort cross_kv.ort frontend.ort adapter.ort streaming_config.json tokenizer.bin"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BOLD='\033[1m'
NC='\033[0m'

info()  { echo -e "${GREEN}==>${NC} ${BOLD}$*${NC}"; }
warn()  { echo -e "${YELLOW}WARNING:${NC} $*"; }
error() { echo -e "${RED}ERROR:${NC} $*" >&2; }
die()   { error "$@"; exit 1; }

# ─── Helpers ─────────────────────────────────────────────────────────────────

is_debian() {
    [ -f /etc/os-release ] && grep -qiE 'debian|ubuntu|raspbian' /etc/os-release
}

# ─── Preflight checks ───────────────────────────────────────────────────────

preflight() {
    info "Checking prerequisites..."
    local ok=true

    # Required commands
    for cmd in gcc g++ cmake git git-lfs go curl zstd pkg-config; do
        if command -v "$cmd" &>/dev/null; then
            printf "  %-20s %s\n" "$cmd" "$(command -v "$cmd")"
        else
            error "Missing: $cmd"
            ok=false
        fi
    done

    # Go version check (need 1.21+)
    if command -v go &>/dev/null; then
        local go_ver
        go_ver=$(go version | grep -oP 'go\K[0-9]+\.[0-9]+')
        local major minor
        major=$(echo "$go_ver" | cut -d. -f1)
        minor=$(echo "$go_ver" | cut -d. -f2)
        if (( major < 1 || (major == 1 && minor < 21) )); then
            error "Go 1.21+ required, found $go_ver"
            ok=false
        fi
    fi

    # PortAudio dev headers
    if pkg-config --exists portaudio-2.0 2>/dev/null; then
        printf "  %-20s %s\n" "portaudio-dev" "$(pkg-config --modversion portaudio-2.0)"
    else
        error "Missing: portaudio dev headers"
        ok=false
    fi

    # JACK (via PipeWire) — optional, warn only
    if ldconfig -p 2>/dev/null | grep -q libjack.so; then
        printf "  %-20s found\n" "libjack"
    elif find /usr/lib* -path '*/pipewire*/jack/libjack.so' 2>/dev/null | grep -q .; then
        printf "  %-20s found (pipewire)\n" "libjack"
    else
        warn "libjack not found (may be needed by PortAudio on some systems)"
    fi

    if [ "$ok" = false ]; then
        echo ""
        if is_debian; then
            echo "Install missing dependencies (Debian/Ubuntu/RPi):"
            echo "  sudo apt install -y build-essential cmake git git-lfs portaudio19-dev zstd"
        else
            echo "Install missing dependencies (Fedora):"
            echo "  sudo dnf install -y gcc gcc-c++ cmake git git-lfs portaudio-devel \\"
            echo "    pipewire-jack-audio-connection-kit-devel zstd"
        fi
        echo ""
        die "Preflight check failed"
    fi

    echo ""
    info "All prerequisites satisfied"
}

# ─── Clone/update Moonshine ─────────────────────────────────────────────────

setup_moonshine() {
    if [ -d "$VENDOR_DIR/src/core" ]; then
        # Check if LFS files are resolved
        if head -1 "$VENDOR_DIR/src/core/speaker-embedding-model-data.cpp" 2>/dev/null | grep -q '^version https://git-lfs'; then
            warn "LFS files not resolved, re-cloning..."
            rm -rf "$VENDOR_DIR/src"
        else
            info "Moonshine source already present"
        fi
    fi

    if [ ! -d "$VENDOR_DIR/src/core" ]; then
        info "Cloning Moonshine (with LFS)..."
        mkdir -p "$VENDOR_DIR"
        git clone --depth 1 https://github.com/moonshine-ai/moonshine.git "$VENDOR_DIR/src"
        info "Moonshine source ready"
    fi

    # Ensure core symlink points to the cloned source
    rm -rf "$VENDOR_DIR/core" 2>/dev/null || true
    ln -snf "$VENDOR_DIR/src/core" "$VENDOR_DIR/core"
}

# ─── Build libmoonshine.so ───────────────────────────────────────────────────

build_moonshine() {
    local build_dir="$VENDOR_DIR/core/build"
    local host_arch
    host_arch=$(uname -m)

    # Copy ONNX Runtime to vendor first (needed for build)
    local ort_src="$VENDOR_DIR/core/third-party/onnxruntime/lib/linux/$host_arch"
    local ort_dst="$VENDOR_DIR/onnxruntime"

    # Detect ORT version from the source lib
    local ort_src_ver=""
    if [ -d "$ort_src" ]; then
        ort_src_ver=$(strings "$ort_src"/libonnxruntime.so* 2>/dev/null | grep -oP '^[0-9]+\.[0-9]+\.[0-9]+$' | head -1 || true)
    fi

    # Check if existing ORT matches source version
    if [ -f "$ort_dst/libonnxruntime.so.1" ]; then
        local ort_dst_ver
        ort_dst_ver=$(readelf -V "$ort_dst/libonnxruntime.so.1" 2>/dev/null | grep -oP 'VERS_\K[0-9.]+' | head -1 || true)
        if [ -n "$ort_src_ver" ] && [ -n "$ort_dst_ver" ] && [ "$ort_dst_ver" != "$ort_src_ver" ]; then
            warn "ORT version mismatch: installed=$ort_dst_ver source=$ort_src_ver, updating..."
            rm -f "$ort_dst/libonnxruntime.so.1"
        fi
    fi

    if [ ! -f "$ort_dst/libonnxruntime.so.1" ]; then
        if [ ! -d "$ort_src" ]; then
            die "No ONNX Runtime found for $host_arch at $ort_src"
        fi
        mkdir -p "$ort_dst"
        cp "$ort_src"/libonnxruntime.so* "$ort_dst/"
        # Ensure .so.1 symlink exists
        if [ ! -f "$ort_dst/libonnxruntime.so.1" ]; then
            ln -sf "$(ls "$ort_dst"/libonnxruntime.so.* | head -1)" "$ort_dst/libonnxruntime.so.1"
        fi
        info "ONNX Runtime copied for $host_arch ($ort_src_ver)"
    fi

    # Check if existing .so matches architecture and ORT version
    local need_build=false
    if [ -f "$build_dir/libmoonshine.so" ]; then
        local so_arch
        so_arch=$(file "$build_dir/libmoonshine.so" 2>/dev/null | grep -oP '(x86-64|aarch64|ARM)' || echo "unknown")
        case "$host_arch" in
            x86_64)  [[ "$so_arch" == "x86-64" ]] || need_build=true ;;
            aarch64) [[ "$so_arch" == "aarch64" || "$so_arch" == "ARM" ]] || need_build=true ;;
            *)       need_build=true ;;
        esac

        # Check if libmoonshine links against the same ORT version we have
        if [ "$need_build" = false ]; then
            local linked_ort
            linked_ort=$(readelf -V "$build_dir/libmoonshine.so" 2>/dev/null | grep -oP 'VERS_\K[0-9.]+' | head -1 || true)
            local current_ort
            current_ort=$(readelf -V "$ort_dst/libonnxruntime.so.1" 2>/dev/null | grep -oP 'VERS_\K[0-9.]+' | head -1 || true)
            if [ -n "$linked_ort" ] && [ -n "$current_ort" ] && [ "$linked_ort" != "$current_ort" ]; then
                warn "libmoonshine.so linked against ORT $linked_ort but have ORT $current_ort, rebuilding..."
                need_build=true
            fi
        fi

        if [ "$need_build" = true ]; then
            rm -rf "$build_dir"
        else
            info "libmoonshine.so already built for $host_arch"
        fi
    else
        need_build=true
    fi

    if [ "$need_build" = true ]; then
        info "Building libmoonshine.so for $host_arch..."
        mkdir -p "$build_dir"
        cmake -B "$build_dir" -S "$VENDOR_DIR/core" \
            -DCMAKE_BUILD_TYPE=Release \
            -DMOONSHINE_BUILD_SHARED=ON 2>&1
        cmake --build "$build_dir" -j"$(nproc)" 2>&1
        if [ ! -f "$build_dir/libmoonshine.so" ]; then
            die "Failed to build libmoonshine.so"
        fi
        info "libmoonshine.so built"
    fi
}

# ─── Download model ──────────────────────────────────────────────────────────

download_model() {
    local model="$1"
    local model_dir="$PROJECT_DIR/models/$model"

    if [ -d "$model_dir" ] && [ "$(ls -A "$model_dir" 2>/dev/null)" ]; then
        info "Model '$model' already downloaded"
        return
    fi

    local url="${MODEL_URLS[$model]:-}"
    if [ -z "$url" ]; then
        die "Unknown model: $model. Available: ${!MODEL_URLS[*]}"
    fi

    info "Downloading model '$model'..."
    mkdir -p "$model_dir"

    local files
    case "$model" in
        *streaming*) files=$STREAMING_FILES ;;
        *)           files=$NON_STREAMING_FILES ;;
    esac

    for f in $files; do
        echo "  Downloading $f..."
        curl -fL --progress-bar -o "$model_dir/$f" "$url/$f"
    done

    info "Model '$model' downloaded"
}

download_models() {
    for model in "${MODELS[@]}"; do
        download_model "$model"
    done
}

# ─── Build Go binaries ───────────────────────────────────────────────────────

build_binaries() {
    cd "$PROJECT_DIR"

    info "Fetching Go dependencies..."
    go get github.com/gordonklaus/portaudio
    go mod tidy

    info "Building lunartlk-client..."
    go build -o bin/lunartlk-client ./cmd/lunartlk-client

    info "Building lunartlk-server..."
    go build -o bin/lunartlk-server.bin ./cmd/lunartlk-server

    info "Creating self-extracting server bundle..."
    local staging
    staging=$(mktemp -d)
    trap "rm -rf $staging" EXIT

    cp bin/lunartlk-server.bin "$staging/"
    mkdir -p "$staging/libs"
    cp "$VENDOR_DIR/core/build/libmoonshine.so" "$staging/libs/"
    cp "$VENDOR_DIR/onnxruntime"/libonnxruntime.so* "$staging/libs/"

    local payload
    payload=$(mktemp)
    tar -cf - -C "$staging" . | zstd -3 -T0 > "$payload"

    cat > "$PROJECT_DIR/bin/lunartlk-server" << 'WRAPPER'
#!/bin/bash
set -e
EXTRACT_DIR="${LUNARTLK_CACHE_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/lunartlk}"
MARKER="$EXTRACT_DIR/.extracted"
SELF_HASH=$(md5sum "$0" 2>/dev/null | cut -d' ' -f1)
if [ ! -f "$MARKER" ] || [ "$(cat "$MARKER")" != "$SELF_HASH" ]; then
    echo "Extracting lunartlk server to $EXTRACT_DIR..." >&2
    mkdir -p "$EXTRACT_DIR"
    ARCHIVE_START=$(awk '/^__ARCHIVE_BELOW__$/{print NR + 1; exit 0; }' "$0")
    tail -n +"$ARCHIVE_START" "$0" | zstd -d | tar xf - -C "$EXTRACT_DIR"
    chmod +x "$EXTRACT_DIR/lunartlk-server.bin"
    echo "$SELF_HASH" > "$MARKER"
fi
export LD_LIBRARY_PATH="$EXTRACT_DIR/libs:${LD_LIBRARY_PATH:-}"
export _MOONSHINE_DIR="$EXTRACT_DIR"
exec "$EXTRACT_DIR/lunartlk-server.bin" "$@"
__ARCHIVE_BELOW__
WRAPPER

    cat "$payload" >> "$PROJECT_DIR/bin/lunartlk-server"
    chmod +x "$PROJECT_DIR/bin/lunartlk-server"
    rm -f "$PROJECT_DIR/bin/lunartlk-server.bin" "$payload"

    local size
    size=$(du -h "$PROJECT_DIR/bin/lunartlk-server" | cut -f1)
    info "Server bundle: bin/lunartlk-server ($size, models download on first run)"
}

# ─── Main ────────────────────────────────────────────────────────────────────

main() {
    echo ""
    echo -e "${BOLD}╔══════════════════════════════════════╗${NC}"
    echo -e "${BOLD}║         lunartlk build system        ║${NC}"
    echo -e "${BOLD}╚══════════════════════════════════════╝${NC}"
    echo ""

    preflight
    setup_moonshine
    build_moonshine
    build_binaries

    echo ""
    info "Build complete!"
    echo ""
    echo "  Server: bin/lunartlk-server [-addr :9765] [-lang es|en]"
    echo "  Client: bin/lunartlk-client [-server http://localhost:9765]"
    echo ""
    echo "  Models download automatically on first server start."
    echo ""
}

main "$@"
