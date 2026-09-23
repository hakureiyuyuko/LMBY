/**
 * 用户与权限（管理员）—— 设置在「设置 → 用户」页签里。
 *
 * 只做四件事，别的都不放：
 *   1. 建用户 / 禁用 / 删除（含「最后一个管理员」这类硬规则的解释）；
 *   2. 三个能力开关：允许转码、允许直播、按库限制；
 *   3. 按库勾选可见范围（白名单语义：给什么就是什么）；
 *   4. 重置口令（重置即把那个人踢下线 —— 界面上直说）。
 *
 * 界面口径：**界面上不该出现点了就 403 的入口**，所以这里的开关与后端的判定
 * 一一对应（后端的拒绝只是兜底）。
 */
import { useCallback, useEffect, useState } from 'react';
import { ApiError, api } from '../api';
import { useAuth } from '../auth';
import { useI18n } from '../i18n';
import { BotKeyCard } from '../components/BotKeyCard';
import type { LibrarySummary, UserView } from '../api';

export function Users() {
  const { t } = useI18n();
  const { user: me } = useAuth();
  const [users, setUsers] = useState<UserView[] | null>(null);
  const [libs, setLibs] = useState<LibrarySummary[]>([]);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [openId, setOpenId] = useState<number | null>(null);
  const [creating, setCreating] = useState(false);

  const load = useCallback(async () => {
    try {
      const [u, l] = await Promise.all([api.users(), api.libraries()]);
      setUsers(u.users);
      setLibs(l.libraries);
      setError('');
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('读取用户列表失败'));
    }
  }, [t]);

  useEffect(() => {
    void load();
  }, [load]);

  async function run(fn: () => Promise<unknown>, ok: string) {
    setError('');
    try {
      await fn();
      setNotice(ok);
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('操作失败'));
    }
  }

  return (
    <>
      <div className="card">
        <h2>{t('用户')}</h2>
        <p className="hint">
          {t('权限只有四项：管理员、能看到哪些媒体库、并发播放数（0 = 用全局上限）、以及能不能转码 / 看直播。改口令、禁用或收紧库范围后，对方的登录会立刻失效。')}
        </p>
        {error && <div className="alert alert-error">{error}</div>}
        {notice && <div className="alert">{notice}</div>}
        {!users && <p className="muted">{t('正在读取…')}</p>}
        {users && (
          <div className="row" style={{ marginTop: 8 }}>
            <span className="badge">{t('用户 {n}', { n: users.length })}</span>
            <div className="spacer" />
            <button type="button" className="btn btn-primary btn-sm" onClick={() => setCreating((v) => !v)}>
              {creating ? t('取消') : t('＋ 新建用户')}
            </button>
          </div>
        )}
        {creating && (
          <CreateUserForm
            onDone={async (name) => {
              setCreating(false);
              setNotice(t('已创建用户「{name}」。', { name }));
              await load();
            }}
            onError={setError}
          />
        )}
        {users && users.length > 0 && (
          <div className="user-list">
            {users.map((u) => (
              <UserRow
                key={u.id}
                u={u}
                libs={libs}
                isSelf={me?.id === u.id}
                open={openId === u.id}
                onToggle={() => setOpenId(openId === u.id ? null : u.id)}
                run={run}
                onError={setError}
              />
            ))}
          </div>
        )}
      </div>
      <BotKeyCard />
    </>
  );
}

