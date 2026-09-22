import { useCallback, useEffect, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { api } from '../api';
import type {
  ChildSummary,
  Item,
  ItemDetail,
  ItemPerson,
  ItemPlaylist,
  PlaybackProgress,
} from '../api';

/**
 * 条目详情页（M6）—— 浏览路径的终点。
 *
 * 它同时管电影、剧集与单集（`/item/{id}`）；剧集视图（`/series/{id}`）已并入这里
 * （季集列表就在下面），旧地址会重定向过来。
 *
 * 三个刻意的做法：
 *   - **数据并发拿**：条目 / 子项 / 演职员 / 相关推荐 / 文件 / 观看进度共六路，
 *     各自独立失败 —— 演职员拿不到不该让整页打不开（那是最容易「整页白屏」的写法）；
 *   - **多版本**：同一部片子有多个文件时给一个选择器，选中后播放地址带
 *     `?file=<id>`，服务端只放那一个（不然决策引擎会「帮你挑个更流畅的」）；
 *   - **演职员如实说明来源**：本地 nfo 才有，TMDB 的 credits 还没接 ——
 *     界面上要写清楚，否则用户会以为是坏了。
 */
const stateLabels: Record<string, string> = {
  local: '未刮削',
  nfo: '来自 nfo',
  matched: '已匹配',
  review: '待确认',
  manual: '已人工处理',
  failed: '没找到',
};

const roleLabels: Record<string, string> = {
  actor: '演员',
  director: '导演',
  writer: '编剧',
  producer: '制片',
  composer: '作曲',
  gueststar: '客串',
};

/** 把 ticks（100ns）换成「1 小时 23 分」。 */
function runtimeText(ticks?: number): string {
  if (!ticks || ticks <= 0) return '';
  const min = Math.round(ticks / 600000000);
  if (min < 60) return `${min} 分钟`;
  return `${Math.floor(min / 60)} 小时 ${min % 60} 分`;
}

/** 把 ticks 换成播放器时间轴那种「1:23:45」。 */
function clockText(ticks?: number): string {
  if (!ticks || ticks <= 0) return '0:00';
  const total = Math.floor(ticks / 10000000);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  const two = (n: number) => String(n).padStart(2, '0');
  return h > 0 ? `${h}:${two(m)}:${two(s)}` : `${m}:${two(s)}`;
}

function sizeText(bytes?: number): string {
  if (!bytes || bytes <= 0) return '';
  if (bytes >= 1024 ** 3) return `${(bytes / 1024 ** 3).toFixed(1)} GB`;
  return `${Math.round(bytes / 1024 ** 2)} MB`;
}

function roleLabel(role: string): string {
  return roleLabels[role.toLowerCase()] ?? role;
}

export function Detail() {
  const params = useParams();
  const itemId = Number(params.id);

  const [detail, setDetail] = useState<ItemDetail | null>(null);
  const [children, setChildren] = useState<ChildSummary | null>(null);
  const [people, setPeople] = useState<ItemPerson[] | null>(null);
  const [related, setRelated] = useState<Item[]>([]);
  const [playlist, setPlaylist] = useState<ItemPlaylist | null>(null);
  const [progress, setProgress] = useState<PlaybackProgress | null>(null);
  const [season, setSeason] = useState<number | null>(null);
  const [episodes, setEpisodes] = useState<ChildSummary | null>(null);
  const [version, setVersion] = useState<number | null>(null);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    if (!Number.isFinite(itemId) || itemId <= 0) {
      setError('条目 id 非法');
      return;
    }
    setError('');
    // 六路并发：任何一路失败都只是「那一块没有」，不影响整页
    const [d, c, p, r, pl, pg] = await Promise.all([
      api.item(itemId).catch(() => null),
      api.children(itemId).catch(() => null),
      api.itemPeople(itemId).catch(() => null),
      api.itemRelated(itemId).catch(() => null),
      api.itemPlaylist(itemId).catch(() => null),
      api.itemProgress(itemId).catch(() => null),
    ]);
    if (!d) {
      setError('读不到这个条目（可能已被删除，或者链接不对）');
      return;
    }
    setDetail(d);
    setChildren(c);
    setPeople(p?.people ?? null);
    setRelated(r?.items ?? []);
    setPlaylist(pl);
    setProgress(pg?.progress ?? null);
    // 默认选中第一个版本；只有一个版本时不显示选择器
    setVersion(pl && pl.files.length > 0 ? pl.files[0].fileId : null);
    const firstSeason = (c?.items ?? []).find((x) => x.kind === 'season');
    setSeason(firstSeason ? firstSeason.id : null);
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
      .catch(() => {
        if (alive) setEpisodes({ item: {} as never, items: [], counts: {} });
      });
    return () => {
      alive = false;
    };
  }, [season]);

  if (error && !detail) {
    return (
      <div className="card">
        <h2>读不到这个条目</h2>
        <div className="alert alert-error">{error}</div>
        <Link className="btn" to="/posters">
          回海报墙
        </Link>
      </div>
    );
  }
  if (!detail) return <p className="muted">正在读取…</p>;

  const it = detail.item;
  const isSeries = it.kind === 'series';
  const isSeason = it.kind === 'season';
  const isEpisode = it.kind === 'episode';

  const seasons = (children?.items ?? []).filter((x) => x.kind === 'season');
  const directEpisodes = (children?.items ?? []).filter((x) => x.kind === 'episode');
  const episodeItems: Item[] = seasons.length > 0 ? (episodes?.items ?? []) : directEpisodes;
  const episodeTotal =
    seasons.length > 0
      ? seasons.reduce((n, s) => n + (children?.counts[String(s.id)] ?? 0), 0)
      : directEpisodes.length;
  const loadingEpisodes = seasons.length > 0 && episodes === null;

  const versions = playlist?.files ?? [];
  const chosen = versions.find((f) => f.fileId === version) ?? versions[0];
  const playURL = version ? `/play/${it.id}?file=${version}` : `/play/${it.id}`;
  const watched = progress && progress.positionTicks > 0 && !progress.played;
  const percent =
    progress && it.runtimeTicks && progress.positionTicks > 0
      ? Math.min(100, Math.round((progress.positionTicks / it.runtimeTicks) * 100))
      : 0;

  return (
    <>
      {/* 头部：背景图 + 海报 + 元数据 + 操作 */}
      <div className="detail-hero">
        <div className="detail-hero-bg">
          <img
            src={`/api/v1/items/${it.id}/images/backdrop?w=1600`}
            alt=""
            loading="lazy"
            onError={(e) => {
              (e.currentTarget as HTMLImageElement).style.display = 'none';
            }}
          />
        </div>
        <div className="detail-hero-inner">
          <img
            className="detail-poster"
            src={`/api/v1/items/${it.id}/images/poster?w=300`}
            alt=""
            onError={(e) => {
              (e.currentTarget as HTMLImageElement).classList.add('poster-missing');
            }}
          />
          <div className="detail-body">
            <h2>{it.title || '（无标题）'}</h2>
            <p className="muted small">
              {[
                isSeries ? '剧集' : isSeason ? '季' : isEpisode ? '集' : '电影',
                it.year ? String(it.year) : '',
                runtimeText(it.runtimeTicks),
                it.communityRating ? `★ ${it.communityRating.toFixed(1)}` : '',
                it.officialRating,
                isSeries && episodeTotal > 0 ? `${seasons.length} 季 / ${episodeTotal} 集` : '',
              ]
                .filter(Boolean)
                .join(' · ')}
            </p>
            {it.originalTitle && it.originalTitle !== it.title && (
              <p className="faint small">原名 {it.originalTitle}</p>
            )}
            {it.tagline && <p className="detail-tagline">{it.tagline}</p>}
            {(it.genres ?? []).length > 0 && (
              <p className="row detail-genres">
                {(it.genres ?? []).map((g) => (
                  <span key={g} className="badge">
                    {g}
                  </span>
                ))}
              </p>
            )}
            {(it.studios?.length ?? 0) > 0 && (
              <p className="faint small">制片 {(it.studios ?? []).join('、')}</p>
            )}
            {it.overview && <p className="detail-overview">{it.overview}</p>}

            {watched && (
              <p className="small muted">
                已看 {clockText(progress?.positionTicks)}
                {percent > 0 ? `（${percent}%）` : ''}
              </p>
            )}
            {progress?.played && <p className="small">已看完</p>}

            <div className="row" style={{ marginTop: 10 }}>
              {!isSeries && !isSeason && (
                <Link className="btn btn-primary" to={playURL}>
                  {watched ? '继续播放' : '播放'}
                </Link>
              )}
              {watched && !isSeries && !isSeason && (
                <Link className="btn" to={`/play/${it.id}?restart=1`}>
                  从头播放
                </Link>
              )}
              {versions.length > 1 && (
                <label className="field-inline">
                  <span>版本</span>
                  <select value={version ?? ''} onChange={(e) => setVersion(Number(e.target.value))}>
                    {versions.map((f) => (
                      <option key={f.fileId} value={f.fileId}>
                        {[
                          f.containerKind.toUpperCase(),
                          f.video[0] ? `${f.video[0].codec.toUpperCase()} ${f.video[0].width}×${f.video[0].height}` : '',
                          sizeText(f.sizeBytes),
                        ]
                          .filter(Boolean)
                          .join(' · ')}
                      </option>
                    ))}
                  </select>
                </label>
              )}
              <Link className="btn" to={`/items/${it.id}`}>
                编辑元数据
              </Link>
              <Link className="btn btn-ghost" to={`/library/${it.libraryId}`}>
                回海报墙
              </Link>
            </div>
            <p className="faint small" style={{ marginTop: 8 }}>
              状态 {stateLabels[it.matchState ?? ''] ?? it.matchState}
              {it.metadataSource ? ` · 元数据来源 ${it.metadataSource}` : ''}
              {chosen ? ` · 文件 ${chosen.containerKind.toUpperCase()} ${sizeText(chosen.sizeBytes)}` : ''}
            </p>
          </div>
        </div>
      </div>

      {/* 季与集（剧集才有） */}
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
      {loadingEpisodes && (
        <div className="card">
          <p className="muted">正在读取集列表…</p>
        </div>
      )}
      {episodeItems.length > 0 && (
        <div className="card">
          <h2>集（{episodeItems.length}）</h2>
          <div className="ep-list">
            {episodeItems.map((ep) => (
              <div key={ep.id} className="ep-row">
                <Link className="ep-main" to={`/item/${ep.id}`}>
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
                    </div>
                    {ep.overview && <div className="muted small search-clamp">{ep.overview}</div>}
                  </div>
                </Link>
                <div className="ep-actions">
                  <Link className="btn btn-sm btn-primary" to={`/play/${ep.id}`}>
                    播放
                  </Link>
                  <Link className="btn btn-sm btn-ghost" to={`/item/${ep.id}`}>
                    详情
                  </Link>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* 演职员 */}
      <div className="card">
        <h2>演职员{people && people.length > 0 ? `（${people.length}）` : ''}</h2>
        {people && people.length > 0 ? (
          <>
            <ul className="cast-list">
              {people.map((p) => (
                <li key={`${p.personId}-${p.role}`} className="cast-item">
                  <span className="cast-avatar">{p.name.slice(0, 1)}</span>
                  <span className="cast-body">
                    <span className="cast-name">{p.name}</span>
                    <span className="faint small">
                      {roleLabel(p.role)}
                      {p.character ? ` · ${p.character}` : ''}
                    </span>
                  </span>
                </li>
              ))}
            </ul>
            <p className="faint small" style={{ marginTop: 8 }}>
              演职员来自媒体同目录的 nfo（本地优先，不联网）。TMDB 的演职员还没接，
              所以没有 nfo 的条目这里是空的。
            </p>
          </>
        ) : (
          <p className="muted small">
            这条的同目录 nfo 里没有演职员信息。换成自带演职员的 nfo 之后，
            在「库管理」里勾上「重读 nfo」重扫一次就会出现（不必改媒体文件）。
          </p>
        )}
      </div>

      {/* 相关推荐 */}
      {related.length > 0 && (
        <div className="card">
          <h2>相关推荐</h2>
          <p className="hint">同库里与它流派相近的（评分高的在前）—— 没配 TMDB 也算得出来。</p>
          <div className="poster-grid">
            {related.map((r) => (
              <Link key={r.id} className="poster-card" to={`/item/${r.id}`}>
                {/* 与海报墙同构：占位块撑住 2:3 的比例，图绝对定位盖在上面。
                    少了这层 .poster-frame，.poster-img（absolute + inset:0）会去贴
                    最近的一个定位祖先，直接把整页盖住（实测踩到）。 */}
                <span className="poster-frame">
                  <span className="poster-ph">{(r.title || '?').slice(0, 1)}</span>
                  <img
                    className="poster-img"
                    src={`/api/v1/items/${r.id}/images/poster?w=300`}
                    alt=""
                    loading="lazy"
                    onError={(e) => {
                      (e.currentTarget as HTMLImageElement).classList.add('poster-missing');
                    }}
                  />
                </span>
                <div className="poster-body">
                  <div className="poster-title">{r.title || '（无标题）'}</div>
                  <div className="muted small">{[r.year, runtimeText(r.runtimeTicks)].filter(Boolean).join(' · ')}</div>
                </div>
              </Link>
            ))}
          </div>
        </div>
      )}

      <p className="faint small">
        <Link to={`/items/${it.id}`}>
          条目 #{it.id}
        </Link>{' '}
        · 更新于 {it.updatedAt ? new Date(it.updatedAt).toLocaleString() : '—'}
        {it.scrapeError ? ` · 刮削留言：${it.scrapeError}` : ''}
      </p>
    </>
  );
}
