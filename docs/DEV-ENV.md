# 开发环境

LMBY 的目标平台是 Linux。Windows 只用于写代码，**不作为服务端**。

## 当前开发/测试容器

> 📌 **本文件里的内网坐标一律是占位符**：`<PVE_HOST>`、`<PVE_HOSTNAME>`、`<LMBY_DEV_IP>`、
> `<LAN_GATEWAY>`、`<MEDIA_SERVER>`、`<MEDIA_SHARE>`、`<SMB_USER>`。
> 这个仓库是**公开**的，所以内网地址、共享路径与账号名都不写进来；
> 真实值见 `docs/local-notes.md`（**已加入 .gitignore，不进版本库**，只在本机/容器里保留）。
> 端口、软件版本（PVE/Debian/Go/ffmpeg）与主机名 `lmby-dev` 保留 —— 它们是软件事实或项目名，不指向具体网络。

| 项 | 值 |
|---|---|
| PVE 宿主 | `<PVE_HOST>`（hostname `<PVE_HOSTNAME>`，PVE 9.2.11，内核 7.0.14-12-pve） |
| 客户机 | LXC **VMID 106**，hostname `lmby-dev` |
| 地址 | `<LMBY_DEV_IP>`（DHCP），网关 `<LAN_GATEWAY>` |
| 系统 | Debian 13 (trixie)，4 核 / 4G 内存 / 24G 磁盘（local-lvm） |
| 显卡直通 | `/dev/dri`（Intel UHD 630，Comet Lake / Gen9.5）→ VAAPI 可用 |
| 服务地址 | `http://<LMBY_DEV_IP>:8099` |
| 仓库位置 | 容器内 `/opt/lmby` |
| 二进制 | `/usr/local/bin/lmby`，systemd 单元 `lmby` |
| 配置 | `/etc/lmby/config.toml`（含数据库口令，权限 600） |
| Go | `/usr/local/go`（官方 1.27.1 版本，非 apt 版） |

容器创建时的两处关键配置（`/etc/pve/lxc/106.conf`）：

```
lxc.cgroup2.devices.allow: c 226:* rwm
lxc.mount.entry: /dev/dri dev/dri none bind,optional,create=dir
```

容器内安装的依赖：

```bash
apt-get install -y ffmpeg vainfo intel-media-va-driver-non-free libvpl2 \
                   libmfx-gen1.2 libvpl-tools intel-gpu-tools \
                   postgresql git curl build-essential jq unzip
```

> 需要注意：Debian 13 的软件源默认只有 `main contrib`，装 Intel 的完整 iHD 驱动
> 必须先补上 `non-free non-free-firmware`。

## 部署流程

```bash
# 0. 先构建前端（web/dist 不进版本库，本地构建后才会有真实产物）
cd web && npm install && npm run build && cd ..

# 1. 打包含前端的源码包（Windows 侧）
tar czf lmby-src.tgz --exclude=./.git --exclude=./web/node_modules -C <仓库路径> .

# 2. 上传到容器
scp lmby-src.tgz root@<LMBY_DEV_IP>:/root/

# 3. 容器内：解包 → 编译 → 重启
ssh root@<LMBY_DEV_IP> '
  rm -rf /opt/lmby && mkdir -p /opt/lmby
  tar xzf /root/lmby-src.tgz -C /opt/lmby
  cd /opt/lmby
  GOPROXY=https://goproxy.cn,https://proxy.golang.org,direct \
    /usr/local/go/bin/go build -trimpath -o /tmp/lmby.new ./cmd/lmby
  systemctl stop lmby && mv /tmp/lmby.new /usr/local/bin/lmby && systemctl start lmby
'
```

> ⚠️ **两次踩坑（血泪教训）**：
> 1. 正在运行的二进制不能直接覆盖（`ETXTBSY`），所以先编到 `/tmp/lmby.new` 再 `mv`。
> 2. **上传/解包可能静默失败**。曾出现「sftp 报成功、tar 也报成功，但解出来的还是旧代码」
>    —— 结果是跑了半天旧逻辑才发现。所以在第 3 步里**必须先 md5 比对上传包**，
>    解包后再 `grep` 一个刚改的标记确认拿到的确实是新代码。
>    用 `tar tvzf` 读回来 grep 那个字符串是最直接的确认方式。
> 3. **往 ssh 命令行里塞引号在 PowerShell 下必翻车**：`$(...)`、`\"`、甚至
>    `grep -c "x" file` 都会被本机 shell 先吃掉（`grep` 会拿到三个参数、把 `x` 当文件名）。
>    一律改成「本地写 .sh → 上传 → bash 执行」：
>    `node ssh.mjs exec "tr -d '\r' < /root/x.sh > /tmp/x.sh && bash /tmp/x.sh"`。
> 4. **sftp 上传后立刻比对 md5 可能读到半截文件**：`fastPut` 回调返回后本机进程就退出了，
>    远端落盘还剩一点尾巴。要么把「上传 + 校验」放在同一次会话里，要么中间
>    `Start-Sleep -Seconds 2`。（本次出现过「md5 不一致 → 隔几秒再比就一致」的假警报。）

