import { useCallback, useSyncExternalStore } from 'react';

/**
 * 界面语言（i18n）。
 *
 * 设计取舍（详见 docs/notes/i18n.md）：
 *
 *  1. **中文当源语言**：`t('搜索')`，键就是中文原文。理由有三 ——
 *     代码与注释本来就是中文，键用原文 diff 最小；不用先发明一套键名再和原文对照；
 *     **漏翻的条目会退化成中文**（用户至少看得懂），而不是露一个 `nav.home` 这种键名。
 *  2. **只带英文目录**：加一门语言 = 加一份目录 + 一个 `Lang` 取值，没有别的改动。
 *     不引 i18next/react-intl：这个项目的文案量用不上复数、性别、日期格式那套，
 *     而多一个依赖就多一份体积与升级负担（前端是 go:embed 进二进制的）。
 *  3. **探测一次，之后以用户选择为准**（localStorage）；选择器在顶栏，
 *     与主题切换放在一起（都是「这台设备上面的显示偏好」）。
 *  4. **后端继续回中文文案**：界面**按 `key` 自己写标题**（首页的行、搜索分面），
 *     后端的中文只作兜底 —— 这样 API 保持语言中立，不必为数据做双语。
 *     已知缺口：服务端的报错文案（如「列表不存在」）仍是中文，见 docs/notes/i18n.md。
 */

export type Lang = 'zh-CN' | 'en-US';

/** 语言列表（选择器用；顺序 = 展示顺序）。 */
export const LANGS: { value: Lang; label: string }[] = [
  { value: 'zh-CN', label: '中文' },
  { value: 'en-US', label: 'English' },
];

const STORAGE_KEY = 'lmby.lang';

/**
 * 英文目录：**键 = 中文原文**。
 *
 * 按页面分组排列，方便对照；同一个中文串在不同页面含义不同时会各写一条
 * （例如「播放」在详情页与播放器里是同一个词，就只留一条）。
 */
