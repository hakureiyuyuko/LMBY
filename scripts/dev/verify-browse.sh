#!/usr/bin/env bash
# M2「海报墙 / 剧集视图 / 批量操作」的真库验收（对着跑着的服务做 HTTP 断言）。
#
# 验三件事：
#   1. 海报墙只给**顶层**条目（电影/剧集），排序与分页/过滤都生效；
#   2. 子项层级对得上：剧集 → 季（带集数）→ 集，且 parent_id 正确；
#   3. 批量操作真的改了状态，并且跑完能还原。
#
# 用法：
#   LMBY_USER=devtest LMBY_PASS=口令 bash scripts/dev/verify-browse.sh
# 可选：BASE、LIB_ID
set -uo pipefail

BASE="${BASE:-http://127.0.0.1:8099}"
USER="${LMBY_USER:-}"
PASS="${LMBY_PASS:-}"
if [[ -z "$USER" || -z "$PASS" ]]; then
  echo "用法：LMBY_USER=devtest LMBY_PASS=口令 bash scripts/dev/verify-browse.sh"
  exit 2
fi

export PGCLIENTENCODING="${PGCLIENTENCODING:-UTF8}"
PGPASSWORD_FILE="${PGPASSWORD_FILE:-/etc/lmby/pg-password}"

JAR=$(mktemp)
trap 'rm -f "$JAR"' EXIT

pass=0; fail=0
ok()    { printf '  \033[32mok\033[0m   %s\n' "$1"; pass=$((pass+1)); }
bad()   { printf '  \033[31mFAIL\033[0m %s\n        期望 %s\n        实际 %s\n' "$1" "$2" "$3"; fail=$((fail+1)); }
check() { if [[ "$2" == "$3" ]]; then ok "$1"; else bad "$1" "$2" "$3"; fi; }
note()  { printf '  \033[33m--\033[0m   %s\n' "$1"; }

st()   { curl -s -o /dev/null -w '%{http_code}' "$@"; }
bd()   { curl -s "$@"; }
json() { curl -s -H 'Content-Type: application/json' "$@"; }

echo "目标：$BASE"

echo
echo "== 1. 登录 =="
code=$(st -X POST "$BASE/api/v1/auth/login" -c "$JAR" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg u "$USER" --arg p "$PASS" '{username:$u,password:$p}')")
check "登录 $USER = 200" 200 "$code"
[[ "$code" != "200" ]] && exit 1

libs=$(bd -b "$JAR" "$BASE/api/v1/libraries")
LIB_ID="${LIB_ID:-$(jq -r '.libraries[0].id' <<<"$libs")}"
COUNTS=$(jq -c --argjson i "$LIB_ID" '.libraries[]|select(.id==$i)|.counts' <<<"$libs")
note "媒体库 id=$LIB_ID 计数=$COUNTS"

echo
echo "== 2. 海报墙：只要顶层条目 =="
b=$(bd -b "$JAR" "$BASE/api/v1/libraries/$LIB_ID/browse?limit=200")
check "browse = 200" 200 "$(st -b "$JAR" "$BASE/api/v1/libraries/$LIB_ID/browse")"
check "结果里没有 season" 0 "$(jq -r '[.items[]|select(.kind=="season")]|length' <<<"$b")"
check "结果里没有 episode" 0 "$(jq -r '[.items[]|select(.kind=="episode")]|length' <<<"$b")"
check "结果里没有 extra" 0 "$(jq -r '[.items[]|select(.kind=="extra")]|length' <<<"$b")"
check "总数 = 电影 + 剧集" \
  "$(jq -r '(.movie // 0) + (.series // 0)' <<<"$COUNTS")" "$(jq -r '.total' <<<"$b")"
check "返回条数 = min(total, limit)" "$(jq -r '[.total, .limit]|min' <<<"$b")" "$(jq -r '.items|length' <<<"$b")"

r=$(bd -b "$JAR" "$BASE/api/v1/libraries/$LIB_ID/browse?kind=movie&limit=200")
check "kind=movie：全是电影" true "$(jq -r 'all(.items[]; .kind == "movie")' <<<"$r")"
check "kind=movie 计数 = 库统计" "$(jq -r '.movie // 0' <<<"$COUNTS")" "$(jq -r '.total' <<<"$r")"

r=$(bd -b "$JAR" "$BASE/api/v1/libraries/$LIB_ID/browse?kind=series&limit=200")
check "kind=series：全是剧集" true "$(jq -r 'all(.items[]; .kind == "series")' <<<"$r")"
check "kind=series 计数 = 库统计" "$(jq -r '.series // 0' <<<"$COUNTS")" "$(jq -r '.total' <<<"$r")"

