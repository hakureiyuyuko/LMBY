// M6「收藏」的界面验收（CDP 直连 headless Chrome，不依赖 Puppeteer）
//
// 闭环一条链：详情页点收藏 → 按钮与「N 人收藏」变了、接口也变了 → 首页出现「我的收藏」行
// 且第一条是它 → 再取消收藏 → 两个地方都回到原样。
//
// 期望值都从接口现取（`GET /api/v1/favorites` / `/api/v1/home`），脚本先记下起点、
// 跑完还原，所以它不假设这个账号原本有没有收藏。
//
// 用法：
//   BASE=http://192.168.x.x:8099 LMBY_USER=devtest LMBY_PASS=口令 node favorites-ui-test.mjs
import { appendFileSync, mkdirSync, writeFileSync } from 'node:fs';
import { spawn } from 'node:child_process';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';

const REPORT = 'favorites-ui-report.txt';
writeFileSync(REPORT, '');
const log = (m) => appendFileSync(REPORT, m + '\n');

const CHROME =
  process.env.CHROME ||
  (process.platform === 'win32'
    ? 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe'
    : '/usr/bin/google-chrome');
const BASE = process.env.BASE || 'http://127.0.0.1:8099';
const USER = process.env.LMBY_USER || 'devtest';
const PASS = process.env.LMBY_PASS;
if (!PASS) {
  throw new Error('请通过环境变量提供测试账号口令：LMBY_PASS=xxx（配合 LMBY_USER）');
}
const OUT = process.env.OUT || 'shots-favorites';
const PORT = Number(process.env.CDP_PORT || 9647 + (process.pid % 200));

mkdirSync(OUT, { recursive: true });
const profile = path.join(os.tmpdir(), `lmby-fav-${Date.now()}`);

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
    '--window-size=1400,1000',
    'about:blank',
  ],
  { stdio: ['ignore', 'ignore', 'ignore'] },
);

let pass = 0;
let fail = 0;
function check(name, expected, actual) {
  const ok = JSON.stringify(expected) === JSON.stringify(actual);
  if (ok) {
    pass++;
    log(`ok   ${name}`);
  } else {
    fail++;
    log(`FAIL ${name}（期望 ${JSON.stringify(expected)}，实际 ${JSON.stringify(actual)}）`);
  }
}

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
        return;
      }
      if (msg.method === 'Runtime.exceptionThrown') {
        const d = msg.params.exceptionDetails;
        pageErrors.push('异常: ' + (d.exception?.description || d.text));
        return;
      }
      if (msg.method === 'Log.entryAdded' && msg.params.entry.level === 'error') {
        const e = msg.params.entry;
        pageErrors.push(`[${e.source}] ${e.text} ${e.url || ''}`);
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
    log('出错表达式（前 200 字）：' + expression.slice(0, 200).replace(/\n/g, ' '));
    throw new Error(
      '页面异常: ' + (r.exceptionDetails.exception?.description || r.exceptionDetails.text || ''),
    );
  }
  return r.result.value;
}
async function waitFor(desc, fn, timeoutMs = 25000) {
  const t0 = Date.now();
  while (Date.now() - t0 < timeoutMs) {
    try {
      if (await fn()) return true;
    } catch {}
    await sleep(150);
  }
  log(`超时 ${desc}`);
  return false;
}
async function shot(name) {
  const r = await send('Page.captureScreenshot', { format: 'png' });
  writeFileSync(path.join(OUT, `${name}.png`), Buffer.from(r.data, 'base64'));
  log(`截图 ${OUT}/${name}.png`);
}
const clickSel = (sel) =>
  `(() => { const e = document.querySelector(${JSON.stringify(sel)}); if (!e) return false; e.click(); return true; })()`;
