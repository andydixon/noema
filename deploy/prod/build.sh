#!/usr/bin/env bash
# Build the Noema image and restart the systemd service. Run from anywhere.
set -euo pipefail
cd "$(dirname "$0")/../.."
VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo dev)
docker build -f deploy/Dockerfile --build-arg VERSION="$VERSION" -t noema:"$VERSION" -t noema:latest .
sudo systemctl restart noema
sleep 3
systemctl --no-pager --lines=5 status noema
