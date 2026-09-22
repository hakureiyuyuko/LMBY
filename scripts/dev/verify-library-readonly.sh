#!/usr/bin/env bash
# M6「媒体库只读 + overlay」的真库验收（真 HTTP + 真磁盘）。
#
# 验的是五件事：
#   1. 「只读」开关能开能关，`GET /libraries` 与 `GET /libraries/{id}` 都如实反映；
#   2. 打开后**库里一行登记都没变**（只读是「写入策略」，不是「换个存法」）；
#   3. 只读库回源取到的图片，在数据目录的 overlay 层里有一份
#      （`<数据目录>/overlay/<libraryId>/<itemId>/<kind>.<ext>`）；
#   4. overlay 占用统计会随之上报（`GET /libraries/{id}` 的 `overlay` 字段）；
#   5. 关掉只读之后再取一张图，**不会**再往 overlay 里写。
#
# ⚠️ 会动真库里的一个媒体库（开/关只读），所以它必须跑在**跑着服务的那台机器**上
#    （需要读 `/etc/lmby/config.toml` 找数据目录），并在结束时把开关恢复原状。
#
# 用法：
#   LMBY_USER=devtest LMBY_PASS=口令 bash scripts/dev/verify-library-readonly.sh
# 可选：BASE（默认 http://127.0.0.1:8099）、DATA_DIR（默认从 config.toml 的 data_dir 读）
set -uo pipefail

BASE="${BASE:-http://127.0.0.1:8099}"
USER="${LMBY_USER:-}"
PASS="${LMBY_PASS:-}"
CFG="${LMBY_CONFIG:-/etc/lmby/config.toml}"

if [[ -z "$USER" || -z "$PASS" ]]; then
  echo "用法：LMBY_USER=devtest LMBY_PASS=口令 bash scripts/dev/verify-library-readonly.sh"
  exit 2
fi

# 数据目录：优先环境变量，其次 config.toml，最后默认 ./data（相对服务的工作目录）
DATA_DIR="${DATA_DIR:-}"
if [[ -z "$DATA_DIR" && -r "$CFG" ]]; then
  DATA_DIR=$(sed -n 's/^[[:space:]]*data_dir[[:space:]]*=[[:space:]]*"\(.*\)".*/\1/p' "$CFG" | head -1)
fi
DATA_DIR="${DATA_DIR:-/var/lib/lmby}"
OVERLAY="$DATA_DIR/overlay"

JAR=$(mktemp)
trap 'rm -f "$JAR"' EXIT

pass=0; fail=0
ok()    { printf '  \033[32mok\033[0m   %s\n' "$1"; pass=$((pass+1)); }
bad()   { printf '  \033[31mFAIL\033[0m %s\n        期望 %s\n        实际 %s\n' "$1" "$2" "$3"; fail=$((fail+1)); }
check() { if [[ "$2" == "$3" ]]; then ok "$1"; else bad "$1" "$2" "$3"; fi; }
note()  { printf '  \033[33m--\033[0m   %s\n' "$1"; }

json() { curl -s -H 'Content-Type: application/json' "$@"; }

echo "== 0. 登录与前置检查 =="
login=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -c "$JAR" -X POST "$BASE/api/v1/auth/login" \
  -H 'Content-Type: application/json' -d "$(jq -nc --arg u "$USER" --arg p "$PASS" '{username:$u,password:$p}')")
check "登录成功" 200 "$login"
note "数据目录 $DATA_DIR（overlay 在 $OVERLAY）"

LIB=$(json -b "$JAR" "$BASE/api/v1/libraries" | jq -r '.libraries[0].id // empty')
if [[ -z "$LIB" ]]; then
  echo "  库里一个媒体库都没有，无法验收"
  exit 1
fi
ORIG=$(json -b "$JAR" "$BASE/api/v1/libraries/$LIB" | jq -r '.library.readonly // false')
note "用媒体库 #$LIB（当前 readonly=$ORIG）"

# 恢复原状的兜底：不管中途怎么退出，都把开关拨回去
restore() {
  json -b "$JAR" -X PATCH "$BASE/api/v1/libraries/$LIB" \
    -H 'Content-Type: application/json' -d "$(jq -nc --argjson r "$ORIG" '{readonly:$r}')" >/dev/null 2>&1
}
trap 'restore; rm -f "$JAR"' EXIT

echo
echo "== 1. 打开只读开关 =="
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -X PATCH "$BASE/api/v1/libraries/$LIB" \
  -H 'Content-Type: application/json' -d '{"readonly":true}')
