/** LMBY 后端接口客户端。 */

import { t } from './i18n';

export interface User {
  id: number;
  username: string;
  displayName: string;
  isAdmin: boolean;
  isDisabled: boolean;
  createdAt: string;
  lastLoginAt?: string;
}

export interface Preferences {
  theme: string;
  language: string;
  subtitlePrefs?: Record<string, unknown>;
  audioPrefs?: Record<string, unknown>;
  libraryViews?: Record<string, unknown>;
}

export interface Me {
  user: User;
  preferences: Preferences;
}

export interface Meta {
  version: string;
  versionFull: string;
  setupRequired: boolean;
  ffmpeg: boolean;
}

export interface Health {
  status: 'ok' | 'degraded' | 'error';
  version: string;
  uptimeSeconds: number;
  database: { ok: boolean; latencyMs?: number; error?: string };
  ffmpeg: {
    path: string;
    available: boolean;
    version: string;
    hw_accels: string[] | null;
    error?: string;
  };
}

export interface SessionInfo {
  id: string;
  createdAt: string;
  lastSeenAt: string;
  expiresAt: string;
  userAgent: string;
  ip: string;
  current: boolean;
}

export interface ChangePasswordResult {
  ok: boolean;
  revokedSessions: number;
}

/** 后端返回的结构化错误。 */
export class ApiError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  if (init.body !== undefined) {
    headers.set('Content-Type', 'application/json');
  }

  let res: Response;
  try {
    res = await fetch(path, { ...init, headers, credentials: 'same-origin' });
  } catch {
    throw new ApiError(0, t('无法连接到服务器，请检查网络或服务是否在运行'));
  }

  const text = await res.text();
  let data: unknown = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = null;
    }
  }

  if (!res.ok) {
    const message =
      data && typeof data === 'object' && 'error' in data
        ? String((data as { error: unknown }).error)
        : `请求失败（HTTP ${res.status}）`;
    throw new ApiError(res.status, message);
  }

  return data as T;
}

const json = (body: unknown): RequestInit => ({ body: JSON.stringify(body) });

