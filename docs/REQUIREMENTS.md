# LMBY 需求（v0.2 — 决策已定稿）

> LMBY = Light 的 Emby。目标：**一个二进制 + 一个自带容器的 PostgreSQL**，对标 Emby/Jellyfin 的核心体验，砍掉"重"的部分。
> 形态：**纯 Web 的在线播放站点** —— 浏览器是唯一客户端，HTTP 是唯一出口。不搞局域网媒体协议、不做客户端适配层。
> 定位：**通用开源软件**，不为特定硬件/库规模做特化。
> 参考实现：`C:\Users\admin\Desktop\dev\_ref\jellyfin`（Jellyfin 源码浅克隆，GPL-2.0，**只读参考，不复制代码**）
> 直播模块参考：`C:\Users\admin\Desktop\dev\TV`（tvhub，自研 AGPL-3.0，可直接复用思路与代码）

---

## 0. 已定稿决策（不再讨论）

| # | 决策 | 结论 |
|---|---|---|
| A | Node 的定位 | **只在构建期**（Vite + TS 编译前端），产物 `go:embed` 进 Go 二进制。运行期零 Node。保留 Node 的理由：前端要写 TS + 组件化，手写无构建的 JS 不划算。 |
| D | 客户端范围 | **只要浏览器**（桌面 + 移动）。**不做 Emby/Jellyfin 客户端协议兼容层**，不追求第三方客户端可用。 |
| E | 直播电视 | **要做**，方案参考 tvhub（M3U 源导入 + 每频道共享 ffmpeg HLS 会话 + 空闲回收）。 |
| G | PostgreSQL 来源 | **LMBY 自己拉容器**：`docker-compose` 内含 `postgres` 服务，用户无需自备。同时支持用环境变量指向外部 PG。 |
| — | 通用性 | **通用软件**：不针对特定 GPU/CPU/库规模优化，硬件能力靠运行时探测 + 可配置覆盖。 |
| — | 个人中心头像 | **不做头像上传**，用用户名首字色块（2026-09-20 用户拍板）。 |
| — | 元数据优先级 | **本地优先，到每集粒度**：媒体同目录有 nfo（含剧集的 `tvshow.nfo`、季的 `season.nfo`、集的 `S01E01.nfo`）就用它（那是人工花大力气整理的），**只在本地没有、或本地文件格式不对时**才去 TMDB 刮。自动流程永不覆盖已有本地元数据，除非显式 `--force`（2026-09-20 用户拍板）。 |
| — | 参考 Jellyfin | 允许并鼓励**阅读** Jellyfin 源码来对齐命名规则、决策逻辑、API 形状；**不复制代码**（其 GPL-2.0 与本项目 AGPL-3.0 不兼容共用同一份文件）。 |

### 待决（不影响开工，边做边定）
- **B. 「Emby 元数据入库」语义**：**默认按 B+C 执行** —— LMBY 自己刮的元数据只写 PG（不生成 XML）；提供一次性导入器读 Emby/Jellyfin 的 `metadata/library/**/*.nfo` + 可选 `library.db` 播放进度。扫描时仍读媒体同目录 nfo 作为输入（A）。
- **C. 图片策略**：默认「**本地文件优先 → 远程下载到缓存目录 → API 按需缩放输出**」，远程图片不入库只记路径/尺寸/hash。
- **H.** 库规模/硬件：不特化，通用软件。
- **J. 音乐库：不做**（已砍）。仅在 `media_items.type` 枚举里保留扩展位，代码上不写任何音频专用路径、不接 MusicBrainz。

---

## 1. 技术栈

| 层 | 选型 | 说明 |
|---|---|---|
| 后端 | Go 1.27（标准库 `net/http` 路由、`pgx/v5` 手写 SQL、`slog`、`go:embed`） | 单二进制，见 ADR-0001 |
| 存储 | PostgreSQL 16+ | 自己带容器，也支持外部 DSN |
| 前端 | React + TypeScript + Vite + TanStack Query（样式先用原生 CSS 变量） | 仅构建期用 Node |
| 转码 | 外部 ffmpeg（用户装或镜像内置），HLS 输出 | 不做内置 ffmpeg |
| 任务队列 | PG 表 + `FOR UPDATE SKIP LOCKED` + 进程内 worker | ❌ 不引 Redis/MQ |
| 实时 | SSE | 扫描进度、转码状态 |
| 迁移 | 自研内嵌迁移器（`go:embed` + advisory lock + 校验和） | `migrations/*.sql`，见 ADR-0001 |

