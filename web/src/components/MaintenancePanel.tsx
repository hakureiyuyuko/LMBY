/**
 * 缓存与清理（设置 → 缓存与清理）。
 *
 * 这一页只处理**缓存性质**的东西：图片缓存（能重新下载）、叠加层里的孤儿
 * （库/条目已经不在库里）、以及「看一眼」转码分片与探测工作目录的占用。
 *
 * 两条边界写在界面上，免得有人以为这里是「清磁盘」：
 *  - 绝不碰媒体文件；
 *  - 叠加层里**还在库里的条目**一个字节都不动（那是刮削产物的备份，不是缓存）。
 */
import { useCallback, useEffect, useState } from 'react';
import type { ReactNode } from 'react';
import { ApiError, api } from '../api';
import { useI18n } from '../i18n';
import type { CacheUsage, MaintenancePayload } from '../api';

/** 字节数说成人话。 */
function fmtBytes(n: number): string {
  if (!n) return '0 B';
  if (n >= 1 << 30) return `${(n / (1 << 30)).toFixed(1)} GB`;
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MB`;
  if (n >= 1 << 10) return `${(n / (1 << 10)).toFixed(1)} KB`;
  return `${n} B`;
}

export function MaintenancePanel() {
  const { t } = useI18n();
  const [data, setData] = useState<MaintenancePayload | null>(null);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      setData(await api.maintenance());
      setError('');
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('读取缓存占用失败'));
    }
  }, [t]);

  useEffect(() => {
    void load();
  }, [load]);

  /** 执行清理并如实报告清掉了多少（不报告的话，用户不知道按钮起作用没有）。 */
  async function clean(body: { images?: boolean; overlayOrphans?: boolean }) {
    const label = body.images ? t('图片缓存') : t('叠加层孤儿');
    if (body.images && !window.confirm(t('清空图片缓存后，下次访问会重新生成或重新下载。继续？'))) {
      return;
    }
    setBusy(true);
    setNotice('');
    setError('');
    try {
      const r = await api.cleanMaintenance(body);
      const one = Object.values(r.cleaned)[0];
      setNotice(
        one
          ? t('已清理「{what}」：{files} 个文件 / {size}', {
              what: label,
              files: one.files,
              size: fmtBytes(one.bytes),
            })
          : t('没有需要清理的内容。'),
      );
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('清理失败'));
    } finally {
      setBusy(false);
    }
  }

  const rows: { key: string; title: string; usage: CacheUsage; action?: ReactNode }[] = data
    ? [
        {
          key: 'images',
          title: t('图片缓存'),
          usage: data.images,
          action: (
            <button
              type="button"
              className="btn btn-sm"
              disabled={busy || data.images.files === 0}
              onClick={() => void clean({ images: true })}
            >
              {t('清空')}
            </button>
          ),
        },
        {
          key: 'streams',
          title: t('转码分片'),
          usage: data.streams,
        },
        {
          key: 'probe',
          title: t('探测工作目录'),
          usage: data.probe,
        },
        {
          key: 'overlay',
          title: t('叠加层'),
          usage: data.overlay,
        },
        {
          key: 'overlayOrphans',
          title: t('叠加层孤儿'),
          usage: data.overlayOrphans,
          action: (
            <button
              type="button"
              className="btn btn-sm"
              disabled={busy || data.overlayOrphans.files === 0}
              onClick={() => void clean({ overlayOrphans: true })}
            >
              {t('清理孤儿')}
            </button>
          ),
        },
      ]
    : [];

  return (
    <div className="card">
      <h2>{t('缓存与清理')}</h2>
      {error && <div className="alert alert-error">{error}</div>}
      {notice && <div className="alert alert-ok">{notice}</div>}
      <p className="faint small">
        {t('这里只处理缓存：图片缓存可以清空（下次访问重新生成），叠加层里「已经不在库里的条目」可以清掉。媒体文件本身以及还在库里的条目，这里一个字节都不会动。')}
      </p>
      {!data && !error && <p className="muted">{t('正在读取…')}</p>}
      {data && (
        <table>
          <thead>
            <tr>
              <th>{t('项目')}</th>
              <th>{t('占用')}</th>
              <th>{t('说明')}</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.key}>
                <td className="small">{r.title}</td>
                <td className="small mono">
                  {r.usage.files} {t('个文件')} / {fmtBytes(r.usage.bytes)}
                </td>
                <td className="small faint">{r.usage.note}</td>
                <td>{r.action}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <div className="row">
        <button type="button" className="btn btn-sm" disabled={busy} onClick={() => void load()}>
          {busy ? t('读取中…') : t('刷新')}
        </button>
      </div>
    </div>
  );
}
