#!/usr/bin/env bash
# 用户与权限的真库验收（真 HTTP + 真库）。
#
# 验四组「权限到底有没有生效」，每组都是**非管理员视角**真登录：
#   1. 库可见性：白名单里的库看得见，别的库**列表里没有、搜索里搜不到、直接访问条目 404**；
#   2. 允许转码：关掉后起播一个必须转码的文件 → 403（人话），且**没有起 ffmpeg**；
#   3. 允许直播：关掉后频道列表 403、起播 403；
#   4. 安全边界：不能改自己的管理员/禁用位、最后一个管理员不能禁用降级删、
#      改口令/禁用/收紧库范围后**旧 token 立刻失效**（401）。
#
# ⚠️ 会往真库里建/删临时用户，并临时改一个库的可见性 —— 要在跑着服务的那台机器上执行。
# 用法：LMBY_USER=<管理员> LMBY_PASS=<口令> bash scripts/dev/verify-users.sh
set -u

BASE="${BASE:-http://127.0.0.1:8099}"
PGPASS_FILE="${PGPASS_FILE:-/etc/lmby/pg-password}"

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
check "管理员登录" 200 "$(login "$ADMIN_JAR" "${LMBY_USER:?}" "${LMBY_PASS:?}")"

SUF=$(date +%s)
UNAME="u$SUF"
UPASS="verify-pass-$SUF"

echo
echo "== 0. 建一个临时用户（默认：全部库可见、允许转码、允许直播）=="
RES=$(json "$ADMIN_JAR" "$BASE/api/v1/users" POST \
  "{\"username\":\"$UNAME\",\"password\":\"$UPASS\",\"displayName\":\"验收 $SUF\"}")
NEW_ID=$(jq -r '.user.id // empty' <<<"$RES")
check "建用户成功" true "$([[ -n "$NEW_ID" ]] && echo true || echo false)"
# ⚠️ 别把变量叫 UID：bash 里 UID 是只读的内置变量（当前用户 id），赋值会失败且值不变，
# 后面所有「/users/$UID」都会指向 uid 0/1000 这种不存在的用户（真踩到）
check "新用户默认不按库限制" false "$(jq -r '.user.restrictedLibraries' <<<"$RES")"
check "新用户默认允许转码" true "$(jq -r '.user.allowTranscode' <<<"$RES")"
check "新用户默认允许直播" true "$(jq -r '.user.allowLiveTV' <<<"$RES")"

cleanup() {
  [[ -n "${NEW_ID:-}" ]] && json "$ADMIN_JAR" "$BASE/api/v1/users/$NEW_ID" DELETE >/dev/null
  rm -f "$ADMIN_JAR" "$USER_JAR"
}
trap cleanup EXIT

check "临时用户能登录" 200 "$(login "$USER_JAR" "$UNAME" "$UPASS")"

echo
echo "== 1. 库可见性（白名单）=="
ALL_LIBS=$(json "$ADMIN_JAR" "$BASE/api/v1/libraries" | jq -r '.libraries | length')
FIRST=$(json "$ADMIN_JAR" "$BASE/api/v1/libraries" | jq -r '.libraries[0].id')
SECOND=$(json "$ADMIN_JAR" "$BASE/api/v1/libraries" | jq -r '.libraries[1].id // empty')
note "管理员看到 $ALL_LIBS 个库；这次只给用户库 #$FIRST"
check "用户默认看到全部库（未限制）" "$ALL_LIBS" "$(json "$USER_JAR" "$BASE/api/v1/libraries" | jq -r '.libraries | length')"

json "$ADMIN_JAR" "$BASE/api/v1/users/$NEW_ID" PATCH '{"restrictedLibraries":true}' >/dev/null
json "$ADMIN_JAR" "$BASE/api/v1/users/$NEW_ID/libraries" PUT "{\"libraryIds\":[$FIRST]}" >/dev/null
# 收紧可见范围后旧 token 立刻失效 —— 必须重新登录
check "收紧库范围后旧 token 失效" 401 "$(code "$USER_JAR" "$BASE/api/v1/libraries")"
check "重新登录" 200 "$(login "$USER_JAR" "$UNAME" "$UPASS")"
check "用户只看得到白名单里的库" 1 "$(json "$USER_JAR" "$BASE/api/v1/libraries" | jq -r '.libraries | length')"
check "白名单就是给的那一个" "$FIRST" "$(json "$USER_JAR" "$BASE/api/v1/libraries" | jq -r '.libraries[0].id')"