**不做**：Redis、MQ、Emby 协议兼容、DRM、云同步、多租户、插件系统、**音乐库**、**DLNA/UPnP**、原生 App（二期可 Tauri 套壳）。

---

## 2. 功能范围

### 2.1 核心（一期必须有）
1. 媒体库管理（Emby 相同的目录/命名约定，零改名接管现有库）
2. 扫描与增量更新（含移动识别、播放进度保留）
3. 元数据：本地 nfo/图片优先 → 缺失走 TMDB（用户自备 Key）
4. 人工匹配与条目编辑（**刚需**，不是可选）
5. Web GUI（库浏览、详情、搜索、设置、任务管理、**明暗主题切换**）
6. 纯 Web 播放（hls.js + 原生 `<video>`）
7. DirectPlay / DirectStream(remux) / Transcode 三档决策
8. 硬件转码（QSV / NVENC / VAAPI / VideoToolbox / AMF，运行时探测）
9. 多用户 + 库级权限 + 家长分级 + 独立播放进度 + **个人中心**（自助改密码、偏好设置、会话管理）
10. **直播电视**（M3U/单播源导入、频道列表、HLS 播放、共享会话、空闲回收）

### 2.2 二期
DVR/录制/EPG、Jellyfin API 兼容层、Tauri 套壳、转码产物持久化缓存、多节点转码卸载。

> **音乐库与 DLNA/UPnP 不在任何阶段**（已砍）：本项目定位就是一个「**纯 Web 的在线播放站点**」，所有输出面都是给浏览器的 HTTP（网页 + HLS + 图片 API），不对外暴露局域网媒体协议、不做设备发现。
> 保留的扩展位：`media_items` 的多态设计、`internal/stream` 的 HLS 出口。

---

## 3. 媒体库与扫描

### 3.1 目录约定（与 Emby 对齐）
```
Movies/Title (2024)/Title (2024).mkv + .nfo + poster.jpg/fanart.jpg/backdrop.jpg/logo.png
Movies/Title (2024)/Title (2024) - 2160p.mkv      # 多版本 → 同一 item 多 MediaFile
Movies/Title (2024)/Extras/behind the scenes/*.mkv
TV/Show (2020)/tvshow.nfo + poster.jpg/fanart.jpg/banner.jpg/thumb.jpg/logo.png
TV/Show (2020)/Season 01/season.nfo + season01-poster.jpg + S01E02 - X.mkv + S01E02.nfo + S01E02-thumb.jpg
```
解析器需覆盖：年份/别名括号、`S01E02`/`1x02`/`E02`、双集连播、`- Part 1` 分片、多碟 `cd1/cd2`、`Season 00` 特典、Extras 分类、`.ignore`、符号链接、扩展名白名单/排除规则、unicode NFC/NFD、`movie.nfo` 兜底。

### 3.2 扫描流程
```
遍历 → 指纹(path,size,mtime_ns) 差异检测（识别"移动"而非删+增）→ 命名解析
→ 目录结构推断层级 → 读本地 nfo/登记图片 → upsert media_items/media_files
→ 未匹配进「待识别队列」→ 异步 ffprobe 探测
```
触发：手动 / 库变更时增量 / inotify 事件（+ 轮询兜底，网络挂载不触发事件）/ cron 计划。

### 3.3 元数据写入语义（决策 B/C）
- **唯一数据源是 PG**。**不生成、不导出任何 XML/nfo**（用户已确认：无导出功能）。
- 图片二进制不入库：`images` 表只存 `path/source/width/height/hash/lang`。
- 覆盖顺序：**媒体同目录本地图片 > 用户手动选择 > TMDB 下载缓存**。
- 本地图片只读不写：LMBY 不往媒体目录写任何东西。用户已明确「以后图片也像 nfo 一样
  存在视频文件旁边」—— 那是同一个模型（媒体目录里的东西优先），写回能力到时候再加；
  真实库里那些已存在的 `poster.jpg` / `S01E01-thumb.jpg` / `Backdrops/` 就是权威。
