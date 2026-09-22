// 发布前的**全站页面巡检**：把每一页真打开、截图，并收集页面异常 / console.error。
//
// 为什么要有它：单个 ui-test 只验「它自己那一页」的交互，谁也不会去点「所有页」——
// 于是发版前没人看得出「某个页面白屏了」「某个入口没了」「某页只剩一行报错」。
// 这个脚本就是发版前的那一眼：**逐页截图 + 每页的异常记录 + 顶部结构清单**。
//
// 它不做业务断言（那是各页 ui-test 的活），只回答三个问题：
//   1. 页面有没有白屏 / 报错 / 只显示错误文案？
//   2. 入口在不在（顶栏、设置页签、首页各行的更多链接）？
//   3. 普通用户看到的东西是不是管理员可见的那些（多用户下的入口收敛）？
//
// 用法：
//   BASE=http://192.168.x.x:8099 LMBY_USER=devtest LMBY_PASS=口令 node pages-audit.mjs
// 可选：CHROME / OUT（截图目录，默认 shots-audit）/ SKIP_USER=1（跳过普通用户视角）
import { appendFileSync, mkdirSync, writeFileSync } from 'node:fs';
import { spawn } from 'node:child_process';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';

const REPORT = 'pages-audit-report.txt';
writeFileSync(REPORT, '');
const log = (m) => appendFileSync(REPORT, m + '\n');

const CHROME = process.env.CHROME || 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const BASE = process.env.BASE || 'http://127.0.0.1:8099';
const USER = process.env.LMBY_USER || 'devtest';
const PASS = process.env.LMBY_PASS;
if (!PASS) throw new Error('请通过环境变量提供测试账号口令：LMBY_PASS=xxx（配合 LMBY_USER）');
const OUT = process.env.OUT || 'shots-audit';
const SKIP_USER = process.env.SKIP_USER === '1';
const PORT = Number(process.env.CDP_PORT || 9700 + (process.pid % 200));

mkdirSync(OUT, { recursive: true });
const profile = path.join(os.tmpdir(), `lmby-audit-${Date.now()}`);

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
    '--window-size=1440,1000',
    'about:blank',
  ],
  { stdio: ['ignore', 'ignore', 'ignore'] },
);

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
let pageErrors = [];
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
      if (msg.method === 'Runtime.consoleAPICalled' && msg.params?.type === 'error') {
        pageErrors.push(
          'console.error: ' + (msg.params.args || []).map((a) => a.value ?? a.description ?? '').join(' ').slice(0, 160),
        );
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
    throw new Error('页面异常: ' + (r.exceptionDetails.exception?.description || r.exceptionDetails.text || ''));
  }
  return r.result.value;
}
async function waitFor(desc, fn, timeoutMs = 25000) {
  const t0 = Date.now();
  while (Date.now() - t0 < timeoutMs) {
    try {
      if (await fn()) return true;
    } catch {}
    await sleep(200);
  }
  log(`    !! 超时：${desc}`);
  return false;
}
async function shot(name, full = false) {
  // 默认只截视口（快，够看首屏与顶栏/入口）；长页面（首页/库浏览/海报墙）要全屏看整版布局。
  await send('Emulation.setDeviceMetricsOverride', {
    width: 1440,
    height: 1000,
    deviceScaleFactor: 0.5,
    mobile: false,
  });
  const r = await send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: full });
  writeFileSync(path.join(OUT, `${name}.png`), Buffer.from(r.data, 'base64'));
  await send('Emulation.clearDeviceMetricsOverride');
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
  if (!(await waitFor('登录表单', async () => (await evaluate(`document.querySelectorAll('.center-card input').length`)) >= 2))) {
    return false;
  }
  await evaluate(setInputIdx(0, u));
  await evaluate(setInputIdx(1, p));
  await evaluate(clickSel('.center-card button[type="submit"]'));
  return waitFor('登录后回首页', async () => (await evaluate('location.pathname')) === '/');
}