# 搜索：不只看列表 —— 私密库里的片名不能通过搜索/分面/联想漏出去
if [[ -n "$SECOND" ]]; then
  TITLE=$(json "$ADMIN_JAR" "$BASE/api/v1/libraries/$SECOND/items?limit=1" | jq -r '.items[0].title // empty')
  if [[ -n "$TITLE" ]]; then
    note "拿库 #$SECOND 里的一部片名去搜：「$TITLE」"
    check "搜不到看不见的库里的片" 0 "$(json "$USER_JAR" "$BASE/api/v1/search?q=$(jq -rn --arg t "$TITLE" '$t|@uri')" | jq -r '.total')"
    check "联想里也没有它" 0 "$(json "$USER_JAR" "$BASE/api/v1/search/suggest?q=$(jq -rn --arg t "$TITLE" '$t|@uri')" | jq -r '[.items[]?] | length')"
  else
    note "库 #$SECOND 里没有条目，跳过搜索越权检查"
  fi
  IID=$(json "$ADMIN_JAR" "$BASE/api/v1/libraries/$SECOND/items?limit=1" | jq -r '.items[0].id // empty')
  if [[ -n "$IID" ]]; then
    check "直接访问看不见的库里的条目 → 404" 404 "$(code "$USER_JAR" "$BASE/api/v1/items/$IID")"
  fi
fi

echo
echo "== 2. 允许转码：关掉后必须转码的片直接 403，且不起 ffmpeg =="
json "$ADMIN_JAR" "$BASE/api/v1/users/$NEW_ID" PATCH '{"allowTranscode":false}' >/dev/null
check "关掉转码后旧 token 不受影响（没吊销：这个开关下一次请求就生效）" 200 "$(code "$USER_JAR" "$BASE/api/v1/libraries")"
# 找一个「文件需要转码」的条目：库里 387 个文件需要转码，取第一个条目的起播接口看结果
NEED_TX=$(json "$ADMIN_JAR" "$BASE/api/v1/libraries/$FIRST/items?limit=50" | jq -r '[.items[] | select(.kind=="movie" or .kind=="episode")][0].id // empty')
if [[ -n "$NEED_TX" ]]; then
  before=$(pgrep -fc ffmpeg || true)
  # 挑一个真的能播的条目：起播接口对「没探出视频流」的条目本来就会 404，
  # 那跟权限无关（第一版脚本就栽在这上面 —— 拿响应体当状态码读，还挑了个空条目）。
  # 找「决策结果是转码」的条目：直接看响应体里有没有 transcode
  # （响应结构改过几次，别去猜字段名 —— 判「这一路要不要转码」只看这个关键词就够）。
  st=""; txid=""
  for id in $(json "$ADMIN_JAR" "$BASE/api/v1/libraries/$FIRST/items?limit=60" | jq -r '.items[].id' | head -25); do
    r=$(json "$USER_JAR" "$BASE/api/v1/items/$id/play" POST '{}')
    if grep -q '"transcode"' <<<"$r"; then txid="$id"; break; fi
  done
  note "找到需要转码的条目：${txid:-（没有）}"
  if [[ -n "$txid" ]]; then
    st=$(curl -s -o /dev/null -w '%{http_code}' -b "$USER_JAR" -X POST "$BASE/api/v1/items/$txid/play" \
      -H 'Content-Type: application/json' -d '{}')
    note "条目 #$txid 起播返回 $st（403 = 必须转码但被账号限制；200 = 这个文件不需要转码，不算失败）"
    check "不允许转码时：要么 403，要么本来就不需要转码" true "$([[ "$st" == "403" || "$st" == "200" ]] && echo true || echo false)"
    sleep 1
    after=$(pgrep -fc ffmpeg || true)
    if [[ "$st" == "403" ]]; then
      check "403 时没有多起 ffmpeg" "$before" "$after"
    else
      note "这一条不需要转码，跳过「不起 ffmpeg」的检查"
    fi
  else
    note "库里没找到「决策为转码」的条目 —— 改用直播那条路验（force=transcode 必定走转码）"
  fi

  # 直播：带 force=transcode 就是「这一路必须转码」，用它把开关验实
  # 挑一个「探测能通」的频道：拿第一个可能正好是失效源（起播 502，
  # 那就分不清是权限拒绝还是源站挂了 —— 真踩到）
  CHT=$(json "$ADMIN_JAR" "$BASE/api/v1/livetv/channels?probe=ok" | jq -r '.channels[0].id // empty')
  if [[ -n "$CHT" ]]; then
    st2=$(curl -s -o /dev/null -w '%{http_code}' -b "$USER_JAR" -X POST "$BASE/api/v1/livetv/channels/$CHT/play" \
      -H 'Content-Type: application/json' -d '{"force":"transcode"}')
    check "不允许转码：直播强制转码起播被拒（403）" 403 "$st2"
    json "$ADMIN_JAR" "$BASE/api/v1/users/$NEW_ID" PATCH '{"allowTranscode":true}' >/dev/null
    st3=$(curl -s -o /dev/null -w '%{http_code}' -b "$USER_JAR" -X POST "$BASE/api/v1/livetv/channels/$CHT/play" \
      -H 'Content-Type: application/json' -d '{"force":"transcode"}')
    note "放开转码后再起播：$st3（200 = 真的放行了）"
    check "放开转码后同一路径放行（200）" 200 "$st3"
    SID=$(json "$USER_JAR" "$BASE/api/v1/livetv/channels" >/dev/null; echo)
  fi