const en: Record<string, string> = {
  // ---------------------------------------------------------------- 导航 / 顶栏 / 通用
  '首页': 'Home',
  '搜索': 'Search',
  '海报墙': 'Library',
  '我的列表': 'My Lists',
  '库管理': 'Libraries',
  '直播': 'Live TV',
  '人工匹配': 'Matching',
  '个人中心': 'Account',
  '会话': 'Sessions',
  '设置': 'Settings',
  '退出': 'Sign out',
  '登录': 'Sign in',
  '用户名': 'Username',
  '口令': 'Password',
  '界面语言': 'Language',
  '切换主题': 'Toggle theme',
  '暗色': 'Dark',
  '亮色': 'Light',
  '加载中': 'Loading…',
  '正在加载': 'Loading…',
  '正在加载…': 'Loading…',
  '保存': 'Save',
  '保存中…': 'Saving…',
  '取消': 'Cancel',
  '删除': 'Delete',
  '编辑': 'Edit',
  '关闭': 'Close',
  '返回': 'Back',
  '更多': 'More',
  '全部': 'All',
  '确认': 'OK',
  '重试': 'Retry',
  '上移': 'Move up',
  '下移': 'Move down',
  '移出': 'Remove',
  '加入': 'Add',
  '打开': 'Open',
  '新建': 'Create',
  '改名': 'Rename',
  '空': 'Empty',
  '人': 'People',
  '保存失败': 'Save failed',
  '操作失败': 'Action failed',
  '没有权限': 'Permission denied',

  '（无标题）': '(untitled)',
  '看到 {t}': 'at {t}',
  '读取首页失败': 'Failed to load the home page',
  '正在加载首页…': 'Loading home…',

  // ---------------------------------------------------------------- 首页
  '播放': 'Play',
  '继续播放': 'Resume',
  '详情': 'Details',
  '继续观看': 'Continue watching',
  '进度按账号独立保存': 'Progress is tracked per account',
  '为你推荐': 'Recommended for you',
  '最近添加': 'Recently added',
  '刚入库或元数据刚更新过的': 'Newly scanned or metadata just updated',
  '我的收藏': 'My favorites',
  '评分最高': 'Top rated',
  '还没有观看记录，先从这些开始': 'No watch history yet — start here',
  '根据你最近看过的 {n} 部作品': 'Based on the {n} titles you watched recently',
  '因为你看过《{title}》': 'Because you watched “{title}”',
  '因为你喜欢 {genres}（最近看过 {n} 部作品）':
    'Because you like {genres} ({n} titles watched recently)',
  '你收藏过的 {n} 个条目': '{n} favorites',
  '媒体库里还没有内容。先去': 'Your library is empty. Head to',
  '欢迎使用': 'Welcome to LMBY',
  '欢迎使用 LMBY': 'Welcome to LMBY',
  '还剩 {t}': '{t} left',
  '添加一个媒体库并扫描，条目的海报、简介与演职员会从同目录的 nfo 读进来（本地优先，不联网）。':
    'to add a library and scan. Posters, overviews and cast come from nfo files next to your media (local first, no network).',
  '库管理链接': 'Libraries',
  '轮播上一部': 'Previous featured title',
  '轮播下一部': 'Next featured title',
  '第 {n} 部：{title}': 'Title {n}: {title}',
  '向左滚动': 'Scroll left',
  '向右滚动': 'Scroll right',

  // ---------------------------------------------------------------- 类型 / 角色名（media.ts / people.ts）
  '电影': 'Movie',
  '剧集': 'Series',
  '季': 'Season',
  '集': 'Episode',
  '花絮': 'Extra',
  '演员': 'Actor',
  '导演': 'Director',
  '编剧': 'Writer',
  '制片': 'Producer',
  '作曲': 'Composer',
  '客串': 'Guest star',
  '未知': 'Unknown',

  // ---------------------------------------------------------------- 媒体库选择页（Posters）
  '选择媒体库': 'Choose a library',
  '每个库是一面独立的墙。': 'Every library is its own wall.',
  '还没有媒体库': 'No libraries yet',
  '媒体库是「一个目录树 = 一面海报墙」。先在库管理里把服务器上的目录加进来，扫一次就有了。':
    'A library is “one folder tree = one poster wall”. Add a folder on the server in Libraries, then run a scan.',
  '去建媒体库': 'Create a library',
  '读不到媒体库': 'Cannot load libraries',
  '去库管理看看': 'Open Libraries',
  '正在读取媒体库…': 'Loading libraries…',
  '读取媒体库失败': 'Failed to load libraries',
  '删除{kind}「{name}」？\n（只删这个列表，媒体文件不受影响）':
    'Delete {kind} “{name}”?\n(only the list is deleted — your media files are untouched)',
  '没有匹配的演职员。演职员数据来自媒体同目录的 nfo（本地优先）。':
    'No matching cast or crew. Cast data comes from nfo files next to your media (local first).',

  // ---------------------------------------------------------------- 登录页
  '登录 LMBY': 'Sign in to LMBY',
  '使用你的账号继续。': 'Continue with your account.',
  '正在登录…': 'Signing in…',
  '登录失败，请重试': 'Sign-in failed, please try again',
  '切换到亮色主题': 'Switch to light theme',
  '切换到暗色主题': 'Switch to dark theme',
  '登录以继续': 'Sign in to continue',
  '登录中…': 'Signing in…',
  '登录失败': 'Sign-in failed',
  '口令错误或账号不存在': 'Wrong password or unknown account',
  'LMBY 的账号由管理员在「设置」里创建。': 'Accounts are created by an administrator in Settings.',

  // ---------------------------------------------------------------- 搜索
  '中文按二元组切词（「炼金」能搜到《钢之炼金术师》），英文按整词，并且':
    'Chinese is indexed by bigrams (「炼金」 finds 《钢の錬金術師》), English by whole words, and ',
  '容忍错字': 'typos are tolerated',
  '（「钢之炼金术土」也能命中）。搜的既有': ' (「钢之炼金术土」 still hits). It searches ',
  '（标题 / 原始标题），也有': ' (title / original title) as well as ',
  '的名字 —— 敲几个字就会有联想，点人名可以只看他的作品。':
    ' names — a few characters give you suggestions, and clicking a name shows only their titles.',
  '库 {id}': 'Library {id}',
  '人 #{id}': 'Person #{id}',
  '只看「{name}」参与的作品：': 'Only titles featuring “{name}”: ',
  '「{q}」命中 {n} 位演职员': '{n} cast & crew matched “{q}”',
  '，显示第 {a}~{b} 位': ', showing {a}–{b}',
  '，显示第 {a}~{b} 条': ', showing {a}–{b}',
  '片名、原名或演员名，如「言叶之庭」「宫崎骏」「Matrix」':
    'Title, original title or cast name — e.g. “Your Name”, “Miyazaki”, “Matrix”',
  '输入片名或演员名开始搜索。': 'Type a title or cast name to search.',
  '清空': 'Clear',
  '清空全部': 'Clear all',
  '清除全部': 'Clear all',
  '搜索中…': 'Searching…',
  '搜索失败': 'Search failed',
  '结果': 'Results',
  '媒体库': 'Library',
  '流派': 'Genre',
  '筛选': 'Filters',
  '取消类型筛选': 'Clear type filter',
  '取消流派筛选': 'Clear genre filter',
  '取消媒体库筛选': 'Clear library filter',
  '取消按人筛选': 'Clear person filter',
  '作品': 'Titles',
  '演职员': 'Cast & crew',
  '搜索「{q}」的全部结果': 'Search all results for “{q}”',
  '{n} 部作品': '{n} titles',
  '点「看作品」列出他参与过的条目。': 'Click “View titles” to list what they appear in.',
  '看作品': 'View titles',
  '没有匹配的演职员': 'No matching cast or crew.',
  '演职员数据来自媒体同目录的 nfo（本地优先）。': 'Cast data comes from nfo files next to your media.',
  '没有匹配的条目。试试只搜其中两个字，或者换个译名；也可以点上面的「人」看看':
    'No matches. Try fewer characters or another translation — or check the People tab above',
  '是不是在找某位演职员。': 'in case you are looking for a person.',
  '共 {n} 条': '{n} results',
  '只看': 'Only',
  '参与的作品': 'titles featuring them',
  '上一页': 'Previous',
  '下一页': 'Next',
  '第 {a} / {b} 页': 'Page {a} / {b}',
  '筛选结果共 {n} 条': '{n} filtered results',

  // ---------------------------------------------------------------- 海报墙
  '这里还没有条目。先在「库管理」里扫描一次 —— 扫描会登记文件、导入同目录的 nfo 与图片。':
    'No items here yet. Run a scan in Libraries — it registers files and imports the nfo and images next to them.',
  ' · 加载中…': ' · loading…',
  '媒体库 id 非法': 'Invalid library id',
  '读取失败': 'Failed to load',
  '正在读取…': 'Loading…',
  '共 {n} 个条目': '{n} items',
  '还没扫到条目': 'No items scanned yet',
  '这个库里还没有条目。回「库管理」确认路径，然后让扫描跑一遍。':
    'This library has no items yet. Check the path in Libraries, then run a scan.',
  '排序': 'Sort',
  '标题': 'Title',
  '最近添加排序': 'Recently added',
  '年份': 'Year',
  '评分': 'Rating',
  '筛选：': 'Filter:',
  '类型': 'Type',
  '搜索过滤': 'Filter',
  '没有符合条件的条目。': 'No items match.',
  '清除筛选': 'Clear filters',

  // ---------------------------------------------------------------- 我的列表
  '详情页': 'detail page',
  '新列表的名字': 'New list name',
  '新的列表名': 'New list name',
  '播放列表': 'Playlist',
  '合集': 'Collection',
  '私人播放列表': 'Private playlist',
  '合集（所有人可见）': 'Collection (visible to everyone)',
  '新列表的名字，如「周末补番」': 'New list name — e.g. “Weekend binge”',
  '播放列表是私人的（只有你自己看得到）；': 'Playlists are private (only you can see them);',
  '对所有人可见，由管理员维护。': 'are visible to everyone and maintained by admins.',
  '条目是在影片详情页的「加入列表」里加进来的。': 'Add titles from a movie’s detail page via “Add to list”.',
  '我的': 'Mine',
  '合集（所有人可见）标题': 'Collections (visible to everyone)',
  '还没有任何列表。上面新建一个，然后去影片详情页把它加进去':
    'No lists yet. Create one above, then add titles from a movie’s detail page',
  '（详情页的「加入列表」按钮）。': '(the “Add to list” button).',
  'M5 播放（直出 / 转封装 / 转码 / 直播）': 'M5 playback (direct / remux / transcode / live TV)',
  '{n} 个条目': '{n} items',
  '由 {name} 维护': 'maintained by {name}',
  '删除{kind}「{name}」？': 'Delete {kind} “{name}”?',
  '（只删这个列表，媒体文件不受影响）': '(only the list is deleted — your files are untouched)',
  '列表': 'List',
  '列表不存在': 'List not found',
  '列表名不能为空': 'List name cannot be empty',
  '已经有同名的列表了': 'A list with that name already exists',
  '读取列表失败': 'Failed to load lists',
  '新建失败': 'Create failed',
  '改名失败': 'Rename failed',
  '删除失败': 'Delete failed',
  '排序失败': 'Reorder failed',
  '移出失败': 'Remove failed',
  '加入列表失败': 'Failed to add to list',
  '新建列表失败': 'Failed to create list',
  '列表是空的': 'List is empty',
  '播放全部': 'Play all',
  '调整顺序': 'Reorder',
  '完成排序': 'Done reordering',
  '回列表页': 'Back to lists',
  '这个列表还是空的。到影片的': 'This list is empty. Open a movie’s',
  '点「加入列表」，把想看的加进来': 'and use “Add to list” to queue something up',
  '（收藏按钮旁边）。': '(next to the favorite button).',
  '列表里共有 {n} 个条目，这里显示了前 {m} 个。':
    '{n} items in this list, showing the first {m}.',
  '＋ 加入列表': '+ Add to list',
  '加入列表': 'Add to list',
  '新建列表并加入': 'Create list & add',
  '还没有列表，在下面新建一个。': 'No lists yet — create one below.',
  '正在读取列表…': 'Loading lists…',
  '已加入「{name}」': 'Added to “{name}”',
  '「{name}」里本来就有这个条目': '“{name}” already contains it',
  '{kind} · {n} 个条目': '{kind} · {n} items',

  // ---------------------------------------------------------------- 详情页
  '未刮削': 'Not scraped',
  '来自 nfo': 'From nfo',
  '已匹配': 'Matched',
  '待确认': 'Needs review',
  '已人工处理': 'Handled manually',
  '没找到': 'Not found',
  '{n} 分钟': '{n} min',
  '{h} 小时 {m} 分': '{h} h {m} min',
  '{s} 季 / {e} 集': '{s} seasons / {e} episodes',
  '条目 id 非法': 'Invalid item id',
  '读不到这个条目（可能已被删除，或者链接不对）':
    'Cannot load this item (deleted, or the link is wrong)',
  '读不到这个条目': 'Cannot load this item',
  '已看': 'Watched',
  '已看完': 'Finished',
  '加入「{name}」': 'Add to “{name}”',
  '新建列表名': 'New list name',
  '状态': 'Status',
  '元数据来源 {src}': 'metadata: {src}',
  '文件': 'file',
  '第 {n} 季': 'Season {n}',
  '{n} 集': '{n} episodes',
  '正在读取集列表…': 'Loading episodes…',
  '集（{n}）': 'Episodes ({n})',
  '（{n}）': ' ({n})',
  '条目 #{id}': 'Item #{id}',
  '更新于 {when}': 'updated {when}',
  '刮削留言：{msg}': 'scrape note: {msg}',
  '收藏': 'Favorite',
  '已收藏': 'Favorited',
  '{n} 人收藏': '{n} favorites',
  '从头播放': 'Play from start',
  '编辑元数据': 'Edit metadata',
  '回海报墙': 'Back to library',
  '相关推荐': 'Related',
  '同库里与它流派相近的（评分高的在前）—— 没配 TMDB 也算得出来。':
    'Same library, close genres, higher rated first — works even without TMDB.',
  '演职员表': 'Cast & crew',
  '职员': 'Crew',
  '时长': 'Runtime',
  '分级': 'Rated',
  '原名': 'Original title',
  '工作室': 'Studio',
  '标签': 'Tags',
  '继续观看进度': 'Resume point',
  '版本': 'Version',
  '多版本': 'Versions',
  '还没有简介。': 'No overview yet.',
  '这一条还没有演职员信息。': 'No cast or crew for this title yet.',
  '同目录 nfo 里没有演职员信息。换成自带演职员的 nfo 之后，在「库管理」里勾上「重读 nfo」重扫一次就会出现（不必改媒体文件）。':
    'The nfo next to this file has no cast information. Switch to an nfo that includes it, tick “re-read nfo” in Libraries and rescan (media files are untouched).',
  '（评分高的在前）': '(higher rated first)',
  '{n} 条': '{n} items',

  '不显示': 'hidden',
  '新建并加入': 'Create & add',
  '演职员来自媒体同目录的 nfo（本地优先，不联网）。TMDB 的演职员还没接，所以没有 nfo 的条目这里是空的。':
    'Cast and crew come from the nfo next to your media (local first, no network). TMDB people are not wired up yet, so titles without nfo show nothing here.',
  '这条的同目录 nfo 里没有演职员信息。换成自带演职员的 nfo 之后，在「库管理」里勾上「重读 nfo」重扫一次就会出现（不必改媒体文件）。':
    'The nfo next to this file has no cast information. Switch to an nfo that includes it, tick “re-read nfo” in Libraries and rescan (your media files stay untouched).',
  // ---------------------------------------------------------------- 播放器
  '载入中…': 'Loading…',
  '正在准备播放…': 'Preparing playback…',
  '这个条目现在放不了': 'This title cannot be played right now',
  '重新开始': 'Start over',
  '去条目详情': 'Go to details',
  '为什么这么播': 'Why this stream',
  '隐藏详情': 'Hide details',
  '播放方式': 'Playback method',
  '视频': 'Video',
  '音频': 'Audio',
  '（降为立体声）': ' (downmixed to stereo)',
  '起播位置': 'Start position',
  '播放进度': 'Progress',
  '音量': 'Volume',
  '音轨：自动': 'Audio: auto',
  '字幕：自动': 'Subtitles: auto',
  '字幕：关闭': 'Subtitles: off',
  '（图形，需烧录）': ' (image-based, needs burning)',
  '图形字幕是位图，只能烧进画面；服务端会重新编码一遍':
    'Image-based subtitles are bitmaps — the server re-encodes to burn them in',
  '字幕将烧进画面（需重新编码）': 'Subtitles will be burned in (re-encode)',
  '画质': 'Quality',
  '选低于源分辨率的档会让服务端转码输出': 'Picking a lower resolution makes the server transcode',
  '画质：自动': 'Quality: auto',
  '画质：原生（{h}p）': 'Quality: original ({h}p)',
  '画质：{h}p': 'Quality: {h}p',
  '全屏': 'Fullscreen',
  '快捷键：空格 播放/暂停 · ←/→ 快退快进 10 秒 · F 全屏 · M 静音。':
    'Shortcuts: Space play/pause · ←/→ seek 10s · F fullscreen · M mute.',
  '字幕取回失败：HTTP {status}': 'Failed to fetch subtitles: HTTP {status}',
  '字幕抽取超时': 'Subtitle extraction timed out',
  'libass 渲染器加载失败': 'Failed to load the libass renderer',
  'libass 渲染器未就绪': 'libass renderer is not ready',
  '开始播放失败': 'Failed to start playback',
  '浏览器拦截了自动播放，点一下 ▶ 开始': 'The browser blocked autoplay — press ▶ to start',
  '点一下 ▶ 开始播放': 'Press ▶ to start',
  '这个浏览器既不支持原生 HLS，也不支持 MSE，无法播放转封装流':
    'This browser supports neither native HLS nor MSE, so remuxed streams cannot play',
  '播放出错（{details}）': 'Playback error ({details})',
  '字幕提取失败，本次先不显示字幕': 'Subtitle extraction failed — playing without subtitles',
  '字幕还在抽取中，稍后重新打开播放器就能看到':
    'Subtitles are still being extracted; reopen the player in a moment',
  '续下一段（{at}）…': 'Loading next segment ({at})…',
  '续下一段失败': 'Failed to load the next segment',
  '这个浏览器不允许全屏': 'This browser refuses fullscreen',
  '服务端没配兜底字体（放任意中文字体到 <数据目录>/fonts/fallback.ttf），特效字幕暂时显示不了':
    'No fallback font configured on the server (drop any CJK font at <data dir>/fonts/fallback.ttf) — ASS effects cannot be rendered yet',
  '特效字幕渲染失败，本条字幕暂不显示': 'ASS rendering failed — this subtitle is off for now',
  '特效字幕渲染器加载失败，本条字幕暂不显示': 'Failed to load the ASS renderer — this subtitle is off for now',
  '返回首页': 'Back to home',
  '这个条目在播放列表里的位置': 'Position of this title in the playlist',
  '播放结束': 'Finished',
  '媒体加载失败（服务端可能已回收这段流），点「重新开始」再试':
    'Media failed to load (the server may have recycled the stream) — hit “Start over”',
  '（转封装模式下拖动到已生成窗口之外时，服务端会从新位置重新生成一段，需要一两秒）':
    '(In remux mode, seeking outside the generated window makes the server rebuild from the new position — takes a second or two)',
  '播放决策是「能直出就直出 → 不行就转封装 → 再不行才转码」。上面每一条都是服务端给出的具体原因（例如视频是 10bit HEVC，浏览器解不了，要重新编码成 h264）。':
    'The order is: direct play → remux → transcode (only if needed). Each line above is the concrete reason from the server (e.g. 10-bit HEVC the browser cannot decode, so it gets re-encoded to H.264).',
  '直接播放': 'Direct play',
  '转封装': 'Remux',
  '需要转码': 'Transcode',
  '原文件按 HTTP Range 分段送出，服务端零转码': 'Original file served via HTTP Range — no transcoding',
  '视频不重新编码，只换容器（HLS 分片）': 'Video is copied as-is, only the container changes (HLS segments)',
  '视频要重新编码（例如 10bit HEVC，或客户端选了更低的画质）':
    'Video must be re-encoded (e.g. 10-bit HEVC, or a lower quality was picked)',
  '原样复制': 'copied',
  '重新编码': 're-encoded',
  '转码': 'transcoded',
  '烧录': 'burned in',
  '不使用': 'unused',
  '上一项': 'Previous',
  '下一项': 'Next',
  '字幕': 'Subtitles',
  '音轨': 'Audio',
  '关闭字幕': 'Off',
  '选择字幕': 'Subtitles',
  '选择音轨': 'Audio track',
  '速度': 'Speed',
  '静音': 'Mute',
  '取消静音': 'Unmute',
  '退出全屏': 'Exit fullscreen',
  '网络状态': 'Network',
  '缓冲中': 'Buffering',
  '播放失败': 'Playback failed',
  '这个条目放不了：': 'This title cannot be played:',
  '重试一次': 'Try again',
};
/** 中文目录：中文没有「翻译表」，但保留这一层让 `translate` 的语义一致。 */
const zh: Record<string, string> = {};

