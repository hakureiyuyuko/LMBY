# 参与贡献

**`main` 是交付 / 发布分支**，只带交付所需的内容；**开发在 `dev` 分支上进行**。

- 想改代码或报问题：请针对 `dev` 分支提 PR / issue；
- `main` 只接受「发布整理」：由维护者从 `dev` 合并，并去掉界面上的内部文案后打 tag 发版；
- 发布流程（两类差异、脚本、发布后自检）见 [docs/RELEASING.md](https://github.com/hakureiyuyuko/LMBY/blob/dev/docs/RELEASING.md)；

## 本地开发

环境与工具链见 [docs/DEV-ENV.md](https://github.com/hakureiyuyuko/LMBY/blob/dev/docs/DEV-ENV.md)，
常用命令见 `Taskfile.yml`。

## 代码风格

- **Go**：`gofmt` + `go vet` + 与 CI 同版本的 `golangci-lint`（CI 会跑）；
- **前端**：TypeScript 严格模式（`npx tsc --noEmit`）；界面文案一律用 `t('中文原文')` 包起来 ——
  CI 有 i18n 覆盖率守卫，漏包会直接失败；
- **提交信息**：写清「为什么这么改」，比写清「改了哪几行」有价值。

## 许可

本项目为 AGPL-3.0-only。提交即表示同意以该许可分发你的贡献。
