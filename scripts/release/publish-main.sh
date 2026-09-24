#!/usr/bin/env bash
# 把 dev 的成果整理成「交付态」（在 main 上跑）：
#   - 删掉不随交付携带的文档与验收脚手架；
#   - 生成 CHANGELOG.md（汇总 docs/releases/*.md）。
#
# 用法：
#   bash scripts/release/publish-main.sh [--dry-run]
#
# 为什么脚本化：main 与 dev 的差异里**机械的那部分**（哪些文件不带、CHANGELOG 怎么生成）
# 不该靠记忆；**需要判断的那部分**（哪句界面文案算「解释性」）交给
# scripts/release/strip-ui-notes.py + 人工过一遍。规则见 docs/RELEASING.md。
set -uo pipefail

DRY=0
[[ "${1:-}" == "--dry-run" ]] && DRY=1
cd "$(dirname "$0")/../.." || exit 1

branch=$(git branch --show-current)
if [[ "$branch" != "main" ]]; then
  echo "这个脚本要在 main 分支上跑（当前：$branch）。先 git switch main && git merge --no-ff dev。" >&2
  exit 2
fi

DROP_FILES=(
  docs/ROADMAP.md
  docs/DEV-ENV.md
  docs/LIBRARY-NOTES.md
  docs/REQUIREMENTS.md
  # 交付 README 的源文件：第 1 步会把它装成 README.md，这里兜底以免残留。
  docs/README.release.md
)
DROP_DIRS=(
  docs/notes
)
# 注意：`docs/local-tools/`、`docs/local-*.md` 是**本机 gitignored 文件**（内网坐标与小工具），
# 它们不属于任何分支 —— 发布脚本不该碰它们（早前把它写进删除清单，等于误删本机文件）。
# scripts/dev 里只留这三个：部署闸门、CI 守卫、建库字符集自救（交付路径上要用）
KEEP_SCRIPTS=(check-web-built.sh i18n-coverage.mjs fix-db-encoding.sh)

echo "== 1. 装交付 README =="
# README 有两个角色，分开维护：
#   · dev 的 README.md 面向**改仓库的人**（怎么构建、怎么验收、分支模型）；
#   · 交付 README（给装来用的人看）的单一来源是 docs/README.release.md，
#     发布时把它装成 README.md（源文件本身不随交付携带）。
# 先做这一步再做删除：这样一来 docs/README.release.md 也能留在 DROP_FILES 里兜底。
# 另外，dev → main 的合并里 README.md 可能冲突 —— 随便取哪边都行，这里会覆盖掉。
if [[ -f docs/README.release.md ]]; then
  if [[ $DRY == 1 ]]; then
    echo "   会用 docs/README.release.md 覆盖 README.md，然后删掉那个源文件"
  else
    cp docs/README.release.md README.md
    echo "   已用 docs/README.release.md 覆盖 README.md（$(wc -l < README.md) 行）"
  fi
else
  echo "   注意：没有 docs/README.release.md，跳过（README.md 保持原样）"
fi

echo
echo "== 2. 删掉不随交付携带的文档与验收脚本 =="
for d in "${DROP_DIRS[@]}"; do
  [[ -e "$d" ]] || continue
  echo "   删目录 $d"
  [[ $DRY == 1 ]] || rm -rf "$d"
done
for f in "${DROP_FILES[@]}"; do
  [[ -e "$f" ]] || continue
  echo "   删文件 $f"
  [[ $DRY == 1 ]] || rm -f "$f"
done
if [[ -d scripts/dev ]]; then
  while IFS= read -r f; do
    base=$(basename "$f")
    keep=0
    for k in "${KEEP_SCRIPTS[@]}"; do [[ "$base" == "$k" ]] && keep=1; done
    [[ $keep == 1 ]] && continue
    echo "   删脚本 $f"
    [[ $DRY == 1 ]] || rm -f "$f"
  done < <(find scripts/dev -maxdepth 1 -type f)
fi

echo
echo "== 3. 生成 CHANGELOG.md（汇总 docs/releases/*.md） =="
if [[ $DRY == 1 ]]; then
  echo "   （dry-run：会收录这些版本）"
  for f in $(ls docs/releases/*.md 2>/dev/null | sort -rV); do echo "   $f"; done
else
  {
    echo "# 更新日志"
    echo
    echo "每个版本的完整说明都单独成文（GitHub Releases 里逐版发布）："
    echo
    for f in $(ls docs/releases/*.md 2>/dev/null | sort -rV); do
      tag=$(basename "$f" .md)
      date=$(git log -1 --format=%ad --date=short -- "$f" 2>/dev/null || true)
      title=$(sed -n '1s/^# *LMBY [^ ]* *—— *//p' "$f")
      echo "- [$tag]($f)（${date:-—}）—— ${title:-（见文件）}"
    done
  } > CHANGELOG.md
  echo "   已写入 CHANGELOG.md（$(wc -l < CHANGELOG.md) 行）"
fi

echo
echo "== 4. 剩下两步要人来做 =="
echo "   a) python3 scripts/release/strip-ui-notes.py   # 界面解释性文案（跑完人工过一遍 diff / 截图）"
echo "   b) 写 docs/releases/vX.Y.Z.md，提交，打 tag（tag 触发发布）"
echo
# 用 if 而不是 `[[ ... ]] && echo`：后者在非 dry-run 时返回 1，会让
# `bash publish-main.sh && python3 strip-ui-notes.py` 这样的链式调用在脚本“成功”后断掉。
if [[ $DRY == 1 ]]; then
  echo "（dry-run 结束：没有改动任何文件）"
fi
