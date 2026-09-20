#!/usr/bin/env bash
# M2「搜索」的真库验收（对着跑着的服务做 HTTP 断言，脚本自选样本、跑完还原）。
#
# 验的是四件事：
#   1. 中文二元组命中：「炼金」能搜到《钢之炼金术师…》；
#   2. **错字容忍**：标题里改掉一个字，仍然能搜到它（trigram 那一路）；
#   3. 单字兜底：一个字也要有结果（bigram 索引里查不到单字，靠 ILIKE）；
#   4. **索引自动同步**：改完标题立刻能搜到新标题（生成列的价值）。
#
# 样本是脚本自己挑的：**最长的一段连续中文**（≥6 字）—— 用它派生的查询词才有辨识度，
# 否则「V」这种词会命中上百条，断言就成了「有没有被翻页挤出去」而不是「搜不搜得到」。
#
# 用法：
#   LMBY_USER=devtest LMBY_PASS=口令 bash scripts/dev/verify-search.sh
# 可选：BASE（默认 http://127.0.0.1:8099）、LIB_ID
set -uo pipefail

BASE="${BASE:-http://127.0.0.1:8099}"
USER="${LMBY_USER:-}"
PASS="${LMBY_PASS:-}"

if [[ -z "$USER" || -z "$PASS" ]]; then
  echo "用法：LMBY_USER=devtest LMBY_PASS=口令 bash scripts/dev/verify-search.sh"
  exit 2
fi

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

# 中文查询词一律用 --data-urlencode，免得手写百分号编码
search()  { curl -s -G -b "$JAR" --data-urlencode "q=$1" "${@:2}" "$BASE/api/v1/search"; }
# 断言用：把一页拉满，免得命中太多时样本被翻页挤出去
searchq() { curl -s -G -b "$JAR" --data-urlencode "q=$1" --data-urlencode "limit=100" "${@:2}" "$BASE/api/v1/search"; }

has_id() { jq -r --argjson id "$2" 'any(.items[]; .id == $id)' <<<"$1"; }

echo "目标：$BASE"

echo
echo "== 1. 登录 =="
code=$(st -X POST "$BASE/api/v1/auth/login" -c "$JAR" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg u "$USER" --arg p "$PASS" '{username:$u,password:$p}')")
check "登录 $USER = 200" 200 "$code"
[[ "$code" != "200" ]] && { echo "登录失败，后面的断言没有意义"; exit 1; }

echo
echo "== 2. 采样：从真库里挑一条中文标题（取最长连续中文段）=="
libs=$(bd -b "$JAR" "$BASE/api/v1/libraries")
LIB_ID="${LIB_ID:-$(jq -r '.libraries[0].id // empty' <<<"$libs")}"
[[ -z "$LIB_ID" ]] && { echo "没有媒体库"; exit 1; }

items=$(bd -b "$JAR" "$BASE/api/v1/libraries/$LIB_ID/items?kind=movie&limit=200")
# 元数据来自 nfo：收尾重扫能把「元数据来源」拉回原样
pick=$(jq -r '[.items[]
    | select(.metadataSource == "nfo")
    | . + {run: ([.title | scan("[一-龥]{6,}")] | if length > 0 then max_by(length) else null end)}
    | select(.run != null)]
  | .[0] | [.id, .title, .run] | @tsv' <<<"$items")
if [[ -z "$pick" ]]; then
  echo "库里没有带 ≥6 字连续中文的电影标题"
  exit 1
fi
ITEM_ID=$(cut -f1 <<<"$pick")
TITLE=$(cut -f2 <<<"$pick")
RUN=$(cut -f3 <<<"$pick")
note "样本：条目 $ITEM_ID《$TITLE》"
note "取中文段：「$RUN」"

# 用 jq 切中文字符（按码点，不受 locale 影响）：
#   MID  = 连续两个字（应当走二元组索引命中）
#   ONE  = 一个字（索引里没有单字单元，靠 ILIKE 兜底）
#   TYPO = 把段里第 3 个字换掉（应当靠 trigram 相似度命中）
MID=$(jq -rn --arg t "$RUN" '$t[1:3]')
ONE=$(jq -rn --arg t "$RUN" '$t[1:2]')
TYPO=$(jq -rn --arg t "$RUN" '($t[0:2] + "土" + $t[3:])')
note "查询词：中段「$MID」 单字「$ONE」 错字「$TYPO」"

echo
echo "== 3. 命中与排序 =="
r=$(search "$TITLE")
check "整标题搜索 = 首条就是它" "$ITEM_ID" "$(jq -r '.items[0].id' <<<"$r")"
check "整标题搜索：total ≥ 1" true "$(jq -r '.total >= 1' <<<"$r")"
check "返回带排序依据（rank/similarity）" true \
  "$(jq -r '.items[0] | has("rank") and has("similarity")' <<<"$r")"

r=$(searchq "$MID")
check "中文中段「$MID」能命中（二元组）" true "$(has_id "$r" "$ITEM_ID")"

