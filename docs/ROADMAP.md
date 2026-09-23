# LMBY 开发 Todolist

> 状态：**M0 / M1 / M2 / M3 均已完成并实测验收；M4（转码与硬件加速）除少数项外已完成**（逐项见下）。
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

## M1 — 媒体库与扫描 ✅（已完成并实测验收）

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

## M2 — 元数据与刮削 ✅（已完成并实测验收）

> 已完成：PostgreSQL 任务队列（`internal/store/tasks.go` + `internal/worker`）、
> ffprobe 流信息探测（`internal/probe`，9 个真实夹具单测）、批量入队/重置接口、
> 界面上的探测进度与队列水位；TMDB provider + `provider_cache` jsonb 缓存 + 限流退避；
> **匹配打分器 `internal/match`（含 `lmby match` 命令行工具）**；
> **刮削处理器 `internal/scrape`（含 `lmby scrape` 与刮削接口）**。
> **M2 已全部完成**（下一步是 M3 播放核心，v0.1 计划发在那里）。

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
- [x] **人工匹配界面**（`web/src/pages/Match.tsx` + `GET|POST /api/v1/items/{id}/match`）：
      待确认 / 没找到 两个标签、候选并列对比（**海报 + 分数 + 打分明细**）、
      换个词重搜、标记「不需要匹配」；应用后状态标 `manual`（人工结果优先）。
      截图：`docs/images/manual-match.png`
- [x] 元数据写入语义：**只写 PG**（不生成 XML、无导出）+ **字段锁定 + 条目编辑界面**
      （`web/src/pages/Item.tsx`、`GET|PATCH /api/v1/items/{id}`、`POST /api/v1/items/{id}/scrape`）：
      逐字段编辑/清空、逐字段加锁；锁定的**执行力在 SQL 里**
      （`applyItemMetaSQL`），nfo 重读与 TMDB 刮削都绕不过去，见下方验收记录
- [x] **搜索**（`internal/store/search.go` + `GET /api/v1/search` + `web/src/pages/Search.tsx`）：
      `pg_trgm` GIN + `tsvector`，中文用**二元组**（不引 zhparser）；
      切词是 PG 函数 + **生成列**（`search_vec`，写入时自动维护，改完标题立刻能搜到）；
      查询三路并存：分词命中（`tsvector`）+ 子串兜底（`ILIKE`，负责单字）+ 词相似容忍错字
      （`<%`，阈值 0.4）；界面有查询词/库/类型过滤、分页、浏览器 URL 同步（可收藏可分享）
- [x] GUI 其余部分：**海报墙与剧集视图**（`web/src/pages/Browse.tsx` / `Series.tsx`，
      `GET /libraries/{id}/browse` 只要顶层条目、`GET /items/{id}/children` 给季/集与集数）、
      **批量匹配**（人工匹配页多选 → 批量入队/强制重刮/标记不需要匹配，
      `POST /items/batch`；媒体库页加「元数据刮削」卡片：一键刮削/强制重刮/重置失败的）、
      **设置页**（`web/src/pages/Settings.tsx`：TMDB 凭据存数据库、保存即生效，
      密钥加密存储、只写不回显，带「测试连接」与系统信息）
- [x] 元数据凭据的可运维性：`settings` 表 + `internal/settings` + `internal/secrets`
      （AES-256-GCM，密钥是数据目录里 0600 的 secret.key），
      运行期可换凭据（`tmdb.Client.SetCredentials`，不必重启）
- [x] **DoD**：无 nfo 的库能一键刮削完成；自测样本自动匹配准确率 ≥ 90%（`scripts/dev/match-sample.sh`
      实测 10 条样本 9 条 auto、0 条误配）；人工匹配可修正；重复刮削零 API 调用（缓存命中）；
      手改字段不被覆盖（`scripts/dev/verify-item-edit.sh` 42 项断言，含对照组）

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
图片管线与人工匹配之后还有：字段锁定的编辑界面、搜索、详情页/海报墙。

### M2 人工匹配界面的验收（2026-09-20，无头 Chrome 实跑）

用户要求：机器拿不准的条目要能人工选。页面截图见 `docs/images/manual-match.png`。

后端四个接口（都在 `internal/api/match.go`）：

```
GET  /api/v1/items/{id}/match          读匹配状态 + 候选（含打分明细）
POST /api/v1/items/{id}/match          {"providerId": 431819} 应用选定
POST /api/v1/items/{id}/match/search   {"query": "..."} 换个词重搜
POST /api/v1/items/{id}/match/skip     {"reason": "..."} 标记不需要匹配
GET  /api/v1/libraries/{id}/items?matchState=review,failed   列表支持按状态过滤
```

几个设计点：

- 候选在**刮削时就存进 `match_candidates`（含每项打分明细）**，界面打开即可选，不需要重新搜
- 候选带 TMDB 海报地址（打分器不填这个字段，刮削侧拿详情时顺手补）——
  **同名条目靠海报一眼就能分辩**，这是这张页面存在的意义
- 应用后状态标 `manual`：人工结果优先，自动重扫不再覆盖（与字段锁定同一套语义）
- 季/集不支持人工指定候选（它们是按位置对应的），接口会直接报错

| 项 | 结果 |
|---|---|
| 合成库（烟花/红辣椒/薄暮/自制KTV合集）| 3 条 review（都是「标题满分但领先 0.00~0.12」）+ 1 条 failed |
| 候选明细 | 《烟花》5 个候选：2017 动画版 / 1954 黑白片 / 两条无年份 / 烟花三月 —— 界面上靠海报一眼分辩 |
| 应用候选 | POST → 200，标题/年份/provider_ids 落库，状态转 `manual`；单测还覆盖了撞唯一索引时的友好报错 |
| 换词搜索 | 传「钢之炼金术师 FA」→ 带分项的候选（年份差 12 年 → 0 分，总分 0.629）|
| 标记不需要匹配 | 状态 `manual` + 原因写进 `scrape_error`（自制片、样品片不会再挂在待处理里）|
| 页面渲染 | 无头 Chrome 截图确认：导航/标签/卡片/候选面板（含海报）/打分明细都正常，亮色主题 |
| 回归 | 媒体库页照常（条目列表 + 分页 + 探测进度 + 扫描记录）|

### M2 GUI 收尾（海报墙 / 剧集视图 / 批量 / 设置页）的验收（2026-09-20）

界面上最后两块：**浏览**与**设置**。

| 接口 | 作用 |
|---|---|
| `GET /api/v1/libraries/{id}/browse?kind=&sort=&limit=&offset=` | 海报墙：只要**顶层**条目（电影/剧集），可按标题/年份/最近添加排序 |
| `GET /api/v1/items/{id}/children` | 子项：剧集 → 季（带每季集数）→ 集 |
| `POST /api/v1/items/batch` | 批量：`scrape`（可 force）/ `skip`（标记不需要匹配），一次一条 SQL 排完 |
| `GET /api/v1/settings` | 设置页内容（TMDB 状态 + 系统信息）；**密钥永不可显** |
| `PUT/DELETE /api/v1/settings/tmdb` | 保存 / 恢复为配置文件的值（管理员专属）|
| `POST /api/v1/provider/test` | 拿当前凭据真打一次 TMDB（绕开缓存 —— 缓存命中会假装通着）|

| 项 | 结果 |
|---|---|
| `scripts/dev/verify-browse.sh` | **40 项断言全绿**：海报墙无季/集/花絮、总数 = 电影+剧集、kind/sort 过滤与分页、层级 parentId 正确、每季集数对得上、参数校验、批量跳转/标记/入队（跑完重扫还原）|
| `scripts/dev/verify-settings.sh` | **38 项断言全绿**：密钥不回显、非管理员 403（临时造了个只读用户）、未登录 401、保存后立刻生效（填错 token → 真实请求立刻失败；**证明不需要重启**）、库里存的是 `enc:v1:` 密文且无明文、恢复为配置文件的值后又能通 |
| `scripts/dev/m2-ui-test.mjs` | **35 项断言全绿**（无头 Chrome）：海报墙（筛选/排序/卡片指向）、剧集视图（季标签 → 集列表 → 点集进该集详情 → 编辑元数据）、人工匹配的勾选与批量条（确认框取消时不误改数据）、设置页（状态/来源/测试连接/保存/恢复/系统信息）。**M6 起剧集入口并进了详情页**，所以断言从「卡片指向 /series/」改成「问接口这条是什么类型」 |
| `scripts/dev/seed-review-item.sh` | 验收库全部是 nfo（没东西可刮）时，用它临时造一条 review 条目来验证人工匹配与批量选择界面（`--restore` 还原）|
| 截图 | `docs/images/poster-wall.png` / `series-view.png` / `settings.png` |

几个设计点：

- **海报墙与条目表分工**：`/libraries/{id}/items` 是原始条目表（含季与集，排查用）；
  `/browse` 只要顶层、带海报，是给人看的。两个都留着。
- **子项数跟子项一起回**：剧集视图要显示「第 1 季 · 20 集」，不这样就得为每一季再发一次请求。
- **批量做成一个接口**：几百条时逐条调用会发几百个请求；而且逐条失败很难说清哪几条成功了。
  上限 500 条（防手写请求把队列灌满）。
- **密钥只写不回显**：`GET` 只回 `hasReadToken/hasApiKey`，界面把它渲染成「已设置（要替换就输入新的）」；
  提交时「没填」与「要清空」是两种意图（指针字段 + `null` 区分）。
- **凭据加密存储**：`internal/secrets` 用数据目录里 0600 的密钥文件做 AES-256-GCM。
  威胁模型写得很具体：防的是**数据库备份单独泄漏**，不防「数据库与数据目录一起被拿走」。
- **数据库优先于配置文件**，但这件事必须在界面上看得见：设置页显示当前值的来源，
  并提供「恢复为配置文件的值」把数据库那条删掉。
- **测试连接走未包缓存的客户端**：否则缓存命中时会回「通着」—— 而用户正是想验证凭据能不能用
  （实测踩到，第一版就是这样）。

### M2 搜索的验收（2026-09-20，真库实跑）

切词与索引：`migrations/0007_search.sql`（PG 函数 + **STORED 生成列** `search_vec`），
查询与排序：`internal/store/search.go`，接口 `GET /api/v1/search?q=&libraryId=&kind=&limit=&offset=`，
界面：`web/src/pages/Search.tsx`（导航第二项「搜索」）。三路匹配并存：

