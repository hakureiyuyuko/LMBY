import { createContext, useCallback, useContext, useEffect, useState } from 'react';
import type { ReactNode } from 'react';
import { api } from './api';
import type { Me, Meta, Preferences, User } from './api';

type Status = 'loading' | 'anonymous' | 'authenticated';

interface AuthValue {
  status: Status;
  meta: Meta | null;
  user: User | null;
  preferences: Preferences | null;
  /** 用后端最新数据刷新整个登录态。 */
  refresh: () => Promise<void>;
  signIn: (username: string, password: string) => Promise<void>;
  runSetup: (username: string, password: string, displayName: string) => Promise<void>;
  signOut: () => Promise<void>;
  /** 其它页面拿到新的 Me（例如改完偏好）后同步到全局。 */
  applyMe: (me: Me) => void;
}

const AuthContext = createContext<AuthValue | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<Status>('loading');
  const [meta, setMeta] = useState<Meta | null>(null);
  const [user, setUser] = useState<User | null>(null);
  const [preferences, setPreferences] = useState<Preferences | null>(null);

  const applyMe = useCallback((me: Me) => {
    setUser(me.user);
    setPreferences(me.preferences);
    setStatus('authenticated');
  }, []);

  const refresh = useCallback(async () => {
    let m: Meta;
    try {
      m = await api.meta();
    } catch {
      setMeta(null);
      setStatus('anonymous');
      return;
    }
    setMeta(m);

    if (m.setupRequired) {
      setUser(null);
      setPreferences(null);
      setStatus('anonymous');
      return;
    }

    try {
      applyMe(await api.me());
    } catch {
      setUser(null);
      setPreferences(null);
      setStatus('anonymous');
    }
  }, [applyMe]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const signIn = useCallback(
    async (username: string, password: string) => {
      applyMe(await api.login(username, password));
    },
    [applyMe],
  );

  const runSetup = useCallback(
    async (username: string, password: string, displayName: string) => {
      applyMe(await api.setup(username, password, displayName));
      setMeta((prev) => (prev ? { ...prev, setupRequired: false } : prev));
    },
    [applyMe],
  );

  const signOut = useCallback(async () => {
    try {
      await api.logout();
    } finally {
      setUser(null);
      setPreferences(null);
      setStatus('anonymous');
    }
  }, []);

  return (
    <AuthContext.Provider
      value={{ status, meta, user, preferences, refresh, signIn, runSetup, signOut, applyMe }}
    >
      {children}
    </AuthContext.Provider>
  );
}

export function useAuth(): AuthValue {
  const ctx = useContext(AuthContext);
  if (!ctx) {
    throw new Error('useAuth 必须在 AuthProvider 内使用');
  }
  return ctx;
}
