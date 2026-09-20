import { useEffect, useState } from 'react';
import { Link, useParams, useSearchParams } from 'react-router-dom';
import { ApiError, api } from '../api';
import type { BrowsePage, LibrarySummary } from '../api';

/**
 * 海报墙：一个媒体库里「给人看」的入口。
 *
 * 与「媒体库」页的分工：那边是原始条目表（含季与集，排查问题用），
 * 这边只要顶层条目（电影、剧集），按海报铺开、可按年份与最近添加排序。
 *
 * 点卡片：剧集 → 剧集视图（季/集），电影 → 条目页。
 */

const kindTabs = [
  { v: '', l: '全部' },
  { v: 'movie', l: '电影' },
  { v: 'series', l: '剧集' },
];

const sortOptions = [
  { v: 'title', l: '按标题' },
  { v: 'year', l: '按年份' },
  { v: 'added', l: '最近添加' },
];

const PAGE_SIZE = 60;

export function Browse() {
  const params0 = useParams();
  const libraryId = Number(params0.id);
  const [params, setParams] = useSearchParams();
  const kind = params.get('kind') ?? '';
  const sort = params.get('sort') ?? 'title';
  const page = Math.max(0, Number(params.get('page') ?? '0') || 0);

  const [data, setData] = useState<BrowsePage | null>(null);
  const [library, setLibrary] = useState<LibrarySummary | null>(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!Number.isFinite(libraryId) || libraryId <= 0) {
      setError('媒体库 id 非法');
      return;
    }
    api
      .library(libraryId)
      .then((d) => setLibrary(d.library))
      .catch(() => {
        /* 库名拿不到不影响浏览 */
      });
  }, [libraryId]);

  useEffect(() => {
    if (!Number.isFinite(libraryId) || libraryId <= 0) return;
    let alive = true;
    setBusy(true);
    api
      .browse(libraryId, { kind: kind || undefined, sort, limit: PAGE_SIZE, offset: page * PAGE_SIZE })
      .then((res) => {
        if (!alive) return;
        setData(res);
        setError('');
      })
      .catch((e) => {
        if (!alive) return;
        setError(e instanceof ApiError ? e.message : '读取失败');
        setData(null);
      })
      .finally(() => {
        if (alive) setBusy(false);
      });
    return () => {
      alive = false;
    };
  }, [libraryId, kind, sort, page]);

  /** 拼 URL 参数（空值不写进去，链接才干净）。 */
  function buildParams(k: string, s: string, p = 0): Record<string, string> {
    const out: Record<string, string> = {};
    if (k) out.kind = k;
    if (s && s !== 'title') out.sort = s;
    if (p > 0) out.page = String(p);
    return out;
  }

  const totalPages = data ? Math.max(1, Math.ceil(data.total / PAGE_SIZE)) : 1;

  return (
    <>
      <div className="card">
        <div className="row">
          <h2 style={{ margin: 0 }}>{library?.name ?? '海报墙'}</h2>
          <div className="tabs" style={{ marginBottom: 0, marginLeft: 12 }}>
            {kindTabs.map((t) => (
              <button
                key={t.v}
                type="button"
                className={kind === t.v ? 'tab active' : 'tab'}
                onClick={() => setParams(buildParams(t.v, sort))}
              >
                {t.l}
              </button>
            ))}
          </div>
          <div className="spacer" />
          <label className="field field-inline">
            <span>排序</span>
            <select value={sort} onChange={(e) => setParams(buildParams(kind, e.target.value))}>
              {sortOptions.map((o) => (
                <option key={o.v} value={o.v}>
                  {o.l}
                </option>
              ))}
            </select>
          </label>
          <Link className="btn btn-sm" to={`/libraries?lib=${libraryId}`}>
            库管理
          </Link>
        </div>

        {error && <div className="alert alert-error">{error}</div>}
        {!data && !error && <p className="muted">正在读取…</p>}
        {data && (
          <p className="muted small" style={{ marginTop: 10 }}>
            共 {data.total} 条{data.total > 0 ? `，显示第 ${data.offset + 1}~${Math.min(data.offset + data.items.length, data.total)} 条` : ''}
            {busy ? ' · 加载中…' : ''}
          </p>
        )}
        {data && data.items.length === 0 && (
          <p className="muted">
            这里还没有条目。先在「库管理」里扫描一次 —— 扫描会登记文件、导入同目录的 nfo 与图片。
          </p>
        )}
      </div>

      {data && data.items.length > 0 && (
        <div className="card">
          <div className="poster-grid">
            {data.items.map((it) => (
              <Link
                key={it.id}
                className="poster-card"
                to={it.kind === 'series' ? `/series/${it.id}` : `/items/${it.id}`}
              >
                {/* 占位块在下、图在上：没海报时不会留下一堆空洞（墙的节奏全靠它）*/}
                <span className="poster-frame">
                  <span className="poster-ph">{(it.title || '?').slice(0, 1)}</span>
                  <img
                    className="poster-img"
                    src={`/api/v1/items/${it.id}/images/poster?w=300`}
                    alt=""
                    loading="lazy"
                    onError={(e) => {
                      (e.currentTarget as HTMLImageElement).classList.add('poster-missing');
                    }}
                  />
                </span>
                <div className="poster-body">
                  <div className="poster-title" title={it.title}>
                    {it.title || '（无标题）'}
                  </div>
                  <div className="muted small">
                    {it.year ?? ''}
                    {it.seasonNumber != null ? ` S${it.seasonNumber}` : ''}
                    {it.episodeNumber != null ? `E${it.episodeNumber}` : ''}
                    {it.kind === 'series' ? ' 剧集' : ''}
                  </div>
                </div>
              </Link>
            ))}
          </div>

          {data.total > PAGE_SIZE && (
            <div className="row" style={{ marginTop: 14 }}>
              <button
                type="button"
                className="btn btn-sm"
                disabled={page === 0}
                onClick={() => setParams(buildParams(kind, sort, page - 1))}
              >
                上一页
              </button>
              <span className="faint">
                第 {page + 1} / {totalPages} 页
              </span>
              <button
                type="button"
                className="btn btn-sm"
                disabled={(page + 1) * PAGE_SIZE >= data.total}
                onClick={() => setParams(buildParams(kind, sort, page + 1))}
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