| 路 | 管什么 | 手段 |
|---|---|---|
| 分词命中 | 中文整句/英文整词，可排序 | 二元组切词 → `search_vec @@ plainto_tsquery('simple', lmby_bigram(q))` |
| 子串兜底 | 单字查询（索引里只有两字的单元） | `title ILIKE '%q%'` |
| 错字容忍 | 「钢之炼金术土」也能搜到 | `q <% title`（词相似，阈值调低到 0.4） |

| 项 | 结果 |
|---|---|
| `scripts/dev/verify-search-sql.sh` | **15 项断言全绿**：中文整句→二元组、单字保留、中英混排分段、全角标点丢弃、日文假名、生成列/索引就位、存量行已回填、索引侧与查询侧切法一致 |
| `scripts/dev/verify-search.sh` | **69 项断言全绿**（真库）：整标题首条命中、《钢之炼金术师》命中 5 条、「科学」中段命中、错字「某科土的超电磁炮」仍命中（相似度 0.417）、单字「科」命中（ILIKE）、库/类型/流派过滤、分页、参数校验与鉴权、**改完标题立刻能搜到新标题、旧片段搜不到**；M6 又补了人名搜索、按人筛作品、即时联想与结果分面 43 项（见下方 M6 搜索验收记录） |
| `scripts/dev/search-ui-test.mjs` | **62 项断言全绿**（无头 Chrome）：导航进搜索页→中文查询出卡片→错字查询仍命中那一条→点卡片进**详情页**→URL 参数直接可开→即时联想（分组/键盘/与结果同序）→分面胶囊（数字 == 结果总计、点击筛筛选、× 取消）→人名联想与「人」这一档→亮暗主题 |
| 截图 | `docs/images/search.png` |

**这次踩到的两个环境级真问题**（都不是代码 bug，但会让中文搜索静默失效，所以写进文档与自检）：

1. **数据库编码是 SQL_ASCII**：宿主 locale 是 C 时 `createdb` 默认就建出这种库。
   `length('钢') = 3`、`ascii('钢') = 233`、`to_tsvector` 根本认不出 CJK —— 中文搜索必然失效。
2. **即使编码是 UTF8，`lc_ctype=C` 也会让 pg_trgm 切不出中文三元组**：
   `show_trgm('某科学的超电磁炮')` 返回空集，错字容忍/模糊匹配整路失效。

两者的共同点是**都不报错**，只会在搜索结果里悄悄少东西。现在的处理：

- 应用启动时自检（`internal/store/store.go` 的 `checkEncoding`）：
  编码不是 UTF8 或 `lc_ctype` 不是 UTF-8 就**拒绝启动**，并打印修法；
- `scripts/dev/setup-pg.sh` 建库时显式 `-E UTF8 --lc-collate/--lc-ctype=C.UTF-8 -T template0`，
  并做一次「汉字算不算字母」的探针；
- `scripts/dev/fix-db-encoding.sh`：就地重建（dump → 旧库改名保留 → 用 UTF8 + C.UTF-8 重建 →
  `lmby migrate` 建结构 → `--data-only` 灌数据 → 对齐 identity 序列 → 逐表比对行数 →
  字符语义与中文三元组自检），任何一步不对都给回滚命令。
- 顺带修掉一个**用户自己 dump/restore 也会踩**的坑：切词函数与生成列表达式里的函数名
  必须写成 `public.lmby_bigram(...)`（pg_restore 会把 `search_path` 置空，
  不限定名就会在 COPY 时报「function ... does not exist」）。

**关于索引的限制**：单字查询走 ILIKE，用不上 trigram 索引；库里几万条以内没问题，
真到几十万条时再把这一路收紧（或干脆在索引里也放单字）。

### M2 条目编辑 / 字段锁定的验收（2026-09-20，真库实跑）

界面：`web/src/pages/Item.tsx`（路由 `/items/{id}`，从媒体库条目表或人工匹配面板点进去）。
接口：

```
GET   /api/v1/items/{id}          条目详情（元数据 + 12 个可编辑字段的形态与锁定状态）
PATCH /api/v1/items/{id}          {"fields":{...},"lockedFields":[...]} 逐字段写入 + 整份锁定集合
POST  /api/v1/items/{id}/scrape   {"force":true} 给这一条排一次刮削（解锁后想取回 TMDB 的值时用）
```

两条语义是**刻意相反**的，界面上也照实写出来：

- **人工编辑**：写什么就是什么（空串 / null 真的落库）——“我就是要清空这一格”
- **自动流程**（nfo 重读、TMDB 刮削、人工指定候选，都走 `ApplyItemMeta`）：空值不覆盖，
  且**跳过 `locked_fields` 里的字段**

字段锁定的执行力放在 **SQL 里**（`applyItemMetaSQL` 的 `case when locked_fields ? 'title' then title else …`），
不放在调用方：调用方有三处，分散判断迟早漏一个，而漏掉的后果是「人工改的数据被悄悄覆盖」——
这种事故从数据上看不出来。两条离线单测钉住它：每个可编辑字段都必须有对应的锁判断；
`internal/scrape` 的字段常量必须与 store 的字段表同名（不能一个写 `providerIds`、一个判 `providers`）。

| 项 | 结果 |
|---|---|
| 真库验收 `scripts/dev/verify-item-edit.sh` | **42 项断言全绿**（改字段 → 锁简介 → `refreshMetadata` 重扫 → 还原）|
| 锁定生效 | 重扫读了 **479 份 nfo**，锁住的简介**没被覆盖** |
| **对照组（关键）** | 同一次重扫里**没锁的标题被 nfo 改回去了** —— 证明这次重扫真的重写了字段，而不是「什么都没读所以什么都没变」|
| 单条重刮 | 对 nfo 条目入队 → 队列跑完，状态/标题/简介一字不变（nfo 优先，处理器直接跳过）|
| 参数校验 | 未知字段名（`providers`）/ 类型不对 / 评分越界 / 清空标题 / 空 body / 未登录 → 400/401，且错误信息列出可用字段名 |
| 还原 | 标题、简介、provider_ids、锁定集合、匹配状态、元数据来源全部还原回跑之前的值 |
| 界面验收 `scripts/dev/item-edit-ui-test.mjs` | **37 项断言全绿**（无头 Chrome）：改简介并保存、非法输入被挡在保存之前、勾锁后行上出现色条与「已锁定 1 个字段」、切主题不丢表单状态、全部解锁、最后还原 |
| 截图 | `docs/images/item-edit.png`（暗色，简介已锁）|

几个设计点：

- **状态不动，锁定逐字段表达**：改一格不等于「整条交给人工」，所以编辑本身不改 `match_state`
  （只有从 `review` / `failed` 上编辑时才顺手标 `manual` —— 那正是这两种状态在等的处理）；
  「哪些字段不能被覆盖」完全由 `locked_fields` 表达
- **`sort_title` 跟着标题派生**，不单独编辑：否则改完标题，列表里的排序位置还是旧的
- **`runtime` 按分钟填**（库里是 100ns 的 tick，与 ffprobe 同一个单位），换算只在一处发生
- **单条重刮的 dedupe key 与批量入队相同**（`item:<id>`）：已经在队列里时把它升级成 force，
  而不是再排一条 —— 否则用户点了「重新刮削」会觉得没反应
- 媒体库条目表里的标题现在是链接；人工匹配面板里也有「编辑字段与锁定」入口

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

## M3 — 播放核心：DirectPlay / DirectStream ✅（已完成并实测验收）

**参考**：`MediaBrowser.Model/Dlna/StreamBuilder.cs` ⭐、`DirectPlayProfile/CodecProfile/TranscodingProfile/SubtitleProfile/ConditionProcessor.cs`、`Jellyfin.Api/Controllers/{VideosController,MediaInfoController,HlsSegmentController}.cs`

- [x] 静态分发：HTTP `Range`（单区间/多区间/后缀/越界 416 全交给 `http.ServeContent`）、`ETag`/`Last-Modified`/`If-None-Match`、容器→Content-Type、断点续传
- [x] 客户端能力上报：前端用 `MediaSource.isTypeSupported` 实测后随开播请求上报（**保守默认档兜底**）
      —— 比静态 DeviceProfile 表更准；没做「服务端可配置 profile JSON」，因为浏览器能力只能实测，配置表反而会撒谎
- [x] **播放决策引擎 v1**（`internal/playback`，纯函数 + 27 项单测）：输出 mode + 每流（视频/音频/字幕）独立动作 + **理由字符串**（界面上的「为什么这么播」）
- [x] DirectStream：`ffmpeg -c copy` remux 到 HLS fMP4（需要时音频转 AAC 并把多声道降到立体声）+ 分片分发
      —— **窗口式预生成**（默认 300s）：`-c copy` 比实时快几十倍，不限量会在几秒内把整部电影拷进磁盘
- [x] 播放会话：创建/查询/seek/心跳/停止、会话与用户绑定（别人的会话一律 403）、每用户并发上限、并发满时淘汰最久未看的会话
- [x] 播放状态同步：进度心跳（10 秒）、续播、已看标记（> 92% 自动）、**每用户独立**、首页「继续观看」
- [x] GUI 播放器：hls.js（Safari/iOS 走原生 HLS）+ 自研控制条（播放/拖动/音量/全屏/音轨与字幕切换/重新载入/快捷键）+ 错误回退
- [x] 字幕：内嵌文本字幕按需抽成 WebVTT（进程内单飞 + 落盘缓存，首次抽取要读源文件所以走「202 正在准备 + 前端轮询」）；图形字幕明确给出「需要烧录，M4」
- [x] **DoD（实测）**：Chrome（CDP 真播）起播 mp4 直出与 mkv(remux) 均通过；拖到已生成窗口之外自动续段；关闭/离开页面 1 秒内回收 ffmpeg 与分片

本次的两处「与路线图不同」的决定：

1. **没做 SSE 状态推送** —— 播放状态由播放器自己掌握（它比服务端更早知道 PTS），
   出错时调一次状态接口就能拿到 ffmpeg 的 stderr 尾巴。为它维护一条广播链路收益很低。
2. **Safari / iOS 未实测** —— 手上没有 Apple 设备；代码路径（原生 HLS、hevc 的 `hvc1` 标签、
   字幕 WebVTT 旁路）都已就位，但**未经真机验证**，发版说明里会写明。

---

## M4 — 转码与硬件加速（3 周，**最大风险块**）

**参考**：`Encoder/EncoderValidator.cs` ⭐（能力探测）、`Transcoding/TranscodeManager.cs`、`Encoder/MediaEncoder.cs`、`Encoder/EncodingUtils.cs` ⭐（命令拼装）、`DynamicHlsController.cs`

