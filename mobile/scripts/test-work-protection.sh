#!/usr/bin/env bash
# Deterministic JVM tests for the active-work power lease; no Android build.
set -euo pipefail
source "$(dirname -- "$0")/env.sh"

test_dir="$(mktemp -d "${TMPDIR:-/tmp}/ainovel-work-protection.XXXXXX")"
trap 'rm -rf -- "$test_dir"' EXIT
javac --release 17 -d "$test_dir" \
  "$AINOVEL_ROOT/mobile/app/src/main/java/org/ainovel/mobile/WorkProtection.java" \
  "$AINOVEL_ROOT/mobile/tests/org/ainovel/mobile/WorkProtectionTest.java"
java -cp "$test_dir" org.ainovel.mobile.WorkProtectionTest
