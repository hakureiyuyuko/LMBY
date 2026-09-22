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
echo "== 1.5 读路径：带可见库过滤的查询不能在真库上 500 =="
# 这一节的由来：M7 把「可见库」接进 SQL 之后，首页 / 收藏的语句**占位符与传参错位**
# （count 复用了带 $5 的条件串，却又多传了 limit/offset → $3/$4 在参数表里但 SQL 里
#  没有引用 → PostgreSQL 42P18「could not determine data type of parameter $3」），
# 表现是前端整页「读取首页失败」。这类错位编译期看不出来、单测也不连库，
# **只有真库真 HTTP 才炸**，所以把每条带 libraryFilter 的读接口都在这里打一遍。
psqlq() { PGPASSWORD=$(cat "$PGPASS_FILE") psql -h 127.0.0.1 -U lmby -d lmby -tAc "$1"; }
# 让验收用户「看过」一条有流派的条目：首页只有这种情况下才走「为你推荐」，
# 而画像 / 种子 / 候选池三段各自带着一个可见库参数（第一个 bug 就藏在里面）。
SEED_ITEM=$(psqlq "select id from media_items where library_id = $FIRST and deleted_at is null
  and kind in ('movie','series') and genres <> '[]'::jsonb order by id limit 1")
if [[ -n "$SEED_ITEM" ]]; then
  psqlq "insert into playback_progress (user_id, item_id, position_ticks, duration_ticks, last_played_at, updated_at)
         values ($NEW_ID, $SEED_ITEM, 1000000, 2000000, now(), now())
         on conflict (user_id, item_id) do update set last_played_at = now(), updated_at = now()" >/dev/null
  note "给验收用户造了一条观看记录（条目 #$SEED_ITEM），首页会走「为你推荐」"
else
  note "库里没有带流派的条目，跳过「为你推荐」那一段"
fi

QTITLE=$(json "$ADMIN_JAR" "$BASE/api/v1/libraries/$FIRST/items?limit=1" | jq -r '.items[0].title // empty')
Q=$(jq -rn --arg t "${QTITLE:-a}" '$t|@uri')
for who in admin user; do
  jar=$ADMIN_JAR; [[ "$who" == user ]] && jar=$USER_JAR
  check "$who 首页 200" 200 "$(code "$jar" "$BASE/api/v1/home")"
  check "$who 收藏 200" 200 "$(code "$jar" "$BASE/api/v1/favorites")"
  check "$who 搜索 200" 200 "$(code "$jar" "$BASE/api/v1/search?q=$Q")"
  check "$who 搜索分面 200" 200 "$(code "$jar" "$BASE/api/v1/search/facets?q=$Q")"
  check "$who 联想 200" 200 "$(code "$jar" "$BASE/api/v1/search/suggest?q=$Q")"
  check "$who 库列表 200" 200 "$(code "$jar" "$BASE/api/v1/libraries")"
  check "$who 库浏览 200" 200 "$(code "$jar" "$BASE/api/v1/libraries/$FIRST/browse")"
  check "$who 库内条目 200" 200 "$(code "$jar" "$BASE/api/v1/libraries/$FIRST/items?limit=5")"
done
if [[ -n "$SEED_ITEM" ]]; then
  check "受限用户 条目详情 200" 200 "$(code "$USER_JAR" "$BASE/api/v1/items/$SEED_ITEM")"
  check "受限用户 相关条目 200" 200 "$(code "$USER_JAR" "$BASE/api/v1/items/$SEED_ITEM/related")"
  check "受限用户 演职员 200" 200 "$(code "$USER_JAR" "$BASE/api/v1/items/$SEED_ITEM/people")"
fi
# 200 还不够：响应体里带「失败」说明服务端是「部分成功」（首页 sections 少一行不明显）
check "首页响应里没有错误文案" 0 "$(json "$USER_JAR" "$BASE/api/v1/home" | grep -c '失败' || true)"
check "受限用户首页出了「为你推荐」" 1 \
  "$(json "$USER_JAR" "$BASE/api/v1/home" | jq -r '[.sections[]? | select(.key=="recommend")] | length')"

# 首页 500 时后端日志里的真实原因（脚本跑完给人一眼看到，不必再翻 journalctl）
if [[ "$(code "$USER_JAR" "$BASE/api/v1/home")" != "200" ]]; then
  note "日志里最后一条首页报错：$(journalctl -u lmby --since '-5 min' --no-pager 2>/dev/null | grep '读取首页' | tail -1)"
fi

echo
echo "== 2. 允许转码：关掉后必须转码的片直接 403，且不起 ffmpeg =="
json "$ADMIN_JAR" "$BASE/api/v1/users/$NEW_ID" PATCH '{"allowTranscode":false}' >/dev/null
check "关掉转码后旧 token 不受影响（没吊销：这个开关下一次请求就生效）" 200 "$(code "$USER_JAR" "$BASE/api/v1/libraries")"
# 找一条**真的要走转码**的条目 —— 挑法用 SQL，不是「盲扫前 25 条」：
# 浏览器解不开的编码（hevc / mpeg2video / vc1…）、10bit、HDR 这几类必然转码。
# 而且要**由管理员去确认决策结果**：受限用户打同一条会先被 403 拦掉，响应里
# 根本没有 mode —— 上一轮「挑不到需要转码的条目」的真身就在这里（脚本拿受限用户
# 去扫候选，遇到真需要转码的条目只会拿到 403，于是永远以为「库里没有」）。
CAND=$(psqlq "select i.id from media_items i
  join media_files f on f.item_id = i.id and f.deleted_at is null
  where i.deleted_at is null and i.kind in ('movie', 'episode') and f.probe_state = 'ok'
    and (coalesce(f.video_streams->0->>'codec', '') not in ('', 'h264')
         or coalesce((f.video_streams->0->>'bitDepth')::int, 8) > 8
         or (f.hdr is not null and f.hdr <> 'null'::jsonb))
  order by i.id limit 8")
note "SQL 挑出 $(wc -w <<<"$CAND") 条「可能要转码」的候选"
TXID=""; TXMODE=""; TXJSON=""
for id in $CAND; do
  r=$(json "$ADMIN_JAR" "$BASE/api/v1/items/$id/play" POST '{}')
  TXMODE=$(jq -r '.mode // empty' <<<"$r")
  if [[ "$TXMODE" == "transcode" ]]; then TXID="$id"; TXJSON="$r"; break; fi
done
check "库里能挑出一条「决策为转码」的条目" true "$([[ -n "$TXID" ]] && echo true || echo false)"
if [[ -n "$TXID" ]]; then
  note "需要转码的条目：#$TXID（$(jq -r '.title // ""' <<<"$TXJSON")，mode=$TXMODE）"
  # 为了确认决策，刚才真起了一路转码：先停干净，否则下面「403 时不起 ffmpeg」的基线会被它污染
  KEY=$(json "$ADMIN_JAR" "$BASE/api/v1/playback/sessions" \
    | jq -r --argjson id "$TXID" '[.sessions[]? | select(.itemId == $id) | .stream.key // empty] | first // empty')
  if [[ -n "$KEY" ]]; then
    curl -s -o /dev/null -b "$ADMIN_JAR" -X POST "$BASE/api/v1/playback/streams/$KEY/stop"
    note "已停掉刚才那一路转码会话（key=$KEY）"
  fi
  sleep 1
  before=$(pgrep -fc ffmpeg || true)
  st=$(curl -s -o /dev/null -w '%{http_code}' -b "$USER_JAR" -X POST "$BASE/api/v1/items/$TXID/play" \
    -H 'Content-Type: application/json' -d '{}')
  check "不允许转码：这条必须转码的片起播被拒（403）" 403 "$st"
  sleep 1
  after=$(pgrep -fc ffmpeg || true)
  check "403 时没有多起 ffmpeg" "$before" "$after"
fi

# 直播：带 force=transcode 就是「这一路必须转码」，用它把开关验实
# 挑一个「探测能通」的频道：**不要拿全量列表的第一条** —— 那可能正好是失效源，
# 起播 502 会跟「权限拒绝」混在一起（真踩到）。probe=ok 是探测快照，
# 快照也会过期（源站会 302 重定向到别的节点、节点会挂），所以下面再用管理员
# 做一次基线：源站真失效时把那条断言**降级为提示**，而不是「今天源站不好，验收红了」。
CHT=$(json "$ADMIN_JAR" "$BASE/api/v1/livetv/channels?probe=ok" | jq -r '.channels[0].id // empty')
if [[ -n "$CHT" ]]; then
  st2=$(curl -s -o /dev/null -w '%{http_code}' -b "$USER_JAR" -X POST "$BASE/api/v1/livetv/channels/$CHT/play" \
    -H 'Content-Type: application/json' -d '{"force":"transcode"}')
  check "不允许转码：直播强制转码起播被拒（403）" 403 "$st2"
  json "$ADMIN_JAR" "$BASE/api/v1/users/$NEW_ID" PATCH '{"allowTranscode":true}' >/dev/null
  st3=$(curl -s -o /dev/null -w '%{http_code}' -b "$USER_JAR" -X POST "$BASE/api/v1/livetv/channels/$CHT/play" \
    -H 'Content-Type: application/json' -d '{"force":"transcode"}')
  note "放开转码后再起播：$st3（200 = 真的放行了）"
  # 基线：管理员打同一条频道。起不来（502）说明是**源站**的事（探测快照过期），
  # 与权限无关 —— 硬断言 200 会变成「今天源站不好，验收红了」这种假失败。
  stbase=$(curl -s -o /dev/null -w '%{http_code}' -b "$ADMIN_JAR" -X POST "$BASE/api/v1/livetv/channels/$CHT/play" \
    -H 'Content-Type: application/json' -d '{"force":"transcode"}')
  if [[ "$stbase" == "200" ]]; then
    check "放开转码后同一路径放行（200）" 200 "$st3"
  else
    note "管理员对 #$CHT 也起不来（$stbase）—— 源站已失效，跳过这条断言（不是权限问题）"
  fi
else
  note "没有「探测能通」的频道，跳过直播转码那两条断言"
fi

# 这一节的开关用完要恢复：后面第 3、4 节还在同一个账号上继续跑
json "$ADMIN_JAR" "$BASE/api/v1/users/$NEW_ID" PATCH '{"allowTranscode":true}' >/dev/null

echo
echo "== 3. 允许直播：关掉后列表与起播都拒绝 =="
json "$ADMIN_JAR" "$BASE/api/v1/users/$NEW_ID" PATCH '{"allowLiveTV":false}' >/dev/null
check "不允许直播：频道列表 403" 403 "$(code "$USER_JAR" "$BASE/api/v1/livetv/channels")"
# 权限判定在拉流之前（403 立刻返回、不会去连源站），挑哪条频道都不影响断言；
# 仍然用 probe=ok 挑，免得日志里混进失效源的无谓起播。
CH=$(json "$ADMIN_JAR" "$BASE/api/v1/livetv/channels?probe=ok" | jq -r '.channels[0].id // empty')
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
