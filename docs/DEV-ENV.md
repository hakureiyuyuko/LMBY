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

# M2 搜索：切词/索引侧（15 项，直接对 PG 跑）
bash scripts/dev/verify-search-sql.sh

# M2 搜索：真库 HTTP 端到端（26 项，含错字容忍与「改完标题立即可搜」）
LMBY_USER=devtest LMBY_PASS=xxx bash scripts/dev/verify-search.sh

# M2 搜索：界面（22 项，无头 Chrome，截图到 shots-search/）
BASE=http://<LMBY_DEV_IP>:8099 LMBY_USER=devtest LMBY_PASS=xxx node scripts/dev/search-ui-test.mjs

# M2 海报墙 / 剧集视图 / 批量 / 设置页：真库 HTTP 验收
LMBY_USER=devtest LMBY_PASS=xxx bash scripts/dev/verify-browse.sh    # 40 项
LMBY_USER=devtest LMBY_PASS=xxx bash scripts/dev/verify-settings.sh  # 38 项

# 同一批的界面验收（34 项，截图到 shots-m2/）；人工匹配的批量选择需要库里有待处理条目：
bash scripts/dev/seed-review-item.sh                                  # 临时造一条 review
BASE=http://<LMBY_DEV_IP>:8099 LMBY_USER=devtest LMBY_PASS=xxx node scripts/dev/m2-ui-test.mjs
bash scripts/dev/seed-review-item.sh --restore                        # 用完还原

# 播放：真库 HTTP 端到端（108 项）——Range/ETag、mkv→HLS 分片、seek、stop 回收、
# 多版本选片、10bit HEVC 的转码决策、字幕抽 WebVTT、进度与续播、继续观看。
# 样本（哪个文件走直出/转封装/转码）由脚本按编码条件从库里现挑，不写死文件名；
# 它用 psql 读库挑样本，所以需要 /etc/lmby/pg-password（默认路径，可用 PGPASSWORD_FILE 覆盖）。
LMBY_USER=devtest LMBY_PASS=xxx bash scripts/dev/verify-play.sh
TEST_IDLE=1 LMBY_USER=devtest LMBY_PASS=xxx bash scripts/dev/verify-play.sh   # 额外验「无人观看 45s 自动回收」

# 播放器界面验收（40 项，真 Chrome 真的把片子放起来，含转码条目真起播、画质档位菜单，截图到 shots-play/）
BASE=http://<LMBY_DEV_IP>:8099 LMBY_USER=devtest LMBY_PASS=xxx node scripts/dev/play-ui-test.mjs

# M4 转码：真库 HTTP 端到端（51 项）——能力表、转码决策与理由链、真出分片并用 ffprobe
# 交叉验证输出确实是 h264、转码路径上的 seek、stop 回收、幅面上限、HDR 色调映射、Hi10P、
# 播放器画质档（maxHeight：选了低档就从直出变转码、切档后确实是另一路会话）。
# 它**不假设本机一定能转**：先读能力表，只有探测到可用的 h264 编码器才断言「能播」，
# 否则断言「如实说放不了」——所以它在弱机器上同样有意义。样本按编码条件现挑，优先 1080p。
LMBY_USER=devtest LMBY_PASS=xxx bash scripts/dev/verify-transcode.sh

