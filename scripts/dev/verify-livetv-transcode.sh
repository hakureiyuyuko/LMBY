#!/usr/bin/env bash
# 直播「浏览器解不开源编码就转码」的真跑验收。
#
# 为什么要这么验：这条规则**只能靠真源真算**体现出来 ——
# 光看接口返回 mode=transcode 不够（可能参数拼错了 ffmpeg 起来就死），
# 光看进程在跑也不够（可能在复制而不是转码）。所以这里：
#   1. 在本机造一路**H.265/HEVC** 的 HLS 源（用 ffmpeg 生成 + python http.server 提供）；
#   2. 把它挂成一个临时直播源/频道，先跑一次探测拿到 video_codec=hevc；
#   3. 带 codecs=["h264"] 起播（模拟 Chrome）→ 期望 mode=transcode；
#      带 codecs=["h264","hevc"] 起播（模拟 Safari）→ 期望 mode=copy；
#   4. 把 LMBY 产出的分片拉下来 **ffprobe**：源是 hevc、输出必须是 **h264** —— 这才叫真转码。
# 跑完清场：停会话、删临时源与频道、杀 http.server、删临时目录。
#
# ⚠️ 要在**跑着服务的那台机器**上执行（要 psql 清场、要能起本地 http 服务）。
#
# 用法：
#   LMBY_USER=devtest LMBY_PASS=口令 bash scripts/dev/verify-livetv-transcode.sh
# 可选：BASE（默认 http://127.0.0.1:8099）、HTTP_PORT（默认 18765）、PGPASS_FILE
set -uo pipefail

BASE="${BASE:-http://127.0.0.1:8099}"
USER="${LMBY_USER:-}"
PASS="${LMBY_PASS:-}"
# 每次用不同端口：上一次跑残留的 http.server 会占着固定端口，
# 结果是新脚本连到旧进程上、拿到 404（真踩到过）
HTTP_PORT="${HTTP_PORT:-$((18000 + RANDOM % 2000))}"
PGPASS_FILE="${PGPASS_FILE:-/etc/lmby/pg-password}"
TAG="LMBY转码验证$(date +%s)"
WORK=$(mktemp -d)

if [[ -z "$USER" || -z "$PASS" ]]; then
  echo "用法：LMBY_USER=devtest LMBY_PASS=口令 bash scripts/dev/verify-livetv-transcode.sh"
  exit 2
fi

JAR=$(mktemp)
SRC_ID=""
CH_ID=""
SID_COPY=""
SID_TX=""
HTTP_PID=""

cleanup() {
  [[ -n "$SID_COPY" ]] && curl -s -o /dev/null -b "$JAR" -X POST "$BASE/api/v1/live/$SID_COPY/stop" || true
  [[ -n "$SID_TX" ]] && curl -s -o /dev/null -b "$JAR" -X POST "$BASE/api/v1/live/$SID_TX/stop" || true
  [[ -n "$SRC_ID" ]] && curl -s -o /dev/null -b "$JAR" -X DELETE "$BASE/api/v1/livetv/sources/$SRC_ID" || true
  if [[ -r "$PGPASS_FILE" ]]; then
    PGPASSWORD=$(cat "$PGPASS_FILE") psql -h 127.0.0.1 -U lmby -d lmby -tAc \
      "delete from tv_channels where group_name = '$TAG'" >/dev/null 2>&1 || true
  fi
  [[ -n "$HTTP_PID" ]] && kill "$HTTP_PID" 2>/dev/null || true
  rm -rf "$WORK" "$JAR"
}
trap cleanup EXIT

pass=0; fail=0
ok()    { printf '  \033[32mok\033[0m   %s\n' "$1"; pass=$((pass+1)); }
bad()   { printf '  \033[31mFAIL\033[0m %s\n        期望 %s\n        实际 %s\n' "$1" "$2" "$3"; fail=$((fail+1)); }
check() { if [[ "$2" == "$3" ]]; then ok "$1"; else bad "$1" "$2" "$3"; fi; }
note()  { printf '  \033[33m--\033[0m   %s\n' "$1"; }