## 容器内编译与验收（本机没装 Go）

所有编译、vet、单测都在容器里跑，不要在本机折腾工具链：

```powershell
# Windows 侧：打包（注意排除 .git 与 web/node_modules）
tar czf $env:TEMP\lmby-src.tgz --exclude=./.git --exclude=./web/node_modules -C . .

node ssh.mjs put $env:TEMP\lmby-src.tgz /root/lmby-src.tgz
node ssh.mjs put scripts/dev/container-verify.sh /root/cv.sh
Start-Sleep -Seconds 2
node ssh.mjs exec "tr -d '\r' < /root/cv.sh > /tmp/cv.sh && bash /tmp/cv.sh"
```

`scripts/dev/container-verify.sh` 一次做完：解包 → 校验新代码标记 → gofmt → `go build`（产物
`/tmp/lmby.new`）→ `go vet` → `go test`。gofmt 真的改过的文件会复制到 `/root/fmt-pull/`，
可以用 `node ssh.mjs get` 拉回本地（**CI 的 lint 任务是跑 golangci-lint 的，格式化不过就是红**）。

> `ssh.mjs` 是本机自用的小工具（本机没 plink/sshpass，带密码 SSH 只能靠 `npm i ssh2` 写一个），
> 支持 `exec` / `put` / `get`，口令从环境变量 `SSH_PASS` 进，不落盘。

`node ssh.mjs` 那条命令的细节见 `docs/DEV-ENV.md` 上方的四个坑；其中\#3（引号）与\#4（md5）
是 2026-09-20 这轮新踩的。

### lint（与 CI 同版本）

```bash
bash scripts/dev/lint.sh          # 需要在 /opt/gcl 有对应版本的 golangci-lint
bash scripts/dev/lint.sh --new-from-rev=HEAD~1   # 只看本次改动的文件
```

> ⚠️ **`.golangci.yml` 必须是 v2 格式（开头要有 `version: "2"`）**。
> CI 的 action 默认装 latest（现在是 v2），v1 格式的配置会让它直接
> `can't load config: unsupported version of the configuration` 退出 ——
> 也就是说 lint 任务从 M0 到 2026-09-20 之间**一次检查都没真跑过**（一直是红的但没人看）。
> 改完 lint 相关的东西，先在容器里跑一遍再推。

## 验收脚本

```bash
# 一次性准备：PostgreSQL 角色/库 + /etc/lmby/config.toml
bash scripts/dev/setup-pg.sh

# 后端接口端到端（42 项断言）
bash scripts/dev/smoke-test.sh
# 已有账号时：SMOKE_USER=admin SMOKE_PASS=xxx bash scripts/dev/smoke-test.sh

# 浏览器端到端（26 项断言，CDP 驱动真实 Chrome，会截图到 shots/）
BASE=http://<LMBY_DEV_IP>:8099 CHROME='/path/to/chrome' node scripts/dev/browser-test.mjs

# M0 界面验收（登录/初始化/个人中心/主题，26 项）
# M1 界面验收（媒体库/扫描进度/条目表，18 项）
BASE=http://<LMBY_DEV_IP>:8099 LMBY_USER=devtest LMBY_PASS=xxx node scripts/dev/m1-ui-test.mjs

# M2 条目编辑（字段锁定）界面验收（37 项，会截图到 shots-item-edit/）
BASE=http://<LMBY_DEV_IP>:8099 LMBY_USER=devtest LMBY_PASS=xxx node scripts/dev/item-edit-ui-test.mjs

# M2 字段锁定的真库验收（42 项）——改字段 → 锁住 → 重扫，
# 验「锁住的没被覆盖、没锁的被 nfo 改写」（对照组），跑完自动还原
LMBY_USER=devtest LMBY_PASS=xxx bash scripts/dev/verify-item-edit.sh
```

这几个脚本都是**无依赖**的（`smoke-test.sh` / `verify-item-edit.sh` 只用 curl + jq；两个界面脚本只用
Node 内置 `WebSocket` 直连 Chrome DevTools Protocol，不需要 Puppeteer）。

### 匹配打分器验收（M2）

