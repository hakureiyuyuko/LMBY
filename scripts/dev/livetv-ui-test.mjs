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

  log('\n== 2. 从导航进入直播页 ==');
  check(
    '导航里有「直播」入口',
    true,
    await waitFor('导航', async () =>
      (await evaluate(`[...document.querySelectorAll('.nav a')].some((a) => a.textContent.trim() === '直播')`)),
    ),
  );
  await evaluate(`[...document.querySelectorAll('.nav a')].find((a) => a.textContent.trim() === '直播').click()`);
  check('进入 /livetv', true, await waitFor('直播页', async () => (await evaluate('location.pathname')) === '/livetv'));
  check(
    '频道列表渲染出来了',
    true,
    await waitFor('频道行', async () => (await evaluate(`document.querySelectorAll('.tv-row').length`)) > 0),
  );
  await shot('01-livetv');

  const rows = await evaluate(rowsSnapshot);
  note(`列表里 ${rows.length} 台频道`);
  check('每行都有「播放」按钮', true, rows.every((r) => r.buttons.includes('播放')));
  check('每行都有「外链」按钮', true, rows.every((r) => r.buttons.includes('外链')));

  log('\n== 3. 探测标记（通 / 失效 / 未探） ==');
  const probeStats = (await evaluate(apiCall('/api/v1/livetv/channels/probe'))).body.stats;
  note(`库里：总 ${probeStats.total} · 能通 ${probeStats.ok} · 失效 ${probeStats.failed} · 没探过 ${probeStats.pending}`);
  const badges = await evaluate(`[...document.querySelectorAll('.tv-row .badge')].map((b) => b.textContent.trim())`);
  if (probeStats.failed > 0) {
    check('有频道被标成「失效」', true, badges.includes('失效'));
  }
  if (probeStats.ok > 0) {
    check('有频道被标成「通」', true, badges.includes('通'));
  }
  if (probeStats.pending > 0) {
    check('有频道被标成「未探」', true, badges.includes('未探'));
  }

  log('\n== 4. 按探测结果筛（只看失效） ==');
  if (probeStats.failed > 0) {
    await evaluate(
      `(() => {
        const sel = [...document.querySelectorAll('.tv-toolbar select')][1];
        Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value').set.call(sel, 'failed');
        sel.dispatchEvent(new Event('change', { bubbles: true }));
      })()`,
    );
    check(
      '筛出来的每一行都是「失效」',
      true,
      await waitFor('失效列表', async () => {
        const rs = await evaluate(rowsSnapshot);
        return rs.length > 0 && rs.every((r) => r.badges.includes('失效'));
      }),
    );
    const failedRows = await evaluate(`document.querySelectorAll('.tv-row').length`);
    note(`只看失效：${failedRows} 台`);
    // 恢复成「全部」
    await evaluate(
      `(() => {
        const sel = [...document.querySelectorAll('.tv-toolbar select')][1];
        Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value').set.call(sel, '');
        sel.dispatchEvent(new Event('change', { bubbles: true }));
      })()`,
    );
    await waitFor('回到全部', async () => (await evaluate(`document.querySelectorAll('.tv-row').length`)) > failedRows);
  } else {
    note('库里没有失效频道，跳过这一节');
  }

  log('\n== 5. 分组筛选 ==');
  const groups = (await evaluate(apiCall('/api/v1/livetv/channels?enabled=1'))).body.groups || [];
  if (groups.length > 0) {
    const g = groups[0];
    note(`选分组「${g.name}」（${g.count} 台）`);
    await evaluate(
      `(() => {
        const sel = document.querySelector('.tv-toolbar select');
        Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value').set.call(sel, ${JSON.stringify(g.name)});
        sel.dispatchEvent(new Event('change', { bubbles: true }));
      })()`,
    );
    await waitFor(
      '分组列表',
      async () => (await evaluate(`document.querySelectorAll('.tv-row').length`)) === g.count,
    );
    check(`分组筛选后只剩 ${g.count} 台`, g.count, await evaluate(`document.querySelectorAll('.tv-row').length`));
    // 恢复
    await evaluate(
      `(() => {
        const sel = document.querySelector('.tv-toolbar select');
        Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value').set.call(sel, '');
        sel.dispatchEvent(new Event('change', { bubbles: true }));
      })()`,
    );
    await waitFor('恢复全部分组', async () => (await evaluate(`document.querySelectorAll('.tv-row').length`)) > g.count);
  } else {
    note('没有分组可筛');
  }

  log('\n== 6. 搜索 ==');
  const all = (await evaluate(apiCall('/api/v1/livetv/channels?enabled=1'))).body.channels || [];
  const sample = all[Math.min(3, all.length - 1)];
  if (sample) {
    note(`搜「${sample.name}」`);
    await evaluate(setInput('.tv-toolbar .search-input', sample.name));
    check(
      '搜索结果里含这条频道，且最多几条',
      true,
      await waitFor('搜索结果', async () => {
        const rs = await evaluate(rowsSnapshot);
        return rs.length > 0 && rs.length <= 5 && rs.some((r) => r.name.includes(sample.name));
      }),
    );
    // 清空
    await evaluate(setInput('.tv-toolbar .search-input', ''));
    await waitFor(
      '清空搜索',
      async () => (await evaluate(`document.querySelectorAll('.tv-row').length`)) === all.length,
      15000,
    );
  } else {
    note('库里没有频道，跳过搜索');
  }

  log('\n== 7. 收藏（真写库，验完还原） ==');
  {
    const target = sample;
    if (target) {
      const cur = (await evaluate(apiCall(`/api/v1/livetv/channels/${target.id}`))).body;
      note(`目标频道「${cur.name}」，当前收藏=${cur.favorite}`);
      if (cur.favorite) {
        // 起点必须是未收藏，否则「切换」的语义验不了
        await evaluate(apiCall(`/api/v1/livetv/channels/${target.id}/favorite`, { method: 'POST' }));
        await sleep(400);
      }
      check('点「☆ 收藏」按钮（真点，不是调接口）', true, await evaluate(clickRowButton(cur.name, '☆ 收藏')));
      check('点收藏后按钮变成「已收藏」', true, await waitFor('收藏按钮', async () => {
        const rs = await evaluate(rowsSnapshot);
        const row = rs.find((r) => r.name.includes(cur.name));
        return row ? row.buttons.includes('★ 已收藏') : false;
      }));
      const now = (await evaluate(apiCall(`/api/v1/livetv/channels/${target.id}`))).body;
      check('接口里 favorite=true', true, now.favorite);
      // 还原
      await evaluate(clickRowButton(cur.name, '★ 已收藏'));
      check('再点一次取消收藏', true, await waitFor('取消收藏', async () => {
        const rs = await evaluate(rowsSnapshot);
        const row = rs.find((r) => r.name.includes(cur.name));
        return row ? row.buttons.includes('☆ 收藏') : false;
      }));
      const back = (await evaluate(apiCall(`/api/v1/livetv/channels/${target.id}`))).body;
      check('接口里 favorite=false（已还原）', false, back.favorite);
    } else {
      note('没有频道可做收藏测试');
    }
  }

  log('\n== 8. 停用 / 启用（验完还原） ==');
  {
    const target = sample;
    if (target) {
      // 默认「只看启用」勾着，停用后会从列表里消失 —— 先取消勾选，才能看到停用后的样子
      await evaluate(`(() => {
        const boxes = [...document.querySelectorAll('.tv-toolbar input[type=checkbox]')];
        if (boxes[0] && boxes[0].checked) boxes[0].click();
      })()`);
      await sleep(600);
      check(
        '「只看启用」确实取消了（否则停用的频道会直接从列表里消失）',
        false,
        await evaluate(`[...document.querySelectorAll('.tv-toolbar input[type=checkbox]')][0].checked`),
      );
      check('点「停用」按钮（真点）', true, await evaluate(clickRowButton(target.name, '停用')));
      check('点停用后出现「已停用」徽标', true, await waitFor('停用徽标', async () => {
        const rs = await evaluate(rowsSnapshot);
        const row = rs.find((r) => r.name.includes(target.name));
        return row ? row.badges.includes('已停用') && row.disabled : false;
      }));
      const off = (await evaluate(apiCall(`/api/v1/livetv/channels/${target.id}`))).body;
      check('接口里 disabled=true', true, off.disabled);
      // 还原：再点「启用」
      await evaluate(clickRowButton(target.name, '启用'));
      check('点启用后徽标消失（已还原）', true, await waitFor('启用还原', async () => {
        const rs = await evaluate(rowsSnapshot);
        const row = rs.find((r) => r.name.includes(target.name));
        return row ? !row.badges.includes('已停用') : false;
      }));
      const on = (await evaluate(apiCall(`/api/v1/livetv/channels/${target.id}`))).body;
      check('接口里 disabled=false（已还原）', false, on.disabled);
      // 勾回来
      await evaluate(`(() => {
        const boxes = [...document.querySelectorAll('.tv-toolbar input[type=checkbox]')];
        if (boxes[0] && !boxes[0].checked) boxes[0].click();
      })()`);
      await sleep(600);
    }
  }

  log('\n== 9. 失效频道起播要给出人话错误 ==');
  const failedList = (await evaluate(apiCall('/api/v1/livetv/channels?probe=failed'))).body.channels || [];
  if (failedList.length > 0) {
    const bad = failedList[0];
    note(`试播失效频道「${bad.name}」`);
    await evaluate(
      `(() => {
        const sel = [...document.querySelectorAll('.tv-toolbar select')][1];
        Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value').set.call(sel, 'failed');
        sel.dispatchEvent(new Event('change', { bubbles: true }));
      })()`,
    );
    await waitFor('失效列表', async () => (await evaluate(`document.querySelectorAll('.tv-row').length`)) > 0);
    await evaluate(clickRowButton(bad.name, '播放'));
    check(
      '播放器上出现失败提示（不是静默转圈）',
      true,
      await waitFor('失败提示', async () => {
        const t = await evaluate(`(document.querySelector('.tv-overlay-err')?.textContent || '')`);
        return t.length > 0;
      }, 20000),
    );
    const msg = await evaluate(`(document.querySelector('.tv-overlay-err')?.textContent || '')`);
    note(`错误文案：${msg.slice(0, 80)}`);
    await shot('02-livetv-failed');
    await evaluate(clickSel('.tv-player-bar .btn-ghost'));
    await sleep(500);
    await evaluate(
      `(() => {
        const sel = [...document.querySelectorAll('.tv-toolbar select')][1];
        Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value').set.call(sel, '');
        sel.dispatchEvent(new Event('change', { bubbles: true }));
      })()`,
    );
    await sleep(400);
  } else {
    note('库里没有失效频道，跳过这一节');
  }

  log('\n== 10. 真起播一个能用的频道（DoD：浏览器起播 < 2 秒） ==');
  const okList = (await evaluate(apiCall('/api/v1/livetv/channels?probe=ok'))).body.channels || [];
  if (okList.length === 0) {
    note('库里没有「探测能通」的频道，跳过起播（先跑 lmby livetv probe）');
  } else {
    const good = okList[0];
    note(`起播「${good.name}」（id=${good.id}）`);
    await evaluate(clickRowButton(good.name, '播放'));
    const playing = await waitFor(
      '画面真的动起来',
      async () => {
        const st = await evaluate(videoState);
        return st && st.readyState >= 2 && st.currentTime > 0 ? st : false;
      },
      30000,
    );
    check('视频进入播放（readyState>=2 且 currentTime>0）', true, Boolean(playing));
    if (playing) note(`video: ${JSON.stringify(playing)}`);

    // 页面上自己量的「浏览器起播 N ms」（点击 → playing 事件）
    let ms = null;
    await waitFor('起播耗时文案', async () => {
      const t = await evaluate(`(document.querySelector('.tv-player-bar .faint')?.textContent || '')`);
      const m = /浏览器起播\s+(\d+)\s+ms/.exec(t);
      if (m) {
        ms = Number(m[1]);
        return true;
      }
      return false;
    }, 15000);
    note(`浏览器起播 ${ms === null ? '（没读到）' : ms + ' ms'}（DoD 上限 ${START_MAX} ms）`);
    check('页面上量到了浏览器起播耗时', true, ms !== null);
    if (ms !== null) check(`浏览器起播 <= ${START_MAX} ms`, true, ms <= START_MAX);

    await shot('03-livetv-playing');

    log('\n== 11. 播放中的行被标出来 ==');
    const nowRows = await evaluate(rowsSnapshot);
    check('正在播的那一行有标记', true, nowRows.some((r) => r.playing && r.name.includes(good.name)));

    log('\n== 12. 外链（免登录） ==');
    const share = (await evaluate(apiCall(`/api/v1/livetv/channels/${good.id}/share`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ hours: 1 }),
    }))).body;
    note(`外链：${share.url}`);
    check('外链路径是 /s/<token>/playlist.m3u', true, typeof share.path === 'string' && share.path.startsWith('/s/') && share.path.endsWith('/playlist.m3u'));
    const shared = (await evaluate(apiCall(share.path))).status;
    check('免登录能拉外链播放列表（200/503 都算，503=分片还没好）', true, shared === 200 || shared === 503);
    const tampered = (await evaluate(apiCall(share.path.replace(/\/s\/(.)/, '/s/Z')))).status;
    check('改一个字符的外链被拒（401/410）', true, tampered === 401 || tampered === 410);
    const anon = (await evaluate(`fetch('/api/v1/livetv/channels', { credentials: 'omit' }).then((r) => r.status)`));
    check('未登录读频道列表被拒（401）', 401, anon);

    log('\n== 13. 停止播放 ==');
    await evaluate(clickSel('.tv-player-bar .btn-ghost'));
    check(
      '点停止后播放区消失（视频也停了）',
      true,
      await waitFor('播放区消失', async () => (await evaluate(`!!document.querySelector('.tv-stage')`)) === false, 15000),
    );
  }

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
    await waitFor('频道行', async () => (await evaluate(`document.querySelectorAll('.tv-row').length`)) > 0);
    check('普通用户没有「直播源」卡片', false, await evaluate(`document.body.textContent.includes('直播源')`));
    check('普通用户没有「频道探测」卡片', false, await evaluate(`document.body.textContent.includes('频道探测')`));
    check('普通用户导航里没有「设置」（管理面收在它里面）', false, await evaluate(`!!document.querySelector('.nav a[href="/settings"]')`));
    check(
      '普通用户仍能起播（有「播放」按钮）',
      true,
      await evaluate(`[...document.querySelectorAll('.tv-row button')].some((b) => b.textContent.trim() === '播放')`),
    );
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
