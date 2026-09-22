import Hls from 'hls.js';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { ApiError, api } from '../api';
import type { LivePlayback, TVChannel, TVGroup } from '../api';
import { hasNativeHls } from '../capabilities';
import { useAuth } from '../auth';
import { LiveProbePanel, LiveSourcePanel } from '../components/LiveSources';

/**
 * 直播电视（M5）。
 *
 * 三件事在这一页：
 *   1. **频道列表**：分组 / 搜索 / 收藏 / 启用停用 / 排序 / logo / 失效标记；
 *   2. **播放**：点一下就地起播（同一频道所有观众共享一路 ffmpeg），
 *      切台只是换一个 sid；
 *   3. **源与探测**（管理员）：导入播放列表、刷新、按需探测失效源。
 *
 * 直播与点播的播放器**刻意不同**：
 *   - 没有进度条、没有音轨/字幕选择（直播没有这些概念），所以直接用 <video controls>
 *     的原生控件（自带播放/音量/全屏，代码少且行为稳定）；
 *   - 没有续段：服务端给的是滚动窗口，hls.js 自己跟着窗口走；
 *   - 离开页面时要退掉订阅（否则服务端会为「没人在看」的频道白跑最多 45 秒）。
 */
export function LiveTV() {
  const { user } = useAuth();
  const isAdmin = user?.isAdmin ?? false;

  const [channels, setChannels] = useState<TVChannel[]>([]);
  const [groups, setGroups] = useState<TVGroup[]>([]);
  const [total, setTotal] = useState(0);
  const [enabled, setEnabled] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');

  // 筛选（搜索框本地去抖 300ms，其余改动立刻查）
  const [qInput, setQInput] = useState('');
  const [q, setQ] = useState('');
  const [group, setGroup] = useState('');
  const [onlyEnabled, setOnlyEnabled] = useState(true);
  const [onlyFav, setOnlyFav] = useState(false);
  const [probeFilter, setProbeFilter] = useState('');

  // 播放
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const hlsRef = useRef<Hls | null>(null);
  const sidRef = useRef('');
  const [playing, setPlaying] = useState<TVChannel | null>(null);
  const [playErr, setPlayErr] = useState('');
  const [starting, setStarting] = useState(false);
  /** 浏览器侧起播耗时（点播放 → 画面真的动起来），DoD 是 < 2 秒。 */
  const [browserMs, setBrowserMs] = useState<number | null>(null);
  const [viewers, setViewers] = useState(0);

  // 编辑
  const [editing, setEditing] = useState<TVChannel | null>(null);
  const [draft, setDraft] = useState({ name: '', group: '', logo: '', sortOrder: 0 });

  useEffect(() => {
    const t = window.setTimeout(() => setQ(qInput.trim()), 300);
    return () => window.clearTimeout(t);
  }, [qInput]);

  const load = useCallback(async () => {
    try {
      const d = await api.liveChannels({ q, group, enabled: onlyEnabled, favorites: onlyFav, probe: probeFilter });
      setChannels(d.channels);
      setGroups(d.groups);
      setTotal(d.total);
      setEnabled(d.enabled);
      setError('');
    } catch (e) {
      setError(e instanceof ApiError ? e.message : '读取频道失败');
    } finally {
      setLoading(false);
    }
  }, [q, group, onlyEnabled, onlyFav, probeFilter]);

  useEffect(() => {
    void load();
  }, [load]);

  /** 退掉订阅：换台 / 停止 / 离开页面都走这里。 */
  const stop = useCallback(async () => {
    const sid = sidRef.current;
    sidRef.current = '';
    hlsRef.current?.destroy();
    hlsRef.current = null;
    const v = videoRef.current;
    if (v) {
      v.pause();
      v.removeAttribute('src');
      v.load();
    }
    setPlaying(null);
    setBrowserMs(null);
    setPlayErr('');
    if (sid) {
      try {
        const r = await api.stopLivePlay(sid);
        setViewers(r.viewers);
      } catch {
        // 会话可能已经被空闲回收了，这不影响用户
      }
    }
  }, []);

  /** 把播放列表挂到 <video> 上（Safari 走原生 HLS，其余走 hls.js）。 */
  const attach = useCallback((info: LivePlayback, t0: number) => {
    const v = videoRef.current;
    if (!v) return;
    v.addEventListener(
      'playing',
      () => setBrowserMs(Math.round(performance.now() - t0)),
      { once: true },
    );

    if (hasNativeHls(v)) {
      v.src = info.playlistUrl;
      v.load();
      void v.play().catch(() => setNotice('浏览器拦了自动播放，点一下播放键开始'));
      return;
    }
    if (!Hls.isSupported()) {
      setPlayErr('这个浏览器既不支持原生 HLS，也不支持 MSE，放不了直播流');
      return;
    }
    const hls = new Hls({ enableWorker: true, maxBufferLength: 12 });
    hlsRef.current = hls;
    hls.on(Hls.Events.ERROR, (_evt: string, data: { fatal: boolean; type: string; details: string }) => {
      if (!data.fatal) return;
      if (data.type === Hls.ErrorTypes.MEDIA_ERROR) {
        hls.recoverMediaError();
        return;
      }
      setPlayErr(`播放出错（${data.details}）`);
    });
    hls.loadSource(info.playlistUrl);
    hls.attachMedia(v);
    hls.on(Hls.Events.MANIFEST_PARSED, () => {
      void v.play().catch(() => setNotice('浏览器拦了自动播放，点一下播放键开始'));
    });
  }, []);

  /** 起播一个频道（点同一个频道 = 重新载入）。 */
  const play = useCallback(
    async (ch: TVChannel) => {
      if (starting) return;
      setStarting(true);
      setPlayErr('');
      setNotice('');
      const t0 = performance.now();
      try {
        if (sidRef.current) await stop();
        const info = await api.startLivePlay(ch.id);
        sidRef.current = info.sid;
        setPlaying(ch);
        setViewers(info.viewers);
        attach(info, t0);
      } catch (e) {
        setPlayErr(e instanceof ApiError ? e.message : '起播失败');
      } finally {
        setStarting(false);
      }
    },
    [attach, starting, stop],
  );

  // 离开页面：退掉订阅。用 beacon 让请求在页面卸载时也能出去。
  useEffect(() => {
    const onHide = () => {
      const sid = sidRef.current;
      if (!sid) return;
      sidRef.current = '';
      try {
        navigator.sendBeacon(`/api/v1/live/${encodeURIComponent(sid)}/stop`);
      } catch {
        /* 退不掉也无所谓：服务端 45 秒空闲回收兜底 */
      }
    };
    window.addEventListener('pagehide', onHide);
    return () => {
      window.removeEventListener('pagehide', onHide);
      onHide();
    };
  }, []);

  async function toggleFav(ch: TVChannel) {
    try {
      const r = await api.toggleLiveFavorite(ch.id);
      setChannels((cs) => cs.map((c) => (c.id === ch.id ? { ...c, favorite: r.favorite } : c)));
    } catch (e) {
      setError(e instanceof ApiError ? e.message : '收藏失败');
    }
  }

  async function toggleDisabled(ch: TVChannel) {
    try {
      const c = await api.updateLiveChannel(ch.id, { disabled: !ch.disabled });
      setChannels((cs) => cs.map((x) => (x.id === c.id ? c : x)));
      setNotice(c.disabled ? `已停用「${c.name}」（不再出现在「只看启用」里）` : `已启用「${c.name}」`);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : '保存失败');
    }
  }

  function startEdit(ch: TVChannel) {
    setEditing(ch);
    setDraft({ name: ch.name, group: ch.group, logo: ch.logo, sortOrder: ch.sortOrder });
    setNotice('');
  }

  async function saveEdit() {
    if (!editing) return;
    try {
      await api.updateLiveChannel(editing.id, {
        name: draft.name.trim(),
        group: draft.group.trim(),
        logo: draft.logo.trim(),
        sortOrder: Number(draft.sortOrder) || 0,
      });
      setNotice(`已保存「${draft.name.trim() || editing.name}」`);
      setEditing(null);
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : '保存失败');
    }
  }

  async function share(ch: TVChannel) {
    try {
      const r = await api.shareLiveChannel(ch.id, 24);
      setNotice(`外链（24 小时有效，无需登录）：${r.url}`);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : '生成外链失败');
    }
  }

  async function copyURL(ch: TVChannel) {
    try {
      await navigator.clipboard.writeText(ch.url);
      setNotice(`已复制「${ch.name}」的地址`);
    } catch {
      // 剪贴板权限被拒（无头浏览器/非 HTTPS）时退化成「显示出来自己复制」
      setNotice(`「${ch.name}」的地址：${ch.url}`);
    }
  }

  const favCount = useMemo(() => channels.filter((c) => c.favorite).length, [channels]);

  return (
    <>
      <div className="card">
        <h2>直播电视</h2>
        <p className="hint">
          {isAdmin
            ? '导入播放列表后就能在这里看电视。点频道就地起播；同一频道所有观众共享一路 ffmpeg（两个人看不会把源站拉两遍）。'
            : '点频道就地起播；同一频道所有观众共享一路推流，切台不用离开页面。'}
        </p>

        {error && <div className="alert alert-error">{error}</div>}
        {notice && <div className="alert alert-ok">{notice}</div>}

        <div className="row tv-toolbar">
          <input
            className="search-input"
            placeholder="搜频道名…"
            value={qInput}
            onChange={(e) => setQInput(e.target.value)}
          />
          <select value={group} onChange={(e) => setGroup(e.target.value)}>
            <option value="">全部分组（{groups.length}）</option>
            {groups.map((g) => (
              <option key={g.name} value={g.name}>
                {g.name}（{g.count}）
              </option>
            ))}
          </select>
          <select value={probeFilter} onChange={(e) => setProbeFilter(e.target.value)}>
            <option value="">探测状态：全部</option>
            <option value="failed">只看失效（上次探测不通）</option>
            <option value="ok">只看能通</option>
            <option value="pending">只看没探过的</option>
          </select>
          <label className="field-inline">
            <input type="checkbox" checked={onlyEnabled} onChange={(e) => setOnlyEnabled(e.target.checked)} />
            <span>只看启用</span>
          </label>
          <label className="field-inline">
            <input type="checkbox" checked={onlyFav} onChange={(e) => setOnlyFav(e.target.checked)} />
            <span>只看收藏{favCount > 0 ? `（${favCount}）` : ''}</span>
          </label>
          <div className="spacer" />
          <a className="btn btn-sm" href={api.liveExportURL()} download>
            导出 m3u
          </a>
        </div>

        <p className="faint small">
          共 {total} 台 · 启用中 {enabled} · 这里显示 {channels.length} 台
          {playing ? ` · 正在看：${playing.name}（${viewers} 个观众）` : ''}
        </p>
      </div>

      {(playing || starting || playErr) && (
        <div className="card">
          <div className="tv-stage">
            {/* 直播不需要自研控制条：没有进度/音轨/字幕可调，原生控件自带音量与全屏 */}
            <video ref={videoRef} className="tv-video" controls playsInline />
            {starting && <div className="tv-overlay">正在起播…</div>}
            {!starting && playErr && (
              <div className="tv-overlay tv-overlay-err">
                <div>{playErr}</div>
                {playing && (
                  <button type="button" className="btn btn-sm" onClick={() => void play(playing)}>
                    重试
                  </button>
                )}
              </div>
            )}
          </div>
          <div className="row tv-player-bar">
            <strong>{playing?.name ?? '—'}</strong>
            <span className="faint small">
              {playing?.kind}
              {playing?.group ? ` · ${playing.group}` : ''}
              {browserMs !== null ? ` · 浏览器起播 ${browserMs} ms` : ''}
            </span>
            <div className="spacer" />
            {playing && (
              <button type="button" className="btn btn-sm" onClick={() => void play(playing)}>
                重新载入
              </button>
            )}
            <button type="button" className="btn btn-sm btn-ghost" onClick={() => void stop()}>
              停止
            </button>
          </div>
        </div>
      )}

      {editing && (
        <div className="card">
          <h2>编辑频道</h2>
          <p className="hint">
            这里改的是「怎么用」（名字 / 分组 / 排序 / logo）。**下次刷新订阅源时会按播放列表还原**
            —— 播放列表才是频道的来源；「启用状态」与「收藏」不会被刷新覆盖。
          </p>
          <label className="field">
            <span>名称</span>
            <input value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} />
          </label>
          <label className="field">
            <span>分组</span>
            <input value={draft.group} onChange={(e) => setDraft({ ...draft, group: e.target.value })} />
          </label>
          <label className="field">
            <span>排序（同分组内，小的在前）</span>
            <input
              type="number"
              value={draft.sortOrder}
              onChange={(e) => setDraft({ ...draft, sortOrder: Number(e.target.value) || 0 })}
            />
          </label>
          <label className="field">
            <span>logo 地址</span>
            <input value={draft.logo} onChange={(e) => setDraft({ ...draft, logo: e.target.value })} />
          </label>
          <div className="row">
            <button type="button" className="btn btn-primary" onClick={() => void saveEdit()}>
              保存
            </button>
            <button type="button" className="btn" onClick={() => setEditing(null)}>
              取消
            </button>
          </div>
        </div>
      )}

      <div className="card">
        <h2>
          频道{loading ? '' : `（${channels.length}）`}
        </h2>
        {loading && <p className="muted">正在读取…</p>}
        {!loading && channels.length === 0 && (
          <p className="muted">
            没有匹配的频道。
            {isAdmin ? '去下面的「直播源」导入一份播放列表（粘贴 / 上传 / 订阅地址都行）。' : '换个筛选条件试试。'}
          </p>
        )}
        {!loading && channels.length > 0 && (
          <ul className="tv-list">
            {channels.map((ch) => (
              <li
                key={ch.id}
                className={
                  'tv-row' +
                  (ch.disabled ? ' tv-row-disabled' : '') +
                  (playing?.id === ch.id ? ' tv-row-playing' : '')
                }
              >
                <span className="tv-logo">
                  {ch.logo ? (
                    <img
                      src={ch.logo}
                      alt=""
                      loading="lazy"
                      onError={(e) => {
                        e.currentTarget.style.display = 'none';
                      }}
                    />
                  ) : (
                    <span className="tv-logo-ph">{ch.name.slice(0, 1)}</span>
                  )}
                </span>
                <span className="tv-main">
                  <span className="tv-name">
                    {ch.name}
                    {ch.favorite && <span className="tv-star"> ★</span>}
                    {ch.disabled && <span className="badge">已停用</span>}
                    {ch.probeOk === false && (
                      <span className="badge badge-bad" title={ch.probe || '上次探测不通'}>
                        失效
                      </span>
                    )}
                    {ch.probeOk === true && (
                      <span className="badge badge-ok" title={ch.probe || '上次探测能通'}>
                        通
                      </span>
                    )}
                    {ch.probeOk === undefined && <span className="badge" title="还没探测过">未探</span>}
                  </span>
                  <span className="faint small">
                    {ch.group || '未分组'} · {ch.kind}
                    {ch.hasHeaders ? ' · 带请求头' : ''}
                    {ch.probeAt ? ` · 上次探测 ${new Date(ch.probeAt).toLocaleString()}` : ''}
                  </span>
                </span>
                <span className="tv-actions">
                  <button
                    type="button"
                    className="btn btn-sm btn-primary"
                    disabled={starting || ch.disabled}
                    title={ch.disabled ? '先启用这条频道' : '起播'}
                    onClick={() => void play(ch)}
                  >
                    播放
                  </button>
                  <button
                    type="button"
                    className="btn btn-sm"
                    title={ch.favorite ? '取消收藏' : '收藏'}
                    onClick={() => void toggleFav(ch)}
                  >
                    {ch.favorite ? '★ 已收藏' : '☆ 收藏'}
                  </button>
                  <button type="button" className="btn btn-sm" onClick={() => void toggleDisabled(ch)}>
                    {ch.disabled ? '启用' : '停用'}
                  </button>
                  <button type="button" className="btn btn-sm" onClick={() => startEdit(ch)}>
                    编辑
                  </button>
                  <button type="button" className="btn btn-sm" onClick={() => void share(ch)} title="生成 24 小时有效的免登录链接">
                    外链
                  </button>
                  <button type="button" className="btn btn-sm btn-ghost" onClick={() => void copyURL(ch)}>
                    复制地址
                  </button>
                </span>
              </li>
            ))}
          </ul>
        )}
      </div>

      {isAdmin && <LiveProbePanel onFinished={() => void load()} />}
      {isAdmin && <LiveSourcePanel onImported={() => void load()} />}
    </>
  );
}
