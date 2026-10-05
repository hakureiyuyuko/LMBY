import { useSyncExternalStore } from 'react';

/**
 * 「体验资源」的下载进度（播放页底部那条进度条）。
 *
 * 为什么需要它：看 ASS 特效字幕要先等几样东西下完 —— 兜底字体（一个 ~20MB 的
 * CJK 字体）、内封字体附件、libass 的 wasm。这些平时是 **Web Worker 里自己
 * fetch** 的，主线程既看不到那个请求、也就没法显示进度，用户看到的就是「卡住」。
 *
 * 做法：在创建渲染器之前，由**主线程**把同样的 URL 先取一遍（见
 * `prefetchWithProgress`）——
 *   1. 进度可见（本模块把字节数汇总起来，界面据此画条）；
 *   2. 响应进了 HTTP 缓存（服务端给了 `Cache-Control: private, max-age=86400`），
 *      worker 随后的取用直接命中缓存，**网络只会下一次**。
 *
 * 所以这只是「把本来就要发生的下载变得看得见」，不额外增加流量。
 */

export type DlSnapshot = {
  /** 正在下载的资源数（0 = 什么都没在下 → 进度条不显示）。 */
  active: number;
  /** 已下载字节数（所有活跃资源之和）。 */
  loaded: number;
  /** 总字节数（Content-Length 之和）；服务端没给长度时是 0（进度条走不确定态）。 */
  total: number;
};

type Job = { loaded: number; total: number };

const jobs = new Map<number, Job>();
let seq = 0;
let snapshot: DlSnapshot = { active: 0, loaded: 0, total: 0 };
const listeners = new Set<() => void>();

function recompute(): void {
  let loaded = 0;
  let total = 0;
  for (const j of jobs.values()) {
    loaded += j.loaded;
    total += j.total;
  }
  snapshot = { active: jobs.size, loaded, total };
  for (const l of listeners) l();
}

function subscribe(cb: () => void): () => void {
  listeners.add(cb);
  return () => {
    listeners.delete(cb);
  };
}

function getSnapshot(): DlSnapshot {
  return snapshot;
}

/** 订阅下载进度。 */
export function useDownloadProgress(): DlSnapshot {
  return useSyncExternalStore(subscribe, getSnapshot, getSnapshot);
}

/** 注册一个下载任务并返回它的 id（进度的累加/清除都按 id 记）。 */
function beginJob(total: number): number {
  const id = ++seq;
  jobs.set(id, { loaded: 0, total });
  recompute();
  return id;
}

function endJob(id: number): void {
  jobs.delete(id);
  recompute();
}

/** 读干一个 ReadableStream，边读边把字节数累到对应任务上。 */
async function drain(id: number, body: ReadableStream<Uint8Array>): Promise<Uint8Array[]> {
  const reader = body.getReader();
  const chunks: Uint8Array[] = [];
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      if (!value) continue;
      chunks.push(value);
      const job = jobs.get(id);
      if (job) {
        job.loaded += value.byteLength;
        recompute();
      }
    }
  } finally {
    endJob(id);
  }
  return chunks;
}

/**
 * 把 URL 整个读掉（预热 + 进度）。任何失败都静默吞掉 —— 预热只是体验优化，
 * 不能挡住播放（真正取用它的是 worker，失败它会自己再试）。
 */
export async function prefetchWithProgress(url: string, init?: RequestInit): Promise<void> {
  let res: Response;
  try {
    res = await fetch(url, { credentials: 'same-origin', ...init });
  } catch {
    return;
  }
  if (!res.ok || !res.body) return;
  const total = Number(res.headers.get('Content-Length') ?? '') || 0;
  const id = beginJob(total);
  try {
    await drain(id, res.body);
  } catch {
    /* 被中断（换会话 / 离开页面）：当作没在下载 */
  }
}

/**
 * 同 `prefetchWithProgress`，但把内容回给调用方（字幕文本这类要自己拿文本的）。
 * 返回的对象沿用原响应的状态码 —— 202（字幕还在抽）之类的分支判断不受影响。
 */
export async function fetchWithProgress(url: string, init?: RequestInit): Promise<Response> {
  const res = await fetch(url, { credentials: 'same-origin', ...init });
  if (!res.ok || !res.body) return res;
  const total = Number(res.headers.get('Content-Length') ?? '') || 0;
  const id = beginJob(total);
  const chunks = await drain(id, res.body);
  return new Response(new Blob(chunks as BlobPart[]), { status: res.status, headers: res.headers });
}
