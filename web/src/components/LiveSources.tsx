import { useCallback, useEffect, useRef, useState } from 'react';
import { ApiError, api } from '../api';
import { t, useI18n } from '../i18n';
import type { TVProbeStatus, TVSource } from '../api';

/**
 * 直播源管理（管理员）。
 *
 * 界面上的三条语义与后端一一对应，别把它们做成「看起来更顺」的样子：
 *   - **地址是频道的身份**：同一个地址只更新元数据，所以「刷新」永远不会
 *     把用户手动停用的频道又打开；
 *   - **删源不删频道**：删掉源之后那些频道还在（只是不再自动刷新），
 *     所以删除按钮的确认文案要这么说；
 *   - **粘贴/上传的源不能「刷新」**：它们只在导入那一刻有内容，要更新只能重新导入。
 *
 * 请求头（m3u 里 `地址|User-Agent=…` 那种）后端只说「有没有」，
 * 所以这里也只显示一个标记，不显示内容。
 */
/** 导入/刷新结果的统计串（两处用同一套口径，免得改了一处漏一处）。 */
function importSummary(im: {
  added: number;
  updated: number;
  kept: number;
  removed: number;
  total: number;
}): string {
  return t('新增 {added} / 更新 {updated} / 保留 {kept}{removed}（这一版共 {total} 条）', {
    added: im.added,
    updated: im.updated,
    kept: im.kept,
    removed: im.removed > 0 ? t(' / 清掉 {n}', { n: im.removed }) : '',
    total: im.total,
  });
}

