import { useCallback, useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { api } from '../api';
import type { ContinueWatchingEntry, Health } from '../api';
import { formatClock } from '../capabilities';

function dotClass(ok: boolean) {
  return ok ? 'dot dot-ok' : 'dot dot-bad';
}

export function Home() {
  const [health, setHealth] = useState<Health | null>(null);
  const [error, setError] = useState('');
  const [resume, setResume] = useState<ContinueWatchingEntry[]>([]);
  const [resumeError, setResumeError] = useState('');

  const load = useCallback(async () => {
    try {
      setHealth(await api.health());
      setError('');
    } catch {
      setError('无法获取服务状态');
    }
  }, []);

  const loadResume = useCallback(async () => {
    try {
      const res = await api.continueWatching(12);
      setResume(res.items);
      setResumeError('');
    } catch {
      setResumeError('读取继续观看列表失败');
    }
  }, []);

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), 10000);
    return () => window.clearInterval(timer);
  }, [load]);

  useEffect(() => {
    void loadResume();
  }, [loadResume]);

  async function markPlayed(itemId: number) {
    try {
      await api.setPlayed([itemId], true);
      await loadResume();
    } catch {
      /* 失败就下次刷新再说 */
    }
  }

  return (
    <>
      <div className="card">
        <h2>继续观看</h2>
        <p className="hint">
          进度按账号独立保存（服务端每 10 秒收一次心跳），关页面不会丢。标记为已看之后就不再出现在这里。
        </p>
        {resumeError && <div className="alert alert-error">{resumeError}</div>}
        {resume.length === 0 && !resumeError && (
          <p className="muted">
            还没有观看记录。去 <Link to="/libraries">媒体库</Link> 挑一部开始。
          </p>
        )}
        {resume.length > 0 && (
          <div className="continue-list">
            {resume.map((e) => {
              const pos = e.progress.positionTicks / 10_000_000;
              const total = e.progress.durationTicks / 10_000_000;
              const pct = total > 0 ? Math.min(100, Math.round((pos / total) * 100)) : 0;
              return (
                <div className="continue-card" key={e.item.id}>
                  <Link className="continue-poster" to={`/play/${e.item.id}`}>
                    <img
                      src={`/api/v1/items/${e.item.id}/images/poster?w=200`}
                      alt=""
                      loading="lazy"
                      onError={(ev) => {
                        (ev.currentTarget as HTMLImageElement).style.visibility = 'hidden';
                      }}
                    />
                    <span className="continue-bar">
                      <span style={{ width: `${pct}%` }} />
                    </span>
                  </Link>
                  <div className="continue-title" title={e.item.title}>
                    {e.item.title || '（无标题）'}
                  </div>
                  <div className="muted small">
                    看到 {formatClock(pos)} · 还剩 {formatClock(e.remainingTicks / 10_000_000)}
                  </div>
                  <div className="row" style={{ marginTop: 6 }}>
                    <Link className="btn btn-sm btn-primary" to={`/play/${e.item.id}`}>
                      继续播放
                    </Link>
                    <button
                      type="button"
                      className="btn btn-sm btn-ghost"
                      onClick={() => void markPlayed(e.item.id)}
                    >
                      标记已看
                    </button>
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </div>

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
        <h2>M1 已完成</h2>
        <p className="hint">媒体库与扫描阶段的交付内容。</p>
        <ul className="muted" style={{ margin: 0, paddingLeft: 20 }}>
          <li>库与多根路径管理，建库时校验路径可达性</li>
          <li>命名解析器：Emby 约定 + 本库「编码 分辨率 色彩 帧率 码率《标题》」规范</li>
          <li>增量扫描：指纹比对、移动识别（保留条目与进度）、软删除</li>
          <li>本地 nfo 导入（只读，不生成 XML）+ 图片归属登记（不入库二进制）</li>
          <li>SSE 实时扫描进度、扫描问题清单</li>
        </ul>
      </div>

      <div className="card">
        <h2>M3 已完成：播放</h2>
        <p className="hint">直出与转封装（DirectPlay / DirectStream）。</p>
        <ul className="muted" style={{ margin: 0, paddingLeft: 20 }}>
          <li>播放决策引擎：能直出就直出，否则只换容器；每一步都给出理由</li>
          <li>直出：HTTP Range / ETag / 断点续传，拖动瞬时响应</li>
          <li>转封装：mkv → HLS fMP4（视频不重新编码），窗口式预生成</li>
          <li>播放进度、续播、已看标记（按账号独立）</li>
          <li>文本字幕 → WebVTT，全屏播放器与快捷键</li>
        </ul>
      </div>

      <div className="card">
        <h2>下一步：M4 转码</h2>
        <p className="hint">路线图见仓库 docs/ROADMAP.md。</p>
        <ul className="muted" style={{ margin: 0, paddingLeft: 20 }}>
          <li>ffmpeg 能力探测（真跑一小段验证）+ 能力矩阵</li>
          <li>CPU / 硬件转码（VAAPI 优先）与质量档位</li>
          <li>图形字幕烧录、HDR → SDR tone mapping</li>
          <li>节流与回收：预生成 N 片后 SIGSTOP，分片被消费时 SIGCONT</li>
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
