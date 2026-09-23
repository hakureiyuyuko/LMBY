import { useCallback, useEffect, useRef, useState } from 'react';
import type { ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { api } from '../api';
import type { ContinueWatchingEntry, HomePayload, HomeSection, HomeTaste, Item } from '../api';
import { formatClock } from '../capabilities';
import { useI18n } from '../i18n';
import { kindLabel } from '../media';

/**
 * 首页（Netflix 风格）。
 *
 * 结构：顶部一条**大屏轮播**（最近更新/入库的 10 条），下面若干**横滑行**：
 * 继续观看 / 为你推荐（按观看历史的流派画像）/ 最近添加。
 *
 * 几个刻意的做法：
 *   - **整页一个请求**（`GET /api/v1/home`）：首屏感觉直接由请求数决定，而且几行之间有
 *     先后关系（有没有观看记录决定出「为你推荐」还是「评分最高」），拆开就得让前端
 *     去判断只有后端才知道的事；
 *   - **轮播 8 秒一张、鼠标悬停即暂停**：自动轮播是「展示」，用户动手时就不该再抢节奏；
 *     另外给了左右箭头与圆点，键盘/触屏用户不必等；
 *   - **宽幅背景优先，缺图退回海报**：`backdrop` 是宽图，很多库没有；
 *     退到海报再退到「隐掉图片留底色渐变」，任何库都不会出现破图；
 *   - 行里复用海报墙的卡片样式（`.poster-card` / `.poster-frame`），
 *     所以「卡片长什么样」全站只有一套定义。
 *
 * M6 调整：原本贴在页面底部的「服务状态」自检面板搬到了设置页 ——
 * 它是「出问题时才看」的信息，不该占首页的位置（browser-test 也跟着改了）。
 */

/** 轮播间隔：够看清简介，又不至于让人等得想起要拖动。 */
const HERO_ROTATE_MS = 8000;

export function Home() {
  const { t } = useI18n();
  const [data, setData] = useState<HomePayload | null>(null);
  const [error, setError] = useState('');
  const [hero, setHero] = useState(0);
  const [paused, setPaused] = useState(false);

  const load = useCallback(async () => {
    try {
      setData(await api.home());
      setError('');
    } catch {
      setError(t('读取首页失败'));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  /**
   * 行的标题/副标题**按 key 本地化**，服务端给的中文只当兜底。
   *
   * 为什么这样做：API 要保持语言中立（它是给所有客户端与脚本用的），而文案属于界面。
   * `key` 是稳定标识，界面按它自己写话 —— 于是切英文不必重启服务，也不必给后端做双语数据。
   */
  function rowTitle(s: HomeSection): string {
    switch (s.key) {
      case 'continue':
        return t('继续观看');
      case 'favorites':
        return t('我的收藏');
      case 'recommend':
        return t('为你推荐');
      case 'recent':
        return t('最近添加');
      case 'top':
        return t('评分最高');
      default:
        return s.title;
    }
  }

  /** 副标题同理；推荐那一行用后端一并返回的「口味画像」拼出真正的依据。 */
  function rowSubtitle(s: HomeSection): string | undefined {
    switch (s.key) {
      case 'continue':
        return t('进度按账号独立保存');
      case 'favorites':
        return t('你收藏过的 {n} 个条目', { n: s.items.length });
      case 'recent':
        return t('刚入库或元数据刚更新过的');
      case 'top':
        return t('还没有观看记录，先从这些开始');
      case 'recommend': {
        const genres = (s.taste ?? [])
          .slice(0, 2)
          .map((x) => x.genre)
          .join(' · ');
        if (s.sourceWorks === 1 && s.seedTitle) {
          return t('因为你看过《{title}》', { title: s.seedTitle });
        }
        if (genres) {
          return t('因为你喜欢 {genres}（最近看过 {n} 部作品）', {
            genres,
            n: s.sourceWorks ?? 0,
          });
        }
        return t('根据你最近看过的 {n} 部作品', { n: s.sourceWorks ?? 0 });
      }
      default:
        return s.subtitle;
    }
  }

  const heroItems = data?.hero ?? [];

  // 轮播：数据变化时把下标夹回范围内（否则刷新后可能停在一个空位）
  useEffect(() => {
    setHero((i) => (heroItems.length === 0 ? 0 : Math.min(i, heroItems.length - 1)));
  }, [heroItems.length]);

  useEffect(() => {
    if (heroItems.length <= 1 || paused) return;
    const timer = window.setInterval(
      () => setHero((i) => (i + 1) % heroItems.length),
      HERO_ROTATE_MS,
    );
    return () => window.clearInterval(timer);
  }, [heroItems.length, paused]);

  const current = heroItems[hero];
  // 这一条（或它所属的剧）有没有看了一半的记录 —— 有就把「播放」换成「继续播放」
  const resume = current
    ? data?.continue.find(
        (e) => e.item.id === current.id || (current.seriesId && e.item.seriesId === current.seriesId),
      )
    : undefined;

  return (
    <div className="home">
      {error && <div className="alert alert-error">{error}</div>}
      {!data && !error && <p className="muted">{t('正在加载首页…')}</p>}

      {current && (
        <section
          className="hero"
          data-hero-id={current.id}
          onMouseEnter={() => setPaused(true)}
          onMouseLeave={() => setPaused(false)}
        >
          {/* key 换成新条目时重新挂载 → CSS 的淡入动画每张都从 0 开始 */}
          <div className="hero-media" key={current.id}>
            <img
              className="hero-img"
              src={`/api/v1/items/${current.id}/images/backdrop?w=1600`}
              alt=""
              onError={(e) => {
                const img = e.currentTarget;
                // 没有宽幅背景 → 退到海报（竖图会被裁，但总比破图强）
                if (!img.dataset.fallback) {
                  img.dataset.fallback = '1';
                  img.src = `/api/v1/items/${current.id}/images/poster?w=1000`;
                  return;
                }
                img.style.visibility = 'hidden';
              }}
            />
          </div>
          <span className="hero-scrim" aria-hidden="true" />

          <div className="hero-body">
            <div className="hero-kind">
              {kindLabel(current.kind)}
              {current.year ? ` · ${current.year}` : ''}
              {current.officialRating ? ` · ${current.officialRating}` : ''}
              {current.communityRating ? ` · ★ ${current.communityRating.toFixed(1)}` : ''}
            </div>
            <h1 className="hero-title">{current.title || t('（无标题）')}</h1>
            {current.genres && current.genres.length > 0 && (
              <div className="hero-genres">
                {current.genres.slice(0, 4).map((g) => (
                  <span className="badge" key={g}>
                    {g}
                  </span>
                ))}
              </div>
            )}
            {current.overview && <p className="hero-overview">{current.overview}</p>}

            <div className="row hero-actions">
              <Link
                className="btn btn-primary hero-play"
                to={resume ? `/play/${resume.item.id}` : `/play/${current.id}`}
              >
                ▶ {resume ? t('继续播放') : t('播放')}
              </Link>
              <Link className="btn" to={`/item/${current.id}`}>
                {t('详情')}
              </Link>
            </div>

            {resume && (
              <div className="hero-resume">
                <span className="hero-progress">
                  <span style={{ width: `${progressPct(resume)}%` }} />
                </span>
                <span className="faint small">
                  {resume.item.title} ·{' '}
                  {t('看到 {t}', { t: formatClock(resume.progress.positionTicks / 10_000_000) })}
                </span>
              </div>
            )}
          </div>

          {heroItems.length > 1 && (
            <div className="hero-nav">
              <button
                type="button"
                className="hero-arrow"
                aria-label={t('轮播上一部')}
                onClick={() => setHero((i) => (i - 1 + heroItems.length) % heroItems.length)}
              >
                ‹
              </button>
              <div className="hero-dots">
                {heroItems.map((h, i) => (
                  <button
                    type="button"
                    key={h.id}
                    className={`hero-dot${i === hero ? ' on' : ''}`}
                    aria-label={t('第 {n} 部：{title}', { n: i + 1, title: h.title })}
                    data-hero-dot={i}
                    onClick={() => setHero(i)}
                  />
                ))}
              </div>
              <button
                type="button"
                className="hero-arrow"
                aria-label={t('轮播下一部')}
                onClick={() => setHero((i) => (i + 1) % heroItems.length)}
              >
                ›
              </button>
            </div>
          )}
        </section>
      )}

      {data && data.continue.length > 0 && (
        <RowBlock title={t('继续观看')} subtitle={t('进度按账号独立保存')} itemsKey="continue">
          {data.continue.map((e) => (
            <Link className="continue-card" key={e.item.id} to={`/play/${e.item.id}`}>
              <span className="continue-poster">
                <img
                  src={`/api/v1/items/${e.item.id}/images/poster?w=200`}
                  alt=""
                  loading="lazy"
                  onError={(ev) => {
                    (ev.currentTarget as HTMLImageElement).style.visibility = 'hidden';
                  }}
                />
                <span className="continue-bar">
                  <span style={{ width: `${progressPct(e)}%` }} />
                </span>
              </span>
              <span className="continue-title" title={e.item.title}>
                {e.item.title || t('（无标题）')}
              </span>
              <span className="muted small">
                {t('还剩 {t}', { t: formatClock(e.remainingTicks / 10_000_000) })}
              </span>
            </Link>
          ))}
        </RowBlock>
      )}

      {data?.sections.map((s) => (
        <RowBlock
          key={s.key}
          title={rowTitle(s)}
          subtitle={rowSubtitle(s)}
          taste={s.taste}
          itemsKey={s.key}
        >
          {s.items.map((it) => (
            <PosterCard key={it.id} item={it} />
          ))}
        </RowBlock>
      ))}

      {data && data.hero.length === 0 && data.sections.length === 0 && (
        <div className="card">
          <h2>{t('欢迎使用 LMBY')}</h2>
          <p className="muted">
            {t('媒体库里还没有内容。先去')} <Link to="/settings/libraries">{t('库管理')}</Link>{' '}
            {t('添加一个媒体库并扫描，海报、简介与演职员信息就会出现在这里。')}
          </p>
        </div>
      )}
    </div>
  );
}

/** 看到哪儿了（百分比）。 */
function progressPct(e: ContinueWatchingEntry): number {
  const pos = e.progress.positionTicks / 10_000_000;
  const total = e.progress.durationTicks / 10_000_000;
  if (total <= 0) return 0;
  return Math.min(100, Math.max(0, Math.round((pos / total) * 100)));
}

/** 一行：标题 + 副标题 + 左右箭头 + 横滑容器。 */
function RowBlock(props: {
  title: string;
  subtitle?: string;
  taste?: HomeTaste[];
  itemsKey: string;
  children: ReactNode;
}) {
  const { t } = useI18n();
  const ref = useRef<HTMLDivElement | null>(null);

  // 一次滚动 80% 可视宽度：留一点重叠，用户才知道「刚才那几张还在左边」
  function scroll(dir: number) {
    const el = ref.current;
    if (!el) return;
    el.scrollBy({ left: dir * el.clientWidth * 0.8, behavior: 'smooth' });
  }

  return (
    <section className="row-block" data-row={props.itemsKey}>
      <div className="row-head">
        <h2>{props.title}</h2>
        {props.subtitle && <span className="muted small">{props.subtitle}</span>}
        {/* 口味画像：说得出「凭什么推给我」，顺便让用户看到自己爱看哪几类 */}
        {props.taste && props.taste.length > 0 && (
          <span className="row-taste">
            {props.taste.slice(0, 3).map((t) => (
              <span className="badge" key={t.genre}>
                {t.genre}
                {t.weight > 1 ? ` ×${t.weight}` : ''}
              </span>
            ))}
          </span>
        )}
        <div className="spacer" />
        <button type="button" className="row-arrow" aria-label={t('向左滚动')} onClick={() => scroll(-1)}>
          ‹
        </button>
        <button type="button" className="row-arrow" aria-label={t('向右滚动')} onClick={() => scroll(1)}>
          ›
        </button>
      </div>
      <div className="row-scroll" ref={ref}>
        {props.children}
      </div>
    </section>
  );
}

/** 海报卡片：与海报墙、详情页的推荐用的是同一套类名（卡片全站只有一套定义）。 */
function PosterCard({ item }: { item: Item }) {
  const { t } = useI18n();
  return (
    <Link className="poster-card row-item" to={`/item/${item.id}`}>
      {/* 占位块在下、图在上：没海报时不会留下空洞 */}
      <span className="poster-frame">
        <span className="poster-ph">{(item.title || '?').slice(0, 1)}</span>
        <img
          className="poster-img"
          src={`/api/v1/items/${item.id}/images/poster?w=300`}
          alt=""
          loading="lazy"
          onError={(e) => {
            (e.currentTarget as HTMLImageElement).classList.add('poster-missing');
          }}
        />
      </span>
      <div className="poster-body">
        <div className="poster-title" title={item.title}>
          {item.title || t('（无标题）')}
        </div>
        <div className="muted small">
          {item.year ?? ''}
          {item.seasonNumber != null ? ` S${item.seasonNumber}` : ''}
          {item.episodeNumber != null ? `E${item.episodeNumber}` : ''}
          {item.kind === 'series' ? t('剧集') : ''}
        </div>
      </div>
    </Link>
  );
}
