/**
 * 关于（设置 → 关于）：版本信息 + 检查更新。
 *
 * 检查更新由**服务端**去查（默认本项目 GitHub 的 latest release）。理由：
 *  1. 这个服务的设计是「HTTP 是唯一出口、服务端是唯一联网方」，看片的人浏览器
 *     在什么网络环境（内网 / 没外网）不该决定服务端能不能检查更新；
 *  2. 服务端查得出问题、也说得清原因（连不上 / 超时 / 上游格式不对）；
 *  3. 结果能缓存，天然保护上游的匿名限流。
 *
 * 默认**不自动查**：只有点了按钮才出网。没有外网出口的机器不会因为打开设置页
 * 而刷失败日志，也不会白耗上游配额。
 */
import { useEffect, useState } from 'react';
import { api, type Meta, type UpdateInfo } from '../api';
import { useI18n } from '../i18n';

function fmtTime(iso?: string): string {
  if (!iso) return '';
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
}

export function AboutPanel() {
  const { t } = useI18n();
  const [meta, setMeta] = useState<Meta | null>(null);
  const [info, setInfo] = useState<UpdateInfo | null>(null);
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api.meta().then(setMeta).catch(() => setMeta(null));
  }, []);

  const check = async () => {
    setBusy(true);
    setErr('');
    try {
      setInfo(await api.checkUpdate(true));
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="card">
      <h2>{t('关于')}</h2>
      <p className="muted">
        {t('当前版本')}
        <span className="mono"> {meta?.versionFull || info?.current || '…'}</span>
      </p>

      <div className="row">
        <button className="btn" onClick={() => void check()} disabled={busy}>
          {busy ? t('正在检查…') : t('检查更新')}
        </button>
        {info?.releaseUrl && (
          <a className="btn btn-ghost" href={info.releaseUrl} target="_blank" rel="noreferrer">
            {t('打开发布页')}
          </a>
        )}
      </div>

      {err && <div className="alert alert-error">{t('检查更新失败：{msg}', { msg: err })}</div>}

      {info && <Outcome info={info} />}
    </div>
  );
}

function Outcome({ info }: { info: UpdateInfo }) {
  const { t } = useI18n();

  if (!info.enabled) {
    return <div className="alert">{t('这台服务没有配置更新源（config.toml 里的 [update] source_url 为空），所以不做检查。')}</div>;
  }
  if (!info.ok) {
    return <div className="alert alert-error">{t('没能查到最新版本：{msg}', { msg: info.error || '' })}</div>;
  }
  if (info.state === 'update-available') {
    return (
      <>
        <div className="alert alert-ok">
          {t('有新版本：{version}', { version: info.latest || '' })}
          {info.publishedAt && (
            <span className="muted"> · {t('发布于 {time}', { time: fmtTime(info.publishedAt) })}</span>
          )}
        </div>
        {info.notes && <p className="muted small">{info.notes}</p>}
        {info.assets && info.assets.length > 0 && (
          <p className="muted small">{t('下载：{assets}', { assets: info.assets.join(' · ') })}</p>
        )}
      </>
    );
  }
  if (info.state === 'dev') {
    return (
      <div className="alert">
        {t('当前是开发构建（不参与版本比较），最新发布是 {version}', { version: info.latest || '' })}
      </div>
    );
  }
  return (
    <>
      <div className="alert alert-ok">{t('已是最新版本（{version}）', { version: info.current })}</div>
      {info.cached && <p className="faint small">{t('（刚查过，这里是上次的结果）')}</p>}
    </>
  );
}
