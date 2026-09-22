// 详情页（M6）界面验收：海报墙进详情、元数据、演职员、季集、相关推荐、多版本、编辑入口。
//
// 必须在同一次运行里「起浏览器 → 跑断言 → 关浏览器」，否则后台 Chrome 会被回收。
//
// 用法：
//   BASE=http://192.168.x.x:8099 LMBY_USER=devtest LMBY_PASS=口令 node detail-ui-test.mjs
// 可选：
//   CHROME              Chrome 路径
//   OUT                 截图目录（默认 shots-detail）
//   MULTI_VERSION_ITEM  一个**有两份文件**的条目 id（用 verify-detail.sh 造出来），
//                       给了就验版本选择器
import { appendFileSync, mkdirSync, writeFileSync } from 'node:fs';
import { spawn } from 'node:child_process';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';

const REPORT = 'detail-ui-report.txt';
writeFileSync(REPORT, '');
const log = (m) => appendFileSync(REPORT, m + '\n');

const CHROME = process.env.CHROME || 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const BASE = process.env.BASE || 'http://127.0.0.1:8099';
const USER = process.env.LMBY_USER || 'devtest';
const PASS = process.env.LMBY_PASS;
const MULTI = Number(process.env.MULTI_VERSION_ITEM || 0);
if (!PASS) {
  throw new Error('请通过环境变量提供测试账号口令：LMBY_PASS=xxx（配合 LMBY_USER）');
}
const OUT = process.env.OUT || 'shots-detail';
const PORT = Number(process.env.CDP_PORT || 9800 + (process.pid % 150));

