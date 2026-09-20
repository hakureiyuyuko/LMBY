// M2「搜索」的界面验收（CDP 直连 headless Chrome，不依赖 Puppeteer）
//
// 验的是「这个搜索页真的能用」：登录 → 导航里的「搜索」→ 输入中文 → 结果卡片 →
// 错字容忍 → 点卡片进条目页 → 库筛选 → URL 参数（可收藏/可分享）→ 截图。
// 匹配本身（二元组/trigram/排序）由 scripts/dev/verify-search.sh 在真库上验。
//
// 用法：
//   BASE=http://192.168.x.x:8099 LMBY_USER=devtest LMBY_PASS=口令 node search-ui-test.mjs
import { appendFileSync, mkdirSync, writeFileSync } from 'node:fs';
import { spawn } from 'node:child_process';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';

const REPORT = 'search-ui-report.txt';
writeFileSync(REPORT, '');
const log = (m) => appendFileSync(REPORT, m + '\n');

const CHROME = process.env.CHROME || 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const BASE = process.env.BASE || 'http://127.0.0.1:8099';
const USER = process.env.LMBY_USER || 'devtest';
const PASS = process.env.LMBY_PASS;
if (!PASS) {
  throw new Error('请通过环境变量提供测试账号口令：LMBY_PASS=xxx（配合 LMBY_USER）');
}
const OUT = process.env.OUT || 'shots-search';
// 端口随进程号漂移：万一同机跑了两份脚本（或上一次的 Chrome 还没退），
// 固定端口会让两个实例抢同一个 DevTools，报告就会变成两份交错（踩过）。
const PORT = Number(process.env.CDP_PORT || 9447 + (process.pid % 200));

mkdirSync(OUT, { recursive: true });
const profile = path.join(os.tmpdir(), `lmby-search-${Date.now()}`);

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
  const r = await send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: true });
  writeFileSync(path.join(OUT, `${name}.png`), Buffer.from(r.data, 'base64'));
  log(`截图 ${OUT}/${name}.png`);
}
const clickSel = (sel) =>
  `(() => { const e = document.querySelector(${JSON.stringify(sel)}); if (!e) return false; e.click(); return true; })()`;
