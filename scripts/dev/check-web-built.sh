#!/usr/bin/env bash
# 判断 web/dist 是不是**真的**构建过。
#
# 为什么不能只看「index.html 里有没有 /assets/」：入库的占位页在**注释里**就写着
# `dist/assets/*`（它在解释构建方式），所以那个判断会把占位页也当成构建产物 ——
# 于是交叉编译出来一个发「前端未构建」页面的二进制，而浏览器还缓存着旧前端，
# 看起来像「UI 没更新」，白排查一阵（2026-09-22 真踩过）。
#
# 可靠判据：
#   - 占位页的标题是「LMBY · 前端未构建」；
#   - 真产物里有 <script … src="/assets/…">。
#
# 用法：bash scripts/dev/check-web-built.sh [web/dist/index.html]
set -uo pipefail

idx=${1:-web/dist/index.html}

if [ ! -f "$idx" ]; then
  echo "FAIL: 找不到 $idx" >&2
  exit 1
fi

if grep -q '前端未构建' "$idx"; then
  echo "FAIL: $idx 还是占位页 —— 先跑 cd web && npm run build，再打包/交叉编译" >&2
  exit 1
fi

if ! grep -qE '<script[^>]+src="/assets/' "$idx"; then
  echo "FAIL: $idx 既不是占位页，也没有引用 /assets/ 下的脚本 —— 构建产物不完整？" >&2
  exit 1
fi

echo "ok: $idx 是真构建产物（引用了 /assets/ 下的脚本）"
