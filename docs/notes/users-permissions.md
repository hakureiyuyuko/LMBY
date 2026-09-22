# 用户与权限（M7 地基 · 设计方案）

> 状态：**已实现并部署**（用户 2026-09-22 拍板「按推荐来」：新用户默认全部库可见、
> 允许转码、允许直播）。实现落在：迁移 0016、`internal/store/permissions.go`（Viewer +
> 唯一入口 ViewerFor + libraryFilter）、只读路径过滤（库列表/搜索含分面与联想/首页四行/
> 续看/收藏/条目详情 404/起播）、6 个管理接口（`internal/api/users.go`）、
> 设置 → 用户页签（`web/src/pages/Users.tsx`）、`scripts/dev/verify-users.sh`。
>
> **两处验收还没打通**（不是没写，是这台机器上凑不出场景，记在这儿免得下次重复踩）：
> 1. 「不允许转码」的 VOD 路径：库 #1 里挑不到「决策为转码」的条目（找不到就跳过了）；
> 2. 同一条限制的直播路径：拿 `force=transcode` 打一台能通的频道，**起播 502**
>    —— 说明这台机器上「直播转码」这条路对某些源站压根起不来（与权限无关，
>    是 liveVideoEncode 拼出来的转码参数对这类源不成立），值得单独查一轮。
> 另外：`max_concurrent_streams` **原本就已经接线**（`s.plays.countForUser`），
> 本文档开头写「没人读它」是错的，已由实测纠正。
> 目标：多用户 + 按用户的可见性与能力限制，且**判定逻辑只有一个入口** ——
> 这是 M7（多用户）的地基，也是 M8 发布前必须稳的东西。

## 一、现状（已经有什么、缺什么）

**已有**（不用重做）：

- 表 `users`：`username / display_name / is_admin / is_disabled / max_concurrent_streams(0=用全局) /
  last_login_at / last_login_ip / preferences`。
- store：`CountUsers / CountAdmins / CreateUser / CreateFirstAdmin / GetUserBy* / ListUsers /
  UpdateUserPassword / UpdateUserDisplayName / SetUserDisabled / TouchUserLogin` + 偏好读写。
- 会话体系：登录发 token、`sessions` 表、可列出与吊销（个人中心已用）。
- `store.Viewer{UserID, IsAdmin}` 已经在**播放列表/合集**那一块用上了（权限判 404/403 的先例）。

**缺**（这就是本次要做的）：

1. **权限本身**：现在任何登录用户都能看到**全部**媒体库、全部条目、全部直播频道。
2. **用户管理接口与界面**：只有登录/改自己口令/改显示名，没有「管理员建用户、授库、禁用」。
3. **并发流上限没接线**：列早就有，但没人读它（现在是全局 `max_sessions`）。
4. **禁用用户不踢线**：`is_disabled` 只在登录时判，已登录的会话还能用。

## 二、权限模型（4 个维度，别多）

| 维度 | 说明 | 存哪 |
|---|---|---|
| 管理员 | 能改全站设置、管用户、管库；**永远可见全部库** | `users.is_admin`（已有） |
| 库可见性 | 能看哪些媒体库。开关 `restrict_libraries` 打开后按白名单，否则全部 | 新：`users.restrict_libraries` + `user_libraries(user_id, library_id)` |
| 并发流上限 | 这个账号同时几条播放（0 = 用全局 `max_sessions`） | `users.max_concurrent_streams`（已有，接上） |
| 允许转码 | 关掉只能直出/转封装（保护 CPU）；关掉后在播放器里显示原因 | 新：`users.allow_transcode` |
| 允许直播 | 关掉看不到「直播」页（网盘/直播源只给家里人看这种场景） | 新：`users.allow_livetv` |

**刻意不做**（列出来，免得以后被问「怎么没有」）：

- 家长控制/分级过滤 → 二期（真要用涉及「哪些分级算儿童」这种要人定的规则）。
- 按用户限速/限码率 → 二期（先有「允许转码」这个粗开关）。
- 每用户的库内过滤（某库的某些目录）→ 不做，库是最小授权单位。

## 三、判定的唯一入口（关键设计）

**只在一个地方判定**，其余地方一律通过它：

```go
// store.Viewer 是「谁在看」：权限判定的唯一载体。
// 新增字段：
//   RestrictedLibraries bool    // 是否按白名单
//   Libraries           []int64 // 白名单（管理员忽略）
//   AllowTranscode      bool
//   AllowLiveTV         bool
//   MaxStreams          int
//
// 用法：api 层一进请求就把 auth 里的用户转成 Viewer，再传给 store；
// store 的每条「列表类」SQL 里统一加一句：
//     and ($n = 0 or i.library_id = any($Libs::bigint[]))
// 由 store.Viewer.VisibleLibraryIDs() 算出（管理员直接返回 nil = 不过滤）。
```

**为什么要这样**：可见性一旦散落在 api/前端各处，迟早漏一处（漏掉的那一处就是
「非管理员能看到不该看的库」）。放在 store 的 SQL 层还有一个好处：分页、分面计数、
推荐、搜索全都自动跟着对，不会出现「列表里没有、但搜索能搜到」。

**影响面（已核对代码）** —— 可见性要接进这些读路径：

