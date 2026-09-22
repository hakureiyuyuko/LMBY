// 用户与权限（M7）界面验收：设置菜单里的「用户」页签能不能真的用起来。
//
// 为什么要有这一条：`/settings/users` 的路由与页面在 M7 就写好了，但**设置页签栏里
// 从来没有指向它的链接** —— 接口验收（verify-users.sh 52/52）全绿，管理员却在界面上
// 根本找不到入口。所以这里第一件事就是断言「入口存在」，再往下走完整条交互链。
//
// 第二条要钉的规矩：**界面上不该出现点了就 403 的入口**。
//   · 管理员不该能给自己改管理员位 / 禁用 / 删除（按钮必须是 disabled）；
//   · 非管理员在设置页看不到「用户」页签（后端 requireAdmin 只是兜底）。
//
// 必须在同一次运行里「起浏览器 → 跑断言 → 关浏览器」，否则后台 Chrome 会被回收。
//
// 用法：
//   BASE=http://192.168.x.x:8099 LMBY_USER=devtest LMBY_PASS=口令 node users-ui-test.mjs
// 可选：
//   CHROME   Chrome 路径
//   OUT      截图目录（默认 shots-users）
import { appendFileSync, mkdirSync, writeFileSync } from 'node:fs';
import { spawn } from 'node:child_process';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';

const REPORT = 'users-ui-report.txt';
writeFileSync(REPORT, '');
const log = (m) => appendFileSync(REPORT, m + '\n');

const CHROME = process.env.CHROME || 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const BASE = process.env.BASE || 'http://127.0.0.1:8099';
const USER = process.env.LMBY_USER || 'devtest';
const PASS = process.env.LMBY_PASS;
if (!PASS) throw new Error('请通过环境变量提供测试账号口令：LMBY_PASS=xxx（配合 LMBY_USER）');
const OUT = process.env.OUT || 'shots-users';
const PORT = Number(process.env.CDP_PORT || 9800 + (process.pid % 150));

