#!/usr/bin/env bash
# M6「首页」的真库验收（对着跑着的服务做 HTTP 断言，脚本自造观看记录、跑完还原）。
#
# 验的是四件事：
#   1. **形状**：hero / continue / sections 三段都在，字段齐、上限生效（hero ≤ 10、每行 ≤ 20）；
#   2. **轮播的语义**：hero 都是顶层条目（电影/剧集）且按 updated_at 倒序 ——
#      也就是「最近更新/入库」，而不是随便挑的；recent 行的前 10 条与之逐条一致；
#   3. **推荐确实来自观看历史**（本脚本最有价值的一段）：把一部有流派的电影标记为已看，
#      然后要求 —— 它**不再出现**在推荐里、推荐的每一条都命中接口给出的口味画像、
#      副标题说得清依据、连续两次请求顺序完全一致；
#   4. 鉴权：未登录 401。
#
# 造数据的手法与还原：`POST /api/v1/items/{id}/played`（played=true 会写 last_played_at，
# 这就是「观看历史」）。跑完把它还原成 played=false。历史行本身删不掉（没有这个接口），
# 但它 position=0 且 played=false，既不出现在「继续观看」也不会被当成本次新增 ——
# 只给这个测试账号的口味画像留了一点权重，是可接受的代价（要彻底干净可以删
# playback_progress 里那一行）。
#
# 用法：
#   LMBY_USER=devtest LMBY_PASS=口令 bash scripts/dev/verify-home.sh
# 可选：BASE（默认 http://127.0.0.1:8099）
set -uo pipefail

BASE="${BASE:-http://127.0.0.1:8099}"
USER="${LMBY_USER:-}"
PASS="${LMBY_PASS:-}"

if [[ -z "$USER" || -z "$PASS" ]]; then
  echo "用法：LMBY_USER=devtest LMBY_PASS=口令 bash scripts/dev/verify-home.sh"
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
home() { curl -s -b "$JAR" "$BASE/api/v1/home"; }

echo "目标：$BASE"

echo
echo "== 1. 登录与鉴权 =="
check "未登录 GET /home = 401" 401 "$(st "$BASE/api/v1/home")"
code=$(st -X POST "$BASE/api/v1/auth/login" -c "$JAR" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg u "$USER" --arg p "$PASS" '{username:$u,password:$p}')")
check "登录 $USER = 200" 200 "$code"
[[ "$code" != "200" ]] && { echo "登录失败，后面的断言没有意义"; exit 1; }

H=$(home)

echo
echo "== 2. 形状与上限 =="
check "hero 是数组" true "$(jq -r '.hero | type == "array"' <<<"$H")"
check "continue 是数组" true "$(jq -r '.continue | type == "array"' <<<"$H")"
check "sections 是数组" true "$(jq -r '.sections | type == "array"' <<<"$H")"
check "hero ≤ 10 条" true "$(jq -r '.hero | length <= 10' <<<"$H")"
check "每行 ≤ 20 条" true "$(jq -r '[.sections[].items | length] | all(. <= 20)' <<<"$H")"
check "hero 里都是顶层条目（movie/series）" true \
  "$(jq -r 'all(.hero[]; .kind == "movie" or .kind == "series")' <<<"$H")"
check "hero 首条字段齐备（id/title/kind/updatedAt）" true \
  "$(jq -r '.hero[0] | has("id") and has("title") and has("kind") and has("updatedAt")' <<<"$H")"
check "每行都有 key/title/items" true \
  "$(jq -r 'all(.sections[]; has("key") and has("title") and (.items | type == "array"))' <<<"$H")"

HERO_N=$(jq -r '.hero | length' <<<"$H")
if [[ "$HERO_N" == "0" ]]; then
  note "库里还没有顶层条目（hero 为空），跳过轮播语义断言"
else
  echo
  echo "== 3. 轮播是「最近更新/入库」 =="
  check "hero 按 updated_at 倒序" true \
    "$(jq -r '[.hero[].updatedAt] as $t | $t == ($t | sort | reverse)' <<<"$H")"
  # recent 行与 hero 用的是同一段查询（只是 limit 不同）：前 10 条必须逐条一致
  check "recent 行前 $HERO_N 条与 hero 逐条一致（同一查询）" true \
    "$(jq -r '.hero as $h | ([.sections[] | select(.key == "recent")][0].items[:($h | length)] | map(.id)) == ($h | map(.id))' <<<"$H")"
  check "recent 行的条数不少于 hero（同一批里多取几条）" true \
    "$(jq -r '([.sections[] | select(.key == "recent")][0].items | length) >= (.hero | length)' <<<"$H")"
fi

echo
echo "== 4. 推荐来自观看历史（自造一条记录）=="
libs=$(bd -b "$JAR" "$BASE/api/v1/libraries")
LIB_ID=$(jq -r '.libraries[0].id // empty' <<<"$libs")
if [[ -z "$LIB_ID" ]]; then
  note "没有媒体库，跳过推荐段"
