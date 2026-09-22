// M1 界面验收（精简版）：登录 → 媒体库页 → 截图 → 触发扫描 → 截图。
// 断言结果写入 m1-ui-report.txt（不依赖 shell 重定向，避免输出丢失）。
import { spawn } from 'node:child_process';
import { appendFileSync, mkdirSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';

const REPORT = 'm1-ui-report.txt';
writeFileSync(REPORT, '');
const log = (m) => appendFileSync(REPORT, m + '\n');

const CHROME = process.env.CHROME || 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const BASE = process.env.BASE || 'http://127.0.0.1:8099';
const USER = process.env.LMBY_USER || 'devtest';
// 口令必须从环境变量传入：这是公开仓库，不应携带任何可用凭据。
const PASS = process.env.LMBY_PASS;
if (!PASS) {
  throw new Error('请通过环境变量提供测试账号口令：LMBY_PASS=xxx（配合 LMBY_USER）');
}
const OUT = 'shots-m1';
const PORT = 9445;

mkdirSync(OUT, { recursive: true });
const profile = path.join(os.tmpdir(), `lmby-m1-${Date.now()}`);

const chrome = spawn(
  CHROME,
  [
    '--headless=new',
    `--remote-debugging-port=${PORT}`,
    `--user-data-dir=${profile}`,
    '--no-first-run',
    '--no-default-browser-check',
    '--disable-extensions',
    '--hide-scrollbars',
    '--window-size=1400,1000',
    'about:blank',
  ],
  { stdio: ['ignore', 'ignore', 'ignore'] },
);

let pass = 0;
let fail = 0;
const check = (name, ok, extra = '') => {
  if (ok) {
    pass++;
    log(`ok   ${name}`);
  } else {
    fail++;
    log(`FAIL ${name} ${extra}`);
  }
};

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
    }, 30000);
  });
}
async function evaluate(expression) {
  const r = await send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
  if (r.exceptionDetails) throw new Error('页面异常: ' + (r.exceptionDetails.text || ''));
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
const setInput = (i, val) => `(() => {
  const el = document.querySelectorAll('.center-card input')[${i}];
  if (!el) return null;
  const s = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
  s.call(el, ${JSON.stringify(val)});
  el.dispatchEvent(new Event('input', { bubbles: true }));
  return el.value;
})()`;
const clickSel = (sel) => `(() => { const e = document.querySelector(${JSON.stringify(sel)}); if (!e) return false; e.click(); return true; })()`;
const clickText = (t) => `(() => {
  const e = [...document.querySelectorAll('.btn')].find((x) => x.textContent.trim() === ${JSON.stringify(t)});
  if (!e) return false;
  e.click();
  return true;
})()`;
const content = `(document.querySelector('.content')?.textContent || '')`;

async function main() {
  const ver = await waitDevtools();
  log(`Chrome ${ver.Browser} → ${BASE}`);

  const tab = await (await fetch(`http://127.0.0.1:${PORT}/json/new?about:blank`, { method: 'PUT' })).json();
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
  await send('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-color-scheme', value: 'dark' }] });

  await send('Page.navigate', { url: `${BASE}/login` });
  await waitFor('登录页', async () => (await evaluate('location.pathname')) === '/login');
  // 必须等表单真的渲染出来：否则 querySelectorAll 返回空，填值静默失败
  check(
    '登录表单已渲染',
    await waitFor('登录表单', async () => (await evaluate('document.querySelectorAll(".center-card input").length')) >= 2),
  );
  await evaluate(setInput(0, USER));
  await evaluate(setInput(1, PASS));
  await evaluate(clickSel('.center-card button[type="submit"]'));
  check('登录成功进入首页', await waitFor('首页', async () => (await evaluate('location.pathname')) === '/'));

  // 库管理现在是设置的子页签（M6 收尾把三个导航项收进设置）
  await evaluate(clickSel('.nav a[href="/settings"]'));
  await waitFor('管理面页签', async () =>
    (await evaluate(`!!document.querySelector('[data-settings-tabs] a[href="/settings/libraries"]')`)) === true);
  await evaluate(clickSel('[data-settings-tabs] a[href="/settings/libraries"]'));
  check('进入库管理页', await waitFor('库管理页', async () => (await evaluate('location.pathname')) === '/settings/libraries'));
  check(
    '渲染出媒体库卡片',
    await waitFor('卡片', async () => (await evaluate(content)).includes('验证库')),
  );

  const body = await evaluate(content);
  check('显示库名', body.includes('验证库（子集）'));
  check('显示条目统计', body.includes('电影') && body.includes('剧集'));
  check('显示图片计数', body.includes('图片'));
  check('显示根路径', body.includes('/mnt/media/'));
  check(
    '显示扫描记录',
    await waitFor('扫描记录', async () => (await evaluate(content)).includes('扫描记录')),
  );
  const body2 = await evaluate(content);
  check('显示扫描统计', body2.includes('视频') && body2.includes('未变'));
  check('显示读取 nfo 数量', body2.includes('读取 nfo'));
  check(
    '条目表有数据行',
    await waitFor('条目行', async () => (await evaluate('document.querySelectorAll("table tbody tr").length')) > 3),
  );
  await shot('01-libraries-dark');

  check('剧集筛选可用', await evaluate(clickText('剧集')));
  await sleep(900);
  const seriesBody = await evaluate(content);
  check('筛选后显示剧集名', seriesBody.includes('钢之炼金术师'));
  await shot('02-series-filter');

  check('触发扫描', await evaluate(clickText('扫描')));
  await waitFor(
    '扫描进度出现',
    async () => {
      const t = await evaluate(content);
      return t.includes('扫描中') || t.includes('正在遍历');
    },
    20000,
  );
  const scanning = await evaluate(content);
  check('界面显示实时进度', scanning.includes('扫描中') || scanning.includes('正在遍历'));
  await shot('03-scan-progress');

  // 不在这里等扫描跑完（网盘上可能要几分钟，会拖垮整个脚本超时）；
  // 扫描本身的正确性由 verify-m1.sql 在数据库侧验证。
  await sleep(3000);
  const after = await evaluate(content);
  check('扫描过程中界面持续更新', after.includes('扫描中') || after.includes('正在遍历') || after.includes('已完成'));
  await shot('04-scan-running');

  await evaluate(clickSel('.theme-toggle'));
  await sleep(400);
  check('切到亮色主题', (await evaluate('document.documentElement.dataset.theme')) === 'light');
  await shot('05-libraries-light');

  log(`结果：${pass} 通过，${fail} 失败`);
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
