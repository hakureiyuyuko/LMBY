# LMBY —— 开发者入口

LMBY 是**纯 Web 在线播放**的轻量自托管媒体服务器：浏览器是唯一客户端、HTTP 是唯一出口，
Go 单二进制（前端 `go:embed` 进去）+ PostgreSQL，**运行期零 Node**，AGPL-3.0。

这个 README 面向**改这个仓库的人**。想装来用的看 [发布页](https://github.com/hakureiyuyuko/LMBY/releases)；
面向使用者的那份（安装、配置、功能清单）是交付 README，单一来源在
[`docs/README.release.md`](docs/README.release.md) —— 发布时 `publish-main.sh` 会把它装成 `main` 上的 `README.md`。

## 本机跑起来

```bash
export PATH="$HOME/.local/go/bin:$HOME/.local/bin:$PATH"   # 本机工具链装在 ~/.local
task              # = task --list，看有哪些活
task web:deps     # 前端依赖（只有构建前端时才需要 Node）
task run          # 构建前端 + 编译 + 起服务（默认 :8099）
```

数据库是 PostgreSQL（**必须 UTF8 + C.UTF-8**）：库若是 `SQL_ASCII` 或 `lc_ctype=C`，
中文检索会**静默**失效 —— `store.Open` 会拒绝启动；一键重建见
`scripts/dev/fix-db-encoding.sh`。

```bash
task migrate
export PGPASSWORD=$(cat /etc/lmby/pg-password)   # 本机实例的口令位置，按需替换
psql -h 127.0.0.1 -U lmby -d lmby -c 'select count(*) from media_items'
```

## 仓库习惯（比代码风格重要）

1. **不提交没真跑验证过的改动。** 修 bug / 加功能都要在真实例上跑通，并**补一条验收**。
2. **验收脚本是资产，不是脚手架。** 后端用 `scripts/dev/verify-*.sh`（真 HTTP，对着一个跑着的实例），
   界面用 `scripts/dev/*-ui-test.mjs`、`pages-audit.mjs`（无头 Chrome + CDP）。新功能请照抄旁边那个的骨架。
3. **不要按测试环境写死。** 凡是与具体机器 / 平台绑定的东西（硬件转码后端与设备节点、码率模式、
   ffmpeg 能力、目录、路径）一律「运行时真跑探测 + 配置项可覆盖」，探测不到就优雅降级；
   文档里写出的实测数字要标成「实例测量值」，不是项目常量。
4. **公开文档不写内网坐标。** 真值放 gitignored 的 `docs/local-*.md`。
5. **界面文案不许硬编码中文**：模块级标签常量表一律禁止（`import` 时就把语言钉死），
   用 `switch` + 内联 `t('中文')`；CI 有 `scripts/dev/i18n-coverage.mjs --max 0` 守卫。
6. **动态拼 SQL 用 `ph()` 模式**（注册参数并返回占位符），保证参数表与占位符天然一致；
   行-结构体映射统一走 `scanUser` / `scanLibrary` 这类 helper。

## 目录速览

| 路径 | 是什么 |
|---|---|
| `cmd/lmby` | 入口：服务、运维开关 `-set key=value`、`backup` / `restore` 子命令 |
| `internal/api` | HTTP 路由与 handler。**`server.go` 的路由表就是接口权威清单**；接口文档见 `docs/API.md` |
| `internal/playback` | 播放决策（纯函数 + 单测，好改好测） |
| `internal/stream` | HLS 转封装 / 转码会话、窗口与节流 |
| `internal/encoder` | 硬件能力真跑探测与参数配方（VAAPI / QSV / NVENC） |
| `internal/probe` | ffprobe 封装与流信息归一 |
| `internal/scrape` `internal/metadata` `internal/provider` `internal/match` | TMDB 刮削与人工匹配 |
| `internal/store` | PostgreSQL 存取（手写 SQL）；迁移在 `migrations/` |
| `internal/images` `overlay` `backup` `logbuf` `secrets` `settings` | 图片缓存 / 只读库叠加层 / 备份恢复 / 内存日志 / 密钥加密 / 设置 |
| `internal/livetv` `livetvsync` | 直播源导入、探测、同步 |
| `internal/worker` `scanner` `scan` | 后台任务队列与扫描调度 |
| `web/src` | 前端（React + TS + Vite），构建产物由 `go:embed` 进二进制 |
| `deploy/` | Dockerfile、compose、systemd 单元、配置样例 |
| `scripts/dev` | 验收与巡检脚本（**只在 dev**，不随交付携带） |
| `scripts/release` | 发布脚本（两分支共用） |

## 检查命令（提交前）

```bash
task check     # = vet + test + 前端构建
# 或者分开来，跟 CI 一一对应：
gofmt -l ./cmd ./internal          # 只该剩下 scripts/dev 豁免名单里那几个
go vet ./...
go test ./... -race
golangci-lint run ./...
(cd web && npx tsc --noEmit && npm run build)
node scripts/dev/i18n-coverage.mjs --max 0
```

`web/dist/index.html` 入库的是**占位页**；真前端由构建/发布流程生成。跑完 `npm run build`
记得还原：`task web:restore-placeholder`（否则提交里会带上构建产物）。

## 分支模型

| 分支 | 角色 |
|---|---|
| `dev` | **开发主线**：内部文档（`docs/ROADMAP.md`、`DEV-ENV.md`、`REQUIREMENTS.md`、`LIBRARY-NOTES.md`、`notes/`）、验收脚本、界面上的解释性文案都在这边 |
| `main` | **交付面**：只带交付所需 —— 源码、交付 README、CHANGELOG、`docs/{ADR,API.md,BOT-API.md,TRANSCODING.md,images,releases}`、`deploy/`、`migrations/`、`CONTRIBUTING.md`、`docs/RELEASING.md`、`scripts/release/` |

从 dev 派生 main 有两类**有意差异**，别手工同步：

1. **机械的**（`scripts/release/publish-main.sh`）：删内部文档与验收脚手架、把
   `docs/README.release.md` 装成 `README.md`、重新生成 `CHANGELOG.md`；
2. **要判断的**（`scripts/release/strip-ui-notes.py` + 人工过 diff）：界面解释性文案。
   **代码注释保留。**

发布流程、检查清单与自检三步见 [`docs/RELEASING.md`](docs/RELEASING.md)。

## 文档地图（dev 独有）

| 文件 | 什么时候看 |
|---|---|
| `docs/RELEASING.md` | 要发版的时候（一步步来） |
| `docs/API.md` | 要动接口 / 写脚本調接口 |
| `docs/BOT-API.md` | 给 bot 用的用户管理密钥 |
| `docs/TRANSCODING.md` | 硬件加速的实测矩阵与踩过的坑 |
| `docs/ROADMAP.md` | 逐项进度、里程碑与决策理由 |
| `docs/REQUIREMENTS.md` | 需求与验收标准 |
| `docs/DEV-ENV.md` | 开发环境与验收矩阵 |
| `docs/LIBRARY-NOTES.md` | 媒体库目录约定 |
| `docs/notes/*.md` | 专题笔记（搜索、直播、权限、Jellyfin 参考…） |
| `docs/ADR/` | 技术选型的「为什么」（例如：为什么只用标准库） |
| `docs/README.release.md` | 交付 README 的单一来源 |
| `docs/local-*.md` | 本机真值（gitignored）：实例地址、口令、部署细节 |

## 许可

AGPL-3.0-only。
