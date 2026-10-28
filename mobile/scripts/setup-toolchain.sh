#!/usr/bin/env bash
# Install the open-source Java/Gradle/Go build tools without changing the host.
# Android SDK/NDK licenses are handled separately by the developer in sdkmanager.
set -euo pipefail
source "$(dirname -- "$0")/env.sh"

if [[ "$(uname -s)" != Linux || "$(uname -m)" != x86_64 ]]; then
  printf 'This bootstrap supports Linux x86_64. Install Java 17 and Gradle 8.11.1 on your platform, then run setup-go-tools.sh.\n' >&2
  exit 1
fi
mkdir -p "$AINOVEL_TOOLCHAIN/downloads"

download_verified() {
  local url="$1" destination="$2" checksum="$3"
  if [[ -f "$destination" ]] && printf '%s  %s\n' "$checksum" "$destination" | sha256sum --check --status; then
    return
  fi
  curl --fail --location --retry 3 "$url" --output "$destination.part"
  printf '%s  %s\n' "$checksum" "$destination.part" | sha256sum --check
  mv "$destination.part" "$destination"
}

if ! command -v javac >/dev/null 2>&1; then
  jdk_archive="$AINOVEL_TOOLCHAIN/downloads/jdk17.tar.gz"
  download_verified \
    "${AINOVEL_JDK_DOWNLOAD_URL:-https://github.com/adoptium/temurin17-binaries/releases/download/jdk-17.0.21%2B9/OpenJDK17U-jdk_x64_linux_hotspot_17.0.21_9.tar.gz}" \
    "$jdk_archive" \
    '8b9b611d917e3dfd8419d3566b5949226f69b052fed7e109c1368add1dfe8711'
  tar -xzf "$jdk_archive" -C "$AINOVEL_TOOLCHAIN"
  ln -sfn 'jdk-17.0.21+9' "$AINOVEL_TOOLCHAIN/jdk"
fi

if [[ ! -x "$AINOVEL_TOOLCHAIN/gradle-8.11.1/bin/gradle" ]]; then
  gradle_archive="$AINOVEL_TOOLCHAIN/downloads/gradle-8.11.1-bin.zip"
  download_verified \
    "${AINOVEL_GRADLE_DOWNLOAD_URL:-https://services.gradle.org/distributions/gradle-8.11.1-bin.zip}" \
    "$gradle_archive" \
    'f397b287023acdba1e9f6fc5ea72d22dd63669d59ed4a289a29b1a76eee151c6'
  unzip -q "$gradle_archive" -d "$AINOVEL_TOOLCHAIN"
fi
"$AINOVEL_ROOT/mobile/scripts/setup-go-tools.sh"
printf '\nJava/Gradle/Go tools are ready. Android SDK and NDK must also be installed.\n'
printf 'Run mobile/scripts/doctor.sh to see the remaining prerequisites.\n'
