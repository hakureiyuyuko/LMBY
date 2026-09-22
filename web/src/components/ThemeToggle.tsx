import { useI18n } from '../i18n';
import { useTheme } from '../theme';

/** 明暗主题切换按钮。默认放右上角；floating 用于登录/初始化这类居中页面。 */
export function ThemeToggle({ floating = false }: { floating?: boolean }) {
  const { resolved, toggle } = useTheme();
  const { t } = useI18n();
  const label = resolved === 'dark' ? t('切换到亮色主题') : t('切换到暗色主题');

  return (
    <button
      type="button"
      className={floating ? 'theme-toggle theme-float' : 'theme-toggle'}
      onClick={toggle}
      title={label}
      aria-label={label}
    >
      <span aria-hidden="true">{resolved === 'dark' ? '☾' : '☀'}</span>
      <span>{resolved === 'dark' ? t('暗色') : t('亮色')}</span>
    </button>
  );
}
