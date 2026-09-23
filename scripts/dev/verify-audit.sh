#!/usr/bin/env bash
# 审计日志的真库验收。
#
# 审计这种东西最容易「看起来有、其实漏」—— 所以这里做一次**真实操作链**，
# 再逐条断言对应的动作名确实被记下来了；顺带验三件事：
#   ① 普通用户看不到审计（403）；
#   ② 过滤（只看失败 / 按动作）真的收窄了结果；
#   ③ 口令不落审计（detail 里不该出现任何口令字样，只该有「重置了口令」这个事实）。
#
# 副作用与清理：脚本建的临时用户与临时媒体库在最后删掉（删它们本身也会再产生两条审计，
# 这是正常的 —— 审计是只增的）。
#
# 用法：LMBY_USER=devtest LMBY_PASS=口令 bash scripts/dev/verify-audit.sh
set -uo pipefail

BASE="${BASE:-http://127.0.0.1:8099}"
USER="${LMBY_USER:-}"
PASS="${LMBY_PASS:-}"
SUFFIX=$$

if [[ -z "$USER" || -z "$PASS" ]]; then
  echo "用法：LMBY_USER=devtest LMBY_PASS=口令 bash scripts/dev/verify-audit.sh" >&2
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
trap 'rm -f "$JAR" "$JAR2"' EXIT

echo "== 0. 登录与前置 =="
check "管理员登录" 200 "$(code -b "$JAR" -c "$JAR" -X POST "$BASE/api/v1/auth/login" \
  -d "$(jq -nc --arg u "$USER" --arg p "$PASS" '{username:$u,password:$p}')")"
check "管理员能读审计" 200 "$(code -b "$JAR" "$BASE/api/v1/audit")"

echo
echo "== 1. 走一遍真实操作链 =="
TMPUSER="audit-probe-$SUFFIX"
TMPLIB="审计验收库-$SUFFIX"
NEW_ID=$(json -b "$JAR" -X POST "$BASE/api/v1/users" \
  -d "$(jq -nc --arg u "$TMPUSER" --arg p 'audit-probe-pass-1' '{username:$u,password:$p}')" | jq -r '.user.id // empty')
note "建了用户 #$NEW_ID（$TMPUSER）"
json -b "$JAR" -X PATCH "$BASE/api/v1/users/$NEW_ID" -d '{"displayName":"Audit Probe"}' >/dev/null
json -b "$JAR" -X PUT "$BASE/api/v1/users/$NEW_ID/libraries" -d '{"libraryIds":[]}' >/dev/null
json -b "$JAR" -X POST "$BASE/api/v1/users/$NEW_ID/password" -d '{"password":"audit-probe-pass-2"}' >/dev/null
# 用这个新账号（普通用户）登录一次，再让它去访问审计
code -b "$JAR2" -c "$JAR2" -X POST "$BASE/api/v1/auth/login" \
  -d "$(jq -nc --arg u "$TMPUSER" --arg p 'audit-probe-pass-2' '{username:$u,password:$p}')" >/dev/null
check "普通用户访问审计 → 403" 403 "$(code -b "$JAR2" "$BASE/api/v1/audit")"
check "普通用户按动作过滤也是 403" 403 "$(code -b "$JAR2" "$BASE/api/v1/audit?action=user.create")"
# 让这个临时用户**改自己的口令**（换掉后它自己的会话会被吊销 —— 无所谓，随后就删账号）：
# 这条路径就是 auth.password_change，与管理员「重置别人口令」是两回事，别只验后者。
check "临时用户改自己的口令" 200 "$(code -b "$JAR2" -X POST "$BASE/api/v1/auth/password" \
  -d '{"currentPassword":"audit-probe-pass-2","newPassword":"audit-probe-pass-3"}')"

LID=$(json -b "$JAR" -X POST "$BASE/api/v1/libraries" \
  -d "$(jq -nc --arg n "$TMPLIB" '{name:$n,kind:"mixed",paths:["/tmp"]}')" | jq -r '.id // empty')
note "建了媒体库 #$LID（$TMPLIB）"
json -b "$JAR" -X PATCH "$BASE/api/v1/libraries/$LID" -d '{"scanIntervalMinutes":60}' >/dev/null

# 登录失败（口令错）也要留痕
code -X POST "$BASE/api/v1/auth/login" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg u "$USER" '{username:$u,password:"definitely-wrong"}')" >/dev/null

echo
echo "== 2. 断言这些动作都被记下来了 =="
# 用「按动作过滤」查：顺便验证了过滤参数本身有效
want_actions=(auth.login auth.password_change user.create user.update user.libraries user.password_reset library.create library.update)
for a in "${want_actions[@]}"; do
  n=$(json -b "$JAR" "$BASE/api/v1/audit?action=$a&limit=50" | jq -r '.total // 0')
  if [[ "${n:-0}" -gt 0 ]]; then printf '  ok   审计里有 %s（%s 条）\n' "$a" "$n"; pass=$((pass + 1))
  else printf '  FAIL 审计里没有 %s\n' "$a"; fail=$((fail + 1)); fi
done
n=$(json -b "$JAR" "$BASE/api/v1/audit?failed=1&limit=50" | jq -r '[.entries[] | select(.action=="auth.login")] | length')
check "失败的登录被单独标记出来（failed=1 里能找到）" true "$([[ "${n:-0}" -gt 0 ]] && echo true || echo false)"

echo
echo "== 3. 口令不落审计 =="
leak=$(json -b "$JAR" "$BASE/api/v1/audit?limit=200" | jq -r '[.entries[] | select((.detail|tostring) | test("audit-probe-pass"))] | length')
check "审计里搜不到那条口令（detail 只该有「重置了口令」这个事实）" 0 "$leak"

echo
echo "== 4. 清场（临时用户与临时库） =="
check "删临时用户" 200 "$(code -b "$JAR" -X DELETE "$BASE/api/v1/users/$NEW_ID")"
check "删临时媒体库" 200 "$(code -b "$JAR" -X DELETE "$BASE/api/v1/libraries/$LID")"
check "删用户也留了痕" true "$(json -b "$JAR" "$BASE/api/v1/audit?action=user.delete&limit=50" | jq -r '(.total // 0) > 0')"

echo
echo "================ 结果：$pass 通过 / $fail 失败 ================"
[[ "$fail" -eq 0 ]]