- [x] ffmpeg 探测：`-hwaccels`/`-encoders`/`-filters` + **真跑 1 秒小样验证** + 结果缓存（界面上的手动覆盖还没做）
- [x] 能力矩阵建模：源(视频/音频/字幕/位深/HDR) × 目标 profile × 硬件后端 → 命令模板
- [x] CPU 基线：libx264/libx265（preset/CRF/最大码率）—— 本机实测 `veryfast` 只有 0.9x 实时，所以它只是兜底
- [x] 硬件后端落地：**VAAPI 真机验证**（硬解硬编 11.8x）；QSV/NVENC/VideoToolbox/AMF 参数照 Jellyfin 写、**未真跑**
- [x] 音频：AAC/多声道 downmix、能直通就直通
- [x] 字幕：文本 → WebVTT（外挂还没做）；ASS/SSA → 前端 libass；**图形字幕(PGS/VOBSUB) → burn-in**（外挂字幕上传未做）
- [x] HDR：HDR10/HLG → SDR tone mapping（走软件链，`tonemap_vaapi` 在本机 iHD 上实测用不了）；DV P5 → HDR10 策略未做
- [x] **节流与回收**：预生成 N 片 → `SIGSTOP` ffmpeg → 分片被消费时 `SIGCONT`；空闲 TTL 回收进程 + 清理分片目录（**禁用 `-re`**）
- [x] 会话复用（同 item + 同编码参数共享一路）+ 并发上限（队列/抢占未做）
- [x] 质量档位（码率阶梯）+ 手动切换；带宽自适应未做
- [ ] Trickplay：章节图 + 时间轴精灵图（异步 + 落盘缓存 + 可关闭）
- [x] 转码监控页：活跃会话、实时 fps/speed/码率、一键终止
- [x] `docs/TRANSCODING.md`：支持矩阵 + 实测数据
- [x] **DoD**：目标机器 1080p HEVC→H264 硬件转码 ≥ 1x 实时（实测 11.8x）；
      转码中拖动/切集/关页无残留进程；4K→1080p 与 HDR→SDR 各有一条真实通过用例

> 还差的（M4 收尾清单，**2026-09-22 决定整体移到二期**，不阻塞 M5/M6）：
> `-copyts -avoid_negative_ts disabled`（绝对时间轴，能删掉播放器的窗口簿记，风险最高）、
> 外挂字幕的 GB18030 编码探测、把服务端字体目录整个喂给 libass（mkv 内封字体已做）、
> Trickplay、探测补 `rotation`/`probe_score`、DV P5 → HDR10、带宽自适应、外挂字幕上传。
> 已顺手修掉的两个真 bug：Hi10P 硬解起不来导致「放不了」（现自动降级软解）、从影片中途续播被节流卡死。

> 📌 **发 v0.1**：M3 结束即可发布（能播 h264 库 + 直出/remux）。M4 结束发 **v0.2**（硬件转码）。

---

## M5 — 直播电视（1.5 周）

**参考**：`C:\Users\admin\Desktop\dev\TV`（tvhub，已验证，AGPL 可复用）+ Jellyfin `src/Jellyfin.LiveTv/*`

- [x] `tv_sources` / `tv_channels` 表 + 迁移（另加 `tv_favorites`；删源不删频道，url 是频道的身份）
- [x] M3U 源导入：粘贴 / 订阅 URL 拉取；按地址增量更新（保留启用状态、收藏、探测结果）
      —— 接口层已完成并验收；**选文件上传**属前端（随 M6 设置页）
- [x] 频道管理 GUI：分组、搜索、启用/禁用、收藏、排序、logo + 失效标记
      （`web/src/pages/LiveTV.tsx`：列表行内改启用/收藏、展开式编辑改名/分组/排序/logo、
      按探测结果筛（`?probe=pending|ok|failed`）；**接口先于界面一年就齐了**，这次只是把它用起来）
- [x] 直播前端：Live TV 页 + 播放器 —— 同一页里「列表 + 就地播放」：
      点频道就在页内起播（直播切台频繁，独立路由每次都要退回去），
      切台/关页面会退掉订阅（否则服务端会为「没人在看」的频道白跑最多 45 秒）；
      播放器直接用原生 `<video controls>`（直播没有进度/音轨/字幕可调，自研控制条只是多余）；
      管理员的源与探测面板也在这一页（导入/刷新/间隔/删除 + 补探/重探与进度）。
- [x] 频道播放：每频道共享一路 ffmpeg HLS 会话（`-c:v copy` + 音频 MP2→AAC），空闲回收 + 分片清理
      滚动窗口 `delete_segments+omit_endlist` + 1 秒分片（实测数据见 `docs/notes/livetv.md`）
- [x] 外部播放器出口：`GET /s/<token>/playlist.m3u`（HMAC 签名 + 过期；限次数/限下载留给 M7）
- [x] 定时刷新订阅源（cron）+ 失效源标记（`probe` / `probe_ok` / `probe_at`）
      —— 服务内调度器（每一跳只查「哪些源到点了」，间隔由每个源自己的
      `refresh_interval_minutes` 决定，改间隔不必重启；`[livetv] auto_refresh=false` 可关）+ 
      `lmby livetv status|refresh|probe` 三个命令行入口；
      探测是**真连一次源站**（ffprobe 走完协议握手，参数与播放共用 `livetv.InputArgs`），
      判定写回 `probe/probe_ok/probe_at`，可按分组/指定频道/只补未探的来探；
      **ffprobe 不可用时一条结果都不写**（否则「工具没装」会变成「全部频道失效」）
- [x] 频道单条读取接口 `GET /api/v1/livetv/channels/{id}` + 列表按探测结果筛（`?probe=pending|ok|failed`）
      —— 顺带修掉一个旧缺口：验收脚本靠这个地址还原收藏现场，以前拿到 404 会多切一次收藏
- [ ] 直播前端：Live TV 页 + 播放器
- [x] **DoD**：导入 149 台单播源（其中 **26 台源站不可达** —— 它们 302 到一个从这张网连不上的地址，
      正好说明失效标记是必需的）；起播 **1351ms**（DoD < 2 秒；口径：服务端到首个分片可用，
      浏览器端随前端落地再验）；两人同看**只跑一路** ffmpeg（会话 `viewers=2`、进程数 1）；
      无人观看 **45s** 退出（判定按 `IdleSeconds - reapInterval`，实测 40~45s）
      —— 浏览器端起播已在 S3 真机复核（`livetv-ui-test.mjs` 三次运行 **691 / 1145 / 1475 ms**，均 < 2 秒）
      （另：上文的「26 台不可达」已被 S4 的全量探测纠正为 67 通 / 83 不通，见下）

> **真跑验收**
>
> M5-S1/S2（2026-09-22 早些时候）：`verify-livetv.sh` **32/32**、`verify-livetv-play.sh` **30/30**；
> 当时改过与点播共用的 `internal/stream`，所以回归了 `verify-play.sh` 108/108 与 `verify-transcode.sh` 100/100。
>
> M5-S4（本次）：`scripts/dev/verify-livetv-sync.sh` **60/60**、`verify-livetv.sh` **32/32**（复跑）、
> `verify-livetv-play.sh` **30/30**（复跑）。

### M5 S4 验收记录（2026-09-22，开发容器实测）

**新脚本** `scripts/dev/verify-livetv-sync.sh` **60/60**。它**自造源**而不是依赖真实 IPTV：
本机 `python3 -m http.server` 上摆三个频道（一条真视频 / 一条连接被拒 / 一条不应答）
加一份订阅列表，于是「探测判定」「按分组探」「按结果筛」「url 型源的刷新」都是确定的；
最后临时把 `[livetv] refresh_tick_seconds` 调到 5 并重启服务，**真验服务内置的调度器会不会自己刷**，
再验 `auto_refresh = false` 时它确实不动手，最后恢复配置（EXIT 陷阱）。

**真源全量探测**（重庆联通单播源，以下均为**实例测量值**）：

| 项 | 值 |
|---|---|
| 频道数 | 150（启用中且有地址） |
| 耗时 | 4 并发 **164 秒** / 单并发 **642 秒** |
| 判定 | 通 **67** / 不通 **83**（两种并发**完全一致**，集合逐个对比零差异） |
| 能通的摘要 | `H.264 1920x1080 / MP2 立体声 48kHz（0.3~0.8s）` |
| 不通的摘要 | 多数是 `超时（8s 内没有应答）`；少数是 `源站故障：… Server returned 5XX` |

**顺带纠正一个旧数字**：M5-S2 记的「149 台里 26 台死源」**不是死源总数** ——
那个数字来自播放验收脚本**「试到第一个能起播的就停」**的路径（它只试了前 27 台）。
全量探测（每一台都真连）在同一时刻给的是 **67 通 / 83 不通**，而且并发与串行的判定
一模一样 —— 所以这批不通的是源站那边真的不应答，不是我们探得太急。
这正是「失效源标记」要做成**能看全量、能重探**的功能的原因：「26」这类数字
会随脚本路径与时刻变化，只有真跑一遍才知道；而探测结果也只是「上次真连的结果」。

**验收过程中抓到的两个真问题**：

1. `ListTVChannelsForProbe` 的选列漏了 `favorite`（`scanTVChannel` 按 16 列扫），
   于是**一条频道都选不出来**、探测全是空的 —— 服务日志里是
   `number of field descriptions must equal number of destinations, got 15 and 16`。
   这正是新验收脚本存在的意义（单测碰不到 SQL 与 scan 的列数对齐）。
2. 新验收脚本自己的健康检查地址写成了 `/api/v1/health`（实际是 `/healthz`），
   所以「重启后服务起没起来」永远判失败；容器里的 `/root/deploy-lmby.sh` 有**同一个错**
   （一直打印 `health: 404`、`lmby -version` 也不是合法子命令），已一并修掉。

### M5 S3 直播前端验收记录（2026-09-22，真浏览器）

界面：`web/src/pages/LiveTV.tsx`（导航新增「直播」）+ `web/src/components/LiveSources.tsx`
（源管理 / 探测面板，仅管理员）。

`scripts/dev/livetv-ui-test.mjs`（CDP 驱动真 Chrome）**42/42**：
导航入口 → 列表渲染（150 台）→ 探测徽标（通 / 失效）→ 按失效筛（83 台全是失效）→
分组筛（12 台）→ 搜索 → 收藏（真点按钮 + 接口回读 + 还原）→ 停用/启用（同上）→
失效频道起播给出人话错误（不是无休止转圈）→ **真起播**（readyState≥2 且 currentTime>0）
→ 播放中的行被标出 → 外链（免登录 200/503、改一个字符 401/410）→ 停止 →
管理员能看到源/探测面板与导出入口 → 普通用户看不到源/探测但能起播 → 页面无 JS 报错。

