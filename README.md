# LMBY

**L**ight 的 **E**mby —— 一个刻意做小的自托管媒体服务器。

Emby / Jellyfin 功能齐全，代价是重量与兼容性负担。LMBY 反过来：只做「**在浏览器里看自己的影视库**」这件事，
把它做到够用、可控、好部署。

---

## 定位

> **纯 Web 的在线播放站点**：浏览器是唯一客户端，HTTP 是唯一出口。

因此 LMBY 不做、也不打算做：DLNA/UPnP、Emby/Jellyfin 专有客户端协议、插件系统、音乐库、直播以外的直播电视扩展。
这些不是「还没做」，而是「故意不做」——它们占了 Emby 大部分复杂度，却不是自用场景的核心价值。

## 技术形态

| 层 | 选型 |
|---|---|
| 后端 | Go（标准库 `net/http` 路由、`pgx/v5` 手写 SQL、`slog`） |
| 前端 | React + TypeScript + Vite（构建期用 Node，**运行期零 Node**） |
| 存储 | PostgreSQL（compose 里自带，也支持外部 DSN） |
| 转码 | 外部 ffmpeg + HLS |
| 任务队列 | PostgreSQL 表（不引 Redis/MQ） |

**部署物只有两件：一个二进制 + 一个 PostgreSQL。**

依赖纪律与取舍详见 [`docs/ADR/0001-stdlib-first.md`](docs/ADR/0001-stdlib-first.md)。

## 功能状态

**M0（骨架）已完成，并在真实容器 + 真实浏览器上实测验收。**

已具备：内嵌数据库迁移（自动应用）、账号体系（初始化向导 / 登录 / 登出 / argon2id）、
个人中心（改口令并踢掉其他设备、显示名、我的设备、外观偏好）、明暗主题
（首屏生效、默认跟随系统、登录后同步账号）、登录失败限流、结构化日志、`/healthz` 健康检查。

验收结果：

| 脚本 | 结果 | 覆盖范围 |
|---|---|---|
| `scripts/dev/smoke-test.sh` | **42/42 通过** | 接口与鉴权边界、会话、偏好、改口令踢设备、登出、静态兜底、非法输入 |
| `scripts/dev/browser-test.mjs` | **26/26 通过** | CDP 驱动真实 Chrome：首屏跟随系统主题、明暗切换与持久化、初始化向导、概览页、个人中心、主题同步账号、界面改口令、重新登录 |

文档：

| 文件 | 内容 |
|---|---|
| [`docs/REQUIREMENTS.md`](docs/REQUIREMENTS.md) | 需求、范围边界、设计取舍 |
| [`docs/ROADMAP.md`](docs/ROADMAP.md) | M0~M9 路线图与逐项验收标准 |
| [`docs/ADR/`](docs/ADR/) | 技术决策记录（为什么只用标准库） |
| [`docs/DEV-ENV.md`](docs/DEV-ENV.md) | 开发环境、部署流程、验收脚本用法 |
| [`docs/TRANSCODING.md`](docs/TRANSCODING.md) | 硬件加速实测矩阵与已踩过的坑 |

## 快速开始

### Docker（推荐）

```bash
cd deploy
POSTGRES_PASSWORD=$(openssl rand -hex 16) \
LMBY_ADMIN_PASSWORD='换成你的口令' \
docker compose up -d
```

打开 `http://localhost:8099`。核显转码需要 `/dev/dri`（compose 里已映射，没有核显可删掉该段）。

### 手动部署

```bash
# 1. 编译（前端 + 后端 → 单二进制）
task build          # 等价于 cd web && npm run build && go build -o lmby ./cmd/lmby

# 2. 准备数据库与配置
#    注意：createdb 必须指定编码与 locale。宿主 locale 是 C 时（很多最小化系统默认就是），
#    不加这些参数建出来的是 SQL_ASCII + C 的库 —— 那种库里中文会退化成字节，
#    中文搜索、模糊匹配会静默失效（LMBY 启动时会直接拦下并告诉你修法）。
sudo -u postgres psql -c "create role lmby login password '你的口令'"
sudo -u postgres createdb -E UTF8 --lc-collate=C.UTF-8 --lc-ctype=C.UTF-8 -T template0 -O lmby lmby
sudo cp deploy/lmby.example.toml /etc/lmby/config.toml   # 改掉其中的 dsn

# 已经建错字符集的库可以就地重建（会备份、逐表比对行数、跑中文自检）：
#   sudo systemctl stop lmby && bash scripts/dev/fix-db-encoding.sh && sudo systemctl start lmby

# 3. 启动（会自动应用迁移）
./lmby serve --config /etc/lmby/config.toml
```

首次打开会进入**初始化向导**，创建第一个管理员账号。
也可以在启动前设置 `LMBY_ADMIN_PASSWORD`，由程序自动创建 `admin` 账号。

### systemd

```bash
sudo install -m 755 lmby /usr/local/bin/lmby
sudo install -m 644 deploy/lmby.service /etc/systemd/system/lmby.service
sudo systemctl enable --now lmby
```

## 配置

优先级：内置默认值 < 配置文件 < 环境变量（`LMBY_*`）。
完整键位见 [`deploy/lmby.example.toml`](deploy/lmby.example.toml)。

常用环境变量：

| 变量 | 说明 |
|---|---|
| `LMBY_LISTEN` | 监听地址，默认 `:8099` |
| `LMBY_DATA_DIR` | 运行期数据目录 |
| `LMBY_DATABASE_DSN` | PostgreSQL 连接串 |
| `LMBY_ADMIN_PASSWORD` | 首次启动自动创建管理员 |
| `LMBY_FFMPEG_PATH` | 指定 ffmpeg 可执行文件 |
| `LMBY_SECURE_COOKIES` | 走 HTTPS 时置 `true` |

## 开发

```bash
task web:dev    # 终端 1：Vite 开发服务器（前端热更新）
task run        # 终端 2：后端
task check      # 提交前：vet + test + 前端构建
```

数据库迁移就是往 `migrations/` 加一个 `NNNN_描述.sql`，格式与注意事项见
[`migrations/embed.go`](migrations/embed.go) 的包注释。

### 硬件加速实测（结论）

在 Intel UHD 630（Comet Lake，Gen9.5）+ Debian 13 上：

- ✅ **VAAPI 可用**：H264/HEVC 硬件编解码，1080p 约 8~11x 实时
- ❌ **QSV 不可用**：Debian 13 已移除 legacy MediaSDK，`libmfx-gen` 只支持 Gen12+，
  `vpl-inspect` 明确报告 `no implementations found`

这个例子正是 LMBY 的一条设计原则：**能力必须运行时实测，不能只看 `ffmpeg -encoders` 列表**
（那里明明列着 `h264_qsv`，但它在这台机器上跑不起来）。
完整数据与参数坑见 [`docs/TRANSCODING.md`](docs/TRANSCODING.md)。

## 许可

AGPL-3.0-only。
