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
JNI_OUT="libs/jni"

echo "[build] 解压 echproxy.aar 的 .so 到 $JNI_OUT"
rm -rf "$JNI_OUT" && mkdir -p "$JNI_OUT"
if [ -f "$AAR_IN" ]; then
  unzip -o -q "$AAR_IN" "jni/*" "classes.jar" -d "$JNI_OUT.tmp"
  # ech-android 只需编译期看到 Go binding；最终交付仍保留原 echproxy.aar。
  mv "$JNI_OUT.tmp/classes.jar" "$BINDING_JAR"
  # unzip 会把 jni/ 解到 jni.tmp/jni/*，移到 libs/jni 下让 sourceSets 识别
  if [ -d "$JNI_OUT.tmp/jni" ]; then
    mv "$JNI_OUT.tmp/jni"/* "$JNI_OUT"/
  fi
  rm -rf "$JNI_OUT.tmp"
  echo "[build] .so 列表:"; find "$JNI_OUT" -name '*.so'
else
  echo "[build][WARN] $AAR_IN 不存在，仅编译 Kotlin 层（缺少 Go .so）"
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
