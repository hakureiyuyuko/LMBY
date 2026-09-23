#!/usr/bin/env bash
# 管理 API 密钥（给 bot / 脚本用）的真库验收。
#
# 验四件事：
#   ① 生成密钥后，用它能做完「注册 / 修改 / 删除账号」这一整套；
#   ② **最小权限**：同一把钥匙开不了别的门（建媒体库、读审计都 403）——
#      这是这个功能最重要的性质：密钥泄露时不该等于交出整个服务器；
#   ③ 错密钥、撤销之后都必须立刻 401；
#   ④ 审计里记的是 `api-token`（一眼看出是程序干的，而不是某个人的会话）。
#
# 收尾：撤销密钥、删掉临时账号（不留任何东西）。
#
# 用法：LMBY_USER=devtest LMBY_PASS=口令 bash scripts/dev/verify-bot-api.sh
set -uo pipefail

BASE="${BASE:-http://127.0.0.1:8099}"
USER="${LMBY_USER:-}"
PASS="${LMBY_PASS:-}"
SUFFIX=$$
TMPUSER="bot-probe-$SUFFIX"

if [[ -z "$USER" || -z "$PASS" ]]; then
  echo "用法：LMBY_USER=devtest LMBY_PASS=口令 bash scripts/dev/verify-bot-api.sh" >&2
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
# bot 请求：默认走 Authorization: Bearer
bot() { curl -s -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' "$@"; }
botcode() { curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' "$@"; }

JAR=$(mktemp)
trap 'rm -f "$JAR"' EXIT

echo "== 0. 管理员登录，并确认还没配置密钥 =="
check "管理员登录" 200 "$(code -b "$JAR" -c "$JAR" -X POST "$BASE/api/v1/auth/login" \
  -d "$(jq -nc --arg u "$USER" --arg p "$PASS" '{username:$u,password:$p}')")"
check "配置状态可读" 200 "$(code -b "$JAR" "$BASE/api/v1/settings/bot-key")"

echo
echo "== 1. 生成密钥 =="
RESP=$(json -b "$JAR" -X POST "$BASE/api/v1/settings/bot-key")
KEY=$(echo "$RESP" | jq -r '.key // empty')
check "拿到了密钥" true "$([[ -n "$KEY" ]] && echo true || echo false)"
check "密钥带可辨认的前缀" true "$([[ "$KEY" == lmby_* ]] && echo true || echo false)"
note "密钥前缀：$(echo "$RESP" | jq -r '.prefix')（明文只在这里出现一次）"
check "再查只回前缀、不回明文" false "$(json -b "$JAR" "$BASE/api/v1/settings/bot-key" | jq -r 'has("key")')"

echo
echo "== 2. 用密钥把「账号的一生」走完 =="
check "列账号" 200 "$(botcode "$BASE/api/v1/users")"
NEW_ID=$(bot -X POST "$BASE/api/v1/users" \
  -d "$(jq -nc --arg u "$TMPUSER" '{username:$u,password:"bot-probe-pass-1"}')" | jq -r '.user.id // empty')
check "注册账号" true "$([[ -n "$NEW_ID" ]] && echo true || echo false)"
note "新账号 id=$NEW_ID（$TMPUSER）"
check "改账号（显示名）" 200 "$(botcode -X PATCH "$BASE/api/v1/users/$NEW_ID" -d '{"displayName":"Bot Probe"}')"
check "改可见库" 200 "$(botcode -X PUT "$BASE/api/v1/users/$NEW_ID/libraries" -d '{"libraryIds":[]}')"
check "重置口令" 200 "$(botcode -X POST "$BASE/api/v1/users/$NEW_ID/password" -d '{"password":"bot-probe-pass-2"}')"
check "用 X-API-Key 头也能过（不是只认 Bearer）" 200 \
  "$(curl -s -o /dev/null -w '%{http_code}' -H "X-API-Key: $KEY" "$BASE/api/v1/users")"

echo
echo "== 3. 最小权限：同一把钥匙开不了别的门 =="
check "建媒体库 → 403" 403 "$(botcode -X POST "$BASE/api/v1/libraries" \
  -d '{"name":"bot-should-not-create","kind":"mixed","paths":["/tmp"]}')"
check "读审计 → 403" 403 "$(botcode "$BASE/api/v1/audit")"
check "读维护信息 → 403" 403 "$(botcode "$BASE/api/v1/maintenance")"
check "改设置 → 403" 403 "$(botcode -X POST "$BASE/api/v1/settings/bot-key")"

echo
echo "== 4. 密钥错误 / 撤销 =="
check "错密钥 → 401" 401 "$(curl -s -o /dev/null -w '%{http_code}' \
  -H 'Authorization: Bearer lmby_definitely-wrong' "$BASE/api/v1/users")"
check "什么都不带 → 401" 401 "$(code "$BASE/api/v1/users")"
check "删账号（撤销前还能用）" 200 "$(botcode -X DELETE "$BASE/api/v1/users/$NEW_ID")"
check "撤销密钥" 200 "$(code -b "$JAR" -X DELETE "$BASE/api/v1/settings/bot-key")"
check "撤销后立刻失效 → 401" 401 "$(botcode "$BASE/api/v1/users")"

echo
echo "== 5. 审计里能看出是程序干的 =="
n=$(json -b "$JAR" "$BASE/api/v1/audit?limit=50" | jq -r '[.entries[] | select(.actorName=="api-token")] | length')
check "审计里有 actorName=api-token 的记录" true "$([[ "${n:-0}" -gt 0 ]] && echo true || echo false)"
note "其中 $(json -b "$JAR" "$BASE/api/v1/audit?limit=50" | jq -r '[.entries[] | select(.actorName=="api-token")] | length') 条来自密钥"
check "生成密钥这件事本身也被记了" true \
  "$(json -b "$JAR" "$BASE/api/v1/audit?action=settings.bot_key_created&limit=10" | jq -r '(.total // 0) > 0')"

echo
echo "== 6. 收尾 =="
if [[ -n "$NEW_ID" ]]; then
  # 上面删过一次；万一没删掉再补一刀（幂等）
  code -b "$JAR" -X DELETE "$BASE/api/v1/users/$NEW_ID" >/dev/null
fi
check "没有残留的临时账号" 0 \
  "$(json -b "$JAR" "$BASE/api/v1/users" | jq -r --arg u "$TMPUSER" '[.users[] | select(.username==$u)] | length')"

echo
echo "================ 结果：$pass 通过 / $fail 失败 ================"
[[ "$fail" -eq 0 ]]
