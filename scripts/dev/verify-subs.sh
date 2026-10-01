#!/usr/bin/env bash
# 校验「外挂字幕与视频的关联」：扫描到 .ass/.srt 之后，播放器要能像选内嵌轨道一样选它。
#
# 背景：M1 起扫描器就只对外挂字幕做了一次 w.stats.Subtitles++（见 docs/ROADMAP.md
# 「外挂字幕与视频的关联」与 docs/LIBRARY-NOTES.md 的字幕行），库里没有任何关联记录，
# 结果就是首页海报拿得到、字幕一条都选不到 —— 哪怕目录里每集都配了 .chs.ass。
#
# 分两段验：
#   第一段（纯 HTTP，自造素材）：扫描能把同名外挂字幕挂到视频上，界面能看到合成轨道。
#   第二段（借真实库一条已探测的剧集）：ass 原样下发、srt 转 WebVTT、
#     非 UTF-8（GB18030）会被转成 UTF-8，三种形态字节都要对。
#
# 为什么第二段要借真实条目的库：起播接口只在「探测完成、判定可播」时才给会话 id，
# 而新扫描的文件要排队等 ffprobe —— 真实环境的探测队列可能积压几千个（本环境实测
# 9408 个、约 3~4 小时），等不完。所以这一段直接在库里插一条字幕记录（扫描器本该写的那行），
# 借一条已探测的剧集把下发链路走通。
#
# 用法：LMBY_USER=... LMBY_PASS=... bash scripts/dev/verify-subs.sh
# 环境变量：BASE、FFMPEG、PG_*（第二段用，默认连本机 lmby 库；连不上就跳过第二段）
set -uo pipefail