| 项 | 值 |
|---|---|
| **浏览器端起播**（点击 → playing 事件，三次运行） | **691 / 1145 / 1475 ms**（DoD < 2 秒，**真机复核通过**） |
| 失效频道起播的错误文案 | `拉流失败：等待转封装起步超时：等待第一个分片超过 8s` |
| 外链 | `/s/<token>/playlist.m3u` 免登录 200；改一个字符 → 401 |
| 截图 | `docs/images/livetv.png`、`docs/images/livetv-player.png` |

验收时抓到的都是**测试脚本自己**的错（忘了真去点按钮、拿 `waitFor` 的返回值当数据用），
不是页面的问题 —— 但这正好说明界面验收要「真点、真回读接口」，光看 DOM 有没有渲染出来不算。

---

## M6 — GUI 完整化（2 周，可与 M3-M5 交叉）

- [ ] 布局：库导航 + 海报墙（虚拟滚动，万级条目流畅）+ 筛选（类型/流派/年份/评分/已看/收藏）+ 排序
- [x] 详情页：背景图、简介、演职员、季集列表、多版本选择、相关推荐
      （`web/src/pages/Detail.tsx`，`/item/:id`；旧的 `/series/:id` 重定向过来，
      `Series.tsx` 已删除 —— 剧集与电影共用一页，季集列表就在下面）：
      背景图 + 海报 + 标题/年/时长/评分/分级/流派/工作室/简介；继续观看进度与「从头播放」；
      剧集有季标签 + 集列表（点进去是该集详情）；
      **演职员**来自 nfo（见下方验收记录：388 个条目、7093 条关系）；
      **多版本**：一个条目有多个文件时给选择器，选中的版本写进播放地址 `?file=<id>`
      （接口与服务端决策引擎配套，见 `playRequest.FileID`）；
      **相关推荐**离线可算（同库 + 同类型 + 共同流派，评分高的在前）——
      不依赖 TMDB，所以全本地 nfo 库也有推荐。
- [x] **首页「Netflix 风格」：大屏轮播 + 推荐行**（`web/src/pages/Home.tsx` + `GET /api/v1/home`）：
      轮播取最近更新/入库的 10 条（宽幅背景 + 标题/简介/流派/评分 + 播放与详情），
      8 秒一张、悬停暂停、可点圆点/箭头切；下面横滑行：**继续观看**（带进度条）、
      **为你推荐**（按观看历史的「流派画像」打分，离线可算；副标题写「因为你看过《X》」，
      行头显示画像 chips）、**最近添加**；没有观看记录时退成「评分最高」。
      详见 `docs/notes/home.md`
- [x] **搜索：即时联想 + 结果分面（电影/剧集/人）**（`web/src/components/SearchBox.tsx` +
      `web/src/pages/Search.tsx`，接口 `GET /search{,/facets,/people,/suggest}`）：
      **联想**（防抖 200ms、旧响应按序号丢弃、与结果同一排序、只看已敲过的关键词不看当前筛选、
      带海报缩略图与人名分组）、**分面**（类型 / 媒体库 / 流派 / 人 四档带命中数，
      每一维统计时关掉自己那一维的筛选）、**「人」这一档**（人名搜索 + 参演作品数，
      点人名 → `personId` 筛作品；人名接的是 0010 的演职员表，不联网、不占 TMDB 配额）；
      详见 `docs/notes/search.md`
- [x] 收藏、播放列表、合集（Collection）—— **全部完成**（迁移 0012 + 0013）
      - **收藏**：`favorites(user_id,item_id)` 主键幂等、不存计数器；接口 `GET /api/v1/favorites`、
        `GET|POST /api/v1/items/{id}/favorite`；详情页爱心按钮 + 「N 人收藏」；首页多一行「我的收藏」
      - **播放列表 / 合集**：`playlists(kind=playlist|collection)` + `playlist_items(sort_order)`；
        10 个接口（列表 CRUD、条目增删与重排、播放器要的 neighbors）；页面 `/lists` 与 `/list/:id`；
        详情页「加入列表」（可现场新建）；播放器带 `?list=` 时给「上一项 / 下一项」
      - 权限只在 store 里判一次（`store.Viewer`）：看不见 → 404、看得见不能改 → 403、
        用户输入错 → 400（`InvalidInputError`，不把用户错记成 5xx）
      - 验收：`verify-favorites.sh` 30/30、`verify-lists.sh` 49/49（都含**造临时账号**验多用户隔离）、
        `favorites-ui-test.mjs` 18/18、`lists-ui-test.mjs` 37/37；设计与 5 条坑见 `docs/notes/lists.md`
- [x] **导航重排 + 管理面收进设置**（2026-09-22 用户要求）：顶栏顺序固定为
      **首页 / 直播 / 搜索 / 海报墙 / 我的列表 / 个人中心 / 设置**；
      **库管理、人工匹配、会话监控收进设置的子页签**（`/settings/{libraries,match,sessions}`，
      旧地址 `/libraries` `/match` `/sessions` 保留重定向，老书签与测试都不碎）。
      做法：`Settings.tsx` 拆成「外壳（管理员门 + 页签栏 + `<Outlet/>`）」与
      「概览（TMDB / 服务状态 / 系统信息）」，路由从 `/settings` 起挂子路由。
      验收 `m2-ui-test.mjs` **40/40**（含新增的两条：页签出现、点页签进库管理）。
- [x] **媒体库「只读」开关 + overlay 写入**（网盘 / 只读挂载，2026-09-22 用户要求）：
      迁移 0014 给 `libraries` 加 `readonly`（并与 `library_paths.readonly` 事务同步）；
      `PATCH /libraries/{id}` 受理 `readonly`；打开后**一个字节也不写媒体目录**，
      刮削到的图片与元数据快照落进 `<数据目录>/overlay/<lib>/<item>/`
      （算数据、不参与图片缓存淘汰；本地图不进，只有回源的才补）。
      图片写在取图的唯一漏斗 `images.Open`（`PutImageIfMissing`），
      这样「打开开关之前就缓存过的图」也会补上；元数据快照写在 `scrape.applyMeta`，
      写快照失败只记警告（不把刮削判成失败）。
      **写回媒体目录不做**（用户 2026-09-22 明确要求）—— overlay 就是最终落点，
      换成设置页的叠加层看板（`GET /api/v1/overlay`）与「清空叠加层」按钮；
      `overlay.AssertWritable` 作为以后真要做写回时的唯一闸门留着（已被单测钉住）。
      验收：`verify-library-readonly.sh` **35/35** + `internal/overlay` 单测 9 个；
      设计与未做项见 `docs/notes/library-readonly.md`
- [x] **媒体库可编辑（类型 / 根路径）**：`PATCH /libraries/{id}` 现同时受理
      `name` / `kind` / `paths` / `readonly`；`paths` 是**整份替换**（事务里增删同步），
      已入库条目不受影响（界面写明了）。非法类型 / 相对路径 / 不存在的路径 / 空数组都是 400
- [x] **直播页改成「看电视台」的样子（M6 收尾，用户给了目标图）**：
      左边是**换台栏**（分组可收起/展开 + 计数 + 收藏星 + 失效红点），右边是**播放器**，
      下面一条：当前频道名 · 分组 · kind · 浏览器起播耗时 + **上一个 / 下一个 / 断开**。
      **前台只列「能用能看的」**：停用的、**探索过且不通（无效的）**、停用源带来的都不出现
      （`hide_failed=1`，后端与列表/统计共用判据；没探过的照旧显示 ——
      刚导入还没探测时前台不该是空白）。
      **「外部播放器（VLC / Kodi / 电视盒子）」整块拿掉**（用户要求）；
      导出 m3u 的能力本身保留，入口搬到设置 → 频道管理的工具栏上。
      频道的管理动作（改名 / 分组 / 排序 / logo、停用启用、外链、复制地址）
      全部搬进**设置 → 直播源 → 频道管理**（新组件 `components/ChannelManager.tsx`）；
      源管理与失效源探测同页。前台不再出现任何管理按钮与筛选工具栏。
      **停用一个直播源，它带来的频道在前台不再出现**（`tvVisibleSourceCond`；
      列表与头部统计（共 N 台 / 启用中 M）用同一套判据，频道自己的 `enabled` 不动）；
      删源仍然「不删频道」—— `source_id` 是 `on delete set null`，孤儿频道照常可见。
      验收：`verify-livetv-disabled-source.sh` **20/20**（含 `hide_failed` 的四条：
      失效频道一条不剩、条数正好少那么多、头部统计对得上、没探过的没被误删）、
      `livetv-ui-test.mjs` **50/50**
      （重写为验新布局：侧栏 / 折叠 / 换台 / 断线 / 星标 / 真起播 / 前台无管理按钮 / 无外部播放器）
- [x] **直播视频转码兜底（M6 收尾，用户要求）**：以前直播恒为**转封装**（`-c copy`），
      源里一旦是 H.265/HEVC、MPEG-2 这类浏览器解不开的编码，就是发一路客户端放不了的流。
      现在：探测时顺手记下源视频编码/高度（`tv_channels.video_codec/video_height`，
      迁移 0015）→ 起播带上前端报的「我能解哪些编码」（capabilities.detectProfile）→
      `liveVideoModeOf` 判转封装还是转码（纯函数，表测试 13 例）。
      转码参数全部交给 `internal/encoder`（与点播同一条路：硬件后端/码率模式/
      关键帧对齐 `KeyframeSeconds=1s`），目标是 H.264。
      **会话键带处理方式**（`live:ch7:copy` / `live:ch7:transcode`）：能解 HEVC 的 Safari
      与解不开的 Chrome 同时看一个台，各拿自己那一路。
      未知编码（没探测过）仍先转封装，前端带 `force=transcode` 重试一次兜底；
      界面在底部条标「转码中」。
      验收：`verify-livetv-transcode.sh` **12/12**（本机造 HEVC 源 → 探到 videoCodec=hevc →
      Chrome 口径起播 mode=transcode 且**产出分片 ffprobe 为 h264**；
      Safari 口径 mode=copy 且输出仍 hevc；force 覆盖有效）、
      `internal/api` 表测试 13 例 + 会话键测试
