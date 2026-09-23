/**
 * 扫描计划：每个库多久扫一次（设置 → 扫描计划）。
 *
 * 设计取舍：**间隔属于库**，不属于这里 —— 界面只负责「把库上的那个数字改掉」，
 * 真正的判定在服务端（调度器每隔一会儿问一句「谁到期了」，见 store.ListLibrariesDueForScan）。
 * 所以改完立刻生效、重启也不丢，界面上不需要维护任何定时状态。
 */
import { useCallback, useEffect, useMemo, useState } from 'react';
import { ApiError, api } from '../api';
import { useI18n } from '../i18n';
import type { LibrarySummary } from '../api';

/** 把时间点说成人话：优先「多久以前 / 多久以后」，太远才落到具体时间。 */
function relTime(iso: string | undefined, t: (k: string, p?: Record<string, string | number>) => string): string {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  const diff = Date.now() - d.getTime();
  if (diff < 0) {
    const mins = Math.round(-diff / 60000);
    if (mins <= 1) return t('马上');
    if (mins < 60) return t('{n} 分钟后', { n: mins });
    const hours = Math.round(mins / 60);
    if (hours < 24) return t('{n} 小时后', { n: hours });
    return d.toLocaleString();
  }
  const mins = Math.round(diff / 60000);
  if (mins <= 1) return t('刚刚');
  if (mins < 60) return t('{n} 分钟前', { n: mins });
  const hours = Math.round(mins / 60);
  if (hours < 24) return t('{n} 小时前', { n: hours });
  return d.toLocaleString();
}

export function ScanPlanPanel() {
  const { t } = useI18n();
  const [libs, setLibs] = useState<LibrarySummary[] | null>(null);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [busyId, setBusyId] = useState<number | null>(null);

  const load = useCallback(async () => {
    try {
      const l = await api.libraries();
      setLibs(l.libraries);
      setError('');
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('读取媒体库失败'));
    }
  }, [t]);

  useEffect(() => {
    void load();
  }, [load]);

  const options = useMemo(
    () => [
      { value: 0, label: t('不自动') },
      { value: 60, label: t('每小时') },
      { value: 360, label: t('每 6 小时') },
      { value: 1440, label: t('每天') },
      { value: 10080, label: t('每周') },
    ],
    [t],
  );

  async function setIntervalMinutes(lib: LibrarySummary, minutes: number) {
    setError('');
    setNotice('');
    try {
      await api.updateLibrary(lib.id, { scanIntervalMinutes: minutes });
      setNotice(
        minutes > 0
          ? t('已把「{name}」设为{interval}扫一次。', {
              name: lib.name,
              interval: options.find((o) => o.value === minutes)?.label ?? `${minutes} 分钟`,
            })
          : t('已把「{name}」设为不自动扫描。', { name: lib.name }),
      );
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('保存扫描计划失败'));
    }
  }

  async function scanNow(lib: LibrarySummary) {
    setError('');
    setNotice('');
    setBusyId(lib.id);
    try {
      await api.startScan(lib.id);
      setNotice(t('已开始扫描「{name}」，进度在「库管理」里看。', { name: lib.name }));
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('启动扫描失败'));
    } finally {
      setBusyId(null);
    }
  }

  return (
    <div className="card">
      <h2>{t('扫描计划')}</h2>
      {error && <div className="alert alert-error">{error}</div>}
      {notice && <div className="alert alert-ok">{notice}</div>}
      <p className="faint small">
        {t('扫描间隔是每个媒体库自己的设置。服务端会定期检查有没有库到期，到点就自动扫一次；不会因为「新加了一个文件」而立刻醒来（那是文件系统监控的事，属于二期）。')}
      </p>

      {!libs && !error && <p className="muted">{t('正在读取…')}</p>}
      {libs && libs.length === 0 && (
        <p className="muted">{t('还没有媒体库 —— 先去「库管理」加一个。')}</p>
      )}

      {libs && libs.length > 0 && (
        <table>
          <thead>
            <tr>
              <th>{t('媒体库')}</th>
              <th>{t('扫描间隔')}</th>
              <th>{t('上次扫描')}</th>
              <th>{t('下次扫描')}</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {libs.map((lib) => (
              <tr key={lib.id}>
                <td>{lib.name}</td>
                <td>
                  <select
                    value={lib.scanIntervalMinutes ?? 0}
                    aria-label={t('扫描间隔')}
                    onChange={(e) => void setIntervalMinutes(lib, Number(e.target.value))}
                  >
                    {options.map((o) => (
                      <option key={o.value} value={o.value}>
                        {o.label}
                      </option>
                    ))}
                    {/* 库里存着别的值（配置文件/接口直接设的）时不要把它吃掉 */}
                    {![0, 60, 360, 1440, 10080].includes(lib.scanIntervalMinutes ?? 0) && (
                      <option value={lib.scanIntervalMinutes ?? 0}>
                        {t('每 {n} 分钟', { n: lib.scanIntervalMinutes ?? 0 })}
                      </option>
                    )}
                  </select>
                </td>
                <td className="small faint">{relTime(lib.lastScanAt, t)}</td>
                <td className="small faint">
                  {lib.scanIntervalMinutes ? relTime(lib.nextScanAt, t) : '—'}
                </td>
                <td className="right">
                  <button
                    type="button"
                    className="btn btn-sm"
                    disabled={lib.scanRunning || busyId === lib.id}
                    onClick={() => void scanNow(lib)}
                  >
                    {lib.scanRunning ? t('扫描中…') : t('立即扫描')}
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
