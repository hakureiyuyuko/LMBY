// 直播起播失败的**界面文案**验收：验证「后端给 code → 前端翻成人话」这条链路。
//
// 为什么单独一个脚本：这条链路的后端一半（返回 code）在 verify-livetv-reprobe.sh 里有断言，
// 但「界面真的把人话显示出来」只有真浏览器里才看得到 —— 而这里最容易退化的地方是
// **前端忘了映射、直接把后端文案抛出来**（那就是「拉流失败：等待转封装起步超时：
// 等待第一个分片超过 8s」，用户完全看不懂）。
//
// 怎么造出一个**稳定可复现**的失败：用「本地并发打满」（`livetv_busy`）。
//   · 它不依赖源站（不像「源站挂了」那样一秒钟就被自动重探清掉，播完就没现场了）；
//   · 页面上照样点得动，失败路径与真实起播完全一样。
// 所以：先在页面里用 fetch 连起几路直到拿到 `livetv_busy`，**再在界面上点一台频道**，
// 断言错误浮层里出现的是人话、而不是后端原始串。
//
// 必须在同一次运行里「起浏览器 → 跑断言 → 关浏览器」。
//
// 用法：
//   BASE=http://192.168.x.x:8099 LMBY_USER=devtest LMBY_PASS=口令 node livetv-err-ui-test.mjs
// 可选：CHROME / OUT（截图目录，默认 shots-livetv-err）
import { appendFileSync, mkdirSync, writeFileSync } from 'node:fs';
import { spawn } from 'node:child_process';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';

const REPORT = 'livetv-err-ui-report.txt';
writeFileSync(REPORT, '');
const log = (m) => appendFileSync(REPORT, m + '\n');

const CHROME = process.env.CHROME || 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const BASE = process.env.BASE || 'http://127.0.0.1:8099';
const USER = process.env.LMBY_USER || 'devtest';
const PASS = process.env.LMBY_PASS;
if (!PASS) throw new Error('请通过环境变量提供测试账号口令：LMBY_PASS=xxx（配合 LMBY_USER）');
const OUT = process.env.OUT || 'shots-livetv-err';
const PORT = Number(process.env.CDP_PORT || 9950 + (process.pid % 100));

mkdirSync(OUT, { recursive: true });
const profile = path.join(os.tmpdir(), `lmby-liveerr-${Date.now()}`);

const chrome = spawn(
  CHROME,
  [
    '--headless=new',
    `--remote-debugging-port=${PORT}`,
    `--user-data-dir=${profile}`,
    '--no-first-run',
    '--no-default-browser-check',
    '--disable-extensions',
    '--disable-gpu',
    '--disable-dev-shm-usage',
    '--hide-scrollbars',
    '--mute-audio',
    '--autoplay-policy=no-user-gesture-required',
    '--window-size=1440,1100',
    'about:blank',
  ],
  { stdio: ['ignore', 'ignore', 'ignore'] },
);

let pass = 0;
let fail = 0;
function check(name, expected, actual) {
  // 「期望是子串」的断言用 { contains: '...' } 表示；其它按 JSON 相等比。
  let ok;
  if (expected && typeof expected === 'object' && 'contains' in expected) {
    ok = typeof actual === 'string' && actual.includes(expected.contains);
  } else if (expected && typeof expected === 'object' && 'notContains' in expected) {
    ok = typeof actual === 'string' && !actual.includes(expected.notContains);
  } else {
    ok = JSON.stringify(expected) === JSON.stringify(actual);
  }
  if (ok) {
    pass++;
    log(`ok   ${name}`);
  } else {
    fail++;
    log(`FAIL ${name}（期望 ${JSON.stringify(expected)}，实际 ${JSON.stringify(actual).slice(0, 220)}）`);
  }
}
const note = (m) => log(`--   ${m}`);

async function waitDevtools() {
  for (let i = 0; i < 80; i++) {
    try {
      const r = await fetch(`http://127.0.0.1:${PORT}/json/version`);
      if (r.ok) return await r.json();
    } catch {}
    await sleep(250);
  }
  throw new Error('Chrome 未就绪');
}

