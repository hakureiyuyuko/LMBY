import { useCallback, useEffect, useState } from 'react';
import { NavLink, Outlet } from 'react-router-dom';
import { ApiError, api } from '../api';
import { useAuth } from '../auth';
import { t, useI18n } from '../i18n';
import { LiveProbePanel, LiveSourcePanel } from './../components/LiveSources';
import { ChannelManager } from '../components/ChannelManager';
import { TranscodePanel } from '../components/TranscodePanel';
import type { Health, ProviderTestResult, SettingsPayload } from '../api';

/**
 * 设置页（管理员）—— 也是管理面的壳：库管理 / 人工匹配 / 会话监控都是它的子页签。
 *
 * 为什么收进来：这三件事都是「管理员偶尔进来调一下」的，占着顶栏等于把普通用户
 * 永远用不到的入口排在最显眼处；而它们彼此之间又常要来回跳
 * （库里发现匹配不对 → 去人工匹配 → 回来重扫）。
 *
 * 两条规矩写在界面上：
 *   - **密钥永远不回显**：只显示「已设置 / 未设置」，要换就输入新的；
 *   - **数据库优先于配置文件**：来源会显示出来，并提供「恢复为配置文件的值」。
 */

const languageOptions = ['zh-CN', 'zh-TW', 'en-US', 'ja-JP', 'ko-KR'];

