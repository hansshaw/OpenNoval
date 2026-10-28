#!/usr/bin/env bash
set -euo pipefail
source "$(dirname -- "$0")/env.sh"

input="${1:-$AINOVEL_ROOT/mobile/app/build/outputs/apk/release/app-release-unsigned.apk}"
signing_dir="${AINOVEL_SIGNING_DIR:-$HOME/.local/share/ainovel/android-signing}"
build_tools="$ANDROID_HOME/build-tools/35.0.0"
if [[ ! -f "$input" ]]; then
  printf 'Missing unsigned release APK: %s\n' "$input" >&2
  exit 1
fi
if [[ ! -f "$signing_dir/ainovel-release.p12" || ! -f "$signing_dir/keystore-password" ]]; then
  printf 'Missing persistent release key. Run: source mobile/scripts/env.sh && python3 mobile/scripts/init-signing-key.py\n' >&2
  exit 1
fi
version="$("$build_tools/aapt" dump badging "$input" | sed -n "s/^package:.*versionName='\([^']*\)'.*/\1/p")"
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([-+][A-Za-z0-9.-]+)?$ ]]; then
  printf 'Invalid APK version: %s\n' "$version" >&2
  exit 1
fi
destination="${2:-$AINOVEL_ROOT/output/android/ainovel-android-$version-universal.apk}"
mkdir -p "$AINOVEL_ROOT/output/android" "$(dirname -- "$destination")"
aligned="$(mktemp "$AINOVEL_ROOT/output/android/aligned-XXXXXX.apk")"
trap 'rm -f "$aligned"' EXIT
"$build_tools/zipalign" -f -P 16 4 "$input" "$aligned"
"$build_tools/apksigner" sign \
  --ks "$signing_dir/ainovel-release.p12" --ks-key-alias ainovel-android-release \
  --ks-pass "file:$signing_dir/keystore-password" \
  --v4-signing-enabled false --out "$destination" "$aligned"
"$build_tools/zipalign" -c -P 16 4 "$destination"
"$build_tools/apksigner" verify --verbose --print-certs "$destination"
printf '\nSigned release APK: %s\n' "$destination"
