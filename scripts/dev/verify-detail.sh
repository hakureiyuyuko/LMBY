#!/usr/bin/env bash
# 详情页（M6）后端验收：演职员 / 相关推荐 / 多版本选择。
#
# 用法：
#   LMBY_USER=devtest LMBY_PASS=xxx bash scripts/dev/verify-detail.sh
#
# 环境变量：
#   BASE          服务地址（默认 http://127.0.0.1:8099）
#   LIB_ID        指定媒体库（默认取第一个库）
#
# 说明：
#   - 多版本那一段会**临时**往库里插一条「同一部片子的第二个版本」的 media_files 行
#     （路径指向同一个库里另一个真实文件，所以路径校验能过），EXIT 陷阱里删掉；
#   - 演职员目前只有 nfo 这一个来源，所以「库里有没有演职员」取决于测试库的 nfo
#     —— 没有就只验「接口形状与空值语义」，不假装有。
set -uo pipefail

BASE=${BASE:-http://127.0.0.1:8099}
USER=${LMBY_USER:-}
PASS=${LMBY_PASS:-}
LIB=${LIB_ID:-}
PG_PASS_FILE=${PG_PASS_FILE:-/etc/lmby/pg-password}

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
checkge() { # checkge 名称 下限 实际
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

sql() {
  local pw="${PGPASSWORD:-$(cat "$PG_PASS_FILE" 2>/dev/null)}"
  PGPASSWORD="$pw" psql -h 127.0.0.1 -U lmby -d lmby -tAc "$1" 2>/dev/null
}

JAR=$(mktemp)
TMP_FILE_ID=""
SIDS=""

cleanup() {
  for sid in $SIDS; do
    curl -s -o /dev/null -b "$JAR" -X POST "$BASE/api/v1/play/$sid/stop" || true
  done
  if [[ -n "$TMP_FILE_ID" ]]; then
    sql "delete from media_files where id = $TMP_FILE_ID" >/dev/null
  fi
  rm -f "$JAR"
}
trap cleanup EXIT

echo "== 0. 登录 / 前置 =="
code=$(curl -s -o /dev/null -w '%{http_code}' -c "$JAR" -X POST "$BASE/api/v1/auth/login" \
  -H 'Content-Type: application/json' --data-binary "$(jq -nc --arg u "$USER" --arg p "$PASS" '{username:$u,password:$p}')")
check "登录" 200 "$code"
[[ "$code" == "200" ]] || exit 1
check "能连上数据库" 1 "$(sql 'select 1')"

if [[ -z "$LIB" ]]; then
  LIB=$(curl -s -b "$JAR" "$BASE/api/v1/libraries" | jq -r '.libraries[0].id // empty')
fi
check "有媒体库可选" true "$([[ -n $LIB ]] && echo true || echo false)"
[[ -n "$LIB" ]] || exit 1

MOVIES=$(curl -s -b "$JAR" "$BASE/api/v1/libraries/$LIB/browse?kind=movie&limit=20")
SERIES=$(curl -s -b "$JAR" "$BASE/api/v1/libraries/$LIB/browse?kind=series&limit=5")
MOVIE_ID=$(jq -r '.items[0].id // empty' <<<"$MOVIES")
SERIES_ID=$(jq -r '.items[0].id // empty' <<<"$SERIES")
note "库 $LIB：电影 $(jq -r '.items|length' <<<"$MOVIES") 条、剧集 $(jq -r '.items|length' <<<"$SERIES") 条"

echo
echo "== 1. 演职员接口 =="
# 在前 20 部电影 + 前 5 部剧集里找一个真的带演职员的
WITH_PEOPLE=""
for id in $(jq -r '.items[].id' <<<"$MOVIES") $(jq -r '.items[].id' <<<"$SERIES"); do
  n=$(curl -s -b "$JAR" "$BASE/api/v1/items/$id/people" | jq -r '.people | length')
  if [[ "$n" =~ ^[0-9]+$ ]] && ((n > 0)); then WITH_PEOPLE="$id"; break; fi
done

SHAPE=$(curl -s -b "$JAR" "$BASE/api/v1/items/$MOVIE_ID/people")
check "接口回 source=nfo（如实说明来源）" "nfo" "$(jq -r '.source' <<<"$SHAPE")"
check "people 是数组" "array" "$(jq -r '.people | type' <<<"$SHAPE")"

if [[ -n "$WITH_PEOPLE" ]]; then
  P=$(curl -s -b "$JAR" "$BASE/api/v1/items/$WITH_PEOPLE/people")
  N=$(jq -r '.people | length' <<<"$P")
  note "条目 $WITH_PEOPLE 有 $N 位演职员"
  checkge "演职员数 > 0" 1 "$N"
  check "每条都有名字" 0 "$(jq -r '[.people[] | select((.name // "") == "")] | length' <<<"$P")"
  check "每条都有角色" 0 "$(jq -r '[.people[] | select((.role // "") == "")] | length' <<<"$P")"
  check "第一个人是演员（演员排在前面）" "actor" "$(jq -r '.people[0].role | ascii_downcase' <<<"$P")"
  check "演员带 character（演的是谁）" true \
    "$(jq -r '[.people[] | select((.role | ascii_downcase) == "actor") | .character] | map(select(. != null and . != "")) | length > 0' <<<"$P")"
  check "外部 id 有就带出来（tmdb/imdb）" true \
    "$(jq -r '[.people[] | select(.providerIds != null and (.providerIds | length) > 0)] | length > 0' <<<"$P")"
  note "前三位：$(jq -r '[.people[0:3][] | .name + "/" + .role] | join("、")' <<<"$P")"
else
  note "测试库里没有带演职员的 nfo（接口本身照常可用）"
fi

# 空值语义：挑一个没有演职员的条目（如果有）
NO_PEOPLE=$(jq -r '[.items[].id] | .[0]' <<<"$MOVIES")
if [[ "$NO_PEOPLE" != "$WITH_PEOPLE" ]]; then
  check "没有 nfo 演职员的条目回空数组" 0 \
    "$(curl -s -b "$JAR" "$BASE/api/v1/items/$NO_PEOPLE/people" | jq -r '.people | length')"
fi

check "未登录读演职员被拒（401）" 401 "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/v1/items/$MOVIE_ID/people")"
check "不存在的条目回 404" 404 "$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" "$BASE/api/v1/items/999999999/people")"

