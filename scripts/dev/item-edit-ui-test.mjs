// M2「字段锁定编辑界面」的界面验收（CDP 直连 headless Chrome，不依赖 Puppeteer）
//
// 它验的是「这个界面真的能被人用起来」：
//   登录 → 打开条目页 → 改简介并保存 → 非法输入被挡 → 勾锁定并保存 →
//   「已锁定 N 个字段」出现 → 全部解锁 → 还原。
// 服务端那套语义（重扫不覆盖）由 scripts/dev/verify-item-edit.sh 在真库上验。
//
// 必须在同一次运行里「起浏览器 → 跑断言 → 关浏览器」，否则后台 Chrome 会被回收。
//
// 用法：
//   BASE=http://192.168.x.x:8099 LMBY_USER=devtest LMBY_PASS=口令 node item-edit-ui-test.mjs
// 可选：CHROME（Chrome 路径）、ITEM_ID（不指定就自动挑一条有 nfo 的电影）
import { appendFileSync, mkdirSync, writeFileSync } from 'node:fs';
import { spawn } from 'node:child_process';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';

const REPORT = 'item-edit-ui-report.txt';
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
const OUT = process.env.OUT || 'shots-item-edit';
const PORT = Number(process.env.CDP_PORT || 9446);

mkdirSync(OUT, { recursive: true });
const profile = path.join(os.tmpdir(), `lmby-item-${Date.now()}`);

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
    '--window-size=1400,1200',
    'about:blank',
  ],
  { stdio: ['ignore', 'ignore', 'ignore'] },
);

let pass = 0;
let fail = 0;

