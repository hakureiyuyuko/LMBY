#!/usr/bin/env bash
# 「检查更新」的真库验收（真 HTTP）。
#
# 要验的不是「GitHub 上有没有新版」，而是这条链路的**边界与诚实**：
#   - 只有管理员能查（这是实例级操作，普通账号 403）；
#   - 结果结构完整，且 state 与 current/latest **自洽**（不能出现「明明有新版、
#     state 却说已最新」这种界面会骗人的组合）；
#   - 查不成也是 HTTP 200 + {ok:false, error:"人话"}：内网机器没外网时，界面要把
#     原因显示出来，而不是丢一个 500 或者静默「已是最新」；
#   - 连点两次回缓存（保护上游匿名限流：GitHub 是 60 次/小时/IP）。
#
# 这台机器有没有外网出口不影响本脚本的判定：ok=true 时查自洽性，
# ok=false 时查「降级有没有说清原因」。
#
# 用法：LMBY_USER=<管理员> LMBY_PASS=<口令> bash scripts/dev/verify-update.sh
set -u

BASE="${BASE:-http://127.0.0.1:8099}"

pass=0; fail=0
json() { # json <cookie jar> <url> [method] [body]
  local jar="$1" url="$2" method="${3:-GET}" body="${4:-}"
  if [[ -n "$body" ]]; then
    curl -s -b "$jar" -c "$jar" -X "$method" "$url" -H 'Content-Type: application/json' -d "$body"
  else
    curl -s -b "$jar" -c "$jar" -X "$method" "$url"
  fi
}
note() { printf '  · %s\n' "$*"; }
check() { # check <名字> <期望> <实际>
  if [[ "$2" == "$3" ]]; then pass=$((pass+1)); printf 'ok   %s\n' "$1"
  else fail=$((fail+1)); printf 'FAIL %s（期望 %s，实际 %s）\n' "$1" "$2" "$3"; fi
}
login() { # login <jar> <user> <pass> → 成功输出 200
  local jar="$1"
  curl -s -o /dev/null -w '%{http_code}' -b "$jar" -c "$jar" -X POST "$BASE/api/v1/auth/login" \
    -H 'Content-Type: application/json' -d "{\"username\":\"$2\",\"password\":\"$3\"}"
}
code() { curl -s -o /dev/null -w '%{http_code}' -b "$1" "$2"; }

ADMIN_JAR=$(mktemp); USER_JAR=$(mktemp)
trap 'rm -f "$ADMIN_JAR" "$USER_JAR"' EXIT
check "管理员登录" 200 "$(login "$ADMIN_JAR" "${LMBY_USER:?}" "${LMBY_PASS:?}")"

SUF=$(date +%s)
UNAME="upd$SUF"
UPASS="verify-pass-$SUF"

echo
echo "== 0. 权限：登录才能查，且只有管理员 =="
check "未登录 → 401" 401 "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/v1/update")"
json "$ADMIN_JAR" "$BASE/api/v1/users" POST \
  "{\"username\":\"$UNAME\",\"password\":\"$UPASS\"}" >/dev/null
NEW_ID=$(json "$ADMIN_JAR" "$BASE/api/v1/users" | jq -r --arg u "$UNAME" '.users[]|select(.username==$u)|.id')
check "普通用户登录" 200 "$(login "$USER_JAR" "$UNAME" "$UPASS")"
check "普通用户查更新 → 403" 403 "$(code "$USER_JAR" "$BASE/api/v1/update")"

echo
echo "== 1. 管理员查一次（?refresh=1 = 用户点了按钮）=="
RESP=$(json "$ADMIN_JAR" "$BASE/api/v1/update?refresh=1")
echo "$RESP" | jq -c '{enabled,ok,state,current,latest,error,cached}' | sed 's/^/  /'
check "有 enabled 字段" true "$(jq 'has("enabled")' <<<"$RESP")"
check "有 checkedAt" true "$(jq 'has("checkedAt")' <<<"$RESP")"
check "state 合法" true "$(jq '.state|test("^(up-to-date|update-available|dev|unknown)$")' <<<"$RESP")"
META_VER=$(json "$ADMIN_JAR" "$BASE/api/v1/meta" | jq -r .version)
check "current 与 /api/v1/meta 的版本一致" "$META_VER" "$(jq -r .current <<<"$RESP")"

if [[ "$(jq -r .enabled <<<"$RESP")" == "true" ]]; then
  if [[ "$(jq -r .ok <<<"$RESP")" == "true" ]]; then
    check "ok 时应当带回 latest" true "$(jq '.latest|type=="string" and length>0' <<<"$RESP")"
    CONSIST=$(python3 - "$RESP" <<'PY'
import json, sys
d = json.loads(sys.argv[1])
def parse(v):
    v = (v or '').strip().lstrip('vV')
    if not v:
        return None
    v = v.split('-')[0].split('+')[0]
    parts = v.split('.')
    if not all(p.isdigit() for p in parts):
        return None
    return [int(p) for p in parts]
cur, lat = parse(d.get('current')), parse(d.get('latest'))
if cur is None:
    print('ok' if d['state'] == 'dev' else 'BAD:开发构建却报 ' + str(d['state']))
elif lat is None:
    print('ok')
else:
    want = 'update-available' if lat > cur else 'up-to-date'
    print('ok' if d['state'] == want else 'BAD:%s（按版本号应当是 %s）' % (d['state'], want))
PY
)
    check "state 与版本号自洽" ok "$CONSIST"
  else
    note "没查成（这台机器多半没有外网出口）—— 那就要看降级说得清不清楚"
    check "降级时 error 是人话（非空、不短）" true "$(jq '.error|type=="string" and length>8' <<<"$RESP")"
    check "降级时不得谎报 latest" true "$(jq 'has("latest")|not' <<<"$RESP")"
  fi
else
  note "更新源未配置（[update] source_url 为空）→ 界面应当把按钮置灰并说明"
  check "未配置时 state=unknown" unknown "$(jq -r .state <<<"$RESP")"
  check "未配置时 error 说清原因" true "$(jq '.error|length>8' <<<"$RESP")"
fi

echo
echo "== 2. 最小间隔内重复点 → 回缓存（保护上游限流）=="
RESP2=$(json "$ADMIN_JAR" "$BASE/api/v1/update?refresh=1")
check "第二次 cached=true" true "$(jq -r .cached <<<"$RESP2")"
check "缓存内容与首次一致（state|latest）" \
  "$(jq -r '.state + "|" + (.latest // "-")' <<<"$RESP")" \
  "$(jq -r '.state + "|" + (.latest // "-")' <<<"$RESP2")"
check "第二次 checkedAt 没刷新" "$(jq -r .checkedAt <<<"$RESP")" "$(jq -r .checkedAt <<<"$RESP2")"

echo
echo "== 9. 清理临时用户 =="
[[ -n "$NEW_ID" ]] && json "$ADMIN_JAR" "$BASE/api/v1/users/$NEW_ID" DELETE >/dev/null
check "临时用户已删除" true "$(json "$ADMIN_JAR" "$BASE/api/v1/users" \
  | jq --arg u "$UNAME" '[.users[]|select(.username==$u)]|length==0')"

echo
if [[ $fail -eq 0 ]]; then printf '检查更新：%d 项全过\n' "$pass"
else printf '检查更新：%d 过 / %d 败\n' "$pass" "$fail"; fi
[[ $fail -eq 0 ]]