echo
echo "== 3. 排序与分页 =="
r=$(bd -b "$JAR" "$BASE/api/v1/libraries/$LIB_ID/browse?sort=title&limit=100")
check "sort=title：sortTitle 有序" true "$(jq -r '[.items[].sortTitle] as $a | ($a == ($a|sort))' <<<"$r")"
r=$(bd -b "$JAR" "$BASE/api/v1/libraries/$LIB_ID/browse?sort=year&limit=100")
check "sort=year：年份单调不增（跳过空年份）" true \
  "$(jq -r '[.items[].year // empty] as $y | ($y == ($y|sort|reverse))' <<<"$r")"
r1=$(bd -b "$JAR" "$BASE/api/v1/libraries/$LIB_ID/browse?limit=1&offset=0")
r2=$(bd -b "$JAR" "$BASE/api/v1/libraries/$LIB_ID/browse?limit=1&offset=1")
check "limit=1 只回 1 条" 1 "$(jq -r '.items|length' <<<"$r1")"
check "offset 生效" true \
  "$(jq -r --argjson a "$(jq -r '.items[0].id' <<<"$r1")" '.items[0].id != $a' <<<"$r2")"

echo
echo "== 4. 子项层级（剧集 → 季 → 集）=="
SERIES_ID=$(bd -b "$JAR" "$BASE/api/v1/libraries/$LIB_ID/browse?kind=series&limit=1" | jq -r '.items[0].id // empty')
if [[ -z "$SERIES_ID" ]]; then
  note "库里没有剧集，跳过层级断言"
else
  c=$(bd -b "$JAR" "$BASE/api/v1/items/$SERIES_ID/children")
  note "剧集 $SERIES_ID 的子项 $(jq -r '.items|length' <<<"$c") 个"
  check "子项的 parentId 都指向该剧集" true \
    "$(jq -r --argjson p "$SERIES_ID" 'all(.items[]; .parentId == $p)' <<<"$c")"
  check "子项类型是 season 或 episode" true \
    "$(jq -r 'all(.items[]; .kind == "season" or .kind == "episode")' <<<"$c")"
  check "counts 的键与子项 id 对应" true \
    "$(jq -r '([.items[].id|tostring]|sort) == (.counts|keys|sort)' <<<"$c")"

  SEASON_ID=$(jq -r '[.items[]|select(.kind=="season")][0].id // empty' <<<"$c")
  if [[ -n "$SEASON_ID" ]]; then
    want=$(jq -r --argjson s "$SEASON_ID" '.counts[$s|tostring]' <<<"$c")
    c2=$(bd -b "$JAR" "$BASE/api/v1/items/$SEASON_ID/children")
    check "季的 counts 与它的集数一致" "$want" "$(jq -r '.items|length' <<<"$c2")"
    check "集的 parentId 指向该季" true \
      "$(jq -r --argjson p "$SEASON_ID" 'all(.items[]; .parentId == $p)' <<<"$c2")"
    check "集都带集号" true "$(jq -r 'all(.items[]; .episodeNumber != null)' <<<"$c2")"
    note "季 $SEASON_ID 有 $(jq -r '.items|length' <<<"$c2") 集"
  else
    note "这部剧没有季（集直接挂在剧集下）"
  fi
fi

MOVIE_ID=$(bd -b "$JAR" "$BASE/api/v1/libraries/$LIB_ID/browse?kind=movie&limit=1" | jq -r '.items[0].id // empty')
if [[ -n "$MOVIE_ID" ]]; then
  c=$(bd -b "$JAR" "$BASE/api/v1/items/$MOVIE_ID/children")
  check "电影的子项是空数组（不是 500/null）" 0 "$(jq -r '.items|length' <<<"$c")"
fi

echo
echo "== 5. 参数校验 =="
check "kind=album = 400" 400 "$(st -b "$JAR" "$BASE/api/v1/libraries/$LIB_ID/browse?kind=album")"
check "sort=random = 400" 400 "$(st -b "$JAR" "$BASE/api/v1/libraries/$LIB_ID/browse?sort=random")"
check "不存在的库 = 404" 404 "$(st -b "$JAR" "$BASE/api/v1/libraries/999999999/browse")"
check "未登录 browse = 401" 401 "$(st "$BASE/api/v1/libraries/$LIB_ID/browse")"
check "未登录 children = 401" 401 "$(st "$BASE/api/v1/items/1/children")"
check "不存在的条目 children = 404" 404 "$(st -b "$JAR" "$BASE/api/v1/items/999999999/children")"
check "非法条目 id children = 400" 400 "$(st -b "$JAR" "$BASE/api/v1/items/abc/children")"