echo
echo "== 2. 相关推荐 =="
REL=$(curl -s -b "$JAR" "$BASE/api/v1/items/$MOVIE_ID/related?limit=6")
TOTAL=$(jq -r '.items | length' <<<"$REL")
if [[ -n "$MOVIE_ID" ]]; then
  checkge "有推荐结果" 1 "$TOTAL"
  check "推荐里不含自己" 0 "$(jq -r --argjson id "$MOVIE_ID" '[.items[] | select(.id == $id)] | length' <<<"$REL")"
  check "都来自同一个库" 0 \
    "$(jq -r --argjson lib "$LIB" '[.items[] | select(.libraryId != $lib)] | length' <<<"$REL")"
  check "只推荐电影/剧集（不推季与集）" 0 \
    "$(jq -r '[.items[] | select(.kind != "movie" and .kind != "series")] | length' <<<"$REL")"
  check "limit 生效（<=6）" true "$(jq -r '.items | length <= 6' <<<"$REL")"
  if ((TOTAL >= 2)); then
    SCORES=$(jq -r '[.items[] | (.communityRating // 0)] | . as $a | ($a == ($a | sort | reverse))' <<<"$REL")
    check "按评分从高到低" true "$SCORES"
  fi
  note "推荐前三条：$(jq -r '[.items[0:3][].title] | join("、")' <<<"$REL")"
else
  note "库里没有电影，跳过相关推荐"
fi
check "未登录读相关推荐被拒（401）" 401 \
  "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/v1/items/$MOVIE_ID/related")"

echo
echo "== 3. 多版本选择 =="
# 找一部「能直出」的片子（h264 + mp4 最稳），再给它临时加一个版本
GOOD_ID=""
GOOD_FILE=""
for id in $(jq -r '.items[].id' <<<"$MOVIES"); do
  PL=$(curl -s -b "$JAR" "$BASE/api/v1/items/$id/playlist")
  f=$(jq -r '[.files[] | select((.video[0].codec // "") == "h264")][0].fileId // empty' <<<"$PL")
  if [[ -n "$f" ]]; then GOOD_ID="$id"; GOOD_FILE="$f"; break; fi
