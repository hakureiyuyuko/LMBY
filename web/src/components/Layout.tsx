import { NavLink, Outlet, useLocation } from 'react-router-dom';
import { useAuth } from '../auth';
import { LANGS, useI18n } from '../i18n';
import { ThemeToggle } from './ThemeToggle';

export function Layout() {
  const { user, meta, signOut } = useAuth();
  const { lang, setLang, t } = useI18n();
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
          LMBY <small>{t('Light 的 Emby')}</small>
        </div>
        <nav className="nav">
          <NavLink to="/" end className={({ isActive }) => (isActive ? 'active' : '')}>
            {t('首页')}
          </NavLink>
          {/* 直播电视（M5）：普通用户也能用（频道列表 + 起播），源与探测只对管理员显示。
              被管理员关掉「允许直播」的账号看不到这个入口 —— 界面上不做点了就 403 的东西。 */}
          {(user?.allowLiveTV ?? true) && (
          <NavLink to="/livetv" className={({ isActive }) => (isActive ? 'active' : '')}>
            {t('直播')}
          </NavLink>
          )}
          <NavLink to="/search" className={({ isActive }) => (isActive ? 'active' : '')}>
            {t('搜索')}
          </NavLink>
          <NavLink to="/posters" className={({ isActive }) => (isActive || inBrowse ? 'active' : '')}>
            {t('海报墙')}
          </NavLink>
          <NavLink
            to="/lists"
            className={({ isActive }) =>
              isActive || loc.pathname.startsWith('/list/') ? 'active' : ''
            }
          >
            {t('我的列表')}
          </NavLink>
          <NavLink to="/account" className={({ isActive }) => (isActive ? 'active' : '')}>
            {t('个人中心')}
          </NavLink>
          {/* 设置只对管理员有意义（接口也会拦），普通用户不显示入口，免得点进去只看到一句无权限。
              库管理 / 人工匹配 / 会话监控都收在设置里（子页签），导航栏只留一条。 */}
          {user?.isAdmin && (
            <NavLink
              to="/settings"
              className={({ isActive }) => (isActive ? 'active' : '')}
            >
              {t('设置')}
            </NavLink>
          )}
        </nav>
        <div className="spacer" />
        <ThemeToggle />
        {/* 界面语言与主题并排：两个都是「这台设备上的显示偏好」，都是就地切换 */}
        <select
          className="lang-select"
          aria-label={t('界面语言')}
          data-lang-select
          value={lang}
          onChange={(e) => setLang(e.target.value as (typeof LANGS)[number]['value'])}
        >
          {LANGS.map((l) => (
            <option key={l.value} value={l.value}>
              {l.label}
            </option>
          ))}
        </select>
        <span className="faint">{user?.displayName || user?.username}</span>
        <button type="button" className="btn btn-sm btn-ghost" onClick={() => void signOut()}>
          {t('退出')}
        </button>
      </header>

      <main className={isHome ? 'content content-wide' : 'content'}>
        <Outlet />
      </main>

      <footer className="footer">
        LMBY {meta?.version ?? 'dev'}
      </footer>    </div>
  );
}
