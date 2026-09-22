-- 0011_search_people.sql：演职员也进搜索（人名可搜、可联想、可当筛选条件）
--
-- 背景：0007 给 media_items 做了中文二元组搜索，人的名字一直搜不到。
-- M6 的搜索要「即时联想 + 结果分面（电影/剧集/人）」，所以这里把人名接上
-- **同一套**切词函数（public.lmby_bigram）—— 人名与标题的命中行为因此完全一致：
-- 「宫崎」能搜到「宫崎骏」，英文名按整词，错字靠 trigram 兜底。
--
-- 为什么是生成列：与 media_items.search_vec 同理 —— 派生数据必须与源数据同步，
-- 最省心的做法是让数据库自己算（insert on conflict do update 里改 name 时自动重算）。
--
-- ⚠️ 函数名一律 public. 限定（pg_restore 会把 search_path 置空，
-- 生成列表达式里的函数名按当时的 search_path 解析）—— 见 0007 里的同一段注释。

alter table people
    add column if not exists search_vec tsvector
        generated always as (
            setweight(to_tsvector('simple', public.lmby_bigram(name)), 'A')
        ) stored;

comment on column people.search_vec is
    '人名的检索单元（中文二元组 + 英文整词），生成列；查询见 internal/store/search.go';

-- 分词命中那一路的索引
create index if not exists people_search_vec_idx
    on people using gin (search_vec);

-- 子串兜底（单字查询）与词相似（错字容忍）都要它：
-- 只有 lc_ctype 认识中文的库（如 C.UTF-8）trigram 才切得出中文三元组，
-- lc_ctype=C 的库这一路会静默失效（见 internal/store/store.go 的 checkEncoding）。
create index if not exists people_name_trgm_idx
    on people using gin (name gin_trgm_ops);

-- 「按人找作品」：0010 已有 item_people (person_id) 索引，但那条查询还要
-- join 回 media_items 过滤 deleted_at 与 kind，把 item_id 一起放进索引可以少回表。
create index if not exists item_people_person_item_idx on item_people (person_id, item_id);
