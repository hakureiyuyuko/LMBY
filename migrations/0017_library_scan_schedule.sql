-- 0017_library_scan_schedule.sql：扫描计划
--
-- 设计取舍：**间隔属于库**，不属于调度器 —— 与直播源的 refresh_interval_minutes 一个路子。
-- 这样「这个库每小时扫、那个库每天扫」是数据而不是代码，调度器只负责「现在谁到期了」。
--
-- 0 = 不自动扫（只手动）。上限一周（10080 分钟）：再长就该用别的机制了
-- （这个字段是「多久扫一次」，不是「什么时候扫」）。
alter table libraries
    add column if not exists scan_interval_minutes integer not null default 0;

do $$
begin
    if not exists (
        select 1 from pg_constraint where conname = 'libraries_scan_interval_check'
    ) then
        alter table libraries
            add constraint libraries_scan_interval_check
            check (scan_interval_minutes >= 0 and scan_interval_minutes <= 10080);
    end if;
end $$;

-- 到期判定用得到：`scan_interval_minutes > 0` 的库才参与（写出来让计划器少扫全表）。
create index if not exists libraries_scan_schedule_idx
    on libraries (scan_interval_minutes)
    where scan_interval_minutes > 0;
