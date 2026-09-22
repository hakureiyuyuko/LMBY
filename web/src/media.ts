/**
 * 媒体条目类型的小工具。
 *
 * 说明：Item.tsx / Libraries.tsx / Match.tsx 里各有一份同内容的旧拷贝，
 * 改到那几页时顺手换成这里的导入即可（本次只新增搜索相关两处使用，
 * 不动那些页面的既有代码）。
 */

const kindLabels: Record<string, string> = {
  movie: '电影',
  series: '剧集',
  season: '季',
  episode: '集',
  extra: '花絮',
};

/** 条目类型的中文名（认不出来的原样显示）。 */
export function kindLabel(kind: string): string {
  return kindLabels[kind] ?? kind;
}

/** 类型 → 中文的映射（分面/筛选下拉要按键值对遍历）。 */
export const KIND_LABELS = kindLabels;
