-- 0006_scrape.sql：刮削结果的落库字段
--
-- 0005 已经预留了 scrape_error / scrape_attempts / last_scraped_at / metadata_source，
-- 这里只补一件东西：进人工队列时把候选存下来。
--
-- 为什么要存候选：人工匹配界面不能让用户「重新搜一遍再挑」——
-- 那等于把已经花掉的 API 调用浪费掉，而且用户看到的候选会随 TMDB 数据变化、
-- 与当初自动匹配的判断对不上。把候选（连同每项的打分明细）存下来之后，
-- 界面直接展示即可，也给「为什么是这条」留了证据。

alter table media_items
    add column if not exists match_candidates jsonb not null default '[]'::jsonb;

-- match_state 的取值（0003 的注释只列了五个，现在补齐）：
--   local   —— 只有扫描/nfo 得到的信息，还没刮
--   matched —— 自动匹配成功，元数据来自 provider
--   review  —— 有像样的候选但不够确定，等人工确认
--   manual  —— 人工指定过或字段被锁，重扫不覆盖
--   failed  —— 找不到候选 / 反复失败
-- （pending 预留给「已入队待刮」的展示，当前实现不设置它：队列本身就能看到待办数。）
comment on column media_items.match_state is
    'local | matched | review | manual | failed';
comment on column media_items.match_candidates is
    '进人工队列时的候选与打分明细（JSON 数组），供人工匹配界面直接使用';
comment on column media_items.locked_fields is
    '被人工锁定、重扫时不允许覆盖的字段名（JSON 数组）';
