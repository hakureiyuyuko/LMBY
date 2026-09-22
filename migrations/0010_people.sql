-- 0010_people.sql：演职员（people + item_people）
--
-- 为什么现在才做：nfo 解析器从 M1 起就在读 <actor> / <director> / <credits>，
-- 但**一直没落库**（解析出来的 md.People 被丢掉了）。M6 的详情页要用它，
-- 于是把这条链补上。
--
-- 设计要点：
--   1. 人与关系分两张表：同一个人会出现在很多条目里（一部剧的每一集都是同一批演员），
--      people 只存一份，item_people 存「在这条里演什么」；
--   2. **去重键** dedupe_key：有外部 id（tmdb/imdb/tvdb）就按它 ——
--      同名不同人不能混成一个人；都没有才退化成规范化后的名字；
--   3. 演职员是「一对多 + 每人还有角色」，塞进 media_items 的一列 jsonb 也能用，
--      但「按演员找他参演的其他作品」这类查询就只能全表扫了 ——
--      两张表是 M7（多用户）/M8（导入器）的基础。
--
-- 数据来源目前**只有 nfo**（本地优先）：TMDB 的 credits 还没接，
-- 所以没 nfo 的条目这一块是空的（见 docs/ROADMAP.md 的 M6 清单）。

create table if not exists people (
    id           bigserial   primary key,
    name         text        not null,
    -- 外部 id（tmdb / imdb / tvdb）：nfo 的 <actor> 里带什么就存什么
    provider_ids jsonb       not null default '{}'::jsonb,
    dedupe_key   text        not null,
    created_at   timestamptz not null default now(),
    updated_at   timestamptz not null default now()
);

create unique index if not exists people_dedupe_key_key on people (dedupe_key);

create table if not exists item_people (
    item_id    bigint  not null references media_items (id) on delete cascade,
    person_id  bigint  not null references people (id) on delete cascade,
    -- actor / director / writer / …（nfo 的 <type> 写什么就是什么，缺省 actor）
    role       text    not null default 'actor',
    -- 演的是谁（演员才有，幕后为空串）
    character  text    not null default '',
    sort_order integer not null default 0,
    primary key (item_id, person_id, role)
);

-- 条目页按顺序读（演员在前是排序里做的，这里只保证同一个人同一条目只有一行）
create index if not exists item_people_item_idx on item_people (item_id, sort_order);

-- 「这个人还演过什么」：留给后面的演员页/相关推荐
create index if not exists item_people_person_idx on item_people (person_id);