else
  items=$(bd -b "$JAR" "$BASE/api/v1/libraries/$LIB_ID/items?kind=movie&limit=200")
  pick=$(jq -r '[.items[] | select((.genres // []) | length > 0)][0]
    | select(. != null) | [.id, .title, ((.genres // []) | join("、"))] | @tsv' <<<"$items")
  if [[ -z "$pick" ]]; then
    note "库里没有带流派的电影，跳过推荐段（nfo 里得有 <genre>）"
  else
    A_ID=$(cut -f1 <<<"$pick")
    A_TITLE=$(cut -f2 <<<"$pick")
    A_GENRES=$(cut -f3 <<<"$pick")
    note "样本：条目 $A_ID《$A_TITLE》流派 $A_GENRES"

    # 造「看过」：played=true 会写 last_played_at，这就是口味画像的输入
    code=$(json -b "$JAR" -X POST "$BASE/api/v1/items/$A_ID/played" \
      -H 'Content-Type: application/json' -d '{"played":true}' -o /dev/null -w '%{http_code}')
    check "标记《$A_TITLE》为已看 = 200" 200 "$code"

    H=$(home)
    REC_N=$(jq -r '[.sections[] | select(.key == "recommend")][0].items | length // 0' <<<"$H")
    HAS_REC=$(jq -r 'any(.sections[]; .key == "recommend")' <<<"$H")
    if [[ "$HAS_REC" != "true" || "$REC_N" == "0" ]]; then
      note "这一轮没有「为你推荐」行（看过的作品可能没有流派），跳过推荐断言"
    else
      check "推荐的条目里不含刚标记为已看的《$A_TITLE》" false \
        "$(jq -r --argjson id "$A_ID" 'any([.sections[] | select(.key == "recommend")][0].items[]; .id == $id)' <<<"$H")"

      # ★ 核心断言：推荐的每一条都要命中接口给出的口味画像里的至少一个流派
      # （画像随响应一起返回，所以这条断言可以精确判定，不靠猜）
      BAD_IDS=$(jq -r '([.sections[] | select(.key == "recommend")][0]) as $r
        | ($r.taste | map(.genre)) as $t
        | [ $r.items[] | select(([.genres[]?] | map(select(. as $g | $t | index($g))) | length) == 0) | .id ]
        | join(",")' <<<"$H")
      ALL_HIT=$(jq -r '([.sections[] | select(.key == "recommend")][0]) as $r
        | ($r.taste | map(.genre)) as $t
        | all($r.items[]; ([.genres[]?] | map(select(. as $g | $t | index($g))) | length) > 0)' <<<"$H")
      check "推荐里每一条都命中口味画像里的流派（不命中：${BAD_IDS:-无}）" true "$ALL_HIT"

      check "口味画像按权重倒序" true \
        "$(jq -r '([.sections[] | select(.key == "recommend")][0].taste | map(.weight)) as $w | $w == ($w | sort | reverse)' <<<"$H")"
      check "口味画像非空" true \
        "$(jq -r '([.sections[] | select(.key == "recommend")][0].taste | length) > 0' <<<"$H")"
      # 刚看过的那部作品的流派必须进了画像 —— 这是「推荐确实由观看历史驱动」
      # 的正面证据，与「它自己不再被推」互为反证）
      A_GENRES_JSON=$(jq -nc --arg s "$A_GENRES" '$s | split("、")')
      check "画像里包含刚看过那部的流派（$A_GENRES）" true \
        "$(jq -r --argjson gs "$A_GENRES_JSON" \
          '([.sections[] | select(.key == "recommend")][0].taste | map(.genre)) as $t
           | all($gs[]; . as $g | $t | index($g) != null)' <<<"$H")"
      check "副标题说明了依据（因为/根据）" true \
        "$(jq -r '([.sections[] | select(.key == "recommend")][0].subtitle) | test("因为|根据")' <<<"$H")"
      check "推荐条目都是顶层条目" true \
        "$(jq -r 'all([.sections[] | select(.key == "recommend")][0].items[]; .kind == "movie" or .kind == "series")' <<<"$H")"

      # 稳定：同一份数据两次请求的顺序必须一致（否则「刷新就变样」，也说明排序不确定）
      H2=$(home)
      check "两次请求的 hero 顺序一致" "$(jq -c '[.hero[].id]' <<<"$H")" "$(jq -c '[.hero[].id]' <<<"$H2")"
      check "两次请求的推荐顺序一致" \
        "$(jq -c '[.sections[] | select(.key == "recommend")][0].items | map(.id)' <<<"$H")" \
        "$(jq -c '[.sections[] | select(.key == "recommend")][0].items | map(.id)' <<<"$H2")"
    fi

    echo
    echo "== 5. 还原 =="
    code=$(json -b "$JAR" -X POST "$BASE/api/v1/items/$A_ID/played" \
      -H 'Content-Type: application/json' -d '{"played":false}' -o /dev/null -w '%{http_code}')
    check "还原《$A_TITLE》的已看状态 = 200" 200 "$code"
    check "还原后首页仍然可用（200）" 200 "$(st -b "$JAR" "$BASE/api/v1/home")"
    check "还原后它不在「继续观看」里（position=0 不该出现）" false \
      "$(jq -r --argjson id "$A_ID" 'any(.continue[]; .item.id == $id)' <<<"$(home)")"
  fi
fi

echo
printf '\033[1m结果：%d 通过，%d 失败\033[0m\n' "$pass" "$fail"
[[ "$fail" -eq 0 ]]
