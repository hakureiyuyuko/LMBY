import { useState } from 'react';
import type { FormEvent } from 'react';
import { useNavigate } from 'react-router-dom';
import { ApiError } from '../api';
import { useAuth } from '../auth';
import { AuthShell } from '../components/AuthShell';
import { useI18n } from '../i18n';

export function Login() {
  const { signIn } = useAuth();
  const navigate = useNavigate();
  const { t } = useI18n();

  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setError('');
    setBusy(true);
    try {
      await signIn(username.trim(), password);
      navigate('/', { replace: true });
    } catch (err) {
      setError(err instanceof ApiError ? err.message : t('登录失败，请重试'));
    } finally {
      setBusy(false);
    }
  }

  return (
    <AuthShell title={t('登录 LMBY')} subtitle={t('使用你的账号继续。')}>
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
          />
        </label>

        <label className="field">
          <span>{t('口令')}</span>
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="current-password"
            required
          />
        </label>

        <button type="submit" className="btn btn-primary" disabled={busy} style={{ width: '100%' }}>
          {busy ? t('正在登录…') : t('登录')}
        </button>
      </form>
    </AuthShell>
  );
}
