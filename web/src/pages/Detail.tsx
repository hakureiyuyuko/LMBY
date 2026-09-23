import { useCallback, useEffect, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { ApiError, api } from '../api';
import { t, useI18n } from '../i18n';
import { roleLabel } from '../people';
import type {
  ChildSummary,
  FavoriteState,
  Item,
  ItemDetail,
  ItemPerson,
  ItemPlaylist,
  PlaybackProgress,
  PlaylistSummary,
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
/** 把 ticks（100ns）换成「1 小时 23 分」。 */
function runtimeText(ticks?: number): string {
  if (!ticks || ticks <= 0) return '';
  const min = Math.round(ticks / 600000000);
  if (min < 60) return t('{n} 分钟', { n: min });
  return t('{h} 小时 {m} 分', { h: Math.floor(min / 60), m: min % 60 });
}

/** 匹配状态 → 人话。是**函数而不是常量表**：标签要在渲染时取当前语言，
 * 模块级常量表会在 import 时就把语言钉死（切语言后不跟着变）。 */
function stateLabel(state: string | undefined): string {
  switch (state) {
    case 'local':
      return t('未刮削');
    case 'nfo':
      return t('来自 nfo');
    case 'matched':
      return t('已匹配');
    case 'review':
      return t('待确认');
    case 'manual':
      return t('已人工处理');
    case 'failed':
      return t('没找到');
    default:
      return state ?? '';
  }
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

export function Detail() {
  const { t } = useI18n();
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
  // 收藏状态（按账号）：单独一个接口，不混进条目本体（见 internal/api/lists.go 的说明）
  const [fav, setFav] = useState<FavoriteState | null>(null);
  // 「加入列表」面板：只在真点开时才去拉列表（绝大多数访问不看这个面板）
  const [listPanel, setListPanel] = useState(false);
  const [lists, setLists] = useState<PlaylistSummary[] | null>(null);
  const [newListName, setNewListName] = useState('');
  const [listMsg, setListMsg] = useState('');
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    if (!Number.isFinite(itemId) || itemId <= 0) {
      setError(t('条目 id 非法'));
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
      setError(t('读不到这个条目（可能已被删除，或者链接不对）'));
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

  // 收藏状态：读失败就当作「没收藏」（按钮仍然可点，点了会以服务端为准）
  useEffect(() => {
    if (!Number.isFinite(itemId) || itemId <= 0) return;
    let alive = true;
    api
      .favoriteState(itemId)
      .then((res) => {
        if (alive) setFav(res);
      })
      .catch(() => {
        if (alive) setFav(null);
      });
    return () => {
      alive = false;
    };
  }, [itemId]);

  /** 收藏 / 取消收藏：响应里带新状态与新的总数，直接拿来更新按钮，不再多查一次。 */
  const toggleFavorite = useCallback(async () => {
    try {
      setFav(await api.setFavorite(itemId, !(fav?.favorite ?? false)));
    } catch {
      /* 失败就保持原样，下次刷新再说（不弹一个只有技术细节的错） */
    }
  }, [itemId, fav]);

  const loadLists = useCallback(async () => {
    try {
      setLists((await api.lists()).playlists);
    } catch {
      setLists([]);
    }
  }, []);

  /** 点「加入列表」才去拉列表；拉过就不再拉（除了加入之后要刷新计数）。 */
  const openListPanel = useCallback(async () => {
    setListPanel((open) => !open);
    setListMsg('');
    if (lists === null) await loadLists();
  }, [lists, loadLists]);

  /** 加入一个已有列表；响应里的 added 能区分「刚加进去」与「本来就在里面」。 */
  const addTo = useCallback(
    async (listId: number, name: string) => {
      try {
        const res = await api.addToList(listId, [itemId]);
        setListMsg(res.added > 0 ? t('已加入「{name}」', { name }) : t('「{name}」里本来就有这个条目', { name }));
        await loadLists();
      } catch (e) {
        setListMsg(e instanceof ApiError ? e.message : t('加入列表失败'));
      }
    },
    [itemId, loadLists],
  );

  /** 新建一个列表并立刻把当前条目加进去（新建完还得手动加一次是很傻的）。 */
  const createAndAdd = useCallback(
    async (e: React.FormEvent) => {
      e.preventDefault();
      const name = newListName.trim();
      if (!name) return;
      try {
        const created = await api.createList({ name });
        setNewListName('');
        await addTo(created.playlist.id, created.playlist.name);
      } catch (e2) {
        setListMsg(e2 instanceof ApiError ? e2.message : t('新建列表失败'));
      }
    },
    [newListName, addTo],
  );

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
        <h2>{t('读不到这个条目')}</h2>
        <div className="alert alert-error">{error}</div>
        <Link className="btn" to="/posters">
          {t('回海报墙')}
        </Link>
      </div>
    );
  }
  if (!detail) return <p className="muted">{t('正在读取…')}</p>;

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
            <h2>{it.title || t('（无标题）')}</h2>
            <p className="muted small">
              {[
                isSeries ? t('剧集') : isSeason ? t('季') : isEpisode ? t('集') : t('电影'),
                it.year ? String(it.year) : '',
                runtimeText(it.runtimeTicks),
                it.communityRating ? `★ ${it.communityRating.toFixed(1)}` : '',
                it.officialRating,
                isSeries && episodeTotal > 0 ? t('{s} 季 / {e} 集', { s: seasons.length, e: episodeTotal }) : '',
              ]
                .filter(Boolean)
                .join(' · ')}
            </p>
            {it.originalTitle && it.originalTitle !== it.title && (
              <p className="faint small">{t('原名')} {it.originalTitle}</p>
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
              <p className="faint small">{t('制片')} {(it.studios ?? []).join('、')}</p>
            )}
            {it.overview && <p className="detail-overview">{it.overview}</p>}

            {watched && (
              <p className="small muted">
                {t('已看')} {clockText(progress?.positionTicks)}
                {percent > 0 ? `（${percent}%）` : ''}
              </p>
            )}
            {progress?.played && <p className="small">{t('已看完')}</p>}

            <div className="row" style={{ marginTop: 10 }}>
              {!isSeries && !isSeason && (
                <Link className="btn btn-primary" to={playURL}>
                  {watched ? t('继续播放') : t('播放')}
                </Link>
              )}
              {watched && !isSeries && !isSeason && (
                <Link className="btn" to={`/play/${it.id}?restart=1`}>
                  {t('从头播放')}
                </Link>
              )}
              <button
                type="button"
                className={`btn btn-fav${fav?.favorite ? ' on' : ''}`}
                aria-pressed={fav?.favorite ?? false}
                data-favorite={fav?.favorite ? 'on' : 'off'}
                onClick={() => void toggleFavorite()}
              >
                ★ {fav?.favorite ? t('已收藏') : t('收藏')}
              </button>
              {(fav?.count ?? 0) > 0 && (
                <span className="faint small">{t('{n} 人收藏', { n: fav?.count ?? 0 })}</span>
              )}
              {versions.length > 1 && (
                <label className="field-inline">
                  <span>{t('版本')}</span>
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
                {t('编辑元数据')}
              </Link>
              <Link className="btn btn-ghost" to={`/library/${it.libraryId}`}>
                {t('回海报墙')}
              </Link>
              <button
                type="button"
                className="btn"
                data-add-to-list="toggle"
                aria-expanded={listPanel}
                onClick={() => void openListPanel()}
              >
                ＋ {t('加入列表')}
              </button>
            </div>

            {/* 加入列表的面板：列我的列表 + 合集；没列表时给「新建」这句就有用了 */}
            {listPanel && (
              <div className="list-picker" data-list-picker>
                {listMsg && <p className="small">{listMsg}</p>}
                {lists === null && <p className="muted small">{t('正在读取列表…')}</p>}
                {lists !== null && lists.length === 0 && (
                  <p className="muted small">{t('还没有列表，在下面新建一个。')}</p>
                )}
                {lists !== null && lists.length > 0 && (
                  <ul className="list-picker-items">
                    {lists.map((p) => (
                      <li key={p.id}>
                        <button
                          type="button"
                          className="btn btn-sm"
                          data-add-to={p.id}
                          onClick={() => void addTo(p.id, p.name)}
                        >
                          {t('加入「{name}」', { name: p.name })}
                        </button>
                        <span className="faint small">
                          {p.kind === 'collection' ? t('合集') : t('列表')} · {t('{n} 个条目', { n: p.itemCount })}
                        </span>
                      </li>
                    ))}
                  </ul>
                )}
                <form
                  className="row"
                  style={{ marginTop: 8 }}
                  onSubmit={(e) => void createAndAdd(e)}
                >
                  <input
                    value={newListName}
                    onChange={(e) => setNewListName(e.target.value)}
                    placeholder={t('新建列表并加入')}
                    aria-label={t('新建列表名')}
                  />
                  <button type="submit" className="btn btn-sm" disabled={!newListName.trim()}>
                    {t('新建并加入')}
                  </button>
                </form>
              </div>
            )}
            <p className="faint small" style={{ marginTop: 8 }}>
              {t('状态')} {stateLabel(it.matchState)}
              {it.metadataSource ? ` · ${t('元数据来源 {src}', { src: it.metadataSource })}` : ''}
              {chosen ? ` · ${t('文件')} ${chosen.containerKind.toUpperCase()} ${sizeText(chosen.sizeBytes)}` : ''}
            </p>
          </div>
        </div>
      </div>

      {/* 季与集（剧集才有） */}
      {seasons.length > 0 && (
        <div className="card">
          <div className="row">
            <h2 style={{ margin: 0 }}>{t('季')}</h2>
            <div className="tabs" style={{ marginBottom: 0, marginLeft: 12 }}>
              {seasons.map((s) => (
                <button
                  key={s.id}
                  type="button"
                  className={season === s.id ? 'tab active' : 'tab'}
                  onClick={() => setSeason(s.id)}
                >
                  {t('第 {n} 季', { n: s.seasonNumber ?? '?' })}
                  <span className="faint"> · {t('{n} 集', { n: children?.counts[String(s.id)] ?? 0 })}</span>
                </button>
              ))}
            </div>
          </div>
        </div>
      )}
      {loadingEpisodes && (
        <div className="card">
          <p className="muted">{t('正在读取集列表…')}</p>
        </div>
      )}
      {episodeItems.length > 0 && (
        <div className="card">
          <h2>{t('集（{n}）', { n: episodeItems.length })}</h2>
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
                      {ep.title || t('（无标题）')}
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
                    {t('播放')}
                  </Link>
                  <Link className="btn btn-sm btn-ghost" to={`/item/${ep.id}`}>
                    {t('详情')}
                  </Link>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* 演职员 */}
      <div className="card">
        <h2>{t('演职员')}{people && people.length > 0 ? t('（{n}）', { n: people.length }) : ''}</h2>
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
            <p className="faint small" style={{ marginTop: 8 }}>{t('演职员信息来自媒体目录里的 nfo。')}
            </p>
          </>
        ) : (
          <p className="muted small">{t('这条的 nfo 里没有演职员信息。')}
          </p>
        )}
      </div>

      {/* 相关推荐 */}
      {related.length > 0 && (
        <div className="card">
          <h2>{t('相关推荐')}</h2>
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
                  <div className="poster-title">{r.title || t('（无标题）')}</div>
                  <div className="muted small">{[r.year, runtimeText(r.runtimeTicks)].filter(Boolean).join(' · ')}</div>
                </div>
              </Link>
            ))}
          </div>
        </div>
      )}

      <p className="faint small">
        <Link to={`/items/${it.id}`}>
          {t('条目 #{id}', { id: it.id })}
        </Link>{' '}
        · {t('更新于 {when}', { when: it.updatedAt ? new Date(it.updatedAt).toLocaleString() : '—' })}
        {it.scrapeError ? ` · ${t('刮削留言：{msg}', { msg: it.scrapeError })}` : ''}
      </p>
    </>
  );
}
