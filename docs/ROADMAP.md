# LMBY 开发 Todolist

> 状态：**M0 已完成并实测验收**，下一步 M1（媒体库与扫描）。
> 已完成项标 `[x]`，未完成/改期的项保留在下方并注明原因。
> 原则：先打通端到端最小闭环（扫描→入库→播放），再堆功能；**转码是唯一高风险块，尽早真机验证**。
> 参考代码：`C:\Users\admin\Desktop\dev\_ref\jellyfin`（只读阅读，不复制文件）、`C:\Users\admin\Desktop\dev\TV`（自研，可复用）。

---

## M0 — 项目骨架 ✅（已完成并实测验收）

- [x] git 仓库、`README.md`、`LICENSE`（AGPL-3.0-only）、`docs/ADR/`
- [x] 目录结构定稿（`internal/{config,store,auth,api,ffmpeg,version}` 已落地，其余目录随里程碑增加）
- [x] `go.mod`（Go 1.27）+ `golangci-lint` 配置 + `Taskfile.yml`；**不用 sqlc**（见 ADR-0001）
- [x] `internal/config`：TOML + `LMBY_*` 环境变量 + 取值校验 + 默认值
- [x] `migrations/0001_init.sql`：users / user_preferences / sessions / settings / libraries / library_paths / user_library_access
- [x] `migrations/0002_queue.sql`：tasks（含 `for update skip locked` 抢占索引）
- [x] **自研内嵌迁移器**：`go:embed` + `pg_advisory_lock` + 逐文件事务 + sha256 校验和（历史迁移被改直接报错）
- [x] HTTP 骨架：标准库 `ServeMux` 路由、`/healthz`（PG ping + ffmpeg 探测）、`slog` JSON 日志、访问日志、panic 恢复、安全响应头、SPA 静态兜底、`--version`/`help`
- [x] **认证地基**：初始化向导（`CreateFirstAdmin` 条件插入防并发重复）、`LMBY_ADMIN_PASSWORD` 自动创建、登录/登出、argon2id（代价参数升级自动重算）、DummyVerify 抹平时序、登录失败限流（按 用户名+IP）、会话 Cookie（HttpOnly/SameSite/可撤销）、**全接口默认鉴权中间件**
- [x] **个人中心**：改口令（须验旧口令 + 踢掉其他设备）、修改显示名、我的设备（列表 / 撤销单条）、外观偏好
- [x] **明暗主题**：首屏内联脚本防 FOUC、**右上角常驻切换入口**、localStorage 持久化、默认跟随系统 `prefers-color-scheme`、登录后与账号偏好同步
- [x] 前端：React 19 + TypeScript + Vite 8 + react-router 7，产物 `go:embed` 进二进制（运行期零 Node）
- [x] `deploy/docker-compose.yml`（lmby + postgres:17 + `/dev/dri`）、`deploy/Dockerfile`（多阶段）、`deploy/lmby.service`、`deploy/lmby.example.toml`
- [x] CI（GitHub Actions）：`go vet` / `go test -race` / `golangci-lint` / `tsc --noEmit` / `vite build`
- [x] 开发与验收脚本：`scripts/dev/{setup-pg,smoke-test}.sh`、`scripts/dev/browser-test.mjs`
- [ ] SSE 通道 —— **改期到 M1**，与扫描进度一起做，避免现在做一遍再改
- [ ] `docs/CONFIG.md` —— 暂由 `deploy/lmby.example.toml` 承担，M1 配置项变多后再单独成文

### DoD 验收记录（2026-09-20，开发容器实测）

| 项 | 结果 |
|---|---|
| 运行环境 | PVE 9.2.11 上的 LXC `lmby-dev`（Debian 13 / 4 核 / 4G / Intel UHD 630 直通），systemd 单元 `lmby` |
| 启动日志 | 迁移应用 2 条、schemaVersion=2、ffmpeg 7.1.5 识别成功 |
| 数据库 | 9 张业务表 + 2 条 `schema_migrations` |
| `smoke-test.sh` | **42/42 通过**（健康检查、鉴权边界、会话、偏好、显示名、设备列表、改口令踢其他设备、登出、静态兜底、非法输入校验） |
| `browser-test.mjs` | **26/26 通过**（CDP 驱动真实 Chrome 153：首屏跟随系统主题、明暗切换与 localStorage、初始化向导、概览页、个人中心、主题同步账号、界面改口令、刷新后主题保持、新口令重新登录） |
| 硬件加速 | VAAPI 实测可用，QSV 不可用（详见 `docs/TRANSCODING.md`） |


---

## M1 — 媒体库与扫描 🚧（主体已完成并实测验收）

