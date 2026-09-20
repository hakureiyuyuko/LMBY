#!/usr/bin/env bash
# 拿真实媒体库里的条目实盘跑一遍匹配打分器（对着真 TMDB），统计自动匹配率。
#
# 这是 M2「自动匹配准确率 ≥ 90%」这条 DoD 的验收工具：
# 样本直接从 media_items 里随机抽，人工只需扫一眼榜首对不对。
#
# 用法（容器内）：
#   bash scripts/dev/match-sample.sh              # 2 部剧集 + 10 部随机电影
#   bash scripts/dev/match-sample.sh 20           # 电影抽样数
#   DEEP=1 bash scripts/dev/match-sample.sh 3     # 额外对每条候选取详情（慢，但能命中别名）
#
# 前置：有一个可用的 lmby 二进制 —— 优先刚编译的 /tmp/lmby.new，
# 拿不到就用已部署的 /usr/local/bin/lmby（部署脚本会把它 mv 走，别忘了这点）。
set -o pipefail

MOVIES=${1:-10}
DEEP=${DEEP:-0}
DB=${DB:-lmby}

BIN=${BIN:-}
if [ -z "$BIN" ]; then
  if [ -x /tmp/lmby.new ]; then BIN=/tmp/lmby.new; else BIN=/usr/local/bin/lmby; fi
fi
if [ ! -x "$BIN" ]; then
  echo "找不到可执行的 lmby（BIN=$BIN）：先跑 scripts/dev/container-verify.sh"
  exit 2
fi
echo "用二进制：$BIN"

export PGPASSWORD=$(cat /etc/lmby/pg-password)
PSQL="/usr/bin/psql -h 127.0.0.1 -U lmby -d $DB -tA -F|"
SUMMARY=/tmp/match-summary.txt
: > "$SUMMARY"

EXTRA=""
[ "$DEEP" = "1" ] && EXTRA="--deep"

run_one() {
  kind=$1; title=$2; year=$3
  if [ -n "$year" ] && [ "$year" != "0" ]; then
    out=$("$BIN" match --kind "$kind" --title "$title" --year "$year" --top 3 $EXTRA 2>/dev/null)
  else
    out=$("$BIN" match --kind "$kind" --title "$title" --top 3 $EXTRA 2>/dev/null)
  fi
  # 第 2 行是榜首（第 1 行是「本地：…」表头），前面的箭头只是个标记，去掉
  top=$(printf '%s\n' "$out" | sed -n '2p' | sed 's/^→ //')
  if [ -z "$top" ]; then
    printf '%s (%s)\t无候选\n' "$title" "$year" >> "$SUMMARY"
  else
    printf '%s (%s)\t%s\n' "$title" "$year" "$top" >> "$SUMMARY"
  fi
}

echo "======= 剧集（全部）======="
$PSQL -c "select title, coalesce(year,0) from media_items
          where kind='series' and deleted_at is null order by title" > /tmp/match-series.txt
while IFS='|' read -r title year; do
  [ -z "$title" ] && continue
  echo "-- $title ($year)"
  run_one tv "$title" "$year"
done < /tmp/match-series.txt

echo
echo "======= 随机 $MOVIES 部电影 ======="
$PSQL -c "select title, coalesce(year,0) from media_items
          where kind='movie' and deleted_at is null and year is not null
          order by random() limit $MOVIES" > /tmp/match-movies.txt
while IFS='|' read -r title year; do
  [ -z "$title" ] && continue
  run_one movie "$title" "$year"
done < /tmp/match-movies.txt

echo
echo "======= 汇总（榜首 + 判定）======="
cat "$SUMMARY"
echo
echo "------ 判定分布 ------"
for d in auto review reject 无候选; do
  printf '%s: %s\n' "$d" "$(grep -c "$d" "$SUMMARY" || true)"
done
printf '样本总数: %s\n' "$(grep -c . "$SUMMARY" || true)"
