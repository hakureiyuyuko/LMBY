import { useCallback, useEffect, useRef, useState } from 'react';
import type { ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { api } from '../api';
import type { ContinueWatchingEntry, Health, HomePayload, HomeTaste, Item } from '../api';
import { formatClock } from '../capabilities';
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
 */

/** 轮播间隔：够看清简介，又不至于让人等得想起要拖动。 */
const HERO_ROTATE_MS = 8000;

export function Home() {
  const [data, setData] = useState<HomePayload | null>(null);
  const [error, setError] = useState('');
  const [hero, setHero] = useState(0);
  const [paused, setPaused] = useState(false);
  const [health, setHealth] = useState<Health | null>(null);

  const load = useCallback(async () => {
    try {
      setData(await api.home());
      setError('');
    } catch {
      setError('读取首页失败');
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  // 自检面板（M0 起就在首页的那个）只需要事实，不轮询：看完刷新一下就好
  useEffect(() => {
    api
      .health()
      .then(setHealth)
      .catch(() => setHealth(null));
  }, []);

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
      {!data && !error && <p className="muted">正在加载首页…</p>}

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
            <h1 className="hero-title">{current.title || '（无标题）'}</h1>
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
                ▶ {resume ? '继续播放' : '播放'}
              </Link>
              <Link className="btn" to={`/item/${current.id}`}>
                详情
              </Link>
            </div>

            {resume && (
              <div className="hero-resume">
                <span className="hero-progress">
                  <span style={{ width: `${progressPct(resume)}%` }} />
                </span>
                <span className="faint small">
                  {resume.item.title} · 看到 {formatClock(resume.progress.positionTicks / 10_000_000)}
                </span>
              </div>
            )}
          </div>

          {heroItems.length > 1 && (
            <div className="hero-nav">
              <button
                type="button"
                className="hero-arrow"
                aria-label="上一部"
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
                    aria-label={`第 ${i + 1} 部：${h.title}`}
                    data-hero-dot={i}
                    onClick={() => setHero(i)}
                  />
                ))}
              </div>
              <button
                type="button"
                className="hero-arrow"
                aria-label="下一部"
                onClick={() => setHero((i) => (i + 1) % heroItems.length)}
              >
                ›
              </button>
            </div>
          )}
        </section>
      )}

      {data && data.continue.length > 0 && (
        <RowBlock title="继续观看" subtitle="进度按账号独立保存" itemsKey="continue">
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
                {e.item.title || '（无标题）'}
              </span>
              <span className="muted small">
                还剩 {formatClock(e.remainingTicks / 10_000_000)}
              </span>
            </Link>
          ))}
        </RowBlock>
      )}

      {data?.sections.map((s) => (
        <RowBlock
          key={s.key}
          title={s.title}
          subtitle={s.subtitle}
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
          <h2>欢迎使用 LMBY</h2>
          <p className="muted">
            媒体库里还没有内容。先去 <Link to="/libraries">库管理</Link> 添加一个媒体库并扫描，
            条目的海报、简介与演职员会从同目录的 nfo 读进来（本地优先，不联网）。
          </p>
        </div>
      )}

      <details className="card home-health">
        <summary>服务状态</summary>
        {!health && <p className="muted">正在读取…</p>}
        {health && (
          <>
            <p style={{ marginTop: 10 }}>
              <span className="badge">
                <span className={health.status === 'ok' ? 'dot dot-ok' : 'dot dot-warn'} />
                {health.status === 'ok' ? '运行正常' : health.status === 'degraded' ? '降级运行' : '异常'}
              </span>{' '}
              <span className="badge">
                <span className={health.database.ok ? 'dot dot-ok' : 'dot dot-bad'} />
                数据库{' '}
                {health.database.ok ? `${health.database.latencyMs ?? 0} ms` : health.database.error}
              </span>{' '}
              <span className="badge">
                <span className={health.ffmpeg.available ? 'dot dot-ok' : 'dot dot-bad'} />
                ffmpeg {health.ffmpeg.available ? health.ffmpeg.version : '不可用'}
              </span>
            </p>
            <dl className="kv">
              <dt>版本</dt>
              <dd>{health.version}</dd>
              <dt>运行时长</dt>
              <dd>{formatUptime(health.uptimeSeconds)}</dd>
              <dt>ffmpeg 路径</dt>
              <dd>{health.ffmpeg.path}</dd>
              <dt>硬件加速后端</dt>
              <dd>{health.ffmpeg.hw_accels?.join(', ') || '—'}</dd>
            </dl>
            <p className="faint" style={{ marginBottom: 0 }}>
              注意：「列出的后端」不等于「真的能用」。例如本机 ffmpeg 列出了 qsv，
              但核显缺运行时，实际只能用 vaapi —— 所以能力探测会真跑一小段转码来验证。
            </p>
          </>
        )}
      </details>
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
        <button type="button" className="row-arrow" aria-label="向左滚动" onClick={() => scroll(-1)}>
          ‹
        </button>
        <button type="button" className="row-arrow" aria-label="向右滚动" onClick={() => scroll(1)}>
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
          {item.title || '（无标题）'}
        </div>
        <div className="muted small">
          {item.year ?? ''}
          {item.seasonNumber != null ? ` S${item.seasonNumber}` : ''}
          {item.episodeNumber != null ? `E${item.episodeNumber}` : ''}
          {item.kind === 'series' ? ' 剧集' : ''}
        </div>
      </div>
    </Link>
  );
}

function formatUptime(sec: number): string {
  if (sec < 60) return `${sec} 秒`;
  const m = Math.floor(sec / 60);
  if (m < 60) return `${m} 分 ${sec % 60} 秒`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h} 小时 ${m % 60} 分`;
  return `${Math.floor(h / 24)} 天 ${h % 24} 小时`;
}