/** 与 browser-test.mjs 一致：期望值与实际值按 JSON 比较（这样空字符串、false、0 都能正确判定）。 */
function check(name, expected, actual) {
  const ok = JSON.stringify(expected) === JSON.stringify(actual);
  if (ok) {
    pass++;
    log(`ok   ${name}`);
  } else {
    fail++;
    log(
      `FAIL ${name}（期望 ${JSON.stringify(expected)}，实际 ${JSON.stringify(actual)}）`,
    );
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
      // window.confirm()：headless 下必须显式应答，否则拿不到返回值
      if (msg.method === 'Page.javascriptDialogOpening') {
        send('Page.handleJavaScriptDialog', { accept: true }).catch(() => {});
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
  const r = await send('Runtime.evaluate', {
    expression: expression,
    returnByValue: true,
    awaitPromise: true,
  });
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
const clickText = (t) => `(() => {
  const e = [...document.querySelectorAll('.btn')].find((x) => x.textContent.trim() === ${JSON.stringify(t)});
  if (!e) return false;
  e.click();
  return true;
})()`;
const content = `(document.querySelector('.content')?.textContent || '')`;

/**
 * 在页面里读接口（带上会话 cookie）。
 * 注意必须包成 async IIFE：CDP 的 Runtime.evaluate 不接受顶层 await
 * （写成 `(await fetch(...)).json()` 会报 SyntaxError: Unexpected identifier 'fetch'）。
 */
const apiGet = (itemId) =>
  `(async () => (await fetch('/api/v1/items/${itemId}', { credentials: 'same-origin' })).json())()`;

/** 读某个库的扫描状态（同样要包 async IIFE）。 */
const apiScan = (libId) =>
  `(async () => (await fetch('/api/v1/libraries/${libId}/scan', { credentials: 'same-origin' })).json())()`;

/** 受控组件：必须走原生 setter + input 事件（直接改 .value 不会触发 onChange）。 */
const setField = (name, value) => `(() => {
  const el = document.querySelector('[data-field=${JSON.stringify(name)}]');
  if (!el) return null;
  const proto = el.tagName === 'TEXTAREA' ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  Object.getOwnPropertyDescriptor(proto, 'value').set.call(el, ${JSON.stringify(value)});
  el.dispatchEvent(new Event('input', { bubbles: true }));
  return el.value;
})()`;

const fieldValue = (name) =>
  `(document.querySelector('[data-field=${JSON.stringify(name)}]')?.value ?? null)`;

/** 登录表单（初始化向导/登录页都是 .center-card）。 */
const setInput = (i, val) => `(() => {
  const el = document.querySelectorAll('.center-card input')[${i}];
  if (!el) return null;
  const s = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
  s.call(el, ${JSON.stringify(val)});
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
  // 把界面语言钉成中文：i18n 按 navigator.language 探测，而 headless Chrome 默认是 en-US。
  // ⚠️ 用 localStorage + reload 钉，而不是 Emulation.setLocaleOverride ——
  //    后者在部分 Chrome 上只影响 Intl、不改 navigator.language（实测无效）。
  await send('Page.navigate', { url: `${BASE}/login` });
  await sleep(900);
  await evaluate(`localStorage.setItem('lmby.lang', 'zh-CN')`);
  await send('Page.reload');
  await sleep(900);
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
      (await evaluate('document.querySelectorAll(".center-card input").length')) >= 2,
    ),
  );
  await evaluate(setInput(0, USER));
  await evaluate(setInput(1, PASS));
  await evaluate(clickSel('.center-card button[type="submit"]'));
  check('登录成功进入首页', true, await waitFor('首页', async () => (await evaluate('location.pathname')) === '/'));

  log('\n== 2. 找一条有 nfo 的电影并快照原值 ==');
  const picked = await evaluate(`(async () => {
    const get = async (u) => (await fetch(u, { credentials: 'same-origin' })).json();
    const libs = await get('/api/v1/libraries');
    for (const lib of libs.libraries) {
      const page = await get('/api/v1/libraries/' + lib.id + '/items?kind=movie&limit=200');
      const hit = (page.items || []).find((x) => x.metadataSource === 'nfo' && (x.title || '').length > 0);
      if (hit) {
        const detail = await get('/api/v1/items/' + hit.id);
        return { id: hit.id, item: detail.item, fields: detail.fields };
      }
    }
    return null;
  })()`);
  if (!picked) throw new Error('没找到 metadata_source=nfo 的电影条目');
  const ITEM_ID = Number(process.env.ITEM_ID || picked.id);
  log(`条目 id=${ITEM_ID}《${picked.item.title}》`);

  // 不管 ITEM_ID 是不是自动挑的，都在页面里重新快照一次（还原时要原样写回）
  const snap = await evaluate(apiGet(ITEM_ID));
  const orig = {
    title: snap.item.title ?? '',
    overview: snap.item.overview ?? '',
    providerIds: snap.item.providerIds ?? {},
    lockedFields: snap.item.lockedFields ?? [],
  };
  log(`原值：锁定=${JSON.stringify(orig.lockedFields)} providerIds=${JSON.stringify(orig.providerIds)}`);

  log('\n== 3. 打开条目页（只读断言）==');
  await send('Page.navigate', { url: `${BASE}/items/${ITEM_ID}` });
  check(
    '进入条目页',
    true,
    await waitFor('条目页', async () =>
      (await evaluate(`!!document.querySelector('[data-field="title"]')`)) === true,
    ),
  );
  check('标题框带出原值', orig.title, await evaluate(fieldValue('title')));
  check(
    '12 个可编辑字段',
    12,
    await evaluate('document.querySelectorAll("[data-field]").length'),
  );
  check(
    '12 个锁定开关',
    12,
    await evaluate('document.querySelectorAll("[data-lock]").length'),
  );
  check('简介是多行框', 'TEXTAREA', await evaluate(
    `(document.querySelector('[data-field="overview"]')?.tagName ?? '')`,
  ));
  const runtimeShown = await evaluate(fieldValue('runtime'));
  const runtimeWant = snap.item.runtimeTicks
    ? String(Math.round(snap.item.runtimeTicks / 600000000))
    : '';
  check('时长按分钟显示', runtimeWant, runtimeShown);
  check(
    '元数据 id 显示成 键=值',
    Object.entries(orig.providerIds)
      .map(([k, v]) => `${k}=${v}`)
      .join(', '),
    await evaluate(fieldValue('providerIds')),
  );
  const head = await evaluate(`(document.querySelector('.card')?.textContent || '')`);
  check('头部显示匹配状态', true, head.includes('来自 nfo') || head.includes('已人工处理'));
  check('提示写明锁定语义', true, (await evaluate(content)).includes('都不会覆盖它'));
  await shot('01-item-dark');

  log('\n== 4. 改简介并保存 ==');
  const MARK = `LMBY 界面验收 ${new Date().toISOString().slice(11, 19)}`;
  check('填入简介', MARK, await evaluate(setField('overview', MARK)));
  check('该行出现「已改」标记', true, await evaluate(
    `[...document.querySelectorAll('.edit-table tbody tr')].some((tr) => tr.textContent.includes('已改'))`,
  ));
  await evaluate(clickText('保存'));
  check(
    '出现保存成功提示',
    true,
    await waitFor('保存提示', async () =>
      (await evaluate(content)).includes('已保存'),
    ),
  );
  const saved = await evaluate(apiGet(ITEM_ID));
  check('接口里简介已更新', MARK, saved.item.overview);
  check('接口里标了人工来源', 'manual', saved.item.metadataSource);
  check('锁定集合没被动', JSON.stringify(orig.lockedFields), JSON.stringify(saved.item.lockedFields));

  log('\n== 5. 非法输入被挡在保存之前 ==');
  await evaluate(setField('year', '不是年份'));
  await evaluate(clickText('保存'));
  check(
    '整型字段填错会报错',
    true,
    await waitFor('错误提示', async () => (await evaluate(content)).includes('需要整数')),
  );
  const stillOk = await evaluate(apiGet(ITEM_ID));
  check('非法输入没有写进库（年份未变）', String(snap.item.year ?? ''), String(stillOk.item.year ?? ''));

  log('\n== 6. 锁定字段 ==');
  // 重新进页面，清掉上一次的「已改」状态，从干净状态开始勾锁
  await send('Page.navigate', { url: `${BASE}/items/${ITEM_ID}` });
  await waitFor('条目页', async () => (await evaluate(`!!document.querySelector('[data-lock="overview"]')`)) === true);
  check('勾选简介的锁定', true, await evaluate(clickSel('[data-lock="overview"]')));
  check('该行被标为已锁', true, await evaluate(
    `!!document.querySelector('.row-locked [data-lock="overview"]')`,
  ));
  check('头部提示「已锁定 1 个字段」', true, await evaluate(
    `${content}.includes('已锁定 1 个字段')`,
  ));
  await evaluate(clickText('保存'));
  await waitFor('保存提示', async () => (await evaluate(content)).includes('已保存'));
  const locked = await evaluate(apiGet(ITEM_ID));
  check('接口里锁定集合 = [overview]', '["overview"]', JSON.stringify(locked.item.lockedFields));
  check(
    '接口里字段级锁定状态为真',
    true,
    locked.fields.find((f) => f.name === 'overview')?.locked === true,
  );
  await shot('02-item-locked-dark');

  log('\n== 7. 亮色主题 ==');
  await evaluate(clickSel('.theme-toggle'));
  await sleep(400);
  check('切到亮色', 'light', await evaluate('document.documentElement.dataset.theme'));
  // 切主题会顺便把偏好同步到账号，验证这过程中表单状态没丢
  check('切主题后标题框内容不变', orig.title, await evaluate(fieldValue('title')));
  check('切主题后锁定仍勾着', true, await evaluate(
    `document.querySelector('[data-lock="overview"]')?.checked === true`,
  ));
  await shot('03-item-locked-light');
  await evaluate(clickSel('.theme-toggle'));
  await sleep(300);

  log('\n== 8. 全部解锁 ==');
  await evaluate(clickText('全部解锁'));
  check(
    '解锁后提示',
    true,
    await waitFor('解锁提示', async () => (await evaluate(content)).includes('已全部解锁')),
  );
  const unlocked = await evaluate(apiGet(ITEM_ID));
  check('接口里锁定集合已清空', '[]', JSON.stringify(unlocked.item.lockedFields));

  log('\n== 9. 还原 ==');
  const restored = await evaluate(`(async () => {
    const r = await fetch('/api/v1/items/${ITEM_ID}', {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: JSON.stringify({
        fields: ${JSON.stringify({ title: orig.title, overview: orig.overview, providerIds: orig.providerIds })},
        lockedFields: ${JSON.stringify(orig.lockedFields)}
      })
    });
    return await r.json();
  })()`);
  check('还原：标题', orig.title, restored.item.title ?? '');
  // 空字符串在 JSON 里会被 omitempty 省掉 → undefined，比对前归一成空串
  check('还原：简介', orig.overview, restored.item.overview ?? '');
  check('还原：元数据 id', JSON.stringify(orig.providerIds), JSON.stringify(restored.item.providerIds));
  check('还原：锁定集合', JSON.stringify(orig.lockedFields), JSON.stringify(restored.item.lockedFields));

  log('\n== 10. 重扫一次，把「元数据来源/状态」也还原 ==');
  // 人工编辑会把 metadata_source 标成 manual（那是对的：值确实是人写的）。
  // 测试结束要把它还原回 nfo，所以再重扫一遍 —— 这也是这套语义的正常用法。
  const libId = snap.item.libraryId;
  const started = await evaluate(`(async () => {
    const r = await fetch('/api/v1/libraries/${libId}/scan', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: JSON.stringify({ refreshMetadata: true })
    });
    return r.status;
  })()`);
  check('触发重扫 = 202', 202, started);
  const settled = await waitFor(
    '扫描结束',
    async () => {
      const st = await evaluate(apiScan(libId));
      return st.running === false;
    },
    180000,
  );
  check('扫描结束', true, settled);
  const final = await evaluate(apiGet(ITEM_ID));
  check('还原：匹配状态', snap.item.matchState ?? '', final.item.matchState ?? '');
  check('还原：元数据来源', snap.item.metadataSource ?? '', final.item.metadataSource ?? '');

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