let ws;
let id = 0;
const pending = new Map();
const pageErrors = [];
function connect(url) {
  return new Promise((resolve, reject) => {
    ws = new WebSocket(url);
    ws.onopen = () => resolve();
    ws.onerror = () => reject(new Error('CDP 连接失败'));
    ws.onmessage = (ev) => {
      const msg = JSON.parse(ev.data);
      if (msg.id && pending.has(msg.id)) {
        const { resolve: res, reject: rej } = pending.get(msg.id);
        pending.delete(msg.id);
        if (msg.error) rej(new Error(msg.error.message));
        else res(msg.result);
        return;
      }
      if (msg.method === 'Page.javascriptDialogOpening') {
        send('Page.handleJavaScriptDialog', { accept: true }).catch(() => {});
      }
      if (msg.method === 'Runtime.exceptionThrown') {
        const d = msg.params?.exceptionDetails;
        pageErrors.push('异常: ' + (d?.exception?.description || d?.text || '').split('\n')[0]);
      }
    };
  });
}
function send(method, params = {}) {
  const mid = ++id;
  return new Promise((resolve, reject) => {
    pending.set(mid, { resolve, reject });
    ws.send(JSON.stringify({ id: mid, method, params }));
    setTimeout(() => {
      if (pending.has(mid)) {
        pending.delete(mid);
        reject(new Error('CDP 超时 ' + method));
      }
    }, 60000);
  });
}
async function evaluate(expression) {
  const r = await send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
  if (r.exceptionDetails) {
    throw new Error(
      '页面异常: ' + (r.exceptionDetails.exception?.description || r.exceptionDetails.text || ''),
    );
  }
  return r.result.value;
}
async function waitFor(desc, fn, timeoutMs = 30000) {
  const t0 = Date.now();
  while (Date.now() - t0 < timeoutMs) {
    try {
      if (await fn()) return true;
    } catch {}
    await sleep(200);
  }
  log(`超时 ${desc}`);
  return false;
}
async function shot(name) {
  await send('Emulation.setDeviceMetricsOverride', {
    width: 1280,
    height: 900,
    deviceScaleFactor: 1,
    mobile: false,
  });
  const r = await send('Page.captureScreenshot', { format: 'png' });
  writeFileSync(path.join(OUT, `${name}.png`), Buffer.from(r.data, 'base64'));
  await send('Emulation.clearDeviceMetricsOverride');
  log(`截图 ${OUT}/${name}.png`);
}
const setInputIdx = (idx, val) => `(() => {
  const el = document.querySelectorAll('.center-card input')[${idx}];
  if (!el) return null;
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(el, ${JSON.stringify(
    String(val),
  )});
  el.dispatchEvent(new Event('input', { bubbles: true }));
  return el.value;
})()`;
const clickSel = (sel) =>
  `(() => { const e = document.querySelector(${JSON.stringify(sel)}); if (!e) return false; e.click(); return true; })()`;
/** 在页面上下文里发请求（与界面同源同会话），返回 {status, body}。 */
const apiCall = (p, init) =>
  `(async () => {
    const res = await fetch(${JSON.stringify(p)}, Object.assign({ credentials: 'same-origin' }, ${JSON.stringify(
      init || {},
    )}));
    const text = await res.text();
    try { return { status: res.status, body: JSON.parse(text) }; }
    catch { return { status: res.status, body: text.slice(0, 300) }; }
  })()`;

async function login(u, p) {
  await send('Page.navigate', { url: `${BASE}/` });
  await sleep(600);
  await evaluate(
    `(async () => { await fetch('/api/v1/auth/logout', { method: 'POST', credentials: 'same-origin' }); return true; })()`,
  );
  await send('Page.navigate', { url: `${BASE}/login` });
  await waitFor('登录页', async () => (await evaluate('location.pathname')) === '/login');
  const rendered = await waitFor(
    '登录表单',
    async () => (await evaluate(`document.querySelectorAll('.center-card input').length`)) >= 2,
  );
  if (!rendered) return false;
  await evaluate(setInputIdx(0, u));
  await evaluate(setInputIdx(1, p));
  await evaluate(clickSel('.center-card button[type="submit"]'));
  return waitFor('登录后回首页', async () => (await evaluate('location.pathname')) === '/');
}

