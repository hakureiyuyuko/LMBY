-- 0016_users_permissions.sql
--
-- 用户与权限（M7 地基）。设计见 docs/notes/users-permissions.md。
--
-- 权限只有 4 个维度：管理员（users.is_admin，已有）、库可见性、并发流上限（已有列，
-- 这次把它接上）、允许转码 / 允许直播。
--
-- 三条默认值都等于**现在的行为**（全部库可见、允许转码、允许直播）——
-- 升级后老账号不会忽然看不见东西，想限制谁就去勾谁。
alter table users add column if not exists restrict_libraries boolean not null default false;
alter table users add column if not exists allow_transcode boolean not null default true;
alter table users add column if not exists allow_livetv boolean not null default true;

-- 库白名单。只在 restrict_libraries = true 时生效（管理员永远全部可见，不看这张表）。
-- 用 (user_id, library_id) 主键：授权是集合语义，重复勾选是幂等的。
create table if not exists user_libraries (
  user_id    bigint not null references users (id) on delete cascade,
  library_id bigint not null references libraries (id) on delete cascade,
  created_at timestamptz not null default now(),
  primary key (user_id, library_id)
);

-- 反过来查「这个库给了哪些人」也要快（界面上想按库看授权时用得上）
create index if not exists idx_user_libraries_library on user_libraries (library_id);
