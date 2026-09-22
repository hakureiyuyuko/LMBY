#!/usr/bin/env bash
# M6「播放列表 / 合集」的真库验收（真 HTTP；会造一个**临时账号**验可见性与权限，跑完删掉）。
#
# 验的是六件事：
#   1. 建列表：私人列表谁都能建、**合集只有管理员能建**、重名 409、空名 400；
#   2. 条目：批量加入保序、重复加入幂等、移出、整串重排；
#   3. 改名 / 改说明：**只给要改的字段**（没给的不会被清掉）；
#   4. 可见性：私人列表别人看不见（404，而不是 403 —— 不泄露「存在」）、合集所有人可见；
#   5. 权限：看得见 ≠ 改得动（别人的合集 → 403）；
#   6. 播放器要的邻居接口：index/total/prevId/nextId 都要对（这是「连着看」的依据）。
#
# 要在跑着服务的那台机器上执行（需要 /usr/local/bin/lmby 造临时账号、psql 清理）。
#
# 用法：
#   LMBY_USER=devtest LMBY_PASS=口令 bash scripts/dev/verify-lists.sh
# 可选：BASE、LMBY_BIN、PGPASS_FILE
set -uo pipefail

BASE="${BASE:-http://127.0.0.1:8099}"
USER="${LMBY_USER:-}"
PASS="${LMBY_PASS:-}"
LMBY_BIN="${LMBY_BIN:-lmby}"
PGPASS_FILE="${PGPASS_FILE:-/etc/lmby/pg-password}"
TAG="tmp-list-$$"

if [[ -z "$USER" || -z "$PASS" ]]; then
  echo "用法：LMBY_USER=devtest LMBY_PASS=口令 bash scripts/dev/verify-lists.sh"
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
login() {
  local jar="$1" u="$2" p="$3"
  curl -s -o /dev/null -b "$jar" -c "$jar" -X POST "$BASE/api/v1/auth/login" \
    -H 'Content-Type: application/json' -d "$(jq -nc --arg u "$u" --arg p "$p" '{username:$u,password:$p}')" \
    -w '%{http_code}'
}

echo "目标：$BASE"

echo
echo "== 1. 登录与鉴权 =="
check "未登录 GET /playlists = 401" 401 "$(st "$BASE/api/v1/playlists")"
check "未登录 POST /playlists = 401" 401 \
  "$(st -X POST "$BASE/api/v1/playlists" -H 'Content-Type: application/json' -d '{"name":"x"}')"
code=$(login "$JAR" "$USER" "$PASS")
check "登录 $USER = 200" 200 "$code"
[[ "$code" != "200" ]] && { echo "登录失败，后面的断言没有意义"; exit 1; }

echo
echo "== 2. 建列表 =="
check "空名 = 400" 400 \
  "$(st -b "$JAR" -X POST "$BASE/api/v1/playlists" -H 'Content-Type: application/json' -d '{"name":"  "}')"
check "非法 kind = 400（用户输入错不该记成 500）" 400 \
  "$(st -b "$JAR" -X POST "$BASE/api/v1/playlists" -H 'Content-Type: application/json' -d '{"name":"x","kind":"whatever"}')"

R=$(json -b "$JAR" -X POST "$BASE/api/v1/playlists" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg n "$TAG 私人" '{name:$n}')")
L1=$(jq -r '.playlist.id' <<<"$R")
check "新建私人列表 = 有 id" true "$([[ "$L1" -gt 0 ]] && echo true || echo false)"
check "新建的列表 kind = playlist" "playlist" "$(jq -r '.playlist.kind' <<<"$R")"
check "新建的列表 mine = true" true "$(jq -r '.playlist.mine' <<<"$R")"
check "新建的列表条目数为 0" 0 "$(jq -r '.playlist.itemCount' <<<"$R")"

check "同名再建一次 = 409（不分大小写）" 409 \
  "$(st -b "$JAR" -X POST "$BASE/api/v1/playlists" -H 'Content-Type: application/json' \
     -d "$(jq -nc --arg n "$TAG 私人" '{name:$n}')")"
check "大小写不同也算同名 = 409" 409 \
  "$(st -b "$JAR" -X POST "$BASE/api/v1/playlists" -H 'Content-Type: application/json' \
     -d "$(jq -nc --arg n "$(tr '[:lower:]' '[:upper:]' <<<"$TAG") 私人" '{name:$n}')")"

echo
echo "== 3. 加入条目（保序 / 幂等）=="
libs=$(bd -b "$JAR" "$BASE/api/v1/libraries")
LIB_ID=$(jq -r '.libraries[0].id // empty' <<<"$libs")
items=$(bd -b "$JAR" "$BASE/api/v1/libraries/$LIB_ID/items?kind=movie&limit=10")
A_ID=$(jq -r '.items[0].id // empty' <<<"$items")
B_ID=$(jq -r '.items[1].id // empty' <<<"$items")
[[ -z "$A_ID" || -z "$B_ID" ]] && { echo "库里至少要两部电影"; exit 1; }
note "样本条目：$A_ID / $B_ID"

