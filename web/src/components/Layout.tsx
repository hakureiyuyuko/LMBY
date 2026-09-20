import { NavLink, Outlet } from 'react-router-dom';
import { useAuth } from '../auth';
import { ThemeToggle } from './ThemeToggle';

export function Layout() {
  const { user, meta, signOut } = useAuth();

  return (
    <div className="shell">
      <header className="topbar">
        <div className="brand">
          LMBY <small>Light 的 Emby</small>
        </div>
        <nav className="nav">
          <NavLink to="/libraries" className={({ isActive }) => (isActive ? 'active' : '')}>
            媒体库
          </NavLink>
          <NavLink to="/match" className={({ isActive }) => (isActive ? 'active' : '')}>
            人工匹配
          </NavLink>
          <NavLink to="/" end className={({ isActive }) => (isActive ? 'active' : '')}>
            概览
          </NavLink>
          <NavLink to="/account" className={({ isActive }) => (isActive ? 'active' : '')}>
            个人中心
          </NavLink>
        </nav>
        <div className="spacer" />
        <ThemeToggle />
        <span className="faint">{user?.displayName || user?.username}</span>
        <button type="button" className="btn btn-sm btn-ghost" onClick={() => void signOut()}>
          退出
        </button>
      </header>

      <main className="content">
        <Outlet />
      </main>

      <footer className="footer">LMBY {meta?.version ?? 'dev'} · M1 媒体库与扫描</footer>
    </div>
  );
}
