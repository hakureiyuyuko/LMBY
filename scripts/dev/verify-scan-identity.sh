#!/usr/bin/env bash
# 「条目身份」验收：加了新文件、或文件被软删又回来时，**不许再造一份同名剧集**。
#
# 背景（2026-09-27 在验证实例上查清的真 bug）：
#   条目身份是 `(库, 类型, 标题, 年份)`，而这两个字段**会被 nfo / 刮削改写**
#   （`applyItemMetaSQL`：title / year 都在里面），扫描器却是拿**目录名解析出来的
#   标题**去查的。只要 nfo 的标题与目录名不一样（`公主连结！ReDive` vs `Re:Dive`、
#   `战姬绝唱` vs `战姬绝唱Symphogear`…），下次要查身份时就会查不到自己 →
#   凭空再造一份（那份通常没有文件，界面上就是「集没标题、点了放不了」）。
#
# 两个触发条件（都不需要掉盘）：
#   ① 给已有剧集**加新文件**（新一集 / 特典 / 补档）→ 新文件挂到新造的重复剧集上；
#   ② 文件记录被软删（掉盘事故）后又回来 → 重复剧集留下空壳。
#
# 用法：LMBY_USER=<管理员> LMBY_PASS=<口令> bash scripts/dev/verify-scan-identity.sh
#   BASE 默认 http://127.0.0.1:8099；ROOT 默认 /tmp/lmby-scan-identity
set -u

BASE="${BASE:-http://127.0.0.1:8099}"
ROOT="${ROOT:-/tmp/lmby-scan-identity}"
LIBNAME="身份验收 $(date +%s)"
SHOW1="某番 (2020)"        # 有 nfo，且 nfo 标题与目录名不同 ← 触发 bug 的前提
SHOW2="另一部 (2021)"      # 对照组：无 nfo

pass=0; fail=0
check() { # check <名字> <期望> <实际>
  if [[ "$2" == "$3" ]]; then pass=$((pass+1)); printf 'ok   %s\n' "$1"
  else fail=$((fail+1)); printf 'FAIL %s（期望 %s，实际 %s）\n' "$1" "$2" "$3"; fi
}
check_true() { # check_true <名字> <布尔值> [补充]
  if [[ "$2" == "true" ]]; then pass=$((pass+1)); printf 'ok   %s\n' "$1"
  else fail=$((fail+1)); printf 'FAIL %s%s\n' "$1" "${3:+（$3）}"; fi
}
note() { printf '  · %s\n' "$*"; }

JAR=$(mktemp)
LIB_ID=""
cleanup() {
  [[ -n "$LIB_ID" ]] && curl -s -o /dev/null -b "$JAR" -X DELETE "$BASE/api/v1/libraries/$LIB_ID"
  rm -f "$JAR"
  rm -rf "$ROOT"
}
trap cleanup EXIT

api() { # api <method> <path> [body]
  if [[ -n "${3:-}" ]]; then
    curl -s -b "$JAR" -c "$JAR" -X "$1" "$BASE$2" -H 'Content-Type: application/json' -d "$3"
  else
    curl -s -b "$JAR" -c "$JAR" -X "$1" "$BASE$2"
  fi
}
scan() {
  curl -s -o /dev/null -b "$JAR" -c "$JAR" -X POST "$BASE/api/v1/libraries/$LIB_ID/scan" \
    -H 'Content-Type: application/json' -d '{}'
  local st=""
  for _ in $(seq 1 90); do
    st=$(api GET "/api/v1/libraries/$LIB_ID/scan")
    [[ "$(jq -r '.running' <<<"$st")" != "true" ]] && break
    sleep 1
  done
  printf '%s' "$st"
}
series_n()  { api GET "/api/v1/libraries/$LIB_ID/items?kind=series&limit=200"  | jq -r '.total // 0'; }
episode_n() { api GET "/api/v1/libraries/$LIB_ID/items?kind=episode&limit=200" | jq -r '.total // 0'; }
# 剧集标题 → 它的 id（两个剧集都有 S01E01，所以只能按标题锁定）
series_id_of() { # series_id_of <标题>
  api GET "/api/v1/libraries/$LIB_ID/browse?limit=200" \
    | jq -r --arg t "$1" '[.items[] | select(.title == $t)] | first | .id // "无"'
}
# 某个剧集下的集数（按 seriesId 数）
episodes_of() { # episodes_of <剧集 id>
  api GET "/api/v1/libraries/$LIB_ID/items?kind=episode&limit=200" \
    | jq -r --argjson sid "$1" '[.items[] | select(.seriesId == $sid)] | length'
}
mkfile() { head -c 200000 /dev/zero > "$1"; }   # > [scan] min_file_size 默认 64 KiB

echo "== 0. 管理员登录 =="
LOGIN=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -c "$JAR" -X POST "$BASE/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"${LMBY_USER:?}\",\"password\":\"${LMBY_PASS:?}\"}")
check "管理员登录" 200 "$LOGIN"
[[ "$LOGIN" != "200" ]] && exit 1

