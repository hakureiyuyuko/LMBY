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

/**
 * 画质档：
 *   auto     = 自动（服务端按配置的转码上限走，默认压到 1080p）
 *   original = 原生分辨率（不额外压；4K 转码可能起播慢，这是用户自己选的）
 *   数字     = 输出高度上限（选了比源低的档就必须转码）
 */
type QualityChoice = 'auto' | 'original' | number;

/** 菜单里给出的档位。只列比源低的：等于源高度的用「原生」表示，不重复列。 */
const QUALITY_TIERS = [1080, 720, 480, 360, 240, 144];

/** localStorage 键：记住上次选的档位（换片子也沿用）。 */
const QUALITY_KEY = 'lmby:playQuality';

/** 画质档 → 接口的 maxHeight（undefined = 省略字段 = 自动）。 */
function maxHeightOf(q: QualityChoice): number | undefined {
  if (q === 'auto') return undefined;
  if (q === 'original') return 0;
  return q;
}

/** 从 localStorage 读上次选的档（读不出来就当「自动」）。 */
function readQuality(): QualityChoice {
  try {
    const raw = localStorage.getItem(QUALITY_KEY);
    if (!raw || raw === 'auto') return 'auto';
    if (raw === 'original') return 'original';
    const n = Number(raw);
    return Number.isFinite(n) && n > 0 ? n : 'auto';
  } catch {
    return 'auto';
  }
}

/** 资源一律用绝对 URL。 */
function absUrl(p: string): string {
  return new URL(p, window.location.origin).href;
}

/**
 * 取回字幕**文本**。内嵌字幕首次要抽（接口回 202 = 还在抽），所以带重试。
 *
 * 重试预算按**最坏情况**给：抽一条 4MB 的 ASS 要把源文件里那一路读一遍，
 * 在网络盘（CIFS）上、又赶上同时有转码在跑时，几十秒是常态（实测踩到：
 * 服务端 15 秒就回 202，而前端只轮询了 18 秒 → “字幕抽取超时”、画布永远挂不上）。
 * 45 × 1.5s ≈ 70 秒，与后台上限（subtitleExtractTimeout 是 20 分钟）是同一量级。
 *
 * 为什么不让 libass 自己去拉 subUrl：
 *   1）它拿到 202 里的 JSON 会在 worker 里直接崩掉，而且看不到可读错误；
 *   2）自己取回文本还能把失败原因告诉用户。
 */
async function fetchSubtitleText(url: string, tries = 45): Promise<string> {
  for (let i = 0; i < tries; i++) {
    const r = await fetch(url, { credentials: 'same-origin' });
    if (r.status === 202) {
      await new Promise((res) => setTimeout(res, 1500));
      continue;
    }
    if (!r.ok) throw new Error(`字幕取回失败：HTTP ${r.status}`);
    return await r.text();
  }
  throw new Error('字幕抽取超时');
}

/**
 * 等视频挂上元数据并有真实尺寸。
 *
 * octopus 是用 `setVideo` **那一刻**的尺寸建画布的：拿到 0 就把画布
 * `display:none`，之后不会自己重算（实测踩到 —— 画布一直在、就是看不见）。
 * 拿不到就放弃（最多 ~15 秒），让后续流程自己去报错。
 */
async function waitVideoSized(v: HTMLVideoElement, tries = 50): Promise<void> {
  for (let i = 0; i < tries; i++) {
    if (v.videoWidth > 0 && v.clientWidth > 0) return;
    await new Promise((res) => setTimeout(res, 300));
  }
}

/**
 * 服务端的兑底字体：部署时放哪种格式都行（ttf / ttc / otf），按顺序挑第一个存在的。
 *
 * 为什么不在前端写死一个文件名：字体是**部署资产**（几 MB 二进制不进仓库），
 * 换字体（比如思源黑体 / 阿里巴巴普惠体）不该需要改代码重新发版；
 * 而 octopus 的默认值 `default.woff2` 在包里根本不存在，缺了它 worker 会直接崩。
 * 都没找到就返回空串，调用方据此优雅降级（而不是让 worker 崩掉）。
 */
