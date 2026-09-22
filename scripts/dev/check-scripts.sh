#!/usr/bin/env bash
# 对仓库里的所有 shell 脚本跑一遍语法检查（`bash -n`）。
#
# 为什么值得有：验收脚本通常上百行、嵌套一堆 $( ) 与 jq 引号，
# 一个多余的引号就能让整份脚本在**运行到那一步时**才炸（甚至直接 EOF 报错，
# 报的行号还是最后一行）。提交前跑一次只要一秒，能省掉一轮「传上去才发现」。
#
# 用法：bash scripts/dev/check-scripts.sh
set -uo pipefail

cd "$(dirname "$0")/../.." || exit 1

n=0
bad=0
while IFS= read -r f; do
  n=$((n + 1))
  if ! out=$(bash -n "$f" 2>&1); then
    bad=$((bad + 1))
    printf 'FAIL %s\n%s\n' "$f" "$out"
  fi
done < <(git ls-files '*.sh')

if ((bad > 0)); then
  echo "== $bad / $n 个脚本有语法问题 =="
  exit 1
fi
echo "== $n 个脚本语法都 ok =="