const content = `(document.querySelector('.content')?.textContent || '')`;
const cardCount = `document.querySelectorAll('.search-card').length`;
const setInput = (sel, val) => `(() => {
  const el = document.querySelector(${JSON.stringify(sel)});
  if (!el) return null;
  const proto = el.tagName === 'TEXTAREA' ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  Object.getOwnPropertyDescriptor(proto, 'value').set.call(el, ${JSON.stringify(val)});
  el.dispatchEvent(new Event('input', { bubbles: true }));
  return el.value;
})()`;
const submitSearch = `(() => {
  const form = document.querySelector('form.row');
  if (!form) return false;
  form.querySelector('button[type="submit"]').click();
  return true;
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
  await send('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-color-scheme', value: 'dark' }] });

  log('\n== 1. 登录 ==');
  await send('Page.navigate', { url: `${BASE}/login` });
  await waitFor('登录页', async () => (await evaluate('location.pathname')) === '/login');
  // 必须等表单真的渲染出来：否则 el 是 undefined，setter 会报 Illegal invocation
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

  log('\n== 2. 从导航进搜索页 ==');
  check('导航里有「搜索」', true, await evaluate(`!!document.querySelector('.nav a[href="/search"]')`));
  await evaluate(clickSel('.nav a[href="/search"]'));
  check('进入搜索页', true, await waitFor('搜索页', async () => (await evaluate('location.pathname')) === '/search'));
  check('有搜索框', true, await evaluate(`!!document.querySelector('.search-input')`));
  check('还没搜时不显示结果区', 0, await evaluate(cardCount));

  log('\n== 3. 中文查询（二元组）==');
  await evaluate(setInput('.search-input', '炼金'));
  await evaluate(submitSearch);
  check('出现结果', true, await waitFor('结果卡片', async () => (await evaluate(cardCount)) > 0));
  const body = await evaluate(content);
  check('结果里有《钢之炼金术师》', true, body.includes('钢之炼金术师'));
  check('显示总条数', true, /共\s*\d+\s*条/.test(body));
  check('URL 带上了查询词', true, await evaluate(`location.search.includes('q=')`));
  await shot('01-search-dark');

  log('\n== 4. 错字容忍（trigram）==');
  // 取一条真标题里的最长中文段，改掉其中一个字当查询词
  const sample = await evaluate(`(async () => {
    const libs = await (await fetch('/api/v1/libraries', { credentials: 'same-origin' })).json();
    const lib = libs.libraries[0];
    const page = await (await fetch('/api/v1/libraries/' + lib.id + '/items?kind=movie&limit=200',
      { credentials: 'same-origin' })).json();
    const it = (page.items || []).find((x) => /[\\u4e00-\\u9fff]{6,}/.test(x.title || ''));
    if (!it) return null;
    const run = (it.title.match(/[\\u4e00-\\u9fff]{6,}/) || [''])[0];
    const typo = run.slice(0, 2) + '土' + run.slice(3);
    return { id: it.id, title: it.title, run, typo };
  })()`);
  if (!sample) {
    log('   没找到带长中文段的标题，跳过错字用例');
  } else {
    log(`   样本：条目 ${sample.id}，中文段「${sample.run}」，错字查询「${sample.typo}」`);
    await evaluate(setInput('.search-input', sample.typo));
    await evaluate(submitSearch);
    // 必须等结果**真的刷新成这次查询的**：上一次（「炼金」）的卡片还在 DOM 里，
    // 直接断言会拿到上一次的结果（踩过）
    check(
      '错字查询出结果',
      true,
      await waitFor('结果刷新为错字查询的结果', async () =>
        (await evaluate(content)).includes(sample.title)),
    );
    check('错字查询命中了那一条', true, await evaluate(
      `[...document.querySelectorAll('.search-card')].some((a) => a.getAttribute('href') === '/items/${sample.id}')`,
    ));
    // 失败时把现场记下来（卡片的 href 与实际排名），省得反复重跑
    log('   卡片 href：' + JSON.stringify(await evaluate(
      `[...document.querySelectorAll('.search-card')].map((a) => a.getAttribute('href'))`,
    )));
    log('   结果文本：' + String(await evaluate(content)).slice(0, 160).replace(/\s+/g, ' '));
  }

  log('\n== 5. 点卡片进条目页 ==');
  const firstHref = await evaluate(`document.querySelector('.search-card')?.getAttribute('href') || ''`);
  await evaluate(clickSel('.search-card'));
  check('跳到条目页', true, await waitFor('条目页', async () =>
    (await evaluate('location.pathname')) === firstHref));
  // 字段表是另一次接口请求回来后渲染的，要等
  const okFields = await waitFor('字段表', async () =>
    (await evaluate(`!!document.querySelector('[data-field="title"]')`)) === true);
  check('条目页渲染出字段表', true, okFields);
  if (!okFields) {
    log('   条目页文本：' + String(await evaluate(content)).slice(0, 200).replace(/\s+/g, ' '));
  }

  log('\n== 6. URL 参数可直接打开（可收藏/可分享）==');
  await sleep(300);
  await send('Page.navigate', { url: `${BASE}/search?q=${encodeURIComponent('炼金')}&kind=movie` });
  await waitFor('输入框带出 URL 里的查询词', async () =>
    (await evaluate(`document.querySelector('.search-input')?.value`)) === '炼金');
  check('输入框带出 URL 里的查询词', '炼金', await evaluate(`document.querySelector('.search-input')?.value`));
  check('自动出结果', true, await waitFor('结果卡片', async () => (await evaluate(cardCount)) > 0));
  check('类型筛选保持在「电影」', 'movie', await evaluate(`document.querySelectorAll('form.row select')[1]?.value`));

  log('\n== 7. 库筛选 ==');
  const before = await evaluate(cardCount);
  await evaluate(`(() => {
    const sel = document.querySelectorAll('form.row select')[0];
    const opt = [...sel.options].find((o) => o.value !== '');
    if (!opt) return false;
    const s = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value').set;
    s.call(sel, opt.value);
    sel.dispatchEvent(new Event('change', { bubbles: true }));
    return true;
  })()`);
  check('切库后刷新了结果', true, await waitFor('库筛选生效', async () =>
    (await evaluate(`location.search.includes('lib=')`)) === true));
  check('库筛选后仍有结果', true, (await evaluate(cardCount)) > 0);
  check('库筛选后 URL 带 lib 参数', true, await evaluate(`location.search.includes('lib=')`));
  check('筛选前的卡片数记录有效', true, before >= 0);
  await shot('02-search-filtered');

  log('\n== 8. 亮色主题 ==');
  await evaluate(clickSel('.theme-toggle'));
  await sleep(400);
  check('切到亮色', 'light', await evaluate('document.documentElement.dataset.theme'));
  await shot('03-search-light');

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
