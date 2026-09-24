# LMBY HTTP API

LMBY 的**唯一出口就是 HTTP**（浏览器是唯一客户端、静态前端由同一个进程 `go:embed` 提供）。
这份文档是这套接口的完整参考：认证方式、统一约定、逐条端点的参数与响应、以及会用到的枚举值。

- 适用对象：想接自动化（bot / 脚本 / 自己的反代或客户端）、想读懂前端在调什么的人。
- 机器专用的用户管理接口另有一份面向使用者的说明：[`docs/BOT-API.md`](BOT-API.md)（本篇是它的完整版）。
- 接口版本以 URL 前缀 `v1` 表示；`/healthz` 与 `/s/{token}` 不带版本前缀。
- 定值以代码为准（`internal/api/server.go` 的路由表就是权威清单）。

---

## 1. 概览

### 1.1 地址与传输

| 项 | 约定 |
|---|---|
| Base URL | `http://<host>:8099`（端口由 `LMBY_LISTEN` 决定，默认 `:8099`） |
| 数据格式 | 请求体与响应体都是 JSON；响应 `Content-Type: application/json; charset=utf-8` |
| 请求体上限 | **1 MiB**（超过直接 400） |
| 未知字段 | **被拒绝**（`DisallowUnknownFields`）—— 拼错字段名会立刻 400，而不是被静默忽略 |
| 图片/分片/字幕 | 不是 JSON：直接返回二进制或文本，见 [§12](#12-播放vod) 与 [§14](#14-图片) |

### 1.2 时长：ticks

所有「时长 / 播放位置」字段都用 **.NET tick**：`1 tick = 100ns`，即 **1 秒 = 10,000,000 ticks**。
字段名一般以 `Ticks` 结尾（`durationTicks`、`positionTicks`、`runtimeTicks`），单位与数据库一致，不要当成毫秒。

### 1.3 时间格式

- 接口自己拼出来的时间戳用 RFC3339：`2006-01-02T15:04:05Z07:00`（如 `2026-09-23T13:45:00+08:00`）。
- 直接由 Go 结构体 `time.Time` 序列化的字段是 RFC3339Nano（可能带小数秒）。
- 两者都能被 `Date.parse` 解析，客户端按标准 RFC3339 处理即可。

### 1.4 分页

带 `limit` / `offset` 的列表接口响应统一是：

```json
{ "items": [ ... ], "total": 1234, "limit": 60, "offset": 0 }
```

`total` 是**符合当前筛选条件的总数**，与 `items.length` 无关（它是这一页）。默认与上限各接口不同，见各自的说明。

---

## 2. 认证

有三种「访问级别」，每个端点都会标注：

| 标注 | 含义 |
|---|---|
| **公开** | 不需要登录（`/healthz`、`/api/v1/meta`、初始化、登录、`/s/{token}` 外链） |
| **登录** | 需要一个有效会话，或（仅用户管理接口）一把管理密钥 |
| **管理员** | 在「登录」之上还要求 `isAdmin = true` |

### 2.1 会话 Cookie（人用）

登录成功后服务端下发 `Set-Cookie: lmby_session=<token>`：

- `HttpOnly`（前端 JS 读不到）、`SameSite=Lax`（挡掉绝大多数 CSRF）、
  `Secure` 由 `LMBY_SECURE_COOKIES` 控制（走 HTTPS 时置 `true`）；
- 同源请求自动带上它。浏览器用 `fetch` 时请带 `credentials: 'same-origin'`；
- 令牌在库里只存哈希，服务端每 5 分钟才刷新一次活跃时间；
- **改口令 / 被禁用 / 收紧可见库 / 被管理员重置口令都会立刻吊销相关会话**（有意为之）。

会话有效期由 `LMBY_SESSION_TTL_HOURS` 决定。

### 2.2 管理密钥（机器用）

给 bot / 脚本的**长期凭据**，有两种等价的写法：

```
Authorization: Bearer lmby_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
X-API-Key: lmby_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

- 明文**只在生成时显示一次**，库里只存 SHA-256；忘了就轮换（旧密钥立刻失效）。
- **最小权限是刻意的**：这把钥匙只在下面这 7 个路径上有效，其余**一律 403**：

  | 方法 | 路径 |
  |---|---|
  | GET / POST | `/api/v1/users` |
  | GET / PATCH / DELETE | `/api/v1/users/{id}` |
  | PUT | `/api/v1/users/{id}/libraries` |
  | POST | `/api/v1/users/{id}/password` |

- 密钥做的每次操作都进审计，操作者记为 `api-token`。
- 生成 / 查看 / 撤销见 [§15.4](#154-管理密钥给-bot--脚本)。

### 2.3 权限模型速览

- **库可见性**：用户可以「不限制」（看全部库）或按白名单看部分库（`restrictedLibraries`）。
  每个请求都会算出可见库集合并注入所有查询 —— 列表、总数、搜索、分面、图片、播放都受它约束。
- **看不到的库 = 不存在**：条目级接口在库不可见时返回 **404**（不区分「不存在」与「不给你看」，以免用状态码探测别人的库）。
- **`allowTranscode`**：关掉后只能直出 / 转封装，不能转码。
- **`allowLiveTV`**：关掉后直播列表与起播都回 403（前台也会把入口藏起来）。
- **`maxConcurrentStreams`**：该账号同时播放的路数上限。

---

## 3. 统一错误格式

所有失败响应都是同一个形状：

```json
{ "error": "人类可读的中文说明", "code": "可翻译的稳定标识（部分错误才有）" }
```

- `error`：给人看的中文兜底文案。
- `code`：可选。只给「用户会看懂并需要行动」的错误配，供界面查 i18n 词条（如 `livetv_source_timeout`）。

常用状态码：

| 状态 | 什么时候 |
|---|---|
| 400 | 参数不合法（字段名拼错、取值越界、缺必填） |
| 401 | 未登录 / 会话失效 / 用户名或口令错误 / 管理密钥不对 |
| 403 | 权限不足（非管理员、账号被限制、密钥越权） |
| 404 | 不存在，或**该库对你看不见**（等效于不存在） |
| 409 | 冲突（重复初始化、扫描已在跑、未配置元数据源、最后一个管理员不能删/降级） |
| 429 | 登录尝试过于频繁（带 `Retry-After`，单位秒） |
| 502 / 503 | 上游依赖问题（源站、ffmpeg 未就绪、未装载签名密钥、数据库不可用） |

---

## 4. 元信息与健康

### `GET /healthz` — **公开**

服务自身与依赖的健康状态。数据库不可用返回 **503**；只是缺 ffmpeg 则返回 **200 + `status: "degraded"`**。

```json
{
  "status": "ok",
  "version": "v0.9.0",
  "uptimeSeconds": 3600,
  "database": { "ok": true, "latencyMs": 1 },
  "ffmpeg": { "path": "/usr/bin/ffmpeg", "available": true, "version": "6.1.1", "hw_accels": ["vaapi"] }
}
```

### `GET /api/v1/meta` — **公开**

前端启动所需的一点点公开信息（登录页也要用，所以不需要认证）。

```json
{ "version": "v0.9.0", "versionFull": "v0.9.0 (commit abc1234, built ...)", "setupRequired": false, "ffmpeg": true }
```

`setupRequired` 为 `true` 表示**还没有任何账号**，应引导用户走初始化（[§5.1](#51-初始化)）。

---

## 5. 账号与会话

### 5.1 初始化

#### `POST /api/v1/setup` — **公开**

创建初始管理员并**直接登录**（响应会带 `Set-Cookie`）。系统已有账号时返回 **409**。

```json
{ "username": "admin", "password": "至少8位", "displayName": "管理员" }
```

响应 **201**：`{ "user": User, "preferences": Preferences }`。

用户名规则：`^[A-Za-z0-9][A-Za-z0-9._-]{2,31}$`（3~32 位）。口令至少 8 位。

### 5.2 登录 / 登出

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| POST | `/api/v1/auth/login` | 公开 | `{username, password}` → **200** `{user, preferences}` + `Set-Cookie`；失败 **401**；限流 **429** |
| POST | `/api/v1/auth/logout` | 登录 | 撤销当前会话并清 Cookie → `{ "ok": true }` |

登录失败按 `用户名 + IP` 限流（默认 15 分钟内 8 次失败），超限回 **429** 并带 `Retry-After`。
用户名不存在时也做一次等价哈希运算（抹平响应时间差异），所以**无法**用耗时区分账号是否存在。

### 5.3 个人资料与偏好

| 方法 | 路径 | 权限 | 请求体 | 响应 |
|---|---|---|---|---|
| GET | `/api/v1/auth/me` | 登录 | — | `{user, preferences}` |
| PATCH | `/api/v1/auth/me` | 登录 | `{displayName}` | `{user, preferences}` |
| PATCH | `/api/v1/auth/me/preferences` | 登录 | `{theme, language, subtitlePrefs, audioPrefs, libraryViews}` | `{user, preferences}` |
| POST | `/api/v1/auth/password` | 登录 | `{currentPassword, newPassword}` | `{ "ok": true, "revokedSessions": 3 }` |

- `theme`：`light` / `dark` / `system`。
- **偏好是整份覆盖**（不是局部 PATCH）：没给的字段会回落到默认值
  （`theme` → `system`、`language` → `zh-CN`、三个 map → `{}`）。请**带上当前完整偏好**再改，
  否则会把其它项清掉。相比之下 `PATCH /auth/me` 的 `displayName` 是普通可选字段（不传就不改）。
- 改口令会**吊销其它所有会话**（当前这台除外），`revokedSessions` 是被踢掉的台数；
  新口令不能与当前相同，也要满足最小长度。

### 5.4 我的设备（会话）

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/v1/auth/sessions` | 登录 | 列出自己所有有效会话 → `{ "sessions": [SessionInfo] }` |
| DELETE | `/api/v1/auth/sessions/{id}` | 登录 | 撤销指定会话 → `{ "ok": true }` |

`SessionInfo`：`{id, createdAt, lastSeenAt, expiresAt, userAgent, ip, current}`。
`current: true` 表示当前这一条；`id` 是会话标识（撤销时用，**不是** Cookie 值）。

---

## 6. 媒体库与扫描

### 6.1 媒体库

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/v1/libraries` | 登录 | 列出**当前用户可见**的库 → `{ "libraries": [LibrarySummary] }` |
| POST | `/api/v1/libraries` | 登录 | 建库 → **201** `{ "library": LibrarySummary, "counts": {} }` |
| GET | `/api/v1/libraries/{id}` | 登录 | 库详情（含最近扫描、问题、探测进度、叠加层占用） |
| PATCH | `/api/v1/libraries/{id}` | 登录 | 编辑（可一次改多项）→ `{ "ok": true }` |
| DELETE | `/api/v1/libraries/{id}` | 登录 | 删库 → `{ "ok": true }` |

`LibrarySummary`（`store.Library` + 统计）：

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` / `name` / `kind` | — | `kind ∈ movie \| tv \| homevideo \| mixed` |
| `paths` | `[{path, ...}]` | 根路径列表 |
| `counts` | `object` | 各类型条目数（`movie`/`series`/`season`/`episode`/`extra`） |
| `imageCount` | int | 图片数 |
| `scanRunning` | bool | 是否有扫描在跑 |
| `readonly` | bool | 只读库（网盘/只读挂载）：LMBY 不往库目录写，刮削产物落进 overlay |
| `scanIntervalMinutes` | int | 自动扫描间隔（分钟，**0 = 不自动**，上限 10080） |
| `lastScanAt` / `nextScanAt` | time? | **派生字段**（按间隔算出来），只为界面显示 |

**建库**请求体：`{ "name": "...", "kind": "movie", "paths": ["/abs/path"] }`。
根路径必须存在且是目录（建库时就检查，错误信息里会带不可访问的那一条）。

**编辑**请求体（所有字段可选，只改给了的）：

```json
{ "name": "...", "kind": "tv", "paths": ["/a", "/b"], "readonly": true, "scanIntervalMinutes": 60 }
```

- `paths` 是**整份替换**（不是增量）；想清空列表请删库。
- `scanIntervalMinutes` 越界（< 0 或 > 10080）回 400。

**库详情**（`GET /api/v1/libraries/{id}`）响应：

```json
{
  "library": LibrarySummary,
  "lastScan": ScanRun | null,
  "issues": [ScanIssue],
  "overlay": { "files": 120, "bytes": 3456789 },
  "progress": ScanProgress | null,
  "probe": { "pending": 3, "ok": 400, "failed": 2 }
}
```

`ScanRun`：`{id, state: running|done|failed|canceled, trigger, startedAt, finishedAt?, stats, error?}`。
`ScanIssue`：`{id, severity, path, message, at}`。

### 6.2 扫描

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| POST | `/api/v1/libraries/{id}/scan` | 登录 | 触发扫描 → **202** `{ "scanRunId": 7 }`；已在跑回 **409**（响应里也带 `scanRunId`） |
| DELETE | `/api/v1/libraries/{id}/scan` | 登录 | 取消扫描 → `{ "ok": true }`；没有在跑回 **409** |
| GET | `/api/v1/libraries/{id}/scan` | 登录 | 查询状态 → `{running, progress, lastScan, issues}` |

触发扫描可选请求体：`{ "refreshMetadata": true }` —— 文件没变也重读同目录的 nfo
（手改了 nfo 之后靠它生效，不必去 `touch` 媒体文件）。

`ScanProgress`（运行中才有）：`{phase, scanRunId, libraryId, videos, newFiles, changedFiles, movedFiles,
deletedFiles, unchanged, itemsNew, images, issues, currentPath, elapsedMs, at}`。

### 6.3 条目原始表

#### `GET /api/v1/libraries/{id}/items` — **登录**

「原始条目表」：平铺所有类型（含季与集），排查问题用。给人看的海报墙是
[`/browse`](#71-海报墙与子项)。

参数：`kind`（可选，`movie|series|season|episode|extra`）、
`matchState`（可选，**逗号分隔**多个，如 `review,failed`）、`limit`（默认 100）、`offset`。
响应：标准分页 `{items, total, limit, offset}`。

### 6.4 只读库的叠加层（overlay）

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| DELETE | `/api/v1/libraries/{id}/overlay` | 登录 | 清空该库的叠加层 → `{ "ok": true, "files": 120, "bytes": 3456789 }` |
| GET | `/api/v1/overlay` | 登录 | 各库叠加层占用汇总（设置页看板） |

`GET /api/v1/overlay` 响应：

```json
{
  "root": "/var/lib/lmby/overlay",
  "libraries": [ { "libraryId": 1, "name": "电影", "files": 120, "bytes": 3456789 } ],
  "total": { "files": 120, "bytes": 3456789 }
}
```

> 清空 overlay 只动数据目录里那一块，**媒体目录与数据库不动**。

---

## 7. 浏览与条目

### 7.1 海报墙与子项

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/v1/libraries/{id}/browse` | 登录 | 海报墙：只要**顶层**条目（电影、剧集），可排序分页 |
| GET | `/api/v1/items/{id}/children` | 登录 | 子项：剧集 → 季，季 → 集 |

`browse` 参数：`kind`（`movie` 或 `series`，留空=两者）、
`sort`（`title` / `year` / `added`，默认 `title`）、`limit`（默认 60，1~200）、`offset`。
响应：`{items, total, limit, offset}`。

`children` 响应：`{ "item": ItemBrief, "items": [Item], "counts": { "season": 2, "episode": 24 } }`
—— `counts` 是每个子项自己的子项数，界面用来显示「第 1 季 · 12 集」，省掉逐季再请求。

### 7.2 条目详情与编辑

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/v1/items/{id}` | 登录 | 详情（编辑界面的数据源） |
| PATCH | `/api/v1/items/{id}` | 登录 | 人工编辑字段 + 字段锁定 |
| POST | `/api/v1/items/{id}/scrape` | 登录 | 只给这一条排一次刮削 |
| GET | `/api/v1/items/{id}/people` | 登录 | 演职员 |
| GET | `/api/v1/items/{id}/related` | 登录 | 相关推荐（同库 + 同类型 + 共同流派，离线可算） |
| POST | `/api/v1/items/batch` | 登录 | 批量操作（多选重刮 / 标记无需匹配） |

**详情**响应 `ItemDetail`：

```json
{
  "item": FullItem,
  "fields": [ { "name": "title", "kind": "text", "locked": false } ],
  "scrapeConfigured": true
}
```

`fields` 是**可编辑字段的形态表**（`kind ∈ text|int|float|list|map|date`，`unit` 目前只有时长的 `minutes`），
界面据此生成表单。

`FullItem` 是完整的 [Item](#appendix-item) 外加 `sortTitle`、`providerIds`、`lockedFields`、`lastScrapedAt` 等。

**编辑**请求体：

```json
{ "fields": { "title": "新的标题", "genres": ["动画"], "year": null }, "lockedFields": ["title"] }
```

- `fields` 里**出现过的键**才会被写；值为 `null` 表示**清空**（人工编辑是显式表达，与自动刮削的「空值不覆盖」相反）。
- `lockedFields` **非 nil 时整体替换**锁定集合（`[]` = 全部解锁）；不传则不动。
- 至少要给 `fields` 或 `lockedFields` 之一，否则 400。
- `title` 不能改成空串。编辑 `review` / `failed` 状态的条目会顺手标成 `manual`。

**单条重刮**请求体：`{ "force": true }` → `{ "taskId": 12, "enqueued": true, "force": true }`。
`force` 为真时连已有元数据（含 nfo / manual）的条目也重刮。

**演职员**响应：`{ "itemId": 7, "people": [ItemPerson], "source": "nfo" }`。
`ItemPerson`：`{personId, name, role, character?, order, providerIds?}`。

**相关推荐**参数 `limit`（默认 12）→ `{ "items": [Item], "total": 12 }`。

**批量操作**请求体：

```json
{ "action": "scrape", "itemIds": [1,2,3], "force": false, "reason": "" }
```

- `action`：`scrape`（排刮削任务）或 `skip`（标记不需要匹配）。
- `scrape` 需要已配置元数据源，否则 **409**。
- 单次最多 **500** 条，超了 400。
- 响应：`{ "action": "scrape", "applied": 3, "skipped": 0 }`。

---

## 8. 搜索

搜索用中文二元组切词 + 单字兜底 + 错字容忍（调低 pg_trgm 词相似阈值）。
四个接口各管一件事，前端按需并发拿。

公共筛选参数：`q`、`libraryId`、`kind`、`genre`、`personId`。
**`q` 可以为空，但那时必须至少给一个别的筛选条件**，否则 400 ——
「返回全部条目」是列表接口的活，不该伪装成搜索结果。

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/v1/search` | 登录 | 结果列表（分页） |
| GET | `/api/v1/search/facets` | 登录 | 结果分面（类型 / 库 / 流派 / 人的命中数） |
| GET | `/api/v1/search/people` | 登录 | 「人」这一档的结果列表（分页，只认 `q`） |
| GET | `/api/v1/search/suggest` | 登录 | 即时联想（作品 + 人，每组几条，不翻页） |

**`/search`** 另有 `limit`（上限 100）、`offset`。响应：

```json
{
  "query": "进击",
  "filters": { "kind": "", "genre": "", "libraryId": null, "personId": null },
  "items": [SearchHit],
  "total": 12, "limit": 30, "offset": 0
}
```

**`/search/facets`**：筛选参数必须与 `/search` **完全一致**，否则分面数字与结果对不上。响应：

```json
{
  "query": "进击",
  "filters": { ... },
  "facets": {
    "kind": [ { "value": "series", "count": 12 } ],
    "library": [ { "value": "5", "count": 12 } ],
    "genre": [ { "value": "动画", "count": 12 } ],
    "people": 3,
    "total": 12
  }
}
```

两个自检不变量：`sum(kind) == total`、`sum(library) == total`（每条恰好一个类型、一个库）；
`sum(genre) >= total`（一条可以有多个流派）。`library` 分面的 `value` 是**库 id 的字符串**。

**`/search/people`** 参数 `q`（必填）、`limit`、`offset`。响应：
`{query, people: [PersonHit], total, limit, offset}`，`PersonHit`：`{id, name, roles, works, rank, similarity}`。

**`/search/suggest`** 参数 `q`（必填）、`limit`。响应：

```json
{ "query": "巨人", "items": [Suggestion], "people": [Suggestion] }
```

`Suggestion`：`{type: "item"|"person", id, title, kind?, year?, works?, roles?}`。

---

## 9. 收藏

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/v1/favorites` | 登录 | 我的收藏（分页，可按类型过滤） |
| GET | `/api/v1/items/{id}/favorite` | 登录 | 单条收藏状态 |
| POST | `/api/v1/items/{id}/favorite` | 登录 | 收藏 / 取消收藏（幂等） |

- `GET /favorites` 参数：`kind`、`limit`、`offset` → 分页 `{items, total, limit, offset}`。
- 单条状态 `FavoriteState`：`{itemId, favorite, count}` —— `favorite` 是**我**有没有收藏，
  `count` 是**全站**收藏数。
- 收藏请求体：`{ "favorite": true }` → 响应为 `FavoriteState & {ok: true}`（带回新状态与新的总数，界面不用再查）。

---

## 10. 播放列表 / 合集

`kind` 有两种：`playlist`（私人，只有自己和能看到的人）与 `collection`（合集，所有人可见）。
新建 `collection` 需要管理员。

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/v1/playlists` | 登录 | 我的列表 + 所有合集（我的在前）→ `{ "playlists": [PlaylistSummary] }` |
| POST | `/api/v1/playlists` | 登录 | 新建 → `{ "playlist": PlaylistSummary }` |
| GET | `/api/v1/playlists/{id}` | 登录 | 单个 → `{ "playlist": PlaylistSummary }` |
| PATCH | `/api/v1/playlists/{id}` | 登录 | 改名 / 改说明 → `{ "playlist": PlaylistSummary }` |
| DELETE | `/api/v1/playlists/{id}` | 登录 | 删除 → `{ "ok": true }` |
| GET | `/api/v1/playlists/{id}/items` | 登录 | 列表内容（分页） |
| POST | `/api/v1/playlists/{id}/items` | 登录 | 批量加入（追加，幂等） |
| PUT | `/api/v1/playlists/{id}/items` | 登录 | 按给定顺序整体重排 |
| DELETE | `/api/v1/playlists/{id}/items/{itemId}` | 登录 | 移除一项 |
| GET | `/api/v1/playlists/{id}/neighbors` | 登录 | 某条目的前后邻居与位次（播放器「下一项」） |

- 新建：`{ "name": "...", "kind": "collection", "overview": "..." }`。
- 改名：`{ "name"?, "overview"? }` —— **没给的字段不会被改**（给空串是「清空说明」）。
- 加入：`{ "itemIds": [1,2,3] }` → `{ "ok": true, "added": 3, "itemCount": 30 }`（重复加入不报错也不多一条）。
- 重排：`{ "itemIds": [...] }`（整串写一遍）→ `{ "ok": true }`。
- 邻居：参数 `itemId` → `{playlistId, playlistName, playlistKind, itemId, index, total, prevId, nextId}`，
  `index` 从 1 开始（0 = 这个条目不在列表里）。

---

## 11. 首页与播放进度

### 11.1 首页

#### `GET /api/v1/home` — **登录**

一次取全：轮播 + 继续观看 + 推荐行（为什么不拆成多个请求：首屏感觉直接由请求数决定，
且几行之间有先后关系）。响应 `HomePayload`：

```json
{ "hero": [Item], "continue": [ContinueWatchingEntry], "sections": [HomeSection] }
```

`hero` 是大屏轮播的几条（宽幅背景用）。`HomeSection`：
`{key, title, subtitle?, taste?, sourceWorks?, seedTitle?, items}` —— `taste` / `sourceWorks` / `seedTitle`
只有「为你推荐」那一行才有（口味画像与它的依据）。本行的键 `key` 决定它是什么类型的推荐行。

### 11.2 继续观看与进度

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/v1/continue` | 登录 | 继续观看（参数 `limit`，默认 20）→ `{ "items": [ContinueWatchingEntry] }` |
| GET | `/api/v1/items/{id}/progress` | 登录 | 该条目进度 → `{ "progress": PlaybackProgress \| null }` |
| POST | `/api/v1/items/{id}/played` | 登录 | 标记单条已看 / 未看 |
| POST | `/api/v1/items/played` | 登录 | 批量标记（最多 500 条） |
| GET | `/api/v1/items/{id}/playlist` | 登录 | 条目下的文件与流（播放器的音轨/字幕选择器用） |

- `ContinueWatchingEntry`：`{item, progress, remainingTicks}`。
- `PlaybackProgress`：`{itemId, positionTicks, durationTicks, played, playCount}`。
- 标记已看请求体：`{ "played": true }`（单条接口用路径里的 id，会覆盖请求体）或批量 `{ "itemIds": [1,2], "played": true }`。
  响应：`{ "ok": true, "count": 2, "played": true }`。
- `GET /items/{id}/playlist` 响应：`{ "itemId": 7, "files": [PlaylistFile] }`，
  `PlaylistFile` 含 `video` / `audio` / `subtitles` 三个流数组（每个流有 `index`、`codec`、`title`、`language`、`default` 等）。

---

## 12. 播放（VOD）

播放分三种模式，由服务端**决策引擎**决定（能直出就直出，否则只换容器，都不行才转码）：

| 模式 | 含义 | 客户端用什么 URL |
|---|---|---|
| `direct` | 直出原文件（HTTP Range / ETag / 断点续传） | `directUrl` |
| `remux` | 只换容器（转封装 HLS fMP4，视频不重编码） | `hlsUrl` |
| `transcode` | 转码成 H.264 | `hlsUrl` |

### 12.1 起播

#### `POST /api/v1/items/{id}/play` — **登录**

请求体（全部可选）：

```json
{
  "profile": { "videoCodecs": ["h264"], "audioCodecs": ["aac"], "containers": ["mp4"],
               "maxHeight": 1080, "maxBitDepth": 8, "maxAudioChannels": 6,
               "supportsHls": true, "supportsFmp4": true, "supportsTs": false },
  "fileId": 12,
  "videoStreamIndex": 0,
  "audioStreamIndex": 1,
  "subtitleStreamIndex": 0,
  "startPositionTicks": 0,
  "restart": false,
  "maxHeight": 1080,
  "burnSubtitle": false
}
```

- `profile`：客户端**实测**出来的解码能力（浏览器用 `MediaSource.isTypeSupported` 测）；
  不传时服务端用保守默认档兜底。
- `fileId`：多版本时指定用哪个文件；省略由决策引擎挑（优先能直出的那个）。
- `subtitleStreamIndex`：`-1` = 明确不要字幕，`0/缺省` = 自动（只选强制字幕轨）。
- `maxHeight`：画质档（输出高度上限）。省略=自动（按配置的转码上限），`0`=原生分辨率，`>0`=该高度。
- `burnSubtitle`：把选中的[图形字幕](#appendix-subtitle)烧进画面（会强制转码，即使本来能直出），界面会写明这个代价。
- `restart`：从头开始。

响应 `PlaybackState`（**200**；起播失败见下）：

```json
{
  "playSessionId": "ps_xxx",
  "mode": "remux",
  "playable": true,
  "state": "ready",
  "reasons": ["HEVC 无法被该客户端解码，改为转码"],
  "plan": PlaybackPlan,
  "startSeconds": 120.5,
  "durationSeconds": 1440.0,
  "directUrl": "/api/v1/play/ps_xxx/stream",
  "hlsUrl": "/api/v1/play/ps_xxx/index.m3u8",
  "subtitleUrl": "/api/v1/play/ps_xxx/subtitles/sub_0.vtt",
  "subtitleFormat": "vtt",
  "subtitleState": "ready",
  "windowEndSeconds": 420.0,
  "itemId": 7,
  "title": "某片",
  "progress": PlaybackProgress,
  "playbackSeconds": 120.5
}
```

字段说明：

| 字段 | 说明 |
|---|---|
| `mode` | `direct` / `remux` / `transcode` |
| `playable` | 能不能播；`false` 时看 `reasons`（界面展示「为什么这么播/为什么放不了」，而不是黑屏） |
| `state` | `direct` / `starting` / `ready` / `finished` / `error` / `stopped` |
| `plan` | 决策明细：整片 + 视频/音频/字幕三条流各自的处理（`action`、`reason`、转码目标等） |
| `subtitleFormat` | `vtt`（浏览器原生轨道）或 `ass`（前端 libass 渲染，保留特效） |
| `subtitleState` | `ready`（字幕已可挂）/ `preparing`（内嵌字幕还在后台抽取） |
| `windowEndSeconds` | 转封装/转码模式：这一段预生成窗口的结束位置（秒）；0 = 直出或未知 |

**状态码**（起播**不**用状态码表达「能不能播」）：

| 状态 | 什么时候 |
|---|---|
| 200 | 正常。`playable:false` 也在 200 里 —— 这时看 `reasons`（以及 `state: "error"`） |
| 403 | 需要转码但该账号 `allowTranscode=false`；或文件路径不在媒体库范围内 |
| 404 | 条目不存在 / 指定的 `fileId` 不在该条目下 |
| 409 | 同时播放路数已达 `maxConcurrentStreams` |

转码/转封装起不来（超时等）时 `playable` 会转成 `false`，前端据此展示失败原因与「重试」。直播起播才用 `code`（见 [§13.4](#134-直播播放)）。

### 12.2 播放会话

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/v1/play/{sid}` | 登录 | 轮询会话状态（同 `PlaybackState`） |
| POST | `/api/v1/play/{sid}/seek` | 登录 | 切换位置：`{ "positionTicks": 600000000 }` → 新的 `PlaybackState` |
| POST | `/api/v1/play/{sid}/progress` | 登录 | 上报进度：`{ "positionTicks": ..., "durationTicks": ... }` → `{progress, played}` |
| POST | `/api/v1/play/{sid}/stop` | 登录 | 关闭播放 → `{ "ok": true }` |

- 拖动（seek）：`direct` 模式浏览器自己发 Range 请求，服务端几乎不用做事；
  转封装/转码模式会按新位置重开一段窗口（旧的立刻回收）。
- 进度上报是最准的「客户端看到哪了」：转码/转封装的**节流**用它判断要不要暂停生成。
  看到片尾（≥ 92%）会自动标记已看，并清掉续播位置。
- `/stop` 会**立刻回收** ffmpeg 与分片；浏览器关闭页面时前端用 `sendBeacon` 调它（实测 1 秒内回收）。

### 12.3 媒体分发（不是 JSON）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/play/{sid}/stream` | 直出原文件。支持 HTTP Range / ETag / If-Range / HEAD（标准库 `ServeContent` 处理） |
| GET | `/api/v1/play/{sid}/index.m3u8` | HLS 播放列表（`application/vnd.apple.mpegurl`） |
| GET | `/api/v1/play/{sid}/{name}` | HLS 分片（`init.mp4` / `seg_00000.m4s`） |
| GET | `/api/v1/play/{sid}/subtitles/{name}` | 字幕：`.vtt` → `text/vtt`，`.ass`/`.ssa` → `text/x-ssa`；还没抽好回 **202** `{status:"preparing", message}`（带 `Retry-After: 3`），抽取失败回 **502** |
| GET | `/api/v1/play/{sid}/fonts` | 内封字体（mkv 附件）列表 |
| GET | `/api/v1/play/{sid}/fonts/{n}` | 按序号取内封字体字节 |

> **分片路径必须与播放列表同级**：m3u8 里写的是相对文件名（`init.mp4` / `seg_00000.m4s`），
> 任何标准 HLS 客户端都会拿**播放列表的 URL** 作基准去拼 —— 放到 `/seg/` 子路径下会全部 404。

### 12.4 前端 libass 兜底字体

#### `GET /api/v1/fonts/{name}` — **登录**

提供 `<数据目录>/fonts/` 下的字体文件。必须有：libass(WASM) 只认自己虚拟文件系统里的字体，
看不到客户端系统字体；`subtitles-octopus` 默认的 `default.woff2` 在 npm 包里并不存在，
少了它整个渲染器会起不来。字体不入库，部署时由安装脚本从系统字体复制。

### 12.5 会话监控（管理员）

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/v1/playback/sessions` | 登录 | 活跃播放/转码会话。普通用户只看得到自己的；管理员看全部 |
| POST | `/api/v1/playback/streams/{key}/stop` | 管理员 | 掐掉某一路转封装/转码进程 → `{ "ok": true }` |

`GET /playback/sessions` 响应：`{ "sessions": [PlaySessionInfo], "transcodeSessions": [TranscodeSessionStat] }`。

- `PlaySessionInfo`：`{playSessionId, itemId, title, userId, mode, startSeconds, durationSeconds, ageSeconds, idleSeconds, file, stream?}`。
- `TranscodeSessionStat`：`{key, state, startSeconds, uptimeSec, idleSec, segments, generatedSeconds,
  clientSeconds, aheadSeconds, throttled, fps?, speed?, bitrate?, mediaTime?, error?, log?}`
  —— `state ∈ starting|ready|finished|error`，`aheadSeconds` 是「已生成 − 客户端已下载」，
  超过阈值会 `throttled`（暂停 ffmpeg）。

### 12.6 转码能力

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/v1/transcode/capabilities` | 登录 | 本机能力表（**真跑探测**过，带磁盘缓存） |
| POST | `/api/v1/transcode/capabilities/refresh` | 管理员 | 重新探测 |

响应：`{ "capabilities": TranscodeCapabilities, "best": TranscodeBackend | null }`。

> `encode` / `decode` / `quality` 都是**真跑过**的结论：拿 1 秒小样真编一遍，
> 只有跑通的才是 `true`。`ffmpeg -encoders` 列出某个编码器**不代表**这台机器能用它。
> `best` 是当前会用的后端（都不可用时为 `null`）。

---

## 13. 直播电视

### 13.1 频道

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/v1/livetv/channels` | 登录 | 频道列表（筛选全在服务端做） |
| GET | `/api/v1/livetv/channels/{id}` | 登录 | 单个频道 |
| PATCH | `/api/v1/livetv/channels/{id}` | 登录 | 改名 / 分组 / logo / 排序 / 启停 |
| POST | `/api/v1/livetv/channels/{id}/favorite` | 登录 | 切换收藏 → `{ok, favorite}` |
| GET | `/api/v1/livetv/export.m3u` | 登录 | 导出为 m3u（直接当下载用） |

`GET /channels` 参数：`q`（名字）、`group`、`enabled=1`（只看启用）、`favorites=1`（只看收藏）、
`probe`（`pending|ok|failed`，按探测结果筛）、`hide_failed=1`（前台用：探测过且不通的不出现）。
响应：`{ "channels": [TVChannel], "groups": [TVGroup], "total": N, "enabled": M }`。

`TVChannel`：

| 字段 | 说明 |
|---|---|
| `id` / `name` / `group` / `logo` / `tvgId` | 基本信息 |
| `url` | **频道自己的地址**（可复制给外部播放器） |
| `kind` | 频道类型 |
| `hasHeaders` | 源站是否要求自定义请求头（**内容不回显**，只说有没有） |
| `sortOrder` / `disabled` / `favorite` | 排序 / 启用 / 是否我收藏 |
| `probe` / `probeOk` / `probeAt` | 最近一次探测：`probe` 是人话摘要，`probeOk` 缺省=还没探过（三态） |

PATCH 请求体：`{ "name"?, "group"?, "logo"?, "sortOrder"?, "disabled"? }` → 返回更新后的 `TVChannel`。

导出参数：`all=1` 时含停用的频道；响应 `Content-Type: audio/x-mpegurl`，带 `Content-Disposition`。

### 13.2 直播源

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/v1/livetv/sources` | 登录 | 源列表（带每个源当前挂的频道数） |
| POST | `/api/v1/livetv/sources` | **管理员** | 新建源（粘贴 / 上传文件 / 订阅 URL） |
| PATCH | `/api/v1/livetv/sources/{id}` | **管理员** | 改名 / 改 URL / 启停 / 改刷新间隔 |
| DELETE | `/api/v1/livetv/sources/{id}` | **管理员** | 删除源 |
| POST | `/api/v1/livetv/sources/{id}/refresh` | **管理员** | 重新拉取订阅源并导入 |

新建源请求体：

```json
{ "name": "我的源", "kind": "paste", "url": "", "content": "#EXTM3U\n...",
  "enabled": true, "refreshIntervalMinutes": 0 }
```

- `kind`：`paste` / `file` / `url`。`url` 必须给地址；`paste`/`file` 必须给 `content`。
- 先拉取/解析成功**才**建源（内容坏了不会在列表里留一个没频道的空壳）。
- 响应 **201**：`{ "source": TVSource, "import": TVImport, "total": N, "enabled": M }`。
- `TVImport`：`{added, updated, kept, removed, total}`；`TVSource` 含
  `{id, name, kind, url?, enabled, refreshIntervalMinutes, lastRefreshAt, lastStatus, lastChannelCount, channelCount}`。

`refreshIntervalMinutes` 为 0 表示不自动刷新（间隔属于**源**，不是全局调度器）。

### 13.3 频道探测

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/v1/livetv/channels/probe` | 登录 | 探测状态（这一次的进度 + 库里的总账） |
| POST | `/api/v1/livetv/channels/probe` | **管理员** | 起一次探测（后台跑，**立刻回 202**） |

- 探测会**真连源站**，可能几分钟，所以不同步等结果。
- 起探测请求体（都可选）：`{ "onlyUnknown": true, "group": "央视", "channelIds": [1,2] }`；
  都不给就是全探启用中的频道。响应 `{ "running": true }`。
- 已有一次在跑时回 **409**；ffprobe 不可用时回 **503**（避免起一趟「全都失败」的探测把频道全标成失效）。
- 状态响应 `TVProbeStatus`：`{running, startedAt?, elapsedSeconds, selection, progress, stats}`。
  `progress` 是**这一次**的（`{total, done, ok, failed, current, canceled}`），
  `stats` 是**库里的总账**（`{total, pending, ok, failed}`）—— 刷新页面、重启进程都不会把进度归零。

### 13.4 直播播放

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| POST | `/api/v1/livetv/channels/{id}/play` | 登录 | 起播频道（同一频道所有观众**共用一路 ffmpeg**） |
| POST | `/api/v1/livetv/channels/{id}/share` | 登录 | 生成外链（见 §13.5） |
| GET | `/api/v1/livetv/sessions` | 登录 | 正在跑的直播会话（监控用） |
| GET | `/api/v1/live/{sid}/index.m3u8` | 登录 | 直播播放列表 |
| GET | `/api/v1/live/{sid}/{name}` | 登录 | 直播分片 |
| POST | `/api/v1/live/{sid}/stop` | 登录 | 停止观看 → `{ "ok": true, "viewers": 1 }` |

起播请求体（可选）：`{ "codecs": ["h264","aac"], "force": "transcode" }`。

- `codecs`：浏览器声明的可解码编码（服务端据此判断「转封装够不够」；HEVC/MPEG-2 这类它解不开的直接转码）。
- `force`：`"transcode"` = 刚才放不出来，转码重试；`"copy"` = 明确要求别转。

响应 `LivePlayback`：

```json
{
  "sid": "lp_xxx", "channelId": 29, "name": "CCTV-1", "kind": "tv",
  "playlistUrl": "/api/v1/live/lp_xxx/index.m3u8",
  "startupMs": 750, "segmentSeconds": 6, "listSize": 0,
  "viewers": 2, "mode": "copy", "videoCodec": "h264"
}
```

- `sid` 只是**我这次观看的票**，不是新起的一路流（同一频道共享）。
- 起播失败**不是**给一串实现细节，而是一句人话 + 稳定 `code`：

  | `code` | 状态 | 含义 |
  |---|---|---|
  | `livetv_busy` | 503 | 本地并发打满（与源站无关，停掉一路即可） |
  | `livetv_source_timeout` | 503 | 源站没及时返回画面（可能已失效；后台已自动重探一次） |
  | `livetv_failed` | 502 | 其它拉流失败 |

- `/live/{sid}/index.m3u8` 同样遵守「分片与播放列表同级」的规则。
- 会话被回收后（比如标签页切回来）重新拉播放列表会**就地重拉一路**，而不是回 410。

`GET /livetv/sessions` → `{ "sessions": [LiveSessionInfo], "count": N }`，
`LiveSessionInfo`：`{key, channelId, name, state, viewers, uptimeSec, idleSec, segments, fps, speed, bitrate, error?}`。

### 13.5 外链出口（**公开**）

给 VLC / 手机播放器等不带 Cookie 的客户端用，靠**签名 token** 鉴权：

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/s/{token}/playlist.m3u` | 公开 | 签名播放列表 |
| GET | `/s/{token}/{name}` | 公开 | 签名分片 |

生成外链：`POST /api/v1/livetv/channels/{id}/share`，请求体 `{ "hours": 24 }`（1~168，默认 24）。

```json
{
  "path": "/s/<token>/playlist.m3u",
  "url": "http://<host>:8099/s/<token>/playlist.m3u",
  "expiresAt": "2026-09-24T13:45:00+08:00",
  "channelId": 29, "name": "CCTV-1", "segmentSec": 6
}
```

token 过期或非法回 **401**；未装载签名密钥时 **503**。

---

## 14. 图片

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/v1/items/{id}/images` | 登录 | 列出该条目现有的图片（**不回源**，只说本地有什么） |
| GET | `/api/v1/items/{id}/images/{kind}` | 登录 | 取某类图片，按需缩放 |

- `kind`：`poster` / `fanart` / `backdrop` / `banner` / `logo` / `disc` / `thumb` / `art`。
  同一来源下按标准文件名优先挑（如 `poster.jpg` 优先于 `folder.jpg`）。
- 取图顺序：媒体目录里的本地图 > 用户手选 > provider 下载缓存；都没有才回源一次。
- 本地图能直接用就直接送原文件（零拷贝），需要缩放/转格式才落缓存。
- 取图参数：`w` / `h`（最大尺寸，**等比缩放进这个框，不放大**，上限 4096）、
  `format`（`jpeg` / `png`）、`q`（jpeg 质量，上限 100）。
- 响应是图片二进制，带 `ETag` 与 `Cache-Control: private, max-age=3600`，
  并回 `X-Image-Width` / `X-Image-Height`；`If-None-Match` 命中回 **304**。
  这个条目没有这类图片回 **404**。
- 列表响应：`{ "items": [ImageInfo] }`。

---

## 15. 设置（管理员）

### 15.1 读取设置

#### `GET /api/v1/settings` — **管理员**

```json
{
  "tmdb": {
    "configured": true, "hasReadToken": true, "hasApiKey": false,
    "language": "zh-CN", "fromDb": true, "encrypted": true,
    "fallback": { "hasReadToken": false, "hasApiKey": false, "language": "zh-CN" }
  },
  "system": {
    "databaseEncoding": "UTF8", "databaseCollate": "C.UTF-8", "databaseCtype": "C.UTF-8",
    "schemaVersion": 18, "itemCount": 30430, "fileCount": 27683, "imageCount": 1200,
    "taskPending": 0, "serverTime": "2026-09-23T13:45:00+08:00"
  }
}
```

密钥类字段**永不回显**，只回 `has*` 布尔；`fromDb` 说明值来自数据库还是配置文件兜底。

### 15.2 TMDB 凭据

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| PUT | `/api/v1/settings/tmdb` | 管理员 | 保存凭据（写库 + 立刻生效，不必重启） |
| DELETE | `/api/v1/settings/tmdb` | 管理员 | 重置为配置文件兜底值 |
| POST | `/api/v1/provider/test` | 管理员 | 用当前凭据**真打一次** provider 请求 |

PUT 请求体（三个字段都是**指针语义**：`nil` = 不改，指向空串 = 清掉）：

```json
{ "readToken": "eyJ...", "apiKey": "...", "language": "zh-CN" }
```

响应：`{ "ok": true, "tmdb": TMDBSettings, "clearedCache": 42 }` —— 换了语言会顺手清掉旧语言的缓存。

`POST /provider/test` 可选参数 `?q=测试词` → `{ok, query, count?, samples?, elapsed?, error?}`。
值得做：TMDB 只在**第一次真请求**时才暴露问题（Key 写错、被停用、网络不通、语言码非法）。

### 15.3 日志与审计

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/v1/logs` | 管理员 | **最近日志**（内存环形缓冲，2000 条） |
| GET | `/api/v1/audit` | 管理员 | **审计日志**（持久）：谁在什么时候做了什么 |

- `GET /logs` 参数：`limit`、`level`（最低级别 `DEBUG|INFO|WARN|ERROR`）、`q`（关键字）。
  响应 `LogsPayload`：`{entries, total, capacity, dropped}`。`entries` 时间倒序，
  `LogEntry`：`{time, level, msg, attrs?}`。
- `GET /audit` 参数：`limit`、`offset`、`action`、`q`、`failed=1`（只看失败的）。
  响应 `AuditPayload`：`{entries, total, limit, offset}`，`AuditEntry`：
  `{id, at, actorId?, actorName, action, target, result, detail?, ip}`。

  常见 `action`：`auth.login`、`auth.password_change`、`user.create`、`user.update`、`user.delete`、
  `user.libraries`、`user.password_reset`、`library.create`、`library.update`、`library.delete`、
  `library.scan`、`settings.update`、`settings.bot_key_created`、`settings.bot_key_revoked`、
  `maintenance.images_cleared`、`maintenance.overlay_orphans_purged`。
  `result ∈ ok | failed`；`target` 形如 `user:3` / `library:1`。

> 两条硬规矩：审计写失败**只 Warn、不拖累主操作**；**口令 / token / 密钥不落审计**。

### 15.4 管理密钥（给 bot / 脚本）

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/v1/settings/bot-key` | 管理员 | 当前密钥元信息 → `{configured, prefix?, createdAt?}` |
| POST | `/api/v1/settings/bot-key` | 管理员 | 生成 / 轮换 → `{key, prefix, note}`（`key` 是**明文，只出现这一次**） |
| DELETE | `/api/v1/settings/bot-key` | 管理员 | 撤销 → `{ "revoked": true }` |

用法与最小权限白名单见 [§2.2](#22-管理密钥机器用)，面向使用者的说明见 [`docs/BOT-API.md`](BOT-API.md)。

### 15.5 检查更新

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/v1/update` | 管理员 | 查有没有新版本（默认查本项目 GitHub 的 latest release）；`?refresh=1` 表示“用户点了按钮”，但服务端仍按最小间隔（10 分钟）回缓存 |

**由服务端去查**，不是浏览器直接打 GitHub —— 看片的人浏览器在什么网络环境（内网 / 没外网）
不该决定服务端能不能检查更新；而且服务端查得出原因、结果能缓存（保护上游的
匿名限流：GitHub 是 60 次/小时/IP）。默认**不自动查**，只有点按钮才出网。

响应（HTTP **200**）：

```json
{
  "enabled": true,
  "ok": true,
  "state": "update-available",
  "current": "v1.0.0",
  "commit": "288f486",
  "built": "2026-09-23T...",
  "latest": "v1.0.1",
  "publishedAt": "2026-09-24T00:00:00Z",
  "releaseUrl": "https://github.com/.../releases/tag/v1.0.1",
  "notes": "发布说明（压成一行，最多 600 字）",
  "assets": ["lmby-linux-amd64", "SHA256SUMS.txt"],
  "checkedAt": "2026-09-24T01:00:00Z",
  "cached": false
}
```

- `state` 是给界面用的结论：`up-to-date` / `update-available` / `dev`（当前是
  `dev-xxxx` 这类开发构建，不参与版本比较）/ `unknown`。
- **`ok:false` + `error:"人话原因"` 也是 200**：请求本身是合法的，只是没查成
  （没配更新源、服务器没有外网出口、上游超时 / 限流、上游返回的不是 release JSON）。
  界面要把原因显示给管理员看，所以不走统一的 `{error, code}` 错误格式（那套是给
  「请求非法」用的：400 / 401 / 403）。
- `cached:true` 表示这次是回合成的缓存（距上次真查不到 10 分钟）。
- 更新源可配：`config.toml` 的 `[update] source_url` 指向任何返回 GitHub 形状
  release JSON 的地址（镜像 / 自建）；留空则 `enabled:false`、不做检查。
  `timeout_seconds` 默认 8。

---

## 16. 维护（缓存与清理）

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/v1/maintenance` | 管理员 | 各缓存目录占用 + 叠加层孤儿统计（**不删**） |
| POST | `/api/v1/maintenance/clean` | 管理员 | 执行清理，返回每一项清掉多少 |

`GET /maintenance` 响应 `MaintenancePayload`：

```json
{
  "images": { "dir": "...", "files": 120, "bytes": 3456789, "note": "图片缓存：…" },
  "streams": { ... }, "probe": { ... }, "overlay": { ... },
  "overlayOrphans": { "files": 0, "bytes": 0, "note": "库或条目已经不在库里的叠加层数据" }
}
```

清理请求体：`{ "images": true, "overlayOrphans": true }` →
`{ "cleaned": { "images": { "files": 120, "bytes": 3456789 } } }`。

> 三条边界：**绝不碰媒体文件**；叠加层里**还在库里的**条目一个字节都不动；
> 清空图片缓存会引起一次「重新下载」的流量 —— 所以它是显式动作，不做自动调用。
> 清理孤儿时**查库出错一律不删**（「查不到」≠「不存在」）。

---

## 17. 后台任务队列

探测与刮削都走 PostgreSQL 任务队列（不引 Redis/MQ）。

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/v1/tasks` | 登录 | 队列水位与最近任务 |
| POST | `/api/v1/libraries/{id}/probe` | 登录 | 把该库还没探测的文件批量入队 |
| POST | `/api/v1/libraries/{id}/probe/reset` | 登录 | 把探测失败的重置为待探测并入队 |
| POST | `/api/v1/libraries/{id}/scrape` | 登录 | 批量排刮削任务 |
| GET | `/api/v1/libraries/{id}/scrape` | 登录 | 刮削进度 |
| POST | `/api/v1/libraries/{id}/scrape/reset` | 登录 | 重置失败的刮削并入队 |

- `GET /tasks` 参数 `state`（可选）→ `{stats, byKind, recent}`。
  `stats`：`{pending, running, done, failed, canceled, oldestPendingSeconds}`；
  `recent`：`[TaskInfo]`，每项 `{id, kind, state, attempts, maxAttempts, lastError?, lockedBy?}`。
- 入队探测 → `{ "enqueued": 120, "probe": {pending, ok, failed} }`。
- 重置探测 → `{ "reset": 3, "enqueued": 120, "probe": {...} }`。
- 入队刮削请求体（可选）：`{ "force": false, "kind": "movie" }`（`kind ∈ movie|series`，空=两者）
  → `{ "enqueued": 40, "scrape": {nfo, local, matched, review, manual, failed} }`。
  未配置元数据源时回 **409**。
- 刮削进度 → `{ "configured": true, "scrape": ScrapeProgress }`。
  `ScrapeProgress`：`{nfo, local, matched, review, manual, failed}`（按匹配状态分）。
- 重置刮削 → `{ "reset": 3, "enqueued": 40, "scrape": {...} }`。

> **扫描不会自动刮**（刮削要花 API 配额，得由人决定何时开跑）；这个接口就是那个「一键刮削」入口。

---

## 18. 人工匹配

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/v1/items/{id}/match` | 登录 | 匹配状态与**已存的**候选（含打分明细，不必重搜） |
| POST | `/api/v1/items/{id}/match` | 登录 | 应用选定候选 |
| POST | `/api/v1/items/{id}/match/search` | 登录 | 换个词重新搜候选（不落库） |
| POST | `/api/v1/items/{id}/match/skip` | 登录 | 标记「不需要自动匹配」 |

- GET 响应：`{ "item": MatchItem, "candidates": [MatchCandidate] }`。没有候选时前端给「换个词再搜」入口。
- 应用候选：`{ "providerId": 12345 }` → `{ "ok": true, "matchState": "manual" }`。
- 重搜：`{ "query": "..." }`（空则用条目标题）→ `{ "query": "...", "candidates": [MatchCandidate] }`。
- 跳过：`{ "reason": "..." }`（可选，可空体）。
- 未配置元数据源时这几个接口回 **409**。
- `MatchCandidate`：`{candidateId, kind, title, year?, score, decision, margin?, posterUrl?, matchedAlias?, parts?}`，
  `parts` 是打分明细。

---

## 19. 用户与权限（管理员）

设计见 [`docs/notes/users-permissions.md`](notes/users-permissions.md)（dev 分支）。三条硬规则：

1. 只有管理员能碰这些接口；
2. **不能改自己的管理员位 / 禁用位**（防手一滑把自己锁在外面）；
3. **不能把最后一个活跃管理员弄没**（禁用 / 降级 / 删除都算，回 409）。

> 改口令、禁用、收紧库白名单之后**立刻吊销该用户的会话** —— 否则旧 token 还能照旧看片。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/users` | 列出账号 → `{ "users": [UserView] }` |
| POST | `/api/v1/users` | 注册 → `{ "user": UserView }` |
| PATCH | `/api/v1/users/{id}` | 改账号 → `{ "user": UserView }` |
| PUT | `/api/v1/users/{id}/libraries` | 设可见库（白名单）→ `{ "user": UserView }` |
| POST | `/api/v1/users/{id}/password` | 重置口令 → `{ "ok": true }` |
| DELETE | `/api/v1/users/{id}` | 删除 → `{ "ok": true }` |

`UserView`（**绝不带口令哈希**）：

```json
{
  "id": 3, "username": "alice", "displayName": "Alice",
  "isAdmin": false, "isDisabled": false,
  "maxConcurrentStreams": 2, "restrictedLibraries": true, "libraryIds": [1, 2],
  "allowTranscode": false, "allowLiveTV": true,
  "lastLoginAt": "...", "createdAt": "...", "activeSessions": 1
}
```

- 注册请求体：`{ "username": "alice", "password": "...", "displayName": "Alice", "isAdmin": false }`
  （默认「全部库可见 + 允许转码 + 允许直播」）。
- 改账号请求体（全可选）：`displayName`、`isAdmin`、`isDisabled`、`maxConcurrentStreams`、
  `restrictedLibraries`、`allowTranscode`、`allowLiveTV`。
- 设可见库：`{ "libraryIds": [1,2] }` —— **整份替换**，`[]` = 什么都看不到。
- 重置口令：`{ "password": "..." }`。

这些接口**同时**接受会话 Cookie 与 [管理密钥](#22-管理密钥机器用)；用密钥时操作者记为 `api-token`。

---

## 20. 实时事件（SSE）

#### `GET /api/v1/events` — **登录**

服务端单向推扫描进度。用 SSE 而不是 WebSocket：这里只需要单向下推，
SSE 是纯 HTTP、浏览器自带断线重连，不必引额外协议。

- `Content-Type: text/event-stream`；反代下带 `X-Accel-Buffering: no`。
- 连上先收一条 `event: hello`；扫描进度是 `event: scan`；每 20 秒发一次 `: ping` 心跳。
- 前端可以用 `EventSource` 直接消费。

---

## 21. 静态资源与兜底

#### `GET /`（及其它未匹配的前端路由）— **公开**

`go:embed` 内嵌的前端 SPA。除 `/api/…` 与上面列出的路径之外，路由都落到静态处理器
（未命中的前端路径回落 `index.html`，交给前端路由）。

---

## 附录

<a id="appendix-item"></a>
### A. `Item` 字段速查

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` / `libraryId` / `kind` | — | `kind ∈ movie \| series \| season \| episode \| extra` |
| `parentId` / `seriesId` | int? | 顶层条目没有 `parentId` |
| `seasonNumber` / `episodeNumber` / `episodeEnd` | int? | 季集编号（`episodeEnd` 支持区间合并） |
| `title` / `sortTitle` / `originalTitle` | string | — |
| `year` / `premiereDate` | — | — |
| `overview` / `tagline` | string | — |
| `runtimeTicks` | int? | 时长（ticks） |
| `communityRating` | float? | 社区评分（0~10） |
| `officialRating` | string | 分级（PG-13 / TV-MA…） |
| `genres` / `tags` / `studios` | string[] | — |
| `providerIds` | object | 外部 id（`tmdb` / `tvdb`…） |
| `fileTech` | object | 文件技术信息（分辨率、编码等，扫描时记录） |
| `matchState` | string | 见附录 B |
| `matchScore` | float? | 匹配总分（0~1） |
| `lockedFields` | string[] | 锁住的字段，自动流程不覆盖 |
| `metadataSource` | string | 元数据来源 |
| `scrapeError` | string? | 最近一次刮削错误 |
| `lastScrapedAt` / `updatedAt` | time | — |

<a id="appendix-enums"></a>
### B. 枚举速查

| 概念 | 取值 |
|---|---|
| 库类型 | `movie` / `tv` / `homevideo` / `mixed` |
| 条目类型 | `movie` / `series` / `season` / `episode` / `extra` |
| 匹配状态 `matchState` | `local` / `nfo` / `matched` / `review` / `failed` / `manual` |
| 播放模式 `mode` | `direct` / `remux` / `transcode` |
| 播放状态 `state` | `direct` / `starting` / `ready` / `finished` / `error` / `stopped` |
| 转码会话 `state` | `starting` / `ready` / `finished` / `error` |
| 直播处理方式 `mode` | `copy` / `transcode` |
| 扫描状态 `ScanRun.state` | `running` / `done` / `failed` / `canceled` |
| 列表类型 `playlist.kind` | `playlist` / `collection` |
| 审计结果 `result` | `ok` / `failed` |
| 主题 | `light` / `dark` / `system` |

<a id="appendix-subtitle"></a>
### C. 字幕的三种形态

按「能不能还原原意」分：

| 形态 | 处理 | 客户端 |
|---|---|---|
| ASS / SSA | **原样抽出来**交给前端 libass | `subtitleFormat: "ass"`（定位、动画、卡拉OK、矢量绘图全保住） |
| 纯文本（subrip 等） | 转 WebVTT | `subtitleFormat: "vtt"`（浏览器原生轨道） |
| 图形（PGS / VobSub） | **烧进画面**（位图，浏览器渲不了） | 用户显式选择，界面写明「需重新编码」 |

`subtitleState: "preparing"` 表示内嵌字幕还在后台抽取（大文件冷读要时间），
前端轮询；第二次播放直接命中缓存。

<a id="appendix-ticks"></a>
### D. ticks 换算

```
秒      = ticks / 10_000_000
ticks   = 秒 * 10_000_000
```

例：`positionTicks = 600000000` → 60.0 秒。
