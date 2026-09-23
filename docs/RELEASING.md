# 发布流程（dev → main）

本仓库有两个长期分支，**职责不同**：

| 分支 | 角色 | 内容 |
|---|---|---|
| `dev` | 开发主线 | 全部开发成果 + 内部文档（设计笔记、路线图、开发环境说明）+ 验收脚本 |
| `main` | 交付 / 发布分支 | 只带交付所需：源码、README、CHANGELOG、`docs/{ADR,TRANSCODING.md,images,releases}`、`deploy/`、`migrations/` |

**规矩：所有开发都在 `dev` 上做。** 经人工检查后，把**界面上的内部文案**去掉，再合并进
`main`，然后打 tag 发版。

## main 与 dev 的差异只有两类

### 1. 机械的（脚本负责）

`scripts/release/publish-main.sh`：

- 删掉不随交付携带的东西：`docs/notes/`、`docs/ROADMAP.md`、`docs/DEV-ENV.md`、
  `docs/LIBRARY-NOTES.md`、`docs/REQUIREMENTS.md`、`docs/local-tools/`（本来就不入库）、
  以及 `scripts/dev/` 下的验收脚本；
- **保留** `scripts/dev/{check-web-built.sh, i18n-coverage.mjs, fix-db-encoding.sh}` ——
  部署闸门、CI 守卫、建库字符集自救，这三件在交付路径上要用；
- 生成 `CHANGELOG.md`（汇总 `docs/releases/*.md`）。

### 2. 需要判断的（半自动 + 人工）

界面上的解释性文字。`scripts/release/strip-ui-notes.py` 做机械部分：

- 删掉 `<p className="hint">…</p>` 说明段落（保留 404 / 权限 / 当前状态三类短提示）；
- 按规则表把长篇机制解释压成一句事实；
- 清掉界面上的里程碑代号（`M2`/`M5`/`M6`）与内部黑话（`overlay` / `ffprobe` / `ffmpeg` 术语）；
- 同步英文目录 `web/src/i18n.ts`。

**跑完必须人工过一遍**（看 diff 或截图；`/settings`、`/search`、`/libraries` 这几页最容易漏），
再提交。**代码注释一律保留** —— 那是写给维护者的，不是给用户的。

## 步骤

```bash
# 0. 在 dev 上开发完：CI 绿、验收脚本跑过
git switch dev

# 1. 合并进 main（保留两侧历史）
git switch main && git merge --no-ff dev

# 2. 机械剥离（删文档 / 脚手架 + 生成 CHANGELOG）
bash scripts/release/publish-main.sh

# 3. 界面文案剥离（脚本 + 人工过一遍）
python3 scripts/release/strip-ui-notes.py
npm --prefix web run build && (cd web && npx tsc --noEmit)
git diff --stat          # 人工检查

# 4. 写发版说明（在 main 上写：release workflow 会用它当 GitHub Release 正文）
$EDITOR docs/releases/vX.Y.Z.md

# 5. 提交 + 打 tag（tag 即触发发布）
git commit -am "release: vX.Y.Z"
git tag -a vX.Y.Z -m "LMBY vX.Y.Z"
git push origin main vX.Y.Z
```

## 发布后自检（三步，缺一不可）

1. `Release` workflow 绿：四个平台 + `SHA256SUMS.txt`，Release 正文用的是 `docs/releases/<tag>.md`；
2. 下载本机架构的产物：`sha256sum -c SHA256SUMS.txt`，再跑 `./lmby-linux-amd64 version`
   确认版本号是刚打的 tag；
3. 把**官方产物**装到验证实例上跑一遍（`/healthz` + `/api/v1/meta` 的 version）——
   这是「用户下载到的东西能不能跑」的唯一硬证据。

## 一条经验

`main` 的这个形态是**有意**的：不是「删掉注释的代码」，而是「不带开发过程的交付物」。
所以判断标准很简单：**这句话是写给用户的，还是写给我们自己的？** 前者留在 main，后者只在 dev。
