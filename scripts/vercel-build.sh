#!/bin/sh
set -eu

# The Vercel Go runtime supplies its chosen Go toolchain, Linux target and
# output path. Build from the module root so internal imports and local Go
# module replacements work exactly as they do for the local control plane.
cd "$(dirname "$0")/.."
npm --prefix ui ci
make embed-ui
output_file=${VERCEL_OUTPUT_FILE:-bin/afs-control-plane}
mkdir -p "$(dirname "$output_file")"
# Git-connected builds may retain an incomplete .git directory after Vercel
# applies .vercelignore. Deployment metadata already records the commit.
go build -buildvcs=false -trimpath -o "$output_file" ./cmd/afs-control-plane
