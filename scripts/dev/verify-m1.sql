-- M1 扫描结果验证：在数据库侧直接核对扫描是否正确落库。
--
-- 用法：PGPASSWORD=$(cat /etc/lmby/pg-password) psql -h 127.0.0.1 -U lmby -d lmby -f verify-m1.sql
--
-- 全部使用只读查询，可反复执行。

\pset border 2

\echo '=== 1. 条目按类型统计 ==='
select kind, count(*) as 数量
from media_items
where deleted_at is null
group by kind
order by 2 desc;

\echo '=== 2. 文件 / 图片 / 问题总数 ==='
select
  (select count(*) from media_files where deleted_at is null) as 文件数,
  (select count(*) from images) as 图片数,
  (select count(*) from scan_issues) as 问题数;

\echo '=== 3. 元数据是否真的从 nfo 落库（有简介的比例）==='
select
  kind,
  count(*) as 总数,
  count(*) filter (where overview <> '') as 有简介,
  count(*) filter (where year is not null) as 有年份,
  count(*) filter (where provider_ids <> '{}'::jsonb) as 有外部id
from media_items
where deleted_at is null
group by kind
order by 2 desc;

\echo '=== 4. 剧集与季的结构（季/集编号是否正确）==='
select
  s.title as 剧集,
  s.year as 年,
  count(distinct m.id) filter (where m.kind = 'season') as 季数,
  count(distinct m.id) filter (where m.kind = 'episode') as 集数,
  count(distinct f.id) as 文件数
from media_items s
left join media_items m on m.series_id = s.id and m.deleted_at is null
left join media_files f on f.item_id = m.id and f.deleted_at is null
where s.kind = 'series' and s.deleted_at is null
group by s.id, s.title, s.year
order by s.title;

\echo '=== 5. 每季的集号范围（检查 SxxExx 是否解析正确）==='
select
  s.title as 剧集,
  e.season_number as 季,
  min(e.episode_number) as 最小集,
  max(e.episode_number) as 最大集,
  count(*) as 集数
from media_items e
join media_items s on s.id = e.series_id
where e.kind = 'episode' and e.deleted_at is null
group by s.title, e.season_number
order by s.title, e.season_number;

\echo '=== 6. 集标题（SxxExx 无集标题时应回退到剧集名）==='
select
  s.title as 剧集,
  e.season_number as 季,
  e.episode_number as 集,
  e.episode_end as 连播到,
  e.title as 集标题
from media_items e
join media_items s on s.id = e.series_id
where e.kind = 'episode' and e.deleted_at is null
order by s.title, e.season_number, e.episode_number
limit 12;

\echo '=== 7. 电影：标题与年份 ==='
select title as 标题, year as 年份, file_tech as 文件标记
from media_items
where kind = 'movie' and deleted_at is null
order by title
limit 15;

\echo '=== 8. 命名规范识别（书名号 + 技术标记）==='
select title as 标题, file_tech as 技术标记
from media_items
where kind = 'movie' and deleted_at is null and file_tech <> '{}'::jsonb
order by title
limit 10;

\echo '=== 9. 图片归属（按类别统计）==='
select kind as 图片类别, count(*) as 数量
from images
group by kind
order by 2 desc;

\echo '=== 10. 剧集级图片（poster/fanart/banner/logo 挂到 series 上有几张）==='
select i.title as 剧集, img.kind as 类别, count(*) as 张数
from images img
join media_items i on i.id = img.item_id
where i.kind = 'series'
group by i.title, img.kind
order by i.title, img.kind;

\echo '=== 11. 季海报是否挂到 season 条目 ==='
select i.title as 季标题, img.kind as 类别, count(*) as 张数
from images img
join media_items i on i.id = img.item_id
where i.kind = 'season'
group by i.title, img.kind;

\echo '=== 12. 集缩略图（SxxExx-thumb.jpg）是否挂到 episode ==='
select i.title as 剧集, e.episode_number as 集, img.kind as 类别, img.path
from images img
join media_items e on e.id = img.item_id
join media_items i on i.id = e.series_id
where e.kind = 'episode'
order by e.episode_number
limit 5;

\echo '=== 13. 扫描问题（按类型汇总）==='
select severity as 级别, message as 说明, count(*) as 条数
from scan_issues
group by severity, message
order by 3 desc
limit 15;

\echo '=== 14. 扫描运行记录 ==='
select id, state, trigger, started_at, finished_at,
       stats->>'videos' as 视频,
       stats->>'newFiles' as 新文件,
       stats->>'unchanged' as 未变化,
       stats->>'movedFiles' as 移动,
       stats->>'deletedFiles' as 删除,
       stats->>'nfoRead' as nfo读取,
       stats->>'images' as 图片,
       stats->>'issues' as 问题,
       stats->>'elapsedMs' as 耗时毫秒
from scan_runs
order by id desc
limit 5;

\echo '=== 15. 条目层级完整性（有没有孤儿条目）==='
select
  count(*) filter (where kind = 'episode' and series_id is null) as 无剧集的集,
  count(*) filter (where kind = 'episode' and parent_id is null) as 无父季的集,
  count(*) filter (where kind in ('episode','season') and series_id is null) as 无剧集的季或集
from media_items
where deleted_at is null;
