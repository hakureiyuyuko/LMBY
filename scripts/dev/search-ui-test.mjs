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
/** 页面抛出的错误与 console.error（验收报告里逐条打印，省得「白屏了但不知道为啥」）。 */
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
  const r = await send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: true });
  writeFileSync(path.join(OUT, `${name}.png`), Buffer.from(r.data, 'base64'));
  log(`截图 ${OUT}/${name}.png`);
}
const clickSel = (sel) =>
  `(() => { const e = document.querySelector(${JSON.stringify(sel)}); if (!e) return false; e.click(); return true; })()`;
/** 在页面里发一个带 cookie 的 GET（断言的期望值直接用后端算，不猜）。 */
const apiGet = (path) => `(async () => {
  const r = await fetch(${JSON.stringify(path)}, { credentials: 'same-origin' });
  return await r.json();
})()`;
const content = `(document.querySelector('.content')?.textContent || '')`;

/** CDP 键盘事件（React 监听 keydown，所以用 rawKeyDown）。 */
const KEYS = {
  ArrowDown: { code: 'ArrowDown', vk: 40 },
  ArrowUp: { code: 'ArrowUp', vk: 38 },
  Enter: { code: 'Enter', vk: 13 },
  Escape: { code: 'Escape', vk: 27 },
};
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

async function pressKey(name) {
  const k = KEYS[name];
  const base = {
    key: name,
    code: k.code,
    windowsVirtualKeyCode: k.vk,
    nativeVirtualKeyCode: k.vk,
  };
  await send('Input.dispatchKeyEvent', { type: 'rawKeyDown', ...base });
  await send('Input.dispatchKeyEvent', { type: 'keyUp', ...base });
}

/**
 * 截图前把联想下拉收起来。
 *
 * 因为测试里的点击是 JS 的 `.click()`，**不会触发 mousedown**，
 * 而下拉是靠「点页面别处（mousedown）」收起的 —— 真鼠标不会碰到这个差别，
 * 但截图会被下拉盖住半页（本次就是）。用 Esc 收，和真人按 Esc 一样。
 */