> 已完成：库管理、扫描器（增量/移动/软删除）、命名解析器、本地 nfo 导入、图片归属登记、
> SSE 实时进度、扫描问题清单、媒体库界面。
> **未完成并改期**：ffprobe 流信息探测 —— 改到 M2 与持久化任务队列一起做，
> 避免现在写一套临时的并发控制再推翻（见下方说明）。

**参考**：`Emby.Naming/**`（尤其 `NamingOptions.cs` 的常量表、`Video/CleanStringParser.cs`、`TV/EpisodePathParser.cs`）

- [ ] 库 CRUD API + 类型（movie/tv/homevideo/mixed）+ 多根路径 + 只读标记
- [ ] 文件遍历器：扩展名白名单、排除规则（glob/regex）、最小文件大小、隐藏目录、`.ignore`、符号链接策略
- [ ] 指纹与增量：`(path,size,mtime_ns)` 差异检测；**移动识别**（同 size+mtime → 保留 item id 与播放进度）；删除软标记 + 清理策略
- [ ] **命名解析器**（纯函数、可单测）
  - 电影：`Title (Year) [edition] - 2160p.mkv`、`Title.2024.mkv`、`movie.nfo` 兜底
  - 剧集：`S01E02` / `1x02` / `E02` / 双集 `S01E02E03` / `- Part 1` / `Season 00` / 多碟 `cd1`
  - 清洗：非法字符、unicode NFC/NFD、`.`/`_` 分隔、大小写
  - **语料库** `internal/parser/testdata/cases.yaml` ≥ 150 条真实命名（含中文剧、日番、外文原文、特典、多版本）
- [ ] 目录结构推断：series/season/episode 层级、`Extras` 分类（trailer/behind the scenes/deleted scenes/interview）
- [ ] 本地元数据只读：`.nfo`（movie/tvshow/season/episode）+ 本地图片约定登记
- [ ] `internal/probe`：ffprobe 封装 + 归一化（参考 `Probing/MediaStreamInfo.cs`、`ProbeResultNormalizer.cs`）；异步任务 + 限流 + 重试 → 写入 `media_files`
- [ ] 扫描触发：手动 / 库变更增量 / inotify（Linux）+ 定时轮询兜底 / cron 计划
- [ ] `scan_runs` + `scan_issues` + SSE 实时进度推送
- [ ] 最小 GUI：登录页 + 添加库向导、库列表、扫描进度、原始条目表格（不做漂亮详情页），**带明暗切换**（localStorage 持久化）
- [x] **DoD（实测于 `\\<MEDIA_SERVER>\<MEDIA_SHARE>`）**：见下方验收记录

### M1 验收记录（2026-09-20）

测试库：真实 Emby 刮削库的子集（4 个根路径：测试片目录 + 2 部剧集 + 剧场动画）。
完整库 3 万文件 / 7.5T 因网盘后端读取延迟而只做子集验证（见 `docs/DEV-ENV.md`）。

| 项 | 结果 |
|---|---|
| 首次全量扫描 | 479 视频 → 479 文件行、337 条目（电影 335 / 剧集 2 / 季 5 / 集 130）、图片 1220、nfo 读取 479 |
| 二次扫描（无变化） | 200 文件 **255ms**，全部走「未变化」快速路径（比首轮快约 340 倍） |
| 剧集结构 | 钢之炼金术师 S1=64 集 + 特典 4 集；致不灭的你 S1/S2/S3 = 20/20/22 —— 季集号全对 |
| nfo 元数据 | 130/130 集、259 电影有简介+年份+外部 id（tmdb/imdb/tvdb） |
| 图片归属 | 剧集海报/背景/logo、季海报、集缩略图分别挂到 series/season/episode |
| 命名规范 | 《》标题与 6 类技术标记（codec/分辨率/色深/帧率/码率/特性）均正确提取并入库 |
| 解析器语料 | `internal/parser/testdata/cases.json` 46 视频 + 17 目录 + 14 分类用例全绿 |
| 扫描器单测 | `internal/scanner/scanner_test.go` 6 个层级/特典/花絮场景全绿 |
| 界面验收 | CDP 驱动真实 Chrome 18/18：库卡片、统计、扫描记录、条目表、类型筛选、SSE 实时进度、明暗主题 |

**扫描器正确性校验**：`scripts/dev/verify-m1.sql`（15 组只读查询：类型统计、元数据落库率、
季集号范围、层级完整性、图片归属、扫描问题）。

### M1 改期项

- [ ] **ffprobe 流信息探测** → 改到 M2。理由：探测需要持久化任务队列 + 并发控制 + 失败重试，
      而这些正是 M2 的刮削基础设施；现在先写一套临时的，下个里程碑就要推翻。
      当前 `media_files.probe_state` 已经预置为 `pending`，接上队列即可开跑。
