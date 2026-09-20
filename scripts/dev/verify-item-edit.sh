#!/usr/bin/env bash
# M2「字段锁定编辑界面」的真库验收（在 LMBY 容器里跑，对着跑着的服务做完整 HTTP 往返）
#
# 为什么不能只测接口返回：这条 DoD 是「**手改字段不被覆盖**」，
# 它只有在「改字段 → 锁住 → 重扫（重读 nfo）→ 值还在」这条链路上成立才算数。
# 所以脚本做四件事：
#
#   1. 挑一条 metadata_source=nfo 的条目，快照原值；
#   2. PATCH：改标题（**不锁**）+ 改简介（**锁**）；
#   3. 触发 refreshMetadata 重扫（会重读同目录 nfo），然后同时断言两件事：
#        - 简介还是人工写的值 → 锁定生效；
#        - 标题被 nfo 改回去了 → **对照组**：证明这次重扫真的重写了字段，
#          而不是「什么都没读所以什么都没变」（那样锁定验的就是假命题）。
#      对照组是否成立取决于该条目的 nfo 里有没有 <title>，脚本会如实报告。
#   4. 还原成跑之前的样子（字段 + 锁定集合 + 状态/来源），并再验一次。
#
# 外加：参数校验（非法字段名/类型/越界）与「单条重刮不会动 nfo 条目」。
#
# 用法：
#   LMBY_USER=devtest LMBY_PASS=口令 bash scripts/dev/verify-item-edit.sh
# 可选环境变量：
#   BASE（默认 http://127.0.0.1:8099）、LIB_ID、ITEM_ID
#   KEEP=1 跑完不还原（想看现场时用；默认还原）
set -uo pipefail

BASE="${BASE:-http://127.0.0.1:8099}"
USER="${LMBY_USER:-}"
PASS="${LMBY_PASS:-}"
KEEP="${KEEP:-}"

if [[ -z "$USER" || -z "$PASS" ]]; then
  echo "用法：LMBY_USER=devtest LMBY_PASS=口令 bash scripts/dev/verify-item-edit.sh"
  exit 2
fi

JAR=$(mktemp)
trap 'rm -f "$JAR"' EXIT

pass=0; fail=0
ok()    { printf '  \033[32mok\033[0m   %s\n' "$1"; pass=$((pass+1)); }
bad()   { printf '  \033[31mFAIL\033[0m %s（期望 %s，实际 %s）\n' "$1" "$2" "$3"; fail=$((fail+1)); }
check() { if [[ "$2" == "$3" ]]; then ok "$1"; else bad "$1" "$2" "$3"; fi; }
note()  { printf '  \033[33m--\033[0m   %s\n' "$1"; }

st()   { curl -s -o /dev/null -w '%{http_code}' "$@"; }
bd()   { curl -s "$@"; }
json() { curl -s -H 'Content-Type: application/json' "$@"; }
patch() { json -b "$JAR" -X PATCH "$BASE/api/v1/items/$ITEM_ID" -H 'Content-Type: application/json' -d "$1"; }
item() { bd -b "$JAR" "$BASE/api/v1/items/$ITEM_ID"; }

echo "目标：$BASE"

echo
echo "== 1. 登录 =="
code=$(st -X POST "$BASE/api/v1/auth/login" -c "$JAR" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg u "$USER" --arg p "$PASS" '{username:$u,password:$p}')")
check "登录 $USER = 200" 200 "$code"
if [[ "$code" != "200" ]]; then
  echo "登录失败，后面的断言没有意义"
  exit 1
fi

echo
echo "== 2. 挑一条有 nfo 元数据的条目 =="
libs=$(bd -b "$JAR" "$BASE/api/v1/libraries")
LIB_ID="${LIB_ID:-$(jq -r '.libraries[0].id // empty' <<<"$libs")}"
if [[ -z "$LIB_ID" ]]; then
  echo "库里没有媒体库，先建一个再跑"
  exit 1
fi
note "媒体库 id=$LIB_ID（$(jq -r --argjson i "$LIB_ID" '.libraries[]|select(.id==$i)|.name' <<<"$libs")）"

if [[ -z "${ITEM_ID:-}" ]]; then
  items=$(bd -b "$JAR" "$BASE/api/v1/libraries/$LIB_ID/items?kind=movie&limit=200")
  ITEM_ID=$(jq -r '[.items[] | select(.metadataSource=="nfo" and (.title|length)>0)][0].id // empty' <<<"$items")
fi
if [[ -z "${ITEM_ID:-}" ]]; then
  echo "没找到 metadata_source=nfo 的电影条目（可用 ITEM_ID= 指定一条）"
  exit 1
fi

detail=$(item)
orig_title=$(jq -r '.item.title' <<<"$detail")
orig_overview=$(jq -r 'if .item.overview == null then "" else .item.overview end' <<<"$detail")
orig_provider=$(jq -c '.item.providerIds // {}' <<<"$detail")
orig_locked=$(jq -c '.item.lockedFields // []' <<<"$detail")
orig_state=$(jq -r '.item.matchState' <<<"$detail")
orig_source=$(jq -r '.item.metadataSource // ""' <<<"$detail")
note "条目 id=$ITEM_ID《$orig_title》状态=$orig_state 来源=$orig_source 锁定=$orig_locked"

