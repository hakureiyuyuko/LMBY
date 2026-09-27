-- 0019_item_scan_key.sql：给条目记一个「扫描器专用的身份」（scan_key）
--
-- 解决的问题（2026-09-27 在验证实例上查清）：
--   条目身份原本是 `(库, 类型, 标题, 年份)`，而**标题与年份都会被 nfo / 刮削改写**
--   （见 internal/store/items.go 的 applyItemMetaSQL：title / year 都在它手里）。
--   扫描器却是拿**目录名解析出来的标题**去查，于是：
--     · nfo 的标题与目录名不同（`公主连结！ReDive` vs `Re:Dive`、`战姬绝唱` vs
--       `战姬绝唱Symphogear`）→ 查不到自己 → 凭空再造一份同名剧集；
--     · 那份新条目通常没有文件（path 唯一约束还被原条目占着），界面上就是
--       「集没标题、点了放不了」。
--   触发条件只需要「给已有剧集加一个文件」，不需要任何故障。
--
-- 做法：把「扫描器第一次见到它时用的那个身份」原样存下来，此后只由扫描器读写，
-- 元数据（nfo / 刮削 / 人工编辑）**碰不到它**。查找顺序变成：
--   ① scan_key 精确命中 → 用它（稳定，元数据怎么改都不影响）
--   ② (标题, 年份) 精确命中 → 兼容老数据，并顺手补上 scan_key
--   ③ 同库同类型里只有一个「标题高度相似 + 年份相容」的候选 → 认领它并补 scan_key
--   ④ 都没有 → 新建（写入 scan_key）
--
-- 键的写法与扫描器内存里的去重键一致：`lower(标题) + '|' + 年份`（年份未知写 0）。
-- 空串表示「还没算出来」（迁移前的老数据），由扫描开始时的回填补上。
alter table media_items add column if not exists scan_key text not null default '';

-- 查找几乎都带 (library_id, kind)，所以索引按这个前缀建。
create index if not exists media_items_scan_key_idx on media_items (library_id, kind, scan_key);
