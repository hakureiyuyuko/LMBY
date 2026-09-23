import { useCallback, useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { api } from '../api';
import type { PlaySessionInfo, TranscodeSessionStat } from '../api';
import { useAuth } from '../auth';
import { useI18n } from '../i18n';
import { formatClock } from '../capabilities';

/**
 * 会话监控（M4）。
 *
 * 为什么需要这一页：转码是「边编边播」，出问题时（卡顿、CPU 打满、一直转圈）
 * 从播放器界面上根本看不出原因 —— 得看这一路 ffmpeg 到底在干什么：
 * 速度够不够实时、有没有被节流、客户端消费到哪、日志尾巴说了什么。
 *
 * 每 3 秒自动刷新；管理员还能一刀掐掉某一路（客户端已经不管、进程还在烧 CPU 时用）。
 */
export function Sessions() {
  const { t } = useI18n();
  const { user } = useAuth();
  const [sessions, setSessions] = useState<PlaySessionInfo[]>([]);
  const [streams, setStreams] = useState<TranscodeSessionStat[]>([]);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState('');
  const [loaded, setLoaded] = useState(false);

  const load = useCallback(async () => {
    try {
      const r = await api.playSessions();
      setSessions(r.sessions ?? []);
      setStreams(r.transcodeSessions ?? []);
      setError('');
    } catch (e) {
      setError(e instanceof Error ? e.message : t('读取会话失败'));
    } finally {
      setLoaded(true);
    }
  }, []);

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), 3000);
    return () => window.clearInterval(timer);
  }, [load]);

  const kill = async (key: string) => {
    setBusy(key);
    try {
      await api.stopTranscodeSession(key);
      await load();
    } catch (e) {
      setError(e instanceof Error ? e.message : t('终止失败'));
    } finally {
      setBusy('');
    }
  };

  return (
    <>
      {error && <div className="alert alert-error">{error}</div>}

      <div className="card">
        <h2>{t('播放会话')}</h2>
        {sessions.length === 0 ? (
          <p className="faint">{loaded ? t('当前没有人在播放。') : t('正在读取…')}</p>
        ) : (
          <table>
            <thead>
              <tr>
                <th>{t('条目')}</th>
                <th>{t('方式')}</th>
                <th>{t('文件')}</th>
                <th>{t('起始位置')}</th>
                <th>{t('时长')}</th>
                <th>{t('空闲')}</th>
              </tr>
            </thead>
            <tbody>
              {sessions.map((s) => (
                <tr key={s.playSessionId}>
                  <td>
                    <Link to={`/items/${s.itemId}`}>{s.title || `#${s.itemId}`}</Link>
                  </td>
                  <td>{s.mode}</td>
                  <td className="faint">{s.file}</td>
                  <td>{formatClock(s.startSeconds)}</td>
                  <td>{formatClock(s.durationSeconds)}</td>
                  <td className="faint">{s.idleSeconds}s</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      <div className="card">
        <h2>{t('转码 / 转封装会话')}</h2>
        {streams.length === 0 ? (
          <p className="faint">{loaded ? t('当前没有转码进程。') : t('正在读取…')}</p>
        ) : (
          <table>
            <thead>
              <tr>
                <th>{t('状态')}</th>
                <th>{t('速度')}</th>
                <th>fps</th>
                <th>{t('码率')}</th>
                <th>{t('分片')}</th>
                <th>{t('已生成')}</th>
                <th>{t('客户端')}</th>
                <th>{t('领先')}</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {streams.map((st) => (
                <tr key={st.key}>
                  <td>
                    {st.state}
                    {st.throttled ? t('（节流中）') : ''}
                  </td>
                  <td>{st.speed ? `${st.speed.toFixed(2)}x` : '—'}</td>
                  <td>{st.fps ? st.fps.toFixed(0) : '—'}</td>
                  <td>{st.bitrate && st.bitrate !== 'N/A' ? st.bitrate : '—'}</td>
                  <td>{st.segments}</td>
                  <td>{formatClock(st.generatedSeconds)}</td>
                  <td>{formatClock(st.clientSeconds)}</td>
                  <td>{formatClock(Math.max(0, st.aheadSeconds))}</td>
                  <td>
                    {user?.isAdmin && (
                      <button
                        type="button"
                        className="btn btn-sm btn-ghost"
                        disabled={busy === st.key}
                        onClick={() => void kill(st.key)}
                      >
                        {t('终止')}
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {streams.some((st) => st.log) && (
          <details>
            <summary className="hint">
              {t('日志')}
            </summary>
            {streams.map((st) =>
              st.log ? (
                <div key={st.key}>
                  <div className="faint">{st.key}</div>
                  <pre className="player-log">{st.log}</pre>
                </div>
              ) : null,
            )}
          </details>
        )}
      </div>
    </>
  );
}
