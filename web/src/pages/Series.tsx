import { useCallback, useEffect, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { ApiError, api } from '../api';
import type { ChildSummary, Item, ItemDetail } from '../api';

/**
 * 剧集视图：一部剧的季与集。
 *
 * 层级来自扫描器推断出的目录结构（剧集 → 季 → 集）；有些库里集直接挂在剧集下，
 * 所以两种都处理：有季就按季分标签，没季就直接列集。
 *
 * 每一集点进去是条目页（改字段/加锁/看技术信息都在那里）。
 */

const stateLabels: Record<string, string> = {
  local: '未刮削',
  nfo: '来自 nfo',
  matched: '已匹配',
  review: '待确认',
  manual: '已人工处理',
  failed: '没找到',
};

/** 把 ticks（100ns）换成「1 小时 23 分」。 */
function runtimeText(ticks?: number): string {
  if (!ticks || ticks <= 0) return '';
  const min = Math.round(ticks / 600000000);
  if (min < 60) return `${min} 分钟`;
  return `${Math.floor(min / 60)} 小时 ${min % 60} 分`;
}

export function SeriesView() {
  const params0 = useParams();
  const itemId = Number(params0.id);

  const [detail, setDetail] = useState<ItemDetail | null>(null);
  const [children, setChildren] = useState<ChildSummary | null>(null);
  const [season, setSeason] = useState<number | null>(null);
  const [episodes, setEpisodes] = useState<ChildSummary | null>(null);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    if (!Number.isFinite(itemId) || itemId <= 0) {
      setError('条目 id 非法');
      return;
    }
    try {
      const [d, c] = await Promise.all([api.item(itemId), api.children(itemId)]);
      setDetail(d);
      setChildren(c);
      setError('');
      // 有季就默认选第一季（用户点进来最想看到最新/第一季的集列表）
      const firstSeason = c.items.find((x) => x.kind === 'season');
      setSeason(firstSeason ? firstSeason.id : null);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : '读取失败');
    }
  }, [itemId]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    if (season == null) {
      setEpisodes(null);
      return;
    }
    let alive = true;
    api
      .children(season)
      .then((res) => {
        if (alive) setEpisodes(res);
      })
      .catch((e) => {
        if (alive) setError(e instanceof ApiError ? e.message : '读取集列表失败');
      });
    return () => {
      alive = false;
    };
  }, [season]);

  async function rescrape() {
    if (
      !window.confirm(
        '重新刮削这部剧？TMDB 的值会写进**未锁定**的字段（锁住的不动），跑完点「刷新」看结果。',
      )
    ) {
      return;
    }
    setBusy(true);
    setNotice('');
    try {
      const res = await api.rescrapeItem(itemId, true);
      setNotice(res.enqueued ? '已入队，稍后点「刷新」看结果。' : '队列里已经有这条的任务了。');
    } catch (e) {
      setError(e instanceof ApiError ? e.message : '入队失败');
    } finally {
      setBusy(false);
    }
  }

  if (!detail) {
    return (
      <div className="card">
        {error ? <div className="alert alert-error">{error}</div> : <p className="muted">正在读取…</p>}
      </div>
    );
  }

  const it = detail.item;
  const seasons = (children?.items ?? []).filter((x) => x.kind === 'season');
  const directEpisodes = (children?.items ?? []).filter((x) => x.kind === 'episode');
  const episodeItems: Item[] = seasons.length > 0 ? (episodes?.items ?? []) : directEpisodes;
  const episodeTotal = seasons.length > 0
    ? seasons.reduce((n, s) => n + (children?.counts[String(s.id)] ?? 0), 0)
    : directEpisodes.length;
  // 选了季但集还没拿到时不算「没有集」—— 否则加载中的那一瞬时界面会闪一句
  // 「这部剧下面还没有季或集」，看起来像是数据丢了。
  const loadingEpisodes = seasons.length > 0 && episodes === null;
  const noChildren = children !== null && children.items.length === 0;

  return (
    <>
      <div className="card">
        <div className="item-head">
          <img
            className="item-poster"
            src={`/api/v1/items/${it.id}/images/poster?w=300`}
            alt=""
            onError={(e) => {
              (e.currentTarget as HTMLImageElement).style.visibility = 'hidden';
            }}
          />
          <div className="item-head-body">
            <h2>{it.title || '（无标题）'}</h2>
            <p className="muted small">
              剧集{it.year ? ` · ${it.year}` : ''} · {seasons.length} 季 / {episodeTotal} 集 · 状态{' '}
              {stateLabels[it.matchState ?? ''] ?? it.matchState}
              {it.metadataSource ? ` · 元数据来源 ${it.metadataSource}` : ''}
            </p>
            {it.originalTitle && <p className="muted small">原名 {it.originalTitle}</p>}
            {it.overview && <p className="small">{it.overview}</p>}
            <div className="row" style={{ marginTop: 10 }}>
              <Link className="btn" to={`/library/${it.libraryId}`}>
                返回海报墙
              </Link>
              <Link className="btn" to={`/items/${it.id}`}>
                编辑字段与锁定
              </Link>
              {detail.scrapeConfigured && (
                <button type="button" className="btn" disabled={busy} onClick={() => void rescrape()}>
                  重新刮削这部剧
                </button>
              )}
              <button type="button" className="btn btn-ghost" disabled={busy} onClick={() => void load()}>
                刷新
              </button>
            </div>
          </div>
        </div>
        {error && <div className="alert alert-error">{error}</div>}
        {notice && <div className="alert alert-ok">{notice}</div>}
      </div>

      {noChildren && (
        <div className="card">
          <p className="muted">
            这部剧下面还没有季或集。如果媒体目录里其实有，先回「库管理」扫描一次。
          </p>
        </div>
      )}

      {loadingEpisodes && (
        <div className="card">
          <p className="muted">正在读取集列表…</p>
        </div>
      )}

      {seasons.length > 0 && (
        <div className="card">
          <div className="row">
            <h2 style={{ margin: 0 }}>季</h2>
            <div className="tabs" style={{ marginBottom: 0, marginLeft: 12 }}>
              {seasons.map((s) => (
                <button
                  key={s.id}
                  type="button"
                  className={season === s.id ? 'tab active' : 'tab'}
                  onClick={() => setSeason(s.id)}
                >
                  第 {s.seasonNumber ?? '?'} 季
                  <span className="faint"> · {children?.counts[String(s.id)] ?? 0} 集</span>
                </button>
              ))}
            </div>
          </div>
        </div>
      )}

      {episodeItems.length > 0 && (
        <div className="card">
          <h2>集（{episodeItems.length}）</h2>
          <div className="ep-list">
            {episodeItems.map((ep) => (
              <Link key={ep.id} className="ep-row" to={`/items/${ep.id}`}>
                <img
                  className="ep-thumb"
                  src={`/api/v1/items/${ep.id}/images/thumb?w=200`}
                  alt=""
                  loading="lazy"
                  onError={(e) => {
                    (e.currentTarget as HTMLImageElement).classList.add('poster-missing');
                  }}
                />
                <div className="ep-body">
                  <div className="ep-title">
                    <span className="faint">
                      S{String(ep.seasonNumber ?? 0).padStart(2, '0')}
                      E{String(ep.episodeNumber ?? 0).padStart(2, '0')}
                    </span>{' '}
                    {ep.title || '（无标题）'}
                  </div>
                  <div className="muted small">
                    {ep.year ? `${ep.year} · ` : ''}
                    {runtimeText(ep.runtimeTicks)}
                    {ep.matchState ? ` · ${stateLabels[ep.matchState] ?? ep.matchState}` : ''}
                  </div>
                  {ep.overview && <div className="muted small search-clamp">{ep.overview}</div>}
                </div>
              </Link>
            ))}
          </div>
        </div>
      )}
    </>
  );
}