- 缩放输出不预生成：请求带 `w/h` 才缩，结果按内容寻址落 `DataDir/images/cache`（不看就删），
  回源下到的原图落 `DataDir/images/remote`（那是数据，不参与缓存清理）。
- 手改字段写入 `locked_fields`，重新刮削不覆盖。
- 出图走 `GET /api/v1/items/{id}/images/{kind}?w=&h=&format=webp&quality=`。

### 3.4 迁移导入器（决策 C）
读 `/config/metadata/library/<hash>/<itemid>/*.nfo`（Emby/Jellyfin 会把 xbmc 格式 nfo 写在这里）+ 媒体同目录 nfo → 匹配到 LMBY item → 灌入 PG；可选读 `library.db`(SQLite) 导入播放进度。

### 3.5 性能目标（通用软件，作为回归红线而非特化目标）
- 首次全量扫描 10 万文件 < 5 分钟（SSD 本地盘）
- 无变化二次扫描 < 30 秒
- 空闲内存 < 200 MB（不含 ffmpeg 子进程）

---

## 4. 元数据与刮削

- `Provider` 接口 + 注册表；首发 **TMDB**（user 自备 API Key），预留 TVDB/Bangumi/豆瓣/OMDb/OpenSubtitles。
- 匹配打分：标题相似度（中文 bigram / 英文 token 归一）、年份、类型、集数-时长吻合、别名命中、可选文件 hash → 阈值以上自动，以下进人工匹配队列。
- 工程要求：令牌桶限流、429/5xx 指数退避、`provider_cache` jsonb 缓存（重扫不打 API）、PG 持久化任务队列（可暂停/续跑/看失败原因）、多语言 fallback（本地 → `zh-CN` → 原名 → 别名）。
- 搜索：`pg_trgm` GIN + `tsvector`，**不引 zhparser**（部署负担），中文用 bigram/trigram 模糊匹配。

---

## 5. 播放与转码（核心难点）

### 5.1 三档决策（视频/音频/字幕各自独立）
- **DirectPlay**：全兼容 → 直接给原文件（HTTP Range）
- **DirectStream**：只换容器（mkv→fMP4/TS），流不重编码（**轻的最大红利**）
- **Transcode**：HLS 实时切片（优先 fMP4，支持 HEVC/AV1；TS 仅 H264/HEVC）

输入：媒体流 codec/profile/位深/HDR + 客户端能力上报（`MediaCapabilities`/`canPlayType`/屏幕/是否 iOS）+ 带宽估算。输出附带**理由字符串**（UI 要显示"为什么在转码"）。
设备 profile 表（DirectPlayProfile/CodecProfile/TranscodingProfile/SubtitleProfile + 条件判断）设计参考 Jellyfin 的 `MediaBrowser.Model/Dlna/` 命名。

### 5.2 转码要点
- **节流**：禁用 `-re`。做法 = 预生成 N 片 → `SIGSTOP` ffmpeg → 分片被消费时 `SIGCONT`；空闲 TTL 回收进程 + 清理分片目录。
- **能力探测**：`-hwaccels`/`-encoders`/`-filters` + **真跑 1 秒小样验证**（不只看"存在"），结果缓存 + GUI 可覆盖。
- **后端**：QSV / VAAPI(`/dev/dri/renderD128`) / NVENC+NVDEC(含 `scale_cuda`、tonemap) / VideoToolbox / AMF / 软件兜底 `libx264/libx265/libsvtav1`。
- **会话复用**：同 item + 同输出 profile 共享一路 ffmpeg。
- **并发**：全局 + 每用户上限；硬件编码会话数不足时排队。
- **HDR**：HDR10/HLG → SDR tone mapping（含硬件路径）；DV P5 → HDR10（能 copy 则 copy，否则降级）。
- **音频**：直通优先；否则 AAC/AC3/Opus，多声道 downmix 可选。
- **字幕**：文本 → WebVTT 轨道；图形字幕(PGS/VOBSUB) → burn-in 或明确提示不支持；支持外挂字幕上传。
- **Trickplay**：章节图 + 时间轴精灵图，异步生成 + 落盘缓存 + 可关闭。
- **跳转**：HLS 按需 seek 起新 ffmpeg（`-ss` 关键帧对齐）；DirectStream 靠 Range 天然支持。

