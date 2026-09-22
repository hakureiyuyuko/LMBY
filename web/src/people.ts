/**
 * 演职员展示用的小工具。
 *
 * 角色的写法来自 nfo（`actor` / `GuestStar` / `Director` …，大小写照原样入库），
 * 界面统一按小写查表 —— 详情页、搜索页、联想下拉都走这里，
 * 免得三处各写一份映射、某一处漏了「客串」。
 */

import { t } from './i18n';

/**
 * 演职员展示用的小工具。
 *
 * 角色的写法来自 nfo（`actor` / `GuestStar` / `Director` …，大小写照原样入库），
 * 界面统一按小写查表 —— 详情页、搜索页、联想下拉都走这里，
 * 免得三处各写一份映射、某一处漏了「客串」。
 *
 * i18n：中文名当键（`t()` 在函数体里调，语言一变标签就跟着变）。
 */
const roleKeys: Record<string, string> = {
  actor: '演员',
  director: '导演',
  writer: '编剧',
  producer: '制片',
  composer: '作曲',
  gueststar: '客串',
};

/** 把 nfo 里的角色名换成界面语言（认不出来的原样显示，不丢信息）。 */
export function roleLabel(role: string): string {
  const key = roleKeys[role.toLowerCase()];
  return key ? t(key) : role;
}

/** 把一串角色拼成「演员 / 导演」（去重、保持原顺序）。 */
export function roleText(roles?: string[]): string {
  if (!roles || roles.length === 0) return '';
  const seen = new Set<string>();
  const out: string[] = [];
  for (const r of roles) {
    if (!r || seen.has(r)) continue;
    seen.add(r);
    out.push(roleLabel(r));
  }
  return out.join(' / ');
}
