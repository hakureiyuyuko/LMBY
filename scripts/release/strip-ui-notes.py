#!/usr/bin/env python3
"""界面解释性文案剥离（在 main 上跑；发布流程第 3 步）。

做三件事：
  1. 删掉说明性段落：`hint` 类一律删（保留 404 / 权限 / 当前状态三类短提示）；
     其余 `faint*` 类**只删命中了 DROP_PARA_MARKERS 的解释性长句** —— 那个 class 同时
     也用于短状态行（如详情页的「原名 / 制片 / 条目 #id」），不能整类删；
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
KEEP_HINTS = (
    '没有对应页面',
    '只有管理员能改全站设置',
    '现在这个人能看到全部媒体库',
    # 这句是「当前状态」而非解释：它和前面的说明句在同一段里，规则会把说明句清掉，
    # 只留这一句（顺带保住它用到的 current 变量，否则 tsc 报未使用）。
    '当前显示与本机选择不一致',
)

# 非 hint 类（faint / faint small …）里，命中任一子串的**整段**删掉。
# 这些是 M6/M8 新页面的「这页是干什么的」长句；同 class 的短状态行不在列，因此不受影响。
DROP_PARA_MARKERS = (
    '谁在什么时候做了什么',          # 审计日志页
    '它只能用于用户管理接口',        # 管理密钥卡片
    '粘贴/上传的源只在导入这一刻',  # 直播源
    '这里只处理缓存',                # 缓存与清理
    '扫描间隔是每个媒体库自己的设置',  # 扫描计划
    '语言影响标题/简介用哪种译名',  # 设置 - 元数据语言
    '硬件后端能不能用要看',          # 设置 - 服务状态
)

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
    # —— v1.0.1 新增界面的解释性文案 ——
    ('这个文件的内封字幕要现抽出来，第一次会慢一些（几十秒）。等它好了会自动开始播放，之后再看同一部就会立刻加载。',
     '内封字幕要先抽出来，第一次会慢一些（几十秒），抽好后会自动开始播放。',
     'Embedded subtitles are extracted before playback — the first time can take a few tens of seconds. Playback then starts automatically.'),
    ('当前是开发构建（不参与版本比较），最新发布是 {version}',
     '当前是开发构建，最新发布是 {version}',
     'This is a development build; the latest release is {version}'),
    # —— M6/M8 新增界面的解释性文案（页面提示语不在 <p> 里、需逐条压短的）——
    ('这里还没有条目。先在「库管理」里扫描一次 —— 扫描会登记文件、导入同目录的 nfo 与图片。',
     '这里还没有条目。先去「库管理」扫描一次。',
     'No items yet — run a scan in Library management first.'),
    ('已保存（{fields} 个字段{locked}）。锁住的字段重扫重刮都不会被覆盖。',
     '已保存（{fields} 个字段{locked}）。', 'Saved ({fields} fields{locked}).'),
    ('重新刮削：TMDB 的值会写进未锁定的字段（锁住的不动）。\\n队列里没有别的活时几秒内跑完，之后点「刷新」看结果。继续？',
     '重新刮削：TMDB 的值会写进未锁定的字段（锁住的不动）。继续？',
     'Rescrape: TMDB values go into unlocked fields (locked ones untouched). Continue?'),
    ('清空「{name}」的叠加层？只删数据目录里这个库的刮削产物，媒体目录与数据库一个字都不动。',
     '清空「{name}」的刮削产物？媒体目录与数据库不会动。',
     'Clear scrape artifacts for “{name}”? The media directory and database are untouched.'),
    ('右上角随时可以快速切换；这里的设置会同步到账号，换设备登录后同样生效。', None, None),
    # —— 首页行副标题：带「为什么 / 怎么存」的解释，压成一句事实或直接去掉 ——
    ('因为你喜欢 {genres}（最近看过 {n} 部作品）', '因为你喜欢 {genres}', 'Because you like {genres}'),
    ('根据你最近看过的 {n} 部作品', '根据你的观看记录', 'Based on your viewing history'),
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

# 1) 删说明性段落（hint 一律删；其它 faint* 只在命中 DROP 标记时删）
#    - `(?m)^[ \t]*<p …` 行首锚定：只删**独占整行**的段落，不碰
#      `{cond && <p …>…</p>}` 这种行内的（删了会破坏 JSX）；
#    - class 后面可能跟 style={{…}}，所以属性部分用 [^>]* 吃掉；
#    - 结尾要求 `</p>` 后是行尾，保证不会跨段吞并后面的段落。
para = re.compile(r'(?m)^[ \t]*<p className="([^"]+)"[^>]*>[\s\S]*?</p>[ \t]*\n')
removed = 0
for f in files:
    p = ROOT / f
    src = p.read_text(encoding='utf-8')
    out, last, n = [], 0, 0
    for m in para.finditer(src):
        cls, blk = m.group(1), m.group(0)
        if cls == 'hint':
            if any(k in blk for k in KEEP_HINTS):
                continue
        elif cls.startswith('faint'):
            # faint* 也用于短状态行（详情页的「原名 / 制片 / 条目 #id」），只删命中标记的
            if not any(k in blk for k in DROP_PARA_MARKERS):
                continue
        else:
            continue
        out.append(src[last:m.start()])
        last = m.end()
        n += 1
    if n:
        out.append(src[last:])
        p.write_text(''.join(out), encoding='utf-8')
        print(f'  {f}：删 {n} 段说明')
        removed += n

# 1b) 段落删完可能出现「空的条件块」：`{cond && ( )}`（原文案是它唯一的子节点）。
#     留着就是语法错误（实测 LiveSources.tsx 会被 tsc 拦下），整块删掉。
empty_cond = re.compile(r'(?m)^[ \t]*\{[^\n{}]*?&&[ \t]*\(\s*\)\}[ \t]*\n')
for f in files:
    p = ROOT / f
    src = p.read_text(encoding='utf-8')
    out, n = empty_cond.subn('', src)
    if n:
        p.write_text(out, encoding='utf-8')
        print(f'  {f}：删 {n} 个空条件块')

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
    if new is None or ("'" + new + "':") in s:
        # 目标键已存在（或本就该清空）：只删旧条目，避免生成重复键（tsc TS1117）
        s = pat.sub('', s)
    else:
        s = pat.sub("'" + new + "': '" + en.replace("'", "\\'") + "',\n", s)
i18n.write_text(s, encoding='utf-8')

# 4) 删段后的收尾：清掉因文案消失而变成「未使用」的变量（否则 tsc 报 TS6133）
POST_CLEANUP = [
    # SettingsLiveTV 的唯一文案是那段 hint，删掉后 t 就没人用了
    ('export function SettingsLiveTV() {\n  const { t } = useI18n();\n  return (',
     'export function SettingsLiveTV() {\n  return ('),
    # 首页两行的副标题本身就是解释（“进度按账号独立保存”“还没有观看记录，先从这些开始”）：
    # 直接去掉副标题（返回 undefined，RowBlock 就不渲染它）。
    ("      case 'continue':\n        return t('进度按账号独立保存');\n",
     "      case 'continue':\n        return undefined;\n"),
    ("      case 'top':\n        return t('还没有观看记录，先从这些开始');\n",
     "      case 'top':\n        return undefined;\n"),
    # 「继续观看」那一行是独立渲染的（不走 rowSubtitle），副标题也硬编码在那里 —— 去掉。
    ("        <RowBlock title={t('继续观看')} subtitle={t('进度按账号独立保存')} itemsKey=\"continue\">",
     "        <RowBlock title={t('继续观看')} itemsKey=\"continue\">"),
]
for f in files:
    p = ROOT / f
    src = p.read_text(encoding='utf-8')
    orig = src
    for old, new in POST_CLEANUP:
        src = src.replace(old, new)
    if src != orig:
        p.write_text(src, encoding='utf-8')
        print(f'  {f}：收尾清理（未使用变量）')

print(f'\n完成：删 {removed} 段 hint、改 {changed} 个源文件；'
      f'i18n 里 {missing} 条规则未命中（可能已处理过或键名变了，属正常）。')
print('👉 下一步：npm --prefix web run build && (cd web && npx tsc --noEmit)，再人工过一遍 git diff。')
