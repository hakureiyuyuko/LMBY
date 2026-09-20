#!/usr/bin/env bash
# 把 LMBY 的数据库重建成「UTF8 编码 + UTF-8 字符类（lc_ctype）」。
#
# 为什么需要这个脚本：非 Docker 安装（apt 装 PostgreSQL）时，如果宿主 locale 是 C
# （很多最小化系统就是这样），`createdb` 建出来的库是 **SQL_ASCII + C**。
# 这种库"看起来"一切正常（Go 写进去的 UTF-8 字节原样存、原样取），但：
#
#   * server_encoding=SQL_ASCII → 所有**字符语义**退化成字节语义：
#     length('钢') = 3、ascii('钢') = 233、lower() 对非 ASCII 无效、
#     to_tsvector 完全认不出 CJK（中文搜索必然失效）；
#   * lc_ctype=C → 即使编码是 UTF8，pg_trgm 也**切不出中文三元组**
#     （show_trgm('某科学的超电磁炮') 是空的），
#     「中文模糊匹配 / 错字容忍」这一整路静默失效。
#
# 两件事都不会报错，只会在搜索/排序的某一刻给出错误结果 —— 所以要在做搜索之前修掉。
# 应用侧也有一道开机自检（internal/store/store.go 的 checkEncoding），
# 不满足就直接拒绝启动并指向本脚本。
#
# 安全性：
#   - 先 dump 一份（默认落在 postgres 能读到的地方），旧库**改名保留**而不是删掉；
#   - 还原后逐表比对行数、并做字符语义与中文三元组自检；
#   - 任何一步不对都给出回滚命令；
#   - 服务需要先停（脚本会检查，不会替你停）。
#
# 用法（在数据库所在机器上，通常就是 LMBY 容器里）：
#   systemctl stop lmby
#   bash scripts/dev/fix-db-encoding.sh
#   systemctl start lmby
#
# 环境变量：DB_NAME / DB_USER / LOCALE（默认 C.UTF-8）/ DUMP / LMBY_BIN
set -uo pipefail

DB_NAME="${DB_NAME:-lmby}"
DB_USER="${DB_USER:-lmby}"
LOCALE="${LOCALE:-C.UTF-8}"
LMBY_BIN="${LMBY_BIN:-/usr/local/bin/lmby}"
# 备份默认落在 postgres 用户能读到的地方：还原是用 postgres 跑的，
# 若放在 /root（0700）会得到「Permission denied」——踩过。
DUMP="${DUMP:-/var/lib/postgresql/${DB_NAME}-utf8-$(date +%Y%m%d-%H%M%S).dump}"

as_pg()  { runuser -u postgres -- "$@"; }
q_pg()   { as_pg psql -tAq -d postgres -c "$1"; }
enc_of()   { q_pg "select pg_encoding_to_char(encoding) from pg_database where datname = '$1'"; }
ctype_of() { q_pg "select datctype from pg_database where datname = '$1'"; }
q_db()   { as_pg psql -tAq -d "$1" -c "$2"; }

# 逐表行数（用 query_to_xml 绕过去，免得写成一大串 count(*)）。
table_counts() {
  q_db "$1" "
    select relname || '=' ||
           (xpath('/row/c/text()',
                  query_to_xml(format('select count(*) as c from %I.%I', schemaname, relname),
                               false, true, '')))[1]::text
    from pg_stat_user_tables
    order by relname"
}