```bash
# 拿真实库里的条目对着真 TMDB 跑一遍，统计自动匹配率（就是 DoD 里那条 ≥ 90%）
bash scripts/dev/match-sample.sh          # 2 部剧集 + 10 部随机电影
DEEP=1 bash scripts/dev/match-sample.sh 3 # 额外取详情（含 alternative_titles），能救回中文译名与主标题差得远的情况
```

二进制它自己找：优先 `/tmp/lmby.new`（刚编译的），否则用已部署的 `/usr/local/bin/lmby`。
脚本直接读 `media_items` 随机抽样，人工只需扫一眼榜首对不对。

### M1 新增：数据库侧校验

```bash
PGPASSWORD=$(cat /etc/lmby/pg-password) \
  psql -h 127.0.0.1 -U lmby -d lmby -f scripts/dev/verify-m1.sql
```

15 组只读查询：类型统计、元数据落库率、季集号范围、层级完整性、图片归属、扫描问题。
任何一项异常都能直接定位到扫描器的哪一步出了问题。

## 媒体库挂载（CIFS，测试用）

```bash
# 凭据文件（600，不进版本库）
printf "username=<SMB_USER>\npassword=***\n" > /etc/lmby/cifs-gdfs.cred
chmod 600 /etc/lmby/cifs-gdfs.cred

# 关键：rsize/wsize 不能太大
mount -t cifs //<MEDIA_SERVER>/<MEDIA_SHARE> /mnt/media \
  -o credentials=/etc/lmby/cifs-gdfs.cred,vers=3.0,iocharset=utf8,\
     uid=0,gid=0,file_mode=0444,dir_mode=0555,\
     rsize=131072,wsize=131072,cache=loose,actimeo=60
```

### 重要发现：`rsize=4MB` 会让小文件读取慢 150 倍

这台 CIFS 服务端后面挂的是网盘，实测：

| 挂载参数 | 读一个 5KB 的 nfo |
|---|---|
| `rsize=4194304,wsize=4194304`（内核默认） | **2.13 秒**（`strace` 里是单次 `read()` 阻塞） |
| `rsize=131072,wsize=131072,cache=loose,actimeo=60` | **~11 毫秒** |

排查方法：`strace -f -tt -T -e trace=openat,read -p <pid>`，能看到
`read(7, "<?xml...", 5080) = 5079 <2.130672>` 这种「读 5KB 花了 2 秒」的调用。

> 注意：`stat` 与「打开不存在的文件」都是毫秒级 —— 慢的只是**读已存在文件的内容**，
> 所以单纯跑 `find -type f | wc -l` 测速是发现不了问题的。

## 账号管理 CLI

初始化向导只能建第一个账号，后续账号（后台部署、自动化测试、忘记口令）用 CLI：

```bash
LMBY_PASSWORD=xxx lmby user add devtest --admin --config /etc/lmby/config.toml
lmby user passwd devtest --config /etc/lmby/config.toml   # 会顺便撤销该账号全部会话
lmby user ls --config /etc/lmby/config.toml
```

口令来源优先级：`--password` < 环境变量 `LMBY_PASSWORD` < 标准输入（避免落进 shell 历史与 `ps`）。
旗标位置不敏感（`user add devtest --admin` 与 `user add --admin devtest` 都行）。

## 匹配打分器 CLI
调阈值、看候选排序、解释「为什么匹配到这一条」都靠它（对着真 TMDB）：

```bash
# 剧集：本地标题/年份/季号/集数都喂进去，看候选排序与每项明细
/tmp/lmby.new match --kind tv --title "钢之炼金术师 FULLMETAL ALCHEMIST" \
                    --year 2009 --season 1 --episodes 64 --top 3

# --deep：对每个候选取详情（含 alternative_titles），把集数与单集时长也拉进来参与打分
/tmp/lmby.new match --kind movie --title "孔中窥见真理之貌" --year 2013 --deep

# --json：给脚本用（其余日志一律走 stderr，stdout 只放数据）
/tmp/lmby.new match --kind movie --title "言叶之庭" --year 2013 --json
```

输出里每一行明细就是一项打分：`title / year / structure`，各带权重、得分与说明文字。
直接打 `lmby match`（不带参数）会打用法。

## 刮削 CLI 与接口

**元数据优先级：本地优先，到每集粒度。** 媒体同目录有 nfo 就用 nfo
（剧集看 `tvshow.nfo`、季看 `season.nfo`、集看 `S01E01.nfo`、电影看同名 nfo）；
**本地没有、或本地文件格式不对**才去扫；自动流程永不覆盖已有本地元数据，
除非显式 `--force`。`lmby scrape status` 会把「nfo 元数据」单独列一项。

