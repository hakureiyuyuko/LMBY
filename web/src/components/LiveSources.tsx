import { useCallback, useEffect, useRef, useState } from 'react';
import { ApiError, api } from '../api';
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
export function LiveSourcePanel({ onImported }: { onImported: () => void }) {
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
      setError(e instanceof ApiError ? e.message : '读取直播源失败');
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
        name: name.trim() || (kind === 'url' ? url.trim() : '手动导入'),
        kind,
        refreshIntervalMinutes: interval,
      };
      if (kind === 'url') body.url = url.trim();
      else body.content = content;

      const res = await api.createLiveSource(body);
      const im = res.import;
      setNotice(
        `已导入「${res.source.name}」：新增 ${im.added} / 更新 ${im.updated} / 保留 ${im.kept}` +
          `${im.removed > 0 ? ` / 清掉 ${im.removed}` : ''}（这一版共 ${im.total} 条）`,
      );
      setName('');
      setUrl('');
      setContent('');
      setFileName('');
      await load();
      onImported();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : '导入失败');
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
      const im = res.import;
      setNotice(
        `已刷新「${src.name}」：新增 ${im.added} / 更新 ${im.updated} / 保留 ${im.kept}` +
          `${im.removed > 0 ? ` / 清掉 ${im.removed}` : ''}（这一版共 ${im.total} 条）`,
      );
      await load();
      onImported();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : '刷新失败');
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
      setError(e instanceof ApiError ? e.message : '保存失败');
    } finally {
      setBusy(false);
    }
  }

  async function remove(src: TVSource) {
    if (
      !window.confirm(
        `删除直播源「${src.name}」？\n\n它导入的 ${src.channelCount} 台频道会保留（只是不再自动刷新，` +
          `也不会再被这次删除影响）。要连频道一起清掉得另外手动处理。`,
      )
    ) {
      return;
    }
    setBusy(true);
    setError('');
    setNotice('');
    try {
      await api.deleteLiveSource(src.id);
      setNotice(`已删除直播源「${src.name}」`);
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : '删除失败');
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
      setError('读文件失败');
    }
  }

  return (
    <div className="card">
      <div className="row">
        <h2 style={{ margin: 0 }}>直播源</h2>
        <div className="spacer" />
        <button type="button" className="btn btn-sm" onClick={() => setOpen((v) => !v)}>
          {open ? '收起' : '管理直播源'}
        </button>
      </div>

      {error && <div className="alert alert-error">{error}</div>}
      {notice && <div className="alert alert-ok">{notice}</div>}

      {!open && (
        <p className="hint">
          订阅源会按各自的间隔自动刷新（间隔 0 = 只手动刷）。展开可以导入新的播放列表、
          改间隔、看上次刷新结果。
        </p>
      )}

      {open && (
        <>
          <h3>导入播放列表</h3>
          <div className="seg">
            <button type="button" aria-pressed={kind === 'url'} onClick={() => setKind('url')}>
              订阅地址
            </button>
            <button type="button" aria-pressed={kind === 'paste'} onClick={() => setKind('paste')}>
              粘贴文本
            </button>
            <button type="button" aria-pressed={kind === 'file'} onClick={() => setKind('file')}>
              上传文件
            </button>
          </div>

          <label className="field">
            <span>名称（留空则用地址 / 「手动导入」）</span>
            <input value={name} onChange={(e) => setName(e.target.value)} placeholder="例如：重庆联通单播" />
          </label>

          {kind === 'url' && (
            <label className="field">
              <span>订阅地址</span>
              <input
                value={url}
                onChange={(e) => setUrl(e.target.value)}
                placeholder="http://…/playlist.m3u"
              />
            </label>
          )}

          {kind === 'paste' && (
            <label className="field">
              <span>播放列表内容</span>
              <textarea
                rows={6}
                value={content}
                onChange={(e) => setContent(e.target.value)}
                placeholder="#EXTM3U&#10;#EXTINF:-1 group-title=&quot;央视&quot;,CCTV1 综合&#10;rtsp://…"
              />
            </label>
          )}

          {kind === 'file' && (
            <label className="field">
              <span>播放列表文件（.m3u / .m3u8 / .txt）</span>
              <input
                type="file"
                accept=".m3u,.m3u8,.txt,text/plain,audio/x-mpegurl"
                onChange={(e) => void pickFile(e.target.files?.[0])}
              />
              {fileName && (
                <span className="faint small">
                  已读入 {fileName}（{content.length} 字节）
                </span>
              )}
            </label>
          )}

          <label className="field">
            <span>自动刷新间隔（分钟，0 = 不自动刷新）</span>
            <input
              type="number"
              min={0}
              value={interval}
              onChange={(e) => setIntervalMinutes(Math.max(0, Number(e.target.value) || 0))}
            />
          </label>
          <p className="faint small" style={{ marginTop: -8 }}>
            粘贴/上传的源只在导入这一刻有内容，所以它们的「刷新」按钮不会有（要更新就再导入一次）。
          </p>

          <div className="row">
            <button
              type="button"
              className="btn btn-primary"
              disabled={busy || (kind === 'url' ? !url.trim() : !content.trim())}
              onClick={() => void create()}
            >
              {busy ? '导入中…' : '导入'}
            </button>
          </div>

          <h3>已有的源（{sources?.length ?? 0}）</h3>
          {sources === null && <p className="muted">正在读取…</p>}
          {sources?.length === 0 && <p className="muted">还没有任何直播源：上面导入一份播放列表就能看电视了。</p>}
          {sources && sources.length > 0 && (
            <ul className="tv-src-list">
              {sources.map((src) => (
                <li key={src.id} className="tv-src-row">
                  <span className="tv-src-main">
                    <strong>{src.name}</strong>
                    <span className="faint small">
                      {kindLabel(src.kind)} · 频道 {src.channelCount}
                      {src.url ? ` · ${src.url}` : ''}
                    </span>
                    <span className="faint small">
                      {src.lastRefreshAt
                        ? `上次刷新 ${new Date(src.lastRefreshAt).toLocaleString()}：${src.lastStatus || '—'}（这一版 ${src.lastChannelCount} 条）`
                        : '还没刷新过'}
                    </span>
                  </span>
                  <span className="tv-src-actions">
                    <label className="field-inline" title="自动刷新间隔（分钟），0 = 不自动刷新">
                      <span>间隔</span>
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
                      title={src.url ? '按地址重新拉取并增量更新' : '粘贴/上传的源没有可重拉的地址'}
                      onClick={() => void refresh(src)}
                    >
                      刷新
                    </button>
                    <button
                      type="button"
                      className="btn btn-sm"
                      disabled={busy}
                      onClick={() => void patch(src, { enabled: !src.enabled })}
                    >
                      {src.enabled ? '停用' : '启用'}
                    </button>
                    <button type="button" className="btn btn-sm btn-ghost" disabled={busy} onClick={() => void remove(src)}>
                      删除
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
  const [st, setSt] = useState<TVProbeStatus | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    try {
      setSt(await api.liveProbeStatus());
      setError('');
    } catch (e) {
      setError(e instanceof ApiError ? e.message : '读取探测进度失败');
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  // 探测在跑的时候轮询进度（服务端是后台任务，这里只管看）
  useEffect(() => {
    if (!st?.running) return;
    const t = window.setInterval(() => void load(), 2000);
    return () => window.clearInterval(t);
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
      setError(e instanceof ApiError ? e.message : '起探测失败');
    } finally {
      setBusy(false);
    }
  }

  const stats = st?.stats;
  const p = st?.progress;

  return (
    <div className="card">
      <h2>频道探测（失效源标记）</h2>
      <p className="hint">
        真连一次源站看它出不出得来流（死源常常 TCP 连得上，是在协议握手那一步挂掉的），
        结果写回每条频道的「通 / 失效」。全量探测每个频道都要连一次，以分钟计；
        关掉页面不会中断，回来接着看进度。
      </p>

      {error && <div className="alert alert-error">{error}</div>}

      {stats && (
        <p className="small">
          启用中的频道 <strong>{stats.total}</strong> 台：能通 <strong>{stats.ok}</strong> · 失效{' '}
          <strong>{stats.failed}</strong> · 没探过 <strong>{stats.pending}</strong>
        </p>
      )}

      {st?.running && p && (
        <div className="alert alert-ok">
          正在探测 {p.done}/{p.total}（已探完中：通 {p.ok} / 不通 {p.failed}）· 已用 {st.elapsedSeconds} 秒
          {p.current ? ` · 当前：${p.current}` : ''}
        </div>
      )}

      <div className="row">
        <button
          type="button"
          className="btn"
          disabled={busy || st?.running || (stats ? stats.pending === 0 : false)}
          title={stats && stats.pending === 0 ? '所有频道都探过了' : '只探「没探过」的那些'}
          onClick={() => void start(true)}
        >
          补探没探过的{stats ? `（${stats.pending}）` : ''}
        </button>
        <button
          type="button"
          className="btn"
          disabled={busy || st?.running}
          title="每个启用的频道都重新连一次（源站地址没变但源站坏了，只有重探才知道）"
          onClick={() => void start(false)}
        >
          重探全部
        </button>
        <div className="spacer" />
        <button type="button" className="btn btn-sm btn-ghost" disabled={busy} onClick={() => void load()}>
          刷新进度
        </button>
      </div>
    </div>
  );
}

function kindLabel(kind: string): string {
  switch (kind) {
    case 'url':
      return '订阅地址';
    case 'file':
      return '上传文件';
    case 'paste':
      return '粘贴文本';
  }
  return kind;
}
