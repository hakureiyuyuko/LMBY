// M6「响应式」的界面验收（CDP 直连 headless Chrome）
//
// 「响应式」不是「看起来还行」，而是可以判定的两件事：
//   1. **任何主要页面在三种宽度下都没有横向滚动**（这是手机上最刺眼的坏味道）；
//   2. 手机上该点得到的东西点得到（导航能滑到「设置」、海报墙仍有两列、
//      分面标签不被挤成一条、编辑页的表格能横滑）。
//
// 三个宽度：375×812（手机）、768×1024（平板竖屏）、1440×900（桌面）。
// 顺手在三个宽度下各截一张首页图，放进 docs/images/。
//
// 用法：
//   BASE=http://192.168.x.x:8099 LMBY_USER=devtest LMBY_PASS=口令 node responsive-ui-test.mjs
import { appendFileSync, mkdirSync, writeFileSync } from 'node:fs';
import { spawn } from 'node:child_process';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';

const REPORT = 'responsive-ui-report.txt';
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
const OUT = process.env.OUT || 'shots-responsive';
const PORT = Number(process.env.CDP_PORT || 9847 + (process.pid % 200));

mkdirSync(OUT, { recursive: true });
const profile = path.join(os.tmpdir(), `lmby-responsive-${Date.now()}`);

/** 三个宽度：手机 / 平板竖屏 / 桌面。 */
const VIEWPORTS = [
  { name: '手机 375', width: 375, height: 812, mobile: true },
  { name: '平板 768', width: 768, height: 1024, mobile: false },
  { name: '桌面 1440', width: 1440, height: 900, mobile: false },
];

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
    '--window-size=1440,900',
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
const apiGet = (p) => `(async () => {
  const r = await fetch(${JSON.stringify(p)} + (${JSON.stringify(p)}.includes('?') ? '&' : '?') + '_=' + Date.now(),
    { credentials: 'same-origin' });
  return await r.json();
})()`;
const setInput = (sel, val) => `(() => {
  const el = document.querySelector(${JSON.stringify(sel)});
  if (!el) return null;
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(el, ${JSON.stringify(val)});
  el.dispatchEvent(new Event('input', { bubbles: true }));
  return el.value;
})()`;

/** 横向溢出多少像素（0 表示没有横向滚动；留 1px 给亚像素舍入）。 */
const overflowExpr = `(() => {
  const d = document.documentElement;
  return Math.max(0, d.scrollWidth - d.clientWidth);
})()`;

/** 谁撑出去了：列出右边界超过视口的元素（诊断用，失败时打进报告）。 */
const wideExpr = `(() => {
  const w = document.documentElement.clientWidth;
  return [...document.querySelectorAll('body *')]
    .filter((e) => {
      const r = e.getBoundingClientRect();
      return r.width > 0 && r.right > w + 1;
    })
    .slice(0, 8)
    .map((e) => (e.tagName + '.' + String(e.className || '')).slice(0, 40) + '→' + Math.round(e.getBoundingClientRect().right)
      + (e.getAttribute('href') ? '(' + e.getAttribute('href').slice(0, 24) + ')' : ''));
})()`;

