import { useEffect, useState } from 'react';
import { Link, useParams, useSearchParams } from 'react-router-dom';
import { ApiError, api } from '../api';
import { t, useI18n } from '../i18n';
import { kindLabel } from '../media';
import type { BrowsePage, LibrarySummary } from '../api';

/**
 * 海报墙：一个媒体库里「给人看」的入口。
 *
 * 与「媒体库」页的分工：那边是原始条目表（含季与集，排查问题用），
 * 这边只要顶层条目（电影、剧集），按海报铺开、可按年份与最近添加排序。
 *
 * 点卡片：剧集 → 剧集视图（季/集），电影 → 条目页。
 */

/** 类型标签页（写成函数：标签要跟着语言走）。 */
function kindTabs(): { v: string; l: string }[] {
  return [
    { v: '', l: t('全部') },
    { v: 'movie', l: t('电影') },
    { v: 'series', l: t('剧集') },
  ];
}

/** 排序选项。 */
function sortOptions(): { v: string; l: string }[] {
  return [
    { v: 'title', l: t('按标题') },
    { v: 'year', l: t('按年份') },
    { v: 'added', l: t('最近添加') },
  ];
}

const PAGE_SIZE = 60;

export function Browse() {
  const { t } = useI18n();
  const params0 = useParams();
  const libraryId = Number(params0.id);
  const [params, setParams] = useSearchParams();
  const kind = params.get('kind') ?? '';
  const sort = params.get('sort') ?? 'title';
  const page = Math.max(0, Number(params.get('page') ?? '0') || 0);

  const [data, setData] = useState<BrowsePage | null>(null);
  const [library, setLibrary] = useState<LibrarySummary | null>(null);
  // 库列表：多个库时在顶上给出切换入口（只有一个库就不显示，免得白占一行）
  const [libs, setLibs] = useState<LibrarySummary[]>([]);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let alive = true;
    api
      .libraries()
      .then((r) => {
        if (alive) setLibs(r.libraries ?? []);
      })
      .catch(() => undefined);
    return () => {
      alive = false;
    };
  }, []);

  useEffect(() => {
    if (!Number.isFinite(libraryId) || libraryId <= 0) {
      setError(t('媒体库 id 非法'));
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
        setError(e instanceof ApiError ? e.message : t('读取失败'));
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
      {libs.length > 1 && (
        <div className="card">
          <div className="row">
            <strong className="small">{t('媒体库')}</strong>
            <div className="tabs" style={{ marginBottom: 0 }}>
              {libs.map((l) => (
                <Link
                  key={l.id}
                  className={l.id === libraryId ? 'tab active' : 'tab'}
                  to={`/library/${l.id}`}
                >
                  {l.name}
                  <span className="faint small">
                    {' '}
                    {(l.counts?.movie ?? 0) + (l.counts?.series ?? 0)}
                  </span>
                </Link>
              ))}
            </div>
          </div>
        </div>
      )}

      <div className="card">
        <div className="row">
          <h2 style={{ margin: 0 }}>{library?.name ?? t('海报墙')}</h2>
          <div className="tabs" style={{ marginBottom: 0, marginLeft: 12 }}>
            {kindTabs().map((tab) => (
              <button
                key={tab.v}
                type="button"
                className={kind === tab.v ? 'tab active' : 'tab'}
                onClick={() => setParams(buildParams(tab.v, sort))}
              >
                {t(tab.l)}
              </button>
            ))}
          </div>
          <div className="spacer" />
          <label className="field field-inline">
            <span>{t('排序')}</span>
            <select value={sort} onChange={(e) => setParams(buildParams(kind, e.target.value))}>
              {sortOptions().map((o) => (
                <option key={o.v} value={o.v}>
                  {o.l}
                </option>
              ))}
            </select>
          </label>
          <Link className="btn btn-sm" to={`/libraries?lib=${libraryId}`}>
            {t('库管理')}
          </Link>
        </div>

        {error && <div className="alert alert-error">{error}</div>}
        {!data && !error && <p className="muted">{t('正在读取…')}</p>}
        {data && (
          <p className="muted small" style={{ marginTop: 10 }}>
            {t('共 {n} 条', { n: data.total })}
            {data.total > 0
              ? t('，显示第 {a}~{b} 条', {
                  a: data.offset + 1,
                  b: Math.min(data.offset + data.items.length, data.total),
                })
              : ''}
            {busy ? t(' · 加载中…') : ''}
          </p>
        )}
        {data && data.items.length === 0 && (
          <p className="muted">
            {t('这里还没有条目。先去「库管理」扫描一次。')}
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
                to={`/item/${it.id}`}
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
                    {it.title || t('（无标题）')}
                  </div>
                  <div className="muted small">
                    {it.year ?? ''}
                    {it.seasonNumber != null ? ` S${it.seasonNumber}` : ''}
                    {it.episodeNumber != null ? `E${it.episodeNumber}` : ''}
                    {it.kind === 'series' ? ` ${kindLabel(it.kind)}` : ''}
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
                {t('上一页')}
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
                {t('下一页')}
              </button>
            </div>
          )}
        </div>
      )}
    </>
  );
}