# 字符语义与中文三元组自检：这两件事「不查就不会报错」，所以固定在脚本里验。
self_checks() {
  local ok=1
  echo
  echo "== 字符语义与中文三元组自检 =="
  probe() { # 名字 SQL 期望
    local got
    got=$(q_db "$DB_NAME" "$2")
    if [[ "$got" == "$3" ]]; then
      printf '  \033[32mok\033[0m   %s（%s）\n' "$1" "$got"
    else
      printf '  \033[31mFAIL\033[0m %s（期望 %s，实际 %s）\n' "$1" "$3" "$got"
      ok=0
    fi
  }
  probe "按字符计数"        "select length('钢')"                       1
  probe "子串按字符切"      "select substring('钢之炼金术师' from 3 for 2)" 炼金
  probe "lc_ctype 认识汉字" "select ('钢' ~ '[[:alpha:]]')"              t

  local has_trgm trgm
  has_trgm=$(q_db "$DB_NAME" "select count(*) from pg_extension where extname = 'pg_trgm'")
  if [[ "$has_trgm" == "1" ]]; then
    trgm=$(q_db "$DB_NAME" "select show_trgm('某科学的超电磁炮')")
    if [[ -n "$trgm" && "$trgm" != "{}" ]]; then
      printf '  \033[32mok\033[0m   中文三元组可切（pg_trgm 模糊匹配可用）\n'
    else
      printf '  \033[31mFAIL\033[0m pg_trgm 切不出中文三元组（show_trgm 返回空）\n'
      ok=0
    fi
  else
    printf '  \033[33m--\033[0m   pg_trgm 还没装（首次启动时由迁移 0003 装），跳过三元组自检\n'
  fi
  return $((1 - ok))
}

rollback_hint() {
  echo
  echo "回滚（把旧库改回来）："
  echo "  runuser -u postgres -- psql -d postgres -c 'drop database \"$DB_NAME\"'"
  echo "  runuser -u postgres -- psql -d postgres -c 'alter database \"$OLD\" rename to \"$DB_NAME\"'"
}

echo "== 检查当前状态 =="
CUR_ENC="$(enc_of "$DB_NAME")"
CUR_CTYPE="$(ctype_of "$DB_NAME")"
if [[ -z "$CUR_ENC" ]]; then
  echo "数据库 $DB_NAME 不存在"
  exit 1
fi
echo "encoding=$CUR_ENC  lc_ctype=$CUR_CTYPE"

NEED_FIX=0
[[ "$CUR_ENC" == "UTF8" ]] || NEED_FIX=1
[[ "$(echo "$CUR_CTYPE" | tr 'A-Z' 'a-z')" == *utf* ]] || NEED_FIX=1
if [[ "$NEED_FIX" -eq 0 ]]; then
  echo "已经满足要求（UTF8 + $CUR_CTYPE），不用重建 —— 跑一遍自检确认："
  if self_checks; then
    echo "自检通过"
  else
    echo "自检没过：字符集看着对，但功能不对，请把上面的 FAIL 贴出来"
    exit 1
  fi
  exit 0
fi

echo
echo "== 检查服务是否已停 =="
if pgrep -x lmby >/dev/null 2>&1; then
  echo "!! lmby 进程还在跑（pid $(pgrep -x lmby | tr '\n' ' '))，先 systemctl stop lmby"
  exit 1
fi

echo
echo "== 备份（pg_dump -Fc，客户端编码 UTF8：SQL_ASCII→UTF8 是无转换的字节直通）=="
as_pg pg_dump -Fc -E UTF8 -d "$DB_NAME" >"$DUMP" || {
  echo "!! pg_dump 失败"
  exit 1
}
ls -la "$DUMP"

echo
echo "== 旧库改名保留 =="
OLD="${DB_NAME}_old_$(date +%Y%m%d%H%M%S)"
as_pg psql -v ON_ERROR_STOP=1 -d postgres -c "alter database \"$DB_NAME\" rename to \"$OLD\"" || exit 1
echo "旧库现在是：$OLD"

echo
echo "== 用 UTF8 + $LOCALE 重建 =="
# -T template0 是必须的：template1 的编码/locale 跟集群默认走（就是出问题的那个）。
# lc_ctype 必须是 UTF-8 的，否则 pg_trgm 切不出中文三元组（见文件头）。
as_pg createdb -E UTF8 --lc-collate="$LOCALE" --lc-ctype="$LOCALE" -T template0 \
  -O "$DB_USER" "$DB_NAME" || {
  echo "!! 建库失败（$LOCALE 这个 locale 可能不存在，locale -a 看看）"
  rollback_hint
  exit 1
}
echo "新库：encoding=$(enc_of "$DB_NAME")  lc_ctype=$(ctype_of "$DB_NAME")"

