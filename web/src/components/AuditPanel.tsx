/**
 * 审计日志：谁在什么时候做了什么（设置 → 审计日志）。
 *
 * 和「日志」页不是一回事：那边是**最近的服务端日志**（内存、会滚掉、排查用），
 * 这里是**持久的问责记录** —— 能按人 / 动作 / 对象翻旧账（「谁把那个库删了」）。
 * 所以这里的过滤维度是按**语义**（动作 / 操作者 / 只看失败），而不是按关键字搜文本。
 */
import { useCallback, useEffect, useState } from 'react';
import { ApiError, api } from '../api';
import { useI18n } from '../i18n';
import type { AuditEntry, AuditPayload } from '../api';

const PAGE = 50;

type Translate = (key: string, params?: Record<string, string | number>) => string;

/**
 * 动作名 → 人话。
 *
 * 用 switch 而不是模块级常量表：常量表会在 `import` 时把语言钉死
 * （项目里踩过这个坑，见 i18n 的注释）。
 */
function actionLabel(action: string, t: Translate): string {
  switch (action) {
    case 'auth.login':
      return t('登录');
    case 'auth.password_change':
      return t('修改口令');
    case 'user.create':
      return t('新建用户');
    case 'user.update':
      return t('修改用户');
    case 'user.delete':
      return t('删除用户');
    case 'user.libraries':
      return t('调整可见库');
    case 'user.password_reset':
      return t('重置口令');
    case 'library.create':
      return t('新建媒体库');
    case 'library.update':
      return t('修改媒体库');
    case 'library.delete':
      return t('删除媒体库');
    case 'library.scan':
      return t('手动扫描');
    case 'settings.update':
      return t('修改设置');
    default:
      return action;
  }
}

/** 过滤用的动作清单（与后端插桩的动作名保持一致）。 */
const ACTIONS = [
  'auth.login',
  'auth.password_change',
  'user.create',
  'user.update',
  'user.delete',
  'user.libraries',
  'user.password_reset',
  'library.create',
  'library.update',
  'library.delete',
  'library.scan',
  'settings.update',
];

/** 把 detail 压成一行「k=v k=v」（太长的值截断，详情不是用来读全文的）。 */
function detailLine(detail: Record<string, unknown> | undefined): string {
  if (!detail) return '';
  const parts: string[] = [];
  for (const [k, v] of Object.entries(detail)) {
    const s = typeof v === 'string' ? v : JSON.stringify(v);
    parts.push(`${k}=${(s ?? '').length > 40 ? `${(s ?? '').slice(0, 40)}…` : s}`);
  }
  return parts.join(' ');
}

export function AuditPanel() {
  const { t } = useI18n();
  const [data, setData] = useState<AuditPayload | null>(null);
  const [error, setError] = useState('');
  const [action, setAction] = useState('');
  const [query, setQuery] = useState('');
  const [failedOnly, setFailedOnly] = useState(false);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(false);

  const load = useCallback(
    async (nextOffset: number) => {
      setLoading(true);
      setError('');
      try {
        const d = await api.audit({
          limit: PAGE,
          offset: nextOffset,
          action: action || undefined,
          q: query.trim() || undefined,
          failed: failedOnly || undefined,
        });
        setData(d);
        setOffset(nextOffset);
      } catch (e) {
        setError(e instanceof ApiError ? e.message : t('读取审计日志失败'));
      } finally {
        setLoading(false);
      }
    },
    [action, failedOnly, query, t],
  );

  // 过滤条件一变就回到第一页重查（分页是对「过滤后的结果」分页）。
  useEffect(() => {
    void load(0);
  }, [load]);

  const total = data?.total ?? 0;
  const from = total === 0 ? 0 : offset + 1;
  const to = Math.min(offset + PAGE, total);

  return (
    <div className="card">
      <h2>{t('审计日志')}</h2>
      {error && <div className="alert alert-error">{error}</div>}

      <div className="row">
        <select value={action} aria-label={t('动作')} onChange={(e) => setAction(e.target.value)}>
          <option value="">{t('全部动作')}</option>
          {ACTIONS.map((a) => (
            <option key={a} value={a}>
              {actionLabel(a, t)}
            </option>
          ))}
        </select>
        <input
          type="search"
          value={query}
          placeholder={t('搜操作者或对象…')}
          aria-label={t('搜索')}
          onChange={(e) => setQuery(e.target.value)}
        />
        <label className="row" style={{ gap: 6 }}>
          <input
            type="checkbox"
            checked={failedOnly}
            onChange={(e) => setFailedOnly(e.target.checked)}
          />
          {t('只看失败')}
        </label>
        <button type="button" className="btn btn-sm" disabled={loading} onClick={() => void load(offset)}>
          {loading ? t('读取中…') : t('刷新')}
        </button>
      </div>

      {!data && !error && <p className="muted">{t('正在读取…')}</p>}
      {data && data.entries.length === 0 && <p className="muted">{t('没有符合条件的记录。')}</p>}

      {data && data.entries.length > 0 && (
        <>
          <table>
            <thead>
              <tr>
                <th>{t('时间')}</th>
                <th>{t('操作者')}</th>
                <th>{t('动作')}</th>
                <th>{t('对象')}</th>
                <th>{t('结果')}</th>
                <th>{t('细节')}</th>
              </tr>
            </thead>
            <tbody>
              {data.entries.map((e: AuditEntry) => (
                <tr key={e.id}>
                  <td className="small faint">{new Date(e.at).toLocaleString()}</td>
                  <td className="small">{e.actorName || t('（未知）')}</td>
                  <td className="small">{actionLabel(e.action, t)}</td>
                  <td className="small faint mono">{e.target || '—'}</td>
                  <td>
                    <span className={`badge ${e.result === 'ok' ? 'badge-ok' : 'badge-bad'}`}>
                      {e.result === 'ok' ? t('成功') : t('失败')}
                    </span>
                  </td>
                  <td className="small faint">{detailLine(e.detail) || '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <div className="row" style={{ justifyContent: 'space-between', alignItems: 'center' }}>
            <span className="faint small">{t('第 {from}–{to} 条，共 {total} 条', { from, to, total })}</span>
            <span className="row" style={{ gap: 8 }}>
              <button
                type="button"
                className="btn btn-sm"
                disabled={offset === 0 || loading}
                onClick={() => void load(Math.max(0, offset - PAGE))}
              >
                {t('上一页')}
              </button>
              <button
                type="button"
                className="btn btn-sm"
                disabled={to >= total || loading}
                onClick={() => void load(offset + PAGE)}
              >
                {t('下一页')}
              </button>
            </span>
          </div>
        </>
      )}
    </div>
  );
}
