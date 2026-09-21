/** LMBY 后端接口客户端。 */

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
    throw new ApiError(0, '无法连接到服务器，请检查网络或服务是否在运行');
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

  // ---------------------------------------------------------------- 搜索
  search: (params: {
    q: string;
    libraryId?: number;
    kind?: string;
    limit?: number;
    offset?: number;
  }) => {
    const sp = new URLSearchParams({ q: params.q });
    if (params.libraryId) sp.set('libraryId', String(params.libraryId));
    if (params.kind) sp.set('kind', params.kind);
    if (params.limit) sp.set('limit', String(params.limit));
    if (params.offset) sp.set('offset', String(params.offset));
    return request<SearchPage>(`/api/v1/search?${sp.toString()}`);
  },

  // ---------------------------------------------------------------- 条目详情与人工编辑
  item: (id: number) => request<ItemDetail>(`/api/v1/items/${id}`),
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
  title: string;
  originalTitle?: string;
  year?: number;
  seasonNumber?: number;
  episodeNumber?: number;
  episodeEnd?: number;
  overview?: string;
  runtimeTicks?: number;
  premiereDate?: string;
  fileTech?: Record<string, unknown>;
  genres?: string[];
  matchState?: string;
  matchScore?: number;
  metadataSource?: string;
  scrapeError?: string;
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

/** 搜索命中：条目本体 + 排序依据。 */
export interface SearchHit extends Item {
  rank: number;
  similarity: number;
}

export interface SearchPage {
  query: string;
  items: SearchHit[];
  total: number;
  limit: number;
  offset: number;
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
}

/** 条目下的文件与流（播放器的音轨/字幕选择器用）。 */
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
