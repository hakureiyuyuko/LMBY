import Hls from 'hls.js';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { api } from '../api';
import type { ItemPlaylist, PlaybackState } from '../api';
import {
  actionLabel,
  detectProfile,
  formatClock,
  hasNativeHls,
  modeLabel,
  secondsToTicks,
} from '../capabilities';

/**
 * 播放器（M3）。
 *
 * 两条路：
 *   - 直出（direct）：<video src=原文件>，浏览器自己发 Range 请求，拖动是瞬时的；
 *   - 转封装（remux）：HLS 分片。服务端每次只预生成一段（窗口），
 *     所以播放器要负责「快播到窗口末端时续下一段」，以及把「窗口内的时间」
 *     换算成「整部片子的时间」。
 */
const REPORT_INTERVAL_MS = 10_000;
/** 距离窗口末端多少秒开始续下一段（留出 ffmpeg 起步时间）。 */
const ADVANCE_MARGIN_S = 20;
/** 进度上报最小变化（秒）：暂停时重复上报没有意义。 */
const MIN_REPORT_DELTA_S = 3;

export function Player() {
  const params = useParams();
  const [search] = useSearchParams();
  const navigate = useNavigate();
  const itemId = Number(params.id);
  /** ?restart=1 = 从头播放（条目页的「从头播放」按钮）；只生效一次。 */
  const restartOnceRef = useRef(search.get('restart') === '1');

  const videoRef = useRef<HTMLVideoElement>(null);
  const stageRef = useRef<HTMLDivElement>(null);
  const hlsRef = useRef<Hls | null>(null);

  // 这些值变化频繁（每帧都可能动），放 ref 里避免把整页重渲染成幻灯片
  const sessionIdRef = useRef('');
  const baseRef = useRef(0); // 当前窗口的起点（秒）：整片时间 = base + video.currentTime
  const durationRef = useRef(0);
  const windowEndRef = useRef(0);
  const advancingRef = useRef(false);
  const loadingRef = useRef(true);
  const lastReportRef = useRef(0);
  const stateRef = useRef<PlaybackState | null>(null);

  const [state, setState] = useState<PlaybackState | null>(null);
  const [playlist, setPlaylist] = useState<ItemPlaylist | null>(null);
  const [audioSel, setAudioSel] = useState(0);
  const [subSel, setSubSel] = useState(0);
  const [subReady, setSubReady] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [loading, setLoading] = useState(true);
  const [position, setPosition] = useState(0);
  const [playing, setPlaying] = useState(false);
  const [volume, setVolume] = useState(1);
  const [muted, setMuted] = useState(false);
  const [showInfo, setShowInfo] = useState(false);
  const [isFullscreen, setIsFullscreen] = useState(false);
  const [seq, setSeq] = useState(0); // 换音轨/字幕/重播时 +1，触发重新开流

  const duration = state?.durationSeconds || 0;
  const mode = state ? modeLabel(state.plan.mode) : null;

  const audioOptions = useMemo(() => playlist?.files[0]?.audio ?? [], [playlist]);
  const subOptions = useMemo(() => playlist?.files[0]?.subtitles ?? [], [playlist]);

  /** 记住最新的会话状态，供事件回调（闭包里拿不到新 state）使用。 */
  useEffect(() => {
    stateRef.current = state;
  }, [state]);

  useEffect(() => {
    loadingRef.current = loading;
  }, [loading]);

  // ---------------------------------------------------------------- 开流

  const loadSidebar = useCallback(async () => {
    if (!Number.isFinite(itemId) || itemId <= 0) return;
    try {
      setPlaylist(await api.itemPlaylist(itemId));
    } catch {
      /* 拿不到流清单不影响播放，只是没得选音轨 */
    }
  }, [itemId]);

  useEffect(() => {
    void loadSidebar();
  }, [loadSidebar]);

  const start = useCallback(
    async (opts: { restart?: boolean; position: number; audio: number; sub: number }) => {
      setError('');
      setNotice('');
      setLoading(true);
      setSubReady(false);

      const prev = sessionIdRef.current;
      sessionIdRef.current = '';
      if (prev) void api.stopPlayback(prev).catch(() => undefined);
      if (hlsRef.current) {
        hlsRef.current.destroy();
        hlsRef.current = null;
      }

      try {
        const st = await api.startPlayback(itemId, {
          profile: detectProfile(),
          audioStreamIndex: opts.audio || undefined,
          subtitleStreamIndex: opts.sub,
          restart: opts.restart,
          startPositionTicks: opts.position > 0 ? secondsToTicks(opts.position) : undefined,
        });
        setState(st);
        if (!st.playable || !st.playSessionId) {
          setLoading(false);
          return;
        }
        sessionIdRef.current = st.playSessionId;
        baseRef.current = st.startSeconds;
        durationRef.current = st.durationSeconds;
        windowEndRef.current = st.windowEndSeconds ?? 0;
        attach(st);
        if (st.subtitleUrl) void pollSubtitle(st.subtitleUrl);
      } catch (e) {
        setError(messageOf(e, '开始播放失败'));
        setLoading(false);
      }
    },
    [itemId],
  );

  useEffect(() => {
    if (!Number.isFinite(itemId) || itemId <= 0) return;
    void start({ restart: restartOnceRef.current, position: 0, audio: audioSel, sub: subSel });
    // 只生效一次：之后换音轨/重新载入都走续播逻辑
    restartOnceRef.current = false;
    return () => {
      // 离开页面：停掉转封装会话（服务端会顺手回收 ffmpeg 与分片）
      stopWithBeacon();
    };
    // seq 变化 = 重新载入；音轨/字幕改了 → 用新选择重开一路
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [itemId, seq, audioSel, subSel]);

  /** 把媒体挂到 <video> 上（HLS 优先用原生支持，其次 hls.js）。 */
  const attach = useCallback((st: PlaybackState) => {
    const v = videoRef.current;
    if (!v) return;
    if (st.mode === 'direct' && st.directUrl) {
      v.src = st.directUrl;
      v.load();
      void v.play().catch(() => setNotice('浏览器拦截了自动播放，点一下 ▶ 开始'));
      return;
    }
    if (!st.hlsUrl) return;
    if (hasNativeHls(v)) {
      v.src = st.hlsUrl;
      v.load();
      void v.play().catch(() => setNotice('点一下 ▶ 开始播放'));
      return;
    }
    if (!Hls.isSupported()) {
      setError('这个浏览器既不支持原生 HLS，也不支持 MSE，无法播放转封装流');
      setLoading(false);
      return;
    }
    const hls = new Hls({ enableWorker: true, maxBufferLength: 30 });
    hlsRef.current = hls;
    hls.on(Hls.Events.ERROR, (_evt: string, data: { fatal: boolean; type: string; details: string }) => {
      if (!data.fatal) return;
      if (data.type === Hls.ErrorTypes.MEDIA_ERROR) {
        hls.recoverMediaError();
        return;
      }
      setError(`播放出错（${data.details}）`);
      void refreshState();
    });
    hls.loadSource(st.hlsUrl);
    hls.attachMedia(v);
    hls.on(Hls.Events.MANIFEST_PARSED, () => {
      void v.play().catch(() => setNotice('点一下 ▶ 开始播放'));
    });
  }, []);

  /** 字幕抽取是异步的（内嵌字幕要读一遍源文件），就绪后再挂上去。 */
  const pollSubtitle = useCallback(async (url: string) => {
    for (let i = 0; i < 60; i++) {
      try {
        const res = await fetch(url, { credentials: 'same-origin' });
        if (res.ok) {
          setSubReady(true);
          return;
        }
        if (res.status !== 202) {
          setNotice('字幕提取失败，本次先不显示字幕');
          return;
        }
      } catch {
        /* 网络抖动，继续重试 */
      }
      await new Promise((r) => setTimeout(r, 3000));
    }
    setNotice('字幕还在抽取中，稍后重新打开播放器就能看到');
  }, []);

  /** 会话状态（ffmpeg 报错时能拿到 stderr 尾巴）。 */
  const refreshState = useCallback(async () => {
    const sid = sessionIdRef.current;
    if (!sid) return;
    try {
      const st = await api.playbackState(sid);
      setState(st);
      if (st.log) setNotice(st.log);
    } catch {
      /* 会话可能已被回收，忽略 */
    }
  }, []);

  // ---------------------------------------------------------------- 进度与续窗口

  const report = useCallback(async (force = false) => {
    const v = videoRef.current;
    const sid = sessionIdRef.current;
    if (!v || !sid) return;
    const abs = baseRef.current + v.currentTime;
    if (!force && Math.abs(abs - lastReportRef.current) < MIN_REPORT_DELTA_S) return;
    lastReportRef.current = abs;
    try {
      await api.reportProgress(sid, secondsToTicks(abs), secondsToTicks(durationRef.current));
    } catch {
      /* 进度上报失败不该打断播放 */
    }
  }, []);

  useEffect(() => {
    const timer = window.setInterval(() => void report(), REPORT_INTERVAL_MS);
    return () => window.clearInterval(timer);
  }, [report]);

  /** 离开页面时用 sendBeacon 送最后一次进度并停掉会话（比同步 fetch 可靠）。 */
  const stopWithBeacon = useCallback(() => {
    const sid = sessionIdRef.current;
    const v = videoRef.current;
    if (!sid) return;
    sessionIdRef.current = '';
    const body = JSON.stringify({
      positionTicks: v ? secondsToTicks(baseRef.current + v.currentTime) : 0,
      durationTicks: secondsToTicks(durationRef.current),
    });
    try {
      navigator.sendBeacon(`/api/v1/play/${encodeURIComponent(sid)}/stop`, new Blob([body], { type: 'application/json' }));
    } catch {
      void api.stopPlayback(sid).catch(() => undefined);
    }
  }, []);

  useEffect(() => {
    const onHide = () => stopWithBeacon();
    window.addEventListener('pagehide', onHide);
    return () => window.removeEventListener('pagehide', onHide);
  }, [stopWithBeacon]);

  /** 续下一段窗口：让服务端从目标位置重新生成一段 HLS。 */
  const advance = useCallback(async (at: number) => {
    const sid = sessionIdRef.current;
    const v = videoRef.current;
    if (!sid || !v || advancingRef.current) return;
    advancingRef.current = true;
    setNotice(`续下一段（${formatClock(at)}）…`);
    const wasPaused = v.paused;
    try {
      const st = await api.seekPlayback(sid, secondsToTicks(at));
      if (st.state === 'error' || !st.hlsUrl) {
        setError(st.error || '续下一段失败');
        return;
      }
      baseRef.current = st.startSeconds;
      windowEndRef.current = st.windowEndSeconds ?? 0;
      setState(st);
      setNotice('');
      reloadStream(st.hlsUrl, wasPaused);
      // 续段后立刻上报一次：用户很可能就是在拖到新位置之后关页面的，
      // 如果只靠 10 秒一次的定期上报，这一次跳转就丢了。
      void report(true);
    } catch (e) {
      setError(messageOf(e, '续下一段失败'));
    } finally {
      advancingRef.current = false;
    }
  }, [report]);

  /** 用新地址重挂流（hls.js 重建 source / 原生直接换 src），并尽量续上播放。 */
  const reloadStream = useCallback((url: string, wasPaused: boolean) => {
    const v = videoRef.current;
    if (!v) return;
    if (hlsRef.current) {
      hlsRef.current.loadSource(url);
      if (!wasPaused) void v.play().catch(() => undefined);
      return;
    }
    v.src = url;
    v.load();
    if (!wasPaused) void v.play().catch(() => undefined);
  }, []);

  const onTimeUpdate = useCallback(() => {
    const v = videoRef.current;
    if (!v) return;
    // 时间在走就说明有画面了：把「载入中」遮罩收掉
    //（续窗口之后 reload 期间会重新置上，靠这一句兜底）。
    if (loadingRef.current) setLoading(false);
    const abs = baseRef.current + v.currentTime;
    setPosition(abs);

    const st = stateRef.current;
    if (!st || st.mode === 'direct' || advancingRef.current) return;
    const we = windowEndRef.current;
    if (!we || we >= durationRef.current - 1) return;
    if (abs >= we - ADVANCE_MARGIN_S) void advance(we);
  }, [advance]);

  /** 拖动：窗口内直接拖（瞬时），窗口外让服务端重开一段。 */
  const seekTo = useCallback(
    async (target: number) => {
      const v = videoRef.current;
      const st = stateRef.current;
      if (!v || !st) return;
      if (st.mode === 'direct') {
        v.currentTime = target;
        setPosition(target);
        void report(true);
        return;
      }
      const inWindow = target >= baseRef.current && target < windowEndRef.current - 1;
      if (inWindow) {
        v.currentTime = target - baseRef.current;
        setPosition(target);
        void report(true);
        return;
      }
      await advance(target);
    },
    [advance, report],
  );

  const togglePlay = useCallback(() => {
    const v = videoRef.current;
    if (!v) return;
    if (v.paused) void v.play().catch(() => undefined);
    else v.pause();
  }, []);

  const toggleFullscreen = useCallback(() => {
    const el = stageRef.current;
    if (!el) return;
    if (document.fullscreenElement) void document.exitFullscreen();
    else void el.requestFullscreen().catch(() => setNotice('这个浏览器不允许全屏'));
  }, []);

  useEffect(() => {
    const onFs = () => setIsFullscreen(Boolean(document.fullscreenElement));
    document.addEventListener('fullscreenchange', onFs);
    return () => document.removeEventListener('fullscreenchange', onFs);
  }, []);

  // 键盘快捷键：空格播放/暂停、←/→ 10 秒、f 全屏、m 静音
  useEffect(() => {
    const onKey = (ev: KeyboardEvent) => {
      const v = videoRef.current;
      if (!v) return;
      const tag = (ev.target as HTMLElement | null)?.tagName;
      if (tag === 'INPUT' || tag === 'SELECT' || tag === 'TEXTAREA') return;
      switch (ev.key) {
        case ' ':
          ev.preventDefault();
          togglePlay();
          break;
        case 'ArrowLeft':
          void seekTo(Math.max(0, baseRef.current + v.currentTime - 10));
          break;
        case 'ArrowRight':
          void seekTo(baseRef.current + v.currentTime + 10);
          break;
        case 'f':
          toggleFullscreen();
          break;
        case 'm':
          v.muted = !v.muted;
          setMuted(v.muted);
          break;
        default:
          break;
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [togglePlay, toggleFullscreen, seekTo]);

  // 字幕开关（<track> 用 textTracks 控制显示）
  useEffect(() => {
    const v = videoRef.current;
    if (!v) return;
    const tracks = v.textTracks;
    for (let i = 0; i < tracks.length; i++) tracks[i].mode = subSel === -1 ? 'disabled' : 'showing';
  }, [subReady, subSel]);

  if (!Number.isFinite(itemId) || itemId <= 0) {
    return (
      <div className="card">
        <h2>条目 id 非法</h2>
        <Link className="btn" to="/">
          返回概览
        </Link>
      </div>
    );
  }

  const canPlay = Boolean(state?.playable && state.playSessionId);
  const subtitleOn = subSel !== -1 && subReady;

  return (
    <div className="player" ref={stageRef}>
      <div className="player-topbar">
        <button type="button" className="btn btn-sm btn-ghost" onClick={() => navigate(-1)}>
          ← 返回
        </button>
        <span className="player-title">{state?.title || `条目 ${itemId}`}</span>
        {mode && (
          <span className="badge" title={mode.hint}>
            {mode.label}
          </span>
        )}
        <span className="spacer" />
        <button type="button" className="btn btn-sm btn-ghost" onClick={() => setShowInfo((v) => !v)}>
          {showInfo ? '隐藏详情' : '为什么这么播'}
        </button>
      </div>

      <div className="player-stage">
        {/* eslint-disable-next-line jsx-a11y/media-has-caption */}
        <video
          ref={videoRef}
          className="player-video"
          playsInline
          onTimeUpdate={onTimeUpdate}
          onPlay={() => setPlaying(true)}
          onPause={() => {
            setPlaying(false);
            // 暂停往往就是用户要走开的信号：立刻报一次进度
            void report(true);
          }}
          onWaiting={() => setLoading(true)}
          onPlaying={() => {
            setLoading(false);
            setNotice('');
          }}
          onCanPlay={() => setLoading(false)}
          onLoadedMetadata={(e) => {
            const v = e.currentTarget;
            v.volume = volume;
            v.muted = muted;
          }}
          onEnded={() => {
            void report(true);
            setNotice('播放结束');
          }}
          onError={() => {
            if (!stateRef.current) return;
            void refreshState();
            setError('媒体加载失败（服务端可能已回收这段流），点「重新开始」再试');
            setLoading(false);
          }}
        >
          {subtitleOn && state?.subtitleUrl && (
            <track kind="subtitles" src={state.subtitleUrl} srcLang="zh" label="字幕" default />
          )}
        </video>

        {loading && canPlay && <div className="player-spinner">载入中…</div>}

        {!canPlay && !state && <div className="player-spinner">正在准备播放…</div>}

        {!canPlay && state && (
          <div className="player-blocked">
            <h3>这个条目现在放不了</h3>
            {error && <div className="alert alert-error">{error}</div>}
            {state && (
              <>
                <ul className="player-reasons">
                  {state.reasons.map((r, i) => (
                    <li key={i}>{r}</li>
                  ))}
                </ul>
                <p className="hint">
                  播放决策是「能直出就直出 → 不行就转封装 → 再不行才转码」。上面每一条都是
                  服务端给出的具体原因（例如视频是 10bit HEVC，浏览器解不了，需要 M4 的转码能力）。
                </p>
              </>
            )}
            <div className="row">
              <button type="button" className="btn" onClick={() => setSeq((v) => v + 1)}>
                重新开始
              </button>
              <Link className="btn btn-ghost" to={`/items/${itemId}`}>
                去条目详情
              </Link>
            </div>
          </div>
        )}

        {(notice || error) && canPlay && (
          <div className={error ? 'player-toast player-toast-error' : 'player-toast'}>
            {error || notice}
          </div>
        )}

        {showInfo && state && (
          <div className="player-info">
            <h4>为什么这么播</h4>
            <ul className="player-reasons">
              {state.reasons.map((r, i) => (
                <li key={i}>{r}</li>
              ))}
            </ul>
            <dl className="kv">
              <dt>播放方式</dt>
              <dd>
                {mode?.label}（{state.mode}
                {state.plan.segmentFormat ? ` / ${state.plan.segmentFormat}` : ''}）
              </dd>
              <dt>视频</dt>
              <dd>
                #{state.plan.video.index} {state.plan.video.codec} → {actionLabel(state.plan.video.action)}
              </dd>
              <dt>音频</dt>
              <dd>
                #{state.plan.audio.index} {state.plan.audio.codec} → {actionLabel(state.plan.audio.action)}
                {state.plan.audio.downmix ? '（降为立体声）' : ''}
              </dd>
              <dt>字幕</dt>
              <dd>
                {state.plan.subtitle.index >= 0 ? `#${state.plan.subtitle.index} ${state.plan.subtitle.codec} → ` : ''}
                {actionLabel(state.plan.subtitle.action)}
              </dd>
              <dt>起播位置</dt>
              <dd>{formatClock(state.startSeconds)}</dd>
            </dl>
            {state.log && <pre className="player-log">{state.log}</pre>}
          </div>
        )}
      </div>

      {/* 控制条只在真能播时出现：放不了的时候留下一个禁用的控制条，
          会让人以为「再点一下就好」，而真正该看的是上面的理由。 */}
      {canPlay && (
        <div className="player-controls">
          <button type="button" className="player-btn" onClick={togglePlay}>
          {playing ? '⏸' : '▶'}
        </button>
        <span className="player-time">
          {formatClock(position)} / {formatClock(duration)}
        </span>
        <input
          className="player-progress"
          type="range"
          min={0}
          max={Math.max(1, Math.floor(duration))}
          value={Math.min(Math.floor(position), Math.floor(duration))}
          onChange={(e) => void seekTo(Number(e.target.value))}
          disabled={!canPlay}
          aria-label="播放进度"
        />
        <label className="player-vol">
          <button
            type="button"
            className="player-btn"
            onClick={() => {
              const v = videoRef.current;
              if (!v) return;
              v.muted = !v.muted;
              setMuted(v.muted);
            }}
          >
            {muted || volume === 0 ? '🔇' : '🔊'}
          </button>
          <input
            type="range"
            min={0}
            max={100}
            value={Math.round(volume * 100)}
            onChange={(e) => {
              const next = Number(e.target.value) / 100;
              setVolume(next);
              const v = videoRef.current;
              if (v) {
                v.volume = next;
                v.muted = next === 0;
                setMuted(v.muted);
              }
            }}
            aria-label="音量"
          />
        </label>

        {audioOptions.length > 1 && (
          <select
            className="player-select"
            value={audioSel}
            onChange={(e) => setAudioSel(Number(e.target.value))}
            aria-label="音轨"
          >
            <option value={0}>音轨：自动</option>
            {audioOptions.map((a) => (
              <option key={a.index} value={a.index}>
                #{a.index} {a.codec} {a.channels ? `${a.channels}ch` : ''} {a.language || ''} {a.title || ''}
              </option>
            ))}
          </select>
        )}

        {subOptions.length > 0 && (
          <select
            className="player-select"
            value={subSel}
            onChange={(e) => setSubSel(Number(e.target.value))}
            aria-label="字幕"
          >
            <option value={0}>字幕：自动</option>
            <option value={-1}>字幕：关闭</option>
            {subOptions.map((s) => (
              <option key={s.index} value={s.index}>
                #{s.index} {s.codec} {s.language || ''} {s.title || ''}
                {s.isImage ? '（图形，本次不显示）' : ''}
              </option>
            ))}
          </select>
        )}

        <button type="button" className="player-btn" onClick={toggleFullscreen} aria-label="全屏">
          {isFullscreen ? '⤢' : '⛶'}
        </button>
        <button
          type="button"
          className="player-btn"
          onClick={() => void start({ restart: true, position: 0, audio: audioSel, sub: subSel })}
          title="从头播放"
        >
          ⟲
        </button>
        </div>
      )}

      {canPlay && (
        <p className="faint player-hint">
          快捷键：空格 播放/暂停 · ←/→ 快退快进 10 秒 · F 全屏 · M 静音。
          {state?.mode === 'remux' &&
            '（转封装模式下拖动到已生成窗口之外时，服务端会从新位置重新生成一段，需要一两秒）'}
        </p>
      )}
    </div>
  );
}

function messageOf(e: unknown, fallback: string): string {
  if (e instanceof Error && e.message) return e.message;
  return fallback;
}