async function pickFallbackFont(): Promise<string> {
  for (const name of ['fallback.ttf', 'fallback.ttc', 'fallback.otf']) {
    const u = absUrl(`/api/v1/fonts/${name}`);
    try {
      const r = await fetch(u, { method: 'HEAD', credentials: 'same-origin' });
      if (r.ok) return u;
    } catch {
      /* 试下一个 */
    }
  }
  return '';
}

/** octopus 实例的最小接口（它是外部脚本，没有 .d.ts）。 */
interface OctopusInstance {
  /**
   * 销毁：终止 worker 并把画布从 DOM 里摘掉。
   *
   * ⚠️ 方法名是 **`dispose`** —— 这个库**没有** `destroy()`（它只在内部向 worker
   * 发一个 `{target:'destroy'}` 消息）。真跑踩到过：原来写的是 `inst.destroy()`，
   * 运行时抛 TypeError 又被 catch 沙掉，于是画布永远留在 DOM 里，每重跑一次 effect
   * （切画质/换会话 → subtitleUrl 变了）就多叠一层 —— 用户看到的是「特效字幕变成重影」。
   */
  dispose(): void;
  /** 按视频元素当前尺寸重算画布（切窗口/进全屏后要对齐）。 */
  resize?(): void;
  /** 字幕时间基准（秒）：libass 用 `video.currentTime + timeOffset` 作为字幕时间。 */
  timeOffset?: number;
}

/**
 * 销毁一个 octopus 实例，并把**残留的画布**一并清掉。
 *
 * 画布是 `video` 后面的兄弟节点（`.libassjs-canvas-parent`），一个实例一张。
 * 只要有一张没清掉，它就会停在前一帧字幕上，与新画布叠在一起 → 重影。
 * 所以这里除了按对的方名销毁，还多一道兜底：把剩下的画布节点直接移除。
 */
function disposeOctopus(inst: OctopusInstance | null | undefined): void {
  if (inst) {
    const any = inst as unknown as { dispose?: () => void; destroy?: () => void };
    try {
      if (typeof any.dispose === 'function') any.dispose();
      else if (typeof any.destroy === 'function') any.destroy();
    } catch {
      /* 重复销毁不算错 */
    }
  }
  // 兜底：worker 已停但画布还在（例如实例是在 effect 已经取消之后才建出来的）。
  document.querySelectorAll('.libassjs-canvas-parent').forEach((el) => el.remove());
}

/**
 * 懒加载 libass（SubtitlesOctopus）—— ASS/SSA 的特效字幕交给它渲染。
 *
 * 为什么不用现成的 WebVTT：转成 WebVTT 会把定位（\pos）、轨迹（\move）、
 * 插值动画（\t）、卡拉OK（\k）、矢量绘图（\p）全丢掉，而带特效的字幕正是
 * ASS 的常态。资源在 /subtitles-octopus/（构建时从 libass-wasm 复制，见
 * web/scripts/copy-octopus.mjs），只有真要看 ASS 字幕才拉（~1.5MB，会缓存）。
 */
let octopusPromise: Promise<void> | null = null;