mkdirSync(OUT, { recursive: true });
const profile = path.join(os.tmpdir(), `lmby-users-${Date.now()}`);

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
      // 删用户会弹 confirm：弹窗不处理的话页面会一直挂着（后面的断言全超时）
      if (msg.method === 'Page.javascriptDialogOpening') {
        log(`--   （自动确认浏览器弹窗）${msg.params?.message || ''}`);
        send('Page.handleJavaScriptDialog', { accept: true }).catch(() => {});
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
async function waitFor(desc, fn, timeoutMs = 20000) {
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
  await sleep(200);
  await send('Emulation.setDeviceMetricsOverride', {
    width: 1280,
    height: 1000,
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

/** 按「行头里的用户名字」定位那一行。
 *  ⚠️ 不能只看 `.user-name`：那里显示的是**显示名**（displayName || username），
 *  而用户名只在行头的副文本里 —— 按显示名找新建的用户永远找不到（真踩到）。 */
const rowByName = (name) => `[...document.querySelectorAll('.user-row')].find((r) =>
  ((r.querySelector('.user-head')?.textContent || '')).includes(${JSON.stringify(name)}))`;

/** 某个用户行里、某个 label 文案对应的 checkbox 状态（找不到返回 null）。 */
const toggleOf = (name, label) => `(() => {
  const row = ${rowByName(name)};
  if (!row) return null;
  const lab = [...row.querySelectorAll('label')].find((l) => (l.textContent || '').trim().startsWith(${JSON.stringify(
    label,
  )}));
  if (!lab) return null;
  const cb = lab.querySelector('input[type=checkbox]');
  return cb ? { checked: cb.checked, disabled: cb.disabled } : null;
})()`;

/** 点某个用户行里的按钮（按文案前缀）。 */
const clickRowButton = (name, label) => `(() => {
  const row = ${rowByName(name)};
  if (!row) return false;
  const b = [...row.querySelectorAll('button')].find((x) => (x.textContent || '').trim().startsWith(${JSON.stringify(
    label,
  )}));
  if (!b) return false;
  b.click();
  return true;
})()`;

/** 点某个用户行里、某个 label 文案对应的 checkbox（开关）。 */
const clickLabelToggle = (name, labelPrefix) => `(() => {
  const row = ${rowByName(name)};
  if (!row) return false;
  const lab = [...row.querySelectorAll('label')].find((l) => (l.textContent || '').trim().startsWith(${JSON.stringify(
    labelPrefix,
  )}));
  if (!lab) return false;
  const cb = lab.querySelector('input[type=checkbox]');
  if (!cb) return false;
  cb.click();
  return true;
})()`;

/** 该行「只给勾选的媒体库」下面那批库勾选框里的第一个 label（顺带作为「库列表出来了」的判据）。 */
const libLabelExpr = (name) => `(() => {
  const row = ${rowByName(name)};
  if (!row) return null;
  const body = row.querySelector('.user-body');
  if (!body) return null;
  return [...body.querySelectorAll('label')].find((l) =>
    l.querySelector('input[type=checkbox]') && !/只给勾选|允许|管理员|禁用/.test(l.textContent)) || null;
})()`;

/** 展开 / 收起某个用户行（点行头）。 */
const toggleRow = (name) => `(() => {
  const row = ${rowByName(name)};
  if (!row) return false;
  row.querySelector('.user-head').click();
  return true;
})()`;

/** 新建表单里按 label 文案填 input。 */
const setFormField = (labelPrefix, val) => `(() => {
  const lab = [...document.querySelectorAll('.card label')].find((l) =>
    (l.textContent || '').trim().startsWith(${JSON.stringify(labelPrefix)}));
  if (!lab) return null;
  const el = lab.querySelector('input');
  if (!el) return null;
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(el, ${JSON.stringify(
    String(val),
  )});
  el.dispatchEvent(new Event('input', { bubbles: true }));
  return el.value;
})()`;

async function login(u, p) {
  // 已登录时 /login 会被重定向回首页（拿不到登录表单）——
  // 所以先落一个页、调一次 logout，再进登录页。第一次登录时这个 logout 会 401，无碍。
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
  // 界面语言钉成中文：i18n 按 navigator.language 探测，headless Chrome 默认 en-US。
  // ⚠️ 用 localStorage + reload 钉（Emulation.setLocaleOverride 只影响 Intl，实测无效）。
  await send('Page.navigate', { url: `${BASE}/login` });
  await sleep(900);
  await evaluate(`localStorage.setItem('lmby.lang', 'zh-CN')`);
  await send('Page.reload');
  await sleep(900);

  const suffix = String(Date.now()).slice(-6);
  const NAME = `uitest${suffix}`;
  const NEWPW = `uitest-pass-${suffix}`;

  log('\n== 1. 管理员登录 ==');
  check('登录成功', true, await login(USER, PASS));

  log('\n== 2. 设置菜单里有「用户」入口 ==');
  await evaluate(clickSel('.nav a[href="/settings"]'));
  check('进入设置页', true, await waitFor('设置页', async () => (await evaluate('location.pathname')) === '/settings'));
  check(
    '页签栏存在',
    true,
    await waitFor('页签栏', async () => await evaluate(`!!document.querySelector('[data-settings-tabs]')`)),
  );
  const tabs = await evaluate(
    `[...document.querySelectorAll('[data-settings-tabs] a')].map((a) => ({ href: a.getAttribute('href'), label: a.textContent.trim() }))`,
  );
  note(`页签：${tabs.map((x) => x.label).join(' / ')}`);
  check(
    '页签里有「用户」且指向 /settings/users',
    true,
    tabs.some((x) => x.href === '/settings/users' && x.label === '用户'),
  );
  await evaluate(clickSel('[data-settings-tabs] a[href="/settings/users"]'));
  check(
    '点进去就是用户页',
    true,
    await waitFor('用户页', async () => (await evaluate('location.pathname')) === '/settings/users'),
  );
  check(
    '用户列表渲染出来',
    true,
    await waitFor('用户列表', async () => (await evaluate(`document.querySelectorAll('.user-row').length`)) > 0),
  );
  await shot('01-users-tab');

  log('\n== 3. 自己那一行：不该出现「点了就失败」的按钮 ==');
  const meName = await evaluate(`(() => {
    const row = [...document.querySelectorAll('.user-row')].find((r) =>
      (r.querySelector('.user-head')?.textContent || '').includes('这是你'));
    return row ? row.querySelector('.user-name').textContent.trim() : null;
  })()`);
  check('列表里有一行标着「这是你」', true, !!meName);
  note(`当前账号显示名：${meName}`);
  const meHead = await evaluate(`(() => {
    const row = ${rowByName(meName || '')};
    return row ? row.querySelector('.user-head').textContent.replace(/\\s+/g, ' ').trim() : '';
  })()`);
  check('自己那行带「管理员」徽标', true, meHead.includes('管理员'));
  await evaluate(toggleRow(meName || ''));
  const selfAdmin = await evaluate(toggleOf(meName || '', '管理员'));
  const selfDisabled = await evaluate(toggleOf(meName || '', '禁用这个账号'));
  check('「管理员」开关存在且对自己禁用', { checked: true, disabled: true }, selfAdmin);
  check('「禁用这个账号」对自己禁用', { checked: false, disabled: true }, selfDisabled);
  const selfDelDisabled = await evaluate(
    `(() => { const row = ${rowByName(meName || '')}; const b = [...row.querySelectorAll('button')].find((x) => x.textContent.trim() === '删除用户'); return b ? b.disabled : null; })()`,
  );
  check('「删除用户」对自己禁用', true, selfDelDisabled);
  await evaluate(toggleRow(meName || ''));

  log('\n== 4. 新建用户（非管理员） ==');
  await evaluate(
    `(() => { const b = [...document.querySelectorAll('button')].find((x) => x.textContent.includes('新建用户')); if (!b) return false; b.click(); return true; })()`,
  );
  await waitFor('新建表单', async () => !!(await evaluate(setFormField('用户名（登录用', NAME))));
  await evaluate(setFormField('显示名（可留空）', `验收 ${suffix}`));
  await evaluate(setFormField('口令（至少 8 位）', NEWPW));
  await evaluate(
    `(() => { const b = [...document.querySelectorAll('button')].find((x) => x.textContent.trim() === '创建'); if (!b) return false; b.click(); return true; })()`,
  );
  check(
    '新用户出现在列表里',
    true,
    await waitFor('新用户行', async () => !!(await evaluate(`!!(${rowByName(NAME)})`))),
  );
  // 失败时把现场打出来：创建表单填了什么、服务端报了什么错 ——
  // 否则只能看到一句「新用户没出现」，查起来就是猜。
  await sleep(800);
  const createDiag = await evaluate(`(() => {
    const alerts = [...document.querySelectorAll('.alert')].map((a) => a.textContent.trim());
    const form = [...document.querySelectorAll('.card')].find((c) => (c.textContent || '').includes('用户名（登录用'));
    const vals = form
      ? [...form.querySelectorAll('input')].map((i) => ({ type: i.type, value: i.value, checked: i.checked }))
      : null;
    return { alerts, vals, rows: document.querySelectorAll('.user-row').length };
  })()`);
  note(`创建后现场：${JSON.stringify(createDiag)}`);
  const newHead = await evaluate(
    `(() => { const row = ${rowByName(NAME)}; return row ? row.querySelector('.user-head').textContent.replace(/\\s+/g, ' ').trim() : ''; })()`,
  );
  check('新用户默认「全部库」+ 允许转码 + 允许直播', true, newHead.includes('全部库'));
  check('新用户默认不是管理员', false, newHead.includes('管理员'));

  log('\n== 5. 权限开关与按库授权（真点、真落库） ==');
  await evaluate(toggleRow(NAME));
  await waitFor('新用户行展开', async () => !!(await evaluate(toggleOf(NAME, '允许转码'))));
  const trans = await evaluate(toggleOf(NAME, '允许转码'));
  check('「允许转码」默认勾上', true, trans?.checked);
  await evaluate(clickLabelToggle(NAME, '允许转码'));
  check(
    '取消「允许转码」后行摘要出现「禁止转码」',
    true,
    await waitFor('摘要更新', async () =>
      (await evaluate(`(() => { const row = ${rowByName(NAME)}; return !!row && row.querySelector('.user-head').textContent.includes('禁止转码'); })()`)),
    ),
  );
  // 改回允许，免得后面那个账号看直播/转码被限制（这一步同时验了开关的双向）
  await evaluate(clickLabelToggle(NAME, '允许转码'));
  check(
    '再勾回来，摘要里的「禁止转码」消失',
    true,
    await waitFor('摘要复原', async () =>
      (await evaluate(`(() => { const row = ${rowByName(NAME)}; return !!row && !row.querySelector('.user-head').textContent.includes('禁止转码'); })()`)),
    ),
  );

  await evaluate(clickLabelToggle(NAME, '只给勾选的媒体库'));
  const libBoxes = await waitFor('出现媒体库勾选框', async () =>
    !!(await evaluate(`!!(${libLabelExpr(NAME)})`)),
  );
  check('勾了「只给勾选的媒体库」后列出库', true, libBoxes);
  const firstLib = await evaluate(
    `(() => { const l = ${libLabelExpr(NAME)}; return l ? l.textContent.trim() : null; })()`,
  );
  note(`给这个用户勾上库：${firstLib}`);
  await evaluate(
    `(() => { const l = ${libLabelExpr(NAME)}; if (!l) return false; l.querySelector('input').click(); return true; })()`,
  );
  await evaluate(clickRowButton(NAME, '保存可见库'));
  check(
    '保存可见库后给出提示',
    true,
    await waitFor('授权提示', async () =>
      (await evaluate(
        `([...document.querySelectorAll('.alert')].map((a) => a.textContent).join(' ')).includes('已保存库授权')`,
      )),
    ),
  );
  check(
    '行摘要变成「1 个库」',
    true,
    await waitFor('摘要变按库', async () =>
      (await evaluate(`(() => { const row = ${rowByName(NAME)}; return !!row && row.querySelector('.user-head').textContent.includes('1 个库'); })()`)),
    ),
  );
  await shot('02-user-perms');

  log('\n== 6. 非管理员：压根看不到这些入口 ==');
  check('用新账号登录', true, await login(NAME, NEWPW));
  // 顶栏的「设置」按项目规矩只对管理员渲染（接口 requireAdmin 只是兜底）——
  // 所以普通用户不应该看到这个入口，而不是「点进去看到一句无权限」。
  check(
    '非管理员顶栏没有「设置」入口',
    0,
    await evaluate(`document.querySelectorAll('.nav a[href="/settings"]').length`),
  );
  // 直接敲地址：界面也不该渲染出用户列表
  await send('Page.navigate', { url: `${BASE}/settings/users` });
  await sleep(1200);
  check('直接敲 /settings/users 也看不到用户列表', 0, await evaluate(`document.querySelectorAll('.user-row').length`));
  check(
    '页面上给出「只有管理员能改」的解释',
    true,
    await waitFor('非管理员提示', async () =>
      (await evaluate(`(document.body.innerText || '').includes('只有管理员能改全站设置')`)),
    ),
  );
  await shot('03-non-admin');

  log('\n== 7. 回到管理员删掉临时用户 ==');
  check('管理员重新登录', true, await login(USER, PASS));
  await send('Page.navigate', { url: `${BASE}/settings/users` });
  await waitFor('用户列表', async () => (await evaluate(`document.querySelectorAll('.user-row').length`)) > 0);
  await evaluate(toggleRow(NAME));
  await waitFor('行展开', async () => !!(await evaluate(toggleOf(NAME, '允许转码'))));
  await evaluate(clickRowButton(NAME, '删除用户'));
  check(
    '删除后这一行没了',
    true,
    await waitFor('行消失', async () => !(await evaluate(`!!(${rowByName(NAME)})`))),
  );

  log('\n== 8. 页面没有报错 ==');
  const errs = [...new Set(pageErrors)];
  check('没有页面异常 / console.error', 0, errs.length);
  if (errs.length) log(`   ${JSON.stringify(errs.slice(0, 5))}`);

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
