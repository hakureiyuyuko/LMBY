#!/usr/bin/env bash
# 「扫描遇到掉盘 / 文件回来」的验收（真库 + 真文件系统）。
#
# 背景（2026-09-25 在验证实例上实测的事故）：
#   · CIFS 挂载掉线 → 扫描每个根只报一条 `lstat …: host is down`、videos=0
#     → 走删除步骤把**整库**软删（38437 个文件），而且扫描还记「成功」；
#   · 之后每次重扫，那些文件在磁盘上都在，但增量状态只读「活着的行」→ 被当成新文件
#     去插 → 撞 media_files.path 唯一约束 → 每条都报「该路径已被其它条目占用，已跳过」
#     → 文件永远回不来（库 2 里 38461 个文件被这么钉死）。
#
# 本脚本验的就是这两件事的护栏：
#   A. 库根读不到（挂载掉线）→ 扫描判失败 + **一个文件都不删**；根回来之后一切照旧。
#   B. 目录还在但读成空的（挂载点变空）→ 要删的比例超过安全阀 → 不删 + 判失败。
#   C. 小库（文件数低于安全阀规模）删掉再放回 → 能复活，且不会再报
#      「该路径已被其它条目占用」。
#
# 用法：LMBY_USER=<管理员> LMBY_PASS=<口令> bash scripts/dev/verify-scan-robust.sh
#   BASE 默认 http://127.0.0.1:8099（在验证实例本机上跑）
#   ROOT 默认 /tmp/lmby-scan-robust（临时目录，脚本自己建、自己删）
set -u

BASE="${BASE:-http://127.0.0.1:8099}"
ROOT="${ROOT:-/tmp/lmby-scan-robust}"
BIG=24   # 越过 minFilesForDeleteGuard(20)，比例闸才会生效
SMALL=4  # 低于安全阀规模：小库按比例卡会把正常删除也拦下，所以它不设闸
LIBNAME="掉盘验收 $(date +%s)"
LIBNAME2="小库复活验收 $(date +%s)"

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
LIB_ID=""; LIB2_ID=""
cleanup() {
  for id in "$LIB_ID" "$LIB2_ID"; do
    [[ -n "$id" ]] && curl -s -o /dev/null -b "$JAR" -X DELETE "$BASE/api/v1/libraries/$id"
  done
  rm -f "$JAR"
  rm -rf "$ROOT" "$ROOT-small"
}
trap cleanup EXIT

api() { # api <method> <path> [body]
  if [[ -n "${3:-}" ]]; then
    curl -s -b "$JAR" -c "$JAR" -X "$1" "$BASE$2" -H 'Content-Type: application/json' -d "$3"
  else
    curl -s -b "$JAR" -c "$JAR" -X "$1" "$BASE$2"
  fi
}

# scan <库 id> 触发一次扫描并等到结束，把 scan 状态 JSON 打到 stdout。
scan() {
  local lib="$1"
  curl -s -o /dev/null -b "$JAR" -c "$JAR" -X POST "$BASE/api/v1/libraries/$lib/scan" \
    -H 'Content-Type: application/json' -d '{}'
  local st=""
  for _ in $(seq 1 90); do
    st=$(api GET "/api/v1/libraries/$lib/scan")
    [[ "$(jq -r '.running' <<<"$st")" != "true" ]] && break
    sleep 1
  done
  printf '%s' "$st"
}
stat_() { jq -r ".lastScan.stats.$2 // 0" <<<"$1"; }   # stat_ <扫描JSON> <字段>
state_() { jq -r '.lastScan.state // "?"' <<<"$1"; }
err_() { jq -r '.lastScan.error // ""' <<<"$1"; }
issues_() { jq -r '.issues | length' <<<"$1"; }
no_conflict_() { jq -r '[.issues[].message | select(test("已被其它条目占用"))] | length == 0' <<<"$1"; }

# mkfiles <目录> <数量>：造 n 个 200 KB 的「视频」（> [scan] min_file_size 默认 64 KiB）
mkfiles() {
  mkdir -p "$1"
  for i in $(seq -w 1 "$2"); do
    head -c 200000 /dev/zero > "$1/S01E$i.mkv"
  done
}

