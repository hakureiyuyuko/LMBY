#!/usr/bin/env bash
# 直播源「停用即前台不可见」的验收（真库，纯 HTTP + 末尾 psql 清场）。
#
# 为什么单独一个脚本：这条规则横跨两个东西 ——
#   **源**（tv_sources.enabled）与**前台频道列表**（/api/v1/livetv/channels）。
# 规则一句话：**停用一个直播源，它带来的频道在前台不再出现**（频道自己 enabled 不动），
# 同时头部的「共 N 台 / 启用中 M」要跟着降 —— 两处必须是同一套可见性判据，
# 否则界面会说「共 150 台」却只列 67 台。
#
# 因为开发库里现在的频道都是「没有源」的（source_id is null，永远可见），
# 这个脚本会**临时造一个源**（粘贴两条假地址的 m3u）来验这条规则，跑完：
#   - 停用 → 隐藏；恢复 → 回来；
#   - **删源 → 频道还在且重新可见**（库表上 source_id 是 `on delete set null`，
#     这正是界面写的「删源不删频道」）；
#   - 最后用 psql 把这两条临时频道删掉，不留垃圾。
#
# ⚠️ 要在**跑着服务的那台机器**上执行（末尾要 psql 清场）。
#
# 用法：
#   LMBY_USER=devtest LMBY_PASS=口令 bash scripts/dev/verify-livetv-disabled-source.sh
# 可选：BASE（默认 http://127.0.0.1:8099）、PGPASS_FILE（默认 /etc/lmby/pg-password）
set -uo pipefail

BASE="${BASE:-http://127.0.0.1:8099}"
USER="${LMBY_USER:-}"
PASS="${LMBY_PASS:-}"
PGPASS_FILE="${PGPASS_FILE:-/etc/lmby/pg-password}"
TAG="LMBY验证$(date +%s)"

if [[ -z "$USER" || -z "$PASS" ]]; then
  echo "用法：LMBY_USER=devtest LMBY_PASS=口令 bash scripts/dev/verify-livetv-disabled-source.sh"
  exit 2
fi

JAR=$(mktemp)
SRC_ID=""
cleanup() {
  # 尽力而为：删源（若还在）+ 删掉两条临时频道
  if [[ -n "$SRC_ID" ]]; then
    curl -s -o /dev/null -b "$JAR" -X DELETE "$BASE/api/v1/livetv/sources/$SRC_ID" || true
  fi
  if [[ -r "$PGPASS_FILE" ]]; then
    PGPASSWORD=$(cat "$PGPASS_FILE") psql -h 127.0.0.1 -U lmby -d lmby -tAc \
      "delete from tv_channels where group_name = '$TAG'" >/dev/null 2>&1 || true
  fi
  rm -f "$JAR"
}
trap cleanup EXIT

pass=0; fail=0
ok()    { printf '  \033[32mok\033[0m   %s\n' "$1"; pass=$((pass+1)); }
bad()   { printf '  \033[31mFAIL\033[0m %s\n        期望 %s\n        实际 %s\n' "$1" "$2" "$3"; fail=$((fail+1)); }
check() { if [[ "$2" == "$3" ]]; then ok "$1"; else bad "$1" "$2" "$3"; fi; }
note()  { printf '  \033[33m--\033[0m   %s\n' "$1"; }

json() { curl -s -H 'Content-Type: application/json' "$@"; }
channels() { json -b "$JAR" "$BASE/api/v1/livetv/channels?enabled=false"; }
mine() { channels | jq -r --argjson s "$1" '[.channels[] | select(.sourceId == $s)] | length'; }

echo "== 0. 登录 =="
check "登录成功" 200 \
  "$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -c "$JAR" -X POST "$BASE/api/v1/auth/login" \
    -H 'Content-Type: application/json' -d "$(jq -nc --arg u "$USER" --arg p "$PASS" '{username:$u,password:$p}')")"

echo
echo "== 1. 造一个临时源（两条假地址的频道） =="
PLAYLIST="#EXTM3U
#EXTINF:-1 group-title=\"$TAG\",$TAG 频道一
http://127.0.0.1:9/a.m3u8
#EXTINF:-1 group-title=\"$TAG\",$TAG 频道二
http://127.0.0.1:9/b.m3u8"
created=$(json -b "$JAR" -X POST "$BASE/api/v1/livetv/sources" \
  -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg n "$TAG" --arg c "$PLAYLIST" '{name:$n, kind:"paste", content:$c}')")
