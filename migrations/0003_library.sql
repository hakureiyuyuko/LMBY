-- 0003_library.sql：媒体条目、文件、图片、扫描记录
--
-- 设计要点：
--   1. PostgreSQL 是元数据的唯一数据源，不生成 XML/nfo（图片同理，只存路径）；
--   2. media_files.path 全局唯一 —— 一个物理文件只属于一个条目；
--   3. 增量扫描靠 (size_bytes, mtime_ns) 指纹比对，因此这两列必须进索引；
--   4. 「移动文件」通过「指纹相同 + 路径消失」推断，保留条目 id 与播放进度；
--   5. 未在本轮扫描中出现的文件用 deleted_at 软删除，避免误删（网络盘掉线时尤其重要）。

-- 标题模糊检索需要 pg_trgm。它自 PG 13 起是 trusted extension，
-- 数据库属主即可创建，不需要超级用户 —— 所以这里不必要求高权限。
-- 注意：必须在用到 gin_trgm_ops 的索引之前创建。
create extension if not exists pg_trgm;

-- ---------------------------------------------------------------- 条目

create table if not exists media_items (
    id                bigint generated always as identity primary key,
    library_id        bigint      not null references libraries (id) on delete cascade,
    kind              text        not null,                  -- movie | series | season | episode | extra
    parent_id         bigint      references media_items (id) on delete cascade,
    series_id         bigint      references media_items (id) on delete cascade,
    season_number     integer,
    episode_number    integer,
    episode_end       integer,                               -- 双集连播 S01E01E02 的结束集号
    extra_type        text        not null default '',

    title             text        not null default '',
    sort_title        text        not null default '',
    original_title    text        not null default '',
    year              integer,
    overview          text        not null default '',
    tagline           text        not null default '',
    runtime_ticks     bigint,
    community_rating  double precision,
    official_rating   text        not null default '',
    genres            jsonb       not null default '[]'::jsonb,
    tags              jsonb       not null default '[]'::jsonb,
    studios           jsonb       not null default '[]'::jsonb,
    provider_ids      jsonb       not null default '{}'::jsonb,
    premiere_date     date,

    -- 从文件名解析出的技术标记（编码/分辨率/色深/帧率/码率/特性）
    file_tech         jsonb       not null default '{}'::jsonb,

    match_state       text        not null default 'local',  -- local | pending | matched | manual | failed
    locked_fields     jsonb       not null default '[]'::jsonb,
    deleted_at        timestamptz,

    created_at        timestamptz not null default now(),
    updated_at        timestamptz not null default now(),

    constraint media_items_kind_check
        check (kind in ('movie', 'series', 'season', 'episode', 'extra'))
);

create index if not exists media_items_library_kind_idx
    on media_items (library_id, kind) where deleted_at is null;
create index if not exists media_items_parent_idx on media_items (parent_id);
create index if not exists media_items_series_idx
    on media_items (series_id, season_number, episode_number);
create index if not exists media_items_library_idx on media_items (library_id);

-- 同名同年的剧集/电影在同一库里只应存在一个条目，否则重扫会造出重复。
-- 用「表达式 + 部分索引」表达，upsert 时用 select-then-insert 兼顾可读性。
create unique index if not exists media_items_series_uniq
    on media_items (library_id, lower(title), coalesce(year, 0))
    where kind = 'series' and deleted_at is null;
create unique index if not exists media_items_movie_uniq
    on media_items (library_id, lower(title), coalesce(year, 0))
    where kind = 'movie' and deleted_at is null;
create unique index if not exists media_items_season_uniq
    on media_items (parent_id, season_number)
    where kind = 'season' and deleted_at is null;
create unique index if not exists media_items_extra_uniq
    on media_items (parent_id, lower(title))
    where kind = 'extra' and deleted_at is null;