done

if [[ -z "$GOOD_ID" ]]; then
  note "库里没有 h264 的电影文件，跳过多版本用例"
else
  note "用条目 $GOOD_ID 的文件 $GOOD_FILE 做多版本测试"
  check "只有一个版本时 playlist 回 1 个文件" 1 \
    "$(curl -s -b "$JAR" "$BASE/api/v1/items/$GOOD_ID/playlist" | jq -r '.files | length')"

  # 同一个库里另一个**真实存在但没登记在 media_files 里**的文件（图片）——
  # 用它的路径造第二个版本：路径校验能过（在库根目录下）、也不会撞 path 唯一索引
  # （media_files.path 是全局唯一的，拿另一个已登记的视频路径插会直接报冲突）
  OTHER_PATH=$(sql "select path from images
                     where item_id in (select id from media_items where library_id = $LIB)
                     order by id limit 1")
  OTHER_PATH=${OTHER_PATH//\'/\'\'}   # 路径里可能有单引号，转义一下
  if [[ -z "$OTHER_PATH" ]]; then
    note "库里找不到可用的第二路径（images 表为空），跳过多版本用例"
  else
    # 注意 psql 在 INSERT 之后还会打一行命令标签（INSERT 0 1），只取第一行
    TMP_FILE_ID=$(sql "insert into media_files
        (item_id, path, size_bytes, mtime_ns, container, duration_ticks,
         video_streams, audio_streams, subtitle_streams, chapters, hdr, probe_state, probed_at)
      select item_id, '${OTHER_PATH}', size_bytes, mtime_ns, container, duration_ticks,
             video_streams, audio_streams, subtitle_streams, chapters, hdr, 'ok', now()
        from media_files where id = $GOOD_FILE
      returning id" | head -1)
    TMP_FILE_ID=$(printf '%s' "$TMP_FILE_ID" | tr -dc '0-9')
    note "临时第二版本路径：$OTHER_PATH"
    check "临时第二个版本插进去了" true "$([[ -n $TMP_FILE_ID ]] && echo true || echo false)"

    PL2=$(curl -s -b "$JAR" "$BASE/api/v1/items/$GOOD_ID/playlist")
    check "playlist 现在回 2 个文件" 2 "$(jq -r '.files | length' <<<"$PL2")"

    # 指定第二个版本 → 放的就是它（决策引擎不许「帮你挑个更流畅的」）
    R=$(curl -s -b "$JAR" -X POST "$BASE/api/v1/items/$GOOD_ID/play" -H 'Content-Type: application/json' \
      --data-binary "$(jq -nc --argjson f "$TMP_FILE_ID" '{fileId:$f}')")
    check "指定第二个版本：按指定的放" "$TMP_FILE_ID" "$(jq -r '.fileId // empty' <<<"$R")"
    SID=$(jq -r '.playSessionId // empty' <<<"$R")
    [[ -n "$SID" ]] && SIDS="$SIDS $SID"

    R2=$(curl -s -b "$JAR" -X POST "$BASE/api/v1/items/$GOOD_ID/play" -H 'Content-Type: application/json' \
      --data-binary '{}')
    AUTO=$(jq -r '.fileId // empty' <<<"$R2")
    check "不带 fileId 时由决策引擎挑（挑到的是库里那一版之一）" true \
      "$(jq -r --argjson a "$AUTO" '[.files[].fileId] | index($a) != null' <<<"$PL2")"
    SID2=$(jq -r '.playSessionId // empty' <<<"$R2")
    [[ -n "$SID2" ]] && SIDS="$SIDS $SID2"

    check "不存在的 fileId 回 404（不静默放另一版）" 404 \
      "$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -X POST "$BASE/api/v1/items/$GOOD_ID/play" \
        -H 'Content-Type: application/json' --data-binary '{"fileId":999999999}')"
  fi
fi

echo
echo "== 4. 单测过的纯逻辑（提醒） =="
note "说明文本与角色标签在前端（roleLabel），这里只验接口形状"

echo
echo "================ 结果：$PASS_N 通过 / $FAIL_N 失败 ================"
[[ "$FAIL_N" -eq 0 ]]