- [ ] **文件系统事件（inotify）与定时扫描** → 改到 M6/M7；目前扫描是手动触发。
      网络挂载本来不触发 inotify，真正需要的是轮询，和定时任务一起做更划算。
- [ ] **外挂字幕与视频的关联** → 改到 M2（现在只统计数量，不建关联关系）。

---

## M2 — 元数据与刮削 🚧（任务队列 + 探测 + provider + 匹配打分器已完成）

> 已完成：PostgreSQL 任务队列（`internal/store/tasks.go` + `internal/worker`）、
> ffprobe 流信息探测（`internal/probe`，9 个真实夹具单测）、批量入队/重置接口、
> 界面上的探测进度与队列水位；TMDB provider + `provider_cache` jsonb 缓存 + 限流退避；
> **匹配打分器 `internal/match`（含 `lmby match` 命令行工具）**；
> **刮削处理器 `internal/scrape`（含 `lmby scrape` 与刮削接口）**。
> 待完成：字段锁定的人工编辑界面、人工匹配界面、搜索。

**参考**：`MediaBrowser.Providers/{Movies,Manager}/*`、`MediaBrowser.LocalMetadata/Parsers/*`、`MediaBrowser.XbmcMetadata/*`（导入参考）

- [x] **PG 任务队列**：`for update skip locked` 抢占、幂等入队（部分唯一索引）、
      优先级、指数退避（30s→2m→8m）、启动/定时回收卡住的任务、历史自动清理、worker 数可配
- [x] **流信息探测**：ffprobe 归一化（容器/编码/profile/分辨率/位深/帧率/HDR/杜比视界/
      音轨/图形字幕/章节）、批量入队、单文件超时、时长回填条目
- [x] 扫描结束后自动入队探测；界面显示探测进度与可手动补跑/重置
- [x] `Provider` 接口（`internal/provider`，语义固定为「按标题+年份搜索 → 按 id 取详情」）
- [ ] provider 注册表（按配置启用与排序）—— 现在只有 TMDB 一个源，等接入第二个源（TVDB/Bangumi）时再加
- [x] **TMDB provider**：search / movie / tv / season / episode / credits / images / external_ids / alternative_titles，语言参数化
- [x] 限流器（令牌桶 + 可配并发）+ 429/5xx 指数退避 + `provider_cache` jsonb 缓存
- [x] **匹配打分器**（`internal/match`，纯标准库、可离线单测）：
      标题相似度（二元组 / 分词 / 包含 / 分文字体系加权，取最大值；繁简与日文旧字体折叠、
      丢掉连接性助词）+ 年份 + 类型过滤 + 集数-时长结构 + 别名命中，
      每项都带权重与说明文字（界面要拿它解释「为什么是这个分」）；
      阈值以上自动、以下进人工队列，**并且要求明显领先第二名**（同名重制版的保险）
- [x] **刮削任务处理器**（`internal/scrape`）：挂到已有队列（`worker.Handler`），
      **必须取详情（含 `alternative_titles`）再决定**、**不给搜索传 year**
      （两条都来自实盘，见下方验收记录）；字段锁定生效（锁住的字段重扫不覆盖）；
      **nfo 优先**（有 nfo 的条目不刮，见 `docs/REQUIREMENTS.md` §0）；
      自动落库 / 进人工队列（候选与打分明细存 `match_candidates`）/ 判定失败；
      CLI `lmby scrape enqueue|run|status|reset` + API `POST|GET /api/v1/libraries/{id}/scrape`
- [x] **季/集级元数据**：剧集匹配上（或从 nfo 读到 provider id）之后，把每一季/每一集的
      元数据拉回来；季/集走 (剧集 provider id, 季号, 集号) 直接对应，不经搜索、不算相似度
- [x] **图片管线**（`internal/images`）：按需缩放输出（`GET /api/v1/items/{id}/images/{kind}?w=&h=&format=&q=`）+
      列出条目图片（`GET /api/v1/items/{id}/images`）+ 本地图优先的覆盖顺序 +
      本地没有时回源下载到数据目录（内容寻址去重，同一张只下一次）+ ETag/304 + 缓存上限清理。
      没做 webp 编码：纯 Go 只有无损 webp 编码器，照片用它体积反而更大（见验收记录）
- [ ] 元数据写入语义：**只写 PG**（不生成 XML、无导出）+ 字段锁定（手改字段重扫不覆盖）
- [ ] GUI：详情页（海报墙 + 剧集视图）、条目编辑（标题/简介/年份/流派/海报选择）、**人工匹配**（搜索候选→指定→应用）、批量匹配
- [ ] 搜索：`pg_trgm` GIN + `tsvector`（中文 bigram，不引 zhparser）
- [ ] **DoD**：无 nfo 的库能一键刮削完成；自测样本自动匹配准确率 ≥ 90%（`scripts/dev/match-sample.sh`
      实测 10 条样本 9 条 auto、0 条误配）；人工匹配可修正；重复刮削零 API 调用（缓存命中）；手改字段不被覆盖

