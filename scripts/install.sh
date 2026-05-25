#!/usr/bin/env bash
# install.sh — install upgrade-guardian on the local machine.
#
# Usage (from inside an unpacked release tarball):
#   ./scripts/install.sh                 # interactive, prompts for sudo
#   ./scripts/install.sh --prefix ~/.local --no-systemd
#
# Flags:
#   --prefix DIR     Install root for the binary. Default: /usr/local
#   --plugin-dir DIR Override Headlamp plugin directory.
#                    Default: $HOME/.config/Headlamp/plugins/upgrade-guardian
#   --no-systemd     Skip systemd unit installation on Linux
#   --no-launchd     Skip LaunchAgent installation on macOS
#   --uninstall      Remove a previous installation

set -euo pipefail

PREFIX="/usr/local"
PLUGIN_DIR=""
INSTALL_SYSTEMD=1
INSTALL_LAUNCHD=1
UNINSTALL=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --prefix)      PREFIX="$2"; shift 2 ;;
    --plugin-dir)  PLUGIN_DIR="$2"; shift 2 ;;
    --no-systemd)  INSTALL_SYSTEMD=0; shift ;;
    --no-launchd)  INSTALL_LAUNCHD=0; shift ;;
    --uninstall)   UNINSTALL=1; shift ;;
    -h|--help)
      sed -n '2,16p' "$0"; exit 0 ;;
    *)
      echo "Unknown flag: $1" >&2; exit 2 ;;
  esac
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

OS="$(uname -s)"
case "$OS" in
  Linux)  PLATFORM=linux ;;
  Darwin) PLATFORM=darwin ;;
  *)      echo "Unsupported OS: $OS" >&2; exit 1 ;;
esac

# Default Headlamp plugin directory per OS.
if [ -z "$PLUGIN_DIR" ]; then
  if [ "$PLATFORM" = "darwin" ]; then
    PLUGIN_DIR="$HOME/Library/Application Support/Headlamp/plugins/upgrade-guardian"
  else
    PLUGIN_DIR="$HOME/.config/Headlamp/plugins/upgrade-guardian"
  fi
fi

BIN_TARGET="$PREFIX/bin/upgrade-guardian"

# ---- Uninstall path ---------------------------------------------------------
if [ "$UNINSTALL" -eq 1 ]; then
  echo ">> Removing $BIN_TARGET"
  sudo rm -f "$BIN_TARGET"
  echo ">> Removing $PREFIX/bin/upgrade-guardian-cli"
  sudo rm -f "$PREFIX/bin/upgrade-guardian-cli"
  echo ">> Removing $PLUGIN_DIR"
  rm -rf "$PLUGIN_DIR"
  if [ "$PLATFORM" = "linux" ]; then
    if systemctl --user is-enabled upgrade-guardian 2>/dev/null; then
      systemctl --user disable --now upgrade-guardian
      rm -f "$HOME/.config/systemd/user/upgrade-guardian.service"
    fi
  elif [ "$PLATFORM" = "darwin" ]; then
    LA="$HOME/Library/LaunchAgents/com.upgrade-guardian.plist"
    if [ -f "$LA" ]; then
      launchctl unload "$LA" 2>/dev/null || true
      rm -f "$LA"
    fi
  fi
  echo ">> Uninstalled."
  exit 0
fi

# ---- Install ----------------------------------------------------------------
echo ">> Installing upgrade-guardian"
echo "   binary    -> $BIN_TARGET"
echo "   plugin    -> $PLUGIN_DIR"
echo "   platform  -> $PLATFORM"
echo ""

# 1. Binaries (server and CLI).
if [ ! -f "$ROOT/bin/upgrade-guardian" ]; then
  echo "error: bin/upgrade-guardian not found in $ROOT" >&2
  exit 1
fi
sudo install -m 0755 "$ROOT/bin/upgrade-guardian" "$BIN_TARGET"

if [ -f "$ROOT/bin/upgrade-guardian-cli" ]; then
  sudo install -m 0755 "$ROOT/bin/upgrade-guardian-cli" "$PREFIX/bin/upgrade-guardian-cli"
fi

# 2. Headlamp plugin.
if [ ! -f "$ROOT/plugin/main.js" ]; then
  echo "error: plugin/main.js not found in $ROOT" >&2
  exit 1
fi
mkdir -p "$PLUGIN_DIR"
install -m 0644 "$ROOT/plugin/main.js" "$PLUGIN_DIR/main.js"
if [ -f "$ROOT/plugin/package.json" ]; then
  install -m 0644 "$ROOT/plugin/package.json" "$PLUGIN_DIR/package.json"
fi

# 3. Service unit (optional).
if [ "$PLATFORM" = "linux" ] && [ "$INSTALL_SYSTEMD" -eq 1 ] && [ -f "$ROOT/scripts/systemd/upgrade-guardian.service" ]; then
  UNIT_DIR="$HOME/.config/systemd/user"
  mkdir -p "$UNIT_DIR"
  # Substitute BIN_PATH placeholder.
  sed "s|__BIN_PATH__|$BIN_TARGET|g" "$ROOT/scripts/systemd/upgrade-guardian.service" > "$UNIT_DIR/upgrade-guardian.service"
  systemctl --user daemon-reload
  systemctl --user enable --now upgrade-guardian
  echo ">> Started systemd user unit upgrade-guardian"
elif [ "$PLATFORM" = "darwin" ] && [ "$INSTALL_LAUNCHD" -eq 1 ] && [ -f "$ROOT/scripts/launchd/com.upgrade-guardian.plist" ]; then
  LA_DIR="$HOME/Library/LaunchAgents"
  mkdir -p "$LA_DIR"
  sed -e "s|__BIN_PATH__|$BIN_TARGET|g" -e "s|__USER__|$USER|g" "$ROOT/scripts/launchd/com.upgrade-guardian.plist" > "$LA_DIR/com.upgrade-guardian.plist"
  launchctl unload "$LA_DIR/com.upgrade-guardian.plist" 2>/dev/null || true
  launchctl load "$LA_DIR/com.upgrade-guardian.plist"
  echo ">> Loaded LaunchAgent com.upgrade-guardian"
fi

echo ""
echo ">> Done. Verify with:"
echo "     curl http://localhost:8090/healthz"
echo "   Then open Headlamp and look for the 'Upgrade Guardian' page."
