import { useCallback, useEffect, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { ApiError, api } from '../api';
import { t, useI18n } from '../i18n';
import { kindLabel as itemKindLabel } from '../media';
import type { FullItem, ItemDetail, ItemFieldInfo, ItemPlaylist, PlaybackProgress } from '../api';
import { formatClock } from '../capabilities';

/**
 * 条目编辑（含字段锁定）。
 *
 * 为什么这张页面是必要的：元数据有两条来源 —— 以前人工整理的 nfo 与现在的 TMDB 刮削，
 * 两者都可能不对或不够（译名、年份、简介、流派、provider id…）。用户必须能亲手改，
 * 而且改完要**锁住**：否则下一次重扫/重刮就把人工成果冲掉了。
 * 后端那套「空值不覆盖 + locked_fields」的语义早就生效了，缺的就是这个能用的入口。
 *
 * 与「人工匹配」的分工：人工匹配解决「机器认错了是哪一部」，
 * 这里解决「数据本身要修」。
 */

/** 匹配状态 → 人话。是**函数而不是常量表**：模块级常量会在 import 时把语言钉死。 */
function itemStateLabel(state: string | undefined): string {
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
      return state ?? '';
  }
}

/** 字段在界面上的标题与提示（键是后端的字段名）。同样是函数：标签跟着语言走。 */
function itemRowMeta(): Record<string, { label: string; hint?: string }> {
  return {
    title: { label: t('标题'), hint: t('列表与排序都用它；不能为空') },
    originalTitle: { label: t('原始标题'), hint: t('原名/译名，匹配打分器会拿它再搜一次') },
    year: { label: t('年份') },
    premiereDate: { label: t('首播日期'), hint: 'YYYY-MM-DD' },
    runtime: { label: t('时长'), hint: t('分钟') },
    rating: { label: t('评分'), hint: '0~10' },
    officialRating: { label: t('分级'), hint: t('如 PG-13 / TV-14') },
    overview: { label: t('简介') },
    tagline: { label: t('标语') },
    genres: { label: t('流派'), hint: t('逗号分隔') },
    studios: { label: t('制片公司'), hint: t('逗号分隔') },
    providerIds: { label: t('元数据 id'), hint: t('键=值，逗号分隔，如 tmdb=603') },
  };
}

/** 字段名 → 条目 JSON 里的键（多数同名，时长与评分在库里叫别的）。 */
const itemKeys: Record<string, string> = {
  runtime: 'runtimeTicks',
  rating: 'communityRating',
};

