#!/usr/bin/env bash
# M4「转码」的真库验收：把「视频必须重新编码」的内容真播一遍。
#
# 与 verify-play.sh 的分工：那边验直出 / 转封装 / 会话 / 进度，这边专验转码链路 ——
# 决策（要转成什么）→ 参数配方（怎么转）→ 真跑（有没有出分片）→ 回收。
#
# 三条不写死的原则（这是给通用软件用的验收，不是给这台机器写的）：
#   1. **不假设本机一定能转**：先读能力表（运行时真跑探测的结果）。探测到可用的
#      h264 编码器才断言「能播」，否则断言的是「如实说放不了」——两条路都必须对。
#   2. **样本按编码条件现挑**，不写死文件名（库会变，编码条件不会）。优先挑 1080p：
#      4K HDR 转码在弱机器上只有零点几倍速，验收得能反复跑。
#   3. 幅面/倍速这类数字只**记录**不硬卡：本项目不把「这台机器的实测值」当常量。
#
# 用法：
#   LMBY_USER=devtest LMBY_PASS=口令 bash scripts/dev/verify-transcode.sh
# 可选环境变量：
#   BASE         默认 http://127.0.0.1:8099
#   STREAMS_DIR  默认 /var/lib/lmby/streams
set -uo pipefail

BASE="${BASE:-http://127.0.0.1:8099}"
USER="${LMBY_USER:-}"
PASS="${LMBY_PASS:-}"
STREAMS_DIR="${STREAMS_DIR:-/var/lib/lmby/streams}"
PGPASSWORD_FILE="${PGPASSWORD_FILE:-/etc/lmby/pg-password}"

if [[ -z "$USER" || -z "$PASS" ]]; then
  echo "用法：LMBY_USER=devtest LMBY_PASS=口令 bash scripts/dev/verify-transcode.sh"
  exit 2
fi

JAR=$(mktemp)
M3U8=/tmp/vt.m3u8
trap 'rm -f "$JAR" "$M3U8"' EXIT

pass=0; fail=0
ok()    { printf '  \033[32mok\033[0m   %s\n' "$1"; pass=$((pass+1)); }
bad()   { printf '  \033[31mFAIL\033[0m %s\n        期望 %s\n        实际 %s\n' "$1" "$2" "$3"; fail=$((fail+1)); }
check() { if [[ "$2" == "$3" ]]; then ok "$1"; else bad "$1" "$2" "$3"; fi; }
note()  { printf '  \033[33m--\033[0m   %s\n' "$1"; }
bool()  { [[ "$1" == "true" ]] && echo true || echo false; }

