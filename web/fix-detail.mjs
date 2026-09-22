// 处理详情页里几处「跨行 JSX 文本」的包裹（缩进/换行点不一致，用正则更稳）。
import { readFileSync, writeFileSync } from 'node:fs';

const p = 'web/src/pages/Detail.tsx';
let s = readFileSync(p, 'utf8');
let n = 0;

function sub(re, repl, label) {
  const before = s;
  s = s.replace(re, repl);
  if (s !== before) {
    n++;
    console.log('✓ ' + label);
  } else {
    console.log('✗ 没匹配到：' + label);
  }
}

sub(
  />演职员来自[\s\S]*?这里是空的。</,
  `>{t('演职员来自媒体同目录的 nfo（本地优先，不联网）。TMDB 的演职员还没接，所以没有 nfo 的条目这里是空的。')}<`,
  '演职员说明',
);
sub(
  />这条的同目录 nfo[\s\S]*?不必改媒体文件）。</,
  `>{t('这条的同目录 nfo 里没有演职员信息。换成自带演职员的 nfo 之后，在「库管理」里勾上「重读 nfo」重扫一次就会出现（不必改媒体文件）。')}<`,
  '无演职员提示',
);
sub(
  /(<Link className="btn btn-sm btn-primary" to=\{`\/play\/\$\{ep\.id\}`\}>\s*)播放(\s*<\/Link>\s*<Link className="btn btn-sm btn-ghost" to=\{`\/item\/\$\{ep\.id\}`\}>\s*)详情(\s*<\/Link>)/,
  '$1{t(\'播放\')}$2{t(\'详情\')}$3',
  '集列表的播放/详情按钮',
);

writeFileSync(p, s);
console.log(`共替换 ${n} 处`);
