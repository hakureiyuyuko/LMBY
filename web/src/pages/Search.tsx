import { useEffect, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { ApiError, api } from '../api';
import type { LibrarySummary, SearchPage } from '../api';

/**
 * 搜索。
 *
 * 匹配是怎么做的（细节见 migrations/0007_search.sql 与 internal/store/search.go）：
 *   - 中文按二元组切词（「炼金」能搜到《钢之炼金术师》），英文按整词；
 *   - 同时容忍错字（trigram 相似：「钢之炼金术土」也能命中）；
 *   - 单字查询走子串兜底（索引里只有两字的单元）。
 * 搜的是标题与原始标题。
 *
 * 查询词放在 URL 上（?q=&kind=&lib=&page=）：可收藏、可后退、也能直接贴给别人。
 */

const kindLabels: Record<string, string> = {
  movie: '电影',
  series: '剧集',
  season: '季',
  episode: '集',
  extra: '花絮',
};

const PAGE_SIZE = 24;

export function Search() {
  const [params, setParams] = useSearchParams();
  const query = params.get('q') ?? '';
  const kind = params.get('kind') ?? '';
  const lib = params.get('lib') ?? '';
  const page = Math.max(0, Number(params.get('page') ?? '0') || 0);

  const [input, setInput] = useState(query);
  const [libraries, setLibraries] = useState<LibrarySummary[]>([]);
  const [data, setData] = useState<SearchPage | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  // 后退/前进时输入框跟着 URL 走
  useEffect(() => setInput(query), [query]);

  useEffect(() => {
    api
      .libraries()
      .then((res) => setLibraries(res.libraries))
      .catch(() => {
        /* 库列表拿不到不影响搜索 */
      });
  }, []);

  useEffect(() => {
    if (!query) {
      setData(null);
      return;
    }
    let alive = true;
    setBusy(true);
    api
      .search({
        q: query,
        kind: kind || undefined,
        libraryId: lib ? Number(lib) : undefined,
        limit: PAGE_SIZE,
        offset: page * PAGE_SIZE,
      })
      .then((res) => {
        if (!alive) return;
        setData(res);
        setError('');
      })
      .catch((e) => {
        if (!alive) return;
        setError(e instanceof ApiError ? e.message : '搜索失败');
        setData(null);
      })
      .finally(() => {
        if (alive) setBusy(false);
      });
    return () => {
      alive = false;
    };
  }, [query, kind, lib, page]);

  /** 拼 URL 参数（空值不写进去，链接才干净）。 */
  function buildParams(q: string, k: string, l: string, p = 0): Record<string, string> {
    const out: Record<string, string> = {};
    if (q) out.q = q;
    if (k) out.kind = k;
    if (l) out.lib = l;
    if (p > 0) out.page = String(p);
    return out;
  }

  const libraryName = (id?: number) =>
    libraries.find((l) => l.id === id)?.name ?? (id ? `库 ${id}` : '');

  const shown = data ? Math.min(data.offset + data.items.length, data.total) : 0;

  return (
    <>
      <div className="card">
        <h2>搜索</h2>
        <p className="hint">
          中文按二元组切词（「炼金」能搜到《钢之炼金术师》），英文按整词，并且<strong>容忍错字</strong>
          （「钢之炼金术土」也能命中）。搜的是标题与原始标题。
        </p>

        <form
          className="row"
          onSubmit={(e) => {
            e.preventDefault();
            setParams(buildParams(input.trim(), kind, lib));
          }}
        >
          <input
            className="search-input"
            value={input}
            autoFocus
            placeholder="片名或原名，如「言叶之庭」「Matrix」"
            onChange={(e) => setInput(e.target.value)}
          />
          <button type="submit" className="btn btn-primary" disabled={busy}>
            {busy ? '搜索中…' : '搜索'}
          </button>
          <div className="spacer" />
          <label className="field field-inline">
            <span>媒体库</span>
            <select
              value={lib}
              onChange={(e) => setParams(buildParams(query, kind, e.target.value))}
            >
              <option value="">全部</option>
              {libraries.map((l) => (
                <option key={l.id} value={l.id}>
                  {l.name}
                </option>
              ))}
            </select>
          </label>
          <label className="field field-inline">
            <span>类型</span>
            <select
              value={kind}
              onChange={(e) => setParams(buildParams(query, e.target.value, lib))}
            >
              <option value="">全部</option>
              {Object.entries(kindLabels).map(([v, label]) => (
                <option key={v} value={v}>
                  {label}
                </option>
              ))}
            </select>
          </label>
        </form>

        {error && <div className="alert alert-error">{error}</div>}
        {!query && <p className="muted">输入片名开始搜索。</p>}
        {query && data && (
          <p className="muted small">
            「{data.query}」共 {data.total} 条
            {data.total > 0 ? `，显示第 ${data.offset + 1}~${shown} 条` : ''}
          </p>
        )}
        {query && data && data.items.length === 0 && (
          <p className="muted">没有匹配的条目。试试只搜其中两个字，或者换个译名。</p>
        )}
      </div>

      {data && data.items.length > 0 && (
        <div className="card">
          <div className="search-grid">
            {data.items.map((it) => (
              <Link key={it.id} className="search-card" to={`/item/${it.id}`}>
                <img
                  className="search-thumb"
                  src={`/api/v1/items/${it.id}/images/poster?w=160`}
                  alt=""
                  loading="lazy"
                  onError={(e) => {
                    (e.currentTarget as HTMLImageElement).style.visibility = 'hidden';
                  }}
                />
                <div className="search-card-body">
                  <div className="search-title">{it.title || '（无标题）'}</div>
                  <div className="muted small">
                    {kindLabels[it.kind] ?? it.kind}
                    {it.seasonNumber != null ? ` S${it.seasonNumber}` : ''}
                    {it.episodeNumber != null ? `E${it.episodeNumber}` : ''}
                    {it.year ? ` · ${it.year}` : ''}
                    {libraryName(it.libraryId) ? ` · ${libraryName(it.libraryId)}` : ''}
                  </div>
                  {it.originalTitle && <div className="muted small">{it.originalTitle}</div>}
                  {it.overview && <div className="muted small search-clamp">{it.overview}</div>}
                </div>
              </Link>
            ))}
          </div>

          {data.total > PAGE_SIZE && (
            <div className="row" style={{ marginTop: 12 }}>
              <button
                type="button"
                className="btn btn-sm"
                disabled={page === 0}
                onClick={() => setParams(buildParams(query, kind, lib, page - 1))}
              >
                上一页
              </button>
              <span className="faint">
                第 {page + 1} / {Math.max(1, Math.ceil(data.total / PAGE_SIZE))} 页
              </span>
              <button
                type="button"
                className="btn btn-sm"
                disabled={(page + 1) * PAGE_SIZE >= data.total}
                onClick={() => setParams(buildParams(query, kind, lib, page + 1))}
              >
                下一页
              </button>
            </div>
          )}
        </div>
      )}
    </>
  );
}