export function LiveSourcePanel({ onImported }: { onImported: () => void }) {
  const { t } = useI18n();
  const [sources, setSources] = useState<TVSource[] | null>(null);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);
  const [open, setOpen] = useState(false);

  // 新建源
  const [kind, setKind] = useState<'url' | 'paste' | 'file'>('url');
  const [name, setName] = useState('');
  const [url, setUrl] = useState('');
  const [content, setContent] = useState('');
  const [interval, setIntervalMinutes] = useState(0);
  const [fileName, setFileName] = useState('');

  const load = useCallback(async () => {
    try {
      const d = await api.liveSources();
      setSources(d.sources);
      setError('');
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('读取直播源失败'));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function create() {
    setBusy(true);
    setError('');
    setNotice('');
    try {
      const body: {
        name: string;
        kind: 'paste' | 'file' | 'url';
        url?: string;
        content?: string;
        refreshIntervalMinutes?: number;
      } = {
        name: name.trim() || (kind === 'url' ? url.trim() : t('手动导入')),
        kind,
        refreshIntervalMinutes: interval,
      };
      if (kind === 'url') body.url = url.trim();
      else body.content = content;

      const res = await api.createLiveSource(body);
      setNotice(
        t('已导入「{name}」：{summary}', {
          name: res.source.name,
          summary: importSummary(res.import),
        }),
      );
      setName('');
      setUrl('');
      setContent('');
      setFileName('');
      await load();
      onImported();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('导入失败'));
    } finally {
      setBusy(false);
    }
  }

  async function refresh(src: TVSource) {
    setBusy(true);
    setError('');
    setNotice('');
    try {
      const res = await api.refreshLiveSource(src.id);
      setNotice(
        t('已刷新「{name}」：{summary}', {
          name: src.name,
          summary: importSummary(res.import),
        }),
      );
      await load();
      onImported();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('刷新失败'));
    } finally {
      setBusy(false);
    }
  }

  async function patch(src: TVSource, body: Parameters<typeof api.updateLiveSource>[1]) {
    setBusy(true);
    setError('');
    try {
      const updated = await api.updateLiveSource(src.id, body);
      setSources((prev) => prev?.map((s) => (s.id === updated.id ? updated : s)) ?? prev);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('保存失败'));
    } finally {
      setBusy(false);
    }
  }

  async function remove(src: TVSource) {
    if (
      !window.confirm(
        t('删除直播源「{name}」？\n\n它导入的 {count} 台频道会保留（只是不再自动刷新，也不会再被这次删除影响）。要连频道一起清掉得另外手动处理。', { name: src.name, count: src.channelCount }),
      )
    ) {
      return;
    }
    setBusy(true);
    setError('');
    setNotice('');
    try {
      await api.deleteLiveSource(src.id);
      setNotice(t('已删除直播源「{name}」', { name: src.name }));
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('删除失败'));
    } finally {
      setBusy(false);
    }
  }

  async function pickFile(file: File | undefined) {
    if (!file) return;
    try {
      setContent(await file.text());
      setFileName(file.name);
      setKind('file');
      setError('');
    } catch {
      setError(t('读文件失败'));
    }
  }

  return (
    <div className="card">
      <div className="row">
        <h2 style={{ margin: 0 }}>{t('直播源')}</h2>
        <div className="spacer" />
        <button type="button" className="btn btn-sm" onClick={() => setOpen((v) => !v)}>
          {open ? t('收起') : t('管理直播源')}
        </button>
      </div>

      {error && <div className="alert alert-error">{error}</div>}
      {notice && <div className="alert alert-ok">{notice}</div>}

      {open && (
        <>
          <h3>{t('导入播放列表')}</h3>
          <div className="seg">
            <button type="button" aria-pressed={kind === 'url'} onClick={() => setKind('url')}>
              {t('订阅地址')}
            </button>
            <button type="button" aria-pressed={kind === 'paste'} onClick={() => setKind('paste')}>
              {t('粘贴文本')}
            </button>
            <button type="button" aria-pressed={kind === 'file'} onClick={() => setKind('file')}>
              {t('上传文件')}
            </button>
          </div>

          <label className="field">
            <span>{t('名称（留空则用地址 / 「手动导入」）')}</span>
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={t('例如：重庆联通单播')}
            />
          </label>

          {kind === 'url' && (
            <label className="field">
              <span>{t('订阅地址')}</span>
              <input
                value={url}
                onChange={(e) => setUrl(e.target.value)}
                placeholder="http://…/playlist.m3u"
              />
            </label>
          )}

          {kind === 'paste' && (
            <label className="field">
              <span>{t('播放列表内容')}</span>
              <textarea
                rows={6}
                value={content}
                onChange={(e) => setContent(e.target.value)}
                placeholder="#EXTM3U&#10;#EXTINF:-1 group-title=&quot;News&quot;,Channel 1&#10;rtsp://…"
              />
            </label>
          )}

          {kind === 'file' && (
            <label className="field">
              <span>{t('播放列表文件（.m3u / .m3u8 / .txt）')}</span>
              <input
                type="file"
                accept=".m3u,.m3u8,.txt,text/plain,audio/x-mpegurl"
                onChange={(e) => void pickFile(e.target.files?.[0])}
              />
              {fileName && (
                <span className="faint small">
                  {t('已读入 {name}（{bytes} 字节）', { name: fileName, bytes: content.length })}
                </span>
              )}
            </label>
          )}

          <label className="field">
            <span>{t('自动刷新间隔（分钟，0 = 不自动刷新）')}</span>
            <input
              type="number"
              min={0}
              value={interval}
              onChange={(e) => setIntervalMinutes(Math.max(0, Number(e.target.value) || 0))}
            />
          </label>

          <div className="row">
            <button
              type="button"
              className="btn btn-primary"
              disabled={busy || (kind === 'url' ? !url.trim() : !content.trim())}
              onClick={() => void create()}
            >
              {busy ? t('导入中…') : t('导入')}
            </button>
          </div>

          <h3>{t('已有的源（{n}）', { n: sources?.length ?? 0 })}</h3>
          {sources === null && <p className="muted">{t('正在读取…')}</p>}
          {sources?.length === 0 && (
            <p className="muted">{t('还没有任何直播源：上面导入一份播放列表就能看电视了。')}</p>
          )}
          {sources && sources.length > 0 && (
            <ul className="tv-src-list">
              {sources.map((src) => (
                <li key={src.id} className="tv-src-row">
                  <span className="tv-src-main">
                    <strong>{src.name}</strong>
                    <span className="faint small">
                      {kindLabel(src.kind)} · {t('频道 {n}', { n: src.channelCount })}
                      {src.url ? ` · ${src.url}` : ''}
                    </span>
                    <span className="faint small">
                      {src.lastRefreshAt
                        ? t('上次刷新 {when}：{status}（这一版 {n} 条）', {
                            when: new Date(src.lastRefreshAt).toLocaleString(),
                            status: src.lastStatus || '—',
                            n: src.lastChannelCount,
                          })
                        : t('还没刷新过')}
                    </span>
                  </span>
                  <span className="tv-src-actions">
                    <label className="field-inline" title={t('自动刷新间隔（分钟），0 = 不自动刷新')}>
                      <span>{t('间隔')}</span>
                      <input
                        type="number"
                        min={0}
                        style={{ width: 70 }}
                        defaultValue={src.refreshIntervalMinutes}
                        disabled={busy}
                        onBlur={(e) => {
                          const v = Math.max(0, Number(e.target.value) || 0);
                          if (v !== src.refreshIntervalMinutes) void patch(src, { refreshIntervalMinutes: v });
                        }}
                      />
                    </label>
                    <button
                      type="button"
                      className="btn btn-sm"
                      disabled={busy || !src.enabled || !src.url}
                      title={src.url ? t('按地址重新拉取并增量更新') : t('粘贴/上传的源没有可重拉的地址')}
                      onClick={() => void refresh(src)}
                    >
                      {t('刷新')}
                    </button>
                    <button
                      type="button"
                      className="btn btn-sm"
                      disabled={busy}
                      onClick={() => void patch(src, { enabled: !src.enabled })}
                    >
                      {src.enabled ? t('停用') : t('启用')}
                    </button>
                    <button type="button" className="btn btn-sm btn-ghost" disabled={busy} onClick={() => void remove(src)}>
                      {t('删除')}
                    </button>
                  </span>
                </li>
              ))}
            </ul>
          )}
        </>
      )}
    </div>
  );
}

