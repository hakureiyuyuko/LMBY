#!/usr/bin/env bash
# M2「设置页（TMDB 凭据）」的真库验收。
#
# 验四件事：
#   1. 密钥**永远不回显**（响应里不能出现凭据值）；
#   2. 保存后立刻生效：填一个错的 token，真实请求马上失败 —— 证明换凭据不需要重启；
#   3. 库里存的是**密文**（enc:v1: 前缀，且不含明文）；
#   4. 权限：非管理员 403、未登录 401；「恢复为配置文件的值」能回落。
#
# 跑完会把设置恢复原样（删掉数据库里那条），不影响原有部署。
#
# 用法：LMBY_USER=devtest LMBY_PASS=口令 bash scripts/dev/verify-settings.sh
set -uo pipefail

BASE="${BASE:-http://127.0.0.1:8099}"
USER="${LMBY_USER:-}"
PASS="${LMBY_PASS:-}"
if [[ -z "$USER" || -z "$PASS" ]]; then
  echo "用法：LMBY_USER=devtest LMBY_PASS=口令 bash scripts/dev/verify-settings.sh"
  exit 2
fi

export PGCLIENTENCODING="${PGCLIENTENCODING:-UTF8}"
CONFIG_FILE="${CONFIG_FILE:-/etc/lmby/config.toml}"
PW_FILE="${PW_FILE:-/etc/lmby/pg-password}"

JAR=$(mktemp); JAR2=$(mktemp)
trap 'rm -f "$JAR" "$JAR2"' EXIT

pass=0; fail=0
ok()    { printf '  \033[32mok\033[0m   %s\n' "$1"; pass=$((pass+1)); }
bad()   { printf '  \033[31mFAIL\033[0m %s\n        期望 %s\n        实际 %s\n' "$1" "$2" "$3"; fail=$((fail+1)); }
check() { if [[ "$2" == "$3" ]]; then ok "$1"; else bad "$1" "$2" "$3"; fi; }
note()  { printf '  \033[33m--\033[0m   %s\n' "$1"; }

st()   { curl -s -o /dev/null -w '%{http_code}' "$@"; }
bd()   { curl -s "$@"; }
json() { curl -s -H 'Content-Type: application/json' "$@"; }

psql_q() { PGPASSWORD="$(cat "$PW_FILE")" psql -tAq -h 127.0.0.1 -U lmby -d lmby -c "$1"; }

echo "目标：$BASE"

echo
echo "== 1. 登录（管理员）=="
code=$(st -X POST "$BASE/api/v1/auth/login" -c "$JAR" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg u "$USER" --arg p "$PASS" '{username:$u,password:$p}')")
check "登录 $USER = 200" 200 "$code"
[[ "$code" != "200" ]] && exit 1

echo
echo "== 2. 读设置：结构对、密钥不回显 =="
s=$(bd -b "$JAR" "$BASE/api/v1/settings")
check "GET /api/v1/settings = 200" 200 "$(st -b "$JAR" "$BASE/api/v1/settings")"
check "含 tmdb 段" true "$(jq -r 'has("tmdb")' <<<"$s")"
check "含 system 段" true "$(jq -r 'has("system")' <<<"$s")"
check "tmdb 只给 has* 布尔（不回显值）" true \
  "$(jq -r '.tmdb | (has("hasReadToken") and has("hasApiKey") and (has("readToken")|not) and (has("apiKey")|not))' <<<"$s")"
check "system 报数据库字符集" true "$(jq -r '.system.databaseEncoding|length > 0' <<<"$s")"
check "system 报结构版本（≥7）" true "$(jq -r '.system.schemaVersion >= 7' <<<"$s")"

# 配置文件里的 token 前 12 位 —— 用来确认它没有出现在任何响应里
CFG_TOKEN=$(grep -oE 'read_token *= *"[^"]+"' "$CONFIG_FILE" | head -1 | sed 's/.*"\(.*\)"/\1/')
ORIG_LANG=$(jq -r '.tmdb.language' <<<"$s")
ORIG_FROM_DB=$(jq -r '.tmdb.fromDb' <<<"$s")
note "配置文件语言=$ORIG_LANG，当前来源来自数据库=$ORIG_FROM_DB"

