-- 0013_playlists.sql：播放列表 + 合集
--
-- 两者共用一张表，靠 `kind` 区分 —— 它们的**结构完全一样**（一串有序条目 + 名字 + 说明），
-- 差别只在可见性与谁能改：
--
--   * `playlist`  私人：只有主人看得到、只有主人能改；
--   * `collection` 合集：**所有人可见**（管理员整理出来的「宫崎骏作品集」这种），
--                  管理员都能改（M7 之后可以按库授权再收紧）。
--
-- 为什么不做成两张表：那样「加入列表」的接口要复制一份、有序条目的排序逻辑要复制一份，
-- 而唯一的差别（可见性）只是 where 上的一个条件。一张表 + 一条可见性规则更好维护。
--
-- 有序条目用 `sort_order`（整数）而不是链表/浮点：重排是「把整串按新顺序写一遍」，
-- 一次事务、一次写入，没有中间态（链表要改两行、浮点会慢慢退化）。

create table if not exists playlists (
    id         bigserial   primary key,
    user_id    bigint      not null references users (id) on delete cascade,
    name       text        not null,
    kind       text        not null default 'playlist',
    overview   text        not null default '',
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    constraint playlists_kind_check check (kind in ('playlist', 'collection'))
);

-- 同一个人的同名列表只留一个（不区分大小写）；合集之间也同样不重名
create unique index if not exists playlists_owner_name_key
    on playlists (user_id, kind, lower(name));

-- 「我的列表」按最近改动倒序
create index if not exists playlists_owner_idx on playlists (user_id, updated_at desc);

create table if not exists playlist_items (
    playlist_id bigint      not null references playlists (id) on delete cascade,
    item_id     bigint      not null references media_items (id) on delete cascade,
    sort_order  integer     not null default 0,
    added_at    timestamptz not null default now(),
    primary key (playlist_id, item_id)
);

-- 列表内的顺序（也是取「封面条目」用的那条）
create index if not exists playlist_items_order_idx
    on playlist_items (playlist_id, sort_order, added_at);

comment on table playlists is '播放列表与合集（kind 区分）；可见性规则见 internal/store/lists.go';
comment on table playlist_items is '列表内的有序条目；sort_order 是整串重写式的排序，不留空洞也不留中间态';
