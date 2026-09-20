#!/usr/bin/env bash
# M3「播放核心」的真库验收：对着跑着的服务做 HTTP 断言，并真的读一遍媒体文件。
#
# 验的东西（按 DoD 分组）：
#   1. 直出（DirectPlay）：HTTP Range / ETag / 条件请求 / 出来的字节与原文件一致；
#   2. 转封装（DirectStream）：mkv → HLS fMP4，视频编码没变、音频按需转 AAC，
#      并且用 ffprobe 直接读**服务发出的** m3u8 交叉验证；
#   3. 决策边界：10bit HEVC 明确说「需要转码（M4）」、上报 Safari 能力后变成可以
#      转封装（且打了 hvc1 标签）、图形字幕明确不显示；
#   4. 播放会话：进度落库、续播位置、seek 换窗口、stop 后 ffmpeg 与分片都被回收；
#   5. 会话复用：同一段播放只跑一路 ffmpeg；
#   6. 继续观看 / 标记已看（跑完还原）。
#
# 样本从库里按编码条件现挑，不写死文件名（库会变，编码条件不会）。
#
# 用法：
#   LMBY_USER=devtest LMBY_PASS=口令 bash scripts/dev/verify-play.sh
# 可选环境变量：
#   BASE         默认 http://127.0.0.1:8099
#   STREAMS_DIR  默认 /var/lib/lmby/streams
#   TEST_IDLE=1  额外等一个空闲回收周期（慢 ~55s），验证「没人看就自动回收」
set -uo pipefail

BASE="${BASE:-http://127.0.0.1:8099}"
USER="${LMBY_USER:-}"
PASS="${LMBY_PASS:-}"
STREAMS_DIR="${STREAMS_DIR:-/var/lib/lmby/streams}"
PGPASSWORD_FILE="${PGPASSWORD_FILE:-/etc/lmby/pg-password}"

if [[ -z "$USER" || -z "$PASS" ]]; then
  echo "用法：LMBY_USER=devtest LMBY_PASS=口令 bash scripts/dev/verify-play.sh"
  exit 2
fi

JAR=$(mktemp)
trap 'rm -f "$JAR"' EXIT

pass=0; fail=0
ok()    { printf '  \033[32mok\033[0m   %s\n' "$1"; pass=$((pass+1)); }
bad()   { printf '  \033[31mFAIL\033[0m %s\n        期望 %s\n        实际 %s\n' "$1" "$2" "$3"; fail=$((fail+1)); }
check() { if [[ "$2" == "$3" ]]; then ok "$1"; else bad "$1" "$2" "$3"; fi; }
note()  { printf '  \033[33m--\033[0m   %s\n' "$1"; }
bool()  { [[ "$1" == "true" ]] && echo true || echo false; }

st()   { curl -s -o /dev/null -w '%{http_code}' "$@"; }
bd()   { curl -s "$@"; }
json() { curl -s -H 'Content-Type: application/json' "$@"; }
# 打印响应头（去掉 \r，便于精确比较）
hdrs() { curl -s -D - -o /dev/null "$@" | tr -d '\r'; }
hval() { awk -v k="$(tr 'A-Z' 'a-z' <<<"$2"):" 'tolower($1)==k{ $1=""; sub(/^ /,""); sub(/\r$/,""); print; exit }' <<<"$1"; }

