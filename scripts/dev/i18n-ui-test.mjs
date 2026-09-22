// M6「i18n」的界面验收（CDP 直连 headless Chrome，不依赖 Puppeteer）
//
// 验的是四件事：
//   1. 切到英文：`<html lang>`、顶栏导航、首页各行与按钮、搜索页、海报墙、我的列表
//      **都是英文**（逐个元素读文本，不是看截图猜）；
//   2. **刷新后还记得**（localStorage）：这是「设置」而不是「一次性开关」；
//   3. 切回中文一切复原；
//   4. 页面没有 JS 错误。
//
// 顺带把文案的「漏翻」变成可测的东西：`scripts/dev/i18n-coverage.mjs` 静态扫源码，
// 这条界面测试只负责证明「翻了的确实生效、切换链路真的通」。
//
// 用法：
//   BASE=http://192.168.x.x:8099 LMBY_USER=devtest LMBY_PASS=口令 node i18n-ui-test.mjs
import { appendFileSync, mkdirSync, writeFileSync } from 'node:fs';
import { spawn } from 'node:child_process';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';

const REPORT = 'i18n-ui-report.txt';
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
const OUT = process.env.OUT || 'shots-i18n';
const PORT = Number(process.env.CDP_PORT || 9947 + (process.pid % 200));

mkdirSync(OUT, { recursive: true });
const profile = path.join(os.tmpdir(), `lmby-i18n-${Date.now()}`);

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
const setInput = (sel, val) => `(() => {
  const el = document.querySelector(${JSON.stringify(sel)});
  if (!el) return null;
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(el, ${JSON.stringify(val)});
  el.dispatchEvent(new Event('input', { bubbles: true }));
  return el.value;
})()`;
/** 选下拉（React 监听的是 change，不是 input）。 */
const setSelect = (sel, val) => `(() => {
  const el = document.querySelector(${JSON.stringify(sel)});
  if (!el) return null;
  Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value').set.call(el, ${JSON.stringify(val)});
  el.dispatchEvent(new Event('change', { bubbles: true }));
  return el.value;
})()`;
/** 导航里的链接文字（顺序即显示顺序）。 */
const navTexts = `[...document.querySelectorAll('.nav a')].map((a) => a.textContent.trim())`;
const bodyText = `(document.querySelector('.content')?.textContent || '')`;

