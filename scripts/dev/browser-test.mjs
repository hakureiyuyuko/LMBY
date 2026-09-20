// LMBY 浏览器端到端测试（CDP 直连 headless Chrome，不依赖 Puppeteer）
//
// 必须在同一次运行里「起浏览器 → 跑断言 → 关浏览器」，否则后台 Chrome 会被回收。
//
// 用法：
//   BASE=http://127.0.0.1:8099 node browser-test.mjs
import { spawn } from 'node:child_process';
import { mkdirSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';

const CHROME =
  process.env.CHROME || 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const BASE = process.env.BASE || 'http://127.0.0.1:8099';
const OUT = process.env.OUT || 'shots';
const PORT = Number(process.env.CDP_PORT || 9333);

mkdirSync(OUT, { recursive: true });
const profile = path.join(os.tmpdir(), `lmby-chrome-${Date.now()}`);

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
    '--hide-scrollbars',
    '--window-size=1280,900',
    'about:blank',
  ],
  { stdio: ['ignore', 'ignore', 'pipe'] },
);
let chromeStderr = '';
chrome.stderr.on('data', (d) => {
  chromeStderr += d.toString();
});

let pass = 0;
let fail = 0;
function check(name, expected, actual) {
  const ok = JSON.stringify(expected) === JSON.stringify(actual);
  if (ok) {
    pass++;
    console.log(`  \x1b[32mok\x1b[0m   ${name}`);
  } else {
    fail++;
    console.log(
      `  \x1b[31mFAIL\x1b[0m ${name}（期望 ${JSON.stringify(expected)}，实际 ${JSON.stringify(actual)}）`,
    );
  }
}
function note(msg) {
  console.log(`  \x1b[33m--\x1b[0m   ${msg}`);
}

// ---------------------------------------------------------------- CDP 基础

async function waitDevtools() {
  for (let i = 0; i < 80; i++) {
    try {
      const r = await fetch(`http://127.0.0.1:${PORT}/json/version`);
      if (r.ok) return await r.json();
    } catch {
      /* 还没起来 */
    }
    await sleep(250);
  }
  throw new Error('Chrome DevTools 未就绪：' + chromeStderr.slice(0, 600));
}

let ws;
let msgId = 0;
const pending = new Map();

function connect(url) {
  return new Promise((resolve, reject) => {
    ws = new WebSocket(url);
    ws.onopen = () => resolve();
    ws.onerror = () => reject(new Error('CDP WebSocket 连接失败'));
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
  const id = ++msgId;
  return new Promise((resolve, reject) => {
    pending.set(id, { resolve, reject });
    ws.send(JSON.stringify({ id, method, params }));
    setTimeout(() => {
      if (pending.has(id)) {
        pending.delete(id);
        reject(new Error('CDP 调用超时: ' + method));
      }
    }, 30000);
  });
}

async function evaluate(expression) {
  const r = await send('Runtime.evaluate', {
    expression,
    returnByValue: true,
    awaitPromise: true,
  });
  if (r.exceptionDetails) {
    throw new Error(
      '页面执行出错: ' +
        (r.exceptionDetails.exception?.description || r.exceptionDetails.text || 'unknown'),
    );
  }
  return r.result.value;
}

async function waitFor(desc, fn, timeoutMs = 20000) {
  const start = Date.now();
  let lastErr;
  while (Date.now() - start < timeoutMs) {
    try {
      if (await fn()) return;
    } catch (e) {
      lastErr = e;
    }
    await sleep(150);
  }
  throw new Error(`等待超时: ${desc}${lastErr ? ' (' + lastErr.message + ')' : ''}`);
}

async function screenshot(name) {
  const r = await send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: true });
  const file = path.join(OUT, `${name}.png`);
  writeFileSync(file, Buffer.from(r.data, 'base64'));
  note(`截图 ${file}`);
}

/** React 受控组件必须走原生 setter + input 事件，直接改 .value 不会触发 onChange。 */
const setInput = (selector, value) => `(() => {
  const el = document.querySelector(${JSON.stringify(selector)});
  if (!el) return null;
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
  setter.call(el, ${JSON.stringify(value)});
  el.dispatchEvent(new Event('input', { bubbles: true }));
  return el.value;
})()`;

const click = (selector) => `(() => {
  const el = document.querySelector(${JSON.stringify(selector)});
  if (!el) return false;
  el.click();
  return true;
})()`;

