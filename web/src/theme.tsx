import { createContext, useCallback, useContext, useEffect, useState } from 'react';
import type { ReactNode } from 'react';

/** 用户可选的主题模式；system 表示跟随操作系统。 */
export type ThemeMode = 'light' | 'dark' | 'system';
export type ResolvedTheme = 'light' | 'dark';

const STORAGE_KEY = 'lmby-theme';

function systemTheme(): ResolvedTheme {
  return window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
}

/** 读取用户显式选择的模式；没选过返回 null，表示「跟随系统」。 */
export function readStoredMode(): ThemeMode | null {
  try {
    const v = localStorage.getItem(STORAGE_KEY);
    return v === 'light' || v === 'dark' || v === 'system' ? v : null;
  } catch {
    return null;
  }
}

interface ThemeValue {
  mode: ThemeMode;
  resolved: ResolvedTheme;
  setMode: (mode: ThemeMode) => void;
  toggle: () => void;
}

const ThemeContext = createContext<ThemeValue | null>(null);

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [mode, setModeState] = useState<ThemeMode>(() => readStoredMode() ?? 'system');
  const [sys, setSys] = useState<ResolvedTheme>(() => systemTheme());

  const resolved: ResolvedTheme = mode === 'system' ? sys : mode;

  // 跟随系统时，监听系统主题变化
  useEffect(() => {
    const mq = window.matchMedia('(prefers-color-scheme: light)');
    const onChange = () => setSys(systemTheme());
    mq.addEventListener('change', onChange);
    return () => mq.removeEventListener('change', onChange);
  }, []);

  // 应用到 <html>，同时持久化用户选择
  useEffect(() => {
    document.documentElement.dataset.theme = resolved;
    try {
      if (mode === 'system') {
        localStorage.removeItem(STORAGE_KEY);
      } else {
        localStorage.setItem(STORAGE_KEY, mode);
      }
    } catch {
      /* 隐私模式下 localStorage 可能不可用，忽略即可 */
    }
  }, [mode, resolved]);

  const setMode = useCallback((m: ThemeMode) => setModeState(m), []);

  // 右上角按钮：在当前显示效果上做反色，并把这个选择固定下来
  const toggle = useCallback(() => {
    setModeState(resolved === 'dark' ? 'light' : 'dark');
  }, [resolved]);

  return (
    <ThemeContext.Provider value={{ mode, resolved, setMode, toggle }}>
      {children}
    </ThemeContext.Provider>
  );
}

export function useTheme(): ThemeValue {
  const ctx = useContext(ThemeContext);
  if (!ctx) {
    throw new Error('useTheme 必须在 ThemeProvider 内使用');
  }
  return ctx;
}
