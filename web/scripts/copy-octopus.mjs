// 把 libass-wasm 的运行时文件复制到 public/subtitles-octopus/，随 vite 一起进 dist。
//
// 为什么不 import 进来打包：这几个是 emscripten 的产物（worker + wasm），运行时必须
// 按**独立文件路径**加载 —— worker 自己会去同目录找 .wasm，打进 bundle 就找不到自己了。
// 放 public/ 让 vite 原样复制到 dist 根，前端按固定路径 /subtitles-octopus/xxx 懒加载。
//
// 顺带把 COPYRIGHT 一起带上：libass-wasm 是 LGPL-2.1-or-later（含 FFmpeg 组件），
// 分发时要保留声明、并保证这个库本身可被替换 —— 以独立文件形式提供正好满足。
import { copyFileSync, mkdirSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const from = resolve(here, '../node_modules/libass-wasm/dist/js');
const to = resolve(here, '../public/subtitles-octopus');

const files = [
  'subtitles-octopus.js',
  'subtitles-octopus-worker.js',
  'subtitles-octopus-worker.wasm',
  'subtitles-octopus-worker-legacy.js',
  'COPYRIGHT',
];

mkdirSync(to, { recursive: true });
for (const f of files) {
  copyFileSync(resolve(from, f), resolve(to, f));
}
console.log(`subtitles-octopus：已复制 ${files.length} 个文件到 public/subtitles-octopus/`);
