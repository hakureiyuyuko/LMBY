/**
 * 媒体条目类型的小工具。
 *
 * i18n：标签走 `t()`（在函数体里调，每次渲染读当前语言），
 * 于是「电影 / Movie」这类词只在一处定义。
 *
 * 写成 `switch` 而不是「常量表 + 变量查表」：一是 **`t()` 里的键静态可见**
 * （`scripts/dev/i18n-coverage.mjs` 能扫到，不会漏翻），二是标签本来就跟着语言走。
 *
 * 说明：Match.tsx / Item.tsx / Libraries.tsx 里还各有一份同内容的旧拷贝，
 * 改到那几页时顺手换成这里的导入即可。
 */

import { t } from './i18n';

/** 条目类型名（认不出来的原样显示）。 */
export function kindLabel(kind: string): string {
  switch (kind) {
    case 'movie':
      return t('电影');
    case 'series':
      return t('剧集');
    case 'season':
      return t('季');
    case 'episode':
      return t('集');
    case 'extra':
      return t('花絮');
    default:
      return kind;
  }
}
