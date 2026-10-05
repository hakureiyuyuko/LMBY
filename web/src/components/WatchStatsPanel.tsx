/**
 * 观看统计（设置 → 观看统计，只给管理员看）。
 *
 * 回答两个问题：
 *   1. **谁看了什么** —— 明细表，可按用户筛、分页；
 *   2. **什么 / 谁最热** —— 两个排行（看得最多的影片、看得最多的用户）。
 *
 * 数据源是 playback_progress（口径见 internal/store/watchstats.go 的注释）：
 * 只统计「开播过」的条目 —— play_count 在开播那一刻就 +1，所以点开看了几秒也算。
 */
import { useCallback, useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { ApiError, api } from '../api';
import { useI18n } from '../i18n';
import type { UserView, WatchRecordsPayload, WatchStatsPayload } from '../api';

const PAGE = 50;

/** 把「剧名 · S01E02 · 集标题」拼成一行可读的标题。 */
function titleOf(x: {
  title: string;
  seriesTitle?: string;
  seasonNumber?: number;
  episodeNumber?: number;
}): string {
  const parts: string[] = [];
  if (x.seriesTitle) parts.push(x.seriesTitle);
  if (x.seasonNumber != null && x.episodeNumber != null) {
    parts.push(
      `S${String(x.seasonNumber).padStart(2, '0')}E${String(x.episodeNumber).padStart(2, '0')}`,
    );
  }
  if (x.title && x.title !== x.seriesTitle) parts.push(x.title);
  return parts.join(' · ') || '—';
}

/** 观看进度文案：看完了 / 看了百分之多少 / 没记录。 */
function progressText(positionTicks: number, durationTicks: number, played: boolean): string {
  if (played) return '100%';
  if (durationTicks <= 0) return '—';
  return `${Math.min(100, Math.round((positionTicks / durationTicks) * 100))}%`;
}

export function WatchStatsPanel() {
  const { t } = useI18n();
  const [stats, setStats] = useState<WatchStatsPayload | null>(null);
  const [records, setRecords] = useState<WatchRecordsPayload | null>(null);
  const [users, setUsers] = useState<UserView[]>([]);
  const [userId, setUserId] = useState(0);
  const [offset, setOffset] = useState(0);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);

  // 用户下拉：拿不到也不挡事（明细照样能看，只是不能按人筛）。
  useEffect(() => {
    void (async () => {
      try {
        const d = await api.users();
        setUsers(d.users);
      } catch {
        /* 静默 */
      }
    })();
  }, []);

  // 总览 + 两个排行：进页面加载一次。
  useEffect(() => {
    void (async () => {
      try {
        setStats(await api.watchStats({ top: 10 }));
      } catch (e) {
        setError(e instanceof ApiError ? e.message : t('读取观看统计失败'));
      }
    })();
  }, [t]);

  const loadRecords = useCallback(
    async (nextOffset: number, uid: number) => {
      setLoading(true);
      setError('');
      try {
        const d = await api.watchRecords({
          userId: uid || undefined,
          limit: PAGE,
          offset: nextOffset,
        });
        setRecords(d);
        setOffset(nextOffset);
      } catch (e) {
        setError(e instanceof ApiError ? e.message : t('读取观看记录失败'));
      } finally {
        setLoading(false);
      }
    },
    [t],
  );

  // 换用户就回到第一页（分页是对「筛选后的结果」分页）。
  useEffect(() => {
    void loadRecords(0, userId);
  }, [loadRecords, userId]);

  const nameOf = (u: { displayName: string; username: string }): string =>
    u.displayName || u.username || '—';
  const total = records?.total ?? 0;
  const from = total === 0 ? 0 : offset + 1;
  const to = Math.min(offset + PAGE, total);

  return (
    <div className="card">
      <h2>{t('观看统计')}</h2>
      {error && <div className="alert alert-error">{error}</div>}
      <p className="faint small">
        {t('按播放记录统计：谁看了哪些影片，以及看得最多的影片与用户。数据来自每个人的播放进度，只统计开播过的条目。')}
      </p>

      {stats && (
        <>
          <div className="stat-cards">
            <div className="stat-card">
              <span className="stat-num">{stats.totals.totalPlays}</span>
              <span className="stat-label">{t('总播放次数')}</span>
            </div>
            <div className="stat-card">
              <span className="stat-num">{stats.totals.watchedItems}</span>
              <span className="stat-label">{t('被看过的影片')}</span>
            </div>
            <div className="stat-card">
              <span className="stat-num">{stats.totals.activeUsers}</span>
              <span className="stat-label">{t('看过的用户')}</span>
            </div>
          </div>

          <div className="stat-cols">
            <div>
              <h3>{t('看得最多的影片')}</h3>
              {stats.topItems.length === 0 ? (
                <p className="muted">{t('还没有播放记录。')}</p>
              ) : (
                <table>
                  <thead>
                    <tr>
                      <th>#</th>
                      <th>{t('影片')}</th>
                      <th>{t('次数')}</th>
                      <th>{t('人数')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {stats.topItems.map((it, i) => (
                      <tr key={it.itemId}>
                        <td className="faint">{i + 1}</td>
                        <td className="small">
                          <Link to={`/item/${it.itemId}`}>{titleOf(it)}</Link>
                        </td>
                        <td>{it.plays}</td>
                        <td className="faint">{it.viewers}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
            <div>
              <h3>{t('看得最多的用户')}</h3>
              {stats.topUsers.length === 0 ? (
                <p className="muted">{t('还没有播放记录。')}</p>
              ) : (
                <table>
                  <thead>
                    <tr>
                      <th>#</th>
                      <th>{t('用户')}</th>
                      <th>{t('次数')}</th>
                      <th>{t('影片数')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {stats.topUsers.map((u, i) => (
                      <tr key={u.userId}>
                        <td className="faint">{i + 1}</td>
                        <td className="small">
                          <button
                            type="button"
                            className="linklike"
                            onClick={() => setUserId(u.userId)}
                          >
                            {nameOf(u)}
                          </button>
                        </td>
                        <td>{u.plays}</td>
                        <td className="faint">{u.items}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
          </div>
        </>
      )}

      <h3>{t('谁看了什么')}</h3>
      <div className="row">
        <select
          value={String(userId)}
          aria-label={t('用户')}
          onChange={(e) => setUserId(Number(e.target.value))}
        >
          <option value="0">{t('全部用户')}</option>
          {users.map((u) => (
            <option key={u.id} value={u.id}>
              {nameOf(u)}
            </option>
          ))}
        </select>
        <button
          type="button"
          className="btn btn-sm"
          disabled={loading}
          onClick={() => void loadRecords(offset, userId)}
        >
          {loading ? t('读取中…') : t('刷新')}
        </button>
      </div>

      {!records && !error && <p className="muted">{t('正在读取…')}</p>}
      {records && records.records.length === 0 && <p className="muted">{t('没有符合条件的记录。')}</p>}

      {records && records.records.length > 0 && (
        <>
          <table>
            <thead>
              <tr>
                <th>{t('用户')}</th>
                <th>{t('影片')}</th>
                <th>{t('次数')}</th>
                <th>{t('进度')}</th>
                <th>{t('最后观看')}</th>
              </tr>
            </thead>
            <tbody>
              {records.records.map((r) => (
                <tr key={`${r.userId}:${r.itemId}`}>
                  <td className="small">{nameOf(r)}</td>
                  <td className="small">
                    <Link to={`/item/${r.itemId}`}>{titleOf(r)}</Link>
                  </td>
                  <td>{r.plays}</td>
                  <td className="faint">
                    {progressText(r.positionTicks, r.durationTicks, r.played)}
                  </td>
                  <td className="small faint">
                    {r.lastPlayedAt ? new Date(r.lastPlayedAt).toLocaleString() : '—'}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          <div className="row" style={{ justifyContent: 'space-between', alignItems: 'center' }}>
            <span className="faint small">
              {t('第 {from}–{to} 条，共 {total} 条', { from, to, total })}
            </span>
            <span className="row" style={{ gap: 8 }}>
              <button
                type="button"
                className="btn btn-sm"
                disabled={offset === 0 || loading}
                onClick={() => void loadRecords(Math.max(0, offset - PAGE), userId)}
              >
                {t('上一页')}
              </button>
              <button
                type="button"
                className="btn btn-sm"
                disabled={to >= total || loading}
                onClick={() => void loadRecords(offset + PAGE, userId)}
              >
                {t('下一页')}
              </button>
            </span>
          </div>
        </>
      )}
    </div>
  );
}
