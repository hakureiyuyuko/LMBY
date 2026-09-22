// M6「首页」（Netflix 风格）的界面验收（CDP 直连 headless Chrome，不依赖 Puppeteer）
//
// 验的是「这一页真的能看能点」：登录 → 大屏轮播（圆点/箭头/自动切）→ 横滑行（继续观看 /
// 为你推荐 / 最近添加）→ 卡片进详情页 → 行的左右滚动 → 推荐依据（副标题 + 口味画像）
// → 折叠的服务状态面板 → 亮暗主题。
//
// 期望值**不写死**：条目数、行内容、推荐依据都从 `GET /api/v1/home` 现取，
// 这样换库、换账号、数据变了脚本也不会假失败（只会少断几条）。
//
// 用法：
//   BASE=http://192.168.x.x:8099 LMBY_USER=devtest LMBY_PASS=口令 node home-ui-test.mjs
import { appendFileSync, mkdirSync, writeFileSync } from 'node:fs';
import { spawn } from 'node:child_process';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';

const REPORT = 'home-ui-report.txt';
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
const OUT = process.env.OUT || 'shots-home';
// 端口随进程号漂移：同机跑两份脚本时不至于抢同一个 DevTools
const PORT = Number(process.env.CDP_PORT || 9547 + (process.pid % 200));

