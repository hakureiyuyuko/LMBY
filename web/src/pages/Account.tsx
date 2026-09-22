import { useCallback, useEffect, useState } from 'react';
import type { FormEvent } from 'react';
import { ApiError, api } from '../api';
import type { SessionInfo } from '../api';
import { useAuth } from '../auth';
import { t, useI18n } from '../i18n';
import { useTheme } from '../theme';
import type { ThemeMode } from '../theme';

/** 个人中心：资料、改口令、我的设备、外观。 */
export function Account() {
  const { user, preferences } = useAuth();
  const { t } = useI18n();

  return (
    <>
      <ProfileCard />
      <PasswordCard />
      <AppearanceCard
        current={(preferences?.theme as ThemeMode) ?? 'system'}
        language={preferences?.language ?? 'zh-CN'}
      />
      <SessionsCard />
      <p className="faint">
        {t('账号：{name}', { name: user?.username ?? '' })}
        {user?.isAdmin ? t('（管理员）') : ''} ·
        {t('注册于 {when}', {
          when: user?.createdAt ? formatTime(user.createdAt) : '—',
        })}
      </p>
    </>
  );
}

// ---------------------------------------------------------------- 资料

function ProfileCard() {
  const { t } = useI18n();
  const { user, applyMe } = useAuth();
  const [name, setName] = useState(user?.displayName ?? '');
  const [msg, setMsg] = useState('');
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setMsg('');
    setErr('');
    setBusy(true);
    try {
      applyMe(await api.updateProfile(name.trim()));
      setMsg(t('已保存'));
    } catch (error) {
      setErr(error instanceof ApiError ? error.message : t('保存失败'));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="card">
      <h2>{t('资料')}</h2>
      <p className="hint">{t('显示名会出现在界面右上角。')}</p>
      <form onSubmit={onSubmit}>
        {msg && <div className="alert alert-ok">{msg}</div>}
        {err && <div className="alert alert-error">{err}</div>}
        <label className="field">
          <span>{t('显示名')}</span>
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={t('留空则显示用户名')}
          />
        </label>
        <button type="submit" className="btn btn-primary" disabled={busy}>
          {busy ? t('保存中…') : t('保存')}
        </button>
      </form>
    </div>
  );
}

// ---------------------------------------------------------------- 改口令

function PasswordCard() {
  const { t } = useI18n();
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [confirm, setConfirm] = useState('');
  const [msg, setMsg] = useState('');
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setMsg('');
    setErr('');

    if (next !== confirm) {
      setErr(t('两次输入的新口令不一致'));
      return;
    }

    setBusy(true);
    try {
      const res = await api.changePassword(current, next);
      setCurrent('');
      setNext('');
      setConfirm('');
      setMsg(
        res.revokedSessions > 0
          ? t('口令已修改，并已让其它 {n} 个设备下线', { n: res.revokedSessions })
          : t('口令已修改'),
      );
    } catch (error) {
      setErr(error instanceof ApiError ? error.message : t('修改失败'));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="card">
      <h2>{t('修改口令')}</h2>
      <p className="hint">
        {t('需要先验证当前口令。修改成功后，除当前设备外的其它登录会话都会立即失效。')}
      </p>
      <form onSubmit={onSubmit}>
        {msg && <div className="alert alert-ok">{msg}</div>}
        {err && <div className="alert alert-error">{err}</div>}

        <label className="field">
          <span>{t('当前口令')}</span>
          <input
            type="password"
            value={current}
            onChange={(e) => setCurrent(e.target.value)}
            autoComplete="current-password"
            required
          />
        </label>
        <label className="field">
          <span>{t('新口令')}</span>
          <input
            type="password"
            value={next}
            onChange={(e) => setNext(e.target.value)}
            autoComplete="new-password"
            required
            minLength={8}
          />
        </label>
        <label className="field">
          <span>{t('确认新口令')}</span>
          <input
            type="password"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            autoComplete="new-password"
            required
            minLength={8}
          />
        </label>

        <button type="submit" className="btn btn-primary" disabled={busy}>
          {busy ? t('提交中…') : t('修改口令')}
        </button>
      </form>
    </div>
  );
}

// ---------------------------------------------------------------- 外观

