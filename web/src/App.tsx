import { useEffect, useRef } from 'react';
import { Link, Navigate, Route, Routes, useParams } from 'react-router-dom';
import { AuthProvider, useAuth } from './auth';
import { Layout } from './components/Layout';
import { t } from './i18n';
import { Account } from './pages/Account';
import { Browse } from './pages/Browse';
import { Detail } from './pages/Detail';
import { Home } from './pages/Home';
import { ItemEdit } from './pages/Item';
import { Libraries } from './pages/Libraries';
import { ListDetail } from './pages/ListDetail';
import { Lists } from './pages/Lists';
import { LiveTV } from './pages/LiveTV';
import { Login } from './pages/Login';
import { Match } from './pages/Match';
import { Player } from './pages/Player';
import { Posters } from './pages/Posters';
import { Search } from './pages/Search';
import { Sessions } from './pages/Sessions';
import { Settings, SettingsOverview } from './pages/Settings';
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
    return <div className="splash">{t('正在加载 LMBY…')}</div>;
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
        <Route path="/lists" element={<Lists />} />
        <Route path="/list/:id" element={<ListDetail />} />
        {/* 直播电视（M5）：频道列表 + 就地播放 + 管理员看的源/探测面板 */}
        <Route path="/livetv" element={<LiveTV />} />
        {/* 海报墙入口：一个库时直接进那面墙，多个库时先挑 */}
        <Route path="/posters" element={<Posters />} />
        <Route path="/library/:id" element={<Browse />} />
        {/* 条目详情（M6）：电影/剧集/季/集都在这一页；旧的剧集地址重定向过来 */}
        <Route path="/item/:id" element={<Detail />} />
        <Route path="/series/:id" element={<SeriesRedirect />} />
        {/* 设置：库管理 / 人工匹配 / 会话监控都是它的子页签（M6 收尾把三个导航项收进来） */}
        <Route path="/settings" element={<Settings />}>
          <Route index element={<SettingsOverview />} />
          <Route path="libraries" element={<Libraries />} />
          <Route path="match" element={<Match />} />
          <Route path="sessions" element={<Sessions />} />
        </Route>
        {/* 旧地址重定向：老书签、浏览器历史、别人分享过的链接都还指向它们 */}
        <Route path="/libraries" element={<Navigate to="/settings/libraries" replace />} />
        <Route path="/match" element={<Navigate to="/settings/match" replace />} />
        <Route path="/sessions" element={<Navigate to="/settings/sessions" replace />} />
        <Route path="/search" element={<Search />} />
        <Route path="/items/:id" element={<ItemEdit />} />
        {/* 播放器独立成页（全屏播放）*/}
        <Route path="/play/:id" element={<Player />} />
        <Route path="/account" element={<Account />} />
        <Route path="*" element={<NotFound />} />
      </Route>
    </Routes>
  );
}

/** 旧的剧集地址（`/series/:id`）重定向到详情页：
 *  剧集视图已经并入 `/item/:id`（季集列表就在详情页下面）。
 *  留着这条路由是因为老书签、浏览器历史与之前分享过的链接都指向它。 */
function SeriesRedirect() {
  const { id } = useParams();
  return <Navigate to={`/item/${id}`} replace />;
}

function NotFound() {
  return (
    <div className="card">
      <h2>{t('页面不存在')}</h2>
      <p className="hint">{t('这个地址在 LMBY 里没有对应页面。')}</p>
      <Link className="btn" to="/">
        {t('返回概览')}
      </Link>
    </div>
  );
}
