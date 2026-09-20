#!/usr/bin/env bash
# 在容器里跑与 CI 同版本的 golangci-lint。
#
# 为什么要在容器里跑一遍：CI 的 lint 任务曾经因为 `.golangci.yml` 是 v1 格式、
# 而 action 装的是 v2，直接「加载配置失败」退出 —— 从 M0 开始就一直是红的，
# 但没人注意。所以改完 lint 相关的东西，先在容器里真跑一次再推。
#
# 准备二进制（本机下载后上传，容器里直连 GitHub 慢）：
#   curl.exe -L -o $env:TEMP\gcl.tar.gz https://github.com/golangci/golangci-lint/releases/download/v2.13.2/golangci-lint-2.13.2-linux-amd64.tar.gz
#   node ssh.mjs put $env:TEMP\gcl.tar.gz /root/gcl.tar.gz
#   解包到 /opt/gcl（本脚本会自动检查）
#
# 用法：bash scripts/dev/lint.sh [额外参数，如 --new-from-rev=HEAD~1]
set -o pipefail

LINT=${LINT:-/opt/gcl/golangci-lint}
DST=${DST:-/opt/lmby}

export PATH=$PATH:/usr/local/go/bin

if [ ! -x "$LINT" ]; then
  echo "找不到 $LINT：先从 golangci-lint 的 release 里下对应版本的 linux-amd64 包，"
  echo "上传到容器并解包到 /opt/gcl（注意要和 CI 的 action 版本一致）。"
  exit 2
fi

cd "$DST" || exit 1
"$LINT" --version

# 这一步别省：action 会先跑 config verify，
# 而配置不合 schema 时 `run` 可能什么都不报就直接过了
# （v1→v2 的 issues.exclude-rules 就是这么踩到的）。
echo "== 校验 .golangci.yml =="
if ! "$LINT" config verify; then
  echo "!! 配置不合 v2 schema —— CI 会在 config verify 这一步就挂掉"
  exit 3
fi

echo "== 开始检查 =="
"$LINT" run "$@"
code=$?
echo "== golangci-lint 退出码: $code =="
exit $code