- [ ] 明暗主题切换：**首屏/主页右上角常驻入口**（不藏在设置里）+ localStorage 先落盘，登录后与服务端偏好双向同步；跟随系统(`prefers-color-scheme`)作为初始默认
- [x] **响应式（手机可用）+ i18n（zh-CN / en-US）** —— 两半都已完成：
      - **响应式**：三个断点（≤1024 / ≤768 / ≤480）；手机顶栏换两行、导航自己一行横滑；
        表格与搜索结果卡片不会再把页面顶宽。验收 `responsive-ui-test.mjs` **35/35**
        （8 个页面 × 3 个宽度都没横向滚动 + 手机上导航单行、海报墙 ≥2 列等）
      - **i18n**：**界面已全部翻完（覆盖率 0）** —— 顶栏、登录与初始化、首页、搜索与联想、
        海报墙、我的列表、详情页、播放器，**以及管理面**（库管理、条目编辑、人工匹配、
        直播电视与直播源/探测、设置、会话监控、个人中心），加上 `media.ts` / `people.ts` / `capabilities.ts`；
        语言探测 + localStorage 持久化 + 顶栏/登录页选择器；
        **后端保持语言中立**（行标题按 key 在客户端写，推荐依据用 API 的 taste/sourceWorks/seedTitle 拼）；
        验收 `i18n-ui-test.mjs` **30/30**；工具 `i18n-coverage.mjs`（未包 t 的文案 + 用了 t 却没目录），
        **已进 CI：`node scripts/dev/i18n-coverage.mjs --max 0`**（新增漏包文案会当场失败）；
        设计与坑见 `docs/notes/i18n.md`
      - 　— 仍未翻（**故意**）：开发者错误（改成英文，用户看不见）、服务端给的文字
        （报错文案与播放理由正文，配 message code 跟 M7 一起做）、直播源的 m3u 示例文本（已改中性）。
- [ ] 海报墙虚拟滚动（万级条目）+ 筛选排序 + 多选批量加入列表 → **已挪到二期**（见 M9）
- [ ] **DoD**：10 万条目海报墙滚动达到可接受帧率；手机上能完整完成"找片→播放→续播"

---

### M6 详情页验收记录（2026-09-22，真库 + 真浏览器）

**演职员：先把 M1 的遗留补上。** nfo 解析器从 M1 起就在读 `<actor>` / `<director>` / `<credits>`，
但解析结果一直**没落库**（被丢掉了）。这一轮加了迁移 `0010_people.sql`（people + item_people）
并在扫描器里接上 —— 于是**不用联网、不占 TMDB 配额**，光重读一遍 nfo 就有了演职员。

| 项 | 值 |
|---|---|
| 重扫真实库（`refreshMetadata=true`，只重读 nfo，不碰媒体文件） | **8 秒**（479 个 nfo） |
| 落库 | **2793 人 / 7096 条关系 / 388 个条目有演职员** |
| 角色分布 | Actor 5588 · GuestStar 1253 · Director 255 |
| 演职员最多的条目 | 蜘蛛侠：纵横宇宙 89 人 |

**后端验收** `scripts/dev/verify-detail.sh` **27/27**：演职员接口（形状、演员排在前面、
带 tmdb/imdb 外部 id、空值语义、401/404）、相关推荐（不含自己、同库、只推电影/剧集、
limit 生效、评分降序、401）、多版本（临时造第二个版本 → playlist 回 2 个 →
指定 fileId 放的就是那一个 → 不存在的 fileId 回 404 而不是静默换一版）。

**界面验收** `scripts/dev/detail-ui-test.mjs` **35/35**（CDP 驱动真 Chrome）：海报墙点卡片进详情、
头部海报+背景图、推荐卡片可点、**演职员条数与接口一致**、剧集季标签+集列表+点集进该集、
`/series/{id}` 重定向、编辑元数据 → 编辑页 → 返回详情、多版本选择器与 `?file=`、搜索入口也改指详情页、
无 JS 报错。回归 `m2-ui-test.mjs` **35/35**（剧集入口与卡片链接改了，断言跟着改成「问接口这条是什么类型」）。

截图：`docs/images/detail.png`、`docs/images/detail-cast.png`。

### M6 搜索（联想 + 分面）验收记录（2026-09-22，真库 + 真浏览器）

**后端** `scripts/dev/verify-search.sh` **69/69**（M2 的 26 项 + M6 新增 43 项）：
演职员三路（全名 / 人名二元组 / 错字）都能命中、`works` 把集数折进剧集、
**只有 `personId` 没有查询词**也能搜（抽查前 3 条真回查演职员表）、
联想与结果**同一排序**且字段轻量、分面四条不变量
（`sum(kind) == total`、`sum(library) == total`、`sum(genre) ≤ total`、
关掉某一维后 == 同一查询去掉该筛选的全集）与「**分面数字 == 按那个值真搜到的条数**」、
类型/库/流派三种筛选下分面与列表的 total 对齐、四个接口的参数校验与 401。

**界面** `scripts/dev/search-ui-test.mjs` **62/62**（CDP 驱动真 Chrome）：
敲一个字就有联想（带分组、与结果同一排序）、清空即收起、↑↓ 高亮（含回绕）与 Enter 进详情页、
从 URL 进来不自动弹下拉、分面胶囊的数字 == 结果总计、点类型/库胶囊 → URL 同步且列表随之变化、
× 取消筛选、人名联想（演职员分组 + 作品数）、点人名 → `personId` 筛选（且没有 `q`）、
按人筛选的结果确实都有这个人、「人」这一档的列表与计数、亮暗主题。
脚本还会把页面的 JS 错误一并打出来（本次只有 favicon 404、未登录时的 `/auth/me` 401、
以及某条目缺 backdrop 图 404 —— 均非缺陷）。

截图：`docs/images/search.png`（分面）、`docs/images/search-suggest.png`、
`docs/images/search-people.png`。

⚠️ 顺带修掉一个**早就烂掉的验收脚本**：`search-ui-test.mjs` 从 M2 之后再没跑过，
M6 把搜索卡片改指详情页、把库/类型下拉换成分面胶囊之后，它有 5 条断言永久失败
（「验收基线」表里没有它）。本轮把它修好并补上了联想/分面/人的断言。

⚠️ 演职员数据只对「上次重扫过 nfo」的库有：用户的「连载动画」库需要自己在
库管理里勾「重读 nfo」重扫一次。

### M6 首页（Netflix 风格）验收记录（2026-09-22，真库 + 真浏览器）

**接口**：`GET /api/v1/home` 一次给全 `hero`（最近更新/入库的 10 条）+ `continue` + `sections`
（`recommend` / `top` / `recent`，每行 ≤ 20）。

**后端** `scripts/dev/verify-home.sh` **26/26**：三段形状与上限、hero 都是顶层条目且按 `updated_at` 倒序、
recent 前 10 条与 hero 逐条一致；**自造一条观看记录**后验推荐 —— 刚标记为已看的那部**不再被推**、
推荐里每条都命中接口返回的**口味画像**、画像里确实多了它的流派、副标题说得清依据、
连续两次请求顺序完全一致；跑完还原；未登录 401。

**界面** `scripts/dev/home-ui-test.mjs` **34/34**：圆点数量 == 接口条数、点圆点/箭头切轮播、
**自动轮播（等 9 秒真验）**、宽幅背景图真的加载出来了（`naturalWidth > 0`）、
每行卡片数 == 接口条数、行右滚（`scrollLeft` 真的变了）、推荐依据（副标题 + 画像 chips）、
点卡片进详情页、首页不再放服务状态、亮暗主题。

回归：`m2-ui-test` **38/38**、`play-ui-test` **49 通过 0 失败**（其中一条就是首页的「继续观看」区块）、
`browser-test` **27/27**（全新安装路径，起一个空白实例真跑）。

截图：`docs/images/home.png`、`docs/images/home-light.png`。

> 服务状态面板原本贴在首页底部，M6 后期搬到了**设置页**（在「系统信息」上面）：
> 它是「出问题时才看」的信息，不该占首页位置。断言也跟着搬家：设置页在 `m2-ui-test`，
> 首页「不再有它」在 `home-ui-test`，全新安装路径在 `browser-test`。

⚠️ 首页的推荐**不联网、不依赖 TMDB**（流派画像全本地算），所以全本地 nfo 库也有推荐；
代价是它只知道「流派」，不知道「同一导演/同一系列」—— 等接了 provider 可以再加一路并合并，
接口形状不用改。

### M6 收藏 / 播放列表 / 合集验收记录（2026-09-22，真库 + 真浏览器）

**收藏**（迁移 0012）
- 后端 `verify-favorites.sh` **30/30**：幂等（重复收藏不会把收藏数算成 2）、列表倒序与类型过滤、
  翻页、首页那一行、鉴权；**造临时账号验多用户隔离**（他看不到我的、我也看不到他的，
  而全站收藏数确实变成 2），跑完 psql 删掉临时账号
- 界面 `favorites-ui-test.mjs` **18/18**：详情页点收藏 → 按钮/计数/接口都对 →
  首页出现「我的收藏」行且第一条是它 → 取消收藏后两处都还原

**播放列表 / 合集**（迁移 0013）
- 后端 `verify-lists.sh` **49/49**：空名/非法 kind 都回 400、重名 409；批量加入保序与幂等、
  移出、整串重排；PATCH 只改要改的字段（没给的不动）；邻居接口的 index/total/prevId/nextId
  逐个校对；**私人列表别人看不见（404 而不是 403）、合集所有人可见、
  看得见但不能改 → 403、非管理员建不了合集**、删除列表不动媒体文件
- 界面 `lists-ui-test.mjs` **37/37**：一整条用户路径 —— 建列表 → 在两个详情页「加入列表」→
  列表里看到两条 → ↑↓ 调序（与服务端顺序核对）→ 「播放全部」→ 播放器里队列徽标 `(1/2)`、
  点「下一项」真的换片且 URL 带着 `list` → `(2/2)` 时下一项变灰 → 回列表移出 → 删列表
  （分两次验：先「取消确认不删」、再「确认后真删」）
- 截图：`docs/images/list-detail.png`、`docs/images/player-queue.png`

⚠️ 已知的口径：合集**只有管理员能建/改**（M7 按库授权后再收紧）；
播放列表的条目详情页能加、列表页能删与调序，但**批量加入（从海报墙多选）还没做**——
那是「海报墙多选」那一条的一部分。

## M7 — 多用户、权限、分享（地基已完成，剩下的见下）

**参考**：`Jellyfin.Server.Implementations/Users/*`

