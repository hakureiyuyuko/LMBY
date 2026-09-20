import type { ReactNode } from 'react';
import { ThemeToggle } from './ThemeToggle';

/** 登录 / 初始化这类「未登录页面」的外壳：居中卡片 + 右上角主题切换。 */
export function AuthShell({
  title,
  subtitle,
  children,
}: {
  title: string;
  subtitle: string;
  children: ReactNode;
}) {
  return (
    <>
      <ThemeToggle floating />
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