echo
echo "== 6. 批量操作（跑完还原）=="
# 挑一条 nfo 状态的条目：批量标记后可以用一次重扫还原
items=$(bd -b "$JAR" "$BASE/api/v1/libraries/$LIB_ID/items?kind=movie&limit=200")
PICK=$(jq -r '[.items[]|select(.metadataSource=="nfo")][0].id // empty' <<<"$items")
if [[ -z "$PICK" ]]; then
  note "没有 nfo 条目可用来测批量标记，跳过"
else
  before=$(bd -b "$JAR" "$BASE/api/v1/items/$PICK")
  check "批量标记前的状态" nfo "$(jq -r '.item.matchState' <<<"$before")"

  r=$(json -b "$JAR" -X POST "$BASE/api/v1/items/batch" -H 'Content-Type: application/json' \
    -d "$(jq -nc --argjson id "$PICK" '{action:"skip", itemIds:[$id], reason:"验收脚本批量标记"}')")
  check "批量标记 = applied 1" 1 "$(jq -r '.applied' <<<"$r")"
  after=$(bd -b "$JAR" "$BASE/api/v1/items/$PICK")
  check "状态已变 manual" manual "$(jq -r '.item.matchState' <<<"$after")"
  check "原因写进了 scrape_error" true \
    "$(jq -r '.item.scrapeError|test("验收脚本批量标记")' <<<"$after")"

  # 批量入队刮削（非 force）：nfo 条目会被处理器跳过，状态不该变
  r=$(json -b "$JAR" -X POST "$BASE/api/v1/items/batch" -H 'Content-Type: application/json' \
    -d "$(jq -nc --argjson id "$PICK" '{action:"scrape", itemIds:[$id], force:false}')")
  check "批量入队 = applied ≥ 1" true "$(jq -r '.applied >= 1' <<<"$r")"
  for _ in $(seq 1 40); do
    q=$(bd -b "$JAR" "$BASE/api/v1/tasks" | jq -r '(.stats.pending // 0) + (.stats.running // 0)')
    [[ "$q" == "0" ]] && break
    sleep 1
  done
  after2=$(bd -b "$JAR" "$BASE/api/v1/items/$PICK")
  check "队列跑完状态仍是 manual（处理器不动人工结果）" manual "$(jq -r '.item.matchState' <<<"$after2")"

  # 参数校验
  check "action 非法 = 400" 400 \
    "$(st -b "$JAR" -X POST "$BASE/api/v1/items/batch" -H 'Content-Type: application/json' \
        -d "$(jq -nc --argjson id "$PICK" '{action:"delete", itemIds:[$id]}')")"
  check "itemIds 空 = 400" 400 \
    "$(st -b "$JAR" -X POST "$BASE/api/v1/items/batch" -H 'Content-Type: application/json' \
        -d '{"action":"skip","itemIds":[]}')"
  check "未登录批量 = 401" 401 \
    "$(st -X POST "$BASE/api/v1/items/batch" -H 'Content-Type: application/json' -d '{"action":"skip","itemIds":[1]}')"

  echo
  echo "== 7. 还原（重扫把状态拉回 nfo）=="
  st -b "$JAR" -X POST "$BASE/api/v1/libraries/$LIB_ID/scan" \
    -H 'Content-Type: application/json' -d '{"refreshMetadata":true}' >/dev/null
  for _ in $(seq 1 120); do
    [[ "$(bd -b "$JAR" "$BASE/api/v1/libraries/$LIB_ID/scan" | jq -r '.running')" == "false" ]] && break
    sleep 2
  done
  restored=$(bd -b "$JAR" "$BASE/api/v1/items/$PICK")
  check "还原：状态" "$(jq -r '.item.matchState' <<<"$before")" "$(jq -r '.item.matchState' <<<"$restored")"
  check "还原：元数据来源" "$(jq -r '.item.metadataSource' <<<"$before")" "$(jq -r '.item.metadataSource' <<<"$restored")"
fi

echo
printf '\033[1m结果：%d 通过，%d 失败\033[0m\n' "$pass" "$fail"
[[ "$fail" -eq 0 ]]