> **地基（2026-09-22 完成）**：用户 CRUD（含「最后一个管理员」硬规则）、库级可见性白名单、
> `allow_transcode` / `allow_livetv`、并发流上限接线、改口令 / 禁用 / 收紧库范围就吊销会话、
> 设置 → 用户页签 + 界面验收。设计文档 `docs/notes/users-permissions.md`；
> `verify-users.sh` **52 项** + `users-ui-test.mjs` **27 项**全绿。
> ⚠️ 这一块上线时砸过一次首页（可见库参数把 SQL 占位符挤错位，42P18），
> 教训见第十一批第 52 条。

- [x] 用户 CRUD（管理员）、库级访问权限、并发流上限
- [ ] 家长分级（official rating）→ 二期（要人定「哪些分级算儿童」，见设计文档 §二）
- [ ] **个人中心**：修改密码（**需验证旧密码**）、修改显示名、主题/语言偏好、字幕与音轨偏好
      （显示名 / 口令 / 外观 / 设备会话已可用，字幕与音轨偏好未做）
- [x] 管理员视角：用户列表、重置密码、禁用账号（强制下线已随吊销一起做）
- [ ] ~~外链分享（受限 token：有效期/次数/是否允许下载）~~ —— **不做**（2026-09-23 用户拍板）：
      媒体库是给自己人看的，多一条「公开访问路径」只会多一块攻击面；
      真要分享就用现成的账号体系（建个受限账号）或自己去外面加反代。
- [x] 审计日志（登录、改密、管理操作）—— 2026-09-23：`audit_logs` + 设置 → 审计日志页签；
      `verify-audit.sh` 18/0（含「口令不落审计」与「普通用户 403」）
- [ ] **DoD**：两用户进度/收藏互相隔离；无权限库在 API 与 GUI 双层拦截；普通用户无法修改他人密码或越权访问；改密后除当前会话外其余按策略失效；个人中心在手机上可用

---

## M8 — 运维、发布（1.0）

- [x] 【发布流程：release workflow 交叉编译 4 平台（linux amd64/arm64/armv7 + windows amd64）
      + `SHA256SUMS.txt` + GitHub Release（说明用 `docs/releases/<tag>.md`）—— 每个 tag 自动出，
      已经跑过 v0.2.0 / v0.3.x / v0.9.0】
- [ ] 缓存与清理策略：转码分片（孤立会话残留）、图片缓存、孤儿文件清理、日志
- [ ] `lmby backup` / `lmby restore`（PG dump + 配置 + 数据目录清单）
- [ ] `/metrics`（Prometheus，可选）
- [ ] 文档：`docs/CONFIG.md`、`docs/ARCHITECTURE.md`（README / TRANSCODING / ADR 已有）
- [ ] **DoD**：全新用户照 README 30 分钟内从零跑起来并播出第一个视频

> **Emby/Jellyfin 导入器已经挪到 M9（可选）** —— 它是「迁移工具」而不是服务器能力，
> 1.0 的判定标准是「从零跑起来能播」而不是「能把旧数据搬过来」；而且外部格式会变
> （Emby / Jellyfin 各自的 nfo 与 library.db 结构都改过几轮），跟着它们追的成本不该
> 压在 1.0 上。真要迁移的人，按 nfo 约定把目录摆好再扫一遍就能得到大部分结果。

---

## M9 — 二期候选（先不做，留扩展点）
> 音乐库与 DLNA/UPnP **已砍**，不在候选列表内（本项目就是纯 Web 播放站点）。
- [ ] **Emby/Jellyfin 导入器**（从 M8 挪过来）：读 `/config/metadata/library/**/*.nfo` +
      媒体同目录 nfo → 匹配灌入 PG；可选读 `library.db` 导入播放进度；图片路径映射。
      等真有迁移需求时再做（外部格式会变，跟着追的成本不低）
- [ ] DVR / 录制 / EPG（XMLTV）
- [ ] **海报墙虚拟滚动（万级条目）+ 筛选排序 + 多选批量加入列表**（从 M6 挪过来：
      10 万条目下的帧率是性能债，但它不挡住「手机能用」与「能发版」；
      多选批量加入列表属于同一个交互（海报墙多选））
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

## 2026-09-22 的四条教训
1. **占位页陷阱（真踩了）**：仓库里的 `web/dist/index.html` 是占位页，`go build` 会把它嵌进二进制。
   提交前要还原它（`git checkout HEAD -- web/dist/index.html`）—— 但**还原之后如果还要交叉编译，
   必须先重跑 `npm run build`**。本次连续几次部署把占位页打进二进制，服务端在发「前端未构建」，
   用户看到的只是浏览器缓存里的旧前端，误导了一阵排查。根治：走 `task build`（依赖链自带 `web:build`）。
   现在的做法：交叉编译前先卡一道断言 —— `bash scripts/dev/check-web-built.sh`（或 `task build`）。
   注意判据别写成「index.html 里含 `/assets/`」：**占位页的注释里就有这个字符串**，
   会把占位页当成构建产物（2026-09-22 发现，改成看标题与真正的 `<script src="/assets/…">`）。
2. **`gofmt -w <目录>` 会改到你没碰过的文件**：它顺手重排了 `playback.go` 与 `throttle_test.go`
   （这两个文件本来就不是 gofmt 干净状态）。只对本次改动的文件跑 `gofmt -w <文件>`。
3. **推送前先跑容器里的同版本工具**：lint 又连红两次（unparam、errorlint），
   第二次改成先 `bash /root/srccheck.sh`（gofmt + build + vet + test + golangci-lint）再推，一次过。
   和 09-20 那条是同一个道理。
4. **布局怪问题不要用肉眼看像素**：用无头 Chrome 走 CDP 把 `getBoundingClientRect` 与计算样式打出来。
   本次靠它定位到「继续观看」卡片高矮不一的真因 —— flex 子项默认 `min-width: auto`，
   长标题（`nowrap`）把卡片顶得比 `flex-basis` 宽，实测同一行出现 358 / 168 / 270 三种宽度；
   加一行 `min-width: 0` 后全部回到 168。

## 2026-09-22 的第二批教训（S4 验收时踩到）

5. **验收脚本里的一句 SQL 与 `scanTVChannel` 的列数没对齐**：`ListTVChannelsForProbe` 少了
   一列 `favorite`，探测一条频道都选不出来。这类 bug 单测碰不到（不连库），
   是**新验收脚本第一次真跑就抓到**的 —— 所以每次加接口都要配一段真跑。
6. **脚本的语法错误只能用工具找**：嵌套 `$( ) + [[ ]] + jq 单引号` 里多写一个引号，
   `bash -n` 只会说「最后一行 EOF 少一个 `)`」。本次临时写了个「括号/引号配平扫描器」
   （Python 小脚本）一次定位到行号；仓库里留下了 `scripts/dev/check-scripts.sh`
   （对所有 `*.sh` 跑 `bash -n`，提交前跑一秒）。
7. **验收脚本跑着的时候，别手动去动同一个服务**：本次为了查「起播为什么失败」
   手动起了播放并 `rm -rf /var/lib/lmby/streams/*`，正好砸中同时在跑的
   `verify-livetv-play.sh` → 21 通过 / 9 失败（全是假失败）；清场后单独重跑就是 30/30。
   要查问题就先停掉脚本，或者换一个实例。
8. **ssh 命令行里不要塞引号 / `&&` / 重定向**（已经第三次了）：
   `ssh host 'nohup bash -c "env X=1 bash y.sh" &'` 里的内层引号会被 PowerShell 吃掉，
   最后执行的是裸 `env`（把环境变量全打印出来当结果）。一律「本地写 .sh → scp → bash 跑」。
9. **不要轻信脚本报出来的「死源数」**：`verify-livetv-play.sh` 是「试到第一个能起播的就停」，
   所以它的「跳过死源 26 个」只是「它试过的前 26 台」；全量探测（每一台都真连）在同一时刻是
   150 台里 67 通 / 83 不通（而且单并发与 4 并发的判定集合完全一致）。
   推广一句：**任何「碰到第一个成功就停」的检查，它报的失败数都是下限**。
   另外「探测不通」只是**上一次真连的结果**，会随源站状态变 —— 配置与界面都要按这个语义写。
10. **复制一段现成的 DOM 时要连包装一起复制**：详情页的「相关推荐」照抄了卡片结构却漏了
   `.poster-frame`。而 `.poster-img` 是 `position: absolute; inset: 0`（靠外层撑比例），
   少了那层它就贴到最近的定位祖先上，**一张图把整页盖住**，按钮点不动、截图全是那张图。
   教训：布局怪问题一律用 CDP 读 `getBoundingClientRect` + 计算样式（本次一读就看到
   `position: absolute` 与 1265x900 的尺寸，和自己写 CSS 时的假设完全不同）。
11. **验收脚本里的正则要当心两层转义**：`.mjs` 里手写的 `/\\?file=\\d+/` 在文件里就成了
   `\\?file=\\d+`（多一层反斜杠），断言会永远为假而看起来像“页面 bug”。
   能不用正则就不用（`String(href).includes('?file=')`），必须用就先打印一次匹配结果。
12. **打包必须在仓库根目录**：本次在 `web/` 下 `tar czf … -C . .`，包里全是前端目录。
   幸好 `cv.sh` 开头校「解出来的是不是预期代码」当场停下（代价是把 `/opt/lmby` 清空了，
   因为它是先 `rm -rf` 再解包）—— 服务没受影响（旧二进制还在跑）。
   安全网值得留：**破坏性步骤前面先做一次“这批东西对不对”的断言**。

## 2026-09-22 的第三批教训（M6 搜索的联想与分面）

13. **分面断言先想清楚「这个数是在哪个集合上数的」**：第一版验收脚本写的是
    「`sum(kind) == total`」，并且在「类型=电影」生效时也断言相等 —— 必然失败，
    因为分面**故意**统计的是「关掉自己那一维」的超集。
    错的断言比没有断言更浪费：它把正确实现判成 bug，让人去改对的东西。
    正确的不变量是「关掉某一维后 == 同一查询去掉该筛选的全集」与
    「分面数字 == 按那个值真搜到的条数」；同理 `sum(genre) >= total` 也是错的
    （没有流派的作品不贡献格子）。详见 `docs/notes/search.md` 第三节。
14. **界面验收脚本会悄悄烂掉**：`search-ui-test.mjs` 从 M2 之后再没跑过，
    M6 把搜索卡片改指详情页（`/items/{id}` → `/item/{id}`）、把库/类型下拉换成
    分面胶囊之后，它有 5 条断言**永久失败** —— 而「验收基线」表里根本没有它，就没人发现。
    教训：改入口时顺手跑一遍相关界面脚本，基线表里要留着它们。