```bash
# 入队（--force 连已匹配的重刮，--kind 限 movie/series，--library 限某个库）
lmby scrape enqueue --library 1

# 入队并就地跑完（不依赖服务进程，验收时用这个盯单条结果）
lmby scrape run --limit 12

# 看队列水位 + 各库的匹配状态分布
lmby scrape status
lmby scrape status --json

# TMDB 短暂不可用导致一批失败后，重置重试
lmby scrape reset --library 1
```

`scrape run` 结束时会打「缓存命中 / 回源」计数 —— 重跑时回源应当为 0
（这是 DoD 里「重复刮削零 API 调用」的观测量）。

服务侧对应三个接口（需要登录）：

```
POST /api/v1/libraries/{id}/scrape         body: {"force": false, "kind": "movie"}  → 入队
GET  /api/v1/libraries/{id}/scrape                                              → 刮削进度
POST /api/v1/libraries/{id}/scrape/reset                                        → 重置失败项并入队
```

未配 TMDB 凭据时入队接口回 **409**，且服务启动时不会注册刮削处理器
（日志里会有一行「未配置 TMDB 凭据：元数据刮削与图片回源不可用」）—— 不让任务白排队。

## 图片接口

覆盖顺序：**媒体目录里的本地图 > 用户手选（UI 未做）> TMDB 下载缓存**。
本地图能直接用就直接送原文件；要缩放才落缓存（`<data_dir>/images/cache`，默认上限 512MB），
回源下到的原图落 `<data_dir>/images/remote`（算数据，不参与清理）。

```
GET /api/v1/items/{id}/images                      → 列出这个条目有哪些图（本地/回源、尺寸、URL）
GET /api/v1/items/{id}/images/{kind}?w=&h=&format=&q=  → 输出图（kind: poster|fanart|logo|banner|thumb...）
```

`w`/`h` 是「等比缩放进这个框」，不放大；`format=jpeg|png` 默认尽量保持原格式；
带 ETag 与 `Cache-Control: private, max-age=3600`，重复请求会走 304。

配置（config.toml）：

```toml
[images]
cache_dir = "/var/lib/lmby/images"  # 默认 <data_dir>/images
max_cache_mb = 512
```

## 人工匹配接口

界面上是导航里的「人工匹配」页（`/match`）。后端四个接口：

```
GET  /api/v1/items/{id}/match          状态 + 候选（含打分明细，读的是刮削时存的 match_candidates）
POST /api/v1/items/{id}/match          {"providerId": 431819} 应用选定 → 状态转 manual
POST /api/v1/items/{id}/match/search   {"query": "..."} 换个词重搜（不落库）
POST /api/v1/items/{id}/match/skip     {"reason": "..."} 标记不需要匹配
GET  /api/v1/libraries/{id}/items?matchState=review,failed   列表支持逗号分隔的状态过滤
```

前端不单独跑：`cd web && npm run build` 的产物会被 `go:embed` 进二进制。
容器里验前端渲染可以无头 Chrome 截图（见 M2 验收记录里的做法）。

### 手改 nfo 之后怎么让它生效

nfo 在本项目里是权威元数据，所以给了个「重新导入」的口子：

```bash
# 触发扫描时带 refreshMetadata：文件没变也重读一遍 nfo
# （包括目录级的 tvshow.nfo / season.nfo）
curl -b cookies.txt -X POST http://<LMBY_DEV_IP>:8099/api/v1/libraries/1/scan \
  -H 'Content-Type: application/json' -d '{"refreshMetadata": true}'
```

实测 479 个文件全量重读只花 6.4 秒（CIFS 上 ~11ms/个）。
不用 touch 媒体文件 —— 网络盘上 touch 会连带把几万个文件的重新探测都触发一遍。

## 环境相关的注意事项

1. **`truncate users cascade` 可以重置到「首次安装」状态**，用于重复验证初始化向导。
2. 容器 root 的口令登录默认被 sshd 拒绝，需要 `/etc/ssh/sshd_config.d/99-lmby.conf`
   里显式打开 `PermitRootLogin yes`。
3. Windows 侧没有 plink/sshpass，带口令的非交互 SSH 走 Node + `ssh2`（密码经环境变量传入）。
   上传后要校验 md5，但**别在同一秒就比**（sftp 落盘有尾巴）。
4. 前台跑的构建/安装在一次会话结束后可能被回收 —— 长任务用 `nohup ... &` 放后台并轮询日志。
5. 工具调用有 120 秒上限：浏览器端到端脚本要控制在 100 秒内（把「等待扫描跑完」这类
   长等待移出去，改为在数据库侧校验）。
