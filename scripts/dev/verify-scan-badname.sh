#!/usr/bin/env bash
# 「文件名含非 UTF-8 字节」的扫描验收（真库 + 真文件系统）。
#
# 为什么单独验这件事：数据库是 UTF-8 的，而 SMB / 网盘共享上的文件名不保证是 UTF-8。
# 2026-09-25 在验证实例上实测到的故障链是：
#   坏名字文件插不进 media_files → 记一条带该路径的 issue → **整批扫描问题写失败**
#   → 扫描被判「失败」，后面几百条问题全丢、耗时/问题数还显示成 0。
# 修复后期望的行为：
#   · 坏名字（文件与目录）在遍历时就被跳过，并各记一条说明（含坏字节的十六进制）；
#   · 扫描照常 **done**，不再 failed；
#   · 正常文件照常入库；坏命名目录里的东西**不**入库（整棵跳过是对的：
#     那种目录下的路径同样存不进去）；
#   · stats.issues / stats.elapsedMs 有真实值（不再出现「耗时 0.0 秒 / 问题 0」）。
#
# 用法：LMBY_USER=<管理员> LMBY_PASS=<口令> bash scripts/dev/verify-scan-badname.sh
#   BASE 默认 http://127.0.0.1:8099（在验证实例本机上跑）
#   ROOT 默认 /tmp/lmby-scan-badname（临时目录，脚本自己建、自己删）
set -u

BASE="${BASE:-http://127.0.0.1:8099}"
ROOT="${ROOT:-/tmp/lmby-scan-badname}"
LIBNAME="坏名字验收 $(date +%s)"

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
  # 别把测试库留在实例上（删库会级联删掉它的扫描记录与问题）
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

echo "== 0. 管理员登录 =="
LOGIN=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -c "$JAR" -X POST "$BASE/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"${LMBY_USER:?}\",\"password\":\"${LMBY_PASS:?}\"}")
check "管理员登录" 200 "$LOGIN"
[[ "$LOGIN" != "200" ]] && exit 1

echo
echo "== 1. 造带坏名字的目录树 =="
rm -rf "$ROOT"
mkdir -p "$ROOT/正常剧集 (2024)/Season 1"
# 200 KB > [scan] min_file_size 默认 64 KiB（太小会被当垃圾跳过，那样验不到入库）
head -c 200000 /dev/zero > "$ROOT/正常剧集 (2024)/Season 1/S01E01.mkv"

# 名字里带裸字节 0xde 0x20（SMB 共享上真实出现过的形态）
BADFILE="$ROOT/"$'x\xde y.mkv'
head -c 200000 /dev/zero > "$BADFILE"
# 坏名字的目录（里面放一个正常名字的视频：整棵目录应当被跳过）
BADDIR="$ROOT/"$'坏\xde 目录'
mkdir -p "$BADDIR"
head -c 200000 /dev/zero > "$BADDIR/藏在里面 S01E01.mkv"

note "正常文件：正常剧集 (2024)/Season 1/S01E01.mkv"
note "坏名字文件：$(cd "$ROOT" && ls | sed -n '1p' | cat -v)"
note "坏名字目录：$(cd "$ROOT" && ls | sed -n '3p' | cat -v)"
check_true "坏名字文件已造出来" "$([[ -f "$BADFILE" ]] && echo true || echo false)"
check_true "坏名字目录已造出来" "$([[ -d "$BADDIR" ]] && echo true || echo false)"

echo
echo "== 2. 建库并扫描 =="
LIB=$(api POST /api/v1/libraries "{\"name\":\"$LIBNAME\",\"kind\":\"tv\",\"paths\":[\"$ROOT\"]}")
LIB_ID=$(jq -r '.library.id // .id // empty' <<<"$LIB")
check_true "建库成功" "$([[ -n "$LIB_ID" ]] && echo true || echo false)" "$LIB"
if [[ -z "$LIB_ID" ]]; then echo "建库失败，后续无法进行"; exit 1; fi

SCAN_CODE=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -c "$JAR" \
  -X POST "$BASE/api/v1/libraries/$LIB_ID/scan" -H 'Content-Type: application/json' -d '{}')
