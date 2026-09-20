import { useCallback, useEffect, useState } from 'react';
import { api } from '../api';
import type { Health } from '../api';

function dotClass(ok: boolean) {
  return ok ? 'dot dot-ok' : 'dot dot-bad';
}

export function Home() {
  const [health, setHealth] = useState<Health | null>(null);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    try {
      setHealth(await api.health());
      setError('');
    } catch {
      setError('无法获取服务状态');
    }
  }, []);

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), 10000);
    return () => window.clearInterval(timer);
  }, [load]);

  return (
    <>
      <div className="card">
        <h2>服务状态</h2>
        <p className="hint">
          每 10 秒自动刷新。这里是 M0 骨架的验收面板，M1 起会被媒体库首页取代。
        </p>

        {error && <div className="alert alert-error">{error}</div>}

        {!health && !error && <p className="muted">正在读取…</p>}

        {health && (
          <>
            <p style={{ marginTop: 0 }}>
              <span className="badge">
                <span className={health.status === 'ok' ? 'dot dot-ok' : 'dot dot-warn'} />
                {health.status === 'ok' ? '运行正常' : health.status === 'degraded' ? '降级运行' : '异常'}
              </span>{' '}
              <span className="badge">
                <span className={dotClass(health.database.ok)} />
                数据库{' '}
                {health.database.ok ? `${health.database.latencyMs ?? 0} ms` : health.database.error}
              </span>{' '}
              <span className="badge">
                <span className={dotClass(health.ffmpeg.available)} />
                ffmpeg {health.ffmpeg.available ? health.ffmpeg.version : '不可用'}
              </span>
            </p>

            <dl className="kv">
              <dt>版本</dt>
              <dd>{health.version}</dd>
              <dt>运行时长</dt>
              <dd>{formatUptime(health.uptimeSeconds)}</dd>
              <dt>ffmpeg 路径</dt>
              <dd>{health.ffmpeg.path}</dd>
              <dt>硬件加速后端</dt>
              <dd>{health.ffmpeg.hw_accels?.join(', ') || '—'}</dd>
            </dl>

            <p className="faint" style={{ marginBottom: 0, marginTop: 14 }}>
              注意：「列出的后端」不等于「真的能用」。例如本机 ffmpeg 列出了 qsv，
              但 Gen9.5 核显缺运行时，实际只能用 vaapi —— 所以 M4 的能力探测会真跑一小段转码来验证。
            </p>
          </>
        )}
      </div>

      <div className="card">
        <h2>M0 已完成</h2>
        <p className="hint">骨架阶段的交付内容。</p>
        <ul className="muted" style={{ margin: 0, paddingLeft: 20 }}>
          <li>单二进制 + PostgreSQL，迁移内嵌在程序里，启动自动应用</li>
          <li>账号体系：初始化向导、登录/登出、argon2id 口令、可撤销会话</li>
          <li>个人中心：改口令（改完踢掉其它设备）、显示名、我的设备、主题偏好</li>
          <li>明暗主题：首屏即生效，localStorage 落盘，默认跟随系统</li>
          <li>登录失败限流、结构化日志、/healthz 健康检查</li>
        </ul>
      </div>

      <div className="card">
        <h2>下一步：M1 媒体库与扫描</h2>
        <p className="hint">路线图见仓库 docs/ROADMAP.md。</p>
        <ul className="muted" style={{ margin: 0, paddingLeft: 20 }}>
          <li>库与多根路径管理</li>
          <li>文件遍历 + 指纹增量 + 移动识别</li>
          <li>与 Emby 兼容的命名解析（含 150+ 条真实命名语料库）</li>
          <li>ffprobe 探测、本地 .nfo 与图片登记</li>
        </ul>
      </div>
    </>
  );
}

function formatUptime(sec: number): string {
  if (sec < 60) return `${sec} 秒`;
  const m = Math.floor(sec / 60);
  if (m < 60) return `${m} 分 ${sec % 60} 秒`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h} 小时 ${m % 60} 分`;
  return `${Math.floor(h / 24)} 天 ${h % 24} 小时`;
}
