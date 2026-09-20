-- 0004_tasks.sql：给任务队列补上「幂等入队」与运维需要的索引
--
-- 0002 已经建了 tasks 表本体，这里补三件事：
--   1. dedupe_key 让「同一个文件不需要重复探测」这类语义由数据库强制保证；
--   2. 按 kind/state 的统计索引，便于界面展示队列水位；
--   3. 清理索引，让历史任务能高效删除。

alter table tasks add column if not exists dedupe_key text not null default '';

-- 同一个 (kind, dedupe_key) 在「待处理 / 进行中」状态只允许存在一条。
--
-- 注意是**部分索引**：已完成/失败的历史记录不参与约束，
-- 这样重试与重新入队不会被历史挡掉，也不会因为历史记录而永久无法入队。
create unique index if not exists tasks_dedupe_idx
    on tasks (kind, dedupe_key)
    where dedupe_key <> '' and state in ('pending', 'running');

create index if not exists tasks_state_kind_idx on tasks (state, kind);

create index if not exists tasks_cleanup_idx
    on tasks (finished_at)
    where state in ('done', 'failed', 'canceled');

-- 探测结果的一个便利视图：还有多少文件没探测
create index if not exists media_files_pending_probe_idx
    on media_files (id)
    where probe_state = 'pending' and deleted_at is null;