echo "== 0. 管理员登录 =="
LOGIN=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -c "$JAR" -X POST "$BASE/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"${LMBY_USER:?}\",\"password\":\"${LMBY_PASS:?}\"}")
check "管理员登录" 200 "$LOGIN"
[[ "$LOGIN" != "200" ]] && exit 1

rm -rf "$ROOT"
mkfiles "$ROOT/某番 (2024)/Season 1" "$BIG"
LIB=$(api POST /api/v1/libraries "{\"name\":\"$LIBNAME\",\"kind\":\"tv\",\"paths\":[\"$ROOT\"]}")
LIB_ID=$(jq -r '.library.id // .id // empty' <<<"$LIB")
check_true "建库成功" "$([[ -n "$LIB_ID" ]] && echo true || echo false)" "$LIB"
[[ -z "$LIB_ID" ]] && { echo "建库失败，后续无法进行"; exit 1; }

echo
echo "== 1. 基线：$BIG 个文件都入库 =="
ST=$(scan "$LIB_ID")
check_true "首次扫描成功" "$([[ "$(state_ "$ST")" == "done" ]] && echo true || echo false)" "state=$(state_ "$ST")"
check "入库视频数" "$BIG" "$(stat_ "$ST" videos)"
check "没有删除" 0 "$(stat_ "$ST" deletedFiles)"

echo
echo "== 2. 库根读不到（= 挂载掉线）：一个文件都不许删，且要判失败 =="
mv "$ROOT" "$ROOT.moved"   # 比 rm 更贴切：整个根「不见了」
ST=$(scan "$LIB_ID")
note "state=$(state_ "$ST") deletedFiles=$(stat_ "$ST" deletedFiles) unreadableRoots=$(stat_ "$ST" unreadableRoots)"
note "error=$(err_ "$ST")"
check_true "扫描判失败（而不是报成功）" "$([[ "$(state_ "$ST")" == "failed" ]] && echo true || echo false)" "state=$(state_ "$ST")"
check "一个文件都没删" 0 "$(stat_ "$ST" deletedFiles)"
check "读不到的根路径数" 1 "$(stat_ "$ST" unreadableRoots)"
check_true "错误里说清了「根路径读不到」" "$(jq -r '.lastScan.error // "" | test("根路径")' <<<"$ST")"
check_true "问题清单里有「访问失败」" \
  "$(jq -r '[.issues[].message | select(test("访问失败"))] | length > 0' <<<"$ST")"

echo
echo "== 3. 根回来之后一切照旧（没有丢数据） =="
mv "$ROOT.moved" "$ROOT"
ST=$(scan "$LIB_ID")
check_true "扫描恢复成功" "$([[ "$(state_ "$ST")" == "done" ]] && echo true || echo false)" "state=$(state_ "$ST")"
check "$BIG 个文件全在（未变）" "$BIG" "$(stat_ "$ST" unchanged)"
check "没有删除" 0 "$(stat_ "$ST" deletedFiles)"

echo
echo "== 4. 比例闸：目录还在但读成空的（挂载点变空） =="
mkdir -p "$ROOT/空目录"
rm -rf "$ROOT/某番 (2024)"   # 文件全没了但根还在 → 只能靠比例闸拦
ST=$(scan "$LIB_ID")
note "state=$(state_ "$ST") deletedFiles=$(stat_ "$ST" deletedFiles) skippedDeletions=$(stat_ "$ST" skippedDeletions)"
check_true "扫描判失败" "$([[ "$(state_ "$ST")" == "failed" ]] && echo true || echo false)" "state=$(state_ "$ST")"
check "一个文件都没删" 0 "$(stat_ "$ST" deletedFiles)"
check "跳过删除数 = $BIG（本该删的量也要报出来）" "$BIG" "$(stat_ "$ST" skippedDeletions)"
check_true "错误里点到了安全阀" "$(jq -r '.lastScan.error // "" | test("max_delete_ratio")' <<<"$ST")"