### M2 刮削处理器的验收（2026-09-20，真库全量实跑）

工具：`lmby scrape enqueue|run|status|reset`（`run` = 入队并就地跑完，便于盯单条结果）

| 项 | 结果 |
|---|---|
| 覆盖 | 库里 337 个电影/剧集条目全部跑完：**matched 231 / review 21 / failed 85** |
| 自动匹配率 | 分母只算「搜到过候选」的 253 条：**231 / 253 = 91.3%**（DoD 要求 ≥ 90%） |
| 自动匹配的分数区间 | 最低 **0.864** / 平均 0.996 / 最高 1.000 —— 阈值 0.85 真的在拦，没有低分被自动落库 |
| 重复刮削 | 106 条全部**缓存命中 178 次、回源 0 次，耗时 0 秒**（DoD 里「零 API 调用」） |
| 幂等 | 重跑后状态分布一字不变 |
| 失败构成 | 85 条里 84 条是「搜不到候选」—— 库里那些码流测试片/演示片（`AVC 4K YUV420P8 120fps…《索尼 - DEMO Mont Blanc》`），本来就不是作品 |
| 进人工队列的 21 条 | 共同特征：**标题满分（1.000）但领先第二名不够** —— TMDB 上有多条同名条目（《烟花》《红辣椒》《薄暮》《镜之孤城》…），正是「领先幅度」这道安全网要挡的情况；候选与打分明细已存进 `match_candidates`，人工界面直接展示即可 |

**实机抓到并修掉的两个真 bug**（都不是测试环境的问题）：

1. **同名条目撞唯一索引**：同一个作品被扫成两个条目时，刮削改名会撞
   `media_items_movie_uniq`，原来会白重试到 `max_attempts`。
   现在 `store.ApplyItemMeta` 识别 SQLSTATE 23505 并包装成 `ErrAlreadyExists`，
   刮削处理器把它转成「进人工确认（可能是重复条目）」并写明原因。
2. **排队优先级**：刮削与探测同为优先级 0、按先到先得，结果「刚扫完的 300 条待探测」
   把刮削压到十几分钟之后（实测盯着等了 90 秒没动静）。现在刮削用优先级 10 —— 
   探测是纯背景工作，刮削是用户点了就想看结果的事。

**已知未做**（下一步）：季/集级元数据还没拉（只刮到电影/剧集层级），
图片管线之后还有：字段锁定的人工编辑界面、人工匹配界面、搜索。

### M2 图片管线的验收（2026-09-20，真库 + 合成库实跑）

覆盖顺序（用户定的）：**媒体目录里的本地图 > 用户手选（UI 未做）> provider 下载缓存**。
本地图能直接用就直接送原文件（零拷贝、零磁盘），要缩放才落缓存。

| 项 | 结果 |
|---|---|
| 扫描登记的本地图 | 1220 张：poster 356 / fanart 261 / logo 259 / banner 214 / thumb 130 |
| 原图直给 | 取《钢之炼金术师》剧集海报（不加参数）→ `Content-Length: 320872`，**与磁盘上那张 poster.jpg 一字不差**；853×1280 |
| 缩放 | `?w=300` → 300×450（等比）、50190 字节、带 ETag |
| 缓存 | 第二次请求字节完全相同（md5 一致）、ETag 一致；`If-None-Match` → **304** |
| 回源（合成库：一部没有本地图的电影） | 扫描无图 → 刮削拿到 tmdb 198375 → 取海报时回源下载 w500 原图（139641 字节，500×750）落 `remote/`，输出 300×450；**再取一次不再下载**（remote 文件数 1→1） |
| 缓存清理 | 缩放产物落 `cache/`，超 `images.max_cache_mb`（默认 512）按最旧优先删；回源原图在 `remote/`，算数据不参与清理 |

**关于 webp**：`?format=webp` 没有实现。纯 Go 只有无损 webp 编码器（照片用它体积反而比 jpeg 大），
为它引 cgo 或第三方库不划算；接口先支持 `format=jpeg|png`，空值则「尽量保持原格式」
（png 保持 png，其余转 jpeg）。以后真需要 webp 再单独议。

**关于写入媒体目录**：用户已明确「以后图片也像 nfo 一样存在视频文件旁边」。
当前只读不写（真实测试库是网盘只读挂载，也不具备写条件）；
写回能力与 nfo 一样，等真需要时再加，模型不变：媒体目录里的东西就是权威。

### 季/集元数据与「本地优先到每集」的实测（2026-09-20）

用户要求：本地优先，**本地没有 / 格式不对才去刮**，粒度要细到每一集。

实现：

