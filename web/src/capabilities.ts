import type { PlaybackProfile } from './api';

/**
 * 播放能力探测：把「这个浏览器到底能解什么」实测一遍上报给服务端。
 *
 * 为什么需要它：服务端有一份保守的默认能力表（三端浏览器交集），但真机上差别很大 ——
 * Safari 能解 HEVC、Windows 上的 Chrome 也常能、Linux 上多半不行；AC3 同理。
 * 静态表只能取交集（等于谁都不支持 HEVC/AC3），实测才能把能力用起来。
 *
 * 原则：**宁可少报，不可多报**。多报一个编码的代价是黑屏或无声（用户只会说「坏了」），
 * 少报的代价只是多转封装一次（画面一样）。
 * 所以只认 MediaSource.isTypeSupported 的明确 true，异常一律当不支持。
 */

let cached: PlaybackProfile | null = null;

/** probeCodec 探测一个具体的 MIME + codecs 组合。 */
function probeCodec(type: string): boolean {
  try {
    if (typeof MediaSource !== 'undefined' && typeof MediaSource.isTypeSupported === 'function') {
      return MediaSource.isTypeSupported(type);
    }
  } catch {
    // 某些浏览器在非安全上下文里会直接抛异常
  }
  try {
    const v = document.createElement('video');
    return v.canPlayType(type) !== '';
  } catch {
    return false;
  }
}

export function detectProfile(): PlaybackProfile {
  if (cached) return cached;

  const containers: string[] = [];
  const videoCodecs: string[] = [];
  const audioCodecs: string[] = [];

  // 容器：mp4 是所有浏览器的基础；webm 只有支持 VP8/9 的才算数
  if (probeCodec('video/mp4; codecs="avc1.42E01E"')) containers.push('mp4');
  if (probeCodec('video/webm; codecs="vp9"') || probeCodec('video/webm; codecs="vp8"')) {
    containers.push('webm');
  }
  if (containers.length === 0) containers.push('mp4');

  // 视频编码
  const h264 = probeCodec('video/mp4; codecs="avc1.640028"');
  if (h264) videoCodecs.push('h264');
  const hevc =
    probeCodec('video/mp4; codecs="hvc1.1.6.L120.90"') ||
    probeCodec('video/mp4; codecs="hvc1.1.6.L93.B0"');
  if (hevc) videoCodecs.push('hevc');
  if (probeCodec('video/mp4; codecs="vp09.00.10.08"')) videoCodecs.push('vp9');
  if (probeCodec('video/mp4; codecs="av01.0.05M.08"')) videoCodecs.push('av1');
  if (probeCodec('video/webm; codecs="vp8"')) videoCodecs.push('vp8');
  if (videoCodecs.length === 0) videoCodecs.push('h264');

  // 音频编码
  if (probeCodec('audio/mp4; codecs="mp4a.40.2"')) audioCodecs.push('aac');
  if (probeCodec('audio/mpeg')) audioCodecs.push('mp3');
  if (probeCodec('audio/webm; codecs="opus"') || probeCodec('audio/mp4; codecs="opus"')) {
    audioCodecs.push('opus');
  }
  if (probeCodec('audio/webm; codecs="vorbis"')) audioCodecs.push('vorbis');
  if (probeCodec('audio/mp4; codecs="flac"') || probeCodec('audio/flac')) audioCodecs.push('flac');
  if (probeCodec('audio/mp4; codecs="ac-3"')) audioCodecs.push('ac3');
  if (probeCodec('audio/mp4; codecs="ec-3"')) audioCodecs.push('eac3');
  if (audioCodecs.length === 0) audioCodecs.push('aac');

  // 分辨率/色深上限：这里给的是「现实可达」的上限。
  //
  // 10bit 单独看编码：HEVC 10bit 在 Safari 与部分 Chromium 上能放，
  // 但 **10bit H.264（Hi10P）任何浏览器都放不了** —— 服务端也知道这一点
  // （playback.Profile.SupportsVideo 里单独判了），所以这里照实报就行。
  const profile: PlaybackProfile = {
    name: 'browser',
    containers,
    videoCodecs,
    audioCodecs,
    subtitleFormats: ['webvtt'],
    maxWidth: 3840,
    maxHeight: 2160,
    maxBitDepth: hevc ? 10 : 8,
    maxAudioChannels: 6,
    supportsHls: true,
    supportsFmp4: true,
    supportsTs: true,
  };
  cached = profile;
  return profile;
}