echo
echo "== 还原：先用应用自己的迁移建结构，再灌数据 =="
# 为什么不直接 pg_restore 整个 dump：生成列 search_vec 的表达式依赖迁移里建的
# lmby_bigram()，而 pg_dump **不会为生成列记录这个依赖** —— pg_restore 会把
# CREATE TABLE 排在 CREATE FUNCTION 之前，建表直接失败（实测踩到：
# 报「relation public.media_items does not exist」，后面 27 个索引/外键全跟着挂）。
# 结构本来就该由迁移定义，所以先用 lmby migrate 建好，再 --data-only 灌数据：
# 顺序问题消失，而且迁移比 dump 新时还能顺带把结构升级上去。
# （注意用的是**新二进制**：旧版本没有修好那个 search_path 问题。）
"$LMBY_BIN" migrate || {
  echo "!! $LMBY_BIN migrate 失败（确认二进制与 /etc/lmby/config.toml 在位）"
  rollback_hint
  exit 1
}

# pg_restore 没有 pg_dump 的 --exclude-table-data，而 schema_migrations 的数据本来
# 就不该灌：新库的结构已由 migrate 建好、校验和也记好了，再从 dump COPY 一遍只会
# 撞主键报错（那个报错还会连带跳过该表后续行）。所以用 -L 过一遍目录清单挑出去。
as_pg pg_restore -l "$DUMP" | grep -v 'TABLE DATA public schema_migrations' >/tmp/lmby-restore.list
as_pg env PGCLIENTENCODING=UTF8 pg_restore -L /tmp/lmby-restore.list -d "$DB_NAME" \
  --data-only --disable-triggers --no-owner --role="$DB_USER" "$DUMP" \
  || echo "!! pg_restore --data-only 有报错（见上）"

# identity 列的下一个值不会跟着 COPY 走，不对齐就会在下次插入时撞主键 —— 必须修。
as_pg psql -q -v ON_ERROR_STOP=1 -d "$DB_NAME" <<'SQL'
do $$
declare r record;
begin
  for r in
    select table_name, column_name,
           pg_get_serial_sequence(quote_ident(table_name), column_name) as seq
    from information_schema.columns
    where table_schema = 'public' and is_identity = 'YES'
  loop
    if r.seq is not null then
      execute format(
        'select setval(%L, coalesce((select max(%I) from public.%I), 1), ' ||
        '(select max(%I) from public.%I) is not null)',
        r.seq, r.column_name, r.table_name, r.column_name, r.table_name);
    end if;
  end loop;
end $$;
SQL
echo "identity 序列已对齐"

echo
echo "== 逐表比对行数 =="
BEFORE="$(table_counts "$OLD")"
AFTER="$(table_counts "$DB_NAME")"
if [[ -z "$BEFORE" || -z "$AFTER" ]]; then
  echo "!! 行数比对没比出东西（查询或库不对劲），当成失败处理"
  rollback_hint
  exit 1
fi
if [[ "$BEFORE" != "$AFTER" ]]; then
  echo "!! 行数不一致，别急着启动服务"
  diff <(echo "$BEFORE") <(echo "$AFTER") || true
  rollback_hint
  exit 1
fi
echo "行数一致："
sed 's/^/  /' <<<"$AFTER"

if ! self_checks; then
  echo
  echo "!! 自检没过，先别启动服务"
  rollback_hint
  exit 1
fi

cat <<EOF

完成。
  备份：$DUMP
  旧库：$OLD（确认新库没问题后再删：runuser -u postgres -- psql -d postgres -c 'drop database "$OLD"'）
  下一步：systemctl start lmby
EOF
