-- 0001_init.sql：LMBY 基础表（用户、偏好、会话、设置、媒体库）
-- 约定：媒体库的每一条记录都以 PostgreSQL 为唯一数据源，不生成 XML/nfo。

-- ---------------------------------------------------------------- 用户

create table if not exists users (
    id                     bigint generated always as identity primary key,
    username               text        not null,
    display_name           text        not null default '',
    password_hash          text        not null,
    is_admin               boolean     not null default false,
    is_disabled            boolean     not null default false,
    max_concurrent_streams integer     not null default 0,   -- 0 = 用全局默认
    created_at             timestamptz not null default now(),
    updated_at             timestamptz not null default now(),
    last_login_at          timestamptz,
    last_login_ip          inet
);

-- 用户名大小写不敏感且唯一
create unique index if not exists users_username_lower_key on users (lower(username));

-- ---------------------------------------------------------------- 用户偏好

create table if not exists user_preferences (
    user_id        bigint primary key references users (id) on delete cascade,
    theme          text        not null default 'system',   -- light | dark | system
    language       text        not null default 'zh-CN',
    subtitle_prefs jsonb       not null default '{}'::jsonb,
    audio_prefs    jsonb       not null default '{}'::jsonb,
    library_views  jsonb       not null default '{}'::jsonb,
    updated_at     timestamptz not null default now()
);

-- ---------------------------------------------------------------- 会话

-- 只存令牌的 sha256，泄露数据库也无法直接冒用会话。
create table if not exists sessions (
    id           text primary key,
    user_id      bigint      not null references users (id) on delete cascade,
    created_at   timestamptz not null default now(),
    last_seen_at timestamptz not null default now(),
    expires_at   timestamptz not null,
    revoked_at   timestamptz,
    user_agent   text        not null default '',
    ip           inet
);

create index if not exists sessions_user_active_idx on sessions (user_id) where revoked_at is null;
create index if not exists sessions_expires_idx on sessions (expires_at);

-- ---------------------------------------------------------------- 全局设置

create table if not exists settings (
    key        text primary key,
    value      jsonb       not null,
    updated_at timestamptz not null default now()
);

-- ---------------------------------------------------------------- 媒体库

create table if not exists libraries (
    id         bigint generated always as identity primary key,
    name       text        not null,
    kind       text        not null,                       -- movie | tv | homevideo | mixed
    options    jsonb       not null default '{}'::jsonb,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    constraint libraries_kind_check check (kind in ('movie', 'tv', 'homevideo', 'mixed'))
);

create table if not exists library_paths (
    id         bigint generated always as identity primary key,
    library_id bigint      not null references libraries (id) on delete cascade,
    path       text        not null,
    readonly   boolean     not null default false,
    sort_order integer     not null default 0,
    created_at timestamptz not null default now()
);

create unique index if not exists library_paths_path_key on library_paths (path);
create index if not exists library_paths_library_idx on library_paths (library_id, sort_order);

-- 库级访问权限；某用户没有任何记录时的语义由上层决定（M7 定）。
create table if not exists user_library_access (
    user_id    bigint not null references users (id) on delete cascade,
    library_id bigint not null references libraries (id) on delete cascade,
    primary key (user_id, library_id)
);
