#!/usr/bin/env bash
# 造一条「待确认」条目，好在界面上看到人工匹配页与批量选择（验收脚本的夹具）。
#
# 为什么需要它：验收库里 472 条全部是 nfo 状态（本地 nfo 优先，没东西可刮），
# 于是人工匹配页是空的 —— 批量选择、候选对比这些界面就没法验。
# 这个脚本临时把某条置为 review（并塞两个假候选），跑完用下面的还原命令恢复。
#
# 用法：
#   bash scripts/dev/seed-review-item.sh          # 造夹具
#   bash scripts/dev/seed-review-item.sh --restore # 还原（重扫 + 清掉假候选）
#
# ⚠️ 它会改**真实库**里的一条数据（只改状态与候选，不动标题/文件）。用完记得还原。
set -uo pipefail

export PGCLIENTENCODING="${PGCLIENTENCODING:-UTF8}"
export PGPASSWORD="${PGPASSWORD:-$(cat /etc/lmby/pg-password 2>/dev/null)}"
BASE="${BASE:-http://127.0.0.1:8099}"
DB="${DB_NAME:-lmby}"

q() { psql -tAq -h 127.0.0.1 -U lmby -d "$DB" -c "$1"; }

restore() {
  echo "-- 重扫（refreshMetadata）把状态拉回 nfo"
  local jar; jar=$(mktemp)
  curl -s -c "$jar" -X POST "$BASE/api/v1/auth/login" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg u "${LMBY_USER:-devtest}" --arg p "${LMBY_PASS:-}" '{username:$u,password:$p}')" >/dev/null
  lib=$(curl -s -b "$jar" "$BASE/api/v1/libraries" | jq -r '.libraries[0].id')
  curl -s -b "$jar" -X POST "$BASE/api/v1/libraries/$lib/scan" \
    -H 'Content-Type: application/json' -d '{"refreshMetadata":true}' >/dev/null
  for _ in $(seq 1 120); do
    [ "$(curl -s -b "$jar" "$BASE/api/v1/libraries/$lib/scan" | jq -r '.running')" = "false" ] && break
    sleep 2
  done
  rm -f "$jar"

  echo "-- 清掉夹具留下的候选与错误"
  q "update media_items set scrape_error = '', match_score = null,
            match_candidates = '[]'::jsonb where id = $SEED_ID" >/dev/null
  q "select 'id=$SEED_ID state=' || match_state || ' source=' || metadata_source from media_items where id = $SEED_ID"
}

SEED_ID_FILE=/tmp/lmby-seed-review-id

if [[ "${1:-}" == "--restore" ]]; then
  SEED_ID="$(cat "$SEED_ID_FILE" 2>/dev/null || q "select id from media_items where deleted_at is null order by id limit 1")"
  restore
  exit 0
fi

SEED_ID="$(q "select id from media_items where metadata_source = 'nfo' and deleted_at is null order by id limit 1")"
if [[ -z "$SEED_ID" ]]; then
  echo "库里没有 nfo 条目可用作夹具"
  exit 1
fi
echo "$SEED_ID" >"$SEED_ID_FILE"

echo "-- 把条目 $SEED_ID 临时置为 review（带两个假候选）"
q "update media_items set
     match_state = 'review',
     scrape_error = '临时置为 review（界面验收夹具）',
     match_score = 0.86,
     match_candidates = '[
       {\"candidateId\":603,\"kind\":\"movie\",\"title\":\"验收候选 A\",\"year\":1999,\"score\":0.86,\"decision\":\"review\"},
       {\"candidateId\":604,\"kind\":\"movie\",\"title\":\"验收候选 B\",\"year\":2001,\"score\":0.74,\"decision\":\"review\"}
     ]'::jsonb
   where id = $SEED_ID" >/dev/null
q "select 'id=$SEED_ID state=' || match_state || ' title=' || title from media_items where id = $SEED_ID"

cat <<EOF

夹具已就绪（条目 $SEED_ID）。现在可以跑：
  BASE=... LMBY_USER=... LMBY_PASS=... node scripts/dev/m2-ui-test.mjs
用完还原：
  bash scripts/dev/seed-review-item.sh --restore
EOF
