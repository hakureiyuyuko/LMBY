/**
 * 直播电视（M5）——**播放器优先**的一页。
 *
 * 布局照「看电视台」的习惯来（而不是照「后台列表」）：
 *
 *   ┌──────────────┬────────────────────────────────┐
 *   │ 频道换台栏    │  播放器                         │
 *   │ （分组可收起） │                                │
 *   │  ★ 收藏       ├────────────────────────────────┤
 *   │  …            │  当前频道 · 上一个 / 下一个 / 断开 │
 *   │ 外部播放器 ▸   │                                │
 *   └──────────────┴────────────────────────────────┘
 *
 * 左边只做一件事：**换台**（选频道、收藏、按分组收起来看）。
 * 频道的「管理」（改名 / 分组 / 排序 / logo、停用与启用、24 小时外链、复制地址）
 * 全部搬去了**设置 → 直播源 → 频道管理**，源与探测也在那儿 ——
 * 前台不该出现一堆只有管理员才用得上的按钮。
 */
import Hls from 'hls.js';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { ApiError, api } from '../api';
import { detectProfile, hasNativeHls } from '../capabilities';
import { useAuth } from '../auth';
import { useI18n } from '../i18n';
import type { TVChannel } from '../api';

/**
 * 浏览器能解开的视频编码（只探测一次）。
 *
 * 服务端拿它决定「转封装够不够」：报不出来的（老浏览器 / 探测失败）就少报，
 * 让服务端倾向转码 —— 宁可多花点 CPU，也别发一路放不了的流。
 */
let cachedCodecs: string[] | null = null;
function clientCodecs(): string[] {
  if (cachedCodecs) return cachedCodecs;
  try {
    cachedCodecs = detectProfile().videoCodecs;
  } catch {
    cachedCodecs = ['h264']; // 探测失败时保守：只声明最普遍的底线
  }
  return cachedCodecs;
}

