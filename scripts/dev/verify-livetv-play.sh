#!/usr/bin/env bash
# 直播电视（M5）播放验收：起播速度 / 共享一路 ffmpeg / 空闲回收 / 外链 token。
#
# 用法：
#   LMBY_USER=devtest LMBY_PASS=xxx bash scripts/dev/verify-livetv-play.sh
#
# 环境变量：
#   BASE           服务地址（默认 http://127.0.0.1:8099）
#   LIVE_IDLE_MAX  允许的空闲回收上限秒数（默认 45，就是 DoD 的数字）
#   LIVE_START_MAX 起播耗时上限毫秒（默认 2000，DoD 是浏览器起播 < 2 秒）
#   LIVE_TRY_MAX   最多试几个频道来挑一个能起播的（默认 40；真实列表里死源不少，
#                  每试一个死源要等一次起播超时（8 秒），所以别指望很快）
#
# 前提：库里已经有频道（先跑 verify-livetv.sh 或从界面导入）。
# 脚本自己会收尾：停掉它起的所有播放会话（不留 ffmpeg 影响后续用例）。
set -uo pipefail

BASE=${BASE:-http://127.0.0.1:8099}
USER=${LMBY_USER:-}
PASS=${LMBY_PASS:-}
IDLE_MAX=${LIVE_IDLE_MAX:-45}
START_MAX=${LIVE_START_MAX:-2000}
TRY_MAX=${LIVE_TRY_MAX:-40}

PASS_N=0
FAIL_N=0
SIDS=""
check() { # check 名称 期望 实际
  local name="$1" want="$2" got="$3"
  if [[ "$want" == "$got" ]]; then
    printf '  ok   %s\n' "$name"; PASS_N=$((PASS_N + 1))
  else
    printf '  FAIL %s（期望 %q，实际 %q）\n' "$name" "$want" "$got"; FAIL_N=$((FAIL_N + 1))
  fi
}
checkle() { # checkle 名称 上限 实际
  local name="$1" max="$2" got="$3"
  if [[ "$got" =~ ^[0-9]+$ ]] && ((got <= max)); then
    printf '  ok   %s（%s <= %s）\n' "$name" "$got" "$max"; PASS_N=$((PASS_N + 1))
  else
    printf '  FAIL %s（期望 <= %s，实际 %q）\n' "$name" "$max" "$got"; FAIL_N=$((FAIL_N + 1))
  fi
}
checkge() { # checkge 名称 下限 实际
  local name="$1" min="$2" got="$3"
  if [[ "$got" =~ ^[0-9]+$ ]] && ((got >= min)); then
    printf '  ok   %s（%s >= %s）\n' "$name" "$got" "$min"; PASS_N=$((PASS_N + 1))
  else
    printf '  FAIL %s（期望 >= %s，实际 %q）\n' "$name" "$min" "$got"; FAIL_N=$((FAIL_N + 1))
  fi
}
note() { printf '  --   %s\n' "$1"; }

if [[ -z "$USER" || -z "$PASS" ]]; then
  echo "用法：LMBY_USER=... LMBY_PASS=... bash $0" >&2
  exit 2
fi

JAR=$(mktemp)
cleanup() {
  for sid in $SIDS; do
    curl -s -o /dev/null -b "$JAR" -X POST "$BASE/api/v1/live/$sid/stop" || true
  done
  rm -f "$JAR"
}
trap cleanup EXIT

# ffmpeg_count 数「拉这条地址的 ffmpeg 进程」个数。
#
# 两个坑都踩过：
#   1. 用 `ps | grep -F <url>` 时，**grep 自己的 cmdline 里就带这个 URL**，
#      于是永远多算一个（本脚本第一次跑就是这么假失败的）；pgrep 不会把自己算进去；
#   2. pgrep 的模式是扩展正则，而 URL 里有 `?` `+` `()` 这些元字符，必须先转义，
#      否则模式根本匹不上（`?` 会变成量词）。
ffmpeg_count() {
  local pat
  pat=$(printf '%s' "$1" | sed 's/[][\.^$*+?(){}|\\]/\\&/g')
  pgrep -fc -- "$pat" || true
}

echo "== 0. 登录 =="
code=$(curl -s -o /dev/null -w '%{http_code}' -c "$JAR" -X POST "$BASE/api/v1/auth/login" \
  -H 'Content-Type: application/json' --data-binary "$(jq -nc --arg u "$USER" --arg p "$PASS" '{username:$u,password:$p}')")
check "登录" 200 "$code"
[[ "$code" == "200" ]] || exit 1

echo
echo "== 1. 挑一个能起播的频道（真实列表里有死源，逐个试） =="
CANDS=$(curl -s -b "$JAR" "$BASE/api/v1/livetv/channels?enabled=1" \
  | jq -r --argjson n "$TRY_MAX" '[.channels[] | select(.kind == "RTSP")][0:$n] | .[] | "\(.id)\t\(.name)\t\(.url)"')
if [[ -z "$CANDS" ]]; then
  echo "库里没有启用中的 RTSP 频道，先跑 verify-livetv.sh 导入" >&2
  exit 2
fi

CH_ID=""; CH_NAME=""; CH_URL=""; DEAD=0; START=""
while IFS=$'\t' read -r id name url; do
  [[ -z "$id" ]] && continue
  resp=$(curl -s -b "$JAR" -w $'\n%{http_code}' -X POST "$BASE/api/v1/livetv/channels/$id/play")
  code=$(tail -1 <<<"$resp")
  body=$(sed '$d' <<<"$resp")
  if [[ "$code" == "200" ]]; then
    CH_ID="$id"; CH_NAME="$name"; CH_URL="$url"; START="$body"
    break
  fi
  DEAD=$((DEAD + 1))
  note "跳过死源：$name（HTTP $code）"
done <<<"$CANDS"

check "挑到可起播的频道" true "$([[ -n $CH_ID ]] && echo true || echo false)"
if [[ -z "$CH_ID" ]]; then
  echo "试了 $DEAD 个频道都起不来，后面的用例没法跑" >&2
  exit 1
fi
note "选中：$CH_NAME（id=$CH_ID，试了 $((DEAD + 1)) 个）"

# 清掉可能遗留的同频道直播流：脚本被中途打断（或被 pkill）时 EXIT 陷阱不一定跑，
# 会留下拉流的孤儿 ffmpeg，后面「只该有一路」的断言就全乱了。
# 用 M4 就有的管理员接口按 stream key 停，比按进程名 kill 干净。
stop_stream() { curl -s -o /dev/null -b "$JAR" -X POST "$BASE/api/v1/playback/streams/$1/stop"; }
# 会话键带处理方式后缀（live:ch<id>:copy / :transcode）—— 两种都要停，
# 只停无后缀的那个键是停不到任何东西的（脚本曾经因为这项把「起始 0 路」误判成失败）。
stop_stream "live:ch$CH_ID:copy"
stop_stream "live:ch$CH_ID:transcode"
sleep 2
check "同频道遗留流已清（起始应为 0 路）" 0 "$(ffmpeg_count "$CH_URL")"

echo
echo "== 2. 起播（DoD：< 2 秒） =="
SID=$(jq -r '.sid // empty' <<<"$START")
SIDS="$SIDS $SID"
check "起播返回 sid" true "$([[ -n $SID ]] && echo true || echo false)"
START_MS=$(jq -r '.startupMs' <<<"$START")
checkle "起播耗时（ms）" "$START_MAX" "$START_MS"
check "播放列表地址" "/api/v1/live/$SID/index.m3u8" "$(jq -r '.playlistUrl' <<<"$START")"
check "分片时长" 1 "$(jq -r '.segmentSeconds' <<<"$START")"

echo
echo "== 3. 播放列表与分片 =="
PL_URL="$BASE$(jq -r '.playlistUrl' <<<"$START")"
PL=$(curl -s -b "$JAR" "$PL_URL")
check "播放列表可拉取" true "$([[ "$PL" == *"#EXTM3U"* ]] && echo true || echo false)"
SEG_N=$(grep -cE '^[^#]' <<<"$PL" || true)
checkge "列表里已有分片" 1 "$SEG_N"
checkle "滚动窗口不超过 10 片" 10 "$SEG_N"
SEG=$(grep -E '^[^#]' <<<"$PL" | head -1)
SEG_CODE=$(curl -s -o /tmp/live-seg.ts -w '%{http_code}' -b "$JAR" "$BASE/api/v1/live/$SID/$SEG")
check "分片可下载" 200 "$SEG_CODE"
SEG_SIZE=$(stat -c %s /tmp/live-seg.ts 2>/dev/null || echo 0)
checkge "分片大小（字节）" 1000 "$SEG_SIZE"
SEG_TYPE=$(curl -s -o /dev/null -w '%{content_type}' -b "$JAR" "$BASE/api/v1/live/$SID/$SEG")
check "分片 Content-Type" "video/mp2t" "$SEG_TYPE"
check "分片禁缓存（no-store）" "no-store" \
  "$(curl -s -o /dev/null -D - -b "$JAR" "$BASE/api/v1/live/$SID/$SEG" | grep -i '^cache-control' | tr -d '\r' | cut -d' ' -f2)"

echo
echo "== 4. 滚动窗口在动 =="
FIRST_SEG="$SEG"
sleep 5
PL2=$(curl -s -b "$JAR" "$PL_URL")
check "播放列表随时间滚动" false "$([[ "$PL2" == "$PL" ]] && echo true || echo false)"
SEG2=$(grep -E '^[^#]' <<<"$PL2" | tail -1)
note "分片序：$FIRST_SEG → $SEG2"

echo
echo "== 5. 两个人看同一频道：只有一路 ffmpeg（DoD） =="
check "该频道只有一路 ffmpeg" 1 "$(ffmpeg_count "$CH_URL")"
START2=$(curl -s -b "$JAR" -X POST "$BASE/api/v1/livetv/channels/$CH_ID/play")
SID2=$(jq -r '.sid // empty' <<<"$START2")
SIDS="$SIDS $SID2"
check "第二个观众拿到了自己的 sid" true "$([[ -n $SID2 && $SID2 != $SID ]] && echo true || echo false)"
curl -s -o /dev/null -b "$JAR" "$BASE$(jq -r '.playlistUrl' <<<"$START2")"
SESS=$(curl -s -b "$JAR" "$BASE/api/v1/livetv/sessions")
check "会话数（两观众共一路）" 1 "$(jq -r '.count' <<<"$SESS")"
check "观众数" 2 "$(jq -r --argjson id "$CH_ID" '[.sessions[] | select(.channelId == $id)][0].viewers' <<<"$SESS")"
check "第二个人进来后 ffmpeg 仍然只有一路" 1 "$(ffmpeg_count "$CH_URL")"

echo
echo "== 6. 停一路不全灭，全停才回收 =="
curl -s -o /dev/null -b "$JAR" -X POST "$BASE/api/v1/live/$SID/stop"
SIDS="${SIDS// $SID/}"
sleep 2
check "还有一个观众时 ffmpeg 仍在" 1 "$(ffmpeg_count "$CH_URL")"
curl -s -o /dev/null -b "$JAR" -X POST "$BASE/api/v1/live/$SID2/stop"
SIDS="${SIDS// $SID2/}"
sleep 3
check "最后一个观众走了就停掉 ffmpeg" 0 "$(ffmpeg_count "$CH_URL")"

echo
echo "== 7. 没人看时自动回收（DoD：45 秒内退出） =="
START3=$(curl -s -b "$JAR" -X POST "$BASE/api/v1/livetv/channels/$CH_ID/play")
SID3=$(jq -r '.sid // empty' <<<"$START3")
SIDS="$SIDS $SID3"
curl -s -o /dev/null -b "$JAR" "$BASE/api/v1/live/$SID3/index.m3u8"
note "起播后不再拉列表，等空闲回收…"
waited=0
while ((waited < IDLE_MAX)); do
  sleep 5
  waited=$((waited + 5))
  [[ "$(ffmpeg_count "$CH_URL")" == "0" ]] && break
done
SIDS="${SIDS// $SID3/}"
checkle "空闲回收耗时（秒）" "$IDLE_MAX" "$waited"

echo
echo "== 8. 外链 token（给外部播放器） =="
SHARE=$(curl -s -b "$JAR" -X POST "$BASE/api/v1/livetv/channels/$CH_ID/share" \
  -H 'Content-Type: application/json' --data-binary '{"hours":1}')
SHARE_PATH=$(jq -r '.path // empty' <<<"$SHARE")
check "分享接口给出路径" true "$([[ -n $SHARE_PATH ]] && echo true || echo false)"
SPL=$(curl -s "$BASE$SHARE_PATH")   # 关键：**不带** cookie
check "外链无需登录可拉列表" true "$([[ "$SPL" == *"#EXTM3U"* ]] && echo true || echo false)"
TOKEN=$(sed -E 's#^/s/([^/]+)/.*#\1#' <<<"$SHARE_PATH")
SSEG=$(grep -E '^[^#]' <<<"$SPL" | head -1)
SCODE=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/s/$TOKEN/$SSEG")
check "外链分片可下载" 200 "$SCODE"
BAD_CODE=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/s/x$TOKEN/playlist.m3u")
check "改过的 token 被拒（401）" 401 "$BAD_CODE"
NOAUTH=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/s/1-9999999999-deadbeefdeadbeef/playlist.m3u")
check "伪造签名被拒（401）" 401 "$NOAUTH"

echo
echo "== 9. 权限 =="
check "未登录看会话列表被拒（401）" 401 \
  "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/v1/livetv/sessions")"
check "未登录起播被拒（401）" 401 \
  "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/v1/livetv/channels/$CH_ID/play")"

echo
echo "================ 结果：$PASS_N 通过 / $FAIL_N 失败 ================"
[[ "$FAIL_N" -eq 0 ]]
