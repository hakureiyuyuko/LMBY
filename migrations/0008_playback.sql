-- 0008_playback.sql：播放进度
--
-- 设计要点：
--   1. 主键是 (user_id, item_id) —— 进度天然是「每人每片」的状态，
--      「看了多少」与「看没看完」放同一行，避免两处状态打架
--      （分开存会导致「标记已看」之后进度条还停在第 3 分钟）；
--   2. 剧集只记「集」这一条：季/剧集的聚合进度现算（见 store.ListContinueWatching），
--      这样重看某一集不会把整季的已看状态冲掉；
--   3. 位置与时长统一用 tick（1 tick = 100ns），与 media_files.duration_ticks、
--      media_items.runtime_ticks 同单位，避免浮点秒在往返中丢精度；
--   4. last_played_at 只记「真正开始播」的时间（心跳更新 updated_at），
--      首页「继续观看」按它倒序，不会因为长时间暂停而反复跳到最前面。

create table if not exists playback_progress (
    user_id        bigint      not null references users (id) on delete cascade,
    item_id        bigint      not null references media_items (id) on delete cascade,

    position_ticks bigint      not null default 0,
    duration_ticks bigint      not null default 0,
    played         boolean     not null default false,
    play_count     integer     not null default 0,

    last_played_at timestamptz,
    updated_at     timestamptz not null default now(),

    primary key (user_id, item_id)
);

-- 「继续观看」列表：只看没看完的，按最近播放倒序。
create index if not exists playback_progress_continue_idx
    on playback_progress (user_id, last_played_at desc nulls last)
    where played = false;

-- 「某条目被谁看过 / 已看人数」这类反查
create index if not exists playback_progress_item_idx
    on playback_progress (item_id);
