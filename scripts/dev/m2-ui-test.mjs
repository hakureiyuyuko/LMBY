// M2 界面收尾验收：海报墙、剧集视图、人工匹配的批量选择、设置页。
//
// 必须在同一次运行里「起浏览器 → 跑断言 → 关浏览器」，否则后台 Chrome 会被回收。
//
// 用法：
//   BASE=http://192.168.x.x:8099 LMBY_USER=devtest LMBY_PASS=口令 node m2-ui-test.mjs
// 可选：CHROME（Chrome 路径）、OUT（截图目录）
import { appendFileSync, mkdirSync, writeFileSync } from 'node:fs';
import { spawn } from 'node:child_process';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';

const REPORT = 'm2-ui-report.txt';
writeFileSync(REPORT, '');
const log = (m) => appendFileSync(REPORT, m + '\n');

const CHROME = process.env.CHROME || 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const BASE = process.env.BASE || 'http://127.0.0.1:8099';
const USER = process.env.LMBY_USER || 'devtest';
const PASS = process.env.LMBY_PASS;
if (!PASS) {
  throw new Error('请通过环境变量提供测试账号口令：LMBY_PASS=xxx（配合 LMBY_USER）');
}
const OUT = process.env.OUT || 'shots-m2';
// 端口随进程号漂移：两个实例抢同一个 DevTools 会让报告交错（踩过）
const PORT = Number(process.env.CDP_PORT || 9450 + (process.pid % 150));

mkdirSync(OUT, { recursive: true });
const profile = path.join(os.tmpdir(), `lmby-m2-${Date.now()}`);

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
    '--window-size=1440,1100',
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
// 弹窗默认「确定」；测「取消」路径时临时改成 false
let dialogAccept = true;
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
        send('Page.handleJavaScriptDialog', { accept: dialogAccept }).catch(() => {});
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
    log('出错表达式（前 160 字）：' + expression.slice(0, 160).replace(/\n/g, ' '));
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
  // 用 0.5 的设备像素比拍整页：海报墙这种长页面原图能到 2MB，文档里没必要那么大。
  // （用 clip+scale 会把懒加载的图拍成空白 —— 得靠 captureBeyondViewport 触发它们）
  await send('Emulation.setDeviceMetricsOverride', {
    width: 1440,
    height: 1100,
    deviceScaleFactor: 0.5,
    mobile: false,
  });
  const r = await send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: true });
  writeFileSync(path.join(OUT, `${name}.png`), Buffer.from(r.data, 'base64'));
  await send('Emulation.clearDeviceMetricsOverride');
  log(`截图 ${OUT}/${name}.png`);
}
const clickSel = (sel) =>
  `(() => { const e = document.querySelector(${JSON.stringify(sel)}); if (!e) return false; e.click(); return true; })()`;
