#!/usr/bin/env bash
# 扫描计划的真库验收：**调度器会不会真的按间隔把库扫起来**。
#
# 为什么必须真跑：这段逻辑的正确性全在「SQL 到期判定 + 调度器 tick + Manager 去重」
# 三者的配合上 —— 不连库的单测看不到它。脚本的做法：
#
#   1. 把某个库的间隔设成 1 分钟（最小可观测值）；
#   2. 把它最近一条 scan_run 的 started_at 改到 2 分钟前（伪造「该扫了」）；
#   3. 等一个 tick（服务端默认 60 秒），断言出现了 trigger='schedule' 的**新** scan_run；
#   4. 收尾：把间隔改回原值（跑完不留痕）。
#
# 前提：服务端的 [scan] schedule_tick_seconds > 0（默认 60）。tick 设得越大等待越久 ——
# 想快就临时把它改小（像 verify-livetv-sync.sh 那样改配置重启），本脚本默认按 60 秒等。
#
# 用法：
#   LMBY_USER=devtest LMBY_PASS=口令 bash scripts/dev/verify-scan-schedule.sh
# 可选：
#   LIBRARY_ID     要用的库（默认取列表里第一个）
#   SCHEDULE_WAIT  最多等多少秒（默认 100）
set -uo pipefail

BASE="${BASE:-http://127.0.0.1:8099}"
USER="${LMBY_USER:-}"
PASS="${LMBY_PASS:-}"
LIBRARY_ID="${LIBRARY_ID:-}"
WAIT_MAX="${SCHEDULE_WAIT:-100}"
PG_PASS_FILE="${PG_PASS_FILE:-/etc/lmby/pg-password}"

if [[ -z "$USER" || -z "$PASS" ]]; then
  echo "用法：LMBY_USER=devtest LMBY_PASS=口令 bash scripts/dev/verify-scan-schedule.sh" >&2
  exit 2
fi

pass=0
fail=0
check() {
  if [[ "$2" == "$3" ]]; then
    printf '  ok   %s\n' "$1"; pass=$((pass + 1))
  else
    printf '  FAIL %s（期望 %q，实际 %q）\n' "$1" "$2" "$3"; fail=$((fail + 1))
  fi
}
note() { printf '  --   %s\n' "$1"; }

sql() {
  local pw="${PGPASSWORD:-$(cat "$PG_PASS_FILE" 2>/dev/null)}"
  PGPASSWORD="$pw" psql -h 127.0.0.1 -U lmby -d lmby -tAc "$1" 2>/dev/null
}
json() { curl -s -H 'Content-Type: application/json' "$@"; }

JAR=$(mktemp)
trap 'rm -f "$JAR"' EXIT

echo "== 0. 登录与前置 =="
login=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -c "$JAR" -X POST "$BASE/api/v1/auth/login" \
  -H 'Content-Type: application/json' -d "$(jq -nc --arg u "$USER" --arg p "$PASS" '{username:$u,password:$p}')")
check "登录" 200 "$login"
check "能连数据库（本脚本要伪造一条到期的扫描记录）" "1" "$(sql 'select 1')"

if [[ -z "$LIBRARY_ID" ]]; then
  LIBRARY_ID=$(json -b "$JAR" "$BASE/api/v1/libraries" | jq -r '.libraries[0].id // empty')
fi
check "拿到一个媒体库" true "$([[ -n "$LIBRARY_ID" ]] && echo true || echo false)"
[[ -n "$LIBRARY_ID" ]] || exit 1

ORIG=$(sql "select scan_interval_minutes from libraries where id = $LIBRARY_ID")
note "用媒体库 #$LIBRARY_ID（当前扫描间隔 ${ORIG:-?} 分钟）"

echo
echo "== 1. 设成「每 1 分钟」，并把最近一次扫描挪到 2 分钟前 =="
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -X PATCH "$BASE/api/v1/libraries/$LIBRARY_ID" \
  -H 'Content-Type: application/json' -d '{"scanIntervalMinutes":1}')
check "设置间隔返回 200" 200 "$code"
check "库里存下来了" 1 "$(sql "select scan_interval_minutes from libraries where id = $LIBRARY_ID")"

BEFORE_MAX=$(sql "select coalesce(max(id), 0) from scan_runs where library_id = $LIBRARY_ID")
if [[ "$(sql "select count(*) from scan_runs where library_id = $LIBRARY_ID")" != "0" ]]; then
  sql "update scan_runs set started_at = now() - interval '2 minutes'
       where id = (select id from scan_runs where library_id = $LIBRARY_ID order by started_at desc limit 1)" >/dev/null
  note "已把最近一条扫描记录改到 2 分钟前（伪造「到期」）"
else
  note "这个库还没扫过 —— 从没扫过的库本来就立即到期，不用伪造"
fi

echo
echo "== 2. 等调度器到点（最多 ${WAIT_MAX} 秒） =="
t0=$(date +%s)
NEW=""
while (( $(date +%s) - t0 < WAIT_MAX )); do
  NEW=$(sql "select id from scan_runs where library_id = $LIBRARY_ID and id > $BEFORE_MAX
             order by id desc limit 1")
  [[ -n "$NEW" ]] && break
  sleep 5
done
WAITED=$(( $(date +%s) - t0 ))
note "等了 ${WAITED} 秒"
check "调度器触发了一次新扫描" true "$([[ -n "$NEW" ]] && echo true || echo false)"
if [[ -n "$NEW" ]]; then
  check "新记录的触发来源是 schedule" "schedule" "$(sql "select trigger from scan_runs where id = $NEW")"
  note "新 scan_run #$NEW：state=$(sql "select state from scan_runs where id = $NEW")"
fi

echo
echo "== 3. 收尾：把间隔改回原值 =="
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -X PATCH "$BASE/api/v1/libraries/$LIBRARY_ID" \
  -H 'Content-Type: application/json' -d "{\"scanIntervalMinutes\":${ORIG:-0}}")
check "恢复间隔返回 200" 200 "$code"
check "恢复成了原值" "${ORIG:-0}" "$(sql "select scan_interval_minutes from libraries where id = $LIBRARY_ID")"

echo
echo "================ 结果：$pass 通过 / $fail 失败 ================"
[[ "$fail" -eq 0 ]]