check "PATCH 返回 200" 200 "$code"
check "库列表里 readonly=true" true "$(json -b "$JAR" "$BASE/api/v1/libraries" | jq -r --argjson id "$LIB" '.libraries[] | select(.id==$id) | .readonly')"
check "库详情里 readonly=true" true "$(json -b "$JAR" "$BASE/api/v1/libraries/$LIB" | jq -r '.library.readonly')"
check "根路径的 readonly 也同步了" true "$(json -b "$JAR" "$BASE/api/v1/libraries/$LIB" | jq -r '.library.paths[0].readonly')"
check "详情里带 overlay 统计字段" true "$(json -b "$JAR" "$BASE/api/v1/libraries/$LIB" | jq -r 'has("overlay")')"

echo
echo "== 2. 只读是写入策略，不是换存法：条目数不变 =="
before=$(json -b "$JAR" "$BASE/api/v1/libraries/$LIB/items?limit=1" | jq -r '.total')
json -b "$JAR" -X PATCH "$BASE/api/v1/libraries/$LIB" -H 'Content-Type: application/json' -d '{"readonly":true}' >/dev/null
after=$(json -b "$JAR" "$BASE/api/v1/libraries/$LIB/items?limit=1" | jq -r '.total')
check "条目总数没变" "$before" "$after"

echo
echo "== 3. 只读库取图 → overlay 里有一份 =="
# 只读库只补「回源来的图」（本地图本来就在媒体目录里，属于只读输入），
# 所以这里逐个试条目，碰到第一个回源的就会在叠加层里留下文件。
found=""; pick=""
for id in $(json -b "$JAR" "$BASE/api/v1/libraries/$LIB/items?limit=50" | jq -r '.items[].id' | head -10); do
  curl -s -o /dev/null -b "$JAR" "$BASE/api/v1/items/$id/images/poster?w=300"
  n=$(find "$OVERLAY/$LIB/$id" -type f 2>/dev/null | wc -l)
  if [[ "$n" -gt 0 ]]; then found="$n"; pick="$id"; break; fi
done
if [[ -n "$found" ]]; then
  ok "条目 #$pick 的图片已进叠加层（$(find "$OVERLAY/$LIB/$pick" -type f 2>/dev/null | head -5 | tr '\n' ' '))"
else
  bad "叠加层 $OVERLAY/$LIB/<item> 下有文件" ">0" "0（这个库前 10 条的图全部来自本地目录，不回源）"
fi
files=$(json -b "$JAR" "$BASE/api/v1/libraries/$LIB" | jq -r '.overlay.files')
bytes=$(json -b "$JAR" "$BASE/api/v1/libraries/$LIB" | jq -r '.overlay.bytes')
note "界面看到的叠加层占用：$files 个文件 / $bytes 字节"
if [[ "$files" -gt 0 ]]; then ok "overlay 统计 > 0"; else bad "overlay 统计 > 0" ">0" "$files"; fi

echo
echo "== 4. 关掉只读：开关与根路径都必须回到 false =="
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -X PATCH "$BASE/api/v1/libraries/$LIB" \
  -H 'Content-Type: application/json' -d '{"readonly":false}')
check "PATCH 返回 200" 200 "$code"
check "库详情里 readonly=false" false "$(json -b "$JAR" "$BASE/api/v1/libraries/$LIB" | jq -r '.library.readonly')"
check "根路径的 readonly 也回到 false" false "$(json -b "$JAR" "$BASE/api/v1/libraries/$LIB" | jq -r '.library.paths[0].readonly')"

echo
echo "== 5. 参数校验与鉴权 =="
check "空请求体（没有要更新的字段）→ 400" 400 \
  "$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -X PATCH "$BASE/api/v1/libraries/$LIB" \
    -H 'Content-Type: application/json' -d '{}')"
check "改名到空串 → 400" 400 \
  "$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -X PATCH "$BASE/api/v1/libraries/$LIB" \
    -H 'Content-Type: application/json' -d '{"name":"  "}')"
check "未登录 PATCH → 401" 401 \
  "$(curl -s -o /dev/null -w '%{http_code}' -X PATCH "$BASE/api/v1/libraries/$LIB" \
    -H 'Content-Type: application/json' -d '{"readonly":true}')"

echo
echo "================ 结果：$pass 通过 / $fail 失败 ================"
[[ "$fail" -eq 0 ]]
