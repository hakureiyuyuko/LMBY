import { NavLink, Outlet, useLocation } from 'react-router-dom';
import { useAuth } from '../auth';
import { ThemeToggle } from './ThemeToggle';

export function Layout() {
  const { user, meta, signOut } = useAuth();
  const loc = useLocation();
  // 「海报墙」在只有一个库时会重定向到 /library/{id}，所以在库/剧集/详情页里
  // 也要把它标成当前项 —— 否则点进去之后导航栏看起来“没选中任何东西”。
  const inBrowse =
    loc.pathname.startsWith('/library/') ||
    loc.pathname.startsWith('/series/') ||
    loc.pathname.startsWith('/item/');
  // 首页（Netflix 风格）要更宽的容器：轮播横幅在 960px 里显得局促
  const isHome = loc.pathname === '/';

  return (
    <div className="shell">
      <header className="topbar">
        <div className="brand">
          LMBY <small>Light 的 Emby</small>
        </div>
        <nav className="nav">
          <NavLink to="/search" className={({ isActive }) => (isActive ? 'active' : '')}>
            搜索
          </NavLink>
          <NavLink to="/posters" className={({ isActive }) => (isActive || inBrowse ? 'active' : '')}>
            海报墙
          </NavLink>
          <NavLink to="/libraries" className={({ isActive }) => (isActive ? 'active' : '')}>
            库管理
          </NavLink>
          {/* 直播电视（M5）：普通用户也能用（频道列表 + 起播），源与探测只对管理员显示 */}
          <NavLink to="/livetv" className={({ isActive }) => (isActive ? 'active' : '')}>
            直播
          </NavLink>
          <NavLink to="/match" className={({ isActive }) => (isActive ? 'active' : '')}>
            人工匹配
          </NavLink>
          <NavLink to="/" end className={({ isActive }) => (isActive ? 'active' : '')}>
            首页
          </NavLink>
          <NavLink to="/account" className={({ isActive }) => (isActive ? 'active' : '')}>
            个人中心
          </NavLink>
          {/* 会话监控（M4）也只对管理员显示：普通用户看自己的播放会话没多大意义 */}
          {user?.isAdmin && (
            <NavLink to="/sessions" className={({ isActive }) => (isActive ? 'active' : '')}>
              会话
            </NavLink>
          )}
          {/* 设置只对管理员有意义（接口也会拦），普通用户不显示入口，免得点进去只看到一句无权限 */}
          {user?.isAdmin && (
            <NavLink to="/settings" className={({ isActive }) => (isActive ? 'active' : '')}>
              设置
            </NavLink>
          )}
        </nav>
        <div className="spacer" />
        <ThemeToggle />
        <span className="faint">{user?.displayName || user?.username}</span>
        <button type="button" className="btn btn-sm btn-ghost" onClick={() => void signOut()}>
          退出
        </button>
      </header>

      <main className={isHome ? 'content content-wide' : 'content'}>
        <Outlet />
      </main>

      <footer className="footer">LMBY {meta?.version ?? 'dev'} · M5 播放（直出 / 转封装 / 转码 / 直播）</footer>
    </div>
  );
}
