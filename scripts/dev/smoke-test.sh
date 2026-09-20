#!/usr/bin/env bash
# LMBY 后端冒烟测试（M0 验收用）
#
# 对正在运行的服务做一遍端到端 HTTP 断言，不需要任何测试框架依赖（curl + jq）。
#
# 用法：
#   bash smoke-test.sh                                  # 空库：走初始化向导创建账号
#   SMOKE_USER=admin SMOKE_PASS=xxx bash smoke-test.sh  # 已有账号：走登录路径
#   BASE=http://127.0.0.1:8099 bash smoke-test.sh
#
# 退出码：0 全部通过；1 有失败。
set -uo pipefail

BASE="${BASE:-http://127.0.0.1:8099}"
JAR="$(mktemp)"; JAR2="$(mktemp)"
trap 'rm -f "$JAR" "$JAR2"' EXIT

pass=0; fail=0
ok()    { printf '  \033[32mok\033[0m   %s\n' "$1"; pass=$((pass+1)); }
bad()   { printf '  \033[31mFAIL\033[0m %s（期望 %s，实际 %s）\n' "$1" "$2" "$3"; fail=$((fail+1)); }
check() { if [[ "$2" == "$3" ]]; then ok "$1"; else bad "$1" "$2" "$3"; fi; }
note()  { printf '  \033[33m--\033[0m   %s\n' "$1"; }

st() { curl -s -o /dev/null -w '%{http_code}' "$@"; }
bd() { curl -s "$@"; }
json() { curl -s -H 'Content-Type: application/json' "$@"; }

echo "目标：$BASE"
echo
echo "== 1. 健康检查与元信息 =="
h="$(bd "$BASE/healthz")"
check "GET /healthz = 200" 200 "$(st "$BASE/healthz")"
check "/healthz status" ok "$(jq -r .status <<<"$h")"
check "/healthz database.ok" true "$(jq -r .database.ok <<<"$h")"
check "/healthz ffmpeg.available" true "$(jq -r .ffmpeg.available <<<"$h")"
check "/healthz 报告了 version" true "$([[ $(jq -r '.version|length' <<<"$h") -gt 0 ]] && echo true || echo false)"

m="$(bd "$BASE/api/v1/meta")"
check "GET /api/v1/meta = 200" 200 "$(st "$BASE/api/v1/meta")"
check "meta 含 setupRequired 字段" true "$(jq -r 'has("setupRequired")' <<<"$m")"

echo
echo "== 2. 鉴权边界 =="
check "未登录 GET /api/v1/auth/me = 401" 401 "$(st "$BASE/api/v1/auth/me")"
check "未登录 POST /auth/logout = 401" 401 "$(st -X POST "$BASE/api/v1/auth/logout")"
check "未登录 PATCH /auth/me/preferences = 401" 401 \
  "$(st -X PATCH "$BASE/api/v1/auth/me/preferences" -H 'Content-Type: application/json' -d '{"theme":"dark"}')"
check "未登录 DELETE /auth/sessions/x = 401" 401 "$(st -X DELETE "$BASE/api/v1/auth/sessions/x")"

echo
echo "== 3. 获取会话 =="
USER="${SMOKE_USER:-}"
PASS="${SMOKE_PASS:-}"
PASS_NEW='lmby-smoke-newpass-2'

if [[ -n "$USER" ]]; then
  note "使用已有账号 $USER 登录"
  check "登录成功 = 200" 200 \
    "$(st -X POST "$BASE/api/v1/auth/login" -c "$JAR" -H 'Content-Type: application/json' \
        -d "$(jq -nc --arg u "$USER" --arg p "$PASS" '{username:$u,password:$p}')")"
else
  USER="smoke$(date +%s)"
  PASS='lmby-smoke-pass-1'
  code=$(st -X POST "$BASE/api/v1/setup" -c "$JAR" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg u "$USER" --arg p "$PASS" '{username:$u,password:$p,displayName:"冒烟测试"}')")
  if [[ "$code" == "201" ]]; then
    ok "初始化向导创建首个管理员 = 201"
  else
    bad "初始化向导创建首个管理员" 201 "$code"
    note "系统可能已初始化，请改用 SMOKE_USER/SMOKE_PASS 重跑"
  fi
fi

echo
echo "== 4. 会话与个人中心 =="
me="$(bd -b "$JAR" "$BASE/api/v1/auth/me")"
check "GET /auth/me = 200" 200 "$(st -b "$JAR" "$BASE/api/v1/auth/me")"
check "/auth/me 用户名一致" "$USER" "$(jq -r .user.username <<<"$me")"
check "/auth/me 含 preferences" true "$(jq -r 'has("preferences")' <<<"$me")"
check "/auth/me 不泄露口令哈希" false "$(jq -r '.user|has("passwordHash")' <<<"$me")"

check "PATCH 偏好为 dark = 200" 200 \
  "$(st -b "$JAR" -X PATCH "$BASE/api/v1/auth/me/preferences" -H 'Content-Type: application/json' -d '{"theme":"dark"}')"
