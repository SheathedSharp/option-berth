#!/usr/bin/env bash
#
# 把原生客户端打成一个 .app。
#
# 只用 CommandLineTools 里的 swiftc —— 不需要 Xcode（.app 的骨架自己拼，
# Info.plist 自己写）。这样一个 600 KB 的二进制 + 随包的字体，比 200 MB 的
# Electron 运行时好交代得多，代价是每个平台的窗口各写一份。
#
# 用法：
#   ./build.sh            # 构建到 build/OptionBerth.app
#   ./build.sh --run      # 构建完直接打开
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo="$(cd "$here/../.." && pwd)"
out="$here/build"
app="$out/OptionBerth.app"
version="${VERSION:-}"
if [[ -z "$version" && -f "$repo/VERSION" ]]; then
  version="$(<"$repo/VERSION")"
fi
version="${version#v}"
if [[ ! "$version" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo "invalid version: $version (expected MAJOR.MINOR.PATCH, for example 0.1.0)" >&2
  exit 2
fi
build_number="${BUILD_NUMBER:-$(git -C "$repo" rev-list --count HEAD 2>/dev/null || printf '0')}"
commit="$(git -C "$repo" rev-parse HEAD 2>/dev/null || printf unknown)"
if [[ ! "$build_number" =~ ^[0-9]+$ ]]; then
  echo "invalid build number: $build_number" >&2
  exit 2
fi
# 目标定在 14.0：这台机器上是 26，但没必要把版本要求抬到别人装不了。
target="arm64-apple-macosx14.0"
sdk="$(xcrun --show-sdk-path)"

rm -rf "$app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources/Fonts"

echo "编译 Sources/*.swift → $app"
swiftc \
  -swift-version 5 \
  -parse-as-library \
  -O \
  -target "$target" \
  -sdk "$sdk" \
  -o "$app/Contents/MacOS/OptionBerth" \
  "$here"/Sources/*.swift

sed \
  -e "s/@VERSION@/$version/g" \
  -e "s/__COMMIT__/$commit/g" \
  -e "s/@BUILD_NUMBER@/$build_number/g" \
  "$here/Info.plist.in" > "$app/Contents/Info.plist"

# 等宽字体跟应用一起走：不要求用户先装字体，也不去猜系统里那版是哪一个。
# 许可是 SIL OFL 1.1，要求随附许可原文 —— Fonts/LICENSE-Monaspace.txt 就是为这条留的。
cp "$here"/Fonts/*.otf "$app/Contents/Resources/Fonts/"
cp "$here/Fonts/LICENSE-Monaspace.txt" "$app/Contents/Resources/Fonts/"

# 图标从标志生成，不存手画的图 —— 这样 Dock 里的形状和界面里的形状不可能走开。
# iconutil 只有 macOS 上有；生成不出来就跳过，不影响应用本身。
iconset="$out/AppIcon.iconset"
if "$app/Contents/MacOS/OptionBerth" --write-icon "$iconset" >/dev/null 2>&1 \
  && iconutil -c icns "$iconset" -o "$app/Contents/Resources/AppIcon.icns" 2>/dev/null; then
  echo "已生成 AppIcon.icns（从标志画出各档尺寸）"
else
  echo "跳过图标生成（应用本身不受影响）"
fi
rm -rf "$iconset"

# 本地跑的包不需要开发者证书，但要有个签名，否则某些系统 API 会拒绝它。
codesign --force --sign - "$app" >/dev/null 2>&1 \
  && echo "已做 ad-hoc 签名" \
  || echo "跳过签名（本地运行不受影响）"

echo "版本：${version}（build ${build_number}）"
echo "好了：$app"
if [ "${1:-}" = "--run" ]; then
  open "$app"
fi
