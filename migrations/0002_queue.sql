-- 0002_queue.sql：后台任务队列
--
-- 不引入 Redis/MQ：任务就是 PG 表里的一行，worker 用
-- `for update skip locked` 抢占，进程重启后未完成的任务自然还能被捡起来。

create table if not exists tasks (
    id           bigint generated always as identity primary key,
    kind         text        not null,                      -- scan | probe | scrape | trickplay ...
    payload      jsonb       not null default '{}'::jsonb,
    state        text        not null default 'pending',    -- pending | running | done | failed | canceled
    priority     integer     not null default 0,            -- 越大越先执行
    attempts     integer     not null default 0,
    max_attempts integer     not null default 3,
    last_error   text        not null default '',
    run_at       timestamptz not null default now(),        -- 到期才可被领取（支持退避）
    started_at   timestamptz,
    finished_at  timestamptz,
    locked_by    text        not null default '',           -- worker 标识，便于排查卡死
    created_at   timestamptz not null default now(),
    constraint tasks_state_check check (state in ('pending', 'running', 'done', 'failed', 'canceled'))
);

-- 领取任务用：只扫 pending，按优先级 + id 排序
create index if not exists tasks_claim_idx
    on tasks (run_at, priority desc, id)
    where state = 'pending';

create index if not exists tasks_kind_state_idx on tasks (kind, state);
