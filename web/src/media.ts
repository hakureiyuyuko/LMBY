/**
 * 媒体条目类型的小工具。
 *
 * 说明：Item.tsx / Libraries.tsx / Match.tsx 里各有一份同内容的旧拷贝，
 * 改到那几页时顺手换成这里的导入即可（本次只新增搜索相关两处使用，
 * 不动那些页面的既有代码）。
 */

import { t } from './i18n';

/**
 * 媒体条目类型的小工具。
 *
 * i18n：标签走 `t()`（在函数体里调，每次渲染读当前语言），
 * 于是「电影 / Movie」这类词只在一处定义。
 *
 * 说明：Match.tsx / Item.tsx / Libraries.tsx 里还各有一份同内容的旧拷贝，
 * 改到那几页时顺手换成这里的导入即可。
 */
const kindKeys: Record<string, string> = {
  movie: '电影',
  series: '剧集',
  season: '季',
  episode: '集',
  extra: '花絮',
};

/** 条目类型名（认不出来的原样显示）。 */
export function kindLabel(kind: string): string {
  const key = kindKeys[kind];
  return key ? t(key) : kind;
}