function CreateUserForm({
  onDone,
  onError,
}: {
  onDone: (name: string) => void | Promise<void>;
  onError: (msg: string) => void;
}) {
  const { t } = useI18n();
  const [username, setUsername] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [password, setPassword] = useState('');
  const [isAdmin, setIsAdmin] = useState(false);
  const [busy, setBusy] = useState(false);

  async function submit() {
    if (!username.trim() || !password) {
      onError(t('用户名与口令都要填'));
      return;
    }
    setBusy(true);
    try {
      await api.createUser({ username: username.trim(), password, displayName, isAdmin });
      await onDone(username.trim());
    } catch (e) {
      onError(e instanceof ApiError ? e.message : t('创建用户失败'));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="card" style={{ background: 'var(--bg-elev-2)' }}>
      <label className="field">
        <span>{t('用户名（登录用，不能带空格）')}</span>
        <input value={username} onChange={(e) => setUsername(e.target.value)} />
      </label>
      <label className="field">
        <span>{t('显示名（可留空）')}</span>
        <input value={displayName} onChange={(e) => setDisplayName(e.target.value)} />
      </label>
      <label className="field">
        <span>{t('口令（至少 8 位）')}</span>
        <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} />
      </label>
      <label className="small" style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
        <input type="checkbox" checked={isAdmin} onChange={(e) => setIsAdmin(e.target.checked)} />
        {t('设为管理员')}
      </label>
      <div className="row" style={{ marginTop: 10 }}>
        <button type="button" className="btn btn-primary btn-sm" disabled={busy} onClick={() => void submit()}>
          {busy ? t('创建中…') : t('创建')}
        </button>
      </div>
    </div>
  );
}

