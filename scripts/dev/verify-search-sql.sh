#!/usr/bin/env bash
# 校验搜索切词（0007 迁移建的那几个函数）：直接对 PG 跑，不经过 HTTP。
#
# 为什么单独一个脚本：切词是搜索的全部「智能」所在，而它在数据库里（plpgsql），
# Go 单测覆盖不到 —— 用真 PG 跑一组固定输入最实在，也顺便验证
# 「索引侧与查询侧用同一个切法」这件事（两边不一致会出现「搜不到但数据明明在」）。
#
# 用法：
#   bash scripts/dev/verify-search-sql.sh                     # 用本机的 lmby 库
#   bash scripts/dev/verify-search-sql.sh "postgres://..."    # 指定连接串
#   PGURI=postgres://... bash scripts/dev/verify-search-sql.sh
set -uo pipefail

# 客户端编码显式 UTF8：很多最小化系统的 locale 是 C，psql 会默认用 SQL_ASCII 发数据，
# 那样中文实参就不会被服务端校验/转换（能跑，但属于「碰巧对」）。
export PGCLIENTENCODING="${PGCLIENTENCODING:-UTF8}"

PG_ARGS=()
if [[ $# -gt 0 ]]; then
  PG_ARGS=("$1")
elif [[ -n "${PGURI:-}" ]]; then
  PG_ARGS=("$PGURI")
else
  export PGPASSWORD="${PGPASSWORD:-$(cat /etc/lmby/pg-password 2>/dev/null)}"
  PG_ARGS=(-h 127.0.0.1 -U lmby -d lmby)
fi

pass=0; fail=0
ok()  { printf '  \033[32mok\033[0m   %s\n' "$1"; pass=$((pass+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n        期望 %s\n        实际 %s\n' "$1" "$2" "$3"; fail=$((fail+1)); }

q()        { psql -tAq "${PG_ARGS[@]}" -c "$1"; }
# 把值嵌进 SQL 字面量（仅用于测试脚本：转义单引号即可）
sql_lit()  { printf '%s' "$1" | sed "s/'/''/g"; }

echo "目标：$(q 'select current_database()')"

echo
echo "== 1. 切词（lmby_bigram）=="
check() { # 名字 期望 输入（SQL 里可以直接写的字面量）
  local got; got=$(q "select lmby_bigram('$3')")
  if [[ "$got" == "$2" ]]; then ok "$1"; else bad "$1" "$2" "$got"; fi
}
check '中文整句 → 二元组'     '钢之 之炼 炼金 金术 术师' '钢之炼金术师'
check '单字保留'             '钢'                      '钢'
check '英文按词切 + 转小写'   'the matrix'              'The.Matrix'
check '中文夹英文'           '钢之 之炼 炼金 the 术师'   '钢之炼金the术师'
check '括号/破折号/叹号丢弃'  'go tv 2024'              'Go (TV) —— 2024!'
check '间隔号当分隔符'        '斩 赤红 红之 之瞳'         '斩·赤红之瞳'
check '分辨率与数字保留'      'avc 1080p 4k'            'AVC 1080P 4K'
check '日文假名也切二元组'     '千と と千 千寻'           '千と千寻'
check '空串'                 ''                        ''

echo
echo "== 2. 生成列与索引 =="
check2() { # 名字 期望 sql
  local got; got=$(q "$3")
  if [[ "$got" == "$2" ]]; then ok "$1"; else bad "$1" "$2" "$got"; fi
}
check2 "search_vec 是 STORED 生成列" 1 \
  "select count(*) from information_schema.columns
    where table_name='media_items' and column_name='search_vec' and is_generated='ALWAYS'"
check2 "GIN 索引就位" 1 \
  "select count(*) from pg_indexes
    where tablename='media_items' and indexname='media_items_search_vec_idx'"
check2 "原始标题的 trigram 索引就位" 1 \
  "select count(*) from pg_indexes
    where tablename='media_items' and indexname='media_items_original_title_trgm_idx'"
check2 "存量行已回填（加列时 PG 重写表）" 0 \
  "select count(*) from media_items where title <> '' and search_vec is null"

echo
echo "== 3. 索引侧与查询侧切法一致（拿真库里的标题自问自答）=="
long_title=$(q "select title from media_items where length(title) > 6 order by length(title) desc limit 1")
if [[ -z "$long_title" ]]; then
  printf '  \033[33m--\033[0m   库里没有足够长的标题，跳过\n'
else
  printf '  \033[33m--\033[0m   最长标题：《%s》\n' "$long_title"
  hit=$(q "select count(*) from media_items
           where search_vec @@ plainto_tsquery('simple', lmby_bigram('$(sql_lit "$long_title")'))")
  if [[ "$hit" -ge 1 ]]; then ok "整标题能命中自己（命中 $hit 条）"; else bad "整标题能命中自己" "≥1" "$hit"; fi

  # 取标题里连续两个字当查询：应当仍能命中该条（这是中文搜索的主要用法）
  # 用 jq 切字符（按码点）：容器里 locale 是 C，cut -c 切的是字节，会把汉字切成碎片
  mid=$(jq -rn --arg t "$long_title" '$t[1:3]')
  if [[ -n "$mid" ]]; then
    hit2=$(q "select count(*) from media_items
             where search_vec @@ plainto_tsquery('simple', lmby_bigram('$(sql_lit "$mid")'))")
    if [[ "$hit2" -ge 1 ]]; then ok "标题中段「$mid」能命中（$hit2 条）"; else bad "标题中段能命中" "≥1" "$hit2"; fi
  fi
fi

printf '  \033[33m--\033[0m   参考：搜「炼金」命中 %s 条，「之」命中 %s 条（单字只能靠 ILIKE 兜底）\n' \
  "$(q "select count(*) from media_items where search_vec @@ plainto_tsquery('simple', lmby_bigram('炼金'))")" \
  "$(q "select count(*) from media_items where search_vec @@ plainto_tsquery('simple', lmby_bigram('之'))")"

echo
printf '\033[1m结果：%d 通过，%d 失败\033[0m\n' "$pass" "$fail"
[[ "$fail" -eq 0 ]]
