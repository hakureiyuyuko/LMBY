-- 0009_livetv.sql：直播电视（源 + 频道 + 收藏）
--
-- 设计要点：
--   1. 源与频道分成两张表：一次导入（粘贴 / 上传 / 订阅 URL）产出一批频道，
--      而「这条频道是哪次导入来的」对用户没用 —— 但「这个订阅源上次刷新成不成功」
--      很有用，所以来源信息留在 tv_sources，频道只挂一个可空的 source_id；
--      删除源**不删频道**（set null），因为用户可能只想不再自动刷新，不是想丢频道；
--   2. 频道以 **url 为自然键**：m3u 里地址就是频道的身份，「按地址增量更新」
--      （保留启用状态/收藏/探测结果）靠它做 upsert；地址变了就是另一条频道；
--   3. 启用状态、收藏、探测结果都是**用户态**，导入时一律不覆盖
--      （否则每次刷新订阅源都会把用户手动停用的频道又打开）；
--   4. 探测结果是三态：probe 为空 = 没探过；probe_ok = false = 探过但不通
--      （界面上灰掉/标红）；true = 通。timezone 用 probe_at 让界面显示「多久前探的」；
--   5. 收藏按 (user_id, channel_id) 存，为 M7 多用户留位置：现在的单用户就是一行。

create table if not exists tv_sources (
    id                      bigserial   primary key,
    name                    text        not null,
    kind                    text        not null,   -- paste / file / url
    url                     text        not null default '',  -- kind=url 时的订阅地址
    enabled                 boolean     not null default true,
    -- 自动刷新间隔（分钟）；0 = 不自动刷新（只有手动点刷新）
    refresh_interval_minutes integer    not null default 0,
    last_refresh_at         timestamptz,
    last_status             text        not null default '',  -- ok / error（给人看的摘要）
    last_channel_count      integer     not null default 0,
    created_at              timestamptz not null default now(),
    updated_at              timestamptz not null default now()
);

create table if not exists tv_channels (
    id          bigserial   primary key,
    source_id   bigint      references tv_sources (id) on delete set null,
    name        text        not null,
    url         text        not null,
    group_name  text        not null default '',
    logo        text        not null default '',
    tvg_id      text        not null default '',
    -- 源站要求的自定义请求头（m3u 里 `地址|User-Agent=xxx&Referer=yyy` 那种写法）
    headers     text        not null default '',
    sort_order  integer     not null default 0,
    disabled    boolean     not null default false,
    probe       text        not null default '',
    probe_ok    boolean,
    probe_at    timestamptz,
    created_at  timestamptz not null default now(),
    updated_at  timestamptz not null default now()
);

-- 地址是频道的身份：导入时按它 upsert
create unique index if not exists tv_channels_url_key on tv_channels (url);

-- 频道列表的默认排序（分组内按 sort_order，再按 id 稳定）
create index if not exists tv_channels_order_idx on tv_channels (group_name, sort_order, id);

-- 频道名搜索：中文三元组需要 pg_trgm（库里已启用；见 0007_search.sql）
create index if not exists tv_channels_name_trgm_idx on tv_channels using gin (name gin_trgm_ops);

create table if not exists tv_favorites (
    user_id    bigint      not null references users (id) on delete cascade,
    channel_id bigint      not null references tv_channels (id) on delete cascade,
    created_at timestamptz not null default now(),
    primary key (user_id, channel_id)
);