r=$(searchq "$TYPO")
check "错字「$TYPO」仍能命中（trigram 容忍）" true "$(has_id "$r" "$ITEM_ID")"

r=$(searchq "$ONE")
check "单字「$ONE」能命中（ILIKE 兜底）" true "$(has_id "$r" "$ITEM_ID")"

r=$(search "$TITLE" --data-urlencode "limit=1")
check "limit=1 时只回 1 条" 1 "$(jq -r '.items | length' <<<"$r")"

r1=$(search "$MID" --data-urlencode "limit=1")
r2=$(search "$MID" --data-urlencode "limit=1" --data-urlencode "offset=1")
a=$(jq -r '.items[0].id // empty' <<<"$r1")
b=$(jq -r '.items[0].id // empty' <<<"$r2")
check "offset 生效（第 2 条不同于第 1 条）" true \
  "$([[ -z "$b" || "$b" != "$a" ]] && echo true || echo false)"

echo
echo "== 4. 过滤 =="
r=$(searchq "$MID" --data-urlencode "libraryId=$LIB_ID")
check "库过滤：结果都在该库" true "$(jq -r "all(.items[]; .libraryId == $LIB_ID)" <<<"$r")"

r=$(searchq "$MID" --data-urlencode "kind=movie")
check "类型过滤：结果都是电影" true "$(jq -r 'all(.items[]; .kind == "movie")' <<<"$r")"

r=$(searchq "$MID" --data-urlencode "libraryId=999999")
check "不存在的库 = 0 条（不是 500）" 0 "$(jq -r '.total' <<<"$r")"

echo
echo "== 5. 参数校验与鉴权 =="
check "缺 q = 400" 400 "$(st -b "$JAR" "$BASE/api/v1/search")"
check "空 q = 400" 400 "$(st -b "$JAR" "$BASE/api/v1/search?q=")"
check "空白 q = 400" 400 "$(st -b "$JAR" --get --data-urlencode 'q=   ' "$BASE/api/v1/search")"
check "非法 kind = 400" 400 "$(st -b "$JAR" "$BASE/api/v1/search?q=x&kind=album")"
check "非法 libraryId = 400" 400 "$(st -b "$JAR" "$BASE/api/v1/search?q=x&libraryId=abc")"
check "未登录 = 401" 401 "$(st "$BASE/api/v1/search?q=x")"
check "未登录（中文查询词）= 401" 401 "$(st --get --data-urlencode "q=$MID" "$BASE/api/v1/search")"
check "超限 limit 被夹到 100" 100 \
  "$(jq -r '.limit' <<<"$(searchq "$MID" --data-urlencode 'limit=9999')")"

echo
echo "== 6. 索引自动同步（改完标题立刻生效）=="
before=$(bd -b "$JAR" "$BASE/api/v1/items/$ITEM_ID")
ORIG_TITLE_ALT=$(jq -r '.item.originalTitle // ""' <<<"$before")
NEW_TITLE="LMBY 检索验收 $ITEM_ID"
resp=$(json -b "$JAR" -X PATCH "$BASE/api/v1/items/$ITEM_ID" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg t "$NEW_TITLE" '{fields:{title:$t}}')")
check "改标题 = 200" "$NEW_TITLE" "$(jq -r '.item.title' <<<"$resp")"

r=$(searchq "$NEW_TITLE")
check "新标题立刻能搜到" true "$(has_id "$r" "$ITEM_ID")"

if [[ "$ORIG_TITLE_ALT" == *"$MID"* ]]; then
  note "该条目有 original_title（含「$MID」），旧片段仍会命中它，跳过「搜不到旧标题」断言"
else
  r=$(searchq "$MID")
  check "旧标题片段搜不到它了" false "$(has_id "$r" "$ITEM_ID")"
fi

echo
echo "== 7. 还原 =="
json -b "$JAR" -X PATCH "$BASE/api/v1/items/$ITEM_ID" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg t "$TITLE" '{fields:{title:$t}}')" >/dev/null
# 人工编辑会把元数据来源标成 manual，重扫一次把它拉回 nfo（与 verify-item-edit.sh 同样的收尾）
st -b "$JAR" -X POST "$BASE/api/v1/libraries/$LIB_ID/scan" \
  -H 'Content-Type: application/json' -d '{"refreshMetadata":true}' >/dev/null
for _ in $(seq 1 120); do
  [[ "$(bd -b "$JAR" "$BASE/api/v1/libraries/$LIB_ID/scan" | jq -r '.running')" == "false" ]] && break
  sleep 2
done
after=$(bd -b "$JAR" "$BASE/api/v1/items/$ITEM_ID")
check "还原：标题" "$TITLE" "$(jq -r '.item.title' <<<"$after")"
check "还原：元数据来源" nfo "$(jq -r '.item.metadataSource // ""' <<<"$after")"
r=$(searchq "$MID")
check "还原后中文片段重新能搜到" true "$(has_id "$r" "$ITEM_ID")"

echo
printf '\033[1m结果：%d 通过，%d 失败\033[0m\n' "$pass" "$fail"
[[ "$fail" -eq 0 ]]
