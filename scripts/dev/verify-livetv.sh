#!/usr/bin/env bash
# 直播电视（M5）接口验收：源导入 / 增量更新 / 频道管理 / 收藏 / 导出。
#
# 用法：
#   LMBY_USER=devtest LMBY_PASS=xxx bash scripts/dev/verify-livetv.sh
#
# 环境变量：
#   BASE             服务地址（默认 http://127.0.0.1:8099）
#   LIVE_M3U         真实播放列表文件路径（不给就用仓库里的 testdata/sample.m3u 假地址）
#   LIVE_EXPECT      真实播放列表期望导入的频道数（给了 LIVE_M3U 时用；不给就只断言 > 0）
#
# 说明：
#   - 脚本会真的往库里导入一批频道（这是它要验的事），并在结束时删掉自己的直播源；
#     导入进来的频道**保留**（对用户有用：149 台就是这么导进去的）；
#   - 频道字段的手工改动会在结束时还原（现场快照 + EXIT 陷阱）。
set -uo pipefail

BASE=${BASE:-http://127.0.0.1:8099}
USER=${LMBY_USER:-}
PASS=${LMBY_PASS:-}
M3U=${LIVE_M3U:-}
EXPECT=${LIVE_EXPECT:-}
SRC_NAME="验收-直播源-$$"

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
note() { printf '  --   %s\n' "$1"; }

if [[ -z "$USER" || -z "$PASS" ]]; then
  echo "用法：LMBY_USER=... LMBY_PASS=... bash $0" >&2
  exit 2
fi

JAR=$(mktemp)
SRC_ID=""
SNAP_CH=""
TMP_M3U=$(mktemp)

cleanup() {
  # 还原被改过的频道字段与收藏状态（脚本自己动过的，别把现场搞脏）
  if [[ -n "$SNAP_CH" ]]; then
    local id name group sort disabled fav cur
    id=$(jq -r '.id' <<<"$SNAP_CH")
    name=$(jq -r '.name' <<<"$SNAP_CH")
    group=$(jq -r '.group' <<<"$SNAP_CH")
    sort=$(jq -r '.sortOrder' <<<"$SNAP_CH")
    disabled=$(jq -r '.disabled' <<<"$SNAP_CH")
    fav=$(jq -r '.favorite' <<<"$SNAP_CH")
    curl -s -o /dev/null -b "$JAR" -X PATCH "$BASE/api/v1/livetv/channels/$id" \
      -H 'Content-Type: application/json' \
      --data-binary "$(jq -nc --arg n "$name" --arg g "$group" --argjson s "$sort" --argjson d "$disabled" \
        '{name:$n,group:$g,sortOrder:$s,disabled:$d}')" || true
    cur=$(curl -s -b "$JAR" "$BASE/api/v1/livetv/channels/$id" | jq -r '.favorite')
    if [[ "$cur" != "$fav" ]]; then
      curl -s -o /dev/null -b "$JAR" -X POST "$BASE/api/v1/livetv/channels/$id/favorite" || true
    fi
  fi
  if [[ -n "$SRC_ID" ]]; then
    curl -s -o /dev/null -b "$JAR" -X DELETE "$BASE/api/v1/livetv/sources/$SRC_ID" || true
  fi
  rm -f "$JAR" "$TMP_M3U"
}
trap cleanup EXIT

echo "== 0. 登录 =="
code=$(curl -s -o /dev/null -w '%{http_code}' -c "$JAR" -X POST "$BASE/api/v1/auth/login" \
  -H 'Content-Type: application/json' --data-binary "$(jq -nc --arg u "$USER" --arg p "$PASS" '{username:$u,password:$p}')")
check "登录" 200 "$code"
[[ "$code" == "200" ]] || exit 1

echo
echo "== 0.5 清掉上次可能遗留的验收源 =="
# 脚本被管道 SIGPIPE 打断时 EXIT 陷阱不一定会跑，这里补一道自愈
LEFTOVER=$(curl -s -b "$JAR" "$BASE/api/v1/livetv/sources" \
  | jq -r '.sources[] | select(.name | startswith("验收-直播源-")) | .id')
for old in $LEFTOVER; do
  curl -s -o /dev/null -b "$JAR" -X DELETE "$BASE/api/v1/livetv/sources/$old"
done
check "遗留的验收源已清理" 0 \
  "$(curl -s -b "$JAR" "$BASE/api/v1/livetv/sources" | jq '[.sources[] | select(.name | startswith("验收-直播源-"))] | length')"

# 准备播放列表文本
if [[ -n "$M3U" && -f "$M3U" ]]; then
  cp "$M3U" "$TMP_M3U"
  note "使用真实播放列表：$M3U"
else
  cp "$(dirname "$0")/../../internal/livetv/testdata/sample.m3u" "$TMP_M3U"
  note "使用仓库内假地址样例（未给 LIVE_M3U）"
fi

echo
echo "== 1. 导入播放列表（粘贴） =="
BODY=$(jq -nc --arg name "$SRC_NAME" --rawfile content "$TMP_M3U" \
  '{name:$name,kind:"paste",content:$content}')
CREATED=$(curl -s -b "$JAR" -X POST "$BASE/api/v1/livetv/sources" \
  -H 'Content-Type: application/json' --data-binary "$BODY")
SRC_ID=$(jq -r '.source.id // empty' <<<"$CREATED")
check "导入返回中有 source.id" true "$([[ -n $SRC_ID ]] && echo true || echo false)"
ADDED=$(jq -r '.import.added' <<<"$CREATED")
# 「新增或复用」的总数：首次导入是 added=N、重复导入是 kept=N，两种都算通过
check "导入条目数（去重 + 过滤非流地址后）" "${EXPECT:-8}" "$(jq -r '.import.total' <<<"$CREATED")"
check "新增或复用总数" "${EXPECT:-8}" "$(jq -r '.import.added + .import.kept' <<<"$CREATED")"

echo
echo "== 2. 频道列表 / 分组 / 搜索 =="
LIST=$(curl -s -b "$JAR" "$BASE/api/v1/livetv/channels")
TOTAL=$(jq -r '.total' <<<"$LIST")
checkge "频道总数" "${EXPECT:-$ADDED}" "$TOTAL"
GROUP_N=$(jq -r '.groups | length' <<<"$LIST")
checkge "分组数" 1 "$GROUP_N"
# 分组里应当出现「央视」（样例与真实单播源都有 CCTV）
check "有「央视」分组" true "$(jq -r '[.groups[].name] | contains(["央视"])' <<<"$LIST")"
FIRST=$(jq -r '.channels[0]' <<<"$LIST")
SNAP_CH="$FIRST"
CH_ID=$(jq -r '.id' <<<"$FIRST")
CH_NAME=$(jq -r '.name' <<<"$FIRST")
check "频道有协议标签" true "$(jq -r '.kind | test("RTSP|HTTP|UDP|RTMP|其他")' <<<"$FIRST")"

SEARCH=$(curl -s -b "$JAR" --get --data-urlencode "q=$CH_NAME" "$BASE/api/v1/livetv/channels")
checkge "按名字搜索命中" 1 "$(jq -r '.channels | length' <<<"$SEARCH")"
SEARCH_MISS=$(curl -s -b "$JAR" --get --data-urlencode "q=不存在的频道名字zzz" "$BASE/api/v1/livetv/channels")
check "搜不到时返回 0 条" 0 "$(jq -r '.channels | length' <<<"$SEARCH_MISS")"

echo
echo "== 3. 手动编辑 / 停用 / 收藏 =="
PATCHED=$(curl -s -b "$JAR" -X PATCH "$BASE/api/v1/livetv/channels/$CH_ID" \
  -H 'Content-Type: application/json' --data-binary '{"group":"验收分组","disabled":true}')
check "改分组生效" "验收分组" "$(jq -r '.group' <<<"$PATCHED")"
check "停用生效" true "$(jq -r '.disabled' <<<"$PATCHED")"
ENABLED_ONLY=$(curl -s -b "$JAR" "$BASE/api/v1/livetv/channels?enabled=1")
check "停用的频道不在「只看启用」里" false \
  "$(jq -r --argjson id "$CH_ID" '[.channels[].id] | contains([$id])' <<<"$ENABLED_ONLY")"

FAV0=$(jq -r '.favorite' <<<"$FIRST")
if [[ "$FAV0" == "true" ]]; then
  # 脚本要断言「切换」的语义，起点必须是未收藏（上次跑剩下的话先归零）
  curl -s -o /dev/null -b "$JAR" -X POST "$BASE/api/v1/livetv/channels/$CH_ID/favorite"
fi
FAV=$(curl -s -b "$JAR" -X POST "$BASE/api/v1/livetv/channels/$CH_ID/favorite")
check "收藏返回 favorite=true" true "$(jq -r '.favorite' <<<"$FAV")"
FAV_ONLY=$(curl -s -b "$JAR" "$BASE/api/v1/livetv/channels?favorites=1")
check "只看收藏命中" true "$(jq -r --argjson id "$CH_ID" '[.channels[].id] | contains([$id])' <<<"$FAV_ONLY")"
FAV2=$(curl -s -b "$JAR" -X POST "$BASE/api/v1/livetv/channels/$CH_ID/favorite")
check "再点一次取消收藏" false "$(jq -r '.favorite' <<<"$FAV2")"

echo
echo "== 4. 重复导入：增量更新，且不冲掉用户的停用与收藏 =="
# 先给这条频道重新加上收藏，验证下一次导入不会把收藏/停用冲掉
curl -s -o /dev/null -b "$JAR" -X POST "$BASE/api/v1/livetv/channels/$CH_ID/favorite"
AGAIN=$(curl -s -b "$JAR" -X POST "$BASE/api/v1/livetv/sources" \
  -H 'Content-Type: application/json' --data-binary "$(jq -nc --arg name "$SRC_NAME-2" --rawfile content "$TMP_M3U" \
    '{name:$name,kind:"paste",content:$content}')")
SRC2_ID=$(jq -r '.source.id' <<<"$AGAIN")
check "第二次导入没有新增" 0 "$(jq -r '.import.added' <<<"$AGAIN")"
checkge "第二次导入是 kept" 1 "$(jq -r '.import.kept' <<<"$AGAIN")"
AFTER=$(curl -s -b "$JAR" "$BASE/api/v1/livetv/channels?favorites=1")
check "收藏没被导入冲掉" true "$(jq -r --argjson id "$CH_ID" '[.channels[].id] | contains([$id])' <<<"$AFTER")"
STILL_DISABLED=$(curl -s -b "$JAR" --get --data-urlencode "q=$CH_NAME" "$BASE/api/v1/livetv/channels")
check "停用状态没被导入冲掉" true \
  "$(jq -r --argjson id "$CH_ID" '[.channels[] | select(.id==$id) | .disabled] | first' <<<"$STILL_DISABLED")"
curl -s -o /dev/null -b "$JAR" -X DELETE "$BASE/api/v1/livetv/sources/$SRC2_ID"

echo
echo "== 5. 源管理 =="
SOURCES=$(curl -s -b "$JAR" "$BASE/api/v1/livetv/sources")
check "源列表里有本次的源" true "$(jq -r --arg n "$SRC_NAME" '[.sources[].name] | contains([$n])' <<<"$SOURCES")"
check "源上带 lastStatus=ok" "ok" "$(jq -r --arg n "$SRC_NAME" '[.sources[] | select(.name==$n) | .lastStatus] | first' <<<"$SOURCES")"
UPD=$(curl -s -b "$JAR" -X PATCH "$BASE/api/v1/livetv/sources/$SRC_ID" \
  -H 'Content-Type: application/json' --data-binary '{"refreshIntervalMinutes":360}')
check "改自动刷新间隔" 360 "$(jq -r '.refreshIntervalMinutes' <<<"$UPD")"
NONURL=$(curl -s -b "$JAR" -o /dev/null -w '%{http_code}' -X POST "$BASE/api/v1/livetv/sources/$SRC_ID/refresh")
check "粘贴型源刷新被拒（400）" 400 "$NONURL"
SRC_BEFORE=$(curl -s -b "$JAR" "$BASE/api/v1/livetv/sources" | jq '.sources | length')
EMPTY=$(curl -s -b "$JAR" -o /dev/null -w '%{http_code}' -X POST "$BASE/api/v1/livetv/sources" \
  -H 'Content-Type: application/json' --data-binary '{"name":"空的","kind":"paste","content":"#EXTM3U\n"}')
check "空内容被拒（400）" 400 "$EMPTY"
check "被拒时没留下空源" "$SRC_BEFORE" \
  "$(curl -s -b "$JAR" "$BASE/api/v1/livetv/sources" | jq '.sources | length')"

echo
echo "== 6. 导出 m3u =="
EXPORT=$(curl -s -b "$JAR" "$BASE/api/v1/livetv/export.m3u")
check "导出以 #EXTM3U 开头" "#EXTM3U" "$(head -1 <<<"$EXPORT")"
check "导出里有 RTSP 地址" true "$(grep -q 'rtsp://' <<<"$EXPORT" && echo true || echo false)"

echo
echo "== 7. 删除源后频道仍在 =="
curl -s -o /dev/null -b "$JAR" -X DELETE "$BASE/api/v1/livetv/sources/$SRC_ID"
SRC_ID=""
LEFT=$(curl -s -b "$JAR" --get --data-urlencode "q=$CH_NAME" "$BASE/api/v1/livetv/channels")
checkge "频道还在" 1 "$(jq -r '.channels | length' <<<"$LEFT")"

echo
echo "== 8. 权限 =="
ANON=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/v1/livetv/channels")
check "未登录读频道列表被拒（401）" 401 "$ANON"
ANON2=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/v1/livetv/sources")
check "未登录读源列表被拒（401）" 401 "$ANON2"

echo
echo "================ 结果：$PASS_N 通过 / $FAIL_N 失败 ================"
[[ "$FAIL_N" -eq 0 ]]
