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

## M2 — 元数据与刮削（2 周）

**参考**：`MediaBrowser.Providers/{Movies,Manager}/*`、`MediaBrowser.LocalMetadata/Parsers/*`、`MediaBrowser.XbmcMetadata/*`（导入参考）

- [ ] `Provider` 接口 + 注册表（配置驱动启用与优先级）
- [ ] **TMDB provider**：search / movie / tv / season / episode / credits / images / external_ids / alternative_titles，语言参数化
- [ ] 限流器（令牌桶 + 可配并发）+ 429/5xx 指数退避 + `provider_cache` jsonb 缓存
- [ ] 匹配打分器：标题相似度（中文 bigram / 英文归一）、年份、类型、集数-时长吻合、别名命中 → 阈值以上自动 / 以下进人工队列
- [ ] PG 任务队列：批量入队、优先级、暂停/取消/重试、失败原因可视化、进程重启续跑
- [ ] 图片管线：按需下载到缓存目录（hash 去重）+ 尺寸/格式元数据 + `GET /items/{id}/images/{kind}?w=&h=&format=webp` 缩放输出 + 本地图片优先覆盖顺序
- [ ] 元数据写入语义：**只写 PG**（不生成 XML、无导出）+ 字段锁定（手改字段重扫不覆盖）
- [ ] GUI：详情页（海报墙 + 剧集视图）、条目编辑（标题/简介/年份/流派/海报选择）、**人工匹配**（搜索候选→指定→应用）、批量匹配
- [ ] 搜索：`pg_trgm` GIN + `tsvector`（中文 bigram，不引 zhparser）
- [ ] **DoD**：无 nfo 的库能一键刮削完成；自测样本自动匹配准确率 ≥ 90%；人工匹配可修正；重复刮削零 API 调用（缓存命中）；手改字段不被覆盖

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
