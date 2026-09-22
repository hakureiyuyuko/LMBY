#!/usr/bin/env bash
# 直播电视（M5）S4 验收：订阅源刷新（定时/手动） + 频道探测（失效源标记）。
#
# 用法：
#   LMBY_USER=devtest LMBY_PASS=xxx bash scripts/dev/verify-livetv-sync.sh
#
# 环境变量：
#   BASE          服务地址（默认 http://127.0.0.1:8099）
#   PORT          自造源用的本地 HTTP 端口（默认 18099）
#   SKIP_TICKER=1 跳过「调度器」那一段（它需要临时改配置并重启服务）
#
# 为什么要**自造**源：真实 IPTV 源的可用性随网络与时刻变化，不能拿它当断言的依据。
# 所以这里在本机起一个静态 HTTP 服务，摆三个频道：
#   - 一个真能读的视频（http://127.0.0.1:PORT/ok.mp4）        → 应判「通」
#   - 一个连接被拒的地址（rtsp://127.0.0.1:9/...）            → 应判「不通：连接被拒绝」
#   - 一个不应答的地址（rtsp://192.0.2.1:554/...，TEST-NET）  → 应判「不通：超时」
# 三个都在同一个分组里，于是「按分组探测」这条路也一并验了。
#
# 真实源的全量探测不属于本脚本的断言（跑 `lmby livetv probe` 即可），
# 因为「这台源今天通不通」不是代码的正确性。
#
# 副作用与清理：临时源、临时频道、临时配置文件都在 EXIT 陷阱里恢复。
set -uo pipefail