echo
echo "== 1. 造两个剧集：一个有 nfo（标题与目录名不同），一个没有 =="
rm -rf "$ROOT"
mkdir -p "$ROOT/$SHOW1/Season 1" "$ROOT/$SHOW2/Season 1"
for i in 01 02 03; do mkfile "$ROOT/$SHOW1/Season 1/S01E$i.mkv"; done
mkfile "$ROOT/$SHOW2/Season 1/S01E01.mkv"
cat > "$ROOT/$SHOW1/tvshow.nfo" <<'NFO'
<?xml version="1.0" encoding="UTF-8"?>
<tvshow>
  <title>某番 完全版</title>
  <plot>nfo 里的标题与目录名故意不一样 —— 这正是身份漂移的触发条件。</plot>
</tvshow>
NFO
note "目录名：$SHOW1 ；nfo 标题：某番 完全版"

LIB=$(api POST /api/v1/libraries "{\"name\":\"$LIBNAME\",\"kind\":\"tv\",\"paths\":[\"$ROOT\"]}")
LIB_ID=$(jq -r '.library.id // .id // empty' <<<"$LIB")
check_true "建库成功" "$([[ -n "$LIB_ID" ]] && echo true || echo false)" "$LIB"
[[ -z "$LIB_ID" ]] && { echo "建库失败，后续无法进行"; exit 1; }

echo
echo "== 2. 基线扫描 =="
ST=$(scan)
note "state=$(jq -r '.lastScan.state' <<<"$ST") series=$(series_n) episode=$(episode_n)"
check "剧集数 = 2" 2 "$(series_n)"
check "集数 = 4" 4 "$(episode_n)"
BROWSE=$(api GET "/api/v1/libraries/$LIB_ID/browse?limit=200")
check_true "nfo 的标题确实覆盖了目录名（bug 的前提成立）" \
  "$(jq -r '[.items[].title] | index("某番 完全版") != null' <<<"$BROWSE")" \
  "顶层标题：$(jq -c '[.items[].title]' <<<"$BROWSE")"
SHOW1_ID=$(series_id_of "某番 完全版")
note "「某番 完全版」的条目 id：$SHOW1_ID；它的集数：$(episodes_of "$SHOW1_ID")"
check "该剧集下有 3 集" 3 "$(episodes_of "$SHOW1_ID")"

echo
echo "== 3. 触发条件①：给这个剧集加一集，再扫 =="
SERIES_BEFORE=$(series_n); EP_BEFORE=$(episode_n)
mkfile "$ROOT/$SHOW1/Season 1/S01E04.mkv"
ST=$(scan)
note "state=$(jq -r '.lastScan.state' <<<"$ST") series=$(series_n) episode=$(episode_n) newFiles=$(jq -r '.lastScan.stats.newFiles // 0' <<<"$ST")"
check "剧集数没变（没造出重复剧集）" "$SERIES_BEFORE" "$(series_n)"
check "集数 +1" "$((EP_BEFORE + 1))" "$(episode_n)"
check "新加的 S01E04 挂在**原来那份**剧集上（该剧集变成 4 集）" 4 "$(episodes_of "$SHOW1_ID")"
check_true "本轮没有新建剧集" \
  "$(jq -r '(.lastScan.stats.seriesNew // 0) == 0' <<<"$ST")" \
  "$(jq -c '{seriesNew:.lastScan.stats.seriesNew,episodesNew:.lastScan.stats.episodesNew}' <<<"$ST")"

echo
echo "== 4. 触发条件②：文件被软删（掉盘）之后又回来 =="
SERIES_BEFORE=$(series_n); EP_BEFORE=$(episode_n)
rm -f "$ROOT/$SHOW1/Season 1/S01E02.mkv"
ST=$(scan)
check "先软删掉 1 个文件" 1 "$(jq -r '.lastScan.stats.deletedFiles // 0' <<<"$ST")"
mkfile "$ROOT/$SHOW1/Season 1/S01E02.mkv"
ST=$(scan)
note "state=$(jq -r '.lastScan.state' <<<"$ST") series=$(series_n) episode=$(episode_n) revived=$(jq -r '.lastScan.stats.revivedFiles // 0' <<<"$ST") issues=$(jq -r '.issues | length' <<<"$ST")"
check "剧集数没变（没造出重复剧集）" "$SERIES_BEFORE" "$(series_n)"
check "集数没变" "$EP_BEFORE" "$(episode_n)"
check "该剧集仍只有 4 集" 4 "$(episodes_of "$SHOW1_ID")"
check "文件被复活" 1 "$(jq -r '.lastScan.stats.revivedFiles // 0' <<<"$ST")"
check "没有问题记录" 0 "$(jq -r '.issues | length' <<<"$ST")"
check_true "没有新建剧集" \
  "$(jq -r '(.lastScan.stats.seriesNew // 0) == 0' <<<"$ST")"

echo
echo "== 5. 收尾：再扫一次应当全「未变」 =="
SERIES_BEFORE=$(series_n); EP_BEFORE=$(episode_n)
ST=$(scan)
check "剧集数没变" "$SERIES_BEFORE" "$(series_n)"
check "集数没变" "$EP_BEFORE" "$(episode_n)"
check "没有删除" 0 "$(jq -r '.lastScan.stats.deletedFiles // 0' <<<"$ST")"

echo
if [[ $fail -eq 0 ]]; then printf '条目身份验收：%d 项全过\n' "$pass"
else printf '条目身份验收：%d 过 / %d 败\n' "$pass" "$fail"; fi
[[ $fail -eq 0 ]]