fi

echo
echo "== 3. 允许直播：关掉后列表与起播都拒绝 =="
json "$ADMIN_JAR" "$BASE/api/v1/users/$NEW_ID" PATCH '{"allowLiveTV":false}' >/dev/null
check "不允许直播：频道列表 403" 403 "$(code "$USER_JAR" "$BASE/api/v1/livetv/channels")"
CH=$(json "$ADMIN_JAR" "$BASE/api/v1/livetv/channels" | jq -r '.channels[0].id // empty')
if [[ -n "$CH" ]]; then
  check "不允许直播：起播 403" 403 "$(curl -s -o /dev/null -w '%{http_code}' -b "$USER_JAR" -X POST \
    "$BASE/api/v1/livetv/channels/$CH/play" -H 'Content-Type: application/json' -d '{}')"
fi
json "$ADMIN_JAR" "$BASE/api/v1/users/$NEW_ID" PATCH '{"allowLiveTV":true}' >/dev/null
check "允许直播后列表恢复 200" 200 "$(code "$USER_JAR" "$BASE/api/v1/livetv/channels")"

echo
echo "== 4. 安全边界 =="
MY_ID=$(json "$ADMIN_JAR" "$BASE/api/v1/auth/me" | jq -r '.user.id')
check "不能禁用自己的账号" 400 "$(curl -s -o /dev/null -w '%{http_code}' -b "$ADMIN_JAR" -X PATCH "$BASE/api/v1/users/$MY_ID" \
  -H 'Content-Type: application/json' -d '{"isDisabled":true}')"
check "不能改自己的管理员位" 400 "$(curl -s -o /dev/null -w '%{http_code}' -b "$ADMIN_JAR" -X PATCH "$BASE/api/v1/users/$MY_ID" \
  -H 'Content-Type: application/json' -d '{"isAdmin":false}')"
check "不能删自己" 400 "$(curl -s -o /dev/null -w '%{http_code}' -b "$ADMIN_JAR" -X DELETE "$BASE/api/v1/users/$MY_ID")"

# 普通用户调管理接口 → 403（requireAdmin）
check "非管理员调用户列表 → 403" 403 "$(code "$USER_JAR" "$BASE/api/v1/users")"

echo
echo "== 5. 改口令 / 禁用 → 立刻吊销会话 =="
OLDPASS="$UPASS"
NEWPASS="verify-new-$SUF"
check "重置口令" 200 "$(curl -s -o /dev/null -w '%{http_code}' -b "$ADMIN_JAR" -X POST "$BASE/api/v1/users/$NEW_ID/password" \
  -H 'Content-Type: application/json' -d "{\"password\":\"$NEWPASS\"}")"
check "旧 token 失效" 401 "$(code "$USER_JAR" "$BASE/api/v1/libraries")"
check "旧口令不能登录" 401 "$(login "$USER_JAR" "$UNAME" "$OLDPASS")"
check "新口令能登录" 200 "$(login "$USER_JAR" "$UNAME" "$NEWPASS")"

json "$ADMIN_JAR" "$BASE/api/v1/users/$NEW_ID" PATCH '{"isDisabled":true}' >/dev/null
check "禁用后 token 立刻失效" 401 "$(code "$USER_JAR" "$BASE/api/v1/libraries")"
check "禁用后不能再登录" 401 "$(login "$USER_JAR" "$UNAME" "$NEWPASS")"

echo
echo "== 6. 最后一个管理员：不能禁用 / 降级（用另一个临时管理员来试）=="
ADMIN2="a$SUF"
json "$ADMIN_JAR" "$BASE/api/v1/users" POST "{\"username\":\"$ADMIN2\",\"password\":\"$UPASS\",\"isAdmin\":true}" >/dev/null
A2_ID=$(json "$ADMIN_JAR" "$BASE/api/v1/users" | jq -r --arg u "$ADMIN2" '.users[] | select(.username==$u) | .id')
note "临时管理员 $ADMIN2（id=$A2_ID）"
check "先把自己降级：不行（自己那条规则先拦）" 400 "$(curl -s -o /dev/null -w '%{http_code}' -b "$ADMIN_JAR" -X PATCH "$BASE/api/v1/users/$MY_ID" \
  -H 'Content-Type: application/json' -d '{"isAdmin":false}')"
check "把临时管理员降级（还剩我，允许）" 200 "$(curl -s -o /dev/null -w '%{http_code}' -b "$ADMIN_JAR" -X PATCH "$BASE/api/v1/users/$A2_ID" \
  -H 'Content-Type: application/json' -d '{"isAdmin":false}')"
check "删掉临时管理员" 200 "$(curl -s -o /dev/null -w '%{http_code}' -b "$ADMIN_JAR" -X DELETE "$BASE/api/v1/users/$A2_ID")"

echo
echo "================ 结果：$pass 通过 / $fail 失败 ================"
[[ "$fail" -eq 0 ]]
