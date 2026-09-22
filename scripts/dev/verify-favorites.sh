#!/usr/bin/env bash
# M6「收藏」的真库验收（真 HTTP；会**造一个临时账号**验多用户隔离，跑完删掉）。
#
# 验的是五件事：
#   1. 收藏 / 取消收藏的基本语义与**幂等**（重复收藏不会把「收藏数」算成 2）；
#   2. 「我的收藏」列表：按收藏时间倒序、可按类型过滤、翻页参数有效；
#   3. **按账号隔离**（M7 多用户的地基）：另一个账号看不到我的收藏，我也看不到他的；
#   4. 首页的「我的收藏」行确实反映了它（收藏后出现、取消后消失）；
#   5. 鉴权：三个接口未登录都是 401。
#
# 临时账号用命令行造（`lmby user add`），跑完用 psql 删掉（级联会带走它的收藏）。
# 所以这个脚本要在**跑着服务的那台机器**上执行（需要 /usr/local/bin/lmby 与
# /etc/lmby/pg-password）。其余断言都是纯 HTTP。
#
# 用法：
#   LMBY_USER=devtest LMBY_PASS=口令 bash scripts/dev/verify-favorites.sh
# 可选：BASE（默认 http://127.0.0.1:8099）、LMBY_BIN（默认 lmby）
set -uo pipefail

BASE="${BASE:-http://127.0.0.1:8099}"
USER="${LMBY_USER:-}"
PASS="${LMBY_PASS:-}"
LMBY_BIN="${LMBY_BIN:-lmby}"
PGPASS_FILE="${PGPASS_FILE:-/etc/lmby/pg-password}"

if [[ -z "$USER" || -z "$PASS" ]]; then
  echo "用法：LMBY_USER=devtest LMBY_PASS=口令 bash scripts/dev/verify-favorites.sh"
  exit 2
fi

JAR=$(mktemp)
JAR2=$(mktemp)
trap 'rm -f "$JAR" "$JAR2"' EXIT

pass=0; fail=0
ok()    { printf '  \033[32mok\033[0m   %s\n' "$1"; pass=$((pass+1)); }
bad()   { printf '  \033[31mFAIL\033[0m %s\n        期望 %s\n        实际 %s\n' "$1" "$2" "$3"; fail=$((fail+1)); }
check() { if [[ "$2" == "$3" ]]; then ok "$1"; else bad "$1" "$2" "$3"; fi; }
note()  { printf '  \033[33m--\033[0m   %s\n' "$1"; }

st()   { curl -s -o /dev/null -w '%{http_code}' "$@"; }
bd()   { curl -s "$@"; }
json() { curl -s -H 'Content-Type: application/json' "$@"; }
login() { # login <jar> <user> <pass>
  local jar="$1" u="$2" p="$3"
  curl -s -o /dev/null -b "$jar" -c "$jar" -X POST "$BASE/api/v1/auth/login" \
    -H 'Content-Type: application/json' -d "$(jq -nc --arg u "$u" --arg p "$p" '{username:$u,password:$p}')" \
    -w '%{http_code}'
}
fav_set() { # fav_set <jar> <itemId> <true|false>
  json -b "$1" -X POST "$BASE/api/v1/items/$2/favorite" \
    -H 'Content-Type: application/json' -d "$(jq -nc --argjson f "$3" '{favorite:$f}')"
}
fav_get() { bd -b "$1" "$BASE/api/v1/items/$2/favorite"; }
fav_list() { bd -b "$1" "$BASE/api/v1/favorites$2"; }
home() { bd -b "$1" "$BASE/api/v1/home"; }

echo "目标：$BASE"

echo
echo "== 1. 登录与鉴权 =="
check "未登录 GET /favorites = 401" 401 "$(st "$BASE/api/v1/favorites")"
check "未登录 GET /items/1/favorite = 401" 401 "$(st "$BASE/api/v1/items/1/favorite")"
check "未登录 POST /items/1/favorite = 401" 401 \
  "$(st -X POST "$BASE/api/v1/items/1/favorite" -H 'Content-Type: application/json' -d '{"favorite":true}')"