json() { curl -s -H 'Content-Type: application/json' "$@"; }

echo "== 0. 登录 =="
check "登录成功" 200 \
  "$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -c "$JAR" -X POST "$BASE/api/v1/auth/login" \
    -H 'Content-Type: application/json' -d "$(jq -nc --arg u "$USER" --arg p "$PASS" '{username:$u,password:$p}')")"

echo
echo "== 1. 造一路 HEVC 的 HLS 源供 LMBY 拉 =="
ffmpeg -hide_banner -loglevel error -y \
  -f lavfi -i "testsrc2=size=640x360:rate=25" -t 40 \
  -c:v libx265 -preset ultrafast -pix_fmt yuv420p -tag:v hvc1 -g 25 \
  -c:a aac -b:a 64k \
  -f hls -hls_time 2 -hls_list_size 0 -hls_segment_filename "$WORK/seg%d.ts" "$WORK/source.m3u8" 2>"$WORK/gen.log"
if [[ ! -f "$WORK/source.m3u8" ]]; then
  echo "  生成 HEVC 测试源失败："; cat "$WORK/gen.log"; exit 1
fi
codec_src=$(ffprobe -v error -select_streams v:0 -show_entries stream=codec_name -of csv=p=0 "$WORK/source.m3u8" 2>/dev/null | head -1)
check "测试源真的是 HEVC（源侧）" hevc "$codec_src"
(cd "$WORK" && nohup python3 -m http.server "$HTTP_PORT" --bind 127.0.0.1 >/dev/null 2>&1 & echo $! > "$WORK/http.pid")
HTTP_PID=$(cat "$WORK/http.pid")
SRC_URL="http://127.0.0.1:$HTTP_PORT/source.m3u8"
note "源地址 $SRC_URL（本机 http.server $HTTP_PID）"
# 等它真的起来（进程起来了 ≠ 端口已 listen）
src_code=000
for _ in $(seq 1 20); do
  src_code=$(curl -s -o /dev/null -w '%{http_code}' "$SRC_URL")
  [[ "$src_code" == "200" ]] && break
  sleep 0.5
done
check "源可访问" 200 "$src_code"

echo
echo "== 2. 挂成临时直播源并探测（拿到 video_codec）=="
PLAYLIST="#EXTM3U
#EXTINF:-1 group-title=\"$TAG\",$TAG 频道
$SRC_URL"
created=$(json -b "$JAR" -X POST "$BASE/api/v1/livetv/sources" \
  -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg n "$TAG" --arg c "$PLAYLIST" '{name:$n, kind:"paste", content:$c}')")
SRC_ID=$(jq -r '.source.id // empty' <<<"$created")
check "建临时源成功" true "$([[ -n "$SRC_ID" ]] && echo true || echo false)"

# 先拿到频道 id，再**只探这一条** —— 不带参数＝全量探测 150+ 台，
# 跑好几分钟也等不到它，脚本会误判成「探测没记编码」（真踩到过）。
CH_ID=$(json -b "$JAR" "$BASE/api/v1/livetv/channels?q=$TAG" | jq -r '.channels[0].id // empty')
check "频道拿到 id" true "$([[ -n "$CH_ID" ]] && echo true || echo false)"
if [[ -z "$CH_ID" ]]; then
  echo "  临时源里没有频道，后面的检查没意义"
  echo "================ 结果：$pass 通过 / $fail 失败 ================"
  exit 1
fi
curl -s -o /dev/null -b "$JAR" -X POST "$BASE/api/v1/livetv/channels/probe" \
  -H 'Content-Type: application/json' -d "$(jq -nc --argjson id "$CH_ID" '{channelIds:[$id]}')"
