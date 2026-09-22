// M6「播放列表 / 合集」的界面验收（CDP 直连 headless Chrome）
//
// 走一条完整的用户路径：
//   我的列表 → 新建列表 → 在两个影片详情页「加入列表」→ 打开列表看条目 →
//   调整顺序（↑↓）→ 「播放全部」→ 播放器里「下一项」真的换片且 URL 带着 list →
//   回列表移出条目 → 删除列表。
//
// 期望值都从接口现取或与接口核对；列表名带时间戳，跑完自己删干净。
//
// 用法：
//   BASE=http://192.168.x.x:8099 LMBY_USER=devtest LMBY_PASS=口令 node lists-ui-test.mjs
import { appendFileSync, mkdirSync, writeFileSync } from 'node:fs';
import { spawn } from 'node:child_process';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';

const REPORT = 'lists-ui-report.txt';
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
const OUT = process.env.OUT || 'shots-lists';
const PORT = Number(process.env.CDP_PORT || 9747 + (process.pid % 200));
const LIST_NAME = `验收列表 ${Date.now()}`;

mkdirSync(OUT, { recursive: true });
const profile = path.join(os.tmpdir(), `lmby-lists-${Date.now()}`);

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
        // 删除列表会弹 confirm：验收里一律「确定」（要验的就是删得掉）
        log('   （自动确认弹窗）');
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
// 在页面里发一个带 cookie 的 GET。
// 总是加一个时间戳（`_=`）：浏览器对没有缓存头的 JSON 会做启发式缓存，
// 轮询「服务端跟上没有」时可能一直拿到同一份旧响应（本地开发环境真踩到过）。
const apiGet = (p) => `(async () => {
  const url = ${JSON.stringify(p)} + (${JSON.stringify(p)}.includes('?') ? '&' : '?') + '_=' + Date.now();
  const r = await fetch(url, { credentials: 'same-origin' });
  return await r.json();
})()`;
const setInput = (sel, val) => `(() => {
  const el = document.querySelector(${JSON.stringify(sel)});
  if (!el) return null;
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(el, ${JSON.stringify(val)});
  el.dispatchEvent(new Event('input', { bubbles: true }));
  return el.value;
})()`;
const nav = (u) => send('Page.navigate', { url: u });
const onPath = async (p) => (await evaluate('location.pathname')) === p;

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
  await nav(`${BASE}/login`);
  await waitFor('登录页', async () => onPath('/login'));
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
  check('登录成功', true, await waitFor('首页', async () => onPath('/')));

  log('\n== 2. 导航到「我的列表」并新建 ==');
  await evaluate(clickSel('.nav a[href="/lists"]'));
  check('进入我的列表页', true, await waitFor('列表页', async () => onPath('/lists')));
  check(
    '页面就绪（有新建表单）',
    true,
    await waitFor('新建表单', async () => await evaluate(`!!document.querySelector('.card form input')`)),
  );
  await evaluate(setInput('.card form input', LIST_NAME));
  await evaluate(clickSel('.card form button[type="submit"]'));
  check(
    '新建的列表出现在页面上',
    true,
    await waitFor('列表卡片', async () =>
      await evaluate(`[...document.querySelectorAll('.list-name')].some((a) => a.textContent === ${JSON.stringify(
        LIST_NAME,
      )})`),
    ),
  );
  const lists = await evaluate(apiGet('/api/v1/playlists'));
  const mine = (lists.playlists || []).find((p) => p.name === LIST_NAME);
  check('接口里也能查到新列表', true, !!mine);
  const listId = mine?.id ?? 0;
  log(`   新列表 id=${listId}，名字「${LIST_NAME}」`);

  log('\n== 3. 空列表的引导文案 ==');
  await nav(`${BASE}/list/${listId}`);
  check('打开列表详情页', true, await waitFor('列表详情页', async () => onPath(`/list/${listId}`)));
  check('空列表给了引导文案', true, await waitFor('空状态', async () =>
    await evaluate(`/详情页|加入列表/.test(document.querySelector('.content')?.textContent || '')`)));
  check(
    '空列表时「播放全部」不可点',
    true,
    await evaluate(`(() => {
      const b = document.querySelector('[data-play-all]');
      if (b) return false;
      const btn = [...document.querySelectorAll('.card button')].find((e) => e.textContent.includes('列表是空的'));
      return !!btn && btn.disabled;
    })()`),
  );

  log('\n== 4. 在两个影片详情页加入列表 ==');
  const picks = await evaluate(`(async () => {
    const libs = await (await fetch('/api/v1/libraries', { credentials: 'same-origin' })).json();
    for (const lib of libs.libraries || []) {
      const page = await (await fetch('/api/v1/libraries/' + lib.id + '/items?kind=movie&limit=5',
        { credentials: 'same-origin' })).json();
      const its = (page.items || []).slice(0, 2).map((x) => ({ id: x.id, title: x.title }));
      if (its.length >= 2) return its;
    }
    return [];
  })()`);
  if (picks.length < 2) throw new Error('库里至少要有两部电影');
  log(`   样本：${picks.map((p) => `${p.id}《${p.title}》`).join(' / ')}`);

  for (const p of picks) {
    await nav(`${BASE}/item/${p.id}`);
    check(`条目 ${p.id} 的详情页就绪`, true, await waitFor('详情页', async () =>
      await evaluate(`!!document.querySelector('[data-add-to-list]')`)));
    await evaluate(clickSel('[data-add-to-list]'));
    check(
      '「加入列表」面板打开并列出我的列表',
      true,
      await waitFor('列表面板', async () =>
        await evaluate(`!!document.querySelector('[data-list-picker] [data-add-to="${listId}"]')`),
      ),
    );
    await evaluate(clickSel(`[data-add-to="${listId}"]`));
    check(
      '加入成功有回执',
      true,
      await waitFor('回执', async () =>
        await evaluate(`/已加入|本来就有/.test(document.querySelector('[data-list-picker]')?.textContent || '')`),
      ),
    );
  }

  log('\n== 5. 列表里看到两条 + 顺序调整 ==');
  await nav(`${BASE}/list/${listId}`);
  await waitFor('列表详情页', async () => onPath(`/list/${listId}`));
  check(
    '列表里有 2 个条目',
    2,
    await waitFor('两行卡片', async () =>
      (await evaluate(`document.querySelectorAll('.list-item-card').length`)) === 2,
    ) && (await evaluate(`document.querySelectorAll('.list-item-card').length`)),
  );
  check(
    '卡片指向详情页',
    true,
    await evaluate(`[...document.querySelectorAll('.list-item-card .search-card')]
      .every((a) => /^\\/item\\/\\d+$/.test(a.getAttribute('href')))`),
  );
  check(
    '「播放全部」带着 list 参数',
    true,
    (await evaluate(`document.querySelector('[data-play-all]')?.getAttribute('href') || ''`)).includes(
      `?list=${listId}`,
    ),
  );
  await shot('01-list-detail');

  const before = await evaluate(
    `[...document.querySelectorAll('.list-item-card')].map((e) => Number(e.dataset.item))`,
  );
  await evaluate(`(() => {
    const btn = [...document.querySelectorAll('.card button')].find((b) => b.textContent.includes('调整顺序'));
    if (!btn) return false;
    btn.click();
    return true;
  })()`);
  await waitFor('进入排序模式', async () =>
    await evaluate(`!!document.querySelector('.list-item-card [aria-label="下移"]')`),
  );
  await evaluate(clickSel('.list-item-card [aria-label="下移"]'));
  const after = await evaluate(
    `[...document.querySelectorAll('.list-item-card')].map((e) => Number(e.dataset.item))`,
  );
  log(`   顺序：${before.join(',')} → ${after.join(',')}`);
  check('界面上前两条换了位置', true, after[0] === before[1] && after[1] === before[0]);
  const apiOrder = await waitFor('服务端顺序跟上界面', async () => {
    const res = await evaluate(apiGet(`/api/v1/playlists/${listId}/items`));
    return JSON.stringify((res.items || []).map((x) => x.id)) === JSON.stringify(after);
  });
  check('服务端顺序与界面一致（重排是真落库的）', true, apiOrder);

  log('\n== 6. 播放全部 → 播放器里的「下一项」 ==');
  await evaluate(clickSel('[data-play-all]'));
  check('进了播放器', true, await waitFor('播放器', async () =>
    (await evaluate('location.pathname')).startsWith('/play/')));
  const playUrl = await evaluate('location.pathname + location.search');
  log(`   播放地址：${playUrl}`);
  check('播放地址带着 list 参数', true, playUrl.includes(`list=${listId}`));
  check(
    '播放器显示了队列位置',
    true,
    await waitFor('队列徽标', async () =>
      await evaluate(`!!document.querySelector('[data-queue]')`),
    ),
  );
  check(
    '队列徽标是 (1/2)',
    true,
    (await evaluate(`document.querySelector('[data-queue]')?.textContent || ''`)).includes('1/2'),
  );
  check(
    '第一条没有「上一项」',
    true,
    await evaluate(`document.querySelector('[data-queue-prev]')?.disabled === true`),
  );
  check(
    '「下一项」可点',
    true,
    await evaluate(`document.querySelector('[data-queue-next]')?.disabled === false`),
  );
  await shot('02-player-queue');

  await evaluate(clickSel('[data-queue-next]'));
  check('点「下一项」换了片', true, await waitFor('第二条', async () =>
    (await evaluate('location.pathname')) === `/play/${after[1]}`));
  check(
    '换片后仍然带着 list 参数',
    true,
    (await evaluate('location.search')).includes(`list=${listId}`),
  );
  check(
    '队列徽标变成 (2/2)',
    true,
    await waitFor('队列 2/2', async () =>
      (await evaluate(`document.querySelector('[data-queue]')?.textContent || ''`)).includes('2/2'),
    ),
  );
  check(
    '最后一条的「下一项」不可点',
    true,
    await evaluate(`document.querySelector('[data-queue-next]')?.disabled === true`),
  );
  check(
    '「上一项」可点',
    true,
    await evaluate(`document.querySelector('[data-queue-prev]')?.disabled === false`),
  );

  log('\n== 7. 移出条目 / 删除列表 ==');
  await nav(`${BASE}/list/${listId}`);
  await waitFor('列表详情页', async () => onPath(`/list/${listId}`));
  await waitFor('两行卡片', async () =>
    (await evaluate(`document.querySelectorAll('.list-item-card').length`)) === 2,
  );
  await evaluate(`document.querySelector('.list-item-card .list-item-actions button:last-child').click()`);
  check(
    '移出后只剩 1 个条目',
    true,
    await waitFor('剩一条', async () =>
      (await evaluate(`document.querySelectorAll('.list-item-card').length`)) === 1,
    ),
  );
  const afterRemove = await evaluate(apiGet(`/api/v1/playlists/${listId}/items`));
  check('服务端也只剩 1 条', 1, afterRemove.total);

  await nav(`${BASE}/lists`);
  await waitFor('列表页', async () => onPath('/lists'));
  // ⚠️ 必须先等卡片真的渲染出来再点：刚 nav 过来时 DOM 还是空的，
  // 直接点会「什么都没点到」，而随后的「卡片消失了吗」会**真空通过**（本次真踩到）
  check(
    '列表页重新渲染出了这一条',
    true,
    await waitFor('我的列表卡片', async () =>
      await evaluate(`!!document.querySelector('.list-card[data-list="${listId}"]')`),
    ),
  );

  const clickDelete = `(() => {
    const card = document.querySelector('.list-card[data-list="${listId}"]');
    if (!card) return 'no-card';
    const del = [...card.querySelectorAll('button')].find((b) => b.textContent.trim() === '删除');
    if (!del) return 'no-button';
    del.click();
    return 'clicked';
  })()`;

  // 先验「取消确认」：什么都不该发生（删除是有副作用的动作，得给个后悔的机会）
  await evaluate('window.confirm = () => false');
  log(`   删除按钮：${await evaluate(clickDelete)}（confirm 被替为「取消」）`);
  await sleep(400);
  check(
    '取消确认时列表还在（接口）',
    true,
    ((await evaluate(apiGet('/api/v1/playlists'))).playlists || []).some((p) => p.id === listId),
  );

  // 再验真删。headless 里拿不到真的对话框点击（而且确认框有同步语义），
  // 所以直接替掉 confirm —— 验的是「确认之后到底删不删」，不是浏览器会不会弹框。
  await evaluate('window.confirm = () => true');
  log(`   删除按钮：${await evaluate(clickDelete)}（confirm 被替为「确定」）`);
  check(
    '确认后页面上没有了',
    true,
    await waitFor('列表消失', async () =>
      await evaluate(`!document.querySelector('.list-card[data-list="${listId}"]')`),
    ),
  );
  const afterDelete = await evaluate(apiGet('/api/v1/playlists'));
  check(
    '接口里也没了',
    true,
    !(afterDelete.playlists || []).some((p) => p.id === listId),
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