async function main() {
  const ver = await waitDevtools();
  log(`Chrome ${ver.Browser} → ${BASE}`);
  const tab = await (await fetch(`http://127.0.0.1:${PORT}/json/new?about:blank`, { method: 'PUT' })).json();
  await connect(tab.webSocketDebuggerUrl);
  await send('Page.enable');
  await send('Runtime.enable');
  await send('Page.navigate', { url: `${BASE}/login` });
  await sleep(900);
  await evaluate(`localStorage.setItem('lmby.lang', 'zh-CN')`);
  await send('Page.reload');
  await sleep(900);

  log('\n== 1. 管理员登录 + 进直播页 ==');
  check('登录成功', true, await login(USER, PASS));
  await evaluate(clickSel('.nav a[href="/livetv"]'));
  check(
    '进了直播页',
    true,
    await waitFor('直播页', async () => (await evaluate('location.pathname')) === '/livetv'),
  );
  check(
    '换台栏渲染出来',
    true,
    await waitFor('频道行', async () => (await evaluate(`document.querySelectorAll('.tv-ch').length`)) > 0),
  );

  log('\n== 2. 在页面里把本地并发打满（拿到 livetv_busy 为止） ==');
  // 先清掉可能残留的会话，免得基线不稳
  const listed = await evaluate(apiCall('/api/v1/livetv/sessions'));
  note(`起播前会话数：${JSON.stringify((listed.body && listed.body.sessions && listed.body.sessions.length) ?? '?')}`);
  const ids = await evaluate(
    `(async () => {
      const r = await fetch('/api/v1/livetv/channels?enabled=1&hide_failed=1', { credentials: 'same-origin' });
      const j = await r.json();
      return (j.channels || []).map((c) => ({ id: c.id, name: c.name }));
    })()`,
  );
  check('前台有可点的频道', true, ids.length > 0);

  const started = [];
  let busyHit = null;
  for (const ch of ids.slice(0, 14)) {
    const r = await evaluate(
      apiCall('/api/v1/livetv/channels/' + ch.id + '/play', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({}),
      }),
    );
    if (r.status === 200 && r.body && r.body.sid) {
      started.push({ id: ch.id, sid: r.body.sid });
      note(`起播 #${ch.id} ${ch.name} 成功（当前 ${started.length} 路）`);
      continue;
    }
    if (r.body && r.body.code === 'livetv_busy') {
      busyHit = { ch, status: r.status, body: r.body };
      note(`并发打满：起播 #${ch.id} ${ch.name} → ${r.status} code=${r.body.code}`);
      break;
    }
    note(`起播 #${ch.id} ${ch.name} 失败（不是并发原因，继续试）：${r.status} ${JSON.stringify(r.body).slice(0, 120)}`);
  }
  check('能造出 livetv_busy（本地并发打满）', true, !!busyHit);
  check('打满时会占用若干路真实会话', true, started.length > 0);
  if (busyHit) {
    check('busy 的响应带 code', 'livetv_busy', busyHit.body.code);
    check('busy 的后端文案是中文兜底', { contains: '上限' }, String(busyHit.body.error || ''));
  }

  log('\n== 3. 界面上点一台频道 → 错误浮层必须是**人话** ==');
  if (!busyHit) {
    note('没造出 busy，跳过界面断言（这条会红，因为上面已经 FAIL）');
  } else {
    // 点「刚才被拒的那一台」：它在界面上是可见的（busy 不影响前台可见性）
    const clicked = await evaluate(`(() => {
      const rows = [...document.querySelectorAll('.tv-ch')];
      const row = rows.find((r) => (r.querySelector('.tv-ch-name')?.textContent || '').trim().includes(${JSON.stringify(
        busyHit.ch.name,
      )}));
      if (!row) return false;
      row.querySelector('.tv-ch-main').click();
      return true;
    })()`);
    check('能在换台栏里点到那台频道', true, clicked);
    const shown = await waitFor(
      '错误浮层',
      async () => (await evaluate(`(document.body.innerText || '').includes('路数已达上限')`)),
      15000,
    );
    check('错误浮层显示人话（并发上限）', true, shown);
    const alertText = await evaluate(
      `(() => { const e = document.querySelector('.tv-hint-overlay.tv-hint-err') || [...document.querySelectorAll('.tv-hint-overlay')].find((x) => (x.textContent || '').length > 0); return e ? (e.textContent || '') : ''; })()`,
    );
    log(`  浮层文案：${alertText}`);
    check('显示的是前端词条（人话）', { contains: '先停掉一路再试' }, alertText);
    check('**没有**把后端原始串抛给用户', { notContains: '请稍后再试' }, alertText);
    check('**没有**实现细节黑话', { notContains: '转封装' }, alertText);
    await shot('01-busy-error');
  }

  log('\n== 4. 清理：停掉本次起的会话 ==');
  let stopped = 0;
  for (const s of started) {
    const r = await evaluate(apiCall('/api/v1/live/' + s.sid + '/stop', { method: 'POST' }));
    if (r.status === 200) stopped++;
  }
  check('起的会话都停掉了', started.length, stopped);

  check('没有页面异常', 0, [...new Set(pageErrors)].length);
  log(`\n================ 结果：${pass} 通过 / ${fail} 失败 ================`);
  chrome.kill();
  process.exit(fail === 0 ? 0 : 1);
}

main().catch((e) => {
  log(`运行失败: ${e.message}`);
  log(`================ 结果：${pass} 通过 / ${fail + 1} 失败 ================`);
  chrome.kill();
  process.exit(1);
});