function UserRow({
  u,
  libs,
  isSelf,
  open,
  onToggle,
  run,
  onError,
}: {
  u: UserView;
  libs: LibrarySummary[];
  isSelf: boolean;
  open: boolean;
  onToggle: () => void;
  run: (fn: () => Promise<unknown>, ok: string) => Promise<void>;
  onError: (msg: string) => void;
}) {
  const { t } = useI18n();
  const [displayName, setDisplayName] = useState(u.displayName);
  const [streams, setStreams] = useState(String(u.maxConcurrentStreams));
  const [picked, setPicked] = useState<number[]>(u.libraryIds);
  const [newPw, setNewPw] = useState('');

  useEffect(() => {
    setDisplayName(u.displayName);
    setStreams(String(u.maxConcurrentStreams));
    setPicked(u.libraryIds);
  }, [u]);

  return (
    <div className="user-row">
      <button type="button" className="user-head" onClick={onToggle}>
        <span className="user-name">{u.displayName || u.username}</span>
        <span className="faint small">{u.username}</span>
        {u.isAdmin && <span className="badge">{t('管理员')}</span>}
        {u.isDisabled && <span className="badge">{t('已禁用')}</span>}
        <div className="spacer" />
        <span className="faint small">
          {u.restrictedLibraries ? t('{n} 个库', { n: u.libraryIds.length }) : t('全部库')}
          {u.maxConcurrentStreams > 0 ? t(' · 并发 {n}', { n: u.maxConcurrentStreams }) : ''}
          {u.allowTranscode ? '' : t(' · 禁止转码')}
          {u.allowLiveTV ? '' : t(' · 无直播')}
          {isSelf ? t(' · 这是你') : ''}
        </span>
      </button>

      {open && (
        <div className="user-body">
          <div className="row">
            <label className="field" style={{ minWidth: 200 }}>
              <span>{t('显示名')}</span>
              <input value={displayName} onChange={(e) => setDisplayName(e.target.value)} />
            </label>
            <label className="field" style={{ minWidth: 160 }}>
              <span>{t('并发播放上限（0 = 全局）')}</span>
              <input value={streams} onChange={(e) => setStreams(e.target.value)} inputMode="numeric" />
            </label>
            <button
              type="button"
              className="btn btn-sm"
              onClick={() =>
                void run(
                  () =>
                    api.updateUser(u.id, {
                      displayName,
                      maxConcurrentStreams: Number(streams) || 0,
                    }),
                  t('已保存「{name}」。', { name: u.displayName || u.username }),
                )
              }
            >
              {t('保存')}
            </button>
          </div>

          <div className="row" style={{ marginTop: 6 }}>
            <Toggle
              label={t('管理员')}
              on={u.isAdmin}
              disabled={isSelf}
              hint={isSelf ? t('不能改自己（防手一滑把自己锁在外面）') : undefined}
              onChange={(on) =>
                void run(() => api.updateUser(u.id, { isAdmin: on }), t('已更新。'))
              }
            />
            <Toggle
              label={t('允许转码')}
              on={u.allowTranscode}
              onChange={(on) =>
                void run(() => api.updateUser(u.id, { allowTranscode: on }), t('已更新。'))
              }
            />
            <Toggle
              label={t('允许直播')}
              on={u.allowLiveTV}
              onChange={(on) =>
                void run(() => api.updateUser(u.id, { allowLiveTV: on }), t('已更新。'))
              }
            />
            <Toggle
              label={t('禁用这个账号')}
              on={u.isDisabled}
              disabled={isSelf}
              onChange={(on) =>
                void run(
                  () => api.updateUser(u.id, { isDisabled: on }),
                  on ? t('已禁用（这个人的登录会立刻失效）。') : t('已恢复。'),
                )
              }
            />
          </div>

          <div style={{ marginTop: 10 }}>
            <Toggle
              label={t('只给勾选的媒体库')}
              on={u.restrictedLibraries}
              onChange={(on) =>
                void run(
                  () => api.updateUser(u.id, { restrictedLibraries: on }),
                  on ? t('已改为按库限制：下面勾什么就给什么。') : t('已改为全部库可见。'),
                )
              }
            />
            {u.restrictedLibraries ? (
              <>
                <div className="row" style={{ marginTop: 6, flexWrap: 'wrap' }}>
                  {libs.map((l) => (
                    <label key={l.id} className="small" style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
                      <input
                        type="checkbox"
                        checked={picked.includes(l.id)}
                        onChange={(e) =>
                          setPicked((prev) =>
                            e.target.checked ? [...prev, l.id] : prev.filter((x) => x !== l.id),
                          )
                        }
                      />
                      {l.name}
                    </label>
                  ))}
                </div>
                <button
                  type="button"
                  className="btn btn-sm btn-primary"
                  style={{ marginTop: 8 }}
                  onClick={() =>
                    void run(
                      () => api.setUserLibraries(u.id, picked),
                      t('已保存库授权（对方的登录会立刻失效，重新登录后生效）。'),
                    )
                  }
                >
                  {t('保存可见库（{n} 个）', { n: picked.length })}
                </button>
              </>
            ) : (
              <p className="hint">{t('现在这个人能看到全部媒体库。勾上面这项才能按库限制。')}</p>
            )}
          </div>

          <div className="row" style={{ marginTop: 12 }}>
            <label className="field" style={{ minWidth: 200 }}>
              <span>{t('重置口令（至少 8 位，重置即把他踢下线）')}</span>
              <input
                type="password"
                value={newPw}
                onChange={(e) => setNewPw(e.target.value)}
              />
            </label>
            <button
              type="button"
              className="btn btn-sm"
              disabled={!newPw}
              onClick={() =>
                void run(() => api.setUserPassword(u.id, newPw), t('口令已重置（旧登录已失效）。')).then(
                  () => setNewPw(''),
                )
              }
            >
              {t('重置口令')}
            </button>
            <div className="spacer" />
            <button
              type="button"
              className="btn btn-sm btn-danger"
              disabled={isSelf}
              onClick={() => {
                if (
                  !window.confirm(
                    t('删除用户「{name}」？他的收藏、播放列表与观看进度都会一起删掉（媒体文件不受影响）。', {
                      name: u.displayName || u.username,
                    }),
                  )
                ) {
                  return;
                }
                void run(() => api.deleteUser(u.id), t('已删除。')).catch((e) =>
                  onError(e instanceof ApiError ? e.message : t('删除失败')),
                );
              }}
            >
              {t('删除用户')}
            </button>
          </div>
        </div>
      )}
    </div>
  );
}

function Toggle({
  label,
  on,
  onChange,
  disabled,
  hint,
}: {
  label: string;
  on: boolean;
  onChange: (on: boolean) => void;
  disabled?: boolean;
  hint?: string;
}) {
  return (
    <label className="small" style={{ display: 'flex', gap: 6, alignItems: 'center' }} title={hint}>
      <input type="checkbox" checked={on} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
      {label}
    </label>
  );
}
