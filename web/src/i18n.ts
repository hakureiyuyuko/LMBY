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

  // ---------------------------------------------------------------- 库管理（Libraries）
  '只读': 'Read-only',
  '设为只读': 'Make read-only',
  '改为可写': 'Make writable',
  '切换只读失败': 'Failed to toggle read-only',
  '网盘 / 只读挂载的库打开它：LMBY 不再写媒体目录，刮削产物落进数据目录的 overlay 层':
    'Turn this on for network drives / read-only mounts: LMBY stops writing into the media folders and keeps scraped artifacts in an overlay under the data directory',
  '已把「{name}」设为只读：刮削产物写进数据目录的 overlay 层，不写媒体目录。':
    '“{name}” is now read-only: scraped artifacts go into the overlay under the data directory, never into the media folders.',
  '已把「{name}」改为可写。': '“{name}” is writable again.',
  '这个库是只读的：LMBY 不会往它的目录里写任何东西。刮削到的元数据快照与图片写在数据目录的 overlay 层（每库一块、不参与图片缓存淘汰），当前 {files} 个文件 / {size}。':
    'This library is read-only: LMBY never writes inside its folders. Scraped metadata snapshots and images go into an overlay under the data directory (one tree per library, not subject to image-cache eviction) — currently {files} files / {size}.',
  '家庭视频': 'Home videos',
  '混合（推荐）': 'Mixed (recommended)',
  '添加媒体库': 'Add a library',
  '根路径是服务器上的绝对路径。多个路径可以指向不同磁盘；目录约定与 Emby 一致，现有库可以零改名接管。':
    'Root paths are absolute paths on the server. Multiple paths may point at different disks; the folder conventions match Emby, so an existing library works without renaming anything.',
  '名称': 'Name',
  '例如：电影 / 剧集': 'e.g. Movies / TV',
  '根路径（每行一个）': 'Root paths (one per line)',
  '创建中…': 'Creating…',
  '创建媒体库': 'Create library',
  '创建失败': 'Create failed',
  '请填写名称与至少一个根路径': 'Enter a name and at least one root path',
  '扫描只读取文件系统与本地 nfo，不会修改你的文件；图片只登记路径，不入库。':
    'Scanning only reads the filesystem and local nfo files — your files are never modified; images are recorded as paths only, not imported.',
  '还没有媒体库。用上面的表单添加一个根路径即可。':
    'No libraries yet. Add a root path with the form above.',
  '扫描': 'Scan',
  '取消扫描': 'Cancel scan',
  '扫描中：视频 {videos} · 新增 {added} · 条目 {items} · 图片 {images} · {sec} 秒':
    'Scanning: {videos} videos · {added} new · {items} items · {images} images · {sec}s',
  '正在遍历目录…': 'Walking the directory…',
  '图片 {n}': 'Images {n}',
  '启动扫描失败': 'Failed to start the scan',
  '取消扫描失败': 'Failed to cancel the scan',
  '确定删除媒体库「{name}」？其条目与文件记录会一并删除（磁盘文件不受影响）。':
    'Delete the library “{name}”? Its items and file records go away too (files on disk are untouched).',
  '扫描记录': 'Scan history',
  '触发方式': 'Trigger',
  '手动': 'Manual',
  '开始时间': 'Started',
  '耗时': 'Duration',
  '{n} 秒': '{n}s',
  '扫描结果': 'Scan results',
  '视频 {videos} · 新增 {added} · 变化 {changed} · 移动 {moved} · 删除 {deleted} · 未变 {unchanged}':
    'Videos {videos} · new {added} · changed {changed} · moved {moved} · deleted {deleted} · unchanged {unchanged}',
  '新建条目': 'New items',
  '剧集 {series} · 季 {seasons} · 集 {episodes} · 电影 {movies}':
    'Series {series} · seasons {seasons} · episodes {episodes} · movies {movies}',
  '读取 nfo': 'nfo read',
  '图片 / 问题': 'Images / issues',
  '错误': 'Error',
  '还没有扫描记录。': 'No scans yet.',
  '条目（{n}）': 'Items ({n})',
  '扫描入库的原始条目。matching 与海报墙属于 M2 / M5。':
    'The raw items registered by scanning. Matching and the poster wall belong to M2 / M5.',
  '没有条目。': 'No items.',
  '年': 'Year',
  '季/集': 'S/E',
  '文件名标记': 'Filename marker',
  '（无标题，待刮削）': '(untitled, awaiting scrape)',
  '进行中': 'Running',
  '已完成': 'Done',
  '失败': 'Failed',
  '已取消': 'Canceled',
  '元数据刮削': 'Metadata scraping',
  '有本地 nfo 的条目默认不会被刮削（nfo 是当初人工整理的，最权威）；只有没 nfo 的才会去 TMDB 找，找不到或拿不准的进「人工匹配」。':
    'Items with a local nfo are not scraped by default (that nfo was curated by hand and wins); only items without one are looked up on TMDB, and anything missing or uncertain lands in Matching.',
  '还没配 TMDB 凭据：去「设置」页填一个 Read Access Token（或 API Key）就能开始刮削。':
    'No TMDB credentials yet: add a Read Access Token (or API key) in Settings and scraping can start.',
  '来自 nfo {n}': 'from nfo {n}',
  '已匹配 {n}': 'Matched {n}',
  '待确认 {n}': 'Review {n}',
  '没找到 {n}': 'Not found {n}',
  '已人工 {n}': 'Manual {n}',
  '未刮削 {n}': 'Not scraped {n}',
  '一键刮削（{n} 条待处理）': 'Scrape all ({n} pending)',
  '已入队 {n} 条（已有 nfo/已匹配的会自动跳过）':
    '{n} queued (items with an nfo or already matched are skipped automatically)',
  '强制重刮全部': 'Force re-scrape all',
  '强制重刮会把 TMDB 的值写进所有条目的未锁定字段（包括有 nfo 的）。\n锁住的字段与人工改过的字段不会被动。确定继续？':
    'A forced re-scrape writes TMDB values into every item’s unlocked fields (including items with an nfo).\nLocked and hand-edited fields are left alone. Continue?',
  '已强制入队 {n} 条': 'Force-queued {n} items',
  '重置失败的': 'Reset failed',
  '已重置 {reset} 条失败记录，并重新入队 {enqueued} 条':
    'Reset {reset} failed records and queued {enqueued} items again',
  '刷新': 'Refresh',
  '已刷新': 'Refreshed',
  '去人工匹配': 'Open matching',
  '流信息探测': 'Stream probing',
  'ffprobe 读取每个文件的容器/编码/位深/HDR/音轨/字幕/章节信息，写入数据库。\n扫描结束后会自动排队，这里可以手动补跑。':
    'ffprobe reads each file’s container/codecs/bit depth/HDR/audio/subtitles/chapters and stores it in the database.\nProbes are queued automatically after a scan; you can also run them by hand here.',
  '已完成 {n}': 'Done {n}',
  '待探测 {n}': 'Pending {n}',
  '失败 {n}': 'Failed {n}',
  '把待探测的文件入队': 'Queue files awaiting probe',
  '已入队 {n} 个探测任务': 'Queued {n} probe jobs',
  '重置失败的探测': 'Reset failed probes',
  '已重置 {reset} 个失败的探测，重新入队 {enqueued} 个':
    'Reset {reset} failed probes and re-queued {enqueued}',
  '扫描问题（{n}）': 'Scan issues ({n})',
  '不致命但值得看一眼：文件过小、识别不了、nfo 解析失败等。':
    'Not fatal, but worth a look: tiny files, unrecognised names, unparseable nfo, and so on.',
  '级别': 'Level',
  '路径': 'Path',
  '说明': 'Note',
  '收起': 'Collapse',
  '展开全部 {n} 条': 'Show all {n}',

  // ---------------------------------------------------------------- 条目编辑（Item）
  '条目': 'Item',
  '读取条目失败': 'Failed to load the item',
  '标记失败': 'Failed to update the watched state',
  '已标记为看过': 'Marked as watched',
  '已标记为未看': 'Marked as unwatched',
  ' · 条目 {id} · 状态 {state}': ' · item {id} · {state}',
  ' · 匹配分 {score}': ' · match {score}',
  '机器给的结论：{msg}': 'The scraper says: {msg}',
  '已锁定 {n} 个字段': '{n} fields locked',
  '（重扫与重刮都不会覆盖它们）': '(rescans and re-scrapes will not overwrite them)',
  '返回详情页': 'Back to details',
  '全部解锁': 'Unlock all',
  '重新刮削这条': 'Re-scrape this item',
  '没有改动。': 'Nothing changed.',
  '已保存（{fields} 个字段{locked}）。锁住的字段重扫重刮都不会被覆盖。':
    'Saved ({fields} fields{locked}). Locked fields survive rescans and re-scrapes.',
  '，锁定 {n} 个': ', {n} locked',
  '本来就没有锁定任何字段。': 'No fields are locked.',
  '解锁全部 {n} 个字段？之后重扫/重刮会重新覆盖它们。':
    'Unlock all {n} fields? Rescans and re-scrapes will overwrite them again.',
  '已全部解锁。': 'All fields unlocked.',
  '解锁失败': 'Failed to unlock',
  '重新刮削：TMDB 的值会写进未锁定的字段（锁住的不动）。\n队列里没有别的活时几秒内跑完，之后点「刷新」看结果。继续？':
    'Re-scrape: TMDB values are written into unlocked fields (locked ones are left alone).\nIf the queue is idle this finishes in seconds — hit “Refresh” afterwards to see the result. Continue?',
  '已入队，跑完后点「刷新」看结果。': 'Queued — hit “Refresh” after it finishes to see the result.',
  '队列里已经有这条的任务了，跑完后点「刷新」看结果。':
    'This item is already queued — hit “Refresh” after it finishes to see the result.',
  '入队失败': 'Failed to queue',
  '原始标题': 'Original title',
  '首播日期': 'Air date',
  '分钟': 'minutes',
  '简介': 'Overview',
  '标语': 'Tagline',
  '制片公司': 'Studios',
  '元数据 id': 'Metadata id',
  '列表与排序都用它；不能为空': 'Used for listings and sorting; cannot be empty',
  '原名/译名，匹配打分器会拿它再搜一次':
    'Original or alternate title; the matcher uses it for another search',
  '如 PG-13 / TV-14': 'e.g. PG-13 / TV-14',
  '逗号分隔': 'comma separated',
  '键=值，逗号分隔，如 tmdb=603': 'key=value, comma separated, e.g. tmdb=603',
  '服务端会在开播前做一次播放决策：能直出就直出（原文件 + HTTP Range，最省资源），否则就只换容器（转封装，视频不重新编码），两者都不行才需要转码 —— 具体选了哪条、为什么，播放器里点「为什么这么播」看得到。':
    'Before playback starts the server makes a decision: direct play when possible (original file over HTTP Range — cheapest), otherwise remux only (container change, video untouched), and transcoding only if neither works. The player’s “Why this way?” panel shows what was chosen and why.',
  '文件 {container} · 视频 {video} · 音频 {audio}': 'File {container} · video {video} · audio {audio}',
  '（无）': '(none)',
  ' · {n} 条字幕': ' · {n} subtitles',
  '继续播放（{time}）': 'Resume ({time})',
  '标记为已看': 'Mark as watched',
  '已看过 {n} 次': 'Watched {n} times',
  '字段': 'Fields',
  '改完点上面的「保存」。勾上「锁定」的字段，重新扫描（nfo 重读）与重新刮削（TMDB）都不会覆盖它 —— 这是「人工改过的数据不被机器冲掉」的唯一保证。没锁的字段会被自动流程补上。留空表示清空（与自动刮削的「空值不覆盖」相反，人工编辑写什么就是什么）。':
    'Hit “Save” above when done. A field marked “locked” is never overwritten by a rescan (nfo re-read) or a re-scrape (TMDB) — that is the only guarantee that hand-edited data survives automation. Unlocked fields get filled in by the automatic pipeline. Leaving a field empty clears it (unlike automatic scraping, where empty values do not overwrite; a manual edit is taken literally).',
  '值': 'Value',
  '锁定': 'Lock',
  '（分钟）': '(minutes)',
  '已改': 'Edited',
  '已锁': 'Locked',
  '未锁': 'Unlocked',
  '{label} 需要整数（清空就留空）': '{label} must be an integer (leave it empty to clear)',
  '{label} 需要数字（清空就留空）': '{label} must be a number (leave it empty to clear)',
  '{label} 要写成键=值（如 tmdb=603），收到「{got}」':
    '{label} must be key=value (e.g. tmdb=603), got “{got}”',
  '{label} 要写成 YYYY-MM-DD': '{label} must be YYYY-MM-DD',

  // ---------------------------------------------------------------- 直播（LiveTV）
  '直播电视': 'Live TV',
  '读取频道失败': 'Failed to load channels',
  '频道': 'Channels',
  '频道（{n}）': 'Channels ({n})',
  '没有匹配的频道。': 'No channels match.',
  '导入播放列表后就能在这里看电视。点频道就地起播；同一频道所有观众共享一路 ffmpeg（两个人看不会把源站拉两遍）。':
    'Import a playlist and you can watch TV here. Clicking a channel starts playback in place; every viewer of a channel shares one ffmpeg process (two people watching will not pull the source twice).',
  '点频道就地起播；同一频道所有观众共享一路推流，切台不用离开页面。':
    'Clicking a channel starts playback in place; every viewer of a channel shares one stream, and switching channels never leaves the page.',
  '去下面的「直播源」导入一份播放列表（粘贴 / 上传 / 订阅地址都行）。':
    'Import a playlist under “Live sources” below (paste, upload, or a subscription URL).',
  '换个筛选条件试试。': 'Try a different filter.',
  '搜频道名…': 'Search channel names…',
  '全部分组（{n}）': 'All groups ({n})',
  '分组': 'Group',
  '未分组': 'Ungrouped',
  '探测状态：全部': 'Probe: all',
  '只看失效（上次探测不通）': 'Failed only (last probe failed)',
  '只看能通': 'OK only',
  '只看没探过的': 'Never probed',
  '只看启用': 'Enabled only',
  '只看收藏': 'Favorites only',
  '只看收藏（{n}）': 'Favorites only ({n})',
  '导出 m3u': 'Export m3u',
  '共 {total} 台 · 启用中 {enabled} · 这里显示 {shown} 台':
    '{total} channels · {enabled} enabled · {shown} shown here',
  ' · 正在看：{name}（{n} 个观众）': ' · watching: {name} ({n} viewers)',
  '正在起播…': 'Starting…',
  '起播': 'Play',
  '起播失败': 'Failed to start playback',
  '播放出错（{detail}）': 'Playback error ({detail})',
  '浏览器拦了自动播放，点一下播放键开始': 'The browser blocked autoplay — hit play to start',
  '这个浏览器既不支持原生 HLS，也不支持 MSE，放不了直播流':
    'This browser supports neither native HLS nor MSE, so live streams cannot play',
  ' · 浏览器起播 {ms} ms': ' · browser start {ms} ms',
  '重新载入': 'Reload',
  '停止': 'Stop',
  '编辑频道': 'Edit channel',
  '这里改的是「怎么用」（名字 / 分组 / 排序 / logo）。下次刷新订阅源时会按播放列表还原 —— 播放列表才是频道的来源；「启用状态」与「收藏」不会被刷新覆盖。':
    'What you change here is how the channel is used (name / group / order / logo). Refreshing the source restores values from the playlist — the playlist is the source of truth; enabled state and favorites are not overwritten.',
  '排序（同分组内，小的在前）': 'Order (within a group, smaller first)',
  'logo 地址': 'Logo URL',
  '已保存「{name}」': 'Saved “{name}”',
  '已停用': 'Disabled',
  '已停用「{name}」（不再出现在「只看启用」里）':
    'Disabled “{name}” (it no longer shows under “Enabled only”)',
  '已启用「{name}」': 'Enabled “{name}”',
  '启用': 'Enable',
  '停用': 'Disable',
  '失效': 'Failed',
  '通': 'OK',
  '未探': 'Unprobed',
  '上次探测不通': 'Last probe failed',
  '上次探测能通': 'Last probe succeeded',
  '还没探测过': 'Not probed yet',
  ' · 上次探测 {when}': ' · last probed {when}',
  ' · 带请求头': ' · with request headers',
  '先启用这条频道': 'Enable this channel first',
  '取消收藏': 'Unfavorite',
  '★ 已收藏': '★ Favorited',
  '☆ 收藏': '☆ Favorite',
  '收藏失败': 'Failed to update the favorite',
  '外链': 'Link',
  '生成 24 小时有效的免登录链接': 'Create a sign-in-free link valid for 24 hours',
  '外链（24 小时有效，无需登录）：{url}':
    'Public link (valid for 24 hours, no sign-in needed): {url}',
  '生成外链失败': 'Failed to create the public link',
  '复制地址': 'Copy address',
  '已复制「{name}」的地址': 'Copied the address of “{name}”',
  '「{name}」的地址：{url}': 'Address of “{name}”: {url}',

  // ---------------------------------------------------------------- 直播源与探测（LiveSources）
  '直播源': 'Live sources',
  '管理直播源': 'Manage live sources',
  '读取直播源失败': 'Failed to load live sources',
  '订阅源会按各自的间隔自动刷新（间隔 0 = 只手动刷）。展开可以导入新的播放列表、改间隔、看上次刷新结果。':
    'Subscription sources refresh automatically at their own interval (0 = manual only). Expand to import a new playlist, change the interval, or check the last refresh result.',
  '导入播放列表': 'Import a playlist',
  '订阅地址': 'Subscription URL',
  '粘贴文本': 'Paste text',
  '上传文件': 'Upload file',
  '名称（留空则用地址 / 「手动导入」）': 'Name (leave empty to use the URL, or “Manual import”)',
  '例如：重庆联通单播': 'e.g. My ISP playlist',
  '手动导入': 'Manual import',
  '播放列表内容': 'Playlist content',
  '播放列表文件（.m3u / .m3u8 / .txt）': 'Playlist file (.m3u / .m3u8 / .txt)',
  '已读入 {name}（{bytes} 字节）': 'Loaded {name} ({bytes} bytes)',
  '自动刷新间隔（分钟，0 = 不自动刷新）': 'Auto-refresh interval (minutes; 0 = manual only)',
  '粘贴/上传的源只在导入这一刻有内容，所以它们的「刷新」按钮不会有（要更新就再导入一次）。':
    'Pasted/uploaded sources only have content at the moment of import, so they get no “Refresh” button (re-import to update).',
  '导入中…': 'Importing…',
  '导入': 'Import',
  '导入失败': 'Import failed',
  '已有的源（{n}）': 'Existing sources ({n})',
  '还没有任何直播源：上面导入一份播放列表就能看电视了。':
    'No live sources yet — import a playlist above and you can start watching.',
  '频道 {n}': '{n} channels',
  '新增 {added} / 更新 {updated} / 保留 {kept}{removed}（这一版共 {total} 条）':
    '{added} added / {updated} updated / {kept} kept{removed} ({total} in this revision)',
  ' / 清掉 {n}': ' / {n} removed',
  '已导入「{name}」：{summary}': 'Imported “{name}”: {summary}',
  '已刷新「{name}」：{summary}': 'Refreshed “{name}”: {summary}',
  '刷新失败': 'Refresh failed',
  '删除直播源「{name}」？\n\n它导入的 {count} 台频道会保留（只是不再自动刷新，也不会再被这次删除影响）。要连频道一起清掉得另外手动处理。':
    'Delete the live source “{name}”?\n\nThe {count} channels it imported stay (they just stop auto-refreshing and are unaffected by this deletion). Removing those too is a separate manual step.',
  '已删除直播源「{name}」': 'Deleted the live source “{name}”',
  '读文件失败': 'Failed to read the file',
  '上次刷新 {when}：{status}（这一版 {n} 条）':
    'Last refreshed {when}: {status} ({n} in this revision)',
  '还没刷新过': 'Never refreshed',
  '间隔': 'Interval',
  '自动刷新间隔（分钟），0 = 不自动刷新': 'Auto-refresh interval (minutes); 0 = manual only',
  '按地址重新拉取并增量更新': 'Re-fetch from the URL and update incrementally',
  '粘贴/上传的源没有可重拉的地址': 'Pasted/uploaded sources have no URL to re-fetch',
  '频道探测（失效源标记）': 'Channel probing (dead-source marks)',
  '读取探测进度失败': 'Failed to load probe progress',
  '起探测失败': 'Failed to start the probe',
  '真连一次源站看它出不出得来流（死源常常 TCP 连得上，是在协议握手那一步挂掉的），结果写回每条频道的「通 / 失效」。全量探测每个频道都要连一次，以分钟计；关掉页面不会中断，回来接着看进度。':
    'Actually connect to the source once to see whether a stream comes out (dead sources often accept the TCP connection and fail at the protocol handshake). The result is written back to each channel as OK / failed. A full probe connects to every channel once and takes minutes; closing the page does not interrupt it — come back to watch the progress.',
  '启用中的频道 {total} 台：能通 {ok} · 失效 {failed} · 没探过 {pending}':
    'Enabled channels: {total} — OK {ok} · failed {failed} · never probed {pending}',
  '正在探测 {done}/{total}（已探完中：通 {ok} / 不通 {failed}）· 已用 {sec} 秒':
    'Probing {done}/{total} (of those finished: OK {ok} / failed {failed}) · {sec}s elapsed',
  ' · 当前：{name}': ' · now: {name}',
  '所有频道都探过了': 'Every channel has been probed',
  '只探「没探过」的那些': 'Probe only the never-probed ones',
  '补探没探过的': 'Probe the unprobed',
  '补探没探过的（{n}）': 'Probe the unprobed ({n})',
  '重探全部': 'Re-probe all',
  '每个启用的频道都重新连一次（源站地址没变但源站坏了，只有重探才知道）':
    'Reconnect to every enabled channel (if the URL is unchanged but the source broke, only a re-probe reveals it)',
  '刷新进度': 'Refresh progress',

  // ---------------------------------------------------------------- 设置页（Settings）
  '元数据与服务状态': 'Metadata & status',
  '只有管理员能改全站设置。需要修改时请让管理员登录，或用管理员账号看这一页。':
    'Only administrators can change server-wide settings. Ask an administrator to sign in, or open this page with an admin account.',
  '元数据源（TMDB）': 'Metadata provider (TMDB)',
  'TMDB 用来刮削元数据与回源图片。填 Read Access Token（v4，推荐）或 API Key（v3），二选一即可。密钥只写不回显，保存后立刻生效、不必重启。数据库里的设置优先于 config.toml。':
    'TMDB is used to scrape metadata and fetch images. Provide a Read Access Token (v4, recommended) or an API key (v3) — either one works. Secrets are write-only (never echoed back), and changes take effect immediately with no restart. Settings in the database take precedence over config.toml.',
  '读取设置失败': 'Failed to load settings',
  '当前状态：': 'Status: ',
  '已配置': 'Configured',
  '未配置': 'Not configured',
  '来源 {from} · 密钥 {enc} · Read Token {rt} · API Key {ak}':
    'Source {from} · secret {enc} · Read Token {rt} · API Key {ak}',
  '数据库（本页保存的）': 'database (saved on this page)',
  'config.toml / 环境变量': 'config.toml / environment',
  '加密存储': 'encrypted at rest',
  '明文存储（密钥文件不可用）': 'stored in plain text (secret key file unavailable)',
  '已设置': 'set',
  '未设置': 'not set',
  'Read Access Token（v4）': 'Read Access Token (v4)',
  'API Key（v3）': 'API Key (v3)',
  '已设置 —— 要替换就输入新的（留空不改）': 'Already set — enter a new one to replace it (leave empty to keep)',
  '粘贴 eyJhbGciOi…': 'paste eyJhbGciOi…',
  '32 位十六进制': '32 hex digits',
  '语言': 'Language',
  '语言影响标题/简介用哪种译名；改了会顺手清掉旧语言的缓存。':
    'The language decides which translations are used for titles and overviews; changing it also clears the cache of the old language.',
  '已保存并立刻生效（不必重启）。': 'Saved and active immediately (no restart needed).',
  '换了语言，顺手清掉了 {n} 条旧语言的元数据缓存。':
    'The language changed, so {n} cached metadata entries in the old language were cleared.',
  '测试中…': 'Testing…',
  '测试连接': 'Test connection',
  '测试失败': 'Test failed',
  '恢复为配置文件的值': 'Reset to config.toml values',
  '删掉数据库里的 TMDB 设置，恢复成 config.toml 里的值？':
    'Delete the TMDB settings in the database and restore the config.toml values?',
  '已恢复为配置文件的值。': 'Restored to the config.toml values.',
  '已恢复为配置文件的值（配置文件里没有凭据）。':
    'Restored to the config.toml values (config.toml has no credentials).',
  '恢复失败': 'Reset failed',
  '连通正常：搜「{q}」拿到 {n} 条结果（{ms} ms）':
    'Connection OK: searching “{q}” returned {n} results ({ms} ms)',
  '，例如 {samples}': ', e.g. {samples}',
  '连接失败：{error}': 'Connection failed: {error}',
  '服务状态': 'Service status',
  '实例自检：状态、数据库、ffmpeg（与它的硬件加速后端）。首页那份自检面板 M6 搬到了这里 —— 它属于「出问题时才看」的信息，不该占首页的位置。':
    'Self-check: status, database, ffmpeg (and its hardware acceleration backends). This panel moved here from the bottom of Home in M6 — it is troubleshooting information and does not deserve space on the home page.',
  '运行正常': 'Running',
  '降级运行': 'Degraded',
  '异常': 'Error',
  '数据库': 'Database',
  '不可用': 'unavailable',
  '运行时长': 'Uptime',
  'ffmpeg 路径': 'ffmpeg path',
  '硬件加速后端': 'Hardware acceleration backends',
  '注意：「列出的后端」不等于「真的能用」。例如本机 ffmpeg 列出了 qsv，但核显缺运行时，实际只能用 vaapi —— 所以能力探测会真跑一小段转码来验证。':
    'Note: backends being listed does not mean they work. Here, for instance, ffmpeg lists qsv but the iGPU lacks its runtime, so only vaapi is usable — which is why capability probing actually runs a short transcode to verify.',
  '系统信息': 'System information',
  '数据库字符集不对：encoding={enc}、lc_ctype={ctype}。中文搜索与模糊匹配会静默少结果。停掉服务后跑 {script} 就地重建。':
    'Wrong database character set: encoding={enc}, lc_ctype={ctype}. Chinese search and fuzzy matching will silently return fewer results. Stop the service and run {script} to rebuild in place.',
  '数据库字符集': 'Database character set',
  '数据库结构版本': 'Schema version',
  '条目 / 文件 / 图片': 'Items / files / images',
  '队里待跑的任务': 'Queued tasks',
  '服务器时间': 'Server time',
  '{m} 分 {s} 秒': '{m} min {s} s',
  '{d} 天 {h} 小时': '{d} d {h} h',

  // ---------------------------------------------------------------- 人工匹配（Match）
  '覆盖顺序是「本地 nfo / 本地图优先 → 缺的才去 TMDB 刮」。这里只处理机器拿不准的：':
    'The order is: local nfo and local images first, then TMDB for whatever is missing. This page only handles what the machine is unsure about: ',
  '（有候选但不够确定，多半是 TMDB 上有多条同名条目）与':
    ' (candidates exist but the lead is not decisive — usually several same-named entries on TMDB) and ',
  '（搜不到候选）。候选连同打分明细是刮削时存下来的，打开就能选，不用重新搜。':
    ' (no candidates at all). Candidates and their score breakdown were stored during scraping, so you can just pick one — no need to search again.',
  '没有待确认的条目 —— 都匹配好了。': 'Nothing awaiting review — everything is matched.',
  '没有「没找到」的条目。': 'No “not found” items.',
  '共 {total} 条，显示前 {shown} 条 —— 勾选卡片左上角可以批量操作':
    '{total} items, showing the first {shown} — tick the boxes in the card corners for batch actions',
  '已选 {n} 条': '{n} selected',
  '排到队尾刮削': 'Queue scraping at the end',
  '强制重刮（覆盖未锁字段）': 'Force re-scrape (overwrite unlocked fields)',
  '标记不需要匹配': 'Mark as “no match needed”',
  '全选本页': 'Select all on this page',
  '清除选择': 'Clear selection',
  '勾选后可批量操作': 'Tick to enable batch actions',
  '把选中的 {n} 条标记为「不需要匹配」？': 'Mark the {n} selected items as “no match needed”?',
  '给选中的 {n} 条排到队尾重刮，并强制覆盖已有元数据（锁住的字段不动）？':
    'Queue a forced re-scrape for the {n} selected items, overwriting existing metadata (locked fields untouched)?',
  '给选中的 {n} 条排一次刮削（已有元数据的会自动跳过）？':
    'Queue a scrape for the {n} selected items (items that already have metadata are skipped automatically)?',
  '人工批量标记：不需要匹配': 'Manual batch mark: no match needed',
  '已处理 {n} 条{skipped}。': 'Processed {n} items{skipped}.',
  '，跳过 {n} 条': ', skipped {n}',
  '批量操作失败': 'Batch action failed',
  '已应用，这条以后不会再被自动覆盖': 'Applied — this item will not be overwritten automatically again',
  '应用失败': 'Failed to apply',
  '为什么不需要匹配？（会记在条目上）': 'Why is no match needed? (this note is stored on the item)',
  '自制/测试片，不需要元数据': 'Home video / test clip — no metadata needed',
  '已标记为「不需要匹配」': 'Marked as “no match needed”',
  ' · 分数 {n}': ' · score {n}',
  '分数 {n}': 'score {n}',
  ' · 领先 {n}': ' · lead {n}',
  ' · 靠别名命中「{alias}」': ' · matched an alias “{alias}”',
  ' · 原名 {title}': ' · original title {title}',
  ' · 状态 {state}': ' · {state}',
  '换个词再搜': 'Try another search term',
  '编辑字段与锁定': 'Edit fields & locks',
  '正在读取候选…': 'Loading candidates…',
  '没有候选。换个词再搜，或者标记为不需要匹配。':
    'No candidates. Try another search term, or mark the item as “no match needed”.',
  '用这条': 'Use this one',

  // ---------------------------------------------------------------- 个人中心（Account）
  '账号：{name}': 'Account: {name}',
  '（管理员）': ' (admin)',
  '注册于 {when}': 'registered {when}',
  '资料': 'Profile',
  '显示名': 'Display name',
  '显示名会出现在界面右上角。': 'The display name appears in the top-right corner.',
  '留空则显示用户名': 'Leave empty to show the username',
  '已保存': 'Saved',
  '两次输入的新口令不一致': 'The two new passwords do not match',
  '口令已修改': 'Password changed',
  '口令已修改，并已让其它 {n} 个设备下线':
    'Password changed, and {n} other devices were signed out',
  '修改失败': 'Change failed',
  '修改口令': 'Change password',
  '需要先验证当前口令。修改成功后，除当前设备外的其它登录会话都会立即失效。':
    'Your current password is verified first. After a successful change, every session except this device is invalidated immediately.',
  '当前口令': 'Current password',
  '新口令': 'New password',
  '确认新口令': 'Confirm new password',
  '提交中…': 'Submitting…',
  '外观': 'Appearance',
  '右上角随时可以快速切换；这里的设置会同步到账号，换设备登录后同样生效。':
    'The top-right corner switches at any time; the setting here syncs to your account and applies on other devices too.',
  '（当前显示与本机选择不一致，已按本机选择显示）':
    ' (the display does not match this device’s choice — this device’s choice is used)',
  '跟随系统': 'Follow system',
  '已在本机切换，但同步到账号失败': 'Switched on this device, but syncing to the account failed',
  '我的设备': 'My devices',
  '当前有效的登录会话。撤销某个会话会立即让对应设备退出登录。':
    'Active sign-in sessions. Revoking a session signs that device out immediately.',
  '读取设备列表失败': 'Failed to load the device list',
  '撤销失败': 'Failed to revoke',
  '没有有效会话。': 'No active sessions.',
  '设备 / 客户端': 'Device / client',
  '来源 IP': 'Source IP',
  '最后活跃': 'Last seen',
  '当前': 'Current',
  '撤销': 'Revoke',
  '未知客户端': 'Unknown client',
  '其它': 'Other',
  '未知系统': 'Unknown OS',

  // ---------------------------------------------------------------- 会话监控（Sessions）
  '播放会话': 'Playback sessions',
  '读取会话失败': 'Failed to load sessions',
  '终止失败': 'Failed to terminate',
  '当前正在播放的会话。直出与转封装/转码都会出现在这里；转码那一路的实时状态见下表。':
    'Sessions playing right now. Direct play, remux and transcode all show up here; the live state of each transcode is in the table below.',
  '当前没有人在播放。': 'Nobody is watching right now.',
  '方式': 'Mode',
  '起始位置': 'Start',
  '空闲': 'Idle',
  '转码 / 转封装会话': 'Transcode / remux sessions',
  '每一路 ffmpeg 的实时状态。速度低于 1x 就跟不上播放（画面会卡）；显示「节流中」是好事 —— 说明它已经跑在客户端前面，正歇着等，避免白烧 CPU。':
    'Live state of every ffmpeg process. A speed below 1x means it cannot keep up (playback stalls); “throttled” is good news — it means ffmpeg has run ahead of the client and is idle-waiting instead of burning CPU.',
  '当前没有转码/转封装进程。': 'No transcode/remux processes right now.',
  '码率': 'Bitrate',
  '分片': 'Segments',
  '已生成': 'Generated',
  '客户端': 'Client',
  '领先': 'Ahead',
  '（节流中）': ' (throttled)',
  '终止': 'Terminate',
  'ffmpeg 日志尾巴（排查「为什么卡/为什么起不来」）':
    'Tail of the ffmpeg log (to debug “why does it stall / why does it not start”)',

  // ---------------------------------------------------------------- 杂项（App / Setup / Browse / api）
  '正在加载 LMBY…': 'Loading LMBY…',
  '页面不存在': 'Page not found',
  '这个地址在 LMBY 里没有对应页面。': 'This address has no page in LMBY.',
  '返回概览': 'Back to home',
  '无法连接到服务器，请检查网络或服务是否在运行':
    'Cannot reach the server — check your network or whether the service is running',
  'Light 的 Emby': 'Light’s Emby',
  '按标题': 'By title',
  '按年份': 'By year',
  '初始化 LMBY': 'Set up LMBY',
  '这是第一次启动。请创建管理员账号，口令至少 8 个字符。':
    'This is the first launch. Create the administrator account — the password needs at least 8 characters.',
  '显示名（可留空）': 'Display name (optional)',
  '用于界面显示': 'Shown in the interface',
  '确认口令': 'Confirm password',
  '两次输入的口令不一致': 'The two passwords do not match',
  '初始化失败，请重试': 'Setup failed, please try again',
  '正在创建…': 'Creating…',
  '创建并进入': 'Create and continue',

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
