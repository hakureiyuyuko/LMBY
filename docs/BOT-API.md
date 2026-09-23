# 给 bot / 脚本用的用户管理 API

LMBY 给外部程序（bot、脚本、自动化）留了一把**长期凭据**，用来做账号的注册 / 修改 / 删除。

它存在的理由：登录会话是**给人用的** —— cookie 跟着浏览器、会过期、一改口令就被吊销；
机器需要的是「放在 Header 里的长期凭据」。

## 1. 生成密钥

界面：**设置 → 用户 → 「API 密钥（给 bot / 脚本）」** → 生成。

- 明文**只显示一次**：后端只存 SHA-256，之后连 LMBY 自己都拿不回来（忘了就轮换一把）；
- 随时可以**轮换**（旧密钥立刻失效）或**撤销**。

## 2. 怎么调用

两种等价写法：

```bash
export LMBY_BOT_KEY=lmby_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx

curl -H "Authorization: Bearer $LMBY_BOT_KEY" http://<host>:8099/api/v1/users
curl -H "X-API-Key: $LMBY_BOT_KEY"            http://<host>:8099/api/v1/users
```

## 3. 能用哪些接口（**只有**这些）

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/api/v1/users` | 列账号 |
| POST | `/api/v1/users` | 注册账号 |
| GET | `/api/v1/users/{id}` | 读单个账号 |
| PATCH | `/api/v1/users/{id}` | 改账号（显示名 / 管理员位 / 禁用 / 允许转码 / 允许直播 / 并发上限） |
| DELETE | `/api/v1/users/{id}` | 删除账号 |
| PUT | `/api/v1/users/{id}/libraries` | 设可见媒体库（`[]` = 什么都看不到） |
| POST | `/api/v1/users/{id}/password` | 重置口令 |

**其它接口一律 403**（返回「这个管理密钥只能用于用户管理接口」）。

这是刻意的：密钥泄露时最坏情况是「乱建 / 乱删账号」，而不是「改设置、删媒体库、
读别人库里的内容」。要更大的权限，就给它一个真正的管理员账号。

## 4. 请求体示例

注册（默认就是「全部库可见 + 允许转码 + 允许直播」）：

```bash
curl -X POST http://<host>:8099/api/v1/users \
  -H "Authorization: Bearer $LMBY_BOT_KEY" -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"alice-pass-1","displayName":"Alice","isAdmin":false}'
```

改权限（只给这两个库、允许直播、不许转码、并发 1）：

```bash
curl -X PATCH http://<host>:8099/api/v1/users/<id> \
  -H "Authorization: Bearer $LMBY_BOT_KEY" -H 'Content-Type: application/json' \
  -d '{"allowTranscode":false,"allowLiveTV":true,"maxSessions":1}'

curl -X PUT http://<host>:8099/api/v1/users/<id>/libraries \
  -H "Authorization: Bearer $LMBY_BOT_KEY" -H 'Content-Type: application/json' \
  -d '{"libraryIds":[1,2]}'
```

> 规则：口令至少 8 位；用户名不能带空格。
> **改口令 / 禁用 / 收紧库范围都会让该账号现有的登录立刻失效**（有意为之）。

## 5. 错误码

| 状态 | 含义 |
|---|---|
| 401 | 没带密钥，或密钥不对（撤销之后也是它） |
| 403 | 密钥有效，但**这个接口不在白名单里** |
| 400 | 参数不合法（用户名重复、口令太短、间隔越界…） |
| 409 | 冲突（例如「最后一个管理员不能删 / 不能降级」） |

## 6. 审计

密钥做的每一次操作都会进**审计日志**（设置 → 审计日志），操作者显示为 `api-token` ——
「哪个账号是谁建的」永远查得到，不用猜。

## 7. 安全建议

- 密钥当密码保管：只放在 bot 自己的密钥管理里，别写进仓库、别贴进聊天；
- 给 bot 单独一把，**不要**和人工运维共用（这样轮换时互不影响）；
- 不用了就撤销 —— 撤销是立刻生效的。
