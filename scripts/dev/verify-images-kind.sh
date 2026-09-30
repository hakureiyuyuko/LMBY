#!/usr/bin/env bash
# 校验「本地宽幅图必须能被 backdrop 请求命中」（图片 kind 别名：fanart ↔ backdrop）。
#
# 背景：扫描器把媒体目录里的宽幅图（fanart.jpg / backdrop.jpg / background.jpg / art.jpg）
# 一律登记成 kind=fanart（见 internal/scanner 的 dirLevelImageKind），而界面请求的是
# kind=backdrop，TMDB 回源回来的也记成 backdrop。
#
# 两边名字不一致时，取图如果按「严格相等」匹配，本地那张宽幅图就永远命不中 ——
# 症状是「明明有 fanart.jpg，界面还是去 TMDB 回源」，首页 hero 与详情页背景
# 只能退回竖版海报（Home.tsx 的 onError 就是退到 poster）。
#
# 这条脚本把「同目录只有 fanart.jpg 时，backdrop 也要拿得到同一张图」钉死。
# 它自己造素材、自己在库里加一个临时媒体库，跑完连库带目录一起收掉。
#
# 用法：LMBY_USER=... LMBY_PASS=... bash scripts/dev/verify-images-kind.sh
# 环境变量：BASE（默认 http://127.0.0.1:8099）、FFMPEG（默认 ffmpeg）
# 依赖：curl、jq、ffmpeg（造测试图）、mktemp
set -uo pipefail

BASE=${BASE:-http://127.0.0.1:8099}
USER=${LMBY_USER:-}
PASS=${LMBY_PASS:-}
FF=${FFMPEG:-ffmpeg}
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

cleanup() {
  if [[ -n "$LIB_ID" ]]; then
    curl -s -o /dev/null -X DELETE -b "$JAR" "$BASE/api/v1/libraries/$LIB_ID"
  fi
  rm -rf "$WORK" "$JAR"
}
trap cleanup EXIT

echo "== 0. 登录 =="
code=$(curl -s -o /dev/null -w '%{http_code}' -c "$JAR" -X POST "$BASE/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  --data-binary "$(jq -nc --arg u "$USER" --arg p "$PASS" '{username:$u,password:$p}')")
check "登录" 200 "$code"
[[ "$code" == "200" ]] || { note "登录失败，后面没法验，直接退出"; exit 2; }

echo
echo "== 1. 造素材（宽 1920x1080 的 fanart.jpg + 竖 600x900 的 poster.jpg）=="
SERIES="$WORK/media/Verify Kind Show (2020)"
mkdir -p "$SERIES/Season 1"
$FF -hide_banner -loglevel error -f lavfi -i "testsrc2=size=1920x1080" \
  -frames:v 1 -q:v 3 -y "$SERIES/fanart.jpg"
$FF -hide_banner -loglevel error -f lavfi -i "testsrc2=size=600x900" \
  -frames:v 1 -q:v 3 -y "$SERIES/poster.jpg"
# 扫描器默认 64KiB 以下不算视频，所以给个 200KB 的假视频占位
head -c 200000 /dev/urandom > "$SERIES/Season 1/S01E01.mp4"
printf '<?xml version="1.0" encoding="utf-8"?>\n<tvshow><title>Verify Kind Show</title><year>2020</year></tvshow>\n' \
  > "$SERIES/tvshow.nfo"
check "fanart.jpg 就位" "true" "$([[ -s "$SERIES/fanart.jpg" ]] && echo true || echo false)"

echo
echo "== 2. 建临时媒体库并扫描 =="
libjson=$(curl -s -b "$JAR" -H 'Content-Type: application/json' \
  --data-binary "$(jq -nc --arg p "$WORK/media" '{name:"verify-images-kind",kind:"tv",paths:[$p]}')" \
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
check "扫描跑完（不再 running）" "false" "$running"

echo
echo "== 3. 找条目 =="
series_id=$(curl -s -b "$JAR" "$BASE/api/v1/libraries/$LIB_ID/items?kind=series" \
  | jq -r '.items[0].id // empty')
check "有剧集条目" "true" "$([[ -n "$series_id" ]] && echo true || echo false)"
[[ -n "$series_id" ]] || { note "没有条目，后面没法验"; exit 1; }

# fetch 取图：回显 "状态码 输出宽 字节数"
fetch() {
  # 注意：赋值必须分开写 —— bash 会先展开整条 local 的右侧，
  # 写成一行时 body="...$kind..." 里的 $kind 还没赋值，set -u 会直接报错。
  local kind="$1" code w
  local body="$WORK/got-$kind.bin"
  local hdr="$WORK/hdr-$kind.txt"
  code=$(curl -s -o "$body" -D "$hdr" -w '%{http_code}' -b "$JAR" \
    "$BASE/api/v1/items/$series_id/images/$kind?w=800")
  w=$(grep -i '^x-image-width:' "$hdr" | tr -d '\r' | awk '{print $2}')
  echo "$code ${w:-0} $(stat -c%s "$body" 2>/dev/null || echo 0)"
}

echo
echo "== 4. 取图（series id=$series_id）=="
read -r p_code p_w _p_bytes <<<"$(fetch poster)"
check "poster → 200（本地 poster.jpg）" "200" "$p_code"
check "poster 宽度 = 600" "600" "$p_w"

read -r f_code f_w f_bytes <<<"$(fetch fanart)"
check "fanart → 200（本地宽幅图）" "200" "$f_code"
check "fanart 宽度 = 800" "800" "$f_w"
check "fanart 真的拿到了字节（不为空）" "true" "$([[ "${f_bytes:-0}" -gt 0 ]] && echo true || echo false)"

read -r b_code b_w b_bytes <<<"$(fetch backdrop)"
check "backdrop → 200（本地只有 fanart.jpg 也要拿得到）" "200" "$b_code"
check "backdrop 宽度 = 800（给的是那张 fanart）" "800" "$b_w"
check "backdrop 与 fanart 是同一张图（字节数一致）" "$f_bytes" "$b_bytes"

read -r t_code _t_w _t_bytes <<<"$(fetch thumb)"
check "thumb → 404（这个库确实没有剧照）" "404" "$t_code"

echo
echo "== 5. 收摊 =="
del_code=$(curl -s -o /dev/null -w '%{http_code}' -X DELETE -b "$JAR" "$BASE/api/v1/libraries/$LIB_ID")
check "删除临时库" "200" "$del_code"
get_code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" "$BASE/api/v1/libraries/$LIB_ID")
check "删完再查 → 404" "404" "$get_code"
LIB_ID=""

echo
echo "== 汇总：$PASS_N 过 / $FAIL_N 败 =="
[[ "$FAIL_N" -eq 0 ]] || exit 1
