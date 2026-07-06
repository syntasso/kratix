#!/usr/bin/env bash
# Installs the CLIs needed for the Tilt-based local dev environment.
# Host use only; installs current Homebrew versions. For a pinned, reproducible
# set (the source of truth for CLI versions) use the devcontainer instead —
# .devcontainer/Dockerfile pins each tool via an ARG.
set -euo pipefail

if ! command -v brew >/dev/null; then
  echo "Homebrew required: https://brew.sh" >&2
  exit 1
fi

# tilt-dev tap provides tilt + ctlptl
brew tap tilt-dev/tap 2>/dev/null || true

pkgs=(tilt-dev/tap/tilt tilt-dev/tap/ctlptl ko kind kubectl helm yq)
for p in "${pkgs[@]}"; do
  name="${p##*/}"
  if command -v "$name" >/dev/null; then
    echo "✓ $name already installed"
  else
    echo "Installing $name..."
    brew install "$p"
  fi
done

echo "All dev tools installed:"
tilt version && ctlptl version && ko version && kind version && kubectl version --client && helm version --short && yq --version
