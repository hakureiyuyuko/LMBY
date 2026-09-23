/**
 * 转码与硬件：本机编码能力表（设置 → 转码与硬件）。
 *
 * 数据来自 `GET /api/v1/transcode/capabilities` —— 里面每一条都是**真跑过**的结论
 * （拿 1 秒小样真编一遍再验证），所以它能回答「这台机器到底能不能硬解 HEVC」；
 * 而 `ffmpeg -encoders` 的列表回答不了（列着 qsv 不代表这台机器能用它）。
 *
 * 换机器、装/更新显卡驱动、升级 ffmpeg 之后，这里要手动重新探测一次才会更新。
 */
import { useCallback, useEffect, useState } from 'react';
import { ApiError, api } from '../api';
import { useI18n } from '../i18n';
import type { TranscodeBackend, TranscodeCapabilitiesPayload } from '../api';

const CODEC_NAMES: Record<string, string> = {
  h264: 'H.264',
  hevc: 'HEVC',
  av1: 'AV1',
  mpeg2video: 'MPEG-2',
};

/** 能力映射 → 「H.264 ✓ · HEVC ✗」这种一行文字。 */
function codecLine(m?: Record<string, boolean>): string {
  if (!m) return '—';
  const parts = Object.entries(m).map(([k, v]) => `${CODEC_NAMES[k] ?? k} ${v ? '✓' : '✗'}`);
  return parts.length ? parts.join(' · ') : '—';
}

function fmtTime(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
}

export function TranscodePanel({ isAdmin }: { isAdmin: boolean }) {
  const { t } = useI18n();
  const [data, setData] = useState<TranscodeCapabilitiesPayload | null>(null);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      setData(await api.capabilities());
      setError('');
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('读取编码能力失败'));
    }
  }, [t]);

  useEffect(() => {
    void load();
  }, [load]);

  async function refresh() {
    setBusy(true);
    setNotice('');
    setError('');
    try {
      const r = await api.refreshCapabilities();
      setData(r);
      setNotice(t('已重新探测（耗时 {ms} 毫秒）。', { ms: r.capabilities.elapsedMs }));
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('重新探测失败'));
    } finally {
      setBusy(false);
    }
  }

  const caps = data?.capabilities;
  const best = data?.best ?? null;
  const backends: TranscodeBackend[] = caps ? [caps.software, ...caps.backends] : [];
  // 失败记录只列**可用后端**的：不可用后端的每个编码都会失败，全列出来是噪声
  // （它们为什么不可用，上面「注意事项」与状态列已经说清了）。
  const failedNotes = backends
    .filter((b) => b.available)
    .flatMap((b) => (b.notes ?? []).map((n) => ({ name: b.name, note: n })));
  // 实测倍速是可选数据（探测时可能没测），一个都没有时干脆不显示这一列
  const hasSpeeds = Object.keys(caps?.speeds ?? {}).length > 0;

  return (
    <>
      <div className="card">
        <h2>{t('转码与硬件')}</h2>
        {error && <div className="alert alert-error">{error}</div>}
        {notice && <div className="alert alert-ok">{notice}</div>}
        {!caps && !error && <p className="muted">{t('正在读取…')}</p>}
        {caps && (
          <>
            <p>
              <span className={`badge ${best ? 'badge-ok' : 'badge-bad'}`}>
                {best ? t('当前后端：{name}', { name: best.name }) : t('没有可用的硬件后端，走软件编码')}
              </span>{' '}
              {best?.device && <span className="badge">{best.device}</span>}
            </p>
            <dl className="kv">
              <dt>{t('探测时间')}</dt>
              <dd>
                {fmtTime(caps.probedAt)} · {t('耗时 {ms} 毫秒', { ms: caps.elapsedMs })}
              </dd>
              <dt>{t('ffmpeg')}</dt>
              <dd>
                {caps.version} · {caps.ffmpeg}
              </dd>
              <dt>{t('设备节点')}</dt>
              <dd>{caps.devices?.join(', ') || '—'}</dd>
            </dl>
            {isAdmin && (
              <div className="row">
                <button type="button" className="btn btn-sm" disabled={busy} onClick={() => void refresh()}>
                  {busy ? t('正在重新探测…') : t('重新探测')}
                </button>
                <span className="faint small">{t('装好显卡驱动、换机器或升级 ffmpeg 之后点这里。')}</span>
              </div>
            )}
          </>
        )}
      </div>

      {caps && (
        <div className="card">
          <h2>{t('后端能力')}</h2>
          <table>
            <thead>
              <tr>
                <th>{t('后端')}</th>
                <th>{t('设备')}</th>
                <th>{t('状态')}</th>
                <th>{t('编码')}</th>
                <th>{t('解码')}</th>
                <th>{t('码率模式')}</th>
                {hasSpeeds && <th>{t('实测倍速')}</th>}
              </tr>
            </thead>
            <tbody>
              {backends.map((b) => {
                const speed = caps.speeds?.[`${b.kind}/h264`] ?? caps.speeds?.[`${b.kind}/hevc`];
                return (
                  <tr key={`${b.kind}-${b.name}`}>
                    <td>
                      {b.name}
                      {b.lowPower ? ` · ${t('低功耗')}` : ''}
                    </td>
                    <td className="faint small">{b.device || '—'}</td>
                    <td>
                      <span className={`badge ${b.available ? 'badge-ok' : 'badge-bad'}`}>
                        {b.available ? t('可用') : t('不可用')}
                      </span>
                    </td>
                    <td className="small">{codecLine(b.encode)}</td>
                    <td className="small">{codecLine(b.decode)}</td>
                    <td className="small">{(b.quality ?? []).join(' / ') || '—'}</td>
                    {hasSpeeds && (
                      <td className="small">{speed ? `${speed.toFixed(1)}x` : '—'}</td>
                    )}
                  </tr>
                );
              })}
            </tbody>
          </table>
          {caps.warnings?.length ? (
            <>
              <h3>{t('注意事项')}</h3>
              <ul className="small">
                {caps.warnings.map((w) => (
                  <li key={w}>{w}</li>
                ))}
              </ul>
            </>
          ) : null}
          {failedNotes.length > 0 && (
            <>
              <h3>{t('真跑失败的记录')}</h3>
              <ul className="small">
                {failedNotes.map((f) => (
                  <li key={`${f.name}-${f.note}`}>
                    <strong>{f.name}</strong>：{f.note}
                  </li>
                ))}
              </ul>
            </>
          )}
        </div>
      )}
    </>
  );
}
