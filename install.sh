#!/usr/bin/env bash
set -euo pipefail

# Colors
G='\033[0;32m'; C='\033[0;36m'; Y='\033[1;33m'; R='\033[0;31m'; B='\033[1m'; NC='\033[0m'
ok()   { echo -e "${G}✓${NC} $1"; }
info() { echo -e "${C}→${NC} $1"; }
warn() { echo -e "${Y}⚠${NC}  $1"; }
die()  { echo -e "${R}✗${NC} $1" >&2; exit 1; }
bold() { echo -e "${B}$1${NC}"; }

REPO="bradygerndt/project-brain"
BIN_DIR="${BRAIN_BIN_DIR:-$HOME/.local/bin}"
BRAIN_BIN="$BIN_DIR/brain"

echo ""
bold "  project-brain installer"
echo ""

# ── Requirements ─────────────────────────────────────────────────────────────
command -v curl &>/dev/null || die "curl is required to install project-brain."
command -v tar &>/dev/null || die "tar is required to install project-brain."

# ── Detect platform ──────────────────────────────────────────────────────────
case "$(uname -s)" in
  Linux)  OS=linux ;;
  Darwin) OS=darwin ;;
  *) die "Unsupported OS: $(uname -s). See https://github.com/$REPO/releases for manual builds." ;;
esac

case "$(uname -m)" in
  x86_64|amd64)  ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) die "Unsupported architecture: $(uname -m). See https://github.com/$REPO/releases for manual builds." ;;
esac
info "Detected $OS/$ARCH"

# ── Resolve latest release ───────────────────────────────────────────────────
info "Looking up latest release…"
TAG=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
  | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')
[[ -n "$TAG" ]] || die "Couldn't resolve the latest release. Check https://github.com/$REPO/releases"
info "Latest release: $TAG"

# ── Download the matching binary ─────────────────────────────────────────────
ASSET="brain_${OS}_${ARCH}.tar.gz"
URL="https://github.com/$REPO/releases/download/$TAG/$ASSET"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

info "Downloading $ASSET…"
curl -fsSL "$URL" -o "$TMP_DIR/$ASSET" || die "Download failed: $URL"
tar -xzf "$TMP_DIR/$ASSET" -C "$TMP_DIR" brain

mkdir -p "$BIN_DIR"
mv "$TMP_DIR/brain" "$BRAIN_BIN"
chmod +x "$BRAIN_BIN"
ok "brain → $BRAIN_BIN"

# Sanity check
if ! "$BRAIN_BIN" version &>/dev/null; then
  die "Installed binary failed to run — see $BRAIN_BIN"
fi

# ── PATH check ───────────────────────────────────────────────────────────────
if [[ ":$PATH:" != *":$BIN_DIR:"* ]]; then
  warn "$BIN_DIR is not in your PATH yet"
  echo ""

  SHELL_RC=""
  if [[ -f "$HOME/.zshrc" ]]; then SHELL_RC="$HOME/.zshrc"
  elif [[ -f "$HOME/.bashrc" ]]; then SHELL_RC="$HOME/.bashrc"
  fi

  if [[ -n "$SHELL_RC" ]]; then
    # stdin isn't a TTY under `curl | bash` (it's occupied by the piped
    # script itself), so `read` can't prompt there — default to Y rather
    # than let a failed read abort the rest of the install under set -e.
    if [[ -t 0 ]]; then
      read -rp "  Add it to $SHELL_RC automatically? [Y/n] " REPLY
      REPLY="${REPLY:-Y}"
    else
      REPLY="Y"
      info "Non-interactive install — adding to PATH automatically"
    fi
    if [[ "$REPLY" =~ ^[Yy]$ ]]; then
      echo '' >> "$SHELL_RC"
      echo '# project-brain CLI' >> "$SHELL_RC"
      echo "export PATH=\"\$HOME/.local/bin:\$PATH\"" >> "$SHELL_RC"
      ok "Added to $SHELL_RC"
      warn "Run: source $SHELL_RC  (or open a new terminal)"
    else
      echo ""
      echo "  Add this line to your shell config manually:"
      echo -e "  ${C}export PATH=\"\$HOME/.local/bin:\$PATH\"${NC}"
      echo ""
    fi
  else
    echo "  Add this to your shell config:"
    echo -e "  ${C}export PATH=\"\$HOME/.local/bin:\$PATH\"${NC}"
    echo ""
  fi
else
  ok "$BIN_DIR is already in PATH"
fi

# ── ~/.config/brain/.env ─────────────────────────────────────────────────────
# No local checkout exists anymore, so this can't live next to a compose
# file — brain itself loads it from here before creating any container.
CONFIG_DIR="${BRAIN_CONFIG_DIR:-$HOME/.config/brain}"
ENV_FILE="$CONFIG_DIR/.env"
if [[ ! -f "$ENV_FILE" ]]; then
  mkdir -p "$CONFIG_DIR"
  printf 'ANTHROPIC_API_KEY=\n' > "$ENV_FILE"
  warn ".env created — edit $ENV_FILE and add your ANTHROPIC_API_KEY"
  warn "(only needed for the memory_extract tool)"
else
  ok ".env exists"
fi

# ── Docker ───────────────────────────────────────────────────────────────────
if ! command -v docker &>/dev/null; then
  warn "Docker not found"
  echo ""
  echo "  Option A — Enable Docker Desktop WSL2 integration:"
  echo "    Docker Desktop → Settings → Resources → WSL Integration"
  echo ""
  echo "  Option B — Install Docker Engine directly in WSL2:"
  echo "    curl -fsSL https://get.docker.com | sh"
  echo "    sudo usermod -aG docker \$USER && newgrp docker"
  echo ""
else
  DOCKER_VER=$(docker --version 2>/dev/null | grep -oP '\d+\.\d+\.\d+' | head -1 || echo "found")
  ok "Docker $DOCKER_VER"
fi

# ── Tailscale ────────────────────────────────────────────────────────────────
if ! command -v tailscale &>/dev/null; then
  warn "Tailscale not found (optional — needed for cross-device access)"
  echo "    curl -fsSL https://tailscale.com/install.sh | sh"
  echo "    sudo systemctl enable --now tailscaled && sudo tailscale up"
  echo ""
fi

# ── Done ─────────────────────────────────────────────────────────────────────
echo ""
ok "Installation complete!"
echo ""
echo "  Next steps:"
[[ ":$PATH:" != *":$BIN_DIR:"* ]] && echo "    1. source your shell config (see above)"
echo "    brain help                  see all commands"
echo "    brain ps                    check instance status"
echo "    brain start                 start all instances (requires Docker)"
echo "    brain config                get MCP config for Claude Code"
echo ""