/** 打开一页：等 pathname、等正文渲染出来，再截图 + 记录现场。 */
async function visit(name, url, waitExpr, full = false) {
  pageErrors = [];
  await send('Page.navigate', { url: `${BASE}${url}` });
  // 用「正文有内容」当加载完成的信号：比固定 sleep 稳，也比等具体类名通用
  await waitFor(`正文渲染 ${url}`, async () => {
    const t = await evaluate(`(document.body.innerText || '').trim().length`);
    return t > 120;
  });
  if (waitExpr) {
    await waitFor(`期望元素 ${waitExpr}`, async () =>
      await evaluate(`!!document.querySelector(${JSON.stringify(waitExpr)})`),
    );
  }
  await sleep(450); // 让图片/图表停一下再截
  const info = await evaluate(
    `(() => ({ path: location.pathname + location.search, text: (document.body.innerText || '').replace(/\\s+/g, ' ').slice(0, 300) }))()`,
  );
  await shot(name, full);
  const errs = [...new Set(pageErrors)];
  log(`\n[${name}] ${info.path}`);
  log(`   正文：${info.text.slice(0, 220)}`);
  if (errs.length) log(`   ⚠️ 页面异常：${JSON.stringify(errs.slice(0, 4))}`);
  else log('   页面异常：无');
  return { info, errs };
}

