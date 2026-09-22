#!/usr/bin/env bash
# 直播「起播失败 → 自动重探」验收（2026-09-22 新增）。
#
# 验的是一条**运维行为**：前台「只列能用能看的」用的是探测快照（`probe_ok`），
# 而源站是外部世界、会变（RTSP 302 换调度节点、节点会挂）。所以起播失败时
# 后台要真探一次，让这一台按**最新**结果决定去留；同时不能因为「本地并发打满」
# 这种与源站无关的失败去把好频道标成失效。
#
# 为什么要**自造**源：真实 IPTV 源的可用性随时刻与网络变化，不能当断言依据。
# 这里在本机起一个静态 HTTP，放一个真视频；先探一次、起播一次证明它是通的，
# **然后把 HTTP 服务杀掉**（模拟源站挂掉），再起播 → 502 → 等自动重探落地，
# 看它是不是把这条频道标成了失效、并从前台口径消失。
#
# 用法：
#   LMBY_USER=devtest LMBY_PASS=xxx bash scripts/dev/verify-livetv-reprobe.sh
# 环境变量：
#   BASE   服务地址（默认 http://127.0.0.1:8099）
#   PORT   自造源用的本地 HTTP 端口（默认随机 18000~19999）
#
# 副作用与清理：临时源、临时频道、临时 HTTP 都在 EXIT 陷阱里收拾。
set -uo pipefail

BASE=${BASE:-http://127.0.0.1:8099}
PORT=${PORT:-$((18000 + RANDOM % 2000))}
USER=${LMBY_USER:-}
PASS=${LMBY_PASS:-}
GROUP="验收重探-$$"
SRC_NAME="验收-起播重探-$$"
PG_PASS_FILE=${PG_PASS_FILE:-/etc/lmby/pg-password}

PASS_N=0
FAIL_N=0
check() { # check 名称 期望 实际
  if [[ "$2" == "$3" ]]; then
    printf '  ok   %s\n' "$1"; PASS_N=$((PASS_N + 1))
  else
    printf '  FAIL %s（期望 %q，实际 %q）\n' "$1" "$2" "$3"; FAIL_N=$((FAIL_N + 1))
  fi
}
check_ne() { # check_ne 名称 不该等于 实际
  if [[ "$2" != "$3" ]]; then
    printf '  ok   %s\n' "$1"; PASS_N=$((PASS_N + 1))
  else
    printf '  FAIL %s（不该等于 %q）\n' "$1" "$2"; FAIL_N=$((FAIL_N + 1))
  fi
}
check_has() { # check_has 名称 期望包含的子串 实际
  if [[ "$3" == *"$2"* ]]; then
    printf '  ok   %s\n' "$1"; PASS_N=$((PASS_N + 1))
  else
    printf '  FAIL %s（期望包含 %q，实际 %q）\n' "$1" "$2" "$3"; FAIL_N=$((FAIL_N + 1))
  fi
}
note() { printf '  --   %s\n' "$1"; }

if [[ -z "$USER" || -z "$PASS" ]]; then
  echo "用法：LMBY_USER=... LMBY_PASS=... bash $0" >&2
  exit 2
fi

sql() {
  local pw="${PGPASSWORD:-$(cat "$PG_PASS_FILE" 2>/dev/null)}"
  PGPASSWORD="$pw" psql -h 127.0.0.1 -U lmby -d lmby -tAc "$1" 2>/dev/null
}

WORK=$(mktemp -d)
JAR=$(mktemp)
WWW="$WORK/www"
mkdir -p "$WWW"
SRV_PID=""
SRC_ID=""

cleanup() {
  [[ -n "$SRV_PID" ]] && kill "$SRV_PID" 2>/dev/null
  [[ -n "$SRC_ID" ]] && curl -s -o /dev/null -b "$JAR" -X DELETE "$BASE/api/v1/livetv/sources/$SRC_ID"
  sql "delete from tv_channels where url like 'http://127.0.0.1:$PORT/%'" >/dev/null
  rm -rf "$WORK" "$JAR"
}
trap cleanup EXIT

json() { # json <url> [method] [body]
  local method="${2:-GET}" body="${3:-}"
  if [[ -n "$body" ]]; then
    curl -s -b "$JAR" -X "$method" "$1" -H 'Content-Type: application/json' --data-binary "$body"
  else
    curl -s -b "$JAR" -X "$method" "$1"
  fi
}
play_code() { # 起播这条频道，回 HTTP 状态码
  curl -s -o "$WORK/play.json" -w '%{http_code}' -b "$JAR" -X POST \
    "$BASE/api/v1/livetv/channels/$1/play" -H 'Content-Type: application/json' -d '{}'
}

echo "== 0. 前置 =="
code=$(curl -s -o /dev/null -w '%{http_code}' -c "$JAR" -X POST "$BASE/api/v1/auth/login" \
  -H 'Content-Type: application/json' --data-binary "$(jq -nc --arg u "$USER" --arg p "$PASS" '{username:$u,password:$p}')")
check "登录" 200 "$code"
[[ "$code" == "200" ]] || exit 1
check "能连数据库（本脚本要读 probe/probe_ok）" "1" "$(sql 'select 1')"

# 清掉上次被 SIGPIPE 打断留下的（EXIT 陷阱不一定跑）
for old in $(json "$BASE/api/v1/livetv/sources" | jq -r '.sources[] | select(.name | startswith("验收-起播重探-")) | .id'); do
  curl -s -o /dev/null -b "$JAR" -X DELETE "$BASE/api/v1/livetv/sources/$old"
done
sql "delete from tv_channels where url like 'http://127.0.0.1:%/ok.mp4' and name like '验收-%'" >/dev/null

echo
echo "== 1. 造一个真能播的源（本机 HTTP + 一段真视频） =="
ffmpeg -hide_banner -loglevel error -y -f lavfi -i testsrc=size=320x240:rate=10 -t 4 \
  -c:v libx264 -pix_fmt yuv420p -movflags +faststart "$WWW/ok.mp4" \
  || { echo "  生成测试视频失败（缺 ffmpeg/libx264？）" >&2; exit 1; }
cat > "$WWW/list.m3u" <<EOF
#EXTM3U
#EXTINF:-1 group-title="$GROUP",验收-重探-目标
http://127.0.0.1:$PORT/ok.mp4
EOF
( cd "$WORK" && exec python3 -m http.server "$PORT" --bind 127.0.0.1 --directory "$WWW" ) >"$WORK/http.log" 2>&1 &
SRV_PID=$!
sleep 1
check "自造源可访问" 200 "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/list.m3u")"