const clickText = (t) => `(() => {
  const e = [...document.querySelectorAll('.btn')].find((x) => x.textContent.trim() === ${JSON.stringify(t)});
  if (!e) return false;
  e.click();
  return true;
})()`;
const content = `(document.querySelector('.content')?.textContent || '')`;
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
  await send('Emulation.setEmulatedMedia', {
    features: [{ name: 'prefers-color-scheme', value: 'dark' }],
  });

  log('\n== 1. 登录 ==');
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
  check('登录成功', true, await waitFor('首页', async () => (await evaluate('location.pathname')) === '/'));

  log('\n== 2. 海报墙（从媒体库页进入）==');
  await evaluate(clickSel('.nav a[href="/libraries"]'));
  check('进入媒体库页', true, await waitFor('媒体库页', async () =>
    (await evaluate('location.pathname')) === '/libraries'));
  check('媒体库卡片上有「海报墙」入口', true, await waitFor('海报墙链接', async () =>
    (await evaluate(`!!document.querySelector('.btn[href^="/library/"]')`)) === true));
  await evaluate(clickSel('.btn[href^="/library/"]'));
  check('进入海报墙', true, await waitFor('海报墙', async () =>
    (await evaluate('location.pathname')).startsWith('/library/')));
  check('铺出了海报卡片', true, await waitFor('海报卡片', async () =>
    (await evaluate(`document.querySelectorAll('.poster-card').length`)) > 0));
  const totalText = await evaluate(`document.querySelector('.card')?.textContent || ''`);
  check('显示总数', true, /共\s*\d+\s*条/.test(totalText));
  await shot('01-poster-wall-dark');

  const allCount = await evaluate(`document.querySelectorAll('.poster-card').length`);
  await evaluate(`(() => {
    const tab = [...document.querySelectorAll('.tabs .tab')].find((b) => b.textContent.trim() === '电影');
    tab.click();
    return true;
  })()`);
  check('切到电影：URL 带 kind', true, await waitFor('filter', async () =>
    (await evaluate('location.search')).includes('kind=movie')));
  // 等卡片**真的换成电影**再往下：只等 URL 会拿到上一批（剧集）的卡片
  await waitFor('电影卡片刷新', async () =>
    (await evaluate(`[...document.querySelectorAll('.poster-card')].every((a) => a.getAttribute('href').startsWith('/items/'))`)) === true);
  check('切到电影后仍有卡片', true, (await evaluate(`document.querySelectorAll('.poster-card').length`)) > 0);
  check('筛选后数量不超过全集', true,
    (await evaluate(`document.querySelectorAll('.poster-card').length`)) <= allCount);

  log('\n== 3. 剧集视图（点剧集卡片）==');
  await evaluate(`(() => {
    const tab = [...document.querySelectorAll('.tabs .tab')].find((b) => b.textContent.trim() === '剧集');
    tab.click();
    return true;
  })()`);
  await waitFor('剧集筛选', async () => (await evaluate('location.search')).includes('kind=series'));
  // 同上：等卡片真的换成剧集（否则点到的是上一批电影卡片，测出来的是条目页）
  const hasSeries = await waitFor('剧集卡片刷新', async () =>
    (await evaluate(`document.querySelectorAll('.poster-card').length`)) > 0 &&
    (await evaluate(`[...document.querySelectorAll('.poster-card')].every((a) => a.getAttribute('href').startsWith('/series/'))`)) === true);
  if (!hasSeries) {
    log('   库里没有剧集卡片，跳过剧集视图用例');
  } else {
    const href = await evaluate(`document.querySelector('.poster-card')?.getAttribute('href') || ''`);
    check('卡片指向剧集页', true, href.startsWith('/series/'));
    await evaluate(clickSel('.poster-card'));
    check('跳到剧集页', true, await waitFor('剧集页', async () =>
      (await evaluate('location.pathname')) === href));
    check('剧集页有季标签或集列表', true, await waitFor('季/集', async () =>
      (await evaluate(`document.querySelectorAll('.tabs .tab').length`)) > 0 ||
      (await evaluate(`document.querySelectorAll('.ep-row').length`)) > 0));
    // 集列表是选中季之后**再发一次请求**回来的，必须等它渲染（不等会拿到 0 条）
    const gotEpisodes = await waitFor('集列表', async () =>
      (await evaluate(`document.querySelectorAll('.ep-row').length`)) > 0);
    check('列出了集（首季）', true, gotEpisodes);
    if (!gotEpisodes) {
      log('   页面文本：' + String(await evaluate(content)).slice(0, 160).replace(/\s+/g, ' '));
    }
    check('集行显示了季集号', true, await evaluate(
      `(document.querySelector('.ep-row')?.textContent || '').includes('E0')`,
    ));
    check('有「返回海报墙」', true, await evaluate(`!!document.querySelector('.btn[href^="/library/"]')`));
    check('有「编辑字段与锁定」', true, await evaluate(`!!document.querySelector('.btn[href^="/items/"]')`));
    await shot('02-series-view-dark');

    // 点一集 → 条目页
    await evaluate(clickSel('.ep-row'));
    check('点集进条目页', true, await waitFor('条目页', async () =>
      (await evaluate('location.pathname')).startsWith('/items/')));
    check('条目页字段表渲染', true, await waitFor('字段表', async () =>
      (await evaluate(`!!document.querySelector('[data-field="title"]')`)) === true));
  }

  log('\n== 4. 人工匹配：批量选择界面 ==');
  await evaluate(clickSel('.nav a[href="/match"]'));
  check('进入人工匹配', true, await waitFor('匹配页', async () =>
    (await evaluate('location.pathname')) === '/match'));
  const hasItems = await waitFor('候选卡片', async () =>
    (await evaluate(`document.querySelectorAll('.match-cell').length`)) > 0, 12000);
  if (!hasItems) {
    log('   人工匹配页没有待处理条目（说明库里都处理完了），跳过批量用例');
  } else {
    check('卡片上有勾选框', true, await evaluate(
      `!!document.querySelector('.cell-check input[type="checkbox"]')`,
    ));
    check('未勾选时没有批量条', false, await evaluate(`!!document.querySelector('.batch-bar')`));
    await evaluate(clickSel('.cell-check input'));
    check('勾选后出现批量条', true, await waitFor('批量条', async () =>
      (await evaluate(`!!document.querySelector('.batch-bar')`)) === true));
    check('批量条显示已选数量', true, await evaluate(
      `(document.querySelector('.batch-bar')?.textContent || '').includes('已选 1 条')`,
    ));

    const n = await evaluate(`document.querySelectorAll('.match-cell').length`);
    await evaluate(clickText('全选本页'));
    check('全选本页勾上了本页所有卡片', true, await waitFor('全选', async () =>
      (await evaluate(`document.querySelectorAll('.cell-check input:checked').length`)) === n));

    // 真的点一次批量动作，但把确认框**取消**掉：验证按钮接上了、且不会误改数据
    dialogAccept = false;
    await evaluate(clickText('强制重刮（覆盖未锁字段）'));
    await sleep(600);
    check('取消确认后不会发请求（没有成功提示）', false,
      (await evaluate(content)).includes('已处理'));
    dialogAccept = true;

    await evaluate(clickText('清除选择'));
    check('清除选择后批量条消失', true, await waitFor('清除', async () =>
      (await evaluate(`!!document.querySelector('.batch-bar')`)) === false));
  }

  log('\n== 5. 设置页（TMDB 凭据）==');
  check('导航里有「设置」（管理员）', true, await evaluate(`!!document.querySelector('.nav a[href="/settings"]')`));
  await evaluate(clickSel('.nav a[href="/settings"]'));
  check('进入设置页', true, await waitFor('设置页', async () =>
    (await evaluate('location.pathname')) === '/settings'));
  check('有 Read Access Token 输入框', true, await waitFor('表单', async () =>
    (await evaluate(`document.querySelectorAll('input[type="password"]').length`)) === 2));
  const settingsText = await evaluate(content);
  check('显示当前状态（已配置/未配置）', true,
    settingsText.includes('已配置') || settingsText.includes('未配置'));
  check('显示来源（数据库/配置文件）', true, settingsText.includes('来源'));
  check('有系统信息卡（含数据库字符集）', true, settingsText.includes('数据库字符集'));
  const langValue = await evaluate(`document.querySelector('input[list="tmdb-languages"]')?.value || ''`);
  check('语言框带出当前值', true, langValue.length > 0);

  // 测试连接：真打一次 TMDB（用当前凭据）
  await evaluate(clickText('测试连接'));
  check('测试连接有结果', true, await waitFor('测试结果', async () =>
    (await evaluate(content)).includes('连通正常') || (await evaluate(content)).includes('连接失败'), 30000));
  const testOk = (await evaluate(content)).includes('连通正常');
  check('凭据可用（连通正常）', true, testOk);
  await shot('03-settings-dark');

  // 保存一次（语言填一样的值）→ 断言提示 → 再「恢复为配置文件的值」还原
  await evaluate(setInput('input[list="tmdb-languages"]', langValue));
  await evaluate(clickText('保存'));
  check('保存有成功提示', true, await waitFor('保存提示', async () =>
    (await evaluate(content)).includes('已保存并立刻生效')));
  check('保存后来源变为数据库', true, await waitFor('来源', async () =>
    (await evaluate(content)).includes('数据库（本页保存的）')));

  await evaluate(clickText('恢复为配置文件的值'));
  check('恢复成功', true, await waitFor('恢复提示', async () =>
    (await evaluate(content)).includes('已恢复为配置文件的值')));
  check('恢复后来源回到配置文件', true, (await evaluate(content)).includes('config.toml / 环境变量'));

  log('\n== 6. 亮色主题 ==');
  await evaluate(clickSel('.theme-toggle'));
  await sleep(400);
  check('切到亮色', 'light', await evaluate('document.documentElement.dataset.theme'));
  await shot('04-settings-light');

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