SRC_ID=$(jq -r '.source.id // empty' <<<"$created")
check "建源成功" true "$([[ -n "$SRC_ID" ]] && echo true || echo false)"
[[ -z "$SRC_ID" ]] && { echo "  建源失败：$created"; exit 1; }
note "临时源 #$SRC_ID「$TAG」，导入 $(jq -r '.import.total // "?"' <<<"$created") 条"

before_mine=$(mine "$SRC_ID")
check "两条频道挂上了这个源" 2 "$before_mine"
before_all=$(channels | jq -r '.total')

echo
echo "== 2. 停用源：前台立刻看不到这批频道 =="
check "PATCH 停用返回 200" 200 \
  "$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -X PATCH "$BASE/api/v1/livetv/sources/$SRC_ID" \
    -H 'Content-Type: application/json' -d '{"enabled":false}')"
check "停用后该源频道数为 0" 0 "$(mine "$SRC_ID")"
after_all=$(channels | jq -r '.total')
check "列表条数正好少了两条" "$((before_all - 2))" "$after_all"
check "头部统计与实际列出一致" "$after_all" "$(channels | jq -r '.channels | length')"

if [[ -r "$PGPASS_FILE" ]]; then
  ap=$(cat "$PGPASS_FILE")
  in_db=$(PGPASSWORD=$ap psql -h 127.0.0.1 -U lmby -d lmby -tAc \
    "select count(*) from tv_channels where source_id = $SRC_ID and not disabled")
  check "频道自己的 enabled 没被动过（只是前台看不见）" 2 "$in_db"
fi

echo
echo "== 3. 恢复启用：整批回来 =="
check "PATCH 启用返回 200" 200 \
  "$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -X PATCH "$BASE/api/v1/livetv/sources/$SRC_ID" \
    -H 'Content-Type: application/json' -d '{"enabled":true}')"
check "频道数回到 2" 2 "$(mine "$SRC_ID")"
check "前台总数回到原值" "$before_all" "$(channels | jq -r '.total')"

echo
echo "== 4. 删源：频道保留且重新可见（on delete set null）=="
check "DELETE 源返回 200" 200 \
  "$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -X DELETE "$BASE/api/v1/livetv/sources/$SRC_ID")"
SRC_ID=""
orphan=$(channels | jq -r --arg n "$TAG" '[.channels[] | select(.name | startswith($n))] | length')
check "两条频道还在（删源不删频道）" 2 "$orphan"
check "而且重新算作可见（前台总数回到原值）" "$before_all" "$(channels | jq -r '.total')"
check "看板上这个源没了" 0 \
  "$(json -b "$JAR" "$BASE/api/v1/livetv/sources" | jq -r --arg n "$TAG" '[.sources[] | select(.name == $n)] | length')"

echo
echo "== 5. hide_failed：前台不列「无效的」频道（探测过且不通）=="
all=$(channels | jq -r '.channels | length')
failed=$(channels | jq -r '[.channels[] | select(.probeOk == false)] | length')
pending=$(channels | jq -r '[.channels[] | select(.probeOk == null)] | length')
wf=$(json -b "$JAR" "$BASE/api/v1/livetv/channels?enabled=false&hide_failed=1")
wf_n=$(jq -r '.channels | length' <<<"$wf")
wf_failed=$(jq -r '[.channels[] | select(.probeOk == false)] | length' <<<"$wf")
wf_pending=$(jq -r '[.channels[] | select(.probeOk == null)] | length' <<<"$wf")
note "全量 $all 台（其中失效 $failed、没探过 $pending）；hide_failed 后 $wf_n 台"
check "hide_failed 后列表里没有失效频道" 0 "$wf_failed"
check "条数正好少了失效那些" "$((all - failed))" "$wf_n"
check "头部统计与列出一致（hide_failed）" "$(jq -r '.total' <<<"$wf")" "$wf_n"
check "没探过的没被一起筛掉" "$pending" "$wf_pending"

if [[ -r "$PGPASS_FILE" ]]; then
  ap=$(cat "$PGPASS_FILE")
  real_failed=$(PGPASSWORD=$ap psql -h 127.0.0.1 -U lmby -d lmby -tAc \
    "select count(*) from tv_channels c where c.probe_ok is false")
  check "库里的失效频道与接口算出的一致" "$real_failed" "$failed"
fi

echo
echo "================ 结果：$pass 通过 / $fail 失败 ================"
[[ "$fail" -eq 0 ]]