// 原生 HLS 支持（Safari / iOS）：能直接给 <video src=*.m3u8>，不用 hls.js。
//
// 注意这里**不能**只看 canPlayType：实测 Chrome（2026 年的版本）对
// 'application/vnd.apple.mpegurl' 也会回非空值（"maybe"），于是我们会把 m3u8
// 直接塞给 <video src> —— 结果 readyState=4 但 duration=0、画面永远不动，
// 而且不报错（比直接失败更难查）。
//
// 所以先用 UA 判「是不是真的 Safari/iOS 系」：只有它们把 HLS 当一等公民。
// 其余浏览器（包括将来真支持 HLS 的 Chrome）走 hls.js —— MSE 路径一样能播。
export function hasNativeHls(video: HTMLVideoElement | null): boolean {
  if (!video) return false;
  if (!isSafariFamily()) return false;
  try {
    return video.canPlayType('application/vnd.apple.mpegurl') !== '';
  } catch {
    return false;
  }
}

// isSafariFamily 判断是否 Safari / iOS / iPadOS（iPadOS 的 UA 伪装成 Macintosh）。
function isSafariFamily(): boolean {
  const ua = navigator.userAgent || '';
  const isIOS =
    /iPad|iPhone|iPod/.test(ua) || (ua.includes('Macintosh') && (navigator.maxTouchPoints ?? 0) > 1);
  if (isIOS) return true;
  // 其它浏览器的 UA 里都带自己的标识（Chrome/CriOS/FxiOS/Edg），排除掉剩下的才是 Safari
  return /Safari\//.test(ua) && !/Chrome|Chromium|CriOS|FxiOS|Edg|OPR|Android/i.test(ua);
}

/** ticks ↔ 秒。1 tick = 100ns，与后端一致。 */
export const TICKS_PER_SECOND = 10_000_000;

export function ticksToSeconds(t: number): number {
  return t > 0 ? t / TICKS_PER_SECOND : 0;
}

export function secondsToTicks(s: number): number {
  return s > 0 ? Math.round(s * TICKS_PER_SECOND) : 0;
}

/** 把秒格式化成 mm:ss / h:mm:ss。 */
export function formatClock(sec: number): string {
  if (!Number.isFinite(sec) || sec < 0) sec = 0;
  const total = Math.floor(sec);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  const mm = h > 0 ? String(m).padStart(2, '0') : String(m);
  return `${h > 0 ? `${h}:` : ''}${mm}:${String(s).padStart(2, '0')}`;
}

/** 播放方式的界面标签与说明。 */
import { t } from './i18n';

export function modeLabel(mode: string): { label: string; hint: string } {
  switch (mode) {
    case 'direct':
      return { label: t('直接播放'), hint: t('原文件按 HTTP Range 分段送出，服务端零转码') };
    case 'remux':
      return { label: t('转封装'), hint: t('视频不重新编码，只换容器（HLS 分片）') };
    case 'transcode':
      return { label: t('需要转码'), hint: t('视频要重新编码（例如 10bit HEVC，或客户端选了更低的画质）') };
    default:
      return { label: mode, hint: '' };
  }
}

/** 流动作的界面文案。 */
export function actionLabel(action: string): string {
  switch (action) {
    case 'copy':
      return t('原样复制');
    case 'convert':
      return t('重新编码');
    case 'transcode':
      return t('转码');
    case 'burn':
      return t('烧录');
    case 'drop':
      return t('不显示');
    default:
      return t('不使用');
  }
}