-- 同一剧集内「季+集」应当唯一，防止重复扫描造出重复条目
-- 同一剧集内「季+集」应当唯一，防止重扫造出重复条目。
-- 只用「集号」做键，不用标题：集标题常常来自 nfo 或后续刮削，
-- 若把标题纳入唯一键，刮削一写入新标题就会在下次扫描时造出新条目。
-- 集号未知（NULL）时 PG 会把它们视为互不相同，这是想要的行为。
create unique index if not exists media_items_episode_uniq
    on media_items (series_id, season_number, episode_number)
    where kind = 'episode' and deleted_at is null;
create index if not exists media_items_title_trgm_idx
    on media_items using gin (title gin_trgm_ops);

-- ---------------------------------------------------------------- 文件

create table if not exists media_files (
    id             bigint generated always as identity primary key,
    item_id        bigint      not null references media_items (id) on delete cascade,

    path           text        not null,
    size_bytes     bigint      not null default 0,
    mtime_ns       bigint      not null default 0,

    container      text        not null default '',
    duration_ticks bigint,
    video_streams  jsonb,
    audio_streams  jsonb,
    subtitle_streams jsonb,
    chapters       jsonb,
    hdr            jsonb,

    probe_state    text        not null default 'pending',   -- pending | ok | skipped | failed
    probe_error    text        not null default '',
    probed_at      timestamptz,

    deleted_at     timestamptz,
    created_at     timestamptz not null default now(),
    updated_at     timestamptz not null default now()
);

create unique index if not exists media_files_path_key on media_files (path);
create index if not exists media_files_item_idx on media_files (item_id) where deleted_at is null;
-- 增量扫描的核心：只读「还活着」的文件行，按路径定位
create index if not exists media_files_live_idx on media_files (path) where deleted_at is null;
-- 探测队列用：只扫等待探测的行
create index if not exists media_files_probe_idx
    on media_files (probe_state, id) where probe_state = 'pending' and deleted_at is null;
-- 增量扫描用：按指纹找「移动」的文件
create index if not exists media_files_fingerprint_idx on media_files (size_bytes, mtime_ns);

-- ---------------------------------------------------------------- 图片
--
-- 只登记路径与元信息，二进制永不入库。覆盖顺序由上层决定：
--   媒体同目录本地图 > 用户手动选择 > 远程下载缓存

create table if not exists images (
    id          bigint generated always as identity primary key,
    item_id     bigint      not null references media_items (id) on delete cascade,
    kind        text        not null,      -- poster | fanart | backdrop | banner | logo | thumb | disc | art
    path        text        not null,
    source      text        not null default 'local',   -- local | remote | uploaded
    width       integer,
    height      integer,
    size_bytes  bigint,
    mtime_ns    bigint,
    lang        text        not null default '',
    created_at  timestamptz not null default now()
);

create unique index if not exists images_item_kind_path_key on images (item_id, kind, path);
create index if not exists images_item_idx on images (item_id);

-- ---------------------------------------------------------------- 扫描记录

create table if not exists scan_runs (
    id           bigint generated always as identity primary key,
    library_id   bigint      not null references libraries (id) on delete cascade,
    state        text        not null default 'running',   -- running | done | failed | canceled
    trigger      text        not null default 'manual',    -- manual | schedule | watch
    started_at   timestamptz not null default now(),
    finished_at  timestamptz,
    stats        jsonb       not null default '{}'::jsonb,
    error        text        not null default '',
    constraint scan_runs_state_check
        check (state in ('running', 'done', 'failed', 'canceled'))
);

create index if not exists scan_runs_library_idx on scan_runs (library_id, started_at desc);

create table if not exists scan_issues (
    id          bigint generated always as identity primary key,
    scan_run_id bigint      references scan_runs (id) on delete cascade,
    library_id  bigint      not null references libraries (id) on delete cascade,
    severity    text        not null default 'warning',    -- info | warning | error
    path        text        not null default '',
    message     text        not null,
    created_at  timestamptz not null default now()
);

create index if not exists scan_issues_library_idx on scan_issues (library_id, created_at desc);
