import { useCallback, useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { ApiError, api } from '../api';
import { t, useI18n } from '../i18n';
import { kindLabel as itemKindLabel } from '../media';
import type { Item, LibrarySummary, MatchCandidate, MatchDetail } from '../api';

/**
 * 人工匹配：把「机器拿不准」的条目摆出来让人选。
 *
 * 为什么这张页面是必要的：TMDB 上同名条目很多（《烟花》《红辣椒》都有好几个版本），
 * 机器能算出「标题满分但领先第二名不够」，但选哪一个只有人知道。
 * 候选连同打分明细在刮削时就存进库了，所以这里不用重新搜一遍 —— 打开就能选。
 */

/** 匹配状态 → 人话。是**函数而不是常量表**：模块级常量会在 import 时把语言钉死。 */
function stateLabel(state: string): string {
  switch (state) {
    case 'local':
      return t('未刮削');
    case 'nfo':
      return t('来自 nfo');
    case 'matched':
      return t('已匹配');
    case 'review':
      return t('待确认');
    case 'manual':
      return t('已人工处理');
    case 'failed':
      return t('没找到');
    default:
      return state;
  }
}

type Tab = 'review' | 'failed';

export function Match() {
  const { t } = useI18n();
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
      .catch((e) => setError(e instanceof ApiError ? e.message : t('读取媒体库失败')));
  }, []);

  const loadItems = useCallback(async () => {
    if (libraryId == null) return;
    try {
      const res = await api.items(libraryId, '', 200, 0, [tab]);
      setItems(res.items);
      setTotal(res.total);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('读取条目失败'));
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
      if (!window.confirm(t('把选中的 {n} 条标记为「不需要匹配」？', { n: ids.length }))) return;
    } else {
      const tip = force
        ? t('给选中的 {n} 条排到队尾重刮，并强制覆盖已有元数据（锁住的字段不动）？', { n: ids.length })
        : t('给选中的 {n} 条排一次刮削（已有元数据的会自动跳过）？', { n: ids.length });
      if (!window.confirm(tip)) return;
    }
    setBusy(true);
    setNotice('');
    try {
      const res = await api.batchItems({
        action,
        itemIds: ids,
        force,
        reason: t('人工批量标记：不需要匹配'),
      });
      setNotice(
        t('已处理 {n} 条{skipped}。', {
          n: res.applied,
          skipped: res.skipped > 0 ? t('，跳过 {n} 条', { n: res.skipped }) : '',
        }),
      );
      setError('');
      setChecked(new Set());
      setSelected(null);
      await loadItems();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('批量操作失败'));
    } finally {
      setBusy(false);
    }
  }

  async function apply(itemId: number, providerId: number) {
    setBusy(true);
    setNotice('');
    try {
      await api.applyMatch(itemId, providerId);
      setNotice(t('已应用，这条以后不会再被自动覆盖'));
      setError('');
      await loadItems();
      setSelected(null);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('应用失败'));
    } finally {
      setBusy(false);
    }
  }

  async function skip(itemId: number) {
    const reason = window.prompt(t('为什么不需要匹配？（会记在条目上）'), t('自制/测试片，不需要元数据'));
    if (reason === null) return;
    setBusy(true);
    try {
      await api.skipMatch(itemId, reason);
      setNotice(t('已标记为「不需要匹配」'));
      await loadItems();
      setSelected(null);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('标记失败'));
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <div className="card">
        <h2>{t('人工匹配')}</h2>
        <p className="hint">
          {t('覆盖顺序是「本地 nfo / 本地图优先 → 缺的才去 TMDB 刮」。这里只处理机器拿不准的：')}
          <strong>{t('待确认')}</strong>
          {t('（有候选但不够确定，多半是 TMDB 上有多条同名条目）与')}
          <strong>{t('没找到')}</strong>
          {t('（搜不到候选）。候选连同打分明细是刮削时存下来的，打开就能选，不用重新搜。')}
        </p>

        <div className="row">
          <label className="field">
            <span>{t('媒体库')}</span>
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
              {t('待确认')}
            </button>
            <button
              type="button"
              className={tab === 'failed' ? 'tab active' : 'tab'}
              onClick={() => setTab('failed')}
            >
              {t('没找到')}
            </button>
          </div>
          <div className="spacer" />
          <button type="button" className="btn btn-sm" onClick={() => void loadItems()}>
            {t('刷新')}
          </button>
        </div>

        {error && <div className="alert alert-error">{error}</div>}
        {notice && <div className="alert alert-ok">{notice}</div>}
        {!items && <p className="muted">{t('正在读取…')}</p>}
        {items && items.length === 0 && (
          <p className="muted">
            {tab === 'review' ? t('没有待确认的条目 —— 都匹配好了。') : t('没有「没找到」的条目。')}
          </p>
        )}
        {items && items.length > 0 && (
          <p className="muted small">
            {t('共 {total} 条，显示前 {shown} 条 —— 勾选卡片左上角可以批量操作', {
              total,
              shown: items.length,
            })}
          </p>
        )}

        {checked.size > 0 && (
          <div className="row batch-bar">
            <strong>{t('已选 {n} 条', { n: checked.size })}</strong>
            <button
              type="button"
              className="btn btn-sm"
              disabled={busy}
              onClick={() => void batch('scrape')}
            >
              {t('排到队尾刮削')}
            </button>
            <button
              type="button"
              className="btn btn-sm btn-primary"
              disabled={busy}
              onClick={() => void batch('scrape', true)}
            >
              {t('强制重刮（覆盖未锁字段）')}
            </button>
            <button
              type="button"
              className="btn btn-sm btn-ghost"
              disabled={busy}
              onClick={() => void batch('skip')}
            >
              {t('标记不需要匹配')}
            </button>
            <div className="spacer" />
            <button
              type="button"
              className="btn btn-sm"
              disabled={busy}
              onClick={() => setChecked(new Set(items?.map((x) => x.id) ?? []))}
            >
              {t('全选本页')}
            </button>
            <button type="button" className="btn btn-sm btn-ghost" onClick={() => setChecked(new Set())}>
              {t('清除选择')}
            </button>
          </div>
        )}

        <div className="match-grid">
          {items?.map((it) => (
            <div className="match-cell" key={it.id}>
              <label className="cell-check" title={t('勾选后可批量操作')}>
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
                  <div className="match-title">{it.title || t('（无标题）')}</div>
                  <div className="muted small">
                    {itemKindLabel(it.kind)}
                    {it.year ? ` · ${it.year}` : ''}
                    {it.matchScore != null ? t(' · 分数 {n}', { n: it.matchScore.toFixed(2) }) : ''}
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
  const { t } = useI18n();
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
      .catch((e) => onError(e instanceof ApiError ? e.message : t('读取条目失败')));
  }, [itemId, onError]);

  async function search() {
    setSearching(true);
    try {
      const res = await api.searchMatch(itemId, query);
      setCandidates(res.candidates ?? []);
    } catch (e) {
      onError(e instanceof ApiError ? e.message : t('搜索失败'));
    } finally {
      setSearching(false);
    }
  }

  if (!detail) return <div className="card">{t('正在读取…')}</div>;
  const it = detail.item;

  return (
    <div className="card">
      <h2>{it.title || t('（无标题）')}</h2>
      <p className="muted small">
        {itemKindLabel(it.kind)}
        {it.year ? ` · ${it.year}` : ''}
        {it.originalTitle ? t(' · 原名 {title}', { title: it.originalTitle }) : ''}
        {t(' · 状态 {state}', { state: stateLabel(it.matchState) })}
        {it.metadataSource ? ` · ${t('元数据来源 {src}', { src: it.metadataSource })}` : ''}
      </p>
      {it.scrapeError && (
        <p className="muted small">{t('机器给的结论：{msg}', { msg: it.scrapeError })}</p>
      )}
      {it.overview && <p className="small">{it.overview}</p>}

      <div className="row">
        <input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={t('换个词再搜')}
          onKeyDown={(e) => {
            if (e.key === 'Enter') void search();
          }}
        />
        <button type="button" className="btn" disabled={searching} onClick={() => void search()}>
          {searching ? t('搜索中…') : t('搜索')}
        </button>
        <div className="spacer" />
        <Link className="btn btn-sm btn-ghost" to={`/items/${itemId}`}>
          {t('编辑字段与锁定')}
        </Link>
        <button type="button" className="btn btn-ghost" disabled={busy} onClick={onSkip}>
          {t('标记不需要匹配')}
        </button>
      </div>

      {!candidates && <p className="muted">{t('正在读取候选…')}</p>}
      {candidates && candidates.length === 0 && (
        <p className="muted">{t('没有候选。换个词再搜，或者标记为不需要匹配。')}</p>
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
                {t('分数 {n}', { n: c.score.toFixed(3) })}
                {c.margin != null ? t(' · 领先 {n}', { n: c.margin.toFixed(3) }) : ''}
                {c.matchedAlias ? t(' · 靠别名命中「{alias}」', { alias: c.matchedAlias }) : ''}
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
              {t('用这条')}
            </button>
          </div>
        ))}
      </div>
    </div>
  );
}
