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
  echo "创建数据库 ${DB_NAME}"
  runuser -u postgres -- createdb -O "${DB_USER}" "${DB_NAME}"
else
  echo "数据库 ${DB_NAME} 已存在"
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
  "select 'pg ok: ' || current_database() || ' / ' || current_user"

echo "配置已写入: ${CONFIG_FILE}"