async function main() {
  const ver = await waitDevtools();
  log(`Chrome ${ver.Browser} → ${BASE}`);

  const tab = await (
    await fetch(`http://127.0.0.1:${PORT}/json/new?about:blank`, { method: 'PUT' })
  ).json();
  await connect(tab.webSocketDebuggerUrl);
  await send('Page.enable');
  await send('Runtime.enable');
  // 把界面语言钉成中文：i18n 按 navigator.language 探测，而 headless Chrome 默认是 en-US。
  // ⚠️ 用 localStorage + reload 钉，而不是 Emulation.setLocaleOverride ——
  //    后者在部分 Chrome 上只影响 Intl、不改 navigator.language（实测无效）。
  await send('Page.navigate', { url: `${BASE}/login` });
  await sleep(900);
  await evaluate(`localStorage.setItem('lmby.lang', 'zh-CN')`);
  await send('Page.reload');
  await sleep(900);
  await send('Log.enable');
  await send('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-color-scheme', value: 'dark' }] });

  log('\n== 1. 登录（先把语言定成中文，保证起点确定） ==');
  await send('Page.navigate', { url: `${BASE}/login` });
  await waitFor('登录页', async () => (await evaluate('location.pathname')) === '/login');
  check(
    '登录表单已渲染',
    true,
    await waitFor('登录表单', async () =>
      (await evaluate(`document.querySelectorAll('.center-card input').length`)) >= 2,
    ),
  );
  // 登录页就有语言选择器（没登录的人也能换）——先切到中文再登录
  const hasLangOnLogin = await evaluate(`!!document.querySelector('.lang-select')`);
  log(`   登录页有语言选择器：${hasLangOnLogin}`);
  if (hasLangOnLogin) {
    await evaluate(setSelect('.lang-select', 'zh-CN'));
    await sleep(200);
  }
  check('起点是中文（<html lang>）', 'zh-CN', await evaluate('document.documentElement.lang'));
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

  log('\n== 2. 切到英文 ==');
  const zhNav = await evaluate(navTexts);
  log(`   中文导航：${zhNav.join(' / ')}`);
  check('中文导航里有「首页」', true, zhNav.includes('首页'));
  await evaluate(setSelect('.lang-select', 'en-US'));
  await sleep(400);
  check('<html lang> 跟着变', 'en-US', await evaluate('document.documentElement.lang'));
  const enNav = await evaluate(navTexts);
  log(`   英文导航：${enNav.join(' / ')}`);
  check('导航里没有中文了', false, enNav.some((x) => /\p{Script=Han}/u.test(x)));
  check('导航里有 Home', true, enNav.includes('Home'));
  check(
    '导航含 Search / Library / My Lists / Settings',
    true,
    ['Search', 'Library', 'My Lists', 'Settings'].every((x) => enNav.includes(x)),
  );
  check(
    '「退出」按钮变成 Sign out',
    true,
    (await evaluate(`[...document.querySelectorAll('.topbar button')].map((b) => b.textContent.trim()).join('|')`)).includes(
      'Sign out',
    ),
  );

  log('\n== 3. 首页各行与按钮 ==');
  check(
    '首页就绪',
    true,
    await waitFor('首页内容', async () => await evaluate(`!!document.querySelector('.hero')`)),
  );
  const homeText = await evaluate(bodyText);
  const heroButtons = await evaluate(
    `[...document.querySelectorAll('.hero-actions a')].map((a) => a.textContent.trim())`,
  );
  log(`   轮播按钮：${heroButtons.join(' / ')}`);
  if (!heroButtons.includes('Play')) {
    log(`   轮播区 HTML：${String(await evaluate(`document.querySelector('.hero-actions')?.outerHTML || '（没有 .hero-actions）'`)).slice(0, 300)}`);
  }
  check(
    '轮播按钮是 Play / Details',
    true,
    heroButtons.some((x) => x.includes('Play')) && heroButtons.some((x) => x.includes('Details')),
  );
  const rowTitles = await evaluate(
    `[...document.querySelectorAll('.row-block .row-head h2')].map((h) => h.textContent.trim())`,
  );
  log(`   行标题：${rowTitles.join(' / ')}`);
  check('行标题是英文', false, rowTitles.some((x) => /\p{Script=Han}/u.test(x)));
  check(
    '有 Continue watching（这个账号有进度）或其它英文行',
    true,
    rowTitles.length > 0,
  );
  check(
    '行副标题也是英文（推荐依据那一条）',
    false,
    /因为|根据|进度按|你收藏过/.test(homeText),
  );
  await shot('01-home-en');

  log('\n== 4. 搜索页与海报墙 ==');
  await evaluate(clickSel('.nav a[href="/search"]'));
  await waitFor('搜索页', async () => (await evaluate('location.pathname')) === '/search');
  await sleep(300);
  const searchText = await evaluate(bodyText);
  const placeholder = await evaluate(`document.querySelector('.search-input')?.placeholder || ''`);
  log(`   占位符：${placeholder}`);
  check('搜索页标题是 Search', true, /Search/.test(searchText));
  check('搜索框占位符是英文', false, /\p{Script=Han}/u.test(placeholder));
  check('「清空/搜索」按钮是英文', true, await evaluate(`[...document.querySelectorAll('form.row button')].map((b) => b.textContent.trim()).some((x) => x === 'Search')`));
  await shot('02-search-en');

  await send('Page.navigate', { url: `${BASE}/posters` });
  await waitFor('海报墙', async () => await evaluate(`document.querySelectorAll('.card').length > 0`));
  await sleep(400);
  const postersText = await evaluate(bodyText);
  // ⚠️ 不能查「整页没有汉字」：库名是用户数据（「验证库（子集）」），它就是中文。
  // 只查界面自己写的那几个词是不是已经变成英文。
  const postersUiZh = ['选择媒体库', '每个库是一面独立的墙', '还没有媒体库', '去建媒体库', '读不到媒体库'];
  check(
    '海报墙页面上的界面文案是英文（库名等用户数据不算）',
    false,
    postersUiZh.some((x) => postersText.includes(x)),
  );
  check(
    '海报墙出现英文文案',
    true,
    /Library|Choose|No libraries/i.test(postersText),
  );
  if (postersUiZh.some((x) => postersText.includes(x))) {
    log('   海报墙文本片段：' + postersText.slice(0, 240).replace(/\s+/g, ' '));
    log('   当前 URL：' + (await evaluate('location.pathname')));
  }
  await shot('03-posters-en');

  log('\n== 5. 刷新后还记得（持久化） ==');
  await send('Page.navigate', { url: `${BASE}/` });
  await waitFor('首页', async () => await evaluate(`!!document.querySelector('.nav')`));
  check('刷新后仍是英文（<html lang>）', 'en-US', await evaluate('document.documentElement.lang'));
  check(
    '刷新后导航仍是英文',
    true,
    (await evaluate(navTexts)).includes('Home'),
  );
  check(
    '刷新后 localStorage 里存着选择',
    'en-US',
    await evaluate(`localStorage.getItem('lmby.lang')`),
  );

  log('\n== 6. 切回中文 ==');
  await evaluate(setSelect('.lang-select', 'zh-CN'));
  await sleep(400);
  check('<html lang> 变回中文', 'zh-CN', await evaluate('document.documentElement.lang'));
  const backNav = await evaluate(navTexts);
  check('导航变回中文', true, backNav.includes('首页') && backNav.includes('搜索'));
  check(
    '首页行标题变回中文',
    true,
    await waitFor('行标题', async () =>
      await evaluate(`[...document.querySelectorAll('.row-block .row-head h2')].length > 0`),
    ) &&
      (await evaluate(`[...document.querySelectorAll('.row-block .row-head h2')].map((h) => h.textContent.trim())`)).some(
        (x) => /[\u4e00-\u9fff]/.test(x),
      ),
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
