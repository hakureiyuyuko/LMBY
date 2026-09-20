import { useCallback, useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { ApiError, api } from '../api';
import type { Item, LibrarySummary, MatchCandidate, MatchDetail } from '../api';

/**
 * 人工匹配：把「机器拿不准」的条目摆出来让人选。
 *
 * 为什么这张页面是必要的：TMDB 上同名条目很多（《烟花》《红辣椒》都有好几个版本），
 * 机器能算出「标题满分但领先第二名不够」，但选哪一个只有人知道。
 * 候选连同打分明细在刮削时就存进库了，所以这里不用重新搜一遍 —— 打开就能选。
 */

const stateLabels: Record<string, string> = {
  local: '未刮削',
  nfo: '来自 nfo',
  matched: '已匹配',
  review: '待确认',
  manual: '已人工处理',
  failed: '没找到',
};

const kindLabels: Record<string, string> = {
  movie: '电影',
  series: '剧集',
  season: '季',
  episode: '集',
  extra: '花絮',
};

type Tab = 'review' | 'failed';

export function Match() {
  const [libraries, setLibraries] = useState<LibrarySummary[]>([]);
  const [libraryId, setLibraryId] = useState<number | null>(null);
  const [tab, setTab] = useState<Tab>('review');
  const [items, setItems] = useState<Item[] | null>(null);
  const [total, setTotal] = useState(0);
  const [selected, setSelected] = useState<number | null>(null);
  // checked 是「批量操作」的勾选集合；与 selected（打开右侧面板的那一条）是两件事：
  // 前者是选区，后者是焦点。混在一个状态里会让「点了卡片却又勾了别的」变得说不清。
  const [checked, setChecked] = useState<Set<number>>(new Set());
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api
      .libraries()
      .then((res) => {
        setLibraries(res.libraries);
        setLibraryId((prev) => prev ?? res.libraries[0]?.id ?? null);
      })
      .catch((e) => setError(e instanceof ApiError ? e.message : '读取媒体库失败'));
  }, []);

  const loadItems = useCallback(async () => {
    if (libraryId == null) return;
    try {
      const res = await api.items(libraryId, '', 200, 0, [tab]);
      setItems(res.items);
      setTotal(res.total);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : '读取条目失败');
      setItems([]);
    }
  }, [libraryId, tab]);

  useEffect(() => {
    setSelected(null);
    setChecked(new Set());
    void loadItems();
  }, [loadItems]);

  function toggleCheck(id: number) {
    setChecked((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  /** 批量操作：选中一批条目做同一件事（重刮 / 标记不需要匹配）。 */
  async function batch(action: 'scrape' | 'skip', force = false) {
    const ids = [...checked];
    if (ids.length === 0) return;
    if (action === 'skip') {
      if (!window.confirm(`把选中的 ${ids.length} 条标记为「不需要匹配」？`)) return;
    } else {
      const tip = force
        ? `给选中的 ${ids.length} 条排到队尾重刮，并强制覆盖已有元数据（锁住的字段不动）？`
        : `给选中的 ${ids.length} 条排一次刮削（已有元数据的会自动跳过）？`;
      if (!window.confirm(tip)) return;
    }
    setBusy(true);
    setNotice('');
    try {
      const res = await api.batchItems({ action, itemIds: ids, force, reason: '人工批量标记：不需要匹配' });
      setNotice(`已处理 ${res.applied} 条${res.skipped > 0 ? `，跳过 ${res.skipped} 条` : ''}。`);
      setError('');
      setChecked(new Set());
      setSelected(null);
      await loadItems();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : '批量操作失败');
    } finally {
      setBusy(false);
    }
  }

  async function apply(itemId: number, providerId: number) {
    setBusy(true);
    setNotice('');
    try {
      await api.applyMatch(itemId, providerId);
      setNotice('已应用，这条以后不会再被自动覆盖');
      setError('');
      await loadItems();
      setSelected(null);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : '应用失败');
    } finally {
      setBusy(false);
    }
  }

  async function skip(itemId: number) {
    const reason = window.prompt('为什么不需要匹配？（会记在条目上）', '自制/测试片，不需要元数据');
    if (reason === null) return;
    setBusy(true);
    try {
      await api.skipMatch(itemId, reason);
      setNotice('已标记为「不需要匹配」');
      await loadItems();
      setSelected(null);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : '标记失败');
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <div className="card">
        <h2>人工匹配</h2>
        <p className="hint">
          覆盖顺序是「本地 nfo / 本地图优先 → 缺的才去 TMDB 刮」。这里只处理机器拿不准的：
          <strong>待确认</strong>（有候选但不够确定，多半是 TMDB 上有多条同名条目）与
          <strong>没找到</strong>（搜不到候选）。候选连同打分明细是刮削时存下来的，打开就能选，不用重新搜。
        </p>

        <div className="row">
          <label className="field">
            <span>媒体库</span>
            <select
              value={libraryId ?? ''}
              onChange={(e) => setLibraryId(e.target.value ? Number(e.target.value) : null)}
            >
              {libraries.map((lib) => (
                <option key={lib.id} value={lib.id}>
                  {lib.name}
                </option>
              ))}
            </select>
          </label>
          <div className="tabs" style={{ marginBottom: 0 }}>
            <button
              type="button"
              className={tab === 'review' ? 'tab active' : 'tab'}
              onClick={() => setTab('review')}
            >
              待确认
            </button>
            <button
              type="button"
              className={tab === 'failed' ? 'tab active' : 'tab'}
              onClick={() => setTab('failed')}
            >
              没找到
            </button>
          </div>
          <div className="spacer" />
          <button type="button" className="btn btn-sm" onClick={() => void loadItems()}>
            刷新
          </button>
        </div>

        {error && <div className="alert alert-error">{error}</div>}
        {notice && <div className="alert alert-ok">{notice}</div>}
        {!items && <p className="muted">正在读取…</p>}
        {items && items.length === 0 && (
          <p className="muted">
            {tab === 'review' ? '没有待确认的条目 —— 都匹配好了。' : '没有「没找到」的条目。'}
          </p>
        )}
        {items && items.length > 0 && (
          <p className="muted small">
            共 {total} 条，显示前 {items.length} 条 —— 勾选卡片左上角可以批量操作
          </p>
        )}

        {checked.size > 0 && (
          <div className="row batch-bar">
            <strong>已选 {checked.size} 条</strong>
            <button
              type="button"
              className="btn btn-sm"
              disabled={busy}
              onClick={() => void batch('scrape')}
            >
              排到队尾刮削
            </button>
            <button
              type="button"
              className="btn btn-sm btn-primary"
              disabled={busy}
              onClick={() => void batch('scrape', true)}
            >
              强制重刮（覆盖未锁字段）
            </button>
            <button
              type="button"
              className="btn btn-sm btn-ghost"
              disabled={busy}
              onClick={() => void batch('skip')}
            >
              标记不需要匹配
            </button>
            <div className="spacer" />
            <button
              type="button"
              className="btn btn-sm"
              disabled={busy}
              onClick={() => setChecked(new Set(items?.map((x) => x.id) ?? []))}
            >
              全选本页
            </button>
            <button type="button" className="btn btn-sm btn-ghost" onClick={() => setChecked(new Set())}>
              清除选择
            </button>
          </div>
        )}

        <div className="match-grid">
          {items?.map((it) => (
            <div className="match-cell" key={it.id}>
              <label className="cell-check" title="勾选后可批量操作">
                <input
                  type="checkbox"
                  data-check={it.id}
                  checked={checked.has(it.id)}
                  onChange={() => toggleCheck(it.id)}
                />
              </label>
              <button
                type="button"
                className={selected === it.id ? 'match-card selected' : 'match-card'}
                onClick={() => setSelected(it.id === selected ? null : it.id)}
              >
                <img
                  className="match-thumb"
                  src={`/api/v1/items/${it.id}/images/poster?w=160`}
                  alt=""
                  loading="lazy"
                  onError={(e) => {
                    (e.currentTarget as HTMLImageElement).style.visibility = 'hidden';
                  }}
                />
                <div className="match-card-body">
                  <div className="match-title">{it.title || '（无标题）'}</div>
                  <div className="muted small">
                    {kindLabels[it.kind] ?? it.kind}
                    {it.year ? ` · ${it.year}` : ''}
                    {it.matchScore != null ? ` · 分数 ${it.matchScore.toFixed(2)}` : ''}
                  </div>
                  {it.scrapeError && <div className="muted small match-why">{it.scrapeError}</div>}
                </div>
              </button>
            </div>
          ))}
        </div>
      </div>

      {selected != null && (
        <MatchPanel
          itemId={selected}
          busy={busy}
          onApply={(providerId) => void apply(selected, providerId)}
          onSkip={() => void skip(selected)}
          onError={setError}
        />
      )}
    </>
  );
}