export const api = {
  meta: () => request<Meta>('/api/v1/meta'),
  health: () => request<Health>('/healthz'),

  me: () => request<Me>('/api/v1/auth/me'),
  login: (username: string, password: string) =>
    request<Me>('/api/v1/auth/login', { method: 'POST', ...json({ username, password }) }),
  logout: () => request<{ ok: boolean }>('/api/v1/auth/logout', { method: 'POST' }),
  setup: (username: string, password: string, displayName: string) =>
    request<Me>('/api/v1/setup', { method: 'POST', ...json({ username, password, displayName }) }),

  updateProfile: (displayName: string) =>
    request<Me>('/api/v1/auth/me', { method: 'PATCH', ...json({ displayName }) }),
  updatePreferences: (prefs: Preferences) =>
    request<Me>('/api/v1/auth/me/preferences', {
      method: 'PATCH',
      ...json(prefs),
    }),
  changePassword: (currentPassword: string, newPassword: string) =>
    request<ChangePasswordResult>('/api/v1/auth/password', {
      method: 'POST',
      ...json({ currentPassword, newPassword }),
    }),

  sessions: () => request<{ sessions: SessionInfo[] }>('/api/v1/auth/sessions'),
  revokeSession: (id: string) =>
    request<{ ok: boolean }>(`/api/v1/auth/sessions/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    }),

  // ---------------------------------------------------------------- 媒体库
  libraries: () => request<{ libraries: LibrarySummary[] }>('/api/v1/libraries'),
  library: (id: number) => request<LibraryDetail>(`/api/v1/libraries/${id}`),
  createLibrary: (name: string, kind: string, paths: string[]) =>
    request<LibrarySummary>('/api/v1/libraries', { method: 'POST', ...json({ name, kind, paths }) }),
  /** 改名 / 改类型 / 替换根路径 / 只读开关，共用一个 PATCH。 */
  updateLibrary: (id: number, body: { name?: string; kind?: string; paths?: string[]; readonly?: boolean }) =>
    request<{ ok: boolean }>(`/api/v1/libraries/${id}`, { method: 'PATCH', ...json(body) }),
  /** 清空某个只读库的叠加层（只删数据目录里那一块，媒体目录与数据库不动）。 */
  clearLibraryOverlay: (id: number) =>
    request<{ ok: boolean; files: number; bytes: number }>(`/api/v1/libraries/${id}/overlay`, {
      method: 'DELETE',
    }),
  /** 各库叠加层占用汇总（设置页看板）。 */
  overlayStats: () =>
    request<{
      root: string;
      libraries: { libraryId: number; name: string; files: number; bytes: number }[];
      total: { files: number; bytes: number };
    }>('/api/v1/overlay'),
  deleteLibrary: (id: number) =>
    request<{ ok: boolean }>(`/api/v1/libraries/${id}`, { method: 'DELETE' }),

  startScan: (id: number) =>
    request<{ scanRunId: number }>(`/api/v1/libraries/${id}/scan`, { method: 'POST' }),
  cancelScan: (id: number) =>
    request<{ ok: boolean }>(`/api/v1/libraries/${id}/scan`, { method: 'DELETE' }),
  scanStatus: (id: number) =>
    request<{ running: boolean; progress: ScanProgress | null; lastScan: ScanRun | null; issues: ScanIssue[] }>(
      `/api/v1/libraries/${id}/scan`,
    ),
  items: (id: number, kind = '', limit = 100, offset = 0, matchState: string[] = []) =>
    request<ItemsPage>(
      `/api/v1/libraries/${id}/items?kind=${encodeURIComponent(kind)}&limit=${limit}&offset=${offset}` +
        (matchState.length > 0 ? `&matchState=${encodeURIComponent(matchState.join(','))}` : ''),
    ),

  // ---------------------------------------------------------------- 浏览（海报墙 / 子项）
  browse: (
    libraryId: number,
    params: { kind?: string; sort?: string; limit?: number; offset?: number } = {},
  ) => {
    const sp = new URLSearchParams();
    if (params.kind) sp.set('kind', params.kind);
    if (params.sort) sp.set('sort', params.sort);
    if (params.limit) sp.set('limit', String(params.limit));
    if (params.offset) sp.set('offset', String(params.offset));
    return request<BrowsePage>(`/api/v1/libraries/${libraryId}/browse?${sp.toString()}`);
  },
  children: (itemId: number) => request<ChildSummary>(`/api/v1/items/${itemId}/children`),
  batchItems: (body: { action: 'scrape' | 'skip'; itemIds: number[]; force?: boolean; reason?: string }) =>
    request<BatchResult>('/api/v1/items/batch', { method: 'POST', ...json(body) }),

  // ---------------------------------------------------------------- 播放（M3）
  /**
   * 开始播放。body.profile 是前端实测出的能力（见 capabilities.ts）——
   * 服务端拿它决定「能不能直出 / 要不要转封装」。不传则用服务端的保守默认档。
   */
  startPlayback: (itemId: number, body: StartPlaybackBody = {}) =>
    request<PlaybackState>(`/api/v1/items/${itemId}/play`, { method: 'POST', ...json(body) }),
  playbackState: (sessionId: string) =>
    request<PlaybackState>(`/api/v1/play/${encodeURIComponent(sessionId)}`),
  seekPlayback: (sessionId: string, positionTicks: number) =>
    request<PlaybackState>(`/api/v1/play/${encodeURIComponent(sessionId)}/seek`, {
      method: 'POST',
      ...json({ positionTicks }),
    }),
  reportProgress: (sessionId: string, positionTicks: number, durationTicks: number) =>
    request<{ progress: PlaybackProgress; played: boolean }>(
      `/api/v1/play/${encodeURIComponent(sessionId)}/progress`,
      { method: 'POST', ...json({ positionTicks, durationTicks }) },
    ),
  stopPlayback: (sessionId: string, body: { positionTicks?: number; durationTicks?: number } = {}) =>
    request<{ ok: boolean }>(`/api/v1/play/${encodeURIComponent(sessionId)}/stop`, {
      method: 'POST',
      ...json(body),
    }),
  itemPlaylist: (itemId: number) => request<ItemPlaylist>(`/api/v1/items/${itemId}/playlist`),
  /**
   * 取这个文件**内封的字体**（mkv 附件），交给前端 libass 渲染 `\fn` 引用的特效字体。
   *
   * 服务端要把整部片子的附件抽出来（附件常在文件末尾，得把源文件读一遍；网络盘上
   * 大文件好几分钟），所以这里会轮询 202。超时或报错都返回空数组 —— 字幕退化成
   * 兜底字体，不影响播放。
   */
  attachmentFonts: async (sessionId: string, tries = 60, delayMs = 3000): Promise<AttachmentFont[]> => {
    const path = `/api/v1/play/${encodeURIComponent(sessionId)}/fonts`;
    for (let i = 0; i < tries; i++) {
      let res: Response;
      try {
        res = await fetch(path, { credentials: 'same-origin' });
      } catch {
        return [];
      }
      if (res.status === 200) {
        const body = (await res.json().catch(() => null)) as { fonts?: AttachmentFont[] } | null;
        return body?.fonts ?? [];
      }
      if (res.status !== 202) return []; // 404 / 502 等：这次没有字体可用
      await new Promise((r) => setTimeout(r, delayMs));
    }
    return [];
  },
  /** 活跃播放会话 + 转码/转封装会话（监控页用；非管理员只看到自己的播放会话）。 */
  playSessions: () =>
    request<{ sessions: PlaySessionInfo[]; transcodeSessions: TranscodeSessionStat[] }>(
      '/api/v1/playback/sessions',
    ),
  /** 强制终止某一路转码/转封装进程（管理员）。 */
  stopTranscodeSession: (key: string) =>
    request<{ ok: boolean }>(`/api/v1/playback/streams/${encodeURIComponent(key)}/stop`, {
      method: 'POST',
    }),
  itemProgress: (itemId: number) =>
    request<{ progress: PlaybackProgress | null }>(`/api/v1/items/${itemId}/progress`),
  continueWatching: (limit = 20) =>
    request<{ items: ContinueWatchingEntry[] }>(`/api/v1/continue?limit=${limit}`),
  setPlayed: (itemIds: number[], played: boolean) =>
    request<{ ok: boolean; count: number; played: boolean }>('/api/v1/items/played', {
      method: 'POST',
      ...json({ itemIds, played }),
    }),

  // ---------------------------------------------------------------- 刮削（库级）
  scrapeStatus: (libraryId: number) =>
    request<{ configured: boolean; scrape: ScrapeProgress }>(`/api/v1/libraries/${libraryId}/scrape`),
  enqueueScrapes: (libraryId: number, body: { force?: boolean; kind?: string } = {}) =>
    request<{ enqueued: number; scrape: ScrapeProgress }>(`/api/v1/libraries/${libraryId}/scrape`, {
      method: 'POST',
      ...json(body),
    }),
  resetFailedScrapes: (libraryId: number) =>
    request<{ reset: number; enqueued: number; scrape: ScrapeProgress }>(
      `/api/v1/libraries/${libraryId}/scrape/reset`,
      { method: 'POST' },
    ),

  // ---------------------------------------------------------------- 设置（管理员）
  settings: () => request<SettingsPayload>('/api/v1/settings'),
  updateTMDBSettings: (body: { readToken?: string; apiKey?: string; language?: string }) =>
    request<{ ok: boolean; tmdb: TMDBSettings; clearedCache: number }>('/api/v1/settings/tmdb', {
      method: 'PUT',
      ...json(body),
    }),
  resetTMDBSettings: () =>
    request<{ ok: boolean; tmdb: TMDBSettings }>('/api/v1/settings/tmdb', { method: 'DELETE' }),
  testProvider: (q?: string) =>
    request<ProviderTestResult>(
      `/api/v1/provider/test${q ? `?q=${encodeURIComponent(q)}` : ''}`,
      { method: 'POST' },
    ),

  // ---------------------------------------------------------------- 收藏
  /** 我的收藏（按账号；可按类型过滤、可翻页）。 */
  favorites: (params: { kind?: string; limit?: number; offset?: number } = {}) => {
    const sp = new URLSearchParams();
    if (params.kind) sp.set('kind', params.kind);
    if (params.limit) sp.set('limit', String(params.limit));
    if (params.offset) sp.set('offset', String(params.offset));
    const qs = sp.toString();
    return request<FavoritePage>(`/api/v1/favorites${qs ? `?${qs}` : ''}`);
  },

  /** 单条条目的收藏状态：我收藏了没 + 全站收藏数。 */
  favoriteState: (itemId: number) =>
    request<FavoriteState>(`/api/v1/items/${itemId}/favorite`),

  /** 收藏 / 取消收藏（幂等）。响应里带新状态与新的总数，界面直接用，不必再查。 */
  setFavorite: (itemId: number, favorite: boolean) =>
    request<FavoriteState & { ok: boolean }>(`/api/v1/items/${itemId}/favorite`, {
      method: 'POST',
      ...json({ favorite }),
    }),

  // ---------------------------------------------------------------- 播放列表 / 合集
  /** 我的列表 + 所有合集（我的在前）。 */
  lists: () => request<{ playlists: PlaylistSummary[] }>('/api/v1/playlists'),

  /** 新建播放列表（kind='collection' 需要管理员）。 */
  createList: (body: { name: string; kind?: string; overview?: string }) =>
    request<{ playlist: PlaylistSummary }>('/api/v1/playlists', {
      method: 'POST',
      ...json(body),
    }),

  list: (id: number) => request<{ playlist: PlaylistSummary }>(`/api/v1/playlists/${id}`),

  /** 改名 / 改说明：**没给的字段不会被改**（给空串是「清空说明」）。 */
  updateList: (id: number, body: { name?: string; overview?: string }) =>
    request<{ playlist: PlaylistSummary }>(`/api/v1/playlists/${id}`, {
      method: 'PATCH',
      ...json(body),
    }),

  deleteList: (id: number) =>
    request<{ ok: boolean }>(`/api/v1/playlists/${id}`, { method: 'DELETE' }),

  listItems: (id: number, params: { limit?: number; offset?: number } = {}) => {
    const sp = new URLSearchParams();
    if (params.limit) sp.set('limit', String(params.limit));
    if (params.offset) sp.set('offset', String(params.offset));
    const qs = sp.toString();
    return request<PlaylistItemsPage>(`/api/v1/playlists/${id}/items${qs ? `?${qs}` : ''}`);
  },

  /** 批量加入（追加到末尾，幂等：重复加入不会报错也不会多一条）。 */
  addToList: (id: number, itemIds: number[]) =>
    request<{ ok: boolean; added: number; itemCount: number }>(`/api/v1/playlists/${id}/items`, {
      method: 'POST',
      ...json({ itemIds }),
    }),

  removeFromList: (id: number, itemId: number) =>
    request<{ ok: boolean }>(`/api/v1/playlists/${id}/items/${itemId}`, { method: 'DELETE' }),

  /** 按给定顺序重排（整串写一遍）。 */
  reorderList: (id: number, itemIds: number[]) =>
    request<{ ok: boolean }>(`/api/v1/playlists/${id}/items`, {
      method: 'PUT',
      ...json({ itemIds }),
    }),

  /** 某个条目在列表里的前后邻居与位次（播放器的「下一项」）。 */
  listNeighbors: (id: number, itemId: number) =>
    request<PlaylistNeighbors>(`/api/v1/playlists/${id}/neighbors?itemId=${itemId}`),

  // ---------------------------------------------------------------- 首页
  /**
   * 首页一次取全：轮播 + 继续观看 + 推荐行。
   *
   * 为什么不拆成三四个请求：首页的首屏感觉直接由请求数决定，而且几行之间有先后关系
   * （有没有观看记录，决定了出「为你推荐」还是「评分最高」）。
   */
  home: () => request<HomePayload>('/api/v1/home'),

  // ---------------------------------------------------------------- 搜索
  /**
   * 搜索条目。q 可以为空，但那时**必须**至少给一个筛选条件（库/类型/流派/人），
   * 否则后端回 400 —— 空词返回全部是库列表接口的活。
   */
  search: (params: {
    q?: string;
    libraryId?: number;
    kind?: string;
    genre?: string;
    personId?: number;
    limit?: number;
    offset?: number;
  }) => {
    const sp = new URLSearchParams({ q: params.q ?? '' });
    if (params.libraryId) sp.set('libraryId', String(params.libraryId));
    if (params.kind) sp.set('kind', params.kind);
    if (params.genre) sp.set('genre', params.genre);
    if (params.personId) sp.set('personId', String(params.personId));
    if (params.limit) sp.set('limit', String(params.limit));
    if (params.offset) sp.set('offset', String(params.offset));
    return request<SearchPage>(`/api/v1/search?${sp.toString()}`);
  },

  /**
   * 结果分面（类型 / 媒体库 / 流派 / 人 的命中数）。
   *
   * 筛选参数与 search 必须**完全一致**，否则分面的数字与结果对不上。
   */
  searchFacets: (params: {
    q?: string;
    libraryId?: number;
    kind?: string;
    genre?: string;
    personId?: number;
  }) => {
    const sp = new URLSearchParams({ q: params.q ?? '' });
    if (params.libraryId) sp.set('libraryId', String(params.libraryId));
    if (params.kind) sp.set('kind', params.kind);
    if (params.genre) sp.set('genre', params.genre);
    if (params.personId) sp.set('personId', String(params.personId));
    return request<SearchFacetsPage>(`/api/v1/search/facets?${sp.toString()}`);
  },

  /** 「人」这一档的结果列表（只认 q，人没有库与流派归属）。 */
  searchPeople: (params: { q: string; limit?: number; offset?: number }) => {
    const sp = new URLSearchParams({ q: params.q });
    if (params.limit) sp.set('limit', String(params.limit));
    if (params.offset) sp.set('offset', String(params.offset));
    return request<SearchPeoplePage>(`/api/v1/search/people?${sp.toString()}`);
  },

  /** 即时联想（作品 + 人）。前缀匹配 / 错字容忍与搜索结果同一套规则。 */
  searchSuggest: (params: { q: string; limit?: number }) => {
    const sp = new URLSearchParams({ q: params.q });
    if (params.limit) sp.set('limit', String(params.limit));
    return request<SearchSuggestPage>(`/api/v1/search/suggest?${sp.toString()}`);
  },

  // ---------------------------------------------------------------- 直播电视（M5）
  /**
   * 频道列表。筛选全在服务端做（`probe=failed` 就是「只看失效的」）。
   *
   * 注意 url 是**频道自己的地址**（要复制到外部播放器用），而后端**不会**回显
   * 自定义请求头的内容，只说 `hasHeaders`。
   */
  liveChannels: (params: {
    q?: string;
    group?: string;
    enabled?: boolean;
    favorites?: boolean;
    probe?: string;
    /** 前台用：只留「能用能看的」—— 探测过且不通的频道不出现。 */
    hideFailed?: boolean;
  } = {}) => {
    const sp = new URLSearchParams();
    if (params.q) sp.set('q', params.q);
    if (params.group) sp.set('group', params.group);
    if (params.enabled) sp.set('enabled', '1');
    if (params.favorites) sp.set('favorites', '1');
    if (params.probe) sp.set('probe', params.probe);
    if (params.hideFailed) sp.set('hide_failed', '1');
    const qs = sp.toString();
    return request<TVChannelPage>(`/api/v1/livetv/channels${qs ? `?${qs}` : ''}`);
  },
  liveChannel: (id: number) => request<TVChannel>(`/api/v1/livetv/channels/${id}`),
  updateLiveChannel: (
    id: number,
    patch: { name?: string; group?: string; logo?: string; sortOrder?: number; disabled?: boolean },
  ) => request<TVChannel>(`/api/v1/livetv/channels/${id}`, { method: 'PATCH', ...json(patch) }),
  toggleLiveFavorite: (id: number) =>
    request<{ ok: boolean; favorite: boolean }>(`/api/v1/livetv/channels/${id}/favorite`, {
      method: 'POST',
    }),

  liveSources: () => request<TVSourcePage>('/api/v1/livetv/sources'),
  createLiveSource: (body: {
    name: string;
    kind: 'paste' | 'file' | 'url';
    url?: string;
    content?: string;
    refreshIntervalMinutes?: number;
  }) => request<{ source: TVSource; import: TVImport; total: number }>('/api/v1/livetv/sources', {
    method: 'POST',
    ...json(body),
  }),
  updateLiveSource: (
    id: number,
    body: { name?: string; url?: string; enabled?: boolean; refreshIntervalMinutes?: number },
  ) => request<TVSource>(`/api/v1/livetv/sources/${id}`, { method: 'PATCH', ...json(body) }),
  deleteLiveSource: (id: number) =>
    request<{ ok: boolean }>(`/api/v1/livetv/sources/${id}`, { method: 'DELETE' }),
  refreshLiveSource: (id: number) =>
    request<{ source: TVSource; import: TVImport; total: number; enabled: number }>(
      `/api/v1/livetv/sources/${id}/refresh`,
      { method: 'POST' },
    ),

  /** 导出启用的频道为 m3u（直接当 <a download> 用；要含停用的加 `?all=1`）。 */
  liveExportURL: (all = false) => `/api/v1/livetv/export.m3u${all ? '?all=1' : ''}`,

  /**
   * 起播一个频道：同一频道所有观众共享一路 ffmpeg，所以这里返回的 sid
   * 只是「我这次观看」的票，不是新起的一路流。
   *
   * codecs：浏览器能解的编码（capabilities.ts 的 detectProfile()）。服务端拿它
   * 判断「转封装够不够」—— HEVC / MPEG-2 这类它解不开的会直接转码。
   * force：'transcode' 是「刚才放不出来，转码重试一次」；'copy' 是明确要求别转。
   */
  startLivePlay: (
    channelId: number,
    body: { codecs?: string[]; force?: 'transcode' | 'copy' } = {},
  ) =>
    request<LivePlayback>(`/api/v1/livetv/channels/${channelId}/play`, {
      method: 'POST',
      ...json(body),
    }),
  stopLivePlay: (sid: string) =>
    request<{ ok: boolean; viewers: number }>(`/api/v1/live/${encodeURIComponent(sid)}/stop`, {
      method: 'POST',
    }),
  shareLiveChannel: (channelId: number, hours = 24) =>
    request<{ path: string; url: string; expiresAt: string; channelId: number; name: string }>(
      `/api/v1/livetv/channels/${channelId}/share`,
      { method: 'POST', ...json({ hours }) },
    ),
  liveSessions: () =>
    request<{ sessions: LiveSessionInfo[]; count: number }>('/api/v1/livetv/sessions'),

  /** 频道探测进度（探了多少、通不通全部由服务端从库里现算）。 */
  liveProbeStatus: () => request<TVProbeStatus>('/api/v1/livetv/channels/probe'),
  /**
   * 起一次频道探测（管理员；后台跑，立刻回 202）。
   * 三个条件都可选，都不给就是全探启用中的频道。
   */
  startLiveProbe: (body: { onlyUnknown?: boolean; group?: string; channelIds?: number[] } = {}) =>
    request<{ running: boolean }>('/api/v1/livetv/channels/probe', { method: 'POST', ...json(body) }),

  // ---------------------------------------------------------------- 条目详情与人工编辑
  item: (id: number) => request<ItemDetail>(`/api/v1/items/${id}`),
  /** 演职员（目前只有 nfo 这一个来源）。 */
  itemPeople: (id: number) =>
    request<{ itemId: number; people: ItemPerson[]; source: string }>(`/api/v1/items/${id}/people`),
  /** 相关推荐（同库 + 同类型 + 共同流派，离线可算）。 */
  itemRelated: (id: number, limit = 12) =>
    request<{ items: Item[]; total: number }>(`/api/v1/items/${id}/related?limit=${limit}`),
  updateItem: (id: number, body: { fields?: Record<string, unknown>; lockedFields?: string[] }) =>
    request<ItemDetail>(`/api/v1/items/${id}`, { method: 'PATCH', ...json(body) }),
  rescrapeItem: (id: number, force = true) =>
    request<{ taskId: number; enqueued: boolean; force: boolean }>(`/api/v1/items/${id}/scrape`, {
      method: 'POST',
      ...json({ force }),
    }),

  // ---------------------------------------------------------------- 人工匹配
  itemMatch: (id: number) => request<MatchDetail>(`/api/v1/items/${id}/match`),
  applyMatch: (id: number, providerId: number) =>
    request<{ ok: boolean; matchState: string }>(`/api/v1/items/${id}/match`, {
      method: 'POST',
      ...json({ providerId }),
    }),
  searchMatch: (id: number, query: string) =>
    request<{ query: string; candidates: MatchCandidate[] }>(`/api/v1/items/${id}/match/search`, {
      method: 'POST',
      ...json({ query }),
    }),
  skipMatch: (id: number, reason: string) =>
    request<{ ok: boolean; matchState: string }>(`/api/v1/items/${id}/match/skip`, {
      method: 'POST',
      ...json({ reason }),
    }),

  // ---------------------------------------------------------------- 后台任务
  tasks: () =>
    request<{ stats: TaskStats; byKind: Record<string, number>; recent: TaskInfo[] }>('/api/v1/tasks'),
  enqueueProbes: (id: number) =>
    request<{ enqueued: number; probe: ProbeProgress }>(`/api/v1/libraries/${id}/probe`, {
      method: 'POST',
    }),
  resetFailedProbes: (id: number) =>
    request<{ reset: number; enqueued: number; probe: ProbeProgress }>(
      `/api/v1/libraries/${id}/probe/reset`,
      { method: 'POST' },
    ),
};

// ---------------------------------------------------------------- 媒体库类型

/** 内封字体（mkv 附件）的一条：前端把 url 交给 libass 去取。 */
export interface AttachmentFont {
  index: number;
  name: string;
  size: number;
  url: string;
}

export interface LibraryPath {
  id: number;
  libraryId: number;
  path: string;
  readonly: boolean;
  sortOrder: number;
}

export interface LibrarySummary {
  id: number;
  name: string;
  kind: string;
  paths: LibraryPath[];
  counts: Record<string, number>;
  imageCount: number;
  scanRunning: boolean;
  /** 只读（网盘 / 只读挂载）：LMBY 不往库目录里写，刮削产物落进 overlay。 */
  readonly?: boolean;
}

export interface ScanRun {
  id: number;
  state: 'running' | 'done' | 'failed' | 'canceled';
  trigger: string;
  startedAt: string;
  finishedAt?: string;
  stats: Record<string, number>;
  error?: string;
}

export interface ScanIssue {
  id: number;
  severity: string;
  path: string;
  message: string;
  at: string;
}

export interface ScanProgress {
  phase: string;
  scanRunId: number;
  libraryId: number;
  videos: number;
  newFiles: number;
  changedFiles: number;
  movedFiles: number;
  deletedFiles: number;
  unchanged: number;
  itemsNew: number;
  images: number;
  issues: number;
  currentPath: string;
  elapsedMs: number;
  at: string;
}

export interface LibraryDetail {
  library: LibrarySummary;
  lastScan: ScanRun | null;
  issues: ScanIssue[];
  progress: ScanProgress | null;
  probe: ProbeProgress;
  /** 叠加层占用（只读库的刮削产物落地岛；未接入时是 0）。 */
  overlay?: { files: number; bytes: number };
}

/** 流信息探测进度。 */
export interface ProbeProgress {
  pending: number;
  ok: number;
  failed: number;
}

/** 后台任务队列水位。 */
export interface TaskStats {
  pending: number;
  running: number;
  done: number;
  failed: number;
  canceled: number;
  oldestPendingSeconds: number;
}

export interface TaskInfo {
  id: number;
  kind: string;
  state: string;
  attempts: number;
  maxAttempts: number;
  lastError?: string;
  lockedBy?: string;
}

export interface Item {
  id: number;
  libraryId?: number;
  kind: string;
  /** 顶层条目为 undefined；季/集的父项与所属剧集。 */
  parentId?: number;
  seriesId?: number;
  title: string;
  originalTitle?: string;
  year?: number;
  seasonNumber?: number;
  episodeNumber?: number;
  episodeEnd?: number;
  overview?: string;
  tagline?: string;
  runtimeTicks?: number;
  premiereDate?: string;
  /** 社区评分（TMDB/nfo 来的，0~10）。 */
  communityRating?: number;
  /** 分级（PG-13 / TV-MA 之类）。 */
  officialRating?: string;
  fileTech?: Record<string, unknown>;
  genres?: string[];
  tags?: string[];
  studios?: string[];
  matchState?: string;
  matchScore?: number;
  metadataSource?: string;
  scrapeError?: string;
  updatedAt?: string;
}

/** 海报墙（只含顶层条目）。 */
export interface BrowsePage {
  items: Item[];
  total: number;
  limit: number;
  offset: number;
}

/** 子项列表（剧集 → 季，季 → 集）+ 每个子项自己的子项数。 */
export interface ChildSummary {
  item: MatchItem;
  items: Item[];
  counts: Record<string, number>;
}

/** 批量操作结果。 */
export interface BatchResult {
  action: string;
  applied: number;
  skipped: number;
}

/** 刮削进度（按匹配状态分）。 */
export interface ScrapeProgress {
  nfo: number;
  local: number;
  matched: number;
  review: number;
  manual: number;
  failed: number;
}

/** TMDB 设置（密钥永不会回显，只回 has* 布尔）。 */
export interface TMDBSettings {
  configured: boolean;
  hasReadToken: boolean;
  hasApiKey: boolean;
  language: string;
  /** 值来自数据库（设置页写入的）还是配置文件兜底。 */
  fromDb: boolean;
  /** 密钥在库里是不是加密存的。 */
  encrypted: boolean;
  fallback?: { hasReadToken: boolean; hasApiKey: boolean; language: string };
}

/** 系统信息（只读）。 */
export interface SystemInfo {
  databaseEncoding: string;
  databaseCollate: string;
  databaseCtype: string;
  schemaVersion: number;
  itemCount: number;
  fileCount: number;
  imageCount: number;
  taskPending: number;
  serverTime: string;
}

export interface SettingsPayload {
  tmdb: TMDBSettings;
  system: SystemInfo;
}

/** 「测试连接」的结果。 */
export interface ProviderTestResult {
  ok: boolean;
  query: string;
  count?: number;
  samples?: { id: number; title: string; year: number }[];
  elapsed?: number;
  error?: string;
}

/** 首页的一行（标题 + 副标题 + 条目）。 */
/** 一个播放列表 / 合集（带界面要用的统计）。 */
export interface PlaylistSummary {
  id: number;
  userId: number;
  name: string;
  /** playlist = 私人；collection = 合集（所有人可见）。 */
  kind: 'playlist' | 'collection';
  overview?: string;
  itemCount: number;
  /** 列表里第一条还在的条目（界面拿它当封面）。 */
  coverItemId?: number | null;
  ownerName?: string;
  /** 是不是我建的。 */
  mine: boolean;
  createdAt: string;
  updatedAt: string;
}

export interface PlaylistItemsPage {
  items: Item[];
  total: number;
  limit: number;
  offset: number;
}

/** 某个条目在列表里的位置与前后邻居（播放器用）。 */
export interface PlaylistNeighbors {
  playlistId: number;
  playlistName: string;
  playlistKind: string;
  itemId: number;
  /** 从 1 开始；0 表示这个条目不在列表里。 */
  index: number;
  total: number;
  prevId?: number | null;
  nextId?: number | null;
}

/** 收藏状态（单条条目）。 */
export interface FavoriteState {
  itemId: number;
  favorite: boolean;
  /** 全站收藏数（不是「我的」）。 */
  count: number;
}

export interface FavoritePage {
  items: Item[];
  total: number;
  limit: number;
  offset: number;
}

/** 口味画像的一档：流派 + 权重（在看过的作品里出现过几次）。 */
export interface HomeTaste {
  genre: string;
  weight: number;
}

export interface HomeSection {
  key: string;
  title: string;
  subtitle?: string;
  /** 只有「为你推荐」有：口味画像。 */
  taste?: HomeTaste[];
  /** 只有「为你推荐」有：画像用到了多少部作品（界面拼本地化依据用）。 */
  sourceWorks?: number;
  /** 只有「为你推荐」有：最近看过的那部作品名。 */
  seedTitle?: string;
  items: Item[];
}

export interface HomePayload {
  /** 大屏轮播：最近更新/入库的几条（宽幅背景用）。 */
  hero: Item[];
  continue: ContinueWatchingEntry[];
  sections: HomeSection[];
}

/** 搜索命中：条目本体 + 排序依据。 */
export interface SearchHit extends Item {
  rank: number;
  similarity: number;
}

/** 生效的筛选条件（后端回显，界面用它画「已筛选」标签）。 */
export interface SearchFilters {
  kind: string;
  genre: string;
  libraryId: number | null;
  personId: number | null;
}

export interface SearchPage {
  query: string;
  filters: SearchFilters;
  items: SearchHit[];
  total: number;
  limit: number;
  offset: number;
}

/** 一个分面取值与命中数（媒体库分面的 value 是库 id 的字符串）。 */
export interface SearchFacet {
  value: string;
  count: number;
}

/**
 * 结果分面。
 *
 * 两个可以用来自检的不变量：sum(kind) == total、sum(library) == total
 * （每个条目恰好一个类型、一个库）；sum(genre) >= total（一条可以有多个流派）。
 */
export interface SearchFacets {
  kind: SearchFacet[];
  library: SearchFacet[];
  genre: SearchFacet[];
  people: number;
  total: number;
}

export interface SearchFacetsPage {
  query: string;
  filters: SearchFilters;
  facets: SearchFacets;
}

/** 「人」这一档的一条命中。 */
export interface PersonHit {
  id: number;
  name: string;
  roles: string[];
  /** 参演作品数：集数已折进所属剧集。 */
  works: number;
  rank: number;
  similarity: number;
}

export interface SearchPeoplePage {
  query: string;
  people: PersonHit[];
  total: number;
  limit: number;
  offset: number;
}

/** 联想下拉里的一项（作品或人）。 */
export interface Suggestion {
  type: 'item' | 'person';
  id: number;
  title: string;
  kind?: string;
  year?: number;
  works?: number;
  roles?: string[];
}

export interface SearchSuggestPage {
  query: string;
  items: Suggestion[];
  people: Suggestion[];
}

/** 后端返回的完整条目（GET /api/v1/items/{id} 里的 item）。 */
export interface FullItem extends Item {
  libraryId: number;
  originalTitle?: string;
  sortTitle?: string;
  tagline?: string;
  runtimeTicks?: number;
  communityRating?: number;
  officialRating?: string;
  studios?: string[];
  providerIds?: Record<string, string>;
  premiereDate?: string;
  lockedFields?: string[];
  lastScrapedAt?: string;
  updatedAt?: string;
}

/** 一个可编辑字段的形态（后端 store 的字段表）。 */
export interface ItemFieldInfo {
  name: string;
  kind: 'text' | 'int' | 'float' | 'list' | 'map' | 'date';
  /** 界面上的单位（目前只有时长的 minutes）。 */
  unit?: string;
  locked: boolean;
}

/** 条目详情（编辑界面的数据源）。 */
export interface ItemDetail {
  item: FullItem;
  fields: ItemFieldInfo[];
  scrapeConfigured: boolean;
}

/** 人工匹配：一个候选（含打分明细）。 */
export interface MatchCandidate {
  candidateId: number;
  kind: string;
  title: string;
  year?: number;
  score: number;
  decision: string;
  margin?: number;
  posterUrl?: string;
  matchedAlias?: string;
  parts?: { name: string; weight: number; score: number; note: string }[];
}

/** 人工匹配：条目的摘要。 */
export interface MatchItem {
  id: number;
  libraryId: number;
  kind: string;
  title: string;
  originalTitle?: string;
  year?: number;
  matchState: string;
  metadataSource?: string;
  scrapeError?: string;
  matchScore?: number;
  overview?: string;
  providerIds?: Record<string, string>;
  lockedFields?: string[];
}

export interface MatchDetail {
  item: MatchItem;
  candidates: MatchCandidate[];
}

export interface ItemsPage {
  items: Item[];
  total: number;
  limit: number;
  offset: number;
}

/** 条目的一位演职员（数据来源：同目录 nfo 的 <actor>/<director>/<credits>）。 */
export interface ItemPerson {
  personId: number;
  name: string;
  /** actor / director / writer / …（nfo 的 <type> 写什么就是什么）。 */
  role: string;
  /** 演的是谁（演员才有）。 */
  character?: string;
  order: number;
  providerIds?: Record<string, string>;
}

// ---------------------------------------------------------------- 播放（M3）

/** 客户端上报的播放能力（服务端的 DeviceProfile 只做兜底）。 */
export interface PlaybackProfile {
  name: string;
  containers: string[];
  videoCodecs: string[];
  audioCodecs: string[];
  subtitleFormats?: string[];
  maxWidth: number;
  maxHeight: number;
  maxBitDepth: number;
  maxAudioChannels: number;
  supportsHls: boolean;
  supportsFmp4: boolean;
  supportsTs: boolean;
}

/** 一条流的处理决定（含「为什么」）。 */
export interface StreamPlan {
  action: 'copy' | 'convert' | 'transcode' | 'burn' | 'drop' | 'none';
  index: number;
  codec?: string;
  reason: string;
  channels?: number;
  language?: string;
  title?: string;
  default?: boolean;
  downmix?: boolean;
  image?: boolean;
  forced?: boolean;
  // 转码专用（M4）：转到什么、怎么转
  targetCodec?: 'h264' | 'hevc';
  targetWidth?: number;
  targetHeight?: number;
  tonemap?: boolean;
  deinterlace?: boolean;
  tenBitToEight?: boolean;
  quality?: 'high' | 'medium' | 'low';
  backend?: string;
  // 源信息：画质档位菜单要用源分辨率算「哪些档比源低」
  sourceCodec?: string;
  sourceWidth?: number;
  sourceHeight?: number;
  sourceBitDepth?: number;
  sourceHdr?: boolean;
  sourceInterlaced?: boolean;
}

/** 播放决策（`GET /items/{id}/play` 里的 plan）。 */
export interface PlaybackPlan {
  mode: 'direct' | 'remux' | 'transcode';
  fileId?: number;
  container?: string;
  containerKind?: string;
  durationTicks: number;
  startTicks: number;
  segmentFormat?: 'fmp4' | 'ts';
  video: StreamPlan;
  audio: StreamPlan;
  subtitle: StreamPlan;
  playable: boolean;
  reasons: string[];
}

/** 转码/转封装会话的实时状态（监控页用）。 */
export interface TranscodeSessionStat {
  key: string;
  state: 'starting' | 'ready' | 'finished' | 'error';
  startSeconds: number;
  uptimeSec: number;
  idleSec: number;
  segments: number;
  generatedSeconds: number;
  clientSeconds: number;
  aheadSeconds: number;
  throttled: boolean;
  fps?: number;
  speed?: number;
  bitrate?: string;
  mediaTime?: string;
  error?: string;
  log?: string;
}

/** 一条活跃播放会话（监控页用）。 */
export interface PlaySessionInfo {
  playSessionId: string;
  itemId: number;
  title?: string;
  userId: number;
  mode: 'direct' | 'remux' | 'transcode';
  startSeconds: number;
  durationSeconds: number;
  ageSeconds: number;
  idleSeconds: number;
  file?: string;
  stream?: TranscodeSessionStat;
}

/** 播放会话状态：模式决定用哪个 URL。 */
export interface PlaybackState {
  playSessionId?: string;
  mode: 'direct' | 'remux' | 'transcode';
  playable: boolean;
  state: 'direct' | 'starting' | 'ready' | 'finished' | 'error' | 'stopped';
  error?: string;
  log?: string;
  reasons: string[];
  plan: PlaybackPlan;
  startSeconds: number;
  durationSeconds: number;
  directUrl?: string;
  hlsUrl?: string;
  subtitleUrl?: string;
  /** vtt：浏览器原生轨道；ass：前端 libass（SubtitlesOctopus）渲染，保留特效。 */
  subtitleFormat?: 'vtt' | 'ass';
  /** ready：字幕已可挂；preparing：内嵌字幕还在抽（后端异步任务）。 */
  subtitleState?: 'ready' | 'preparing';
  /** 转封装模式：这一段预生成窗口的结束位置（秒）。0 = 直出或未知。 */
  windowEndSeconds?: number;
  itemId: number;
  title?: string;
  progress?: PlaybackProgress | null;
  playbackSeconds?: number;
}

export interface PlaybackProgress {
  itemId: number;
  positionTicks: number;
  durationTicks: number;
  played: boolean;
  playCount: number;
}

export interface ContinueWatchingEntry {
  item: Item;
  progress: PlaybackProgress;
  remainingTicks: number;
}

/** 开始播放的请求体。 */
export interface StartPlaybackBody {
  profile?: PlaybackProfile;
  /**
   * 指定用条目下的哪个文件（多版本）。
   *
   * 省略时由服务端的播放决策引擎自己挑（它会优先能直出的那一个）；
   * 指定了就只放这一个 —— 「我要 4K 那一版」是用户的明确选择。
   */
  fileId?: number;
  videoStreamIndex?: number;
  audioStreamIndex?: number;
  /** -1 = 明确不要字幕，0/缺省 = 自动（只选强制字幕轨）。 */
  subtitleStreamIndex?: number;
  startPositionTicks?: number;
  restart?: boolean;
  /**
   * 画质档（输出高度上限）：
   * 省略 = 自动（服务端按配置的转码上限走）；0 = 原生分辨率（不额外压）；
   * > 0 = 该高度。选了比源低的档会强制转码。
   */
  maxHeight?: number;
  /**
   * 把选中的字幕**烧进画面**。
   *
   * 只对图形字幕（PGS/VobSub）有意义：它是位图，浏览器渲染不了。
   * 烧录必须重新编码，所以它会把这个文件强制拉进转码（即使本来能直出）。
   */
  burnSubtitle?: boolean;
}

/** 一个条目下的文件与流（播放器的音轨/字幕选择器用）。 */
export interface PlaylistFile {
  fileId: number;
  container: string;
  containerKind: string;
  sizeBytes: number;
  durationTicks: number;
  probeState: string;
  video: { index: number; codec: string; width: number; height: number; bitDepth?: number; title?: string; language?: string; default?: boolean }[];
  audio: { index: number; codec: string; channels: number; language?: string; title?: string; default?: boolean }[];
  subtitles: { index: number; codec: string; language?: string; title?: string; default?: boolean; forced?: boolean; isImage?: boolean }[];
}

export interface ItemPlaylist {
  itemId: number;
  files: PlaylistFile[];
}

// ---------------------------------------------------------------- 直播电视（M5）的类型

/** 一条直播频道。`probeOk` 缺省 = 还没探过（后端三态：缺省 / true / false）。 */
export interface TVChannel {
  id: number;
  sourceId?: number;
  name: string;
  /** 频道自己的地址：界面要能复制出去给外部播放器。 */
  url: string;
  kind: string;
  group: string;
  logo: string;
  tvgId?: string;
  /** 源站要求的自定义请求头（内容不回显，只说有没有）。 */
  hasHeaders: boolean;
  sortOrder: number;
  disabled: boolean;
  favorite: boolean;
  /** 最近一次探测的摘要（人话，如 `H.264 1920x1080 / MP2 立体声 48kHz（0.3s）`）。 */
  probe?: string;
  probeOk?: boolean;
  probeAt?: string;
}

export interface TVGroup {
  name: string;
  count: number;
}

export interface TVChannelPage {
  channels: TVChannel[];
  groups: TVGroup[];
  total: number;
  enabled: number;
}

/** 一个直播源（粘贴 / 上传文件 / 订阅 URL）。 */
export interface TVSource {
  id: number;
  name: string;
  kind: string;
  url?: string;
  enabled: boolean;
  /** 0 = 不自动刷新。 */
  refreshIntervalMinutes: number;
  lastRefreshAt?: string;
  lastStatus?: string;
  lastChannelCount: number;
  /** 当前挂在这个源下的频道数。 */
  channelCount: number;
}

export interface TVSourcePage {
  sources: TVSource[];
  total: number;
  enabled: number;
}

/** 一次导入的结果。 */
export interface TVImport {
  added: number;
  updated: number;
  kept: number;
  removed: number;
  total: number;
}

/** 起播响应（`playlistUrl` 就是要交给 hls.js / <video> 的地址）。 */
export interface LivePlayback {
  sid: string;
  channelId: number;
  name: string;
  kind: string;
  playlistUrl: string;
  /** 服务端起播耗时（到首个分片可用），毫秒。 */
  startupMs: number;
  segmentSeconds: number;
  listSize: number;
  viewers: number;
  /** 这一路实际用的方式：copy（转封装，默认）或 transcode（转码成 H.264）。 */
  mode?: 'copy' | 'transcode';
  /** 源视频编码（探测记下的，空 = 未知）。 */
  videoCodec?: string;
}

/** 正在跑的直播会话（监控用）。 */
export interface LiveSessionInfo {
  key: string;
  channelId: number;
  name: string;
  state: string;
  viewers: number;
  uptimeSec: number;
  idleSec: number;
  segments: number;
  fps: number;
  speed: number;
  bitrate: string;
  error?: string;
}

/** 探测进度：`progress` 是**这一次**的，`stats` 是库里的总账。 */
export interface TVProbeStatus {
  running: boolean;
  startedAt?: string;
  elapsedSeconds: number;
  selection: { onlyUnknown?: boolean; group?: string; channelIds?: number[] };
  progress: {
    total: number;
    done: number;
    ok: number;
    failed: number;
    current?: string;
    canceled: boolean;
  };
  stats: { total: number; pending: number; ok: number; failed: number };
}