echo
echo "== 5. 文件放回：因为第 4 步没删，它们应当「未变」而不是被复活（没丢过） =="
rm -rf "$ROOT/空目录"
mkfiles "$ROOT/某番 (2024)/Season 1" "$BIG"
ST=$(scan "$LIB_ID")
note "state=$(state_ "$ST") changed=$(stat_ "$ST" changedFiles) new=$(stat_ "$ST" newFiles) revivedFiles=$(stat_ "$ST" revivedFiles)"
check_true "扫描成功" "$([[ "$(state_ "$ST")" == "done" ]] && echo true || echo false)" "state=$(state_ "$ST")"
check "没有删除" 0 "$(stat_ "$ST" deletedFiles)"
check "没有当成新文件" 0 "$(stat_ "$ST" newFiles)"
check "没有复活（本来就没被删）" 0 "$(stat_ "$ST" revivedFiles)"
# 重新生成的文件 mtime 变了，所以算「变化」而不是「未变」—— 两条合起来就该是 $BIG
check "$BIG 个文件都还挂在原来的条目上" "$BIG" "$(( $(stat_ "$ST" changedFiles) + $(stat_ "$ST" unchanged) ))"
check_true "没有「已被其它条目占用」假警告" "$(no_conflict_ "$ST")"

echo
echo "== 6. 单个文件删掉再放回（同一路径）→ 复活，且不再报「已被其它条目占用」 =="
rm -f "$ROOT/某番 (2024)/Season 1/S01E07.mkv"
ST=$(scan "$LIB_ID")
check "先软删掉 1 个" 1 "$(stat_ "$ST" deletedFiles)"
head -c 200000 /dev/zero > "$ROOT/某番 (2024)/Season 1/S01E07.mkv"
ST=$(scan "$LIB_ID")
note "revivedFiles=$(stat_ "$ST" revivedFiles) deletedFiles=$(stat_ "$ST" deletedFiles) issues=$(issues_ "$ST")"
check "复活 1 个文件" 1 "$(stat_ "$ST" revivedFiles)"
check "没有删除" 0 "$(stat_ "$ST" deletedFiles)"
check_true "**没有**「该路径已被其它条目占用」这种假警告" "$(no_conflict_ "$ST")"

echo
echo "== 7. 小库（$SMALL 个文件）：删光再放回 → 全量复活 =="
ROOT2="$ROOT-small"
rm -rf "$ROOT2"
mkfiles "$ROOT2/某番 (2024)/Season 1" "$SMALL"
LIB2=$(api POST /api/v1/libraries "{\"name\":\"$LIBNAME2\",\"kind\":\"tv\",\"paths\":[\"$ROOT2\"]}")
LIB2_ID=$(jq -r '.library.id // .id // empty' <<<"$LIB2")
check_true "小库建库成功" "$([[ -n "$LIB2_ID" ]] && echo true || echo false)" "$LIB2"
ST=$(scan "$LIB2_ID")
check "$SMALL 个文件入库" "$SMALL" "$(stat_ "$ST" videos)"

rm -rf "$ROOT2/某番 (2024)"
ST=$(scan "$LIB2_ID")
note "小库全删后：state=$(state_ "$ST") deletedFiles=$(stat_ "$ST" deletedFiles)"
check_true "小库不受比例闸限制（低于 $((SMALL + 1)) 个文件）" \
  "$([[ "$(state_ "$ST")" == "done" ]] && echo true || echo false)" "state=$(state_ "$ST")"
check "确实软删了 $SMALL 个" "$SMALL" "$(stat_ "$ST" deletedFiles)"

mkfiles "$ROOT2/某番 (2024)/Season 1" "$SMALL"
ST=$(scan "$LIB2_ID")
note "放回后：state=$(state_ "$ST") revivedFiles=$(stat_ "$ST" revivedFiles) issues=$(issues_ "$ST")"
check_true "扫描成功" "$([[ "$(state_ "$ST")" == "done" ]] && echo true || echo false)" "state=$(state_ "$ST")"
check "全量复活 $SMALL 个" "$SMALL" "$(stat_ "$ST" revivedFiles)"
check_true "没有「已被其它条目占用」假警告" "$(no_conflict_ "$ST")"
rm -rf "$ROOT2"

echo
if [[ $fail -eq 0 ]]; then printf '掉盘/复活验收：%d 项全过\n' "$pass"
else printf '掉盘/复活验收：%d 过 / %d 败\n' "$pass" "$fail"; fi
[[ $fail -eq 0 ]]