mkdirSync(OUT, { recursive: true });
const profile = path.join(os.tmpdir(), `lmby-detail-${Date.now()}`);

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
    '--window-size=1440,1200',
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
function note(m) {
  log(`--   ${m}`);
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
      if (msg.method === 'Runtime.exceptionThrown') {
        const d = msg.params?.exceptionDetails;
        pageErrors.push('异常: ' + (d?.exception?.description || d?.text || '').split('\n')[0]);
      }
      if (msg.method === 'Runtime.consoleAPICalled' && msg.params?.type === 'error') {
        pageErrors.push(
          'console.error: ' + (msg.params.args || []).map((a) => a.value ?? a.description ?? '').join(' ').slice(0, 200),
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
    log('出错表达式（前 200 字）：' + expression.slice(0, 200).replace(/\n/g, ' '));
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
  await evaluate('window.scrollTo(0, 0)');
  await sleep(250);
  await send('Emulation.setDeviceMetricsOverride', { width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
  const r = await send('Page.captureScreenshot', { format: 'png' });
  writeFileSync(path.join(OUT, `${name}.png`), Buffer.from(r.data, 'base64'));
  await send('Emulation.clearDeviceMetricsOverride');
  log(`截图 ${OUT}/${name}.png`);
}
async function dumpPage(tag) {
  const url = await evaluate('location.href');
  const text = await evaluate(`(document.body.innerText || '').slice(0, 300).replace(/\\s+/g, ' | ')`);
  log(`现场[${tag}] url=${url}`);
  log(`  正文: ${text}`);
  if (pageErrors.length > 0) log(`  页面错误: ${JSON.stringify([...new Set(pageErrors)].slice(0, 5))}`);
}
const setInputIdx = (idx, val) => `(() => {
  const el = document.querySelectorAll('.center-card input')[${idx}];
  if (!el) return null;
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(el, ${JSON.stringify(String(val))});
  el.dispatchEvent(new Event('input', { bubbles: true }));
  return el.value;
})()`;
const clickSel = (sel) =>
  `(() => { const e = document.querySelector(${JSON.stringify(sel)}); if (!e) return false; e.click(); return true; })()`;
const apiCall = (p, init) =>
  `(async () => {
    const res = await fetch(${JSON.stringify(p)}, Object.assign({ credentials: 'same-origin' }, ${JSON.stringify(init || {})}));
    const text = await res.text();
    try { return { status: res.status, body: JSON.parse(text) }; }
    catch { return { status: res.status, body: text.slice(0, 200) }; }
  })()`;

async function login(u, p) {
  await send('Page.navigate', { url: `${BASE}/login` });
  await waitFor('登录页', async () => (await evaluate('location.pathname')) === '/login');
  const ok = await waitFor('登录表单', async () => (await evaluate(`document.querySelectorAll('.center-card input').length`)) >= 2);
  if (!ok) return false;
  await evaluate(setInputIdx(0, u));
  await evaluate(setInputIdx(1, p));
  await evaluate(clickSel('.center-card button[type="submit"]'));
  return waitFor('回首页', async () => (await evaluate('location.pathname')) === '/');
}

/** 找一个真带演职员的条目（API 层，最多看 12 条）。 */
async function findItemWithPeople() {
  return await evaluate(`(async () => {
    const libs = (await (await fetch('/api/v1/libraries')).json()).libraries || [];
    if (!libs.length) return null;
    const lib = libs[0].id;
    const ids = [];
    for (const kind of ['movie', 'series']) {
      const b = await (await fetch('/api/v1/libraries/' + lib + '/browse?kind=' + kind + '&limit=20')).json();
      for (const it of b.items || []) ids.push(it.id);
    }
    for (const id of ids) {
      const r = await (await fetch('/api/v1/items/' + id + '/people')).json();
      if ((r.people || []).length > 0) return { id, count: r.people.length, first: r.people[0].name };
    }
    return null;
  })()`);
}

async function main() {
  const ver = await waitDevtools();
  log(`Chrome ${ver.Browser} → ${BASE}`);

  const tab = await (await fetch(`http://127.0.0.1:${PORT}/json/new?about:blank`, { method: 'PUT' })).json();
  await connect(tab.webSocketDebuggerUrl);
  await send('Page.enable');
  await send('Runtime.enable');

  log('\n== 1. 登录 ==');
  check('登录成功', true, await login(USER, PASS));

  // 测试样本（都走接口拿，不写死 id）
  const lib = await evaluate(
    `(async () => (await (await fetch('/api/v1/libraries')).json()).libraries[0].id)()`,
  );
  const movie = await evaluate(
    `(async () => { const b = await (await fetch('/api/v1/libraries/${lib}/browse?kind=movie&limit=1')).json(); return b.items[0] || null; })()`,
  );
  const series = await evaluate(
    `(async () => { const b = await (await fetch('/api/v1/libraries/${lib}/browse?kind=series&limit=1')).json(); return b.items[0] || null; })()`,
  );
  note(`样本：库=${lib} 电影=${movie ? `${movie.id} ${movie.title}` : '（无）'}`);
  if (!movie) {
    log('库里没有电影，后面的用例没法跑');
    process.exit(2);
  }

  log('\n== 2. 海报墙点卡片 → 详情页 ==');
  await send('Page.navigate', { url: `${BASE}/library/${lib}` });
  check(
    '海报墙渲染出卡片',
    true,
    await waitFor('卡片', async () => (await evaluate(`document.querySelectorAll('.poster-card').length`)) > 0),
  );
  const href = await evaluate(`document.querySelector('.poster-card')?.getAttribute('href') || ''`);
  check('卡片指向 /item/{id}（不再是编辑页）', true, href.startsWith('/item/'));
  await evaluate(clickSel('.poster-card'));
  check('详情页打开', true, await waitFor('详情页', async () => (await evaluate('location.pathname')) === href));
  check(
    '头部有海报与背景图',
    true,
    await waitFor('头部元素', async () =>
      (await evaluate(`!!document.querySelector('.detail-poster')`)) &&
      (await evaluate(`!!document.querySelector('.detail-hero-bg img')`)),
    ),
  );
  const title = await evaluate(`(document.querySelector('.detail-body h2')?.textContent || '').trim()`);
  check('标题非空', true, title.length > 0);
  check('有「播放」按钮', true, await evaluate(`!!document.querySelector('a.btn-primary[href^="/play/"]')`));
  check('有「编辑元数据」入口', true, await evaluate(`!!document.querySelector('a.btn[href^="/items/"]')`));
  check('有「回海报墙」', true, await evaluate(`!!document.querySelector('a.btn[href^="/library/"]')`));
  await shot('01-detail-movie');

  log('\n== 3. 相关推荐 ==');
  const relCount = (await evaluate(apiCall(`/api/v1/items/${movie.id}/related?limit=6`))).body.items?.length ?? 0;
  if (relCount > 0) {
    check(
      '页面上有推荐卡片',
      true,
      await waitFor('推荐卡片', async () => (await evaluate(`document.querySelectorAll('.poster-grid .poster-card').length`)) > 0),
    );
    const before = await evaluate('location.pathname');
    await evaluate(clickSel('.poster-grid .poster-card'));
    check(
      '点推荐卡片进另一个详情页',
      true,
      await waitFor('另一个详情页', async () => {
        const p = await evaluate('location.pathname');
        return p.startsWith('/item/') && p !== before;
      }),
    );
  } else {
    note('接口没有返回推荐（库里同类条目太少），跳过');
  }

  log('\n== 4. 演职员 ==');
  const withPeople = await findItemWithPeople();
  if (withPeople) {
    note(`条目 ${withPeople.id} 有 ${withPeople.count} 位演职员（第一位：${withPeople.first}）`);
    await send('Page.navigate', { url: `${BASE}/item/${withPeople.id}` });
    check(
      '演职员区块渲染出来了',
      true,
      await waitFor('演员卡', async () => (await evaluate(`document.querySelectorAll('.cast-item').length`)) > 0),
    );
    check('演员条数与接口一致', withPeople.count, await evaluate(`document.querySelectorAll('.cast-item').length`));
    const first = await evaluate(`(document.querySelector('.cast-name')?.textContent || '').trim()`);
    check('第一位与接口一致', withPeople.first, first);
    check(
      '显示角色（演员/导演…）',
      true,
      await evaluate(`/演员|导演|编剧|制片|作曲/.test(document.querySelector('.cast-item .faint')?.textContent || '')`),
    );
    check(
      '页面说明了演职员来自 nfo',
      true,
      await evaluate(`document.body.textContent.includes('nfo')`),
    );
    const withEpisodes = series ? null : null;
    void withEpisodes;
    await shot('02-detail-cast');
  } else {
    note('库里没有带演职员的条目，跳出演职员渲染用例');
    check(
      '没有演职员时给出「怎么才有」的提示',
      true,
      await evaluate(`document.body.textContent.includes('nfo')`),
    );
  }

  log('\n== 5. 剧集：季标签 + 集列表 ==');
  if (!series) {
    note('库里没有剧集，跳过');
  } else {
    note(`剧集 ${series.id} ${series.title}`);
    await send('Page.navigate', { url: `${BASE}/item/${series.id}` });
    check(
      '有季标签',
      true,
      await waitFor('季标签', async () => (await evaluate(`document.querySelectorAll('.tabs .tab').length`)) > 0),
    );
    check(
      '有集列表',
      true,
      await waitFor('集列表', async () => (await evaluate(`document.querySelectorAll('.ep-row').length`)) > 0),
    );
    check(
      '集行显示季集号',
      true,
      await evaluate(`(document.querySelector('.ep-row')?.textContent || '').includes('E0')`),
    );
    check(
      '剧集本身不显示「播放」（它是分组，不是文件）',
      false,
      await evaluate(`!!document.querySelector('.detail-body a.btn-primary[href^="/play/"]')`),
    );
    await shot('03-detail-series');

    const epHref = await evaluate(`document.querySelector('.ep-row .ep-main')?.getAttribute('href') || ''`);
    check('集链接指向 /item/{id}', true, epHref.startsWith('/item/'));
    await evaluate(clickSel('.ep-row .ep-main'));
    check(
      '点集进该集详情',
      true,
      await waitFor('集详情', async () => (await evaluate('location.pathname')) === epHref),
    );
    check(
      '单集有「播放」',
      true,
      await waitFor('单集播放按钮', async () =>
        (await evaluate(`!!document.querySelector('.detail-body a.btn-primary[href^="/play/"]')`))),
    );
  }

  log('\n== 6. 旧地址 /series/{id} 重定向 ==');
  if (series) {
    await send('Page.navigate', { url: `${BASE}/series/${series.id}` });
    check(
      '重定向到 /item/{id}',
      true,
      await waitFor('重定向', async () => (await evaluate('location.pathname')) === `/item/${series.id}`),
    );
  } else {
    note('库里没有剧集，跳过');
  }

  log('\n== 7. 编辑元数据 → 返回详情 ==');
  await send('Page.navigate', { url: `${BASE}/item/${movie.id}` });
  // 等数据真的加载完再点：SPA 的地址栏是立刻变的，而按钮要等接口回来才有
  check(
    '详情页的「编辑元数据」出来了',
    true,
    await waitFor('编辑入口', async () => (await evaluate(`!!document.querySelector('a.btn[href^="/items/"]')`)), 20000),
  );
  check('点「编辑元数据」', true, await evaluate(clickSel('a.btn[href^="/items/"]')));
  check(
    '进了编辑页',
    true,
    await waitFor('编辑页', async () => (await evaluate('location.pathname')).startsWith('/items/')),
  );
  check(
    '字段表渲染',
    true,
    await waitFor('字段表', async () => (await evaluate(`!!document.querySelector('[data-field="title"]')`)) === true),
  );
  check(
    '编辑页有「返回详情页」',
    true,
    await waitFor('返回详情', async () => (await evaluate(`!!document.querySelector('a.btn[href^="/item/"]')`))),
  );
  await evaluate(clickSel('a.btn[href^="/item/"]'));
  check(
    '回到详情页',
    true,
    await waitFor('回详情', async () => (await evaluate('location.pathname')) === `/item/${movie.id}`),
  );

  log('\n== 8. 多版本选择器 ==');
  if (MULTI > 0) {
    await send('Page.navigate', { url: `${BASE}/item/${MULTI}` });
    check(
      '详情页有版本选择器',
      true,
      await waitFor('版本选择器', async () =>
        (await evaluate(`[...document.querySelectorAll('.field-inline span')].some((s) => s.textContent.includes('版本'))`)),
      ),
    );
    // 用 includes 不用正则：正则里的反斜杠在「模板字符串 / Node 侧」两层转义里极易写错
    // （本脚本第一版就因为 \\?file= 多了一层反斜杠而假失败）
    const playHref = await evaluate(`document.querySelector('.detail-body a.btn-primary[href^="/play/"]')?.getAttribute('href') || ''`);
    check('播放链接带上 ?file=<id>', true, String(playHref).includes('?file='));
    // 切到第二个版本 → 链接里的 file 跟着变
    const before = playHref;
    await evaluate(`(() => {
      const sel = document.querySelector('.field-inline select');
      if (!sel) return false;
      const opts = sel.querySelectorAll('option');
      if (opts.length < 2) return false;
      Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value').set.call(sel, opts[1].value);
      sel.dispatchEvent(new Event('change', { bubbles: true }));
      return true;
    })()`);
    check(
      '换了版本之后播放链接变了',
      true,
      await waitFor('链接变化', async () => {
        const now = await evaluate(`document.querySelector('.detail-body a.btn-primary[href^="/play/"]')?.getAttribute('href') || ''`);
        return now !== before && String(now).includes('?file=');
      }),
    );
    await shot('04-detail-versions');
  } else {
    note('没给 MULTI_VERSION_ITEM，跳过版本选择器（用 scripts/dev/verify-detail.sh 造一个多版本条目）');
  }

  log('\n== 9. 搜索入口也指向详情页 ==');
  await send('Page.navigate', { url: `${BASE}/search?q=${encodeURIComponent(movie.title)}` });
  const okSearch = await waitFor('搜索结果', async () => (await evaluate(`document.querySelectorAll('.search-card').length`)) > 0);
  if (okSearch) {
    const shref = await evaluate(`document.querySelector('.search-card')?.getAttribute('href') || ''`);
    check('搜索卡片指向 /item/{id}', true, shref.startsWith('/item/'));
  } else {
    note('搜不到样本标题，跳过');
  }

  log('\n== 10. 页面没有 JS 报错 ==');
  check('没有页面异常/console.error', '[]', JSON.stringify([...new Set(pageErrors)].slice(0, 3)));
  if (pageErrors.length > 0) await dumpPage('结尾');

  try {
    chrome.kill();
  } catch {}
  log(`\n================ 结果：${pass} 通过 / ${fail} 失败 ================`);
  process.exit(fail === 0 ? 0 : 1);
}

main().catch(async (e) => {
  log('运行出错: ' + (e?.stack || e));
  try {
    await dumpPage('异常');
  } catch {}
  try {
    chrome.kill();
  } catch {}
  log(`\n================ 结果：${pass} 通过 / ${fail} 失败（提前结束） ================`);
  process.exit(1);
});
