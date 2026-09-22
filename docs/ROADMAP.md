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
| `scripts/dev/verify-search.sh` | **26 项断言全绿**（真库）：整标题首条命中、《钢之炼金术师》命中 5 条、「科学」中段命中、错字「某科土的超电磁炮」仍命中（相似度 0.417）、单字「科」命中（ILIKE）、库/类型过滤、分页、参数校验与鉴权、**改完标题立刻能搜到新标题、旧片段搜不到** |
| `scripts/dev/search-ui-test.mjs` | **22 项断言全绿**（无头 Chrome，连跑两次稳定）：导航进搜索页→中文查询出卡片→错字查询仍命中那一条→点卡片进条目页→URL 参数直接可开→库/类型筛选→亮暗主题 |
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
- [ ] 首页「继续观看」+ 最近添加（继续观看已有；「最近添加」还没做）
- [ ] 搜索：即时联想 + 结果分面（电影/剧集/人）
- [ ] 收藏、播放列表、合集（Collection）
- [ ] 设置页：库管理、扫描计划、转码/硬件、provider 与 TMDB Key、用户与权限、日志查看
      （「直播源」已随 M5 的直播页完成，**故意不放设置页**：它与频道列表是一件事，
      拆到两个页面反而要来回跳）
- [ ] 明暗主题切换：**首屏/主页右上角常驻入口**（不藏在设置里）+ localStorage 先落盘，登录后与服务端偏好双向同步；跟随系统(`prefers-color-scheme`)作为初始默认
- [ ] 响应式（手机可用）+ i18n（zh-CN / en-US）
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
