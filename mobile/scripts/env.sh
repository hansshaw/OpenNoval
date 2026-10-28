#!/usr/bin/env bash
# Shared local defaults. Explicit environment variables always take precedence.
AINOVEL_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
export AINOVEL_ROOT
export AINOVEL_TOOLCHAIN="${AINOVEL_TOOLCHAIN:-$AINOVEL_ROOT/output/android-toolchain}"
export AINOVEL_MOBILE_VERSION="v0.0.0-20261011034528-197770444867"
export AINOVEL_NDK_VERSION="28.0.13004108"
export AINOVEL_GRADLE_VERSION="8.11.1"

if [[ -z "${JAVA_HOME:-}" && -x "$AINOVEL_TOOLCHAIN/jdk/bin/java" ]]; then
  export JAVA_HOME="$AINOVEL_TOOLCHAIN/jdk"
fi
if [[ -n "${JAVA_HOME:-}" ]]; then
  export PATH="$JAVA_HOME/bin:$PATH"
fi

if [[ -z "${ANDROID_HOME:-}" ]]; then
  if [[ -n "${ANDROID_SDK_ROOT:-}" ]]; then
    export ANDROID_HOME="$ANDROID_SDK_ROOT"
  elif [[ -d "$HOME/Android/Sdk" ]]; then
    export ANDROID_HOME="$HOME/Android/Sdk"
  else
    export ANDROID_HOME="$AINOVEL_TOOLCHAIN/sdk"
  fi
fi
export ANDROID_NDK_HOME="${ANDROID_NDK_HOME:-$ANDROID_HOME/ndk/$AINOVEL_NDK_VERSION}"
export PATH="$AINOVEL_TOOLCHAIN/bin:$AINOVEL_TOOLCHAIN/gradle-$AINOVEL_GRADLE_VERSION/bin:$ANDROID_HOME/cmdline-tools/latest/bin:$ANDROID_HOME/platform-tools:$PATH"