const text = (selector) =>
  `(document.querySelector(${JSON.stringify(selector)})?.textContent || '').trim()`;

// ---------------------------------------------------------------- 主流程

async function main() {
  const ver = await waitDevtools();
  console.log(`Chrome: ${ver.Browser}\n目标: ${BASE}\n`);

  const tab = await (
    await fetch(`http://127.0.0.1:${PORT}/json/new?about:blank`, { method: 'PUT' })
  ).json();
  await connect(tab.webSocketDebuggerUrl);
  await send('Page.enable');
  await send('Runtime.enable');

  // 让 headless 明确报告「系统偏好 = 暗色」，用来验证「默认跟随系统」
  await send('Emulation.setEmulatedMedia', {
    features: [{ name: 'prefers-color-scheme', value: 'dark' }],
  });

  console.log('== 1. 首屏：跟随系统主题 + 初始化向导 ==');
  await send('Page.navigate', { url: `${BASE}/` });
  await waitFor('页面 ready', async () => (await evaluate('document.readyState')) === 'complete');
  await waitFor('渲染出内容', async () => (await evaluate('!!document.querySelector("#root > *")')) === true);
  await waitFor('进入初始化向导', async () => (await evaluate('location.pathname')) === '/setup');

  check('首次访问跳转到初始化向导', '/setup', await evaluate('location.pathname'));
  check(
    '首屏主题跟随系统（dark）',
    'dark',
    await evaluate('document.documentElement.dataset.theme'),
  );
  check('向导标题', '初始化 LMBY', await evaluate(text('.center-card h1')));
  check('右上角有主题切换按钮', true, await evaluate('!!document.querySelector(".theme-toggle")'));
  await screenshot('01-setup-dark');

  console.log('\n== 2. 明暗切换（首页右上角常驻入口） ==');
  await evaluate(click('.theme-toggle'));
  await sleep(200);
  check('点击后切到亮色', 'light', await evaluate('document.documentElement.dataset.theme'));
  check(
    '选择已写入 localStorage',
    'light',
    await evaluate('localStorage.getItem("lmby-theme")'),
  );
  await screenshot('02-setup-light');

  await evaluate(click('.theme-toggle'));
  await sleep(200);
  check('再点一次回到暗色', 'dark', await evaluate('document.documentElement.dataset.theme'));
  await screenshot('03-setup-dark-again');

  console.log('\n== 3. 初始化向导创建管理员 ==');
  check('填入用户名', 'admin', await evaluate(setInput('.center-card input', 'admin')));
  check(
    '填入显示名',
    '管理员',
    await evaluate(`(() => {
      const el = document.querySelectorAll('.center-card input')[1];
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
      setter.call(el, '管理员');
      el.dispatchEvent(new Event('input', { bubbles: true }));
      return el.value;
    })()`),
  );
  await evaluate(`(() => {
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
    // 下面是脚本**自己通过初始化向导创建的一次性测试账号**的口令：
    // 测试库用完即弃，不是任何真实账号的凭据。
    const pw = 'lmby-gui-pass-1';
    [2, 3].forEach((i) => {
      const el = document.querySelectorAll('.center-card input')[i];
      setter.call(el, pw);
      el.dispatchEvent(new Event('input', { bubbles: true }));
    });
    return true;
  })()`);
  await evaluate(click('.center-card button[type="submit"]'));
  await waitFor('跳转到概览页', async () => (await evaluate('location.pathname')) === '/');
  check('创建成功后进入概览页', '/', await evaluate('location.pathname'));
  await waitFor('顶栏出现用户名', async () =>
    (await evaluate(text('.topbar'))).includes('管理员'),
  );
  check('顶栏显示当前用户', true, (await evaluate(text('.topbar'))).includes('管理员'));

  console.log('\n== 4. 概览页（健康面板） ==');
  await waitFor('健康面板渲染完成', async () =>
    (await evaluate(text('.card'))).includes('运行正常'),
  );
  const overview = await evaluate(text('.content'));
  check('显示服务状态卡片', true, overview.includes('服务状态'));
  check('显示数据库与 ffmpeg 状态徽章', true, overview.includes('数据库') && overview.includes('ffmpeg'));
  check('列出硬件加速后端', true, overview.includes('vaapi'));
  check('提示「列出后端不等于真能用」', true, overview.includes('不等于'));
  await screenshot('04-overview-dark');

  console.log('\n== 5. 个人中心 ==');
  await evaluate(click('.nav a[href="/account"]'));
  await waitFor('进入个人中心', async () => (await evaluate('location.pathname')) === '/account');
  const account = await evaluate(text('.content'));
  check('有「资料」卡片', true, account.includes('资料'));
  check('有「修改口令」卡片', true, account.includes('修改口令'));
  check('有「外观」卡片', true, account.includes('外观'));
  check('有「我的设备」卡片', true, account.includes('我的设备'));
  check('设备列表标出当前会话', true, account.includes('当前'));
  await screenshot('05-account-dark');

  console.log('\n== 6. 外观：切亮色并同步到账号 ==');
  await evaluate(`(() => {
    const btns = [...document.querySelectorAll('.seg button')];
    const target = btns.find((b) => b.textContent.trim() === '亮色');
    target.click();
    return true;
  })()`);
  await sleep(400);
  check('页面切到亮色', 'light', await evaluate('document.documentElement.dataset.theme'));
  await screenshot('06-account-light');

  const themeResp = await evaluate(`(async () => {
    const r = await fetch('/api/v1/auth/me', { credentials: 'same-origin' });
    const j = await r.json();
    return j.preferences.theme;
  })()`);
  check('主题偏好已同步到账号', 'light', themeResp);

  console.log('\n== 7. 修改口令（走界面） ==');
  await evaluate(`(() => {
    const inputs = [...document.querySelectorAll('.card')].find((c) =>
      c.textContent.includes('修改口令'),
    ).querySelectorAll('input');
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
    const vals = ['lmby-gui-pass-1', 'lmby-gui-pass-2', 'lmby-gui-pass-2'];
    inputs.forEach((el, i) => {
      setter.call(el, vals[i]);
      el.dispatchEvent(new Event('input', { bubbles: true }));
    });
    return true;
  })()`);
  await evaluate(`(() => {
    const card = [...document.querySelectorAll('.card')].find((c) =>
      c.textContent.includes('修改口令'),
    );
    card.querySelector('button[type="submit"]').click();
    return true;
  })()`);
  await waitFor('出现修改成功提示', async () =>
    (await evaluate(text('.content'))).includes('口令已修改'),
  );
  check('界面显示改口令成功', true, (await evaluate(text('.content'))).includes('口令已修改'));
  await screenshot('07-password-changed');

  console.log('\n== 8. 刷新后主题仍保持（localStorage 持久化） ==');
  await send('Page.navigate', { url: `${BASE}/account` });
  await waitFor('页面 ready', async () => (await evaluate('document.readyState')) === 'complete');
  await waitFor('重新回到个人中心', async () => (await evaluate('location.pathname')) === '/account');
  check('刷新后仍是亮色', 'light', await evaluate('document.documentElement.dataset.theme'));
  await screenshot('08-after-reload');

  console.log('\n== 9. 用新口令重新登录 ==');
  await evaluate(click('.btn-ghost')); // 退出
  await waitFor('回到登录页', async () => (await evaluate('location.pathname')) === '/login');
  check('登出后回到登录页', '/login', await evaluate('location.pathname'));
  await screenshot('09-login');

  await evaluate(setInput('.center-card input', 'admin'));
  await evaluate(`(() => {
    const el = document.querySelectorAll('.center-card input')[1];
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
    setter.call(el, 'lmby-gui-pass-2');
    el.dispatchEvent(new Event('input', { bubbles: true }));
    return el.value;
  })()`);
  await evaluate(click('.center-card button[type="submit"]'));
  await waitFor('用新口令登录成功', async () => (await evaluate('location.pathname')) === '/');
  check('新口令可以登录', '/', await evaluate('location.pathname'));
  await screenshot('10-relogin-ok');

  console.log(`\n\x1b[1m结果：${pass} 通过，${fail} 失败\x1b[0m`);
  return fail;
}

let code = 1;
try {
  code = await main();
} catch (e) {
  console.error('\n运行失败: ' + e.message);
  code = 2;
} finally {
  try {
    ws?.close();
  } catch {
    /* ignore */
  }
  chrome.kill();
  await sleep(300);
}

// 注意：不要用 process.exit()，在管道输出时它可能丢掉缓冲区里的内容。
process.exitCode = code;
