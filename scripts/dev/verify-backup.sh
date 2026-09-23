#!/usr/bin/env bash
# 备份 / 恢复的真库验收。
#
# 验四件事：
#   ① 备份包里确实有该有的东西（MANIFEST + db.dump + 配置 + 密钥）；
#   ② MANIFEST 说的版本/schema 版本是真的；
#   ③ **不给 --yes 必须拒绝**（恢复会清空目标库，这是不可逆的）；
#   ④ 恢复到另一个库之后，关键表的行数与源库一致 —— 这才是「备份有用」的硬证据。
#
# 恢复到**临时库**（不是踩正在用的那个），跑完删掉。
#
# 用法（在跑着服务的机器上，需要 root 以便 su postgres 建临时库）：
#   bash scripts/dev/verify-backup.sh [--keep]
set -uo pipefail

BASE="${BASE:-http://127.0.0.1:8099}"
DATA_DIR="${DATA_DIR:-/var/lib/lmby}"
PGPASS_FILE="${PGPASS_FILE:-/etc/lmby/pg-password}"
TMP_LIB="lmby_backup_verify_$$"
PACK="/tmp/lmby-backup-verify-$$.tar.gz"
KEEP=0
[[ "${1:-}" == "--keep" ]] && KEEP=1

pass=0; fail=0
check() {
  if [[ "$2" == "$3" ]]; then printf '  ok   %s\n' "$1"; pass=$((pass + 1))
  else printf '  FAIL %s（期望 %q，实际 %q）\n' "$1" "$2" "$3"; fail=$((fail + 1)); fi
}
note() { printf '  --   %s\n' "$1"; }
has() { # has <名字> <子串> <内容>
  if [[ "$3" == *"$2"* ]]; then printf '  ok   %s\n' "$1"; pass=$((pass + 1))
  else printf '  FAIL %s（没找到 %q）\n' "$1" "$2"; fail=$((fail + 1)); fi
}
cleanup() {
  [[ $KEEP == 1 ]] && { note "保留产物：$PACK 与库 $TMP_LIB"; return; }
  rm -f "$PACK" "$PACK.out"
  rm -rf "/tmp/lmby-restore-verify-$$"
  su - postgres -c "psql -q -c 'drop database if exists $TMP_LIB'" >/dev/null 2>&1
}
trap cleanup EXIT

if ! command -v lmby >/dev/null; then
  echo "找不到 lmby（要在跑着服务的机器上跑这个脚本）" >&2
  exit 2
fi
if [[ ! -r "$PGPASS_FILE" ]]; then
  echo "读不到 $PGPASS_FILE（需要 root 或相应权限）" >&2
  exit 2
fi
PW=$(cat "$PGPASS_FILE")

echo "== 1. 备份 =="
if ! lmby backup -o "$PACK" >"$PACK.out" 2>&1; then
  echo "备份失败："; cat "$PACK.out"; exit 1
fi
check "备份文件已生成" true "$([[ -s "$PACK" ]] && echo true || echo false)"
LIST=$(tar -tzf "$PACK")
has "包里有 MANIFEST.json" "MANIFEST.json" "$LIST"
has "包里有 db.dump" "db.dump" "$LIST"
has "包里有 secret.key（凭据要靠它解）" "secret.key" "$LIST"
note "包内容：$(echo "$LIST" | tr '\n' ' ')"

MF=$(tar -xzOf "$PACK" MANIFEST.json)
check "MANIFEST 格式版本为 1" 1 "$(echo "$MF" | jq -r '.formatVersion')"
FV=$(echo "$MF" | jq -r '.schemaVersion')
check "MANIFEST 记下了 schema 版本（>0）" true "$([[ "${FV:-0}" -gt 0 ]] && echo true || echo false)"
note "备份自：$(echo "$MF" | jq -r '.lmbyVersion')，schemaVersion=$FV"

echo
echo "== 2. 准备一个临时库 =="
su - postgres -c "psql -q -c 'drop database if exists $TMP_LIB' -c \"create database $TMP_LIB owner lmby\"" >/dev/null
DSN_TEST="postgres://lmby:$PW@127.0.0.1:5432/$TMP_LIB?sslmode=disable"
check "临时库已建好" 1 "$(su - postgres -c "psql -tAc \"select 1 from pg_database where datname='$TMP_LIB'\"" | tr -d ' ')"

echo
echo "== 3. 不给 --yes 必须拒绝（不可逆动作的闸门） =="
if lmby restore -i "$PACK" --dsn "$DSN_TEST" >/tmp/lmby-restore-noyes-$$.out 2>&1; then
  printf '  FAIL 没带 --yes 却执行了恢复\n'; fail=$((fail + 1))
else
  printf '  ok   没带 --yes 被拒绝\n'; pass=$((pass + 1))
fi
has "拒绝时给出了「加 --yes」的提示" "--yes" "$(cat /tmp/lmby-restore-noyes-$$.out)"
rm -f /tmp/lmby-restore-noyes-$$.out

echo
echo "== 4. 真的恢复一次，并对行数 =="
rm -rf "/tmp/lmby-restore-verify-$$"
if lmby restore -i "$PACK" --dsn "$DSN_TEST" --yes --data-dir "/tmp/lmby-restore-verify-$$" >"$PACK.out" 2>&1; then
  printf '  ok   恢复成功\n'; pass=$((pass + 1))
else
  printf '  FAIL 恢复失败：\n'; sed 's/^/       /' "$PACK.out"; fail=$((fail + 1))
fi
for t in users libraries media_items; do
  a=$(su - postgres -c "psql -d lmby -tAc \"select count(*) from $t\"" 2>/dev/null | tr -d ' ')
  b=$(su - postgres -c "psql -d $TMP_LIB -tAc \"select count(*) from $t\"" 2>/dev/null | tr -d ' ')
  check "$t 行数与源库一致（$a）" "$a" "$b"
done
check "恢复写出的 secret.key 权限为 600" 600 \
  "$(stat -c '%a' "/tmp/lmby-restore-verify-$$/secret.key" 2>/dev/null || echo missing)"
has "日志里没有明文口令" "lmby:***@" "$(cat "$PACK.out" | tr -d '\n')"

echo
echo "================ 结果：$pass 通过 / $fail 失败 ================"
[[ "$fail" -eq 0 ]]