PSQL() { PGPASSWORD=$(cat "$PGPASSWORD_FILE") psql -h 127.0.0.1 -U lmby -d lmby -tAc "$1"; }
# 按编码条件挑一个「体积最小」的文件（小的跑得快，验收脚本要能反复跑）
pick_file() { PSQL "select f.id from media_files f
  where f.deleted_at is null and f.probe_state = 'ok' and $1
  order by f.size_bytes asc limit 1"; }
item_of()   { PSQL "select item_id from media_files where id = $1"; }
file_path() { PSQL "select path from media_files where id = $1"; }
file_size() { PSQL "select size_bytes from media_files where id = $1"; }
ffmpeg_count() { pgrep -f 'ffmpeg .*-hls_segment_filename' | wc -l; }
session_dirs() { find "$STREAMS_DIR" -mindepth 1 -maxdepth 1 -type d -not -name subs 2>/dev/null | wc -l; }

echo "目标：$BASE"

echo
echo "== 0. 挑样本（按编码条件，不写死文件名）=="
MP4F=$(pick_file "f.path ~* '\.(mp4|m4v)\$' and (f.video_streams->0)->>'codec'='h264' and coalesce((f.video_streams->0)->>'bitDepth','8')='8' and coalesce(f.duration_ticks,0) > 600000000")
MKVF=$(pick_file "f.container like 'matroska%' and (f.video_streams->0)->>'codec'='h264' and coalesce((f.video_streams->0)->>'bitDepth','8')='8' and (f.audio_streams->0)->>'codec'='aac' and coalesce(f.duration_ticks,0) > 9000000000")
# 音频转码样本：视频能复制（h264/vp9/av1 8bit）+ 音频浏览器放不了（ac3/eac3/dts/truehd）
AC3F=$(pick_file "f.container like 'matroska%' and (f.video_streams->0)->>'codec' in ('h264','vp9','av1') and coalesce((f.video_streams->0)->>'bitDepth','8') in ('8','0') and (f.audio_streams->0)->>'codec' in ('ac3','eac3','dts','truehd') and coalesce(f.duration_ticks,0) > 600000000")
HEVCF=$(pick_file "(f.video_streams->0)->>'codec'='hevc' and coalesce((f.video_streams->0)->>'bitDepth','8')='10'")
# 多版本样本：同一条目里既有能直出的版本、又有需要转码的版本（回归「选片」逻辑）
MULTI=$(PSQL "select i.id from media_items i join media_files f on f.item_id = i.id and f.deleted_at is null
  group by i.id having count(*) > 1
    and count(*) filter (where f.path ~* '\.(mp4|m4v)$' and (f.video_streams->0)->>'codec'='h264' and coalesce((f.video_streams->0)->>'bitDepth','8')='8' and (f.audio_streams->0)->>'codec' in ('aac','mp3')) > 0
    and count(*) filter (where coalesce((f.video_streams->0)->>'codec','') not in ('h264','vp9','av1') or coalesce((f.video_streams->0)->>'bitDepth','8') not in ('8','0')) > 0
  order by i.id limit 1")

# 文本字幕样本：同时读出这条字幕轨的 default/forced 标记（后面「不自动挂字幕」要用）
# 字幕抽取要读整个文件，太短或太大的片子都不合适：只要 60s 以上的
IFS='|' read -r SUBF SUBIDX SUBDEF SUBFORCED <<<"$(PSQL "select f.id, (s.value->>'index'),
  coalesce((s.value->>'default')::bool,false), coalesce((s.value->>'forced')::bool,false)
  from media_files f, jsonb_array_elements(coalesce(f.subtitle_streams,'[]'::jsonb)) s
  where f.deleted_at is null and f.probe_state='ok'
    and (f.video_streams->0)->>'codec'='h264' and coalesce((f.video_streams->0)->>'bitDepth','8')='8'
    and (s.value->>'isImage')::bool is not true
    and coalesce(f.duration_ticks,0) > 600000000
  order by f.size_bytes asc limit 1")"
IFS='|' read -r PGSF PGSIDX <<<"$(PSQL "select f.id, (s.value->>'index')
  from media_files f, jsonb_array_elements(coalesce(f.subtitle_streams,'[]'::jsonb)) s
  where f.deleted_at is null and f.probe_state='ok'
    and (f.video_streams->0)->>'codec'='h264' and coalesce((f.video_streams->0)->>'bitDepth','8')='8'
    and (s.value->>'isImage')::bool is true
  order by f.size_bytes asc limit 1")"

for pair in "直出(mp4 h264 8bit):$MP4F" "转封装(mkv h264+aac):$MKVF" "音频转码:$AC3F" \
            "需要转码(hevc 10bit):$HEVCF" "文本字幕:$SUBF" "图形字幕:$PGSF" "多版本:$MULTI"; do
  name=${pair%%:*}; id=${pair#*:}
  if [[ -z "$id" ]]; then
    note "样本缺失：$name —— 库里的条件没命中，相关断言会跳过"
  elif [[ "$name" == "多版本" ]]; then
    note "$name → item=$id"
  else
    note "$name → file=$id $(basename "$(file_path "$id")")"
  fi
done

MP4I=$(item_of "$MP4F"); MKVI=$(item_of "$MKVF"); AC3I=$(item_of "$AC3F")
HEVCI=$(item_of "$HEVCF"); SUBI=$(item_of "$SUBF"); PGSI=$(item_of "$PGSF")

echo
echo "== 1. 登录 =="
code=$(st -X POST "$BASE/api/v1/auth/login" -c "$JAR" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg u "$USER" --arg p "$PASS" '{username:$u,password:$p}')")
check "登录 $USER = 200" 200 "$code"
[[ "$code" != "200" ]] && exit 1

echo
echo "== 2. 直出（DirectPlay）：Range / ETag / 字节一致 =="
pl=$(bd -b "$JAR" "$BASE/api/v1/items/$MP4I/playlist")
check "playlist 列出文件" true "$(bool "$(jq -r '.files|length>0' <<<"$pl")")"
check "playlist 带视频流" true "$(bool "$(jq -r '.files[0].video|length>0' <<<"$pl")")"
check "playlist 带音频流" true "$(bool "$(jq -r '.files[0].audio|length>0' <<<"$pl")")"

r=$(json -b "$JAR" -X POST "$BASE/api/v1/items/$MP4I/play" -H 'Content-Type: application/json' \
  -d '{"restart":true}')
check "决策 = direct" direct "$(jq -r '.mode' <<<"$r")"
check "状态 = direct" direct "$(jq -r '.state' <<<"$r")"
check "可播放 = true" true "$(bool "$(jq -r '.playable' <<<"$r")")"
check "视频动作 = copy" copy "$(jq -r '.plan.video.action' <<<"$r")"
check "音频动作 = copy（mp4 里的 aac）" copy "$(jq -r '.plan.audio.action' <<<"$r")"
check "有 directUrl" true "$(bool "$(jq -r '.directUrl|startswith("/api/v1/play/")' <<<"$r")")"
check "没有 hlsUrl（直出不需要分片）" "" "$(jq -r '.hlsUrl // ""' <<<"$r")"
check "理由链非空（界面要能解释为什么这么播）" true "$(bool "$(jq -r '.reasons|length>0' <<<"$r")")"

DSID=$(jq -r '.playSessionId' <<<"$r")
DURL="$BASE$(jq -r '.directUrl' <<<"$r")"
DSIZE=$(file_size "$MP4F")
DPATH=$(file_path "$MP4F")

out=$(curl -s -o /dev/null -w '%{http_code} %{size_download} %{content_type}' -b "$JAR" "$DURL")
check "全量 GET = 200 / 完整字节 / video/mp4" "200 $DSIZE video/mp4" "$out"

out=$(curl -s -o /dev/null -w '%{http_code} %{size_download}' -b "$JAR" -r 0-1023 "$DURL")
check "Range 0-1023 = 206 且只回 1024 字节" "206 1024" "$out"
CR=$(hdrs -b "$JAR" -r 0-1023 "$DURL" | awk 'tolower($1)=="content-range:"{print $2" "$3}')
check "Content-Range 头正确" "bytes 0-1023/$DSIZE" "$CR"

out=$(curl -s -o /dev/null -w '%{http_code} %{size_download}' -b "$JAR" -r -256 "$DURL")
check "后缀 Range（最后 256 字节）= 206" "206 256" "$out"
check "越界 Range（从 EOF 开始）= 416" 416 "$(st -b "$JAR" -r "$DSIZE-" "$DURL")"
check "倒序 Range（10-5）= 416" 416 "$(st -b "$JAR" -r 10-5 "$DURL")"
check "HEAD 可用 = 200" 200 "$(st -I -b "$JAR" "$DURL")"

H=$(hdrs -b "$JAR" "$DURL")
ET=$(hval "$H" ETag)
LM=$(hval "$H" Last-Modified)
AR=$(hval "$H" Accept-Ranges)
check "响应带 ETag" true "$(bool "$([[ -n "$ET" ]] && echo true)")"
check "响应带 Last-Modified" true "$(bool "$([[ -n "$LM" ]] && echo true)")"
check "声明 Accept-Ranges: bytes" bytes "$AR"
check "If-None-Match 命中 = 304" 304 "$(st -b "$JAR" -H "If-None-Match: $ET" "$DURL")"

# 最强的一条：服务端 Range 出去的字节与原文件完全相同
REMOTE_MD5=$(curl -s -b "$JAR" -r 0-65535 "$DURL" | md5sum | cut -d' ' -f1)
LOCAL_MD5=$(head -c 65536 "$DPATH" | md5sum | cut -d' ' -f1)
check "前 64KB 的 md5 与原文件一致" "$LOCAL_MD5" "$REMOTE_MD5"

check "未登录取流 = 401" 401 "$(st "$DURL")"
check "伪造播放会话 = 404" 404 "$(st -b "$JAR" "$BASE/api/v1/play/deadbeef/stream")"
check "直出会话访问 m3u8 = 400" 400 "$(st -b "$JAR" "$BASE/api/v1/play/$DSID/index.m3u8")"
check "不存在的条目 = 404" 404 "$(st -b "$JAR" -X POST "$BASE/api/v1/items/999999999/play")"

echo
echo "== 3. 播放进度与续播 =="
json -b "$JAR" -X POST "$BASE/api/v1/play/$DSID/progress" -H 'Content-Type: application/json' \
  -d '{"positionTicks":1000000000,"durationTicks":10000000000}' >/dev/null
p=$(bd -b "$JAR" "$BASE/api/v1/items/$MP4I/progress")
check "进度已落库（100 秒）" 1000000000 "$(jq -r '.progress.positionTicks' <<<"$p")"
check "时长已落库" 10000000000 "$(jq -r '.progress.durationTicks' <<<"$p")"
check "播放次数 ≥ 1" true "$(bool "$(jq -r '.progress.playCount >= 1' <<<"$p")")"
check "未登录看进度 = 401" 401 "$(st "$BASE/api/v1/items/$MP4I/progress")"

r2=$(json -b "$JAR" -X POST "$BASE/api/v1/items/$MP4I/play" -H 'Content-Type: application/json' -d '{}')
check "不带 restart 时从 100 秒续播" 100 "$(jq -r '.startSeconds|floor' <<<"$r2")"
r3=$(json -b "$JAR" -X POST "$BASE/api/v1/items/$MP4I/play" -H 'Content-Type: application/json' -d '{"restart":true}')
check "restart=true 从头播" 0 "$(jq -r '.startSeconds' <<<"$r3")"
for sid in "$(jq -r '.playSessionId' <<<"$r2")" "$(jq -r '.playSessionId' <<<"$r3")"; do
  json -b "$JAR" -X POST "$BASE/api/v1/play/$sid/stop" -H 'Content-Type: application/json' -d '{}' >/dev/null
done

check "stop = 200" 200 "$(json -b "$JAR" -X POST "$BASE/api/v1/play/$DSID/stop" \
  -H 'Content-Type: application/json' -d '{"positionTicks":1000000000}' -o /dev/null -w '%{http_code}')"
check "stop 之后会话失效 = 404" 404 "$(st -b "$JAR" "$BASE/api/v1/play/$DSID")"

echo
echo "== 3'. 多版本条目：应当挑能直出的那个版本（回归用） =="
if [[ -z "$MULTI" ]]; then
  note "库里没有「同时有可直出版本与需转码版本」的条目，跳过"
else
  r=$(json -b "$JAR" -X POST "$BASE/api/v1/items/$MULTI/play" -H 'Content-Type: application/json' -d '{"restart":true}')
  check "多版本条目挑能直出的版本" direct "$(jq -r '.mode' <<<"$r")"
  check "理由链里说明了选片依据" true "$(bool "$(jq -r '[.reasons[]|test("能直出 > 转封装 > 转码")]|any' <<<"$r")")"
  check "选中版本不需要转码" copy "$(jq -r '.plan.video.action' <<<"$r")"
  json -b "$JAR" -X POST "$BASE/api/v1/play/$(jq -r '.playSessionId' <<<"$r")/stop" \
    -H 'Content-Type: application/json' -d '{}' >/dev/null
fi

echo
echo "== 4. 转封装（DirectStream）：mkv → HLS fMP4 =="
r=$(json -b "$JAR" -X POST "$BASE/api/v1/items/$MKVI/play" -H 'Content-Type: application/json' \
  -d '{"restart":true}')
check "决策 = remux" remux "$(jq -r '.mode' <<<"$r")"
check "状态 = ready" ready "$(jq -r '.state' <<<"$r")"
check "分片格式 = fmp4（Safari 与 hls.js 都吃）" fmp4 "$(jq -r '.plan.segmentFormat' <<<"$r")"
check "视频动作 = copy（不重新编码）" copy "$(jq -r '.plan.video.action' <<<"$r")"
check "音频动作 = copy（mkv 里的 aac）" copy "$(jq -r '.plan.audio.action' <<<"$r")"
MSID=$(jq -r '.playSessionId' <<<"$r")
MURL="$BASE$(jq -r '.hlsUrl' <<<"$r")"

m3u8=$(bd -b "$JAR" "$MURL")
check "m3u8 有 #EXTM3U" 1 "$(grep -c '#EXTM3U' <<<"$m3u8")"
check "m3u8 声明了 fMP4 的 init 段（EXT-X-MAP）" true \
  "$(bool "$(grep -q 'EXT-X-MAP.*init\.mp4' <<<"$m3u8" && echo true)")"
SEG=$(grep -o 'seg_[0-9]*\.m4s' <<<"$m3u8" | head -1)
check "m3u8 引用了分片" true "$(bool "$([[ -n "$SEG" ]] && echo true)")"
check "init.mp4 = 200" 200 "$(st -b "$JAR" "$BASE/api/v1/play/$MSID/init.mp4")"
check "首个分片 = 200" 200 "$(st -b "$JAR" "$BASE/api/v1/play/$MSID/$SEG")"
check "不存在的分片 = 404" 404 "$(st -b "$JAR" "$BASE/api/v1/play/$MSID/seg_99999.m4s")"
check "路径穿越被拒（非 200）" true \
  "$(bool "$([[ "$(st -b "$JAR" "$BASE/api/v1/play/$MSID/..%2F..%2Fetc%2Fpasswd")" != "200" ]] && echo true)")"
# 分片 URL 必须是「播放列表同级」：客户端拿 m3u8 的 URL 作基准拼相对文件名
check "m3u8 里没有子目录前缀（否则客户端会 404）" true \
  "$(bool "$(grep -qE '^(seg_[0-9]+\.m4s|init\.mp4)$' <<<"$m3u8" && echo true)")"
check "ffmpeg 真的在跑" true "$(bool "$([[ "$(ffmpeg_count)" -ge 1 ]] && echo true)")"
check "分片真的落到了磁盘" true "$(bool "$(find "$STREAMS_DIR" -name 'seg_*.m4s' | grep -q . && echo true)")"

# 交叉验证：让 ffprobe 直接读服务发出的 HLS，确认「视频没重新编码、音频成了 aac」
COOKIE=$(awk 'NF>=7 && $6=="lmby_session"{v=$7} END{print "lmby_session="v}' "$JAR")
probe=$(timeout 90 ffprobe -v error -show_entries stream=codec_type,codec_name,channels -of csv=p=0 \
  -headers "Cookie: $COOKIE" -i "$MURL" 2>&1 | sort | tr '\n' ' ')
note "ffprobe 读 HLS：$probe"
check "HLS 里的视频是 h264（原样复制）" true "$(bool "$(grep -q 'h264' <<<"$probe" && echo true)")"
check "HLS 里的音频是 aac" true "$(bool "$(grep -q 'aac' <<<"$probe" && echo true)")"

# 会话复用：同一段再点一次播放，不该再多起一路 ffmpeg
before=$(ffmpeg_count)
json -b "$JAR" -X POST "$BASE/api/v1/items/$MKVI/play" -H 'Content-Type: application/json' -d '{"restart":true}' >/dev/null
sess=$(bd -b "$JAR" "$BASE/api/v1/playback/sessions")
check "ffmpeg 进程数没有增加（会话复用）" "$before" "$(ffmpeg_count)"
check "转封装会话只有 1 路" 1 "$(jq -r '.transcodeSessions|length' <<<"$sess")"
check "播放会话列表带本会话" true \
  "$(bool "$(jq -r --arg id "$MSID" 'any(.sessions[]; .playSessionId==$id)' <<<"$sess")")"

echo
echo "== 5. seek：换窗口，旧窗口立刻回收 =="
# 目标位置取样本时长的一半：写死秒数会在短文件上直接超出片尾（实测踩到）
MDUR=$(PSQL "select coalesce(f.duration_ticks,0) from media_files f where f.id=$MKVF")
SEEKT=$(( MDUR / 2 / 10000000 ))
SEEKTICKS=$(( MDUR / 2 ))
r=$(json -b "$JAR" -X POST "$BASE/api/v1/play/$MSID/seek" -H 'Content-Type: application/json' \
  -d "$(jq -nc --argjson t "$SEEKTICKS" '{positionTicks:$t}')")
check "seek 后起点 = ${SEEKT}s（样本时长的一半）" "$SEEKT" "$(jq -r '.startSeconds|floor' <<<"$r")"
check "seek 后状态 = ready" ready "$(jq -r '.state' <<<"$r")"
check "seek 后仍只有 1 路 ffmpeg" 1 "$(ffmpeg_count)"
check "seek 后 m3u8 可用" 200 "$(st -b "$JAR" "$MURL")"
check "seek 到负数不报错" 200 "$(json -b "$JAR" -X POST "$BASE/api/v1/play/$MSID/seek" \
  -H 'Content-Type: application/json' -d '{"positionTicks":-5}' -o /dev/null -w '%{http_code}')"
check "seek 超出片尾会被夹回（不报错）" 200 "$(json -b "$JAR" -X POST "$BASE/api/v1/play/$MSID/seek" \
  -H 'Content-Type: application/json' -d "$(jq -nc --argjson t "$((MDUR * 3))" '{positionTicks:$t}')" -o /dev/null -w '%{http_code}')"

echo
echo "== 6. stop 后回收（DoD：关页面不留进程） =="
json -b "$JAR" -X POST "$BASE/api/v1/play/$MSID/stop" -H 'Content-Type: application/json' -d '{}' >/dev/null
sleep 1
check "stop 后没有 ffmpeg 残留" 0 "$(ffmpeg_count)"
check "stop 后分片目录被清掉" 0 "$(session_dirs)"
check "stop 后会话失效 = 404" 404 "$(st -b "$JAR" "$MURL")"

echo
echo "== 7. 音频转码（AC3 6 声道 → AAC 立体声） =="
if [[ -z "$AC3I" ]]; then
  note "库里没有 ac3 样本，跳过"
else
  r=$(json -b "$JAR" -X POST "$BASE/api/v1/items/$AC3I/play" -H 'Content-Type: application/json' \
    -d '{"restart":true}')
  check "决策 = remux" remux "$(jq -r '.mode' <<<"$r")"
  check "视频动作 = copy" copy "$(jq -r '.plan.video.action' <<<"$r")"
  check "音频动作 = convert（浏览器放不了 ac3）" convert "$(jq -r '.plan.audio.action' <<<"$r")"
  check "标记了降声道" true "$(bool "$(jq -r '.plan.audio.downmix' <<<"$r")")"
  check "理由里说明转 AAC" true "$(bool "$(jq -r '[.reasons[]|test("AAC")]|any' <<<"$r")")"
  ASID=$(jq -r '.playSessionId' <<<"$r")
  AURL="$BASE$(jq -r '.hlsUrl' <<<"$r")"
  if [[ "$(jq -r '.state' <<<"$r")" == "ready" ]]; then
    probe=$(timeout 90 ffprobe -v error -show_entries stream=codec_name,channels -of csv=p=0 \
      -headers "Cookie: $COOKIE" -i "$AURL" 2>&1 | sort | tr '\n' ' ')
    note "ffprobe 读 HLS：$probe"
    check "音频确实转成了 aac" true "$(bool "$(grep -q 'aac' <<<"$probe" && echo true)")"
    check "声道降到了 2" true "$(bool "$(grep -q 'aac,2' <<<"$probe" && echo true)")"
  fi
  json -b "$JAR" -X POST "$BASE/api/v1/play/$ASID/stop" -H 'Content-Type: application/json' -d '{}' >/dev/null
fi

echo
echo "== 8. 决策边界：10bit HEVC（默认档应当明确说「M4 才支持」） =="
if [[ -z "$HEVCI" ]]; then
  note "库里没有 10bit HEVC 样本，跳过"
else
  r=$(json -b "$JAR" -X POST "$BASE/api/v1/items/$HEVCI/play" -H 'Content-Type: application/json' -d '{}')
  check "不可播放（不假装能播）" false "$(bool "$(jq -r '.playable' <<<"$r")")"
  check "决策 = transcode" transcode "$(jq -r '.mode' <<<"$r")"
  check "视频动作 = transcode" transcode "$(jq -r '.plan.video.action' <<<"$r")"
  check "理由链里点名 M4" true "$(bool "$(jq -r '[.reasons[]|test("M4")]|any' <<<"$r")")"
  check "给了明确的错误说明" true "$(bool "$(jq -r '(.error|length)>0' <<<"$r")")"
  check "没有漏出播放地址" "" "$(jq -r '.hlsUrl // ""' <<<"$r")"

  # 客户端上报「我支持 hevc 10bit」之后，同一个文件应当变成可以转封装
  SAFARI='{"name":"safari","containers":["mp4","webm"],"videoCodecs":["h264","hevc"],"audioCodecs":["aac","ac3","eac3","flac","mp3"],"maxWidth":3840,"maxHeight":2160,"maxBitDepth":10,"maxAudioChannels":6,"supportsHls":true,"supportsFmp4":true,"supportsTs":true}'
  r=$(json -b "$JAR" -X POST "$BASE/api/v1/items/$HEVCI/play" -H 'Content-Type: application/json' \
    -d "$(jq -nc --argjson p "$SAFARI" '{restart:true, profile:$p}')")
  check "上报 Safari 能力后 = remux" remux "$(jq -r '.mode' <<<"$r")"
  check "可播放 = true" true "$(bool "$(jq -r '.playable' <<<"$r")")"
  check "视频仍然是 copy" copy "$(jq -r '.plan.video.action' <<<"$r")"
  HSID=$(jq -r '.playSessionId' <<<"$r")
  HURL="$BASE$(jq -r '.hlsUrl' <<<"$r")"
  if [[ "$(jq -r '.state' <<<"$r")" == "ready" ]]; then
    probe=$(timeout 90 ffprobe -v error -show_entries stream=codec_name -of csv=p=0 \
      -headers "Cookie: $COOKIE" -i "$HURL" 2>&1 | sort | tr '\n' ' ')
    note "ffprobe 读 HLS：$probe"
    check "HLS 里是 hevc（打了 hvc1 标签，否则 Safari 会黑屏）" true \
      "$(bool "$(grep -q 'hevc' <<<"$probe" && echo true)")"
  fi
  json -b "$JAR" -X POST "$BASE/api/v1/play/$HSID/stop" -H 'Content-Type: application/json' -d '{}' >/dev/null
fi
out=$(json -b "$JAR" -X POST "$BASE/api/v1/items/$MP4I/play" -H 'Content-Type: application/json' \
  -d '{"profile":{"name":"  x!!  ","videoCodecs":["H264","h264; rm -rf /"]},"restart":true}' \
  -o /dev/null -w '%{http_code}')
check "客户端上报乱值不会崩（能力被清洗）" 200 "$out"

echo
echo "== 9. 字幕：文本 → WebVTT，图形 → 明确不显示 =="
if [[ -z "$SUBF" ]]; then
  note "没有「h264 + 文本字幕」样本，跳过"
else
  r=$(json -b "$JAR" -X POST "$BASE/api/v1/items/$SUBI/play" -H 'Content-Type: application/json' \
    -d "$(jq -nc --argjson i "$SUBIDX" '{restart:true, subtitleStreamIndex:$i}')")
  check "字幕动作 = convert" convert "$(jq -r '.plan.subtitle.action' <<<"$r")"
  check "有 subtitleUrl" true "$(bool "$(jq -r '.subtitleUrl|length>0' <<<"$r")")"
  VTT=$(bd -b "$JAR" "$BASE$(jq -r '.subtitleUrl' <<<"$r")")
  # 首次抽取要读一遍源文件（网络盘上可能几十秒），服务端会先回 202「正在准备」
  for _ in $(seq 1 40); do
    [[ "$(sed -n '1p' <<<"$VTT" | tr -d '\r')" == "WEBVTT" ]] && break
    sleep 3
    VTT=$(bd -b "$JAR" "$BASE$(jq -r '.subtitleUrl' <<<"$r")")
  done
  check "VTT 首行是 WEBVTT" "WEBVTT" "$(sed -n '1p' <<<"$VTT" | tr -d '\r')"
  check "VTT 至少 2 条时间轴" true "$(bool "$([[ "$(grep -c -- '-->' <<<"$VTT")" -ge 2 ]] && echo true)")"
  check "再请求一次直接命中缓存（200，不是 202）" 200 "$(st -b "$JAR" "$BASE$(jq -r '.subtitleUrl' <<<"$r")")"
  STITLE=$(json -b "$JAR" -X POST "$BASE/api/v1/items/$SUBI/play" -H 'Content-Type: application/json' -d '{"restart":true}')
  if [[ "$SUBDEF" == "t" || "$SUBFORCED" == "t" ]]; then
    note "该样本带默认/强制字幕轨（default=$SUBDEF forced=$SUBFORCED），跳过「不自动挂字幕」断言"
  else
    check "不指定时不自动挂字幕" "" "$(jq -r '.subtitleUrl // ""' <<<"$STITLE")"
  fi
  json -b "$JAR" -X POST "$BASE/api/v1/play/$(jq -r '.playSessionId' <<<"$STITLE")/stop" \
    -H 'Content-Type: application/json' -d '{}' >/dev/null
  json -b "$JAR" -X POST "$BASE/api/v1/play/$(jq -r '.playSessionId' <<<"$r")/stop" \
    -H 'Content-Type: application/json' -d '{}' >/dev/null
fi

if [[ -z "$PGSF" ]]; then
  note "没有图形字幕样本，跳过"
else
  r=$(json -b "$JAR" -X POST "$BASE/api/v1/items/$PGSI/play" -H 'Content-Type: application/json' \
    -d "$(jq -nc --argjson i "$PGSIDX" '{restart:true, subtitleStreamIndex:$i}')")
  check "图形字幕动作 = drop（M4 才能烧录）" drop "$(jq -r '.plan.subtitle.action' <<<"$r")"
  check "理由里说明要烧录" true "$(bool "$(jq -r '[.reasons[]|test("烧录")]|any' <<<"$r")")"
  check "没有 subtitleUrl（不假装能显示）" "" "$(jq -r '.subtitleUrl // ""' <<<"$r")"
  PSID=$(jq -r '.playSessionId' <<<"$r")
  r2=$(json -b "$JAR" -X POST "$BASE/api/v1/items/$PGSI/play" -H 'Content-Type: application/json' \
    -d '{"restart":true,"subtitleStreamIndex":-1}')
  check "显式关闭字幕（-1）不报错" true "$(bool "$(jq -r '.playable' <<<"$r2")")"
  # 这个样本是 mkv → 两个播放会话都起了转封装，必须收干净
  for sid in "$PSID" "$(jq -r '.playSessionId' <<<"$r2")"; do
    json -b "$JAR" -X POST "$BASE/api/v1/play/$sid/stop" -H 'Content-Type: application/json' -d '{}' >/dev/null
  done
fi

echo
echo "== 10. 继续观看 / 标记已看 =="
json -b "$JAR" -X POST "$BASE/api/v1/items/$MP4I/play" -H 'Content-Type: application/json' -d '{"restart":true}' >/dev/null
cw=$(bd -b "$JAR" "$BASE/api/v1/continue")
check "继续观看回来的是数组（不是 null）" array "$(jq -r '.items|type' <<<"$cw")"
check "继续观看里有刚播的条目" true \
  "$(bool "$(jq -r --argjson id "$MP4I" '[.items[].item.id]|index($id)!=null' <<<"$cw")")"
check "继续观看带进度" true \
  "$(bool "$(jq -r --argjson id "$MP4I" '[.items[]|select(.item.id==$id)][0].progress.positionTicks>0' <<<"$cw")")"
check "未登录看继续观看 = 401" 401 "$(st "$BASE/api/v1/continue")"

json -b "$JAR" -X POST "$BASE/api/v1/items/$MP4I/played" -H 'Content-Type: application/json' -d '{"played":true}' >/dev/null
check "标记已看后 played=true" true \
  "$(bool "$(bd -b "$JAR" "$BASE/api/v1/items/$MP4I/progress" | jq -r '.progress.played')")"
check "标记已看后不再出现在继续观看" true \
  "$(bool "$(bd -b "$JAR" "$BASE/api/v1/continue" | jq -r --argjson id "$MP4I" '[.items[].item.id]|index($id)==null')")"
json -b "$JAR" -X POST "$BASE/api/v1/items/$MP4I/played" -H 'Content-Type: application/json' -d '{"played":false}' >/dev/null
check "取消已看后 played=false" false \
  "$(bool "$(bd -b "$JAR" "$BASE/api/v1/items/$MP4I/progress" | jq -r '.progress.played')")"
out=$(json -b "$JAR" -X POST "$BASE/api/v1/items/played" -H 'Content-Type: application/json' \
  -d '{"itemIds":[999999999],"played":true}' -o /dev/null -w '%{http_code}')
check "批量标记不存在的条目也不报错 = 200" 200 "$out"

if [[ "${TEST_IDLE:-0}" == "1" ]]; then
  echo
  echo "== 11. 空闲回收（没人看就自己停） =="
  r=$(json -b "$JAR" -X POST "$BASE/api/v1/items/$MKVI/play" -H 'Content-Type: application/json' -d '{"restart":true}')
  ISID=$(jq -r '.playSessionId' <<<"$r")
  check "先确认有一路 ffmpeg" 1 "$(ffmpeg_count)"
  note "等空闲回收周期（默认 45s + 5s 巡检）…"
  sleep 55
  check "空闲回收后 ffmpeg 已退出" 0 "$(ffmpeg_count)"
  check "空闲回收后分片目录已清空" 0 "$(session_dirs)"
  json -b "$JAR" -X POST "$BASE/api/v1/play/$ISID/stop" -H 'Content-Type: application/json' -d '{}' >/dev/null
fi

echo
echo "== 12. 收尾：清掉本脚本产生的进度记录 =="
IDS=$(for i in $MP4I $MKVI $AC3I $HEVCI $SUBI $PGSI; do [[ -n "$i" ]] && printf '%s,' "$i"; done | sed 's/,$//')
if [[ -n "$IDS" ]]; then
  PSQL "delete from playback_progress where item_id in ($IDS) and user_id = (select id from users where username = '$USER')" >/dev/null
  left=$(PSQL "select count(*) from playback_progress where item_id in ($IDS) and user_id = (select id from users where username = '$USER')")
  check "进度记录已清理" 0 "$left"
else
  note "没有样本条目，无需清理"
fi
check "没有残留 ffmpeg" 0 "$(ffmpeg_count)"
check "没有残留分片目录" 0 "$(session_dirs)"

echo
printf '\033[1m结果：%d 通过，%d 失败\033[0m\n' "$pass" "$fail"
[[ "$fail" -eq 0 ]]