st()   { curl -s -o /dev/null -w '%{http_code}' "$@"; }
bd()   { curl -s "$@"; }
json() { curl -s -H 'Content-Type: application/json' "$@"; }
PSQL() { PGPASSWORD=$(cat "$PGPASSWORD_FILE") psql -h 127.0.0.1 -U lmby -d lmby -tAc "$1"; }
ffmpeg_count()  { pgrep -f 'ffmpeg .*hls_segment_filename' | wc -l; }
session_dirs()  { find "$STREAMS_DIR" -mindepth 1 -maxdepth 1 -type d -not -name subs 2>/dev/null | wc -l; }
seg_count()     { grep -c 'seg_[0-9]*\.m4s' "$M3U8" 2>/dev/null || echo 0; }
pick_file()     { PSQL "select f.id from media_files f
  where f.deleted_at is null and f.probe_state = 'ok' and $1
  order by f.size_bytes asc limit 1"; }
item_of()       { PSQL "select item_id from media_files where id = $1"; }
file_path()     { PSQL "select path from media_files where id = $1"; }
file_dur()      { PSQL "select coalesce(duration_ticks,0) from media_files where id = $1"; }
file_w()        { PSQL "select coalesce((video_streams->0)->>'width','0')::int from media_files where id = $1"; }
file_h()        { PSQL "select coalesce((video_streams->0)->>'height','0')::int from media_files where id = $1"; }
HDR_COND="(f.video_streams->0)->>'colorTransfer' in ('smpte2084','arib-std-b67')"
SDR10_COND="(f.video_streams->0)->>'codec'='hevc' and coalesce((f.video_streams->0)->>'bitDepth','8')='10' and coalesce((f.video_streams->0)->>'colorTransfer','') not in ('smpte2084','arib-std-b67')"

echo "目标：$BASE"

# 上一个进程留下的分片目录会等到空闲回收（默认 45s）才消失，所以目录数
# 断言「回到基线」而不是硬卡 0 —— 否则验收结果取决于上一次什么时候跑的。
DIRS0=$(session_dirs)
if [[ "${DIRS0:-0}" -gt 0 ]]; then
  note "启动时有 ${DIRS0} 个分片目录残留（上一次运行的会话还没被回收）"
fi

echo
echo "== 0. 挑样本（按编码条件，不写死文件名）=="
S10=$(pick_file "$SDR10_COND and coalesce((f.video_streams->0)->>'width','0')::int between 1 and 1920 and coalesce(f.duration_ticks,0) > 600000000")
S10_4K=$(pick_file "$SDR10_COND and coalesce((f.video_streams->0)->>'width','0')::int > 1920 and (select count(*) from media_files f2 where f2.item_id = f.item_id and f2.deleted_at is null) = 1")
HDRF=$(pick_file "$HDR_COND and coalesce(f.duration_ticks,0) > 60000000")
# 同理：该条目只有一个文件，play 才会落到这只文件上
HI10P=$(pick_file "(f.video_streams->0)->>'codec'='h264' and coalesce((f.video_streams->0)->>'bitDepth','8')='10' and (select count(*) from media_files f2 where f2.item_id = f.item_id and f2.deleted_at is null) = 1")
# 能直出的 1080p+ 单文件条目：验「用户选了低档 → 本来能直出的也要转」
DIRECT_ID=$(pick_file "(f.path ilike '%.mp4' or f.path ilike '%.m4v') and (f.video_streams->0)->>'codec'='h264' and coalesce((f.video_streams->0)->>'bitDepth','8')='8' and coalesce((f.video_streams->0)->>'height','0')::int >= 1080 and coalesce(f.duration_ticks,0) > 600000000 and (select count(*) from media_files f2 where f2.item_id = f.item_id and f2.deleted_at is null) = 1")
for pair in "1080p 10bit HEVC SDR:$S10" "4K 10bit HEVC SDR:$S10_4K" "HDR:$HDRF" "Hi10P（10bit H.264）:$HI10P" "能直出的 1080p+:$DIRECT_ID"; do
  name=${pair%%:*}; id=${pair#*:}
  if [[ -z "$id" ]]; then
    note "$name：库里没有符合条件的样本（相关断言会跳过）"
  else
    note "$name → file=$id $(basename "$(file_path "$id")")"
  fi
done

echo
echo "== 1. 登录与能力表（能不能转由运行时真跑探测说了算） =="
code=$(st -X POST "$BASE/api/v1/auth/login" -c "$JAR" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg u "$USER" --arg p "$PASS" '{username:$u,password:$p}')")
check "登录 $USER = 200" 200 "$code"
[[ "$code" != "200" ]] && exit 1

CAPS=$(bd -b "$JAR" "$BASE/api/v1/transcode/capabilities")
BEST=$(jq -r '.best.name // "（无）"' <<<"$CAPS")
CAN_ENC=$(jq -r '[.capabilities.backends[]? | select(.available == true)] | length' <<<"$CAPS")
note "首选后端：$BEST；可用后端数：$CAN_ENC"
if [[ "${CAN_ENC:-0}" -ge 1 ]]; then
  CAN_TRANSCODE=true
  note "本机能转码 → 下面验「真转真播」"
else
  CAN_TRANSCODE=false
  note "本机没探测到可用编码器 → 下面验「如实说放不了」"
fi

echo
echo "== 2. 转码决策（10bit HEVC：浏览器解不了，位深也超限） =="
if [[ -z "$S10" ]]; then
  note "没有 1080p 10bit HEVC 样本，跳过"
else
  S10I=$(item_of "$S10")
  S10_DUR=$(file_dur "$S10")
  t0=$SECONDS
  r=$(json -b "$JAR" -X POST "$BASE/api/v1/items/$S10I/play" -H 'Content-Type: application/json' -d '{"restart":true}')
  start_cost=$((SECONDS - t0))
  check "决策 = transcode" transcode "$(jq -r '.mode' <<<"$r")"
  check "视频动作 = transcode" transcode "$(jq -r '.plan.video.action' <<<"$r")"
  check "标记需要降到 8bit" true "$(bool "$(jq -r '.plan.video.tenBitToEight' <<<"$r")")"
  check "理由链说明了为什么要转" true "$(bool "$(jq -r '[.reasons[]|test("转码为")]|any' <<<"$r")")"
  check "理由链说明位深超限" true "$(bool "$(jq -r '[.reasons[]|test("10bit")]|any' <<<"$r")")"
  TARGET=$(jq -r '.plan.video.targetCodec // ""' <<<"$r")
  SID=$(jq -r '.playSessionId // ""' <<<"$r")
  HLS=$(jq -r '.hlsUrl // ""' <<<"$r")
  if [[ "$CAN_TRANSCODE" == "true" ]]; then
    check "目标编码 = h264（兼容性最好）" h264 "$TARGET"
    check "起播状态 = ready" ready "$(jq -r '.state' <<<"$r")"
    check "给了播放地址" true "$(bool "$([[ -n "$HLS" && "$HLS" != "null" ]] && echo true)")"
    note "起播耗时 ${start_cost}s（API 的起播超时是 20s）"
    check "起播在超时之内（硬编机器上应当远小于 20s）" true "$(bool "$([[ "$start_cost" -lt 20 ]] && echo true)")"
  else
    check "如实说放不了（不假装能播）" false "$(bool "$(jq -r '.playable' <<<"$r")")"
    check "理由链点名没有可用编码器" true "$(bool "$(jq -r '[.reasons[]|test("没有可用的编码器")]|any' <<<"$r")")"
    check "没有漏出播放地址" "" "$(jq -r '.hlsUrl // ""' <<<"$r")"
  fi

  if [[ "$CAN_TRANSCODE" == "true" && -n "$HLS" && "$HLS" != "null" ]]; then
    U="$BASE$HLS"

    echo
    echo "== 3. 真出分片（转码不是「配好参数就算完」） =="
    sleep 2
    bd -b "$JAR" -o "$M3U8" "$U"
    n1=$(seg_count)
    check "m3u8 有 #EXTM3U" 1 "$(grep -c '#EXTM3U' "$M3U8" 2>/dev/null || echo 0)"
    check "m3u8 声明了 fMP4 init 段" true "$(bool "$(grep -q 'EXT-X-MAP.*init\.mp4' "$M3U8" && echo true)")"
    check "已经产出分片" true "$(bool "$([[ "${n1:-0}" -ge 1 ]] && echo true)")"
    check "ffmpeg 真的在跑" true "$(bool "$([[ "$(ffmpeg_count)" -ge 1 ]] && echo true)")"
    check "分片落到了磁盘" true "$(bool "$(find "$STREAMS_DIR" -name 'seg_*.m4s' | grep -q . && echo true)")"
    SEG=$(grep -o 'seg_[0-9]*\.m4s' "$M3U8" | head -1)
    SEGDIR=$(dirname "$U")
    check "从服务取分片 = 200" 200 "$(st -b "$JAR" "$SEGDIR/seg_00000.m4s")"
    bytes=$(bd -b "$JAR" -o /dev/null -w '%{size_download}' "$SEGDIR/$SEG")
    check "分片不是空的" true "$(bool "$([[ "${bytes:-0}" -gt 10000 ]] && echo true)")"
    note "首个分片 ${bytes} 字节"

    echo
    echo "== 4. 交叉验证：让 ffprobe 直接读服务发出的 HLS =="
    COOKIE=$(awk 'NF>=7 && $6=="lmby_session"{v=$7} END{print "lmby_session="v}' "$JAR")
    probe=$(timeout 90 ffprobe -v error -select_streams v:0 -show_entries stream=codec_name,width,height -of csv=p=0 \
      -headers "Cookie: $COOKIE" -i "$U" 2>&1 | head -1)
    note "ffprobe 读 HLS：${probe:-（读不到）}"
    check "出来的视频确实是 h264（真重编码了）" h264 "$(cut -d, -f1 <<<"$probe")"
    TW=$(jq -r '.plan.video.targetWidth // 0' <<<"$r")
    TH=$(jq -r '.plan.video.targetHeight // 0' <<<"$r")
    if [[ "$TW" == "0" || "$TH" == "0" ]]; then
      TW=$(file_w "$S10"); TH=$(file_h "$S10")
      note "计划里没有缩放目标 → 期望等于源幅面 ${TW}x${TH}"
    fi
    check "输出幅面 = 计划里的目标（${TW}x${TH}）" "${TW}x${TH}" "$(cut -d, -f2,3 <<<"$probe" | tr ',' 'x')"

    echo
    echo "== 5. 生成速率（只记录，不当常量卡阈值） =="
    n1=$(seg_count)
    sleep 6
    bd -b "$JAR" -o "$M3U8" "$U"
    n2=$(seg_count)
    check "6 秒内还在继续产出分片（没卡死）" true "$(bool "$([[ "${n2:-0}" -ge "${n1:-0}" ]] && echo true)")"
    note "6 秒里 $n1 → $n2 个分片（分片时长见 [playback] hls_segment_seconds；含预生成，不是长时间平均倍速）"

    echo
    echo "== 6. 转码路径上的 seek（换窗口要重新起一段转码） =="
    MST=$(( S10_DUR / 2 ))
    t0=$SECONDS
    r2=$(json -b "$JAR" -X POST "$BASE/api/v1/play/$SID/seek" -H 'Content-Type: application/json' \
      -d "$(jq -nc --argjson t "$MST" '{positionTicks:$t}')")
    seek_cost=$((SECONDS - t0))
    check "seek 后仍是转码" transcode "$(jq -r '.mode' <<<"$r2")"
    check "seek 后状态 = ready" ready "$(jq -r '.state' <<<"$r2")"
    check "seek 后起点在片子中间" true \
      "$(bool "$([[ "$(jq -r '.startSeconds|floor' <<<"$r2")" -ge $(( MST / 10000000 - 3 )) ]] && echo true)")"
    note "seek 起播耗时 ${seek_cost}s"
    check "seek 后仍只有 1 路 ffmpeg" 1 "$(ffmpeg_count)"
    check "seek 后 m3u8 仍然可用" 200 "$(st -b "$JAR" "$U")"

    echo
    echo "== 7. stop 后回收（转码进程不能留在后台烧 CPU） =="
    json -b "$JAR" -X POST "$BASE/api/v1/play/$SID/stop" -H 'Content-Type: application/json' -d '{}' >/dev/null
    sleep 1
    check "stop 后没有 ffmpeg 残留" 0 "$(ffmpeg_count)"
    check "stop 后分片目录回到基线（${DIRS0:-0}）" "${DIRS0:-0}" "$(session_dirs)"
    check "stop 后会话失效 = 404" 404 "$(st -b "$JAR" "$U")"
  fi
fi

echo
echo "== 8. 转码输出幅面上限（[playback] transcode_max_height） =="
if [[ -z "$S10_4K" ]]; then
  note "没有 4K 10bit HEVC 样本，跳过"
elif [[ "$CAN_TRANSCODE" != "true" ]]; then
  note "本机不能转码，跳过"
else
  r=$(json -b "$JAR" -X POST "$BASE/api/v1/items/$(item_of "$S10_4K")/play" -H 'Content-Type: application/json' -d '{"restart":true}')
  SID4=$(jq -r '.playSessionId // ""' <<<"$r")
  MODE4=$(jq -r '.mode' <<<"$r")
  if [[ "$MODE4" != "transcode" ]]; then
    # 条目下有能直出/转封装的版本时选中它是**对的**（能直出 > 转封装 > 转码），
    # 只是这次就没验到幅面上限那条路。
    note "这个条目下有能直出/转封装的版本（选了 $MODE4），没覆盖到幅面上限路径"
  else
    check "4K 源要转码" transcode "$MODE4"
    TW=$(jq -r '.plan.video.targetWidth // 0' <<<"$r")
    TH=$(jq -r '.plan.video.targetHeight // 0' <<<"$r")
    if [[ "$TW" == "0" || "$TH" == "0" ]]; then
      note "没有压幅面（transcode_max_height=0 或客户端上限更严）；源幅面 $(file_w "$S10_4K")x$(file_h "$S10_4K")"
    else
      check "目标高度不超过 1080（默认上限）" true "$(bool "$([[ "$TH" -le 1080 ]] && echo true)")"
      check "理由链说明了缩放" true "$(bool "$(jq -r '[.reasons[]|test("缩放到")]|any' <<<"$r")")"
      note "4K → ${TW}x${TH}"
      check "起播状态 = ready（压了幅面之后能在超时内起来）" ready "$(jq -r '.state' <<<"$r")"
    fi
  fi
  [[ -n "$SID4" && "$SID4" != "null" ]] && json -b "$JAR" -X POST "$BASE/api/v1/play/$SID4/stop" -H 'Content-Type: application/json' -d '{}' >/dev/null
fi

echo
echo "== 9. HDR 色调映射（不做映射画面会发灰，做不了要如实说） =="
if [[ -z "$HDRF" ]]; then
  note "库里没有 HDR 样本，跳过"
elif [[ "$CAN_TRANSCODE" != "true" ]]; then
  note "本机不能转码，跳过"
else
  r=$(json -b "$JAR" -X POST "$BASE/api/v1/items/$(item_of "$HDRF")/play" -H 'Content-Type: application/json' -d '{"restart":true}')
  check "HDR 需要转码" transcode "$(jq -r '.mode' <<<"$r")"
  check "计划里标记了源是 HDR" true "$(bool "$(jq -r '.plan.video.sourceHdr' <<<"$r")")"
  if [[ "$(jq -r '.plan.video.tonemap' <<<"$r")" == "true" ]]; then
    check "理由链说明会做色调映射" true "$(bool "$(jq -r '[.reasons[]|test("色调映射")]|any' <<<"$r")")"
    SIDH=$(jq -r '.playSessionId // ""' <<<"$r")
    HLSH=$(jq -r '.hlsUrl // ""' <<<"$r")
    check "起播状态 = ready" ready "$(jq -r '.state' <<<"$r")"
    if [[ -n "$HLSH" && "$HLSH" != "null" ]]; then
      sleep 3
      bd -b "$JAR" -o "$M3U8" "$BASE$HLSH"
      check "HDR 转码真的产出了分片" true "$(bool "$([[ "$(seg_count)" -ge 1 ]] && echo true)")"
    fi
    [[ -n "$SIDH" && "$SIDH" != "null" ]] && json -b "$JAR" -X POST "$BASE/api/v1/play/$SIDH/stop" -H 'Content-Type: application/json' -d '{}' >/dev/null
  else
    check "做不了色调映射时理由链如实说明画面会发灰" true \
      "$(bool "$(jq -r '[.reasons[]|test("发灰")]|any' <<<"$r")")"
    SIDH=$(jq -r '.playSessionId // ""' <<<"$r")
    [[ -n "$SIDH" && "$SIDH" != "null" ]] && json -b "$JAR" -X POST "$BASE/api/v1/play/$SIDH/stop" -H 'Content-Type: application/json' -d '{}' >/dev/null
  fi
fi

echo
echo "== 10. Hi10P（10bit H.264）：任何浏览器都解不了，必须转码 =="
if [[ -z "$HI10P" ]]; then
  note "库里没有 10bit H.264 样本，跳过"
else
  r=$(json -b "$JAR" -X POST "$BASE/api/v1/items/$(item_of "$HI10P")/play" -H 'Content-Type: application/json' -d '{"restart":true}')
  check "决策 = transcode" transcode "$(jq -r '.mode' <<<"$r")"
  if [[ "$CAN_TRANSCODE" == "true" ]]; then
    check "计划里目标编码 = h264" h264 "$(jq -r '.plan.video.targetCodec // ""' <<<"$r")"
    check "标记了降到 8bit" true "$(bool "$(jq -r '.plan.video.tenBitToEight' <<<"$r")")"
  fi
  SIDX=$(jq -r '.playSessionId // ""' <<<"$r")
  [[ -n "$SIDX" && "$SIDX" != "null" ]] && json -b "$JAR" -X POST "$BASE/api/v1/play/$SIDX/stop" -H 'Content-Type: application/json' -d '{}' >/dev/null
fi

echo
echo "== 11. 播放器画质档（maxHeight）：能直出的也要按要求转 =="
if [[ -z "$DIRECT_ID" ]]; then
  note "库里没有「能直出的 1080p+ 单文件条目」，跳过"
elif [[ "$CAN_TRANSCODE" != "true" ]]; then
  note "本机不能转码，跳过"
else
  DIT=$(item_of "$DIRECT_ID")
  r=$(json -b "$JAR" -X POST "$BASE/api/v1/items/$DIT/play" -H 'Content-Type: application/json' -d '{"restart":true}')
  check "不选档位时照旧直出" direct "$(jq -r '.mode' <<<"$r")"
  DA=$(jq -r '.playSessionId // ""' <<<"$r")
  [[ -n "$DA" && "$DA" != "null" ]] && json -b "$JAR" -X POST "$BASE/api/v1/play/$DA/stop" -H 'Content-Type: application/json' -d '{}' >/dev/null

  r=$(json -b "$JAR" -X POST "$BASE/api/v1/items/$DIT/play" -H 'Content-Type: application/json' -d '{"restart":true,"maxHeight":720}')
  check "选了 720p 就变成转码（否则等于没选）" transcode "$(jq -r '.mode' <<<"$r")"
  check "目标高度 = 720" 720 "$(jq -r '.plan.video.targetHeight' <<<"$r")"
  check "理由链说清「是用户自己选的画质」" true "$(bool "$(jq -r '[.reasons[]|test("你选了")]|any' <<<"$r")")"
  QS=$(jq -r '.playSessionId // ""' <<<"$r")
  QU="$BASE$(jq -r '.hlsUrl // ""' <<<"$r")"
  if [[ -n "$QU" && "$QU" != "$BASE" ]]; then
    sleep 2
    QCOOKIE=$(awk 'NF>=7 && $6=="lmby_session"{v=$7} END{print "lmby_session="v}' "$JAR")
    qp=$(timeout 60 ffprobe -v error -select_streams v:0 -show_entries stream=width,height -of csv=p=0 -headers "Cookie: $QCOOKIE" -i "$QU" 2>&1 | head -1)
    note "720p 档的 HLS 输出：${qp:-（读不到）}"
    check "真出 720p（ffprobe 交叉验证）" "1280x720" "$(tr ',' 'x' <<<"$qp")"
  fi
  # 切档位必须换一路会话：会话键里带了编码参数指纹，否则会复用上一档的流
  #（用户切了画质、画面却没变 —— 这是真跑拓出来的坑）
  KEYS1=$(bd -b "$JAR" "$BASE/api/v1/playback/sessions" | jq -r '[.transcodeSessions[].key] | join(",")')
  [[ -n "$QS" && "$QS" != "null" ]] && json -b "$JAR" -X POST "$BASE/api/v1/play/$QS/stop" -H 'Content-Type: application/json' -d '{}' >/dev/null
  r=$(json -b "$JAR" -X POST "$BASE/api/v1/items/$DIT/play" -H 'Content-Type: application/json' -d '{"restart":true,"maxHeight":480}')
  check "切到 480p 仍是转码" transcode "$(jq -r '.mode' <<<"$r")"
  check "切档后目标高度 = 480" 480 "$(jq -r '.plan.video.targetHeight' <<<"$r")"
  KEYS2=$(bd -b "$JAR" "$BASE/api/v1/playback/sessions" | jq -r '[.transcodeSessions[].key] | join(",")')
  check "切档后是另一路会话（会话键含编码参数）" true "$(bool "$([[ "$KEYS1" != "$KEYS2" ]] && echo true)")"
  QS2=$(jq -r '.playSessionId // ""' <<<"$r")
  [[ -n "$QS2" && "$QS2" != "null" ]] && json -b "$JAR" -X POST "$BASE/api/v1/play/$QS2/stop" -H 'Content-Type: application/json' -d '{}' >/dev/null

  # 选「原生」（maxHeight = 0）：不能被配置里的转码上限压掉
  r=$(json -b "$JAR" -X POST "$BASE/api/v1/items/$DIT/play" -H 'Content-Type: application/json' -d '{"restart":true,"maxHeight":0}')
  check "选「原生」时不该因转码上限而转码" direct "$(jq -r '.mode' <<<"$r")"
  DS=$(jq -r '.playSessionId // ""' <<<"$r")
  [[ -n "$DS" && "$DS" != "null" ]] && json -b "$JAR" -X POST "$BASE/api/v1/play/$DS/stop" -H 'Content-Type: application/json' -d '{}' >/dev/null
fi

echo
echo "== 12. 节流：转码跑到客户端前面就暂停 ffmpeg =="
# 必须挑够长的片子：短片（几十秒）整个窗口会被几秒编完、ffmpeg 正常退出，
# 根本没机会节流 —— 这一条踩过（60 秒的样本 9 秒就 finished 了）。
LONG=$(pick_file "$SDR10_COND and coalesce(f.duration_ticks,0) > 36000000000")
if [[ -z "$LONG" || "$CAN_TRANSCODE" != "true" ]]; then
  note "没有「1 小时以上的 10bit HEVC」样本，跳过节流验证"
else
  note "长片样本：file=$LONG $(basename "$(file_path "$LONG")")"
  sstat() { bd -b "$JAR" "$BASE/api/v1/playback/sessions" | jq -c '.transcodeSessions[0] // {}'; }
  r=$(json -b "$JAR" -X POST "$BASE/api/v1/items/$(item_of "$LONG")/play" -H 'Content-Type: application/json' -d '{"restart":true}')
  TS=$(jq -r '.playSessionId // ""' <<<"$r")
  if [[ -z "$TS" || "$TS" == "null" ]]; then
    note "起播失败，跳过节流验证"
  else
    # 这里**故意不拉分片**：客户位置停在起点，转码很快就跑到前面去。
    # 转码比实时快得多（实测 20x+），300 秒窗口十几秒就编完 —— 必须用密集轮询
    # 在这段时间里抓住「暂停」，等固定秒数会错过（ffmpeg 已经正常退出了）。
    paused=0; s1='{}'
    for _ in $(seq 1 24); do
      sleep 0.5
      s1=$(sstat)
      [[ "$(jq -r '.throttled // false' <<<"$s1")" == "true" ]] && { paused=1; break; }
      [[ "$(jq -r '.state // ""' <<<"$s1")" == "finished" ]] && break
    done
    note "节流时状态：$(jq -c '{generatedSeconds,clientSeconds,aheadSeconds,throttled,segments,state}' <<<"$s1")"
    check "ffmpeg 还在跑（短片会几秒编完，那样没机会节流）" ready "$(jq -r '.state // ""' <<<"$s1")"
    check "转码跑到客户端前面后，ffmpeg 被暂停" 1 "$paused"

    if [[ "$paused" == "1" ]]; then
      n1=$(jq -r '.segments // 0' <<<"$s1")
      FPID=$(pgrep -f 'ffmpeg .*hls_segment_filename' | head -1)
      check "ffmpeg 进程真的被停住（/proc 状态 = T）" T "$(awk '/^State:/{print $2}' "/proc/$FPID/status" 2>/dev/null)"
      sleep 6
      n2=$(jq -r '.segments // 0' <<<"$(sstat)")
      check "暂停期间不再产出分片（还在涨就说明没停住）" "$n1" "$n2"

      if [[ "${n2:-0}" -ge 1 ]]; then
        # 把最后几个分片拉一遍 = 客户端消费到那里 → 差值回落 → 应当恢复
        for i in $(seq "$(( n2 > 4 ? n2 - 4 : 0 ))" "$(( n2 - 1 ))"); do
          bd -b "$JAR" -o /dev/null "$BASE/api/v1/play/$TS/$(printf 'seg_%05d.m4s' "$i")"
        done
        sleep 7
        s3=$(sstat)
        note "客户端追上后：$(jq -c '{generatedSeconds,clientSeconds,aheadSeconds,throttled,segments,state}' <<<"$s3")"
        check "恢复后继续产出分片（真的从暂停里回来了）" true "$(bool "$([[ "$(jq -r '.segments // 0' <<<"$s3")" -gt "$n2" ]] && echo true)")"
        RPID=$(pgrep -f 'ffmpeg .*hls_segment_filename' | head -1)
        check "恢复后进程不再处于暂停态" true "$(bool "$([[ "$(awk '/^State:/{print $2}' "/proc/$RPID/status" 2>/dev/null)" != "T" ]] && echo true)")"
      fi
    fi
    json -b "$JAR" -X POST "$BASE/api/v1/play/$TS/stop" -H 'Content-Type: application/json' -d '{}' >/dev/null
  fi
fi

echo
echo "== 13. 收尾：没有会话/进程残留 =="
json -b "$JAR" -X POST "$BASE/api/v1/auth/logout" -o /dev/null >/dev/null 2>&1 || true
sleep 2
check "跑完没有 ffmpeg 残留" 0 "$(ffmpeg_count)"
check "跑完分片目录没有增长（回到基线 ${DIRS0:-0}）" "${DIRS0:-0}" "$(session_dirs)"

echo
echo "================ 结果：$pass 通过 / $fail 失败 ================"
[[ "$fail" -eq 0 ]] || exit 1