check "详情返回 200" 200 "$(st -b "$JAR" "$BASE/api/v1/items/$ITEM_ID")"
check "可编辑字段 12 个" 12 "$(jq -r '.fields|length' <<<"$detail")"
check "字段带形态（title=text）" text \
  "$(jq -r '.fields[]|select(.name=="title")|.kind' <<<"$detail")"
check "时长字段带分钟单位" minutes \
  "$(jq -r '.fields[]|select(.name=="runtime")|.unit' <<<"$detail")"
check "字段名与刮削侧一致（providerIds 存在）" true \
  "$(jq -r '[.fields[].name]|index("providerIds")!=null' <<<"$detail")"
check "元数据 id 字段是键值形态" map \
  "$(jq -r '.fields[]|select(.name=="providerIds")|.kind' <<<"$detail")"
check "报告元数据源是否已配置" true "$(jq -r 'has("scrapeConfigured")' <<<"$detail")"

echo
echo "== 3. 人工改：标题（不锁）+ 简介（锁） =="
MARK="LMBY 人工编辑验收 $(date +%H:%M:%S)"
new_title="$orig_title ✎"
resp=$(patch "$(jq -nc --arg t "$new_title" --arg o "$MARK" \
  '{fields:{title:$t, overview:$o}, lockedFields:["overview"]}')")
check "PATCH = 200" "$new_title" "$(jq -r '.item.title' <<<"$resp")"
check "简介已写入" "$MARK" "$(jq -r '.item.overview' <<<"$resp")"
check "锁定集合 = [overview]" '["overview"]' "$(jq -c '.item.lockedFields' <<<"$resp")"
check "元数据来源标记为 manual" manual "$(jq -r '.item.metadataSource' <<<"$resp")"
check "字段级锁定状态：overview 已锁" true \
  "$(jq -r '.fields[]|select(.name=="overview")|.locked' <<<"$resp")"
check "字段级锁定状态：title 未锁" false \
  "$(jq -r '.fields[]|select(.name=="title")|.locked' <<<"$resp")"
check "重读详情：改动已持久化" "$MARK" "$(jq -r '.item.overview' <<<"$(item)")"
check "重读详情：标题已持久化" "$new_title" "$(jq -r '.item.title' <<<"$(item)")"

echo
echo "== 4. 重扫（refreshMetadata，会重读 nfo）="
code=$(st -b "$JAR" -X POST "$BASE/api/v1/libraries/$LIB_ID/scan" \
  -H 'Content-Type: application/json' -d '{"refreshMetadata":true}')
check "触发重扫 = 202" 202 "$code"
note "等扫描结束…"
for _ in $(seq 1 120); do
  [[ "$(bd -b "$JAR" "$BASE/api/v1/libraries/$LIB_ID/scan" | jq -r '.running')" == "false" ]] && break
  sleep 2
done
scan=$(bd -b "$JAR" "$BASE/api/v1/libraries/$LIB_ID/scan")
nfo_read=$(jq -r '.lastScan.stats.nfoRead // 0' <<<"$scan")
note "本次重扫读取 nfo $nfo_read 份，视频 $(jq -r '.lastScan.stats.videos // 0' <<<"$scan") 个"
check "重扫真的读了 nfo" true "$([[ "${nfo_read:-0}" -gt 0 ]] && echo true || echo false)"

after=$(item)
check "锁住的简介没被 nfo 覆盖" "$MARK" "$(jq -r '.item.overview' <<<"$after")"
if [[ "$(jq -r '.item.title' <<<"$after")" == "$orig_title" ]]; then
  ok "对照组：没锁的标题被 nfo 改回去了"
elif [[ "$(jq -r '.item.title' <<<"$after")" == "$new_title" ]]; then
  note "对照组不成立：这条的 nfo 没给 <title>（或没有同名 nfo），所以标题没被改写"
  note "「简介没变」仍成立，但它是锁定生效还是没读到数据，这条不能单独区分 —— 见上面的 nfoRead 计数"
else
  bad "对照组：标题应当被 nfo 改写或原样保留" "$orig_title" "$(jq -r '.item.title' <<<"$after")"
fi
check "锁定集合仍在" '["overview"]' "$(jq -c '.item.lockedFields' <<<"$after")"

echo
echo "== 5. 单条重刮不动有 nfo 的条目 =="
configured=$(bd -b "$JAR" "$BASE/api/v1/libraries/$LIB_ID/scrape" | jq -r '.configured')
if [[ "$configured" != "true" ]]; then
  note "未配置元数据源，跳过（接口应当回 409）"
  check "未配置时入队 = 409" 409 \
    "$(st -b "$JAR" -X POST "$BASE/api/v1/items/$ITEM_ID/scrape" -H 'Content-Type: application/json' -d '{"force":false}')"
