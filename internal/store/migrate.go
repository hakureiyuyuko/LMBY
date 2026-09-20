package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/hakureiyuyuko/lmby/migrations"
)

// advisoryLockKey 是迁移用的 PostgreSQL 建议锁键，避免多实例并发迁移。
const advisoryLockKey int64 = 0x6c6d6279 // "lmby"

// Migration 描述一次已执行的迁移，用于日志输出。
type Migration struct {
	Version  int64
	Name     string
	Checksum string
}

// MigrateOptions 控制迁移行为。
type MigrateOptions struct {
	// AcceptChecksumChanges 允许「已应用过的迁移文件被修改」这种情况。
	//
	// 默认关闭：迁移一旦执行就不应再改，否则不同环境的结构会静默地不一致。
	// 但现实中确实会出现「刚发完就发现写错了」的场景，所以提供一个
	// 必须显式指定的逃生口，并把每一处改动都回报给调用方。
	AcceptChecksumChanges bool
}

// MigrateResult 是一次迁移的结果。
type MigrateResult struct {
	// Applied 是本次新执行的迁移。
	Applied []Migration
	// Reconciled 是校验和被重新登记的历史迁移（仅在开启上面那个选项时非空）。
	Reconciled []Migration
}

// Migrate 按版本顺序应用所有未执行的迁移。
//
// 设计取舍（见 docs/ADR/0001-stdlib-first.md）：
//   - 不使用外部迁移工具或二进制，迁移文件通过 go:embed 打进二进制；
//   - 用 session 级 advisory lock 保证并发安全；
//   - 每个文件在自己的事务里执行，失败即整体回滚；
//   - 已执行迁移的校验和不匹配时报错（防止有人改历史迁移）。
func Migrate(ctx context.Context, s *Store, opts MigrateOptions) (MigrateResult, error) {
	var res MigrateResult

	all, err := migrations.All()
	if err != nil {
		return res, fmt.Errorf("读取内嵌迁移失败: %w", err)
	}

	// 用一条独占连接持有建议锁，保证多个实例不会同时迁移。
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return res, fmt.Errorf("获取数据库连接失败: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `select pg_advisory_lock($1)`, advisoryLockKey); err != nil {
		return res, fmt.Errorf("获取迁移锁失败: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `select pg_advisory_unlock($1)`, advisoryLockKey)
	}()

	const createTable = `
		create table if not exists schema_migrations (
			version    bigint primary key,
			name       text        not null,
			checksum   text        not null,
			applied_at timestamptz not null default now()
		)`
	if _, err := conn.Exec(ctx, createTable); err != nil {
		return res, fmt.Errorf("创建 schema_migrations 表失败: %w", err)
	}

	applied := map[int64]Migration{}
	rows, err := conn.Query(ctx, `select version, name, checksum from schema_migrations`)
	if err != nil {
		return res, fmt.Errorf("读取已应用迁移失败: %w", err)
	}
	for rows.Next() {
		var m Migration
		if err := rows.Scan(&m.Version, &m.Name, &m.Checksum); err != nil {
			rows.Close()
			return res, err
		}
		applied[m.Version] = m
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}

	for _, m := range all {
		sum := checksum(m.SQL)

		if prev, ok := applied[m.Version]; ok {
			if prev.Checksum == sum {
				continue
			}
			if !opts.AcceptChecksumChanges {
				return res, fmt.Errorf(
					"迁移 %04d_%s 已被修改（校验和不一致）\n"+
						"迁移文件一经发布就不应再改动，请新增一个迁移文件；\n"+
						"若确实只是刚发布时写错了，可用 lmby migrate --accept-checksum-changes 显式接受此改动",
					m.Version, m.Name)
			}
			if _, err := conn.Exec(ctx,
				`update schema_migrations set checksum = $2, applied_at = applied_at where version = $1`,
				m.Version, sum); err != nil {
				return res, fmt.Errorf("更新迁移 %04d 校验和失败: %w", m.Version, err)
			}
			res.Reconciled = append(res.Reconciled, Migration{Version: m.Version, Name: m.Name, Checksum: sum})
			continue
		}

		if err := applyOne(ctx, s, m, sum); err != nil {
			return res, err
		}
		applied[m.Version] = Migration{Version: m.Version, Name: m.Name, Checksum: sum}
		res.Applied = append(res.Applied, Migration{Version: m.Version, Name: m.Name, Checksum: sum})
	}

	return res, nil
}

// applyOne 在单个事务里执行一个迁移文件。
func applyOne(ctx context.Context, s *Store, m migrations.Migration, sum string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	// 迁移文件是一整个 SQL 脚本（多条语句），必须走简单查询协议，
	// 否则 pgx 的扩展协议会拒绝「一次多条命令」。
	if _, err := tx.Conn().PgConn().Exec(ctx, m.SQL).ReadAll(); err != nil {
		return fmt.Errorf("执行迁移 %04d_%s 失败: %w", m.Version, m.Name, err)
	}

	if _, err := tx.Exec(ctx,
		`insert into schema_migrations (version, name, checksum) values ($1, $2, $3)`,
		m.Version, m.Name, sum); err != nil {
		return fmt.Errorf("记录迁移 %04d_%s 失败: %w", m.Version, m.Name, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交迁移 %04d_%s 失败: %w", m.Version, m.Name, err)
	}
	return nil
}

func checksum(sql string) string {
	sum := sha256.Sum256([]byte(sql))
	return hex.EncodeToString(sum[:])
}

// SchemaVersion 返回当前数据库已应用的最大迁移版本号（0 表示空库）。
func SchemaVersion(ctx context.Context, s *Store) (int64, error) {
	var v *int64
	err := s.pool.QueryRow(ctx, `select max(version) from schema_migrations`).Scan(&v)
	if err != nil {
		return 0, err
	}
	if v == nil {
		return 0, nil
	}
	return *v, nil
}

// unusedTime 让 time 包在后续扩展中保持引用（避免 lint 噪音时被误删）。
var _ = time.Now
