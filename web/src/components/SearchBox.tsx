import { useEffect, useRef, useState } from 'react';
import type { KeyboardEvent } from 'react';
import { api } from '../api';
import type { Suggestion } from '../api';
import { kindLabel } from '../media';
import { roleText } from '../people';

/**
 * 搜索框 + 即时联想下拉（M6）。
 *
 * 行为上的三个决定：
 *   - **防抖 200ms**：每敲一个字都发请求会把后端与网络压满，而人打字间隔通常
 *     在 100~300ms —— 200ms 是「感觉是即时」与「不浪费」的折中；
 *   - **旧结果丢弃**：用自增的序号认领响应（不是只靠 AbortController），
 *     否则「钢」的响应可能晚于「钢炼」到达，把下拉刷回旧的一批；
 *   - **联想不看当前筛选**：用户还在敲字时用库/流派去套只会让下拉越来越空，
 *     筛选是敲完回车之后的事（后端 /search/suggest 也不收这些参数）。
 *
 * 键盘：↑↓ 在两组之间连续移动，Enter 选中，Esc 关掉；没有选中项时 Enter
 * 交给外面的表单去搜（所以这个组件自己不拦 Enter）。
 */

const SUGGEST_DEBOUNCE = 200;

export interface SearchBoxProps {
  value: string;
  onChange: (v: string) => void;
  /** 回车（且没有高亮的候选项）时的动作，参数是 trim 过的词。 */
  onSubmit: (q: string) => void;
  /** 点/选中一个作品。 */
  onPickItem: (s: Suggestion) => void;
  /** 点/选中一个人。 */
  onPickPerson: (s: Suggestion) => void;
  placeholder?: string;
  autoFocus?: boolean;
}

