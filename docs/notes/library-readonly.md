# 只读媒体库与 overlay（设计笔记）

> 需求原话：**「媒体库加个开关 只读 以适应网盘挂载；打开只读模式后刮削的元数据和图片
> 以 overlay 的模式写入」**。

## 一、为什么需要

库可以挂在网盘 / 只读挂载上（Alist、rclone、NFS `ro`、只读 bind mount）。
这种库上：

- **扫描**：只读文件系统与本地 nfo，本来就不写；
- **探测**：`ffprobe` 只读；
- **但「刮削产出」得找地方落** —— 不能落在媒体目录里（写不进去，或者更糟：
  网盘上会触发一次重上传）。

而按「媒体目录里的东西优先」这条老原则（与 nfo 同一个模型），
刮削出来的图片、元数据**本来是该写回媒体目录的**（ROADMAP 里写着「以后的可选能力」）。
只读库把这件事堵死了，于是需要一个「不落在媒体目录里的落点」—— 就是 overlay。

## 二、做法

### 开关

- 迁移 `migrations/0014_library_readonly.sql`：`libraries.readonly boolean not null default false`。
- `library_paths.readonly` 早在 `0001_init.sql` 就有了（**根路径级**的事实），
  但一直没有代码用它。现在两者**同步**：`store.SetLibraryReadOnly` 在一个事务里
  同时改库级列与所有根路径的行 —— 不同步的话，以后按路径判断写入权限的人会拿到错的答案。
- 接口：`PATCH /api/v1/libraries/{id}` 现在同时受理 `name` 与 `readonly`
  （原来只支持改名）；`GET /libraries` 与 `GET /libraries/{id}` 的库对象里直接带 `readonly`。

### overlay 层

```
<数据目录>/overlay/<libraryId>/<itemId>/poster.jpg     ← 刮削到的图片
<数据目录>/overlay/<libraryId>/<itemId>/meta.json      ← 元数据快照
```

三条取舍（`internal/overlay` 的包注释里同样写着）：

1. **按库分块，不按路径分**：overlay 属于「这个库」。重新挂载换了挂载点、
   条目换了路径，叠加层里的东西照样对得上号。
2. **不参与图片缓存淘汰**：`images` 包里的 `cache/` 是缓存（按上限清理），
   `remote/` 是数据；overlay 里放的是「这个库的刮削成果」，清掉就得重新联网刮一次，
   所以它算**数据**。目录也刻意不放进 `images/` 下面，免得以后有人顺手给缓存清理加了它。
3. **本地图不进 overlay**：本地图本来就在媒体目录里，属于只读输入；
   只有 `source = remote`（回源来的）才补一份进叠加层。

### 图片怎么进去

写点在「取图的唯一漏斗」`images.Service.Open`，**不是**只在下载处：

- 下载处只覆盖「这次真的联网下了一张」；
- 打开只读开关之前就缓存进 `remote/` 的图，也得补 —— 否则叠加层只能拿到
  「开关之后新下的那几张」。放在 `Open` 里用 `PutImageIfMissing`（有就跳过）收敛。

### 元数据快照怎么进去

`scrape` 落库的地方统一走 `Handler.applyMeta`（`internal/scrape/handler.go`）：
先 `ApplyItemMeta`，再对只读库写一份 JSON 快照。

- **落库失败照旧原样返回**（调用方要区分 `ErrAlreadyExists` 这种业务结论，不能吞）；
- **写快照失败只记警告**：叠加层是「收获」，不该反过来把刮削判成失败。

快照字段是这次刮削到的值（标题/原名/年份/简介/评分/流派/providerIds…）+ `scrapedAt`，
字段名与 API 的 camelCase 一致，方便以后做「导入 / 搬走这一库的元数据」。

### 写入前的闸（现在是给以后留的）

`overlay.AssertWritable(ctx, target)` 是**唯一**一道「不许写进只读库」的检查：
拿绝对路径问一遍，落在任一「只读」库的根路径之下就报错。

今天还没有「写回媒体目录」的能力，所以它拦不到东西 —— 这正是要留着的理由：
等写回做出来时，正确的做法是「所有写操作先过这道闸」，而不是在每个写点各写一遍判断。
单测已经把「库根目录本身、子路径、带 `..` 的路径都拒绝；同前缀的兄弟目录放行」钉住了。

## 三、界面

- 库列表每行：`只读` 徽章 + 「设为只读 / 改为可写」按钮（带 tooltip 说明它干什么）。
- 库详情卡：只读时显示一条说明 + **叠加层占用**（`GET /libraries/{id}` 的 `overlay` 字段：
  `files` / `bytes`），文案说清「不会写进媒体目录」。

## 四、真跑验收

`scripts/dev/verify-library-readonly.sh`（在跑服务的机器上执行，**15 通过 / 0 失败**）：

1. `PATCH` 开关 → 库列表 / 库详情 / 根路径三处都如实反映，详情里带 `overlay` 统计字段；
2. 只读是**写入策略不是换存法**：条目总数不变；
3. 只读库取图 → `<数据目录>/overlay/<lib>/<item>/poster.jpg` 真的出现，
   `overlay.files` / `overlay.bytes` 跟着涨；
4. 关掉开关 → 库与根路径都回到 `false`；
5. 参数校验与鉴权：空请求体 400、改名到空串 400、未登录 401。

元数据快照那条路径是**单独手工验的**（要真刮一次，脚本里不写死）：
对一个只读库的集强制重刮，约 6 秒后
`/var/lib/lmby/overlay/1/110/meta.json` 出现，内容是这次刮到的简介 / 评分 / `tmdb` id。

单测：`internal/overlay/overlay_test.go`（7 个用例：只读才写、可写不写、kind 消毒、
快照、Stats、闸门、条目不存在）。实例数据（不是项目常量）：图片那份 12 KB；
`1 个文件 / 12015 字节`。

## 五、还没做的（说清楚，别让人以为有了）

1. **写回媒体目录**（把 overlay 里的东西「提升」到媒体目录）—— 仍然没做。
   现在只读库的刮削产物在数据目录里，可写库的图在 `images/remote/` 缓存里；
   两边的模型是同一个，写回能力到时候再加，且必须先过 `AssertWritable`。
2. **overlay 的容量上限 / 清理**：现在只统计、不清理（它算数据）。
   真要清理得给「按库预留多少」这类配置，属于以后的事。
3. **overlay 优先于缓存**：取图时仍从 `images` 那份出图（overlay 是副本）。
   以后做写回时，overlay 才需要成为「权威副本」。