const catalogs: Record<Lang, Record<string, string>> = { 'zh-CN': zh, 'en-US': en };

function detectLang(): Lang {
  try {
    const saved = localStorage.getItem(STORAGE_KEY);
    if (saved === 'zh-CN' || saved === 'en-US') return saved;
  } catch {
    /* 隐私模式下 localStorage 会抛：当作没存过 */
  }
  const nav = typeof navigator !== 'undefined' ? navigator.language || '' : '';
  if (/^zh/i.test(nav)) return 'zh-CN';
  if (/^en/i.test(nav)) return 'en-US';
  // 其它语言先给中文：项目的主用户群是中文用户，而英文目录只覆盖界面文案
  return 'zh-CN';
}

let current: Lang = detectLang();
const listeners = new Set<() => void>();

if (typeof document !== 'undefined') {
  document.documentElement.lang = current;
}

/** 当前语言（给组件之外的地方用，比如请求头或日志）。 */
export function getLang(): Lang {
  return current;
}

/** 切语言：写 localStorage、更新 `<html lang>`、通知订阅者（组件重渲染）。 */
export function setLang(l: Lang): void {
  if (l === current || !catalogs[l]) return;
  current = l;
  try {
    localStorage.setItem(STORAGE_KEY, l);
  } catch {
    /* 存不住就算了，至少这一次会话是对的 */
  }
  if (typeof document !== 'undefined') document.documentElement.lang = l;
  listeners.forEach((fn) => fn());
}

