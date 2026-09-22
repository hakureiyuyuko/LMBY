-- 0015_livetv_channel_codec.sql
--
-- 记下频道源的视频编码（探测时顺手拿到，见 internal/livetvsync 的 SummarizeStreams）。
--
-- 为什么需要：直播起播默认是**转封装**（视频 -c copy，不重编码），
-- 但浏览器不是什么都解得开（H.265/HEVC、MPEG-2 基本不行）——
-- 有了这个字段，服务端就能在起播前判断「这份源浏览器吃不吃」，
-- 吃不下就直接转码（H.264）而不是发一路它放不了的分片。
--
-- 没探测过（video_codec is null）的频道照旧按转封装处理：
-- 前端在真正放不出来时会带 force=transcode 重试一次（未知编码的兜底）。
alter table tv_channels add column if not exists video_codec text;
-- 源视频高度（0/空 = 未知）：决定转码时要不要往下缩（转码上限由 [playback] transcode_max_height 给）
alter table tv_channels add column if not exists video_height int not null default 0;
