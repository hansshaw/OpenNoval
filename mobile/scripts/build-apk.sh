#!/usr/bin/env bash
set -euo pipefail
source "$(dirname -- "$0")/env.sh"

variant="${1:-debug}"
if [[ "$variant" != debug && "$variant" != release ]]; then
  printf 'Usage: %s [debug|release]\n' "$0" >&2
  exit 2
fi
"$AINOVEL_ROOT/mobile/scripts/doctor.sh"
mkdir -p "$AINOVEL_ROOT/mobile/app/libs" "$AINOVEL_ROOT/output/android"

# The small nested module keeps gomobile dependencies out of the desktop server.
cd "$AINOVEL_ROOT/mobile/bind"
go mod download
gomobile bind \
  -target="${AINOVEL_ANDROID_TARGETS:-android/arm64,android/arm,android/amd64}" \
  -androidapi 26 \
  -javapkg org.ainovel.mobile \
  -trimpath \
  -ldflags='-s -w' \
  -o "$AINOVEL_ROOT/mobile/app/libs/ainovel.aar" .

cd "$AINOVEL_ROOT/mobile"
gradle_args=(--no-daemon)
if [[ -n "${AINOVEL_GRADLE_INIT_SCRIPT:-}" ]]; then
  gradle_args+=(--init-script "$AINOVEL_GRADLE_INIT_SCRIPT")
fi
if [[ "$variant" == debug ]]; then
  gradle "${gradle_args[@]}" assembleDebug
  cp app/build/outputs/apk/debug/app-debug.apk "$AINOVEL_ROOT/output/android/AInovel-debug.apk"
  printf '\nAPK: %s/output/android/AInovel-debug.apk\n' "$AINOVEL_ROOT"
else
  gradle "${gradle_args[@]}" assembleRelease lintRelease
  cp app/build/outputs/apk/release/*.apk "$AINOVEL_ROOT/output/android/"
  printf '\nRelease APK files: %s/output/android/\n' "$AINOVEL_ROOT"
  printf 'An unsigned release APK must be signed before it can be installed.\n'
fi