/** 在页面里发一个带 cookie 的请求（断言的期望值直接用后端算，不猜）。 */
const apiFetch = (path, init = '') => `(async () => {
  const r = await fetch(${JSON.stringify(path)}, { credentials: 'same-origin'${init} });
  return await r.json();
})()`;
const setInput = (sel, val) => `(() => {
  const el = document.querySelector(${JSON.stringify(sel)});
  if (!el) return null;
  const proto = el.tagName === 'TEXTAREA' ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  Object.getOwnPropertyDescriptor(proto, 'value').set.call(el, ${JSON.stringify(val)});
  el.dispatchEvent(new Event('input', { bubbles: true }));
  return el.value;
})()`;

async function main() {
  const ver = await waitDevtools();
  log(`Chrome ${ver.Browser} → ${BASE}`);

  const tab = await (
    await fetch(`http://127.0.0.1:${PORT}/json/new?about:blank`, { method: 'PUT' })
  ).json();
  await connect(tab.webSocketDebuggerUrl);
  await send('Page.enable');
  await send('Runtime.enable');
  await send('Log.enable');
  await send('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-color-scheme', value: 'dark' }] });

  log('\n== 1. 登录 ==');
  await send('Page.navigate', { url: `${BASE}/login` });
  await waitFor('登录页', async () => (await evaluate('location.pathname')) === '/login');
  check(
    '登录表单已渲染',
    true,
    await waitFor('登录表单', async () =>
      (await evaluate(`document.querySelectorAll('.center-card input').length`)) >= 2,
    ),
  );
  await evaluate(setInput('.center-card input', USER));
  await evaluate(`(() => {
    const el = document.querySelectorAll('.center-card input')[1];
    const s = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
    s.call(el, ${JSON.stringify(PASS)});
    el.dispatchEvent(new Event('input', { bubbles: true }));
    return true;
  })()`);
  await evaluate(clickSel('.center-card button[type="submit"]'));
  check('登录成功', true, await waitFor('首页', async () => (await evaluate('location.pathname')) === '/'));

  log('\n== 2. 挑一个样本条目并归零 =');
  const sample = await evaluate(`(async () => {
    const libs = await (await fetch('/api/v1/libraries', { credentials: 'same-origin' })).json();
    for (const lib of libs.libraries || []) {
      const page = await (await fetch('/api/v1/libraries/' + lib.id + '/items?kind=movie&limit=10',
        { credentials: 'same-origin' })).json();
      const it = (page.items || [])[0];
      if (it) return { id: it.id, title: it.title };
    }
    return null;
  })()`);
  if (!sample) throw new Error('库里没有电影，收藏界面验收需要至少一部电影');
  log(`   样本：条目 ${sample.id}《${sample.title}》`);

  // 归零：这个账号可能上次跑残留了收藏
  const beforeFav = await evaluate(apiFetch(`/api/v1/items/${sample.id}/favorite`));
  if (beforeFav.favorite) {
    log('   起点是「已收藏」（上次残留），先取消掉');
    await evaluate(
      apiFetch(`/api/v1/items/${sample.id}/favorite`, `, method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ favorite: false })`),
    );
  }
  const homeBefore = await evaluate(apiFetch('/api/v1/home'));
  const homeRowBefore = (homeBefore.sections || []).some((s) => s.key === 'favorites');
  log(`   起点：首页有「我的收藏」行 = ${homeRowBefore}`);

  log('\n== 3. 详情页的收藏按钮 ==');
  await send('Page.navigate', { url: `${BASE}/item/${sample.id}` });
  check(
    '详情页渲染出来了',
    true,
    await waitFor('详情页', async () => await evaluate(`!!document.querySelector('.detail-hero-inner')`)),
  );
  check(
    '详情页有收藏按钮',
    true,
    await waitFor('收藏按钮', async () =>
      await evaluate(`!!document.querySelector('[data-favorite]')`),
    ),
  );
  check(
    '初始状态是未收藏',
    'off',
    await evaluate(`document.querySelector('[data-favorite]')?.dataset.favorite || 'off'`),
  );
  const btnText = await evaluate(`document.querySelector('[data-favorite]')?.textContent?.trim() || ''`);
  log(`   按钮文案：${btnText}`);
  check('按钮文案是「收藏」', true, btnText.includes('收藏') && !btnText.includes('已收藏'));

  await evaluate(clickSel('[data-favorite]'));
  check(
    '点一下变成已收藏',
    true,
    await waitFor('已收藏', async () =>
      (await evaluate(`document.querySelector('[data-favorite]')?.dataset.favorite`)) === 'on',
    ),
  );
  check(
    '按钮文案变成「已收藏」',
    true,
    (await evaluate(`document.querySelector('[data-favorite]')?.textContent || ''`)).includes('已收藏'),
  );
  const favApi = await evaluate(apiFetch(`/api/v1/items/${sample.id}/favorite`));
  check('接口也说是已收藏', true, favApi.favorite);
  check('收藏数 ≥ 1', true, favApi.count >= 1);
  check(
    '页面显示了「N 人收藏」',
    true,
    await waitFor('收藏数', async () =>
      await evaluate(`/\\d+ 人收藏/.test(document.querySelector('.detail-hero-inner')?.textContent || '')`),
    ),
  );
  await shot('01-favorite-on');

  log('\n== 4. 首页出现「我的收藏」行 ==');
  await send('Page.navigate', { url: `${BASE}/` });
  check('回到首页', true, await waitFor('首页', async () => (await evaluate('location.pathname')) === '/'));
  check(
    '首页有「我的收藏」行',
    true,
    await waitFor('我的收藏行', async () =>
      await evaluate(`!!document.querySelector('.row-block[data-row="favorites"]')`),
    ),
  );
  check(
    '该行第一条就是刚收藏的条目',
    `/item/${sample.id}`,
    await evaluate(
      `document.querySelector('.row-block[data-row="favorites"] .poster-card')?.getAttribute('href') || ''`,
    ),
  );
  await shot('02-home-favorites');

  log('\n== 5. 取消收藏后两处都还原 ==');
  await send('Page.navigate', { url: `${BASE}/item/${sample.id}` });
  await waitFor('详情页', async () => await evaluate(`!!document.querySelector('.detail-hero-inner')`));
  await waitFor('收藏按钮', async () => await evaluate(`!!document.querySelector('[data-favorite]')`));
  await evaluate(clickSel('[data-favorite]'));
  check(
    '点一下变回未收藏',
    true,
    await waitFor('未收藏', async () =>
      (await evaluate(`document.querySelector('[data-favorite]')?.dataset.favorite`)) === 'off',
    ),
  );
  const favApi2 = await evaluate(apiFetch(`/api/v1/items/${sample.id}/favorite`));
  check('接口也说是未收藏', false, favApi2.favorite);

  await send('Page.navigate', { url: `${BASE}/` });
  await waitFor('首页', async () => (await evaluate('location.pathname')) === '/');
  const homeAfter = await evaluate(apiFetch('/api/v1/home'));
  const homeRowAfter = (homeAfter.sections || []).some((s) => s.key === 'favorites');
  check('首页「我的收藏」行回到起点状态', homeRowBefore, homeRowAfter);
  check(
    '该条目不在「我的收藏」行里了',
    true,
    await evaluate(`(() => {
      const row = document.querySelector('.row-block[data-row="favorites"]');
      if (!row) return true;
      return ![...row.querySelectorAll('.poster-card')].some((a) => a.getAttribute('href') === '/item/${sample.id}');
    })()`),
  );

  if (pageErrors.length > 0) {
    log(`\n页面 JS 错误（${pageErrors.length} 条，取前 10）：`);
    for (const e of pageErrors.slice(0, 10)) log('  ' + String(e).slice(0, 300));
  } else {
    log('\n页面没有 JS 错误');
  }
  log(`\n结果：${pass} 通过，${fail} 失败`);
  return fail;
}

let code = 1;
try {
  code = await main();
} catch (e) {
  log('运行失败: ' + e.message);
  code = 2;
} finally {
  try {
    ws?.close();
  } catch {}
  chrome.kill();
  await sleep(300);
}
process.exitCode = code;