/** 字节数 → 「1.2 MB」这种人话（叠加层看板用）。 */
function bytesText(bytes: number): string {
  if (!bytes || bytes <= 0) return '0 B';
  if (bytes >= 1024 ** 3) return `${(bytes / 1024 ** 3).toFixed(2)} GB`;
  if (bytes >= 1024 ** 2) return `${(bytes / 1024 ** 2).toFixed(1)} MB`;
  if (bytes >= 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${bytes} B`;
}

/** 把秒换成「3 小时 12 分」这类人话（服务状态那一栏用）。 */
function formatUptime(sec: number): string {
  if (sec < 60) return t('{n} 秒', { n: sec });
  const m = Math.floor(sec / 60);
  if (m < 60) return t('{m} 分 {s} 秒', { m, s: sec % 60 });
  const h = Math.floor(m / 60);
  if (h < 24) return t('{h} 小时 {m} 分', { h, m: m % 60 });
  return t('{d} 天 {h} 小时', { d: Math.floor(h / 24), h: h % 24 });
}

export function Settings() {
  const { t } = useI18n();
  const { user } = useAuth();
  const isAdmin = user?.isAdmin ?? false;

  if (!isAdmin) return <NotAdmin />;

  return (
    <>
      {/* 管理面页签：库管理 / 人工匹配 / 会话监控都收在这里（顶栏只留一个「设置」） */}
      <div className="card">
        <h2>{t('设置')}</h2>
        <nav className="tabs" data-settings-tabs>
          <NavLink to="/settings" end className={({ isActive }) => (isActive ? 'tab active' : 'tab')}>
            {t('元数据与服务状态')}
          </NavLink>
          <NavLink
            to="/settings/transcode"
            className={({ isActive }) => (isActive ? 'tab active' : 'tab')}
          >
            {t('转码与硬件')}
          </NavLink>
          <NavLink
            to="/settings/libraries"
            className={({ isActive }) => (isActive ? 'tab active' : 'tab')}
          >
            {t('库管理')}
          </NavLink>
          <NavLink
            to="/settings/match"
            className={({ isActive }) => (isActive ? 'tab active' : 'tab')}
          >
            {t('人工匹配')}
          </NavLink>
          <NavLink
            to="/settings/sessions"
            className={({ isActive }) => (isActive ? 'tab active' : 'tab')}
          >
            {t('会话')}
          </NavLink>
          <NavLink
            to="/settings/livetv"
            className={({ isActive }) => (isActive ? 'tab active' : 'tab')}
          >
            {t('直播源')}
          </NavLink>
          <NavLink
            to="/settings/users"
            className={({ isActive }) => (isActive ? 'tab active' : 'tab')}
          >
            {t('用户')}
          </NavLink>
        </nav>
      </div>

      <Outlet />
    </>
  );
}

/** 非管理员看到的那一页（接口也会拦，这里只是不让人困惑）。 */
function NotAdmin() {
  const { t } = useI18n();
  return (
    <div className="card">
      <h2>{t('设置')}</h2>
      <p className="hint">
        {t('只有管理员能改全站设置。需要修改时请让管理员登录，或用管理员账号看这一页。')}
      </p>
    </div>
  );
}

/** 设置页签：转码与硬件（本机编码能力表 —— 每台机器不一样，只能实测）。 */
export function SettingsTranscode() {
  const { user } = useAuth();
  return <TranscodePanel isAdmin={user?.isAdmin ?? false} />;
}

/** 设置页签：直播源与探测（M6 收尾从「直播」页搬过来的）。
 *
 * 看频道、起播与切台在「直播」页 —— 这里只管源：导入 / 刷新 / 停用与失效源探测。
 * 两个面板的完成回调在设置页没有要刷新列表，传空函数即可。
 */
export function SettingsLiveTV() {
  const { t } = useI18n();
  return (
    <>
      <p className="hint">
        {t('这里管直播源（导入 / 刷新 / 停用）、频道管理与失效源探测；看频道、起播与切台在「直播」页。')}
      </p>
      <LiveSourcePanel onImported={() => {}} />
      <ChannelManager />
      <LiveProbePanel onFinished={() => {}} />
    </>
  );
}

/** 设置首页（`/settings`）：TMDB 凭据 + 叠加层看板 + 服务状态 + 系统信息。 */
export function SettingsOverview() {
  const { t } = useI18n();
  const { user } = useAuth();
  const isAdmin = user?.isAdmin ?? false;

  const [data, setData] = useState<SettingsPayload | null>(null);
  const [readToken, setReadToken] = useState('');
  const [apiKey, setApiKey] = useState('');
  const [language, setLanguage] = useState('zh-CN');
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);
  // 服务状态（M6 从首页底部移过来的）：/healthz 是公开端点，读不到就什么都不显示
  const [health, setHealth] = useState<Health | null>(null);
  const [test, setTest] = useState<ProviderTestResult | null>(null);
  const [testing, setTesting] = useState(false);

  const load = useCallback(async () => {
    if (!isAdmin) return;
    try {
      const d = await api.settings();
      setData(d);
      setLanguage(d.tmdb.language);
      setError('');
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('读取设置失败'));
    }
  }, [isAdmin]);

  useEffect(() => {
    void load();
  }, [load]);

  // 服务状态（M6 从首页底部搬过来）：读不到就不显示，不挡设置页的其他内容
  useEffect(() => {
    api
      .health()
      .then(setHealth)
      .catch(() => setHealth(null));
  }, []);

  // 只读库叠加层看板：读不到就不显示（它只是「看一眼」的信息）
  const [overlayStats, setOverlayStats] = useState<Awaited<
    ReturnType<typeof api.overlayStats>
  > | null>(null);
  const [clearing, setClearing] = useState<number | null>(null);

  const loadOverlay = useCallback(async () => {
    try {
      setOverlayStats(await api.overlayStats());
    } catch {
      setOverlayStats(null);
    }
  }, []);

  useEffect(() => {
    void loadOverlay();
  }, [loadOverlay]);

  async function clearOverlay(libraryId: number, name: string) {
    if (
      !window.confirm(
        t('清空「{name}」的叠加层？只删数据目录里这个库的刮削产物，媒体目录与数据库一个字都不动。',
          { name }),
      )
    ) {
      return;
    }
    setClearing(libraryId);
    try {
      await api.clearLibraryOverlay(libraryId);
      await loadOverlay();
      setNotice(t('已清空「{name}」的叠加层。', { name }));
      setError('');
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('清空叠加层失败'));
    } finally {
      setClearing(null);
    }
  }

  async function save() {
    setBusy(true);
    setError('');
    setNotice('');
    try {
      // 密钥留空 = 不改（不是清空）；要清空用「恢复为配置文件的值」。
      const body: { readToken?: string; apiKey?: string; language?: string } = { language };
      if (readToken.trim()) body.readToken = readToken.trim();
      if (apiKey.trim()) body.apiKey = apiKey.trim();

      const res = await api.updateTMDBSettings(body);
      setData((prev) => (prev ? { ...prev, tmdb: res.tmdb } : prev));
      setReadToken('');
      setApiKey('');
      setNotice(
        t('已保存并立刻生效（不必重启）。') +
          (res.clearedCache > 0
            ? t('换了语言，顺手清掉了 {n} 条旧语言的元数据缓存。', { n: res.clearedCache })
            : ''),
      );
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('保存失败'));
    } finally {
      setBusy(false);
    }
  }

  async function resetToConfig() {
    if (!window.confirm(t('删掉数据库里的 TMDB 设置，恢复成 config.toml 里的值？'))) return;
    setBusy(true);
    setError('');
    setNotice('');
    try {
      const res = await api.resetTMDBSettings();
      await load();
      setReadToken('');
      setApiKey('');
      setNotice(
        res.tmdb.configured
          ? t('已恢复为配置文件的值。')
          : t('已恢复为配置文件的值（配置文件里没有凭据）。'),
      );
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('恢复失败'));
    } finally {
      setBusy(false);
    }
  }

  async function runTest() {
    setTesting(true);
    setTest(null);
    try {
      setTest(await api.testProvider());
    } catch (e) {
      setTest({ ok: false, query: '', error: e instanceof ApiError ? e.message : t('测试失败') });
    } finally {
      setTesting(false);
    }
  }

  if (!isAdmin) return <NotAdmin />;

  const tmdb = data?.tmdb;
  const system = data?.system;
  const charsetBad =
    !!system &&
    (system.databaseEncoding.toUpperCase() !== 'UTF8' ||
      !system.databaseCtype.toLowerCase().includes('utf'));

  return (
    <>
      <div className="card">
        <h2>{t('元数据源（TMDB）')}</h2>
        <p className="hint">
          {t('TMDB 用来刮削元数据与回源图片。填 Read Access Token（v4，推荐）或 API Key（v3），二选一即可。密钥只写不回显，保存后立刻生效、不必重启。数据库里的设置优先于 config.toml。')}
        </p>

        {!data && !error && <p className="muted">{t('正在读取…')}</p>}
        {error && <div className="alert alert-error">{error}</div>}
        {notice && <div className="alert alert-ok">{notice}</div>}

        {tmdb && (
          <>
            <p className="small">
              {t('当前状态：')}{' '}
              {tmdb.configured ? (
                <span className="badge">{t('已配置')}</span>
              ) : (
                <span className="badge">{t('未配置')}</span>
              )}{' '}
              <span className="muted">
                {t('来源 {from} · 密钥 {enc} · Read Token {rt} · API Key {ak}', {
                  from: tmdb.fromDb ? t('数据库（本页保存的）') : t('config.toml / 环境变量'),
                  enc: tmdb.encrypted ? t('加密存储') : t('明文存储（密钥文件不可用）'),
                  rt: tmdb.hasReadToken ? t('已设置') : t('未设置'),
                  ak: tmdb.hasApiKey ? t('已设置') : t('未设置'),
                })}
              </span>
            </p>

            <label className="field">
              <span>{t('Read Access Token（v4）')}</span>
              <input
                type="password"
                value={readToken}
                autoComplete="off"
                placeholder={
                  tmdb.hasReadToken
                    ? t('已设置 —— 要替换就输入新的（留空不改）')
                    : t('粘贴 eyJhbGciOi…')
                }
                onChange={(e) => setReadToken(e.target.value)}
              />
            </label>

            <label className="field">
              <span>{t('API Key（v3）')}</span>
              <input
                type="password"
                value={apiKey}
                autoComplete="off"
                placeholder={tmdb.hasApiKey ? t('已设置 —— 要替换就输入新的（留空不改）') : t('32 位十六进制')}
                onChange={(e) => setApiKey(e.target.value)}
              />
            </label>

            <label className="field">
              <span>{t('语言')}</span>
              <input
                value={language}
                list="tmdb-languages"
                onChange={(e) => setLanguage(e.target.value)}
              />
              <datalist id="tmdb-languages">
                {languageOptions.map((l) => (
                  <option key={l} value={l} />
                ))}
              </datalist>
            </label>
            <p className="faint small" style={{ marginTop: -8 }}>
              {t('语言影响标题/简介用哪种译名；改了会顺手清掉旧语言的缓存。')}
            </p>

            <div className="row">
              <button type="button" className="btn btn-primary" disabled={busy} onClick={() => void save()}>
                {t('保存')}
              </button>
              <button type="button" className="btn" disabled={testing} onClick={() => void runTest()}>
                {testing ? t('测试中…') : t('测试连接')}
              </button>
              <div className="spacer" />
              <button type="button" className="btn btn-ghost" disabled={busy} onClick={() => void resetToConfig()}>
                {t('恢复为配置文件的值')}
              </button>
            </div>

            {test && (
              <div className={test.ok ? 'alert alert-ok' : 'alert alert-error'}>
                {test.ok
                  ? t('连通正常：搜「{q}」拿到 {n} 条结果（{ms} ms）', {
                      q: test.query,
                      n: test.count ?? 0,
                      ms: test.elapsed ?? 0,
                    }) +
                    (test.samples && test.samples.length > 0
                      ? t('，例如 {samples}', {
                          samples: test.samples
                            .map((s) => `${s.title}${s.year ? ` (${s.year})` : ''}`)
                            .join(', '),
                        })
                      : '')
                  : t('连接失败：{error}', { error: test.error ?? '' })}
              </div>
            )}
          </>
        )}
      </div>

      <div className="card">
        <h2>{t('只读库叠加层')}</h2>
        <p className="hint">
          {t('只读媒体库（网盘 / 只读挂载）的刮削产物存在这里：数据目录下每库一块，不写媒体目录。目录：{root}',
            { root: overlayStats?.root || '—' })}
        </p>
        {overlayStats && (
          <>
            <p className="small">
              {t('总计 {files} 个文件 / {size}', {
                files: overlayStats.total.files,
                size: bytesText(overlayStats.total.bytes),
              })}
            </p>
            {overlayStats.libraries.length === 0 && (
              <p className="muted">{t('还没有任何只读库的刮削产物。')}</p>
            )}
            {overlayStats.libraries.map((row) => (
              <div className="row" key={row.libraryId} style={{ marginBottom: 6 }}>
                <span>{row.name || t('媒体库 #{id}', { id: row.libraryId })}</span>
                <span className="faint small">
                  {t('{files} 个文件 / {size}', {
                    files: row.files,
                    size: bytesText(row.bytes),
                  })}
                </span>
                <div className="spacer" />
                <button
                  type="button"
                  className="btn btn-sm btn-danger"
                  disabled={clearing === row.libraryId}
                  onClick={() => void clearOverlay(row.libraryId, row.name || `#${row.libraryId}`)}
                >
                  {clearing === row.libraryId ? t('清空中…') : t('清空叠加层')}
                </button>
              </div>
            ))}
          </>
        )}
      </div>

      <div className="card">
        <h2>{t('服务状态')}</h2>
        <p className="hint">
          {t('实例自检：状态、数据库、ffmpeg（与它的硬件加速后端）。首页那份自检面板 M6 搬到了这里 —— 它属于「出问题时才看」的信息，不该占首页的位置。')}
        </p>
        {!health && <p className="muted">{t('正在读取…')}</p>}
        {health && (
          <>
            <p style={{ marginTop: 10 }}>
              <span className="badge" data-health-status={health.status}>
                <span className={health.status === 'ok' ? 'dot dot-ok' : 'dot dot-warn'} />
                {health.status === 'ok'
                  ? t('运行正常')
                  : health.status === 'degraded'
                    ? t('降级运行')
                    : t('异常')}
              </span>{' '}
              <span className="badge">
                <span className={health.database.ok ? 'dot dot-ok' : 'dot dot-bad'} />
                {t('数据库')}{' '}
                {health.database.ok ? `${health.database.latencyMs ?? 0} ms` : health.database.error}
              </span>{' '}
              <span className="badge">
                <span className={health.ffmpeg.available ? 'dot dot-ok' : 'dot dot-bad'} />
                ffmpeg {health.ffmpeg.available ? health.ffmpeg.version : t('不可用')}
              </span>
            </p>
            <dl className="kv">
              <dt>{t('版本')}</dt>
              <dd>{health.version}</dd>
              <dt>{t('运行时长')}</dt>
              <dd>{formatUptime(health.uptimeSeconds)}</dd>
              <dt>{t('ffmpeg 路径')}</dt>
              <dd>{health.ffmpeg.path}</dd>
              <dt>{t('硬件加速后端')}</dt>
              <dd>{health.ffmpeg.hw_accels?.join(', ') || '—'}</dd>
            </dl>
            <p className="faint" style={{ marginBottom: 0 }}>
              {t('硬件后端能不能用要看「转码与硬件」页：那里每一条都是真跑过的结论。')}
            </p>
          </>
        )}
      </div>

      <div className="card">
        <h2>{t('系统信息')}</h2>
        {!system && <p className="muted">{t('正在读取…')}</p>}
        {system && (
          <>
            {charsetBad && (
              <div className="alert alert-error">
                {t('数据库字符集不对：encoding={enc}、lc_ctype={ctype}。中文搜索与模糊匹配会静默少结果。停掉服务后跑 {script} 就地重建。', {
                  enc: system.databaseEncoding,
                  ctype: system.databaseCtype,
                  script: 'scripts/dev/fix-db-encoding.sh',
                })}
              </div>
            )}
            <dl className="kv">
              <dt>{t('数据库字符集')}</dt>
              <dd>
                {system.databaseEncoding} / {system.databaseCtype}（collate {system.databaseCollate}）
              </dd>
              <dt>{t('数据库结构版本')}</dt>
              <dd>{system.schemaVersion}</dd>
              <dt>{t('条目 / 文件 / 图片')}</dt>
              <dd>
                {system.itemCount} / {system.fileCount} / {system.imageCount}
              </dd>
              <dt>{t('队里待跑的任务')}</dt>
              <dd>{system.taskPending}</dd>
              <dt>{t('服务器时间')}</dt>
              <dd>{new Date(system.serverTime).toLocaleString()}</dd>
            </dl>
          </>
        )}
      </div>
    </>
  );
}
