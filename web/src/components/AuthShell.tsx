import type { ReactNode } from 'react';
import { LANGS, useI18n } from '../i18n';
import { ThemeToggle } from './ThemeToggle';

/** 登录 / 初始化这类「未登录页面」的外壳：居中卡片 + 右上角主题与语言切换。 */
export function AuthShell({
  title,
  subtitle,
  children,
}: {
  title: string;
  subtitle: string;
  children: ReactNode;
}) {
  const { lang, setLang, t } = useI18n();
  return (
    <>
      <ThemeToggle floating />
      {/* 登录页也要能选语言：还没登录的人最需要它（他是谁、说什么语言都还不知道） */}
      <select
        className="lang-select lang-float"
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
      <div className="center-shell">
        <div className="center-card">
          <h1>{title}</h1>
          <p className="sub">{subtitle}</p>
          {children}
        </div>
      </div>
    </>
  );
}
