#!/usr/bin/env bash
# 构建 ech-android.aar（Kotlin 封装层 + Go 核心 .so 合并为单一 aar）
#
# 前置：
#   1) 已用 gomobile 编出 echproxy.aar（CI build-aar.yml 产物）放在 ./libs/echproxy.aar
#   2) 本机有 Android SDK + Gradle
#
# 产出：ech-android.aar（含 Kotlin 封装 + jniLibs/*.so + echproxy Java 绑定类）
set -euo pipefail

MODULE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$MODULE_DIR"

AAR_IN="libs/echproxy.aar"
BINDING_JAR="libs/echproxy-classes.jar"

echo "[build] 提取 echproxy.aar 的 Go binding classes.jar（JNI 只保留在 echproxy.aar）"
rm -f "$BINDING_JAR"
if [ -f "$AAR_IN" ]; then
  unzip -p "$AAR_IN" classes.jar > "$BINDING_JAR"
  test -s "$BINDING_JAR"
else
  echo "[build][ERROR] 缺少 $AAR_IN" >&2
  exit 1
fi

echo "[build] gradle assembleRelease"
gradle assembleRelease --no-daemon

OUT=$(find . -path '*/build/outputs/aar/*.aar' | head -1)
if [ -n "$OUT" ]; then
  cp "$OUT" ech-android.aar
  echo "[build] 产出 ech-android.aar ($(du -h ech-android.aar | cut -f1))"
else
  echo "[build][ERROR] 未找到产物 aar" >&2
  exit 1
fi
