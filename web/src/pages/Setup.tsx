import { useState } from 'react';
import type { FormEvent } from 'react';
import { useNavigate } from 'react-router-dom';
import { ApiError } from '../api';
import { useAuth } from '../auth';
import { useI18n } from '../i18n';
import { AuthShell } from '../components/AuthShell';

/** 初始化向导：系统还没有任何账号时创建第一个管理员。 */
export function Setup() {
  const { t } = useI18n();
  const { runSetup } = useAuth();
  const navigate = useNavigate();

  const [username, setUsername] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setError('');
    if (password !== confirm) {
      setError(t('两次输入的口令不一致'));
      return;
    }
    setBusy(true);
    try {
      await runSetup(username.trim(), password, displayName.trim());
      navigate('/', { replace: true });
    } catch (err) {
      setError(err instanceof ApiError ? err.message : t('初始化失败，请重试'));
    } finally {
      setBusy(false);
    }
  }

  return (
    <AuthShell
      title={t('初始化 LMBY')}
      subtitle={t('这是第一次启动。请创建管理员账号，口令至少 8 个字符。')}
    >
      <form onSubmit={onSubmit}>
        {error && <div className="alert alert-error">{error}</div>}

        <label className="field">
          <span>{t('用户名')}</span>
          <input
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            autoComplete="username"
            autoFocus
            required
            placeholder="admin"
          />
        </label>

        <label className="field">
          <span>{t('显示名（可留空）')}</span>
          <input
            value={displayName}
            onChange={(e) => setDisplayName(e.target.value)}
            placeholder={t('用于界面显示')}
          />
        </label>

        <label className="field">
          <span>{t('口令')}</span>
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="new-password"
            required
          />
        </label>

        <label className="field">
          <span>{t('确认口令')}</span>
          <input
            type="password"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            autoComplete="new-password"
            required
          />
        </label>

        <button type="submit" className="btn btn-primary" disabled={busy} style={{ width: '100%' }}>
          {busy ? t('正在创建…') : t('创建并进入')}
        </button>
      </form>
    </AuthShell>
  );
}