15. **测试里「把同一个值写回输入框」不会触发联想**：React 状态没变化 → `useEffect` 不重跑，
    请求根本没发出去，测试把好功能判成坏功能。要先清空再敲。
16. **JS 的 `.click()` 不触发 `mousedown`**：下拉是靠 mousedown（点别处）收起的，
    于是界面脚本的截图里下拉把半个页面盖住了（真鼠标不会碰到这个差别）。
    截图前按 Esc（或补一次真实的 mousedown）。
17. **路由切换后 DOM 不是同一拍就绪的**：点导航进搜索页后立刻查 `.search-input` 会拿到
    `false`（URL 早就变了、DOM 还没渲染完），后面一连串断言跟着假失败。
    凡「切页之后马上断言」的地方，先 `waitFor` 目标元素出现。
18. **自检运行时的状态别用事件维护，直接用 DOM 事实**：输入框「有没有焦点」如果靠
    focus/blur 事件记，会出现「元素本来就拿着焦点 → 再调 focus() 不触发事件 → 记的状态是错的」。
    改成当场读 `document.activeElement` 就与事实一致（无头浏览器里真踩到）。

## 2026-09-22 的第四批教训（M6 首页）

19. **`select <一长串列> ... join <带 id 的 CTE>` → `column reference "id" is ambiguous`**（直接 500）。
    把 CTE 里的列改名成 `item_id` 就好了。拼 SQL 时这种「列名撞车」很容易发生，
    报错还算好的（更坏的是不报错但算错）。
20. **Go 的原始字符串（反引号）里不能出现反引号**：SQL 注释里写了 `` `select id, …` ``，
    字符串提前结束，`go build` 报 `unexpected keyword select`。SQL 注释里别用反引号（用「」）。
21. **截图要等图片真的加载完**：第一次首页截图的横幅是纯黑 —— 看着像功能坏了，
    其实是 `w=1600` 的宽幅背景还在路上。验收脚本里先等 `img.complete && naturalWidth > 0`
    再截图，顺带把「图挂了」变成一条真断言（本来只是「看着不对」）。
22. **界面验收里「点得太早」会假通过**：刚 `Page.navigate` 过去时 DOM 还是空的，
    点击找不到元素（静默返回 false），而紧接着的「卡片消失了吗」一查也是空 DOM → **真空通过**。
    凡「点了会消失 / 会删除」的断言，必须先等目标元素真的渲染出来再点。
23. **删除类操作的确认框在无头浏览器里不能用真点击**：`window.confirm` 拿不到用户输入，
    而不替换它就只能验到「什么都没发生」。验的是「确认之后到底删不删」，
    所以直接替掉 `confirm`，并分两次测：先 `false` 验「取消不删」、再 `true` 验真删。
24. **轮询后端状态要加缓存扰动**：没有缓存头的 JSON 响应，浏览器会做启发式缓存 ——
    同一 URL 连着翻几个轮子可能一直拿到同一份旧响应，断言会莫名其妙地失败。
    验收里的 `GET` 都带一个 `_=时间戳`。
25. **只差一个字段的 PATCH 得用指针**：`name`/`overview` 如果都用空串表示「不改」，
    界面只改名字就会把说明抹掉。nil = 不改、空串 = 清空，两者才区分得开。

## 2026-09-22 的第六批教训（M6 响应式）

26. **「没有横向滚动」是最低标准，不是全部**：第一版导航在手机上被压成了**每字一行的竖排**
    （中文标签被 flex 压缩），而「横向溢出 0px」的检查**完全抓不到** —— 页面很规整，就是难看。
    补的断言是自标定的：所有导航项高度应当彼此接近（`max <= min * 1.5`），
    某一项被压成竖排时会明显高出其它项。**别用「我看了截图觉得还行」当验收**。
27. **flex/grid 子项的 `min-width: auto` 会咬人**：中文长标题的 min-content 很宽，
    搜索结果卡片因此把整行顶出去 7px。修法是给卡片与内容块 `min-width: 0`
    + 标题 `overflow-wrap: anywhere` —— 这是「手机上没有横向滚动」的关键修复，不是可选项。
28. **给导航加 `overflow-x: auto` 只是一半**：还要给子项 `flex: 0 0 auto; white-space: nowrap`，
    否则 flex 会先把它们压扁（然后才轮到滚动条）。这两条要成对出现。

## 2026-09-22 的第七批教训（M6 i18n）

29. **测试会依赖「环境默认语言」**：i18n 一上线（按 `navigator.language` 探测），
    headless Chrome（默认 `en-US`）里所有断言中文的界面测试**一起假失败**。
    修法是测试里显式钉死：`localStorage.setItem('lmby.lang','zh-CN')` + reload。
    ⚠️ `Emulation.setLocaleOverride` 在这里**没用**：它在部分 Chrome 上只影响 `Intl`，
    不改 `navigator.language`（实测白折腾了一轮）。
30. **漏翻要能被工具量出来**：`t('选择媒体库')` 包了、但英文目录里没写这条，
    `t()` 会**静默退化成中文** —— 页面上看着像漏包，实际是漏写目录。
    所以覆盖率工具做了两项检查：「没包 t 的文案」与「用了 t 却没目录的键」（后者真抓到 11 条）。
    “测不到的东西就会悄悄坏”。
31. **别拿 aria-label / 可见文本当测试选择器**：把轮播箭头的 `aria-label` 从「下一部」
    改成「轮播下一部」（顺手更准确），当场打断一条断言。稳定钩子用 `data-*`。
32. **裸中文当键能编译，却是陷阱**：CJK 在 JS 里是合法标识符字符，`搜索: 'Search'` 能过；
    但 `正在加载…: 'Loading…'` 带 `…` 直接语法错误 —— 而 `…` 是中文文案里最常见的字符之一。
    统一加引号（工具两种都认）。

### 管理面 i18n 收尾（2026-09-22，第八批）

33. **`t()` 的参数换行写，覆盖率工具会误报**：工具的判据是「字符串前面 3 个字符是不是 `t(`」。
    写成 `t(\n  '长文案',\n)` 时前面是换行缩进，就会被判成「没包 t()」。
    **把字符串放在 `t(` 同一行**（超长也照放），否则计数永远清不到 0。
34. **模块级标签常量表会把语言钉死**（`import` 时取值）。管理面四份重复的
    `kindLabels` / `stateLabels` 全删掉，改成小函数（`stateLabel(state)`）与共享的 `media.ts#kindLabel`；
    `switch` 里**内联** `t('电影')` 比「常量表 + `t(key)`」两头都好：键静态可见（工具不漏报）、语言自动跟随。
35. **`.map((t) => ...)` 会把 i18n 的 `t` 遮蔽掉**（会话页真踩到：`t('终止')` 编译不过）；
    回调参数改名（`st`），组件里 `const t = window.setTimeout(...)` 同理。
36. **浏览器 `confirm` / `prompt` 不渲染 Markdown**：两处确认文案原来写成 `**未锁定**`，
    用户在弹窗里看到的是字面星号；顺手去掉并用 `t()` 包起来（弹窗也是界面文案）。
37. **开发者错误不要翻译**：`useAuth must be used inside an AuthProvider` 这类是内部不变式被破坏，
    用户永远看不到 —— 这一轮从中文改成英文，免得翻译者与覆盖率工具把它当漏翻。

## 2026-09-22 的第九批教训（导航收窄 + 只读库）

38. **`/tmp` 写满会把界面验收卡成「假失败」**：Chrome 的 profile 落在 `/tmp/lmby-*`，
    每个几十 ~ 几百 MB，而本机 `/tmp` 是 **2G tmpfs** —— 攒几个就只剩几十 MB，
    之后 CDP 命令（`Emulation.setEmulatedMedia` / `Log.enable`）**全部 60 秒超时**，
    报告里只留一行「运行失败: CDP 超时」，看上去像代码坏了。
    以后跑界面测试前先 `rm -rf /tmp/lmby-*`（`bash /tmp/ui-run.sh` 里已加），并先看 `df -h /tmp`。
39. **拿「整页文本」断言界面文案是假失败制造机**：i18n 测试里
    `/因为|根据|……/.test(homeText)` 把卡片里的**中文数据**（条目简介、标题、流派）
    也算进去了 —— 某天轮播换了一部片、简介里带「根据」，断言当场红。
    正确做法是把断言限定在**界面元素**上（`.row-block .row-head` 的标题/副标题）。
40. **导航收窄时老地址要留重定向**：把 `/libraries` 收进 `/settings/libraries` 之后，
    老书签、浏览器历史、别人分享过的链接都还会指着它 —— 三个旧地址都加了 `Navigate replace`。
    连带把界面测试里「点导航项」改成「点页签」，**并且先等页签元素真的渲染出来再点**
    （导航后立即点会静默落空，是第二批第 3 条那个坑的第二次现场）。
41. **只读库的产物写在哪，得先想清楚「算缓存还是算数据」**：
    图片缓存按上限淘汰、`remote/` 算数据；overlay 里是「刮削成果」，
    清掉就得重新联网刮一次 —— 所以它另开一棵树，**不放进 `images/` 下面**，
    免得以后有人顺手把缓存清理的目录范围调大就把它删了。

## 2026-09-22 的第十批教训（直播页重做）

42. **把「布局」当需求时，先要一张目标图**：这一页第一版是「卡片列表 + 一堆按钮」，
    功能都在但根本不是用户想要的样子；拿到目标图（左换台栏 + 播放器 + 底部控制条）
    之后一次就到位。教训：**布局本身也是需求**，不要拿「功能齐了」交差。
43. **重写界面测试时，别把「导航」也删了**：旧断言里第一行是「点导航进直播页」，
    我从「== 2.」开始整段替换，把那一行顺手删了 —— 结果后面所有断言都在登录页上跑，
    一片红。现在第一行显式写「点导航 → 等 pathname」。
44. **点完按钮立刻读 DOM 会假失败**（第三批第 3 条的又一次复现）：
    「理由面板的字段标签是英文」这条在重跑中随机红/绿 —— 面板要等一次渲染。
    改成 `waitFor(<dt> 出现)` 之后再断言，稳定绿。
45. **测试要用真实的失败容忍度**：直播这一页「单个频道起不起得来」受源站影响，
    所以「真起播」改成**试前 6 个探测正常的频道，只要有一个出画面就通过** ——
    拿一个频道赌运气，会变成随机红的测试（随机红 = 没人看的红）。