function AppearanceCard({ current, language }: { current: ThemeMode; language: string }) {
  const { t } = useI18n();
  const { mode, setMode } = useTheme();
  const { applyMe } = useAuth();
  const [err, setErr] = useState('');

  const choose = useCallback(
    async (target: ThemeMode) => {
      setMode(target);
      try {
        applyMe(await api.updatePreferences({ theme: target, language }));
        setErr('');
      } catch {
        // 服务端同步失败不影响本机显示，下次再同步
        setErr(t('已在本机切换，但同步到账号失败'));
      }
    },
    [applyMe, language, setMode],
  );

  const options: { value: ThemeMode; label: string }[] = [
    { value: 'light', label: t('亮色') },
    { value: 'dark', label: t('暗色') },
    { value: 'system', label: t('跟随系统') },
  ];

  return (
    <div className="card">
      <h2>{t('外观')}</h2>
      <p className="hint">
        {t('右上角随时可以快速切换；这里的设置会同步到账号，换设备登录后同样生效。')}
        {current !== mode && t('（当前显示与本机选择不一致，已按本机选择显示）')}
      </p>
      {err && <div className="alert alert-error">{err}</div>}
      <div className="seg">
        {options.map((o) => (
          <button
            key={o.value}
            type="button"
            aria-pressed={mode === o.value}
            onClick={() => void choose(o.value)}
          >
            {o.label}
          </button>
        ))}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------- 我的设备

function SessionsCard() {
  const { t } = useI18n();
  const [sessions, setSessions] = useState<SessionInfo[] | null>(null);
  const [err, setErr] = useState('');

  const load = useCallback(async () => {
    try {
      const res = await api.sessions();
      setSessions(res.sessions);
      setErr('');
    } catch (error) {
      setErr(error instanceof ApiError ? error.message : t('读取设备列表失败'));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function revoke(id: string) {
    setErr('');
    try {
      await api.revokeSession(id);
      await load();
    } catch (error) {
      setErr(error instanceof ApiError ? error.message : t('撤销失败'));
    }
  }

  return (
    <div className="card">
      <h2>{t('我的设备')}</h2>
      <p className="hint">
        {t('当前有效的登录会话。撤销某个会话会立即让对应设备退出登录。')}
      </p>
      {err && <div className="alert alert-error">{err}</div>}
      {!sessions && <p className="muted">{t('正在读取…')}</p>}
      {sessions && sessions.length === 0 && <p className="muted">{t('没有有效会话。')}</p>}
      {sessions && sessions.length > 0 && (
        <table>
          <thead>
            <tr>
              <th>{t('设备 / 客户端')}</th>
              <th>{t('来源 IP')}</th>
              <th>{t('最后活跃')}</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {sessions.map((s) => (
              <tr key={s.id}>
                <td>
                  {shortAgent(s.userAgent)}{' '}
                  {s.current && <span className="badge">{t('当前')}</span>}
                </td>
                <td className="faint">{s.ip || '—'}</td>
                <td className="faint">{formatTime(s.lastSeenAt)}</td>
                <td style={{ textAlign: 'right' }}>
                  {!s.current && (
                    <button
                      type="button"
                      className="btn btn-sm btn-danger"
                      onClick={() => void revoke(s.id)}
                    >
                      {t('撤销')}
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

// ---------------------------------------------------------------- 工具

function shortAgent(ua: string): string {
  if (!ua) return t('未知客户端');
  const browser = /Edg\//.test(ua)
    ? 'Edge'
    : /Chrome\//.test(ua)
      ? 'Chrome'
      : /Firefox\//.test(ua)
        ? 'Firefox'
        : /Safari\//.test(ua)
          ? 'Safari'
          : /curl\//.test(ua)
            ? 'curl'
            : t('其它');
  const os = /Windows/.test(ua)
    ? 'Windows'
    : /Android/.test(ua)
      ? 'Android'
      : /iPhone|iPad/.test(ua)
        ? 'iOS'
        : /Mac OS X/.test(ua)
          ? 'macOS'
          : /Linux/.test(ua)
            ? 'Linux'
            : t('未知系统');
  return `${browser} / ${os}`;
}

function formatTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  return d.toLocaleString(undefined, { hour12: false });
}