function loadOctopusScript(): Promise<void> {
  if ((window as { SubtitlesOctopus?: unknown }).SubtitlesOctopus) return Promise.resolve();
  if (!octopusPromise) {
    octopusPromise = new Promise<void>((resolve, reject) => {
      const s = document.createElement('script');
      s.src = '/subtitles-octopus/subtitles-octopus.js';
      s.async = true;
      s.onload = () => resolve();
      s.onerror = () => reject(new Error('libass 渲染器加载失败'));
      document.head.appendChild(s);
    });
  }
  return octopusPromise;
}

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
  const octopusRef = useRef<OctopusInstance | null>(null);

  /**
   * 把字幕渲染器的时间基准同步到当前窗口起点。
   *
   * 为什么需要：转封装是「一段段窗口」在放，`video.currentTime` 是**相对本窗口**的，
   * 而字幕时间轴是**片源绝对时间**。窗口一换（续段/拖动），两者的差就变了 ——
   * 不同步的话表现就是「播一会儿字幕和画面对不上」（实测报过来的现象）。
   */
  function syncSubtitleOffset() {
    const inst = octopusRef.current;
    if (inst) inst.timeOffset = baseRef.current;
  }

  // 这些值变化频繁（每帧都可能动），放 ref 里避免把整页重渲染成幻灯片
  const sessionIdRef = useRef('');
  const baseRef = useRef(0); // 当前窗口的起点（秒）：整片时间 = base + video.currentTime
  const durationRef = useRef(0);
  const windowEndRef = useRef(0);
  const advancingRef = useRef(false);
  const loadingRef = useRef(true);
  // 拖动进度条相关：拖动中的显示值、在途 seek 结束后要补做的目标、防抖定时器
  const dragValueRef = useRef<number | null>(null);
  const pendingSeekRef = useRef<number | null>(null);
  const seekTimerRef = useRef<number | null>(null);
  const lastReportRef = useRef(0);
  const stateRef = useRef<PlaybackState | null>(null);

  const [state, setState] = useState<PlaybackState | null>(null);
  const [playlist, setPlaylist] = useState<ItemPlaylist | null>(null);
  const [audioSel, setAudioSel] = useState(0);
  const [subSel, setSubSel] = useState(0);
  const [subReady, setSubReady] = useState(false);
  // 这个文件内封的字体（mkv 附件）的地址，交给 libass 渲染 \fn 引用的特效字体。
  // 没有就空数组：字幕退化成兑底字体，不影响播放。
  const [attFonts, setAttFonts] = useState<string[]>([]);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [loading, setLoading] = useState(true);
  const [position, setPosition] = useState(0);
  const [dragValue, setDragValue] = useState<number | null>(null);
  const [playing, setPlaying] = useState(false);
  const [volume, setVolume] = useState(1);
  const [muted, setMuted] = useState(false);
  const [showInfo, setShowInfo] = useState(false);
  const [isFullscreen, setIsFullscreen] = useState(false);
  const [seq, setSeq] = useState(0); // 换音轨/字幕/重播时 +1，触发重新开流
  // 画质档记在 localStorage 里，下次进来沿用。用 ref 存一份是因为 start() 是
  // useCallback：直接闭包读 state 会拿到旧值，结果「切了档位却按上一档重开」。
  const [quality, setQuality] = useState<QualityChoice>(readQuality);
  const qualityRef = useRef<QualityChoice>(quality);

  const duration = state?.durationSeconds || 0;
  const mode = state ? modeLabel(state.plan.mode) : null;

  const audioOptions = useMemo(() => playlist?.files[0]?.audio ?? [], [playlist]);
  const subOptions = useMemo(() => playlist?.files[0]?.subtitles ?? [], [playlist]);

  /**
   * 选中的是不是**图形**字幕（PGS/VobSub）：是的话要烧进画面。
   *
   * 为什么只能烧：它是位图，浏览器没法当文本渲染（也不像 ASS 那样能交给
   * libass）。代价是服务端要重编一遍画面 —— 所以界面要把这件事说出来。
   */
  const subBurn = useMemo(
    () => Boolean(subOptions.find((s) => s.index === subSel)?.isImage),
    [subOptions, subSel],
  );

  // 画质菜单只列比源低的档（比源高的档没有意义：那等于原生）。
  const sourceHeight = state?.plan.video.sourceHeight ?? 0;
  const qualityTiers = useMemo(
    () => QUALITY_TIERS.filter((h) => !sourceHeight || h < sourceHeight),
    [sourceHeight],
  );

  /** 记住最新的会话状态，供事件回调（闭包里拿不到新 state）使用。 */
  useEffect(() => {
    stateRef.current = state;
  }, [state]);

  useEffect(() => {
    qualityRef.current = quality;
  }, [quality]);

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
    async (opts: {
      restart?: boolean;
      position: number;
      audio: number;
      sub: number;
      maxHeight?: number;
      burn?: boolean;
    }) => {
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
          burnSubtitle: opts.burn,
          restart: opts.restart,
          startPositionTicks: opts.position > 0 ? secondsToTicks(opts.position) : undefined,
          maxHeight: opts.maxHeight !== undefined ? opts.maxHeight : maxHeightOf(qualityRef.current),
        });
        setState(st);
        if (!st.playable || !st.playSessionId) {
          setLoading(false);
          return;
        }
        sessionIdRef.current = st.playSessionId;
        baseRef.current = st.startSeconds;
        syncSubtitleOffset();
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
    const restart = restartOnceRef.current;
    restartOnceRef.current = false;
    // 换音轨/字幕/画质时从**当前位置**续播：服务端存的那条进度可能落后几秒，
    // 用它续播会看到画面往回跳一下。
    const here = restart ? 0 : baseRef.current + (videoRef.current?.currentTime ?? 0);
    void start({ restart, position: here, audio: audioSel, sub: subSel, burn: subBurn });
    return () => {
      // 离开页面：停掉转封装会话（服务端会顺手回收 ffmpeg 与分片）
      stopWithBeacon();
    };
    // seq 变化 = 重新载入；音轨/字幕/画质改了 → 用新选择重开一路
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [itemId, seq, audioSel, subSel, quality, subBurn]);

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
    if (!sid || !v) return;
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
      syncSubtitleOffset();
      setState(st);
      setNotice('');
      reloadStream(st.hlsUrl, wasPaused);
      // 续段后立刻上报一次：用户很可能就是在拖到新位置之后关页面的，
      // 如果只靠 10 秒一次的定期上报，这一次跳转就丢了。
      void report(true);
    } catch (e) {
      setError(messageOf(e, '续下一段失败'));
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

  /**
   * 真正执行一次跳转。
   *
   * 为什么要排队：拖进度条时浏览器会连发上百个事件（几乎每个像素一个），
   * 而转封装模式下每个「窗口外」的位置都要服务端重开一段 ffmpeg —— 不可能全执行。
   * 早先的写法是直接丢弃后来的请求，后果是「拖到 1:30，结果只跳到第一个事件的位置」
   * （用户的原话：拖到后面却从头开始放）。现在改成：在途时只记下最后的目标，
   * 上一次完成后补做最后那一个 —— 拖到哪，最终就停在哪。
   */
  const doSeek = useCallback(
    async (target: number) => {
      const v = videoRef.current;
      const st = stateRef.current;
      if (!v || !st) return;
      if (advancingRef.current) {
        pendingSeekRef.current = target;
        return;
      }
      advancingRef.current = true;
      try {
        if (st.mode === 'direct') {
          v.currentTime = target;
          setPosition(target);
          void report(true);
        } else {
          // 能不能就地跳？必须同时满足：
          //   1. 目标在本段窗口里；
          //   2. 目标已经在**浏览器已加载的区间**里。
          //
          // 第 2 条是踩出来的：hls.js 的可用区间只是「已下载的那几个分片」，
          // 只按窗口判断的话，跳到一个还没下载到的位置会被浏览器夹回去 ——
          // 表现就是「拖到 1:18，却从 0:00 继续放」。不在已加载区间就老老实实
          // 让服务端从目标位置重开一段（`-c copy` 很快，一两秒事）。
          const loaded = v.seekable.length > 0 ? v.seekable.end(v.seekable.length - 1) : 0;
          const inLocalRange =
            target >= baseRef.current &&
            target < baseRef.current + loaded - 1 &&
            target < windowEndRef.current - 1;
          if (inLocalRange) {
            v.currentTime = target - baseRef.current;
            setPosition(target);
            void report(true);
          } else {
            await advance(target);
            setPosition(target);
          }
        }
      } finally {
        advancingRef.current = false;
        setDragValue(null);
        const next = pendingSeekRef.current;
        pendingSeekRef.current = null;
        if (next !== null && Math.abs(next - target) > 2) void doSeek(next);
      }
    },
    [advance, report],
  );

  /** 拖动中：只改显示，250ms 防抖后才真提交（松手时立即提交）。 */
  const scheduleSeek = useCallback(
    (target: number) => {
      dragValueRef.current = target;
      setDragValue(target);
      if (seekTimerRef.current !== null) window.clearTimeout(seekTimerRef.current);
      seekTimerRef.current = window.setTimeout(() => {
        seekTimerRef.current = null;
        void doSeek(target);
      }, 250);
    },
    [doSeek],
  );

  /** 松手（鼠标抬起 / 键盘调整）：不等防抖，立即提交。 */
  const commitSeek = useCallback(() => {
    if (seekTimerRef.current !== null) {
      window.clearTimeout(seekTimerRef.current);
      seekTimerRef.current = null;
    }
    const target = dragValueRef.current;
    dragValueRef.current = null;
    if (target !== null) void doSeek(target);
  }, [doSeek]);

  /** 快捷键用的相对跳转。 */
  const nudge = useCallback(
    (delta: number) => {
      const v = videoRef.current;
      if (!v) return;
      void doSeek(Math.max(0, baseRef.current + v.currentTime + delta));
    },
    [doSeek],
  );

  const onTimeUpdate = useCallback(() => {
    const v = videoRef.current;
    if (!v) return;
    // 时间在走就说明有画面了：把「载入中」遮罩收掉
    //（续窗口之后 reload 期间会重新置上，靠这一句兜底）。
    if (loadingRef.current) setLoading(false);
    const abs = baseRef.current + v.currentTime;
    // 拖动中不要用播放位置盖掉滑块，否则滑块会被拽回去
    if (dragValueRef.current === null) setPosition(abs);

    const st = stateRef.current;
    if (!st || st.mode === 'direct' || advancingRef.current) return;
    const we = windowEndRef.current;
    if (!we || we >= durationRef.current - 1) return;
    if (abs >= we - ADVANCE_MARGIN_S) void doSeek(we);
  }, [doSeek]);

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
          nudge(-10);
          break;
        case 'ArrowRight':
          nudge(10);
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
  }, [togglePlay, toggleFullscreen, nudge]);

  // 字幕开关（<track> 用 textTracks 控制显示）
  useEffect(() => {
    const v = videoRef.current;
    if (!v) return;
    const tracks = v.textTracks;
    for (let i = 0; i < tracks.length; i++) tracks[i].mode = subSel === -1 ? 'disabled' : 'showing';
  }, [subReady, subSel]);

  // 内封字体（mkv 附件）：只在字幕交给 libass 时才需要。
  // 服务端要把源文件读一遍才抽得出来，所以这里耐心轮询；拿不到就空着（退化成兜底字体）。
  useEffect(() => {
    const sid = state?.playSessionId;
    if (!sid || state?.subtitleFormat !== 'ass') {
      setAttFonts([]);
      return;
    }
    let cancelled = false;
    void (async () => {
      const fonts = await api.attachmentFonts(sid);
      if (!cancelled && fonts.length) setAttFonts(fonts.map((f) => absUrl(f.url)));
    })();
    return () => {
      cancelled = true;
    };
  }, [state?.playSessionId, state?.subtitleFormat]);

  // 特效字幕（ASS/SSA）：交给 libass 画在 canvas 上（WebVTT 装不下那些特效）。
  // 关字幕 / 换字幕轨 / 换片子时销毁重建 —— 比增量控制简单且不会漏。
  useEffect(() => {
    const v = videoRef.current;
    const url = state?.subtitleUrl;
    if (!v || !url || !subReady || subSel === -1 || state?.subtitleFormat !== 'ass') return;
    let cancelled = false;
    let inst: OctopusInstance | null = null;
    void (async () => {
      try {
        // 先等到视频真的有尺寸，否则画布会被建成 0×0 并隐藏（见 waitVideoSized）。
        await waitVideoSized(v);
        const text = await fetchSubtitleText(url);
        if (cancelled) return;
        await loadOctopusScript();
        if (cancelled) return;
        const Ctor = (
          window as unknown as {
            SubtitlesOctopus?: new (o: Record<string, unknown>) => OctopusInstance;
          }
        ).SubtitlesOctopus;
        if (!Ctor) throw new Error('libass 渲染器未就绪');
        const font = await pickFallbackFont();
        if (cancelled) return;
        if (!font) {
          setNotice('服务端没配兑底字体（放任意中文字体到 <数据目录>/fonts/fallback.ttf），特效字幕暂时显示不了');
          return;
        }
        // 建实例之前先把可能残留的画布清掉：万一上次是被中途取消的（见下），
        // 残留画布会永远停在上一帧字幕上 → 重影。
        disposeOctopus(null);
        inst = new Ctor({
          video: v,
          // 直接喂字幕**文本**（见 fetchSubtitleText 的注释）。
          subContent: text,
          // 路径必须是绝对的：SPA 路由下（/play/123）相对路径会被解析成
          // /play/xxx.js，静态服务只会回 404，渲染器连 worker 都起不来。
          workerUrl: absUrl('/subtitles-octopus/subtitles-octopus-worker.js'),
          legacyWorkerUrl: absUrl('/subtitles-octopus/subtitles-octopus-worker-legacy.js'),
          renderMode: 'js-blend',
          // HLS 给的是「窗口相对时间」：把窗口起点当偏移，字幕才对得上片源时间轴。
          // 不传这个，续段之后字幕就会整体偏移（用户报的「播一会就对不上」）。
          timeOffset: baseRef.current,
          // 兑底字体由服务端提供：libass/WASM 看不到客户端的系统字体，而 octopus
          // 默认要的 `default.woff2` 在 npm 包里根本不存在 —— 缺了它 worker 直接崩。
          fallbackFont: font,
          // 内封字体（mkv 附件）：字幕里 \fn / Style 引用的特效字体就在这里面。
          // 不给的话 libass 只能拿兑底字体画 —— 表现是特效标题/美术字糊成一团
          //（用户报的「用保底字体又叠了一层」）。libass 会按字体内部的家族名自己匹配。
          // 首次要等服务端把附件抽出来（要读一遍整部片子），先拿到多少就先给多少。
          fonts: attFonts,
          onError: () => {
            if (!cancelled) setNotice('特效字幕渲染失败，本条字幕暂不显示');
          },
        });
        // 创建是同步的，但上面几个 await 期间 effect 可能已经被取消 ——
        // 那个实例**没被任何人引用**，只能在这里就地销毁，否则它那张画布会留下来。
        if (cancelled) {
          disposeOctopus(inst);
          return;
        }
        octopusRef.current = inst;
      } catch {
        if (!cancelled) setNotice('特效字幕渲染器加载失败，本条字幕暂不显示');
      }
    })();
    // 视频尺寸会变（续段换窗口、切画质、进全屏）：octopus 只在创建时和 window resize
    // 时算尺寸，**视频自己**的尺寸变化它不管 —— 更糟的是尺寸瞬间为 0 时它会把画布
    // `display:none` 且不恢复（实测：续段之后字幕就没了）。所以自己盯着。
    const ro = new ResizeObserver(() => {
      void (async () => {
        await waitVideoSized(v);
        octopusRef.current?.resize?.();
      })();
    });
    ro.observe(v);

    return () => {
      cancelled = true;
      ro.disconnect();
      // 实例可能是这个 effect 里建的（inst），也可能是上一次留下来的（octopusRef）
      disposeOctopus(inst ?? octopusRef.current);
      octopusRef.current = null;
    };
  }, [state?.subtitleUrl, state?.subtitleFormat, subReady, subSel, attFonts]);

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
          {/* ASS/SSA 由 libass 画在 canvas 上，不走原生轨道 */}
          {subtitleOn && state?.subtitleUrl && state.subtitleFormat !== 'ass' && (
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
                  服务端给出的具体原因（例如视频是 10bit HEVC，浏览器解不了，要重新编码成 h264）。
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
          value={Math.min(Math.floor(dragValue ?? position), Math.floor(duration))}
          onChange={(e) => scheduleSeek(Number(e.target.value))}
          onPointerUp={commitSeek}
          onKeyUp={commitSeek}
          onBlur={commitSeek}
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
                {s.isImage ? '（图形，需烧录）' : ''}
              </option>
            ))}
          </select>
        )}

        {/* 图形字幕只能烧进画面（服务端要重编一遍）——这是有代价的选择，
            所以不在菜单里偷偷做，而是选完就把代价写出来。 */}
        {subBurn && (
          <span className="faint" title="图形字幕是位图，只能烧进画面；服务端会重新编码一遍">
            字幕将烧进画面（需重新编码）
          </span>
        )}

        {sourceHeight > 0 && (
          <select
            className="player-select"
            value={String(quality)}
            onChange={(e) => {
              const raw = e.target.value;
              const next: QualityChoice =
                raw === 'auto' ? 'auto' : raw === 'original' ? 'original' : Number(raw);
              qualityRef.current = next; // 先喂 ref：重开流是随后同步触发的
              setQuality(next);
              try {
                localStorage.setItem(QUALITY_KEY, String(next));
              } catch {
                /* 隐私模式下写不进去，不影响播放 */
              }
            }}
            aria-label="画质"
            title="选低于源分辨率的档会让服务端转码输出"
          >
            <option value="auto">画质：自动</option>
            <option value="original">画质：原生（{sourceHeight}p）</option>
            {qualityTiers.map((h) => (
              <option key={h} value={String(h)}>
                画质：{h}p
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