R=$(json -b "$JAR" -X POST "$BASE/api/v1/playlists/$L1/items" -H 'Content-Type: application/json' \
  -d "$(jq -nc --argjson a "$A_ID" --argjson b "$B_ID" '{itemIds:[$a,$b]}')")
check "一次加入两个 = added 2" 2 "$(jq -r '.added' <<<"$R")"
check "加入后 itemCount = 2" 2 "$(jq -r '.itemCount' <<<"$R")"
R=$(json -b "$JAR" -X POST "$BASE/api/v1/playlists/$L1/items" -H 'Content-Type: application/json' \
  -d "$(jq -nc --argjson a "$A_ID" '{itemIds:[$a]}')")
check "重复加入 = added 0（幂等）" 0 "$(jq -r '.added' <<<"$R")"
check "重复加入后 itemCount 仍是 2" 2 "$(jq -r '.itemCount' <<<"$R")"
ITEMS=$(bd -b "$JAR" "$BASE/api/v1/playlists/$L1/items")
check "列表顺序与传入顺序一致（A 在 B 前）" true \
  "$(jq -r --argjson a "$A_ID" --argjson b "$B_ID" '.items[0].id == $a and .items[1].id == $b' <<<"$ITEMS")"

check "重排（B 提到前面）" true \
  "$([[ "$(st -b "$JAR" -X PUT "$BASE/api/v1/playlists/$L1/items" -H 'Content-Type: application/json' \
      -d "$(jq -nc --argjson a "$A_ID" --argjson b "$B_ID" '{itemIds:[$b,$a]}')")" == "200" ]] && echo true || echo false)"
check "重排后第一条是 B" "$B_ID" \
  "$(jq -r '.items[0].id' <<<"$(bd -b "$JAR" "$BASE/api/v1/playlists/$L1/items")")"
check "移出 A 后剩 1 条" true \
  "$([[ "$(st -b "$JAR" -X DELETE "$BASE/api/v1/playlists/$L1/items/$A_ID")" == "200" ]] \
     && [[ "$(jq -r '.total' <<<"$(bd -b "$JAR" "$BASE/api/v1/playlists/$L1/items")")" == "1" ]] && echo true || echo false)"
check "移出的只是关系：条目本身还在" 200 "$(st -b "$JAR" "$BASE/api/v1/items/$A_ID")"

echo
echo "== 4. 改名 / 改说明（没给的字段不改）=="
R=$(json -b "$JAR" -X PATCH "$BASE/api/v1/playlists/$L1" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg n "$TAG 改过名" '{name:$n}')")
check "改名生效" "$TAG 改过名" "$(jq -r '.playlist.name' <<<"$R")"
R=$(json -b "$JAR" -X PATCH "$BASE/api/v1/playlists/$L1" -H 'Content-Type: application/json' \
  -d '{"overview":"说明文字"}')
check "只改说明：名字保持不变" "$TAG 改过名" "$(jq -r '.playlist.name' <<<"$R")"
check "只改说明：说明写进去了" "说明文字" "$(jq -r '.playlist.overview' <<<"$R")"
R=$(json -b "$JAR" -X PATCH "$BASE/api/v1/playlists/$L1" -H 'Content-Type: application/json' -d '{}')
check "空的 PATCH：名字与说明都不动" "$TAG 改过名|说明文字" \
  "$(jq -r '.playlist.name + "|" + .playlist.overview' <<<"$R")"

echo
echo "== 5. 邻居（播放器「下一项」的依据）=="
# 当前顺序是 [B, A]（上一节把 A 移到末尾后又删掉、再加回末尾）
json -b "$JAR" -X POST "$BASE/api/v1/playlists/$L1/items" -H 'Content-Type: application/json' \
  -d "$(jq -nc --argjson a "$A_ID" '{itemIds:[$a]}')" >/dev/null
ORDER=$(jq -r '[.items[].id] | join(",")' <<<"$(bd -b "$JAR" "$BASE/api/v1/playlists/$L1/items")")
note "当前顺序：$ORDER"
NB=$(bd -b "$JAR" "$BASE/api/v1/playlists/$L1/neighbors?itemId=$B_ID")
NA=$(bd -b "$JAR" "$BASE/api/v1/playlists/$L1/neighbors?itemId=$A_ID")
check "邻居接口带回列表名" true "$(jq -r '.playlistName | length > 0' <<<"$NB")"
check "B 是第 1 条（index=1，共 2 条）" "1|2" \
  "$(jq -r '"\(.index)|\(.total)"' <<<"$NB")"