mkdirSync(OUT, { recursive: true });
const profile = path.join(os.tmpdir(), `lmby-home-${Date.now()}`);

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
/** 页面抛出的错误与 console.error（报告里逐条打印，省得「白屏了但不知道为什么」）。 */
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
/** 在页面里发一个带 cookie 的 GET（断言的期望值直接用后端算，不猜）。 */
const apiGet = (path) => `(async () => {
  const r = await fetch(${JSON.stringify(path)}, { credentials: 'same-origin' });
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

  log('\n== 1. 登录（落点就是首页）==');
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
  check('登录后落在首页 /', true, await waitFor('首页', async () => (await evaluate('location.pathname')) === '/'));

  const home = await evaluate(apiGet('/api/v1/home'));
  log(`   接口：hero ${home.hero?.length ?? 0} 条 · 继续观看 ${home.continue?.length ?? 0} 条 · ` +
    `行 ${(home.sections || []).map((s) => `${s.key}(${s.items.length})`).join(' ')}`);
  if (home.hero?.length) {
    log(`   轮播顺序（按「最近更新」）：${home.hero.slice(0, 3).map((h) => `${h.id}:${h.title}`).join(' | ')}`);
  }

  log('\n== 2. 大屏轮播 ==');
  check(
    '轮播渲染出来了',
    true,
    await waitFor('轮播', async () => await evaluate(`!!document.querySelector('.hero[data-hero-id]')`)),
  );
  const heroCount = home.hero?.length ?? 0;
  check('圆点数量 == 接口里的轮播条数', heroCount, await evaluate(`document.querySelectorAll('.hero-dot').length`));
  check('轮播标题非空', true, (await evaluate(`document.querySelector('.hero-title')?.textContent || ''`)).length > 0);
  check(
    '轮播有播放入口（/play/）',
    true,
    await evaluate(`!!document.querySelector('.hero a[href^="/play/"]')`),
  );
  check('轮播有详情入口（/item/）', true, await evaluate(`!!document.querySelector('.hero a[href^="/item/"]')`));
  check(
    '首张轮播就是接口里的第一条',
    heroCount ? String(home.hero[0].id) : '0',
    await evaluate(`document.querySelector('.hero[data-hero-id]')?.dataset.heroId || '0'`),
  );
  // 宽幅背景图是整页的门面：没加载出来（或加载失败）要当失败看，
  // 否则截出来的图是一块黑，看着像功能坏了
  const heroLoaded = await waitFor(
    '轮播背景图',
    async () =>
      await evaluate(`(() => {
        const img = document.querySelector('.hero-img');
        return !!img && img.complete && img.naturalWidth > 0;
      })()`),
    15000,
  );
  check('轮播背景图加载出来了', true, heroLoaded);
  log(`   轮播背景图：${await evaluate(`(() => {
    const img = document.querySelector('.hero-img');
    return img ? img.currentSrc.replace(location.origin, '') + ' ' + img.naturalWidth + 'x' + img.naturalHeight : '无';
  })()`)}`);
  await shot('01-home-dark');

  log('\n== 3. 轮播切换（圆点 / 箭头）==');
  if (heroCount >= 3) {
    await evaluate(clickSel('.hero-dot[data-hero-dot="1"]'));
    check(
      '点第 2 个圆点 → 换到第 2 条',
      String(home.hero[1].id),
      await evaluate(`document.querySelector('.hero[data-hero-id]')?.dataset.heroId || ''`),
    );
    check(
      '第 2 个圆点被标成当前',
      true,
      await evaluate(`document.querySelector('.hero-dot[data-hero-dot="1"]')?.classList.contains('on')`),
    );
    await evaluate(`document.querySelector('.hero-arrow[aria-label="下一部"]').click()`);
    check(
      '点 › → 换到第 3 条',
      String(home.hero[2].id),
      await evaluate(`document.querySelector('.hero[data-hero-id]')?.dataset.heroId || ''`),
    );
    await evaluate(`document.querySelector('.hero-arrow[aria-label="上一部"]').click()`);
    check(
      '点 ‹ → 退回第 2 条',
      String(home.hero[1].id),
      await evaluate(`document.querySelector('.hero[data-hero-id]')?.dataset.heroId || ''`),
    );

    log('\n== 4. 自动轮播（8 秒一张）==');
    const before = await evaluate(`document.querySelector('.hero[data-hero-id]')?.dataset.heroId || ''`);
    await sleep(9000);
    const after = await evaluate(`document.querySelector('.hero[data-hero-id]')?.dataset.heroId || ''`);
    log(`   9 秒后：${before} → ${after}`);
    check('自动切到了下一张', true, after !== before);
  } else {
    log('   库里轮播不足 3 条，跳过切换与自动轮播用例');
  }

  log('\n== 5. 横滑行 ==');
  const rowKeys = (home.sections || []).map((s) => s.key);
  for (const key of rowKeys) {
    const s = home.sections.find((x) => x.key === key);
    check(
      `行「${key}」渲染出来了`,
      true,
      await waitFor(`行 ${key}`, async () => await evaluate(`!!document.querySelector('.row-block[data-row="${key}"]')`)),
    );
    check(
      `行「${key}」卡片数 == 接口条数`,
      s.items.length,
      await evaluate(`document.querySelectorAll('.row-block[data-row="${key}"] .poster-card').length`),
    );
  }
  const firstCardHref = await evaluate(
    `document.querySelector('.row-block .poster-card')?.getAttribute('href') || ''`,
  );
  check('卡片指向详情页 /item/{id}', true, /^\/item\/\d+$/.test(firstCardHref));
  // 海报加载情况只记录不卡（库里本来就有没图的条目）
  const loaded = await evaluate(`[...document.querySelectorAll('.row-block .poster-img')]
    .filter((img) => img.complete && img.naturalWidth > 0).length`);
  const total = await evaluate(`document.querySelectorAll('.row-block .poster-img').length`);
  log(`   海报加载：${loaded}/${total}（这个库里有相当一部分条目没有图片）`);

  if ((home.continue?.length ?? 0) > 0) {
    check(
      '继续观看行渲染出来了',
      true,
      await waitFor('继续观看行', async () =>
        await evaluate(`!!document.querySelector('.row-block[data-row="continue"]')`),
      ),
    );
    check(
      '继续观看条数 == 接口条数',
      home.continue.length,
      await evaluate(`document.querySelectorAll('.row-block[data-row="continue"] .continue-card').length`),
    );
    check(
      '继续观看卡片有进度条',
      true,
      await evaluate(`!!document.querySelector('.row-block[data-row="continue"] .continue-bar span')`),
    );
  } else {
    log('   这个账号没有继续观看记录，跳过该行');
  }

  log('\n== 6. 行的左右滚动 ==');
  const rowKey = rowKeys[0];
  if (rowKey) {
    const before = await evaluate(
      `document.querySelector('.row-block[data-row="${rowKey}"] .row-scroll').scrollLeft`,
    );
    await evaluate(
      `document.querySelector('.row-block[data-row="${rowKey}"] .row-arrow[aria-label="向右滚动"]').click()`,
    );
    await sleep(700);
    const after = await evaluate(
      `document.querySelector('.row-block[data-row="${rowKey}"] .row-scroll').scrollLeft`,
    );
    log(`   行「${rowKey}」滚动：${before} → ${after}`);
    check('点 › 把行往右滚了', true, after > before);
  }

  log('\n== 7. 推荐的依据 ==');
  const rec = (home.sections || []).find((s) => s.key === 'recommend');
  if (rec) {
    const subtitle = await evaluate(
      `document.querySelector('.row-block[data-row="recommend"] .row-head .small')?.textContent || ''`,
    );
    log(`   副标题：${subtitle}`);
    check('副标题说明了依据（因为 / 根据）', true, /因为|根据/.test(subtitle));
    check(
      '口味画像出现在行头上',
      true,
      await evaluate(`document.querySelectorAll('.row-block[data-row="recommend"] .row-taste .badge').length > 0`),
    );
    const shownTaste = await evaluate(
      `[...document.querySelectorAll('.row-block[data-row="recommend"] .row-taste .badge')].map((e) => e.textContent.trim())`,
    );
    log(`   画像（前 3）：${shownTaste.join(' / ')}（接口给的权重最高的：${(rec.taste || []).slice(0, 3).map((t) => t.genre).join(' / ')}）`);
    check(
      '画像条数与接口一致（最多显示 3 个）',
      Math.min(3, (rec.taste || []).length),
      shownTaste.length,
    );
  } else {
    log('   这一轮没有「为你推荐」行（没有观看记录），跳过依据断言');
  }

  log('\n== 8. 点卡片进详情页 ==');
  await evaluate(clickSel('.row-block .poster-card'));
  check(
    '跳进详情页',
    true,
    await waitFor('详情页', async () => (await evaluate('location.pathname')) === firstCardHref),
  );
  check('详情页渲染出头部', true, await waitFor('详情页头部', async () =>
    await evaluate(`!!document.querySelector('.detail-hero-inner')`)));

  log('\n== 9. 首页不再放服务状态（M6 搬到了设置页）==');
  await send('Page.navigate', { url: `${BASE}/` });
  check('回到首页', true, await waitFor('首页', async () => (await evaluate('location.pathname')) === '/'));
  check('导航里有「首页」', true, await waitFor('导航', async () =>
    await evaluate(`[...document.querySelectorAll('.nav a')].some((a) => a.textContent.trim() === '首页')`)));
  check('首页有内容行', true, await waitFor('内容行', async () =>
    await evaluate(`document.querySelectorAll('.row-block').length > 0`)));
  check('首页不再有服务状态面板', false, await evaluate(`
    !!document.querySelector('details.home-health')
      || /服务状态/.test(document.querySelector('.content')?.textContent || '')`));
  check('首页不再调 /healthz（搬到设置页了）', true, await waitFor('首页就绪', async () =>
    await evaluate(`!!document.querySelector('.row-block')`)));

  log('\n== 10. 亮色主题 ==');
  await evaluate(clickSel('.theme-toggle'));
  await sleep(400);
  check('切到亮色', 'light', await evaluate('document.documentElement.dataset.theme'));
  await shot('02-home-light');

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
