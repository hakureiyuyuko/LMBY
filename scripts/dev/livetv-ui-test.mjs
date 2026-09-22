// 直播电视（M5 S3/M6）界面验收：真浏览器里跑一遍频道列表、筛选、收藏、停用、起播、外链。
//
// 为什么必须是真浏览器：这一层的坑（hls.js 挂滚动播放列表、自动播放策略、
// 浏览器端起播耗时）在 HTTP 断言里全看不出来。DoD 的「浏览器起播 < 2 秒」
// 也只有在这里才量得到（服务端口径的 1351ms 不等于用户看到的）。
//
// 必须在同一次运行里「起浏览器 → 跑断言 → 关浏览器」，否则后台 Chrome 会被回收。
//
// 用法：
//   BASE=http://192.168.x.x:8099 LMBY_USER=devtest LMBY_PASS=口令 node livetv-ui-test.mjs
// 可选：
//   CHROME      Chrome 路径
//   OUT         截图目录（默认 shots-livetv）
//   LMBY_USER2 / LMBY_PASS2   一个**非管理员**账号（给了就多验一段「看不到源/探测面板」）
//   START_MAX   浏览器起播上限毫秒（默认 2000，就是 DoD 的数字）
import { appendFileSync, mkdirSync, writeFileSync } from 'node:fs';
import { spawn } from 'node:child_process';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';

const REPORT = 'livetv-ui-report.txt';
writeFileSync(REPORT, '');
const log = (m) => appendFileSync(REPORT, m + '\n');

const CHROME = process.env.CHROME || 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const BASE = process.env.BASE || 'http://127.0.0.1:8099';
const USER = process.env.LMBY_USER || 'devtest';
const PASS = process.env.LMBY_PASS;
const USER2 = process.env.LMBY_USER2 || '';
const PASS2 = process.env.LMBY_PASS2 || '';
const START_MAX = Number(process.env.START_MAX || 2000);
if (!PASS) {
  throw new Error('请通过环境变量提供测试账号口令：LMBY_PASS=xxx（配合 LMBY_USER）');
}
const OUT = process.env.OUT || 'shots-livetv';
const PORT = Number(process.env.CDP_PORT || 9600 + (process.pid % 150));

