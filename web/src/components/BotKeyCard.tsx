/**
 * 管理 API 密钥：给 bot / 脚本 / 自动化用的长期凭据。
 *
 * 三条要跟用户讲清楚的事（都写在界面上）：
 *  - 明文**只显示一次**（后端只存哈希，之后连自己都拿不回来）；
 *  - 这把钥匙**只能开用户管理那几个接口**（注册 / 修改 / 删除账号），
 *    其它接口一律 403 —— 最小权限，泄露时损失可控；
 *  - 用 `Authorization: Bearer <密钥>` 或 `X-API-Key: <密钥>` 传。
 */
import { useCallback, useEffect, useState } from 'react';
import { ApiError, api } from '../api';
import { useI18n } from '../i18n';

export function BotKeyCard() {
  const { t } = useI18n();
  const [info, setInfo] = useState<{ configured: boolean; prefix?: string; createdAt?: string } | null>(
    null,
  );
  // fresh 是刚生成的明文：只在这一次渲染里存在，刷新页面就没了（后端也不回明文）。
  const [fresh, setFresh] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      setInfo(await api.botKey());
      setError('');
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('读取 API 密钥状态失败'));
    }
  }, [t]);

  useEffect(() => {
    void load();
  }, [load]);

  async function create() {
    if (info?.configured && !window.confirm(t('轮换后旧密钥会立刻失效，正在用它的 bot 需要换新的。继续？'))) {
      return;
    }
    setBusy(true);
    setError('');
    try {
      const r = await api.createBotKey();
      setFresh(r.key);
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('生成失败'));
    } finally {
      setBusy(false);
    }
  }

  async function revoke() {
    if (!window.confirm(t('撤销后这把密钥立刻失效，正在用它的 bot 会开始报 401。继续？'))) return;
    setBusy(true);
    setError('');
    try {
      await api.deleteBotKey();
      setFresh('');
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('撤销失败'));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="card">
      <h2>{t('API 密钥（给 bot / 脚本）')}</h2>
      {error && <div className="alert alert-error">{error}</div>}
      <p className="faint small">
        {t('用 Authorization: Bearer <密钥> 或 X-API-Key 调用。它只能用于用户管理接口（注册 / 修改 / 删除账号、改可见库、重置口令），其它接口一律 403 —— 明文只在生成时显示一次。')}
      </p>

      {fresh && (
        <div className="alert alert-ok">
          <div>{t('这是新密钥，只显示这一次，请立刻保存：')}</div>
          <code className="mono">{fresh}</code>
        </div>
      )}

      <div className="row" style={{ gap: 8, alignItems: 'center' }}>
        {info?.configured ? (
          <>
            <span className="badge badge-ok">{t('已配置')}</span>
            <span className="faint small mono">{info.prefix}…</span>
            <div className="spacer" />
            <button type="button" className="btn btn-sm" disabled={busy} onClick={() => void create()}>
              {t('轮换')}
            </button>
            <button type="button" className="btn btn-sm" disabled={busy} onClick={() => void revoke()}>
              {t('撤销')}
            </button>
          </>
        ) : (
          <>
            <span className="badge">{t('未配置')}</span>
            <div className="spacer" />
            <button type="button" className="btn btn-primary btn-sm" disabled={busy} onClick={() => void create()}>
              {t('生成密钥')}
            </button>
          </>
        )}
      </div>
    </div>
  );
}
