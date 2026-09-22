import { useCallback, useEffect, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { ApiError, api } from '../api';
import type { Item, PlaylistSummary } from '../api';
import { formatClock } from '../capabilities';
import { kindLabel } from '../media';

/**
 * 列表详情（`/list/:id`）：一个播放列表或合集里的条目。
 *
 * 三个动作：
 *   - **播放全部** → 交给播放器（`/play/{第一条}?list=id`），播放器会用
 *     `GET /playlists/{id}/neighbors` 算出「上一项 / 下一项」，所以这个列表真的能连着看；
 *   - **移出**：只断关系，不动媒体文件；
 *   - **排序**（编辑模式下 ↑↓）：本地先换位（立刻有反馈），再整串写回服务端。
 *
 * 排序为什么不做拖拽：触屏与键盘都不好做，而 ↑↓ 两下就能把顺序调好，
 * 代价是几十行代码而不是一个拖拽库（依赖能少一个是一个）。
 */
export function ListDetail() {
  const { id = '' } = useParams();
  const listId = Number(id);
  const [playlist, setPlaylist] = useState<PlaylistSummary | null>(null);
  const [items, setItems] = useState<Item[]>([]);
  const [total, setTotal] = useState(0);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const [sorting, setSorting] = useState(false);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    if (!Number.isFinite(listId) || listId <= 0) return;
    setLoading(true);
    try {
      const [meta, page] = await Promise.all([api.list(listId), api.listItems(listId, { limit: 200 })]);
      setPlaylist(meta.playlist);
      setItems(page.items);
      setTotal(page.total);
      setError('');
    } catch (e) {
      setError(e instanceof ApiError ? e.message : '读取列表失败');
    } finally {
      setLoading(false);
    }
  }, [listId]);

  useEffect(() => {
    void load();
  }, [load]);

  /** 把第 i 条往上/往下挪，先本地换位（立刻有反馈），再整串写回服务端。 */
  async function move(i: number, dir: -1 | 1) {
    const j = i + dir;
    if (j < 0 || j >= items.length) return;
    const next = items.slice();
    [next[i], next[j]] = [next[j], next[i]];
    setItems(next);
    setBusy(true);
    try {
      await api.reorderList(listId, next.map((it) => it.id));
    } catch (e) {
      setError(e instanceof ApiError ? e.message : '排序失败');
      await load(); // 失败就把服务端的真顺序拉回来，别让界面停在假状态
    } finally {
      setBusy(false);
    }
  }

  async function removeItem(itemId: number) {
    setBusy(true);
    try {
      await api.removeFromList(listId, itemId);
      setItems((cur) => cur.filter((it) => it.id !== itemId));
      setTotal((t) => Math.max(0, t - 1));
    } catch (e) {
      setError(e instanceof ApiError ? e.message : '移出失败');
    } finally {
      setBusy(false);
    }
  }

  const first = items[0];

  return (
    <>
      <div className="card">
        <div className="row">
          <h2 style={{ margin: 0 }}>{playlist?.name ?? '列表'}</h2>
          {playlist?.kind === 'collection' && <span className="badge">合集</span>}
          {playlist && !playlist.mine && playlist.ownerName && (
            <span className="faint small">由 {playlist.ownerName} 维护</span>
          )}
        </div>
        <p className="muted small" style={{ marginTop: 6 }}>
          {total} 个条目
          {playlist?.overview ? ` · ${playlist.overview}` : ''}
        </p>

        <div className="row" style={{ marginTop: 10 }}>
          {first ? (
            <Link
              className="btn btn-primary"
              to={`/play/${first.id}?list=${listId}`}
              data-play-all={listId}
            >
              ▶ 播放全部
            </Link>
          ) : (
            <button type="button" className="btn" disabled>
              列表是空的
            </button>
          )}
          <button
            type="button"
            className={`btn${sorting ? ' btn-primary' : ''}`}
            onClick={() => setSorting((v) => !v)}
            disabled={items.length < 2}
          >
            {sorting ? '完成排序' : '调整顺序'}
          </button>
          <div className="spacer" />
          <Link className="btn" to="/lists">
            回列表页
          </Link>
        </div>

        {error && <div className="alert alert-error">{error}</div>}
        {loading && <p className="muted">正在加载…</p>}
        {!loading && !error && items.length === 0 && (
          <p className="muted">
            这个列表还是空的。到影片的<strong>详情页</strong>点「加入列表」，把想看的加进来
            （收藏按钮旁边）。
          </p>
        )}
      </div>

      {items.length > 0 && (
        <div className="card">
          <div className="search-grid">
            {items.map((it, i) => (
              <div className="list-item-card" key={it.id} data-item={it.id}>
                <Link className="search-card" to={`/item/${it.id}`}>
                  <img
                    className="search-thumb"
                    src={`/api/v1/items/${it.id}/images/poster?w=160`}
                    alt=""
                    loading="lazy"
                    onError={(e) => {
                      (e.currentTarget as HTMLImageElement).style.visibility = 'hidden';
                    }}
                  />
                  <div className="search-card-body">
                    <div className="search-title">{it.title || '（无标题）'}</div>
                    <div className="muted small">
                      {kindLabel(it.kind)}
                      {it.year ? ` · ${it.year}` : ''}
                      {it.runtimeTicks ? ` · ${formatClock(it.runtimeTicks / 10_000_000)}` : ''}
                    </div>
                  </div>
                </Link>
                <div className="list-item-actions">
                  <span className="faint small">#{i + 1}</span>
                  <div className="spacer" />
                  {sorting && (
                    <>
                      <button
                        type="button"
                        className="btn btn-sm"
                        aria-label="上移"
                        disabled={i === 0 || busy}
                        onClick={() => void move(i, -1)}
                      >
                        ↑
                      </button>
                      <button
                        type="button"
                        className="btn btn-sm"
                        aria-label="下移"
                        disabled={i === items.length - 1 || busy}
                        onClick={() => void move(i, 1)}
                      >
                        ↓
                      </button>
                    </>
                  )}
                  <button
                    type="button"
                    className="btn btn-sm"
                    disabled={busy}
                    onClick={() => void removeItem(it.id)}
                  >
                    移出
                  </button>
                </div>
              </div>
            ))}
          </div>
          {total > items.length && (
            <p className="muted small" style={{ marginTop: 10 }}>
              列表里共有 {total} 个条目，这里显示了前 {items.length} 个。
            </p>
          )}
        </div>
      )}
    </>
  );
}