async function main() {
  const ver = await waitDevtools();
  log(`Chrome ${ver.Browser} → ${BASE}（截图为整页 ×0.5 缩放，目录 ${OUT}/）`);
  const tab = await (await fetch(`http://127.0.0.1:${PORT}/json/new?about:blank`, { method: 'PUT' })).json();
  await connect(tab.webSocketDebuggerUrl);
  await send('Page.enable');
  await send('Runtime.enable');
  await send('Page.navigate', { url: `${BASE}/login` });
  await sleep(900);
  await evaluate(`localStorage.setItem('lmby.lang', 'zh-CN')`);
  await send('Page.reload');
  await sleep(900);

  log('\n########## 一、管理员视角 ##########');
  log(`登录 ${USER}：${await login(USER, PASS)}`);

  // 拿点真实数据当样本
  const libs = await evaluate(apiCall('/api/v1/libraries'));
  const libsList = (libs.body && libs.body.libraries) || [];
  const lib = libsList[0] || {};
  const items = await evaluate(apiCall(`/api/v1/libraries/${lib.id}/items?limit=100`));
  const itemList = (items.body && items.body.items) || [];
  const movie = itemList.find((i) => i.kind === 'movie') || itemList[0] || {};
  const series = itemList.find((i) => i.kind === 'series') || {};
  const q = encodeURIComponent(String(movie.title || '的').slice(0, 6));
  log(`样本：库 #${lib.id}「${lib.name}」、条目 #${movie.id}「${movie.title}」、剧集 #${series.id || '-'}`);

  const problems = [];
  const record = async (name, r) => {
    if (r.errs.length) problems.push(`${name}: ${r.errs[0]}`);
    if (/读取.*失败|加载失败|出错了|Error/.test(r.info.text) && !/失败重试|暂无/.test(r.info.text)) {
      // 正文里出现「失败」多半是页面级错误（比如「读取首页失败」）——记下来给人看
      problems.push(`${name}: 正文里出现「失败」字样 → ${r.info.text.slice(0, 120)}`);
    }
  };

  await record('01-首页', await visit('01-首页', '/', '.row-block', true));
  await record('02-直播页', await visit('02-直播页', '/livetv', '.tv-side'));
  await record('03-搜索', await visit('03-搜索', `/search?q=${q}`, '.card'));
  await record('04-海报墙', await visit('04-海报墙', '/posters', null, true));
  await record('05-库浏览', await visit('05-库浏览', `/library/${lib.id}`, null, true));
  await record('06-条目详情-电影', await visit('06-条目详情-电影', `/item/${movie.id}`, null, true));
  if (series.id) {
    await record('07-条目详情-剧集', await visit('07-条目详情-剧集', `/item/${series.id}`, null, true));
  }
  await record('08-我的列表', await visit('08-我的列表', '/lists', null));
  await record('09-个人中心', await visit('09-个人中心', '/account', null));
  await record('10-设置', await visit('10-设置', '/settings', '[data-settings-tabs]'));
  await record('11-设置-库管理', await visit('11-设置-库管理', '/settings/libraries', null));
  await record('12-设置-人工匹配', await visit('12-设置-人工匹配', '/settings/match', null));
  await record('13-设置-会话', await visit('13-设置-会话', '/settings/sessions', null));
  await record('14-设置-直播源', await visit('14-设置-直播源', '/settings/livetv', null, true));
  await record('15-设置-用户', await visit('15-设置-用户', '/settings/users', '.user-list'));
  if (movie.id) {
    await record('16-播放器', await visit('16-播放器', `/play/${movie.id}`, '.player-shell, video'));
  }
  await record('17-表单页-登录（已登录时应重定向回首页）', await visit('17-登录页', '/login', null));

  // 入口清单：顶栏 + 设置页签 + 首页每行的「更多」
  await send('Page.navigate', { url: `${BASE}/` });
  await sleep(1500);
  const navs = await evaluate(`[...document.querySelectorAll('.nav a')].map((a) => a.textContent.trim() + ' → ' + a.getAttribute('href'))`);
  log(`\n顶栏入口：${navs.join(' | ')}`);
  await send('Page.navigate', { url: `${BASE}/settings` });
  await sleep(1200);
  const tabs = await evaluate(`[...document.querySelectorAll('[data-settings-tabs] a')].map((a) => a.textContent.trim() + ' → ' + a.getAttribute('href'))`);
  log(`设置页签：${tabs.join(' | ')}`);
  await send('Page.navigate', { url: `${BASE}/` });
  await sleep(1500);
  const rows = await evaluate(
    `[...document.querySelectorAll('.row-block')].map((r) => (r.querySelector('.row-head')?.textContent || r.textContent || '').replace(/\\s+/g, ' ').trim().slice(0, 60))`,
  );
  log(`首页各行：${rows.join(' || ')}`);

  if (!SKIP_USER) {
    log('\n########## 二、普通用户视角（入口是否收敛）##########');
    const suffix = String(Date.now()).slice(-6);
    const UNAME = `audit${suffix}`;
    const UPASS = `audit-pass-${suffix}`;
    const created = await evaluate(
      apiCall('/api/v1/users', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username: UNAME, password: UPASS, displayName: `巡检 ${suffix}` }),
      }),
    );
    const uid = created.body && created.body.user && created.body.user.id;
    log(`临时普通用户：${UNAME}（id=${uid}）`);
    if (uid) {
      log(`登录普通用户：${await login(UNAME, UPASS)}`);
      await record('18-普通用户-首页', await visit('18-普通用户-首页', '/', '.row-block'));
      const unav = await evaluate(`[...document.querySelectorAll('.nav a')].map((a) => a.textContent.trim() + ' → ' + a.getAttribute('href'))`);
      log(`普通用户顶栏入口：${unav.join(' | ')}`);
      await record('19-普通用户-直接访问设置', await visit('19-普通用户-设置', '/settings', null));
      await record('20-普通用户-直接访问用户页', await visit('20-普通用户-用户页', '/settings/users', null));
      await login(USER, PASS);
      await evaluate(apiCall(`/api/v1/users/${uid}`, { method: 'DELETE' }));
      log('已删除临时普通用户');
    }
  }

  log('\n########## 巡检结论 ##########');
  if (problems.length === 0) log('没有发现「页面级」问题（页面异常 / 正文报错）。');
  else problems.forEach((p) => log(`⚠️ ${p}`));
  log(`截图目录：${OUT}/`);
  log(`\n================ 巡检完成 ================`);
  chrome.kill();
  process.exit(problems.length === 0 ? 0 : 1);
}

main().catch((e) => {
  log(`运行失败: ${e.message}`);
  chrome.kill();
  process.exit(1);
});