if [[ -n "$CFG_TOKEN" && ${#CFG_TOKEN} -ge 12 ]]; then
  check "响应里没有出现配置文件的 token" false \
    "$(grep -qF -- "${CFG_TOKEN:0:12}" <<<"$s" && echo true || echo false)"
fi

echo
echo "== 3. 非管理员 = 403（临时造一个只读用户，跑完删掉）=="
psql_q "insert into users (username, display_name, password_hash, is_admin)
        select 'viewer_probe', '只读探针', password_hash, false from users where username = '$USER'"
viewer_login=$(st -X POST "$BASE/api/v1/auth/login" -c "$JAR2" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg p "$PASS" '{username:"viewer_probe",password:$p}')")
check "只读用户登录 = 200" 200 "$viewer_login"
if [[ "$viewer_login" == "200" ]]; then
  check "非管理员 GET 设置 = 403" 403 "$(st -b "$JAR2" "$BASE/api/v1/settings")"
  check "非管理员 PUT 设置 = 403" 403 \
    "$(st -b "$JAR2" -X PUT "$BASE/api/v1/settings/tmdb" -H 'Content-Type: application/json' -d '{"language":"en-US"}')"
  check "非管理员 DELETE 设置 = 403" 403 "$(st -b "$JAR2" -X DELETE "$BASE/api/v1/settings/tmdb")"
  check "非管理员 测试连接 = 403" 403 "$(st -b "$JAR2" -X POST "$BASE/api/v1/provider/test")"
fi
psql_q "delete from users where username = 'viewer_probe'" >/dev/null
note "已删除只读探针用户"

echo
echo "== 4. 未登录 = 401 =="
check "未登录 GET = 401" 401 "$(st "$BASE/api/v1/settings")"
check "未登录 PUT = 401" 401 \
  "$(st -X PUT "$BASE/api/v1/settings/tmdb" -H 'Content-Type: application/json' -d '{"language":"en-US"}')"

echo
echo "== 5. 保存语言（不动密钥）=="
r=$(json -b "$JAR" -X PUT "$BASE/api/v1/settings/tmdb" -H 'Content-Type: application/json' -d '{"language":"ja-JP"}')
check "PUT = 200" true "$(jq -r '.ok' <<<"$r")"
check "返回语言已更新" ja-JP "$(jq -r '.tmdb.language' <<<"$r")"
check "来源变成数据库" true "$(jq -r '.tmdb.fromDb' <<<"$r")"
s=$(bd -b "$JAR" "$BASE/api/v1/settings")
check "重读：语言" ja-JP "$(jq -r '.tmdb.language' <<<"$s")"
check "重读：来源仍是数据库" true "$(jq -r '.tmdb.fromDb' <<<"$s")"
check "重读：凭据仍然算已配置（配置兜底还在）" true "$(jq -r '.tmdb.configured' <<<"$s")"

echo
echo "== 6. 库里存的是密文 =="
stored=$(psql_q "select value::text from settings where key = 'tmdb.credentials'")
if [[ -z "$stored" ]]; then
  bad "settings 里应当有 tmdb.credentials" "有" "没有"
else
  check "密文带 enc:v1: 前缀" true "$(grep -qF 'enc:v1:' <<<"$stored" && echo true || echo false)"
  if [[ -n "$CFG_TOKEN" && ${#CFG_TOKEN} -ge 12 ]]; then
    check "库里没有明文 token" false "$(grep -qF -- "${CFG_TOKEN:0:12}" <<<"$stored" && echo true || echo false)"
  fi
fi

echo
echo "== 7. 运行时生效（填一个错 token，真请求应当立刻失败）=="
# 先把当前有效凭据记下来，稍后恢复
if [[ -n "$CFG_TOKEN" ]]; then
  r=$(json -b "$JAR" -X PUT "$BASE/api/v1/settings/tmdb" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg t "$CFG_TOKEN" '{readToken:$t}')")
  check "把配置文件里的 token 写进数据库 = 200" true "$(jq -r '.ok' <<<"$r")"
fi
t=$(json -b "$JAR" -X POST "$BASE/api/v1/provider/test")
check "凭据正确时测试连接 ok=true" true "$(jq -r '.ok' <<<"$t")"
check "测试连接拿到了结果" true "$(jq -r '(.count // 0) > 0' <<<"$t")"

r=$(json -b "$JAR" -X PUT "$BASE/api/v1/settings/tmdb" -H 'Content-Type: application/json' \
  -d '{"readToken":"definitely-not-a-valid-token"}')
check "换成错 token = 200（保存本身不校验凭据）" true "$(jq -r '.ok' <<<"$r")"
t=$(json -b "$JAR" -X POST "$BASE/api/v1/provider/test")
check "错 token 立刻失败（不需要重启）" false "$(jq -r '.ok' <<<"$t")"
check "失败时给出原因" true "$(jq -r '.error|length > 0' <<<"$t")"

# 恢复：语言改回去 + 删掉数据库里的设置（回落配置文件）
r=$(json -b "$JAR" -X PUT "$BASE/api/v1/settings/tmdb" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg l "$ORIG_LANG" '{language:$l}')")
check "恢复语言 = 200" true "$(jq -r '.ok' <<<"$r")"

echo
echo "== 8. 恢复为配置文件的值 =="
r=$(json -b "$JAR" -X DELETE "$BASE/api/v1/settings/tmdb")
check "DELETE = 200" true "$(jq -r '.ok' <<<"$r")"
check "DELETE 后来源不是数据库" false "$(jq -r '.tmdb.fromDb' <<<"$r")"
check "settings 表里已经没有这项" "" "$(psql_q "select coalesce(value::text,'') from settings where key='tmdb.credentials'")"
s=$(bd -b "$JAR" "$BASE/api/v1/settings")
check "重读：语言回到配置文件的值" "$ORIG_LANG" "$(jq -r '.tmdb.language' <<<"$s")"
check "重读：来源是配置文件" "$ORIG_FROM_DB" "$(jq -r '.tmdb.fromDb' <<<"$s")"
if [[ -n "$CFG_TOKEN" ]]; then
  t=$(json -b "$JAR" -X POST "$BASE/api/v1/provider/test")
  check "回落之后测试连接又通了（配置里的凭据生效）" true "$(jq -r '.ok' <<<"$t")"
fi

echo
echo "== 9. 参数校验 =="
check "空 body = 400" 400 \
  "$(st -b "$JAR" -X PUT "$BASE/api/v1/settings/tmdb" -H 'Content-Type: application/json' -d '{}')"
check "未知字段 = 400" 400 \
  "$(st -b "$JAR" -X PUT "$BASE/api/v1/settings/tmdb" -H 'Content-Type: application/json' -d '{"tokn":"x"}')"

echo
printf '\033[1m结果：%d 通过，%d 失败\033[0m\n' "$pass" "$fail"
[[ "$fail" -eq 0 ]]
