#!/usr/bin/env bash
set -euo pipefail
source "$(dirname -- "$0")/env.sh"
mkdir -p "$AINOVEL_TOOLCHAIN/bin"
GOBIN="$AINOVEL_TOOLCHAIN/bin" go install "golang.org/x/mobile/cmd/gomobile@$AINOVEL_MOBILE_VERSION"
GOBIN="$AINOVEL_TOOLCHAIN/bin" go install "golang.org/x/mobile/cmd/gobind@$AINOVEL_MOBILE_VERSION"
printf 'Installed gomobile and gobind %s in %s/bin\n' "$AINOVEL_MOBILE_VERSION" "$AINOVEL_TOOLCHAIN"
