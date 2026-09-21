// 特效字幕诊断：开/关字幕做**对照**（画布像素 + 截图），并抓 console 与网络。
//
//   BASE=http://... node diagnose-subs.mjs [itemId] [subtitleIndex]
//
// 两个参数可省略（会自动从库里挑一个带 ASS 字幕的条目）。
import { spawn } from 'node:child_process';
import { writeFileSync, mkdtempSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';

const CHROME = process.env.CHROME || 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const BASE = process.env.BASE || 'http://127.0.0.1:8099';
const PORT = Number(process.env.CDP_PORT || 9566);

const profile = mkdtempSync(path.join(os.tmpdir(), 'lmby-diag-'));
const chrome = spawn(CHROME, [
  '--headless=new',
  `--remote-debugging-port=${PORT}`,
  '--window-size=1400,900',
  '--hide-scrollbars',
  '--no-first-run',
  '--no-default-browser-check',
  `--user-data-dir=${profile}`,
]);

async function targetUrl() {
  for (let i = 0; i < 60; i++) {
    try {
      const list = await (await fetch(`http://127.0.0.1:${PORT}/json/list`)).json();
      const page = list.find((t) => t.type === 'page' && t.webSocketDebuggerUrl);
      if (page) return page.webSocketDebuggerUrl;
    } catch {
      /* 等一会 */
    }
    await sleep(250);
  }
  throw new Error('Chrome 没起来');
}

const ws = new WebSocket(await targetUrl());
await new Promise((res, rej) => {
  ws.onopen = res;
  ws.onerror = rej;
});

let seq = 0;
const pending = new Map();
const consoleMsgs = [];
const netLog = [];
ws.onmessage = (ev) => {
  const msg = JSON.parse(ev.data);
  if (msg.method === 'Runtime.consoleAPICalled') {
    const text = (msg.params.args || [])
      .map((a) => a.value ?? a.description ?? a.type)
      .join(' ');
    consoleMsgs.push(`[${msg.params.type}] ${text}`.slice(0, 300));
  }
  if (msg.method === 'Runtime.exceptionThrown') {
    consoleMsgs.push(`[exception] ${msg.params.exceptionDetails?.text || ''} ${msg.params.exceptionDetails?.exception?.description || ''}`.slice(0, 400));
  }
  if (msg.method === 'Network.responseReceived') {
    const { url, status } = msg.params.response;
    if (/subtitles|octopus|\.wasm|\.ass/i.test(url)) netLog.push(`${status} ${url}`);
  }
  if (msg.method === 'Network.loadingFailed') {
    netLog.push(`FAILED ${msg.params.errorText} type=${msg.params.type}`);
  }
  if (msg.id && pending.has(msg.id)) {
    const { resolve, reject } = pending.get(msg.id);
    pending.delete(msg.id);
    msg.error ? reject(new Error(JSON.stringify(msg.error))) : resolve(msg.result);
  }
};
const send = (method, params = {}) =>
  new Promise((resolve, reject) => {
    const id = ++seq;
    pending.set(id, { resolve, reject });
    ws.send(JSON.stringify({ id, method, params }));
  });

async function evaluate(expr) {
  const r = await send('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true });
  if (r.exceptionDetails) throw new Error(r.exceptionDetails.text || 'evaluate 失败');
  return r.result?.value;
}
async function waitFor(name, fn, timeoutMs = 30000) {
  const t0 = Date.now();
  for (;;) {
    if (await fn()) return true;
    if (Date.now() - t0 > timeoutMs) {
      console.log(`  超时：${name}`);
      return false;
    }
    await sleep(300);
  }
}

const PROBE = `(() => {
  const parent = document.querySelector('.libassjs-canvas-parent');
  if (!parent) return { parent: false };
  const c = parent.querySelector('canvas');
  if (!c) return { parent: true, canvas: false, html: parent.innerHTML.slice(0, 150) };
  let ink = -1;
  try {
    const ctx = c.getContext('2d');
    if (ctx) {
      const d = ctx.getImageData(0, 0, c.width, Math.min(300, c.height)).data;
      ink = 0;
      for (let i = 3; i < d.length; i += 4) if (d[i] > 8) ink++;
    } else { ink = -2; }
  } catch (e) { ink = -3; }
  return {
    parent: true, canvas: true,
    intrinsic: c.width + 'x' + c.height,
    css: c.clientWidth + 'x' + c.clientHeight,
    style: (c.getAttribute('style') || '').slice(0, 120),
    ink,
  };
})()`;

await send('Page.enable');
await send('Runtime.enable');
await send('Network.enable');

await send('Page.navigate', { url: `${BASE}/login` });
await sleep(1000);
const code = await evaluate(
  `fetch('/api/v1/auth/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({username:'devtest',password:'devtest-pass-1'})}).then(r=>r.status)`,
);
console.log(`登录 http=${code}`);

// 找样本：遍历库 → 第一个带 ASS 字幕的条目
const found = await evaluate(`(async () => {
  const libs = await (await fetch('/api/v1/libraries')).json();
  const list = libs.libraries || libs || [];
  for (const lib of list) {
    const r = await fetch('/api/v1/libraries/' + lib.id + '/items?limit=200');
    if (!r.ok) continue;
    const d = await r.json();
    for (const it of d.items || []) {
      const pl = await (await fetch('/api/v1/items/' + it.id + '/playlist')).json();
      for (const f of pl.files || []) {
        const sub = (f.subtitles || []).find((s) => s.codec === 'ass' || s.codec === 'ssa');
        if (sub) return { itemId: it.id, title: it.title, index: sub.index, codec: sub.codec };
      }
    }
  }
  return null;
})()`);
console.log(`样本：${JSON.stringify(found)}`);
if (!found) {
  console.log('库里没找到带 ASS 的条目，退出');
  process.exit(1);
}

await send('Page.navigate', { url: `${BASE}/play/${found.itemId}` });
await waitFor('播放器', async () => Boolean(await evaluate(`!!document.querySelector('.player-video')`)), 30000);
await waitFor(
  '视频可播',
  async () => (await evaluate(`(() => { const v = document.querySelector('.player-video'); return v ? v.readyState : 0; })()`)) >= 2,
  60000,
);
await sleep(3000);

// ① 关字幕（默认）→ 画布像素
const before = await evaluate(PROBE);
console.log(`\n【关字幕】${JSON.stringify(before)}`);
const shotA = await send('Page.captureScreenshot', { format: 'png' });
writeFileSync('diag-off.png', Buffer.from(shotA.data, 'base64'));

// ② 选 ASS 轨
await waitFor('字幕下拉', async () => Boolean(await evaluate(`!!document.querySelector('select[aria-label="字幕"]')`)), 30000);
const picked = await evaluate(`(() => {
  const sel = document.querySelector('select[aria-label="字幕"]');
  const opt = [...sel.options].find((o) => o.value === '${found.index}');
  if (!opt) return null;
  sel.value = opt.value;
  sel.dispatchEvent(new Event('change', { bubbles: true }));
  return opt.textContent.trim();
})()`);
console.log(`选字幕：${picked}`);

await waitFor('libass canvas', async () => Boolean(await evaluate(`!!document.querySelector('.libassjs-canvas-parent')`)), 60000);

// ③ 拿 ASS 内容，挑一条「持续 >1.5 秒」的对白跳过去
const jumpTo = await evaluate(`(async () => {
  const urls = performance.getEntriesByType('resource').map((e) => e.name).filter((n) => /\\/subtitles\\/.*\\.ass$/.test(n));
  if (!urls.length) return { err: '页面没请求过 .ass' };
  const r = await fetch(urls[urls.length - 1]);
  if (!r.ok) return { err: 'ASS http=' + r.status };
  const text = await r.text();
  const toSec = (h, m, s) => (+h) * 3600 + (+m) * 60 + (+s);
  const lines = text.split(/\\r?\\n/).filter((l) => /^Dialogue:/.test(l));
  for (const l of lines) {
    const m = l.match(/^Dialogue:[^,]*, *(\\d+):(\\d+):([0-9.]+), *(\\d+):(\\d+):([0-9.]+)/);
    if (!m) continue;
    const a = toSec(m[1], m[2], m[3]);
    const b = toSec(m[4], m[5], m[6]);
    if (b - a >= 1.5 && a > 1) return { start: a, end: b, raw: l.slice(0, 80) };
  }
  return { err: '没有合适的对白行', dialogues: lines.length };
})()`);
console.log(`跳转目标：${JSON.stringify(jumpTo)}`);
if (jumpTo?.start) {
  // 跳到这里并**暂停**：让那一句字幕定在画面上，好做像素对照。
  await evaluate(`(() => {
    const v = document.querySelector('.player-video');
    if (v) { v.currentTime = ${(jumpTo.start + jumpTo.end) / 2}; v.pause(); }
    return true;
  })()`);
  await sleep(5000);
}
const after = await evaluate(PROBE);
console.log(`\n【开字幕】${JSON.stringify(after)}`);
const shotB = await send('Page.captureScreenshot', { format: 'png' });
writeFileSync('diag-on.png', Buffer.from(shotB.data, 'base64'));

console.log(`\n像素对比：关字幕 ink=${before?.ink} → 开字幕 ink=${after?.ink}`);
console.log(`\n-- 相关网络（${netLog.length}）--`);
for (const l of netLog.slice(-25)) console.log('  ' + l);
console.log(`\n-- console（${consoleMsgs.length}）--`);
for (const l of consoleMsgs.slice(-25)) console.log('  ' + l);
console.log('\n截图：diag-off.png / diag-on.png');

ws.close();
chrome.kill();
