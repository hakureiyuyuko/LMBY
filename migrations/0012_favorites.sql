-- 0012_favorites.sql：收藏
--
-- 归属：**按账号**（`user_id`）。M7 的多用户要的就是这个语义 ——
-- 妈妈收藏的动画片不该出现在孩子的「我的收藏」里。
-- 全站视角的那一半（「这条被多少人收藏了」）用同一个表的反查索引回答，
-- 不需要第二张表也不需要计数器（收藏是低频写、高频读的典型，计数器反而要处理漂移）。
--
-- 为什么不用 `media_items` 上的布尔列或 jsonb：那是「单人版」的写法，
-- 多用户一上来就得推倒重来（而且 jsonb 里放用户 id 数组会让「我收藏的」变成全表扫）。

create table if not exists favorites (
    user_id    bigint      not null references users (id) on delete cascade,
    item_id    bigint      not null references media_items (id) on delete cascade,
    created_at timestamptz not null default now(),
    primary key (user_id, item_id)
);

-- 「我的收藏」列表：按收藏时间倒序翻页
create index if not exists favorites_user_created_idx on favorites (user_id, created_at desc);

-- 「这条被谁收藏了 / 收藏数」以及删除条目时的级联查找
create index if not exists favorites_item_idx on favorites (item_id);

comment on table favorites is '收藏（按账号）：主键 (user_id, item_id) 保证重复收藏幂等';