BASE=${BASE:-http://127.0.0.1:8099}
USER=${LMBY_USER:-}
PASS=${LMBY_PASS:-}
FF=${FFMPEG:-ffmpeg}
PGHOST=${PGHOST:-127.0.0.1}
PGUSER=${PGUSER:-lmby}
PGDATABASE=${PGDATABASE:-lmby}
GROUP="@"
PASS_N=0
FAIL_N=0

check() { # check 名称 期望 实际
  local name="$1" want="$2" got="$3"
  if [[ "$want" == "$got" ]]; then
    printf "${GROUP} ok   %s\n" "$name"; PASS_N=$((PASS_N + 1))
  else
    printf "${GROUP} FAIL %s（期望 %s，实际 %s）\n" "$name" "$want" "$got"; FAIL_N=$((FAIL_N + 1))
  fi
}
note() { printf "${GROUP} --   %s\n" "$1"; }

if [[ -z "$USER" || -z "$PASS" ]]; then
  echo "用法：LMBY_USER=... LMBY_PASS=... bash $0" >&2
  exit 2
fi

JAR=$(mktemp)
WORK=$(mktemp -d)
LIB_ID=""
FIX_ITEM=""

psql_q() { psql -h "$PGHOST" -U "$PGUSER" -d "$PGDATABASE" -tAc "$1"; }

cleanup() {
  [[ -n "$LIB_ID" ]] && curl -s -o /dev/null -X DELETE -b "$JAR" "$BASE/api/v1/libraries/$LIB_ID"
  [[ -n "$FIX_ITEM" ]] && psql_q "delete from subtitles where item_id=$FIX_ITEM" >/dev/null 2>&1
  rm -rf "$WORK" "$JAR"
}
trap cleanup EXIT

echo "== 0. 登录 =="
code=$(curl -s -o /dev/null -w '%{http_code}' -c "$JAR" -X POST "$BASE/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  --data-binary "$(jq -nc --arg u "$USER" --arg p "$PASS" '{username:$u,password:$p}')")
check "登录" 200 "$code"
[[ "$code" == "200" ]] || { note "登录失败，后面没法验"; exit 2; }

echo
echo "== 1. 造素材（3 集：ass / srt / GB18030-srt）=="
SERIES="$WORK/media/Verify Subs Show (2020)"
SEASON="$SERIES/Season 1"
mkdir -p "$SEASON"
# 必须是真的视频：下载/扫描后要能探测出流信息，乱填字节的假文件会被当成「不可播放」。
for e in 01 02 03; do
  $FF -hide_banner -loglevel error -f lavfi -i "testsrc2=size=1280x720:rate=30" -t 3 \
    -c:v libx264 -preset ultrafast -pix_fmt yuv420p -y "$SEASON/S01E$e.mkv"
done
printf '<?xml version="1.0" encoding="utf-8"?>\n<tvshow><title>Verify Subs Show</title><year>2020</year></tvshow>\n' \
  > "$SERIES/tvshow.nfo"
cat > "$SEASON/S01E01.chs.ass" <<'ASS'
[Script Info]
Title: 校验用
ScriptType: v4.00+

[Events]
Format: Layer, Start, End, Style, Text
Dialogue: 0,0:00:01.00,0:00:03.00,,ASS正文标记AAA
ASS
printf '1\n00:00:01,000 --> 00:00:03,000\nSRT正文标记BBB，带逗号\n\n' > "$SEASON/S01E02.chs.srt"
if command -v iconv >/dev/null 2>&1; then
  printf '1\n00:00:01,000 --> 00:00:03,000\nGB18030正文标记CCC\n\n' \
    | iconv -f UTF-8 -t GB18030 > "$SEASON/S01E03.chs.srt"
fi
check "字幕文件就位" "3" "$(ls "$SEASON" | grep -c '\.\(ass\|srt\)$')"

echo
echo "== 2. 建临时媒体库并扫描 =="
libjson=$(curl -s -b "$JAR" -H 'Content-Type: application/json' \
  --data-binary "$(jq -nc --arg p "$WORK/media" '{name:"verify-subs",kind:"tv",paths:[$p]}')" \
  "$BASE/api/v1/libraries")
LIB_ID=$(jq -r '.id // empty' <<<"$libjson")
check "建库拿到 id" "true" "$([[ -n "$LIB_ID" ]] && echo true || echo false)"
[[ -n "$LIB_ID" ]] || { note "建库失败：$libjson"; exit 1; }

curl -s -o /dev/null -b "$JAR" -X POST "$BASE/api/v1/libraries/$LIB_ID/scan"
running=true
for _ in $(seq 1 90); do
  running=$(curl -s -b "$JAR" "$BASE/api/v1/libraries/$LIB_ID/scan" | jq -r '.running // false')
  [[ "$running" == "false" ]] && break
  sleep 1
done
check "扫描跑完" "false" "$running"

echo
echo "== 3. 每集都要挂上外挂字幕（合成序号 1000+，编码与语言从文件名解析出来）=="
episodes=$(curl -s -b "$JAR" "$BASE/api/v1/libraries/$LIB_ID/items?kind=episode" | jq -r '.items[].id')
check "扫到 3 集" "3" "$(wc -w <<<"$episodes")"
EXT_N=0
for eid in $episodes; do
  playlist=$(curl -s -b "$JAR" "$BASE/api/v1/items/$eid/playlist")
  n=$(jq -r '[.files[].subtitles[]? | select(.index >= 1000)] | length' <<<"$playlist" 2>/dev/null)
  EXT_N=$((EXT_N + ${n:-0}))
done
check "3 集各挂 1 条外挂字幕（合计 3）" "3" "$EXT_N"

echo
echo "== 4. 三种形态的下发（借一条真实已探测剧集，直接插库绕过探测队列）=="
if ! command -v psql >/dev/null 2>&1 || [[ -z "${PGPASSWORD:-}" && ! -r /etc/lmby/pg-password ]]; then
  note "没有 psql 或数据库口令，跳过第 4 段"
else
  [[ -z "${PGPASSWORD:-}" ]] && export PGPASSWORD=$(cat /etc/lmby/pg-password)
  # 外挂字幕不进 media_files（扫描器只计数、不登记文件），所以同名字幕要在磁盘上找：
  # 随机取一批已探测的剧集，挑第一个旁边真有 .chs.ass / .ass 的。
  FIX_ITEM=""; VT=""
  while IFS='|' read -r iid vpath; do
    for cand in "${vpath%.mkv}.chs.ass" "${vpath%.mkv}.ass"; do
      if [[ -f "$cand" ]]; then FIX_ITEM="$iid"; VT="$vpath"; break 2; fi
    done
  done < <(psql_q "select mf.item_id, mf.path from media_files mf
      where mf.probe_state = 'ok' and mf.path like '%.mkv' order by random() limit 40")
  check "找到一条带外挂字幕的已探测剧集" "true" \
    "$([[ -n "$FIX_ITEM" && -f "$VT" ]] && echo true || echo false)"

  if [[ -n "$FIX_ITEM" && -f "$VT" ]]; then
    SRC="${VT%.mkv}.chs.ass"
    iconv -f UTF-8 -t GB18030 "$SRC" > "$WORK/gb.ass" 2>/dev/null || cp "$SRC" "$WORK/gb.ass"
    printf '1\n00:00:01,000 --> 00:00:03,000\nSRT标记BBB，带逗号\n\n' > "$WORK/t.srt"
    psql_q "insert into subtitles (item_id,path,language,title,format,forced,size_bytes,mtime_ns)
            values ($FIX_ITEM,'$SRC','chs','','ass',false,0,0),
                   ($FIX_ITEM,'$WORK/gb.ass','cht','GB18030','ass',false,0,0),
                   ($FIX_ITEM,'$WORK/t.srt','chs','SRT','srt',false,0,0)
            on conflict do nothing" >/dev/null

    fetch() { # fetch 序号 → 落盘，同时把决策动作写到 $WORK/act
      local r
      r=$(curl -s -b "$JAR" -H 'Content-Type: application/json' \
        -d "$(jq -nc --argjson i "$1" '{subtitleStreamIndex:$i}')" \
        "$BASE/api/v1/items/$FIX_ITEM/play")
      jq -r '.plan.subtitle.action + "/" + (.plan.subtitle.deliverAs // "-")' <<<"$r" > "$WORK/act"
      local u; u=$(jq -r '.subtitleUrl // empty' <<<"$r")
      [[ -z "$u" ]] && return 1
      [[ "$u" == /* ]] && u="$BASE$u"
      curl -s -b "$JAR" "$u"
    }

    fetch 1000 > "$WORK/a.bin" && check "ass 决策交给 libass 渲染" "convert/libass" "$(cat "$WORK/act")"
    check "ass 原样下发，与磁盘源文件逐字节一致" "true" \
      "$(cmp -s "$WORK/a.bin" "$SRC" && echo true || echo false)"
    fetch 1001 > "$WORK/b.bin"
    check "GB18030 转码后与 UTF-8 版逐字节一致（中文没乱码）" "true" \
      "$(cmp -s "$WORK/b.bin" "$WORK/a.bin" && echo true || echo false)"
    fetch 1002 > "$WORK/c.bin"
    check "srt 转成 WebVTT" "true" "$(head -c 6 "$WORK/c.bin" | grep -q WEBVTT && echo true || echo false)"
    check "srt 正文与逗号都在" "true" \
      "$(grep -q 'SRT标记BBB，带逗号' "$WORK/c.bin" && echo true || echo false)"
  fi
fi

echo
echo "== 5. 收摊 =="
del_code=$(curl -s -o /dev/null -w '%{http_code}' -X DELETE -b "$JAR" "$BASE/api/v1/libraries/$LIB_ID")
check "删除临时库" "200" "$del_code"
LIB_ID=""

echo
echo "== 汇总：$PASS_N 过 / $FAIL_N 败 =="
[[ "$FAIL_N" -eq 0 ]] || exit 1