- 扫描器补上**目录级 nfo**：剧集的 `tvshow.nfo`、季的 `season.nfo`、电影的
  `movie.nfo`/`folder.nfo`（原来只读「同名文件级」nfo，所以剧集/季永远认领不到本地元数据）
- 刮削器按类型分流：电影/剧集走「搜索 + 打分」；
  **季/集走 (剧集 provider id, 季号, 集号) 直接对应** —— 没有候选可比，
  `match_score` 记 NULL 而不是编一个 1.0（不然会把「自动匹配的最低分」这类抽查指标带偏）
- 剧集定位：先用元数据里的 tmdb id（nfo 的 `<tmdbid>` / `<uniqueid type="tmdb">` 会进
  `provider_ids`），没有就按标题搜一次 —— **只用于定位季/集，不写剧集自己的元数据**
- 「格式不对」也算没有本地数据：nfo 解析失败记一条扫描问题、**不标 nfo 状态**，条目留在可刮队列里
- `refreshMetadata` 重扫会连目录级 nfo 一起认领（原来只认领文件级）

| 项 | 结果 |
|---|---|
| 真库重扫后 | 剧集 2/2 认领 `tvshow.nfo`；季 5/5 认领 `season.nfo` |
| 季标题的变化 | TMDB 的「第 1 季」/「特典」→ 你本地的**「第 1 季（觉醒篇）」/「特别篇」/「第 2 季（远征篇）」** |
| 全库状态 | nfo 元数据 **472** / 已匹配 0 / 待人工 0 / 失败 0 / 未刮 0 —— 所有条目都有本地元数据，刮削队列空了 |
| 闭环（合成库：剧集有 tvshow.nfo、S01E01 有 nfo、S01E02 没有） | 剧集 `nfo`：手写标题/简介/流派原样保留，**`provider_ids` 为空**；S01E01 `nfo`：手写集名保留；S01E02 `matched`：TMDB「不老实的少女」+ 简介 + 集 id；季 `matched`：TMDB 简介 + 季 id |
| 定位回退 | 剧集没有 provider id 时按标题搜一次（日志会写明「仅用于取季/集元数据，不写剧集自己的元数据」）；搜不到就安静跳过，条目留在可刮队列里等下次 |

**已知取舍**：

- 季**不写** TMDB 的季标题 —— 本地按季号生成的「第 N 季」比 TMDB 的译名稳
  （TMDB 有时只给英文的 "Season 1"，写进去反而退化）
- 季/集没有「人工队列」这一档：要么定位成功落库，要么（provider 上确实没这一集）
  记为失败并写明原因

### nfo 优先的实测（2026-09-20，用户拍板后）

用户明确要求：**媒体同目录有 nfo 就用 nfo（那是人工花大力气整理的），
只有没 nfo 的才去刮** —— 自动流程永不覆盖 nfo 元数据。这条落在三处：

1. 扫描器：导入 nfo 时标上 `match_state=nfo` + `metadata_source=nfo`
   （空 nfo 不算 —— 否则空文件会把条目钉死、永远不刮）；
2. 刮削器：`nfo` / `manual` / `matched` 默认一律跳过，只有显式 `--force` 才覆盖（先警告）；
3. 入队 SQL：`nfo` 不进队列（同时看 state 与 source，防止历史数据里两者不同步）。

| 项 | 结果 |
|---|---|
| 真库 337 条里有 nfo 的 | **335 条**（只有 2 条没有）—— 用户说的「nfo 是花大力气做的」完全对得上 |
| `refreshMetadata` 重扫后 | 335 条转为 `nfo` 状态；231 条 `matched` 里**有 44 条的标题被 nfo 收回** |
| 标题被改回的样本 | 少女杀手→**风筝：拯救者**、整容液→**奇奇怪怪：整容液**、处女朋克：发条女孩→**维珍朋克：发条女孩**、地海战记→**地海传说**、宝贝公主 Paradise 0→**宝贝公主** |
| 闭环验证（造一个小库：一部手写 nfo + 一部没有） | 有 nfo 的那条：`nfo` 状态，标题/简介/流派全是手写的值，`match_score` 为空（**从头到尾没被刮过**）；没 nfo 的那条：`matched`，TMDB 简介/流派/外部 id 全部写入 |
| 重扫代价 | 479 个文件「没变也重读 nfo」只花 **6.4 秒**（CIFS 上单个小文件 ~11ms） |

**顺带新增的能力**：`POST /api/v1/libraries/{id}/scan` 的 body 支持
`{"refreshMetadata": true}` —— 手改了 nfo 之后不必去 touch 媒体文件
（网络盘上那样会连带触发几万个文件的重新探测），重扫一次就能让 nfo 生效。

### M2 匹配打分器的验收（2026-09-20，开发容器实测）

