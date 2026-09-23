import { useCallback, useEffect, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { ApiError, api } from '../api';
import { t, useI18n } from '../i18n';
import { kindLabel as itemKindLabel } from '../media';
import type { Item, LibrarySummary, ScanIssue, ScanProgress } from '../api';

/** 媒体库类型（movie / tv / homevideo / mixed）→ 界面标签。
 * 写成**函数而不是常量表**：标签要在渲染时取当前语言，
 * 模块级常量会在 import 时就把语言钉死（切语言后不跟着变）。 */
function libraryKindOptions(): { value: string; label: string }[] {
  return [
    { value: 'movie', label: t('电影') },
    { value: 'tv', label: t('剧集') },
    { value: 'homevideo', label: t('家庭视频') },
    { value: 'mixed', label: t('混合（推荐）') },
  ];
}

export function Libraries() {
  const { t } = useI18n();
  const [libraries, setLibraries] = useState<LibrarySummary[] | null>(null);
  const [selected, setSelected] = useState<number | null>(null);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [progress, setProgress] = useState<Record<number, ScanProgress>>({});
  /** 正在编辑的库（一行内展开一个表单：改类型 / 改根路径）。 */
  const [editing, setEditing] = useState<LibrarySummary | null>(null);

  const load = useCallback(async () => {
    try {
      const res = await api.libraries();
      setLibraries(res.libraries);
      setError('');
      if (res.libraries.length > 0 && selected === null) {
        setSelected(res.libraries[0].id);
      }
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('读取媒体库失败'));
    }
  }, [selected]);

  useEffect(() => {
    void load();
  }, [load]);

  // SSE：实时接收扫描进度（EventSource 自带断线重连）
  useEffect(() => {
    const es = new EventSource('/api/v1/events');
    const onScan = (ev: MessageEvent) => {
      try {
        const p = JSON.parse(ev.data) as ScanProgress;
        setProgress((prev) => ({ ...prev, [p.libraryId]: p }));
        if (p.phase === 'done') {
          void load();
        }
      } catch {
        /* 忽略无法解析的事件 */
      }
    };
    es.addEventListener('scan', onScan);
    es.onerror = () => {
      /* 交给 EventSource 自动重连 */
    };
    return () => {
      es.removeEventListener('scan', onScan);
      es.close();
    };
  }, [load]);

  async function startScan(id: number) {
    try {
      await api.startScan(id);
      setError('');
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('启动扫描失败'));
    }
  }

  async function cancelScan(id: number) {
    try {
      await api.cancelScan(id);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('取消扫描失败'));
    }
  }

  /**
   * 切换「只读」（网盘 / 只读挂载）。
   *
   * 打开后：LMBY 一个字节也不往库目录里写，刮削到的元数据快照与图片落进
   * 数据目录的 overlay 层（每库一块，不参与图片缓存淘汰）。
   */
  async function toggleReadOnly(lib: LibrarySummary) {
    const next = !lib.readonly;
    try {
      await api.updateLibrary(lib.id, { readonly: next });
      setError('');
      setNotice(
        next
          ? t('已把「{name}」设为只读：刮削产物写进数据目录，不写媒体目录。', {
              name: lib.name,
            })
          : t('已把「{name}」改为可写。', { name: lib.name }),
      );
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('切换只读失败'));
    }
  }

  async function remove(id: number, name: string) {
    if (
      !window.confirm(
        t('确定删除媒体库「{name}」？其条目与文件记录会一并删除（磁盘文件不受影响）。', { name }),
      )
    ) {
      return;
    }
    try {
      await api.deleteLibrary(id);
      setSelected(null);
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('删除失败'));
    }
  }

  return (
    <>
      <CreateLibraryCard onCreated={(lib) => { setSelected(lib.id); void load(); }} />

      <div className="card">
        <h2>{t('媒体库')}</h2>
        {error && <div className="alert alert-error">{error}</div>}
        {notice && <div className="alert alert-ok">{notice}</div>}
        {!libraries && <p className="muted">{t('正在读取…')}</p>}
        {libraries && libraries.length === 0 && (
          <p className="muted">{t('还没有媒体库。用上面的表单添加一个根路径即可。')}</p>
        )}

        {libraries?.map((lib) => {
          const p = progress[lib.id];
          const running = p && p.phase !== 'done' ? true : lib.scanRunning;
          return (
            <div
              key={lib.id}
              style={{
                border: '1px solid var(--border)',
                borderRadius: 'var(--radius-sm)',
                padding: '12px 14px',
                marginBottom: 10,
                background: lib.id === selected ? 'var(--bg-elev-2)' : 'transparent',
              }}
            >
              <div className="row">
                <strong
                  style={{ cursor: 'pointer' }}
                  onClick={() => setSelected(lib.id)}
                >
                  {lib.name}
                </strong>
                <span className="badge">{libraryKindLabel(lib.kind)}</span>
                {Object.entries(lib.counts).map(([k, n]) => (
                  <span className="badge" key={k}>
                    {itemKindLabel(k)} {n}
                  </span>
                ))}
                <span className="badge">{t('图片 {n}', { n: lib.imageCount })}</span>
                {/* 只读（网盘 / 只读挂载）：刮削产物进数据目录的 overlay 层 */}
                {lib.readonly && <span className="badge">{t('只读')}</span>}
                <div className="spacer" />
                <Link className="btn btn-sm" to={`/library/${lib.id}`}>
                  {t('海报墙')}
                </Link>
                <button
                  type="button"
                  className="btn btn-sm"
                  onClick={() => setEditing(lib)}
                >
                  {t('编辑')}
                </button>
                <button
                  type="button"
                  className="btn btn-sm"
                  title={t('网盘 / 只读挂载的库打开它：不再写媒体目录，刮削产物落进数据目录')}
                  onClick={() => void toggleReadOnly(lib)}
                >
                  {lib.readonly ? t('改为可写') : t('设为只读')}
                </button>
                {running ? (
                  <button type="button" className="btn btn-sm" onClick={() => void cancelScan(lib.id)}>
                    {t('取消扫描')}
                  </button>
                ) : (
                  <button
                    type="button"
                    className="btn btn-sm btn-primary"
                    onClick={() => void startScan(lib.id)}
                  >
                    {t('扫描')}
                  </button>
                )}
                <button
                  type="button"
                  className="btn btn-sm btn-danger"
                  onClick={() => void remove(lib.id, lib.name)}
                >
                  {t('删除')}
                </button>
              </div>

              <div className="faint" style={{ marginTop: 6 }}>
                {lib.paths.map((p2) => p2.path).join('　·　')}
              </div>

              {editing?.id === lib.id && (
                <EditLibraryForm
                  library={lib}
                  onCancel={() => setEditing(null)}
                  onSaved={async (msg) => {
                    setEditing(null);
                    setNotice(msg);
                    setError('');
                    await load();
                  }}
                  onError={setError}
                />
              )}

              {running && (
                <div style={{ marginTop: 8 }}>
                  <div className="faint">
                    {t('扫描中：视频 {videos} · 新增 {added} · 条目 {items} · 图片 {images} · {sec} 秒', {
                      videos: p?.videos ?? 0,
                      added: p?.newFiles ?? 0,
                      items: p?.itemsNew ?? 0,
                      images: p?.images ?? 0,
                      sec: ((p?.elapsedMs ?? 0) / 1000).toFixed(0),
                    })}
                  </div>
                  <div className="faint" style={{ wordBreak: 'break-all' }}>
                    {p?.currentPath ? shorten(p.currentPath) : t('正在遍历目录…')}
                  </div>
                </div>
              )}
            </div>
          );
        })}
      </div>

      {selected !== null && <LibraryDetailCard libraryId={selected} />}
    </>
  );
}

