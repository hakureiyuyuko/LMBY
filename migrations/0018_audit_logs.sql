-- 0018_audit_logs.sql：审计日志（谁在什么时候做了什么）
--
-- 为什么单开一张表，而不是塞进应用日志（internal/logbuf）：
--   ① 应用日志是内存环形缓冲、会滚掉；审计要**能翻旧账**；
--   ② 审计要能按「谁 / 什么动作 / 什么对象」查，而不是按关键字搜一大段文本。
--
-- 关于内容：**口令、token、密钥一律不落这张表** —— detail 里只写「重置了某人的口令」
-- 这种事实，不写值。actor_name 冗余存一份：用户被删掉之后，历史仍要能看懂。
create table if not exists audit_logs (
    id         bigint generated always as identity primary key,
    at         timestamptz not null default now(),
    actor_id   bigint,                                -- 登录失败时不知道是谁，允许 null
    actor_name text        not null default '',
    action     text        not null,                  -- 稳定的点分动作名，如 user.create
    target     text        not null default '',       -- 对象标识，如 user:3 / library:1
    result     text        not null default 'ok',     -- ok | failed
    detail     jsonb       not null default '{}'::jsonb,
    ip         text        not null default ''
);

create index if not exists audit_logs_at_idx on audit_logs (at desc);
create index if not exists audit_logs_actor_idx on audit_logs (actor_id, at desc);
create index if not exists audit_logs_action_idx on audit_logs (action, at desc);
