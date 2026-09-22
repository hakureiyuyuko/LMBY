// i18n 覆盖率（i18n 覆盖工具）：扫前端源码，数一数还有多少**界面文案没包 t(...)**
//
// 为什么要有它：i18n 最怕的不是翻错，而是**漏翻**——「切英文之后某个按钮还是中文」
// 这种事只有用户撞上才会发现。把它变成一条能随时跑、能进 CI 的数字，
// 覆盖率就只会往好的方向走。
//
// 判据（启发式，够用就好）：
//   1. 单/双引号字符串里有中文，且前面不是 `t(`  —— 例如 `aria-label="上一页"`、`placeholder="搜索"`
//   2. JSX 文本节点（`>文本<`）里有中文 —— 例如 `<h2>还没有媒体库</h2>`
// 不算：
//   - 注释（先剥掉）
//   - 模板字符串（`${}` 里的拼接要人工判断，太多误报）
//   - 已经包了 t(...) 的
//   - catalog 本体（i18n.ts 里的 en 目录）
//
// 用法：
//   node scripts/dev/i18n-coverage.mjs                 # 打印明细 + 合计
//   node scripts/dev/i18n-coverage.mjs --max 700       # 超过 700 就退出码 1（守住覆盖率）
//   node scripts/dev/i18n-coverage.mjs --examples 5     # 每个文件多打几条例子
import { readdirSync, readFileSync, statSync } from 'node:fs';
import path from 'node:path';

const ROOT = 'web/src';
const args = process.argv.slice(2);
const maxIdx = args.indexOf('--max');
const MAX = maxIdx >= 0 ? Number(args[maxIdx + 1]) : null;
const exIdx = args.indexOf('--examples');
const EXAMPLES = exIdx >= 0 ? Number(args[exIdx + 1]) : 2;

/** 剥掉块注释与行注释（注释里的中文不是文案）。 */
function stripComments(src) {
  return src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|[^:])\/\/[^\n]*/g, '$1');
}

function files(dir) {
  const out = [];
  for (const name of readdirSync(dir)) {
    const p = path.join(dir, name);
    if (statSync(p).isDirectory()) out.push(...files(p));
    else if (/\.(ts|tsx)$/.test(name)) out.push(p);
  }
  return out;
}

const HAN = /\p{Script=Han}/u;

/** 数一个文件里「没包 t() 的界面文案」。 */
function scan(file, src) {
  const code = stripComments(src);
  const found = [];

  // 1) 字符串字面量里的中文
  const strRe = /(['"])((?:(?!\1)[^\\]|\\.)*?)\1/gs;
  for (const m of code.matchAll(strRe)) {
    const text = m[2];
    if (!HAN.test(text)) continue;
    const before = code.slice(Math.max(0, m.index - 3), m.index);
    if (/t\($/.test(before)) continue; // 已经包了 t(
    const line = code.slice(0, m.index).split('\n').length;
    found.push({ kind: 'str', line, text: text.slice(0, 40) });
  }

  // 2) JSX 文本节点里的中文（`>中文<`，且不在 {} 表达式里）
  const jsxRe = />([^<>{}]*\p{Script=Han}[^<>{}]*)</gu;
  for (const m of code.matchAll(jsxRe)) {
    const text = m[1].trim();
    if (!text) continue;
    const line = code.slice(0, m.index).split('\n').length;
    found.push({ kind: 'jsx', line, text: text.slice(0, 40) });
  }

  return found;
}

const results = [];
for (const f of files(ROOT)) {
  if (f.endsWith('i18n.ts')) continue; // 目录本体不算「漏翻」
  const found = scan(f, readFileSync(f, 'utf8'));
  if (found.length > 0) results.push({ file: f, found });
}
results.sort((a, b) => b.found.length - a.found.length);

// ---- 第二项检查：用了 `t('...')` 但英文目录里没这条 ----
// 这类错误最阴险：`t()` 找不到译文时会**退化成中文**（这是有意的降级），
// 于是「切了英文某处还是中文」看上去像漏包，实际是漏写目录。静态扫一遍就能抓。
const catalogFile = path.join(ROOT, 'i18n.ts');
const catalogSrc = readFileSync(catalogFile, 'utf8');
const enStart = catalogSrc.indexOf('const en: Record<string, string> = {');
const enEnd = catalogSrc.indexOf('\n};', enStart);
const enKeys = new Set(
  [...catalogSrc.slice(enStart, enEnd).matchAll(/^\s{2}'([^']+)':/gm)].map((m) => m[1]),
);
const usedKeys = new Map();
for (const f of files(ROOT)) {
  if (f.endsWith('i18n.ts')) continue;
  const code = stripComments(readFileSync(f, 'utf8'));
  for (const m of code.matchAll(/\bt\(\s*'([^']+)'/g)) {
    if (!usedKeys.has(m[1])) usedKeys.set(m[1], f);
  }
}
const missing = [...usedKeys.entries()].filter(([k]) => !enKeys.has(k));
const unused = [...enKeys].filter((k) => !usedKeys.has(k));

let total = 0;
console.log('i18n 覆盖：还没包 t(...) 的界面文案\n');
for (const r of results) {
  total += r.found.length;
  console.log(`  ${String(r.found.length).padStart(4)}  ${r.file.replace(/^web\/src\//, '')}`);
  for (const ex of r.found.slice(0, EXAMPLES)) {
    console.log(`        第 ${ex.line} 行 [${ex.kind}] ${ex.text}`);
  }
}
console.log(`\n合计：${total} 条待翻译（${results.length} 个文件）`);

if (missing.length > 0) {
  console.log(`\n⚠️  ${missing.length} 条用了 t(...) 但英文目录里没有（会退化成中文）：`);
  for (const [k, f] of missing.slice(0, 30)) {
    console.log(`   ${k}   ← ${f.replace(/^web\/src\//, '')}`);
  }
} else {
  console.log('\n✓ 所有 t(...) 的键都在英文目录里');
}

if (unused.length > 0) {
  console.log(`\n（提示：${unused.length} 条英文目录里的键暂时没人用，可能是历史残留）`);
  for (const k of unused.slice(0, 15)) console.log(`   ${k}`);
}

if (MAX !== null) {
  if (total > MAX) {
    console.error(`\n✗ 超出预算：${total} > ${MAX}（说明这次改动新增了未翻译的文案）`);
    process.exit(1);
  }
  console.log(`✓ 在预算内：${total} <= ${MAX}`);
}