# M4 编码能力探测（13 项）：能力表与这台机器的实际表现逐条对照，含缓存命中与强制刷新
LMBY_USER=devtest LMBY_PASS=xxx bash scripts/dev/verify-caps.sh
```

■ 播放验收的几个坑（都是实测踩出来的）：

1. **分片必须与播放列表同级**（`/api/v1/play/{sid}/seg_00000.m4s`，不是 `/seg/xxx`）。
   m3u8 里写的是相对文件名，hls.js / Safari / ffprobe 都会拿播放列表 URL 作基准拼——
   放在子路径下会全部 404。`verify-play.sh` 用 ffprobe 直接读服务发出的 m3u8 交叉验证，
   就是这一条把问题当场拓出来的。
2. **不要把 m3u8 直接交给 `<video src>`**：Chrome 对 `application/vnd.apple.mpegurl`
   的 `canPlayType` 会回 `maybe`（实测 Chrome 153），真塞进去的话 `readyState=4` 但
   `duration=0`、画面永远不动且不报错。现在只在 UA 确实是 Safari/iOS 时才走原生 HLS。
3. **分片必须禁缓存（`no-store`，也不能带 Last-Modified）**：换一段窗口时分片文件名是
   复用的（都是从 `seg_00000.m4s` 开始），内容却完全不同。允许缓存的话，seek 之后
   浏览器会把**上一个窗口的字节**（片子开头）吐回来 —— 表现是「拖到 1:30 却从头开始放」，
   而时间轴显示的是新位置。`verify-play.sh` 里有三条断言钉住这件事。
4. **拖动不能“就地跳”到未缓冲的位置**：hls.js 的 `seekable` 只是「已下载的那几个分片」，
   把一个还没下载到的位置赋给 `video.currentTime` 会被浏览器夹回起点。客户端的判据是
   「在窗口内 **且** 在已加载区间内」才本地跳，否则让服务端从目标位置重开一段。
5. **进度条拖动会连发上百个事件**（几乎每个像素一个）。早先的实现用「正在处理就丢弃」
   挡并发，结果是「拖到哪都不算数，只跳到第一个事件的位置」。正确做法：拖动期间只更新
   显示，防抖 + 提交时排队（在途的 seek 结束后补做最后那一个目标）。
6. **HDR 转码不要用 `tonemap_vaapi`**（iHD 要输入带 mastering display 元数据，库里常见
   素材没有 → 滤镜报错 → **整路转码起不来**，用户看到的是「放不了」）；而且**缩放要放在
   色调映射之前**（先在显存里 `scale_vaapi` 再 `hwdownload`），否则 4K 连 20 秒起播超时
   都过不去。这两条都是真跑才拓出来的，细节与实测数字见 `docs/TRANSCODING.md`。

■ 脚本写完后**先跑 `bash -n`**（在容器里）再执行：曾因一行少了参数展开的 `}`，
脚本跑到一半报「引号未闭合」，很难看出在哪一行。

■ **改前端 / 重新部署的顺序**（踩过，会直接上线一个残废界面）：

```
cd web && npm run build      # 生成 dist/index.html + dist/assets/*
tar czf ... -C . .           # 打包必须在下一步之前！
# 发布/部署（容器里 go build 把 dist 嵌进二进制）
task web:restore-placeholder # = git checkout -- web/dist/index.html，只在提交前跑
```

入库的 `web/dist/index.html` 是**占位页**；真的构建产物（带哈希的 assets）不入库。
如果在 `npm run build` 之前先把占位页恢复回去，打出来的包就会把占位页嵌进二进制
（或者是更早的构建残留：index.html 引用的 bundle 根本不在包里 —— 界面白屏或
“少几个入口” 且无任何报错，因为后端原先会把缺失的静态资源也回退成 index.html）。
后端现在会对缺失资源回 404，至少能在控制台看到真正的错误。

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

### 数据库字符集（必须 UTF8 + UTF-8 的 lc_ctype）

非 Docker 安装的一大坑：宿主 locale 是 C（最小化系统默认）时，`createdb` 建出来的库是
**SQL_ASCII + C**。这种库“看起来”能用（Go 写进去的 UTF-8 字节原样存原样取），但中文全完：

- `server_encoding=SQL_ASCII`：`length('钢')=3`、`to_tsvector` 认不出 CJK —— 中文搜索必然失效；
- `lc_ctype=C`：就算编码是 UTF8，`pg_trgm` 也切不出中文三元组
  （`show_trgm('某科学的超电磁炮')` 是空集）—— 模糊匹配/错字容忍整路静默失效。

两者都不报错，只会在搜索结果里少东西，所以：

```bash
# 建库时就指定（setup-pg.sh 已内置这段与探针）
createdb -E UTF8 --lc-collate=C.UTF-8 --lc-ctype=C.UTF-8 -T template0 -O lmby lmby

# 已经建错了：就地重建（dump → 旧库改名保留 → 用 UTF8+C.UTF-8 重建 →
#   lmby migrate 建结构 → --data-only 灌数据 → 对齐 identity 序列 →
#   逐表比对行数 → 字符语义与中文三元组自检；任一步不对都给回滚命令）
systemctl stop lmby
bash scripts/dev/fix-db-encoding.sh
systemctl start lmby

# 只是查现状
bash scripts/dev/fix-db-encoding.sh   # 字符集已正确时它会只跑自检
```

应用侧也有一道开机自检（`internal/store/store.go` 的 `checkEncoding`）：
不满足就**拒绝启动**并打印修法 —— 宁可开不了机，也不要「搜索悄悄少结果」。

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
