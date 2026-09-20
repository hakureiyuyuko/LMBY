# 开发环境

LMBY 的目标平台是 Linux。Windows 只用于写代码，**不作为服务端**。

## 当前开发/测试容器

| 项 | 值 |
|---|---|
| PVE 宿主 | `<PVE_HOST>`（hostname `<PVE_HOSTNAME>`，PVE 9.2.11，内核 7.0.14-12-pve） |
| 客户机 | LXC **VMID 106**，hostname `lmby-dev` |
| 地址 | `<LMBY_DEV_IP>`（DHCP），网关 `<LAN_GATEWAY>` |
| 系统 | Debian 13 (trixie)，4 核 / 4G 内存 / 24G 磁盘（local-lvm） |
| 显卡直通 | `/dev/dri`（Intel UHD 630，Comet Lake / Gen9.5）→ VAAPI 可用 |
| 服务地址 | <http://127.0.0.1:8099> |
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

## 验收脚本

```bash
# 一次性准备：PostgreSQL 角色/库 + /etc/lmby/config.toml
bash scripts/dev/setup-pg.sh

# 后端接口端到端（42 项断言）
bash scripts/dev/smoke-test.sh
# 已有账号时：SMOKE_USER=admin SMOKE_PASS=xxx bash scripts/dev/smoke-test.sh

# 浏览器端到端（26 项断言，CDP 驱动真实 Chrome，会截图到 shots/）
BASE=http://127.0.0.1:8099 CHROME='/path/to/chrome' node scripts/dev/browser-test.mjs

# M0 界面验收（登录/初始化/个人中心/主题，26 项）
# M1 界面验收（媒体库/扫描进度/条目表，18 项）
BASE=http://127.0.0.1:8099 LMBY_USER=devtest LMBY_PASS=xxx node scripts/dev/m1-ui-test.mjs
```

两个脚本都是**无依赖**的（`smoke-test.sh` 只用 curl + jq；`browser-test.mjs` 只用 Node 内置
`WebSocket` 直连 Chrome DevTools Protocol，不需要 Puppeteer）。

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

## 环境相关的注意事项

1. **`truncate users cascade` 可以重置到「首次安装」状态**，用于重复验证初始化向导。
2. 容器 root 的口令登录默认被 sshd 拒绝，需要 `/etc/ssh/sshd_config.d/99-lmby.conf`
   里显式打开 `PermitRootLogin yes`。
3. Windows 侧没有 plink/sshpass，带口令的非交互 SSH 走 Node + `ssh2`（密码经环境变量传入）。
4. 前台跑的构建/安装在一次会话结束后可能被回收 —— 长任务用 `nohup ... &` 放后台并轮询日志。
5. 工具调用有 120 秒上限：浏览器端到端脚本要控制在 100 秒内（把「等待扫描跑完」这类
   长等待移出去，改为在数据库侧校验）。