async function setViewport(v) {
  await send('Emulation.setDeviceMetricsOverride', {
    width: v.width,
    height: v.height,
    deviceScaleFactor: 1,
    mobile: v.mobile,
  });
  await sleep(250);
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
  await send('Log.enable');
  await send('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-color-scheme', value: 'dark' }] });
  await setViewport(VIEWPORTS[0]);

  log('\n== 1. 登录（手机宽度下完成） ==');
  await send('Page.navigate', { url: `${BASE}/login` });
  await waitFor('登录页', async () => (await evaluate('location.pathname')) === '/login');
  check(
    '登录表单在手机宽度下没横向滚动',
    0,
    await waitFor('登录页就绪', async () =>
      (await evaluate(`document.querySelectorAll('.center-card input').length`)) >= 2,
    ) && (await evaluate(overflowExpr)),
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

  // 样本：一个媒体库、一部电影（后面的路径都用它）
  const sample = await evaluate(`(async () => {
    const libs = await (await fetch('/api/v1/libraries', { credentials: 'same-origin' })).json();
    const lib = (libs.libraries || [])[0];
    if (!lib) return null;
    const page = await (await fetch('/api/v1/libraries/' + lib.id + '/items?kind=movie&limit=5',
      { credentials: 'same-origin' })).json();
    const it = (page.items || [])[0];
    return { libId: lib.id, itemId: it ? it.id : null, title: it ? it.title : '' };
  })()`);
  if (!sample?.itemId) throw new Error('库里至少要有一部电影');
  log(`   样本：库 ${sample.libId} · 条目 ${sample.itemId}《${sample.title}》`);

  const pages = [
    { path: '/', name: '首页' },
    { path: `/library/${sample.libId}`, name: '海报墙' },
    { path: '/search?q=a', name: '搜索页' },
    { path: `/item/${sample.itemId}`, name: '详情页' },
    { path: `/play/${sample.itemId}`, name: '播放器' },
    { path: '/lists', name: '我的列表' },
    { path: '/livetv', name: '直播页' },
    { path: '/settings', name: '设置页' },
  ];

  for (const v of VIEWPORTS) {
    log(`\n== 2. ${v.name}（${v.width}×${v.height}）下逐个页面看有没有横向滚动 ==`);
    await setViewport(v);
    for (const pg of pages) {
      await send('Page.navigate', { url: BASE + pg.path });
      // 等「主内容区」渲染出来（各页的骨架不同，用 .content 有子节点当信号）
      const ready = await waitFor(`${pg.name} 就绪`, async () =>
        await evaluate(`(document.querySelector('.content')?.children.length || 0) > 0`),
      );
      if (!ready) {
        check(`${v.name}：${pg.name} 渲染出来了`, true, false);
        continue;
      }
      await sleep(400); // 留给图片/表格把布局撑开
      const over = await evaluate(overflowExpr);
      check(`${v.name}：${pg.name} 没有横向滚动（溢出 ${over}px）`, 0, over);
      if (over > 0) {
        log(`      撑出去的元素：${(await evaluate(wideExpr)).join(' | ')}`);
      }
    }
  }

  log('\n== 3. 手机上（375）顶栏与导航 ==');
  await setViewport(VIEWPORTS[0]);
  await send('Page.navigate', { url: `${BASE}/` });
  await waitFor('首页', async () => await evaluate(`!!document.querySelector('.row-block')`));
  const topbarRows = await evaluate(`(() => {
    const t = document.querySelector('.topbar');
    const nav = document.querySelector('.nav');
    if (!t || !nav) return null;
    // 导航是不是被挤到了第二行：它的顶边明显低于品牌
    const brand = document.querySelector('.brand')?.getBoundingClientRect();
    const navBox = nav.getBoundingClientRect();
    return { secondRow: navBox.top >= (brand ? brand.bottom - 2 : 0), scrollable: nav.scrollWidth > nav.clientWidth + 4 };
  })()`);
  check('顶栏在手机上换成两行（导航独立一行）', true, topbarRows?.secondRow);
  check('导航在手机上可以横向滑动（内容比容器宽）', true, topbarRows?.scrollable);
  // 「能滑」不等于「好看」：flex 子项被压缩时中文标签会变成每字一行的竖排
  // （第一版就是这样，而「没有横向滚动」的检查根本抓不到），所以单独断言单行高度。
  const navHeights = await evaluate(`(() => {
    const links = [...document.querySelectorAll('.nav a')];
    if (links.length === 0) return { count: 0, min: 0, max: 0 };
    const hs = links.map((a) => Math.round(a.getBoundingClientRect().height));
    return { count: hs.length, min: Math.min(...hs), max: Math.max(...hs) };
  })()`);
  log(`   导航 ${navHeights.count} 项：高度 ${navHeights.min}~${navHeights.max}px`);
  // 自标定：所有导航项都应该是单行，于是彼此高度接近；
  // 某一项被压成竖排时它会明显高出其它项（不用猜具体像素阈值）。
  check(
    '导航项都是单行（没有被压成竖排）',
    true,
    navHeights.count > 0 && navHeights.max <= navHeights.min * 1.5,
  );
  await evaluate(`(() => { const n = document.querySelector('.nav'); n.scrollLeft = n.scrollWidth; return n.scrollLeft; })()`);
  await sleep(300);
  check(
    '滑到最右能看到「设置」',
    true,
    await evaluate(`(() => {
      const n = document.querySelector('.nav');
      const s = [...n.querySelectorAll('a')].find((a) => a.textContent.trim() === '设置');
      if (!s) return false;
      const nr = n.getBoundingClientRect();
      const sr = s.getBoundingClientRect();
      return sr.left >= nr.left - 1 && sr.right <= nr.right + 1;
    })()`),
  );
  await shot('01-home-phone');

  log('\n== 4. 手机上首页与海报墙的信息密度 ==');
  const hero = await evaluate(`(() => {
    const h = document.querySelector('.hero');
    const title = document.querySelector('.hero-title');
    if (!h) return null;
    return {
      height: Math.round(h.getBoundingClientRect().height),
      vh: Math.round((h.getBoundingClientRect().height / window.innerHeight) * 100),
      titleSize: Number(getComputedStyle(title).fontSize.replace('px', '')),
    };
  })()`);
  log(`   轮播高度 ${hero?.height}px（${hero?.vh}vh），标题字号 ${hero?.titleSize}px`);
  check('轮播在手机上不超过 45% 视口高（不把下面的行全挤走）', true, (hero?.vh ?? 99) <= 45);
  check('轮播标题在手机上不爆字号（< 32px）', true, (hero?.titleSize ?? 99) < 32);

  await send('Page.navigate', { url: `${BASE}/library/${sample.libId}` });
  await waitFor('海报墙', async () => await evaluate(`document.querySelectorAll('.poster-card').length > 0`));
  await sleep(400);
  const columns = await evaluate(`(() => {
    const cards = [...document.querySelectorAll('.poster-card')].slice(0, 6);
    if (cards.length < 2) return 0;
    const top = cards[0].getBoundingClientRect().top;
    return cards.filter((c) => Math.abs(c.getBoundingClientRect().top - top) < 4).length;
  })()`);
  log(`   海报墙：首行 ${columns} 列`);
  check('手机上海报墙至少两列', true, columns >= 2);
  await shot('02-poster-wall-phone');

  log('\n== 5. 手机上搜索页的分面标签 ==');
  await send('Page.navigate', { url: `${BASE}/search?q=a` });
  await waitFor('搜索页', async () => await evaluate(`!!document.querySelector('.facet-label')`));
  await sleep(300);
  check(
    '分面标签在手机上独占一行（不被胶囊挤成一条）',
    true,
    await evaluate(`(() => {
      const label = document.querySelector('.facet-label');
      const chip = label?.parentElement?.querySelector('.chip');
      if (!label || !chip) return false;
      // 独占一行 = 胶囊的顶边不低于标签的底边
      return chip.getBoundingClientRect().top >= label.getBoundingClientRect().bottom - 2;
    })()`),
  );

  log('\n== 6. 手机上编辑页的表格可以横滑 ==');
  await send('Page.navigate', { url: `${BASE}/items/${sample.itemId}` });
  const hasTable = await waitFor('编辑页表格', async () =>
    await evaluate(`!!document.querySelector('table')`),
  );
  if (!hasTable) {
    log('   这个条目没有表格（字段区可能还没渲染），跳过');
  } else {
    check(
      '表格在手机上是可横滑的块',
      true,
      await evaluate(`getComputedStyle(document.querySelector('table')).display === 'block'`),
    );
  }

  log('\n== 7. 平板与桌面截图 ==');
  await setViewport(VIEWPORTS[1]);
  await send('Page.navigate', { url: `${BASE}/` });
  await waitFor('首页', async () => await evaluate(`!!document.querySelector('.hero')`));
  await sleep(600);
  await shot('03-home-tablet');
  await setViewport(VIEWPORTS[2]);
  await send('Page.navigate', { url: `${BASE}/` });
  await waitFor('首页', async () => await evaluate(`!!document.querySelector('.hero')`));
  await sleep(600);
  await shot('04-home-desktop');

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