工具：`scripts/dev/container-verify.sh`（编译+单测）+ `scripts/dev/match-sample.sh`（拿真库抽样实盘跑）

| 项 | 结果 |
|---|---|
| 单测 | `internal/match` 18 项全绿，其中 3 条固定语料是**真实 TMDB 命中**（31911 / 97525 / 198375） |
| 真实库实盘 | 2 部剧集 + 8 部随机电影 → **9 条 auto**（其中 7 条满分 1.000）、1 条 review、**0 条误配** |
| 危险样本 | 《钢之炼金术师 FULLMETAL ALCHEMIST》(2009) vs 2003 版：1.000 / 0.545，领先 0.455 —— 同名重制版自动避开 |
| 别名命中 | 《孔中窥见真理之貌》(2013)：只搜标题只拿到 **0.309**（TMDB 主标题是《偷窺孔》），`--deep` 取回 `alternative_titles` 后命中《孔中窥见真理之貌OVA》→ **0.918 auto** |
| 没有年份时 | 《钢之炼金术师》无年份、候选里同时有本体与 FA：领先只有 0.10 < 阈值 0.12 → **review 而不是 auto**（安全网生效） |
| 非作品文件 | 库里的 `特效内封PGS字幕测试片`、`HEVC 4K … DEMO Mont Blanc` 搜不到候选 → reject，不进人工队列 |

**两条实盘结论（刮削处理器必须照做）**：

1. **必须取详情（含 `alternative_titles`）再决定**：本地中文译名与 TMDB 主标题差得远的不少，
   只看 search 的主标题会漏配（《孔中窥见真理之貌》/《偷窺孔》就是一例）。
2. **不要给 TMDB 搜索传 `year`**：TMDB 的 `year` / `first_air_date_year` 是硬过滤，
   而本地年份可能来自某一季、或干脆解析错了，硬过滤会把正确答案直接筛掉。
   年份交给打分器判（差得多的直接 0 分）。已在 `scripts/dev/match-sample.sh` 与 `lmby match` 里按这个原则做。

**已知不够好的地方**（不阻塞，先记下）：

- 标题相似度对「续作/剧场版」这类前缀关系会给到 0.85~0.92，单靠标题压不住，
  真正把关的是年份与领先幅度。所以**本地年份解析得准不准，直接决定匹配质量**。
- 打分器现在只有两个信号源（年份、集数/时长）。Jellyfin 那样接入「演职员/外部 id」
  交叉验证会更稳，留到有人工匹配界面之后再说。

### M2 前半段的验收（2026-09-20）

| 项 | 结果 |
|---|---|
| 队列实测回收 | 每次 `systemctl restart` 都看到「已回收上次中断的任务 count=4」—— 否则这些任务要白等 30 分钟 |
| 探测落库 | 编码/位深/profile/分辨率/HDR/音轨/图形字幕/章节全部写进 `media_files` 的 jsonb |
| 探测单测 | `internal/probe` 9 个**真实 ffprobe 夹具**（HEVC 10bit + 双 ASS + 章节、AV1 无音轨、H.264 4:4:4 10bit + PCM、老 AVI、杜比视界、HDR10、TrueHD、PGS 图形字幕、误落进来的 jpg）全绿 |
| 数据库侧校验 | `scripts/dev/verify-probe.sql`（12 组只读查询）|

**吞吐现实提醒**：测试库后面是网盘，ffprobe 大量随机寻道（MKV 的 cues、MP4 的 moov），
单文件实测几秒到十几秒。已做三件事缓解：
`-probesize 10M -analyzeduration 10M` 限制读入量（也避免 AVI 无索引时全文件扫描）、
单文件 2 分钟超时（超时标记失败且不重试，不会死循环）、worker 数配置化。
**探测是后台任务，慢不阻塞扫描与浏览** —— 这正是把它放进队列而不是内联在扫描里的原因。

---

## M3 — 播放核心：DirectPlay / DirectStream（1.5 周）

**参考**：`MediaBrowser.Model/Dlna/StreamBuilder.cs` ⭐、`DirectPlayProfile/CodecProfile/TranscodingProfile/SubtitleProfile/ConditionProcessor.cs`、`Jellyfin.Api/Controllers/{VideosController,MediaInfoController,HlsSegmentController}.cs`

