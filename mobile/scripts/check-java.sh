#!/usr/bin/env bash
# Compile Java sources against Android APIs without requiring an installed SDK.
# This checks source/API compatibility only, not APK resources, JNI or a device.
set -euo pipefail
source "$(dirname -- "$0")/env.sh"

android_jar="${1:-$AINOVEL_TOOLCHAIN/downloads/android-all.jar}"
if [[ ! -f "$android_jar" ]]; then
  printf 'Usage: %s /absolute/path/to/android-all-15-robolectric-13954326.jar\n' "$0" >&2
  printf 'The Android API jar must already exist; this check does not download it.\n' >&2
  exit 2
fi
android_jar="$(cd -- "$(dirname -- "$android_jar")" && pwd)/$(basename -- "$android_jar")"
for binary in javac gobind; do
  if ! command -v "$binary" >/dev/null 2>&1; then
    printf 'Missing %s. Install JDK 17 and run mobile/scripts/setup-go-tools.sh first.\n' "$binary" >&2
    exit 1
  fi
done

check_dir="$AINOVEL_TOOLCHAIN/java-source-check"
mkdir -p "$check_dir/generated" "$check_dir/classes" "$check_dir/stubs/org/ainovel/mobile"
# Keep generated Go code out of a repository-wide `go test ./...` traversal.
cat > "$check_dir/go.mod" <<'EOF'
module ainovel.local/java-source-check

go 1.26.0
EOF
cat > "$check_dir/stubs/org/ainovel/mobile/BuildConfig.java" <<'EOF'
package org.ainovel.mobile;
public final class BuildConfig { public static final boolean DEBUG = true; }
EOF
cat > "$check_dir/stubs/org/ainovel/mobile/R.java" <<'EOF'
package org.ainovel.mobile;
public final class R {
    public static final class drawable { public static final int ic_novel = 0x7f010000; }
}
EOF

cd "$AINOVEL_ROOT/mobile/bind"
# Fail if Go dependencies are not cached, rather than fetching during source QA.
GOPROXY=off GOSUMDB=off gobind -lang=go,java -javapkg org.ainovel.mobile -outdir "$check_dir/generated" .
mapfile -d '' java_sources < <(find "$AINOVEL_ROOT/mobile/app/src/main/java" "$check_dir/generated/java" "$check_dir/stubs" -name '*.java' -print0)
javac --release 17 -Xlint:deprecation -cp "$android_jar" -d "$check_dir/classes" "${java_sources[@]}"
printf '\nJava source/API compilation passed (including generated Go bindings).\n'
printf 'This does not validate resource generation, JNI linking, APK packaging, or device behavior.\n'
