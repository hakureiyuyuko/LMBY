/**
 * 频道管理（**只在设置里**）—— 直播页只管看，改频道的事都在这儿。
 *
 * 这里管的是频道的「怎么用」：名字 / 分组 / 排序 / logo、停用与启用、
 * 复制地址、生成 24 小时免登录外链。
 *
 * ⚠️ 这些改动**会被下一次刷新订阅源覆盖**（播放列表才是频道的来源）；
 * 「停用状态」与「收藏」不会被覆盖 —— 界面上写明了，免得用户白改。
 * 探测失效源（那个是真连一次源站）不在这儿，在同一个页签的探测卡片里。
 */
import { useCallback, useEffect, useState } from 'react';
import { ApiError, api } from '../api';
import { useI18n } from '../i18n';
import type { TVChannel } from '../api';

export function ChannelManager({ onChanged }: { onChanged?: () => void }) {
  const { t } = useI18n();
  const [channels, setChannels] = useState<TVChannel[] | null>(null);
  const [q, setQ] = useState('');
  const [qInput, setQInput] = useState('');
  const [editing, setEditing] = useState<TVChannel | null>(null);
  const [draft, setDraft] = useState({ name: '', group: '', sortOrder: 0, logo: '' });
  const [message, setMessage] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const data = await api.liveChannels(q ? { q } : {});
      setChannels(data.channels);
      setError('');
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('读取频道失败'));
    }
  }, [q, t]);

  useEffect(() => {
    void load();
  }, [load]);

  // 搜索防抖：边打字边发请求会把 150 台的列表打爆
  useEffect(() => {
    const timer = window.setTimeout(() => setQ(qInput.trim()), 300);
    return () => window.clearTimeout(timer);
  }, [qInput]);

  async function run(fn: () => Promise<string>) {
    setBusy(true);
    setMessage('');
    setError('');
    try {
      setMessage(await fn());
      await load();
      onChanged?.();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('操作失败'));
    } finally {
      setBusy(false);
    }
  }

  function startEdit(ch: TVChannel) {
    setEditing(ch);
    setDraft({
      name: ch.name,
      group: ch.group ?? '',
      sortOrder: ch.sortOrder ?? 0,
      logo: ch.logo ?? '',
    });
  }

  return (
    <div className="card">
      <h2>{t('频道管理')}</h2>
      <p className="hint">
        {t('这里改的是「怎么用」（名字 / 分组 / 排序 / logo）与「启用状态」。改名与分组下次刷新订阅源时会按播放列表还原 —— 播放列表才是频道的来源；「启用状态」与「收藏」不会被刷新覆盖。')}
      </p>
      {error && <div className="alert alert-error">{error}</div>}
      {message && <div className="alert">{message}</div>}

      <div className="row">
        <input
          value={qInput}
          onChange={(e) => setQInput(e.target.value)}
          placeholder={t('搜频道名…')}
        />
        <div className="spacer" />
        <span className="faint small">{t('共 {n} 台', { n: channels?.length ?? 0 })}</span>
        {/* 原来的「外部播放器」入口已从前台拿掉；导出 m3u 这件事本身还在，放这儿 */}
        <a className="btn btn-sm" href={api.liveExportURL()} download>
          {t('导出 m3u（全部频道）')}
        </a>
      </div>

      {!channels && <p className="muted">{t('正在读取…')}</p>}
      {channels && channels.length === 0 && <p className="muted">{t('没有匹配的频道。')}</p>}

      {channels &&
        channels.map((ch) => (
          <div key={ch.id}>
            <div className="row" style={{ marginBottom: 4 }}>
              <span className="tv-ch-name" title={ch.name}>
                {ch.name}
              </span>
              <span className="faint small">
                {ch.group || t('未分组')} · {ch.kind.toUpperCase()}
              </span>
              {ch.disabled && <span className="badge">{t('已停用')}</span>}
              {ch.probeOk === false && (
                <span className="badge badge-bad" title={ch.probe || t('上次探测不通')}>
                  {t('失效')}
                </span>
              )}
              <div className="spacer" />
              <button
                type="button"
                className="btn btn-sm"
                disabled={busy}
                onClick={() => startEdit(ch)}
              >
                {t('编辑')}
              </button>
              <button
                type="button"
                className="btn btn-sm"
                disabled={busy}
                onClick={() =>
                  void run(async () => {
                    await api.updateLiveChannel(ch.id, { disabled: !ch.disabled });
                    return ch.disabled
                      ? t('已启用「{name}」', { name: ch.name })
                      : t('已停用「{name}」（不再出现在「只看启用」里）', { name: ch.name });
                  })
                }
              >
                {ch.disabled ? t('启用') : t('停用')}
              </button>
              <button
                type="button"
                className="btn btn-sm"
                disabled={busy}
                onClick={() =>
                  void run(async () => {
                    const r = await api.shareLiveChannel(ch.id);
                    return t('外链（24 小时有效，无需登录）：{url}', { url: r.url });
                  })
                }
                title={t('生成 24 小时有效的免登录链接')}
              >
                {t('外链')}
              </button>
              <button
                type="button"
                className="btn btn-sm btn-ghost"
                onClick={async () => {
                  try {
                    await navigator.clipboard.writeText(ch.url);
                    setMessage(t('已复制「{name}」的地址', { name: ch.name }));
                  } catch {
                    setMessage(t('「{name}」的地址：{url}', { name: ch.name, url: ch.url }));
                  }
                }}
              >
                {t('复制地址')}
              </button>
            </div>

            {editing?.id === ch.id && (
              <div className="row" style={{ marginBottom: 8, flexWrap: 'wrap' }}>
                <label className="field">
                  <span>{t('名称')}</span>
                  <input
                    value={draft.name}
                    onChange={(e) => setDraft({ ...draft, name: e.target.value })}
                  />
                </label>
                <label className="field">
                  <span>{t('分组')}</span>
                  <input
                    value={draft.group}
                    onChange={(e) => setDraft({ ...draft, group: e.target.value })}
                  />
                </label>
                <label className="field">
                  <span>{t('排序（同分组内，小的在前）')}</span>
                  <input
                    type="number"
                    value={draft.sortOrder}
                    onChange={(e) => setDraft({ ...draft, sortOrder: Number(e.target.value) })}
                  />
                </label>
                <label className="field">
                  <span>{t('logo 地址')}</span>
                  <input
                    value={draft.logo}
                    onChange={(e) => setDraft({ ...draft, logo: e.target.value })}
                  />
                </label>
                <button
                  type="button"
                  className="btn btn-sm btn-primary"
                  disabled={busy}
                  onClick={() =>
                    void run(async () => {
                      await api.updateLiveChannel(ch.id, draft);
                      setEditing(null);
                      return t('已保存「{name}」', { name: draft.name.trim() || ch.name });
                    })
                  }
                >
                  {t('保存')}
                </button>
                <button type="button" className="btn btn-sm" onClick={() => setEditing(null)}>
                  {t('取消')}
                </button>
              </div>
            )}
          </div>
        ))}
    </div>
  );
}
