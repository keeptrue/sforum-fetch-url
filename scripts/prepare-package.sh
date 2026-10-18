#!/usr/bin/env bash
# 构建本机平台的插件二进制、刷新包摘要并跑一遍宿主契约校验。
# 用法：scripts/prepare-package.sh [extension-root]（默认 plugins/sforum.fetch-url）
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PKG="${1:-$ROOT/plugins/sforum.fetch-url}"
SFORUM_DIR="${SFORUM_DIR:-$(cd "$ROOT/.." && pwd)/SForum}"

if [ ! -d "$SFORUM_DIR/apps/api" ]; then
  echo "找不到 SForum 检出目录：$SFORUM_DIR" >&2
  echo "可用 SFORUM_DIR=/path/to/SForum 指定，或把本仓库与 SForum 放在同一父目录下。" >&2
  exit 1
fi

echo "==> 构建 backend/plugin"
# -buildvcs=false：仓库变成 git 仓库后，Go 默认会把 revision/dirty 状态嵌进二进制，
# 于是同样的源码在不同提交上构建出不同摘要，每 commit 都要让站点重新授权一次。
# 摘要必须只由源码决定，这条 bootstrap 链上的一致做法也是显式关掉。
(cd "$PKG/backend" && go build -trimpath -buildvcs=false -o "$PKG/backend/plugin" ./)

echo "==> 刷新包摘要（extension digest --write）"
(cd "$SFORUM_DIR/apps/api" && go run ./cmd/sforum extension digest --write "$PKG")

echo "==> 校验清单（extension validate）"
(cd "$SFORUM_DIR/apps/api" && go run ./cmd/sforum extension validate "$PKG")

echo "==> 宿主契约检查（extension test）"
(cd "$SFORUM_DIR/apps/api" && go run ./cmd/sforum extension test "$PKG")

echo "完成：$PKG"
