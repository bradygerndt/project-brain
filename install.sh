#!/usr/bin/env bash
set -euo pipefail

# Colors
G='\033[0;32m'; C='\033[0;36m'; Y='\033[1;33m'; R='\033[0;31m'; B='\033[1m'; NC='\033[0m'
ok()   { echo -e "${G}✓${NC} $1"; }
info() { echo -e "${C}→${NC} $1"; }
warn() { echo -e "${Y}⚠${NC}  $1"; }
die()  { echo -e "${R}✗${NC} $1" >&2; exit 1; }
bold() { echo -e "${B}$1${NC}"; }

TARBALL_URL="https://github.com/bradygerndt/project-brain/archive/refs/heads/main.tar.gz"
INSTALL_DIR="${BRAIN_INSTALL_DIR:-$HOME/project-brain}"

if [[ -n "${BASH_SOURCE[0]:-}" && -f "${BASH_SOURCE[0]}" ]]; then
  SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
else
  SCRIPT_DIR=""
fi
BIN_DIR="${BRAIN_BIN_DIR:-$HOME/.local/bin}"
BRAIN_LINK="$BIN_DIR/brain"

echo ""
bold "  project-brain installer"
echo ""

# ── Remote bootstrap ─────────────────────────────────────────────────────────
# Ran via `curl | bash` — there's no local checkout yet, so fetch the source
# as a tarball (no git required) and re-exec this same script from inside it.
# Re-running this later re-fetches and overlays the source, updating the code
# in place — .env, data/, and node_modules/ are gitignored so they're never
# in the tarball and are left untouched.
if [[ -z "$SCRIPT_DIR" || ! -f "$SCRIPT_DIR/package.json" ]]; then
  if ! command -v curl &>/dev/null; then die "curl is required to install project-brain."; fi
  if ! command -v tar &>/dev/null; then die "tar is required to install project-brain."; fi

  info "Downloading project-brain into $INSTALL_DIR…"
  TMP_DIR="$(mktemp -d)"
  trap 'rm -rf "$TMP_DIR"' EXIT
  curl -fsSL "$TARBALL_URL" | tar -xz --strip-components=1 -C "$TMP_DIR"
  mkdir -p "$INSTALL_DIR"
  cp -a "$TMP_DIR"/. "$INSTALL_DIR"/

  exec bash "$INSTALL_DIR/install.sh"
fi

# ── Node.js ──────────────────────────────────────────────────────────────────
if ! command -v node &>/dev/null; then
  die "Node.js is required but not found. Install it via nvm or https://nodejs.org"
fi
NODE_VER=$(node -e "process.stdout.write(process.versions.node)")
info "Node.js $NODE_VER"

# ── Dependencies ──────────────────────────────────────────────────────────────
info "Installing npm dependencies…"
cd "$SCRIPT_DIR"
npm install --silent
ok "Dependencies ready"

# ── Symlink ──────────────────────────────────────────────────────────────────
mkdir -p "$BIN_DIR"
chmod +x "$SCRIPT_DIR/bin/brain.js"
ln -sf "$SCRIPT_DIR/bin/brain.js" "$BRAIN_LINK"
ok "brain → $BRAIN_LINK"

# Verify the symlink actually resolves to the right place
RESOLVED=$(node -e "import('node:url').then(u=>import('node:path').then(p=>console.log(p.resolve(p.dirname(u.fileURLToPath(import.meta.url)),'..')))).catch(()=>{})" \
  --input-type=module < /dev/null 2>/dev/null || true)
# Quick sanity check: run the binary through the symlink
if ! "$BRAIN_LINK" help &>/dev/null; then
  warn "Symlink test failed — falling back to wrapper script"
  cat > "$BRAIN_LINK" <<WRAPPER
#!/usr/bin/env bash
exec node "$SCRIPT_DIR/bin/brain.js" "\$@"
WRAPPER
  chmod +x "$BRAIN_LINK"
  ok "Wrapper script installed at $BRAIN_LINK"
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
    read -rp "  Add it to $SHELL_RC automatically? [Y/n] " REPLY
    REPLY="${REPLY:-Y}"
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

# ── .env ─────────────────────────────────────────────────────────────────────
ENV_FILE="$SCRIPT_DIR/.env"
if [[ ! -f "$ENV_FILE" ]]; then
  cp "$SCRIPT_DIR/.env.example" "$ENV_FILE"
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
