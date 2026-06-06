#!/usr/bin/env bash
set -euo pipefail

# Cross-compile coomerfans for multiple platforms.
#
# Default targets: darwin/arm64, linux/amd64, windows/amd64
#
# Usage:
#   ./compile.sh                                      # build defaults
#   ./compile.sh --arch darwin/arm64 --arch linux/amd64  # build specific targets only
#
# Valid GOOS values:  darwin, linux, windows, freebsd, ...
# Valid GOARCH values: amd64, arm64, 386, arm, ...
# Format: GOOS/GOARCH  e.g. darwin/arm64, windows/amd64

BINARY_NAME="coomerfans"
OUT_DIR="./dist"

DEFAULT_TARGETS=(
  "darwin/arm64"
  "linux/amd64"
  "windows/amd64"
)

# ── Help ─────────────────────────────────────────────────────────────────────

usage() {
  cat << HELP
Usage: $0 [--arch GOOS/GOARCH] ...

Cross-compiles ${BINARY_NAME} for one or more platforms.
With no --arch flags the default targets are built.

Options:
  --arch GOOS/GOARCH   Add a target (repeatable)
  --help               Show this help

Default targets:
  darwin/arm64         macOS Apple Silicon
  linux/amd64          Linux x86-64
  windows/amd64        Windows x86-64

Common targets:
  darwin/amd64         macOS Intel
  linux/arm64          Linux ARM64 (e.g. Raspberry Pi 4, AWS Graviton)
  linux/386            Linux x86 32-bit
  windows/arm64        Windows ARM64 (e.g. Surface Pro X)
  freebsd/amd64        FreeBSD x86-64

All valid GOOS values:   darwin linux windows freebsd openbsd netbsd plan9 solaris
All valid GOARCH values: amd64 arm64 386 arm mips mips64 riscv64 s390x wasm

Examples:
  $0
  $0 --arch darwin/arm64 --arch windows/arm64
  $0 --arch linux/amd64 --arch linux/arm64
HELP
}

# ── Parse --arch flags ────────────────────────────────────────────────────────

targets=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --arch)
      targets+=("$2")
      shift 2
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      echo "Unknown argument: $1" >&2
      echo "Run '$0 --help' for usage." >&2
      exit 1
      ;;
  esac
done

# Fall back to defaults if no --arch given
if [[ ${#targets[@]} -eq 0 ]]; then
  targets=("${DEFAULT_TARGETS[@]}")
fi

# ── Build ─────────────────────────────────────────────────────────────────────

mkdir -p "$OUT_DIR"

for target in "${targets[@]}"; do
  IFS='/' read -r goos goarch <<< "$target"

  if [[ -z "$goos" || -z "$goarch" ]]; then
    echo "  [skip] invalid target format: '$target' (expected GOOS/GOARCH)" >&2
    continue
  fi

  # Append .exe for Windows binaries
  ext=""
  [[ "$goos" == "windows" ]] && ext=".exe"

  out="${OUT_DIR}/${BINARY_NAME}-${goos}-${goarch}${ext}"

  printf "  building %-20s -> %s\n" "${goos}/${goarch}" "$out"

  GOOS="$goos" GOARCH="$goarch" go build -o "$out" .
done

echo
echo "Done. Binaries in ${OUT_DIR}/"
ls -lh "$OUT_DIR/"