codec=""
for _ in $(seq 1 30); do
  ch=$(json -b "$JAR" "$BASE/api/v1/livetv/channels?q=$TAG" | jq -c '.channels[0] // {}')
  codec=$(jq -r '.videoCodec // ""' <<<"$ch")
  probeok=$(jq -r '.probeOk // "null"' <<<"$ch")
  [[ -n "$codec" ]] && break
  sleep 2
done
note "探测结果：probeOk=$probeok videoCodec=${codec:-（空）}"
check "探测记下了源视频编码 = hevc" hevc "$codec"

echo
echo "== 3. 模拟 Chrome（只能解 h264）：期望转码 =="
res=$(json -b "$JAR" -X POST "$BASE/api/v1/livetv/channels/$CH_ID/play" \
  -H 'Content-Type: application/json' -d '{"codecs":["h264","vp9","av1"]}')
SID_TX=$(jq -r '.sid // empty' <<<"$res")
check "起播成功" true "$([[ -n "$SID_TX" ]] && echo true || echo false)"
check "接口报的 mode = transcode" transcode "$(jq -r '.mode // ""' <<<"$res")"

echo
echo "== 4. 产出的分片必须是 h264（这才是真转码）=="
out_codec=""
for _ in $(seq 1 40); do
  pl=$(curl -s -b "$JAR" "$BASE/api/v1/live/$SID_TX/index.m3u8")
  seg=$(grep -v '^#' <<<"$pl" | grep -m1 '\.' | tr -d '\r')
  if [[ -n "$seg" ]]; then
    curl -s -b "$JAR" -o "$WORK/out.ts" "$BASE/api/v1/live/$SID_TX/$seg"
    out_codec=$(ffprobe -v error -select_streams v:0 -show_entries stream=codec_name -of csv=p=0 "$WORK/out.ts" 2>/dev/null | head -1)
    [[ -n "$out_codec" ]] && break
  fi
  sleep 2
done
note "源编码 $codec_src → LMBY 输出编码 $out_codec"
check "输出编码是 h264（转码生效）" h264 "$out_codec"

echo
echo "== 5. 模拟 Safari（能解 hevc）：应当转封装、不烧 CPU =="
res2=$(json -b "$JAR" -X POST "$BASE/api/v1/livetv/channels/$CH_ID/play" \
  -H 'Content-Type: application/json' -d '{"codecs":["h264","hevc"]}')
SID_COPY=$(jq -r '.sid // empty' <<<"$res2")
check "接口报的 mode = copy" copy "$(jq -r '.mode // ""' <<<"$res2")"
out2=""
for _ in $(seq 1 40); do
  pl2=$(curl -s -b "$JAR" "$BASE/api/v1/live/$SID_COPY/index.m3u8")
  seg2=$(grep -v '^#' <<<"$pl2" | grep -m1 '\.' | tr -d '\r')
  if [[ -n "$seg2" ]]; then
    curl -s -b "$JAR" -o "$WORK/out2.ts" "$BASE/api/v1/live/$SID_COPY/$seg2"
    out2=$(ffprobe -v error -select_streams v:0 -show_entries stream=codec_name -of csv=p=0 "$WORK/out2.ts" 2>/dev/null | head -1)
    [[ -n "$out2" ]] && break
  fi
  sleep 2
done
check "转封装那一路输出仍是 hevc（没白转）" hevc "$out2"

echo
echo "== 6. force 覆盖：明确要求转码就转 =="
res3=$(json -b "$JAR" -X POST "$BASE/api/v1/livetv/channels/$CH_ID/play" \
  -H 'Content-Type: application/json' -d '{"codecs":["h264","hevc"],"force":"transcode"}')
check "force=transcode 时 mode = transcode" transcode "$(jq -r '.mode // ""' <<<"$res3")"
sid3=$(jq -r '.sid // empty' <<<"$res3")
[[ -n "$sid3" ]] && curl -s -o /dev/null -b "$JAR" -X POST "$BASE/api/v1/live/$sid3/stop"

echo
echo "================ 结果：$pass 通过 / $fail 失败 ================"
[[ "$fail" -eq 0 ]]
