#!/usr/bin/env bash
# release.sh — build cross-platform release tarballs for upgrade-guardian.
#
# Usage:
#   ./scripts/release.sh                 # uses VERSION from env or git describe
#   VERSION=0.2.0 ./scripts/release.sh   # explicit
#
# Output: dist/upgrade-guardian-<version>-<os>-<arch>.tar.gz

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo "0.0.0-dev")}"
BUILD_DIR="dist"
PLATFORMS=(
  "linux/amd64"
  "linux/arm64"
  "darwin/amd64"
  "darwin/arm64"
)

echo ">> Building upgrade-guardian ${VERSION}"
rm -rf "$BUILD_DIR"
mkdir -p "$BUILD_DIR"

# Build plugin once — same JS bundle works on every OS.
echo ">> Building Headlamp plugin"
( cd plugin && [ -d node_modules ] || npm install ) >/dev/null
( cd plugin && node build.mjs )

for platform in "${PLATFORMS[@]}"; do
  GOOS="${platform%/*}"
  GOARCH="${platform#*/}"
  STAGE="$BUILD_DIR/stage-${GOOS}-${GOARCH}"
  OUT="upgrade-guardian-${VERSION}-${GOOS}-${GOARCH}"

  echo ">> Building ${GOOS}/${GOARCH}"
  rm -rf "$STAGE"
  mkdir -p "$STAGE/${OUT}/bin" \
           "$STAGE/${OUT}/plugin" \
           "$STAGE/${OUT}/scripts" \
           "$STAGE/${OUT}/docs"

  # Build both server and CLI tools
  CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
    go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o "$STAGE/${OUT}/bin/upgrade-guardian" \
      ./cmd/server
  CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
    go build -trimpath \
      -ldflags "-s -w" \
      -o "$STAGE/${OUT}/bin/upgrade-guardian-cli" \
      ./cmd/cli

  cp plugin/dist/main.js  "$STAGE/${OUT}/plugin/main.js"
  cp plugin/package.json  "$STAGE/${OUT}/plugin/package.json"
  cp scripts/install.sh   "$STAGE/${OUT}/scripts/install.sh"
  chmod +x "$STAGE/${OUT}/scripts/install.sh"

  # Service unit files go into the tarball but aren't installed by default.
  if [ -d scripts/systemd ]; then
    cp -r scripts/systemd "$STAGE/${OUT}/scripts/systemd"
  fi
  if [ -d scripts/launchd ]; then
    cp -r scripts/launchd "$STAGE/${OUT}/scripts/launchd"
  fi

  [ -f docs/INSTALL.md ]     && cp docs/INSTALL.md     "$STAGE/${OUT}/docs/INSTALL.md"
  [ -f docs/ARCHITECTURE.md ] && cp docs/ARCHITECTURE.md "$STAGE/${OUT}/docs/ARCHITECTURE.md"
  [ -f README.md ]            && cp README.md           "$STAGE/${OUT}/README.md"

  tar -czf "${BUILD_DIR}/${OUT}.tar.gz" -C "$STAGE" "${OUT}"
  ( cd "$BUILD_DIR" && sha256sum "${OUT}.tar.gz" > "${OUT}.tar.gz.sha256" 2>/dev/null \
                    || shasum -a 256 "${OUT}.tar.gz" > "${OUT}.tar.gz.sha256" )
  rm -rf "$STAGE"
done

echo ""
echo ">> Release artifacts:"
ls -lh "$BUILD_DIR"
