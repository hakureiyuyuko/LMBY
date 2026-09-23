/**
 * 日志：最近的**服务端**日志（设置 → 日志）。
 *
 * 数据来自 `GET /api/v1/logs` —— 服务在内存里留了一份最近 2000 条日志（见
 * `internal/logbuf`），所以**容器里、Windows 上都能看**，不依赖 journalctl。
 * 只给管理员：里面会带用户名、媒体路径与错误详情。
 *
 * 界面按「排查问题」的用法来设计：先按级别收窄（默认 INFO 以上），再按关键字找；
 * 需要留证据时把当前视图下载成 txt。
 */
import { useCallback, useEffect, useMemo, useState } from 'react';
import { ApiError, api } from '../api';
import { useI18n } from '../i18n';
import type { LogEntry, LogsPayload } from '../api';

const LIMIT_DEFAULT = 100;
/** 可选的条数档位（排查时先用小的，需要时再放大）。 */
const LIMIT_CHOICES = [100, 300, 1000];
/** 自动刷新的间隔（毫秒）。 */
const AUTO_MS = 5000;

function fmtTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

function attrsText(e: LogEntry): string {
  if (!e.attrs) return '';
  return Object.entries(e.attrs)
    .map(([k, v]) => `${k}=${typeof v === 'object' ? JSON.stringify(v) : String(v)}`)
    .join('  ');
}

function levelClass(level: string): string {
  if (level === 'ERROR') return 'badge badge-bad';
  if (level === 'WARN') return 'badge badge-warn';
  return 'badge';
}

export function LogsPanel() {
  const { t } = useI18n();
  const [data, setData] = useState<LogsPayload | null>(null);
  const [error, setError] = useState('');
  // 默认只 WARN 以上：INFO 里绝大多数是 HTTP 请求日志，进来先看「哪里出问题了」才有用，
  // 要看全量再自己去切级别。
  const [level, setLevel] = useState('warn');
  const [limit, setLimit] = useState(LIMIT_DEFAULT);
  const [query, setQuery] = useState('');
  const [applied, setApplied] = useState('');
  const [auto, setAuto] = useState(false);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    setBusy(true);
    try {
      setData(await api.logs({ limit, level, q: applied }));
      setError('');
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('读取日志失败'));
    } finally {
      setBusy(false);
    }
  }, [level, limit, applied, t]);

  useEffect(() => {
    void load();
  }, [load]);

  // 自动刷新：只在打开时定时拉一次（排查问题时常开着）
  useEffect(() => {
    if (!auto) return;
    const id = window.setInterval(() => void load(), AUTO_MS);
    return () => window.clearInterval(id);
  }, [auto, load]);

  const levelOptions = useMemo(
    () => [
      { value: 'debug', label: t('全部级别') },
      { value: 'info', label: t('INFO 以上') },
      { value: 'warn', label: t('WARN 以上') },
      { value: 'error', label: t('只有 ERROR') },
    ],
    [t],
  );

  function download() {
    if (!data) return;
    const lines = data.entries
      .map((e) => `${e.time}  ${e.level.padEnd(5)}  ${e.msg}${attrsText(e) ? '  ' + attrsText(e) : ''}`)
      .join('\n');
    const blob = new Blob([lines + '\n'], { type: 'text/plain;charset=utf-8' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = `lmby-logs-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, '-')}.txt`;
    a.click();
    URL.revokeObjectURL(a.href);
  }

  return (
    <div className="card">
      <h2>{t('日志')}</h2>
      {error && <div className="alert alert-error">{error}</div>}

      <div className="row" style={{ gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
        <select value={level} onChange={(e) => setLevel(e.target.value)} aria-label={t('级别')}>
          {levelOptions.map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
        <select value={limit} onChange={(e) => setLimit(Number(e.target.value))} aria-label={t('条数')}>
          {LIMIT_CHOICES.map((n) => (
            <option key={n} value={n}>
              {t('{n} 条', { n })}
            </option>
          ))}
        </select>
        <input
          value={query}
          placeholder={t('搜消息或字段…')}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') setApplied(query.trim());
          }}
          style={{ minWidth: 220 }}
        />
        <button type="button" className="btn btn-sm" onClick={() => setApplied(query.trim())}>
          {t('搜索')}
        </button>
        <button type="button" className="btn btn-sm" disabled={busy} onClick={() => void load()}>
          {busy ? t('读取中…') : t('刷新')}
        </button>
        <label className="small" style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
          <input type="checkbox" checked={auto} onChange={(e) => setAuto(e.target.checked)} />
          {t('自动刷新（5 秒）')}
        </label>
        <div className="spacer" />
        <button type="button" className="btn btn-sm" disabled={!data || data.entries.length === 0} onClick={download}>
          {t('下载当前视图')}
        </button>
      </div>

      {data && (
        <p className="faint small" style={{ marginTop: 8 }}>
          {t('显示最近 {n} 条（缓冲区共 {total} 条，容量 {cap}）', {
            n: data.entries.length,
            total: data.total,
            cap: data.capacity,
          })}
          {data.dropped > 0 ? ` · ${t('更早的 {n} 条已被覆盖', { n: data.dropped })}` : ''}
        </p>
      )}

      {data && data.entries.length === 0 && !error && (
        <p className="muted">{t('没有符合条件的日志。')}</p>
      )}

      {data && data.entries.length > 0 && (
        <table className="logs">
          <tbody>
            {data.entries.map((e, i) => (
              <tr key={`${e.time}-${i}`}>
                <td className="faint small" style={{ whiteSpace: 'nowrap' }}>
                  {fmtTime(e.time)}
                </td>
                <td style={{ whiteSpace: 'nowrap' }}>
                  <span className={levelClass(e.level)}>{e.level}</span>
                </td>
                <td>
                  {e.msg}
                  {attrsText(e) && <div className="faint small mono">{attrsText(e)}</div>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