### 5.3 播放器（Web）
进度记忆/续播、质量手动切换、音轨与字幕切换、倍速、章节、下一集自动连播、片头跳过、PiP/全屏、快捷键、进度条预览图、错误回退与上报。需专门覆盖 iOS Safari（音轨/字幕切换、后台播放限制）。

---

## 6. 直播电视（一期）

参考 tvhub（`C:\Users\admin\Desktop\dev\TV`）已验证的做法：
- 源管理：M3U 粘贴/上传/订阅 URL 导入，按地址增量更新，保留启用状态与收藏
- 频道共享一路 ffmpeg HLS 会话；空闲 45s 自动回收 + 清理分片
- 视频 `-c:v copy` 不转码（源多为 H264 1080p），音频 MP2 → AAC 转码
- 外部播放器出口 `/s/<token>/playlist.m3u`
- 扩展：EPG 导入（XMLTV，二期）、录制/DVR（二期）、按频道的转码档位选择

---

## 7. 数据模型（PostgreSQL）

```
users(id,username,display_name,password_hash[argon2id],is_admin,is_disabled,
  max_concurrent_streams,created_at,last_login_at,last_login_ip)
user_preferences(user_id,theme,language,subtitle_prefs jsonb,audio_prefs jsonb,library_views jsonb)
user_library_access, auth_tokens/sessions
libraries(name,type,options jsonb), library_paths(library_id,path,readonly,order)
media_items(id,library_id,type,parent_id,series_id,season,episode,title,sort_title,
  original_title,year,overview,tagline,runtime_ticks,community_rating,official_rating,
  genres jsonb,tags jsonb,studios jsonb,provider_ids jsonb,premiere_date,added_at,updated_at,
  locked_fields jsonb,match_state)
media_files(id,item_id,path UNIQUE,size,mtime_ns,container,duration_ticks,
  video_streams jsonb,audio_streams jsonb,subtitle_streams jsonb,chapters jsonb,hdr jsonb,
  probed_at,probe_err)
people(id,name,provider_ids), item_people(item_id,person_id,role,character,order)
images(id,item_id,kind,path,source,width,height,hash,lang)      -- 只有路径，无二进制
play_states(user_id,item_id,position_ticks,played,played_at,play_count)
provider_cache(key PK,url,response jsonb,etag,fetched_at,ttl)
tasks(id,kind,payload jsonb,state,priority,attempts,last_error,run_at,started_at,finished_at)
scan_runs, scan_issues
play_sessions(id,token,user_id,item_id,media_file_id,mode,profile jsonb,ffmpeg_pid,args,
  segment_dir,last_activity,client_info jsonb,state)
-- 直播
tv_sources(id,name,type[m3u|url],url,content text,enabled,last_sync_at)
tv_channels(id,source_id,tvg_id,name,logo_url,group_name,stream_url,enabled,favorite,order)
collections, collection_items, playlists, playlist_items
settings(key,value jsonb)   -- 含加密存储的 TMDB Key
```

---

## 8. 部署与运维

- `docker-compose.yml`：`lmby` + `postgres`（LMBY 自己拉，用户零依赖）；备选：外部 PG 用 DSN 指向
- 硬件加速需设备映射：`/dev/dri`（QSV/VAAPI）、`--runtime=nvidia`（NVENC）
- 目标平台：Linux x86_64 → ARM64（NAS/树莓派）→ Windows（开发机）
- 配置文件 TOML + `LMBY_*` 环境变量覆盖；`--version`、`/healthz`（PG + ffmpeg 状态）、`/metrics`（可选）
- 安全：argon2id、会话 Cookie（HttpOnly/Secure/SameSite）+ 可撤销、登录限流、敏感配置加密与日志脱敏；HTTPS 交给反代
- **不提供对外 API / API Key**：浏览器是唯一客户端，不需要给脚本/第三方发凭证（用户已确认）。Web 会话 Cookie 就是唯一认证方式。
- **账号自助**：用户可自行修改密码（**必须验证旧密码**）、管理自己的会话；管理员可重置密码/禁用账号/强制下线；改密后按策略失效其他会话（默认：保留当前、其余失效）
- 备份：`lmby backup` / `lmby restore`

