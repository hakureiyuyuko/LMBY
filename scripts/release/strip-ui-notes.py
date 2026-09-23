#!/usr/bin/env python3
"""界面解释性文案剥离（在 main 上跑；发布流程第 3 步）。

做三件事：
  1. 删掉 `<p className="hint">…</p>` 说明段落（保留 404 / 权限 / 当前状态三类短提示）；
  2. 按下面的 RULES 把长篇机制解释压成一句事实、清掉里程碑代号与内部黑话（M2/M5/M6、
     overlay / ffprobe / ffmpeg 术语…）；
  3. 同步英文目录 `web/src/i18n.ts`（新增/替换键；被清空的条目直接删键）。

**跑完必须人工过一遍**（`git diff` 或截图；`/settings`、`/search`、`/libraries` 最容易漏），
代码注释不动。以后发现新的解释性文案，往 RULES 里加一条即可（不要手改 main）。

用法：python3 scripts/release/strip-ui-notes.py
"""
import pathlib
import re
import subprocess

ROOT = pathlib.Path('.')
if not (ROOT / 'web/src/i18n.ts').exists():
    raise SystemExit('请在仓库根目录运行（找不到 web/src/i18n.ts）')

# 保留的短提示：状态与权限说明，不是「解释」
KEEP_HINTS = ('没有对应页面', '只有管理员能改全站设置', '现在这个人能看到全部媒体库')

# (中文原文, 新文案 或 None=整句清空, 英文)
RULES = [
    # —— 里程碑代号：界面上一律不出现 ——
    ('M5 播放（直出 / 转封装 / 转码 / 直播）', None, None),
    ('扫描入库的原始条目。matching 与海报墙属于 M2 / M5。', '扫描入库的原始条目。', 'Raw items registered by scanning.'),
    ('实例自检：状态、数据库、ffmpeg（与它的硬件加速后端）。首页那份自检面板 M6 搬到了这里 —— 它属于「出问题时才看」的信息，不该占首页的位置。',
     '实例自检：服务状态与数据库。', 'Self-check: service status and database.'),
    # —— 内部实现与黑话 ——
    ('注意：「列出的后端」不等于「真的能用」。例如本机 ffmpeg 列出了 qsv，但核显缺运行时，实际只能用 vaapi —— 所以能力探测会真跑一小段转码来验证。',
     '能力探测会真跑一小段转码，确认后端真的可用。',
     'Capability probing runs a short real transcode to confirm a backend actually works.'),
    ('这个库是只读的：LMBY 不会往它的目录里写任何东西。刮削到的元数据快照与图片写在数据目录的 overlay 层（每库一块、不参与图片缓存淘汰），当前 {files} 个文件 / {size}。',
     '这个库是只读的：不往媒体目录写入任何东西。刮削产物存在数据目录，当前 {files} 个文件 / {size}。',
     'This library is read-only: nothing is written into the media directory. Scrape artifacts live in the data directory — currently {files} files / {size}.'),
    ('已把「{name}」设为只读：刮削产物写进数据目录的 overlay 层，不写媒体目录。',
     '已把「{name}」设为只读：刮削产物写进数据目录，不写媒体目录。',
     '“{name}” is now read-only: scrape artifacts go to the data directory, not the media directory.'),
    ('网盘 / 只读挂载的库打开它：LMBY 不再写媒体目录，刮削产物落进数据目录的 overlay 层',
     '网盘 / 只读挂载的库打开它：不再写媒体目录，刮削产物落进数据目录',
     'Turn this on for network / read-only mounts: nothing is written to the media directory, scrape artifacts go to the data directory'),
    ('ffprobe 读取每个文件的容器/编码/位深/HDR/音轨/字幕/章节信息，写入数据库。\\n扫描结束后会自动排队，这里可以手动补跑。',
     '读取每个文件的编码与音轨/字幕信息。扫描结束后会自动排队，这里可以手动补跑。',
     'Reads codec, audio and subtitle info for every file. Queued automatically after a scan; you can also run it manually here.'),
    ('ffmpeg 日志尾巴（排查「为什么卡/为什么起不来」）', '日志', 'Log'),
    ('原文件按 HTTP Range 分段送出，服务端零转码',
     '直接播放原文件，服务端不重新编码', 'Plays the original file as-is; no server-side re-encoding'),
    ('这个浏览器既不支持原生 HLS，也不支持 MSE，放不了直播流',
     '这个浏览器放不了直播流。', 'This browser cannot play live streams.'),
    ('这个浏览器既不支持原生 HLS，也不支持 MSE，无法播放转封装流',
     '这个浏览器放不了这条流。', 'This browser cannot play this stream.'),
    # —— 长篇机制解释 → 一句事实 ——
    ('演职员来自媒体同目录的 nfo（本地优先，不联网）。TMDB 的演职员还没接，所以没有 nfo 的条目这里是空的。',
     '演职员信息来自媒体目录里的 nfo。', 'Cast & crew come from the nfo beside the media file.'),
    ('这条的同目录 nfo 里没有演职员信息。换成自带演职员的 nfo 之后，在「库管理」里勾上「重读 nfo」重扫一次就会出现（不必改媒体文件）。',
     '这条的 nfo 里没有演职员信息。', 'This item has no cast & crew in its nfo.'),
    ('（转封装模式下拖动到已生成窗口之外时，服务端会从新位置重新生成一段，需要一两秒）',
     '（拖动到尚未生成的区间时，需要一两秒重新生成）',
     '(Seeking past the generated window takes a second or two)'),
    ('图形字幕是位图，只能烧进画面；服务端会重新编码一遍',
     '图形字幕需要重新编码后才能显示。', 'Bitmap subtitles must be re-encoded to be shown.'),
    ('没有匹配的演职员。演职员数据来自媒体同目录的 nfo（本地优先）。',
     '没有匹配的演职员。', 'No matching cast or crew.'),
    ('强制重刮会把 TMDB 的值写进所有条目的未锁定字段（包括有 nfo 的）。\\n锁住的字段与人工改过的字段不会被动。确定继续？',
     '强制重刮会覆盖所有条目的未锁定字段（锁住的与人工改过的不动）。确定继续？',
     'Force rescrape overwrites unlocked fields on every item (locked and manually edited ones are kept). Continue?'),
    ('只有管理员能改全站设置。需要修改时请让管理员登录，或用管理员账号看这一页。',
     '只有管理员能改全站设置。', 'Only administrators can change instance settings.'),
    ('添加一个媒体库并扫描，条目的海报、简介与演职员会从同目录的 nfo 读进来（本地优先，不联网）。',
     '添加一个媒体库并扫描，海报、简介与演职员信息就会出现在这里。',
     'Add a library and run a scan — posters, overviews and cast will show up here.'),
    ('当前没有转码/转封装进程。', '当前没有转码进程。', 'No transcoding sessions right now.'),
]

