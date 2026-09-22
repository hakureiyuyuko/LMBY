#!/usr/bin/env bash
# M2「搜索」的真库验收（对着跑着的服务做 HTTP 断言，脚本自选样本、跑完还原）。
#
# 验的是四件事：
#   1. 中文二元组命中：「炼金」能搜到《钢之炼金术师…》；
#   2. **错字容忍**：标题里改掉一个字，仍然能搜到它（trigram 那一路）；
#   3. 单字兜底：一个字也要有结果（bigram 索引里查不到单字，靠 ILIKE）；
#   4. **索引自动同步**：改完标题立刻能搜到新标题（生成列的价值）。
#
# M6 又补上「即时联想 + 结果分面」四件事（四个各自独立的接口）：
#   5. 演职员也能搜：人名走同一套二元组/错字容忍，并且能当筛选条件（只看这个人的作品）；
#   6. 联想：同一排序（第一条 == 搜索结果第一条）、字段是轻量的、人名也在里面；
#   7. 分面：数字与列表对得上，并且**每一维统计时关掉自己那一维**
#      （不变量：sum(kind) == total、sum(library) == total；关掉某一维后它的分面
#      等于「同一查询去掉该筛选」的全集；分面数字 == 按那个值真搜到的条数）。
#   8. 参数校验与鉴权：一条也没漏。
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
# M6 的三个新接口（一律用 --data-urlencode，中文不用手写百分号编码）
facets()  { curl -s -G -b "$JAR" --data-urlencode "q=$1" "${@:2}" "$BASE/api/v1/search/facets"; }
people()  { curl -s -G -b "$JAR" --data-urlencode "q=$1" "${@:2}" "$BASE/api/v1/search/people"; }
suggest() { curl -s -G -b "$JAR" --data-urlencode "q=$1" "${@:2}" "$BASE/api/v1/search/suggest"; }

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
echo "== 5. 演职员（人名搜索 / 按人筛选作品）=="
# 样本：从条目接口里读真演职员（不链网、不与库表比）。逐条问太慢，只问前 40 条；
# 而且专挑「名字 ≥ 4 字」的 —— 名字太短就派生不出中段与错字两种查询词。
PERSON_PICK=""
ITEM_PERSON=""
for id in $(jq -r '.items[:40][] | .id' <<<"$items"); do
  pp=$(bd -b "$JAR" "$BASE/api/v1/items/$id/people")
  PERSON_PICK=$(jq -r '[.people[] | select((.name | length) >= 4)][0]
    | select(. != null) | [.personId, .name] | @tsv' <<<"$pp")
  if [[ -n "$PERSON_PICK" ]]; then ITEM_PERSON="$id"; break; fi
done

if [[ -z "$PERSON_PICK" ]]; then
  note "库里没有 ≥ 4 字人名的演职员样本，跳过第 5 / 6 节的人相关断言（先重扫一次 nfo）"
else
  PERSON_ID=$(cut -f1 <<<"$PERSON_PICK")
  PERSON_NAME=$(cut -f2 <<<"$PERSON_PICK")
  P_MID=$(jq -rn --arg t "$PERSON_NAME" '$t[1:3]')
  P_TYPO=$(jq -rn --arg t "$PERSON_NAME" '($t[0:2] + "土" + $t[3:])')
  note "样本：条目 $ITEM_PERSON 的演职员 $PERSON_ID「$PERSON_NAME」"
  note "人名查询词：中段「$P_MID」 错字「$P_TYPO」"

  r=$(people "$PERSON_NAME")
  check "人名全名能搜到（人这一档）" true \
    "$(jq -r --argjson id "$PERSON_ID" 'any(.people[]; .id == $id)' <<<"$r")"
  check "人这一档带 roles / works" true \
    "$(jq -r --argjson id "$PERSON_ID" '(.people[] | select(.id == $id)) | (.roles | type == "array") and (.works >= 1)' <<<"$r")"
  check "人这一档带排序依据（rank/similarity）" true \
    "$(jq -r 'all(.people[]; has("rank") and has("similarity"))' <<<"$r")"

  r=$(people "$P_MID")
  check "人名中段「$P_MID」能命中（二元组）" true \
    "$(jq -r --argjson id "$PERSON_ID" 'any(.people[]; .id == $id)' <<<"$r")"

  r=$(people "$P_TYPO")
  check "人名错字「$P_TYPO」仍能命中（trigram 容忍）" true \
    "$(jq -r --argjson id "$PERSON_ID" 'any(.people[]; .id == $id)' <<<"$r")"

  # 按人筛作品：只有 personId、没有查询词（这就是「点人名看作品」发的那条请求）
  r=$(curl -s -G -b "$JAR" --data-urlencode "q=" --data-urlencode "personId=$PERSON_ID" \
      --data-urlencode "limit=100" "$BASE/api/v1/search")
  check "只有 personId（无查询词）= 有结果" true "$(jq -r '.total >= 1' <<<"$r")"
  check "按人筛：原条目在结果里" true \
    "$(jq -r --argjson id "$ITEM_PERSON" 'any(.items[]; .id == $id)' <<<"$r")"
  # 抽前 3 条逐个回查演职员表：确认筛出来的人真的有这个人（不是「同名就算」）
  in_person=0; sampled=0
  for id in $(jq -r '.items[:3][].id' <<<"$r"); do
    sampled=$((sampled+1))
    pp=$(bd -b "$JAR" "$BASE/api/v1/items/$id/people")
    if [[ "$(jq -r --argjson pid "$PERSON_ID" 'any(.people[]; .personId == $pid)' <<<"$pp")" == "true" ]]; then
      in_person=$((in_person+1))
    fi
  done
  check "按人筛：抽查前 $sampled 条都确实有这个人" "$sampled" "$in_person"

  ff=$(curl -s -G -b "$JAR" --data-urlencode "q=" --data-urlencode "personId=$PERSON_ID" \
       "$BASE/api/v1/search/facets")
  check "按人筛：分面 total 与列表一致" "$(jq -r '.total' <<<"$r")" "$(jq -r '.facets.total' <<<"$ff")"
  check "按人筛：类型分面也能切（sum(kind) == total）" "$(jq -r '.facets.total' <<<"$ff")" \
    "$(jq -r '[.facets.kind[].count] | add // 0' <<<"$ff")"
fi

echo
echo "== 6. 即时联想 =="
s=$(suggest "$MID")
check "联想：作品组非空" true "$(jq -r '.items | length > 0' <<<"$s")"
check "联想第一条 == 搜索结果第一条（同一排序）" \
  "$(jq -r '.items[0].id' <<<"$(searchq "$MID")")" "$(jq -r '.items[0].id' <<<"$s")"
check "联想项是轻量字段（不带 overview / genres / providerIds）" true \
  "$(jq -r '.items[0] | (has("overview") or has("genres") or has("providerIds")) | not' <<<"$s")"
check "联想：limit 超限被夹到 20" true \
  "$(jq -r '.items | length <= 20' <<<"$(suggest "$MID" --data-urlencode limit=9999)")"
check "联想：错字也能命中同一条" true \
  "$(jq -r --argjson id "$ITEM_ID" 'any(.items[]; .id == $id)' <<<"$(suggest "$TYPO" --data-urlencode limit=20)")"
if [[ -n "$PERSON_PICK" ]]; then
  s=$(suggest "$PERSON_NAME")
  check "联想：人名出现在演职员组" true \
    "$(jq -r --argjson id "$PERSON_ID" 'any(.people[]; .id == $id)' <<<"$s")"
  check "联想：人这一组带 roles / works" true \
    "$(jq -r 'all(.people[]; (.roles | type == "array") and has("works"))' <<<"$s")"
fi

echo
echo "== 7. 结果分面 =="
f=$(facets "$MID")
check "分面：带 facets 对象" true "$(jq -r '.facets != null' <<<"$f")"
check "分面 total == 列表 total" "$(jq -r '.total' <<<"$(searchq "$MID")")" "$(jq -r '.facets.total' <<<"$f")"
check "sum(类型分面) == total（每个条目恰好一个类型）" "$(jq -r '.facets.total' <<<"$f")" \
  "$(jq -r '[.facets.kind[].count] | add // 0' <<<"$f")"
check "sum(库分面) == total（每个条目恰好属于一个库）" "$(jq -r '.facets.total' <<<"$f")" \
  "$(jq -r '[.facets.library[].count] | add // 0' <<<"$f")"
check "流派分面每格都不超过 total（没有流派的作品不贡献格子，所以和可以小于 total）" true \
  "$(jq -r '(.facets.total) as $t | all(.facets.genre[]; .count <= $t)' <<<"$f")"
check "类型分面只含合法类型" true \
  "$(jq -r 'all(.facets.kind[]; .value | test("^(movie|series|season|episode|extra)$"))' <<<"$f")"
check "分面 people == /search/people 的 total" "$(jq -r '.total' <<<"$(people "$MID")")" \
  "$(jq -r '.facets.people' <<<"$f")"

# ★ 最有价值的一组：分面上的数字必须就是「点一下真能搜到的条数」，
# 否则那个数字就只是装饰（分面与列表共用同一组筛选、同一段条件串，不该对不上）。
KG_FACET=$(jq -r '.facets.kind[0].value // empty' <<<"$f")
if [[ -n "$KG_FACET" ]]; then
  check "类型分面「$KG_FACET」的数字 == 按该类型搜到的条数" \
    "$(jq -r '.facets.kind[0].count' <<<"$f")" \
    "$(jq -r '.total' <<<"$(searchq "$MID" --data-urlencode "kind=$KG_FACET")")"
fi
LIB_FACET=$(jq -r '.facets.library[0].value // empty' <<<"$f")
if [[ -n "$LIB_FACET" ]]; then
  check "库分面「$LIB_FACET」的数字 == 按该库搜到的条数" \
    "$(jq -r '.facets.library[0].count' <<<"$f")" \
    "$(jq -r '.total' <<<"$(searchq "$MID" --data-urlencode "libraryId=$LIB_FACET")")"
fi
G_FACET=$(jq -r '.facets.genre[0].value // empty' <<<"$f")
if [[ -n "$G_FACET" ]]; then
  check "流派分面「$G_FACET」的数字 == 按该流派搜到的条数" \
    "$(jq -r '.facets.genre[0].count' <<<"$f")" \
    "$(jq -r '.total' <<<"$(searchq "$MID" --data-urlencode "genre=$G_FACET")")"
else
  note "匹配到的条目都没有流派，跳过「流派分面数字」的断言"
fi

# 分面的一致性原则：在「类型=电影」下统计类型分面时，把自己那一维关掉，
# 于是别的类型还在（能切过去），而 movie 那一格就等于 total。
fk=$(facets "$MID" --data-urlencode "kind=movie")
check "kind=movie：分面 total == 列表 total" \
  "$(jq -r '.total' <<<"$(searchq "$MID" --data-urlencode kind=movie)")" "$(jq -r '.facets.total' <<<"$fk")"
check "kind=movie：movie 那一格 == total" "$(jq -r '.facets.total' <<<"$fk")" \
  "$(jq -r '.facets.kind[] | select(.value == "movie") | .count' <<<"$fk")"
check "kind=movie：类型分面统计的是「去掉类型筛选」的全集（别的类型才能被点）" \
  "$(jq -r '.total' <<<"$(searchq "$MID")")" "$(jq -r '[.facets.kind[].count] | add // 0' <<<"$fk")"
if [[ "$(jq -r '.facets.kind | length' <<<"$f")" -gt 1 ]]; then
  check "kind=movie：别的类型仍在分面里（切得过去）" true \
    "$(jq -r '([.facets.kind[] | select(.value != "movie")] | length) > 0' <<<"$fk")"
fi

# 库分面同理
LIB_FACET=$(jq -r '.facets.library[0].value // empty' <<<"$f")
if [[ -n "$LIB_FACET" ]]; then
  rl=$(searchq "$MID" --data-urlencode "libraryId=$LIB_FACET")
  check "库筛选：结果都在该库" true "$(jq -r "all(.items[]; .libraryId == $LIB_FACET)" <<<"$rl")"
  fl=$(facets "$MID" --data-urlencode "libraryId=$LIB_FACET")
  check "库筛选：facets.total == 列表 total" "$(jq -r '.total' <<<"$rl")" "$(jq -r '.facets.total' <<<"$fl")"
  check "库筛选：库分面统计的是「去掉库筛选」的全集" \
    "$(jq -r '.total' <<<"$(searchq "$MID")")" \
    "$(jq -r '[.facets.library[].count] | add // 0' <<<"$fl")"
fi

# 流派筛选：挑分面里命中最多的那个流派
G_FACET=$(jq -r '.facets.genre[0].value // empty' <<<"$f")
if [[ -n "$G_FACET" ]]; then
  rg=$(searchq "$MID" --data-urlencode "genre=$G_FACET")
  check "流派筛选：结果都带该流派" true \
    "$(jq -r --arg g "$G_FACET" 'all(.items[]; (.genres // []) | index($g) != null)' <<<"$rg")"
  fg=$(facets "$MID" --data-urlencode "genre=$G_FACET")
  check "流派筛选：facets.total == 列表 total" "$(jq -r '.total' <<<"$rg")" "$(jq -r '.facets.total' <<<"$fg")"
  check "流派筛选：sum(类型分面) == total" "$(jq -r '.facets.total' <<<"$fg")" \
    "$(jq -r '[.facets.kind[].count] | add // 0' <<<"$fg")"
  check "流派筛选：流派分面仍然列出别的流派（自己那一维关掉）" true \
    "$(jq -r '.facets.genre | length >= 1' <<<"$fg")"
fi

echo
echo "== 9. 参数校验与鉴权 =="
check "缺 q = 400" 400 "$(st -b "$JAR" "$BASE/api/v1/search")"
check "空 q = 400" 400 "$(st -b "$JAR" "$BASE/api/v1/search?q=")"
check "空白 q = 400" 400 "$(st -b "$JAR" --get --data-urlencode 'q=   ' "$BASE/api/v1/search")"
check "非法 kind = 400" 400 "$(st -b "$JAR" "$BASE/api/v1/search?q=x&kind=album")"
check "非法 libraryId = 400" 400 "$(st -b "$JAR" "$BASE/api/v1/search?q=x&libraryId=abc")"
check "未登录 = 401" 401 "$(st "$BASE/api/v1/search?q=x")"
check "未登录（中文查询词）= 401" 401 "$(st --get --data-urlencode "q=$MID" "$BASE/api/v1/search")"
check "超限 limit 被夹到 100" 100 \
  "$(jq -r '.limit' <<<"$(searchq "$MID" --data-urlencode 'limit=9999')")"
check "空 q + 一个筛选（kind）= 200（有筛选时词可以为空）" 200 \
  "$(st -b "$JAR" "$BASE/api/v1/search?q=&kind=movie")"
check "非法 personId = 400" 400 "$(st -b "$JAR" "$BASE/api/v1/search?q=x&personId=abc")"
check "分面：空 q 无筛选 = 400" 400 "$(st -b "$JAR" "$BASE/api/v1/search/facets?q=")"
check "分面：未登录 = 401" 401 "$(st "$BASE/api/v1/search/facets?q=x")"
check "分面：非法 kind = 400" 400 "$(st -b "$JAR" "$BASE/api/v1/search/facets?q=x&kind=album")"
check "人：空 q = 400" 400 "$(st -b "$JAR" "$BASE/api/v1/search/people?q=")"
check "人：未登录 = 401" 401 "$(st "$BASE/api/v1/search/people?q=x")"
check "联想：空 q = 400" 400 "$(st -b "$JAR" "$BASE/api/v1/search/suggest?q=")"
check "联想：未登录 = 401" 401 "$(st "$BASE/api/v1/search/suggest?q=x")"
check "联想：limit 参数被夹到 20" 20 \
  "$(jq -r '.limit' <<<"$(suggest "$MID" --data-urlencode limit=9999)")"

echo
echo "== 10. 索引自动同步（改完标题立刻生效）=="
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
echo "== 11. 还原 =="
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
