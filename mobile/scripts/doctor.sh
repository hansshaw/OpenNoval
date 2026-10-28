#!/usr/bin/env bash
set -euo pipefail
source "$(dirname -- "$0")/env.sh"

missing=0
check_command() {
  if command -v "$1" >/dev/null 2>&1; then
    printf 'OK      %s: %s\n' "$1" "$(command -v "$1")"
  else
    printf 'MISSING %s\n' "$1" >&2
    missing=1
  fi
}
check_file() {
  if [[ -f "$2" ]]; then
    printf 'OK      %s\n' "$1"
  else
    printf 'MISSING %s: %s\n' "$1" "$2" >&2
    missing=1
  fi
}

for binary in go java javac gradle gomobile gobind; do
  check_command "$binary"
done
check_file 'Android SDK 35' "$ANDROID_HOME/platforms/android-35/android.jar"
check_file 'Android Build Tools 35.0.0' "$ANDROID_HOME/build-tools/35.0.0/aapt2"
check_file "Android NDK $AINOVEL_NDK_VERSION" "$ANDROID_NDK_HOME/source.properties"

if command -v go >/dev/null 2>&1; then
  go version
fi
if command -v java >/dev/null 2>&1; then
  java -version 2>&1 | head -n 1
fi

if (( missing )); then
  cat >&2 <<'EOF'

Install Java 17, Gradle 8.11.1 and Android command-line tools, then set JAVA_HOME
and ANDROID_HOME. Run mobile/scripts/setup-go-tools.sh for the Go bindings.
Review and accept the Android SDK licenses yourself with sdkmanager --licenses,
then install these packages:
  sdkmanager "platforms;android-35" "build-tools;35.0.0" "ndk;28.0.13004108" "platform-tools"
See mobile/README.md for the full Android build instructions.
EOF
  exit 1
fi