files = subprocess.check_output(['git', 'ls-files', 'web/src'], text=True).strip().split('\n')
files = [f for f in files if f.endswith(('.tsx', '.ts'))]

# 0) 页脚的里程碑标记（整段多余）
layout = ROOT / 'web/src/components/Layout.tsx'
ls = layout.read_text(encoding='utf-8')
ls2 = ls.replace(" · {t('M5 播放（直出 / 转封装 / 转码 / 直播）')}", '')
if ls2 != ls:
    layout.write_text(ls2, encoding='utf-8')
    print('  Layout.tsx：页脚里程碑去掉')

# 1) 删 hint 段落
hint = re.compile(r'[ \t]*<p className="hint">[\s\S]*?</p>\n')
removed = 0
for f in files:
    p = ROOT / f
    src = p.read_text(encoding='utf-8')
    out, last, n = [], 0, 0
    for m in hint.finditer(src):
        if any(k in m.group(0) for k in KEEP_HINTS):
            continue
        out.append(src[last:m.start()])
        last = m.end()
        n += 1
    if n:
        out.append(src[last:])
        p.write_text(''.join(out), encoding='utf-8')
        print(f'  {f}：删 {n} 段 hint')
        removed += n

# 2) 按规则替换文案（直接替换字符串本身，兼容 t('…', {…}) 这种带参数的形态）
changed = 0
for f in files:
    if f.endswith('i18n.ts'):
        continue
    p = ROOT / f
    src = p.read_text(encoding='utf-8')
    orig = src
    for old, new, _en in RULES:
        if old in src:
            src = src.replace(old, new if new is not None else '')
    if src != orig:
        p.write_text(src, encoding='utf-8')
        changed += 1

# 3) 同步英文目录
i18n = ROOT / 'web/src/i18n.ts'
s = i18n.read_text(encoding='utf-8')
missing = 0
for old, new, en in RULES:
    pat = re.compile(r"'(" + re.escape(old) + r")':[ \t]*\n?[ \t]*'[^']*',\n")
    if not pat.search(s):
        missing += 1
        continue
    if new is None:
        s = pat.sub('', s)
    else:
        s = pat.sub("'" + new + "': '" + en.replace("'", "\\'") + "',\n", s)
i18n.write_text(s, encoding='utf-8')

print(f'\n完成：删 {removed} 段 hint、改 {changed} 个源文件；'
      f'i18n 里 {missing} 条规则未命中（可能已处理过或键名变了，属正常）。')
print('👉 下一步：npm --prefix web run build && (cd web && npx tsc --noEmit)，再人工过一遍 git diff。')