- 列表/浏览：`ListLibraries`（只返回可见库）、`ListItems`、`ListLibraryFiles`（仅管理员）
- 搜索：`SearchItems` / `SearchFacets` / `SearchSuggest`（**分面数字也要一起对**）
- 首页：`ListRecentItems` / `ListTopRatedItems` / `RecommendForUser` / `ListContinueWatching`
- 收藏与列表：`ListFavorites` / `ListPlaylistItems` / `ListPlaylists`（已有 Viewer，补库过滤）
- 详情与相关：`GetItem` / `ListRelatedItems`（不可见的库 → **404**，别用 403，免得探出「有这个库」）
- 播放：起播会话前再判一次（库可见 + 允许转码 + 并发上限）
- 直播：`ListTVChannels` / 起播（`allow_livetv`）

## 四、迁移草案（0016_users_permissions.sql）

```sql
-- 库可见性：默认不改行为（restrict_libraries=false → 全部库可见）
alter table users add column if not exists restrict_libraries boolean not null default false;
alter table users add column if not exists allow_transcode     boolean not null default true;
alter table users add column if not exists allow_livetv        boolean not null default true;

create table if not exists user_libraries (
  user_id    bigint not null references users(id)     on delete cascade,
  library_id bigint not null references libraries(id) on delete cascade,
  created_at timestamptz not null default now(),
  primary key (user_id, library_id)
);
create index if not exists idx_user_libraries_user on user_libraries (user_id);
```

要点：**默认值就是现在行为**（全部可见、允许转码、允许直播），所以升级后老账号
不会忽然看不见东西；想限制谁就去勾谁。

## 五、接口草案

```
GET    /api/v1/users                    管理员：用户列表（含库数、是否禁用、上限）
POST   /api/v1/users                    管理员：建用户（username / password / displayName / isAdmin）
PATCH  /api/v1/users/{id}               管理员：改显示名 / 禁用 / 管理员 / 并发上限 / 转码 / 直播 / 库白名单开关
PUT    /api/v1/users/{id}/libraries     管理员：整份替换可见库（白名单语义，和媒体库路径一个风格）
DELETE /api/v1/users/{id}               管理员：删用户（连带收藏/列表/观看进度；**不能删最后一个管理员**）
POST   /api/v1/users/{id}/password      管理员：重置口令（重置后吊销该用户所有会话）
```

安全约束（都要有测试）：

- 只有管理员能调这些；**不能改自己的 `is_admin`/`is_disabled`**（防自锁），
  也**不能删掉/禁用掉最后一个管理员**（否则没人管得了服务器）。
- 改口令 / 禁用 / 收紧库白名单 → **立刻吊销该用户会话**（否则旧 token 还能看）。
- 口令强度沿用现有规则（注册接口那套）。
- 管理员的库列表永远返回全部（不看白名单），免得管理员被自己锁在外面。

## 六、界面草图（设置 → 用户）

```
设置 │ 库管理 │ 人工匹配 │ 会话 │ 直播源 │ 用户        ← 新增页签
┌────────────────────────────────────────────────────┐
│ 用户（3）                              [＋ 新建用户] │
│ ┌────────────────────────────────────────────────┐ │
│ │ hakurei      管理员 · 全部库 · 并发不限        │ │
│ │ devtest      普通 · 3 个库 · 并发 2 · 禁转码   │ │
│ │ guest        普通 · 1 个库 · 只直出   [禁用中] │ │
│ └────────────────────────────────────────────────┘ │
│ 点一行展开：显示名 / 重置口令 / 可见库（勾选）      │
│             / 并发上限 / 允许转码 / 允许直播 / 禁用  │
└────────────────────────────────────────────────────┘
```

个人中心（`/account`）只保留「我自己」的东西（显示名、口令、外观、设备），
管理别人的入口一律在设置的「用户」页签里。

## 七、验收计划（都真跑，写成 verify 脚本）

`scripts/dev/verify-users.sh`：

1. 建用户 → 登录 → 只能看到白名单里的库；不在白名单的库**列表里没有、搜索里搜不到、
   直接访问条目返回 404**；
2. 打开 `allow_transcode=false` → 起播一个必须转码的文件时，播放决策报「不允许转码」
   且**不启动 ffmpeg**；
3. 并发上限 1 → 同一账号第二个播放会话被拒（提示人话）；0 → 用全局上限；
4. 禁用用户 → 已登录会话**立刻失效**（下一个请求 401）；
5. 最后一个管理员：不能禁用、不能降级、不能删（三个都验）；
6. 改口令 → 旧 token 失效、新口令能登录。

界面：扩 `m2-ui-test`（设置里有「用户」页签、能建用户、能勾库）。

## 八、需要你拍板的三件事（我的推荐已写在后）

1. **新用户的库可见性默认**：`全部可见（要限制才勾）` ← 推荐，升级后行为不变 |
   `默认不给任何库（必须管理员勾）` ← 更安全但升级后建用户会「什么都看不到」，容易懵。
2. **要不要「允许转码」开关**：`要` ← 推荐（保护 CPU，家用服务器最实际的限制） | `不要`（只留并发上限）。
3. **要不要「允许直播」开关**：`要` ← 推荐（直播源常是私人的） | `不要`（所有登录用户都能看直播）。

你回一句「按你推荐的来」我就开工；要改哪条直接说。