export function LiveTV() {
  const { t } = useI18n();
  const { user } = useAuth();
  const isAdmin = user?.isAdmin ?? false;

  const videoRef = useRef<HTMLVideoElement | null>(null);
  const hlsRef = useRef<Hls | null>(null);

  const [channels, setChannels] = useState<TVChannel[] | null>(null);
  const [total, setTotal] = useState(0);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [playing, setPlaying] = useState<TVChannel | null>(null);
  const [starting, setStarting] = useState(false);
  const [playErr, setPlayErr] = useState('');
  /** 当前这一路的 sid（断开时要显式告诉服务端）。 */
  const sidRef = useRef<string | null>(null);
  const [browserMs, setBrowserMs] = useState<number | null>(null);
  /** 这一路实际用的方式（copy / transcode）：界面上标「转码」，也用于防重试死循环。 */
  const [mode, setMode] = useState<'copy' | 'transcode'>('copy');
  /** 已经为当前频道「转码重试」过的标记：每个频道只重试一次。 */
  const retriedRef = useRef<number | null>(null);
  /** 收起来的分组（默认全展开：第一次进来要能一眼看到有哪些台）。 */
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());

  const load = useCallback(async () => {
    try {
      // 前台只列「能用能看的」：
      //   enabled=1   → 停用的频道不进前台；
      //   hide_failed → 探测过且不通的（无效的）也不进前台。
      // （停用源带来的频道由后端一并排除；没探过的照旧显示，
      //   刚导入一份播放列表还没探测时前台不该是空白）
      const data = await api.liveChannels({ enabled: true, hideFailed: true });
      setChannels(data.channels);
      setTotal(data.total);
      setError('');
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('读取频道失败'));
    }
  }, [t]);

  useEffect(() => {
    void load();
  }, [load]);

  /** 按后端顺序切成「分组 → 频道」，同组相邻的算一组（后端已按分组排序）。 */
  const groups = useMemo(() => {
    const out: { name: string; items: TVChannel[] }[] = [];
    for (const ch of channels ?? []) {
      const name = ch.group || t('未分组');
      const last = out[out.length - 1];
      if (last && last.name === name) last.items.push(ch);
      else out.push({ name, items: [ch] });
    }
    return out;
  }, [channels, t]);

  const stop = useCallback(() => {
    if (hlsRef.current) {
      hlsRef.current.destroy();
      hlsRef.current = null;
    }
    const v = videoRef.current;
    if (v) {
      v.pause();
      v.removeAttribute('src');
      v.load();
    }
    // 正在看的这一路要显式告诉服务端「人走了」：共享会话按观众数回收，
    // 不主动停会白跑一路 ffmpeg 到空闲超时。
    const sid = sidRef.current;
    if (sid) {
      sidRef.current = null;
      void api.stopLivePlay(sid).catch(() => undefined);
    }
    setPlaying(null);
    setPlayErr('');
    setStarting(false);
    setMode('copy');
  }, []);

  /**
   * 「这一路放不出来」时的兜底：如果刚才用的是转封装、而且这个频道还没重试过，
   * 就带 force=transcode 再来一次（服务端会转码成 H.264）。
   *
   * 用 ref 而不是直接闭包，是为了让 attach 里的监听器拿到最新的 play/playing。
   * 每个频道只重试一次：真的解不开就别来回折腾用户。
   */
  const onPlaybackFailedRef = useRef<(() => void) | null>(null);
  const playingRef = useRef<TVChannel | null>(null);
  const modeRef = useRef<'copy' | 'transcode'>('copy');
  useEffect(() => {
    playingRef.current = playing;
    modeRef.current = mode;
  }, [playing, mode]);

  const attach = useCallback(
    (src: string) => {
      const v = videoRef.current;
      if (!v) return;
      const started = performance.now();

      // ⚠️ 换台时**必须先把上一条彻底拆掉**：hls 实例 + 媒体元素上的 MediaSource。
      // 不拆的话，新实例 attachMedia 会挂在一个已被旧 MediaSource 占住的 <video> 上，
      // 分片加载直接报 `播放出错（levelLoadError）` —— 刷新页面（新元素）才正常。
      // 2026-09-22 真踩到：连看两个台必现。
      hlsRef.current?.destroy();
      hlsRef.current = null;
      v.removeAttribute('src');
      v.load();

      const onPlaying = () => {
        setBrowserMs(Math.round(performance.now() - started));
        setStarting(false);
        setPlayErr('');
      };
      v.addEventListener('playing', onPlaying, { once: true });
      v.addEventListener(
        'error',
        () => {
          setPlayErr(t('浏览器报错：这个流它放不了（可能是编码或传输问题）'));
          // 上一把是转封装、而且这个频道还没重试过 → 转码再来一次。
          // 这一条是「源编码未知（没探测过）」时的兜底路径：服务端拿不到编码信息，
          // 只能等浏览器真放不出来再转。
          onPlaybackFailedRef.current?.();
        },
        { once: true },
      );

      const native = hasNativeHls(v);
      if (native) {
        v.src = src;
        void v.play().catch(() => setNotice(t('浏览器拦了自动播放，点一下播放键开始')));
        return;
      }
      if (!Hls.isSupported()) {
        setPlayErr(t('这个浏览器既不支持原生 HLS，也不支持 MSE，放不了直播流'));
        setStarting(false);
        return;
      }
      const hls = new Hls({ lowLatencyMode: false, maxBufferLength: 20 });
      hlsRef.current = hls;
      hls.on(Hls.Events.ERROR, (_e, data) => {
        if (!data.fatal) return;
        setPlayErr(t('播放出错（{detail}）', { detail: data.details }));
        setStarting(false);
        onPlaybackFailedRef.current?.();
      });
      hls.on(Hls.Events.MANIFEST_PARSED, () => {
        void v.play().catch(() => setNotice(t('浏览器拦了自动播放，点一下播放键开始')));
      });
      hls.loadSource(src);
      hls.attachMedia(v);
    },
    [t],
  );

  const play = useCallback(
    async (ch: TVChannel, force?: 'transcode' | 'copy') => {
      setNotice('');
      setPlayErr('');
      setStarting(true);
      // 换台：先把上一条会话停掉（同一路 ffmpeg 可以被别的观众继续用，不浪费）
      const prevSid = sidRef.current;
      if (prevSid) {
        sidRef.current = null;
        void api.stopLivePlay(prevSid).catch(() => undefined);
      }
      setPlaying(ch);
      try {
        // 把「我能解哪些编码」告诉服务端：它据此判断转封装够不够。
        // 浏览器解不开源编码（HEVC / MPEG-2）时，服务端会直接转码，
        // 免得发一路我们放不了的分片。
        const s = await api.startLivePlay(ch.id, {
          codecs: clientCodecs(),
          force,
        });
        sidRef.current = s.sid;
        setMode(s.mode === 'transcode' ? 'transcode' : 'copy');
        attach(s.playlistUrl);
      } catch (e) {
        setPlayErr(e instanceof ApiError ? e.message : t('起播失败'));
        setStarting(false);
      }
    },
    [attach, t],
  );

  // 钩子本体：转封装放过、这个频道还没重试过 → 转码再来一次
  useEffect(() => {
    onPlaybackFailedRef.current = () => {
      const ch = playingRef.current;
      if (!ch) return;
      if (modeRef.current !== 'copy') return; // 已经转过了，再转没意义
      if (retriedRef.current === ch.id) return; // 每个频道只重试一次
      retriedRef.current = ch.id;
      setNotice(t('这个流浏览器解不开，已自动改成转码重试…'));
      void play(ch, 'transcode');
    };
  }, [play, t]);

  /** 上一个 / 下一个：就在换台栏这一串里走（到头绕回去）。 */
  const step = useCallback(
    (delta: number) => {
      const list = channels ?? [];
      if (list.length === 0) return;
      const i = playing ? list.findIndex((c) => c.id === playing.id) : -1;
      const next = list[(i + delta + list.length) % list.length];
      if (next) void play(next);
    },
    [channels, playing, play],
  );

  // 关掉页面/切换路由时把 ffmpeg 那一路收掉（直播会话是按观众数回收的，别白跑）
  useEffect(() => () => stop(), [stop]);

  async function toggleFav(ch: TVChannel) {
    try {
      const res = await api.toggleLiveFavorite(ch.id);
      setChannels((prev) =>
        (prev ?? []).map((c) => (c.id === ch.id ? { ...c, favorite: res.favorite } : c)),
      );
      setPlaying((prev) => (prev && prev.id === ch.id ? { ...prev, favorite: res.favorite } : prev));
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('收藏失败'));
    }
  }

  const shown = channels?.length ?? 0;

  return (
    <>
      {error && <div className="alert alert-error">{error}</div>}
      {notice && <div className="alert alert-ok">{notice}</div>}

      <div className="tv-shell">
        {/* 左：换台栏。只做「选频道 / 收藏 / 收起分组」，管理动作在设置里 */}
        <aside className="tv-side">
          <div className="tv-side-head">
            <span className="faint small">
              {t('{shown} / {total} 个频道', { shown, total: total || shown })}
            </span>
            <div className="spacer" />
            <button
              type="button"
              className="btn btn-sm btn-ghost"
              onClick={() =>
                setCollapsed((prev) =>
                  prev.size > 0 ? new Set() : new Set(groups.map((g) => g.name)),
                )
              }
            >
              {collapsed.size > 0 ? t('全部展开') : t('全部收起')}
            </button>
          </div>

          {!channels && <p className="muted small">{t('正在读取…')}</p>}
          {channels && channels.length === 0 && (
            <p className="muted small">
              {isAdmin
                ? t('还没有能看的频道：去设置 → 直播源导入一份播放列表。')
                : t('还没有能看的频道。')}
            </p>
          )}

          <div className="tv-list">
            {groups.map((g) => {
              const open = !collapsed.has(g.name);
              return (
                <div className="tv-group" key={g.name}>
                  <button
                    type="button"
                    className="tv-group-head"
                    aria-expanded={open}
                    onClick={() =>
                      setCollapsed((prev) => {
                        const next = new Set(prev);
                        if (next.has(g.name)) next.delete(g.name);
                        else next.add(g.name);
                        return next;
                      })
                    }
                  >
                    <span className="tv-caret">{open ? '▾' : '▸'}</span>
                    <span className="tv-group-name">{g.name}</span>
                    <span className="faint small">{g.items.length}</span>
                  </button>
                  {open &&
                    g.items.map((ch) => (
                      <div
                        className={'tv-ch' + (playing?.id === ch.id ? ' is-playing' : '')}
                        key={ch.id}
                      >
                        <button
                          type="button"
                          className="tv-ch-main"
                          title={ch.name}
                          onClick={() => void play(ch)}
                        >
                          {ch.logo ? (
                            <img
                              className="tv-ch-logo"
                              src={ch.logo}
                              alt=""
                              loading="lazy"
                              onError={(e) => {
                                e.currentTarget.style.display = 'none';
                              }}
                            />
                          ) : null}
                          <span className="tv-ch-name">{ch.name}</span>
                          <span className="tv-ch-kind">{ch.kind.toUpperCase()}</span>
                        </button>
                        <button
                          type="button"
                          className={'tv-star' + (ch.favorite ? ' is-on' : '')}
                          title={ch.favorite ? t('取消收藏') : t('收藏')}
                          aria-pressed={ch.favorite}
                          onClick={() => void toggleFav(ch)}
                        >
                          {ch.favorite ? '★' : '☆'}
                        </button>
                      </div>
                    ))}
                </div>
              );
            })}
          </div>

        </aside>

        {/* 右：播放器 + 一行控制（换台 / 断开） */}
        <section className="tv-pane">
          <div className="tv-stage">
            <video ref={videoRef} className="tv-video" controls playsInline />
            {!playing && !starting && !playErr && (
              <div className="tv-hint-overlay">{t('从左边选一个频道开始看')}</div>
            )}
            {starting && <div className="tv-hint-overlay">{t('正在起播…')}</div>}
            {playErr && !starting && (
              <div className="tv-hint-overlay tv-hint-err">
                <p>{playErr}</p>
                <button type="button" className="btn btn-sm" onClick={() => playing && void play(playing)}>
                  {t('重试')}
                </button>
              </div>
            )}
          </div>

          <div className="tv-bar">
            {playing ? (
              <>
                <strong>{playing.name}</strong>
                <span className="faint small">
                  {playing.group || t('未分组')} · {playing.kind.toUpperCase()}
                  {playing.hasHeaders ? t(' · 带请求头') : ''}
                </span>
                {/* 转码中要给用户一个解释：为什么这次 CPU 转起来了、画质可能不如原画 */}
                {mode === 'transcode' && (
                  <span className="badge" title={t('源编码浏览器解不开，正在实时转码（H.264）')}>
                    {t('转码中')}
                  </span>
                )}
              </>
            ) : (
              <span className="faint">{t('未在播放')}</span>
            )}
            <div className="spacer" />
            {browserMs !== null && playing && (
              <span className="faint small">{t(' · 浏览器起播 {ms} ms', { ms: browserMs })}</span>
            )}
            <button
              type="button"
              className="btn btn-sm"
              disabled={shown === 0}
              onClick={() => step(-1)}
            >
              {t('上一个')}
            </button>
            <button
              type="button"
              className="btn btn-sm"
              disabled={shown === 0}
              onClick={() => step(1)}
            >
              {t('下一个')}
            </button>
            <button
              type="button"
              className="btn btn-sm btn-ghost"
              disabled={!playing}
              onClick={stop}
            >
              {t('断开')}
            </button>
          </div>
        </section>
      </div>
    </>
  );
}
