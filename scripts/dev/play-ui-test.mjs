// M3 播放器界面验收：真的在浏览器里起播、拖动、上报进度、关页面回收。
//
// 为什么必须是真浏览器：这一层的坑（HLS 分片路径、MSE 起播、自动播放策略、
// 分片还没生成完就拉流）在 HTTP 断言里全都看不出来 —— 只有真播一次才知道。
//
// 必须在同一次运行里「起浏览器 → 跑断言 → 关浏览器」，否则后台 Chrome 会被回收。
//
// 用法：
//   BASE=http://192.168.x.x:8099 LMBY_USER=devtest LMBY_PASS=口令 node play-ui-test.mjs
// 可选：
//   CHROME   Chrome 路径
//   OUT      截图目录（默认 shots-play）
//   NO_REMUX=1  跳过转封装相关的断言（没有合适的样本时）
import { appendFileSync, mkdirSync, writeFileSync } from 'node:fs';
import { spawn } from 'node:child_process';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';

const REPORT = 'play-ui-report.txt';
writeFileSync(REPORT, '');
const log = (m) => appendFileSync(REPORT, m + '\n');

const CHROME = process.env.CHROME || 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const BASE = process.env.BASE || 'http://127.0.0.1:8099';
const USER = process.env.LMBY_USER || 'devtest';
const PASS = process.env.LMBY_PASS;
if (!PASS) {
  throw new Error('请通过环境变量提供测试账号口令：LMBY_PASS=xxx（配合 LMBY_USER）');
}
const OUT = process.env.OUT || 'shots-play';
const PORT = Number(process.env.CDP_PORT || 9460 + (process.pid % 150));

mkdirSync(OUT, { recursive: true });
const profile = path.join(os.tmpdir(), `lmby-play-${Date.now()}`);

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
    // 无头环境下没有音频设备；静音播放不影响画面与时间轴推进
    '--mute-audio',
    '--autoplay-policy=no-user-gesture-required',
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
// 页面里的 JS 报错与控制台错误：界面“没渲染出按钮”时，原因基本都在这里
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
        pageErrors.push('console.error: ' + (msg.params.args || []).map((a) => a.value ?? a.description ?? '').join(' ').slice(0, 200));
      }
    };
  });
}

/** 断言失败时把页面现场打印出来（界面测试最容易“只看到一句 false”）。 */
async function dumpPage(tag) {
  const url = await evaluate('location.href');
  const text = await evaluate(`(document.body.innerText || '').slice(0, 400).replace(/\\s+/g, ' | ')`);
  const btns = await evaluate(`[...document.querySelectorAll('a.btn,button.btn')].map((b) => b.textContent.trim()).slice(0, 12)`);
  log(`现场[${tag}] url=${url}`);
  log(`  正文: ${text}`);
  log(`  按钮: ${JSON.stringify(btns)}`);
  if (pageErrors.length > 0) log(`  页面错误: ${JSON.stringify([...new Set(pageErrors)].slice(0, 5))}`);
  pageErrors.length = 0;
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
  await send('Emulation.setDeviceMetricsOverride', {
    width: 1440,
    height: 1100,
    deviceScaleFactor: 0.6,
    mobile: false,
  });
  const r = await send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: true });
  writeFileSync(path.join(OUT, `${name}.png`), Buffer.from(r.data, 'base64'));
  await send('Emulation.clearDeviceMetricsOverride');
  log(`截图 ${OUT}/${name}.png`);
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
/** 按序号设置输入框（登录页的账号/密码）。 */
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
/** 页面内直接调后端接口（同源、带会话 Cookie）。 */
const apiCall = (p, init) =>
  `(async () => {
    const res = await fetch(${JSON.stringify(p)}, Object.assign({ credentials: 'same-origin' }, ${JSON.stringify(
      init ? init : {},
    )}));
    const text = await res.text();
    try { return { status: res.status, body: JSON.parse(text) }; }
    catch { return { status: res.status, body: text.slice(0, 200) }; }
  })()`;

/** 播放器状态快照（每次断言都重新读，避免读到旧值）。 */
const videoState = `(() => {
  const v = document.querySelector('.player-video');
  if (!v) return null;
  return {
    readyState: v.readyState,
    currentTime: Number(v.currentTime.toFixed(2)),
    duration: Number.isFinite(v.duration) ? Number(v.duration.toFixed(1)) : 0,
    paused: v.paused,
    ended: v.ended,
    textTracks: v.textTracks ? v.textTracks.length : 0,
    src: (v.currentSrc || v.src || '').slice(0, 60),
  };
})()`;

/** 从条目页读出的绝对播放位置（控制条上那个 h:mm:ss）。 */
const positionText = `(() => {
  const el = document.querySelector('.player-time');
  return el ? el.textContent.trim() : '';
})()`;