- [ ] 静态分发：HTTP `Range`（单区间，可选多区间）、`ETag`/`Last-Modified`、容器→Content-Type、断点续传
- [ ] 客户端能力上报接口 + 服务端 **DeviceProfile** 表（可配置、可自定义客户端 profile JSON）
- [ ] **播放决策引擎 v1**：输出 mode + 每流（视频/音频/字幕）独立动作 + **理由字符串**（UI 显示"为什么转码"）
- [ ] DirectStream：`ffmpeg -c copy` remux 到 fMP4/TS + HLS 分发（复用 tvhub 已验证的参数）
- [ ] 播放会话：创建/心跳/停止、会话 token、并发与权限校验、SSE 状态推送
- [ ] 播放状态同步：进度上报、续播、已看标记、每用户独立（复用 tvhub 的 `internal/stream` 经验）
- [ ] GUI 播放器 v1：hls.js + 自定义控制条（播放/暂停/seek/音量/全屏/进度记忆/错误回退）
- [ ] **DoD**：Chrome + Safari + iOS 三端可起播 h264/aac 的 mp4 与 mkv(remux)；快速拖动进度不崩；关闭页面后 ffmpeg 进程被回收

---

## M4 — 转码与硬件加速（3 周，**最大风险块**）

**参考**：`Encoder/EncoderValidator.cs` ⭐（能力探测）、`Transcoding/TranscodeManager.cs`、`Encoder/MediaEncoder.cs`、`Encoder/EncodingUtils.cs` ⭐（命令拼装）、`DynamicHlsController.cs`

- [ ] ffmpeg 探测：`-hwaccels`/`-encoders`/`-filters` + **真跑 1 秒小样验证** + 结果缓存 + GUI 手动覆盖
- [ ] 能力矩阵建模：源(视频/音频/字幕/位深/HDR) × 目标 profile × 硬件后端 → 命令模板
- [ ] CPU 基线：libx264/libx265（preset/CRF/最大码率）
- [ ] 硬件后端逐个落地并真机验证：**QSV → NVENC/NVDEC → VAAPI → VideoToolbox → AMF**
- [ ] 音频：AAC/AC3/Opus、多声道 downmix、直通优先
- [ ] 字幕：文本 → WebVTT 轨道（外挂/内嵌）；图形字幕(PGS/VOBSUB) → burn-in 或明确提示不支持；外挂字幕上传
- [ ] HDR：HDR10/HLG → SDR tone mapping（含硬件路径）；DV P5 → HDR10 策略
- [ ] **节流与回收**：预生成 N 片 → `SIGSTOP` ffmpeg → 分片被消费时 `SIGCONT`；空闲 TTL 回收进程 + 清理分片目录（**禁用 `-re`**）
- [ ] 会话复用（同 item + 同 profile 共享一路）+ 并发上限 + 队列 + 抢占
- [ ] 质量档位（码率阶梯）+ 手动切换；带宽自适应（可选）
- [ ] Trickplay：章节图 + 时间轴精灵图（异步 + 落盘缓存 + 可关闭）
- [ ] 转码监控页：活跃会话、实时 fps/speed/码率、一键终止
- [ ] `docs/TRANSCODING.md`：支持矩阵 + 实测数据
- [ ] **DoD**：目标机器 1080p HEVC→H264 硬件转码 ≥ 1x 实时；转码中拖动/切集/关页无残留进程；4K→1080p 与 HDR→SDR 各有一条真实通过用例

> 📌 **发 v0.1**：M3 结束即可发布（能播 h264 库 + 直出/remux）。M4 结束发 **v0.2**（硬件转码）。

---

## M5 — 直播电视（1.5 周）

**参考**：`C:\Users\admin\Desktop\dev\TV`（tvhub，已验证，AGPL 可复用）+ Jellyfin `src/Jellyfin.LiveTv/*`

- [ ] `tv_sources` / `tv_channels` 表 + 迁移
- [ ] M3U 源导入：粘贴 / 上传文件 / 订阅 URL 拉取；按地址增量更新（保留启用状态与收藏）
- [ ] 频道管理 GUI：分组、搜索、启用/禁用、收藏、排序、logo
- [ ] 频道播放：每频道共享一路 ffmpeg HLS 会话（`-c:v copy` + 音频 MP2→AAC），空闲回收 + 分片清理
- [ ] 外部播放器出口：`GET /s/<token>/playlist.m3u`
- [ ] 定时刷新订阅源（cron）+ 失效源标记
- [ ] **DoD**：导入 149 台单播源，浏览器起播 < 2 秒；两人同时看同一频道只跑一路 ffmpeg；无人观看后 45s 内进程退出

---

## M6 — GUI 完整化（2 周，可与 M3-M5 交叉）

- [ ] 布局：库导航 + 海报墙（虚拟滚动，万级条目流畅）+ 筛选（类型/流派/年份/评分/已看/收藏）+ 排序
- [ ] 详情页：背景图、简介、演职员、季集列表、多版本选择、相关推荐
- [ ] 首页「继续观看」+ 最近添加
- [ ] 搜索：即时联想 + 结果分面（电影/剧集/人）
- [ ] 收藏、播放列表、合集（Collection）
- [ ] 设置页：库管理、扫描计划、转码/硬件、provider 与 TMDB Key、用户与权限、日志查看、直播源
- [ ] 明暗主题切换：**首屏/主页右上角常驻入口**（不藏在设置里）+ localStorage 先落盘，登录后与服务端偏好双向同步；跟随系统(`prefers-color-scheme`)作为初始默认
- [ ] 响应式（手机可用）+ i18n（zh-CN / en-US）
- [ ] **DoD**：10 万条目海报墙滚动达到可接受帧率；手机上能完整完成"找片→播放→续播"