// ---------------------------------------------------------------- 新建

function CreateLibraryCard({ onCreated }: { onCreated: (lib: LibrarySummary) => void }) {
  const { t } = useI18n();
  const [name, setName] = useState('');
  const [kind, setKind] = useState('mixed');
  const [paths, setPaths] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  async function submit() {
    setError('');
    const list = paths
      .split('\n')
      .map((p) => p.trim())
      .filter(Boolean);
    if (!name.trim() || list.length === 0) {
      setError(t('请填写名称与至少一个根路径'));
      return;
    }
    setBusy(true);
    try {
      const lib = await api.createLibrary(name.trim(), kind, list);
      setName('');
      setPaths('');
      onCreated(lib);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('创建失败'));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="card">
      <h2>{t('添加媒体库')}</h2>
      {error && <div className="alert alert-error">{error}</div>}
      <label className="field">
        <span>{t('名称')}</span>
        <input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder={t('例如：电影 / 剧集')}
        />
      </label>
      <label className="field">
        <span>{t('类型')}</span>
        <select value={kind} onChange={(e) => setKind(e.target.value)}>
          {libraryKindOptions().map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
      </label>
      <label className="field">
        <span>{t('根路径（每行一个）')}</span>
        <textarea
          value={paths}
          onChange={(e) => setPaths(e.target.value)}
          rows={3}
          placeholder="/mnt/media/Movies"
          style={{
            width: '100%',
            font: 'inherit',
            fontSize: 14,
            padding: '9px 12px',
            borderRadius: 'var(--radius-sm)',
            border: '1px solid var(--border-strong)',
            background: 'var(--bg)',
            color: 'var(--fg)',
          }}
        />
      </label>
      <button type="button" className="btn btn-primary" disabled={busy} onClick={() => void submit()}>
        {busy ? t('创建中…') : t('创建媒体库')}
      </button>
    </div>
  );
}

// ---------------------------------------------------------------- 编辑（类型 / 根路径）

/**
 * 就地编辑一个媒体库：改类型与根路径。
 *
 * 为什么只给这两项：库名在创建后很少变，而「扫描扫错了一类」与「换了挂载点」
 * 才是真实需求。两者都是**策略**：改类型不影响已入库条目的 kind，
 * 移除根路径也不删已入库的条目 —— 界面上写明了，免得用户以为是「把东西删了」。
 */
function EditLibraryForm({
  library,
  onCancel,
  onSaved,
  onError,
}: {
  library: LibrarySummary;
  onCancel: () => void;
  onSaved: (msg: string) => void;
  onError: (msg: string) => void;
}) {
  const { t } = useI18n();
  const [kind, setKind] = useState(library.kind);
  const [paths, setPaths] = useState(library.paths.map((p) => p.path).join('\n'));
  const [busy, setBusy] = useState(false);

  async function submit() {
    const list = paths
      .split('\n')
      .map((p) => p.trim())
      .filter(Boolean);
    if (list.length === 0) {
      onError(t('至少需要一个根路径（想清空请删库）'));
      return;
    }
    setBusy(true);
    try {
      await api.updateLibrary(library.id, { kind, paths: list });
      onSaved(t('已保存「{name}」的类型与根路径（重扫后生效）', { name: library.name }));
    } catch (e) {
      onError(e instanceof ApiError ? e.message : t('保存失败'));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div
      style={{
        marginTop: 10,
        paddingTop: 10,
        borderTop: '1px dashed var(--border)',
      }}
    >
      <label className="field">
        <span>{t('类型')}</span>
        <select value={kind} onChange={(e) => setKind(e.target.value)}>
          {libraryKindOptions().map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
      </label>
      <label className="field">
        <span>{t('根路径（每行一个）')}</span>
        <textarea
          rows={3}
          value={paths}
          onChange={(e) => setPaths(e.target.value)}
          style={{
            width: '100%',
            font: 'inherit',
            fontSize: 14,
            padding: '9px 12px',
            borderRadius: 'var(--radius-sm)',
            border: '1px solid var(--border-strong)',
            background: 'var(--bg)',
            color: 'var(--fg)',
          }}
        />
      </label>
      <div className="row">
        <button type="button" className="btn btn-sm btn-primary" disabled={busy} onClick={() => void submit()}>
          {busy ? t('保存中…') : t('保存')}
        </button>
        <button type="button" className="btn btn-sm" onClick={onCancel}>
          {t('取消')}
        </button>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------- 详情

function LibraryDetailCard({ libraryId }: { libraryId: number }) {
  const { t, lang } = useI18n();
  const [detail, setDetail] = useState<Awaited<ReturnType<typeof api.library>> | null>(null);
  const [items, setItems] = useState<Item[] | null>(null);
  const [total, setTotal] = useState(0);
  const [kind, setKind] = useState('');
  const [page, setPage] = useState(0);
  const pageSize = 50;
  const reqId = useRef(0);

  const loadDetail = useCallback(async () => {
    try {
      setDetail(await api.library(libraryId));
    } catch {
      setDetail(null);
    }
  }, [libraryId]);

  const loadItems = useCallback(async () => {
    const id = ++reqId.current;
    try {
      const res = await api.items(libraryId, kind, pageSize, page * pageSize);
      if (id !== reqId.current) return; // 丢弃过期响应
      setItems(res.items);
      setTotal(res.total);
    } catch {
      setItems([]);
    }
  }, [libraryId, kind, page]);

  useEffect(() => {
    setPage(0);
    setKind('');
    setItems(null);
    void loadDetail();
  }, [libraryId, loadDetail]);

  useEffect(() => {
    void loadItems();
  }, [loadItems]);

  useEffect(() => {
    const t = window.setInterval(() => void loadDetail(), 5000);
    return () => window.clearInterval(t);
  }, [loadDetail]);

  if (!detail) return null;
  const { lastScan, issues } = detail;

  return (
    <>
      <div className="card">
        <h2>
          {detail.library.name} · {t('扫描记录')}
        </h2>
        {detail.library.readonly && (
          <div className="alert">
            {t('这个库是只读的：不往媒体目录写入任何东西。刮削产物存在数据目录，当前 {files} 个文件 / {size}。',
              {
                files: detail.overlay?.files ?? 0,
                size: sizeText(detail.overlay?.bytes ?? 0),
              })}
          </div>
        )}
        {lastScan ? (
          <dl className="kv">
            <dt>{t('状态')}</dt>
            <dd>{stateLabel(lastScan.state)}</dd>
            <dt>{t('触发方式')}</dt>
            <dd>{lastScan.trigger === 'manual' ? t('手动') : lastScan.trigger}</dd>
            <dt>{t('开始时间')}</dt>
            <dd>{new Date(lastScan.startedAt).toLocaleString(lang, { hour12: false })}</dd>
            <dt>{t('耗时')}</dt>
            <dd>{t('{n} 秒', { n: ((lastScan.stats?.elapsedMs ?? 0) / 1000).toFixed(1) })}</dd>
            <dt>{t('扫描结果')}</dt>
            <dd>
              {t('视频 {videos} · 新增 {added} · 变化 {changed} · 移动 {moved} · 删除 {deleted} · 未变 {unchanged}', {
                videos: lastScan.stats?.videos ?? 0,
                added: lastScan.stats?.newFiles ?? 0,
                changed: lastScan.stats?.changedFiles ?? 0,
                moved: lastScan.stats?.movedFiles ?? 0,
                deleted: lastScan.stats?.deletedFiles ?? 0,
                unchanged: lastScan.stats?.unchanged ?? 0,
              })}
            </dd>
            <dt>{t('新建条目')}</dt>
            <dd>
              {t('剧集 {series} · 季 {seasons} · 集 {episodes} · 电影 {movies}', {
                series: lastScan.stats?.seriesNew ?? 0,
                seasons: lastScan.stats?.seasonsNew ?? 0,
                episodes: lastScan.stats?.episodesNew ?? 0,
                movies: lastScan.stats?.moviesNew ?? 0,
              })}
            </dd>
            <dt>{t('读取 nfo')}</dt>
            <dd>{lastScan.stats?.nfoRead ?? 0}</dd>
            <dt>{t('图片 / 问题')}</dt>
            <dd>
              {lastScan.stats?.images ?? 0} / {lastScan.stats?.issues ?? 0}
            </dd>
            {lastScan.error && (
              <>
                <dt>{t('错误')}</dt>
                <dd style={{ color: 'var(--danger)' }}>{lastScan.error}</dd>
              </>
            )}
          </dl>
        ) : (
          <p className="muted">{t('还没有扫描记录。')}</p>
        )}
      </div>

      {issues.length > 0 && <IssuesCard issues={issues} />}

      <ScrapeCard libraryId={libraryId} onChange={loadDetail} />

      <ProbeCard libraryId={libraryId} probe={detail.probe} onChange={loadDetail} />

      <div className="card">
        <h2>{t('条目（{n}）', { n: total })}</h2>
        <div className="row" style={{ marginBottom: 12 }}>
          {[
            { v: '', l: t('全部') },
            { v: 'series', l: t('剧集') },
            { v: 'season', l: t('季') },
            { v: 'episode', l: t('集') },
            { v: 'movie', l: t('电影') },
            { v: 'extra', l: t('花絮') },
          ].map((o) => (
            <button
              key={o.v}
              type="button"
              className={`btn btn-sm ${kind === o.v ? 'btn-primary' : ''}`}
              onClick={() => {
                setKind(o.v);
                setPage(0);
              }}
            >
              {o.l}
            </button>
          ))}
        </div>

        {!items && <p className="muted">{t('正在读取…')}</p>}
        {items && items.length === 0 && <p className="muted">{t('没有条目。')}</p>}
        {items && items.length > 0 && (
          <>
            <table>
              <thead>
                <tr>
                  <th>{t('类型')}</th>
                  <th>{t('标题')}</th>
                  <th>{t('年')}</th>
                  <th>{t('季/集')}</th>
                  <th>{t('文件名标记')}</th>
                </tr>
              </thead>
              <tbody>
                {items.map((it) => (
                  <tr key={it.id}>
                    <td className="faint">{itemKindLabel(it.kind)}</td>
                    <td>
                      {it.title ? (
                        <Link to={`/items/${it.id}`}>{it.title}</Link>
                      ) : (
                        <span className="faint">{t('（无标题，待刮削）')}</span>
                      )}
                    </td>
                    <td className="faint">{it.year ?? ''}</td>
                    <td className="faint">
                      {it.seasonNumber !== undefined && it.seasonNumber !== null ? `S${it.seasonNumber}` : ''}
                      {it.episodeNumber !== undefined && it.episodeNumber !== null
                        ? `E${it.episodeNumber}${it.episodeEnd ? `E${it.episodeEnd}` : ''}`
                        : ''}
                    </td>
                    <td className="faint">{techSummary(it)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            <div className="row" style={{ marginTop: 12 }}>
              <button
                type="button"
                className="btn btn-sm"
                disabled={page === 0}
                onClick={() => setPage((p) => Math.max(0, p - 1))}
              >
                {t('上一页')}
              </button>
              <span className="faint">
                {t('第 {a} / {b} 页', { a: page + 1, b: Math.max(1, Math.ceil(total / pageSize)) })}
              </span>
              <button
                type="button"
                className="btn btn-sm"
                disabled={(page + 1) * pageSize >= total}
                onClick={() => setPage((p) => p + 1)}
              >
                {t('下一页')}
              </button>
            </div>
          </>
        )}
      </div>
    </>
  );
}

/**
 * 刮削卡片：库级的一键刮削、强制重刮、重置失败。
 *
 * 为什么放在库里而不是单独一页：刮削是「这批内容扫进来了，去把元数据补上」的收尾动作，
 * 用户刚看完扫描结果，紧接着就会做这件事。
 */
function ScrapeCard({ libraryId, onChange }: { libraryId: number; onChange: () => void }) {
  const { t } = useI18n();
  const [data, setData] = useState<{ configured: boolean; scrape: import('../api').ScrapeProgress } | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState('');

  const load = useCallback(async () => {
    try {
      setData(await api.scrapeStatus(libraryId));
    } catch {
      /* 读不到就当未配置处理 */
    }
  }, [libraryId]);

  useEffect(() => {
    void load();
  }, [load]);

  async function run(fn: () => Promise<string>) {
    setBusy(true);
    setMessage('');
    try {
      setMessage(await fn());
      await load();
      onChange();
    } catch (e) {
      setMessage(e instanceof ApiError ? e.message : t('操作失败'));
    } finally {
      setBusy(false);
    }
  }

  const p = data?.scrape;
  const todo = p ? p.local + p.failed + p.review : 0;

  return (
    <div className="card">
      <h2>{t('元数据刮削')}</h2>
      {message && <div className="alert">{message}</div>}

      {data && !data.configured && (
        <div className="alert alert-error">
          {t('还没配 TMDB 凭据：去「设置」页填一个 Read Access Token（或 API Key）就能开始刮削。')}
        </div>
      )}

      {p && (
        <div className="row" style={{ marginBottom: 12 }}>
          <span className="badge">{t('来自 nfo {n}', { n: p.nfo })}</span>
          <span className="badge">{t('已匹配 {n}', { n: p.matched })}</span>
          <span className="badge">{t('待确认 {n}', { n: p.review })}</span>
          <span className="badge">{t('没找到 {n}', { n: p.failed })}</span>
          <span className="badge">{t('已人工 {n}', { n: p.manual })}</span>
          <span className="badge">{t('未刮削 {n}', { n: p.local })}</span>
        </div>
      )}

      <div className="row">
        <button
          type="button"
          className="btn btn-sm btn-primary"
          disabled={busy || !data?.configured}
          onClick={() =>
            void run(async () => {
              const res = await api.enqueueScrapes(libraryId, {});
              return t('已入队 {n} 条（已有 nfo/已匹配的会自动跳过）', { n: res.enqueued });
            })
          }
        >
          {t('一键刮削（{n} 条待处理）', { n: todo })}
        </button>
        <button
          type="button"
          className="btn btn-sm"
          disabled={busy || !data?.configured}
          onClick={() => {
            if (
              !window.confirm(
                t('强制重刮会覆盖所有条目的未锁定字段（锁住的与人工改过的不动）。确定继续？'),
              )
            ) {
              return;
            }
            void run(async () => {
              const res = await api.enqueueScrapes(libraryId, { force: true });
              return t('已强制入队 {n} 条', { n: res.enqueued });
            });
          }}
        >
          {t('强制重刮全部')}
        </button>
        <button
          type="button"
          className="btn btn-sm"
          disabled={busy || (p?.failed ?? 0) === 0}
          onClick={() =>
            void run(async () => {
              const res = await api.resetFailedScrapes(libraryId);
              return t('已重置 {reset} 条失败记录，并重新入队 {enqueued} 条', {
                reset: res.reset,
                enqueued: res.enqueued,
              });
            })
          }
        >
          {t('重置失败的')}
        </button>
        <button type="button" className="btn btn-sm btn-ghost" disabled={busy} onClick={() => void run(async () => t('已刷新'))}>
          {t('刷新')}
        </button>
        <div className="spacer" />
        <Link className="btn btn-sm" to="/settings/match">
          {t('去人工匹配')}
        </Link>
      </div>
    </div>
  );
}

function ProbeCard({
  libraryId,
  probe,
  onChange,
}: {
  libraryId: number;
  probe: import('../api').ProbeProgress;
  onChange: () => void;
}) {
  const { t } = useI18n();
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState('');
  const total = probe.ok + probe.pending + probe.failed;
  const percent = total > 0 ? Math.round((probe.ok / total) * 100) : 0;

  async function run(fn: () => Promise<string>) {
    setBusy(true);
    setMessage('');
    try {
      setMessage(await fn());
      onChange();
    } catch (e) {
      setMessage(e instanceof ApiError ? e.message : t('操作失败'));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="card">
      <h2>{t('流信息探测')}</h2>
      {message && <div className="alert">{message}</div>}

      <div className="row" style={{ marginBottom: 10 }}>
        <span className="badge">{t('已完成 {n}', { n: probe.ok })}</span>
        <span className="badge">{t('待探测 {n}', { n: probe.pending })}</span>
        <span className="badge">{t('失败 {n}', { n: probe.failed })}</span>
        <span className="faint">{percent}%</span>
      </div>

      <div
        style={{
          height: 6,
          borderRadius: 3,
          background: 'var(--bg-elev-2)',
          overflow: 'hidden',
          marginBottom: 12,
        }}
      >
        <div
          style={{
            width: `${percent}%`,
            height: '100%',
            background: 'var(--accent)',
            transition: 'width .3s ease',
          }}
        />
      </div>

      <div className="row">
        <button
          type="button"
          className="btn btn-sm btn-primary"
          disabled={busy || probe.pending === 0}
          onClick={() =>
            void run(async () => {
              const res = await api.enqueueProbes(libraryId);
              return t('已入队 {n} 个探测任务', { n: res.enqueued });
            })
          }
        >
          {t('把待探测的文件入队')}
        </button>
        <button
          type="button"
          className="btn btn-sm"
          disabled={busy || probe.failed === 0}
          onClick={() =>
            void run(async () => {
              const res = await api.resetFailedProbes(libraryId);
              return t('已重置 {reset} 个失败的探测，重新入队 {enqueued} 个', {
                reset: res.reset,
                enqueued: res.enqueued,
              });
            })
          }
        >
          {t('重置失败的探测')}
        </button>
      </div>
    </div>
  );
}

function IssuesCard({ issues }: { issues: ScanIssue[] }) {
  const { t } = useI18n();
  const [expanded, setExpanded] = useState(false);
  const shown = expanded ? issues : issues.slice(0, 5);
  return (
    <div className="card">
      <h2>{t('扫描问题（{n}）', { n: issues.length })}</h2>
      <table>
        <thead>
          <tr>
            <th>{t('级别')}</th>
            <th>{t('路径')}</th>
            <th>{t('说明')}</th>
          </tr>
        </thead>
        <tbody>
          {shown.map((is) => (
            <tr key={is.id}>
              <td className="faint">{is.severity}</td>
              <td className="faint" style={{ wordBreak: 'break-all' }}>
                {shorten(is.path)}
              </td>
              <td>{is.message}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {issues.length > 5 && (
        <button type="button" className="btn btn-sm" style={{ marginTop: 10 }} onClick={() => setExpanded((v) => !v)}>
          {expanded ? t('收起') : t('展开全部 {n} 条', { n: issues.length })}
        </button>
      )}
    </div>
  );
}

// ---------------------------------------------------------------- 工具

function libraryKindLabel(kind: string): string {
  return libraryKindOptions().find((o) => o.value === kind)?.label ?? kind;
}

function stateLabel(state: string): string {
  switch (state) {
    case 'running':
      return t('进行中');
    case 'done':
      return t('已完成');
    case 'failed':
      return t('失败');
    case 'canceled':
      return t('已取消');
    default:
      return state;
  }
}

function techSummary(it: Item): string {
  const t = it.fileTech as Record<string, unknown> | undefined;
  if (!t) return '';
  const parts: string[] = [];
  for (const key of ['codec', 'resolution', 'chroma', 'frameRate', 'bitrate']) {
    const v = t[key];
    if (typeof v === 'string' && v) parts.push(v);
  }
  const feats = t.features;
  if (Array.isArray(feats)) parts.push(...feats.filter((f): f is string => typeof f === 'string'));
  return parts.join(' ');
}

function shorten(p: string, max = 72): string {
  if (p.length <= max) return p;
  return '…' + p.slice(p.length - max + 1);
}

/** 字节数 → 「1.2 MB」这种人话（叠加层占用显示用）。 */
function sizeText(bytes: number): string {
  if (!bytes || bytes <= 0) return '0 B';
  if (bytes >= 1024 ** 3) return `${(bytes / 1024 ** 3).toFixed(2)} GB`;
  if (bytes >= 1024 ** 2) return `${(bytes / 1024 ** 2).toFixed(1)} MB`;
  if (bytes >= 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${bytes} B`;
}