check "B 没有上一项" null "$(jq -r '.prevId' <<<"$NB")"
check "B 的下一项是 A" "$A_ID" "$(jq -r '.nextId' <<<"$NB")"
check "A 是第 2 条" 2 "$(jq -r '.index' <<<"$NA")"
check "A 的上一项是 B" "$B_ID" "$(jq -r '.prevId' <<<"$NA")"
check "A 没有下一项（列表到尾了）" null "$(jq -r '.nextId' <<<"$NA")"
check "不在列表里的条目：index=0 且没有邻居" "0|null|null" \
  "$(jq -r '"\(.index)|\(.prevId)|\(.nextId)"' <<<"$(bd -b "$JAR" "$BASE/api/v1/playlists/$L1/neighbors?itemId=99999999")")"
check "缺 itemId = 400" 400 "$(st -b "$JAR" "$BASE/api/v1/playlists/$L1/neighbors")"

echo
echo "== 6. 合集与多用户可见性（临时账号）=="
R=$(json -b "$JAR" -X POST "$BASE/api/v1/playlists" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg n "$TAG 合集" '{name:$n,kind:"collection",overview:"所有人可见"}')")
L2=$(jq -r '.playlist.id' <<<"$R")
check "管理员建合集 = 成功" true "$([[ "$L2" -gt 0 ]] && echo true || echo false)"
check "合集的 kind = collection" "collection" "$(jq -r '.playlist.kind' <<<"$R")"

TMP_USER="tmpuser$$"
TMP_PASS="tmp-lists-pass-$$"
if ! command -v "$LMBY_BIN" >/dev/null 2>&1; then
  note "找不到 $LMBY_BIN，跳过隔离与权限断言"
else
  if "$LMBY_BIN" user add --password "$TMP_PASS" "$TMP_USER" >/dev/null 2>&1; then
    code=$(login "$JAR2" "$TMP_USER" "$TMP_PASS")
    check "临时（非管理员）账号登录 = 200" 200 "$code"
    if [[ "$code" == "200" ]]; then
      check "他看不到我的私人列表（404，不是 403）" 404 "$(st -b "$JAR2" "$BASE/api/v1/playlists/$L1")"
      check "他的列表里没有我的私人列表" true \
        "$(jq -r --argjson id "$L1" 'all(.playlists[]; .id != $id)' <<<"$(bd -b "$JAR2" "$BASE/api/v1/playlists")")"
      check "他看得到合集（所有人可见）" 200 "$(st -b "$JAR2" "$BASE/api/v1/playlists/$L2")"
      check "他的列表里能看到合集" true \
        "$(jq -r --argjson id "$L2" 'any(.playlists[]; .id == $id)' <<<"$(bd -b "$JAR2" "$BASE/api/v1/playlists")")"
      check "合集在他的视角里 mine=false" false \
        "$(jq -r '.playlist.mine' <<<"$(bd -b "$JAR2" "$BASE/api/v1/playlists/$L2")")"
      check "他改不动别人的合集（403）" 403 \
        "$(st -b "$JAR2" -X PATCH "$BASE/api/v1/playlists/$L2" -H 'Content-Type: application/json' -d '{"name":"hacked"}')"
      check "他删不掉别人的合集（403）" 403 "$(st -b "$JAR2" -X DELETE "$BASE/api/v1/playlists/$L2")"
      check "他建不了合集（403）" 403 \
        "$(st -b "$JAR2" -X POST "$BASE/api/v1/playlists" -H 'Content-Type: application/json' \
           -d '{"name":"his collection","kind":"collection"}')"
      check "他能建自己的私人列表（201）" 201 \
        "$(st -b "$JAR2" -X POST "$BASE/api/v1/playlists" -H 'Content-Type: application/json' -d '{"name":"his list"}')"
    fi
  else
    note "创建临时账号失败（权限/DB 口令？），跳过隔离断言"
  fi
fi

echo
echo "== 7. 删除与清理 =="
check "删掉私人列表 = 200" 200 "$(st -b "$JAR" -X DELETE "$BASE/api/v1/playlists/$L1")"
check "删掉后再读 = 404" 404 "$(st -b "$JAR" "$BASE/api/v1/playlists/$L1")"
check "删列表不影响媒体条目" 200 "$(st -b "$JAR" "$BASE/api/v1/items/$B_ID")"
check "删掉合集 = 200" 200 "$(st -b "$JAR" -X DELETE "$BASE/api/v1/playlists/$L2")"
if [[ -n "${TMP_USER:-}" ]] && [[ -r "$PGPASS_FILE" ]]; then
  export PGPASSWORD="$(cat "$PGPASS_FILE")"
  psql -h 127.0.0.1 -U lmby -d lmby -tAc "delete from users where username = '$TMP_USER'" >/dev/null 2>&1 \
    && note "已删掉临时账号 $TMP_USER" \
    || note "临时账号 $TMP_USER 没删掉（手动清理）"
fi

echo
printf '\033[1m结果：%d 通过，%d 失败\033[0m\n' "$pass" "$fail"
[[ "$fail" -eq 0 ]]