export function SearchBox(props: SearchBoxProps) {
  const { value, onChange, onSubmit, onPickItem, onPickPerson, placeholder, autoFocus } = props;

  const [items, setItems] = useState<Suggestion[]>([]);
  const [people, setPeople] = useState<Suggestion[]>([]);
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(-1);

  // 请求序号：只有最后一次发出去的请求才有资格写状态
  const seq = useRef(0);
  const wrapRef = useRef<HTMLDivElement | null>(null);
  // 用户有没有**真的敲过**这个框。
  //
  // 为什么要它：从 URL（收藏、后退、别人贴的链接）进来时 value 是被塞进来的，
  // 联想也会照算 —— 但那不是「用户正在找东西」，一进页面就弹个下拉很吵。
  // 所以只有敲过键盘之后，下拉才允许自己打开。
  const touched = useRef(false);
  const inputRef = useRef<HTMLInputElement | null>(null);

  /**
   * 现在能不能因为「联想回来了」而自动弹下拉。
   *
   * 两个条件缺一不可：
   *   - **用户真的敲过**（touched）：从 URL（收藏 / 后退 / 别人贴的链接）进来时
   *     value 是被塞进来的，那也会去算联想，但不是「用户正在找东西」——
   *     一进页面就弹个下拉很吵；
   *   - **输入框现在真拿着焦点**：点「搜索」按钮会失焦，而那一批联想的响应可能
   *     才刚回来 —— 不判焦点的话下拉会在结果出来之后自己弹回来盖住列表。
   *
   * 焦点直接用 **DOM 的 activeElement** 判，而不是记 focus/blur 事件：
   * 元素本来就拿着焦点时再调 focus() 不会再触发 focus 事件，
   * 拿事件维护的状态会与实际不一致（无头浏览器里真踩到过）。
   */
  const canAutoOpen = () => touched.current && document.activeElement === inputRef.current;

  useEffect(() => {
    const q = value.trim();
    if (!q) {
      seq.current += 1; // 让在途的响应作废
      setItems([]);
      setPeople([]);
      setOpen(false);
      setActive(-1);
      return;
    }
    const mine = ++seq.current;
    const timer = setTimeout(() => {
      api
        .searchSuggest({ q, limit: 8 })
        .then((res) => {
          if (mine !== seq.current) return;
          setItems(res.items);
          setPeople(res.people);
          const total = res.items.length + res.people.length;
          setActive(total > 0 ? 0 : -1);
          setOpen(total > 0 && canAutoOpen());
        })
        .catch(() => {
          // 联想失败不该影响搜索本身：静默关掉下拉
          if (mine !== seq.current) return;
          setItems([]);
          setPeople([]);
          setOpen(false);
        });
    }, SUGGEST_DEBOUNCE);
    return () => clearTimeout(timer);
  }, [value]);

  // 点页面别处就收起
  useEffect(() => {
    if (!open) return;
    const onDocDown = (e: MouseEvent) => {
      if (!wrapRef.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', onDocDown);
    return () => document.removeEventListener('mousedown', onDocDown);
  }, [open]);

  const flat = [...items, ...people];

  function pick(s: Suggestion) {
    setOpen(false);
    setActive(-1);
    if (s.type === 'person') onPickPerson(s);
    else onPickItem(s);
  }

  function onKeyDown(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === 'ArrowDown' && flat.length > 0) {
      e.preventDefault();
      setOpen(true);
      setActive((a) => (a + 1) % flat.length);
      return;
    }
    if (e.key === 'ArrowUp' && flat.length > 0) {
      e.preventDefault();
      setOpen(true);
      setActive((a) => (a - 1 + flat.length) % flat.length);
      return;
    }
    if (e.key === 'Escape') {
      setOpen(false);
      setActive(-1);
      return;
    }
    if (e.key === 'Enter' && open && active >= 0 && active < flat.length) {
      e.preventDefault();
      pick(flat[active]);
    }
  }

  const indexOf = (s: Suggestion) => flat.indexOf(s);

  return (
    <div className="search-box" ref={wrapRef}>
      <input
        ref={inputRef}
        className="search-input"
        value={value}
        autoFocus={autoFocus}
        placeholder={placeholder ?? '片名、原名或演员名，如「言叶之庭」「宫崎骏」「Matrix」'}
        aria-label="搜索"
        autoComplete="off"
        onChange={(e) => {
          touched.current = true;
          onChange(e.target.value);
        }}
        onKeyDown={onKeyDown}
        // 点回这个框（或 tab 进来）也把下拉打开：用户回到输入框就是想继续挑
        onFocus={() => {
          if (touched.current && flat.length > 0) setOpen(true);
        }}
        onClick={() => {
          if (touched.current && flat.length > 0) setOpen(true);
        }}
        onBlur={() => setOpen(false)}
      />

      {open && (
        <div
          className="suggest-panel"
          role="listbox"
          // 按下拉里的东西时不要让输入框失焦（否则 blur 先到、click 就丢了）
          onMouseDown={(e) => e.preventDefault()}
        >
          {items.length > 0 && (
            <>
              <div className="suggest-group">作品</div>
              {items.map((s) => (
                <button
                  type="button"
                  key={`item-${s.id}`}
                  role="option"
                  aria-selected={indexOf(s) === active}
                  className={`suggest-item${indexOf(s) === active ? ' active' : ''}`}
                  onMouseEnter={() => setActive(indexOf(s))}
                  onClick={() => pick(s)}
                >
                  <img
                    className="suggest-thumb"
                    src={`/api/v1/items/${s.id}/images/poster?w=64`}
                    alt=""
                    onError={(e) => {
                      (e.currentTarget as HTMLImageElement).style.visibility = 'hidden';
                    }}
                  />
                  <span className="suggest-body">
                    <span className="suggest-title">{s.title || '（无标题）'}</span>
                    <span className="faint small">
                      {s.kind ? kindLabel(s.kind) : ''}
                      {s.year ? ` · ${s.year}` : ''}
                    </span>
                  </span>
                </button>
              ))}
            </>
          )}

          {people.length > 0 && (
            <>
              <div className="suggest-group">演职员</div>
              {people.map((s) => (
                <button
                  type="button"
                  key={`person-${s.id}`}
                  role="option"
                  aria-selected={indexOf(s) === active}
                  className={`suggest-item${indexOf(s) === active ? ' active' : ''}`}
                  onMouseEnter={() => setActive(indexOf(s))}
                  onClick={() => pick(s)}
                >
                  <span className="suggest-avatar">{s.title.slice(0, 1)}</span>
                  <span className="suggest-body">
                    <span className="suggest-title">{s.title}</span>
                    <span className="faint small">
                      {roleText(s.roles)}
                      {s.works ? `${roleText(s.roles) ? ' · ' : ''}${s.works} 部作品` : ''}
                    </span>
                  </span>
                </button>
              ))}
            </>
          )}

          <button
            type="button"
            className="suggest-item suggest-more"
            onClick={() => {
              setOpen(false);
              onSubmit(value.trim());
            }}
          >
            <span className="suggest-body">搜索「{value.trim()}」的全部结果</span>
          </button>
        </div>
      )}
    </div>
  );
}
