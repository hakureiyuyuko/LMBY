import { useEffect, useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { ApiError, api } from '../api';
import type { LibrarySummary, SearchFacets, SearchPage, SearchPeoplePage } from '../api';
import { SearchBox } from '../components/SearchBox';
import { useI18n } from '../i18n';
import { kindLabel } from '../media';
import { roleText } from '../people';

/**
 * 搜索（M2 起，M6 补齐「即时联想 + 结果分面」）。
 *
 * 匹配（细节见 migrations/0007_search.sql、0011_search_people.sql 与 internal/store/search.go）：
 *   - 中文按二元组切词（「炼金」能搜到《钢之炼金术师》），英文按整词；
 *   - 同时容忍错字（trigram 相似：「钢之炼金术土」也能命中）；
 *   - 单字查询走子串兜底（索引里只有两字的单元）；
 *   - **人和作品用的是同一套切法**，所以演员名也能搜、能联想。
 *
 * 这一页有三件事，各自的边界很清楚：
 *   1. **即时联想**（输入框下面的下拉）：只回答「你正在敲的这几个字最先撞上谁」，
 *      **不看当前筛选** —— 用户还在敲字时用库/流派去套只会让下拉越来越空；
 *   2. **结果分面**（类型 / 媒体库 / 流派 / 人）：每一维统计时都关掉自己这一维的筛选
 *      （正在生效的「类型=电影」不该让「剧集」那一格变成 0，否则永远切不过去）；
 *   3. **结果列表**：条目（可翻页）或「人」（另一档，点进去变成「这个人的作品」）。
 *
 * 全部状态都在 URL 上（?q=&kind=&genre=&lib=&person=&name=&tab=&page=）：
 * 可收藏、可后退、也能直接贴给别人；分面的数字与列表的筛选因此不可能对不上。
 */

const PAGE_SIZE = 24;
const PEOPLE_PAGE_SIZE = 24;

export function Search() {
  const { t } = useI18n();
  const [params, setParams] = useSearchParams();
  const navigate = useNavigate();

  const q = params.get('q') ?? '';
  const kind = params.get('kind') ?? '';
  const genre = params.get('genre') ?? '';
  const lib = params.get('lib') ?? '';
  const person = params.get('person') ?? '';
  const personName = params.get('name') ?? '';
  const tab = params.get('tab') === 'people' && q ? 'people' : 'items';
  const page = Math.max(0, Number(params.get('page') ?? '0') || 0);

  const [input, setInput] = useState(q);
  const [libraries, setLibraries] = useState<LibrarySummary[]>([]);
  const [facets, setFacets] = useState<SearchFacets | null>(null);
  const [data, setData] = useState<SearchPage | null>(null);
  const [people, setPeople] = useState<SearchPeoplePage | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  // 后退/前进时输入框跟着 URL 走
  useEffect(() => setInput(q), [q]);

  useEffect(() => {
    api
      .libraries()
      .then((res) => setLibraries(res.libraries))
      .catch(() => {
        /* 库列表拿不到不影响搜索 */
      });
  }, []);

  const someFilter = Boolean(kind || genre || lib || person);
  const anything = Boolean(q) || someFilter;

  // 分面：只在「词或筛选变了」时重算（翻页不必跟着算，白算）
  useEffect(() => {
    if (!anything) {
      setFacets(null);
      return;
    }
    let alive = true;
    api
      .searchFacets({
        q,
        kind: kind || undefined,
        genre: genre || undefined,
        libraryId: lib ? Number(lib) : undefined,
        personId: person ? Number(person) : undefined,
      })
      .then((res) => {
        if (alive) setFacets(res.facets);
      })
      .catch(() => {
        if (alive) setFacets(null);
      });
    return () => {
      alive = false;
    };
  }, [q, kind, genre, lib, person, anything]);

  // 结果：条目 / 人两条路，参数不同、互不影响
  useEffect(() => {
    if (!anything) {
      setData(null);
      setPeople(null);
      return;
    }
    let alive = true;
    setBusy(true);
    const done = () => {
      if (alive) setBusy(false);
    };
    if (tab === 'people') {
      api
        .searchPeople({ q, limit: PEOPLE_PAGE_SIZE, offset: page * PEOPLE_PAGE_SIZE })
        .then((res) => {
          if (!alive) return;
          setPeople(res);
          setData(null);
          setError('');
        })
        .catch((e) => {
          if (!alive) return;
          setError(e instanceof ApiError ? e.message : t('搜索失败'));
          setPeople(null);
        })
        .finally(done);
    } else {
      api
        .search({
          q,
          kind: kind || undefined,
          genre: genre || undefined,
          libraryId: lib ? Number(lib) : undefined,
          personId: person ? Number(person) : undefined,
          limit: PAGE_SIZE,
          offset: page * PAGE_SIZE,
        })
        .then((res) => {
          if (!alive) return;
          setData(res);
          setPeople(null);
          setError('');
        })
        .catch((e) => {
          if (!alive) return;
          setError(e instanceof ApiError ? e.message : t('搜索失败'));
          setData(null);
        })
        .finally(done);
    }
    return () => {
      alive = false;
    };
  }, [q, kind, genre, lib, person, tab, page, anything]);

  type Nav = {
    q?: string;
    kind?: string;
    genre?: string;
    lib?: string;
    person?: string;
    name?: string;
    tab?: string;
    page?: number;
  };

  /** 改状态一律走它：拼 URL（空值不写进去，链接才干净），并且**换筛选就回到第 1 页**。 */
  function go(over: Nav) {
    const next: Required<Omit<Nav, 'name'>> & { name: string } = {
      q,
      kind,
      genre,
      lib,
      person,
      name: personName,
      tab,
      page: 0,
      ...over,
    };
    const out: Record<string, string> = {};
    if (next.q) out.q = next.q;
    if (next.kind) out.kind = next.kind;
    if (next.genre) out.genre = next.genre;
    if (next.lib) out.lib = next.lib;
    if (next.person) out.person = next.person;
    if (next.person && next.name) out.name = next.name;
    if (next.tab === 'people') out.tab = 'people';
    if (next.page > 0) out.page = String(next.page);
    setParams(out);
  }

  /** 点「人」那一档的一条：变成「只看这个人的作品」——词清掉，否则标题也得匹配（必然 0 条）。 */
  function pickPerson(id: number, name: string) {
    go({ q: '', kind: '', genre: '', lib: '', person: String(id), name, tab: 'items', page: 0 });
  }

  const libraryName = (id?: number | null) =>
    libraries.find((l) => l.id === id)?.name ?? (id ? t('库 {id}', { id }) : '');

  const shown = data ? Math.min(data.offset + data.items.length, data.total) : 0;
  const peopleShown = people ? Math.min(people.offset + people.people.length, people.total) : 0;

  return (
    <>
      <div className="card">
        <h2>{t('搜索')}</h2>
        <p className="hint">
          {t('中文按二元组切词（「炼金」能搜到《钢之炼金术师》），英文按整词，并且')}
          <strong>{t('容忍错字')}</strong>
          {t('（「钢之炼金术土」也能命中）。搜的既有')}
          <strong>{t('作品')}</strong>
          {t('（标题 / 原始标题），也有')}
          <strong>{t('演职员')}</strong>
          {t('的名字 —— 敲几个字就会有联想，点人名可以只看他的作品。')}
        </p>

        <form
          className="row"
          onSubmit={(e) => {
            e.preventDefault();
            go({ q: input.trim(), page: 0 });
          }}
        >
          <SearchBox
            value={input}
            autoFocus
            onChange={setInput}
            onSubmit={(text) => go({ q: text, page: 0 })}
            onPickItem={(s) => navigate(`/item/${s.id}`)}
            onPickPerson={(s) => pickPerson(s.id, s.title)}
          />
          <button type="submit" className="btn btn-primary" disabled={busy}>
            {busy ? t('搜索中…') : t('搜索')}
          </button>
          <div className="spacer" />
          {anything && (
            <button
              type="button"
              className="btn btn-sm"
              onClick={() => {
                setInput('');
                setParams({});
              }}
            >
              {t('清空')}
            </button>          )}
        </form>

        {error && <div className="alert alert-error">{error}</div>}
        {!anything && <p className="muted">{t('输入片名或演员名开始搜索。')}</p>}
      </div>

      {anything && facets && (
        <div className="card">
          <div className="facet-row">
            <span className="facet-label">{t('结果')}</span>
            <button
              type="button"
              className={`chip${!kind ? ' chip-on' : ''}`}
              onClick={() => go({ kind: '', tab: 'items', page: 0 })}
            >
              {t('全部')} <span className="chip-count">{facets.total}</span>
            </button>
            {facets.kind.map((c) => (
              <button
                type="button"
                key={c.value}
                className={`chip${kind === c.value ? ' chip-on' : ''}`}
                data-facet="kind"
                data-value={c.value}
                onClick={() =>
                  go({ kind: kind === c.value ? '' : c.value, tab: 'items', page: 0 })
                }
              >
                {kindLabel(c.value)} <span className="chip-count">{c.count}</span>
              </button>
            ))}
            {q && (
              <button
                type="button"
                className={`chip${tab === 'people' ? ' chip-on' : ''}`}
                data-facet="people"
                // 切到「人」时把条目侧的筛选清掉：人的命中不受库/类型/流派约束，
                // 留着那些高亮会让人以为「人」也筛过了
                onClick={() => go({ tab: 'people', kind: '', genre: '', lib: '', page: 0 })}
              >
                {t('人')} <span className="chip-count">{facets.people}</span>
              </button>
            )}
          </div>

          {facets.library.length > 0 && (
            <div className="facet-row">
              <span className="facet-label">{t('媒体库')}</span>
              {facets.library.map((f) => (
                <button
                  type="button"
                  key={f.value}
                  className={`chip${lib === f.value ? ' chip-on' : ''}`}
                  data-facet="library"
                  data-value={f.value}
                  onClick={() =>
                    go({ lib: lib === f.value ? '' : f.value, kind: '', tab: 'items', page: 0 })
                  }
                >
                  {libraryName(Number(f.value))} <span className="chip-count">{f.count}</span>
                </button>
              ))}
            </div>
          )}

          {facets.genre.length > 0 && (
            <div className="facet-row">
              <span className="facet-label">{t('流派')}</span>
              {facets.genre.map((f) => (
                <button
                  type="button"
                  key={f.value}
                  className={`chip${genre === f.value ? ' chip-on' : ''}`}
                  data-facet="genre"
                  data-value={f.value}
                  onClick={() =>
                    go({ genre: genre === f.value ? '' : f.value, tab: 'items', page: 0 })
                  }
                >
                  {f.value} <span className="chip-count">{f.count}</span>
                </button>
              ))}
            </div>
          )}

          {someFilter && (
            <div className="facet-row">
              <span className="facet-label">{t('筛选')}</span>
              {kind && (
                <span className="chip chip-static">
                  {kindLabel(kind)}                  <button
                    type="button"
                    className="chip-x"
                    aria-label={t('取消类型筛选')}
                    onClick={() => go({ kind: '', page: 0 })}
                  >
                    ×
                  </button>
                </span>
              )}
              {genre && (
                <span className="chip chip-static">
                  {genre}
                  <button
                    type="button"
                    className="chip-x"
                    aria-label={t('取消流派筛选')}
                    onClick={() => go({ genre: '', page: 0 })}
                  >
                    ×
                  </button>
                </span>
              )}
              {lib && (
                <span className="chip chip-static">
                  {libraryName(Number(lib))}
                  <button
                    type="button"
                    className="chip-x"
                    aria-label={t('取消媒体库筛选')}
                    onClick={() => go({ lib: '', page: 0 })}
                  >
                    ×
                  </button>
                </span>
              )}
              {person && (
                <span className="chip chip-static chip-person">
                  {personName || t('人 #{id}', { id: person })}
                  <button
                    type="button"
                    className="chip-x"
                    aria-label={t('取消按人筛选')}
                    onClick={() => go({ person: '', name: '', page: 0 })}
                  >
                    ×
                  </button>
                </span>
              )}
              <button
                type="button"
                className="btn btn-sm"
                onClick={() => {
                  setInput('');
                  setParams({});
                }}
              >
                  {t('清除全部')}
              </button>
            </div>
          )}
        </div>
      )}

      {tab === 'items' && data && (
        <div className="card">
          <p className="muted small">
            {q ? `「${data.query}」` : ''}
            {person ? t('只看「{name}」参与的作品：', { name: personName || `#${person}` }) : ''}
            {t('共 {n} 条', { n: data.total })}
            {data.total > 0
              ? t('，显示第 {a}~{b} 条', { a: data.offset + 1, b: shown })
              : ''}
          </p>

          {data.items.length === 0 && (
            <p className="muted">
              {t('没有匹配的条目。试试只搜其中两个字，或者换个译名；也可以点上面的「人」看看')}{' '}
              {t('是不是在找某位演职员。')}
            </p>
          )}

          {data.items.length > 0 && (
            <>
              <div className="search-grid">
                {data.items.map((it) => (
                  <Link key={it.id} className="search-card" to={`/item/${it.id}`}>
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
                      <div className="search-title">{it.title || t('（无标题）')}</div>
                      <div className="muted small">
                        {kindLabel(it.kind)}
                        {it.seasonNumber != null ? ` S${it.seasonNumber}` : ''}
                        {it.episodeNumber != null ? `E${it.episodeNumber}` : ''}
                        {it.year ? ` · ${it.year}` : ''}
                        {libraryName(it.libraryId) ? ` · ${libraryName(it.libraryId)}` : ''}
                      </div>
                      {it.originalTitle && <div className="muted small">{it.originalTitle}</div>}
                      {it.overview && <div className="muted small search-clamp">{it.overview}</div>}
                    </div>
                  </Link>
                ))}
              </div>

              {data.total > PAGE_SIZE && (
                <div className="row" style={{ marginTop: 12 }}>
                  <button
                    type="button"
                    className="btn btn-sm"
                    disabled={page === 0}
                    onClick={() => go({ page: page - 1 })}
                  >
                    {t('上一页')}
                  </button>
                  <span className="faint">
                    {t('第 {a} / {b} 页', {
                      a: page + 1,
                      b: Math.max(1, Math.ceil(data.total / PAGE_SIZE)),
                    })}
                  </span>                  <button
                    type="button"
                    className="btn btn-sm"
                    disabled={(page + 1) * PAGE_SIZE >= data.total}
                    onClick={() => go({ page: page + 1 })}
                  >
                    {t('下一页')}
                  </button>
                </div>
              )}
            </>
          )}
        </div>
      )}

      {tab === 'people' && people && (
        <div className="card" data-tab="people">
          <p className="muted small">
            {t('「{q}」命中 {n} 位演职员', { q: people.query, n: people.total })}
            {people.total > 0
              ? t('，显示第 {a}~{b} 位', { a: people.offset + 1, b: peopleShown })
              : ''}
            {t('点「看作品」列出他参与过的条目。')}
          </p>

          {people.people.length === 0 && (
            <p className="muted">
              {t('没有匹配的演职员。演职员数据来自媒体同目录的 nfo（本地优先）。')}
            </p>
          )}

          {people.people.length > 0 && (
            <>
              <ul className="people-list">
                {people.people.map((p) => (
                  <li key={p.id} className="people-item" data-person={p.id}>
                    <span className="people-avatar">{p.name.slice(0, 1)}</span>
                    <span className="people-body">
                      <span className="people-name">{p.name}</span>
                      <span className="faint small">
                        {roleText(p.roles)}
                        {p.works
                          ? `${roleText(p.roles) ? ' · ' : ''}${t('{n} 部作品', { n: p.works })}`
                          : ''}
                      </span>
                    </span>
                    <button
                      type="button"
                      className="btn btn-sm"
                      onClick={() => pickPerson(p.id, p.name)}
                    >
                      {t('看作品')}
                    </button>
                  </li>
                ))}
              </ul>

              {people.total > PEOPLE_PAGE_SIZE && (
                <div className="row" style={{ marginTop: 12 }}>
                  <button
                    type="button"
                    className="btn btn-sm"
                    disabled={page === 0}
                    onClick={() => go({ page: page - 1 })}
                  >
                    {t('上一页')}
                  </button>
                  <span className="faint">
                    {t('第 {a} / {b} 页', {
                      a: page + 1,
                      b: Math.max(1, Math.ceil(people.total / PEOPLE_PAGE_SIZE)),
                    })}
                  </span>
                  <button
                    type="button"
                    className="btn btn-sm"
                    disabled={(page + 1) * PEOPLE_PAGE_SIZE >= people.total}
                    onClick={() => go({ page: page + 1 })}
                  >
                    {t('下一页')}
                  </button>
                </div>
              )}
            </>
          )}
        </div>
      )}
    </>
  );
}