check "触发扫描 → 202" 202 "$SCAN_CODE"

ST=""
for _ in $(seq 1 60); do
  ST=$(api GET "/api/v1/libraries/$LIB_ID/scan")
  [[ "$(jq -r '.running' <<<"$ST")" != "true" ]] && break
  sleep 1
done

STATE=$(jq -r '.lastScan.state // "?"' <<<"$ST")
ERR=$(jq -r '.lastScan.error // ""' <<<"$ST")
echo "  扫描结果：state=$STATE error=${ERR:-（无）}"
jq -c '{issues: (.issues|length), stats: .lastScan.stats}' <<<"$ST" | sed 's/^/  /'

echo
echo "== 3. 断言：扫描成功（这正是本次修的那条） =="
check_true "扫描状态是 done 而不是 failed" "$([[ "$STATE" == "done" ]] && echo true || echo false)" "state=$STATE"
check "失败信息为空" "" "$ERR"

echo
echo "== 4. 断言：坏名字被跳过并记成问题（含十六进制，用户能照着改名） =="
BADN=$(jq -r '[.issues[] | select(.message | test("非 UTF-8"))] | length' <<<"$ST")
check_true "有「含非 UTF-8 字节」的问题记录" "$([[ "$BADN" -ge 2 ]] && echo true || echo false)" "共 $BADN 条（文件 1 + 目录 1）"
check_true "问题里带坏字节的十六进制（0xde 0x20）" \
  "$(jq -r '[.issues[] | select(.message | test("0xde 0x20"))] | length > 0' <<<"$ST")"
check_true "问题里的路径用 U+FFFD 标出了坏字节（而不是把名字丢了）" \
  "$(jq -r '[.issues[] | select(.message | test("非 UTF-8")) | .path | test("\uFFFD")] | any' <<<"$ST")"
note "问题清单里那两条："
jq -r '.issues[] | select(.message | test("非 UTF-8")) | "    [\(.severity)] \(.path)\n      \(.message)"' <<<"$ST"

echo
echo "== 5. 断言：统计数字是真的（不再出现「耗时 0.0 秒 / 问题 0」） =="
check_true "stats.issues > 0" "$(jq -r '(.lastScan.stats.issues // 0) > 0' <<<"$ST")" "issues=$(jq -r '.lastScan.stats.issues // 0' <<<"$ST")"
check_true "stats.elapsedMs > 0" "$(jq -r '(.lastScan.stats.elapsedMs // 0) > 0' <<<"$ST")" "elapsedMs=$(jq -r '.lastScan.stats.elapsedMs // 0' <<<"$ST")"

echo
echo "== 6. 断言：正常文件入库，坏命名目录里的文件不入库 =="
SER=$(api GET "/api/v1/libraries/$LIB_ID/browse")
EPI=$(api GET "/api/v1/libraries/$LIB_ID/items?kind=episode")
ser_n=$(jq -r '(.total // (.items|length)) // 0' <<<"$SER")
epi_n=$(jq -r '(.total // (.items|length)) // 0' <<<"$EPI")
note "顶层条目：$(jq -c '[.items[]?.title]' <<<"$SER")"
note "入库的集：$(jq -c '[.items[]? | {kind, title}]' <<<"$EPI")"
# 这两条是本节真正的证据：坏命名目录整棵被跳过，所以它里面的视频既不会形成条目、
# 也不会多出一集（如果跳过了目录但漏了内容，这里会变成 2）。
check_true "整棵树只有 1 个剧集（坏命名目录没造出条目）" "$([[ "$ser_n" -eq 1 ]] && echo true || echo false)" "series=$ser_n"
check_true "正常那一集已入库，且只有它这一集（episode = 1）" "$([[ "$epi_n" -eq 1 ]] && echo true || echo false)" "episode=$epi_n"

echo
if [[ $fail -eq 0 ]]; then printf '坏名字扫描：%d 项全过\n' "$pass"
else printf '坏名字扫描：%d 过 / %d 败\n' "$pass" "$fail"; fi
[[ $fail -eq 0 ]]
