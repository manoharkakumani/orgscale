#!/usr/bin/env bash
# Writes the files a release ships from the binary it builds, so they match
# that version. release-assets/ is gitignored: release-check.sh still sees a
# clean tree.
set -euo pipefail

rm -rf release-assets
mkdir release-assets
go build -o release-assets/orgscale ./cmd/orgscale
release-assets/orgscale generate config > release-assets/config-example.yaml
release-assets/orgscale generate nixos-options --release > release-assets/orgscale-nixos-options.nix
rm release-assets/orgscale