function MatchPanel({
  itemId,
  busy,
  onApply,
  onSkip,
  onError,
}: {
  itemId: number;
  busy: boolean;
  onApply: (providerId: number) => void;
  onSkip: () => void;
  onError: (msg: string) => void;
}) {
  const [detail, setDetail] = useState<MatchDetail | null>(null);
  const [candidates, setCandidates] = useState<MatchCandidate[] | null>(null);
  const [query, setQuery] = useState('');
  const [searching, setSearching] = useState(false);

  useEffect(() => {
    setDetail(null);
    setCandidates(null);
    api
      .itemMatch(itemId)
      .then((d) => {
        setDetail(d);
        setCandidates(d.candidates ?? []);
        setQuery(d.item.title ?? '');
      })
      .catch((e) => onError(e instanceof ApiError ? e.message : '读取条目失败'));
  }, [itemId, onError]);

  async function search() {
    setSearching(true);
    try {
      const res = await api.searchMatch(itemId, query);
      setCandidates(res.candidates ?? []);
    } catch (e) {
      onError(e instanceof ApiError ? e.message : '搜索失败');
    } finally {
      setSearching(false);
    }
  }

  if (!detail) return <div className="card">正在读取…</div>;
  const it = detail.item;

  return (
    <div className="card">
      <h2>{it.title || '（无标题）'}</h2>
      <p className="muted small">
        {kindLabels[it.kind] ?? it.kind}
        {it.year ? ` · ${it.year}` : ''}
        {it.originalTitle ? ` · 原名 ${it.originalTitle}` : ''} · 状态{' '}
        {stateLabel(it.matchState)}
        {it.metadataSource ? ` · 元数据来源 ${it.metadataSource}` : ''}
      </p>
      {it.scrapeError && <p className="muted small">机器给的结论：{it.scrapeError}</p>}
      {it.overview && <p className="small">{it.overview}</p>}

      <div className="row">
        <input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="换个词再搜"
          onKeyDown={(e) => {
            if (e.key === 'Enter') void search();
          }}
        />
        <button type="button" className="btn" disabled={searching} onClick={() => void search()}>
          {searching ? '搜索中…' : '搜索'}
        </button>
        <div className="spacer" />
        <Link className="btn btn-sm btn-ghost" to={`/items/${itemId}`}>
          编辑字段与锁定
        </Link>
        <button type="button" className="btn btn-ghost" disabled={busy} onClick={onSkip}>
          标记不需要匹配
        </button>
      </div>

      {!candidates && <p className="muted">正在读取候选…</p>}
      {candidates && candidates.length === 0 && (
        <p className="muted">没有候选。换个词再搜，或者标记为不需要匹配。</p>
      )}

      <div className="cand-list">
        {candidates?.map((c) => (
          <div key={c.candidateId} className="cand">
            {c.posterUrl ? (
              <img className="cand-poster" src={c.posterUrl} alt="" loading="lazy" />
            ) : (
              <div className="cand-poster cand-poster-empty" />
            )}
            <div className="cand-body">
              <div>
                <strong>{c.title}</strong>
                {c.year ? <span className="muted"> · {c.year}</span> : null}
                <span className="faint small"> · tmdb {c.candidateId}</span>
              </div>
              <div className="muted small">
                分数 {c.score.toFixed(3)}
                {c.margin != null ? ` · 领先 ${c.margin.toFixed(3)}` : ''}
                {c.matchedAlias ? ` · 靠别名命中「${c.matchedAlias}」` : ''}
              </div>
              {c.parts && c.parts.length > 0 && (
                <ul className="parts">
                  {c.parts.map((p) => (
                    <li key={p.name}>
                      <span className="faint">{p.name}</span> ×{p.weight} → {p.score.toFixed(3)}
                      <span className="muted"> {p.note}</span>
                    </li>
                  ))}
                </ul>
              )}
            </div>
            <button
              type="button"
              className="btn btn-sm btn-primary"
              disabled={busy}
              onClick={() => onApply(c.candidateId)}
            >
              用这条
            </button>
          </div>
        ))}
      </div>
    </div>
  );
}

function stateLabel(state: string): string {
  return stateLabels[state] ?? state;
}
