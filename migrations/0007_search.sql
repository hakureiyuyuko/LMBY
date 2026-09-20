-- 0007_search.sql：条目搜索（中文二元组 bigram）
--
-- 方案（见 docs/REQUIREMENTS.md §2.2）：`pg_trgm` GIN + `tsvector`，
-- 中文用二元组模糊匹配，**不引 zhparser**（那要装 SCWS 与 PG 扩展，是部署负担）。
--
-- 为什么切词放在数据库里：语料是「作品标题」这种短文本，切成什么样完全由输入决定
-- （纯函数，不读库也不读 GUC），所以可以做成**生成列**：写入时自动算好，
-- 永远不会忘，应用侧也不需要维护 —— 「派生数据必须与源数据同步」这类问题的标准解法。
--
-- 检索单元（token）的切法：
--   * CJK 连续段 → 相邻两字一组（"钢之炼金术师" → 钢之 之炼 炼金 金术 术师），单字段落保留单字；
--   * 其余字母数字（英文/数字/带重音）→ 按分隔符切成整词并转小写（"The Matrix" → the、matrix）；
--   * 标点与空白丢弃。
-- 于是中英混排、带副标题的标题（"AVC 1080P《Blood-C 测试片》"）都能拆成可检索的单元。
--
-- 单字查询（如「钢」）不会命中 bigram token，由 ILIKE / trigram 兜底 —— 见 internal/store/search.go。

-- ⚠️ 下面这些函数**一律带 public. 限定名**引用彼此，别图省事去掉：
-- pg_restore 会把 search_path 置空，而生成列的表达式与 SQL 函数体里的函数名
-- 是按当时的 search_path 解析的 —— 不限定的话，「先迁结构、再灌数据」的还原
-- 会在 COPY media_items 时报 “function lmby_search_tokens(text) does not exist”
-- （本次实现时就踩到了），用户自己 pg_dump/pg_restore 也会掉进同一个坑。

-- CJK 判断按**码点**，不用正则里的 \u 转义：PG 各版本对 \u 的支持不一致，
-- 而 ascii() 在 UTF-8 库里返回字符的码点，是稳定行为。
create or replace function public.lmby_is_cjk(cp integer) returns boolean
    language sql immutable parallel safe as $$
    select cp between 13312 and 40959      -- CJK 统一表意（含扩展 A）
        or cp between 63744 and 64255      -- CJK 兼容表意
        or cp between 12352 and 12543      -- 日文假名（平/片假名）
        or cp between 44032 and 55215      -- 韩文音节
$$;

comment on function public.lmby_is_cjk(integer) is '按 Unicode 码点判断是否 CJK（搜索切词用）';

-- 把一段 CJK 切成二元组；单字（或空）原样保留。
create or replace function public.lmby_cjk_bigrams(seg text) returns text[]
    language sql immutable parallel safe as $$
    select case
        when seg is null or seg = '' then '{}'::text[]
        when length(seg) = 1 then array[seg]
        else (select array_agg(substr(seg, i, 2)) from generate_series(1, length(seg) - 1) as i)
    end
$$;

comment on function public.lmby_cjk_bigrams(text) is '把 CJK 连续段切成二元组（中文搜索的最小检索单元）';

-- 文本 → 检索单元数组。这就是「中文分词」全部的实现：两字一组。
create or replace function public.lmby_search_tokens(txt text) returns text[]
    language plpgsql immutable parallel safe as $$
declare
    s          text := coalesce(txt, '');
    n          integer := length(coalesce(txt, ''));
    i          integer := 1;
    c          text;
    out_tokens text[] := '{}';
    run        text := '';   -- 累积中的 CJK 连续段
    word       text := '';   -- 累积中的拉丁 / 数字词
begin
    while i <= n loop
        c := substr(s, i, 1);
        if public.lmby_is_cjk(ascii(c)) then
            if word <> '' then
                out_tokens := out_tokens || lower(word);
                word := '';
            end if;
            run := run || c;
        elsif c ~ '[[:alnum:]]' then
            if run <> '' then
                out_tokens := out_tokens || public.lmby_cjk_bigrams(run);
                run := '';
            end if;
            word := word || c;
        else
            -- 分隔符（空白 / 标点 / 符号）：把手上两段都收掉
            if run <> '' then
                out_tokens := out_tokens || public.lmby_cjk_bigrams(run);
                run := '';
            end if;
            if word <> '' then
                out_tokens := out_tokens || lower(word);
                word := '';
            end if;
        end if;
        i := i + 1;
    end loop;
    if run <> '' then
        out_tokens := out_tokens || public.lmby_cjk_bigrams(run);
    end if;
    if word <> '' then
        out_tokens := out_tokens || lower(word);
    end if;
    return out_tokens;
end
$$;

comment on function public.lmby_search_tokens(text) is '文本 → 检索单元（中文二元组 + 英文整词），搜索索引与查询共用';

-- 查询侧用：单元串（空格分隔），交给 plainto_tsquery（它用 & 连起来）。
-- 索引与查询必须用同一个切法，否则会出现「搜不到但数据明明在」这类怪事。
create or replace function public.lmby_bigram(txt text) returns text
    language sql immutable parallel safe as $$
    select coalesce(array_to_string(public.lmby_search_tokens(txt), ' '), '')
$$;

comment on function public.lmby_bigram(text) is '查询词 → 空格分隔的检索单元串（交给 plainto_tsquery）';

-- 生成列：标题权重 A、原始标题权重 B（排序时标题命中更靠前）。
-- 加列时 PG 会为存量行算一遍（本库几百行，瞬间完成）。
alter table media_items
    add column if not exists search_vec tsvector
        generated always as (
            setweight(to_tsvector('simple', public.lmby_bigram(title)), 'A') ||
            setweight(to_tsvector('simple', public.lmby_bigram(original_title)), 'B')
        ) stored;

comment on column media_items.search_vec is
    '标题 / 原始标题的检索单元（中文二元组 + 英文整词），生成列：写入时自动维护，不入库给人看';

-- tsvector 的 GIN 索引（分词命中那一路）
create index if not exists media_items_search_vec_idx
    on media_items using gin (search_vec);

-- 原始标题也需要 trigram 索引：错字容忍那一路会在它上面查相似
-- （title 的 trigram 索引在 0003 已经建好）
create index if not exists media_items_original_title_trgm_idx
    on media_items using gin (original_title gin_trgm_ops);
