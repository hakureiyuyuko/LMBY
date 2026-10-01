-- 外挂字幕与视频的关联。
--
-- 背景：M1 起扫描器就认识外挂字幕（parser.IsSubtitle），但那个分支只做了
-- w.stats.Subtitles++，既不建条目也不建关联（见 docs/LIBRARY-NOTES.md 的字幕行）。
-- 后果是播放器上既没有内封也没有外挂可选 —— 哪怕目录里每集都配了 .chs.ass。
-- 这里补的就是「关联」本身：一条记录 = 某个条目目录下的一个外挂字幕文件。
--
-- 只登记文本格式（ass/ssa/srt）。位图字幕（sup/sub/idx）要烧录才能看，
-- 不在这一版范围内 —— 扫描时仍然只计数，见 internal/scanner 里的注释。
--
-- 二进制永不入库，和 images 一样只存路径与元信息。

create table if not exists subtitles (
  id         bigserial primary key,
  item_id    bigint not null references media_items(id) on delete cascade,
  path       text not null,
  language   text not null default '',
  title      text not null default '',
  format     text not null,
  forced     boolean not null default false,
  size_bytes bigint not null default 0,
  mtime_ns   bigint not null default 0,
  created_at timestamptz not null default now()
);

-- 同一个条目下的同一个文件只登记一次；换个条目重扫时会由
-- DeleteSubtitlesExcept 收掉旧行，所以这里不必对 path 单独做唯一约束。
create unique index if not exists subtitles_item_path_key on subtitles (item_id, path);
create index if not exists subtitles_item_idx on subtitles (item_id);