else
  resp=$(json -b "$JAR" -X POST "$BASE/api/v1/items/$ITEM_ID/scrape" -H 'Content-Type: application/json' -d '{"force":false}')
  check "单条入队拿到 taskId" true "$(jq -r '.taskId > 0' <<<"$resp")"
  for _ in $(seq 1 60); do
    q=$(bd -b "$JAR" "$BASE/api/v1/tasks" | jq -r '(.stats.pending // 0) + (.stats.running // 0)')
    [[ "$q" == "0" ]] && break
    sleep 1
  done
  settled=$(item)
  check "队列跑完后状态仍是 nfo" "$orig_state" "$(jq -r '.item.matchState' <<<"$settled")"
  check "队列跑完后简介没被动" "$MARK" "$(jq -r '.item.overview' <<<"$settled")"
  check "队列跑完后标题没被动" "$(jq -r '.item.title' <<<"$after")" "$(jq -r '.item.title' <<<"$settled")"
fi

echo
echo "== 6. 参数校验（不落库） =="
check "未知字段名 = 400" 400 \
  "$(st -b "$JAR" -X PATCH "$BASE/api/v1/items/$ITEM_ID" -H 'Content-Type: application/json' \
      -d '{"fields":{"providers":"603"}}')"
check "未知字段名给出可用字段" true \
  "$(patch '{"fields":{"providers":"603"}}' | jq -r '.error|test("providerIds")')"
check "年份给字符串 = 400" 400 \
  "$(st -b "$JAR" -X PATCH "$BASE/api/v1/items/$ITEM_ID" -H 'Content-Type: application/json' \
      -d '{"fields":{"year":"1999"}}')"
check "评分 95（越界）= 400" 400 \
  "$(st -b "$JAR" -X PATCH "$BASE/api/v1/items/$ITEM_ID" -H 'Content-Type: application/json' \
      -d '{"fields":{"rating":95}}')"
check "清空标题 = 400" 400 \
  "$(st -b "$JAR" -X PATCH "$BASE/api/v1/items/$ITEM_ID" -H 'Content-Type: application/json' \
      -d '{"fields":{"title":"  "}}')"
check "空请求体 = 400" 400 \
  "$(st -b "$JAR" -X PATCH "$BASE/api/v1/items/$ITEM_ID" -H 'Content-Type: application/json' -d '{}')"
check "顶层未知字段 = 400" 400 \
  "$(st -b "$JAR" -X PATCH "$BASE/api/v1/items/$ITEM_ID" -H 'Content-Type: application/json' \
      -d '{"fiedls":{"title":"x"}}')"
check "锁定集合里的未知字段 = 400" 400 \
  "$(st -b "$JAR" -X PATCH "$BASE/api/v1/items/$ITEM_ID" -H 'Content-Type: application/json' \
      -d '{"lockedFields":["providers"]}')"
check "不存在的条目 = 404" 404 "$(st -b "$JAR" "$BASE/api/v1/items/999999999")"
check "非法条目 id = 400" 400 "$(st -b "$JAR" "$BASE/api/v1/items/abc")"
check "未登录 PATCH = 401" 401 \
  "$(st -X PATCH "$BASE/api/v1/items/$ITEM_ID" -H 'Content-Type: application/json' -d '{"fields":{"title":"x"}}')"

echo
echo "== 7. 还原 =="
if [[ -n "$KEEP" ]]; then
  note "KEEP=1：跳过还原（现场保留，条目 $ITEM_ID 仍是验收后的样子）"
else
  resp=$(patch "$(jq -nc --arg t "$orig_title" --arg o "$orig_overview" \
    --argjson ids "$orig_provider" --argjson locks "$orig_locked" \
    '{fields:{title:$t, overview:$o, providerIds:$ids}, lockedFields:$locks}')")
  code=$(st -b "$JAR" -X POST "$BASE/api/v1/libraries/$LIB_ID/scan" \
    -H 'Content-Type: application/json' -d '{"refreshMetadata":true}')
  for _ in $(seq 1 120); do
    [[ "$(bd -b "$JAR" "$BASE/api/v1/libraries/$LIB_ID/scan" | jq -r '.running')" == "false" ]] && break
    sleep 2
  done
  restored=$(item)
  check "还原：标题" "$orig_title" "$(jq -r '.item.title' <<<"$restored")"
  # 空字符串在 JSON 里被 omitempty 省掉，jq 会给出 null —— 比较前先归一成空串
  check "还原：简介" "$orig_overview" "$(jq -r '.item.overview // ""' <<<"$restored")"
  check "还原：元数据 id" "$orig_provider" "$(jq -c '.item.providerIds // {}' <<<"$restored")"
  check "还原：锁定集合" "$orig_locked" "$(jq -c '.item.lockedFields // []' <<<"$restored")"
  check "还原：匹配状态" "$orig_state" "$(jq -r '.item.matchState' <<<"$restored")"
  check "还原：元数据来源" "$orig_source" "$(jq -r '.item.metadataSource // ""' <<<"$restored")"
fi

echo
printf '\033[1m结果：%d 通过，%d 失败\033[0m\n' "$pass" "$fail"
[[ "$fail" -eq 0 ]]