async function closeSuggest() {
  await evaluate(`document.querySelector('.search-input')?.focus()`);
  await pressKey('Escape');
  await sleep(150);
}

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
  check(
    '导航里有「搜索」',
    true,
    // 登录后导航刚渲染出来可能需要一拍，等它出现（不然偶发假失败）
    await waitFor('导航里的搜索入口', async () =>
      await evaluate(`!!document.querySelector('.nav a[href="/search"]')`),
    ),
  );
  await evaluate(clickSel('.nav a[href="/search"]'));
  check('进入搜索页', true, await waitFor('搜索页', async () => (await evaluate('location.pathname')) === '/search'));
  // ⚠️ 路由切换后内容不是同一拍就好：等输入框真的渲染出来（不等就会在后面各处假失败）
  check(
    '有搜索框',
    true,
    await waitFor('搜索框', async () => await evaluate(`!!document.querySelector('.search-input')`)),
  );
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
      `[...document.querySelectorAll('.search-card')].some((a) => a.getAttribute('href') === '/item/${sample.id}')`,
    ));
    // 失败时把现场记下来（卡片的 href 与实际排名），省得反复重跑
    log('   卡片 href：' + JSON.stringify(await evaluate(
      `[...document.querySelectorAll('.search-card')].map((a) => a.getAttribute('href'))`,
    )));
    log('   结果文本：' + String(await evaluate(content)).slice(0, 160).replace(/\s+/g, ' '));
  }

  log('\n== 5. 点卡片进详情页 ==');
  const firstHref = await evaluate(`document.querySelector('.search-card')?.getAttribute('href') || ''`);
  check('搜索卡片指向详情页 /item/{id}', true, /^\/item\/\d+$/.test(firstHref));
  await evaluate(clickSel('.search-card'));
  check('跳到详情页', true, await waitFor('详情页', async () =>
    (await evaluate('location.pathname')) === firstHref));
  // ⚠️ M6 起卡片去的是**详情页**（`/item/{id}`），字段表在**编辑页**（`/items/{id}`）——
  // 这条断言在 M2 是「等到 [data-field=title]」，M6 之后永远等不到（脚本静默烂了一次）。
  const okHero = await waitFor('详情页头部', async () =>
    (await evaluate(`!!document.querySelector('.detail-hero-inner')`)) === true);
  check('详情页渲染出头部（海报 + 标题）', true, okHero);
  check('详情页有播放入口', true, await evaluate(`!!document.querySelector('a[href^="/play/"]')`));
  if (!okHero) {
    log('   详情页文本：' + String(await evaluate(content)).slice(0, 200).replace(/\s+/g, ' '));
  }

  log('\n== 6. URL 参数可直接打开（可收藏/可分享）==');
  await sleep(300);
  const term = sample ? sample.run.slice(0, 3) : '炼金';
  await send('Page.navigate', {
    url: `${BASE}/search?q=${encodeURIComponent(term)}&kind=movie`,
  });
  await waitFor('输入框带出 URL 里的查询词', async () =>
    (await evaluate(`document.querySelector('.search-input')?.value`)) === term);
  check(
    '输入框带出 URL 里的查询词',
    term,
    await evaluate(`document.querySelector('.search-input')?.value`),
  );
  check('自动出结果', true, await waitFor('结果卡片', async () => (await evaluate(cardCount)) > 0));
  check(
    '类型筛选保持在「电影」（分面胶囊高亮）',
    true,
    await evaluate(`!!document.querySelector('.chip.chip-on')`),
  );
  // 从 URL 进来时不该自己弹联想下拉（只有真敲过键才弹）
  check('从 URL 进来不自动弹联想下拉', false, await evaluate(`!!document.querySelector('.suggest-panel')`));

  log('\n== 7. 即时联想（下拉 / 键盘）==');
  // ⚠️ 先清空再敲：如果直接写回与 URL 里相同的值，React 状态没变化、
  // useSearchParams 也不会动，从而 useEffect 不重跑 —— 联想根本不发请求，
  // 测试会把「功能好好的」判成三个失败（本次真踩到）。
  await evaluate(clickSel('.search-input'));
  // 先敲一个字（值真的变了才会发联想请求）
  await evaluate(setInput('.search-input', term.slice(0, 1)));
  check(
    '敲一个字就有联想',
    true,
    await waitFor('下拉', async () => (await evaluate(`document.querySelectorAll('.suggest-item').length`)) > 0),
  );
  // 失败时把关键现场记下来（焦点在哪、接口回了什么），省得反复重跑
  if (!(await evaluate(`!!document.querySelector('.suggest-panel')`))) {
    log('   诊断：activeElement=' + (await evaluate(`document.activeElement?.className || '?'`)));
    const raw = await evaluate(
      apiGet(`/api/v1/search/suggest?q=${encodeURIComponent(term.slice(0, 1))}&limit=8`),
    );
    log(
      '   诊断：suggest 接口 ' +
        `items=${raw.items?.length} people=${raw.people?.length}（query=${raw.query}）`,
    );
  }
  await evaluate(setInput('.search-input', ''));
  check('清空后下拉收起', false, await evaluate(`!!document.querySelector('.suggest-panel')`));
  await evaluate(setInput('.search-input', term));
  check(
    '联想下拉出现',
    true,
    await waitFor('下拉', async () => (await evaluate(`document.querySelectorAll('.suggest-item').length`)) > 0),
  );
  check(
    '联想分组标题是「作品」',
    true,
    await evaluate(
      `[...document.querySelectorAll('.suggest-group')].some((e) => e.textContent.includes('作品'))`,
    ),
  );
  check(
    '联想项是轻量的（不带简介）',
    false,
    await evaluate(`!!document.querySelector('.suggest-item .search-clamp')`),
  );
  // 联想的头一条必须就是搜索结果的头一条（同一个排序，不然回车后东西会跳位）
  const firstHit = await evaluate(apiGet(`/api/v1/search?q=${encodeURIComponent(term)}&limit=1`));
  const firstId = firstHit?.items?.[0]?.id;
  log(`   联想的第一条应当是条目 ${firstId}`);  check(
    '联想第一条 == 搜索结果第一条',
    true,
    await evaluate(
      `(() => { const img = document.querySelector('.suggest-thumb');
        return !!img && img.getAttribute('src').includes('/items/${firstId}/'); })()`,
    ),
  );
  // 键盘换一个候选更多的词再测：只命中 1 条时「高亮往下移」是空断言（移了个寂寞）。
  // 具体用哪个词不写死 —— 从真库里试几个候选，取第一个联想 ≥3 条的（换库也能跑）。
  let keyTerm = null;
  let keyHits = 0;
  for (const c of ['AVC', 'AV', '之', '电', '科', term.slice(0, 2)]) {
    if (!c) continue;
    const s = await evaluate(apiGet(`/api/v1/search/suggest?q=${encodeURIComponent(c)}&limit=8`));
    const n = (s.items?.length || 0) + (s.people?.length || 0);
    if (n >= 3) {
      keyTerm = c;
      keyHits = n;
      break;
    }
  }
  if (!keyTerm) {
    log('   真库里找不到联想候选 ≥3 条的查询词，跳过键盘断言');
  } else {
    log(`   键盘用查询词「${keyTerm}」（联想 ${keyHits} 条）`);
    await evaluate(setInput('.search-input', keyTerm));
    await waitFor('候选变多', async () =>
      (await evaluate(`document.querySelectorAll('.suggest-item').length`)) >= 3);
    await evaluate(`document.querySelector('.search-input').focus()`);
    // 高亮位置按「不算末尾那条『搜索…全部结果』」的候选列表算（与组件的 flat 一致）
    const nav = `(() => {
      const flat = [...document.querySelectorAll('.suggest-item')].filter((e) => !e.classList.contains('suggest-more'));
      return { n: flat.length, at: flat.findIndex((e) => e.classList.contains('active')) };
    })()`;
    const nav0 = await evaluate(nav);
    log(`   联想候选 ${nav0.n} 项，默认高亮第 ${nav0.at} 项`);
    check('联想候选 ≥ 2 条（键盘断言才有意义）', true, nav0.n >= 2);
    check('默认高亮第一条', 0, nav0.at);
    await pressKey('ArrowDown');
    const nav1 = await evaluate(nav);
    log(`   高亮索引：↓ ${nav0.at} → ${nav1.at}（共 ${nav0.n} 项）`);
    check('↓ 把高亮往下移一格（到末项则回绕到首项）', (nav0.at + 1) % nav0.n, nav1.at);
    await pressKey('ArrowUp');
    check('↑ 把高亮移回去', nav0.at, (await evaluate(nav)).at);
  }
  await shot('04-search-suggest');
  await pressKey('Enter');
  check(
    'Enter 选中高亮项 → 进条目详情页',
    true,
    await waitFor('详情页', async () => (await evaluate('location.pathname')).startsWith('/item/')),
  );

  log('\n== 8. 结果分面 ==');
  await evaluate(clickSel('.nav a[href="/search"]'));
  await waitFor('回到搜索页', async () => (await evaluate('location.pathname')) === '/search');
  check('路由切回搜索页后输入框就绪', true, await waitFor('搜索框', async () =>
    await evaluate(`!!document.querySelector('.search-input')`)));
  await evaluate(setInput('.search-input', term));
  await evaluate(submitSearch);
  check(
    '出现分面胶囊',
    true,
    await waitFor('分面', async () => (await evaluate(`document.querySelectorAll('.chip').length`)) >= 2),
  );
  check(
    '分面里有「结果」与「媒体库」两组',
    true,
    await evaluate(`(() => {
      const t = [...document.querySelectorAll('.facet-label')].map((e) => e.textContent.trim());
      return t.includes('结果') && t.includes('媒体库');
    })()`),
  );
  // 流派分组只在「命中的条目真有流派」时才有（nfo 里没写流派就不会有这一行）——
  // 所以拿接口的答案当期望值，不硬断言它一定在
  const facetsApi = await evaluate(apiGet(`/api/v1/search/facets?q=${encodeURIComponent(term)}`));
  const genreInDom = await evaluate(
    `[...document.querySelectorAll('.facet-label')].some((e) => e.textContent.trim() === '流派')`,
  );
  check(
    '流派分面有数据 ⇔ 界面上有「流派」分组',
    facetsApi.facets.genre.length > 0,
    genreInDom,
  );
  // 「全部」胶囊上的数字必须等于结果区的总数
  const totalFromChips = await evaluate(`(() => {
    const all = [...document.querySelectorAll('.chip')].find((e) => e.textContent.includes('全部'));
    return Number((all?.textContent || '').replace(/\\D/g, ''));
  })()`);
  const summaryText = await evaluate(content);
  check(
    '「全部」的数量 == 结果总计',
    true,
    new RegExp(`共\\s*${totalFromChips}\\s*条`).test(summaryText),
  );
  // 点第一个类型胶囊（不是「全部」/「人」）→ kind 筛选
  const clicked = await evaluate(`(() => {
    const chips = [...document.querySelectorAll('.chip[data-facet="kind"]')];
    const t = chips[0];
    if (!t) return null;
    const n = Number((t.textContent || '').replace(/\\D/g, ''));
    t.click();
    return { value: t.dataset.value, label: t.textContent.trim(), count: n };
  })()`);
  check('有类型胶囊可点', true, !!clicked);
  check(
    '点类型胶囊 → URL 带 kind',
    true,
    await waitFor('kind 参数', async () => (await evaluate('location.search')).includes('kind=')),
  );
  check('被点的胶囊高亮', true, await evaluate(`!!document.querySelector('.chip.chip-on')`));
  if (clicked) {
    log(`   点了胶囊「${clicked.label}」（计数 ${clicked.count}）`);
    const summary2 = await evaluate(content);
    check(
      '筛选后的总数 == 胶囊上的计数',
      true,
      new RegExp(`共\\s*${clicked.count}\\s*条`).test(summary2),
    );
  }
  // 点媒体库胶囊 → lib 筛选（胶囊上有 data-facet/data-value，别靠中文文本找元素）
  const hasLibChip = await evaluate(`!!document.querySelector('.chip[data-facet="library"]')`);
  check('有媒体库胶囊', true, hasLibChip);
  const libClicked = hasLibChip
    ? await evaluate(clickSel('.chip[data-facet="library"]'))
    : false;
  check(
    '点媒体库胶囊 → URL 带 lib',
    true,
    libClicked === true &&
      (await waitFor('lib 参数', async () => (await evaluate('location.search')).includes('lib='))),
  );
  check('库筛选后仍有结果', true, (await evaluate(cardCount)) > 0);
  check('筛选行出现可取消的标签', true, await evaluate(`!!document.querySelector('.chip-static .chip-x')`));
  await evaluate(clickSel('.chip-static .chip-x'));
  check(
    '点 × 取消筛选 → URL 去掉 lib',
    true,
    await waitFor('lib 消失', async () => !(await evaluate('location.search')).includes('lib=')),
  );
  // 给文档留一张「分面长什么样」的图：1 条命中的分面看不出东西，
  // 所以换一个候选多的词（§7 挑出来的那个），再收掉下拉（下拉是用 JS 点击关不掉的）
  if (keyTerm) {
    await send('Page.navigate', {
      url: `${BASE}/search?q=${encodeURIComponent(keyTerm)}`,
    });
    check(
      '换一个大词后分面就绪',
      true,
      await waitFor('分面', async () => (await evaluate(`document.querySelectorAll('.chip').length`)) >= 3),
    );
    await closeSuggest();
    await shot('05-search-facets');
  }

  log('\n== 9. 人名联想与「人」这一档 ==');
  // 样本：从真库的条目接口里读一个 ≥3 字的人名（库里没有演职员时会明确跳过）
  const person = await evaluate(`(async () => {
    const libs = await (await fetch('/api/v1/libraries', { credentials: 'same-origin' })).json();
    const lib = libs.libraries[0];
    const page = await (await fetch('/api/v1/libraries/' + lib.id + '/items?kind=movie&limit=40',
      { credentials: 'same-origin' })).json();
    for (const it of (page.items || []).slice(0, 20)) {
      const p = await (await fetch('/api/v1/items/' + it.id + '/people', { credentials: 'same-origin' })).json();
      const hit = (p.people || []).find((x) => (x.name || '').length >= 3);
      if (hit) return { name: hit.name, personId: hit.personId, itemId: it.id };
    }
    return null;
  })()`);
  if (!person) {
    log('   库里没有 ≥3 字人名的演职员（先重扫一次 nfo），跳过第 9/10 节');
  } else {
    log(`   样本：条目 ${person.itemId} 的演职员「${person.name}」`);
    // 像真用户那样先点进输入框，再敲字
    check('人名联想前输入框就绪', true, await waitFor('搜索框', async () =>
      await evaluate(`!!document.querySelector('.search-input')`)));
    await evaluate(clickSel('.search-input'));
    await evaluate(setInput('.search-input', person.name));
    check(
      '人名联想：下拉里有「演职员」分组',
      true,
      await waitFor('人分组', async () =>
        await evaluate(
          `[...document.querySelectorAll('.suggest-group')].some((e) => e.textContent.includes('演职员'))`,
        ),
      ),
    );
    check(
      '人名联想：列出了这个人',
      true,
      await evaluate(
        `[...document.querySelectorAll('.suggest-item')].some((e) => e.textContent.includes(${JSON.stringify(
          person.name,
        )}))`,
      ),
    );
    check(
      '人名联想：带作品数',
      true,
      await evaluate(`[...document.querySelectorAll('.suggest-item')].some((e) => /\\d+ 部作品/.test(e.textContent))`),
    );
    await evaluate(`(() => {
      const el = [...document.querySelectorAll('.suggest-item')]
        .find((e) => e.textContent.includes(${JSON.stringify(person.name)}));
      if (!el) return false;
      el.click();
      return true;
    })()`);
    check(
      '点人名 → 变成按人筛选（person=，且没有 q=）',
      true,
      await waitFor('person 参数', async () => {
        const s = await evaluate('location.search');
        return s.includes('person=') && !s.includes('q=');
      }),
    );
    check('按人筛选：有结果', true, await waitFor('结果', async () => (await evaluate(cardCount)) > 0));
    // ⚠️ 不能断言「原条目在第一页」：一个演员可能有上百部作品，分页把它挤到后面很正常。
    // 接口层断言「结果集合里有它」，界面层断言「卡片与这个人有关」（抽第一条回查演职员表）。
    const personSearch = await evaluate(
      apiGet(`/api/v1/search?q=&personId=${person.personId}&limit=100`),
    );
    check(
      '按人筛选：接口结果里有原条目',
      true,
      (personSearch.items || []).some((x) => x.id === person.itemId),
    );
    const firstCardId = await evaluate(
      `Number((document.querySelector('.search-card')?.getAttribute('href') || '').replace('/item/', ''))`,
    );
    const firstCardPeople = await evaluate(apiGet(`/api/v1/items/${firstCardId}/people`));
    check(
      '按人筛选：第一张卡片的演职员表里有这个人',
      true,
      (firstCardPeople.people || []).some((x) => x.personId === person.personId),
    );
    check('按人筛选：有可取消的「人」标签', true, await evaluate(`!!document.querySelector('.chip-person')`));
    await shot('06-search-person');

    // 回到「只有查询词」的状态看「人」这一档。
    // ⚠️ 直接清址再导航，而不是接着输入框提交：刚才那条 URL 里还挂着 person=<id>，
    // 合并式改参数会把「人名」当成额外筛选条件，变成「标题+人」两条都要满足（必然 0 条），
    // 于是分面里的人数与人的结果都对不上（本次真踩到）。
    await send('Page.navigate', {
      url: `${BASE}/search?q=${encodeURIComponent(person.name)}`,
    });
    check(
      '出现「人」胶囊',
      true,
      await waitFor('人胶囊', async () =>
        await evaluate(`!!document.querySelector('.chip[data-facet="people"]')`),
      ),
    );
    const peopleChip = await evaluate(`(() => {
      const e = document.querySelector('.chip[data-facet="people"]');
      if (!e) return 0;
      return Number((e.textContent || '').replace(/\\D/g, ''));
    })()`);
    const peopleApi = await evaluate(
      apiGet(`/api/v1/search/people?q=${encodeURIComponent(person.name)}`),
    );
    check('「人」胶囊计数 == 人接口的 total', peopleApi.total, peopleChip);
    await evaluate(clickSel('.chip[data-facet="people"]'));
    check(
      '切到「人」→ URL 带 tab=people',
      true,
      await waitFor('tab', async () => (await evaluate('location.search')).includes('tab=people')),
    );
    check(
      '列出演职员',
      true,
      await waitFor('人名列表', async () =>
        (await evaluate(`document.querySelectorAll('.people-item').length`)) > 0,
      ),
    );
    check('人名列表里能找到那个人', true, await evaluate(
      `[...document.querySelectorAll('.people-item')].some((e) => e.textContent.includes(${JSON.stringify(
        person.name,
      )}))`,
    ));
    check('每条带「看作品」按钮', true, await evaluate(
      `[...document.querySelectorAll('.people-item')].every((e) => !!e.querySelector('.btn'))`,
    ));
    await closeSuggest();
    await shot('07-search-people');
  }

  log('\n== 10. 亮色主题 ==');
  await evaluate(clickSel('.theme-toggle'));
  await sleep(400);
  check('切到亮色', 'light', await evaluate('document.documentElement.dataset.theme'));
  await shot('08-search-light');

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