/** 订阅（react 的 useSyncExternalStore 用）。 */
export function subscribeI18n(fn: () => void): () => void {
  listeners.add(fn);
  return () => {
    listeners.delete(fn);
  };
}

/**
 * 翻译一条。`{name}` 形式的占位符用 params 替换。
 *
 * 找不到译文时**返回原文**（中文）：这是有意选的降级方式 —— 用户看到中文能懂，
 * 看到 `detail.cast.title` 只会觉得坏了。
 */
export function translate(lang: Lang, key: string, params?: Record<string, string | number>): string {
  const raw = catalogs[lang]?.[key] ?? key;
  if (!params) return raw;
  return raw.replace(/\{(\w+)\}/g, (_, k: string) => {
    const v = params[k];
    return v === undefined ? `{${k}}` : String(v);
  });
}

/** 组件外的简写（用当前语言）。 */
export function t(key: string, params?: Record<string, string | number>): string {
  return translate(current, key, params);
}

/** 组件里用：语言一变就重渲染（语言状态在模块级，不引 Context）。 */
export function useI18n(): {
  lang: Lang;
  setLang: (l: Lang) => void;
  t: (key: string, params?: Record<string, string | number>) => string;
} {
  const lang = useSyncExternalStore(subscribeI18n, getLang, getLang);
  const tr = useCallback(
    (key: string, params?: Record<string, string | number>) => translate(lang, key, params),
    [lang],
  );
  return { lang, setLang, t: tr };
}