code=$(login "$JAR" "$USER" "$PASS")
check "登录 $USER = 200" 200 "$code"
[[ "$code" != "200" ]] && { echo "登录失败，后面的断言没有意义"; exit 1; }

echo
echo "== 2. 准备两个样本条目 =="
libs=$(bd -b "$JAR" "$BASE/api/v1/libraries")
LIB_ID=$(jq -r '.libraries[0].id // empty' <<<"$libs")
[[ -z "$LIB_ID" ]] && { echo "没有媒体库"; exit 1; }
items=$(bd -b "$JAR" "$BASE/api/v1/libraries/$LIB_ID/items?kind=movie&limit=50")
A_ID=$(jq -r '[.items[] | select(.kind == "movie")][0].id // empty' <<<"$items")
B_ID=$(jq -r '[.items[] | select(.kind == "movie")][1].id // empty' <<<"$items")
if [[ -z "$A_ID" || -z "$B_ID" ]]; then
  echo "库里至少要两部电影才能验收藏（当前库：$LIB_ID）"
  exit 1
fi
A_TITLE=$(jq -r --argjson id "$A_ID" '.items[] | select(.id == $id) | .title' <<<"$items")
B_TITLE=$(jq -r --argjson id "$B_ID" '.items[] | select(.id == $id) | .title' <<<"$items")
note "样本：$A_ID《$A_TITLE》 / $B_ID《$B_TITLE》"
BASE_TOTAL=$(jq -r '.total' <<<"$(fav_list "$JAR" "?limit=1")")
note "起点：这个账号已有收藏 $BASE_TOTAL 条"

echo
echo "== 3. 收藏 / 取消（含幂等）=="
# 先归一化到「没收藏」，免得受上一次失败运行的影响
fav_set "$JAR" "$A_ID" false >/dev/null
check "初始状态：没收藏" false "$(jq -r '.favorite' <<<"$(fav_get "$JAR" "$A_ID")")"

R=$(fav_set "$JAR" "$A_ID" true)
check "收藏一个条目 = ok" true "$(jq -r '.ok' <<<"$R")"
check "返回的新状态是已收藏" true "$(jq -r '.favorite' <<<"$R")"
C1=$(jq -r '.count' <<<"$R")
check "收藏数 ≥ 1" true "$([[ "$C1" -ge 1 ]] && echo true || echo false)"
check "单条状态接口与之一致" true "$(jq -r '.favorite' <<<"$(fav_get "$JAR" "$A_ID")")"

R2=$(fav_set "$JAR" "$A_ID" true)
check "重复收藏是幂等的（不报错）" true "$(jq -r '.ok' <<<"$R2")"
check "重复收藏不会把收藏数算成 2" "$C1" "$(jq -r '.count' <<<"$R2")"

check "收藏后总数 = 起点 + 1" "$((BASE_TOTAL + 1))" "$(jq -r '.total' <<<"$(fav_list "$JAR" '?limit=1')")"
check "列表里有刚收藏的那条" true \
  "$(jq -r --argjson id "$A_ID" 'any(.items[]; .id == $id)' <<<"$(fav_list "$JAR" "")")"

echo
echo "== 4. 列表：倒序 / 过滤 =="
fav_set "$JAR" "$B_ID" true >/dev/null
L=$(fav_list "$JAR" "")
check "最近收藏的排在第一条" "$B_ID" "$(jq -r '.items[0].id' <<<"$L")"
check "列表按收藏时间倒序（B 在 A 之前）" true \
  "$(jq -r --argjson a "$A_ID" --argjson b "$B_ID" '([.items[].id] | index($b)) < ([.items[].id] | index($a))' <<<"$L")"
check "按类型过滤：kind=movie 的结果都是电影" true \
  "$(jq -r 'all(.items[]; .kind == "movie")' <<<"$(fav_list "$JAR" '?kind=movie')")"
check "按类型过滤：kind=series 里没有刚收藏的电影" true \
  "$(jq -r --argjson a "$A_ID" --argjson b "$B_ID" 'all(.items[]; (.id != $a) and (.id != $b))' <<<"$(fav_list "$JAR" '?kind=series')")"