---

## 9. 参考实现坐标（阅读用，不复制代码）

| LMBY 模块 | Jellyfin 源码位置 |
|---|---|
| 命名解析 | `Emby.Naming/Video/{CleanStringParser,CleanDateTimeParser,ExtraRuleResolver,FileStackRule}.cs`、`Emby.Naming/TV/{EpisodePathParser,SeasonPathParser,SeriesPathParser}.cs`、`Emby.Naming/Common/NamingOptions.cs` |
| 探测 | `MediaBrowser.MediaEncoding/Probing/*`（`MediaStreamInfo.cs`、`ProbeResultNormalizer.cs`）、`Encoder/FFProbeHelpers.cs` |
| 编码能力探测 | `MediaBrowser.MediaEncoding/Encoder/EncoderValidator.cs` ⭐ 现成的 `-encoders/-hwaccels` 解析 |
| **播放决策引擎** | `MediaBrowser.Model/Dlna/StreamBuilder.cs` ⭐ + `DeviceProfile/DirectPlayProfile/CodecProfile/TranscodingProfile/SubtitleProfile/ConditionProcessor.cs` |
| 转码执行 | `MediaBrowser.MediaEncoding/Transcoding/TranscodeManager.cs`、`Encoder/MediaEncoder.cs`、`Encoder/EncodingUtils.cs` ⭐ 命令行拼装 |
| HLS 输出 | `Jellyfin.Api/Controllers/DynamicHlsController.cs`、`HlsSegmentController.cs` |
| 本地 nfo/图片 | `MediaBrowser.LocalMetadata/{Parsers,Savers,Images}/*`、`MediaBrowser.XbmcMetadata/{Parsers,Savers}/*` |
| 元数据 provider | `MediaBrowser.Providers/{Movies,Manager,People}/*`、`MediaBrowser.Model/Providers/*` |
| 用户/权限 | `Jellyfin.Server.Implementations/Users/*`、`Jellyfin.Data/Enums/*` |
| Trickplay | `Jellyfin.Server.Implementations/Trickplay/*`、`TrickplayController.cs` |
| 直播电视 | `src/Jellyfin.LiveTv/{TunerHosts,Guide,Recordings,Timers,Listings}/*` |
| 定时任务 | `ScheduledTasksController.cs` + `Jellyfin.Server.Implementations` 任务层 |
| API 命名参考 | `Jellyfin.Api/Controllers/*.cs`（约 60 个 controller 的功能清单，可当"要做什么"的检查表） |
| 数据表参考 | `Jellyfin.Data/Entities/*`、`Jellyfin.Server.Implementations/{Item,Users,Devices,Activity,Trickplay}/*` |
| 直播（自研） | `C:\Users\admin\Desktop\dev\TV`：`internal/{m3u,stream,store,auth,webui}`（AGPL-3.0，可直接复用） |

> ⚠️ Jellyfin 是 GPL-2.0。**抄逻辑可以，抄代码文件不行**（LMBY 计划 AGPL-3.0）。命名规则的常量表可以自行重写，但要能解释每条规则。

---

## 10. 已知风险
1. **转码决策 + 节流**是唯一可能翻车的地方（Jellyfin 打磨多年）→ 尽早真机 + 真浏览器端到端验证。
2. **iOS Safari** 的容器/音轨/字幕限制 → 专门用例。
3. **命名解析边界无穷**（中文剧、日番、双集连播、多碟、OST）→ 靠可累积测试语料库。
4. **刮削匹配准确率** → 人工匹配 UI 是必需品。
5. **网络存储**（SMB/NFS）的事件、mtime 精度、符号链接、大小写。
6. **HDR tone mapping 性能** → 明确支持矩阵，跑不动就明确不支持（不假装支持）。
7. **通用软件的配置复杂度** → 默认值必须能在"零配置"下跑起来。
