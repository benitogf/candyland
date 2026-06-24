#!/usr/bin/env bash
# Manual release, the same shape as detritus: bump → tag → push; the tag-triggered
# workflow builds the standalone binaries and publishes the GitHub Release.
# Run it directly, or just ask Claude to "release candyland X.Y.Z".
#
#   scripts/release.sh 0.1.0
#
set -euo pipefail

VERSION="${1:-}"
if [ -z "$VERSION" ]; then
  echo "usage: scripts/release.sh X.Y.Z" >&2
  exit 1
fi
TAG="v${VERSION#v}"

# Must be on the default branch with a clean tree (release from merged code).
branch=$(git rev-parse --abbrev-ref HEAD)
if [ "$branch" != "main" ]; then
  echo "Release from 'main' (on '$branch'). Merge first, then release." >&2
  exit 1
fi
if [ -n "$(git status --porcelain)" ]; then
  echo "Working tree not clean — commit or stash first." >&2
  exit 1
fi
# Catch a tag that already exists locally OR on the remote before tagging, so the
# conflict surfaces here rather than as a rejected push later.
git fetch --tags --quiet origin
if git rev-parse "$TAG" >/dev/null 2>&1; then
  echo "Tag $TAG already exists." >&2
  exit 1
fi

git pull --ff-only origin main
git tag -a "$TAG" -m "candyland $TAG"
git push origin "$TAG"

echo "Tagged $TAG and pushed."
echo "The release workflow (.github/workflows/release.yml) builds the standalone"
echo "binaries (version injected via -ldflags) and publishes the GitHub Release."
echo "If you don't see a release run, the workflow may still be at ci/release.yml —"
echo "activate it once (see ci/README.md)."