check "非法 kind = 400" 400 "$(st -b "$JAR" "$BASE/api/v1/favorites?kind=album")"
check "翻页：limit=1 只回 1 条" 1 "$(jq -r '.items | length' <<<"$(fav_list "$JAR" '?limit=1')")"
check "翻页：offset=1 换一条" true \
  "$(jq -r --argjson a "$(jq -r '.items[0].id' <<<"$(fav_list "$JAR" '?limit=1')")" '.items[0].id != $a' <<<"$(fav_list "$JAR" '?limit=1&offset=1')")"

echo
echo "== 5. 首页「我的收藏」行 =="
H=$(home "$JAR")
check "首页有 favorites 行" true "$(jq -r 'any(.sections[]; .key == "favorites")' <<<"$H")"
check "该行第一条就是最近收藏的那条" "$B_ID" \
  "$(jq -r '[.sections[] | select(.key == "favorites")][0].items[0].id' <<<"$H")"

echo
echo "== 6. 按账号隔离（临时账号）=="
TMP_USER="tmpfav$$"
TMP_PASS="tmp-fav-pass-$$"
if ! command -v "$LMBY_BIN" >/dev/null 2>&1; then
  note "找不到 $LMBY_BIN（要在跑服务的那台机器上跑），跳过隔离断言"
else
  if "$LMBY_BIN" user add --password "$TMP_PASS" "$TMP_USER" >/dev/null 2>&1; then
    code=$(login "$JAR2" "$TMP_USER" "$TMP_PASS")
    check "临时账号登录 = 200" 200 "$code"
    if [[ "$code" == "200" ]]; then
      check "别的账号看不到我的收藏（单条状态）" false "$(jq -r '.favorite' <<<"$(fav_get "$JAR2" "$A_ID")")"
      check "别的账号的收藏列表是空的" 0 "$(jq -r '.total' <<<"$(fav_list "$JAR2" "")")"
      # 反过来：他收藏之后，我这边不受影响
      fav_set "$JAR2" "$A_ID" true >/dev/null
      check "他收藏之后我的列表条数不变" "$((BASE_TOTAL + 2))" "$(jq -r '.total' <<<"$(fav_list "$JAR" '?limit=1')")"
      check "他的收藏数与我无关（我的条目仍算 1 人收藏的前一半）" true \
        "$(jq -r '.count >= 1' <<<"$(fav_get "$JAR" "$A_ID")")"
      check "全站收藏数包含了两个账号（A 被两人收藏）" 2 "$(jq -r '.count' <<<"$(fav_get "$JAR" "$A_ID")")"
    fi
  else
    note "创建临时账号失败（权限/DB 口令？），跳过隔离断言"
  fi
fi

echo
echo "== 7. 还原 =="
fav_set "$JAR" "$A_ID" false >/dev/null
fav_set "$JAR" "$B_ID" false >/dev/null
check "还原后总数回到起点" "$BASE_TOTAL" "$(jq -r '.total' <<<"$(fav_list "$JAR" '?limit=1')")"
check "还原后首页不再有 favorites 行（若起点本来就没有）" true \
  "$(jq -r --argjson t "$BASE_TOTAL" '([.sections[] | select(.key == "favorites")] | length > 0) == ($t > 0)' <<<"$(home "$JAR")")"
if [[ -n "${TMP_USER:-}" ]] && [[ -r "$PGPASS_FILE" ]]; then
  export PGPASSWORD="$(cat "$PGPASS_FILE")"
  psql -h 127.0.0.1 -U lmby -d lmby -tAc \
    "delete from users where username = '$TMP_USER'" >/dev/null 2>&1 \
    && note "已删掉临时账号 $TMP_USER" \
    || note "临时账号 $TMP_USER 没删掉（手动清理：delete from users where username='$TMP_USER'）"
fi

echo
printf '\033[1m结果：%d 通过，%d 失败\033[0m\n' "$pass" "$fail"
[[ "$fail" -eq 0 ]]
