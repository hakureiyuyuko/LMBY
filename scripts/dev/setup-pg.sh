#!/usr/bin/env bash
# 在开发容器里准备 LMBY 用的 PostgreSQL 角色与数据库，并写一份 /etc/lmby/config.toml。
#
# 幂等：重复执行安全，不会覆盖已有密码（密码存在 /etc/lmby/pg-password）。
# 用法：bash setup-pg.sh
set -euo pipefail

DB_NAME="${DB_NAME:-lmby}"
DB_USER="${DB_USER:-lmby}"
CONFIG_DIR="${CONFIG_DIR:-/etc/lmby}"
CONFIG_FILE="${CONFIG_DIR}/config.toml"
PW_FILE="${CONFIG_DIR}/pg-password"
DATA_DIR="${DATA_DIR:-/var/lib/lmby}"

mkdir -p "$CONFIG_DIR" "$DATA_DIR"

# ---------------------------------------------------------------- 密码

if [[ -f "$PW_FILE" ]]; then
  PW="$(cat "$PW_FILE")"
else
  PW="$(openssl rand -hex 16)"
  printf '%s' "$PW" > "$PW_FILE"
  chmod 600 "$PW_FILE"
fi

pg() { runuser -u postgres -- psql -v ON_ERROR_STOP=1 "$@"; }

# ---------------------------------------------------------------- 角色与库

if ! pg -tAc "select 1 from pg_roles where rolname='${DB_USER}'" | grep -q 1; then
  echo "创建角色 ${DB_USER}"
  pg -c "create role ${DB_USER} login password '${PW}'"
else
  echo "角色 ${DB_USER} 已存在，同步密码"
  pg -c "alter role ${DB_USER} password '${PW}'"
fi

if ! pg -tAc "select 1 from pg_database where datname='${DB_NAME}'" | grep -q 1; then
  echo "创建数据库 ${DB_NAME}（UTF8 + C.UTF-8）"
  # -E UTF8 -T template0 --lc-* 都不能省：宿主 locale 是 C 时（最小化系统默认），
  # 默认建出来的是 SQL_ASCII + C 的库 ——
  #   * 编码不是 UTF8 中文会退化成字节（length('钢')=3、to_tsvector 认不出中文）；
  #   * lc_ctype 是 C 时 pg_trgm 切不出中文三元组（模糊匹配静默失效）。
  # 详见 internal/store/store.go 的 checkEncoding 与 scripts/dev/fix-db-encoding.sh。
  runuser -u postgres -- createdb -E UTF8 --lc-collate="${LOCALE:-C.UTF-8}" \
    --lc-ctype="${LOCALE:-C.UTF-8}" -T template0 -O "${DB_USER}" "${DB_NAME}"
else
  echo "数据库 ${DB_NAME} 已存在"
fi

# 字符集自检：不满足就别往下走了，省得等到做搜索才发现
DB_ENC=$(pg -tA -d postgres -c "select pg_encoding_to_char(encoding) from pg_database where datname='${DB_NAME}'")
DB_CTYPE=$(pg -tA -d postgres -c "select datctype from pg_database where datname='${DB_NAME}'")
if [[ "$DB_ENC" != "UTF8" || "$(echo "$DB_CTYPE" | tr 'A-Z' 'a-z')" != *utf* ]]; then
  echo "!! 数据库 ${DB_NAME} 的字符集不满足要求：encoding=${DB_ENC} lc_ctype=${DB_CTYPE}"
  echo "   需要 UTF8 + 一个 UTF-8 的 ctype（推荐 C.UTF-8）"
  echo "   停掉服务后跑 scripts/dev/fix-db-encoding.sh 就地重建（会备份与逐表比对行数），"
  echo "   或者删库重建（数据都能从媒体目录重新扫描出来）"
  exit 1
fi
# 不带扩展的 ctype 探针：C locale 下「钢」不是字母，C.UTF-8 下是
if [[ "$(pg -tA -d "${DB_NAME}" -c "select ('钢' ~ '[[:alpha:]]')")" != "t" ]]; then
  echo "!! lc_ctype=${DB_CTYPE} 不认识汉字（pg_trgm 会切不出中文三元组）"
  exit 1
fi

# ---------------------------------------------------------------- 配置文件

cat > "$CONFIG_FILE" <<TOML
# LMBY 开发环境配置（由 scripts/dev/setup-pg.sh 生成）
listen = ":8099"
data_dir = "${DATA_DIR}"
log_level = "info"
secure_cookies = false
session_ttl_hours = 720

[database]
dsn = "postgres://${DB_USER}:${PW}@127.0.0.1:5432/${DB_NAME}?sslmode=disable"
max_conns = 10
auto_migrate = true

[ffmpeg]
path = "ffmpeg"
probe_path = "ffprobe"
TOML
chmod 600 "$CONFIG_FILE"

# ---------------------------------------------------------------- 自检

PGPASSWORD="$PW" psql -h 127.0.0.1 -U "${DB_USER}" -d "${DB_NAME}" -tAc \
  "select 'pg ok: ' || current_database() || ' / ' || current_user || ' / ' || current_setting('server_encoding')"

echo "配置已写入: ${CONFIG_FILE}"
