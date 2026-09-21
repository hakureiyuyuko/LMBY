// 验证「特效字幕」在真浏览器里的渲染：登录 → 打开播放器 → 选 ASS 字幕轨 →
// 等 libass（SubtitlesOctopus）的 canvas 出现 → 跳到有对白的位置 → 截图。
//
// ASS 地址从 Network 监听里拿（octopus 自己会去请求它）—— 这样不必再建一个播放会话。
//
//   BASE=http://... node subs-shot.mjs <itemId> <subtitleIndex> out.png
import { spawn } from 'node:child_process';
import { writeFileSync, mkdtempSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';

const CHROME = process.env.CHROME || 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const BASE = process.env.BASE || 'http://127.0.0.1:8099';
const ITEM = Number(process.argv[2] || 0);
const SUB = Number(process.argv[3] || 0);
const OUT = process.argv[4] || 'subs.png';
const PORT = Number(process.env.CDP_PORT || 9555);

const profile = mkdtempSync(path.join(os.tmpdir(), 'lmby-subs-'));
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
      /* 还没起来 */
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
const subUrls = [];
ws.onmessage = (ev) => {
  const msg = JSON.parse(ev.data);
  if (msg.method === 'Network.requestWillBeSent' && /\/subtitles\/.*\.ass/.test(msg.params.request.url)) {
    subUrls.push(msg.params.request.url);
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
      console.log(`超时：${name}`);
      return false;
    }
    await sleep(300);
  }
}

await send('Page.enable');
await send('Runtime.enable');
await send('Network.enable');

// 直接用接口登录（同源 fetch 会带上会话 cookie），省掉走登录表单
await send('Page.navigate', { url: `${BASE}/login` });
await sleep(1200);
const loginCode = await evaluate(
  `fetch('/api/v1/auth/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({username:'devtest',password:'devtest-pass-1'})}).then(r=>r.status)`,
);
console.log(`登录 http=${loginCode}`);

await send('Page.navigate', { url: `${BASE}/play/${ITEM}` });
await waitFor('播放器', async () => Boolean(await evaluate(`!!document.querySelector('.player-video')`)), 30000);

// 字幕下拉是「加载完 playlist」才渲染的（异步），得等它出现
await waitFor(
  '字幕下拉',
  async () => Boolean(await evaluate(`!!document.querySelector('select[aria-label="字幕"]')`)),
  30000,
);
const picked = await evaluate(`(() => {
  const sel = document.querySelector('select[aria-label="字幕"]');
  if (!sel) return 'no-select';
  const opt = [...sel.options].find((o) => o.value === '${SUB}');
  if (!opt) return 'no-option';
  sel.value = opt.value;
  sel.dispatchEvent(new Event('change', { bubbles: true }));
  return opt.textContent.trim();
})()`);
console.log(`选字幕：${picked}`);

const ready = await waitFor(
  'libass canvas',
  async () => Boolean(await evaluate(`!!document.querySelector('.libassjs-canvas-parent')`)),
  60000,
);
console.log(`libass canvas：${ready ? '已挂上' : '没出现'}`);

// 跳到第一条对白处（否则可能整屏没字幕可看）
await waitFor('抓到 ASS 请求', async () => subUrls.length > 0, 60000);
const assUrl = subUrls[0];
console.log(`ASS 地址：${assUrl}`);
const lineTime = await evaluate(`(async () => {
  const r = await fetch(${JSON.stringify(assUrl)});
  if (!r.ok) return -1;
  const text = await r.text();
  const m = text.match(/^Dialogue:[^,]*, *(\\d+):(\\d+):([0-9.]+)/m);
  if (!m) return -1;
  return Number(m[1]) * 3600 + Number(m[2]) * 60 + Number(m[3]);
})()`);
console.log(`第一条对白在 ${lineTime}s`);

if (lineTime >= 0) {
  await evaluate(`(() => {
    const v = document.querySelector('.player-video');
    if (v) v.currentTime = ${lineTime + 0.5};
    return true;
  })()`);
}
await sleep(9000);

const state = await evaluate(
  `(() => {
    const v = document.querySelector('.player-video');
    const c = document.querySelector('.libassjs-canvas-parent canvas');
    return {
      currentTime: v ? Math.round(v.currentTime * 10) / 10 : -1,
      paused: v ? v.paused : null,
      canvasSize: c ? c.width + 'x' + c.height : null,
      canvasShown: c ? c.clientWidth + 'x' + c.clientHeight : null,
    };
  })()`,
);
console.log(`播放器状态：${JSON.stringify(state)}`);

const shot = await send('Page.captureScreenshot', { format: 'png' });
writeFileSync(OUT, Buffer.from(shot.data, 'base64'));
console.log(`截图已保存：${OUT}`);

ws.close();
chrome.kill();
