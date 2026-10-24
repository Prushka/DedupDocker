#!/usr/bin/env bash
set -euo pipefail

version="$(git describe --tags --dirty --always)"
docker buildx build --platform linux/amd64 -f Dockerfile \
  --tag "meinya/dedup:$version" \
  --tag meinya/dedup:latest --push .

printf '%s\n' "$version"