CREATED=$(json "$BASE/api/v1/livetv/sources" POST "$(jq -nc --arg name "$SRC_NAME" --rawfile content "$WWW/list.m3u" \
  '{name:$name,kind:"paste",content:$content}')")
SRC_ID=$(jq -r '.source.id // empty' <<<"$CREATED")
check "导入 1 条频道" 1 "$(jq -r '.import.total' <<<"$CREATED")"
CH=$(json "$BASE/api/v1/livetv/channels?group=$GROUP" | jq -r '.channels[0].id // empty')
check "拿到频道 id" true "$([[ -n "$CH" ]] && echo true || echo false)"
[[ -n "$CH" ]] || exit 1

echo
echo "== 2. 基线：先真探一次 + 真起播一次（证明它本来是通的） =="
json "$BASE/api/v1/livetv/channels/probe" POST "$(jq -nc --argjson id "$CH" '{channelIds:[$id]}')" >/dev/null
PROBE_OK=""
for _ in $(seq 1 20); do
  PROBE_OK=$(sql "select probe_ok from tv_channels where id = $CH")
  [[ -n "$PROBE_OK" ]] && break
  sleep 1
done
check "探测判定为通" "t" "$PROBE_OK"
PROBE_SUMMARY=$(json "$BASE/api/v1/livetv/channels/$CH" | jq -r '.probe')
check_has "摘要里有真实流信息" "H.264" "$PROBE_SUMMARY"

ST=$(play_code "$CH")
check "起播成功（200）" 200 "$ST"
SID=$(jq -r '.sid // empty' "$WORK/play.json")
if [[ -n "$SID" ]]; then
  curl -s -o /dev/null -b "$JAR" -X POST "$BASE/api/v1/live/$SID/stop"