---

## M7 — 多用户、权限、分享（1 周）

**参考**：`Jellyfin.Server.Implementations/Users/*`

- [ ] 用户 CRUD（管理员）、库级访问权限、家长分级（official rating）、并发流上限
- [ ] **个人中心**：修改密码（**需验证旧密码**）、修改显示名、主题/语言偏好、字幕与音轨偏好
- [ ] 个人中心的「我的设备 / 会话」列表 + 撤销指定会话（踢自己下线）
- [ ] 管理员视角：用户列表、重置密码、禁用账号、强制下线
- [ ] 外链分享（受限 token：有效期/次数/是否允许下载）
- [ ] 审计日志（登录、改密、管理操作）
- [ ] **DoD**：两用户进度/收藏互相隔离；无权限库在 API 与 GUI 双层拦截；普通用户无法修改他人密码或越权访问；改密后除当前会话外其余按策略失效；个人中心在手机上可用

---

## M8 — 迁移、运维、发布（1.5 周）

- [ ] **Emby/Jellyfin 导入器**（`lmby import-emby`）：读 `/config/metadata/library/**/*.nfo` + 媒体同目录 nfo → 匹配灌入 PG；可选读 `library.db` 导入播放进度；图片路径映射
- [ ] 缓存与清理策略：转码分片、trickplay、图片缓存、日志轮转、孤儿文件清理
- [ ] `lmby backup` / `lmby restore`（PG dump + 配置 + 缓存策略）
- [ ] `/metrics`（Prometheus，可选）+ 日志分级
- [ ] 文档：README（截图 + 5 分钟快速开始）、`docs/CONFIG.md`、`docs/TRANSCODING.md`、`docs/ARCHITECTURE.md`、`docs/ADR/*`
- [ ] 发布流程：GitHub Actions 交叉编译（linux amd64/arm64）+ 多架构镜像 + `SHA256SUMS.txt`
- [ ] **DoD**：全新用户照 README 30 分钟内从零跑起来并播出第一个视频

---

## M9 — 二期候选（先不做，留扩展点）
> 音乐库与 DLNA/UPnP **已砍**，不在候选列表内（本项目就是纯 Web 播放站点）。
- [ ] DVR / 录制 / EPG（XMLTV）
- [ ] Jellyfin API 兼容层（第三方客户端）
- [ ] Tauri 桌面套壳 / Capacitor 移动套壳
- [ ] 转码产物持久化缓存、多节点转码卸载
- [ ] SyncPlay（同步观看）

---

## 排期（单人全职估算）
| 里程碑 | 工时 | 累计 | 产出 |
|---|---|---|---|
| M0 骨架 | 1 | 1 | 能跑起来的空壳 |
| M1 扫描 | 2 | 3 | 库能扫进来 |
| M2 元数据 | 2 | 5 | 有海报和简介 |
| M3 播放核心 | 1.5 | 6.5 | **v0.1** 能播 h264 |
| M4 转码 | 3 | 9.5 | **v0.2** 硬件转码 |
| M5 直播 | 1.5 | 11 | 能看电视 |
| M6 GUI | 2 | 13 | 好用 |
| M7 多用户 | 1 | 14 | 家庭共用 |
| M8 迁移/发布 | 1.5 | 15.5 | **v1.0** |

## 三条纪律
1. **每个里程碑必须有真实数据验收**，尤其转码：写完立刻真机 + 真浏览器验证，不攒到最后。
2. **不做超出需求的抽象**。Emby 的"重"很大部分来自插件系统与通用框架——LMBY 只要够用的接口。
3. **只读参考 Jellyfin，不复制其代码**（GPL-2.0 vs 本项目 AGPL-3.0 不兼容）。抄逻辑可以，抄文件不行。

## 2026-09-20 的一条教训：**CI 红了两个月没人发现**

`.github/workflows/ci.yml` 里 backend 与 frontend 一直是绿的，lint 一直是红的：
配置 `.golangci.yml` 是 v1 格式，而 action 装的是 latest（v2），
于是它每次都在「加载配置」这一步就退出 —— 一条检查都没真跑过。
已迁移到 v2 格式，并顺带修完它原本会报的 14 个问题（errcheck 3、unparam 6、unused 3、
staticcheck 1、errorlint 1）。以后改 lint 相关的东西，先在容器里跑 `scripts/dev/lint.sh` 再推。
