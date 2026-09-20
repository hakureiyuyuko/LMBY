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
  kind: string;
  title: string;
  year?: number;
  seasonNumber?: number;
  episodeNumber?: number;
  episodeEnd?: number;
  overview?: string;
  fileTech?: Record<string, unknown>;
  genres?: string[];
  matchState?: string;
  matchScore?: number;
  metadataSource?: string;
  scrapeError?: string;
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
