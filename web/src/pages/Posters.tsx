import { useEffect, useState } from 'react';
import { Link, Navigate } from 'react-router-dom';
import { ApiError, api } from '../api';
import type { LibrarySummary } from '../api';
import { useI18n } from '../i18n';

/**
 * 海报墙入口（导航栏里的「海报墙」）。
 *
 * 为什么需要这一层：海报墙本身是 `/library/{id}`（一个库一面墙），
 * 但导航栏上不该逼用户先知道库 id —— 只有一个库时直接重定向过去，
 * 多个库时让他挑一个，一个都没有就指到库管理。
 */
export function Posters() {
  const { t } = useI18n();
  const [libs, setLibs] = useState<LibrarySummary[] | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    let alive = true;
    api
      .libraries()
      .then((r) => {
        if (alive) setLibs(r.libraries ?? []);
      })
      .catch((e) => {
        if (alive) setError(e instanceof ApiError ? e.message : t('读取媒体库失败'));
      });
    return () => {
      alive = false;
    };
  }, []);

  if (error) {
    return (
      <div className="card">
        <h2>{t('读不到媒体库')}</h2>
        <div className="alert alert-error">{error}</div>
        <Link className="btn" to="/settings/libraries">
          {t('去库管理看看')}
        </Link>
      </div>
    );
  }

  if (!libs) return <p className="muted">{t('正在读取媒体库…')}</p>;

  if (libs.length === 0) {
    return (
      <div className="card">
        <h2>{t('还没有媒体库')}</h2>
        <Link className="btn btn-primary" to="/settings/libraries">
          {t('去建媒体库')}
        </Link>
      </div>
    );
  }

  if (libs.length === 1) {
    return <Navigate to={`/library/${libs[0].id}`} replace />;
  }

  return (
    <div className="card">
      <h2>{t('选择媒体库')}</h2>
      <div className="row">
        {libs.map((l) => (
          <Link key={l.id} className="btn" to={`/library/${l.id}`}>
            {l.name}
            <span className="faint small">
              {' '}
              {t('{n} 条', { n: (l.counts?.movie ?? 0) + (l.counts?.series ?? 0) })}
            </span>
          </Link>
        ))}
      </div>
    </div>
  );
}
