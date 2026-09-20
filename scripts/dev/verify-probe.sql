-- M2 流信息探测的数据库侧验证（只读，可反复执行）。
--
-- 用法：PGPASSWORD=$(cat /etc/lmby/pg-password) \
--         psql -h 127.0.0.1 -U lmby -d lmby -f verify-probe.sql

\pset border 2

\echo '=== 1. 探测进度 ==='
select
  count(*) filter (where probe_state = 'pending') as 待探测,
  count(*) filter (where probe_state = 'ok')      as 已完成,
  count(*) filter (where probe_state = 'failed')  as 失败,
  count(*)                                        as 合计
from media_files where deleted_at is null;

\echo '=== 2. 队列表水位 ==='
select state as 状态, kind as 类型, count(*) as 条数
from tasks group by state, kind order by state, kind;

\echo '=== 3. 探测到的容器分布 ==='
select container as 容器, count(*) as 文件数
from media_files where probe_state = 'ok' group by container order by 2 desc limit 12;

\echo '=== 4. 视频编码与位深分布（从 jsonb 里取）==='
select
  coalesce(video_streams -> 0 ->> 'codec', '?')       as 编码,
  coalesce(video_streams -> 0 ->> 'bitDepth', '?')     as 位深,
  coalesce(video_streams -> 0 ->> 'profile', '-')      as profile,
  count(*) as 文件数
from media_files
where probe_state = 'ok' and video_streams is not null
group by 1, 2, 3 order by 4 desc limit 15;

\echo '=== 5. 分辨率分布 ==='
select
  (video_streams -> 0 ->> 'width') || 'x' || (video_streams -> 0 ->> 'height') as 分辨率,
  count(*) as 文件数
from media_files
where probe_state = 'ok' and video_streams is not null
group by 1 order by 2 desc limit 12;

\echo '=== 6. HDR / 杜比视界识别结果 ==='
select
  coalesce(hdr ->> 'format', 'SDR') as hdr类型,
  coalesce(hdr ->> 'dolbyProfile', '') as dv_profile,
  coalesce(hdr ->> 'transfer', '') as 传输特性,
  count(*) as 文件数
from media_files where probe_state = 'ok'
group by 1, 2, 3 order by 4 desc;

\echo '=== 7. 图形字幕（影响播放决策）==='
select
  sub ->> 'codec' as 字幕编码,
  count(*) as 条数,
  count(distinct f.id) as 涉及文件
from media_files f
cross join lateral jsonb_array_elements(f.subtitle_streams) as sub
where f.probe_state = 'ok' and (sub ->> 'isImage')::boolean
group by 1 order by 2 desc;

\echo '=== 8. 音轨编码分布 ==='
select
  a ->> 'codec' as 音频编码,
  count(*) as 条数,
  count(*) filter (where (a ->> 'atmosHint')::boolean) as 疑似atmos
from media_files f
cross join lateral jsonb_array_elements(f.audio_streams) as a
where f.probe_state = 'ok'
group by 1 order by 2 desc limit 12;

\echo '=== 9. 时长是否回填到条目（集/电影）==='
select
  i.kind as 类型,
  count(*) as 条目数,
  count(*) filter (where i.runtime_ticks > 0) as 有时长
from media_items i
where i.deleted_at is null and i.kind in ('movie', 'episode')
group by 1;

\echo '=== 10. 时长异常的抽样（< 1 分钟 或 > 6 小时）==='
select
  i.title as 标题,
  f.path as 路径,
  round(f.duration_ticks / 10000000.0) as 秒
from media_files f join media_items i on i.id = f.item_id
where f.probe_state = 'ok' and f.duration_ticks > 0
  and (f.duration_ticks < 60 * 10000000 or f.duration_ticks > 6 * 3600 * 10000000)
order by f.duration_ticks desc limit 10;

\echo '=== 11. 探测失败的样本（应当都是「文件本身有问题」）==='
select
  left(f.path, 70) as 路径,
  left(f.probe_error, 90) as 错误
from media_files f
where f.probe_state = 'failed'
order by f.id limit 12;

\echo '=== 12. 单个文件的完整流信息样例 ==='
select jsonb_pretty(jsonb_build_object(
  'path', f.path,
  'container', f.container,
  'durationSec', round(f.duration_ticks / 10000000.0, 1),
  'video', f.video_streams,
  'audio', f.audio_streams,
  'subtitles', f.subtitle_streams,
  'hdr', f.hdr
))
from media_files f
where f.probe_state = 'ok' and jsonb_array_length(coalesce(f.subtitle_streams, '[]'::jsonb)) > 0
limit 1;