BASE=${BASE:-http://127.0.0.1:8099}
PORT=${PORT:-18099}
USER=${LMBY_USER:-}
PASS=${LMBY_PASS:-}
SKIP_TICKER=${SKIP_TICKER:-0}
GROUP="验收探测"
SRC_PASTE="验收-直播探测-$$"
SRC_URL="验收-直播刷新-$$"
LMBY_BIN=${LMBY_BIN:-/usr/local/bin/lmby}
CFG=${LMBY_CONFIG:-/etc/lmby/config.toml}
PG_PASS_FILE=${PG_PASS_FILE:-/etc/lmby/pg-password}

PASS_N=0
FAIL_N=0
check() { # check 名称 期望 实际
  local name="$1" want="$2" got="$3"
  if [[ "$want" == "$got" ]]; then
    printf '  ok   %s\n' "$name"; PASS_N=$((PASS_N + 1))
  else
    printf '  FAIL %s（期望 %q，实际 %q）\n' "$name" "$want" "$got"; FAIL_N=$((FAIL_N + 1))
  fi
}
checkge() { # checkge 名称 下限 实际（数值 >=）
  local name="$1" min="$2" got="$3"
  if [[ "$got" =~ ^[0-9]+$ ]] && ((got >= min)); then
    printf '  ok   %s（%s >= %s）\n' "$name" "$got" "$min"; PASS_N=$((PASS_N + 1))
  else
    printf '  FAIL %s（期望 >= %s，实际 %q）\n' "$name" "$min" "$got"; FAIL_N=$((FAIL_N + 1))
  fi
}
check_has() { # check_has 名称 期望包含的子串 实际
  local name="$1" want="$2" got="$3"
  if [[ "$got" == *"$want"* ]]; then
    printf '  ok   %s\n' "$name"; PASS_N=$((PASS_N + 1))
  else
    printf '  FAIL %s（期望包含 %q，实际 %q）\n' "$name" "$want" "$got"; FAIL_N=$((FAIL_N + 1))
  fi
}
note() { printf '  --   %s\n' "$1"; }

if [[ -z "$USER" || -z "$PASS" ]]; then
  echo "用法：LMBY_USER=... LMBY_PASS=... bash $0" >&2
  exit 2
fi

sql() { # sql <SQL>：查库（本脚本要用它做「到点判定」与现场还原）
  local pw="${PGPASSWORD:-$(cat "$PG_PASS_FILE" 2>/dev/null)}"
  PGPASSWORD="$pw" psql -h 127.0.0.1 -U lmby -d lmby -tAc "$1" 2>/dev/null
}

WORK=$(mktemp -d)
JAR=$(mktemp)
WWW="$WORK/www"
mkdir -p "$WWW"
SRV_PID=""
SRC_PASTE_ID=""
SRC_URL_ID=""
CFG_BAK=""
CFG_DIRTY=0

wait_health() { # wait_health <秒>
  local i
  for i in $(seq 1 "$1"); do
    if [[ "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/healthz")" == "200" ]]; then
      return 0
    fi
    sleep 1
  done
  return 1
}

cleanup() {
  [[ -n "$SRV_PID" ]] && kill "$SRV_PID" 2>/dev/null
  if [[ -n "$JAR" ]]; then
    [[ -n "$SRC_PASTE_ID" ]] && curl -s -o /dev/null -b "$JAR" -X DELETE "$BASE/api/v1/livetv/sources/$SRC_PASTE_ID"
    [[ -n "$SRC_URL_ID" ]] && curl -s -o /dev/null -b "$JAR" -X DELETE "$BASE/api/v1/livetv/sources/$SRC_URL_ID"
  fi
  # 自造的频道直接删掉（没有「删频道」接口：频道是导入出来的，界面不该逐条删）
  sql "delete from tv_channels where url like 'http://127.0.0.1:$PORT/%' or url like 'rtsp://127.0.0.1:9/%' or url like 'rtsp://192.0.2.1/%'" >/dev/null
  if [[ "$CFG_DIRTY" == "1" && -n "$CFG_BAK" ]]; then
    cp -a "$CFG_BAK" "$CFG" && systemctl restart lmby && wait_health 30
  fi
  rm -rf "$WORK" "$JAR" "$CFG_BAK"
}
trap cleanup EXIT

echo "== 0. 前置检查 =="
code=$(curl -s -o /dev/null -w '%{http_code}' -c "$JAR" -X POST "$BASE/api/v1/auth/login" \
  -H 'Content-Type: application/json' --data-binary "$(jq -nc --arg u "$USER" --arg p "$PASS" '{username:$u,password:$p}')")
check "登录" 200 "$code"
[[ "$code" == "200" ]] || exit 1

check "能连上数据库（本脚本要用它验到点判定与还原现场）" "1" "$(sql 'select 1')"
if [[ "$(sql 'select 1')" != "1" ]]; then
  echo "  数据库不可达：设 PGPASSWORD 或确认 $PG_PASS_FILE 可读" >&2
  exit 1
fi
check "lmby 可执行文件在" true "$([[ -x $LMBY_BIN ]] && echo true || echo false)"

# 清掉上次遗留的源（脚本被 SIGPIPE 打断时 EXIT 陷阱不一定会跑）
for old in $(curl -s -b "$JAR" "$BASE/api/v1/livetv/sources" \
  | jq -r '.sources[] | select(.name | startswith("验收-直播探测-") or startswith("验收-直播刷新-")) | .id'); do
  curl -s -o /dev/null -b "$JAR" -X DELETE "$BASE/api/v1/livetv/sources/$old"
done
sql "delete from tv_channels where url like 'http://127.0.0.1:$PORT/%' or url like 'rtsp://127.0.0.1:9/%' or url like 'rtsp://192.0.2.1/%'" >/dev/null

echo
echo "== 1. 造源：本机静态 HTTP + 三个频道（1 通 2 不通） =="
ffmpeg -hide_banner -loglevel error -y -f lavfi -i testsrc=size=320x240:rate=10 -t 2 \
  -c:v libx264 -pix_fmt yuv420p -movflags +faststart "$WWW/ok.mp4" \
  || { echo "  生成测试视频失败（缺 ffmpeg/libx264？）" >&2; exit 1; }
cat > "$WWW/list.m3u" <<EOF
#EXTM3U
#EXTINF:-1 group-title="$GROUP",验收-探测-通
http://127.0.0.1:$PORT/ok.mp4
#EXTINF:-1 group-title="$GROUP",验收-探测-拒绝
rtsp://127.0.0.1:9/nothing
#EXTINF:-1 group-title="$GROUP",验收-探测-超时
rtsp://192.0.2.1:554/nothing
EOF
( cd "$WORK" && exec python3 -m http.server "$PORT" --bind 127.0.0.1 --directory "$WWW" ) >"$WORK/http.log" 2>&1 &
SRV_PID=$!
sleep 1
check "自造源可访问（HTTP 200）" 200 "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/list.m3u")"

CREATED=$(curl -s -b "$JAR" -X POST "$BASE/api/v1/livetv/sources" -H 'Content-Type: application/json' \
  --data-binary "$(jq -nc --arg name "$SRC_PASTE" --rawfile content "$WWW/list.m3u" \
    '{name:$name,kind:"paste",content:$content}')")
SRC_PASTE_ID=$(jq -r '.source.id // empty' <<<"$CREATED")
check "导入三条频道" 3 "$(jq -r '.import.total' <<<"$CREATED")"

LIST=$(curl -s -b "$JAR" --get --data-urlencode "group=$GROUP" "$BASE/api/v1/livetv/channels")
check "按分组能筛出这三条" 3 "$(jq -r '.channels | length' <<<"$LIST")"
ID_OK=$(jq -r '.channels[] | select(.name=="验收-探测-通") | .id' <<<"$LIST")
ID_REF=$(jq -r '.channels[] | select(.name=="验收-探测-拒绝") | .id' <<<"$LIST")
ID_TO=$(jq -r '.channels[] | select(.name=="验收-探测-超时") | .id' <<<"$LIST")
check "三条都有 id" true "$([[ -n $ID_OK && -n $ID_REF && -n $ID_TO ]] && echo true || echo false)"
check "导入后还没探过（probe 为空、probeOk 缺省）" "" \
  "$(jq -r '.channels[] | select(.name=="验收-探测-通") | .probe // ""' <<<"$LIST")"
PENDING=$(curl -s -b "$JAR" "$BASE/api/v1/livetv/channels?probe=pending")
check "「还没探过」筛出这三条" true \
  "$(jq -r --argjson a "$ID_OK" --argjson b "$ID_REF" '[.channels[].id] | (contains([$a]) and contains([$b]))' <<<"$PENDING")"

echo
echo "== 2. 起探测（只探这一个分组） =="
STAT_BEFORE=$(curl -s -b "$JAR" "$BASE/api/v1/livetv/channels/probe" | jq -r '.stats.pending')
START=$(curl -s -b "$JAR" -o /dev/null -w '%{http_code}' -X POST "$BASE/api/v1/livetv/channels/probe" \
  -H 'Content-Type: application/json' --data-binary "$(jq -nc --arg g "$GROUP" '{group:$g}')")
check "起探测返回 202（后台跑，不同步等）" 202 "$START"
DUP=$(curl -s -b "$JAR" -o /dev/null -w '%{http_code}' -X POST "$BASE/api/v1/livetv/channels/probe" \
  -H 'Content-Type: application/json' --data-binary "$(jq -nc --arg g "$GROUP" '{group:$g}')")
check "重复起探测被拒（409）" 409 "$DUP"
check "已登录用户能看进度" true \
  "$(curl -s -b "$JAR" "$BASE/api/v1/livetv/channels/probe" | jq -r '.running')"

RUNNING=true
for _ in $(seq 1 40); do
  ST=$(curl -s -b "$JAR" "$BASE/api/v1/livetv/channels/probe")
  RUNNING=$(jq -r '.running' <<<"$ST")
  [[ "$RUNNING" == "false" ]] && break
  sleep 2
done
check "探测跑完了" false "$RUNNING"
check "这次探了 3 条" 3 "$(jq -r '.progress.total' <<<"$ST")"
check "完成数 = 3" 3 "$(jq -r '.progress.done' <<<"$ST")"
check "判定：1 通" 1 "$(jq -r '.progress.ok' <<<"$ST")"
check "判定：2 不通" 2 "$(jq -r '.progress.failed' <<<"$ST")"

echo
echo "== 3. 判定结果写回库（失效源标记） =="
OKCH=$(curl -s -b "$JAR" "$BASE/api/v1/livetv/channels/$ID_OK")
check "能通的那条 probeOk=true" true "$(jq -r '.probeOk' <<<"$OKCH")"
check_has "摘要里有真实流信息（H.264）" "H.264" "$(jq -r '.probe' <<<"$OKCH")"
check "摘要里标了无音轨" true "$(jq -r '.probe | test("无音轨")' <<<"$OKCH")"
REFCH=$(curl -s -b "$JAR" "$BASE/api/v1/livetv/channels/$ID_REF")
check "连接被拒的那条 probeOk=false" false "$(jq -r '.probeOk' <<<"$REFCH")"
check_has "摘要说明是连接被拒绝" "连接被拒绝" "$(jq -r '.probe' <<<"$REFCH")"
TOCH=$(curl -s -b "$JAR" "$BASE/api/v1/livetv/channels/$ID_TO")
check "不应答的那条 probeOk=false" false "$(jq -r '.probeOk' <<<"$TOCH")"
check_has "摘要说明是超时" "超时" "$(jq -r '.probe' <<<"$TOCH")"
AT_OK=$(jq -r '.probeAt // ""' <<<"$OKCH")
AT_REF=$(jq -r '.probeAt // ""' <<<"$REFCH")
AT_TO=$(jq -r '.probeAt // ""' <<<"$TOCH")
check "三条都记了 probeAt" true "$([[ -n $AT_OK && -n $AT_REF && -n $AT_TO ]] && echo true || echo false)"

echo
echo "== 4. 按探测结果筛（界面上「只看失效」的入口） =="
FAILED=$(curl -s -b "$JAR" "$BASE/api/v1/livetv/channels?probe=failed")
check "failed 里含两条不通的" true \
  "$(jq -r --argjson a "$ID_REF" --argjson b "$ID_TO" '[.channels[].id] | (contains([$a]) and contains([$b]))' <<<"$FAILED")"
check "failed 里不含能通的那条" false \
  "$(jq -r --argjson a "$ID_OK" '[.channels[].id] | contains([$a])' <<<"$FAILED")"
OKONLY=$(curl -s -b "$JAR" "$BASE/api/v1/livetv/channels?probe=ok")
check "ok 里含能通的那条" true \
  "$(jq -r --argjson a "$ID_OK" '[.channels[].id] | contains([$a])' <<<"$OKONLY")"
check "ok 里不含不通的" false \
  "$(jq -r --argjson a "$ID_REF" '[.channels[].id] | contains([$a])' <<<"$OKONLY")"
STILL=$(curl -s -b "$JAR" "$BASE/api/v1/livetv/channels?probe=pending")
check "pending 里已经没有这三条了" false \
  "$(jq -r --argjson a "$ID_OK" '[.channels[].id] | contains([$a])' <<<"$STILL")"

echo
echo "== 5. 总账（库里现算） =="
STAT=$(curl -s -b "$JAR" "$BASE/api/v1/livetv/channels/probe")
checkge "stats.ok >= 1" 1 "$(jq -r '.stats.ok' <<<"$STAT")"
checkge "stats.failed >= 2" 2 "$(jq -r '.stats.failed' <<<"$STAT")"
check "未探数比探测前少了 3" "$((STAT_BEFORE - 3))" "$(jq -r '.stats.pending' <<<"$STAT")"

echo
echo "== 6. 补探语义：onlyUnknown 再来一次 =="
curl -s -o /dev/null -b "$JAR" -X POST "$BASE/api/v1/livetv/channels/probe" -H 'Content-Type: application/json' \
  --data-binary "$(jq -nc --arg g "$GROUP" '{group:$g, onlyUnknown:true}')"
for _ in $(seq 1 40); do
  ST=$(curl -s -b "$JAR" "$BASE/api/v1/livetv/channels/probe")
  [[ "$(jq -r '.running' <<<"$ST")" == "false" ]] && break
  sleep 1
done
check "都探过了，这次没什么可探（total=0）" 0 "$(jq -r '.progress.total' <<<"$ST")"

echo
echo "== 7. 守护：ffprobe 不可用时一条结果都不写 =="
BEFORE_AT=$(sql "select count(*) from tv_channels where url like 'http://127.0.0.1:$PORT/%' and probe_at is not null")
BEFORE_STAT=$(curl -s -b "$JAR" "$BASE/api/v1/livetv/channels/probe" | jq -r '.stats.failed')
OUT=$(LMBY_FFPROBE_PATH=/nonexistent/ffprobe "$LMBY_BIN" livetv probe --group "$GROUP" 2>&1)
RC=$?
check "CLI 报了「ffprobe 不可用」" true "$([[ "$OUT" == *"ffprobe 不可用"* ]] && echo true || echo false)"
check "CLI 以失败退出" true "$([[ $RC -ne 0 ]] && echo true || echo false)"
check "库里结果没被改动（probe_at 数不变）" "$BEFORE_AT" \
  "$(sql "select count(*) from tv_channels where url like 'http://127.0.0.1:$PORT/%' and probe_at is not null")"
check "统计也没变（假失效不会写进去）" "$BEFORE_STAT" \
  "$(curl -s -b "$JAR" "$BASE/api/v1/livetv/channels/probe" | jq -r '.stats.failed')"

echo
echo "== 8. 订阅源刷新（url 型源） =="
URLSRC=$(curl -s -b "$JAR" -X POST "$BASE/api/v1/livetv/sources" -H 'Content-Type: application/json' \
  --data-binary "$(jq -nc --arg name "$SRC_URL" --arg url "http://127.0.0.1:$PORT/list.m3u" \
    '{name:$name,kind:"url",url:$url}')")
SRC_URL_ID=$(jq -r '.source.id // empty' <<<"$URLSRC")
check "建 url 型源并首次导入 3 条" 3 "$(jq -r '.import.total' <<<"$URLSRC")"
REFRESH=$(curl -s -b "$JAR" -X POST "$BASE/api/v1/livetv/sources/$SRC_URL_ID/refresh")
check "接口刷新成功（200 + import）" true "$([[ -n $(jq -r '.import.total // empty' <<<"$REFRESH") ]] && echo true || echo false)"
check "刷新是幂等的（kept=3，不新增）" 3 "$(jq -r '.import.kept' <<<"$REFRESH")"

STATUS=$("$LMBY_BIN" livetv status)
check "CLI status 里能看到这个源" true \
  "$(jq -r --arg n "$SRC_URL" '[.sources[].name] | contains([$n])' <<<"$STATUS")"
check "CLI status 报 lastStatus=ok" "ok" \
  "$(jq -r --arg n "$SRC_URL" '[.sources[] | select(.name==$n) | .lastStatus] | first' <<<"$STATUS")"
check "间隔为 0 时不算「到点」" false \
  "$(jq -r --arg n "$SRC_URL" '[.sources[] | select(.name==$n) | .due] | first' <<<"$STATUS")"

curl -s -o /dev/null -b "$JAR" -X PATCH "$BASE/api/v1/livetv/sources/$SRC_URL_ID" \
  -H 'Content-Type: application/json' --data-binary '{"refreshIntervalMinutes":1}'
sql "update tv_sources set last_refresh_at = now() - interval '10 minutes' where id = $SRC_URL_ID" >/dev/null
check "倒回 10 分钟后算「到点」" true \
  "$("$LMBY_BIN" livetv status | jq -r --arg n "$SRC_URL" '[.sources[] | select(.name==$n) | .due] | first')"

OLD_AT=$(sql "select to_char(last_refresh_at,'YYYY-MM-DD HH24:MI:SS') from tv_sources where id = $SRC_URL_ID")
REFRESHED=$("$LMBY_BIN" livetv refresh | jq -r '.refreshed')
checkge "CLI refresh 只刷到点的源（>=1）" 1 "$REFRESHED"
NEW_AT=$(sql "select to_char(last_refresh_at,'YYYY-MM-DD HH24:MI:SS') from tv_sources where id = $SRC_URL_ID")
check "刷新后 last_refresh_at 变新" true "$([[ "$NEW_AT" > "$OLD_AT" ]] && echo true || echo false)"
check "刷完就不再算「到点」" false \
  "$("$LMBY_BIN" livetv status | jq -r --arg n "$SRC_URL" '[.sources[] | select(.name==$n) | .due] | first')"

echo
echo "== 9. 权限 =="
ANON=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/v1/livetv/channels/probe" \
  -H 'Content-Type: application/json' --data-binary '{}')
check "未登录起探测被拒（401）" 401 "$ANON"
ANON2=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/v1/livetv/channels/probe")
check "未登录看进度被拒（401）" 401 "$ANON2"
# 普通用户：devtest 是管理员，现造一个普通账号来验「起探测限管理员」
TMPU="syncverify$$"
TMPPASS="syncverify-pass-$$"
TMPJAR=$(mktemp)
LMBY_PASSWORD="$TMPPASS" "$LMBY_BIN" user add "$TMPU" --config "$CFG" >/dev/null 2>&1
TMPCODE=$(curl -s -o /dev/null -w '%{http_code}' -c "$TMPJAR" -X POST "$BASE/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  --data-binary "$(jq -nc --arg u "$TMPU" --arg p "$TMPPASS" '{username:$u,password:$p}')")
check "临时普通账号能登录" 200 "$TMPCODE"
if [[ "$TMPCODE" == "200" ]]; then
  check "普通用户起探测被拒（403）" 403 \
    "$(curl -s -o /dev/null -w '%{http_code}' -b "$TMPJAR" -X POST "$BASE/api/v1/livetv/channels/probe" -H 'Content-Type: application/json' --data-binary '{}')"
  check "普通用户能看进度（200）" 200 \
    "$(curl -s -o /dev/null -w '%{http_code}' -b "$TMPJAR" "$BASE/api/v1/livetv/channels/probe")"
fi
rm -f "$TMPJAR"
sql "delete from users where username = '$TMPU'" >/dev/null
check "管理员刷新自己的源是通的（200）" 200 \
  "$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -X POST "$BASE/api/v1/livetv/sources/$SRC_URL_ID/refresh")"

if [[ "$SKIP_TICKER" != "1" ]]; then
  echo
  echo "== 10. 服务里的定时刷新调度器（临时调快检查间隔并重启，验完还原） =="
  if grep -q '^\[livetv\]' "$CFG"; then
    note "$CFG 里已经有 [livetv] 段，跳过这一节（避免写出重复段）"
  else
    CFG_BAK="$(mktemp)"
    cp -a "$CFG" "$CFG_BAK"
    CFG_DIRTY=1
    printf '\n[livetv]\nrefresh_tick_seconds = 5\nprobe_timeout_seconds = 8\nprobe_concurrency = 4\n' >> "$CFG"
    systemctl restart lmby
    if wait_health 30; then
      check "重启后服务又起来了" 200 "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/healthz")"
      check_has "启动日志里有调度器" "直播源定时刷新已启用" \
        "$(journalctl -u lmby --since '-1 min' --no-pager | tail -30)"
      sql "update tv_sources set last_refresh_at = now() - interval '10 minutes' where id = $SRC_URL_ID" >/dev/null
      OLD2=$(sql "select to_char(last_refresh_at,'YYYY-MM-DD HH24:MI:SS') from tv_sources where id = $SRC_URL_ID")
      sleep 16   # 5 秒一跳，等三跳
      NEW2=$(sql "select to_char(last_refresh_at,'YYYY-MM-DD HH24:MI:SS') from tv_sources where id = $SRC_URL_ID")
      check "调度器自己把它刷新了" true "$([[ "$NEW2" > "$OLD2" ]] && echo true || echo false)"
      check_has "日志里记了这一跳" "已按计划刷新订阅源" "$(journalctl -u lmby --since '-2 min' --no-pager | tail -40)"

      # 关掉自动刷新：控制器不该再动手
      cp -a "$CFG_BAK" "$CFG"
      printf '\n[livetv]\nauto_refresh = false\nrefresh_tick_seconds = 5\n' >> "$CFG"
      systemctl restart lmby
      if wait_health 30; then
        check_has "关掉后有明确日志" "直播源自动刷新已关闭" \
          "$(journalctl -u lmby --since '-1 min' --no-pager | tail -30)"
        sql "update tv_sources set last_refresh_at = now() - interval '10 minutes' where id = $SRC_URL_ID" >/dev/null
        OLD3=$(sql "select to_char(last_refresh_at,'YYYY-MM-DD HH24:MI:SS') from tv_sources where id = $SRC_URL_ID")
        sleep 13
        NEW3=$(sql "select to_char(last_refresh_at,'YYYY-MM-DD HH24:MI:SS') from tv_sources where id = $SRC_URL_ID")
        check "auto_refresh=false 时没人刷它" "$OLD3" "$NEW3"
      else
        check "关掉后服务能起来" 200 "timeout"
      fi
    else
      check "重启后服务能起来" 200 "timeout"
    fi
  fi
else
  note "SKIP_TICKER=1，跳过调度器那一段"
fi

echo
echo "================ 结果：$PASS_N 通过 / $FAIL_N 失败 ================"
[[ "$FAIL_N" -eq 0 ]]