/**
 * 频道探测（失效源标记，管理员）。
 *
 * 两条要写在界面上的话：
 *   - 探测是**真连源站**（每个频道一次），全量以分钟计，所以进度从库里现算、
 *     可以关掉页面再回来看；
 *   - 结果是**上一次真连的结果**，会随源站状态变 —— 想更新就再探一次。
 */
export function LiveProbePanel({ onFinished }: { onFinished: () => void }) {
  const { t } = useI18n();
  const [st, setSt] = useState<TVProbeStatus | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    try {
      setSt(await api.liveProbeStatus());
      setError('');
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('读取探测进度失败'));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  // 探测在跑的时候轮询进度（服务端是后台任务，这里只管看）
  useEffect(() => {
    if (!st?.running) return;
    const timer = window.setInterval(() => void load(), 2000);
    return () => window.clearInterval(timer);
  }, [st?.running, load]);

  // 跑完时把频道列表刷一遍（失效标记变了）
  const wasRunning = useRef(false);
  useEffect(() => {
    if (wasRunning.current && st && !st.running) onFinished();
    wasRunning.current = Boolean(st?.running);
  }, [st, onFinished]);

  async function start(onlyUnknown: boolean) {
    setBusy(true);
    setError('');
    try {
      await api.startLiveProbe({ onlyUnknown });
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('起探测失败'));
    } finally {
      setBusy(false);
    }
  }

  const stats = st?.stats;
  const p = st?.progress;

  return (
    <div className="card">
      <h2>{t('频道探测（失效源标记）')}</h2>

      {error && <div className="alert alert-error">{error}</div>}

      {stats && (
        <p className="small">
          {t('启用中的频道 {total} 台：能通 {ok} · 失效 {failed} · 没探过 {pending}', {
            total: stats.total,
            ok: stats.ok,
            failed: stats.failed,
            pending: stats.pending,
          })}
        </p>
      )}

      {st?.running && p && (
        <div className="alert alert-ok">
          {t('正在探测 {done}/{total}（已探完中：通 {ok} / 不通 {failed}）· 已用 {sec} 秒', {
            done: p.done,
            total: p.total,
            ok: p.ok,
            failed: p.failed,
            sec: st.elapsedSeconds,
          })}
          {p.current ? t(' · 当前：{name}', { name: p.current }) : ''}
        </div>
      )}

      <div className="row">
        <button
          type="button"
          className="btn"
          disabled={busy || st?.running || (stats ? stats.pending === 0 : false)}
          title={stats && stats.pending === 0 ? t('所有频道都探过了') : t('只探「没探过」的那些')}
          onClick={() => void start(true)}
        >
          {stats ? t('补探没探过的（{n}）', { n: stats.pending }) : t('补探没探过的')}
        </button>
        <button
          type="button"
          className="btn"
          disabled={busy || st?.running}
          title={t('每个启用的频道都重新连一次（源站地址没变但源站坏了，只有重探才知道）')}
          onClick={() => void start(false)}
        >
          {t('重探全部')}
        </button>
        <div className="spacer" />
        <button type="button" className="btn btn-sm btn-ghost" disabled={busy} onClick={() => void load()}>
          {t('刷新进度')}
        </button>
      </div>
    </div>
  );
}

function kindLabel(kind: string): string {
  switch (kind) {
    case 'url':
      return t('订阅地址');
    case 'file':
      return t('上传文件');
    case 'paste':
      return t('粘贴文本');
  }
  return kind;
}