async function main() {
  const ver = await waitDevtools();
  log(`Chrome ${ver.Browser} → ${BASE}`);

  const tab = await (await fetch(`http://127.0.0.1:${PORT}/json/new?about:blank`, { method: 'PUT' })).json();
  await connect(tab.webSocketDebuggerUrl);
  await send('Page.enable');
  await send('Runtime.enable');

  log('\n== 1. 登录 ==');
  await send('Page.navigate', { url: `${BASE}/login` });
  await waitFor('登录页', async () => (await evaluate('location.pathname')) === '/login');
  // 必须等表单真的渲染出来：刚导航完 DOM 还是空的，直接查 input 会拿到 undefined
  check(
    '登录表单已渲染',
    true,
    await waitFor('登录表单', async () => (await evaluate(`document.querySelectorAll('.center-card input').length`)) >= 2),
  );
  await evaluate(setInputIdx(0, USER));
  await evaluate(setInputIdx(1, PASS));
  await evaluate(clickSel('.center-card button[type="submit"]'));
  check('登录成功（回到首页）', true, await waitFor('首页', async () => (await evaluate('location.pathname')) === '/'));

  log('\n== 2. 首页「继续观看」区块存在 ==');
  check(
    '首页有继续观看卡片',
    true,
    await waitFor('继续观看', async () => (await evaluate(`document.body.textContent.includes('继续观看')`))),
  );
  await shot('01-home');

  log('\n== 3. 用接口挑可播样本（只看不行，要真能播） ==');
  // 通过 /items/{id}/playlist 判断：mp4 + h264 8bit + 浏览器能解的音频 → 应当直出；
  // matroska + h264 8bit → 应当转封装。挑不到就跳过对应段落。
  const cand = await evaluate(`(async () => {
    const out = { direct: null, remux: null, transcode: null };
    const libs = (await (await fetch('/api/v1/libraries')).json()).libraries || [];
    for (const lib of libs) {
      const b = await (await fetch('/api/v1/libraries/' + lib.id + '/browse?kind=movie&limit=200')).json();
      for (const it of b.items || []) {
        const pl = await (await fetch('/api/v1/items/' + it.id + '/playlist')).json();
        const f = (pl.files || []).find((x) => x.video && x.video.length > 0);
        if (!f) continue;
        const v = f.video[0];
        const a = (f.audio || [])[0] || { codec: '', channels: 0 };
        const browserA = ['aac','mp3','opus','vorbis','flac'].includes(a.codec) && a.channels <= 6;
        if (!out.direct && f.containerKind === 'mp4' && v.codec === 'h264' && (v.bitDepth || 8) === 8 && browserA) {
          out.direct = { id: it.id, title: it.title, file: f.containerKind + '/' + v.codec };
        }
        if (!out.remux && f.containerKind === 'mkv' && v.codec === 'h264' && (v.bitDepth || 8) === 8) {
          out.remux = { id: it.id, title: it.title, file: f.containerKind + '/' + v.codec, subs: (f.subtitles || []).length, audio: a.codec };
        }
        if (!out.transcode && v.codec === 'hevc' && (v.bitDepth || 8) === 10) {
          out.transcode = { id: it.id, title: it.title, file: f.containerKind + '/' + v.codec + ' 10bit' };
        }
        if (out.direct && out.remux && out.transcode) return out;
      }
    }
    return out;
  })()`);
  note(`直出样本：${cand.direct ? `${cand.direct.id} ${cand.direct.title}（${cand.direct.file}）` : '未找到'}`);
  note(`转封装样本：${cand.remux ? `${cand.remux.id} ${cand.remux.title}（${cand.remux.file}，${cand.remux.subs} 条字幕）` : '未找到'}`);
  note(`需转码样本：${cand.transcode ? `${cand.transcode.id} ${cand.transcode.title}` : '未找到'}`);

  // ---------------------------------------------------------------- 直出
  log('\n== 4. 直出：真的播起来 + 拖动 ==');
  if (!cand.direct) {
    note('没有直出样本，跳过');
  } else {
    const itemId = cand.direct.id;
    await send('Page.navigate', { url: `${BASE}/items/${itemId}` });
    const playBtnReady = await waitFor('播放按钮', async () =>
      (await evaluate(`[...document.querySelectorAll('a.btn')].some((a) => a.textContent.includes('播放'))`)),
    );
    check('条目页出现播放按钮', true, playBtnReady);
    if (!playBtnReady) {
      await dumpPage('条目页');
      throw new Error('条目页没有播放按钮，后续断言无意义');
    }
    await evaluate(`(() => {
      const a = [...document.querySelectorAll('a.btn')].find((x) => x.textContent.includes('播放'));
      a.click();
      return true;
    })()`);
    check(
      '进入播放器页',
      true,
      await waitFor('播放器', async () => (await evaluate('location.pathname')).startsWith('/play/')),
    );
    check(
      '播放器渲染了 video 与控制条',
      true,
      await waitFor('播放器控件', async () =>
        Boolean(await evaluate(`!!document.querySelector('.player-video') && !!document.querySelector('.player-controls')`)),
      ),
    );
    check(
      '模式徽标 = 直接播放',
      true,
      await waitFor('模式徽标', async () => await evaluate(`document.body.textContent.includes('直接播放')`)),
    );
    const playing = await waitFor(
      '直出起播（currentTime 前进）',
      async () => {
        const st = await evaluate(videoState);
        return st && st.readyState >= 2 && st.currentTime > 0.8;
      },
      30000,
    );
    check('直出真的播起来了（currentTime > 0.8s）', true, playing);
    const st1 = await evaluate(videoState);
    note(`直出 video 状态：${JSON.stringify(st1)}`);
    check('直出用的是原文件地址（/stream）', true, String(st1?.src || '').includes('/stream'));
    check('时长已就绪（> 60s）', true, (st1?.duration ?? 0) > 60);

    // 拖动到 50%
    await evaluate(
      `(() => {
        const el = document.querySelector('.player-progress');
        const s = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
        s.call(el, String(Math.floor(el.max / 2)));
        el.dispatchEvent(new Event('input', { bubbles: true }));
        return el.value;
      })()`,
    );
    check(
      '拖动后从新位置继续播（currentTime 变大过）',
      true,
      await waitFor('拖动生效', async () => {
        const st = await evaluate(videoState);
        return st && st.currentTime > 1;
      }, 15000),
    );
    const pos = await evaluate(positionText);
    note(`控制条时间：${pos}`);
    check('控制条显示的是整片时间（不是 0 起步）', true, !pos.startsWith('0:00 '));
    await shot('02-direct');
  }

  // ---------------------------------------------------------------- 转封装
  log('\n== 5. 转封装：mkv → HLS 分片，真的播起来 ==');
  let remuxSession = null;
  if (!cand.remux) {
    note('没有转封装样本，跳过');
  } else {
    const itemId = cand.remux.id;
    await send('Page.navigate', { url: `${BASE}/play/${itemId}` });
    check(
      '播放器页渲染',
      true,
      await waitFor('播放器', async () => Boolean(await evaluate(`!!document.querySelector('.player-video')`)), 30000),
    );
    check(
      '模式徽标 = 转封装',
      true,
      await waitFor('模式徽标', async () => await evaluate(`document.body.textContent.includes('转封装')`), 30000),
    );
    const playing = await waitFor(
      '转封装起播',
      async () => {
        const st = await evaluate(videoState);
        return st && st.readyState >= 2 && st.currentTime > 0.5;
      },
      45000,
    );
    check('HLS 分片真的播起来了（currentTime > 0.5s）', true, playing);
    const hlsDiag = await evaluate(
      `({ ua: navigator.userAgent, canPlay: document.createElement('video').canPlayType('application/vnd.apple.mpegurl'), mse: typeof MediaSource !== 'undefined' })`,
    );
    note(`HLS 诊断：canPlayType=${JSON.stringify(hlsDiag.canPlay)} MSE=${hlsDiag.mse} UA=${hlsDiag.ua}`);
    const st2 = await evaluate(videoState);
    note(`转封装 video 状态：${JSON.stringify(st2)}`);
    check('转封装走的是 m3u8 地址', true, String(st2?.src || '').includes('.m3u8') || String(st2?.src || '').includes('blob:'));

    const meta = await evaluate(apiCall('/api/v1/playback/sessions'));
    remuxSession = (meta.body?.sessions || [])[0] || null;
    check('服务端有 1 路播放会话', 1, (meta.body?.sessions || []).length);
    check('服务端有 1 路转封装会话', 1, (meta.body?.transcodeSessions || []).length);
    const windowEnd = await evaluate(`(async () => {
      const sid = ${JSON.stringify(remuxSession?.playSessionId || '')};
      if (!sid) return 0;
      const r = await fetch('/api/v1/play/' + sid);
      const j = await r.json();
      return j.windowEndSeconds || 0;
    })()`);
    note(`本段窗口末端：${windowEnd}s`);

    // 「续下一段」：把进度拖到窗口末端附近，播放器应当让服务端从新位置重开一段
    if (windowEnd > 30) {
      const before = await evaluate(positionText);
      await evaluate(
        `(() => {
          const el = document.querySelector('.player-progress');
          const s = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
          s.call(el, String(${Math.floor(windowEnd - 6)}));
          el.dispatchEvent(new Event('input', { bubbles: true }));
          return el.value;
        })()`,
      );
      const advanced = await waitFor(
        '窗口续段',
        async () => {
          const st = await evaluate(videoState);
          return st && st.currentTime > 0.5 && st.currentTime < windowEnd - 20;
        },
        40000,
      );
      check('拖到窗口末端后自动续下一段并继续播', true, advanced);
      const after = await evaluate(positionText);
      note(`续段前 ${before} → 续段后 ${after}`);
      check('续段后整片时间往前跳了', true, after !== before);
    } else {
      note('窗口太短（源文件比窗口还短），跳过续段断言');
    }

    const progReady = await waitFor(
      '进度上报',
      async () => {
        const r = await evaluate(apiCall(`/api/v1/items/${itemId}/progress`));
        return (r.body?.progress?.positionTicks ?? 0) > 0;
      },
      20000,
    );
    const prog = await evaluate(apiCall(`/api/v1/items/${itemId}/progress`));
    note(`服务端进度：${JSON.stringify(prog.body?.progress)}`);
    check('进度已上报到服务端（positionTicks > 0）', true, progReady);
    await shot('03-remux');

    log('\n== 6. 离开页面后自动回收（DoD：关页面不留 ffmpeg） ==');
    const sidBefore = remuxSession?.playSessionId || '';
    await send('Page.navigate', { url: `${BASE}/` });
    await waitFor('回到首页', async () => (await evaluate('location.pathname')) === '/');
    const gone = await waitFor(
      '会话被回收',
      async () => {
        const r = await evaluate(apiCall('/api/v1/playback/sessions'));
        const list = r.body?.sessions || [];
        return !list.some((x) => x.playSessionId === sidBefore);
      },
      20000,
    );
    check('离开播放器后播放会话被回收', true, gone);
    const after = await evaluate(apiCall('/api/v1/playback/sessions'));
    check('转封装会话也停掉了', 0, (after.body?.transcodeSessions || []).length);
  }

  // ---------------------------------------------------------------- 决策解释
  log('\n== 7. 放不了的条目：给出明确理由（不装死） ==');
  if (!cand.transcode) {
    note('没有 10bit HEVC 样本，跳过');
  } else {
    await send('Page.navigate', { url: `${BASE}/play/${cand.transcode.id}` });
    check(
      '显示「放不了」面板',
      true,
      await waitFor('放不了面板', async () => Boolean(await evaluate(`!!document.querySelector('.player-blocked')`)), 30000),
    );
    const reasons = await evaluate(
      `[...document.querySelectorAll('.player-reasons li')].map((li) => li.textContent)`,
    );
    note(`理由链：${JSON.stringify(reasons)}`);
    check('理由链非空', true, (reasons || []).length > 0);
    check('理由里点名需要转码', true, (reasons || []).some((r) => r.includes('转码')));
    check('没有出现播放器控件（不假装能播）', false, await evaluate(`!!document.querySelector('.player-controls')`));
    await shot('04-blocked');
  }

  // ---------------------------------------------------------------- 快捷键
  log('\n== 8. 快捷键与字幕开关 ==');
  if (cand.direct) {
    await send('Page.navigate', { url: `${BASE}/play/${cand.direct.id}` });
    check(
      '再次进入播放器',
      true,
      await waitFor('播放器', async () => Boolean(await evaluate(`!!document.querySelector('.player-video')`)), 30000),
    );
    await waitFor('起播', async () => {
      const st = await evaluate(videoState);
      return st && st.currentTime > 0.5;
    }, 30000);
    const before = await evaluate(videoState);
    await send('Input.dispatchKeyEvent', { type: 'keyDown', key: ' ', code: 'Space', windowsVirtualKeyCode: 32 });
    await send('Input.dispatchKeyEvent', { type: 'keyUp', key: ' ', code: 'Space', windowsVirtualKeyCode: 32 });
    check('空格能暂停', true, await waitFor('暂停', async () => (await evaluate(videoState))?.paused === true, 10000));
    await send('Input.dispatchKeyEvent', { type: 'keyDown', key: ' ', code: 'Space', windowsVirtualKeyCode: 32 });
    await send('Input.dispatchKeyEvent', { type: 'keyUp', key: ' ', code: 'Space', windowsVirtualKeyCode: 32 });
    check('空格能继续', true, await waitFor('继续', async () => (await evaluate(videoState))?.paused === false, 10000));
    note(`快捷键前 currentTime=${before?.currentTime}`);
    await shot('05-keys');
  } else {
    note('没有直出样本，跳过快捷键断言');
  }

  log(`\n结果：${pass} 通过，${fail} 失败`);
  log(`截图目录：${OUT}/`);
}

try {
  await main();
} catch (e) {
  fail++;
  log(`异常：${e?.message || e}`);
} finally {
  try {
    chrome.kill();
  } catch {}
  await sleep(300);
  console.log(`play-ui-test: ${pass} 通过，${fail} 失败（详情见 ${REPORT}）`);
  process.exit(fail === 0 ? 0 : 1);
}