47. **「全量探测」不能拿来当脚本的前置步骤**：验收脚本里想「先探一下这条新频道」，
    结果 `POST /livetv/channels/probe` 不带参数＝**全量探测 150+ 台**，
    跑好几分钟也轮不到新频道，脚本 60 秒轮询超时 → 误判成「探测没记编码」，
    接着错误地得出「自动决策没生效」。改成 `{channelIds:[新频道]}` 后一切正常。
    教训：脚本里的每个前置动作都要问一句「它扫的是整个系统还是我刚造的那条？」
49. **切台报 `levelLoadError`：换台时没把上一条 hls 拆干净**（2026-09-22 用户报）：
    「播放中切台报错、刷新就好了」—— 症状很典型：新的 `Hls` 实例 `attachMedia()` 挂在
    一个**还被旧 MediaSource 占着**的 `<video>` 上（我重写 attach 时把
    「先 destroy 旧实例 + `removeAttribute('src')` + `load()`」那三行丢了），
    分片加载直接失败。刷新页面 = 全新元素，所以「看着像好了」。
    教训：**「切换输入源」的代码必须显式把上一个源拆掉**，而且界面测试不能只断言
    「底部频道名变了」—— 得加一条「切台后视频还在往前走、没有报错浮层」，
    否则这种 bug 会一路绿着发布出去（这次就是这么漏的）。
50. **「怎么判断前端构建过」这件事本身错过一次**：部署闸门我写成
    `grep -q 'assets/' index.html` —— 而**占位页的注释里本来就写着 `dist/assets/*`**，
    于是闸门形同虚设（真出事那次它也拦不住）。仓库里那份
    `scripts/dev/check-web-built.sh` 才是对的（判据是占位页的标题「LMBY · 前端未构建」），
    现在容器闸门直接调它。教训：**判据别手写第二份**，尤其当它已经有一份、而且更懂细节时。
51. **「还原占位页」还原错过两回**：`git checkout HEAD -- web/dist/index.html` 还原的是
    **已提交的那一版** —— 如果某次不小心把真构建产物提交进去了，之后所有「还原」都只是
    把真产物再抄一遍（fa947d4 就是这样白提交一次）。占位页得从**更早、确实没有构建产物**的
    提交里取（`200da30`），并且改完必看 `check-web-built.sh` 的退出码。

48. **临时 http 服务要用随机端口**：验收脚本自己起 `python3 -m http.server` 供 LMBY 拉流，
    固定端口 + 上一次跑残留的进程占着 → 新脚本连到旧进程、拿到 404（而旧进程的目录已删），
    日志里看起来像「新造的源不存在」。每次用 `18000 + RANDOM % 2000` 并与实际 listen 挂钩。

## 2026-09-22 的第十一批教训（首页 500 + 用户管理入口）

52. **动态拼 SQL 时，「参数表」和「占位符」对不上只会在真跑时炸**（首页整页「读取首页失败」）：
    M7 把可见库条件接进列表类 SQL 之后，两处错位 ——
    ① `ListFavorites` 的**计数**查询复用了带 `$5`（可见库）的条件串，又把 `limit/offset`
    一起传进去 → `$3/$4` 出现在参数表里但 SQL 里没有引用，PG 直接 42P18
    「could not determine data type of parameter $3」；
    ② `RecommendForUser` 的三段查询写着 `$4`，却只传了 2~3 个参数（越界 + 中间悬空）。
    编译、`go vet`、单测全绿 —— 因为单测不连库；**管理员账号也照样 500**，
    因为「不限制」的可见库是空数组、仍然要过那几条 SQL（别再用「普通用户才炸」来解释）。
    规矩：拼出来的每条语句，占位符必须**从 `$1` 连续排到最后一个引用**；
    参数个数与编号对不上时，宁可单独特化一份条件串（这次计数查询就是这么改的）。
53. **验收要按「读路径」扫一遍，而且要先把数据造出来**：上一条能上线，是因为
    `verify-users.sh` 只验了权限的边（403/404/401），没验「正常读一页」。
    现在新增的 1.5 节把十条读接口（首页/收藏/搜索/分面/联想/库列表/库浏览/库内条目/
    条目详情/相关/演职员）× **两种视角**（管理员、受限用户）全打一遍，并且先用 psql
    给验收用户**造一条带流派的观看记录** —— 不造这一条，首页的「为你推荐」
    （画像/种子/候选池三段）永远不会执行，bug 就继续藏在那里。
    道理：**没有数据走不到的代码路径 = 没验过**。
54. **一个 502 先问「这条路本身通不通」，再怀疑自己的代码**（「直播转码 502」结案）：
    上一轮把它记成了「liveVideoEncode 拼的转码参数对这类源不成立」，查下去才发现：
    那台频道在库里**本来就是失效源**（`probe_ok=false`、probe 写着「超时（8s 内没有应答）」），
    前台 `hide_failed` 根本不显示它 —— 是验收脚本拿**管理口径全量列表的第一条**挑中的；
    ffmpeg 直测显示源站 302 重定向到另一台连不上的节点（`Connection timed out`）；
    换成探测能通的频道，干净状态下转码 **200 / 750 ms**，分片 ffprobe = h264 1920×1080 + aac。
    两个具体做法：① 挑样本要看**口径**（管理口径的第一条 ≠ 前台能看到的第一条，
    用的是 `?probe=ok`）；② 断言里加**基线**（先用管理员打一次，源站挂了就降级为
    提示，而不是「今天源站不好、验收红了」的假失败 —— 源站是外部世界，会变）。
55. **路由和页面都做好了，不等于用户找得到入口**（用户管理页）：`/settings/users` 路由、
    `Users.tsx` 页面、52 条接口验收在 M7 全都绿着，但**设置页签栏里没有任何链接指向它** ——
    管理员在界面上根本进不去（只有手敲地址）。教训：做完一个页面，
    验收的第一条应该是「**从用户点得到的地方点进去**」；HTTP 层全绿 ≠ 界面上存在。
    顺带三个界面验收的坑（每个都能让脚本假红）：
    ① 按 `.user-name` 找用户行**永远找不到新建的人** —— 那里显示的是**显示名**，
    用户名只在行头副文本里（要匹配整个行头）；
    ② 已登录时访问 `/login` 会被重定向回首页、拿不到登录表单 —— 切账号前先 `logout`；
    ③ `confirm` 弹窗不接会让页面一直挂着、后面断言连锁超时 —— CDP 里接
    `Page.javascriptDialogOpening` 并自动 accept。
56. **「验收挑不到场景」要当 bug 查，不要记成环境限制**：上一轮有两处验收被记为
    「这台机器上凑不出场景」，查到底两处**都是脚本自己的问题**：
    ① 「不允许转码」的 VOD 路径 —— 脚本拿**受限用户**去扫候选条目，而真需要转码的
    条目对受限用户只会返回 403（权限判定在决策之前），响应里根本没有 `mode`，
    于是永远「挑不到」；改成「SQL 挑候选 → 管理员确认 `mode=transcode` → 停掉会话
    → 受限用户打同一条期望 403」就通了。
    ② 直播那条 —— 挑了**管理口径全量列表的第一条**（正好是失效源），而前台口径
    根本不显示它；那是「探测快照」与「挑样本口径」两件事混在一起（见 54 条）。
    教训：脚本里「找不到样本」优先怀疑**挑选方式**（用的是谁的口径？权限判定在不在
    前面？），而不是先怀疑数据/环境。同类小坑：`media_files.probe_state` 的值是
    `ok`/`pending`/`failed`，不是 `probed`；写错时报告只显示一行「挑出 0 条」，
    看上去很像环境问题。
57. **单个 ui-test 全绿 ≠ 功能可用：发版前要逐页巡检**（搜索 0 条就是这么抓到的）：
    M7 把可见库接进搜索时，`Viewer.LibraryIDs()` 被直接当成了 SQL 参数 —— nil 切片
    被 pgx 绑成 NULL，而 `cardinality(NULL) = 0` 也是 NULL → where 恒不成立 →
    **管理员搜什么都 0 条**。接口照样回 200 + 空结果，所以「只看状态码」的那几条断言
    完全看不见它；首页 / 收藏 / 库列表 / 续看那几条路径都过了 `libsArg`，**只有搜索漏了**
    —— 又一次证明「同一个语义必须在每个调用点都成立」。两件事值得记住：
    ① 「不过滤」与「什么都看不到」不能用同一种形状表达：SQL 里空数组 = 不过滤，
    所以「白名单开着但一个库都没勾」得用哨兵（不存在的库 id `-1`）表达，
    并且要在验收里单列一节钉住它（否则就是一个静默的权限漏洞）；
    ② **发版前把每一页真打开看一遍**：`scripts/dev/pages-audit.mjs`（逐页截图 +
    页面异常 + 顶栏 / 设置页签 / 首页各行的入口清单 + 普通用户视角对照）。
    各页 ui-test 都绿、而这个巡检一遍就把它翻出来了。
58. **改「列清单常量」时必须搜所有 Scan 点**（扫描计划这轮踩到两次）：`libraryColumns`
    加一列（`scan_interval_minutes`）之后，`ListLibraries` 里一处**手写 Scan** 没跟上 →
    8 列 vs 7 个目标 → **库列表整个 500**（`number of field descriptions must equal
    number of destinations, got 8 and 7`）。同一次里 `CreateLibrary` 也是这个毛病（早一轮刚修过）。
    根治只能是**统一入口**：库走 `scanLibrary`、用户走 `scanUser`、条目走 `scanItems`；
    改完常量列清单后，`grep` 一遍还有没有手写的 `rows.Scan(&x.` 在同一批查询上，
    然后跑一遍覆盖读路径的验收（`verify-users.sh` 的 1.5 节正好干这个 —— 它会把
    库列表/条目/搜索/首页都打一遍，这次如果先跑它就不会把 500 带上去）。

- [ ] 设置页：扫描计划、转码/硬件、日志查看
      （库管理与直播源已随前几轮进设置；用户与权限见下一条）
- [ ] **用户与权限（M7 地基）**：设计方案已写进 `docs/notes/users-permissions.md`
      （权限 4 维度：管理员 / 库可见性 / 并发流上限 / 允许转码·允许直播；
      **判定只在 store.Viewer 一个入口**，可见性以 `library_id = any($libs)` 落在 SQL 层，
      影响面已核对：库列表 / 条目 / 浏览 / 搜索（含分面）/ 首页与推荐 / 收藏与列表 /
      详情与相关 / 播放 / 直播）；迁移 0016 草稿、6 个接口、设置页「用户」页签草图、
      验收脚本计划都在那份文档里。**等用户拍板三条默认值后开工**。
