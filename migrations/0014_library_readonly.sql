-- 0014_library_readonly.sql
--
-- 媒体库「只读」开关。
--
-- 为什么需要：库可以挂在网盘 / 只读挂载上（Alist、rclone、NFS ro、只读 bind mount）。
-- 这种库上 LMBY 一个字节也不能往库目录里写，否则轻则报错、重则把只读挂载搞挂。
--
-- 打开之后，刮削到的元数据与图片落进数据目录的 overlay 层
-- （<数据目录>/overlay/<libraryId>/...，每个库一块，不参与图片缓存淘汰），
-- 媒体目录始终是「只读输入」。将来启用「写回媒体目录」时，这一列就是那条闸门。
--
-- library_paths 上早在 0001 就有 readonly（根路径级的事实），这里补的是库级开关；
-- 两者由 store.SetLibraryReadOnly 一起改，永远同步。
alter table libraries add column if not exists readonly boolean not null default false;

create index if not exists idx_libraries_readonly on libraries (readonly) where readonly;