fi
check "前台口径里能看到它" 1 \
  "$(json "$BASE/api/v1/livetv/channels?enabled=1&hide_failed=1&group=$GROUP" | jq -r '.channels | length')"

echo
echo "== 3. 把源站弄挂（杀掉 HTTP），再起播 → 应该 502 =="
kill "$SRV_PID" 2>/dev/null
wait "$SRV_PID" 2>/dev/null
SRV_PID=""
# 让端口的监听真正消失：残留的 TIME_WAIT/keep-alive 会让 ffmpeg 连上旧进程
for _ in $(seq 1 20); do
  curl -s -o /dev/null --max-time 1 "http://127.0.0.1:$PORT/list.m3u" || break
  sleep 0.5
done
check "自造源确实不可访问了" "000" "$(curl -s -o /dev/null --max-time 2 -w '%{http_code}' "http://127.0.0.1:$PORT/list.m3u")"

# 先把这条会话停干净：否则 Manager 会复用一个还有分片的旧会话，压根不会去连源站
for m in copy transcode; do
  curl -s -o /dev/null -b "$JAR" -X POST "$BASE/api/v1/playback/streams/live:ch${CH}:${m}/stop"
done
sleep 1
ST=$(play_code "$CH")
check "源站挂了：起播返回 502" 502 "$ST"
check_has "502 的正文是人话（提到源站与画面）" "源站" "$(jq -r '.error // empty' "$WORK/play.json")"
# 差错文案：用户看到的必须是**人话**，而不是「等待转封装起步超时：等待第一个分片超过 8s」
check "502 带可翻译的 code" "livetv_source_timeout" "$(jq -r '.code // empty' "$WORK/play.json")"
check_has "文案里说清了「源站」与「已自动重新探测」" "自动重新探测" "$(jq -r '.error // empty' "$WORK/play.json")"
check "文案里没有实现细节（「转封装」/「分片」）" 0 \
  "$(jq -r '.error // empty' "$WORK/play.json" | grep -cE '转封装|分片' || true)"

echo
echo "== 4. 等自动重探落地（异步，最多等 40 秒） =="
T0=$(date +%s)
NEW_OK=""
for _ in $(seq 1 40); do
  NEW_OK=$(sql "select probe_ok from tv_channels where id = $CH")
  [[ "$NEW_OK" == "f" ]] && break
  sleep 1
done
WAITED=$(( $(date +%s) - T0 ))
note "等了 ${WAITED}s 后 probe_ok=${NEW_OK}"
check "频道被标成失效（probe_ok=false）" "f" "$NEW_OK"
PROBE_AT_1=$(sql "select probe_at from tv_channels where id = $CH")
NEW_SUMMARY=$(json "$BASE/api/v1/livetv/channels/$CH" | jq -r '.probe')
note "新的探测摘要：$NEW_SUMMARY"
check_ne "摘要被刷新（不再是原来那条 H.264）" "$PROBE_SUMMARY" "$NEW_SUMMARY"

echo
echo "== 5. 前台 / 管理口径的分工 =="
check "前台口径（enabled+hide_failed）不再显示它" 0 \
  "$(json "$BASE/api/v1/livetv/channels?enabled=1&hide_failed=1&group=$GROUP" | jq -r '.channels | length')"
check "probe=ok 口径也不再显示它" 0 \
  "$(json "$BASE/api/v1/livetv/channels?probe=ok&group=$GROUP" | jq -r '.channels | length')"
check "管理口径（不带过滤）仍然看得到它（能去修 / 能删源）" 1 \
  "$(json "$BASE/api/v1/livetv/channels?group=$GROUP" | jq -r '.channels | length')"

echo
echo "== 6. 节流：同一频道短时间内不再重复重探 =="
ST=$(play_code "$CH")
check "再起播一次仍然 502" 502 "$ST"
sleep 2
PROBE_AT_2=$(sql "select probe_at from tv_channels where id = $CH")
check "probe_at 没变（节流窗口内不重复探测）" "$PROBE_AT_1" "$PROBE_AT_2"

echo
echo "================ 结果：$PASS_N 通过 / $FAIL_N 失败 ================"
[[ "$FAIL_N" -eq 0 ]]
