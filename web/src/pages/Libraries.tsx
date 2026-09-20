import { useCallback, useEffect, useRef, useState } from 'react';
import { ApiError, api } from '../api';
import type { Item, LibrarySummary, ScanIssue, ScanProgress } from '../api';

const kindLabels: Record<string, string> = {
  movie: '电影',
  series: '剧集',
  season: '季',
  episode: '集',
  extra: '花絮',
};

const libraryKindOptions = [
  { value: 'movie', label: '电影' },
  { value: 'tv', label: '剧集' },
  { value: 'homevideo', label: '家庭视频' },
  { value: 'mixed', label: '混合（推荐）' },
];

export function Libraries() {
  const [libraries, setLibraries] = useState<LibrarySummary[] | null>(null);
  const [selected, setSelected] = useState<number | null>(null);
  const [error, setError] = useState('');
  const [progress, setProgress] = useState<Record<number, ScanProgress>>({});

  const load = useCallback(async () => {
    try {
      const res = await api.libraries();
      setLibraries(res.libraries);
      setError('');
      if (res.libraries.length > 0 && selected === null) {
        setSelected(res.libraries[0].id);
      }
    } catch (e) {
      setError(e instanceof ApiError ? e.message : '读取媒体库失败');
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
      setError(e instanceof ApiError ? e.message : '启动扫描失败');
    }
  }

  async function cancelScan(id: number) {
    try {
      await api.cancelScan(id);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : '取消扫描失败');
    }
  }

  async function remove(id: number, name: string) {
    if (!window.confirm(`确定删除媒体库「${name}」？其条目与文件记录会一并删除（磁盘文件不受影响）。`)) {
      return;
    }
    try {
      await api.deleteLibrary(id);
      setSelected(null);
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : '删除失败');
    }
  }

  return (
    <>
      <CreateLibraryCard onCreated={(lib) => { setSelected(lib.id); void load(); }} />

      <div className="card">
        <h2>媒体库</h2>
        <p className="hint">
          扫描只读取文件系统与本地 nfo，不会修改你的文件；图片只登记路径，不入库。
        </p>
        {error && <div className="alert alert-error">{error}</div>}
        {!libraries && <p className="muted">正在读取…</p>}
        {libraries && libraries.length === 0 && (
          <p className="muted">还没有媒体库。用上面的表单添加一个根路径即可。</p>
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
                <span className="badge">{kindLabel(lib.kind)}</span>
                {Object.entries(lib.counts).map(([k, n]) => (
                  <span className="badge" key={k}>
                    {kindLabels[k] ?? k} {n}
                  </span>
                ))}
                <span className="badge">图片 {lib.imageCount}</span>
                <div className="spacer" />
                {running ? (
                  <button type="button" className="btn btn-sm" onClick={() => void cancelScan(lib.id)}>
                    取消扫描
                  </button>
                ) : (
                  <button
                    type="button"
                    className="btn btn-sm btn-primary"
                    onClick={() => void startScan(lib.id)}
                  >
                    扫描
                  </button>
                )}
                <button
                  type="button"
                  className="btn btn-sm btn-danger"
                  onClick={() => void remove(lib.id, lib.name)}
                >
                  删除
                </button>
              </div>

              <div className="faint" style={{ marginTop: 6 }}>
                {lib.paths.map((p2) => p2.path).join('　·　')}
              </div>

              {running && (
                <div style={{ marginTop: 8 }}>
                  <div className="faint">
                    扫描中：视频 {p?.videos ?? 0} · 新增 {p?.newFiles ?? 0} · 条目 {p?.itemsNew ?? 0} · 图片{' '}
                    {p?.images ?? 0} · {((p?.elapsedMs ?? 0) / 1000).toFixed(0)} 秒
                  </div>
                  <div className="faint" style={{ wordBreak: 'break-all' }}>
                    {p?.currentPath ? shorten(p.currentPath) : '正在遍历目录…'}
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
      setError('请填写名称与至少一个根路径');
      return;
    }
    setBusy(true);
    try {
      const lib = await api.createLibrary(name.trim(), kind, list);
      setName('');
      setPaths('');
      onCreated(lib);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : '创建失败');
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="card">
      <h2>添加媒体库</h2>
      <p className="hint">
        根路径是服务器上的绝对路径。多个路径可以指向不同磁盘；目录约定与 Emby 一致，
        现有库可以零改名接管。
      </p>
      {error && <div className="alert alert-error">{error}</div>}
      <label className="field">
        <span>名称</span>
        <input value={name} onChange={(e) => setName(e.target.value)} placeholder="例如：电影 / 剧集" />
      </label>
      <label className="field">
        <span>类型</span>
        <select value={kind} onChange={(e) => setKind(e.target.value)}>
          {libraryKindOptions.map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
      </label>
      <label className="field">
        <span>根路径（每行一个）</span>
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
        {busy ? '创建中…' : '创建媒体库'}
      </button>
    </div>
  );
}

// ---------------------------------------------------------------- 详情

function LibraryDetailCard({ libraryId }: { libraryId: number }) {
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
        <h2>{detail.library.name} · 扫描记录</h2>
        {lastScan ? (
          <dl className="kv">
            <dt>状态</dt>
            <dd>{stateLabel(lastScan.state)}</dd>
            <dt>触发方式</dt>
            <dd>{lastScan.trigger === 'manual' ? '手动' : lastScan.trigger}</dd>
            <dt>开始时间</dt>
            <dd>{new Date(lastScan.startedAt).toLocaleString('zh-CN', { hour12: false })}</dd>
            <dt>耗时</dt>
            <dd>{((lastScan.stats?.elapsedMs ?? 0) / 1000).toFixed(1)} 秒</dd>
            <dt>扫描结果</dt>
            <dd>
              视频 {lastScan.stats?.videos ?? 0} · 新增 {lastScan.stats?.newFiles ?? 0} · 变化{' '}
              {lastScan.stats?.changedFiles ?? 0} · 移动 {lastScan.stats?.movedFiles ?? 0} · 删除{' '}
              {lastScan.stats?.deletedFiles ?? 0} · 未变 {lastScan.stats?.unchanged ?? 0}
            </dd>
            <dt>新建条目</dt>
            <dd>
              剧集 {lastScan.stats?.seriesNew ?? 0} · 季 {lastScan.stats?.seasonsNew ?? 0} · 集{' '}
              {lastScan.stats?.episodesNew ?? 0} · 电影 {lastScan.stats?.moviesNew ?? 0}
            </dd>
            <dt>读取 nfo</dt>
            <dd>{lastScan.stats?.nfoRead ?? 0}</dd>
            <dt>图片 / 问题</dt>
            <dd>
              {lastScan.stats?.images ?? 0} / {lastScan.stats?.issues ?? 0}
            </dd>
            {lastScan.error && (
              <>
                <dt>错误</dt>
                <dd style={{ color: 'var(--danger)' }}>{lastScan.error}</dd>
              </>
            )}
          </dl>
        ) : (
          <p className="muted">还没有扫描记录。</p>
        )}
      </div>

      {issues.length > 0 && <IssuesCard issues={issues} />}

      <ProbeCard libraryId={libraryId} probe={detail.probe} onChange={loadDetail} />

      <div className="card">
        <h2>条目（{total}）</h2>
        <p className="hint">扫描入库的原始条目。matching 与海报墙属于 M2 / M5。</p>
        <div className="row" style={{ marginBottom: 12 }}>
          {[
            { v: '', l: '全部' },
            { v: 'series', l: '剧集' },
            { v: 'season', l: '季' },
            { v: 'episode', l: '集' },
            { v: 'movie', l: '电影' },
            { v: 'extra', l: '花絮' },
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

        {!items && <p className="muted">正在读取…</p>}
        {items && items.length === 0 && <p className="muted">没有条目。</p>}
        {items && items.length > 0 && (
          <>
            <table>
              <thead>
                <tr>
                  <th>类型</th>
                  <th>标题</th>
                  <th>年</th>
                  <th>季/集</th>
                  <th>文件名标记</th>
                </tr>
              </thead>
              <tbody>
                {items.map((it) => (
                  <tr key={it.id}>
                    <td className="faint">{kindLabels[it.kind] ?? it.kind}</td>
                    <td>{it.title || <span className="faint">（无标题，待刮削）</span>}</td>
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
                上一页
              </button>
              <span className="faint">
                第 {page + 1} / {Math.max(1, Math.ceil(total / pageSize))} 页
              </span>
              <button
                type="button"
                className="btn btn-sm"
                disabled={(page + 1) * pageSize >= total}
                onClick={() => setPage((p) => p + 1)}
              >
                下一页
              </button>
            </div>
          </>
        )}
      </div>
    </>
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
      setMessage(e instanceof ApiError ? e.message : '操作失败');
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="card">
      <h2>流信息探测</h2>
      <p className="hint">
        ffprobe 读取每个文件的容器/编码/位深/HDR/音轨/字幕/章节信息，写入数据库。
        扫描结束后会自动排队，这里可以手动补跑。
      </p>
      {message && <div className="alert">{message}</div>}

      <div className="row" style={{ marginBottom: 10 }}>
        <span className="badge">已完成 {probe.ok}</span>
        <span className="badge">待探测 {probe.pending}</span>
        <span className="badge">失败 {probe.failed}</span>
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
              return `已入队 ${res.enqueued} 个探测任务`;
            })
          }
        >
          把待探测的文件入队
        </button>
        <button
          type="button"
          className="btn btn-sm"
          disabled={busy || probe.failed === 0}
          onClick={() =>
            void run(async () => {
              const res = await api.resetFailedProbes(libraryId);
              return `已重置 ${res.reset} 个失败的探测，重新入队 ${res.enqueued} 个`;
            })
          }
        >
          重置失败的探测
        </button>
      </div>
    </div>
  );
}

function IssuesCard({ issues }: { issues: ScanIssue[] }) {  const [expanded, setExpanded] = useState(false);
  const shown = expanded ? issues : issues.slice(0, 5);
  return (
    <div className="card">
      <h2>扫描问题（{issues.length}）</h2>
      <p className="hint">不致命但值得看一眼：文件过小、识别不了、nfo 解析失败等。</p>
      <table>
        <thead>
          <tr>
            <th>级别</th>
            <th>路径</th>
            <th>说明</th>
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
          {expanded ? '收起' : `展开全部 ${issues.length} 条`}
        </button>
      )}
    </div>
  );
}

// ---------------------------------------------------------------- 工具

function kindLabel(kind: string): string {
  return libraryKindOptions.find((o) => o.value === kind)?.label ?? kind;
}

function stateLabel(state: string): string {
  switch (state) {
    case 'running':
      return '进行中';
    case 'done':
      return '已完成';
    case 'failed':
      return '失败';
    case 'canceled':
      return '已取消';
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