export function ItemEdit() {
  const { t } = useI18n();
  const params = useParams();
  const navigate = useNavigate();
  const itemId = Number(params.id);

  const [detail, setDetail] = useState<ItemDetail | null>(null);
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [dirty, setDirty] = useState<string[]>([]);
  const [locks, setLocks] = useState<string[]>([]);
  const [locksDirty, setLocksDirty] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);
  // 播放相关（M3）：续播位置、已看状态、文件与流信息
  const [progress, setProgress] = useState<PlaybackProgress | null>(null);
  const [playlist, setPlaylist] = useState<ItemPlaylist | null>(null);

  /** 把服务端返回的详情铺进表单（保存成功后也走这里，等于重置了「已改」标记）。 */
  const applyDetail = useCallback((d: ItemDetail) => {
    const next: Record<string, string> = {};
    for (const f of d.fields) {
      next[f.name] = formatValue(f, rawValue(d.item, f.name));
    }
    setDetail(d);
    setDraft(next);
    setDirty([]);
    setLocks(d.fields.filter((f) => f.locked).map((f) => f.name));
    setLocksDirty(false);
  }, []);

  const load = useCallback(async () => {
    if (!Number.isFinite(itemId) || itemId <= 0) {
      setError(t('条目 id 非法'));
      return;
    }
    try {
      applyDetail(await api.item(itemId));
      setError('');
    } catch (e) {
      setError(messageOf(e, t('读取条目失败')));
    }
  }, [itemId, applyDetail]);

  useEffect(() => {
    void load();
  }, [load]);

  // 播放信息（进度 / 可播文件）：与编辑无关，失败也不影响编辑功能
  const loadPlayback = useCallback(async () => {
    if (!Number.isFinite(itemId) || itemId <= 0) return;
    try {
      const p = await api.itemProgress(itemId);
      setProgress(p.progress);
    } catch {
      /* 忽略 */
    }
    try {
      setPlaylist(await api.itemPlaylist(itemId));
    } catch {
      /* 忽略 */
    }
  }, [itemId]);

  useEffect(() => {
    void loadPlayback();
  }, [loadPlayback]);

  async function togglePlayed(next: boolean) {
    try {
      await api.setPlayed([itemId], next);
      setProgress((p) => (p ? { ...p, played: next } : p));
      setNotice(next ? t('已标记为看过') : t('已标记为未看'));
    } catch (e) {
      setError(messageOf(e, t('标记失败')));
    }
  }

  function setField(name: string, value: string) {
    setDraft((d) => ({ ...d, [name]: value }));
    // 改回原值也算「改过」：保存时会原样写一遍，代价只是多一个字段的 UPDATE
    setDirty((list) => (list.includes(name) ? list : [...list, name]));
  }

  function toggleLock(name: string) {
    setLocks((list) => (list.includes(name) ? list.filter((n) => n !== name) : [...list, name]));
    setLocksDirty(true);
  }

  async function save() {
    if (!detail) return;
    setBusy(true);
    setError('');
    setNotice('');
    try {
      const fields: Record<string, unknown> = {};
      for (const name of dirty) {
        const f = detail.fields.find((x) => x.name === name);
        if (!f) continue;
        fields[name] = parseValue(f, draft[name] ?? '');
      }
      if (Object.keys(fields).length === 0 && !locksDirty) {
        setNotice(t('没有改动。'));
        return;
      }

      const body: { fields?: Record<string, unknown>; lockedFields?: string[] } = {};
      if (Object.keys(fields).length > 0) body.fields = fields;
      if (locksDirty) body.lockedFields = locks;

      applyDetail(await api.updateItem(itemId, body));
      setNotice(
        t('已保存（{fields} 个字段{locked}）。锁住的字段重扫重刮都不会被覆盖。', {
          fields: Object.keys(fields).length,
          locked: locksDirty ? t('，锁定 {n} 个', { n: locks.length }) : '',
        }),
      );
    } catch (e) {
      setError(messageOf(e, t('保存失败')));
    } finally {
      setBusy(false);
    }
  }

  async function unlockAll() {
    if (!detail) return;
    if (locks.length === 0) {
      setNotice(t('本来就没有锁定任何字段。'));
      return;
    }
    if (!window.confirm(t('解锁全部 {n} 个字段？之后重扫/重刮会重新覆盖它们。', { n: locks.length }))) return;
    setBusy(true);
    setError('');
    setNotice('');
    try {
      applyDetail(await api.updateItem(itemId, { lockedFields: [] }));
      setNotice(t('已全部解锁。'));
    } catch (e) {
      setError(messageOf(e, t('解锁失败')));
    } finally {
      setBusy(false);
    }
  }

  async function rescrape() {
    if (!detail) return;
    if (
      !window.confirm(
        t('重新刮削：TMDB 的值会写进未锁定的字段（锁住的不动）。\n队列里没有别的活时几秒内跑完，之后点「刷新」看结果。继续？'),
      )
    ) {
      return;
    }
    setBusy(true);
    setError('');
    setNotice('');
    try {
      const res = await api.rescrapeItem(itemId, true);
      setNotice(
        res.enqueued
          ? t('已入队，跑完后点「刷新」看结果。')
          : t('队列里已经有这条的任务了，跑完后点「刷新」看结果。'),
      );
    } catch (e) {
      setError(messageOf(e, t('入队失败')));
    } finally {
      setBusy(false);
    }
  }

  if (!detail) {
    return (
      <div className="card">
        <h2>{t('条目')}</h2>
        {error ? (
          <div className="alert alert-error">{error}</div>
        ) : (
          <p className="muted">{t('正在读取…')}</p>
        )}
      </div>
    );
  }

  const it = detail.item;
  const locked = new Set(locks);
  const resumeSeconds = progress && !progress.played ? progress.positionTicks / 10_000_000 : 0;
  const file = playlist?.files[0];

  return (
    <>
      <div className="card">
        <div className="item-head">
          <img
            className="item-poster"
            src={`/api/v1/items/${it.id}/images/poster?w=240`}
            alt=""
            onError={(e) => {
              (e.currentTarget as HTMLImageElement).style.visibility = 'hidden';
            }}
          />
          <div className="item-head-body">
            <h2>{it.title || t('（无标题）')}</h2>
            <p className="muted small">
              {itemKindLabel(it.kind)}
              {it.seasonNumber != null ? ` S${it.seasonNumber}` : ''}
              {it.episodeNumber != null ? `E${it.episodeNumber}` : ''}
              {it.year ? ` · ${it.year}` : ''}
              {t(' · 条目 {id} · 状态 {state}', {
                id: it.id,
                state: itemStateLabel(it.matchState),
              })}
              {it.metadataSource ? ` · ${t('元数据来源 {src}', { src: it.metadataSource })}` : ''}
              {it.matchScore != null ? t(' · 匹配分 {score}', { score: it.matchScore.toFixed(3) }) : ''}
            </p>
            {it.scrapeError && (
              <p className="muted small">{t('机器给的结论：{msg}', { msg: it.scrapeError })}</p>
            )}
            {locks.length > 0 && (
              <p className="small">
                <strong>{t('已锁定 {n} 个字段', { n: locks.length })}</strong>
                <span className="muted">{t('（重扫与重刮都不会覆盖它们）')}</span>
              </p>
            )}
            <div className="row" style={{ marginTop: 10 }}>
              <Link className="btn" to={`/item/${it.id}`}>
                {t('返回详情页')}
              </Link>
              <button type="button" className="btn btn-primary" disabled={busy} onClick={() => void save()}>
                {t('保存')}
              </button>
              <button type="button" className="btn" disabled={busy} onClick={() => void unlockAll()}>
                {t('全部解锁')}
              </button>
              {detail.scrapeConfigured && (
                <button type="button" className="btn" disabled={busy} onClick={() => void rescrape()}>
                  {t('重新刮削这条')}
                </button>
              )}
              <button type="button" className="btn" disabled={busy} onClick={() => void load()}>
                {t('刷新')}
              </button>
              <div className="spacer" />
              <button type="button" className="btn btn-ghost" onClick={() => navigate(-1)}>
                {t('返回')}
              </button>
              <Link className="btn btn-ghost" to="/settings/match">
                {t('人工匹配')}
              </Link>
            </div>
          </div>
        </div>

        {error && <div className="alert alert-error">{error}</div>}
        {notice && <div className="alert alert-ok">{notice}</div>}
      </div>

      <div className="card">
        <h2>{t('播放')}</h2>
        {file && (
          <p className="muted small">
            {t('文件 {container} · 视频 {video} · 音频 {audio}', {
              container: file.containerKind || file.container || t('未知'),
              video: file.video?.[0]
                ? `${file.video[0].codec} ${file.video[0].width}×${file.video[0].height}`
                : t('（无）'),
              audio: file.audio?.[0]
                ? `${file.audio[0].codec} ${file.audio[0].channels}ch`
                : t('（无）'),
            })}
            {(file.subtitles?.length ?? 0) > 0 ? t(' · {n} 条字幕', { n: file.subtitles.length }) : ''}
          </p>
        )}
        <div className="row" style={{ marginTop: 8 }}>
          <Link className="btn btn-primary" to={`/play/${it.id}`}>
            {resumeSeconds > 0
              ? t('继续播放（{time}）', { time: formatClock(resumeSeconds) })
              : t('播放')}
          </Link>
          {resumeSeconds > 0 && (
            <Link className="btn" to={`/play/${it.id}?restart=1`}>
              {t('从头播放')}
            </Link>
          )}
          <label className="small" style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <input
              type="checkbox"
              checked={Boolean(progress?.played)}
              onChange={(e) => void togglePlayed(e.target.checked)}
            />
            {t('标记为已看')}
          </label>
          {progress && progress.playCount > 0 && (
            <span className="faint small">{t('已看过 {n} 次', { n: progress.playCount })}</span>
          )}
        </div>
      </div>

      <div className="card">
        <h2>{t('字段')}</h2>
        <table className="edit-table">
          <thead>
            <tr>
              <th style={{ width: 140 }}>{t('字段')}</th>
              <th>{t('值')}</th>
              <th style={{ width: 96 }}>{t('锁定')}</th>
            </tr>
          </thead>
          <tbody>
            {detail.fields.map((f) => {
              const meta = itemRowMeta()[f.name] ?? { label: f.name };
              const changed = dirty.includes(f.name);
              const isLocked = locked.has(f.name);
              return (
                <tr key={f.name} className={isLocked ? 'row-locked' : undefined}>
                  <td>
                    {meta.label}
                    {f.unit === 'minutes' && <span className="faint small">{t('（分钟）')}</span>}
                    {changed && (
                      <span className="badge" style={{ marginLeft: 6 }}>
                        {t('已改')}
                      </span>
                    )}
                  </td>
                  <td>
                    {f.name === 'overview' ? (
                      <textarea
                        data-field={f.name}
                        rows={3}
                        value={draft[f.name] ?? ''}
                        onChange={(e) => setField(f.name, e.target.value)}
                      />
                    ) : (
                      <input
                        type="text"
                        data-field={f.name}
                        value={draft[f.name] ?? ''}
                        onChange={(e) => setField(f.name, e.target.value)}
                      />
                    )}
                    {meta.hint && <div className="faint small">{meta.hint}</div>}
                  </td>
                  <td>
                    <label className="lock-cell">
                      <input
                        type="checkbox"
                        data-lock={f.name}
                        checked={isLocked}
                        onChange={() => toggleLock(f.name)}
                      />
                      <span className="faint small">{isLocked ? t('已锁') : t('未锁')}</span>
                    </label>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </>
  );
}

function messageOf(e: unknown, fallback: string): string {
  if (e instanceof ApiError) return e.message;
  if (e instanceof Error) return e.message;
  return fallback;
}

/** 取条目上某个字段的原始值（有些字段在库里叫别的名字）。 */
function rawValue(item: FullItem, name: string): unknown {
  const key = itemKeys[name] ?? name;
  return (item as unknown as Record<string, unknown>)[key];
}

/** 把原始值格式化成输入框里的文本。 */
function formatValue(f: ItemFieldInfo, v: unknown): string {
  if (v === null || v === undefined) return '';
  switch (f.kind) {
    case 'list':
      return Array.isArray(v) ? v.join(', ') : '';
    case 'map':
      return typeof v === 'object'
        ? Object.entries(v as Record<string, string>)
            .map(([k, x]) => `${k}=${x}`)
            .join(', ')
        : '';
    case 'date':
      return String(v).slice(0, 10);
    case 'int':
      // 时长在库里是 tick（1 tick = 100ns），界面上按分钟填
      return f.unit === 'minutes' ? String(Math.round(Number(v) / 600000000)) : String(v);
    default:
      return String(v);
  }
}

/** 把输入框里的文本解析成接口要的值（解析不了就抛错，由保存流程显示）。 */
function parseValue(f: ItemFieldInfo, text: string): unknown {
  const label = itemRowMeta()[f.name]?.label ?? f.name;
  const s = text.trim();
  switch (f.kind) {
    case 'int':
      if (s === '') return null;
      if (!/^-?\d+$/.test(s)) throw new Error(t('{label} 需要整数（清空就留空）', { label }));
      return Number(s);
    case 'float':
      if (s === '') return null;
      if (!Number.isFinite(Number(s))) throw new Error(t('{label} 需要数字（清空就留空）', { label }));
      return Number(s);
    case 'list':
      return s === ''
        ? []
        : s
            .split(/[,，]/)
            .map((x) => x.trim())
            .filter(Boolean);
    case 'map': {
      if (s === '') return {};
      const out: Record<string, string> = {};
      for (const part of s.split(/[,，]/)) {
        const p = part.trim();
        if (!p) continue;
        const i = p.indexOf('=');
        if (i <= 0) throw new Error(t('{label} 要写成键=值（如 tmdb=603），收到「{got}」', { label, got: p }));
        out[p.slice(0, i).trim()] = p.slice(i + 1).trim();
      }
      return out;
    }
    case 'date':
      if (s === '') return null;
      if (!/^\d{4}-\d{2}-\d{2}$/.test(s)) throw new Error(t('{label} 要写成 YYYY-MM-DD', { label }));
      return s;
    default:
      // 文本：原样送（含空格与换行），空串就是清空
      return text;
  }
}
