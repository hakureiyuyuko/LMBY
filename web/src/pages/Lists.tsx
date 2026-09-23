import { useCallback, useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { ApiError, api } from '../api';
import type { PlaylistSummary } from '../api';
import { useI18n } from '../i18n';

/**
 * 「我的列表」：播放列表 + 合集。
 *
 * 两类东西放一页是有意的：它们的结构完全一样（一串有序条目），差别只在可见性 ——
 * 合集用「合集」小标签区分，谁建的也写在卡片上，不必让用户在两页之间来回找。
 *
 * 页面本身很薄：改名用行内输入（点「改名」原地变输入框），删除要二次确认，
 * 这些都是「列表管理」该有的最低限度；把条目加进来是详情页「加入列表」的活。
 */
export function Lists() {
  const { t } = useI18n();
  const [lists, setLists] = useState<PlaylistSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [name, setName] = useState('');
  const [kind, setKind] = useState<'playlist' | 'collection'>('playlist');
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState<number | null>(null);
  const [editName, setEditName] = useState('');

  const load = useCallback(async () => {
    try {
      const res = await api.lists();
      setLists(res.playlists);
      setError('');
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('读取列表失败'));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function create(e: React.FormEvent) {
    e.preventDefault();
    const trimmed = name.trim();
    if (!trimmed) return;
    setBusy(true);
    try {
      await api.createList({ name: trimmed, kind });
      setName('');
      await load();
    } catch (e2) {
      setError(e2 instanceof ApiError ? e2.message : t('新建失败'));
    } finally {
      setBusy(false);
    }
  }

  async function rename(id: number) {
    const trimmed = editName.trim();
    if (!trimmed) return;
    try {
      await api.updateList(id, { name: trimmed });
      setEditing(null);
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('改名失败'));
    }
  }

  async function remove(p: PlaylistSummary) {
    // 删列表**不动媒体文件** —— 这句要写在确认里，不然用户会以为片子被删了
    if (!window.confirm(
      t('删除{kind}「{name}」？\n（只删这个列表，媒体文件不受影响）', {
        kind: p.kind === 'collection' ? t('合集') : t('列表'),
        name: p.name,
      }),
    ))
      return;
    try {
      await api.deleteList(p.id);
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('删除失败'));
    }
  }

  const mine = lists.filter((p) => p.mine);
  const others = lists.filter((p) => !p.mine);

  return (
    <>
      <div className="card">
        <h2>{t('我的列表')}</h2>
        <form className="row" onSubmit={create}>
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={t('新列表的名字，如「周末补番」')}
            aria-label={t('新列表的名字')}
          />
          <select value={kind} onChange={(e) => setKind(e.target.value as 'playlist' | 'collection')}>
            <option value="playlist">{t('私人播放列表')}</option>
            <option value="collection">{t('合集（所有人可见）')}</option>
          </select>
          <button type="submit" className="btn btn-primary" disabled={busy || !name.trim()}>
            {t('新建')}
          </button>
        </form>
        {error && <div className="alert alert-error">{error}</div>}
      </div>

      <div className="card">
        {loading && <p className="muted">{t('正在加载…')}</p>}
        {!loading && lists.length === 0 && (
          <p className="muted">
            {t('还没有任何列表。上面新建一个，然后去影片详情页把它加进去')}
            {t('（详情页的「加入列表」按钮）。')}
          </p>
        )}

        {[
          { title: t('我的'), items: mine },
          { title: t('合集（所有人可见）'), items: others },
        ].map((group) =>
          group.items.length === 0 ? null : (
            <div key={group.title}>
              <h3 className="list-group-title">{group.title}</h3>
              <ul className="list-cards">
                {group.items.map((p) => (
                  <li className="list-card" key={p.id} data-list={p.id}>
                    <Link className="list-cover" to={`/list/${p.id}`} aria-label={p.name}>
                      {p.coverItemId ? (
                        <img
                          src={`/api/v1/items/${p.coverItemId}/images/poster?w=200`}
                          alt=""
                          loading="lazy"
                          onError={(e) => {
                            (e.currentTarget as HTMLImageElement).style.visibility = 'hidden';
                          }}
                        />
                      ) : (
                        <span className="list-cover-empty">{t('空')}</span>
                      )}
                    </Link>
                    <div className="list-body">
                      {editing === p.id ? (
                        <div className="row">
                          <input
                            value={editName}
                            onChange={(e) => setEditName(e.target.value)}
                            aria-label={t('新的列表名')}
                            autoFocus
                          />
                          <button type="button" className="btn btn-sm" onClick={() => void rename(p.id)}>
                            {t('保存')}
                          </button>
                          <button type="button" className="btn btn-sm" onClick={() => setEditing(null)}>
                            {t('取消')}
                          </button>
                        </div>
                      ) : (
                        <Link className="list-name" to={`/list/${p.id}`}>
                          {p.name}
                        </Link>
                      )}
                      <div className="muted small">
                        {p.kind === 'collection' ? t('合集') : t('播放列表')} ·{' '}
                        {t('{n} 个条目', { n: p.itemCount })}
                        {!p.mine && p.ownerName ? ` · ${t('由 {name} 维护', { name: p.ownerName })}` : ''}
                      </div>
                      <div className="row" style={{ marginTop: 6 }}>
                        <Link className="btn btn-sm" to={`/list/${p.id}`}>
                          {t('打开')}
                        </Link>
                        {(p.mine || p.kind === 'collection') && (
                          <button
                            type="button"
                            className="btn btn-sm"
                            onClick={() => {
                              setEditing(p.id);
                              setEditName(p.name);
                            }}
                          >
                            {t('改名')}
                          </button>
                        )}
                        {(p.mine || p.kind === 'collection') && (
                          <button type="button" className="btn btn-sm" onClick={() => void remove(p)}>
                            {t('删除')}
                          </button>
                        )}
                      </div>
                    </div>
                  </li>
                ))}
              </ul>
            </div>
          ),
        )}
      </div>
    </>
  );
}
