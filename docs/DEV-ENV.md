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

> 正在运行的二进制不能直接覆盖（`ETXTBSY`），所以先编到 `/tmp/lmby.new` 再 `mv`。

## 验收脚本

```bash
# 一次性准备：PostgreSQL 角色/库 + /etc/lmby/config.toml
bash scripts/dev/setup-pg.sh

# 后端接口端到端（42 项断言）
bash scripts/dev/smoke-test.sh
# 已有账号时：SMOKE_USER=admin SMOKE_PASS=xxx bash scripts/dev/smoke-test.sh

# 浏览器端到端（26 项断言，CDP 驱动真实 Chrome，会截图到 shots/）
BASE=http://127.0.0.1:8099 CHROME='/path/to/chrome' node scripts/dev/browser-test.mjs
```

两个脚本都是**无依赖**的（`smoke-test.sh` 只用 curl + jq；`browser-test.mjs` 只用 Node 内置
`WebSocket` 直连 Chrome DevTools Protocol，不需要 Puppeteer）。

## 环境相关的注意事项

1. **`truncate users cascade` 可以重置到「首次安装」状态**，用于重复验证初始化向导。
2. 容器 root 的口令登录默认被 sshd 拒绝，需要 `/etc/ssh/sshd_config.d/99-lmby.conf`
   里显式打开 `PermitRootLogin yes`。
3. Windows 侧没有 plink/sshpass，带口令的非交互 SSH 走 Node + `ssh2`（密码经环境变量传入）。
4. 前台跑的构建/安装在一次会话结束后可能被回收 —— 长任务用 `nohup ... &` 放后台并轮询日志。