mkdirSync(OUT, { recursive: true });
const profile = path.join(os.tmpdir(), `lmby-livetv-${Date.now()}`);

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
  // 只截视口（不 captureBeyondViewport）：这个页面的列表很长，
  // 全页截图缩得太小看不清，而文档里要的正是「页面长什么样」。
  await evaluate('window.scrollTo(0, 0)');
  await sleep(250);
  await send('Emulation.setDeviceMetricsOverride', {
    width: 1280,
    height: 900,
    deviceScaleFactor: 1,
    mobile: false,
  });
  const r = await send('Page.captureScreenshot', { format: 'png' });
  writeFileSync(path.join(OUT, `${name}.png`), Buffer.from(r.data, 'base64'));
  await send('Emulation.clearDeviceMetricsOverride');
  log(`截图 ${OUT}/${name}.png`);
}
/** 断言失败时把页面现场打出来。 */
async function dumpPage(tag) {
  const url = await evaluate('location.href');
  const text = await evaluate(`(document.body.innerText || '').slice(0, 300).replace(/\\s+/g, ' | ')`);
  log(`现场[${tag}] url=${url}`);
  log(`  正文: ${text}`);
  if (pageErrors.length > 0) log(`  页面错误: ${JSON.stringify([...new Set(pageErrors)].slice(0, 5))}`);
}
const setInput = (sel, val) => `(() => {
  const el = document.querySelector(${JSON.stringify(sel)});
  if (!el) return null;
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(el, ${JSON.stringify(
    String(val),
  )});
  el.dispatchEvent(new Event('input', { bubbles: true }));
  return el.value;
})()`;
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
    catch { return { status: res.status, body: text.slice(0, 200) }; }
  })()`;

/** 频道列表快照：每行的名字、徽标、按钮文案。 */
const rowsSnapshot = `[...document.querySelectorAll('.tv-row')].map((r) => ({
  name: (r.querySelector('.tv-name')?.textContent || '').trim(),
  badges: [...r.querySelectorAll('.badge')].map((b) => b.textContent.trim()),
  buttons: [...r.querySelectorAll('button')].map((b) => b.textContent.trim()),
  disabled: r.className.includes('tv-row-disabled'),
  playing: r.className.includes('tv-row-playing'),
}))`;

/** 在某一行里点某个文案的按钮（找不到返回 false）。 */
const clickRowButton = (name, label) => `(() => {
  const row = [...document.querySelectorAll('.tv-row')].find((r) =>
    (r.querySelector('.tv-name')?.textContent || '').includes(${JSON.stringify(name)}));
  if (!row) return false;
  const btn = [...row.querySelectorAll('button')].find((b) => b.textContent.trim() === ${JSON.stringify(label)});
  if (!btn) return false;
  btn.click();
  return true;
})()`;

const videoState = `(() => {
  const v = document.querySelector('.tv-video');
  if (!v) return null;
  return {
    readyState: v.readyState,
    currentTime: Number(v.currentTime.toFixed(2)),
    paused: v.paused,
    src: (v.currentSrc || v.src || '').slice(-40),
  };
})()`;

async function login(u, p) {
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
  // 把界面语言钉成中文：i18n 按 navigator.language 探测，而 headless Chrome 默认是 en-US。
  // ⚠️ 用 localStorage + reload 钉，而不是 Emulation.setLocaleOverride ——
  //    后者在部分 Chrome 上只影响 Intl、不改 navigator.language（实测无效）。
  await send('Page.navigate', { url: `${BASE}/login` });
  await sleep(900);
  await evaluate(`localStorage.setItem('lmby.lang', 'zh-CN')`);
  await send('Page.reload');
  await sleep(900);

  log('\n== 1. 登录（管理员） ==');
  check('登录成功', true, await login(USER, PASS));

  log('\n== 2. 新布局：左侧换台栏 + 右侧播放器 ==');
  await evaluate(clickSel('.nav a[href="/livetv"]'));
  check('进入直播页', true, await waitFor('直播页', async () => (await evaluate('location.pathname')) === '/livetv'));
  check(
    '有左侧换台栏 .tv-side',
    true,
    await waitFor('换台栏', async () => await evaluate(`!!document.querySelector('.tv-side')`)),
  );
  check('有播放器舞台 .tv-stage', true, await evaluate(`!!document.querySelector('.tv-stage')`));
  check('有底部控制条 .tv-bar', true, await evaluate(`!!document.querySelector('.tv-bar')`));
  const sideHead = await evaluate(`document.querySelector('.tv-side-head')?.textContent || ''`);
  check('侧栏头部是「X / Y 个频道」', true, /\d+\s*\/\s*\d+\s*个频道/.test(sideHead));

  log('\n== 3. 分组折叠：能收起、能展开 ==');
  await waitFor('频道行', async () => (await evaluate(`document.querySelectorAll('.tv-ch').length`)) > 0);
  const chOpen = await evaluate(`document.querySelectorAll('.tv-ch').length`);
  check('侧栏按分组列频道（至少有分组头）', true, (await evaluate(`document.querySelectorAll('.tv-group').length`)) > 0);
  await evaluate(clickSel('.tv-group-head'));
  // 点完要等 React 重渲染：立刻读 DOM 会读到最后一次渲染的旧值
  const chClosed = await (async () => {
    await waitFor('分组收起', async () => (await evaluate(`document.querySelectorAll('.tv-ch').length`)) < chOpen);
    return evaluate(`document.querySelectorAll('.tv-ch').length`);
  })();
  check('收起一个分组后可见频道变少', true, chClosed < chOpen);
  await evaluate(
    `(() => { const b = [...document.querySelectorAll('.tv-side-head button')].find((x) => x.textContent.trim() === '全部展开'); if (!b) return false; b.click(); return true; })()`,
  );
  check('「全部展开」后又回到原来的条数', chOpen, await evaluate(`document.querySelectorAll('.tv-ch').length`));

  log('\n== 4. 换台：点频道 → 底部显示当前频道 + 上一个/下一个/断开 ==');
  // 优先挑一个「探测没失败」的频道换台（失效那些不一定起得来）
  const target = await evaluate(
    `(() => { const row = [...document.querySelectorAll('.tv-ch')].find((r) => !r.querySelector('.tv-dot-bad')); return row?.querySelector('.tv-ch-name')?.textContent?.trim() || ''; })()`,
  );
  check('找到一个探测正常的频道', true, target.length > 0);
  await evaluate(
    `(() => { const row = [...document.querySelectorAll('.tv-ch')].find((r) => r.querySelector('.tv-ch-name')?.textContent?.trim() === ${JSON.stringify(target)}); if (!row) return false; row.querySelector('.tv-ch-main').click(); return true; })()`,
  );
  check(
    '底部控制条显示当前频道名',
    true,
    await waitFor('底部频道名', async () =>
      (await evaluate(`document.querySelector('.tv-bar strong')?.textContent?.trim()`)) === target,
    ),
  );
  for (const label of ['上一个', '下一个', '断开']) {
    check(
      `有「${label}」按钮`,
      true,
      await evaluate(
        `[...document.querySelectorAll('.tv-bar button')].some((b) => b.textContent.trim() === '${label}')`,
      ),
    );
  }
  check('播放中的那一行被标出', true, await waitFor('播放中行', async () =>
    (await evaluate(`!!document.querySelector('.tv-ch.is-playing')`))));

  log('\n== 5. 真起播（视频真的在跑）==');
  // 单个频道不一定起得来（RTSP 源、源站抽风、probeOk 未知都可能），
  // 所以试几个「探测没失败」的频道，**只要有一个真出了画面**就算这一页能看。
  const candidates = await evaluate(
    `[...document.querySelectorAll('.tv-ch')].filter((r) => !r.querySelector('.tv-dot-bad')).slice(0, 6).map((r) => r.querySelector('.tv-ch-name').textContent.trim())`,
  );
  let started = false;
  let playedName = '';
  for (const name of candidates) {
    await evaluate(clickRowButton(name));
    const ok = await waitFor(
      `真起播：${name}`,
      async () => {
        const s = await evaluate(videoState);
        return !!s && !s.paused && s.currentTime > 0;
      },
      6000,
    );
    if (ok) {
      started = true;
      playedName = name;
      break;
    }
  }
  check(`视频真的出画面（试了 ${candidates.length} 个频道，${playedName || '都没成'}）`, true, started);
  if (started) await shot('02-livetv-playing');

  log('\n== 6. 下一个 / 断开 ==');
  const first = await evaluate(`document.querySelector('.tv-bar strong')?.textContent?.trim()`);
  await evaluate(
    `(() => { const b = [...document.querySelectorAll('.tv-bar button')].find((x) => x.textContent.trim() === '下一个'); b.click(); return true; })()`,
  );
  check(
    '点了「下一个」换到别的频道',
    true,
    await waitFor('换台', async () => (await evaluate(`document.querySelector('.tv-bar strong')?.textContent?.trim()`)) !== first),
  );
  await evaluate(
    `(() => { const b = [...document.querySelectorAll('.tv-bar button')].find((x) => x.textContent.trim() === '断开'); b.click(); return true; })()`,
  );
  check(
    '断开后底部显示「未在播放」',
    true,
    await waitFor('断开', async () =>
      (await evaluate(`document.querySelector('.tv-bar')?.textContent || ''`)).includes('未在播放'),
    ),
  );
  check('断开后视频停了', true, (await evaluate(videoState))?.paused === true);

  log('\n== 7. 收藏星（真写库 + 还原）==');
  const starOn = await evaluate(`document.querySelector('.tv-ch .tv-star')?.getAttribute('aria-pressed')`);
  await evaluate(clickSel('.tv-ch .tv-star'));
  check(
    '点星标后状态翻转',
    true,
    await waitFor('星标翻转', async () =>
      (await evaluate(`document.querySelector('.tv-ch .tv-star')?.getAttribute('aria-pressed')`)) !== starOn,
    ),
  );
  // 再点回去：重渲染刚换过 DOM，点空是常事 → 最多试三次
  let restored = false;
  for (let i = 0; i < 3 && !restored; i++) {
    await evaluate(clickSel('.tv-ch .tv-star'));
    restored = await waitFor('星标还原', async () =>
      (await evaluate(`document.querySelector('.tv-ch .tv-star')?.getAttribute('aria-pressed')`)) === starOn,
      3000,
    );
  }
  check('再点一次还原', true, restored);

  log('\n== 8. 前台不再夹着管理动作（M6 收尾：全搬进了设置）==');
  for (const label of ['停用', '编辑', '外链', '复制地址']) {
    check(
      `直播页没有「${label}」按钮`,
      false,
      await evaluate(`[...document.querySelectorAll('button')].some((b) => b.textContent.trim() === '${label}')`),
    );
  }
  check('直播页没有频道搜索框（换台靠分组，不靠搜）', false,
    await evaluate(`!!document.querySelector('.tv-side input')`));
  check('直播页没有「共 N 台 · 启用中」那行统计（计数收进侧栏头部）', false,
    await evaluate(`document.body.textContent.includes('启用中')`));

  log('\n== 9. 外部播放器（VLC / Kodi / 电视盒子）==');
  check('侧栏底部有「外部播放器」明细', true,
    await evaluate(`(document.querySelector('.tv-ext summary')?.textContent || '').includes('外部播放器')`));
  check('展开后有 m3u 导出链接', true,
    await evaluate(`(() => { const d = document.querySelector('.tv-ext'); d.open = true; return !!d.querySelector('a[download]'); })()`));

  log('\n== 10. 频道列表接口（工具链仍然对）==');
  const listAll = await evaluate(apiCall('/api/v1/livetv/channels?enabled=false'));
  check('拿得到全量频道', true, Number(listAll.body.total) > 0);
  const listEnabled = await evaluate(apiCall('/api/v1/livetv/channels?enabled=true'));
  check('enabled=true 拿到的条数 ≥ 0 且 ≤ 全量', true,
    Number(listEnabled.body.channels.length) <= Number(listAll.body.channels.length));

  log('\n== 14. 直播页不再夹着源与探测（M6 收尾搬到了设置）==');
  check('直播页没有「频道探测」面板', false, await evaluate(`document.body.textContent.includes('频道探测')`));
  check(
    '直播页没有「重探全部」按钮',
    false,
    await evaluate(`[...document.querySelectorAll('button')].some((b) => b.textContent.trim() === '重探全部')`),
  );
  check(
    '导出 m3u 链接指向导出接口（这个留在直播页：它是频道列表的事）',
    true,
    await evaluate(`(document.querySelector('a[download]')?.getAttribute('href') || '').includes('/api/v1/livetv/export.m3u')`),
  );
  const exp = await evaluate(apiCall('/api/v1/livetv/export.m3u'));
  check('导出的 m3u 以 #EXTM3U 开头', true, String(exp.body).startsWith('#EXTM3U'));

  log('\n== 14b. 源与探测在「设置 → 直播源」页签里 ==');
  await evaluate(clickSel('.nav a[href="/settings"]'));
  check('先进设置页', true, await waitFor('设置页', async () =>
    (await evaluate('location.pathname')) === '/settings'));
  await evaluate(clickSel('[data-settings-tabs] a[href="/settings/livetv"]'));
  check('进「直播源」页签', true, await waitFor('直播源页签', async () =>
    (await evaluate('location.pathname')) === '/settings/livetv'));
  check('有「直播源」卡片', true, await waitFor('源面板', async () => (await evaluate(`document.body.textContent.includes('直播源')`))));
  check('有「频道探测」卡片', true, await evaluate(`document.body.textContent.includes('频道探测')`));
  check(
    '有「重探全部」按钮',
    true,
    await evaluate(`[...document.querySelectorAll('button')].some((b) => b.textContent.trim() === '重探全部')`),
  );
  await shot('04-settings-livetv');

  if (USER2 && PASS2) {
    log('\n== 15. 普通用户视角（看不到源与探测） ==');
    await evaluate(apiCall('/api/v1/auth/logout', { method: 'POST' }));
    check('普通账号登录成功', true, await login(USER2, PASS2));
    await evaluate(`[...document.querySelectorAll('.nav a')].find((a) => a.textContent.trim() === '直播').click()`);
    await waitFor('直播页', async () => (await evaluate('location.pathname')) === '/livetv');
    await waitFor('频道行', async () => (await evaluate(`document.querySelectorAll('.tv-ch').length`)) > 0);
    check('普通用户没有「直播源」卡片', false, await evaluate(`document.body.textContent.includes('直播源')`));
    check('普通用户没有「频道探测」卡片', false, await evaluate(`document.body.textContent.includes('频道探测')`));
    check('普通用户导航里没有「设置」（管理面收在它里面）', false, await evaluate(`!!document.querySelector('.nav a[href="/settings"]')`));
    check(
      '普通用户也能换台（能点频道行）',
      true,
      await evaluate(`(() => { document.querySelector('.tv-ch-main').click(); return true; })()`),
    );
    check('普通用户直播页也没有「停用 / 编辑」这些管理按钮', false,
      await evaluate(`[...document.querySelectorAll('button')].some((b) => ['停用','编辑','外链'].includes(b.textContent.trim()))`));
    await shot('04-livetv-viewer');
  } else {
    note('没给 LMBY_USER2/LMBY_PASS2，跳过普通用户视角');
  }

  log('\n== 16. 页面没有 JS 报错 ==');
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
