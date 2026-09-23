#!/usr/bin/env bash
# 「缓存与清理」的真库验收。
#
# 最要紧的不是「能清掉孤儿」，而是**清理的边界**：
#   - 库/条目还在的叠加层数据一个字节都不能动（那是刮削产物的备份，不是缓存）；
#   - 非管理员进不来；
#   - 图片缓存能清空，而清空之后服务照常（缓存可再生）。
#
# 脚本自己造孤儿目录（库 999999 / 条目 888888 保证不存在），不依赖环境里恰好有垃圾。
#
# 用法：LMBY_USER=devtest LMBY_PASS=口令 [DATA_DIR=/var/lib/lmby] bash scripts/dev/verify-maintenance.sh
set -uo pipefail

BASE="${BASE:-http://127.0.0.1:8099}"
DATA_DIR="${DATA_DIR:-/var/lib/lmby}"
USER="${LMBY_USER:-}"
PASS="${LMBY_PASS:-}"
SUFFIX=$$

if [[ -z "$USER" || -z "$PASS" ]]; then
  echo "用法：LMBY_USER=devtest LMBY_PASS=口令 bash scripts/dev/verify-maintenance.sh" >&2
  exit 2
fi

pass=0; fail=0
check() {
  if [[ "$2" == "$3" ]]; then printf '  ok   %s\n' "$1"; pass=$((pass + 1))
  else printf '  FAIL %s（期望 %q，实际 %q）\n' "$1" "$2" "$3"; fail=$((fail + 1)); fi
}
note() { printf '  --   %s\n' "$1"; }
json() { curl -s -H 'Content-Type: application/json' "$@"; }
code() { curl -s -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' "$@"; }

JAR=$(mktemp); JAR2=$(mktemp)
ORPHAN_DIR="$DATA_DIR/overlay/999999/888888"
trap 'rm -f "$JAR" "$JAR2"; rm -rf "$DATA_DIR/overlay/999999"' EXIT

echo "== 0. 权限 =="
check "管理员登录" 200 "$(code -b "$JAR" -c "$JAR" -X POST "$BASE/api/v1/auth/login" \
  -d "$(jq -nc --arg u "$USER" --arg p "$PASS" '{username:$u,password:$p}')")"
check "管理员能读维护信息" 200 "$(code -b "$JAR" "$BASE/api/v1/maintenance")"
TMPUSER="maint-probe-$SUFFIX"
json -b "$JAR" -X POST "$BASE/api/v1/users" \
  -d "$(jq -nc --arg u "$TMPUSER" '{username:$u,password:"maint-probe-pass-1"}')" >/dev/null
code -b "$JAR2" -c "$JAR2" -X POST "$BASE/api/v1/auth/login" \
  -d "$(jq -nc --arg u "$TMPUSER" '{username:$u,password:"maint-probe-pass-1"}')" >/dev/null
check "普通用户读维护信息 → 403" 403 "$(code -b "$JAR2" "$BASE/api/v1/maintenance")"
check "普通用户执行清理 → 403" 403 "$(code -b "$JAR2" -X POST "$BASE/api/v1/maintenance/clean" -d '{"images":true}')"

echo
echo "== 1. 造孤儿（库 999999 / 条目 888888，保证不存在） =="
mkdir -p "$ORPHAN_DIR" && printf 'junk' >"$ORPHAN_DIR/poster.jpg"
note "写到 $ORPHAN_DIR"
n=$(json -b "$JAR" "$BASE/api/v1/maintenance" | jq -r '.overlayOrphans.files // 0')
check "维护信息里的孤儿文件数 > 0" true "$([[ "${n:-0}" -gt 0 ]] && echo true || echo false)"

echo
echo "== 2. 找一个「真实存在」的条目，看它的叠加层数据会不会被动 =="
LIB=$(json -b "$JAR" "$BASE/api/v1/libraries" | jq -r '.libraries[0].id // empty')
ITEM=$(json -b "$JAR" "$BASE/api/v1/libraries/$LIB/items?limit=1" | jq -r '.items[0].id // empty')
LIVE_DIR="$DATA_DIR/overlay/$LIB/$ITEM"
if [[ -n "$LIB" && -n "$ITEM" ]]; then
  mkdir -p "$LIVE_DIR" && printf 'live' >"$LIVE_DIR/poster.jpg"
  note "活的叠加层目录：$LIVE_DIR"
else
  note "库里没有条目，跳过「活目录保留」的检查"
fi

echo
echo "== 3. 清孤儿 =="
cleaned=$(json -b "$JAR" -X POST "$BASE/api/v1/maintenance/clean" -d '{"overlayOrphans":true}' \
  | jq -r '.cleaned.overlayOrphans.files // 0')
check "清理报告了清掉的文件数 > 0" true "$([[ "${cleaned:-0}" -gt 0 ]] && echo true || echo false)"
check "孤儿目录已消失" false "$([[ -e "$ORPHAN_DIR" ]] && echo true || echo false)"
if [[ -n "$LIB" && -n "$ITEM" ]]; then
  check "活条目的叠加层目录原样保留" true "$([[ -e "$LIVE_DIR/poster.jpg" ]] && echo true || echo false)"
fi
check "清完之后孤儿数为 0" 0 "$(json -b "$JAR" "$BASE/api/v1/maintenance" | jq -r '.overlayOrphans.files // 0')"

echo
echo "== 4. 图片缓存：清空之后服务照常 =="
before=$(json -b "$JAR" "$BASE/api/v1/maintenance" | jq -r '.images.files // 0')
code -b "$JAR" -X POST "$BASE/api/v1/maintenance/clean" -d '{"images":true}' >/dev/null
after=$(json -b "$JAR" "$BASE/api/v1/maintenance" | jq -r '.images.files // 0')
note "清空前后：$before → $after 个文件"
check "清空后图片缓存为空" 0 "$after"
check "服务照常（清缓存不影响运行）" 200 "$(code "$BASE/healthz")"

echo
echo "== 5. 清场 =="
UID2=$(json -b "$JAR" "$BASE/api/v1/users" | jq -r --arg u "$TMPUSER" '.users[] | select(.username==$u) | .id')
if [[ -n "$UID2" ]]; then
  check "删临时用户" 200 "$(code -b "$JAR" -X DELETE "$BASE/api/v1/users/$UID2")"
fi

echo
echo "================ 结果：$pass 通过 / $fail 失败 ================"
[[ "$fail" -eq 0 ]]
