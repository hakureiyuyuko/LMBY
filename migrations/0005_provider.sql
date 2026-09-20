-- 0005_provider.sql：元数据刮削（provider）相关
--
-- 设计要点：
--   1. provider_cache 用 (provider, kind, key, lang) 做主键，把 HTTP 响应原样存 jsonb；
--      缓存响应体而不是解析后的模型，这样解析逻辑改了不用重新打 API，
--      而且排查「到底拿到了什么」时能直接看原始数据。
--   2. 有效期由写入方决定（搜索类短、详情类长），过期行由后台任务清理。
--   3. media_items 补上刮削相关的记账字段。

create table if not exists provider_cache (
    provider   text        not null,
    kind       text        not null,   -- movie | tv | season | episode | search-movie | search-tv | images ...
    cache_key  text        not null,   -- 归一化后的请求键
    lang       text        not null default '',
    body       jsonb       not null,
    fetched_at timestamptz not null default now(),
    expires_at timestamptz not null,
    primary key (provider, kind, cache_key, lang)
);

create index if not exists provider_cache_expiry_idx on provider_cache (expires_at);

-- ---------------------------------------------------------------- 条目刮削记账

alter table media_items add column if not exists match_score double precision;
alter table media_items add column if not exists scrape_error text not null default '';
alter table media_items add column if not exists scrape_attempts integer not null default 0;
alter table media_items add column if not exists last_scraped_at timestamptz;
-- metadata_source 记录元数据从哪来：nfo / tmdb / manual / probe
alter table media_items add column if not exists metadata_source text not null default '';

-- 便于「找出这个库里还没刮削的条目」
create index if not exists media_items_match_state_idx
    on media_items (library_id, match_state)
    where deleted_at is null;
