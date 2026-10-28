#!/usr/bin/env bash
# Start a headless test device after the SDK image has been installed.
# This script does not accept licenses, install SDK packages, or erase an AVD.
set -euo pipefail
source "$(dirname -- "$0")/env.sh"

export ANDROID_AVD_HOME="${AINOVEL_AVD_HOME:-$AINOVEL_TOOLCHAIN/avd}"
avd_name="${AINOVEL_AVD_NAME:-ainovel-api35}"
image="system-images;android-35;google_apis;x86_64"
port="${AINOVEL_EMULATOR_PORT:-5554}"
serial="emulator-$port"
emulator="$ANDROID_HOME/emulator/emulator"
emulator_pid=""

if [[ ! -x "$emulator" || ! -f "$ANDROID_HOME/system-images/android-35/google_apis/x86_64/system.img" ]]; then
  printf 'Install emulator and %s with sdkmanager before running this script.\n' "$image" >&2
  exit 1
fi
if [[ ! -r /dev/kvm || ! -w /dev/kvm ]]; then
  printf 'The current user needs read/write access to /dev/kvm.\n' >&2
  exit 1
fi
mkdir -p "$ANDROID_AVD_HOME" "$AINOVEL_ROOT/output/android-smoke"
if [[ ! -f "$ANDROID_AVD_HOME/$avd_name.ini" ]]; then
  printf 'no\n' | avdmanager create avd --name "$avd_name" --package "$image" --device pixel
fi

if ! adb -s "$serial" get-state >/dev/null 2>&1; then
  nohup "$emulator" -avd "$avd_name" -port "$port" -accel on \
    -no-window -no-audio -no-boot-anim -no-snapshot -gpu swiftshader \
    -feature -Vulkan -skin 720x1280 -dpi-device 320 \
    -camera-back none -camera-front none \
    -lowram -memory "${AINOVEL_EMULATOR_MEMORY:-1536}" -cores 2 -netdelay none -netspeed full \
    >"$AINOVEL_ROOT/output/android-smoke/emulator.log" 2>&1 &
  emulator_pid="$!"
  printf '%s\n' "$emulator_pid" >"$AINOVEL_ROOT/output/android-smoke/emulator.pid"
fi

for attempt in {1..120}; do
  if [[ "$(adb -s "$serial" shell getprop sys.boot_completed 2>/dev/null | tr -d '\r')" == 1 ]]; then
    adb -s "$serial" shell wm density 320
    adb -s "$serial" shell input keyevent 82
    printf 'Ready: %s\n' "$serial"
    # Managed command runners may clean up child processes on shell exit.
    # Keep this shell attached when requested; other commands can still use adb.
    if [[ "${AINOVEL_EMULATOR_STAY_ALIVE:-0}" == 1 && -n "$emulator_pid" ]]; then
      wait "$emulator_pid"
    fi
    exit 0
  fi
  sleep 2
done
printf 'Emulator did not finish booting; inspect output/android-smoke/emulator.log.\n' >&2
exit 1
