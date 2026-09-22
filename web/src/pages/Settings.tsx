import { useCallback, useEffect, useState } from 'react';
import { ApiError, api } from '../api';
import { useAuth } from '../auth';
import type { Health, ProviderTestResult, SettingsPayload } from '../api';

/**
 * 设置页（管理员）。
 *
 * 目前只有一件能改的事：TMDB 凭据。以前改它必须编辑 config.toml 再重启，
 * 现在在这里填完保存就生效（下一次刮削/图片回源就用新凭据）。
 *
 * 两条规矩写在界面上：
 *   - **密钥永远不回显**：只显示「已设置 / 未设置」，要换就输入新的；
 *   - **数据库优先于配置文件**：来源会显示出来，并提供「恢复为配置文件的值」。
 */

const languageOptions = ['zh-CN', 'zh-TW', 'en-US', 'ja-JP', 'ko-KR'];

/** 把秒换成「3 小时 12 分」这类人话（服务状态那一栏用）。 */
function formatUptime(sec: number): string {
  if (sec < 60) return `${sec} 秒`;
  const m = Math.floor(sec / 60);
  if (m < 60) return `${m} 分 ${sec % 60} 秒`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h} 小时 ${m % 60} 分`;
  return `${Math.floor(h / 24)} 天 ${h % 24} 小时`;
}

export function Settings() {
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
      setError(e instanceof ApiError ? e.message : '读取设置失败');
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
        '已保存并立刻生效（不必重启）。' +
          (res.clearedCache > 0 ? `换了语言，顺手清掉了 ${res.clearedCache} 条旧语言的元数据缓存。` : ''),
      );
    } catch (e) {
      setError(e instanceof ApiError ? e.message : '保存失败');
    } finally {
      setBusy(false);
    }
  }

  async function resetToConfig() {
    if (!window.confirm('删掉数据库里的 TMDB 设置，恢复成 config.toml 里的值？')) return;
    setBusy(true);
    setError('');
    setNotice('');
    try {
      const res = await api.resetTMDBSettings();
      await load();
      setReadToken('');
      setApiKey('');
      setNotice(res.tmdb.configured ? '已恢复为配置文件的值。' : '已恢复为配置文件的值（配置文件里没有凭据）。');
    } catch (e) {
      setError(e instanceof ApiError ? e.message : '恢复失败');
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
      setTest({ ok: false, query: '', error: e instanceof ApiError ? e.message : '测试失败' });
    } finally {
      setTesting(false);
    }
  }

  if (!isAdmin) {
    return (
      <div className="card">
        <h2>设置</h2>
        <p className="hint">
          只有管理员能改全站设置。需要修改时请让管理员登录，或用管理员账号看这一页。
        </p>
      </div>
    );
  }

  const tmdb = data?.tmdb;
  const system = data?.system;
  const charsetBad =
    !!system &&
    (system.databaseEncoding.toUpperCase() !== 'UTF8' ||
      !system.databaseCtype.toLowerCase().includes('utf'));

  return (
    <>
      <div className="card">
        <h2>元数据源（TMDB）</h2>
        <p className="hint">
          TMDB 用来刮削元数据与回源图片。填 Read Access Token（v4，推荐）或 API Key（v3），二选一即可。
          密钥**只写不回显**，保存后立刻生效、不必重启。数据库里的设置优先于 <code>config.toml</code>。
        </p>

        {!data && !error && <p className="muted">正在读取…</p>}
        {error && <div className="alert alert-error">{error}</div>}
        {notice && <div className="alert alert-ok">{notice}</div>}

        {tmdb && (
          <>
            <p className="small">
              当前状态：{' '}
              {tmdb.configured ? (
                <span className="badge">已配置</span>
              ) : (
                <span className="badge">未配置</span>
              )}{' '}
              <span className="muted">
                来源 {tmdb.fromDb ? '数据库（本页保存的）' : 'config.toml / 环境变量'} · 密钥
                {tmdb.encrypted ? '加密存储' : '明文存储（密钥文件不可用）'} · Read Token{' '}
                {tmdb.hasReadToken ? '已设置' : '未设置'} · API Key {tmdb.hasApiKey ? '已设置' : '未设置'}
              </span>
            </p>

            <label className="field">
              <span>Read Access Token（v4）</span>
              <input
                type="password"
                value={readToken}
                autoComplete="off"
                placeholder={tmdb.hasReadToken ? '已设置 —— 要替换就输入新的（留空不改）' : '粘贴 eyJhbGciOi…'}
                onChange={(e) => setReadToken(e.target.value)}
              />
            </label>

            <label className="field">
              <span>API Key（v3）</span>
              <input
                type="password"
                value={apiKey}
                autoComplete="off"
                placeholder={tmdb.hasApiKey ? '已设置 —— 要替换就输入新的（留空不改）' : '32 位十六进制'}
                onChange={(e) => setApiKey(e.target.value)}
              />
            </label>

            <label className="field">
              <span>语言</span>
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
              语言影响标题/简介用哪种译名；改了会顺手清掉旧语言的缓存。
            </p>

            <div className="row">
              <button type="button" className="btn btn-primary" disabled={busy} onClick={() => void save()}>
                保存
              </button>
              <button type="button" className="btn" disabled={testing} onClick={() => void runTest()}>
                {testing ? '测试中…' : '测试连接'}
              </button>
              <div className="spacer" />
              <button type="button" className="btn btn-ghost" disabled={busy} onClick={() => void resetToConfig()}>
                恢复为配置文件的值
              </button>
            </div>

            {test && (
              <div className={test.ok ? 'alert alert-ok' : 'alert alert-error'}>
                {test.ok
                  ? `连通正常：搜「${test.query}」拿到 ${test.count} 条结果（${test.elapsed} ms）` +
                    (test.samples && test.samples.length > 0
                      ? `，例如 ${test.samples.map((s) => `${s.title}${s.year ? ` (${s.year})` : ''}`).join('、')}`
                      : '')
                  : `连接失败：${test.error}`}
              </div>
            )}
          </>
        )}
      </div>

      <div className="card">
        <h2>服务状态</h2>
        <p className="hint">
          实例自检：状态、数据库、ffmpeg（与它的硬件加速后端）。首页那份自检面板 M6 搬到了这里 ——
          它属于「出问题时才看」的信息，不该占首页的位置。
        </p>
        {!health && <p className="muted">正在读取…</p>}
        {health && (
          <>
            <p style={{ marginTop: 10 }}>
              <span className="badge" data-health-status={health.status}>
                <span className={health.status === 'ok' ? 'dot dot-ok' : 'dot dot-warn'} />
                {health.status === 'ok' ? '运行正常' : health.status === 'degraded' ? '降级运行' : '异常'}
              </span>{' '}
              <span className="badge">
                <span className={health.database.ok ? 'dot dot-ok' : 'dot dot-bad'} />
                数据库{' '}
                {health.database.ok ? `${health.database.latencyMs ?? 0} ms` : health.database.error}
              </span>{' '}
              <span className="badge">
                <span className={health.ffmpeg.available ? 'dot dot-ok' : 'dot dot-bad'} />
                ffmpeg {health.ffmpeg.available ? health.ffmpeg.version : '不可用'}
              </span>
            </p>
            <dl className="kv">
              <dt>版本</dt>
              <dd>{health.version}</dd>
              <dt>运行时长</dt>
              <dd>{formatUptime(health.uptimeSeconds)}</dd>
              <dt>ffmpeg 路径</dt>
              <dd>{health.ffmpeg.path}</dd>
              <dt>硬件加速后端</dt>
              <dd>{health.ffmpeg.hw_accels?.join(', ') || '—'}</dd>
            </dl>
            <p className="faint" style={{ marginBottom: 0 }}>
              注意：「列出的后端」不等于「真的能用」。例如本机 ffmpeg 列出了 qsv，
              但核显缺运行时，实际只能用 vaapi —— 所以能力探测会真跑一小段转码来验证。
            </p>
          </>
        )}
      </div>

      <div className="card">
        <h2>系统信息</h2>
        {!system && <p className="muted">正在读取…</p>}
        {system && (
          <>
            {charsetBad && (
              <div className="alert alert-error">
                数据库字符集不对：encoding={system.databaseEncoding}、lc_ctype={system.databaseCtype}。
                中文搜索与模糊匹配会静默少结果。停掉服务后跑{' '}
                <code>scripts/dev/fix-db-encoding.sh</code> 就地重建。
              </div>
            )}
            <dl className="kv">
              <dt>数据库字符集</dt>
              <dd>
                {system.databaseEncoding} / {system.databaseCtype}（collate {system.databaseCollate}）
              </dd>
              <dt>数据库结构版本</dt>
              <dd>{system.schemaVersion}</dd>
              <dt>条目 / 文件 / 图片</dt>
              <dd>
                {system.itemCount} / {system.fileCount} / {system.imageCount}
              </dd>
              <dt>队里待跑的任务</dt>
              <dd>{system.taskPending}</dd>
              <dt>服务器时间</dt>
              <dd>{new Date(system.serverTime).toLocaleString()}</dd>
            </dl>
          </>
        )}
      </div>
    </>
  );
}