check "偏好已持久化" dark "$(bd -b "$JAR" "$BASE/api/v1/auth/me" | jq -r .preferences.theme)"

check "PATCH 非法主题被拒 = 400" 400 \
  "$(st -b "$JAR" -X PATCH "$BASE/api/v1/auth/me/preferences" -H 'Content-Type: application/json' -d '{"theme":"neon"}')"
check "PATCH 未知字段被拒 = 400" 400 \
  "$(st -b "$JAR" -X PATCH "$BASE/api/v1/auth/me/preferences" -H 'Content-Type: application/json' -d '{"theem":"dark"}')"

check "PATCH 显示名 = 200" 200 \
  "$(st -b "$JAR" -X PATCH "$BASE/api/v1/auth/me" -H 'Content-Type: application/json' -d '{"displayName":"新名字"}')"
check "显示名已生效" "新名字" "$(bd -b "$JAR" "$BASE/api/v1/auth/me" | jq -r .user.displayName)"

echo
echo "== 5. 我的设备（会话列表） =="
sess="$(bd -b "$JAR" "$BASE/api/v1/auth/sessions")"
check "GET /auth/sessions = 200" 200 "$(st -b "$JAR" "$BASE/api/v1/auth/sessions")"
check "会话列表非空" true "$(jq -r '.sessions|length > 0' <<<"$sess")"
check "其中一条标记为 current" true "$(jq -r 'any(.sessions[]; .current)' <<<"$sess")"
check "列表不含令牌本身（只有 sha256 形式的 id）" true \
  "$(jq -r '.sessions[0].id|test("^[0-9a-f]{64}$")' <<<"$sess")"

echo
echo "== 6. 修改口令 =="
check "旧口令填错被拒 = 401" 401 \
  "$(st -b "$JAR" -X POST "$BASE/api/v1/auth/password" -H 'Content-Type: application/json' \
      -d '{"currentPassword":"definitely-wrong","newPassword":"whatever-123"}')"
check "新口令过短被拒 = 400" 400 \
  "$(st -b "$JAR" -X POST "$BASE/api/v1/auth/password" -H 'Content-Type: application/json' \
      -d "$(jq -nc --arg c "$PASS" '{currentPassword:$c,newPassword:"short"}')")"

# 先另开一个会话，验证改密后它会被踢掉
st -X POST "$BASE/api/v1/auth/login" -c "$JAR2" -H 'Content-Type: application/json' \
   -d "$(jq -nc --arg u "$USER" --arg p "$PASS" '{username:$u,password:$p}')" >/dev/null
check "第二个会话可用" 200 "$(st -b "$JAR2" "$BASE/api/v1/auth/me")"

resp="$(json -b "$JAR" -X POST "$BASE/api/v1/auth/password" \
  -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg c "$PASS" --arg n "$PASS_NEW" '{currentPassword:$c,newPassword:$n}')")"
check "修改口令 = 200" true "$(jq -r '.ok // false' <<<"$resp")"
check "撤销了其他会话" true "$(jq -r '.revokedSessions >= 1' <<<"$resp")"
check "当前会话仍有效" 200 "$(st -b "$JAR" "$BASE/api/v1/auth/me")"
check "被踢的会话失效 = 401" 401 "$(st -b "$JAR2" "$BASE/api/v1/auth/me")"
check "旧口令登录失败 = 401" 401 \
  "$(st -X POST "$BASE/api/v1/auth/login" -H 'Content-Type: application/json' \
      -d "$(jq -nc --arg u "$USER" --arg p "$PASS" '{username:$u,password:$p}')")"
check "新口令登录成功 = 200" 200 \
  "$(st -X POST "$BASE/api/v1/auth/login" -H 'Content-Type: application/json' \
      -d "$(jq -nc --arg u "$USER" --arg p "$PASS_NEW" '{username:$u,password:$p}')")"

echo
echo "== 7. 登出 =="
check "POST /auth/logout = 200" 200 "$(st -b "$JAR" -X POST "$BASE/api/v1/auth/logout")"
check "登出后 /auth/me = 401" 401 "$(st -b "$JAR" "$BASE/api/v1/auth/me")"

echo
echo "== 8. 静态资源与路由兜底 =="
idx="$(bd "$BASE/")"
check "GET / = 200" 200 "$(st "$BASE/")"
check "返回 HTML" true "$([[ "$idx" == *"<!doctype html"* || "$idx" == *"<!DOCTYPE html"* ]] && echo true || echo false)"
check "未知接口返回 JSON 404" 404 "$(st "$BASE/api/v1/nope")"
check "未知接口是中转 JSON 错误" "接口不存在" "$(bd "$BASE/api/v1/nope" | jq -r .error)"
check "非法 JSON 请求体 = 400" 400 \
  "$(st -X POST "$BASE/api/v1/auth/login" -H 'Content-Type: application/json' -d '{oops')"

echo
printf '\033[1m结果：%d 通过，%d 失败\033[0m\n' "$pass" "$fail"
[[ "$fail" -eq 0 ]]
