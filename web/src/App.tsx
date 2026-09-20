import { useEffect, useRef } from 'react';
import { Link, Navigate, Route, Routes } from 'react-router-dom';
import { AuthProvider, useAuth } from './auth';
import { Layout } from './components/Layout';
import { Account } from './pages/Account';
import { Browse } from './pages/Browse';
import { Home } from './pages/Home';
import { ItemEdit } from './pages/Item';
import { Libraries } from './pages/Libraries';
import { Login } from './pages/Login';
import { Match } from './pages/Match';
import { Player } from './pages/Player';
import { Search } from './pages/Search';
import { SeriesView } from './pages/Series';
import { Settings } from './pages/Settings';
import { Setup } from './pages/Setup';
import { ThemeProvider, readStoredMode, useTheme } from './theme';

export default function App() {
  return (
    <ThemeProvider>
      <AuthProvider>
        <ThemeSync />
        <AppRoutes />
      </AuthProvider>
    </ThemeProvider>
  );
}

/**
 * 把账号上的主题偏好同步到本机。
 *
 * 规则：只有本机从未显式选择过主题时才采纳服务端设置 —— 这样换设备登录能延续偏好，
 * 而不会把本机用户已经手动切好的主题覆盖掉。
 */
function ThemeSync() {
  const { preferences } = useAuth();
  const { setMode } = useTheme();
  const done = useRef(false);

  useEffect(() => {
    if (done.current) return;
    const t = preferences?.theme;
    if (!t) return;
    done.current = true;
    if (readStoredMode() === null && (t === 'light' || t === 'dark' || t === 'system')) {
      setMode(t);
    }
  }, [preferences, setMode]);

  return null;
}

function AppRoutes() {
  const { status, meta } = useAuth();

  if (status === 'loading') {
    return <div className="splash">正在加载 LMBY…</div>;
  }

  const setupRequired = meta?.setupRequired ?? false;
  const anonymousHome = setupRequired ? '/setup' : '/login';
  const authed = status === 'authenticated';

  return (
    <Routes>
      <Route path="/setup" element={setupRequired ? <Setup /> : <Navigate to="/" replace />} />

      <Route
        path="/login"
        element={
          authed ? (
            <Navigate to="/" replace />
          ) : setupRequired ? (
            <Navigate to="/setup" replace />
          ) : (
            <Login />
          )
        }
      />

      <Route element={authed ? <Layout /> : <Navigate to={anonymousHome} replace />}>
        <Route index element={<Home />} />
        <Route path="/libraries" element={<Libraries />} />
        <Route path="/library/:id" element={<Browse />} />
        <Route path="/series/:id" element={<SeriesView />} />
        <Route path="/settings" element={<Settings />} />
        <Route path="/search" element={<Search />} />
        <Route path="/items/:id" element={<ItemEdit />} />
        {/* 播放器独立成页（全屏播放）*/}
        <Route path="/play/:id" element={<Player />} />
        <Route path="/match" element={<Match />} />
        <Route path="/account" element={<Account />} />
        <Route path="*" element={<NotFound />} />
      </Route>
    </Routes>
  );
}

function NotFound() {
  return (
    <div className="card">
      <h2>页面不存在</h2>
      <p className="hint">这个地址在 LMBY 里没有对应页面。</p>
      <Link className="btn" to="/">
        返回概览
      </Link>
    </div>
  );
}
